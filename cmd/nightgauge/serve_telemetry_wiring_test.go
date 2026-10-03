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
		name      string
		optedIn   bool
		cfg       *config.Config
		editorEnv string
		want      bool
	}{
		{"opted in, telemetry on, editor on", true, telemetryOn, "on", true},
		{"opted in, telemetry unset, no editor", true, &config.Config{}, "", true},
		{"opted in, no config, no editor", true, nil, "", true},
		{"not opted in", false, telemetryOn, "on", false},
		{"platform.telemetry.enabled false", true, telemetryOff, "on", false},
		{"editor withdrew consent", true, telemetryOn, "off", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			opts := serveTelemetryOptions(resolvedPlatformConfig{OptedIn: tc.optedIn}, tc.cfg, tc.editorEnv)
			s := ipc.NewServer(nil, opts...)
			if got := s.TelemetryAllowed(); got != tc.want {
				t.Errorf("TelemetryAllowed() = %v, want %v", got, tc.want)
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

	var allowed atomic.Bool
	svc := schedulerTelemetryService(pc, allowed.Load)
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

	allowed.Store(true)
	svc.PushPipelineRun(context.Background(), record)
	svc.SyncQueue(context.Background(), []platform.QueueSyncItem{{IssueNumber: 7}})
	deadline := time.Now().Add(3 * time.Second)
	for posts.Load() < 2 {
		if time.Now().After(deadline) {
			t.Fatalf("the scheduler's telemetry sent %d of 2 requests with the gate open", posts.Load())
		}
		time.Sleep(10 * time.Millisecond)
	}
}
