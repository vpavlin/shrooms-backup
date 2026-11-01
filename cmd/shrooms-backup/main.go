// shrooms-backup: Shrooms Backup's daemon and its command line (ADR-001).
//
//	shrooms-backup daemon                 run it (as root: a source may back up /etc)
//	shrooms-backup status                 this machine: its role, last run, what it mirrors
//	shrooms-backup now                    back up now
//	shrooms-backup key                    set this source's key from stdin (until the card sets it)
//	shrooms-backup restore --from MIRROR [--source NAME] [--snapshot latest] --target DIR
//
// It runs its own Logos Storage node on the backups' mesh, serves its API
// on that mesh's address (port 7394) and on loopback (7395, where restic
// reaches this machine's repository), and finds the other machines' daemons
// through the shrooms daemon.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/vpavlin/shrooms-backup/internal/backup"
)

const local = "http://127.0.0.1:7395"

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	var err error
	switch os.Args[1] {
	case "daemon":
		err = daemon(os.Args[2:])
	case "status":
		err = status()
	case "now":
		err = now()
	case "key":
		err = setKey(os.Args[2:])
	case "restore":
		err = restore(os.Args[2:])
	default:
		usage()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "shrooms-backup:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: shrooms-backup daemon | status | now | key | restore --from MIRROR [--source NAME] [--snapshot ID] --target DIR")
	os.Exit(2)
}

func daemon(args []string) error {
	fs := flag.NewFlagSet("daemon", flag.ExitOnError)
	cfgPath := fs.String("config", "/etc/shrooms-backup/config.json", "the config")
	state := fs.String("state", "/var/lib/shrooms-backup", "where repositories' maps, the node's store and the cache are kept")
	key := fs.String("key", "/etc/shrooms-backup/key", "this source's restic password (root only)")
	sock := fs.String("socket", "/run/shrooms/shrooms.sock", "the shrooms daemon's control socket")
	cacheMB := fs.Int64("cache-mb", 1024, "the most the cache keeps")
	fs.Parse(args)
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))

	cfg, err := backup.LoadConfig(*cfgPath)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// The mesh: which address to run on, and who everybody is. The daemon
	// may come up after us (a boot); wait for it.
	mesh := &backup.Mesh{Socket: *sock}
	ip, ok := mesh.Addresses()[cfg.Mesh]
	for !ok {
		if err := mesh.Refresh(ctx); err != nil {
			log.Warn("waiting for the shrooms daemon", "err", err)
		}
		if ip, ok = mesh.Addresses()[cfg.Mesh]; ok {
			break
		}
		log.Warn("waiting to be on the backups' mesh", "mesh", cfg.Mesh)
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(10 * time.Second):
		}
	}
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-time.After(30 * time.Second):
				if err := mesh.Refresh(ctx); err != nil {
					log.Warn("mesh", "err", err)
				}
			}
		}
	}()

	node := backup.NewNode(backup.NodeConfig{Bin: cfg.Node.Bin, DataDir: filepath.Join(*state, "node"), IP: ip,
		Port: cfg.Node.Port, APIPort: cfg.Node.APIPort, Quota: cfg.Node.QuotaGB << 30}, log)
	cache, err := backup.NewCache(filepath.Join(*state, "cache"), *cacheMB<<20)
	if err != nil {
		return err
	}
	d := &backup.Daemon{Cfg: cfg, State: *state, Key: *key, Log: log, Mesh: mesh, Node: node, Store: node.Storage(), Cache: cache}
	if err := d.Open(); err != nil {
		return err
	}
	// The nodes it knew, before it starts: the DHT is ours alone.
	node.Bootstrap(d.KnownSPRs())
	go node.Run(ctx)
	if err := node.Ready(ctx, 2*time.Minute); err != nil {
		return err
	}
	d.Start(ctx)

	serve := func(addr string, h http.Handler) {
		ln, err := net.Listen("tcp", addr)
		if err != nil {
			log.Error("listen", "on", addr, "err", err)
			stop()
			return
		}
		log.Info("serving", "on", addr)
		srv := &http.Server{Handler: h, ReadHeaderTimeout: 30 * time.Second}
		go func() { <-ctx.Done(); srv.Close() }()
		srv.Serve(ln)
	}
	go serve(net.JoinHostPort(ip.String(), strconv.Itoa(backup.Port)), d.Handler(false))
	go serve(net.JoinHostPort("127.0.0.1", strconv.Itoa(backup.Port+1)), d.Handler(true))
	<-ctx.Done()
	return nil
}

func getJSON(url string, v any) error {
	resp, err := http.Get(url)
	if err != nil {
		return fmt.Errorf("is the daemon running? %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("%s: %s", resp.Status, strings.TrimSpace(string(b)))
	}
	return json.NewDecoder(resp.Body).Decode(v)
}

func status() error {
	var in backup.Info
	if err := getJSON(local+"/v1/info", &in); err != nil {
		return err
	}
	fmt.Printf("%s: storage %s of %s used", in.Name, size(in.Used), size(in.Quota))
	if in.Mirror {
		fmt.Printf(", a mirror (always on: %v)", in.AlwaysOn)
	}
	fmt.Println()
	if s := in.Source; s != nil {
		fmt.Printf("backs up %s at %s to %s (keep %d daily, %d weekly)\n", strings.Join(s.Paths, " "),
			strings.Join(s.Times, ", "), strings.Join(s.Mirrors, ", "), s.KeepDaily, s.KeepWeekly)
	}
	if r := in.LastRun; r != nil {
		fmt.Printf("last run %s, %s", r.Started.Format("2006-01-02 15:04"), map[bool]string{true: "ok", false: "FAILED"}[r.OK])
		if r.Added != "" {
			fmt.Printf(", added %s", r.Added)
		}
		if r.Error != "" {
			fmt.Printf(": %s", r.Error)
		}
		fmt.Println()
	}
	if len(in.Holds) > 0 {
		fmt.Printf("keeps copies of: %s\n", strings.Join(in.Holds, ", "))
	}
	return nil
}

func size(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GiB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MiB", float64(n)/(1<<20))
	}
	return fmt.Sprintf("%d B", n)
}

func now() error {
	resp, err := http.Post(local+"/v1/run", "application/json", nil)
	if err != nil {
		return fmt.Errorf("is the daemon running? %w", err)
	}
	resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("%s", resp.Status)
	}
	fmt.Println("started; `shrooms-backup status` says how it went")
	return nil
}

func setKey(args []string) error {
	fs := flag.NewFlagSet("key", flag.ExitOnError)
	key := fs.String("key", "/etc/shrooms-backup/key", "where it goes")
	fs.Parse(args)
	fmt.Fprintln(os.Stderr, "this source's backup password (kept root-only; without it the backups cannot be read):")
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil && line == "" {
		return err
	}
	line = strings.TrimSpace(line)
	if len(line) < 16 {
		return fmt.Errorf("at least 16 characters")
	}
	if err := os.MkdirAll(filepath.Dir(*key), 0o700); err != nil {
		return err
	}
	return os.WriteFile(*key, []byte(line+"\n"), 0o600)
}

// restore reads a source's backup from a mirror, with that source's key.
func restore(args []string) error {
	fs := flag.NewFlagSet("restore", flag.ExitOnError)
	from := fs.String("from", "", "the mirror to read from (its device name)")
	source := fs.String("source", "", "whose backup (default: this machine)")
	snap := fs.String("snapshot", "latest", "which snapshot")
	target := fs.String("target", "", "where to put the files")
	key := fs.String("key", "/etc/shrooms-backup/key", "the source's key")
	sock := fs.String("socket", "/run/shrooms/shrooms.sock", "the shrooms daemon's control socket")
	resticBin := fs.String("restic", "restic", "the restic binary")
	fs.Parse(args)
	if *from == "" || *target == "" {
		return fmt.Errorf("--from and --target are needed")
	}
	mesh := &backup.Mesh{Socket: *sock}
	if err := mesh.Refresh(context.Background()); err != nil {
		return err
	}
	if *source == "" {
		*source = mesh.Self()
	}
	a, ok := mesh.Find(*from, "")
	if !ok {
		return fmt.Errorf("%s is not on the mesh", *from)
	}
	cmd := exec.Command(*resticBin, "restore", *snap, "--target", *target)
	cmd.Env = append(os.Environ(),
		"RESTIC_REPOSITORY=rest:http://"+net.JoinHostPort(a.String(), strconv.Itoa(backup.Port))+"/v1/repo/"+*source+"/",
		"RESTIC_PASSWORD_FILE="+*key)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	return cmd.Run()
}
