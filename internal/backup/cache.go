package backup

import (
	"container/list"
	"os"
	"path/filepath"
	"sync"
)

// Cache keeps whole objects on disk, up to Max bytes, oldest use evicted
// first. Logos Storage serves only whole objects and restic reads parts of
// its packs, many times over during a restore or a prune; without a cache
// each part would fetch the whole pack again. Unbounded in the spike, it
// would have kept a second copy of everything read.
type Cache struct {
	dir string
	max int64

	mu    sync.Mutex
	used  int64
	order *list.List               // front: most recent
	items map[string]*list.Element // cid -> element (value: entry)
}

type cacheEntry struct {
	cid  string
	size int64
}

// NewCache empties dir and caches up to max bytes there.
func NewCache(dir string, max int64) (*Cache, error) {
	os.RemoveAll(dir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	return &Cache{dir: dir, max: max, order: list.New(), items: map[string]*list.Element{}}, nil
}

func (c *Cache) path(cid string) string { return filepath.Join(c.dir, cid) }

// Get returns a cached object.
func (c *Cache) Get(cid string) ([]byte, bool) {
	if c == nil {
		return nil, false
	}
	c.mu.Lock()
	e, ok := c.items[cid]
	if ok {
		c.order.MoveToFront(e)
	}
	c.mu.Unlock()
	if !ok {
		return nil, false
	}
	b, err := os.ReadFile(c.path(cid))
	return b, err == nil
}

// Put caches an object, evicting the least recently used to make room. An
// object larger than the whole cache is not kept.
func (c *Cache) Put(cid string, b []byte) {
	if c == nil || int64(len(b)) > c.max {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if e, ok := c.items[cid]; ok {
		c.order.MoveToFront(e)
		return
	}
	for c.used+int64(len(b)) > c.max && c.order.Len() > 0 {
		old := c.order.Back()
		ent := old.Value.(cacheEntry)
		os.Remove(c.path(ent.cid))
		c.order.Remove(old)
		delete(c.items, ent.cid)
		c.used -= ent.size
	}
	if os.WriteFile(c.path(cid), b, 0o600) != nil {
		return
	}
	c.items[cid] = c.order.PushFront(cacheEntry{cid, int64(len(b))})
	c.used += int64(len(b))
}

// Clear empties the cache (after a restore or a prune).
func (c *Cache) Clear() {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for cid := range c.items {
		os.Remove(c.path(cid))
	}
	c.items = map[string]*list.Element{}
	c.order.Init()
	c.used = 0
}
