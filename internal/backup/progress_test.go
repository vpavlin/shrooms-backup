package backup

import (
	"bytes"
	"strings"
	"testing"
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
