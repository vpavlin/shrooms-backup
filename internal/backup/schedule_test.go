package backup

import (
	"log/slog"
	"testing"
	"time"
)

func at(s string) time.Time {
	t, err := time.ParseInLocation("2006-01-02 15:04", s, time.Local)
	if err != nil {
		panic(err)
	}
	return t
}

func TestLastTime(t *testing.T) {
	times := []string{"03:30", "15:30"}
	for _, c := range []struct{ now, want string }{
		{"2026-11-09 10:00", "2026-11-09 03:30"},
		{"2026-11-09 16:00", "2026-11-09 15:30"},
		{"2026-11-09 02:00", "2026-11-08 15:30"},
		{"2026-11-09 03:30", "2026-11-09 03:30"},
	} {
		if got := lastTime(at(c.now), times); !got.Equal(at(c.want)) {
			t.Errorf("at %s: %v, want %s", c.now, got, c.want)
		}
	}
	if !lastTime(at("2026-11-09 10:00"), nil).IsZero() {
		t.Error("no times, yet a last time")
	}
}

// A machine that was off at 03:30 runs that backup when it is back; one that
// ran it (or tried) does not run it again.
func TestMissedRun(t *testing.T) {
	d := &Daemon{Cfg: &Config{Mesh: "office", Source: &SourceConfig{Times: []string{"03:30", "15:30"}}}, Log: slog.New(slog.DiscardHandler)}
	now := at("2026-11-09 10:00")
	d.runs = []Run{{Started: at("2026-11-08 15:30")}}
	if !d.missedRun(now) {
		t.Error("off at 03:30, back at 10:00: not counted as missed")
	}
	d.runs = append(d.runs, Run{Started: at("2026-11-09 08:12"), Error: "no mirror answered"})
	if d.missedRun(now) {
		t.Error("tried at 08:12 (and failed): retried every minute rather than at 15:30")
	}
	d.runs = []Run{{Started: at("2026-11-09 03:30")}}
	if d.missedRun(now) {
		t.Error("ran at 03:30: counted as missed")
	}
	d.runs = nil
	if !d.missedRun(now) {
		t.Error("never ran: not counted as missed")
	}
}

// A missed run waits until a mirror is online on the source's mesh.
func TestMirrorOnline(t *testing.T) {
	mesh := &Mesh{}
	d := &Daemon{Cfg: &Config{Mesh: "office", Source: &SourceConfig{Mirrors: []string{"scribe", "vps"}}}, Mesh: mesh, Log: slog.New(slog.DiscardHandler)}
	mesh.SetPeers("laptop", []Peer{{Name: "scribe", Mesh: "office", Overlay: "fd00::1", Online: false},
		{Name: "pi5", Mesh: "office", Overlay: "fd00::2", Online: true}})
	if d.mirrorOnline() {
		t.Error("no mirror online, yet online")
	}
	mesh.SetPeers("laptop", []Peer{{Name: "vps", Mesh: "home", Overlay: "fd00::3", Online: true}})
	if d.mirrorOnline() {
		t.Error("a mirror online on another mesh counted")
	}
	mesh.SetPeers("laptop", []Peer{{Name: "vps", Mesh: "office", Overlay: "fd00::3", Online: true}})
	if !d.mirrorOnline() {
		t.Error("a mirror online, yet not")
	}
}
