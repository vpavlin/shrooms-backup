package backup

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

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
	how, err := d.SetKey(r.Context(), k)
	if err != nil {
		fail(w, http.StatusConflict, err)
		return
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
	// A repository not made yet has nothing encrypted with the old key.
	if _, ok := d.self.Map().Objects["config"]; !ok {
		return "set", writeKey(d.Key, k)
	}
	if err := d.nodeReady(ctx); err != nil {
		return "", err
	}
	tmp := d.Key + ".new"
	if err := writeKey(tmp, k); err != nil {
		return "", err
	}
	defer os.Remove(tmp)
	if _, err := d.restic(ctx, "key", "add", "--new-password-file", tmp); err != nil {
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
