package backup

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
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

// fakeMirror answers a source's push and its questions after, with what
// left() says the fetch still lacks.
func fakeMirror(t *testing.T, version int64, left func() (missing int64, adopted bool)) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v1/info":
			writeJSON(w, 200, Info{Name: "scribe"})
		case r.Method == "PUT":
			w.WriteHeader(202)
		default:
			miss, done := left()
			st := MirrorStatus{Source: "k11", Pending: version, MissingBytes: miss}
			if done {
				st.Adopted = version
			}
			writeJSON(w, 200, st)
		}
	}))
}

func pushTo(t *testing.T, srv *httptest.Server, m Map) (*Daemon, error) {
	mesh := &Mesh{}
	mesh.SetPeers("k11", nil)
	cfg := &Config{Meshes: []string{"home"}, Source: &SourceConfig{Presets: []string{"shrooms"}, Mirrors: []string{"scribe"}}}
	cfg.fill()
	d := &Daemon{Cfg: cfg, Log: slog.New(slog.DiscardHandler), Mesh: mesh, client: &http.Client{Timeout: 5 * time.Second},
		PeerURL: func(string) (string, error) { return srv.URL, nil },
		prog:    &Progress{Phase: "mirrors", Mirrors: map[string]MirrorProgress{"scribe": {Bytes: m.Size(false)}}}}
	d.sprs = map[string]map[string]string{}
	return d, d.push(context.Background(), "scribe", "", m)
}

func TestAPushThatKeepsMovingIsNotCutOff(t *testing.T) {
	defer func(s time.Duration) { Stalled = s }(Stalled)
	Stalled = 300 * time.Millisecond
	m := Map{Version: 3, Objects: map[string]Object{"a": {CID: "z1", Size: 1000}}}
	var calls atomic.Int64
	// Slower than Stalled in all, but always moving: 100 bytes a poll.
	srv := fakeMirror(t, 3, func() (int64, bool) {
		n := calls.Add(1)
		return max(1000-100*n, 0), n > 10
	})
	defer srv.Close()
	pollEvery = 50 * time.Millisecond
	defer func() { pollEvery = 5 * time.Second }()
	d, err := pushTo(t, srv, m)
	if err != nil {
		t.Fatalf("a moving fetch was given up on: %v", err)
	}
	if mp := d.prog.Mirrors["scribe"]; mp.Done < 900 {
		t.Errorf("progress never rose: %+v", mp)
	}
}

func TestAPushThatStopsMovingGivesUp(t *testing.T) {
	defer func(s time.Duration) { Stalled = s }(Stalled)
	Stalled = 300 * time.Millisecond
	pollEvery = 50 * time.Millisecond
	defer func() { pollEvery = 5 * time.Second }()
	m := Map{Version: 3, Objects: map[string]Object{"a": {CID: "z1", Size: 1000}}}
	srv := fakeMirror(t, 3, func() (int64, bool) { return 600, false })
	defer srv.Close()
	d, err := pushTo(t, srv, m)
	if err == nil || !strings.Contains(err.Error(), "fetched nothing") {
		t.Fatalf("a stalled fetch: %v", err)
	}
	if mp := d.prog.Mirrors["scribe"]; mp.Done != 400 {
		t.Errorf("progress of a stalled fetch: %+v", mp)
	}
	b, _ := json.Marshal(d.prog)
	if !strings.Contains(string(b), `"bytes":1000`) {
		t.Errorf("progress as the apps get it: %s", b)
	}
}
