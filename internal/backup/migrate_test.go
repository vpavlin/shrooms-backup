package backup

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
	"net/http/httptest"
	"net/netip"
	"os"
	osexec "os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// k11's case: a source whose backup is in the staging kept before ADR-014
// (a map, its objects in the node). The new daemon sends it to the mirror
// from there, without a run, and then empties the staging.
func TestTheOldStagingMovesToTheMirror(t *testing.T) {
	bin := restic(t)
	net_ := newNetwork()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// The old staging, written by restic through the old repository.
	state := t.TempDir()
	cache, _ := NewCache(t.TempDir(), 64<<20)
	old, err := OpenRepo("self", filepath.Join(state, "self"), net_.node("k11"), cache)
	if err != nil {
		t.Fatal(err)
	}
	oldSrv := httptest.NewServer(old.Handler(false))
	data := t.TempDir()
	for i := 0; i < 4; i++ {
		os.WriteFile(filepath.Join(data, "f"+string(rune('a'+i))), []byte(strings.Repeat(string(rune('a'+i)), 200000)), 0o644)
	}
	restic := func(repo string, args ...string) {
		t.Helper()
		cmd := osexec.Command(bin, args...)
		cmd.Env = append(os.Environ(), "RESTIC_REPOSITORY=rest:"+repo, "RESTIC_PASSWORD=x", "RESTIC_CACHE_DIR="+t.TempDir())
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("restic %v: %v\n%s", args, err, out)
		}
	}
	restic(oldSrv.URL+"/", "init")
	restic(oldSrv.URL+"/", "backup", data)
	oldSrv.Close()
	if net_.node("k11").count() == 0 {
		t.Fatal("the old staging holds nothing")
	}

	// The mirror, new.
	sMesh := &Mesh{}
	sMesh.Set("scribe", map[string]netip.Addr{"k11": netip.MustParseAddr("127.0.0.1")})
	sCache, _ := NewCache(t.TempDir(), 1<<20)
	s := &Daemon{Cfg: &Config{Mesh: "home", Mirror: true}, State: t.TempDir(), Log: slog.New(slog.DiscardHandler), Mesh: sMesh,
		Store: net_.node("scribe"), Cache: sCache}
	if err := s.Open(); err != nil {
		t.Fatal(err)
	}
	s.Start(ctx)
	sSrv := httptest.NewServer(s.Handler("home"))
	defer sSrv.Close()

	// The source, new, on the same state.
	kMesh := &Mesh{}
	kMesh.Set("k11", map[string]netip.Addr{"scribe": netip.MustParseAddr("127.0.0.1")})
	k := &Daemon{Cfg: &Config{Mesh: "home", Source: &SourceConfig{Paths: []string{data}, Mirrors: []string{"scribe"}, Times: []string{"03:00"}}},
		State: state, Log: slog.New(slog.DiscardHandler), Mesh: kMesh, Store: net_.node("k11"), Cache: cache,
		PeerURL: func(string) (string, error) { return sSrv.URL, nil }}
	if err := k.Open(); err != nil {
		t.Fatal(err)
	}
	if !k.repoMade() {
		t.Fatal("the catalog did not start from the old staging")
	}
	k.Start(ctx)

	deadline := time.Now().Add(30 * time.Second)
	for {
		if _, synced, _, _ := k.src.Pending("scribe"); synced {
			break
		}
		if time.Now().After(deadline) {
			_, _, why, _ := k.src.Pending("scribe")
			t.Fatalf("the mirror never caught up: %s", why)
		}
		time.Sleep(100 * time.Millisecond)
	}
	// The mirror's copy is the backup: plain restic checks and reads it.
	copyDir := filepath.Join(s.State, "mirrors", "home", "k11", "repo")
	cmd := osexec.Command(bin, "check", "--read-data", "-r", copyDir, "--no-cache")
	cmd.Env = append(os.Environ(), "RESTIC_PASSWORD=x")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("restic check on the mirror's copy: %v\n%s", err, out)
	}
	// And the second copy on the source is gone.
	for i := 0; i < 50 && net_.node("k11").count() > 0; i++ {
		time.Sleep(100 * time.Millisecond)
	}
	if n := net_.node("k11").count(); n != 0 {
		t.Errorf("the old staging still holds %d objects", n)
	}
	if _, err := os.Stat(filepath.Join(state, "self", "map.json")); err == nil {
		t.Error("the old staging's map is still in use")
	}
}

// A copy kept in a node before ADR-014 is written into its store, held
// deletions held.
func TestAnOldCopyMovesIntoItsStore(t *testing.T) {
	net_ := newNetwork()
	state := t.TempDir()
	cache, _ := NewCache(t.TempDir(), 1<<20)
	node := net_.node("scribe")
	old, _ := OpenRepo("laptop", filepath.Join(state, "mirrors", "home", "laptop"), node, cache)
	live := strings.Repeat("l", 1000)
	gone := strings.Repeat("g", 500)
	c1, _ := node.Put(context.Background(), "a", []byte(live))
	c2, _ := node.Put(context.Background(), "b", []byte(gone))
	dead := time.Now().Add(-time.Hour)
	liveKey, goneKey := "index/"+sha(live), "index/"+sha(gone)
	old.Adopt(Map{Version: 3, Objects: map[string]Object{
		liveKey: {CID: c1, Size: 1000},
		goneKey: {CID: c2, Size: 500, Deleted: &dead},
	}})
	d := &Daemon{Cfg: &Config{Mesh: "home", Mirror: true}, State: state, Log: slog.New(slog.DiscardHandler), Mesh: &Mesh{},
		Store: node, Cache: cache}
	if err := d.Open(); err != nil {
		t.Fatal(err)
	}
	d.migrateCopies(context.Background())
	st := d.mirrors[mirrorKey("home", "laptop")].store
	if n, ok := st.Has(liveKey); !ok || n != 1000 {
		t.Errorf("the live object: %d %v", n, ok)
	}
	if _, ok := st.Has(goneKey); ok {
		t.Error("a deleted object is live in the store")
	}
	if _, err := os.Stat(filepath.Join(st.dir, ".held", goneKey)); err != nil {
		t.Errorf("the deleted object is not held: %v", err)
	}
}

func sha(s string) string { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:]) }
