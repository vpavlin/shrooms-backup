package backup

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Store is a restic repository in a directory, in restic's own layout
// (data/xx/<id>, index/<id>, ...): a mirror's copy of a source (ADR-014), or
// a source's spool. A copy is readable by plain restic, without this daemon.
// What a source deletes is moved under .held, with the time of deletion as
// its modification time, and purged after Hold.
type Store struct {
	Name string
	dir  string

	mu    sync.Mutex
	size  int64             // bytes in live and held objects
	locks map[string][]byte // restic's locks: mutable, never kept
}

// ErrNoRoom is what a write past a quota gets.
var ErrNoRoom = errors.New("no room")

// OpenStore opens dir as a repository, creating it if needed.
func OpenStore(name, dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	s := &Store{Name: name, dir: dir, locks: map[string][]byte{}}
	filepath.WalkDir(dir, func(p string, e fs.DirEntry, err error) error {
		if err == nil && !e.IsDir() && !strings.HasSuffix(p, ".tmp") {
			if fi, err := e.Info(); err == nil {
				s.size += fi.Size()
			}
		}
		return nil
	})
	return s, nil
}

// path is where key ("config" or "type/name") lives; held, under .held.
func (s *Store) path(key string, held bool) (string, error) {
	base := s.dir
	if held {
		base = filepath.Join(s.dir, ".held")
	}
	if key == "config" {
		return filepath.Join(base, "config"), nil
	}
	typ, name, ok := strings.Cut(key, "/")
	if !ok || !resticTypes[typ] || name == "" || strings.ContainsAny(name, "/\\") || strings.HasPrefix(name, ".") {
		return "", fmt.Errorf("bad object name %q", key)
	}
	if typ == "data" {
		if len(name) < 2 {
			return "", fmt.Errorf("bad object name %q", key)
		}
		return filepath.Join(base, "data", name[:2], name), nil
	}
	return filepath.Join(base, typ, name), nil
}

// Size is what the store takes, held objects included.
func (s *Store) Size() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.size
}

// Has says whether key is live here, and its size.
func (s *Store) Has(key string) (int64, bool) {
	p, err := s.path(key, false)
	if err != nil {
		return 0, false
	}
	fi, err := os.Stat(p)
	if err != nil {
		return 0, false
	}
	return fi.Size(), true
}

// hashed: restic names these by the SHA-256 of their content, which a store
// checks, so a copy cannot hold what its name does not say.
func hashed(key string) bool {
	typ, _, _ := strings.Cut(key, "/")
	return typ == "data" || typ == "index" || typ == "snapshots" || typ == "keys"
}

// Put keeps key with the content r gives, size bytes if known (-1 if not).
// room, if set, is asked first whether size more bytes fit.
func (s *Store) Put(key string, r io.Reader, size int64, room func(int64) error) error {
	p, err := s.path(key, false)
	if err != nil {
		return err
	}
	if room != nil && size > 0 {
		if err := room(size); err != nil {
			return err
		}
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(p), ".put-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(tmp, h), r)
	if err == nil {
		err = tmp.Sync()
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	if size >= 0 && n != size {
		return fmt.Errorf("%s: got %d bytes, want %d", key, n, size)
	}
	if hashed(key) {
		if _, name, _ := strings.Cut(key, "/"); hex.EncodeToString(h.Sum(nil)) != name {
			return fmt.Errorf("%s: its content does not hash to its name", key)
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if fi, err := os.Stat(p); err == nil {
		s.size -= fi.Size()
	}
	if err := os.Rename(tmp.Name(), p); err != nil {
		return err
	}
	s.size += n
	// Written again after a delete: the held copy is the same bytes.
	if hp, err := s.path(key, true); err == nil {
		if fi, err := os.Stat(hp); err == nil && os.Remove(hp) == nil {
			s.size -= fi.Size()
		}
	}
	return nil
}

// Delete holds key: moved under .held, purged after Hold. hold false removes
// it at once (a source's spool, which needs no history).
func (s *Store) Delete(key string, hold bool) error {
	p, err := s.path(key, false)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	fi, err := os.Stat(p)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !hold {
		if err := os.Remove(p); err != nil {
			return err
		}
		s.size -= fi.Size()
		return nil
	}
	hp, _ := s.path(key, true)
	if err := os.MkdirAll(filepath.Dir(hp), 0o700); err != nil {
		return err
	}
	if err := os.Rename(p, hp); err != nil {
		return err
	}
	now := time.Now()
	return os.Chtimes(hp, now, now)
}

// Open is key's content, for reading.
func (s *Store) Open(key string) (*os.File, error) {
	p, err := s.path(key, false)
	if err != nil {
		return nil, err
	}
	return os.Open(p)
}

// Item is one object in a listing.
type Item struct {
	Name string `json:"name"`
	Size int64  `json:"size"`
}

// List is what is live of one type.
func (s *Store) List(typ string) []Item {
	out := []Item{}
	root := filepath.Join(s.dir, typ)
	filepath.WalkDir(root, func(p string, e fs.DirEntry, err error) error {
		if err != nil || e.IsDir() || strings.HasSuffix(p, ".tmp") || strings.HasPrefix(e.Name(), ".") {
			return nil
		}
		if fi, err := e.Info(); err == nil {
			out = append(out, Item{e.Name(), fi.Size()})
		}
		return nil
	})
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Keys is every live object, "type/name" -> size, config included.
func (s *Store) Keys() map[string]int64 {
	out := map[string]int64{}
	if n, ok := s.Has("config"); ok {
		out["config"] = n
	}
	for typ := range resticTypes {
		if typ == "locks" {
			continue
		}
		for _, it := range s.List(typ) {
			out[typ+"/"+it.Name] = it.Size
		}
	}
	return out
}

// Expire purges what was held longer than hold, and says how many.
func (s *Store) Expire(hold time.Duration) (int, error) {
	n := 0
	err := filepath.WalkDir(filepath.Join(s.dir, ".held"), func(p string, e fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return nil
			}
			return err
		}
		if e.IsDir() {
			return nil
		}
		fi, err := e.Info()
		if err != nil || time.Since(fi.ModTime()) <= hold {
			return nil
		}
		if os.Remove(p) == nil {
			s.mu.Lock()
			s.size -= fi.Size()
			s.mu.Unlock()
			n++
		}
		return nil
	})
	return n, err
}

// Handler is restic's REST backend, protocol v2, over this store. readOnly
// refuses writes but locks (a restore takes one); hold keeps deletions; room
// checks a write against the quota.
func (s *Store) Handler(readOnly, hold bool, room func(int64) error) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		p := strings.Trim(req.URL.Path, "/")
		write := req.Method == "POST" || req.Method == "DELETE"
		if readOnly && write && !strings.HasPrefix(p, "locks/") && !(p == "" && req.Method == "POST") {
			http.Error(w, "read-only: this is a mirror's copy", http.StatusForbidden)
			return
		}
		if p == "" && req.Method == "POST" && req.URL.Query().Get("create") == "true" {
			return
		}
		key := p
		if p != "config" {
			typ, name, _ := strings.Cut(p, "/")
			if !resticTypes[typ] {
				http.Error(w, "not found", http.StatusNotFound)
				return
			}
			if name == "" {
				var items []Item
				if typ == "locks" {
					s.mu.Lock()
					for k, v := range s.locks {
						items = append(items, Item{strings.TrimPrefix(k, "locks/"), int64(len(v))})
					}
					s.mu.Unlock()
				} else {
					items = s.List(typ)
				}
				if items == nil {
					items = []Item{}
				}
				w.Header().Set("Content-Type", "application/vnd.x.restic.rest.v2")
				json.NewEncoder(w).Encode(items)
				return
			}
			if typ == "locks" {
				s.lock(w, req, key)
				return
			}
		}
		switch req.Method {
		case "POST":
			if err := s.Put(key, req.Body, req.ContentLength, room); err != nil {
				code := http.StatusBadRequest
				if errors.Is(err, ErrNoRoom) {
					code = http.StatusInsufficientStorage
				}
				http.Error(w, err.Error(), code)
			}
		case "DELETE":
			if err := s.Delete(key, hold); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
			}
		case "HEAD", "GET":
			f, err := s.Open(key)
			if err != nil {
				http.Error(w, "not found", http.StatusNotFound)
				return
			}
			defer f.Close()
			fi, _ := f.Stat()
			if req.Method == "HEAD" {
				w.Header().Set("Content-Length", fmt.Sprint(fi.Size()))
				return
			}
			// restic reads parts of packs (Range).
			http.ServeContent(w, req, "", time.Time{}, f)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})
}

func (s *Store) lock(w http.ResponseWriter, req *http.Request, key string) {
	switch req.Method {
	case "POST":
		b, err := io.ReadAll(io.LimitReader(req.Body, 1<<20))
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		s.mu.Lock()
		s.locks[key] = b
		s.mu.Unlock()
	case "DELETE":
		s.mu.Lock()
		delete(s.locks, key)
		s.mu.Unlock()
	case "HEAD", "GET":
		s.mu.Lock()
		b, ok := s.locks[key]
		s.mu.Unlock()
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
