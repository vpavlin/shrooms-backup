package backup

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// SpoolMax is how much a source keeps of what its mirrors do not all have
// yet, before restic's writes wait (ADR-014).
var SpoolMax int64 = 8 * GB

// SourceRepo is a source's repository as restic sees it, on loopback
// (ADR-014). What restic writes goes into the spool and, at once, to every
// mirror over restic's REST protocol; data stays in the spool only until
// every mirror has it. The catalog is the repository: what restic lists.
// What is not in the spool is read from the staging repository kept before
// ADR-014 (legacy), or from a mirror that has it.
type SourceRepo struct {
	d      *Daemon
	spool  *Store
	legacy *Repo // the node-backed staging of before; nil once moved

	mu      sync.Mutex
	catalog map[string]int64
	catLog  *os.File
	senders map[string]*sender
	locks   *Store // restic's locks, never sent
}

// sender keeps one mirror in step with the catalog.
type sender struct {
	mirror string
	wake   chan struct{}
	// have is what the mirror holds, from its listing and what was sent
	// since; nil until it has been listed.
	have  map[string]int64
	err   string
	moved time.Time
}

// OpenSource opens a source's repository in dir, with the pre-ADR-014
// staging repository, if any.
func OpenSource(d *Daemon, dir string, legacy *Repo) (*SourceRepo, error) {
	spool, err := OpenStore("self", filepath.Join(dir, "spool"))
	if err != nil {
		return nil, err
	}
	locks, err := OpenStore("locks", filepath.Join(dir, "locks"))
	if err != nil {
		return nil, err
	}
	s := &SourceRepo{d: d, spool: spool, legacy: legacy, catalog: map[string]int64{}, senders: map[string]*sender{}, locks: locks}
	if err := s.loadCatalog(filepath.Join(dir, "catalog.log")); err != nil {
		return nil, err
	}
	return s, nil
}

// loadCatalog reads the catalog's log ("+key size", "-key"), writes it
// again compacted, and keeps it open for appending. A source that had only
// the old staging starts from that staging's map.
func (s *SourceRepo) loadCatalog(path string) error {
	if f, err := os.Open(path); err == nil {
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 64<<10), 1<<20)
		for sc.Scan() {
			l := sc.Text()
			switch {
			case strings.HasPrefix(l, "+"):
				k, n, _ := strings.Cut(l[1:], " ")
				size, _ := strconv.ParseInt(n, 10, 64)
				s.catalog[k] = size
			case strings.HasPrefix(l, "-"):
				delete(s.catalog, l[1:])
			}
		}
		f.Close()
	} else if s.legacy != nil {
		for k, o := range s.legacy.Map().Objects {
			if o.Deleted == nil && !strings.HasPrefix(k, "locks/") {
				s.catalog[k] = o.Size
			}
		}
	}
	tmp := path + ".tmp"
	var b bytes.Buffer
	for k, n := range s.catalog {
		fmt.Fprintf(&b, "+%s %d\n", k, n)
	}
	if err := os.WriteFile(tmp, b.Bytes(), 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	s.catLog = f
	return nil
}

func (s *SourceRepo) note(line string) { // with mu held
	s.catLog.WriteString(line + "\n")
}

// Made says the repository exists (restic init has run).
func (s *SourceRepo) Made() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.catalog["config"]
	return ok
}

// Size is what the repository takes, live objects only.
func (s *SourceRepo) Size() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	var n int64
	for _, v := range s.catalog {
		n += v
	}
	return n
}

func (s *SourceRepo) wakeAll() { // with mu held
	for _, se := range s.senders {
		select {
		case se.wake <- struct{}{}:
		default:
		}
	}
}

// Handler is restic's REST backend on loopback for this source.
func (s *SourceRepo) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		p := strings.Trim(req.URL.Path, "/")
		if p == "" && req.Method == "POST" {
			return
		}
		if strings.HasPrefix(p, "locks") {
			s.locks.Handler(false, false, nil).ServeHTTP(w, req)
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
				items := []Item{}
				s.mu.Lock()
				for k, n := range s.catalog {
					if t, nm, _ := strings.Cut(k, "/"); t == typ {
						items = append(items, Item{nm, n})
					}
				}
				s.mu.Unlock()
				slices.SortFunc(items, func(a, b Item) int { return strings.Compare(a.Name, b.Name) })
				w.Header().Set("Content-Type", "application/vnd.x.restic.rest.v2")
				json.NewEncoder(w).Encode(items)
				return
			}
		}
		switch req.Method {
		case "POST":
			if err := s.waitForRoom(req.Context()); err != nil {
				http.Error(w, err.Error(), http.StatusInsufficientStorage)
				return
			}
			if err := s.spool.Put(key, req.Body, req.ContentLength, nil); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			n, _ := s.spool.Has(key)
			s.mu.Lock()
			s.catalog[key] = n
			s.note(fmt.Sprintf("+%s %d", key, n))
			s.wakeAll()
			s.mu.Unlock()
		case "DELETE":
			s.mu.Lock()
			delete(s.catalog, key)
			s.note("-" + key)
			s.wakeAll()
			s.mu.Unlock()
			s.spool.Delete(key, false)
		case "HEAD":
			s.mu.Lock()
			n, ok := s.catalog[key]
			s.mu.Unlock()
			if !ok {
				http.Error(w, "not found", http.StatusNotFound)
				return
			}
			w.Header().Set("Content-Length", strconv.FormatInt(n, 10))
		case "GET":
			s.serve(w, req, key)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})
}

// serve reads key: the spool, the old staging, or a mirror that has it.
func (s *SourceRepo) serve(w http.ResponseWriter, req *http.Request, key string) {
	if f, err := s.spool.Open(key); err == nil {
		defer f.Close()
		http.ServeContent(w, req, "", time.Time{}, f)
		return
	}
	if b, err := s.readLegacy(req.Context(), key); err == nil {
		http.ServeContent(w, req, "", time.Time{}, bytes.NewReader(b))
		return
	}
	for _, m := range s.holders(key) {
		base, err := s.d.mirrorURL(m)
		if err != nil {
			continue
		}
		r, _ := http.NewRequestWithContext(req.Context(), "GET", base+"/v1/repo/"+s.d.Mesh.Self()+"/"+key, nil)
		if rg := req.Header.Get("Range"); rg != "" {
			r.Header.Set("Range", rg)
		}
		resp, err := s.d.bulk().Do(r)
		if err != nil {
			continue
		}
		if resp.StatusCode/100 != 2 {
			resp.Body.Close()
			continue
		}
		for _, h := range []string{"Content-Length", "Content-Range", "Content-Type"} {
			if v := resp.Header.Get(h); v != "" {
				w.Header().Set(h, v)
			}
		}
		w.WriteHeader(resp.StatusCode)
		io.Copy(w, resp.Body)
		resp.Body.Close()
		return
	}
	http.Error(w, "not found", http.StatusNotFound)
}

func (s *SourceRepo) readLegacy(ctx context.Context, key string) ([]byte, error) {
	s.mu.Lock()
	l := s.legacy
	s.mu.Unlock()
	if l == nil {
		return nil, os.ErrNotExist
	}
	o, ok := l.Map().Objects[key]
	if !ok {
		return nil, os.ErrNotExist
	}
	return l.read(ctx, o)
}

// holders are the mirrors known to have key.
func (s *SourceRepo) holders(key string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for m, se := range s.senders {
		if _, ok := se.have[key]; ok {
			out = append(out, m)
		}
	}
	slices.Sort(out)
	return out
}

// waitForRoom holds a write while the spool is over SpoolMax: first it drops
// data some mirror already has, then it waits for the senders, as long as
// something moves.
func (s *SourceRepo) waitForRoom(ctx context.Context) error {
	since := time.Now()
	for s.spool.Size() > SpoolMax {
		s.evict(true)
		if s.spool.Size() <= SpoolMax {
			return nil
		}
		if time.Since(since) > Stalled {
			return fmt.Errorf("the spool is full (%s) and no mirror took anything for %v", humanBytes(s.spool.Size()), Stalled)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
		if s.lastMove().After(since) {
			since = time.Now()
		}
	}
	return nil
}

func (s *SourceRepo) lastMove() time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	var t time.Time
	for _, se := range s.senders {
		if se.moved.After(t) {
			t = se.moved
		}
	}
	return t
}

// evict drops from the spool the data every mirror has, or, under pressure,
// any one has: a mirror that lacks it later gets it from another (serve).
// Metadata stays: restic reads it on every run. keep_local keeps everything.
func (s *SourceRepo) evict(pressed bool) {
	if src := s.d.source(); src != nil && src.KeepLocal {
		return
	}
	for _, it := range s.spool.List("data") {
		key := "data/" + it.Name
		hs := s.holders(key)
		s.mu.Lock()
		all := len(hs) == len(s.senders) && len(hs) > 0
		s.mu.Unlock()
		if all || pressed && len(hs) > 0 {
			s.spool.Delete(key, false)
		}
	}
}

// Sync starts a sender for each mirror and stops those no longer named.
func (s *SourceRepo) Sync(ctx context.Context, mirrors []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, m := range mirrors {
		if _, ok := s.senders[m]; !ok {
			se := &sender{mirror: m, wake: make(chan struct{}, 1), moved: time.Now()}
			s.senders[m] = se
			go s.send(ctx, se)
		}
	}
	for m := range s.senders {
		if !slices.Contains(mirrors, m) {
			delete(s.senders, m) // its loop sees it gone and ends
		}
	}
}

// order: what a mirror gets first. config and keys open the repository; data
// before the index naming it, and both before a snapshot, so a mirror never
// holds a snapshot it cannot restore. Deletions the other way round.
func order(key string) int {
	typ, _, _ := strings.Cut(key, "/")
	return map[string]int{"config": 0, "keys": 1, "data": 2, "index": 3, "snapshots": 4}[typ]
}

// diff is what the mirror lacks, in sending order, and what it has that
// the catalog no longer does, in deleting order.
func (s *SourceRepo) diff(se *sender) (put, del []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for k, n := range s.catalog {
		if m, ok := se.have[k]; !ok || m != n {
			put = append(put, k)
		}
	}
	for k := range se.have {
		if _, ok := s.catalog[k]; !ok {
			del = append(del, k)
		}
	}
	slices.SortFunc(put, func(a, b string) int { return order(a) - order(b) })
	slices.SortFunc(del, func(a, b string) int { return order(b) - order(a) })
	return put, del
}

// Pending is what a mirror still lacks, in bytes, and why not, if stuck.
func (s *SourceRepo) Pending(mirror string) (bytes int64, synced bool, err string, moved time.Time) {
	s.mu.Lock()
	se, ok := s.senders[mirror]
	s.mu.Unlock()
	if !ok {
		return 0, false, "not sending to it", time.Time{}
	}
	put, del := s.diff(se)
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, k := range put {
		bytes += s.catalog[k]
	}
	return bytes, se.have != nil && len(put) == 0 && len(del) == 0, se.err, se.moved
}

func (s *SourceRepo) send(ctx context.Context, se *sender) {
	backoff := time.Second
	fail := func(err error) {
		s.mu.Lock()
		se.err = err.Error()
		s.mu.Unlock()
		select {
		case <-ctx.Done():
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, time.Minute)
	}
	for ctx.Err() == nil {
		s.mu.Lock()
		gone := s.senders[se.mirror] != se
		s.mu.Unlock()
		if gone {
			return
		}
		base, err := s.d.mirrorURL(se.mirror)
		if err != nil {
			fail(err)
			continue
		}
		repo := base + "/v1/repo/" + s.d.Mesh.Self() + "/"
		if se.have == nil {
			have, err := s.list(ctx, repo)
			if err != nil {
				fail(fmt.Errorf("listing %s's copy: %w", se.mirror, err))
				continue
			}
			s.mu.Lock()
			se.have = have
			s.mu.Unlock()
		}
		put, del := s.diff(se)
		if len(put) == 0 && len(del) == 0 {
			s.mu.Lock()
			se.err = ""
			s.mu.Unlock()
			s.evict(false)
			s.dropLegacy(ctx)
			select {
			case <-ctx.Done():
				return
			case <-se.wake:
			case <-time.After(5 * time.Minute): // and list again now and then
				s.mu.Lock()
				se.have = nil
				s.mu.Unlock()
			}
			continue
		}
		var err2 error
		for _, k := range put {
			if err2 = s.sendOne(ctx, se, repo, k); err2 != nil {
				break
			}
		}
		if err2 == nil {
			for _, k := range del {
				if err2 = s.deleteOne(ctx, se, repo, k); err2 != nil {
					break
				}
			}
		}
		if err2 != nil {
			fail(err2)
			continue
		}
		backoff = time.Second
	}
}

func (s *SourceRepo) list(ctx context.Context, repo string) (map[string]int64, error) {
	have := map[string]int64{}
	req, _ := http.NewRequestWithContext(ctx, "HEAD", repo+"config", nil)
	if resp, err := s.d.bulk().Do(req); err != nil {
		return nil, err
	} else {
		resp.Body.Close()
		if resp.StatusCode == 200 {
			have["config"] = resp.ContentLength
		}
	}
	for typ := range resticTypes {
		if typ == "locks" {
			continue
		}
		req, _ := http.NewRequestWithContext(ctx, "GET", repo+typ+"/", nil)
		req.Header.Set("Accept", "application/vnd.x.restic.rest.v2")
		resp, err := s.d.bulk().Do(req)
		if err != nil {
			return nil, err
		}
		var items []Item
		err = json.NewDecoder(resp.Body).Decode(&items)
		resp.Body.Close()
		if resp.StatusCode != 200 {
			return nil, fmt.Errorf("%s: %s", typ, resp.Status)
		}
		if err != nil {
			return nil, err
		}
		for _, it := range items {
			have[typ+"/"+it.Name] = it.Size
		}
	}
	return have, nil
}

// sendOne sends one object, from the spool, the old staging, or another
// mirror; one the catalog dropped meanwhile is skipped.
func (s *SourceRepo) sendOne(ctx context.Context, se *sender, repo, key string) error {
	s.mu.Lock()
	size, live := s.catalog[key]
	s.mu.Unlock()
	if !live {
		return nil
	}
	var body io.Reader
	if f, err := s.spool.Open(key); err == nil {
		defer f.Close()
		body = f
	} else if b, err := s.readLegacy(ctx, key); err == nil {
		body = bytes.NewReader(b)
	} else {
		rec := &bufferResponse{header: http.Header{}}
		req, _ := http.NewRequestWithContext(ctx, "GET", "/"+key, nil)
		s.serve(rec, req, key)
		if rec.code != 0 && rec.code != 200 {
			return fmt.Errorf("%s: not in the spool, the staging or any mirror", key)
		}
		body = &rec.buf
	}
	req, _ := http.NewRequestWithContext(ctx, "POST", repo+key, body)
	req.ContentLength = size
	resp, err := s.d.bulk().Do(req)
	if err != nil {
		return err
	}
	msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		if resp.StatusCode == http.StatusForbidden && bytes.Contains(msg, []byte("read-only")) {
			return fmt.Errorf("%s runs a Shrooms Backup from before ADR-014, which takes no copies this way: update it", se.mirror)
		}
		return fmt.Errorf("%s refused %s: %s %s", se.mirror, key, resp.Status, strings.TrimSpace(string(msg)))
	}
	s.mu.Lock()
	se.have[key], se.moved, se.err = size, time.Now(), ""
	s.mu.Unlock()
	return nil
}

func (s *SourceRepo) deleteOne(ctx context.Context, se *sender, repo, key string) error {
	req, _ := http.NewRequestWithContext(ctx, "DELETE", repo+key, nil)
	resp, err := s.d.bulk().Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode/100 != 2 && resp.StatusCode != 404 {
		return fmt.Errorf("%s refused to delete %s: %s", se.mirror, key, resp.Status)
	}
	s.mu.Lock()
	delete(se.have, key)
	se.moved = time.Now()
	s.mu.Unlock()
	return nil
}

// dropLegacy empties the old staging once every mirror holds all that is
// still live of it: the second copy ADR-014 is about.
func (s *SourceRepo) dropLegacy(ctx context.Context) {
	s.mu.Lock()
	l := s.legacy
	n := len(s.senders)
	s.mu.Unlock()
	if l == nil || n == 0 {
		return
	}
	m := l.Map()
	for k := range m.Objects {
		s.mu.Lock()
		_, live := s.catalog[k]
		s.mu.Unlock()
		if live && len(s.holders(k)) < n {
			return
		}
	}
	for _, o := range m.Objects {
		l.blobs.Delete(ctx, o.CID)
	}
	l.cache.Clear()
	os.Rename(filepath.Join(l.dir, "map.json"), filepath.Join(l.dir, "map.json.moved"))
	s.mu.Lock()
	s.legacy = nil
	s.mu.Unlock()
	s.d.Log.Info("the old staging copy is emptied: every mirror holds the repository", "objects", len(m.Objects))
}

// bufferResponse is a ResponseWriter into memory, for reading through serve.
type bufferResponse struct {
	header http.Header
	code   int
	buf    bytes.Buffer
}

func (b *bufferResponse) Header() http.Header         { return b.header }
func (b *bufferResponse) Write(p []byte) (int, error) { return b.buf.Write(p) }
func (b *bufferResponse) WriteHeader(code int)        { b.code = code }

var errNotSynced = errors.New("not in step")
