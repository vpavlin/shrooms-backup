package backup

import (
	"context"
	"fmt"
	"log/slog"
	"net/netip"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"sync"
	"syscall"
	"time"
)

// NodeConfig is this machine's Logos Storage node for backups.
type NodeConfig struct {
	Bin     string     // the logos-storage binary (a pinned build: ADR-001)
	DataDir string     // its store
	IP      netip.Addr // this machine's address on the backups' mesh
	Port    int        // its libp2p port on IP
	APIPort int        // its REST API, on loopback only (it has no authentication)
	Quota   int64      // bytes
}

// TTL is how long the node keeps a block: 100 years. The default is 30
// days, and 0 does not disable expiry but expires a block at once
// (repostore/store.nim, found by jimmy-crib/vpavlin on 2026-11-01).
// Retention is restic's, never the store's.
const TTL = 3153600000

// Node runs and supervises the logos-storage process: started again if it
// exits, and restarted when the set of nodes to bootstrap from changes (a
// new mirror or source: the DHT is private, of our own nodes only).
type Node struct {
	cfg NodeConfig
	log *slog.Logger

	mu        sync.Mutex
	bootstrap []string // other nodes' SPRs
	cmd       *exec.Cmd
	restart   chan struct{}
}

func NewNode(cfg NodeConfig, log *slog.Logger) *Node {
	return &Node{cfg: cfg, log: log, restart: make(chan struct{}, 1)}
}

// Storage is the node's API.
func (n *Node) Storage() *Storage {
	return &Storage{API: "http://127.0.0.1:" + strconv.Itoa(n.cfg.APIPort)}
}

// Bootstrap sets the other nodes' SPRs, restarting the node when they
// changed.
func (n *Node) Bootstrap(sprs []string) {
	sprs = slices.Clone(sprs)
	slices.Sort(sprs)
	sprs = slices.Compact(sprs)
	n.mu.Lock()
	same := slices.Equal(sprs, n.bootstrap)
	n.bootstrap = sprs
	n.mu.Unlock()
	if !same {
		select {
		case n.restart <- struct{}{}:
		default:
		}
	}
}

func (n *Node) args() []string {
	n.mu.Lock()
	defer n.mu.Unlock()
	ip := n.cfg.IP.String()
	a := []string{"--data-dir=" + n.cfg.DataDir, "--listen-ip=" + ip, "--listen-port=" + strconv.Itoa(n.cfg.Port),
		"--nat=extip:" + ip, "--api-bindaddr=127.0.0.1", "--api-port=" + strconv.Itoa(n.cfg.APIPort),
		"--block-ttl=" + strconv.Itoa(TTL), "--storage-quota=" + strconv.FormatInt(n.cfg.Quota, 10), "--log-level=warn"}
	if len(n.bootstrap) == 0 {
		// Alone: no public bootstrap nodes either. The two flags exclude
		// each other, and a custom bootstrap node replaces the public ones.
		a = append(a, "--no-bootstrap-node")
	}
	for _, s := range n.bootstrap {
		a = append(a, "--bootstrap-node="+s)
	}
	return a
}

// Run keeps the node running until ctx ends.
func (n *Node) Run(ctx context.Context) {
	if err := os.MkdirAll(n.cfg.DataDir, 0o700); err != nil {
		n.log.Error("storage node: data dir", "err", err)
	}
	backoff := time.Second
	for ctx.Err() == nil {
		cmd := exec.Command(n.cfg.Bin, n.args()...)
		cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGTERM}
		if err := cmd.Start(); err != nil {
			n.log.Error("storage node: start", "bin", n.cfg.Bin, "err", err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff):
			}
			backoff = min(backoff*2, time.Minute)
			continue
		}
		n.mu.Lock()
		n.cmd = cmd
		n.mu.Unlock()
		n.log.Info("storage node started", "pid", cmd.Process.Pid, "listen", n.cfg.IP, "bootstrap", len(n.bootstrap))
		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()
		select {
		case <-ctx.Done():
			cmd.Process.Signal(syscall.SIGTERM)
			<-done
			return
		case <-n.restart:
			n.log.Info("storage node: restarting for new bootstrap nodes")
			cmd.Process.Signal(syscall.SIGTERM)
			<-done
			backoff = time.Second
		case err := <-done:
			n.log.Warn("storage node exited; starting it again", "err", err, "in", backoff)
			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff):
			}
			backoff = min(backoff*2, time.Minute)
		}
	}
}

// Ready waits until the node's API answers.
func (n *Node) Ready(ctx context.Context, wait time.Duration) error {
	// A restart asked for and not begun yet would still answer as the old
	// process: let it begin.
	for i := 0; i < 20 && len(n.restart) > 0; i++ {
		time.Sleep(100 * time.Millisecond)
	}
	s := n.Storage()
	deadline := time.Now().Add(wait)
	for time.Now().Before(deadline) {
		if _, err := s.Space(ctx); err == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
	return fmt.Errorf("the storage node did not answer within %v", wait)
}
