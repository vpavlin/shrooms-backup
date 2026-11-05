package backup

import (
	"errors"
	"net/http/httptest"
	"os"
	osexec "os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Plain restic against a Store over HTTP: init, two backups, forget with
// prune, restore. What it deletes is held, and the size kept counts it.
func TestRealResticOnAStore(t *testing.T) {
	bin := restic(t)
	s, err := OpenStore("k11", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	var limit int64 = 1 << 40
	srv := httptest.NewServer(s.Handler(false, true, func(n int64) error {
		if s.Size()+n > limit {
			return ErrNoRoom
		}
		return nil
	}))
	defer srv.Close()
	run := func(args ...string) string {
		t.Helper()
		cmd := osexec.Command(bin, args...)
		cmd.Env = append(os.Environ(), "RESTIC_REPOSITORY=rest:"+srv.URL+"/", "RESTIC_PASSWORD=x", "RESTIC_CACHE_DIR="+t.TempDir())
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("restic %v: %v\n%s", args, err, out)
		}
		return string(out)
	}
	data := t.TempDir()
	for i := 0; i < 5; i++ {
		os.WriteFile(filepath.Join(data, "f"+string(rune('a'+i))), []byte(strings.Repeat(string(rune('a'+i)), 300000)), 0o644)
	}
	run("init")
	run("backup", data)
	os.WriteFile(filepath.Join(data, "fa"), []byte("changed"), 0o644)
	run("backup", data)
	before := s.Size()
	run("forget", "--keep-last", "1", "--prune")
	if s.Size() != before && s.Size() < before-100 {
		t.Errorf("a prune shrank the store from %d to %d: deletions are held", before, s.Size())
	}
	held, _ := filepath.Glob(filepath.Join(s.dir, ".held", "*", "*"))
	if len(held) == 0 {
		t.Error("nothing was held after forget --prune")
	}
	to := t.TempDir()
	run("restore", "latest", "--target", to)
	got, _ := os.ReadFile(filepath.Join(to, data, "fa"))
	if string(got) != "changed" {
		t.Errorf("restored %q", got)
	}
	run("check")

	// The copy is a plain restic repository: restic reads the directory.
	cmd := osexec.Command(bin, "snapshots", "-r", s.dir, "--no-cache")
	cmd.Env = append(os.Environ(), "RESTIC_PASSWORD=x")
	if out, err := cmd.CombinedOutput(); err != nil || !strings.Contains(string(out), "1 snapshots") {
		t.Errorf("plain restic on the directory: %v\n%s", err, out)
	}

	// Held objects go after the hold; the size follows.
	n, err := s.Expire(-time.Second)
	if err != nil || n == 0 {
		t.Errorf("expired %d: %v", n, err)
	}
	var total int64
	filepath.WalkDir(s.dir, func(p string, e os.DirEntry, err error) error {
		if err == nil && !e.IsDir() {
			fi, _ := e.Info()
			total += fi.Size()
		}
		return nil
	})
	if total != s.Size() {
		t.Errorf("size kept %d, on disk %d", s.Size(), total)
	}

	// Past the limit, a write is refused.
	limit = s.Size()
	if err := s.Put("data/"+strings.Repeat("0", 64), strings.NewReader("x"), 1, func(n int64) error {
		if s.Size()+n > limit {
			return ErrNoRoom
		}
		return nil
	}); !errors.Is(err, ErrNoRoom) {
		t.Errorf("over the limit: %v", err)
	}
	// A name its content does not hash to is refused.
	if err := s.Put("data/"+strings.Repeat("ab", 32), strings.NewReader("not that"), -1, nil); err == nil {
		t.Error("kept data under a name it does not hash to")
	}
	if _, err := s.path("data/../../etc", false); err == nil {
		t.Error("a path outside the store")
	}
}
