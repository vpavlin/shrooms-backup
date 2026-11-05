package backup

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

// restic's progress lines go to the run's progress, the rest is kept for the
// summary, however the output is cut into writes.
func TestResticProgressLines(t *testing.T) {
	d := &Daemon{prog: &Progress{Phase: "reading"}}
	var out bytes.Buffer
	w := &statusWriter{out: &out, status: d.resticStatus}
	in := `{"message_type":"status","percent_done":0.5,"total_files":10,"files_done":5,"total_bytes":2000,"bytes_done":1000}` + "\n" +
		`{"message_type":"summary","snapshot_id":"abc","files_new":10}` + "\n" +
		`{"message_type":"status","total_files":10,"files_done":7,"total_bytes":2000,"bytes_done":1400}`
	for i := 0; i < len(in); i += 7 {
		w.Write([]byte(in[i:min(i+7, len(in))]))
	}
	w.Flush()
	if p := d.prog; p.Files != 10 || p.FilesDone != 7 || p.Bytes != 2000 || p.BytesDone != 1400 {
		t.Errorf("progress %+v", p)
	}
	if strings.Contains(out.String(), "status") || !strings.Contains(out.String(), `"snapshot_id":"abc"`) {
		t.Errorf("kept %q", out.String())
	}
	var run Run
	if !summarize(out.String(), &run) || run.FilesNew != 10 {
		t.Errorf("the summary was lost: %+v", run)
	}
}

// A mirror's rate is over the last minutes of sending, with the time left
// at it; too short a window says nothing rather than something wild.
func TestARateAndTimeLeft(t *testing.T) {
	s := &SourceRepo{senders: map[string]*sender{}}
	now := time.Now()
	s.senders["scribe"] = &sender{sent: []sample{{now.Add(-10 * time.Second), 0}, {now, 100 << 20}}}
	if r := s.Rate("scribe"); r != 0 {
		t.Errorf("a rate from 10 s: %v", r)
	}
	s.senders["scribe"].sent = []sample{{now.Add(-100 * time.Second), 0}, {now.Add(-50 * time.Second), 150e6}, {now, 150e6}}
	r := s.Rate("scribe")
	if r < 2.9e6 || r > 3.1e6 {
		t.Errorf("rate %v, want about 3 MB/s", r)
	}
	mp := withRate(MirrorProgress{Bytes: 150e9, Done: 30e9}, r, 120e9)
	if mp.Rate < 2.9e6 || mp.SecondsLeft < 38000 || mp.SecondsLeft > 42000 {
		t.Errorf("%+v", mp)
	}
	if mp := withRate(MirrorProgress{}, 0, 5); mp.Rate != 0 || mp.SecondsLeft != 0 {
		t.Errorf("no rate, yet %+v", mp)
	}
}
