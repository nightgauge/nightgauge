package ipc

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/nightgauge/nightgauge/internal/state"
)

// The #888 defect in one sentence: a complete, successful run on the path the
// VSCode extension uses recorded StageModels and StageAdapters and left every
// other #580 envelope field null, so the routing corpus learned nothing from
// the runs that worked.
//
// These tests assert the VALUES a completed stage carries on the default
// (claude) adapter. Asserting that the fields merely exist passes today and
// proves nothing, which is what the issue's acceptance criteria call out.

// TestNotifyStageTransition_CompletedStageCarriesEnvelope is the headline
// regression: the exact shape observed in the field — feature-dev completing
// on claude/claude-sonnet-5 — must now carry thinking, selection mode and a
// served model.
func TestNotifyStageTransition_CompletedStageCarriesEnvelope(t *testing.T) {
	s, handler := newTransitionTestServer(t)
	ctx := context.Background()
	runID := newTestRunID()

	for i, raw := range []string{
		`{"repo":"","issueNumber":888,"stage":"feature-dev","status":"model-resolved","model":"sonnet","adapter":"claude","runId":"` + runID + `"}`,
		`{"repo":"","issueNumber":888,"stage":"feature-dev","status":"running","model":"sonnet","adapter":"claude","runId":"` + runID + `"}`,
		`{"repo":"","issueNumber":888,"stage":"feature-dev","status":"complete","model":"claude-sonnet-5","adapter":"claude",` +
			`"servedModel":"claude-sonnet-5","servedThinking":"on","runId":"` + runID + `"}`,
	} {
		if _, err := handler(ctx, json.RawMessage(raw)); err != nil {
			t.Fatalf("transition %d: %v", i, err)
		}
	}

	rt := s.activeRuntimes[runID].rs
	stage := state.StageFeatureDev

	if got := rt.StageModel(stage); got != "claude-sonnet-5" {
		t.Errorf("StageModel = %q, want claude-sonnet-5", got)
	}
	// claude-sonnet-5 declares behavior.thinking_default: on, so neither of
	// the resolver's documented absence conditions holds. Null here was the
	// bug.
	if got := rt.StageThinkingState(stage); got != "on" {
		t.Errorf("StageThinking = %q, want \"on\" — the model declares thinking_default:on (#888)", got)
	}
	if got := rt.StageServedModel(stage); got != "claude-sonnet-5" {
		t.Errorf("StageServedModel = %q, want claude-sonnet-5 (#888)", got)
	}
	// modelRoutingMode is total, so this is an attribution Go always has.
	if got := rt.StageModelSelectionMode(stage); got == "" {
		t.Error("StageModelSelectionMode is empty; model_routing.mode always resolves (#888)")
	}
}

// TestNotifyStageTransition_ServedFieldsAreNotFabricated pins the honesty
// half. The extension's `model` is `servedModel ?? modelDecision.model`, so a
// stage whose executor reported no served model still sends a `model`.
// Recording THAT into StageServedModels would manufacture an observation —
// the served_* contract is "observed, or absent".
func TestNotifyStageTransition_ServedFieldsAreNotFabricated(t *testing.T) {
	s, handler := newTransitionTestServer(t)
	ctx := context.Background()
	runID := newTestRunID()

	// No servedModel/servedEffort/servedThinking on the wire at all.
	raw := `{"repo":"","issueNumber":889,"stage":"feature-planning","status":"complete","model":"claude-sonnet-5","adapter":"claude","runId":"` + runID + `"}`
	if _, err := handler(ctx, json.RawMessage(raw)); err != nil {
		t.Fatalf("complete: %v", err)
	}

	rt := s.activeRuntimes[runID].rs
	stage := state.StageFeaturePlanning

	if got := rt.StageServedModel(stage); got != "" {
		t.Errorf("StageServedModel = %q, want empty — the executor reported no served model, "+
			"and `model` is a request-or-served fallback that must not stand in for one (#888)", got)
	}
	// Effort has no first-party evidence on the claude adapter, so it must
	// stay absent rather than inherit a registry default.
	if got := rt.StageEffort(stage); got != "" {
		t.Errorf("StageEffort = %q, want empty — Go has no first-party effort evidence "+
			"on the claude adapter (#580, #888)", got)
	}
	// Derived attribution is still recorded: it comes from the registry and
	// config, not from a claim the executor never made.
	if got := rt.StageThinkingState(stage); got != "on" {
		t.Errorf("StageThinking = %q, want \"on\" — derived, not fabricated (#888)", got)
	}
}

// TestNotifyStageTransition_ServedValueSupersedesRequested mirrors the
// scheduler: when the executor served something other than what was asked
// for, the headline field tells the truth about what ran while the served_*
// field keeps the raw observation.
func TestNotifyStageTransition_ServedValueSupersedesRequested(t *testing.T) {
	s, handler := newTransitionTestServer(t)
	ctx := context.Background()
	runID := newTestRunID()

	raw := `{"repo":"","issueNumber":890,"stage":"feature-validate","status":"complete","model":"claude-sonnet-5","adapter":"claude",` +
		`"servedModel":"claude-sonnet-5","servedThinking":"off","servedEffort":"low","runId":"` + runID + `"}`
	if _, err := handler(ctx, json.RawMessage(raw)); err != nil {
		t.Fatalf("complete: %v", err)
	}

	rt := s.activeRuntimes[runID].rs
	stage := state.StageFeatureValidate

	if got := rt.StageServedThinkingState(stage); got != "off" {
		t.Errorf("StageServedThinking = %q, want off", got)
	}
	// The registry default is "on"; the executor said "off". The headline
	// field must follow the executor, not the registry.
	if got := rt.StageThinkingState(stage); got != "off" {
		t.Errorf("StageThinking = %q, want off — a served value supersedes the derived default (#888)", got)
	}
	if got := rt.StageServedEffort(stage); got != "low" {
		t.Errorf("StageServedEffort = %q, want low", got)
	}
	// Effort had no derived value, but the executor reported one first-hand,
	// so the headline effort is now genuinely known.
	if got := rt.StageEffort(stage); got != "low" {
		t.Errorf("StageEffort = %q, want low", got)
	}
}

// TestNotifyStageTransition_BookendRecordsNoEnvelope guards the other
// direction: the pipeline-start/finish bookends and skipped stages dispatch
// no model, and must not acquire an envelope derived from an empty one.
func TestNotifyStageTransition_BookendRecordsNoEnvelope(t *testing.T) {
	s, handler := newTransitionTestServer(t)
	ctx := context.Background()
	runID := newTestRunID()

	raw := `{"repo":"","issueNumber":891,"stage":"pr-create","status":"complete","runId":"` + runID + `"}`
	if _, err := handler(ctx, json.RawMessage(raw)); err != nil {
		t.Fatalf("complete: %v", err)
	}

	rt := s.activeRuntimes[runID].rs
	stage := state.StagePRCreate

	if got := rt.StageThinkingState(stage); got != "" {
		t.Errorf("StageThinking = %q, want empty — a deterministic stage dispatched no model (#888)", got)
	}
	if got := rt.StageModelSelectionMode(stage); got != "" {
		t.Errorf("StageModelSelectionMode = %q, want empty — nothing selected a model here (#888)", got)
	}
}
