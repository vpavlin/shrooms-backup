// shrooms-backup: Shrooms Backup's daemon and its command line (ADR-001).
//
//	shrooms-backup daemon                 run it (as root: a source may back up /etc)
//	shrooms-backup status                 this machine: its role, last run, what it mirrors
//	shrooms-backup now                    back up now
//	shrooms-backup key                    set this source's key from stdin (until the card sets it)
//	shrooms-backup plan [--preset NAME…]  what this machine would back up, with sizes
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
	"golang.org/x/term"
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
	case "plan":
		err = plan(os.Args[2:])
	default:
		usage()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "shrooms-backup:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: shrooms-backup daemon | status | now | key | plan [--preset NAME…] | restore --from MIRROR [--source NAME] [--snapshot ID] --target DIR")
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

	quota, lowered := backup.AllowedQuota(*state, cfg.Node.QuotaGB, dirSize(filepath.Join(*state, "node")))
	if lowered != "" {
		log.Warn(lowered)
	}
	node := backup.NewNode(backup.NodeConfig{Bin: cfg.Node.Bin, DataDir: filepath.Join(*state, "node"), IP: ip,
		Port: cfg.Node.Port, APIPort: cfg.Node.APIPort, Quota: quota}, log)
	cache, err := backup.NewCache(filepath.Join(*state, "cache"), *cacheMB<<20)
	if err != nil {
		return err
	}
	d := &backup.Daemon{Cfg: cfg, State: *state, Key: *key, Log: log, Mesh: mesh, Node: node, Store: node.Storage(), Cache: cache,
		// systemd starts it again (Restart=always) with the saved settings.
		ConfigPath: *cfgPath, Restart: func() { time.Sleep(time.Second); stop() }}
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
	// Never over a key: the repository is encrypted with it, and a new one
	// would leave this machine unable to read its own backups.
	if _, err := os.Stat(*key); err == nil {
		return fmt.Errorf("%s exists already: this machine's backups are encrypted with it, so it is not replaced", *key)
	}
	var line string
	if term.IsTerminal(int(os.Stdin.Fd())) {
		// Not echoed: it was, on 2026-11-02, and the passphrase sat on the
		// screen and in the terminal's scrollback.
		fmt.Fprint(os.Stderr, "this machine's backup passphrase (kept root-only; without it the backups cannot be read): ")
		a, err := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Fprintln(os.Stderr)
		if err != nil {
			return err
		}
		fmt.Fprint(os.Stderr, "again: ")
		b, err := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Fprintln(os.Stderr)
		if err != nil {
			return err
		}
		if string(a) != string(b) {
			return fmt.Errorf("the two do not match")
		}
		line = string(a)
	} else {
		l, err := bufio.NewReader(os.Stdin).ReadString('\n')
		if err != nil && l == "" {
			return err
		}
		line = l
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

type list []string

func (l *list) String() string     { return strings.Join(*l, ",") }
func (l *list) Set(v string) error { *l = append(*l, v); return nil }

// plan says what a source would back up here: the presets (from the config,
// or given), resolved on this machine, with sizes after the excludes.
func plan(args []string) error {
	fs := flag.NewFlagSet("plan", flag.ExitOnError)
	cfgPath := fs.String("config", "/etc/shrooms-backup/config.json", "the config")
	var presets list
	fs.Var(&presets, "preset", "a preset (repeat); default: the config's")
	fs.Parse(args)
	src := &backup.SourceConfig{Presets: presets}
	if len(presets) == 0 {
		cfg, err := backup.LoadConfig(*cfgPath)
		if err != nil {
			return err
		}
		if cfg.Source == nil {
			return fmt.Errorf("no source in %s; give --preset", *cfgPath)
		}
		src = cfg.Source
	}
	for _, p := range src.Presets {
		if _, ok := backup.Presets[p]; !ok {
			return fmt.Errorf("no preset %q", p)
		}
	}
	paths, exclude := src.Resolve()
	var total int64
	for _, p := range paths {
		if os.Getenv("PLAN_DEBUG") != "" {
			fmt.Fprintln(os.Stderr, "walking", p)
		}
		n := sizeOf(p, exclude)
		total += n
		fmt.Printf("%10s  %s\n", size(n), p)
	}
	fmt.Printf("%10s  in all (%d paths; restic compresses and keeps each chunk once)\n", size(total), len(paths))
	return nil
}

// sizeOf is a path's size without what the excludes leave out.
func sizeOf(root string, exclude []string) int64 {
	var n int64
	filepath.Walk(root, func(p string, fi os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		for _, e := range exclude {
			if backup.Excluded(e, p) {
				if fi.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
		}
		if fi.Mode().IsRegular() {
			n += fi.Size()
		}
		return nil
	})
	return n
}

// dirSize is what the node's store already takes: it counts as room a
// quota may keep, since it is the backups' already.
func dirSize(root string) int64 {
	var n int64
	filepath.WalkDir(root, func(_ string, e os.DirEntry, err error) error {
		if err == nil && !e.IsDir() {
			if fi, err := e.Info(); err == nil {
				n += fi.Size()
			}
		}
		return nil
	})
	return n
}
