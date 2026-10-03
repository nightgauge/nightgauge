package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/config"
	"github.com/nightgauge/nightgauge/internal/ipc"
	"github.com/nightgauge/nightgauge/internal/platform"
	"github.com/nightgauge/nightgauge/internal/state"
)

// TestServeTelemetryOptions pins serve's wiring of the run telemetry
// consent: each of the three halves — the cloud opt-in, the machine tier's
// platform.telemetry.enabled and the consent of the editor that started the
// daemon — can withhold it on its own.
func TestServeTelemetryOptions(t *testing.T) {
	off := false
	on := true
	telemetryOff := &config.Config{Telemetry: &config.TelemetryConfig{Enabled: &off}}
	telemetryOn := &config.Config{Telemetry: &config.TelemetryConfig{Enabled: &on}}

	cases := []struct {
		name       string
		optedIn    bool
		cfg        *config.Config
		editorEnv  string
		streamsEnv string
		want       bool
		wantRuns   bool
	}{
		{"opted in, telemetry on, editor on", true, telemetryOn, "on", "pipeline-run,trace", true, true},
		{"opted in, telemetry unset, no editor", true, &config.Config{}, "", "", true, true},
		{"opted in, no config, no editor", true, nil, "", "", true, true},
		{"not opted in", false, telemetryOn, "on", "", false, false},
		{"platform.telemetry.enabled false", true, telemetryOff, "on", "", false, false},
		{"editor withdrew consent", true, telemetryOn, "off", "", false, false},
		{"editor's pipeline-run stream off", true, telemetryOn, "on", "health,trace", true, false},
		{"editor allows no stream", true, telemetryOn, "on", "none", true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			opts := serveTelemetryOptions(resolvedPlatformConfig{OptedIn: tc.optedIn}, tc.cfg, tc.editorEnv, tc.streamsEnv)
			s := ipc.NewServer(nil, opts...)
			if got := s.TelemetryAllowed(); got != tc.want {
				t.Errorf("TelemetryAllowed() = %v, want %v", got, tc.want)
			}
			if got := s.RunRecordsAllowed(); got != tc.wantRuns {
				t.Errorf("RunRecordsAllowed() = %v, want %v", got, tc.wantRuns)
			}
		})
	}
}

// TestSchedulerTelemetryService_AsksTheGate pins the scheduler's half of the
// consent: its pushes go through the gate serve hands it (the IPC server's
// TelemetryAllowed), so an autonomous run sends nothing the interactive path
// would not.
func TestSchedulerTelemetryService_AsksTheGate(t *testing.T) {
	var posts atomic.Int32
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/health":
			fmt.Fprint(w, `{"status":"ok"}`)
		default:
			posts.Add(1)
			w.WriteHeader(http.StatusAccepted)
			fmt.Fprint(w, `{"accepted":1,"rejected":[]}`)
		}
	}))
	defer mock.Close()

	pc, err := platform.NewClient(platform.Config{BaseURL: mock.URL, PollInterval: time.Hour, AgentID: "machine-1"})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if !pc.ProbeHealth(context.Background()) {
		t.Fatal("precondition: the client must read online")
	}

	var allowed, runsAllowed atomic.Bool
	runsAllowed.Store(true)
	svc := schedulerTelemetryService(pc, allowed.Load, runsAllowed.Load)
	record := state.V2RunRecord{
		SchemaVersion: "2",
		RecordType:    "run",
		IssueNumber:   7,
		Repo:          "acme/app",
		StartedAt:     "2026-04-01T10:00:00Z",
		CompletedAt:   "2026-04-01T10:05:00Z",
		Outcome:       "complete",
	}
	svc.PushPipelineRun(context.Background(), record)
	svc.EmitPipelineEvent(context.Background(), platform.PipelineEvent{EventType: "stage_started", RunID: "run-1", IssueNumber: 7, Stage: "issue-pickup"})
	svc.SyncQueue(context.Background(), []platform.QueueSyncItem{{IssueNumber: 7}})
	time.Sleep(150 * time.Millisecond)
	if n := posts.Load(); n != 0 {
		t.Fatalf("the scheduler's telemetry sent %d requests with the gate closed, want 0", n)
	}

	// The editor's pipeline-run stream off: the run record stays, the queue
	// snapshot goes.
	allowed.Store(true)
	runsAllowed.Store(false)
	svc.PushPipelineRun(context.Background(), record)
	svc.SyncQueue(context.Background(), []platform.QueueSyncItem{{IssueNumber: 7}})
	waitForPosts(t, &posts, 1)
	time.Sleep(100 * time.Millisecond)
	if n := posts.Load(); n != 1 {
		t.Fatalf("with the pipeline-run stream off the scheduler sent %d requests, want only the queue snapshot", n)
	}

	runsAllowed.Store(true)
	svc.PushPipelineRun(context.Background(), record)
	waitForPosts(t, &posts, 2)
}

// waitForPosts waits until the mock has seen want requests.
func waitForPosts(t *testing.T, posts *atomic.Int32, want int32) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for posts.Load() < want {
		if time.Now().After(deadline) {
			t.Fatalf("the scheduler's telemetry sent %d of %d requests", posts.Load(), want)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
