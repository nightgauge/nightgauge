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
	"fmt"
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
	s := NewServer(nil, WithPlatformClient(pc), WithWorkspaceRoot(dir), WithTelemetryPolicy(true, true))

	transition := s.methods["pipeline.notifyStageTransition"]
	complete := s.methods["pipeline.notifyComplete"]

	if _, err := transition(t.Context(), []byte(`{"repo":"nightgauge/acmeapp","issueNumber":777,"stage":"feature-dev","status":"running","runId":"01900309-0000-7000-8000-000000000777"}`)); err != nil {
		t.Fatalf("notifyStageTransition(running): %v", err)
	}
	if _, err := complete(t.Context(), []byte(`{"repo":"nightgauge/acmeapp","issueNumber":777,"success":true,"totalDurationMs":1000,"runId":"01900309-0000-7000-8000-000000000777"}`)); err != nil {
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

// TestNotifyComplete_SendsNothingWithoutConsent pins the consent check on the
// interactive path. A signed-in session builds a platform client on its own,
// so a client existing is no consent: with no cloud opt-in, with
// platform.telemetry.enabled false, or with the editor's consent withdrawn,
// neither the completed run nor a live stage event leaves the machine.
func TestNotifyComplete_SendsNothingWithoutConsent(t *testing.T) {
	cases := map[string]struct {
		opts    []ServerOption
		consent *bool // platform.setTelemetryConsent, when the editor reports one
	}{
		"no policy":                   {},
		"signed in, no cloud opt-in":  {opts: []ServerOption{WithTelemetryPolicy(false, true)}},
		"telemetry off in config":     {opts: []ServerOption{WithTelemetryPolicy(true, false)}},
		"editor consent off at spawn": {opts: []ServerOption{WithTelemetryPolicy(true, true), WithEditorTelemetry("off")}},
		"editor consent withdrawn":    {opts: []ServerOption{WithTelemetryPolicy(true, true), WithEditorTelemetry("on")}, consent: new(bool)},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			var posts int32
			mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/v1/health" {
					w.Header().Set("Content-Type", "application/json")
					_, _ = w.Write([]byte(`{"status":"ok"}`))
					return
				}
				atomic.AddInt32(&posts, 1)
				w.WriteHeader(http.StatusAccepted)
			}))
			defer mock.Close()

			pc, err := platform.NewClient(platform.Config{BaseURL: mock.URL})
			if err != nil {
				t.Fatalf("NewClient: %v", err)
			}
			pc.StartHealthPolling(context.Background())
			defer pc.StopHealthPolling()

			opts := append([]ServerOption{WithPlatformClient(pc), WithWorkspaceRoot(t.TempDir())}, tc.opts...)
			s := NewServer(nil, opts...)
			if _, err := s.methods["platform.setSessionToken"](t.Context(), []byte(`{"token":"jwt.signed.in"}`)); err != nil {
				t.Fatalf("setSessionToken: %v", err)
			}
			if tc.consent != nil {
				raw, _ := json.Marshal(PlatformSetTelemetryConsentParams{Enabled: *tc.consent})
				if _, err := s.methods["platform.setTelemetryConsent"](t.Context(), raw); err != nil {
					t.Fatalf("setTelemetryConsent: %v", err)
				}
			}

			runID := "01900309-0000-7000-8000-000000000778"
			if _, err := s.methods["pipeline.notifyStageTransition"](t.Context(), []byte(`{"repo":"nightgauge/acmeapp","issueNumber":778,"stage":"feature-dev","status":"running","runId":"`+runID+`"}`)); err != nil {
				t.Fatalf("notifyStageTransition: %v", err)
			}
			if _, err := s.methods["pipeline.notifyComplete"](t.Context(), []byte(`{"repo":"nightgauge/acmeapp","issueNumber":778,"success":true,"totalDurationMs":1000,"runId":"`+runID+`"}`)); err != nil {
				t.Fatalf("notifyComplete: %v", err)
			}
			if n := s.getAnalyticsSvc().FlushBuffered(t.Context()); n != 0 {
				t.Errorf("a flush sent %d buffered items", n)
			}
			time.Sleep(300 * time.Millisecond)
			if n := atomic.LoadInt32(&posts); n != 0 {
				t.Errorf("%d requests reached the platform without consent, want 0", n)
			}
		})
	}
}

// TestSetTelemetryConsent_RestoresSending pins the other direction: the editor
// turning telemetry back on lets the next completed run through.
func TestSetTelemetryConsent_RestoresSending(t *testing.T) {
	pushed := make(chan struct{}, 4)
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/health":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		case "/v1/telemetry/pipeline-run":
			pushed <- struct{}{}
			w.WriteHeader(http.StatusAccepted)
		default:
			w.WriteHeader(http.StatusAccepted)
		}
	}))
	defer mock.Close()

	pc, err := platform.NewClient(platform.Config{BaseURL: mock.URL})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	pc.StartHealthPolling(context.Background())
	defer pc.StopHealthPolling()

	s := NewServer(nil, WithPlatformClient(pc), WithWorkspaceRoot(t.TempDir()),
		WithTelemetryPolicy(true, true), WithEditorTelemetry("off"))
	if s.TelemetryAllowed() {
		t.Fatal("editor consent off at spawn must close the gate")
	}
	if _, err := s.methods["platform.setTelemetryConsent"](t.Context(), []byte(`{"enabled":true}`)); err != nil {
		t.Fatalf("setTelemetryConsent: %v", err)
	}
	if !s.TelemetryAllowed() {
		t.Fatal("editor consent restored, opted in, telemetry on: the gate must be open")
	}

	runID := "01900309-0000-7000-8000-000000000779"
	if _, err := s.methods["pipeline.notifyStageTransition"](t.Context(), []byte(`{"repo":"nightgauge/acmeapp","issueNumber":779,"stage":"feature-dev","status":"running","runId":"`+runID+`"}`)); err != nil {
		t.Fatalf("notifyStageTransition: %v", err)
	}
	if _, err := s.methods["pipeline.notifyComplete"](t.Context(), []byte(`{"repo":"nightgauge/acmeapp","issueNumber":779,"success":true,"totalDurationMs":1000,"runId":"`+runID+`"}`)); err != nil {
		t.Fatalf("notifyComplete: %v", err)
	}
	select {
	case <-pushed:
	case <-time.After(3 * time.Second):
		t.Fatal("expected the completed run to be posted once consent was restored")
	}
}

// TestNotifyComplete_PipelineRunStreamOff pins #1796's stream toggle: with the
// editor's pipeline-run stream off, a completed interactive run posts no run
// record, at spawn (the environment) or after a change (the IPC method), and
// the record goes again once the stream is back on.
func TestNotifyComplete_PipelineRunStreamOff(t *testing.T) {
	for name, streamsAtSpawn := range map[string]string{
		"off at spawn":     "health,recommendation,trace",
		"turned off later": "",
		"no stream at all": "none",
	} {
		t.Run(name, func(t *testing.T) {
			pushed := make(chan struct{}, 4)
			mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/v1/health":
					w.Header().Set("Content-Type", "application/json")
					_, _ = w.Write([]byte(`{"status":"ok"}`))
				case "/v1/telemetry/pipeline-run":
					pushed <- struct{}{}
					w.WriteHeader(http.StatusAccepted)
				default:
					w.WriteHeader(http.StatusAccepted)
				}
			}))
			defer mock.Close()

			pc, err := platform.NewClient(platform.Config{BaseURL: mock.URL})
			if err != nil {
				t.Fatalf("NewClient: %v", err)
			}
			pc.StartHealthPolling(context.Background())
			defer pc.StopHealthPolling()

			s := NewServer(nil, WithPlatformClient(pc), WithWorkspaceRoot(t.TempDir()),
				WithTelemetryPolicy(true, true), WithEditorTelemetry("on"),
				WithEditorTelemetryStreams(streamsAtSpawn))
			if streamsAtSpawn == "" {
				if !s.RunRecordsAllowed() {
					t.Fatal("precondition: an unreported stream set allows run records")
				}
				if _, err := s.methods["platform.setTelemetryConsent"](t.Context(),
					[]byte(`{"enabled":true,"streams":["health","trace"]}`)); err != nil {
					t.Fatalf("setTelemetryConsent: %v", err)
				}
			}
			if !s.TelemetryAllowed() || s.RunRecordsAllowed() {
				t.Fatalf("TelemetryAllowed %v RunRecordsAllowed %v, want telemetry on and run records off",
					s.TelemetryAllowed(), s.RunRecordsAllowed())
			}

			complete := func(issue int) {
				t.Helper()
				runID := fmt.Sprintf("01900309-0000-7000-8000-%012d", issue)
				if _, err := s.methods["pipeline.notifyStageTransition"](t.Context(), []byte(fmt.Sprintf(`{"repo":"nightgauge/acmeapp","issueNumber":%d,"stage":"feature-dev","status":"running","runId":"%s"}`, issue, runID))); err != nil {
					t.Fatalf("notifyStageTransition: %v", err)
				}
				if _, err := s.methods["pipeline.notifyComplete"](t.Context(), []byte(fmt.Sprintf(`{"repo":"nightgauge/acmeapp","issueNumber":%d,"success":true,"totalDurationMs":1000,"runId":"%s"}`, issue, runID))); err != nil {
					t.Fatalf("notifyComplete: %v", err)
				}
			}

			complete(780)
			select {
			case <-pushed:
				t.Fatal("a run record was posted with the pipeline-run stream off")
			case <-time.After(300 * time.Millisecond):
			}

			if _, err := s.methods["platform.setTelemetryConsent"](t.Context(),
				[]byte(`{"enabled":true,"streams":["pipeline-run"]}`)); err != nil {
				t.Fatalf("setTelemetryConsent: %v", err)
			}
			complete(781)
			select {
			case <-pushed:
			case <-time.After(3 * time.Second):
				t.Fatal("expected the run record once the stream was back on")
			}
		})
	}
}
