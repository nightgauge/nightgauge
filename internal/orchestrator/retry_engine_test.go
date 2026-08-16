package orchestrator

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/nightgauge/nightgauge/internal/state"
)

func TestRetryEngine_EvaluateBacktrack_NoFile(t *testing.T) {
	engine := NewRetryEngine(DefaultRetryConfig())
	decision, err := engine.EvaluateBacktrack("/nonexistent/feedback.json")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if decision.ShouldBacktrack {
		t.Error("expected no backtrack for missing file")
	}
}

func TestRetryEngine_EvaluateBacktrack_BlockingSignal(t *testing.T) {
	engine := NewRetryEngine(DefaultRetryConfig())

	dir := t.TempDir()
	feedbackFile := filepath.Join(dir, "feedback-42.json")
	content := `{
		"schema_version": "1.0",
		"issue_number": 42,
		"signals": [{
			"signal_type": "PLAN_REVISION_NEEDED",
			"emitted_by_stage": "feature-dev",
			"backtrack_target_stage": "feature-planning",
			"rationale": "API does not exist",
			"evidence": ["missing function"],
			"severity": "blocking"
		}]
	}`
	os.WriteFile(feedbackFile, []byte(content), 0644)

	decision, err := engine.EvaluateBacktrack(feedbackFile)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !decision.ShouldBacktrack {
		t.Error("expected backtrack for blocking signal")
	}
	if decision.TargetStage != state.StageFeaturePlanning {
		t.Errorf("expected target stage feature-planning, got %s", decision.TargetStage)
	}
	if decision.SignalType != "PLAN_REVISION_NEEDED" {
		t.Errorf("expected signal type PLAN_REVISION_NEEDED, got %s", decision.SignalType)
	}
}

func TestRetryEngine_EvaluateBacktrack_MaxBacktracksExceeded(t *testing.T) {
	cfg := DefaultRetryConfig()
	cfg.MaxBacktracks = 1
	engine := NewRetryEngine(cfg)
	engine.RecordBacktrack("feature-dev", "feature-planning", "")

	dir := t.TempDir()
	feedbackFile := filepath.Join(dir, "feedback-42.json")
	content := `{
		"schema_version": "1.0",
		"issue_number": 42,
		"signals": [{
			"signal_type": "SCOPE_DISCOVERED",
			"emitted_by_stage": "feature-dev",
			"backtrack_target_stage": "feature-planning",
			"rationale": "too many files",
			"evidence": [],
			"severity": "blocking"
		}]
	}`
	os.WriteFile(feedbackFile, []byte(content), 0644)

	decision, err := engine.EvaluateBacktrack(feedbackFile)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if decision.ShouldBacktrack {
		t.Error("expected backtrack to be blocked by limit")
	}
	if !decision.LimitReached {
		t.Error("expected LimitReached to be true")
	}
}

func TestRetryEngine_EvaluateBacktrack_OscillationDetected(t *testing.T) {
	engine := NewRetryEngine(DefaultRetryConfig())
	// Record a prior backtrack on the same edge
	engine.RecordBacktrack("feature-dev", "feature-planning", "")

	dir := t.TempDir()
	feedbackFile := filepath.Join(dir, "feedback-42.json")
	content := `{
		"schema_version": "1.0",
		"issue_number": 42,
		"signals": [{
			"signal_type": "PLAN_REVISION_NEEDED",
			"emitted_by_stage": "feature-dev",
			"backtrack_target_stage": "feature-planning",
			"rationale": "same issue again",
			"evidence": [],
			"severity": "blocking"
		}]
	}`
	os.WriteFile(feedbackFile, []byte(content), 0644)

	decision, err := engine.EvaluateBacktrack(feedbackFile)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if decision.ShouldBacktrack {
		t.Error("expected backtrack to be blocked by oscillation")
	}
	if !decision.OscillationBlocked {
		t.Error("expected OscillationBlocked to be true")
	}
}

func TestRetryEngine_EvaluateBacktrack_IgnoresEscalationSignals(t *testing.T) {
	engine := NewRetryEngine(DefaultRetryConfig())

	dir := t.TempDir()
	feedbackFile := filepath.Join(dir, "feedback-42.json")
	content := `{
		"schema_version": "1.0",
		"issue_number": 42,
		"signals": [{
			"signal_type": "MODEL_ESCALATION_NEEDED",
			"emitted_by_stage": "feature-dev",
			"backtrack_target_stage": "feature-dev",
			"rationale": "model too weak",
			"evidence": [],
			"severity": "blocking"
		}]
	}`
	os.WriteFile(feedbackFile, []byte(content), 0644)

	decision, err := engine.EvaluateBacktrack(feedbackFile)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if decision.ShouldBacktrack {
		t.Error("expected MODEL_ESCALATION_NEEDED to be ignored by backtrack evaluator")
	}
}

func TestRetryEngine_EvaluateEscalation(t *testing.T) {
	engine := NewRetryEngine(DefaultRetryConfig())

	// Should escalate from sonnet to opus
	decision := engine.EvaluateEscalation("feature-dev", "sonnet")
	if !decision.ShouldEscalate {
		t.Error("expected escalation from sonnet")
	}
	if decision.NewModel != "opus" {
		t.Errorf("expected opus, got %s", decision.NewModel)
	}
}

func TestRetryEngine_EvaluateEscalation_AtCeiling(t *testing.T) {
	engine := NewRetryEngine(DefaultRetryConfig())

	decision := engine.EvaluateEscalation("feature-dev", "opus")
	if decision.ShouldEscalate {
		t.Error("expected no escalation at opus ceiling")
	}
	if !decision.LimitReached {
		t.Error("expected LimitReached at ceiling")
	}
}

func TestRetryEngine_EvaluateEscalation_MaxPerStage(t *testing.T) {
	cfg := DefaultRetryConfig()
	cfg.MaxEscalationsPerStage = 1
	engine := NewRetryEngine(cfg)
	engine.RecordEscalation("feature-dev", "opus")

	decision := engine.EvaluateEscalation("feature-dev", "sonnet")
	if decision.ShouldEscalate {
		t.Error("expected escalation blocked by per-stage limit")
	}
	if !decision.LimitReached {
		t.Error("expected LimitReached")
	}
}

func TestRetryEngine_NextModel(t *testing.T) {
	engine := NewRetryEngine(DefaultRetryConfig())

	tests := []struct {
		current string
		want    string
		wantOk  bool
	}{
		{"haiku", "sonnet", true},
		{"sonnet", "opus", true},
		{"opus", "", false},
		{"unknown", "", false},
	}

	for _, tt := range tests {
		got, ok := engine.NextModel(tt.current)
		if got != tt.want || ok != tt.wantOk {
			t.Errorf("NextModel(%q) = (%q, %v), want (%q, %v)", tt.current, got, ok, tt.want, tt.wantOk)
		}
	}
}

func TestRetryEngine_Reset(t *testing.T) {
	engine := NewRetryEngine(DefaultRetryConfig())
	engine.RecordBacktrack("feature-dev", "feature-planning", "")
	engine.RecordEscalation("feature-dev", "opus")

	if engine.BacktrackCount() != 1 {
		t.Error("expected backtrack count 1 before reset")
	}

	engine.Reset()

	if engine.BacktrackCount() != 0 {
		t.Error("expected backtrack count 0 after reset")
	}
	if engine.CurrentModel("feature-dev") != "" {
		t.Error("expected empty current model after reset")
	}
}

// TestRetryEngine_Reset_ClearsConflictEdges locks the #4072 review fix: the
// non-issue-scoped conflict-redispatch budget MUST be cleared on Reset, or it
// leaks across runs/issues (the RetryEngine is reused for every issue) and
// silently denies a later issue its first conflict re-dispatch.
func TestRetryEngine_Reset_ClearsConflictEdges(t *testing.T) {
	cfg := DefaultRetryConfig()
	cfg.MaxConflictRedispatch = 2
	engine := NewRetryEngine(cfg)
	feedbackFile := writeConflictFeedback(t, t.TempDir())

	// Exhaust the conflict edge budget (2 traversals).
	for i := 0; i < 2; i++ {
		engine.RecordBacktrack("pr-merge", "feature-dev", "CONFLICT_RESOLUTION_NEEDED")
	}
	if d, _ := engine.EvaluateConflictBacktrack(feedbackFile); d.ShouldBacktrack || !d.LimitReached {
		t.Fatalf("expected conflict edge exhausted before reset, got %+v", d)
	}

	engine.Reset()

	// After reset, a fresh issue's first conflict must rewind again.
	d, err := engine.EvaluateConflictBacktrack(feedbackFile)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !d.ShouldBacktrack || d.LimitReached {
		t.Errorf("conflict edge budget must be cleared by Reset; got %+v", d)
	}
}

func TestRetryEngine_NextModel_EmptyCurrentModel(t *testing.T) {
	engine := NewRetryEngine(DefaultRetryConfig())

	// Empty model should escalate to opus (next after sonnet in ladder)
	next, ok := engine.NextModel("")
	if !ok {
		t.Fatal("expected escalation from empty model")
	}
	if next != "opus" {
		t.Errorf("expected opus from empty model, got %q", next)
	}
}

func TestRetryEngine_NextModel_UnknownModel(t *testing.T) {
	engine := NewRetryEngine(DefaultRetryConfig())

	// Unknown model not in ladder — should not escalate
	next, ok := engine.NextModel("gpt-4")
	if ok {
		t.Errorf("expected no escalation for unknown model, got %q", next)
	}
}

func TestRetryEngine_EscalationTriggersOnEmptyModel(t *testing.T) {
	engine := NewRetryEngine(DefaultRetryConfig())

	// Simulate a stage failure with empty model (the bug we're fixing)
	decision := engine.EvaluateEscalation("feature-dev", "")
	if !decision.ShouldEscalate {
		t.Fatal("expected escalation when current model is empty")
	}
	if decision.NewModel != "opus" {
		t.Errorf("expected opus, got %q", decision.NewModel)
	}
}

func TestRetryEngine_EscalationSonnetToOpus(t *testing.T) {
	engine := NewRetryEngine(DefaultRetryConfig())

	decision := engine.EvaluateEscalation("feature-dev", "sonnet")
	if !decision.ShouldEscalate {
		t.Fatal("expected escalation from sonnet")
	}
	if decision.NewModel != "opus" {
		t.Errorf("expected opus, got %q", decision.NewModel)
	}
}

func TestRetryEngine_EscalationOpusCeiling(t *testing.T) {
	engine := NewRetryEngine(DefaultRetryConfig())

	decision := engine.EvaluateEscalation("feature-dev", "opus")
	if decision.ShouldEscalate {
		t.Error("expected no escalation from opus (ceiling)")
	}
	if !decision.LimitReached {
		t.Error("expected LimitReached for opus ceiling")
	}
}

// TestRetryEngine_ConcurrentAccess exercises the mutex protection added in
// Issue #3198. The test must pass under `go test -race`; any regression that
// drops a mutex will be caught by the detector.
func TestRetryEngine_ConcurrentAccess(t *testing.T) {
	cfg := DefaultRetryConfig()
	cfg.MaxBacktracks = 1000          // generous so RecordBacktrack does not become a no-op
	cfg.MaxEscalationsPerStage = 1000 // same — keep the path hot
	engine := NewRetryEngine(cfg)

	// Pre-create a feedback file shared by all goroutines for EvaluateBacktrack.
	dir := t.TempDir()
	feedbackFile := filepath.Join(dir, "feedback.json")
	feedback := `{
		"schema_version": "1.0",
		"issue_number": 3198,
		"signals": [{
			"signal_type": "PLAN_REVISION_NEEDED",
			"emitted_by_stage": "feature-dev",
			"backtrack_target_stage": "feature-planning",
			"rationale": "concurrent access test",
			"evidence": [],
			"severity": "blocking"
		}]
	}`
	if err := os.WriteFile(feedbackFile, []byte(feedback), 0o644); err != nil {
		t.Fatal(err)
	}

	const goroutines = 50
	const iterations = 100
	stages := []string{"feature-planning", "feature-dev", "feature-validate", "pr-create"}
	models := []string{"haiku", "sonnet", "opus"}

	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func(id int) {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				stage := stages[(id+j)%len(stages)]
				model := models[(id+j)%len(models)]
				switch (id + j) % 6 {
				case 0:
					_, _ = engine.EvaluateBacktrack(feedbackFile)
				case 1:
					engine.RecordBacktrack(stage, "feature-planning", "")
				case 2:
					_ = engine.BacktrackCount()
					_ = engine.CurrentModel(stage)
				case 3:
					_ = engine.EvaluateEscalation(stage, model)
				case 4:
					engine.RecordEscalation(stage, model)
				case 5:
					if j%50 == 0 {
						engine.Reset()
					} else {
						_, _ = engine.NextModel(model)
					}
				}
			}
		}(i)
	}
	wg.Wait()

	if engine.BacktrackCount() < 0 {
		t.Errorf("BacktrackCount should not be negative, got %d", engine.BacktrackCount())
	}
}

func TestLoadLatestRetro(t *testing.T) {
	dir := t.TempDir()
	retroDir := filepath.Join(dir, ".nightgauge", "retros")
	if err := os.MkdirAll(retroDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Write a retro file
	retro := `{
		"issue_number": 139,
		"failed_stage": "feature-dev",
		"findings": [{
			"category": "stall",
			"severity": "high",
			"summary": "Process stalled during test generation",
			"evidence": ["exceeded 3000s threshold"],
			"recommendation": "Split into smaller batches"
		}]
	}`
	retroFile := filepath.Join(retroDir, "2026-03-26_139_retro.json")
	if err := os.WriteFile(retroFile, []byte(retro), 0o644); err != nil {
		t.Fatal(err)
	}

	result := loadLatestRetro(dir, 139, "feature-dev")
	if result == "" {
		t.Fatal("expected retro findings, got empty")
	}
	if !strings.Contains(result, "stall") {
		t.Errorf("expected 'stall' in findings, got: %s", result)
	}
	if !strings.Contains(result, "Split into smaller batches") {
		t.Errorf("expected recommendation in findings, got: %s", result)
	}
}

func TestLoadLatestRetro_NoFile(t *testing.T) {
	dir := t.TempDir()
	result := loadLatestRetro(dir, 999, "feature-dev")
	if result != "" {
		t.Errorf("expected empty for missing retro, got: %s", result)
	}
}

// writeConflictFeedback writes a feedback-{N}.json containing a single
// CONFLICT_RESOLUTION_NEEDED signal (pr-merge → feature-dev), the shape the
// conflict-recovery loop emits.
func writeConflictFeedback(t *testing.T, dir string) string {
	t.Helper()
	feedbackFile := filepath.Join(dir, "feedback-7.json")
	content := `{
		"schema_version": "1.1",
		"issue_number": 7,
		"signals": [{
			"signal_type": "CONFLICT_RESOLUTION_NEEDED",
			"emitted_by_stage": "pr-merge",
			"backtrack_target_stage": "feature-dev",
			"rationale": "rebase conflict",
			"evidence": ["src/foo.go"],
			"severity": "blocking"
		}]
	}`
	if err := os.WriteFile(feedbackFile, []byte(content), 0644); err != nil {
		t.Fatalf("write feedback: %v", err)
	}
	return feedbackFile
}

// TestRetryEngine_GenericBacktrackSkipsConflictSignal locks the #4072 ownership
// split: the generic EvaluateBacktrack (used by the post-stage "stage succeeded"
// and stall-rewind sites) must IGNORE CONFLICT_RESOLUTION_NEEDED signals so it
// never re-consumes a lingering conflict signal and self-loops feature-dev.
func TestRetryEngine_GenericBacktrackSkipsConflictSignal(t *testing.T) {
	engine := NewRetryEngine(DefaultRetryConfig())
	feedbackFile := writeConflictFeedback(t, t.TempDir())

	decision, err := engine.EvaluateBacktrack(feedbackFile)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if decision.ShouldBacktrack {
		t.Error("generic EvaluateBacktrack must NOT act on a conflict signal")
	}
}

// TestRetryEngine_ConflictBacktrackBoundedByEdgeCount confirms the conflict edge
// is bounded by MaxConflictRedispatch (per-edge count), NOT the oscillation block
// + global MaxBacktracks. With the bound at 2 the first two evaluations rewind
// and the third declines with LimitReached — even though MaxBacktracks=1 would
// have capped a generic edge at a single traversal (#4072 review).
func TestRetryEngine_ConflictBacktrackBoundedByEdgeCount(t *testing.T) {
	cfg := DefaultRetryConfig()
	cfg.MaxBacktracks = 1 // a generic edge would be capped at 1
	cfg.MaxConflictRedispatch = 2
	engine := NewRetryEngine(cfg)
	feedbackFile := writeConflictFeedback(t, t.TempDir())

	for i := 1; i <= 2; i++ {
		decision, err := engine.EvaluateConflictBacktrack(feedbackFile)
		if err != nil {
			t.Fatalf("eval %d: %v", i, err)
		}
		if !decision.ShouldBacktrack {
			t.Fatalf("eval %d: conflict edge must rewind within bound, got %+v", i, decision)
		}
		if decision.TargetStage != state.StageFeatureDev {
			t.Errorf("eval %d: target = %s, want feature-dev", i, decision.TargetStage)
		}
		// Record the traversal as the scheduler does (stage == pr-merge at the
		// recovery-resume site, so the edge key matches the evaluator's).
		engine.RecordBacktrack("pr-merge", "feature-dev", decision.SignalType)
	}

	// Third evaluation: bound exhausted → decline with LimitReached.
	decision, err := engine.EvaluateConflictBacktrack(feedbackFile)
	if err != nil {
		t.Fatalf("eval 3: %v", err)
	}
	if decision.ShouldBacktrack {
		t.Error("conflict edge must decline after MaxConflictRedispatch traversals")
	}
	if !decision.LimitReached {
		t.Error("declined conflict edge must report LimitReached")
	}
}

// ── #606: same-model effort descent on fully-collapsed providers ─────────────

// TestEvaluateDowngrade_FullyCollapsedProviderDescendsEffort is the #532
// runtime resolution: on xai — where every ladder rung is grok-4.6 — a
// model-unavailable rejection descends EFFORT within the one model instead of
// exhausting, and the decision carries the envelope of the accepted rung.
func TestEvaluateDowngrade_FullyCollapsedProviderDescendsEffort(t *testing.T) {
	engine := NewRetryEngine(DefaultRetryConfig())
	dg := engine.EvaluateDowngrade("grok-4.6")
	if !dg.ShouldDowngrade {
		t.Fatalf("xai must descend, got %+v", dg)
	}
	if dg.FromTier != "fable" || dg.NewTier != "opus" {
		t.Fatalf("descent must step one band (fable → opus), got %+v", dg)
	}
	if !dg.SameModelDescent || dg.NewModelID != "grok-4.6" || dg.NewEffort != "high" {
		t.Fatalf("decision must carry the same-model envelope (grok-4.6@high), got %+v", dg)
	}
	if dg.Reason != "model_unavailable_effort_descent" {
		t.Fatalf("descent must be distinguishable from a cross-model fallback, got %q", dg.Reason)
	}
	if dg.DescentEffort() != "high" {
		t.Fatalf("DescentEffort must expose the sticky effort, got %q", dg.DescentEffort())
	}
}

// TestEvaluateDowngrade_DescentChainsThroughTheLadder pins the repeat case:
// after fable → opus (grok-4.6@high) is recorded, a rejection of the
// substituted tier descends again (opus → sonnet, grok-4.6@medium) — the
// provider-aware evaluation resolves the band within xai.
func TestEvaluateDowngrade_DescentChainsThroughTheLadder(t *testing.T) {
	engine := NewRetryEngine(DefaultRetryConfig())
	first := engine.EvaluateDowngrade("grok-4.6")
	engine.RecordDowngrade("grok-4.6", first)

	second := engine.EvaluateDowngradeForProvider("opus", "xai")
	if !second.ShouldDowngrade || !second.SameModelDescent {
		t.Fatalf("substituted tier must descend again on xai, got %+v", second)
	}
	if second.NewTier != "sonnet" || second.NewEffort != "medium" {
		t.Fatalf("second descent must be grok-4.6@medium on sonnet, got %+v", second)
	}
	engine.RecordDowngrade("opus", second)

	// The sticky chain reroutes fable all the way down, and the effort of the
	// FINAL tier is what StickyEffort answers with.
	if got := engine.ApplyDowngrades("fable"); got != "sonnet" {
		t.Fatalf("ApplyDowngrades(fable) = %q, want sonnet", got)
	}
	if got := engine.StickyEffort(DowngradeProviderForAdapter("grok"), "sonnet"); got != "medium" {
		t.Fatalf("StickyEffort(xai, sonnet) = %q, want medium", got)
	}
}

// TestEvaluateDowngrade_PartiallyCollapsedProviderKeepsSameModelSkip PINS the
// #606 1(d) decision: on a PARTIALLY-collapsed provider the same-model rungs
// are the SYNTHESIZED effort points the PROVENANCE note on
// routing.CandidateLadder flags — no registry field declares them as a band's
// serving envelope, and the provider has a real weaker MODEL to fall to, so
// the model ladder stays the honest downgrade and the same-model skip stays.
func TestEvaluateDowngrade_PartiallyCollapsedProviderKeepsSameModelSkip(t *testing.T) {
	engine := NewRetryEngine(DefaultRetryConfig())

	// google: flash serves sonnet AND haiku. A flash rejection finds only
	// flash's own synthesized haiku rung below it — skipped, so exhausted.
	dg := engine.EvaluateDowngrade("gemini-2.5-flash")
	if dg.ShouldDowngrade || dg.Reason != "downgrade_ladder_exhausted" {
		t.Fatalf("partially-collapsed same-model rung must stay skipped, got %+v", dg)
	}
	if dg.SameModelDescent {
		t.Fatalf("no descent may be reported on a partially-collapsed provider: %+v", dg)
	}

	// openai: sol serves fable AND opus, but sonnet is a DIFFERENT model —
	// the cross-model fallback stays the downgrade, never an effort descent.
	dg = engine.EvaluateDowngrade("gpt-5.6-sol")
	if !dg.ShouldDowngrade || dg.NewTier != "sonnet" || dg.SameModelDescent {
		t.Fatalf("partially-collapsed provider must fall to its real weaker model, got %+v", dg)
	}
	if dg.Reason != "model_unavailable_fallback" {
		t.Fatalf("cross-model fallback reason changed: %+v", dg)
	}
	if dg.DescentEffort() != "" {
		t.Fatalf("cross-model downgrades must record NO sticky effort, got %q", dg.DescentEffort())
	}
}

// TestRecordDowngrade_CrossModelRecordsNoStickyEffort guards the wire-effort
// chain of every post-downgrade anthropic stage: only a same-model descent
// may substitute effort.
func TestRecordDowngrade_CrossModelRecordsNoStickyEffort(t *testing.T) {
	engine := NewRetryEngine(DefaultRetryConfig())
	dg := engine.EvaluateDowngrade("opus")
	if !dg.ShouldDowngrade || dg.SameModelDescent {
		t.Fatalf("anthropic opus must cross-model downgrade, got %+v", dg)
	}
	engine.RecordDowngrade("opus", dg)
	if got := engine.StickyEffort(DowngradeProviderForAdapter("claude"), dg.NewTier); got != "" {
		t.Fatalf("cross-model downgrade left a sticky effort %q on %s", got, dg.NewTier)
	}
}

// TestRetryEngine_ResetClearsStickyEfforts: the RetryEngine is reused across
// runs; a leaked effort substitution would silently rewrite a later run's
// wire effort.
func TestRetryEngine_ResetClearsStickyEfforts(t *testing.T) {
	engine := NewRetryEngine(DefaultRetryConfig())
	dg := engine.EvaluateDowngrade("grok-4.6")
	engine.RecordDowngrade("grok-4.6", dg)
	if engine.StickyEffort(DowngradeProviderForAdapter("grok"), dg.NewTier) == "" {
		t.Fatal("precondition: descent recorded a sticky effort")
	}
	engine.Reset()
	if got := engine.StickyEffort(DowngradeProviderForAdapter("grok"), dg.NewTier); got != "" {
		t.Fatalf("Reset leaked sticky effort %q", got)
	}
}
