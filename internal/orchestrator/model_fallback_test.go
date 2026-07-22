package orchestrator

import (
	"context"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/state"
)

// ─── Classification (#42) ────────────────────────────────────────────────────

func TestClassifyTerminalKind_ModelUnavailable(t *testing.T) {
	cases := []struct {
		name string
		err  string
		want string
	}{
		{
			name: "anthropic 404 not_found_error naming the model",
			err:  `API Error: 404 {"type":"error","error":{"type":"not_found_error","message":"model: claude-fable-5"}}`,
			want: TerminalKindModelUnavailable,
		},
		{
			name: "invalid model wording",
			err:  "API Error: invalid model name provided",
			want: TerminalKindModelUnavailable,
		},
		{
			name: "unknown model wording",
			err:  "unknown model: claude-fable-9",
			want: TerminalKindModelUnavailable,
		},
		{
			name: "plan restriction naming a registry model ID",
			err:  "claude-fable-5 is not available on your current plan",
			want: TerminalKindModelUnavailable,
		},
		{
			name: "model-specific usage cap naming a display name",
			err:  "You've reached your Fable 5 usage limit — try again later",
			want: TerminalKindModelUnavailable,
		},
		{
			name: "weekly tier cap (Claude Code Max plans)",
			err:  "Opus weekly limit reached",
			want: TerminalKindModelUnavailable,
		},
		// Negatives: the transient kinds keep their backoff-and-retry-same-model
		// behavior (#42 AC) and generic limits keep routing to the quota path.
		{
			name: "overloaded stays api_overloaded",
			err:  "API Error: Overloaded",
			want: TerminalKindApiOverloaded,
		},
		{
			name: "explicit TS quota marker wins over the model heuristic",
			err:  "[rate-limit-quota-exhausted] stall killed waiting for opus bucket",
			want: TerminalKindRateLimitQuotaExhausted,
		},
		{
			name: "account-level usage limit without a model named is NOT a model rejection",
			err:  "You have reached your usage limit. It resets at 3pm.",
			want: "",
		},
		{
			name: "404 without a model reference is NOT a model rejection",
			err:  "gh api returned 404 not found for the pull request",
			want: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ClassifyTerminalKind(tc.err); got != tc.want {
				t.Errorf("ClassifyTerminalKind(%q) = %q, want %q", tc.err, got, tc.want)
			}
		})
	}
}

// ─── Downgrade ladder (#42) ──────────────────────────────────────────────────

func TestRetryEngine_EvaluateDowngrade_WalksTiersDownward(t *testing.T) {
	r := NewRetryEngine(DefaultRetryConfig())

	dg := r.EvaluateDowngrade("fable")
	if !dg.ShouldDowngrade || dg.NewTier != "opus" {
		t.Fatalf("EvaluateDowngrade(fable) = %+v, want fallback to opus", dg)
	}

	// Concrete registry IDs normalize to their tier.
	dg = r.EvaluateDowngrade("claude-fable-5")
	if !dg.ShouldDowngrade || dg.NewTier != "opus" || dg.FromTier != "fable" {
		t.Fatalf("EvaluateDowngrade(claude-fable-5) = %+v, want fable→opus", dg)
	}

	dg = r.EvaluateDowngrade("opus")
	if !dg.ShouldDowngrade || dg.NewTier != "sonnet" {
		t.Fatalf("EvaluateDowngrade(opus) = %+v, want fallback to sonnet", dg)
	}
}

func TestRetryEngine_EvaluateDowngrade_ExhaustedAtHaiku(t *testing.T) {
	r := NewRetryEngine(DefaultRetryConfig())
	dg := r.EvaluateDowngrade("haiku")
	if dg.ShouldDowngrade {
		t.Fatalf("EvaluateDowngrade(haiku) = %+v, want no downgrade (nothing weaker exists)", dg)
	}
	if dg.Reason != "downgrade_ladder_exhausted" {
		t.Errorf("Reason = %q, want downgrade_ladder_exhausted", dg.Reason)
	}
}

func TestRetryEngine_EvaluateDowngrade_NonRegistryModelPassesThrough(t *testing.T) {
	r := NewRetryEngine(DefaultRetryConfig())
	if dg := r.EvaluateDowngrade("gpt-5-codex"); dg.ShouldDowngrade {
		t.Fatalf("EvaluateDowngrade(gpt-5-codex) = %+v, want no downgrade (non-Claude adapter model)", dg)
	}
	if got := r.ApplyDowngrades("gpt-5-codex"); got != "gpt-5-codex" {
		t.Errorf("ApplyDowngrades(gpt-5-codex) = %q, want unchanged", got)
	}
}

func TestRetryEngine_DowngradeIsStickyAcrossStagesAndChains(t *testing.T) {
	r := NewRetryEngine(DefaultRetryConfig())

	// fable rejected → opus, sticky for every later stage regardless of how
	// the stage references the model (tier name or concrete ID).
	r.RecordDowngrade("claude-fable-5", "opus")
	if got := r.ApplyDowngrades("fable"); got != "opus" {
		t.Fatalf("ApplyDowngrades(fable) = %q, want opus", got)
	}
	if got := r.ApplyDowngrades("claude-fable-5"); got != "opus" {
		t.Fatalf("ApplyDowngrades(claude-fable-5) = %q, want opus", got)
	}

	// opus later rejected too → the chain resolves fable all the way to sonnet,
	// and the next downgrade evaluation skips the already-rejected opus rung.
	r.RecordDowngrade("opus", "sonnet")
	if got := r.ApplyDowngrades("fable"); got != "sonnet" {
		t.Fatalf("ApplyDowngrades(fable) after opus rejection = %q, want sonnet (chain)", got)
	}
	if dg := r.EvaluateDowngrade("fable"); !dg.ShouldDowngrade || dg.NewTier != "sonnet" {
		t.Fatalf("EvaluateDowngrade(fable) after opus rejection = %+v, want sonnet (skip rejected opus)", dg)
	}

	// Models below the substitution are untouched.
	if got := r.ApplyDowngrades("haiku"); got != "haiku" {
		t.Errorf("ApplyDowngrades(haiku) = %q, want unchanged", got)
	}
}

func TestRetryEngine_Reset_ClearsDowngrades(t *testing.T) {
	r := NewRetryEngine(DefaultRetryConfig())
	r.RecordDowngrade("fable", "opus")
	r.Reset()
	if got := r.ApplyDowngrades("fable"); got != "fable" {
		t.Errorf("ApplyDowngrades(fable) after Reset = %q, want fable (downgrades are per-run)", got)
	}
	if len(r.Downgrades()) != 0 {
		t.Errorf("Downgrades() after Reset = %v, want empty", r.Downgrades())
	}
}

// ─── Autonomous policy (#42) ─────────────────────────────────────────────────

// A run that terminally fails with model_unavailable (downgrade ladder
// exhausted) is environmental — quota-length backoff, no lifetime-cap
// increment, no queue pause. Mirrors the api-overloaded transient contract.
func TestOnPipelineComplete_ModelUnavailable_EnvironmentalNoPause(t *testing.T) {
	as := &AutonomousScheduler{
		config: AutonomousConfig{MaxConcurrent: 3},
		state: &AutonomousState{
			Status: "running",
			Running: []RunningItem{
				{Repo: "nightgauge/nightgauge", Number: 42, Title: "Fable fallback case"},
			},
			LifetimeIssueFailures: map[string]int{},
		},
		rescanCh:             make(chan struct{}, 1),
		perIssueFailureCount: map[string]int{},
		retryBackoff:         map[string]time.Time{},
	}

	before := time.Now()
	as.onPipelineComplete("nightgauge/nightgauge", 42, false, false,
		TerminalKindModelUnavailable, "claude-fable-5 is not available on your current plan")

	key := "nightgauge/nightgauge#42"

	if got := as.state.LifetimeIssueFailures[key]; got != 0 {
		t.Errorf("LifetimeIssueFailures[%q] = %d, want 0 (environmental, no lifetime-cap increment)", key, got)
	}
	if as.state.Status == "paused" {
		t.Errorf("autonomous paused after model_unavailable; want still running")
	}
	retryAt, ok := as.retryBackoff[key]
	if !ok {
		t.Fatalf("expected retryBackoff[%q] to be set after model_unavailable", key)
	}
	// Quota-length backoff (streamIdleTimeoutBackoff = 1h): model caps reset
	// on Anthropic's rolling windows, so a 5-minute retry would re-fail.
	wait := retryAt.Sub(before)
	if wait < 45*time.Minute || wait > 90*time.Minute {
		t.Errorf("backoff = %v, want ~1h (allowed 45m–90m)", wait)
	}
	if len(as.state.Running) != 0 {
		t.Errorf("expected 0 running after model_unavailable, got %d", len(as.state.Running))
	}
	if len(as.state.Failed) != 1 || as.state.Failed[0].Number != 42 {
		t.Fatalf("expected 1 failed entry for #42, got %+v", as.state.Failed)
	}
}

// ─── IPC-mode fallback flow (#42) ────────────────────────────────────────────

// fallbackMockRunner simulates an IpcStageRunner whose first call is rejected
// by the API with a model-unavailability response: it classifies the error,
// records the sticky downgrade on the shared engine, and sets
// FallbackRecorded (mirroring ipc_stage_runner.go). Subsequent calls succeed.
type fallbackMockRunner struct {
	engine    *RetryEngine
	callCount int
	calls     []StageRunParams
}

func (m *fallbackMockRunner) RunStage(_ context.Context, params StageRunParams) (*StageRunResult, error) {
	m.callCount++
	m.calls = append(m.calls, params)

	if m.callCount == 1 {
		errText := `API Error: 404 {"type":"error","error":{"type":"not_found_error","message":"model: claude-fable-5"}}`
		result := &StageRunResult{ExitCode: 1, ErrorText: errText}
		if ClassifyTerminalKind(errText) == TerminalKindModelUnavailable {
			if dg := m.engine.EvaluateDowngrade(params.Model); dg.ShouldDowngrade {
				m.engine.RecordDowngrade(params.Model, dg.NewTier)
				result.FallbackRecorded = true
				result.FallbackFromModel = params.Model
				result.FallbackToModel = dg.NewTier
			}
		}
		return result, nil
	}
	return &StageRunResult{ExitCode: 0}, nil
}

// TestScheduler_IpcModelFallback verifies the #42 fallback flow for IPC mode,
// mirroring TestScheduler_IpcStageEscalation:
//  1. Stage routed to fable is rejected by the API → runner classifies
//     model_unavailable and records the sticky tier downgrade
//  2. FallbackRecorded=true causes the scheduler to retry the same stage
//  3. On retry, the model resolution reroutes fable → opus via ApplyDowngrades
//  4. Stage succeeds, and a LATER stage requesting fable also resolves opus
//     (sticky for the remainder of the run) — the rejected model is never
//     re-attempted.
func TestScheduler_IpcModelFallback(t *testing.T) {
	engine := NewRetryEngine(DefaultRetryConfig())
	runner := &fallbackMockRunner{engine: engine}

	stage := state.StageFeatureDev
	predictedModel := "fable"

	const maxIter = 5
	for iter := 0; iter < maxIter; iter++ {
		// Scheduler's model resolution: escalation override, then sticky
		// downgrades (scheduler.go model resolution for each dispatch).
		model := predictedModel
		if override := engine.CurrentModel(string(stage)); override != "" {
			model = override
		}
		model = engine.ApplyDowngrades(model)

		result, err := runner.RunStage(context.Background(), StageRunParams{
			Stage:       stage,
			IssueNumber: 42,
			Model:       model,
		})
		if err != nil {
			t.Fatalf("RunStage error: %v", err)
		}

		if result.ExitCode != 0 {
			if result.FallbackRecorded {
				continue // Downgrade recorded by runner; retry same stage
			}
			t.Fatalf("stage failed without fallback recorded on iteration %d", iter)
		}
		break
	}

	if runner.callCount != 2 {
		t.Fatalf("RunStage call count = %d, want 2 (reject, then succeed on fallback)", runner.callCount)
	}
	if runner.calls[0].Model != "fable" {
		t.Errorf("first call model = %q, want fable", runner.calls[0].Model)
	}
	if runner.calls[1].Model != "opus" {
		t.Errorf("second call model = %q, want opus (fallback tier)", runner.calls[1].Model)
	}

	// No upward escalation happened — fallback and escalation are disjoint.
	if got := engine.CurrentModel(string(stage)); got != "" {
		t.Errorf("CurrentModel(feature-dev) = %q, want empty (no escalation recorded)", got)
	}

	// A later stage predicted to fable must ALSO resolve to opus without
	// re-attempting the rejected model (sticky per-run substitution).
	if got := engine.ApplyDowngrades("fable"); got != "opus" {
		t.Errorf("ApplyDowngrades(fable) for a later stage = %q, want opus", got)
	}
}

// ─── Provider-relative downgrade ladder (#56) ────────────────────────────────

func TestRetryEngine_EvaluateDowngrade_CodexWalksProviderLadder(t *testing.T) {
	r := NewRetryEngine(DefaultRetryConfig())

	// gpt-5.5 serves opus+fable for openai; the next distinct model below is
	// the sonnet-band gpt-5.4 — the ladder never "falls" to gpt-5.5 itself.
	dg := r.EvaluateDowngrade("gpt-5.5")
	if !dg.ShouldDowngrade || dg.NewTier != "sonnet" {
		t.Fatalf("EvaluateDowngrade(gpt-5.5) = %+v, want fall to sonnet (gpt-5.4)", dg)
	}

	dg = r.EvaluateDowngrade("gpt-5.4")
	if !dg.ShouldDowngrade || dg.NewTier != "haiku" {
		t.Fatalf("EvaluateDowngrade(gpt-5.4) = %+v, want fall to haiku (gpt-5.4-mini)", dg)
	}

	// The provider's weakest model has nothing to fall to.
	dg = r.EvaluateDowngrade("gpt-5.4-mini")
	if dg.ShouldDowngrade || dg.Reason != "downgrade_ladder_exhausted" {
		t.Fatalf("EvaluateDowngrade(gpt-5.4-mini) = %+v, want exhausted", dg)
	}
}

func TestRetryEngine_EvaluateDowngrade_GeminiMultiBandModels(t *testing.T) {
	r := NewRetryEngine(DefaultRetryConfig())

	// gemini-2.5-pro (opus+fable) falls past itself to the sonnet band.
	dg := r.EvaluateDowngrade("gemini-2.5-pro")
	if !dg.ShouldDowngrade || dg.NewTier != "sonnet" || dg.FromTier != "fable" {
		t.Fatalf("EvaluateDowngrade(gemini-2.5-pro) = %+v, want fable→sonnet (gemini-2.5-flash)", dg)
	}

	// gemini-2.5-flash serves haiku+sonnet — Google's weakest banded model;
	// the haiku rung resolves back to the rejected model, so the ladder is
	// exhausted rather than retrying the same model.
	dg = r.EvaluateDowngrade("gemini-2.5-flash")
	if dg.ShouldDowngrade || dg.Reason != "downgrade_ladder_exhausted" {
		t.Fatalf("EvaluateDowngrade(gemini-2.5-flash) = %+v, want exhausted (nothing weaker at Google)", dg)
	}
}
