package backup

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	osexec "os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A source and a mirror, as the daemons run them: the source backs up,
// hands its map to the mirror, waits until the mirror holds all of it,
// empties its own staging copy; the source restores from the mirror; a
// device that is neither the source nor an owner cannot read it.
func TestASourceAndItsMirror(t *testing.T) {
	bin := restic(t)
	net := newNetwork()
	log := slog.New(slog.DiscardHandler)
	loopback := netip.MustParseAddr("127.0.0.1")

	// proteus, a mirror; the mesh calls whoever connects "laptop".
	pMesh := &Mesh{}
	pMesh.Set("proteus", map[string]netip.Addr{"laptop": loopback})
	pCache, _ := NewCache(t.TempDir(), 64<<20)
	p := &Daemon{Cfg: &Config{Mesh: "office", Mirror: true}, State: t.TempDir(), Log: log, Mesh: pMesh,
		Store: net.node("proteus"), Cache: pCache}
	if err := p.Open(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p.Start(ctx)
	pSrv := httptest.NewServer(p.Handler(false))
	defer pSrv.Close()

	// laptop, the source.
	data := t.TempDir()
	for i := 0; i < 10; i++ {
		os.WriteFile(filepath.Join(data, "f"+string(rune('a'+i))), []byte(strings.Repeat("x", 50000+i)), 0o644)
	}
	key := filepath.Join(t.TempDir(), "key")
	os.WriteFile(key, []byte("test"), 0o600)
	lMesh := &Mesh{}
	lMesh.Set("laptop", map[string]netip.Addr{"proteus": loopback})
	lCache, _ := NewCache(t.TempDir(), 64<<20)
	l := &Daemon{Cfg: &Config{Mesh: "office", Restic: bin, Source: &SourceConfig{Paths: []string{data}, Mirrors: []string{"proteus"},
		Times: []string{"03:00"}, KeepDaily: 14, KeepWeekly: 12}}, State: t.TempDir(), Key: key, Log: log, Mesh: lMesh,
		Store: net.node("laptop"), Cache: lCache,
		PeerURL: func(string) (string, error) { return pSrv.URL, nil }}
	if err := l.Open(); err != nil {
		t.Fatal(err)
	}
	lSrv := httptest.NewServer(l.Handler(true))
	defer lSrv.Close()
	l.LocalURL = lSrv.URL

	run := l.RunSource(ctx)
	if !run.OK || run.Mirrors["proteus"] != "ok" {
		t.Fatalf("run: %+v", run)
	}
	if run.Files != 10 || run.FilesNew != 10 || run.Bytes < 500000 || run.DataAdded == 0 || run.Stored == 0 {
		t.Errorf("run's numbers: %+v", run)
	}
	if n := net.node("laptop").count(); n != 0 {
		t.Errorf("the source kept %d objects after every mirror had them", n)
	}
	live := 0
	for _, o := range l.self.Map().Objects {
		if o.Deleted == nil {
			live++
			if !net.node("proteus").Has(ctx, o.CID, o.Size) {
				t.Errorf("%s not on the mirror", o.CID)
			}
		}
	}
	if live == 0 {
		t.Fatal("nothing was backed up")
	}

	// A second run, after a change: it reads the repository back from the
	// mirror (the staging copy is gone) and still works.
	os.WriteFile(filepath.Join(data, "fa"), []byte("changed"), 0o644)
	if run := l.RunSource(ctx); !run.OK {
		t.Fatalf("second run: %+v", run)
	}

	// The source restores its backup from the mirror's copy.
	to := t.TempDir()
	run2 := func(repo string, args ...string) (string, error) {
		cmd := osexec.Command(bin, args...)
		cmd.Env = append(os.Environ(), "RESTIC_REPOSITORY=rest:"+repo, "RESTIC_PASSWORD=test", "RESTIC_CACHE_DIR="+t.TempDir())
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	if out, err := run2(pSrv.URL+"/v1/repo/laptop/", "restore", "latest", "--target", to); err != nil {
		t.Fatalf("restore from the mirror: %v\n%s", err, out)
	}
	got := tree(t, filepath.Join(to, data))
	want := tree(t, data)
	if len(got) != len(want) {
		t.Fatalf("restored %d files, want %d", len(got), len(want))
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s differs", k)
		}
	}

	// Somebody else on the mesh: no.
	pMesh.Set("proteus", map[string]netip.Addr{"stranger": loopback})
	resp, err := http.Get(pSrv.URL + "/v1/repo/laptop/config")
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("a stranger read the backup: %s", resp.Status)
	}
}

func TestNextTime(t *testing.T) {
	now := time.Date(2026, 11, 1, 14, 0, 0, 0, time.Local)
	if got := nextTime(now, []string{"03:00", "15:00"}); got.Hour() != 15 || got.Day() != 1 {
		t.Errorf("got %v", got)
	}
	if got := nextTime(now, []string{"03:00"}); got.Hour() != 3 || got.Day() != 2 {
		t.Errorf("got %v", got)
	}
}

// Settings from the apps: owners only; times and owners apply at once; the
// mirror role restarts the daemon; a bad setting is refused and not saved.
func TestSettings(t *testing.T) {
	mesh := &Mesh{}
	mesh.Set("proteus", map[string]netip.Addr{"phone": netip.MustParseAddr("127.0.0.1")})
	cfgPath := filepath.Join(t.TempDir(), "config.json")
	restarted := make(chan bool, 1)
	cfg := &Config{Mesh: "office", Source: &SourceConfig{Presets: []string{"shrooms"}, Mirrors: []string{"scribe"}}}
	if err := cfg.fill(); err != nil {
		t.Fatal(err)
	}
	d := &Daemon{Cfg: cfg, State: t.TempDir(), Log: slog.New(slog.DiscardHandler), Mesh: mesh, Store: newNetwork().node("proteus"),
		ConfigPath: cfgPath, Restart: func() { restarted <- true }}
	if err := d.Open(); err != nil {
		t.Fatal(err)
	}
	mesh_ := httptest.NewServer(d.Handler(false))
	defer mesh_.Close()
	local := httptest.NewServer(d.Handler(true))
	defer local.Close()
	put := func(url, body string) int {
		req, _ := http.NewRequest("PUT", url+"/v1/config", strings.NewReader(body))
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	body := `{"owners":["phone"],"always_on":true,"mirror":false,"quota_gb":50,` +
		`"source":{"presets":["shrooms","keys"],"times":["04:30"],"mirrors":["scribe","vps"]}}`
	resp, err := http.Get(mesh_.URL + "/v1/peers")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(b), `"self":"proteus"`) || !strings.Contains(string(b), `"name":"phone"`) {
		t.Errorf("peers: %s", b)
	}
	if code := put(mesh_.URL, body); code != 403 {
		t.Errorf("a device that is not an owner changed the settings: %d", code)
	}
	if code := put(local.URL, body); code != 200 {
		t.Fatalf("this machine could not: %d", code)
	}
	if s := d.source(); s.Times[0] != "04:30" || len(s.Mirrors) != 2 || s.KeepDaily != 14 {
		t.Errorf("not applied: %+v", s)
	}
	saved, err := LoadConfig(cfgPath)
	if err != nil || !saved.AlwaysOn || saved.Mesh != "office" || len(saved.Source.Presets) != 2 {
		t.Errorf("saved: %+v %v", saved, err)
	}
	if code := put(mesh_.URL, strings.Replace(body, `"mirror":false`, `"mirror":true`, 1)); code != 202 {
		t.Errorf("an owner made it a mirror: %d", code)
	}
	select {
	case <-restarted:
	case <-time.After(5 * time.Second):
		t.Error("becoming a mirror did not restart the daemon")
	}
	if code := put(local.URL, strings.Replace(body, `"04:30"`, `"25:00"`, 1)); code != 400 {
		t.Errorf("a bad time: %d", code)
	}
	if saved, _ := LoadConfig(cfgPath); !saved.Mirror {
		t.Error("a refused setting overwrote the saved ones")
	}
}
