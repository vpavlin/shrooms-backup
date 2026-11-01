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
