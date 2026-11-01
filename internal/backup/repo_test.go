package backup

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// network is a few Logos Storage nodes: each holds some CIDs, and a network
// read finds a CID on any of them.
type network struct {
	mu    sync.Mutex
	nodes map[string]map[string][]byte
}

type node struct {
	net  *network
	name string
}

func newNetwork() *network { return &network{nodes: map[string]map[string][]byte{}} }

func (n *network) node(name string) *node {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.nodes[name] == nil {
		n.nodes[name] = map[string][]byte{}
	}
	return &node{n, name}
}

func (b *node) Put(_ context.Context, name string, data []byte) (string, error) {
	h := sha256.Sum256(append([]byte(name+"\x00"), data...))
	cid := "z" + hex.EncodeToString(h[:16])
	b.net.mu.Lock()
	b.net.nodes[b.name][cid] = append([]byte(nil), data...)
	b.net.mu.Unlock()
	return cid, nil
}

func (b *node) Get(_ context.Context, cid string, network bool) ([]byte, error) {
	b.net.mu.Lock()
	defer b.net.mu.Unlock()
	if d, ok := b.net.nodes[b.name][cid]; ok {
		return d, nil
	}
	if network {
		for _, s := range b.net.nodes {
			if d, ok := s[cid]; ok {
				return d, nil
			}
		}
	}
	return nil, fmt.Errorf("%s: not found", cid)
}

func (b *node) Has(_ context.Context, cid string, size int64) bool {
	b.net.mu.Lock()
	defer b.net.mu.Unlock()
	d, ok := b.net.nodes[b.name][cid]
	return ok && int64(len(d)) == size
}

func (b *node) Fetch(ctx context.Context, cid string) error {
	d, err := b.Get(ctx, cid, true)
	if err != nil {
		return err
	}
	b.net.mu.Lock()
	b.net.nodes[b.name][cid] = d
	b.net.mu.Unlock()
	return nil
}

func (b *node) Delete(_ context.Context, cid string) error {
	b.net.mu.Lock()
	delete(b.net.nodes[b.name], cid)
	b.net.mu.Unlock()
	return nil
}

func (b *node) count() int {
	b.net.mu.Lock()
	defer b.net.mu.Unlock()
	return len(b.net.nodes[b.name])
}

func restic(t *testing.T) string {
	t.Helper()
	for _, p := range []string{os.Getenv("RESTIC"), "restic"} {
		if p == "" {
			continue
		}
		if path, err := exec.LookPath(p); err == nil {
			return path
		}
	}
	t.Skip("no restic: set RESTIC to its binary")
	return ""
}

func run(t *testing.T, bin, repo string, args ...string) string {
	t.Helper()
	cmd := exec.Command(bin, args...)
	cmd.Env = append(os.Environ(), "RESTIC_REPOSITORY=rest:"+repo+"/", "RESTIC_PASSWORD=test", "RESTIC_CACHE_DIR="+t.TempDir())
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("restic %v: %v\n%s", args, err, out)
	}
	return string(out)
}

func tree(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	filepath.Walk(dir, func(p string, fi os.FileInfo, err error) error {
		if err == nil && fi.Mode().IsRegular() {
			b, _ := os.ReadFile(p)
			rel, _ := filepath.Rel(dir, p)
			out[rel] = fmt.Sprintf("%x", sha256.Sum256(b))
		}
		return nil
	})
	return out
}

// The whole path a backup takes: restic writes to the source's repository,
// a change stores only the change, a prune's deletions are held, and a
// mirror that adopted the source's map restores the snapshot from its own
// node with the source's node gone.
func TestResticThroughTheBackend(t *testing.T) {
	bin := restic(t)
	net := newNetwork()
	srcNode, mirNode := net.node("laptop"), net.node("proteus")
	cache, _ := NewCache(t.TempDir(), 64<<20)
	src, err := OpenRepo("laptop", t.TempDir(), srcNode, cache)
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(src.Handler(false))
	defer ts.Close()

	data := t.TempDir()
	for i := 0; i < 20; i++ {
		os.WriteFile(filepath.Join(data, fmt.Sprintf("f%02d", i)), []byte(strings.Repeat(fmt.Sprintf("line %d\n", i), 3000)), 0o644)
	}
	run(t, bin, ts.URL, "init")
	run(t, bin, ts.URL, "backup", "--no-scan", data)
	before := srcNode.count()
	os.WriteFile(filepath.Join(data, "f00"), []byte("changed"), 0o644)
	out := run(t, bin, ts.URL, "backup", "--no-scan", data)
	if !strings.Contains(out, "Added to the repository") {
		t.Fatalf("second backup: %s", out)
	}
	if added := srcNode.count() - before; added > 4 {
		t.Errorf("a one-file change added %d objects", added)
	}
	want := tree(t, data)

	// A forget and prune: restic deletes objects; they are held, not gone.
	run(t, bin, ts.URL, "forget", "--keep-last", "1", "--prune")
	m := src.Map()
	held := 0
	for _, o := range m.Objects {
		if o.Deleted != nil {
			held++
		}
	}
	if held == 0 {
		t.Fatal("the prune deleted nothing, or the deletions were not held")
	}
	if srcNode.count() < before {
		t.Error("held objects were removed from the store")
	}

	// The mirror: fetch every CID, adopt the map, lose the source.
	for _, o := range m.Objects {
		if err := mirNode.Fetch(context.Background(), o.CID); err != nil {
			t.Fatal(err)
		}
	}
	mcache, _ := NewCache(t.TempDir(), 64<<20)
	mir, _ := OpenRepo("laptop", t.TempDir(), mirNode, mcache)
	if err := mir.Adopt(m); err != nil {
		t.Fatal(err)
	}
	net.mu.Lock()
	net.nodes["laptop"] = map[string][]byte{}
	net.mu.Unlock()
	ms := httptest.NewServer(mir.Handler(true))
	defer ms.Close()
	to := t.TempDir()
	run(t, bin, ms.URL, "restore", "latest", "--target", to)
	got := tree(t, filepath.Join(to, data))
	if len(got) != len(want) {
		t.Fatalf("restored %d files, want %d", len(got), len(want))
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s differs after restore", k)
		}
	}
	// A mirror's copy takes no writes but restic's locks.
	cmd := exec.Command(bin, "backup", "--no-scan", data)
	cmd.Env = append(os.Environ(), "RESTIC_REPOSITORY=rest:"+ms.URL+"/", "RESTIC_PASSWORD=test", "RESTIC_CACHE_DIR="+t.TempDir())
	if out, err := cmd.CombinedOutput(); err == nil {
		t.Errorf("a backup into a mirror's copy succeeded: %s", out)
	}

	// After the hold, the deleted objects leave the mirror's node.
	n, err := mir.Expire(context.Background(), 0)
	if err != nil || n != held {
		t.Errorf("expired %d (%v), want %d", n, err, held)
	}
}

// A newer map from the source replaces an older one, never the reverse; an
// object the source dropped without a deletion we saw is held all the same.
func TestAdoptKeepsOrderAndHolds(t *testing.T) {
	net := newNetwork()
	r, _ := OpenRepo("x", t.TempDir(), net.node("m"), nil)
	if err := r.Adopt(Map{Version: 2, Objects: map[string]Object{"data/a": {CID: "z1", Size: 1}}}); err != nil {
		t.Fatal(err)
	}
	if err := r.Adopt(Map{Version: 1, Objects: map[string]Object{}}); err == nil {
		t.Error("an older map was adopted")
	}
	if err := r.Adopt(Map{Version: 3, Objects: map[string]Object{}}); err != nil {
		t.Fatal(err)
	}
	if o := r.Map().Objects["data/a"]; o.Deleted == nil || time.Since(*o.Deleted) > time.Minute {
		t.Errorf("a dropped object was not held: %+v", o)
	}
}

func TestCacheEvictsOldestFirst(t *testing.T) {
	c, _ := NewCache(t.TempDir(), 10)
	c.Put("a", []byte("1234"))
	c.Put("b", []byte("1234"))
	c.Get("a")
	c.Put("c", []byte("1234")) // over 10: b, the least recently used, goes
	if _, ok := c.Get("b"); ok {
		t.Error("b should have been evicted")
	}
	if _, ok := c.Get("a"); !ok {
		t.Error("a was used recently and evicted")
	}
	c.Put("huge", make([]byte, 11))
	if _, ok := c.Get("huge"); ok {
		t.Error("an object larger than the cache was kept")
	}
}
