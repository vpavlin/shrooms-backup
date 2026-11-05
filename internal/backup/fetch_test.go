package backup

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"
)

// gappy is a node from which some objects cannot be fetched until later:
// the DHT does not know who has them yet.
type gappy struct {
	*node
	mu    sync.Mutex
	until map[string]int // cid -> fetches that fail first
}

func (g *gappy) Fetch(ctx context.Context, cid string) error {
	g.mu.Lock()
	if g.until[cid] > 0 {
		g.until[cid]--
		g.mu.Unlock()
		return errors.New("Failed to fetch manifest: Key doesn't exist")
	}
	g.mu.Unlock()
	return g.node.Fetch(ctx, cid)
}

func TestAMirrorFetchesPastWhatItCannotFindYet(t *testing.T) {
	defer func(p time.Duration) { retryPause = p }(retryPause)
	retryPause = 10 * time.Millisecond
	net_ := newNetwork()
	src := net_.node("k11")
	m := Map{Version: 1, Objects: map[string]Object{}}
	g := &gappy{node: net_.node("scribe"), until: map[string]int{}}
	for i := 0; i < 20; i++ {
		data := strings.Repeat(string(rune('a'+i)), 100+i)
		cid, _ := src.Put(context.Background(), "data/"+data[:1], []byte(data))
		m.Objects[cid] = Object{CID: cid, Size: int64(len(data))}
		if i%3 == 0 {
			g.until[cid] = 2 // found only on the third pass
		}
	}
	d := &Daemon{Cfg: &Config{Mesh: "home"}, Log: slog.New(slog.DiscardHandler), Store: g}
	mc := &mirrorCopy{mesh: "home", source: "k11"}
	if err := d.fetchAll(context.Background(), mc, m); err != nil {
		t.Fatal(err)
	}
	if g.count() != 20 || mc.missing != 0 || mc.missingBytes != 0 {
		t.Errorf("fetched %d of 20, missing %d (%d bytes)", g.count(), mc.missing, mc.missingBytes)
	}
}

func TestAMirrorGivesUpOnlyWhenNothingMoves(t *testing.T) {
	defer func(p, s time.Duration) { retryPause, Stalled = p, s }(retryPause, Stalled)
	retryPause, Stalled = 10*time.Millisecond, 100*time.Millisecond
	net_ := newNetwork()
	src := net_.node("k11")
	cid, _ := src.Put(context.Background(), "data/x", []byte("never announced"))
	g := &gappy{node: net_.node("scribe"), until: map[string]int{cid: 1 << 30}}
	m := Map{Version: 1, Objects: map[string]Object{cid: {CID: cid, Size: 15}}}
	d := &Daemon{Cfg: &Config{Mesh: "home"}, Log: slog.New(slog.DiscardHandler), Store: g}
	mc := &mirrorCopy{mesh: "home", source: "k11"}
	err := d.fetchAll(context.Background(), mc, m)
	if err == nil || !strings.Contains(err.Error(), "none fetched") {
		t.Fatalf("a fetch that never moves: %v", err)
	}
	if !strings.Contains(mc.err, "not found yet") {
		t.Errorf("the copy did not say why it waits: %q", mc.err)
	}
}
