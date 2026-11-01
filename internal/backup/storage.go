// Package backup is Shrooms Backup (ADR-001): restic's repositories kept in
// Logos Storage, on this machine's node and on the mirrors a source picks.
package backup

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Storage is this machine's Logos Storage node, through its REST API (which
// has no authentication, so it listens on loopback only).
type Storage struct {
	API    string // "http://127.0.0.1:8591"
	Client *http.Client
}

func (s *Storage) client() *http.Client {
	if s.Client != nil {
		return s.Client
	}
	return http.DefaultClient
}

func (s *Storage) url(p string) string { return strings.TrimRight(s.API, "/") + "/api/storage/v1" + p }

// Put stores b and returns its CID. The name goes into the manifest, and so
// into the CID: a re-upload keeps its CID only with the same name and type
// (found refreshing the review host's archives, 2026-11-01).
func (s *Storage) Put(ctx context.Context, name string, b []byte) (string, error) {
	req, _ := http.NewRequestWithContext(ctx, "POST", s.url("/data"), bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("Content-Disposition", `attachment; filename="`+name+`"`)
	resp, err := s.client().Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode/100 != 2 {
		return "", fmt.Errorf("storage upload: %s: %s", resp.Status, strings.TrimSpace(string(out)))
	}
	cid := strings.TrimSpace(string(out))
	if cid == "" {
		return "", fmt.Errorf("storage upload: no CID")
	}
	return cid, nil
}

// Get reads a whole object from this node's store, or, with network, from
// whichever node has it. Logos Storage ignores Range: always whole.
func (s *Storage) Get(ctx context.Context, cid string, network bool) ([]byte, error) {
	p := "/data/" + cid
	if network {
		p += "/network/stream"
	}
	req, _ := http.NewRequestWithContext(ctx, "GET", s.url(p), nil)
	resp, err := s.client().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("%s: %s", cid, resp.Status)
	}
	return io.ReadAll(resp.Body)
}

// Has reports whether this node holds all of cid, of size bytes.
func (s *Storage) Has(ctx context.Context, cid string, size int64) bool {
	req, _ := http.NewRequestWithContext(ctx, "GET", s.url("/data/"+cid), nil)
	resp, err := s.client().Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false
	}
	n, err := io.Copy(io.Discard, resp.Body)
	return err == nil && n == size
}

// Fetch asks this node to fetch cid from the network into its store. It
// returns at once; Has says when it is done.
func (s *Storage) Fetch(ctx context.Context, cid string) error {
	req, _ := http.NewRequestWithContext(ctx, "POST", s.url("/data/"+cid+"/network"), nil)
	resp, err := s.client().Do(req)
	if err != nil {
		return err
	}
	io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("fetch %s: %s", cid, resp.Status)
	}
	return nil
}

// Delete removes cid from this node's store only.
func (s *Storage) Delete(ctx context.Context, cid string) error {
	req, _ := http.NewRequestWithContext(ctx, "DELETE", s.url("/data/"+cid), nil)
	resp, err := s.client().Do(req)
	if err != nil {
		return err
	}
	io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	resp.Body.Close()
	if resp.StatusCode/100 != 2 && resp.StatusCode != http.StatusNotFound {
		return fmt.Errorf("delete %s: %s", cid, resp.Status)
	}
	return nil
}

// Space is the node's quota and use.
type Space struct {
	QuotaMax  int64 `json:"quotaMaxBytes"`
	QuotaUsed int64 `json:"quotaUsedBytes"`
}

func (s *Storage) Space(ctx context.Context) (Space, error) {
	var sp Space
	return sp, s.getJSON(ctx, "/space", &sp)
}

// SPR is the node's signed peer record, which other nodes bootstrap from.
func (s *Storage) SPR(ctx context.Context) (string, error) {
	var out struct{ SPR string }
	err := s.getJSON(ctx, "/spr", &out)
	return out.SPR, err
}

func (s *Storage) getJSON(ctx context.Context, p string, v any) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", s.url(p), nil)
	resp, err := s.client().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: %s", p, resp.Status)
	}
	return json.NewDecoder(resp.Body).Decode(v)
}
