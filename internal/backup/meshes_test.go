package backup

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// A machine on more than one mesh (ADR-011): a node, a store and the API on
// each, names qualified by mesh, owners on the primary unless named with one.

func TestConfigMeshes(t *testing.T) {
	c := &Config{Mesh: "office"}
	if err := c.fill(); err != nil || !slices.Equal(c.Meshes, []string{"office"}) || c.Primary() != "office" {
		t.Fatalf("a config written before meshes: %v %v", c.Meshes, err)
	}
	c = &Config{Meshes: []string{"office", "home"}, Owners: []string{"laptop", "phone@home"},
		Source: &SourceConfig{Presets: []string{"shrooms"}, Mirrors: []string{"scribe"}, Mesh: "home"}}
	if err := c.fill(); err != nil {
		t.Fatal(err)
	}
	if c.Mesh != "office" || c.SourceMesh() != "home" {
		t.Errorf("primary %q, source mesh %q", c.Mesh, c.SourceMesh())
	}
	if p, a := c.Ports("office"); p != 8590 || a != 8591 {
		t.Errorf("the primary's ports moved: %d %d", p, a)
	}
	if p, a := c.Ports("home"); p != 8592 || a != 8593 {
		t.Errorf("home's ports: %d %d", p, a)
	}
	if c.Qualified("k11", "home") != "k11@home" || c.Qualified("laptop", "office") != "laptop" {
		t.Error("qualified names")
	}
	for _, bad := range []*Config{
		{Meshes: []string{"office", "office"}},
		{Meshes: []string{"office"}, Owners: []string{"phone@home"}},
		{Meshes: []string{"office"}, Source: &SourceConfig{Presets: []string{"shrooms"}, Mirrors: []string{"x"}, Mesh: "home"}},
		{},
	} {
		if bad.fill() == nil {
			t.Errorf("accepted %+v", bad)
		}
	}
}

// from is an HTTP client calling from one loopback address, which the mesh
// says is a device on a mesh.
func from(addr string) *http.Client {
	d := &net.Dialer{LocalAddr: &net.TCPAddr{IP: net.ParseIP(addr)}}
	return &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{DialContext: d.DialContext}}
}

func call(t *testing.T, c *http.Client, method, url string, body any) (int, string) {
	t.Helper()
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, url, r)
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func TestAMirrorOnTwoMeshes(t *testing.T) {
	net_ := newNetwork()
	mesh := &Mesh{}
	mesh.SetPeers("scribe", []Peer{
		{Name: "nothing", Mesh: "office", Overlay: "127.0.0.2"},
		{Name: "nothing", Mesh: "home", Overlay: "127.0.0.3"},
		{Name: "laptop", Mesh: "office", Overlay: "127.0.0.4"},
		{Name: "laptop", Mesh: "home", Overlay: "127.0.0.5"},
		{Name: "k11", Mesh: "home", Overlay: "127.0.0.6"},
	})
	cfg := &Config{Meshes: []string{"office", "home"}, Mirror: true, Owners: []string{"laptop"}}
	if err := cfg.fill(); err != nil {
		t.Fatal(err)
	}
	cache, _ := NewCache(t.TempDir(), 64<<20)
	d := &Daemon{Cfg: cfg, State: t.TempDir(), Log: slog.New(slog.DiscardHandler), Mesh: mesh, Cache: cache,
		Stores: map[string]Blobs{"office": net_.node("scribe-office"), "home": net_.node("scribe-home")}}
	if err := d.Open(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	d.Start(ctx)
	office := httptest.NewServer(d.Handler("office"))
	defer office.Close()
	home := httptest.NewServer(d.Handler("home"))
	defer home.Close()

	// Two devices called "nothing", one on each mesh, each with its backup.
	send := func(srv *httptest.Server, addr, source, store, content string) string {
		t.Helper()
		cid, _ := net_.node(store).Put(ctx, "data/x", []byte(content))
		m := Map{Version: 1, Objects: map[string]Object{"data/x": {CID: cid, Size: int64(len(content))}}}
		if code, b := call(t, from(addr), "PUT", srv.URL+"/v1/mirror/"+source, map[string]any{"map": m}); code != 202 {
			t.Fatalf("%s at %s: %d %s", source, srv.URL, code, b)
		}
		return cid
	}
	cOffice := send(office, "127.0.0.2", "nothing", "nothing-office", "the office one")
	cHome := send(home, "127.0.0.3", "nothing", "nothing-home", "the home one")
	waitAdopted := func(addr, url string) {
		t.Helper()
		for i := 0; i < 100; i++ {
			if code, b := call(t, from(addr), "GET", url, nil); code == 200 && strings.Contains(b, `"adopted":1`) {
				return
			}
			time.Sleep(50 * time.Millisecond)
		}
		t.Fatalf("%s never adopted", url)
	}
	// Each asks for its own copy by its own name, on the mesh it is on.
	waitAdopted("127.0.0.2", office.URL+"/v1/mirror/nothing")
	waitAdopted("127.0.0.3", home.URL+"/v1/mirror/nothing")
	if !net_.node("scribe-office").Has(ctx, cOffice, 14) || !net_.node("scribe-home").Has(ctx, cHome, 12) {
		t.Error("a copy is not in its own mesh's store")
	}
	in := d.Info(ctx, "office")
	if !slices.Equal(in.Holds, []string{"nothing", "nothing@home"}) || !slices.Equal(in.Meshes, []string{"office", "home"}) {
		t.Errorf("holds %v, meshes %v", in.Holds, in.Meshes)
	}
	if _, err := os.Stat(filepath.Join(d.State, "mirrors", "home", "nothing", "map.json")); err != nil {
		t.Errorf("the home copy is not kept under its mesh: %v", err)
	}

	// The owner, "laptop" bare, is the office's laptop: it reads both copies.
	if code, _ := call(t, from("127.0.0.4"), "GET", office.URL+"/v1/mirror/nothing@home", nil); code != 200 {
		t.Errorf("the owner could not read nothing@home: %d", code)
	}
	// A laptop on home is someone else's on a friends' mesh: not an owner,
	// and not the office nothing either.
	if code, _ := call(t, from("127.0.0.5"), "GET", home.URL+"/v1/mirror/nothing@home", nil); code != 403 {
		t.Errorf("a laptop on home read nothing@home: %d", code)
	}
	if code, _ := call(t, from("127.0.0.3"), "GET", home.URL+"/v1/mirror/nothing@office", nil); code != 403 {
		t.Errorf("home's nothing read the office's: %d", code)
	}
	// Named with its mesh, it is one.
	d.Cfg.Owners = []string{"laptop", "laptop@home"}
	if code, _ := call(t, from("127.0.0.5"), "GET", home.URL+"/v1/mirror/nothing@home", nil); code != 200 {
		t.Errorf("laptop@home as an owner: %d", code)
	}

	// An address on home, at the office's listener, is no member there.
	if code, _ := call(t, from("127.0.0.6"), "GET", office.URL+"/v1/info", nil); code != 403 {
		t.Errorf("k11 (home) at the office address: %d", code)
	}

	// The machine's limit is over every mesh: a copy that would take the two
	// together over it is refused before anything is fetched.
	d.QuotaBytes = 40
	big := Map{Version: 1, Objects: map[string]Object{"data/y": {CID: "zbig", Size: 20}}}
	if code, b := call(t, from("127.0.0.6"), "PUT", home.URL+"/v1/mirror/k11", map[string]any{"map": big}); code != 507 {
		t.Errorf("over the limit: %d %s", code, b)
	}
	d.QuotaBytes = 100
	if code, b := call(t, from("127.0.0.6"), "PUT", home.URL+"/v1/mirror/k11", map[string]any{"map": big}); code != 202 {
		t.Errorf("within the limit: %d %s", code, b)
	}
}

// What was kept before there was more than one mesh is the primary's.
func TestOneMeshStateMovesUnderThePrimary(t *testing.T) {
	state := t.TempDir()
	old := filepath.Join(state, "mirrors", "laptop")
	os.MkdirAll(old, 0o700)
	os.WriteFile(filepath.Join(old, "map.json"), []byte(`{"version":3,"objects":{}}`), 0o600)
	// A source named like the primary mesh, in the old layout.
	os.MkdirAll(filepath.Join(state, "mirrors", "office"), 0o700)
	os.WriteFile(filepath.Join(state, "mirrors", "office", "map.json"), []byte(`{"version":1,"objects":{}}`), 0o600)
	os.WriteFile(filepath.Join(state, "sprs.json"), []byte(`{"laptop":"spr:abc"}`), 0o600)

	cfg := &Config{Meshes: []string{"office", "home"}, Mirror: true}
	cfg.fill()
	cache, _ := NewCache(t.TempDir(), 1<<20)
	d := &Daemon{Cfg: cfg, State: state, Log: slog.New(slog.DiscardHandler), Mesh: &Mesh{}, Cache: cache,
		Stores: map[string]Blobs{"office": newNetwork().node("o"), "home": newNetwork().node("h")}}
	if err := d.Open(); err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{"laptop", "office"} {
		if _, err := os.Stat(filepath.Join(state, "mirrors", "office", s, "map.json")); err != nil {
			t.Errorf("%s's copy did not move under the primary: %v", s, err)
		}
	}
	if in := d.Info(context.Background(), "office"); !slices.Equal(in.Holds, []string{"laptop", "office"}) {
		t.Errorf("holds after the move: %v", in.Holds)
	}
	if got := d.KnownSPRs("office"); !slices.Equal(got, []string{"spr:abc"}) {
		t.Errorf("the primary's SPRs: %v", got)
	}
	if got := d.KnownSPRs("home"); len(got) != 0 {
		t.Errorf("home knows nodes it never met: %v", got)
	}
	// Opening again changes nothing.
	d2 := &Daemon{Cfg: cfg, State: state, Log: slog.New(slog.DiscardHandler), Mesh: &Mesh{}, Cache: cache, Stores: d.Stores}
	if err := d2.Open(); err != nil {
		t.Fatal(err)
	}
	if in := d2.Info(context.Background(), "office"); !slices.Equal(in.Holds, []string{"laptop", "office"}) {
		t.Errorf("holds on a second open: %v", in.Holds)
	}
}

// A source that backs up over its second mesh: its staging is that mesh's
// store, its mirror is found there, and the copy is kept as name@mesh.
func TestASourceOnItsSecondMesh(t *testing.T) {
	bin := restic(t)
	net_ := newNetwork()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sMesh := &Mesh{}
	sMesh.SetPeers("scribe", []Peer{{Name: "k11", Mesh: "home", Overlay: "127.0.0.1"}})
	sCfg := &Config{Meshes: []string{"office", "home"}, Mirror: true}
	sCfg.fill()
	sCache, _ := NewCache(t.TempDir(), 64<<20)
	s := &Daemon{Cfg: sCfg, State: t.TempDir(), Log: slog.New(slog.DiscardHandler), Mesh: sMesh, Cache: sCache,
		Stores: map[string]Blobs{"office": net_.node("scribe-office"), "home": net_.node("scribe-home")}}
	if err := s.Open(); err != nil {
		t.Fatal(err)
	}
	s.Start(ctx)
	sHome := httptest.NewServer(s.Handler("home"))
	defer sHome.Close()

	data := t.TempDir()
	os.WriteFile(filepath.Join(data, "f"), []byte(strings.Repeat("y", 40000)), 0o644)
	key := filepath.Join(t.TempDir(), "key")
	os.WriteFile(key, []byte("test"), 0o600)
	kMesh := &Mesh{}
	kMesh.SetPeers("k11", []Peer{{Name: "scribe", Mesh: "home", Overlay: "127.0.0.9"}})
	kCfg := &Config{Meshes: []string{"lan", "home"}, Restic: bin,
		Source: &SourceConfig{Paths: []string{data}, Mirrors: []string{"scribe"}, Mesh: "home"}}
	if err := kCfg.fill(); err != nil {
		t.Fatal(err)
	}
	kCache, _ := NewCache(t.TempDir(), 64<<20)
	k := &Daemon{Cfg: kCfg, State: t.TempDir(), Key: key, Log: slog.New(slog.DiscardHandler), Mesh: kMesh, Cache: kCache,
		Stores: map[string]Blobs{"lan": net_.node("k11-lan"), "home": net_.node("k11-home")}}
	if err := k.Open(); err != nil {
		t.Fatal(err)
	}
	// The mirror is found on home, and only there.
	if _, err := k.mirrorURL("scribe"); err != nil {
		t.Fatalf("scribe on home: %v", err)
	}
	k.PeerURL = func(string) (string, error) { return sHome.URL, nil }
	kLocal := httptest.NewServer(k.Handler(""))
	defer kLocal.Close()
	k.LocalURL = kLocal.URL

	if run := k.RunSource(ctx); !run.OK {
		t.Fatalf("run: %+v", run)
	}
	if net_.node("k11-lan").count() != 0 {
		t.Error("the source staged on a mesh it does not back up over")
	}
	in := s.Info(ctx, "home")
	if !slices.Equal(in.Holds, []string{"k11@home"}) || in.Held[0].Mesh != "home" {
		t.Errorf("the mirror holds %v", in.Held)
	}
	// Kept under its mesh, as a plain restic repository (ADR-014).
	if fi, err := os.Stat(filepath.Join(s.State, "mirrors", "home", "k11", "repo", "config")); err != nil || fi.Size() == 0 {
		t.Errorf("the copy is not under home: %v", err)
	}
	if _, err := os.Stat(filepath.Join(s.State, "mirrors", "office", "k11")); err == nil {
		t.Error("a copy under office too")
	}
}
