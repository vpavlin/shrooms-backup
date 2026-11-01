package backup

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// Settings are what the apps may change: everything but the mesh, the node's
// ports and binaries, which are the installer's.
type Settings struct {
	Owners   []string      `json:"owners"`
	AlwaysOn bool          `json:"always_on"`
	Mirror   bool          `json:"mirror"`
	QuotaGB  int64         `json:"quota_gb"`
	Source   *SourceConfig `json:"source"`
}

// putConfig saves the settings an owner sends and applies them. A machine
// that becomes a source or stops being one, or whose mirror role or quota
// changes, restarts: those are its node's and its repository's, set when it
// starts. The rest apply at once.
func (d *Daemon) putConfig(w http.ResponseWriter, r *http.Request) {
	if !d.owner(callerOf(r)) {
		fail(w, http.StatusForbidden, errors.New("owners only"))
		return
	}
	if d.ConfigPath == "" {
		fail(w, http.StatusServiceUnavailable, errors.New("this daemon has no config file to save to"))
		return
	}
	var s Settings
	dec := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&s); err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	d.mu.Lock()
	next := *d.Cfg
	d.mu.Unlock()
	next.Owners, next.AlwaysOn, next.Mirror, next.Node.QuotaGB, next.Source = s.Owners, s.AlwaysOn, s.Mirror, s.QuotaGB, s.Source
	if err := next.fill(); err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	if err := saveConfig(d.ConfigPath, &next); err != nil {
		fail(w, http.StatusInternalServerError, err)
		return
	}
	d.mu.Lock()
	cur := d.Cfg
	restart := cur.Mirror != next.Mirror || cur.Node.QuotaGB != next.Node.QuotaGB || (cur.Source == nil) != (next.Source == nil)
	if !restart {
		cur.Owners, cur.AlwaysOn, cur.Source = next.Owners, next.AlwaysOn, next.Source
	}
	d.mu.Unlock()
	d.Log.Info("settings changed", "by", callerOf(r).name, "restart", restart)
	if restart && d.Restart != nil {
		writeJSON(w, http.StatusAccepted, map[string]bool{"saved": true, "restart": true})
		go d.Restart()
		return
	}
	select {
	case d.wake <- struct{}{}:
	default:
	}
	writeJSON(w, 200, map[string]bool{"saved": true, "restart": restart})
}

// saveConfig writes the config whole, beside the old one, then over it.
func saveConfig(path string, c *Config) error {
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	tmp := filepath.Join(filepath.Dir(path), "."+filepath.Base(path)+".tmp")
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// summarize reads restic backup --json: the summary line into the run.
// False if there was none, so no snapshot was made.
func summarize(out string, run *Run) bool {
	sc := bufio.NewScanner(strings.NewReader(out))
	sc.Buffer(make([]byte, 64<<10), 4<<20)
	for sc.Scan() {
		var m struct {
			Type     string `json:"message_type"`
			New      int64  `json:"files_new"`
			Changed  int64  `json:"files_changed"`
			Files    int64  `json:"total_files_processed"`
			Bytes    int64  `json:"total_bytes_processed"`
			Added    int64  `json:"data_added"`
			Packed   *int64 `json:"data_added_packed"`
			Snapshot string `json:"snapshot_id"`
		}
		if json.Unmarshal(sc.Bytes(), &m) != nil || m.Type != "summary" || m.Snapshot == "" {
			continue
		}
		run.Files, run.FilesNew, run.FilesMod, run.Bytes = m.Files, m.New, m.Changed, m.Bytes
		run.DataAdded = m.Added
		if m.Packed != nil {
			run.DataAdded = *m.Packed
		}
		run.Added = humanBytes(run.DataAdded)
		return true
	}
	return false
}

func humanBytes(n int64) string {
	units := []string{"B", "KiB", "MiB", "GiB", "TiB"}
	f, i := float64(n), 0
	for f >= 1024 && i < len(units)-1 {
		f /= 1024
		i++
	}
	if i == 0 {
		return fmt.Sprintf("%d B", n)
	}
	return fmt.Sprintf("%.3f %s", f, units[i])
}
