package backup

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Port is the backup daemon's, on each mesh address (beside shrooms-agent's
// 7387 and Foray's 7389).
const Port = 7394

// Config is /etc/shrooms-backup/config.json.
type Config struct {
	// Meshes are the meshes backups run on (ADR-011): on each, a storage
	// node and the API listen on this machine's address. The first is the
	// primary: a device named without a mesh is on it.
	Meshes []string `json:"meshes,omitempty"`
	// Mesh is the primary, kept for configs written before Meshes; fill
	// makes it Meshes[0].
	Mesh string `json:"mesh,omitempty"`
	// Mirror: this machine keeps copies for others.
	Mirror bool `json:"mirror"`
	// AlwaysOn says so to the apps; a laptop is a weak mirror.
	AlwaysOn bool `json:"always_on"`
	// Owners are the devices that may restore any source here, start runs
	// and see everything; a source may always reach its own.
	Owners []string `json:"owners,omitempty"`
	// Source is what this machine backs up; nil for a mirror only.
	Source *SourceConfig `json:"source,omitempty"`
	Node   struct {
		Bin     string `json:"bin"`
		Port    int    `json:"port"`
		APIPort int    `json:"api_port"`
		QuotaGB int64  `json:"quota_gb"`
	} `json:"node"`
	Restic string `json:"restic"` // the restic binary
}

// SourceConfig is a source's backup.
type SourceConfig struct {
	// Presets are named sets of paths (presets.go); Paths are more.
	Presets []string `json:"presets,omitempty"`
	Paths   []string `json:"paths,omitempty"`
	Exclude []string `json:"exclude,omitempty"`
	// Times are when it runs, "HH:MM" local time.
	Times   []string `json:"times"`
	Mirrors []string `json:"mirrors"`
	// Mesh is the one it backs up over, where its mirrors must be: its
	// copies are fetched from its node on that mesh. Empty: the primary.
	Mesh string `json:"mesh,omitempty"`
	// KeepLocal keeps the backup in this machine's own node too; otherwise
	// the node is a staging area, emptied once every mirror has the run.
	KeepLocal  bool `json:"keep_local,omitempty"`
	KeepDaily  int  `json:"keep_daily,omitempty"`
	KeepWeekly int  `json:"keep_weekly,omitempty"`
}

// LoadConfig reads the config and fills in defaults.
func LoadConfig(path string) (*Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var c Config
	if err := json.Unmarshal(b, &c); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if err := c.fill(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &c, nil
}

// fill checks a config and fills in its defaults; the settings the apps
// send go through it too.
func (c *Config) fill() error {
	if len(c.Meshes) == 0 && c.Mesh != "" {
		c.Meshes = []string{c.Mesh}
	}
	if len(c.Meshes) == 0 {
		return errors.New("which meshes do backups run on (\"meshes\")?")
	}
	seen := map[string]bool{}
	for _, m := range c.Meshes {
		if !meshLabel.MatchString(m) {
			return fmt.Errorf("mesh %q is not a mesh label", m)
		}
		if seen[m] {
			return fmt.Errorf("mesh %q is listed twice", m)
		}
		seen[m] = true
	}
	c.Mesh = c.Meshes[0]
	if c.Node.Bin == "" {
		c.Node.Bin = "/usr/local/lib/shrooms-backup/logos-storage"
	}
	if c.Node.Port == 0 {
		c.Node.Port = 8590
	}
	if c.Node.APIPort == 0 {
		c.Node.APIPort = 8591
	}
	if c.Node.QuotaGB == 0 {
		c.Node.QuotaGB = DefaultQuotaGB
	}
	if c.Node.QuotaGB < 0 {
		return errors.New("a quota is a number of GB")
	}
	if c.Restic == "" {
		c.Restic = "restic"
	}
	for _, o := range c.Owners {
		name, mesh, _ := strings.Cut(o, "@")
		if !deviceName.MatchString(name) || strings.Contains(o, "@") && !seen[mesh] {
			return fmt.Errorf("owner %q is not a device name, or name@mesh for a mesh backups run on", o)
		}
	}
	if s := c.Source; s != nil {
		if s.Mesh != "" && !seen[s.Mesh] {
			return fmt.Errorf("the source's mesh %q is not one backups run on", s.Mesh)
		}
		if len(s.Paths)+len(s.Presets) == 0 || len(s.Mirrors) == 0 {
			return errors.New("a source needs presets or paths, and mirrors")
		}
		for _, p := range s.Presets {
			if _, ok := Presets[p]; !ok {
				return fmt.Errorf("no preset %q", p)
			}
		}
		for _, p := range s.Paths {
			if !filepath.IsAbs(p) {
				return fmt.Errorf("path %q is not absolute", p)
			}
		}
		for _, m := range s.Mirrors {
			if !deviceName.MatchString(m) {
				return fmt.Errorf("mirror %q is not a device name", m)
			}
		}
		if len(s.Times) == 0 {
			s.Times = []string{"03:00", "15:00"}
		}
		for _, t := range s.Times {
			if _, err := time.Parse("15:04", t); err != nil {
				return fmt.Errorf("time %q: want HH:MM", t)
			}
		}
		if s.KeepDaily == 0 {
			s.KeepDaily = 14
		}
		if s.KeepWeekly == 0 {
			s.KeepWeekly = 12
		}
		if s.KeepDaily < 0 || s.KeepWeekly < 0 {
			return errors.New("keep a number of days and weeks")
		}
	}
	return nil
}

// Daemon is shrooms-backup on one machine.
type Daemon struct {
	Cfg   *Config
	State string // /var/lib/shrooms-backup
	Key   string // the source's restic password file
	Log   *slog.Logger
	Mesh  *Mesh
	// Nodes and Stores are the storage node and its API on each mesh
	// (ADR-011). Node and Store serve any mesh without an entry: a daemon
	// with one store (tests) gives just those.
	Nodes  map[string]*Node
	Stores map[string]Blobs
	Node   *Node
	Store  Blobs
	Cache  *Cache
	// QuotaBytes is what this machine keeps for others, on all its meshes
	// together; 0: Node.QuotaGB.
	QuotaBytes int64
	// LocalURL is this daemon's loopback API, where restic reaches /self/
	// (default http://127.0.0.1:7395); PeerURL finds another daemon's API
	// (default: its mesh address). Both for tests.
	LocalURL string
	PeerURL  func(device string) (string, error)
	// ConfigPath is where the apps' settings are saved; Restart, when a
	// setting cannot change under a running node (the mirror role, the
	// quota), ends the daemon for systemd to start again.
	ConfigPath string
	Restart    func()

	mu      sync.Mutex
	self    *Repo       // the staging repository of before ADR-014, if any
	src     *SourceRepo // this source's repository (ADR-014)
	bulkC   *http.Client
	mirrors map[string]*mirrorCopy       // by mirrorKey: mesh, then the source
	sprs    map[string]map[string]string // mesh -> device -> its node's SPR
	runs    []Run
	running bool
	prog    *Progress     // the run under way, for the apps' bar
	wake    chan struct{} // the schedule, when the times change
	keys    []KeyInfo     // the source's keys, as last read
	client  *http.Client
	ctx     context.Context // set by Start: a copy made after it gets its loop then
}

type mirrorCopy struct {
	mesh   string
	source string
	// store is the copy, a restic repository in a directory (ADR-014);
	// repo the map of a source from before, kept for its version handshake
	// while it pushes the old way, its objects copied into store.
	store   *Store
	repo    *Repo
	pending int64 // the newest map version asked for
	missing int
	// missingBytes is what the fetch still lacks, the whole map's live size
	// while it is still finding out: never less than the truth.
	missingBytes int64
	err          string
	newest       chan Map
}

// Run is one source run, for the apps.
type Run struct {
	Started  time.Time         `json:"started"`
	Finished time.Time         `json:"finished"`
	OK       bool              `json:"ok"`
	Error    string            `json:"error,omitempty"`
	Added    string            `json:"added,omitempty"` // restic's "Added to the repository"
	Mirrors  map[string]string `json:"mirrors"`         // device -> "ok" or why not
	// From restic's summary: what it read, and what was new or changed.
	Files     int64 `json:"files,omitempty"`
	FilesNew  int64 `json:"files_new,omitempty"`
	FilesMod  int64 `json:"files_changed,omitempty"`
	Bytes     int64 `json:"bytes,omitempty"`
	DataAdded int64 `json:"data_added,omitempty"`
	// Stored is the repository's size after the run: what each mirror holds.
	Stored int64 `json:"stored,omitempty"`
}

// Open loads the state.
func (d *Daemon) Open() error {
	d.mirrors, d.sprs = map[string]*mirrorCopy{}, map[string]map[string]string{}
	d.wake = make(chan struct{}, 1)
	d.client = &http.Client{Timeout: 30 * time.Second}
	d.loadSPRs()
	if b, err := os.ReadFile(filepath.Join(d.State, "runs.json")); err == nil {
		json.Unmarshal(b, &d.runs)
	}
	if err := d.migrateMirrors(); err != nil {
		return err
	}
	var err error
	if d.Cfg.Source != nil {
		// The staging of before ADR-014, read until the mirrors hold it all.
		if _, err := os.Stat(filepath.Join(d.State, "self", "map.json")); err == nil {
			if st := d.storeOf(d.Cfg.SourceMesh()); st != nil {
				if d.self, err = OpenRepo("self", filepath.Join(d.State, "self"), st, d.Cache); err != nil {
					return err
				}
			}
		}
		if d.src, err = OpenSource(d, filepath.Join(d.State, "source"), d.self); err != nil {
			return err
		}
	}
	for _, mesh := range d.Cfg.meshes() {
		entries, _ := os.ReadDir(filepath.Join(d.State, "mirrors", mesh))
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			if _, err := d.mirrorOf(mesh, e.Name()); err != nil {
				return err
			}
		}
	}
	return nil
}

// migrateMirrors moves copies kept before there was more than one mesh,
// mirrors/<source>, to mirrors/<primary>/<source>. A directory named after a
// mesh backups run on, with no map of its own, is already the new layout.
func (d *Daemon) migrateMirrors() error {
	dir := filepath.Join(d.State, "mirrors")
	entries, _ := os.ReadDir(dir)
	var old []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		_, err := os.Stat(filepath.Join(dir, e.Name(), "map.json"))
		if slices.Contains(d.Cfg.meshes(), e.Name()) && err != nil {
			continue
		}
		old = append(old, e.Name())
	}
	if len(old) == 0 {
		return nil
	}
	// Through a temporary name: a source called like the primary mesh would
	// otherwise be moved into itself.
	tmp := filepath.Join(d.State, "mirrors.migrating")
	if err := os.MkdirAll(tmp, 0o700); err != nil {
		return err
	}
	for _, n := range old {
		if err := os.Rename(filepath.Join(dir, n), filepath.Join(tmp, n)); err != nil {
			return err
		}
	}
	into := filepath.Join(dir, d.Cfg.Primary())
	if err := os.MkdirAll(into, 0o700); err != nil {
		return err
	}
	for _, n := range old {
		if err := os.Rename(filepath.Join(tmp, n), filepath.Join(into, n)); err != nil {
			return err
		}
	}
	d.Log.Info("copies moved under their mesh", "mesh", d.Cfg.Primary(), "sources", len(old))
	return os.Remove(tmp)
}

// repoMade: this source's repository exists (restic init has run).
func (d *Daemon) repoMade() bool {
	return d.src != nil && d.src.Made()
}

// bulk is the client for objects: no limit on the whole request, which a
// 64 MB pack over a slow link would pass, only on waiting for an answer.
func (d *Daemon) bulk() *http.Client {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.bulkC == nil {
		d.bulkC = &http.Client{Transport: &http.Transport{ResponseHeaderTimeout: 2 * time.Minute, IdleConnTimeout: time.Minute}}
	}
	return d.bulkC
}

// storeOf is the storage on a mesh, nil when this machine has none there.
func (d *Daemon) storeOf(mesh string) Blobs {
	if d.Stores != nil {
		if s, ok := d.Stores[mesh]; ok {
			return s
		}
		return nil
	}
	return d.Store
}

func (d *Daemon) nodeOf(mesh string) *Node {
	if d.Nodes != nil {
		return d.Nodes[mesh]
	}
	return d.Node
}

var (
	deviceName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)
	// meshLabel is what shrooms allows a mesh label to be.
	meshLabel = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,31}$`)
)

// Primary is the first mesh: a device named without a mesh is on it.
func (c *Config) Primary() string {
	if len(c.Meshes) == 0 {
		return c.Mesh // a config not filled in (tests)
	}
	return c.Meshes[0]
}

// meshes are Meshes, or the one Mesh of a config not filled in.
func (c *Config) meshes() []string {
	if len(c.Meshes) == 0 && c.Mesh != "" {
		return []string{c.Mesh}
	}
	return slices.Clone(c.Meshes)
}

// SourceMesh is the mesh this machine backs up over.
func (c *Config) SourceMesh() string {
	if c.Source != nil && c.Source.Mesh != "" {
		return c.Source.Mesh
	}
	return c.Primary()
}

// Ports are the storage node's on a mesh: the primary's are Node.Port and
// Node.APIPort, as before there was more than one; each mesh after it the
// next two. A mesh dropped from the middle moves the ones after it, which
// costs a node restart and a new SPR, learnt again on the next push.
func (c *Config) Ports(mesh string) (port, api int) {
	i := slices.Index(c.Meshes, mesh)
	return c.Node.Port + 2*i, c.Node.APIPort + 2*i
}

// Qualified is how this machine names a device on a mesh: bare on the
// primary, name@mesh on any other. Mesh labels are this machine's own.
func (c *Config) Qualified(name, mesh string) string {
	if mesh == c.Primary() {
		return name
	}
	return name + "@" + mesh
}

func mirrorKey(mesh, source string) string { return mesh + "/" + source }

func (d *Daemon) mirrorOf(mesh, source string) (*mirrorCopy, error) {
	if !deviceName.MatchString(source) {
		return nil, fmt.Errorf("%q is not a device name", source)
	}
	// A node is only needed for copies pushed the old way (ADR-014).
	st := d.storeOf(mesh)
	if st == nil {
		st = noBlobs{}
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if c, ok := d.mirrors[mirrorKey(mesh, source)]; ok {
		return c, nil
	}
	dir := filepath.Join(d.State, "mirrors", mesh, source)
	r, err := OpenRepo(d.Cfg.Qualified(source, mesh), dir, st, d.Cache)
	if err != nil {
		return nil, err
	}
	store, err := OpenStore(d.Cfg.Qualified(source, mesh), filepath.Join(dir, "repo"))
	if err != nil {
		return nil, err
	}
	c := &mirrorCopy{mesh: mesh, source: source, repo: r, store: store, pending: r.Map().Version, newest: make(chan Map, 1)}
	d.mirrors[mirrorKey(mesh, source)] = c
	if d.ctx != nil {
		go d.fetchLoop(d.ctx, c)
	}
	return c, nil
}

// KnownSPRs are the other nodes on a mesh this machine's node there knew,
// to bootstrap from at start. Each mesh's nodes are a DHT of their own.
func (d *Daemon) KnownSPRs(mesh string) []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]string, 0, len(d.sprs[mesh]))
	for _, s := range d.sprs[mesh] {
		out = append(out, s)
	}
	return out
}

// loadSPRs reads sprs.json: mesh -> device -> SPR, or, written before there
// was more than one mesh, device -> SPR, which is the primary's.
func (d *Daemon) loadSPRs() {
	b, err := os.ReadFile(filepath.Join(d.State, "sprs.json"))
	if err != nil {
		return
	}
	if json.Unmarshal(b, &d.sprs) == nil {
		return
	}
	var flat map[string]string
	if json.Unmarshal(b, &flat) == nil {
		d.sprs = map[string]map[string]string{d.Cfg.Primary(): flat}
	}
}

func (d *Daemon) saveSPRs() {
	b, _ := json.Marshal(d.sprs)
	os.WriteFile(filepath.Join(d.State, "sprs.json"), b, 0o600)
}

// nodeReady waits for this machine's node on a mesh, which restarts when it
// learns a peer.
func (d *Daemon) nodeReady(ctx context.Context, mesh string) error {
	n := d.nodeOf(mesh)
	if n == nil {
		return nil
	}
	return n.Ready(ctx, 2*time.Minute)
}

// learnSPR remembers another node on a mesh, and has this machine's node
// there bootstrap from every one known on it.
func (d *Daemon) learnSPR(mesh, device, spr string) {
	if !strings.HasPrefix(spr, "spr:") {
		return
	}
	d.mu.Lock()
	if d.sprs[mesh] == nil {
		d.sprs[mesh] = map[string]string{}
	}
	old := d.sprs[mesh][device]
	if old == spr {
		d.mu.Unlock()
		return
	}
	// The newest SPR is kept, for the node's next start; the node restarts
	// for it only when the peer or its addresses changed (sprIdentity).
	moved := sprIdentity(old) != sprIdentity(spr)
	d.sprs[mesh][device] = spr
	all := make([]string, 0, len(d.sprs[mesh]))
	for _, s := range d.sprs[mesh] {
		all = append(all, s)
	}
	d.saveSPRs()
	d.mu.Unlock()
	if n := d.nodeOf(mesh); n != nil {
		n.SetBootstrap(all, moved)
	}
}

// --- the API --------------------------------------------------------------------

type ctxKey struct{}

type caller struct {
	name  string
	mesh  string // the mesh it called on; the primary for a local caller
	local bool
}

// Handler is the API on a mesh, callers named by the mesh they called on;
// on loopback (mesh ""), this machine, with restic's own repository at /self/.
func (d *Daemon) Handler(mesh string) http.Handler {
	local := mesh == ""
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/info", d.info)
	mux.HandleFunc("PUT /v1/mirror/{source}", d.putMirror)
	mux.HandleFunc("GET /v1/mirror/{source}", d.getMirror)
	mux.HandleFunc("POST /v1/run", d.runNow)
	mux.HandleFunc("GET /v1/runs", d.getRuns)
	mux.HandleFunc("PUT /v1/config", d.putConfig)
	mux.HandleFunc("PUT /v1/key", d.putKey)
	mux.HandleFunc("GET /v1/keys", d.getKeys)
	// The phone app knows one machine; this is how it finds the others.
	mux.HandleFunc("GET /v1/peers", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{"self": d.Mesh.Self(), "peers": d.Mesh.Peers()})
	})
	mux.HandleFunc("GET /v1/presets", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, Presets) })
	mux.HandleFunc("/v1/repo/{source}/", d.repo)
	if local && d.src != nil {
		mux.Handle("/self/", http.StripPrefix("/self", d.src.Handler()))
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c := caller{local: local, mesh: mesh}
		if local {
			c.name, c.mesh = d.Mesh.Self(), d.Cfg.Primary()
		} else if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
			if a, err := netip.ParseAddr(host); err == nil {
				var on string
				c.name, on = d.Mesh.WhoOn(a)
				// An address is on one mesh: one from another, at this
				// mesh's address, is not a member here.
				if on != "" && on != mesh {
					c.name = ""
				}
			}
		}
		if c.name == "" && !local {
			fail(w, http.StatusForbidden, errors.New("the mesh does not know who you are"))
			return
		}
		mux.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, c)))
	})
}

func callerOf(r *http.Request) caller { c, _ := r.Context().Value(ctxKey{}).(caller); return c }

// owner: the caller may change settings, start runs and read every copy. An
// owner named bare is the device of that name on the primary mesh only: on
// another mesh, a friend's say, anyone can be called "laptop".
func (d *Daemon) owner(c caller) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return c.local || slices.Contains(d.Cfg.Owners, d.Cfg.Qualified(c.name, c.mesh)) ||
		slices.Contains(d.Cfg.Owners, c.name+"@"+c.mesh)
}

// resolve is the copy a request names: name@mesh as given; a source's own
// name, said by itself, its own copy on the mesh it called on (it cannot
// know this machine's label for it); any other bare name, the primary's.
func (d *Daemon) resolve(c caller, s string) (mesh, source string, err error) {
	name, m, qualified := strings.Cut(s, "@")
	switch {
	case qualified:
		if !slices.Contains(d.Cfg.meshes(), m) {
			return "", "", fmt.Errorf("backups do not run on a mesh called %q here", m)
		}
	case !c.local && name == c.name:
		m = c.mesh
	default:
		m = d.Cfg.Primary()
	}
	if !deviceName.MatchString(name) {
		return "", "", fmt.Errorf("%q is not a device name", name)
	}
	return m, name, nil
}

// mayRead: a source reads its own copies, an owner any.
func (d *Daemon) mayRead(c caller, mesh, source string) bool {
	return !c.local && c.name == source && c.mesh == mesh || d.owner(c)
}

// source is this machine's source config; the apps may replace it.
func (d *Daemon) source() *SourceConfig {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.Cfg.Source
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

func fail(w http.ResponseWriter, code int, err error) {
	writeJSON(w, code, map[string]string{"error": err.Error()})
}

// Info is what a daemon says about itself.
type Info struct {
	Name     string `json:"name"`
	Mirror   bool   `json:"mirror"`
	AlwaysOn bool   `json:"always_on"`
	SPR      string `json:"spr"`
	Quota    int64  `json:"quota"`
	Used     int64  `json:"used"`
	QuotaGB  int64  `json:"quota_gb"`
	// Disk is the filesystem the store is on, so the apps can say how
	// much of it a quota would take.
	Disk    *Disk         `json:"disk,omitempty"`
	Owners  []string      `json:"owners"`
	Source  *SourceConfig `json:"source,omitempty"`
	LastRun *Run          `json:"last_run,omitempty"`
	Running bool          `json:"running"`
	Next    *time.Time    `json:"next,omitempty"` // the next scheduled run
	Holds   []string      `json:"holds"`          // sources this machine mirrors
	// Held is how far each of those copies is, and its size.
	Held []MirrorStatus `json:"held"`
	// Owner: the caller may change the settings and start runs.
	Owner bool `json:"owner"`
	// Key: a source has its key (without one it cannot back up).
	Key bool `json:"key"`
	// Meshes are the ones backups run on here, the primary first; holds,
	// owners and mirrors on any other are written name@mesh.
	Meshes []string `json:"meshes"`
	// Progress is the run under way, while Running.
	Progress *Progress `json:"progress,omitempty"`
}

// Progress is a run under way, for a progress bar. restic first reads the
// files and stores what is new in this machine's node ("reading": files and
// bytes against what it found); then each mirror fetches what it lacks
// ("mirrors": bytes of this run each holds).
type Progress struct {
	Phase     string    `json:"phase"`
	Started   time.Time `json:"started"`
	Files     int64     `json:"files"`
	FilesDone int64     `json:"files_done"`
	Bytes     int64     `json:"bytes"`
	BytesDone int64     `json:"bytes_done"`
	// Remaining is restic's own estimate while reading, in seconds.
	Remaining int64                     `json:"seconds_remaining,omitempty"`
	Mirrors   map[string]MirrorProgress `json:"mirrors,omitempty"`
}

// MirrorProgress is how much of a run one mirror holds.
type MirrorProgress struct {
	Bytes int64  `json:"bytes"`
	Done  int64  `json:"done"`
	OK    bool   `json:"ok,omitempty"` // it holds the run
	Error string `json:"error,omitempty"`
}

func (d *Daemon) setProgress(f func(p *Progress)) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.prog != nil {
		f(d.prog)
	}
}

// Info is the daemon's state as seen from mesh: the SPR is its node's there.
// Quota and Used are the machine's, over every mesh.
func (d *Daemon) Info(ctx context.Context, mesh string) Info {
	in := Info{Name: d.Mesh.Self(), Holds: []string{}, Held: []MirrorStatus{}, Meshes: d.Cfg.meshes()}
	if k, err := diskOf(d.State); err == nil {
		in.Disk = &k
	}
	if st, ok := d.storeOf(mesh).(*Storage); ok {
		in.SPR, _ = st.SPR(ctx)
	}
	in.Quota, in.Used = d.space(ctx)
	d.mu.Lock()
	in.Mirror, in.AlwaysOn, in.QuotaGB, in.Source = d.Cfg.Mirror, d.Cfg.AlwaysOn, d.Cfg.Node.QuotaGB, d.Cfg.Source
	in.Owners = append([]string{}, d.Cfg.Owners...)
	in.Running = d.running
	if d.prog != nil {
		p := *d.prog
		p.Mirrors = map[string]MirrorProgress{}
		for k, v := range d.prog.Mirrors {
			p.Mirrors[k] = v
		}
		in.Progress = &p
	}
	if in.Source != nil {
		next := nextTime(time.Now(), in.Source.Times)
		in.Next = &next
	}
	copies := make([]*mirrorCopy, 0, len(d.mirrors))
	for _, mc := range d.mirrors {
		copies = append(copies, mc)
	}
	if n := len(d.runs); n > 0 {
		last := d.runs[n-1]
		in.LastRun = &last
	}
	d.mu.Unlock()
	for _, mc := range copies {
		in.Held = append(in.Held, d.mirrorStatus(mc))
	}
	slices.SortFunc(in.Held, func(a, b MirrorStatus) int { return strings.Compare(a.Source, b.Source) })
	for _, h := range in.Held {
		in.Holds = append(in.Holds, h.Source)
	}
	return in
}

// space is what this machine keeps for others and how much of it is used,
// over the nodes on all its meshes. Each node may take the whole quota; the
// machine-wide limit is kept by putMirror.
func (d *Daemon) space(ctx context.Context) (quota, used int64) {
	seen := map[Blobs]bool{}
	stores := []Blobs{d.Store}
	for _, m := range d.Cfg.meshes() {
		stores = append(stores, d.storeOf(m))
	}
	for _, s := range stores {
		st, ok := s.(*Storage)
		if !ok || seen[s] {
			continue
		}
		seen[s] = true
		if sp, err := st.Space(ctx); err == nil {
			quota, used = max(quota, sp.QuotaMax), used+sp.QuotaUsed
		}
	}
	return quota, used
}

func (d *Daemon) quotaBytes() int64 {
	if d.QuotaBytes > 0 {
		return d.QuotaBytes
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.Cfg.Node.QuotaGB == 0 {
		return DefaultQuotaGB * GB // a config not filled in (tests)
	}
	return d.Cfg.Node.QuotaGB * GB
}

func (d *Daemon) info(w http.ResponseWriter, r *http.Request) {
	c := callerOf(r)
	in := d.Info(r.Context(), c.mesh)
	in.Owner = d.owner(c)
	in.Key = in.Source != nil && d.HasKey()
	writeJSON(w, 200, in)
}

// putMirror: a source hands its newest map; this mirror fetches what it lacks
// and adopts the map once it holds all of it.
func (d *Daemon) putMirror(w http.ResponseWriter, r *http.Request) {
	c, source := callerOf(r), r.PathValue("source")
	d.mu.Lock()
	mirror := d.Cfg.Mirror
	d.mu.Unlock()
	if !mirror {
		fail(w, http.StatusForbidden, errors.New("this machine is not a mirror"))
		return
	}
	if c.local || c.name != source {
		fail(w, http.StatusForbidden, fmt.Errorf("only %s may send %s's backup", source, source))
		return
	}
	var body struct {
		SPR string `json:"spr"`
		Map Map    `json:"map"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 256<<20)).Decode(&body); err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	// The machine's limit over every mesh: each mesh's node may take the
	// whole quota, so the sum is kept here, where the size is known before
	// anything is fetched.
	if held, limit := d.heldWithout(c.mesh, source)+body.Map.Size(true), d.quotaBytes(); held > limit { // the old way
		fail(w, http.StatusInsufficientStorage, fmt.Errorf("keeping %s's backup would take %s over its limit (%s of %s)",
			d.Cfg.Qualified(source, c.mesh), d.Mesh.Self(), gbString(held), gbString(limit)))
		return
	}
	mc, err := d.mirrorOf(c.mesh, source)
	if err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	d.learnSPR(c.mesh, source, body.SPR)
	d.mu.Lock()
	if body.Map.Version > mc.pending {
		// All of it, until the fetch has looked: a source asking at once
		// would otherwise read nothing missing, and draw a full bar.
		mc.pending, mc.missingBytes = body.Map.Version, body.Map.Size(false)
	}
	d.mu.Unlock()
	select {
	case <-mc.newest:
	default:
	}
	mc.newest <- body.Map
	writeJSON(w, http.StatusAccepted, d.mirrorStatus(mc))
}

// heldWithout is what the copies kept here take, but one source's.
func (d *Daemon) heldWithout(mesh, source string) int64 {
	d.mu.Lock()
	copies := make([]*mirrorCopy, 0, len(d.mirrors))
	for k, mc := range d.mirrors {
		if k != mirrorKey(mesh, source) {
			copies = append(copies, mc)
		}
	}
	d.mu.Unlock()
	var n int64
	for _, mc := range copies {
		n += mc.size()
	}
	return n
}

func gbString(n int64) string { return strconv.FormatFloat(float64(n)/float64(GB), 'f', 1, 64) + " GB" }

// MirrorStatus is how far a mirror is with a source's copy.
type MirrorStatus struct {
	// Source is name, or name@mesh on a mesh other than the primary.
	Source  string `json:"source"`
	Mesh    string `json:"mesh"`
	Adopted int64  `json:"adopted"` // the newest map it holds in full
	Pending int64  `json:"pending"`
	Missing int    `json:"missing"`
	// MissingBytes is what the fetch still lacks of the pending map.
	MissingBytes int64  `json:"missing_bytes"`
	Error        string `json:"error,omitempty"`
	Bytes        int64  `json:"bytes"` // the adopted copy's size, held deletions included
}

func (d *Daemon) mirrorStatus(mc *mirrorCopy) MirrorStatus {
	m := mc.repo.Map()
	d.mu.Lock()
	defer d.mu.Unlock()
	return MirrorStatus{Source: d.Cfg.Qualified(mc.source, mc.mesh), Mesh: mc.mesh, Adopted: m.Version, Pending: mc.pending,
		Missing: mc.missing, MissingBytes: mc.missingBytes, Error: mc.err, Bytes: mc.size()}
}

// copyFor is the copy a request names, if the caller may read it.
func (d *Daemon) copyFor(w http.ResponseWriter, r *http.Request) (*mirrorCopy, string, bool) {
	c := callerOf(r)
	mesh, source, err := d.resolve(c, r.PathValue("source"))
	if err != nil {
		fail(w, http.StatusBadRequest, err)
		return nil, "", false
	}
	if !d.mayRead(c, mesh, source) {
		fail(w, http.StatusForbidden, errors.New("only the source or an owner may read its backup"))
		return nil, "", false
	}
	d.mu.Lock()
	mc, ok := d.mirrors[mirrorKey(mesh, source)]
	d.mu.Unlock()
	if !ok {
		fail(w, http.StatusNotFound, fmt.Errorf("no copy of %s here", d.Cfg.Qualified(source, mesh)))
		return nil, "", false
	}
	return mc, source, true
}

func (d *Daemon) getMirror(w http.ResponseWriter, r *http.Request) {
	if mc, _, ok := d.copyFor(w, r); ok {
		writeJSON(w, 200, d.mirrorStatus(mc))
	}
}

// Stalled is how long a fetch, or a source waiting for a mirror, goes on
// without anything moving before it gives up.
var Stalled = 30 * time.Minute

// pollEvery is how often a source asks a mirror how far it is.
var pollEvery = 5 * time.Second

// fetchLoop keeps one source's copy in step: for each newest map, fetch
// every live object this node lacks, then adopt the map.
func (d *Daemon) fetchLoop(ctx context.Context, mc *mirrorCopy) {
	source := d.Cfg.Qualified(mc.source, mc.mesh)
	for {
		var m Map
		select {
		case <-ctx.Done():
			return
		case m = <-mc.newest:
		}
		err := d.fetchAll(ctx, mc, m)
		d.mu.Lock()
		if err != nil {
			mc.err = err.Error()
		} else {
			mc.err, mc.missing, mc.missingBytes = "", 0, 0
		}
		d.mu.Unlock()
		if err != nil {
			d.Log.Warn("mirror: not in step; trying again", "source", source, "version", m.Version, "err", err)
			// The same map again in a while, unless a newer one comes: a
			// node restarting (a new peer to bootstrap from) refused the
			// first fetch on proteus's first run, and the map was dropped
			// until the next backup (2026-11-01).
			select {
			case <-ctx.Done():
				return
			case newer := <-mc.newest:
				m = newer
			case <-time.After(30 * time.Second):
			}
			select {
			case mc.newest <- m:
			default:
			}
			continue
		}
		if err := mc.repo.Adopt(m); err != nil {
			d.Log.Warn("mirror: adopt", "source", source, "err", err)
			continue
		}
		if err := d.toStore(ctx, mc); err != nil {
			d.Log.Warn("mirror: into the store", "source", source, "err", err)
		}
		d.Log.Info("mirror: in step", "source", source, "version", m.Version, "objects", len(m.Objects))
	}
}

func (d *Daemon) fetchAll(ctx context.Context, mc *mirrorCopy, m Map) error {
	want := map[string]int64{}
	for _, o := range m.Objects {
		if o.Deleted == nil {
			want[o.CID] = o.Size
		}
	}
	if err := d.nodeReady(ctx, mc.mesh); err != nil {
		return err
	}
	store := d.storeOf(mc.mesh)
	var mu sync.Mutex // guards missing, moved and lastErr, shared with the workers
	missing := map[string]int64{}
	for cid, size := range want {
		if !store.Has(ctx, cid, size) {
			missing[cid] = size
		}
	}
	report := func() { // with mu held
		var left int64
		for _, size := range missing {
			left += size
		}
		d.mu.Lock()
		mc.missing, mc.missingBytes = len(missing), left
		d.mu.Unlock()
	}
	report()

	// Several at a time, and past what cannot be found yet. The node's
	// fetch returns once the object is here, or after about 30 s when no
	// peer says it has it: a manifest is found only through the DHT, and a
	// node announces just its 500 most recent (libp2p's provided-key cap,
	// found by jimmy-crib/vpavlin on k11's 9,500, 2026-11-05). Stopping at
	// the first one not found started the pass over in another order, and
	// fetched nothing for hours. What failed is tried again on the next pass.
	moved := time.Now()
	var lastErr error
	for len(missing) > 0 {
		mu.Lock()
		cids := make([]string, 0, len(missing))
		for cid := range missing {
			cids = append(cids, cid)
		}
		mu.Unlock()
		todo := make(chan string)
		var wg sync.WaitGroup
		for i := 0; i < FetchWorkers; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for cid := range todo {
					mu.Lock()
					size := missing[cid]
					mu.Unlock()
					err := store.Fetch(ctx, cid)
					got := store.Has(ctx, cid, size)
					mu.Lock()
					if got {
						delete(missing, cid)
						moved = time.Now()
					} else if err != nil {
						lastErr = err
					}
					report()
					mu.Unlock()
				}
			}()
		}
	feed:
		for _, cid := range cids {
			select {
			case todo <- cid:
			case <-ctx.Done():
				break feed
			}
		}
		close(todo)
		wg.Wait()
		if err := ctx.Err(); err != nil {
			return err
		}
		if len(missing) == 0 {
			break
		}
		if time.Since(moved) > Stalled {
			return fmt.Errorf("%d objects still missing, none fetched for %v: %v", len(missing), Stalled, lastErr)
		}
		d.mu.Lock()
		mc.err = fmt.Sprintf("%d objects not found yet, trying again: %v", len(missing), lastErr)
		d.mu.Unlock()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(retryPause):
		}
	}
	return nil
}

// FetchWorkers is how many objects a mirror fetches at once.
var FetchWorkers = 8

// retryPause is between passes over what could not be fetched yet.
var retryPause = 20 * time.Second

// repo serves a repository to restic, read-only: a mirror's copy of a
// source, for the source itself or an owner restoring it.
// A source writes its own copy over restic's REST protocol (ADR-014);
// owners, and the source restoring, read it.
func (d *Daemon) repo(w http.ResponseWriter, r *http.Request) {
	c := callerOf(r)
	mesh, source, err := d.resolve(c, r.PathValue("source"))
	if err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	d.mu.Lock()
	mirror := d.Cfg.Mirror
	d.mu.Unlock()
	writer := !c.local && c.name == source && c.mesh == mesh && mirror
	var mc *mirrorCopy
	if writer {
		if mc, err = d.mirrorOf(mesh, source); err != nil {
			fail(w, http.StatusBadRequest, err)
			return
		}
	} else if mc, _, _ = d.copyFor(w, r); mc == nil {
		return
	}
	h := mc.store.Handler(!writer, true, d.room)
	http.StripPrefix("/v1/repo/"+r.PathValue("source"), h).ServeHTTP(w, r)
}

// room says whether n more bytes fit in what this machine keeps for others,
// over every copy and mesh.
func (d *Daemon) room(n int64) error {
	if held, limit := d.heldBytes()+n, d.quotaBytes(); held > limit {
		return fmt.Errorf("%w: %s would keep %s of its %s", ErrNoRoom, d.Mesh.Self(), gbString(held), gbString(limit))
	}
	return nil
}

func (d *Daemon) heldBytes() int64 {
	d.mu.Lock()
	copies := make([]*mirrorCopy, 0, len(d.mirrors))
	for _, mc := range d.mirrors {
		copies = append(copies, mc)
	}
	d.mu.Unlock()
	var n int64
	for _, mc := range copies {
		n += mc.size()
	}
	return n
}

// size is what a copy takes: its store, or, before its old objects are
// copied out of the node, its map's size.
func (mc *mirrorCopy) size() int64 {
	return max(mc.store.Size(), mc.repo.Map().Size(true))
}

// noBlobs is a node a machine does not have (no copies the old way).
type noBlobs struct{}

func (noBlobs) Put(context.Context, string, []byte) (string, error) { return "", errNoNode }
func (noBlobs) Get(context.Context, string, bool) ([]byte, error)   { return nil, errNoNode }
func (noBlobs) Has(context.Context, string, int64) bool             { return false }
func (noBlobs) Fetch(context.Context, string) error                 { return errNoNode }
func (noBlobs) Delete(context.Context, string) error                { return nil }

var errNoNode = errors.New("no storage node on this mesh")

// toStore copies what an old-way map holds out of the node into the copy's
// store: live objects in, deleted ones held. Then the node lets them go.
func (d *Daemon) toStore(ctx context.Context, mc *mirrorCopy) error {
	m := mc.repo.Map()
	have := mc.store.Keys()
	n := 0
	for k, o := range m.Objects {
		if strings.HasPrefix(k, "locks/") {
			continue
		}
		if _, ok := have[k]; !ok {
			b, err := mc.repo.read(ctx, o)
			if err != nil {
				return fmt.Errorf("%s: %w", k, err)
			}
			if err := mc.store.Put(k, bytes.NewReader(b), int64(len(b)), nil); err != nil {
				return fmt.Errorf("%s: %w", k, err)
			}
			n++
		}
		if o.Deleted != nil {
			mc.store.Delete(k, true)
		}
	}
	if n > 0 {
		d.Log.Info("copied out of the storage node", "source", d.Cfg.Qualified(mc.source, mc.mesh), "objects", n)
	}
	return nil
}

// migrateCopies writes the copies kept in a node before ADR-014 into their
// stores, once, in the background.
func (d *Daemon) migrateCopies(ctx context.Context) {
	d.mu.Lock()
	copies := make([]*mirrorCopy, 0, len(d.mirrors))
	for _, mc := range d.mirrors {
		copies = append(copies, mc)
	}
	d.mu.Unlock()
	for _, mc := range copies {
		if len(mc.repo.Map().Objects) == 0 {
			continue
		}
		if err := d.nodeReady(ctx, mc.mesh); err != nil {
			continue
		}
		if err := d.toStore(ctx, mc); err != nil {
			d.Log.Warn("copying a copy out of the storage node", "source", d.Cfg.Qualified(mc.source, mc.mesh), "err", err)
		}
	}
}

func (d *Daemon) runNow(w http.ResponseWriter, r *http.Request) {
	if !d.owner(callerOf(r)) {
		fail(w, http.StatusForbidden, errors.New("owners only"))
		return
	}
	if d.source() == nil {
		fail(w, http.StatusBadRequest, errors.New("this machine backs nothing up"))
		return
	}
	go d.RunSource(context.WithoutCancel(r.Context()))
	writeJSON(w, http.StatusAccepted, map[string]bool{"started": true})
}

func (d *Daemon) getRuns(w http.ResponseWriter, r *http.Request) {
	d.mu.Lock()
	defer d.mu.Unlock()
	writeJSON(w, 200, map[string]any{"runs": d.runs})
}

// --- the source -----------------------------------------------------------------

// Start runs the mirrors' fetch loops, the source's schedule and the daily
// expiry of held objects, until ctx ends.
func (d *Daemon) Start(ctx context.Context) {
	d.mu.Lock()
	d.ctx = ctx
	for _, mc := range d.mirrors {
		go d.fetchLoop(ctx, mc)
	}
	source := d.Cfg.Source
	d.mu.Unlock()
	go d.expireLoop(ctx)
	if source != nil {
		go d.schedule(ctx)
		if d.src != nil {
			// Senders run all the time: a copy is caught up, and the old
			// staging moved, without waiting for a run.
			d.src.Sync(ctx, source.Mirrors)
			if d.repoMade() && d.HasKey() {
				go d.refreshKeys(ctx)
			}
		}
	}
	go d.migrateCopies(ctx)
}

func (d *Daemon) expireLoop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Hour):
		}
		d.mu.Lock()
		repos, stores := []*Repo{}, []*Store{}
		for _, mc := range d.mirrors {
			repos, stores = append(repos, mc.repo), append(stores, mc.store)
		}
		d.mu.Unlock()
		for _, st := range stores {
			if n, err := st.Expire(Hold); err != nil || n > 0 {
				d.Log.Info("held objects expired", "source", st.Name, "count", n, "err", err)
			}
		}
		for _, r := range repos {
			if n, err := r.Expire(ctx, Hold); err != nil || n > 0 {
				d.Log.Info("held objects expired", "source", r.Name, "count", n, "err", err)
			}
		}
	}
}

func (d *Daemon) schedule(ctx context.Context) {
	for {
		next := nextTime(time.Now(), d.source().Times)
		d.Log.Info("next backup", "at", next.Format(time.RFC3339))
		select {
		case <-ctx.Done():
			return
		case <-d.wake:
			continue
		case <-time.After(time.Until(next)):
		}
		d.RunSource(ctx)
	}
}

// nextTime is the first of times (HH:MM, local) after now.
func nextTime(now time.Time, times []string) time.Time {
	var best time.Time
	for _, t := range times {
		hm, _ := time.Parse("15:04", t)
		c := time.Date(now.Year(), now.Month(), now.Day(), hm.Hour(), hm.Minute(), 0, 0, now.Location())
		if !c.After(now) {
			c = c.AddDate(0, 0, 1)
		}
		if best.IsZero() || c.Before(best) {
			best = c
		}
	}
	return best
}

// RunSource is one backup: restic into this machine's repository, retention,
// every mirror brought in step, the staging copy emptied. It counts only when
// every mirror holds the run.
func (d *Daemon) RunSource(ctx context.Context) Run {
	d.mu.Lock()
	if d.running {
		d.mu.Unlock()
		return Run{Error: "already running"}
	}
	d.running = true
	d.prog = &Progress{Phase: "reading", Started: time.Now(), Mirrors: map[string]MirrorProgress{}}
	d.mu.Unlock()
	defer func() { d.mu.Lock(); d.running, d.prog = false, nil; d.mu.Unlock() }()

	run := Run{Started: time.Now(), Mirrors: map[string]string{}}
	err := d.runSource(ctx, &run)
	run.Finished, run.OK = time.Now(), err == nil
	if err != nil {
		run.Error = err.Error()
		d.Log.Warn("backup failed", "err", err)
	} else {
		d.Log.Info("backup done", "added", run.Added, "took", run.Finished.Sub(run.Started).Round(time.Second))
	}
	d.mu.Lock()
	d.runs = append(d.runs, run)
	if len(d.runs) > 60 {
		d.runs = d.runs[len(d.runs)-60:]
	}
	b, _ := json.Marshal(d.runs)
	d.mu.Unlock()
	os.WriteFile(filepath.Join(d.State, "runs.json"), b, 0o600)
	return run
}

func (d *Daemon) restic(ctx context.Context, args ...string) (string, error) {
	return d.resticWith(ctx, d.Key, args...)
}

func (d *Daemon) resticWith(ctx context.Context, key string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, d.Cfg.Restic, args...)
	cmd.Env = append(os.Environ(),
		"RESTIC_REPOSITORY=rest:"+d.localURL()+"/self/",
		"RESTIC_PASSWORD_FILE="+key,
		"RESTIC_CACHE_DIR="+filepath.Join(d.State, "restic-cache"),
		// Its progress every 2 s: without a terminal it says it once a minute.
		"RESTIC_PROGRESS_FPS=0.5",
		// 64 MB packs: a quarter of the objects to send and list (ADR-014).
		"RESTIC_PACK_SIZE=64")
	// Two writers into one buffer, on exec's two copying goroutines: locked.
	var out lockedBuffer
	sw := &statusWriter{out: &out, status: d.resticStatus}
	cmd.Stdout, cmd.Stderr = sw, &out
	err := cmd.Run()
	sw.Flush()
	if err != nil {
		tail := out.String()
		if len(tail) > 600 {
			tail = tail[len(tail)-600:]
		}
		return out.String(), fmt.Errorf("restic %s: %w: %s", args[0], err, strings.TrimSpace(tail))
	}
	return out.String(), nil
}

type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// statusWriter passes restic's --json output through, line by line, but for
// its progress lines, which go to status: a 10-hour run would otherwise keep
// every one of them.
type statusWriter struct {
	out    io.Writer
	status func(line []byte)
	part   []byte
}

func (w *statusWriter) Write(b []byte) (int, error) {
	w.part = append(w.part, b...)
	for {
		i := bytes.IndexByte(w.part, '\n')
		if i < 0 {
			break
		}
		w.line(w.part[:i+1])
		w.part = w.part[i+1:]
	}
	return len(b), nil
}

func (w *statusWriter) Flush() {
	if len(w.part) > 0 {
		w.line(w.part)
		w.part = nil
	}
}

func (w *statusWriter) line(l []byte) {
	if bytes.Contains(l, []byte(`"message_type":"status"`)) {
		w.status(l)
		return
	}
	w.out.Write(l)
}

// resticStatus takes restic's progress into the run's.
func (d *Daemon) resticStatus(line []byte) {
	var m struct {
		Files     int64 `json:"total_files"`
		FilesDone int64 `json:"files_done"`
		Bytes     int64 `json:"total_bytes"`
		BytesDone int64 `json:"bytes_done"`
		Remaining int64 `json:"seconds_remaining"`
	}
	if json.Unmarshal(line, &m) != nil {
		return
	}
	d.setProgress(func(p *Progress) {
		if p.Phase == "reading" {
			p.Files, p.FilesDone, p.Bytes, p.BytesDone, p.Remaining = m.Files, m.FilesDone, m.Bytes, m.BytesDone, m.Remaining
		}
	})
}

func (d *Daemon) runSource(ctx context.Context, run *Run) error {
	s := d.source()
	if d.src == nil {
		return errors.New("this source has no repository open")
	}
	if _, err := os.Stat(d.Key); err != nil {
		return fmt.Errorf("no key (%s): set it with the card, or `shrooms-backup key`", d.Key)
	}
	// The old staging is read through this machine's node until moved.
	if d.self != nil {
		if err := d.nodeReady(ctx, d.Cfg.SourceMesh()); err != nil {
			return err
		}
	}
	d.src.Sync(d.ctxOr(ctx), s.Mirrors)
	if !d.repoMade() {
		if _, err := d.restic(ctx, "init"); err != nil {
			return err
		}
		d.labelPending(ctx)
	}
	paths, exclude := s.Resolve()
	if len(paths) == 0 {
		return errors.New("none of this source's paths exist here")
	}
	// Scanning, which --no-scan skipped, is what gives restic a total, and
	// the apps a percentage; it reads metadata only, beside the backup.
	args := []string{"backup", "--json", "--host", d.Mesh.Self()}
	for _, e := range exclude {
		args = append(args, "--exclude", e)
	}
	out, err := d.restic(ctx, append(args, paths...)...)
	// restic exits 3 when some files could not be read, having saved the rest.
	if !summarize(out, run) {
		if err == nil {
			err = errors.New("restic did not say what it saved")
		}
		return err
	}
	// Retention: forget every run; prune, which reads packs, once a day.
	forget := []string{"forget", "--host", d.Mesh.Self(), "--keep-daily", strconv.Itoa(s.KeepDaily), "--keep-weekly", strconv.Itoa(s.KeepWeekly)}
	if d.firstToday(run.Started) {
		forget = append(forget, "--prune")
	}
	if _, err := d.restic(ctx, forget...); err != nil {
		return err
	}
	run.Stored = d.src.Size()
	if err := d.waitMirrors(ctx, s.Mirrors, run); err != nil {
		return err
	}
	d.refreshKeys(ctx)
	return nil
}

// ctxOr is the daemon's own context, which outlives a run: the senders
// keep going after it (ADR-014).
func (d *Daemon) ctxOr(ctx context.Context) context.Context {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.ctx != nil {
		return d.ctx
	}
	return ctx
}

// waitMirrors waits until every mirror holds the repository as it is, as
// long as each keeps moving (ADR-012), and says how far each is.
func (d *Daemon) waitMirrors(ctx context.Context, mirrors []string, run *Run) error {
	d.setProgress(func(p *Progress) { p.Phase = "mirrors" })
	total := d.src.Size()
	done := map[string]bool{}
	for {
		for _, m := range mirrors {
			if done[m] {
				continue
			}
			left, synced, why, moved := d.src.Pending(m)
			d.setProgress(func(p *Progress) {
				mp := MirrorProgress{Bytes: total, Done: max(total-left, 0), OK: synced, Error: why}
				if synced {
					mp.Done, mp.Error = total, ""
				}
				p.Mirrors[m] = mp
			})
			switch {
			case synced:
				done[m], run.Mirrors[m] = true, "ok"
			case time.Since(moved) > Stalled:
				done[m] = true
				run.Mirrors[m] = fmt.Sprintf("%s took nothing for %v (%s still to go)", m, Stalled, humanBytes(left))
				if why != "" {
					run.Mirrors[m] += ": " + why
				}
			}
		}
		if len(done) == len(mirrors) {
			break
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(pollEvery):
		}
	}
	var bad []string
	for m, st := range run.Mirrors {
		if st != "ok" {
			bad = append(bad, m)
		}
	}
	if len(bad) > 0 {
		slices.Sort(bad)
		return fmt.Errorf("not on every mirror: %s", strings.Join(bad, ", "))
	}
	return nil
}

func (d *Daemon) firstToday(now time.Time) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, r := range d.runs {
		if r.OK && r.Started.YearDay() == now.YearDay() && r.Started.Year() == now.Year() {
			return false
		}
	}
	return true
}

func (d *Daemon) localURL() string {
	if d.LocalURL != "" {
		return d.LocalURL
	}
	return "http://127.0.0.1:" + strconv.Itoa(Port+1)
}

func (d *Daemon) mirrorURL(mirror string) (string, error) {
	if d.PeerURL != nil {
		return d.PeerURL(mirror)
	}
	mesh := d.Cfg.SourceMesh()
	a, ok := d.Mesh.Find(mirror, mesh)
	if !ok {
		return "", fmt.Errorf("%s is not on the %s mesh, which this machine backs up over", mirror, mesh)
	}
	return "http://" + net.JoinHostPort(a.String(), strconv.Itoa(Port)), nil
}

func (d *Daemon) getJSON(ctx context.Context, url string, v any) error {
	req, _ := http.NewRequestWithContext(ctx, "GET", url, nil)
	resp, err := d.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("%s: %s", url, resp.Status)
	}
	return json.NewDecoder(resp.Body).Decode(v)
}
