// Tests that the interactive terminal funnel (pipeline.notifyComplete) pushes
// the completed-run record to the platform telemetry sink
// (POST /v1/telemetry/pipeline-run) — the interactive mirror of the autonomous
// scheduler's recordOutcome. Without this, interactive runs never populated the
// platform's usage_events / cost_events / stage.snapshot analytics tables (nor
// pipeline_runs.cost), so the dashboard's "Tokens today" and cost widgets read
// empty for extension-driven runs.
package ipc

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/platform"
)

func TestNotifyComplete_PushesPipelineRunToPlatform(t *testing.T) {
	var pushCount int32
	var body atomic.Value // []byte
	pushed := make(chan struct{}, 1)

	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v1/health":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		case r.URL.Path == "/v1/telemetry/pipeline-run" && r.Method == http.MethodPost:
			b, _ := io.ReadAll(r.Body)
			body.Store(b)
			atomic.AddInt32(&pushCount, 1)
			w.WriteHeader(http.StatusAccepted)
			select {
			case pushed <- struct{}{}:
			default:
			}
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer mock.Close()

	pc, err := platform.NewClient(platform.Config{BaseURL: mock.URL})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	// StartHealthPolling runs an immediate synchronous check, flipping the
	// client online so PushPipelineRun's IsOnline gate posts rather than buffers.
	pc.StartHealthPolling(context.Background())
	defer pc.StopHealthPolling()
	if !pc.IsOnline() {
		t.Fatal("client should be online after the initial health check")
	}

	dir := t.TempDir()
	s := NewServer(nil, WithPlatformClient(pc), WithWorkspaceRoot(dir))

	transition := s.methods["pipeline.notifyStageTransition"]
	complete := s.methods["pipeline.notifyComplete"]

	if _, err := transition(t.Context(), []byte(`{"repo":"nightgauge/acmeapp","issueNumber":777,"stage":"feature-dev","status":"running"}`)); err != nil {
		t.Fatalf("notifyStageTransition(running): %v", err)
	}
	if _, err := complete(t.Context(), []byte(`{"repo":"nightgauge/acmeapp","issueNumber":777,"success":true,"totalDurationMs":1000}`)); err != nil {
		t.Fatalf("notifyComplete: %v", err)
	}

	// PushPipelineRun is fire-and-forget (goroutine) — wait for the POST.
	select {
	case <-pushed:
	case <-time.After(3 * time.Second):
		t.Fatal("expected POST /v1/telemetry/pipeline-run within 3s of notifyComplete")
	}

	if got := atomic.LoadInt32(&pushCount); got != 1 {
		t.Errorf("pipeline-run push count = %d, want 1", got)
	}

	raw, _ := body.Load().([]byte)
	// The batch envelope carries the issue number + repo of the completed run.
	if !strings.Contains(string(raw), `"issueNumber":777`) {
		t.Errorf("pushed body missing issueNumber 777; body=%s", truncateForTest(raw))
	}
	if !strings.Contains(string(raw), `"repo":"nightgauge/acmeapp"`) {
		t.Errorf("pushed body missing repo; body=%s", truncateForTest(raw))
	}
	// Sanity: the wire is a BARE top-level array of records (#261) — the
	// platform's canonical routes strict-reject any envelope object.
	var env []json.RawMessage
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("pushed body is not a bare JSON record array: %v", err)
	}
	if len(env) != 1 {
		t.Errorf("record array len = %d, want 1", len(env))
	}
}

func truncateForTest(b []byte) string {
	const max = 400
	if len(b) > max {
		return string(b[:max]) + "…"
	}
	return string(b)
}
