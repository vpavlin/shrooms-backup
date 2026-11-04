package backup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

var keyLabel = regexp.MustCompile(`^[A-Za-z0-9 ._-]{0,40}$`)

// KeyInfo is one of a source's keys, for its owners: which it is, where it
// came from, when; never anything that opens it.
type KeyInfo struct {
	ID      string    `json:"id"`
	Label   string    `json:"label"`
	Created time.Time `json:"created"`
	Current bool      `json:"current"` // the one the daemon backs up with
}

// labels are the keys' labels by restic key ID, in the state directory: a
// label is the apps' word for a key, not restic's.
func (d *Daemon) labelsPath() string { return filepath.Join(d.State, "key-labels.json") }

func (d *Daemon) labels() map[string]string {
	m := map[string]string{}
	if b, err := os.ReadFile(d.labelsPath()); err == nil {
		json.Unmarshal(b, &m)
	}
	return m
}

// labelKey labels the key the daemon uses now. Before the repository exists
// there is no key ID yet; the label waits for the first run, which makes it.
func (d *Daemon) labelKey(ctx context.Context, label string) {
	if !d.repoMade() {
		os.WriteFile(filepath.Join(d.State, "key-label-pending"), []byte(label), 0o600)
		return
	}
	keys, err := d.resticKeys(ctx)
	if err != nil {
		d.Log.Warn("labelling the key", "err", err)
		return
	}
	m := d.labels()
	for _, k := range keys {
		if k.Current {
			m[k.ID] = label
		}
	}
	b, _ := json.Marshal(m)
	os.WriteFile(d.labelsPath(), b, 0o600)
}

// labelPending labels the first key once the first run has made the repository.
func (d *Daemon) labelPending(ctx context.Context) {
	p := filepath.Join(d.State, "key-label-pending")
	if b, err := os.ReadFile(p); err == nil {
		os.Remove(p)
		d.labelKey(ctx, string(b))
	}
}

func (d *Daemon) resticKeys(ctx context.Context) ([]KeyInfo, error) {
	out, err := d.restic(ctx, "key", "list", "--json", "--no-lock")
	if err != nil {
		return nil, err
	}
	// restic writes "created" as local time without a zone, "2026-11-02 09:05:45".
	var raw []struct {
		ID      string `json:"id"`
		Current bool   `json:"current"`
		Created string `json:"created"`
	}
	if err := json.Unmarshal([]byte(out[strings.Index(out, "["):]), &raw); err != nil {
		return nil, err
	}
	keys := make([]KeyInfo, 0, len(raw))
	for _, k := range raw {
		at, _ := time.ParseInLocation(time.DateTime, k.Created, time.Local)
		keys = append(keys, KeyInfo{ID: k.ID, Created: at, Current: k.Current})
	}
	return keys, nil
}

// getKeys lists a source's keys for its owners.
func (d *Daemon) getKeys(w http.ResponseWriter, r *http.Request) {
	if !d.owner(callerOf(r)) {
		fail(w, http.StatusForbidden, errors.New("owners only"))
		return
	}
	if d.source() == nil || !d.HasKey() {
		writeJSON(w, 200, map[string]any{"keys": []KeyInfo{}})
		return
	}
	if !d.repoMade() {
		writeJSON(w, 200, map[string]any{"keys": []KeyInfo{}, "pending": true})
		return
	}
	d.mu.Lock()
	keys := append([]KeyInfo(nil), d.keys...)
	d.mu.Unlock()
	if keys == nil {
		var err error
		if keys, err = d.refreshKeys(r.Context()); err != nil {
			fail(w, http.StatusBadGateway, err)
			return
		}
	}
	m := d.labels()
	for i := range keys {
		if keys[i].Label = m[keys[i].ID]; keys[i].Label == "" {
			// Unlabelled: set on the machine, or before keys had labels.
			keys[i].Label = "passphrase"
		}
		keys[i].ID = keys[i].ID[:min(8, len(keys[i].ID))]
	}
	writeJSON(w, 200, map[string]any{"keys": keys})
}

// putKey sets this source's key from an owner's device: the phone, which
// derives it from the Keycard (ADR-001), or anything else an owner runs.
//
// With no key yet, it is the key. With one, the new key is added to the
// repository beside it (restic's keys each unlock the same master key, so
// every backup stays readable) and becomes the one the daemon uses. A key
// is never removed here: a lost or stolen owner device must not be able to
// lock a machine out of its own backups. Removing the old one is done on
// the machine, with restic.
func (d *Daemon) putKey(w http.ResponseWriter, r *http.Request) {
	if !d.owner(callerOf(r)) {
		fail(w, http.StatusForbidden, errors.New("owners only"))
		return
	}
	if d.source() == nil {
		fail(w, http.StatusBadRequest, errors.New("this machine backs nothing up"))
		return
	}
	var body struct {
		Key string `json:"key"`
		// Label says where it came from, for the apps' list of keys:
		// "Keycard 6e021332" (the card's key UID, the same on every card
		// made from one seed).
		Label string `json:"label"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&body); err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	k := strings.TrimSpace(body.Key)
	if len(k) < 16 || len(k) > 512 || strings.ContainsAny(k, "\r\n") {
		fail(w, http.StatusBadRequest, errors.New("a key is 16 to 512 characters on one line"))
		return
	}
	if !keyLabel.MatchString(body.Label) {
		fail(w, http.StatusBadRequest, errors.New("a label is up to 40 letters, digits, spaces, dots and dashes"))
		return
	}
	how, err := d.SetKey(r.Context(), k)
	if err != nil {
		fail(w, http.StatusConflict, err)
		return
	}
	if body.Label != "" {
		d.labelKey(r.Context(), body.Label)
	}
	if d.repoMade() {
		d.refreshKeys(r.Context())
	}
	d.Log.Info("key set", "by", callerOf(r).name, "how", how)
	writeJSON(w, 200, map[string]string{"key": how})
}

// SetKey makes k this source's key; it says how: "set" (there was none),
// "same", or "added" (beside the one the repository had).
func (d *Daemon) SetKey(ctx context.Context, k string) (string, error) {
	d.mu.Lock()
	if d.running {
		d.mu.Unlock()
		return "", errors.New("a backup is running; try again when it is done")
	}
	d.running = true // no run may start while the key changes under it
	d.mu.Unlock()
	defer func() { d.mu.Lock(); d.running = false; d.mu.Unlock() }()

	old, err := os.ReadFile(d.Key)
	if errors.Is(err, os.ErrNotExist) {
		return "set", writeKey(d.Key, k)
	}
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(string(old)) == k {
		return "same", nil
	}
	if d.self == nil {
		return "", fmt.Errorf("this machine is not on the %s mesh, which it backs up over", d.Cfg.SourceMesh())
	}
	// A repository not made yet has nothing encrypted with the old key.
	if !d.repoMade() {
		return "set", writeKey(d.Key, k)
	}
	if err := d.nodeReady(ctx, d.Cfg.SourceMesh()); err != nil {
		return "", err
	}
	tmp := d.Key + ".new"
	if err := writeKey(tmp, k); err != nil {
		return "", err
	}
	defer os.Remove(tmp)
	if _, err := d.restic(ctx, "key", "add", "--new-password-file", tmp, "--host", d.Mesh.Self(), "--user", "shrooms-backup"); err != nil {
		return "", err
	}
	// Opened with the new key before it replaces the old one.
	if _, err := d.resticWith(ctx, tmp, "cat", "config"); err != nil {
		return "", errors.New("the new key was added but does not open the repository; the old one is kept")
	}
	if err := os.Rename(tmp, d.Key); err != nil {
		return "", err
	}
	return "added", nil
}

func writeKey(path, k string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(k+"\n"), 0o600)
}

// HasKey: this source can back up, and its backups can be read.
func (d *Daemon) HasKey() bool {
	_, err := os.Stat(d.Key)
	return err == nil
}

// refreshKeys reads the keys again and keeps them: reading them takes the
// repository's key files from the mirrors, seconds, and a Basecamp view
// calls synchronously.
func (d *Daemon) refreshKeys(ctx context.Context) ([]KeyInfo, error) {
	keys, err := d.resticKeys(ctx)
	if err != nil {
		return nil, err
	}
	d.mu.Lock()
	d.keys = keys
	d.mu.Unlock()
	return append([]KeyInfo(nil), keys...), nil
}
