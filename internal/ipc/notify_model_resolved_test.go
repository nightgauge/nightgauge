package ipc

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/state"
)

// newTransitionTestServer builds a minimal Server whose real
// pipeline.notifyStageTransition handler is registered and reachable. repo is
// left empty on the params below so the best-effort persist is skipped (no FS
// writes) and analyticsSvc is nil so telemetry emission is a no-op.
func newTransitionTestServer(t *testing.T) (*Server, Handler) {
	t.Helper()
	var buf bytes.Buffer
	s := &Server{
		writer:         &buf,
		methods:        make(map[string]Handler),
		activeRuntimes: make(map[string]*state.RuntimeState),
	}
	s.registerMethods()
	handler := s.methods["pipeline.notifyStageTransition"]
	if handler == nil {
		t.Fatal("pipeline.notifyStageTransition must be registered")
	}
	return s, handler
}

// TestNotifyStageTransition_ModelResolved_NoBeginStage verifies the up-front
// "model-resolved" transition (#367) records the resolved model WITHOUT
// starting the stage clock. BeginStage (which stamps StageStart) is owned by
// the later "running" transition; a second reset would corrupt stage duration.
func TestNotifyStageTransition_ModelResolved_NoBeginStage(t *testing.T) {
	s, handler := newTransitionTestServer(t)
	ctx := context.Background()

	mr := json.RawMessage(`{"repo":"","issueNumber":700,"stage":"feature-dev","status":"model-resolved","model":"claude-fable-5","adapter":"claude"}`)
	if _, err := handler(ctx, mr); err != nil {
		t.Fatalf("model-resolved handler error: %v", err)
	}

	rt := s.activeRuntimes["700"]
	if rt == nil {
		t.Fatal("runtime should have been created by the model-resolved transition")
	}
	if got := rt.StageModel(state.StageFeatureDev); got != "claude-fable-5" {
		t.Errorf("StageModel after model-resolved = %q, want claude-fable-5", got)
	}
	if !rt.StageStart.IsZero() {
		t.Errorf("model-resolved must not start the stage clock; StageStart = %v, want zero", rt.StageStart)
	}

	// The subsequent "running" transition DOES start the clock.
	run := json.RawMessage(`{"repo":"","issueNumber":700,"stage":"feature-dev","status":"running"}`)
	if _, err := handler(ctx, run); err != nil {
		t.Fatalf("running handler error: %v", err)
	}
	if rt.StageStart.IsZero() {
		t.Error("running transition must start the stage clock (BeginStage)")
	}
}

// TestNotifyStageTransition_ModelResolved_LatestWins verifies the concrete
// servedModel on the terminal "complete" transition overrides the up-front
// "model-resolved" value — the Go handler records the model on every
// transition, latest-wins.
func TestNotifyStageTransition_ModelResolved_LatestWins(t *testing.T) {
	s, handler := newTransitionTestServer(t)
	ctx := context.Background()

	seq := []string{
		`{"repo":"","issueNumber":701,"stage":"feature-dev","status":"model-resolved","model":"claude-fable-5","adapter":"claude"}`,
		`{"repo":"","issueNumber":701,"stage":"feature-dev","status":"running","model":"claude-fable-5","adapter":"claude"}`,
		`{"repo":"","issueNumber":701,"stage":"feature-dev","status":"complete","model":"claude-opus-4-8","adapter":"claude"}`,
	}
	for i, raw := range seq {
		if _, err := handler(ctx, json.RawMessage(raw)); err != nil {
			t.Fatalf("transition %d error: %v", i, err)
		}
	}

	rt := s.activeRuntimes["701"]
	if got := rt.StageModel(state.StageFeatureDev); got != "claude-opus-4-8" {
		t.Errorf("StageModel after complete = %q, want claude-opus-4-8 (latest-wins)", got)
	}
}

// TestBuildStageTelemetryEvent_ModelResolved_NoEvent verifies the up-front
// "model-resolved" status emits no platform telemetry event. The contrast with
// "running" (same runID + platform stage) proves it is the status switch's
// default that suppresses it, not the runID/stage guard.
func TestBuildStageTelemetryEvent_ModelResolved_NoEvent(t *testing.T) {
	now := time.Time{}
	if _, ok := buildStageTelemetryEvent("run-1", "nightgauge/nightgauge", 1, "feature-dev", "model-resolved", "", 0, 0, 0, 0, 0, now); ok {
		t.Error("model-resolved must not emit a platform telemetry event")
	}
	if _, ok := buildStageTelemetryEvent("run-1", "nightgauge/nightgauge", 1, "feature-dev", "running", "", 0, 0, 0, 0, 0, now); !ok {
		t.Error("running should emit a platform telemetry event (guard sanity check)")
	}
}
