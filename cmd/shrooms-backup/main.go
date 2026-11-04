// shrooms-backup: Shrooms Backup's daemon and its command line (ADR-001).
//
//	shrooms-backup daemon                 run it (as root: a source may back up /etc)
//	shrooms-backup status                 this machine: its role, last run, what it mirrors
//	shrooms-backup now                    back up now
//	shrooms-backup key                    set this source's key from stdin
//	shrooms-backup key --keycard [--machine NAME]   derive a machine's key from the card here and hand it over
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
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/vpavlin/shrooms-backup/internal/backup"
	"github.com/vpavlin/shrooms-backup/internal/card"
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

	// The meshes: which addresses to run on, and who everybody is. The
	// daemon may come up after us (a boot); wait until it says this machine
	// is on at least one of the backups' meshes (ADR-011).
	mesh := &backup.Mesh{Socket: *sock}
	on := func() (map[string]netip.Addr, []string) {
		have, missing := map[string]netip.Addr{}, []string{}
		addrs := mesh.Addresses()
		for _, m := range cfg.Meshes {
			if a, ok := addrs[m]; ok {
				have[m] = a
			} else {
				missing = append(missing, m)
			}
		}
		return have, missing
	}
	ips, missing := on()
	for len(ips) == 0 {
		if err := mesh.Refresh(ctx); err != nil {
			log.Warn("waiting for the shrooms daemon", "err", err)
		}
		if ips, missing = on(); len(ips) > 0 {
			break
		}
		log.Warn("waiting to be on one of the backups' meshes", "meshes", cfg.Meshes)
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(10 * time.Second):
		}
	}
	if len(missing) > 0 {
		log.Warn("not on every mesh backups run on: serving the others, and starting again when it joins", "missing", missing)
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
				// A mesh joined since: its node and API need a start, and
				// systemd gives one (Restart=always).
				if _, now := on(); len(now) < len(missing) {
					log.Info("on another of the backups' meshes now; starting again to serve it")
					stop()
					return
				}
			}
		}
	}()

	// A node per mesh, each in its own store (node-<mesh>); the store kept
	// before there was more than one mesh is the primary's.
	if err := migrateNodeDir(*state, cfg.Primary()); err != nil {
		return err
	}
	var held int64
	for _, m := range cfg.Meshes {
		held += dirSize(filepath.Join(*state, "node-"+m))
	}
	quota, lowered := backup.AllowedQuota(*state, cfg.Node.QuotaGB, held)
	if lowered != "" {
		log.Warn(lowered)
	}
	cache, err := backup.NewCache(filepath.Join(*state, "cache"), *cacheMB<<20)
	if err != nil {
		return err
	}
	d := &backup.Daemon{Cfg: cfg, State: *state, Key: *key, Log: log, Mesh: mesh, Cache: cache, QuotaBytes: quota,
		Nodes: map[string]*backup.Node{}, Stores: map[string]backup.Blobs{},
		// systemd starts it again (Restart=always) with the saved settings.
		ConfigPath: *cfgPath, Restart: func() { time.Sleep(time.Second); stop() }}
	for _, m := range cfg.Meshes {
		ip, ok := ips[m]
		if !ok {
			continue
		}
		port, api := cfg.Ports(m)
		// Each node may take the whole quota: the machine's limit over all
		// of them is kept by the daemon (putMirror).
		n := backup.NewNode(backup.NodeConfig{Bin: cfg.Node.Bin, DataDir: filepath.Join(*state, "node-"+m), IP: ip,
			Port: port, APIPort: api, Quota: quota}, log.With("mesh", m))
		d.Nodes[m], d.Stores[m] = n, n.Storage()
	}
	if err := d.Open(); err != nil {
		return err
	}
	// The nodes each knew, before they start: each mesh's DHT is its own.
	for m, n := range d.Nodes {
		n.Bootstrap(d.KnownSPRs(m))
		go n.Run(ctx)
	}
	for m, n := range d.Nodes {
		if err := n.Ready(ctx, 2*time.Minute); err != nil {
			return fmt.Errorf("the %s mesh's node: %w", m, err)
		}
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
	for m, ip := range ips {
		go serve(net.JoinHostPort(ip.String(), strconv.Itoa(backup.Port)), d.Handler(m))
	}
	go serve(net.JoinHostPort("127.0.0.1", strconv.Itoa(backup.Port+1)), d.Handler(""))
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
	if len(in.Meshes) > 1 {
		fmt.Printf("on meshes %s (the first is the primary: devices elsewhere are name@mesh)\n", strings.Join(in.Meshes, ", "))
	}
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
	useCard := fs.Bool("keycard", false, "derive the key from the Keycard on this machine's reader and hand it to --machine over the mesh")
	machine := fs.String("machine", "", "with --keycard: whose key (its mesh name; default this machine)")
	to := fs.String("to", "", "with --keycard: its backup daemon's address (default: found through the shrooms daemon)")
	check := fs.Bool("check", false, "with --keycard: derive it and print its fingerprint, send nothing")
	reader := fs.String("reader", "", "with --keycard: which reader, when several are attached")
	sock := fs.String("socket", "/run/shrooms/shrooms.sock", "the shrooms daemon's control socket")
	fs.Parse(args)
	if *useCard {
		return keyFromCard(*machine, *to, *reader, *sock, *check)
	}
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
	source := fs.String("source", "", "whose backup (default: this machine); name@mesh, in the mirror's own labels, for one on another of its meshes")
	cfgPath := fs.String("config", "/etc/shrooms-backup/config.json", "this machine's config, for the mesh it backs up over")
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
	// This machine's own backup is reached over the mesh it backs up on:
	// the mirror finds its copy by the mesh the request comes from.
	via := ""
	if *source == "" {
		*source = mesh.Self()
		if cfg, err := backup.LoadConfig(*cfgPath); err == nil {
			via = cfg.SourceMesh()
		}
	}
	a, ok := mesh.Find(*from, via)
	if !ok && via != "" {
		a, ok = mesh.Find(*from, "")
	}
	if !ok {
		return fmt.Errorf("%s is not on this machine's meshes", *from)
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

// migrateNodeDir moves the store kept before there was more than one mesh,
// node/, to the primary mesh's, node-<primary>/.
func migrateNodeDir(state, primary string) error {
	old, now := filepath.Join(state, "node"), filepath.Join(state, "node-"+primary)
	if _, err := os.Stat(old); err != nil {
		return nil
	}
	if _, err := os.Stat(now); err == nil {
		return fmt.Errorf("both %s and %s exist: which is the %s mesh's store?", old, now, primary)
	}
	return os.Rename(old, now)
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

// keyFromCard derives a machine's key from the Keycard on this machine's
// reader and hands it to that machine's daemon (PUT /v1/key), which keeps it
// or adds it beside the key it had. The card goes into whatever has a
// reader, never into the machine being backed up; the key is never printed.
func keyFromCard(machine, to, reader, sock string, check bool) error {
	mesh := &backup.Mesh{Socket: sock}
	meshErr := mesh.Refresh(context.Background())
	if machine == "" {
		if machine = mesh.Self(); machine == "" {
			return fmt.Errorf("--machine NAME: whose key (the shrooms daemon did not say who this is: %v)", meshErr)
		}
	}
	url := ""
	switch {
	case check:
	case to != "":
		url = "http://" + net.JoinHostPort(strings.Trim(to, "[]"), strconv.Itoa(backup.Port))
	case machine == mesh.Self():
		url = local
	default:
		a, ok := mesh.Find(machine, "")
		if !ok {
			return fmt.Errorf("%s is not on this machine's meshes (%v); give --to ADDRESS", machine, meshErr)
		}
		url = "http://" + net.JoinHostPort(a.String(), strconv.Itoa(backup.Port))
	}
	t, done, err := card.OpenReader(reader)
	if err != nil {
		return err
	}
	defer done()
	pin, err := readHidden("Card PIN: ")
	if err != nil {
		return err
	}
	dir := filepath.Join(os.Getenv("HOME"), ".config", "shrooms-backup")
	k, err := card.Key(t, dir, strings.TrimSpace(pin), machine, func() (string, error) {
		return readHidden("This device has not met the card: its pairing password: ")
	})
	if err != nil {
		return err
	}
	id := card.CardID(t)
	fmt.Printf("%s's backup key: fingerprint %s, from card %s\n", machine, card.Fingerprint(k), id)
	if check {
		return nil
	}
	body, _ := json.Marshal(map[string]string{"key": k, "label": strings.TrimSpace("Keycard " + id)})
	req, _ := http.NewRequest("PUT", url+"/v1/key", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := (&http.Client{Timeout: 3 * time.Minute}).Do(req)
	if err != nil {
		return fmt.Errorf("%s's backup daemon: %w", machine, err)
	}
	defer resp.Body.Close()
	var r struct {
		Key   string `json:"key"`
		Error string `json:"error"`
	}
	json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&r)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s refused it: %s", machine, r.Error)
	}
	switch r.Key {
	case "set":
		fmt.Printf("%s has it: its backups are encrypted with it from now on\n", machine)
	case "added":
		fmt.Printf("%s added it beside its old key: every backup opens with either, and it uses this one from now on\n", machine)
	case "same":
		fmt.Printf("%s had it already\n", machine)
	}
	return nil
}

func readHidden(prompt string) (string, error) {
	fmt.Fprint(os.Stderr, prompt)
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		l, err := bufio.NewReader(os.Stdin).ReadString('\n')
		fmt.Fprintln(os.Stderr)
		return strings.TrimSpace(l), err
	}
	b, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Fprintln(os.Stderr)
	return string(b), err
}
