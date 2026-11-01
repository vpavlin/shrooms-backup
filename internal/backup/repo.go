package backup

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Blobs is where a repository's objects live: this machine's Logos Storage
// node (Storage), or a fake in tests.
type Blobs interface {
	Put(ctx context.Context, name string, b []byte) (string, error)
	Get(ctx context.Context, cid string, network bool) ([]byte, error)
	Has(ctx context.Context, cid string, size int64) bool
	Fetch(ctx context.Context, cid string) error
	Delete(ctx context.Context, cid string) error
}

// Object is one restic object as a CID.
type Object struct {
	CID  string `json:"cid"`
	Size int64  `json:"size"`
	// Deleted is when restic deleted it (a prune, a forget). It is kept
	// until then plus Hold, so a source that turns hostile cannot take its
	// history with it (ADR-001).
	Deleted *time.Time `json:"deleted,omitempty"`
}

// Hold is how long a deleted object stays in the store.
const Hold = 30 * 24 * time.Hour

// Map is a repository's objects by restic's "type/name", and its version,
// which counts changes so a mirror can tell an old map from a new one.
type Map struct {
	Version int64             `json:"version"`
	Objects map[string]Object `json:"objects"`
}

// Repo is one restic repository: on a source, its own; on a mirror, a
// source's copy.
type Repo struct {
	Name  string
	dir   string
	blobs Blobs
	cache *Cache

	mu    sync.Mutex
	m     Map
	locks map[string][]byte // restic's locks: mutable, so never stored
}

// OpenRepo loads the repository's map from dir, or starts an empty one.
func OpenRepo(name, dir string, blobs Blobs, cache *Cache) (*Repo, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	r := &Repo{Name: name, dir: dir, blobs: blobs, cache: cache, locks: map[string][]byte{},
		m: Map{Objects: map[string]Object{}}}
	b, err := os.ReadFile(filepath.Join(dir, "map.json"))
	switch {
	case err == nil:
		if err := json.Unmarshal(b, &r.m); err != nil {
			return nil, fmt.Errorf("%s: %w", filepath.Join(dir, "map.json"), err)
		}
		if r.m.Objects == nil {
			r.m.Objects = map[string]Object{}
		}
	case !errors.Is(err, os.ErrNotExist):
		return nil, err
	}
	return r, nil
}

// save writes the map; caller holds mu.
func (r *Repo) save() error {
	b, err := json.Marshal(r.m)
	if err != nil {
		return err
	}
	tmp := filepath.Join(r.dir, "map.json.tmp")
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(r.dir, "map.json"))
}

// Map returns a copy of the repository's map.
func (r *Repo) Map() Map {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := Map{Version: r.m.Version, Objects: make(map[string]Object, len(r.m.Objects))}
	for k, v := range r.m.Objects {
		out.Objects[k] = v
	}
	return out
}

// Adopt replaces the map with a newer one from the source (on a mirror).
// Objects the source deleted keep their deletion time; nothing is removed
// from the store here (see Expire).
func (r *Repo) Adopt(m Map) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if m.Version < r.m.Version {
		return fmt.Errorf("map version %d is older than %d", m.Version, r.m.Version)
	}
	// An object the source no longer lists at all is a deletion we did not
	// see: held like any other.
	now := time.Now()
	for k, o := range r.m.Objects {
		if _, ok := m.Objects[k]; !ok && o.Deleted == nil {
			o.Deleted = &now
			m.Objects[k] = o
		}
	}
	r.m = m
	return r.save()
}

// Expire removes from the store what was deleted longer ago than hold, and
// returns how many. An object whose CID is still used by another live entry
// (the same bytes under two names) is forgotten, not deleted.
func (r *Repo) Expire(ctx context.Context, hold time.Duration) (int, error) {
	r.mu.Lock()
	live := map[string]bool{}
	var gone []string
	for k, o := range r.m.Objects {
		if o.Deleted == nil {
			live[o.CID] = true
		} else if time.Since(*o.Deleted) > hold {
			gone = append(gone, k)
		}
	}
	r.mu.Unlock()
	n := 0
	for _, k := range gone {
		r.mu.Lock()
		o := r.m.Objects[k]
		r.mu.Unlock()
		if !live[o.CID] {
			if err := r.blobs.Delete(ctx, o.CID); err != nil {
				return n, err
			}
		}
		r.mu.Lock()
		delete(r.m.Objects, k)
		r.m.Version++
		err := r.save()
		r.mu.Unlock()
		if err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

var resticTypes = map[string]bool{"data": true, "keys": true, "locks": true, "snapshots": true, "index": true}

// Handler is restic's REST backend, protocol v2, for this repository.
// readOnly refuses writes (a mirror serving a restore).
func (r *Repo) Handler(readOnly bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		p := strings.Trim(req.URL.Path, "/")
		write := req.Method == "POST" || req.Method == "DELETE"
		// Locks are restic's own coordination: even a read-only restore takes one.
		if readOnly && write && !strings.HasPrefix(p, "locks/") && !(p == "" && req.Method == "POST") {
			http.Error(w, "read-only: this is a mirror's copy", http.StatusForbidden)
			return
		}
		switch {
		case p == "" && req.Method == "POST" && req.URL.Query().Get("create") == "true":
			return
		case p == "config":
			r.object(w, req, "config")
			return
		}
		typ, name, _ := strings.Cut(p, "/")
		if !resticTypes[typ] {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		if name == "" {
			r.list(w, typ)
			return
		}
		if strings.Contains(name, "/") {
			http.Error(w, "bad name", http.StatusBadRequest)
			return
		}
		r.object(w, req, typ+"/"+name)
	})
}

func (r *Repo) list(w http.ResponseWriter, typ string) {
	type item struct {
		Name string `json:"name"`
		Size int64  `json:"size"`
	}
	out := []item{}
	r.mu.Lock()
	if typ == "locks" {
		for k, v := range r.locks {
			out = append(out, item{strings.TrimPrefix(k, "locks/"), int64(len(v))})
		}
	} else {
		for k, o := range r.m.Objects {
			if o.Deleted == nil && strings.HasPrefix(k, typ+"/") {
				out = append(out, item{strings.TrimPrefix(k, typ+"/"), o.Size})
			}
		}
	}
	r.mu.Unlock()
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	w.Header().Set("Content-Type", "application/vnd.x.restic.rest.v2")
	json.NewEncoder(w).Encode(out)
}

func (r *Repo) object(w http.ResponseWriter, req *http.Request, key string) {
	ctx := req.Context()
	if strings.HasPrefix(key, "locks/") {
		r.lock(w, req, key)
		return
	}
	switch req.Method {
	case "POST":
		body, err := io.ReadAll(req.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		// The repository's name in the stored name, so a node's contents
		// say whose they are.
		cid, err := r.blobs.Put(ctx, r.Name+"-"+strings.ReplaceAll(key, "/", "-"), body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		r.cache.Put(cid, body)
		r.mu.Lock()
		r.m.Objects[key] = Object{CID: cid, Size: int64(len(body))}
		r.m.Version++
		err = r.save()
		r.mu.Unlock()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
	case "DELETE":
		r.mu.Lock()
		if o, ok := r.m.Objects[key]; ok && o.Deleted == nil {
			now := time.Now()
			o.Deleted = &now
			r.m.Objects[key] = o
			r.m.Version++
		}
		err := r.save()
		r.mu.Unlock()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
	case "HEAD", "GET":
		r.mu.Lock()
		o, ok := r.m.Objects[key]
		r.mu.Unlock()
		if !ok || o.Deleted != nil {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		if req.Method == "HEAD" {
			w.Header().Set("Content-Length", fmt.Sprint(o.Size))
			return
		}
		b, err := r.read(ctx, o)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		// restic reads parts of packs (Range); ServeContent answers them
		// from the whole object.
		http.ServeContent(w, req, "", time.Time{}, bytes.NewReader(b))
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// read gets an object: the cache, this node's store, then the network (a
// staging source whose copy has moved to the mirrors, or a new machine
// restoring).
func (r *Repo) read(ctx context.Context, o Object) ([]byte, error) {
	if b, ok := r.cache.Get(o.CID); ok {
		return b, nil
	}
	b, err := r.blobs.Get(ctx, o.CID, false)
	if err != nil || int64(len(b)) != o.Size {
		b, err = r.blobs.Get(ctx, o.CID, true)
	}
	if err != nil {
		return nil, err
	}
	if int64(len(b)) != o.Size {
		return nil, fmt.Errorf("%s: %d bytes, want %d", o.CID, len(b), o.Size)
	}
	r.cache.Put(o.CID, b)
	return b, nil
}

func (r *Repo) lock(w http.ResponseWriter, req *http.Request, key string) {
	switch req.Method {
	case "POST":
		b, err := io.ReadAll(io.LimitReader(req.Body, 1<<20))
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		r.mu.Lock()
		r.locks[key] = b
		r.mu.Unlock()
	case "DELETE":
		r.mu.Lock()
		delete(r.locks, key)
		r.mu.Unlock()
	case "HEAD", "GET":
		r.mu.Lock()
		b, ok := r.locks[key]
		r.mu.Unlock()
		if !ok {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		if req.Method == "HEAD" {
			w.Header().Set("Content-Length", fmt.Sprint(len(b)))
			return
		}
		w.Write(b)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// Size is what the map's objects take, the held deletions too if held.
func (m Map) Size(held bool) int64 {
	var n int64
	for _, o := range m.Objects {
		if o.Deleted == nil || held {
			n += o.Size
		}
	}
	return n
}
