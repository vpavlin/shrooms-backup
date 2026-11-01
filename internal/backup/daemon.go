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
	// Mesh is the one the storage node and the API listen on.
	Mesh string `json:"mesh"`
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
	if c.Mesh == "" {
		return errors.New("which mesh do backups run on (\"mesh\")?")
	}
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
		if !deviceName.MatchString(o) {
			return fmt.Errorf("owner %q is not a device name", o)
		}
	}
	if s := c.Source; s != nil {
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
	Node  *Node
	Store Blobs
	Cache *Cache
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
	self    *Repo
	mirrors map[string]*mirrorCopy
	sprs    map[string]string // device -> its node's SPR
	runs    []Run
	running bool
	wake    chan struct{} // the schedule, when the times change
	client  *http.Client
	ctx     context.Context // set by Start: a copy made after it gets its loop then
}

type mirrorCopy struct {
	repo    *Repo
	pending int64 // the newest map version asked for
	missing int
	err     string
	newest  chan Map
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
	d.mirrors, d.sprs = map[string]*mirrorCopy{}, map[string]string{}
	d.wake = make(chan struct{}, 1)
	d.client = &http.Client{Timeout: 30 * time.Second}
	if b, err := os.ReadFile(filepath.Join(d.State, "sprs.json")); err == nil {
		json.Unmarshal(b, &d.sprs)
	}
	if b, err := os.ReadFile(filepath.Join(d.State, "runs.json")); err == nil {
		json.Unmarshal(b, &d.runs)
	}
	var err error
	if d.Cfg.Source != nil {
		if d.self, err = OpenRepo("self", filepath.Join(d.State, "self"), d.Store, d.Cache); err != nil {
			return err
		}
	}
	entries, _ := os.ReadDir(filepath.Join(d.State, "mirrors"))
	for _, e := range entries {
		if e.IsDir() {
			if _, err := d.mirrorOf(e.Name()); err != nil {
				return err
			}
		}
	}
	return nil
}

var deviceName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

func (d *Daemon) mirrorOf(source string) (*mirrorCopy, error) {
	if !deviceName.MatchString(source) {
		return nil, fmt.Errorf("%q is not a device name", source)
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if c, ok := d.mirrors[source]; ok {
		return c, nil
	}
	r, err := OpenRepo(source, filepath.Join(d.State, "mirrors", source), d.Store, d.Cache)
	if err != nil {
		return nil, err
	}
	c := &mirrorCopy{repo: r, pending: r.Map().Version, newest: make(chan Map, 1)}
	d.mirrors[source] = c
	if d.ctx != nil {
		go d.fetchLoop(d.ctx, source, c)
	}
	return c, nil
}

// KnownSPRs are the other nodes this one knew, to bootstrap from at start.
func (d *Daemon) KnownSPRs() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]string, 0, len(d.sprs))
	for _, s := range d.sprs {
		out = append(out, s)
	}
	return out
}

func (d *Daemon) saveSPRs() {
	b, _ := json.Marshal(d.sprs)
	os.WriteFile(filepath.Join(d.State, "sprs.json"), b, 0o600)
}

// learnSPR remembers another node and bootstraps from every one known.
// nodeReady waits for this machine's node, which restarts when it learns a
// peer.
func (d *Daemon) nodeReady(ctx context.Context) error {
	if d.Node == nil {
		return nil
	}
	return d.Node.Ready(ctx, 2*time.Minute)
}

func (d *Daemon) learnSPR(device, spr string) {
	if !strings.HasPrefix(spr, "spr:") {
		return
	}
	d.mu.Lock()
	changed := d.sprs[device] != spr
	d.sprs[device] = spr
	all := make([]string, 0, len(d.sprs))
	for _, s := range d.sprs {
		all = append(all, s)
	}
	if changed {
		d.saveSPRs()
	}
	d.mu.Unlock()
	if changed && d.Node != nil {
		d.Node.Bootstrap(all)
	}
}

// --- the API --------------------------------------------------------------------

type ctxKey struct{}

type caller struct {
	name  string
	local bool
}

// Handler is the API: on the mesh, callers named by the mesh; on loopback,
// this machine, with restic's own repository at /self/.
func (d *Daemon) Handler(local bool) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/info", d.info)
	mux.HandleFunc("PUT /v1/mirror/{source}", d.putMirror)
	mux.HandleFunc("GET /v1/mirror/{source}", d.getMirror)
	mux.HandleFunc("POST /v1/run", d.runNow)
	mux.HandleFunc("GET /v1/runs", d.getRuns)
	mux.HandleFunc("PUT /v1/config", d.putConfig)
	// The phone app knows one machine; this is how it finds the others.
	mux.HandleFunc("GET /v1/peers", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{"self": d.Mesh.Self(), "peers": d.Mesh.Peers()})
	})
	mux.HandleFunc("GET /v1/presets", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, Presets) })
	mux.HandleFunc("/v1/repo/{source}/", d.repo)
	if local && d.self != nil {
		mux.Handle("/self/", http.StripPrefix("/self", d.self.Handler(false)))
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c := caller{local: local}
		if local {
			c.name = d.Mesh.Self()
		} else if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
			if a, err := netip.ParseAddr(host); err == nil {
				c.name = d.Mesh.Who(a)
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

func (d *Daemon) owner(c caller) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return c.local || slices.Contains(d.Cfg.Owners, c.name)
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
}

func (d *Daemon) Info(ctx context.Context) Info {
	in := Info{Name: d.Mesh.Self(), Holds: []string{}, Held: []MirrorStatus{}}
	if k, err := diskOf(d.State); err == nil {
		in.Disk = &k
	}
	if st, ok := d.Store.(*Storage); ok {
		in.SPR, _ = st.SPR(ctx)
		if sp, err := st.Space(ctx); err == nil {
			in.Quota, in.Used = sp.QuotaMax, sp.QuotaUsed
		}
	}
	d.mu.Lock()
	in.Mirror, in.AlwaysOn, in.QuotaGB, in.Source = d.Cfg.Mirror, d.Cfg.AlwaysOn, d.Cfg.Node.QuotaGB, d.Cfg.Source
	in.Owners = append([]string{}, d.Cfg.Owners...)
	in.Running = d.running
	if in.Source != nil {
		next := nextTime(time.Now(), in.Source.Times)
		in.Next = &next
	}
	names := []string{}
	for s := range d.mirrors {
		names = append(names, s)
	}
	if n := len(d.runs); n > 0 {
		last := d.runs[n-1]
		in.LastRun = &last
	}
	d.mu.Unlock()
	slices.Sort(names)
	for _, s := range names {
		d.mu.Lock()
		mc := d.mirrors[s]
		d.mu.Unlock()
		in.Holds = append(in.Holds, s)
		in.Held = append(in.Held, d.mirrorStatus(s, mc))
	}
	return in
}

func (d *Daemon) info(w http.ResponseWriter, r *http.Request) {
	in := d.Info(r.Context())
	in.Owner = d.owner(callerOf(r))
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
	if c.name != source {
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
	mc, err := d.mirrorOf(source)
	if err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	d.learnSPR(source, body.SPR)
	d.mu.Lock()
	if body.Map.Version > mc.pending {
		mc.pending = body.Map.Version
	}
	d.mu.Unlock()
	select {
	case <-mc.newest:
	default:
	}
	mc.newest <- body.Map
	writeJSON(w, http.StatusAccepted, d.mirrorStatus(source, mc))
}

// MirrorStatus is how far a mirror is with a source's copy.
type MirrorStatus struct {
	Source  string `json:"source"`
	Adopted int64  `json:"adopted"` // the newest map it holds in full
	Pending int64  `json:"pending"`
	Missing int    `json:"missing"`
	Error   string `json:"error,omitempty"`
	Bytes   int64  `json:"bytes"` // the adopted copy's size, held deletions included
}

func (d *Daemon) mirrorStatus(source string, mc *mirrorCopy) MirrorStatus {
	m := mc.repo.Map()
	d.mu.Lock()
	defer d.mu.Unlock()
	return MirrorStatus{Source: source, Adopted: m.Version, Pending: mc.pending, Missing: mc.missing, Error: mc.err, Bytes: m.Size(true)}
}

func (d *Daemon) getMirror(w http.ResponseWriter, r *http.Request) {
	c, source := callerOf(r), r.PathValue("source")
	if c.name != source && !d.owner(c) {
		fail(w, http.StatusForbidden, errors.New("not yours"))
		return
	}
	d.mu.Lock()
	mc, ok := d.mirrors[source]
	d.mu.Unlock()
	if !ok {
		fail(w, http.StatusNotFound, fmt.Errorf("no copy of %s here", source))
		return
	}
	writeJSON(w, 200, d.mirrorStatus(source, mc))
}

// fetchLoop keeps one source's copy in step: for each newest map, fetch
// every live object this node lacks, then adopt the map.
func (d *Daemon) fetchLoop(ctx context.Context, source string, mc *mirrorCopy) {
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
			mc.err, mc.missing = "", 0
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
	if err := d.nodeReady(ctx); err != nil {
		return err
	}
	missing := map[string]int64{}
	for cid, size := range want {
		if !d.Store.Has(ctx, cid, size) {
			missing[cid] = size
			if err := d.Store.Fetch(ctx, cid); err != nil {
				return err
			}
		}
	}
	deadline := time.Now().Add(6 * time.Hour)
	for len(missing) > 0 {
		d.mu.Lock()
		mc.missing = len(missing)
		d.mu.Unlock()
		if time.Now().After(deadline) {
			return fmt.Errorf("%d objects still missing after 6 h", len(missing))
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
		for cid, size := range missing {
			if d.Store.Has(ctx, cid, size) {
				delete(missing, cid)
			}
		}
	}
	return nil
}

// repo serves a repository to restic, read-only: a mirror's copy of a
// source, for the source itself or an owner restoring it.
func (d *Daemon) repo(w http.ResponseWriter, r *http.Request) {
	c, source := callerOf(r), r.PathValue("source")
	if c.name != source && !d.owner(c) {
		fail(w, http.StatusForbidden, errors.New("only the source or an owner may read its backup"))
		return
	}
	d.mu.Lock()
	mc, ok := d.mirrors[source]
	d.mu.Unlock()
	if !ok {
		fail(w, http.StatusNotFound, fmt.Errorf("no copy of %s here", source))
		return
	}
	http.StripPrefix("/v1/repo/"+source, mc.repo.Handler(true)).ServeHTTP(w, r)
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
	for s, mc := range d.mirrors {
		go d.fetchLoop(ctx, s, mc)
	}
	source := d.Cfg.Source
	d.mu.Unlock()
	go d.expireLoop(ctx)
	if source != nil {
		go d.schedule(ctx)
	}
}

func (d *Daemon) expireLoop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Hour):
		}
		d.mu.Lock()
		repos := []*Repo{}
		for _, mc := range d.mirrors {
			repos = append(repos, mc.repo)
		}
		d.mu.Unlock()
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
	d.mu.Unlock()
	defer func() { d.mu.Lock(); d.running = false; d.mu.Unlock() }()

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
	cmd := exec.CommandContext(ctx, d.Cfg.Restic, args...)
	cmd.Env = append(os.Environ(),
		"RESTIC_REPOSITORY=rest:"+d.localURL()+"/self/",
		"RESTIC_PASSWORD_FILE="+d.Key,
		"RESTIC_CACHE_DIR="+filepath.Join(d.State, "restic-cache"))
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	if err != nil {
		tail := out.String()
		if len(tail) > 600 {
			tail = tail[len(tail)-600:]
		}
		return out.String(), fmt.Errorf("restic %s: %w: %s", args[0], err, strings.TrimSpace(tail))
	}
	return out.String(), nil
}

func (d *Daemon) runSource(ctx context.Context, run *Run) error {
	s := d.source()
	if _, err := os.Stat(d.Key); err != nil {
		return fmt.Errorf("no key (%s): set it with the card, or `shrooms-backup key`", d.Key)
	}
	// The mirrors' nodes first: learning one restarts ours, which must not
	// happen while the mirrors fetch from it.
	for _, mirror := range s.Mirrors {
		if base, err := d.mirrorURL(mirror); err == nil {
			var in Info
			if d.getJSON(ctx, base+"/v1/info", &in) == nil {
				d.learnSPR(mirror, in.SPR)
			}
		}
	}
	if err := d.nodeReady(ctx); err != nil {
		return err
	}
	if _, ok := d.self.Map().Objects["config"]; !ok {
		if _, err := d.restic(ctx, "init"); err != nil {
			return err
		}
	}
	paths, exclude := s.Resolve()
	if len(paths) == 0 {
		return errors.New("none of this source's paths exist here")
	}
	args := []string{"backup", "--json", "--no-scan", "--host", d.Mesh.Self()}
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
	m := d.self.Map()
	run.Stored = m.Size(false)
	if err := d.pushAll(ctx, m, run); err != nil {
		return err
	}
	if !s.KeepLocal {
		d.unstage(ctx, m)
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

// pushAll hands the map to every mirror and waits until each holds it.
func (d *Daemon) pushAll(ctx context.Context, m Map, run *Run) error {
	spr := ""
	if st, ok := d.Store.(*Storage); ok {
		spr, _ = st.SPR(ctx)
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	for _, mirror := range d.source().Mirrors {
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := d.push(ctx, mirror, spr, m)
			mu.Lock()
			if err != nil {
				run.Mirrors[mirror] = err.Error()
			} else {
				run.Mirrors[mirror] = "ok"
			}
			mu.Unlock()
		}()
	}
	wg.Wait()
	var bad []string
	for m, s := range run.Mirrors {
		if s != "ok" {
			bad = append(bad, m)
		}
	}
	if len(bad) > 0 {
		slices.Sort(bad)
		return fmt.Errorf("not on every mirror: %s", strings.Join(bad, ", "))
	}
	return nil
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
	a, ok := d.Mesh.Find(mirror, d.Cfg.Mesh)
	if !ok {
		return "", fmt.Errorf("%s is not on the mesh", mirror)
	}
	return "http://" + net.JoinHostPort(a.String(), strconv.Itoa(Port)), nil
}

func (d *Daemon) push(ctx context.Context, mirror, spr string, m Map) error {
	base, err := d.mirrorURL(mirror)
	if err != nil {
		return err
	}
	// Its node first, so ours can reach it (and it ours, from our SPR).
	var in Info
	if err := d.getJSON(ctx, base+"/v1/info", &in); err != nil {
		return err
	}
	d.learnSPR(mirror, in.SPR)
	body, _ := json.Marshal(map[string]any{"spr": spr, "map": m})
	req, _ := http.NewRequestWithContext(ctx, "PUT", base+"/v1/mirror/"+d.Mesh.Self(), bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := d.client.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("%s refused: %s", mirror, resp.Status)
	}
	deadline := time.Now().Add(6 * time.Hour)
	for time.Now().Before(deadline) {
		var st MirrorStatus
		if err := d.getJSON(ctx, base+"/v1/mirror/"+d.Mesh.Self(), &st); err == nil {
			if st.Adopted >= m.Version {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(5 * time.Second):
		}
	}
	return errors.New("not in step after 6 h")
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

// unstage empties this machine's node of the run once every mirror holds
// it: the source keeps no second copy (ADR-001). The map stays; reads go to
// the mirrors.
func (d *Daemon) unstage(ctx context.Context, m Map) {
	n := 0
	for _, o := range m.Objects {
		if err := d.Store.Delete(ctx, o.CID); err == nil {
			n++
		}
	}
	d.Cache.Clear()
	d.Log.Info("staging emptied", "objects", n)
}
