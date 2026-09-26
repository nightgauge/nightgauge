package execution

import (
	"bufio"
	"context"
	"database/sql"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/execution/adapters"
	"github.com/nightgauge/nightgauge/internal/terminalkind"
)

// endpointFakeAdapter spawns a stage that prints one step_start event and then
// streams whatever the fake model endpoint at url sends, the way a CLI waiting
// on a model request does. It declares an idle-stream bound through the
// StreamIdleBound hook.
type endpointFakeAdapter struct {
	url   string
	bound time.Duration
}

func (endpointFakeAdapter) Name() string { return "endpoint-fake" }
func (a endpointFakeAdapter) BuildCommand(adapters.RunOptions) (string, []string, map[string]string) {
	script := fmt.Sprintf(`printf '{"type":"step_start"}\n'; exec curl -sN --max-time 60 %q`, a.url)
	return "sh", []string{"-c", script}, nil
}
func (endpointFakeAdapter) UsesStdin() bool { return false }
func (endpointFakeAdapter) Agentic() bool   { return true }
func (a endpointFakeAdapter) StreamIdleBound(adapters.RunOptions) (string, time.Duration, string, bool) {
	return "fake-endpoint", a.bound, "", true
}

func requireCurl(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("curl"); err != nil {
		t.Skip("curl not on PATH")
	}
}

func runEndpointStage(t *testing.T, a endpointFakeAdapter, issue int) (*adapters.RunResult, time.Duration) {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".nightgauge", "worktrees", fmt.Sprintf("nightgauge-issue-%d", issue)), 0o755); err != nil {
		t.Fatal(err)
	}
	m := NewManager(root, a)
	start := time.Now()
	result, err := m.RunStage(context.Background(), StageOptions{
		Repo:        "nightgauge/nightgauge",
		IssueNumber: issue,
		Stage:       "feature-dev",
		Timeout:     45 * time.Second,
	})
	if err != nil {
		t.Fatalf("RunStage err = %v", err)
	}
	return result, time.Since(start)
}

// TestRunStage_ModelStreamStalled_EndpointNeverAnswers is #2176's acceptance
// test: an endpoint that accepts the connection and never answers stops the
// stage within the configured bound, long before the stage timeout, and the
// stage is classified model_stream_stalled, not stall_kill.
func TestRunStage_ModelStreamStalled_EndpointNeverAnswers(t *testing.T) {
	requireCurl(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	accepted := make(chan struct{}, 1)
	var mu sync.Mutex
	var conns []net.Conn
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			conns = append(conns, c) // held open, never answered
			mu.Unlock()
			select {
			case accepted <- struct{}{}:
			default:
			}
		}
	}()
	t.Cleanup(func() {
		ln.Close()
		mu.Lock()
		for _, c := range conns {
			c.Close()
		}
		mu.Unlock()
	})

	bound := time.Second
	result, elapsed := runEndpointStage(t, endpointFakeAdapter{url: "http://" + ln.Addr().String() + "/v1/chat/completions", bound: bound}, 2176)

	select {
	case <-accepted:
	default:
		t.Fatal("the fake endpoint never saw the request")
	}
	if result.ExitCode == 0 {
		t.Fatal("ExitCode = 0, want a failure for a stalled model request")
	}
	if !strings.Contains(result.Stderr, ModelStreamStalledMarker) || !strings.Contains(result.Stderr, "endpoint fake-endpoint") {
		t.Fatalf("stderr = %q, want the model-stream-stalled notice naming the endpoint", result.Stderr)
	}
	if strings.Contains(result.Stderr, stageTimeoutMarker) {
		t.Fatalf("stderr = %q, the stage timeout must not also claim it", result.Stderr)
	}
	// Stopped after the bound, and well within the stage timeout: the bound,
	// a poll and the SIGTERM are all it takes.
	if elapsed < bound || elapsed > 15*time.Second {
		t.Fatalf("stage took %v, want between the %v bound and 15s", elapsed, bound)
	}
	if kind := terminalkind.Classify("exit -1: " + strings.TrimSpace(result.Stderr)); kind != "model_stream_stalled" {
		t.Fatalf("classified %q, want model_stream_stalled", kind)
	}
}

// TestRunStage_ModelStreamStalled_SlowStreamIsNotStopped: an endpoint that is
// slow but keeps streaming events, each well inside the bound, over a total
// time several times the bound, is left alone.
func TestRunStage_ModelStreamStalled_SlowStreamIsNotStopped(t *testing.T) {
	requireCurl(t)
	bound := time.Second
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fl := w.(http.Flusher)
		fl.Flush()
		bw := bufio.NewWriter(w)
		for i := 0; i < 16; i++ { // 16 x 200ms = 3.2s, over three bounds
			time.Sleep(200 * time.Millisecond)
			fmt.Fprintf(bw, "{\"type\":\"text\",\"n\":%d}\n", i)
			bw.Flush()
			fl.Flush()
		}
	})}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { srv.Close() })

	result, elapsed := runEndpointStage(t, endpointFakeAdapter{url: "http://" + ln.Addr().String() + "/v1/chat/completions", bound: bound}, 2177)
	if result.ExitCode != 0 || strings.Contains(result.Stderr, ModelStreamStalledMarker) {
		t.Fatalf("exit %d, stderr = %q, want a slow but streaming stage left alone", result.ExitCode, result.Stderr)
	}
	if elapsed < 3*bound {
		t.Fatalf("stage took %v, want it to have streamed past three bounds", elapsed)
	}
	if got := strings.Count(result.Stdout, `"type":"text"`); got != 16 {
		t.Fatalf("stdout carried %d events, want 16", got)
	}
}

// TestModelStreamWatchdog_SessionProgress: past the bound on the stream, a
// running tool or a recent part update in the session database is progress;
// a database quiet for the bound is not.
func TestModelStreamWatchdog_SessionProgress(t *testing.T) {
	now := time.Now()
	stale := now.Add(-2 * time.Minute)
	for name, tc := range map[string]struct {
		p    sessionProgress
		err  error
		fire bool
	}{
		"tool running":   {p: sessionProgress{toolRunning: true, lastUpdate: stale}},
		"recent update":  {p: sessionProgress{lastUpdate: now.Add(-10 * time.Second)}},
		"quiet database": {p: sessionProgress{lastUpdate: stale}, fire: true},
		"empty database": {p: sessionProgress{}, fire: true},
		"unreadable":     {err: os.ErrNotExist, fire: true},
	} {
		w := newModelStreamWatchdog("ep", time.Minute, "dir")
		w.lastEvent.Store(stale.UnixNano())
		w.session = func(string) (sessionProgress, error) { return tc.p, tc.err }
		if got := w.check(now); got != tc.fire {
			t.Errorf("%s: fired = %v, want %v", name, got, tc.fire)
		}
		if w.hasFired() != tc.fire {
			t.Errorf("%s: hasFired = %v", name, w.hasFired())
		}
	}
	// Under the bound nothing is read at all.
	w := newModelStreamWatchdog("ep", time.Minute, "dir")
	w.session = func(string) (sessionProgress, error) {
		t.Fatal("read the database under the bound")
		return sessionProgress{}, nil
	}
	if w.check(time.Now()) {
		t.Fatal("fired under the bound")
	}
	// Only a JSON object on stdout is a stream event.
	w.lastEvent.Store(stale.UnixNano())
	w.observe([]byte("plain text"))
	if time.Unix(0, w.lastEvent.Load()) != time.Unix(0, stale.UnixNano()) {
		t.Fatal("a non-JSON line counted as an event")
	}
	w.observe([]byte(`{"type":"text"}`))
	if !time.Unix(0, w.lastEvent.Load()).After(stale) {
		t.Fatal("a JSON event did not count")
	}
	var nilWatch *modelStreamWatchdog
	nilWatch.observe([]byte("{}"))
	if nilWatch.hasFired() {
		t.Fatal("nil watchdog fired")
	}
}

// TestReadSessionProgress reads OpenCode's part table as 1.18.32 lays it out.
func TestReadSessionProgress(t *testing.T) {
	dir := t.TempDir()
	db, err := sql.Open("sqlite", filepath.Join(dir, "opencode.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec("CREATE TABLE `part` (`id` text PRIMARY KEY, `message_id` text NOT NULL, `session_id` text NOT NULL, `time_created` integer NOT NULL, `time_updated` integer NOT NULL, `data` text NOT NULL)"); err != nil {
		t.Fatal(err)
	}
	insert := func(id string, updated int64, data string) {
		if _, err := db.Exec("INSERT INTO part VALUES (?, 'm', 's', ?, ?, ?)", id, updated, updated, data); err != nil {
			t.Fatal(err)
		}
	}
	insert("a", 1790140887133, `{"type":"tool","state":{"status":"completed"}}`)
	insert("b", 1790140880000, `{"type":"text"}`)
	p, err := readSessionProgress(dir)
	if err != nil {
		t.Fatal(err)
	}
	if p.toolRunning || !p.lastUpdate.Equal(time.UnixMilli(1790140887133)) {
		t.Fatalf("progress = %+v", p)
	}
	insert("c", 1790140890000, `{"type":"tool","state":{"status":"running"}}`)
	if p, err = readSessionProgress(dir); err != nil || !p.toolRunning {
		t.Fatalf("progress = %+v, %v; want a running tool", p, err)
	}
	if _, err := readSessionProgress(t.TempDir()); err == nil {
		t.Fatal("no database: want an error")
	}
}
