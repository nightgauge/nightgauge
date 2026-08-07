// Tests for the learning/calibration outcome written at the extension terminal
// funnel (#304).
//
// Before this, the outcome corpus (.nightgauge/pipeline/history/outcomes.jsonl)
// had exactly ONE writer: scheduler.recordOutcome, reachable only from
// Scheduler.runPipeline. Extension runs go ConcurrentPipelineManager →
// HeadlessOrchestrator → pipeline.notifyComplete and never enter that loop, so
// in the mode the product is actually operated in nothing recorded an outcome —
// while HeadlessOrchestrator's own comment asserted the Go side "records
// outcomes ... on every pipeline completion". These tests pin the corpus write
// at the IPC seam and pin BOTH deliberate non-recording states separately, so a
// regression cannot collapse "we chose not to record" into "we recorded a
// failure" or into silence.
package ipc

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/intelligence/learning"
	"github.com/nightgauge/nightgauge/internal/orchestrator"
	"github.com/nightgauge/nightgauge/internal/state"
)

// outcomesPath is the workspace-scoped corpus the loop verdicts and
// `nightgauge learn tune` read. Deliberately NOT per-repo: sharding it would
// leave the loops as blind as before the fix while every test still passed.
func outcomesPath(root string) string {
	return filepath.Join(root, ".nightgauge", "pipeline", "history", "outcomes.jsonl")
}

// readOutcomes returns the corpus entries under root. A missing file returns
// (nil, false) — distinct from an existing-but-empty file, which returns
// (nil, true). "Never written" and "written empty" are different failures and
// must not share a return shape (#166).
func readOutcomes(t *testing.T, root string) ([]learning.Outcome, bool) {
	t.Helper()
	data, err := os.ReadFile(outcomesPath(root))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, false
		}
		t.Fatalf("read outcomes.jsonl: %v", err)
	}
	var out []learning.Outcome
	for _, line := range splitLinesTest(data) {
		if len(line) == 0 {
			continue
		}
		var o learning.Outcome
		if err := json.Unmarshal(line, &o); err != nil {
			t.Fatalf("decode outcome line: %v\nline=%q", err, string(line))
		}
		out = append(out, o)
	}
	return out, true
}

// THE KEY REGRESSION TEST (#304). An extension-path run that reaches
// pipeline.notifyComplete must append exactly one learning outcome — and that
// outcome must carry real signal, not an all-zero husk. Against unfixed code
// this fails at the existence assertion: internal/ipc did not import the
// learning package at all.
func TestNotifyComplete_WritesLearningOutcome(t *testing.T) {
	dir := t.TempDir()
	s := NewServer(nil, WithWorkspaceRoot(dir))

	transition := s.methods["pipeline.notifyStageTransition"]
	complete := s.methods["pipeline.notifyComplete"]

	if _, err := transition(t.Context(), []byte(`{"repo":"nightgauge/acmeapp","issueNumber":304,"stage":"feature-dev","status":"running","model":"claude-sonnet-5","adapter":"claude"}`)); err != nil {
		t.Fatalf("notifyStageTransition(running): %v", err)
	}
	if _, err := transition(t.Context(), []byte(`{"repo":"nightgauge/acmeapp","issueNumber":304,"stage":"feature-dev","status":"complete","model":"claude-sonnet-5","adapter":"claude","inputTokens":1200,"outputTokens":340,"costUsd":0.42}`)); err != nil {
		t.Fatalf("notifyStageTransition(complete): %v", err)
	}
	if _, err := complete(t.Context(), []byte(`{"repo":"nightgauge/acmeapp","issueNumber":304,"success":true,"totalDurationMs":1000}`)); err != nil {
		t.Fatalf("notifyComplete: %v", err)
	}

	outcomes, exists := readOutcomes(t, dir)
	if !exists {
		t.Fatalf("no learning outcome corpus at %s — the extension terminal funnel recorded nothing (#304)", outcomesPath(dir))
	}
	if len(outcomes) != 1 {
		t.Fatalf("expected exactly one learning outcome, got %d", len(outcomes))
	}
	o := outcomes[0]

	// Non-empty assertions, one per field where empty is the pre-fix failure
	// mode. A record that exists but reads all-zero is the same blindness with
	// a file on disk.
	if o.Repo == "" {
		t.Error("Repo is empty — the outcome cannot be attributed to a repository")
	}
	if o.IssueNumber != 304 {
		t.Errorf("IssueNumber = %d, want 304", o.IssueNumber)
	}
	if !o.Success {
		t.Error("Success = false for a run that completed")
	}
	if o.PredictedModel == "" {
		t.Error("PredictedModel is empty — the run reported a served model, so the outcome must carry it (every pre-#304 corpus record was model-less)")
	}
	if o.ActualModel == "" {
		t.Error("ActualModel is empty — with no refusal fallback it must mirror PredictedModel")
	}
	if o.DurationMs <= 0 {
		t.Errorf("DurationMs = %d, want > 0", o.DurationMs)
	}
	if o.InputTokens <= 0 {
		t.Errorf("InputTokens = %d, want > 0 (the run reported 1200)", o.InputTokens)
	}
	if o.OutputTokens <= 0 {
		t.Errorf("OutputTokens = %d, want > 0 (the run reported 340)", o.OutputTokens)
	}
	if o.CostUSD <= 0 {
		t.Errorf("CostUSD = %f, want > 0 (the run reported 0.42)", o.CostUSD)
	}
	if o.CompletedAt.IsZero() {
		t.Error("CompletedAt is the zero time — every such outcome sorts to the front of the corpus")
	}
	if o.FailedStage != "" {
		t.Errorf("FailedStage = %q on a successful run, want empty", o.FailedStage)
	}

	// The same derivation must land on the run record, exactly as
	// scheduler.recordOutcome's return value is threaded into recordV2History.
	records := readHistoryRecords(t, dir)
	if len(records) != 1 {
		t.Fatalf("expected exactly one RunRecord, got %d", len(records))
	}
	if records[0].OutcomePrediction == nil {
		t.Fatal("RunRecord.OutcomePrediction is nil — the predicted-vs-actual routing fields never reached the record")
	}
	if got := records[0].OutcomePrediction.PredictedModel; got != o.PredictedModel {
		t.Errorf("RunRecord.OutcomePrediction.PredictedModel = %q, outcome says %q — the two sinks disagree", got, o.PredictedModel)
	}
}

// A FAILED extension-path run must also produce an outcome — naming the stage
// it died in. Pre-#304 the corpus saw neither successes nor failures from this
// path, so the reliability loop's success rate was computed entirely from
// autonomous runs.
func TestNotifyComplete_WritesLearningOutcomeForFailure(t *testing.T) {
	dir := t.TempDir()
	s := NewServer(nil, WithWorkspaceRoot(dir))

	transition := s.methods["pipeline.notifyStageTransition"]
	complete := s.methods["pipeline.notifyComplete"]

	if _, err := transition(t.Context(), []byte(`{"repo":"nightgauge/acmeapp","issueNumber":3041,"stage":"feature-dev","status":"running","model":"claude-sonnet-5","adapter":"claude"}`)); err != nil {
		t.Fatalf("notifyStageTransition(running): %v", err)
	}
	if _, err := transition(t.Context(), []byte(`{"repo":"nightgauge/acmeapp","issueNumber":3041,"stage":"feature-dev","status":"failed","model":"claude-sonnet-5","adapter":"claude","inputTokens":900,"outputTokens":100,"costUsd":0.31,"error":"tests failed: 3 assertions"}`)); err != nil {
		t.Fatalf("notifyStageTransition(failed): %v", err)
	}
	if _, err := complete(t.Context(), []byte(`{"repo":"nightgauge/acmeapp","issueNumber":3041,"success":false,"totalDurationMs":2000}`)); err != nil {
		t.Fatalf("notifyComplete: %v", err)
	}

	outcomes, exists := readOutcomes(t, dir)
	if !exists {
		t.Fatalf("no learning outcome corpus at %s — a failed extension-path run recorded nothing (#304)", outcomesPath(dir))
	}
	if len(outcomes) != 1 {
		t.Fatalf("expected exactly one learning outcome, got %d", len(outcomes))
	}
	o := outcomes[0]
	if o.Success {
		t.Error("Success = true for a failed run")
	}
	if o.FailedStage != "feature-dev" {
		t.Errorf("FailedStage = %q, want %q — a failure with no stage cannot feed stage-reliability analysis", o.FailedStage, "feature-dev")
	}
	if o.CostUSD <= 0 {
		t.Errorf("CostUSD = %f, want > 0 — a failed run still spent money and the cost loop must see it", o.CostUSD)
	}
}

// #305 parity: a blocked-dependency deferral is a NON-FAILURE that did no work.
// It must not enter the corpus at all — booking it as Success:false would tell
// the reliability loop the pipeline failed when it deliberately declined to run.
// Pinned separately from the network-unavailable skip so a regression cannot
// merge the two into one "didn't record" branch.
func TestNotifyComplete_SkipsOutcomeForDeferral(t *testing.T) {
	dir := t.TempDir()
	s := NewServer(nil, WithWorkspaceRoot(dir))

	transition := s.methods["pipeline.notifyStageTransition"]
	complete := s.methods["pipeline.notifyComplete"]

	if _, err := transition(t.Context(), []byte(`{"repo":"nightgauge/acmeapp","issueNumber":3042,"stage":"issue-pickup","status":"running"}`)); err != nil {
		t.Fatalf("notifyStageTransition(running): %v", err)
	}
	if _, err := complete(t.Context(), []byte(`{"repo":"nightgauge/acmeapp","issueNumber":3042,"success":false,"deferred":true,"totalDurationMs":50}`)); err != nil {
		t.Fatalf("notifyComplete: %v", err)
	}

	// The run record must still exist (the deferral is booked as a non-failure
	// run) — proving the absent outcome is a decision, not a dead handler.
	if records := readHistoryRecords(t, dir); len(records) != 1 {
		t.Fatalf("expected the deferral's RunRecord to be written, got %d records", len(records))
	}
	if outcomes, exists := readOutcomes(t, dir); exists {
		t.Fatalf("a deferral must not enter the learning corpus; got %d outcome(s): %+v", len(outcomes), outcomes)
	}
}

// #3296 parity: a run killed by extended GitHub connectivity loss is
// environmental noise, not signal about model or stage performance. The Go
// scheduler skips it; the extension seam must skip it identically, or the two
// paths disagree about what the corpus means.
func TestNotifyComplete_SkipsOutcomeForNetworkUnavailable(t *testing.T) {
	dir := t.TempDir()
	s := NewServer(nil, WithWorkspaceRoot(dir))

	transition := s.methods["pipeline.notifyStageTransition"]
	complete := s.methods["pipeline.notifyComplete"]

	if _, err := transition(t.Context(), []byte(`{"repo":"nightgauge/acmeapp","issueNumber":3043,"stage":"feature-dev","status":"running","model":"claude-sonnet-5"}`)); err != nil {
		t.Fatalf("notifyStageTransition(running): %v", err)
	}
	if _, err := transition(t.Context(), []byte(`{"repo":"nightgauge/acmeapp","issueNumber":3043,"stage":"feature-dev","status":"failed","error":"network unavailable: extended GitHub connectivity loss"}`)); err != nil {
		t.Fatalf("notifyStageTransition(failed): %v", err)
	}
	if _, err := complete(t.Context(), []byte(`{"repo":"nightgauge/acmeapp","issueNumber":3043,"success":false,"totalDurationMs":2000}`)); err != nil {
		t.Fatalf("notifyComplete: %v", err)
	}

	records := readHistoryRecords(t, dir)
	if len(records) != 1 {
		t.Fatalf("expected exactly one RunRecord, got %d", len(records))
	}
	if records[0].TerminalFailureKind != orchestrator.TerminalKindNetworkUnavailable {
		t.Fatalf("precondition failed: TerminalFailureKind = %q, want %q",
			records[0].TerminalFailureKind, orchestrator.TerminalKindNetworkUnavailable)
	}
	if outcomes, exists := readOutcomes(t, dir); exists {
		t.Fatalf("a network-unavailable failure must not enter the learning corpus; got %d outcome(s): %+v", len(outcomes), outcomes)
	}
}

// loadCapturedRunRecord reads one redacted, REAL extension-path run record from
// internal/ipc/testdata/outcome-gap (see that directory's README.md for
// provenance and scripts/capture-outcome-gap-fixture.sh for capture).
func loadCapturedRunRecord(t *testing.T, name string) state.V2RunRecord {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "outcome-gap", name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	var rec state.V2RunRecord
	if err := json.Unmarshal(data, &rec); err != nil {
		t.Fatalf("decode fixture %s: %v", name, err)
	}
	return rec
}

// Fixture-driven (#166): the mapper is exercised against a REAL captured
// extension-path run record, not a hand-authored shape. It must produce a
// NON-DEGENERATE outcome — the existing corpus is 8/8 predictedModel:"",
// complexityScore:0, predictedSize:"small", and shipping a fix that reproduces
// that shape would look done and change nothing measurable.
func TestLearningOutcomeFor_FromCapturedRunRecord(t *testing.T) {
	rec := loadCapturedRunRecord(t, "run-record.json")
	now := time.Now()

	o, decision := learningOutcomeFor(rec, nil, "acme/widget", now)
	if decision != outcomeRecord {
		t.Fatalf("decision = %s, want %s for a completed real run", decision, outcomeRecord)
	}

	if !o.Success {
		t.Errorf("Success = false, but the captured record's outcome is %q", rec.Outcome)
	}
	if o.PredictedModel == "" {
		t.Error("PredictedModel is empty — the captured record carries stages[feature-dev].model_selection.model, so the mapper reproduced the degenerate pre-#304 corpus shape")
	}
	if o.ActualModel != o.PredictedModel {
		t.Errorf("ActualModel = %q, want %q (no refusal fallback in this record)", o.ActualModel, o.PredictedModel)
	}
	if o.CostUSD <= 0 {
		t.Errorf("CostUSD = %f, want > 0 — the captured record reports %f", o.CostUSD, rec.Tokens.EstimatedCostUSD)
	}
	if o.InputTokens <= 0 {
		t.Errorf("InputTokens = %d, want > 0 — the captured record reports %d", o.InputTokens, rec.Tokens.TotalInput)
	}
	if o.OutputTokens <= 0 {
		t.Errorf("OutputTokens = %d, want > 0 — the captured record reports %d", o.OutputTokens, rec.Tokens.TotalOutput)
	}
	if o.DurationMs <= 0 {
		t.Errorf("DurationMs = %d, want > 0", o.DurationMs)
	}
	if o.Repo == "" || o.IssueNumber == 0 {
		t.Errorf("Repo/IssueNumber unattributed: repo=%q issue=%d", o.Repo, o.IssueNumber)
	}
	if o.CompletedAt.IsZero() {
		t.Error("CompletedAt is the zero time")
	}
	// The captured record has size:null. PredictedSize must stay EMPTY —
	// explicitly NOT the scheduler's predictedSizeLabel(0)=="small", which is
	// why every existing corpus record is indistinguishable from a genuinely
	// small issue.
	if o.PredictedSize != "" {
		t.Errorf("PredictedSize = %q, want \"\" — the captured record has no size:* label, and an unknown size must not be spelled like a real one", o.PredictedSize)
	}
}

// The captured FAILED extension-path record must map to a failure outcome that
// names its dying stage, derived from the record alone (no live runtime).
func TestLearningOutcomeFor_FromCapturedFailedRunRecord(t *testing.T) {
	rec := loadCapturedRunRecord(t, "run-record-failed.json")

	o, decision := learningOutcomeFor(rec, nil, "acme/widget", time.Now())
	if decision != outcomeRecord {
		t.Fatalf("decision = %s, want %s", decision, outcomeRecord)
	}
	if o.Success {
		t.Errorf("Success = true, but the captured record's outcome is %q", rec.Outcome)
	}
	if o.FailedStage == "" {
		t.Error("FailedStage is empty — a failure outcome with no stage cannot feed stage-reliability analysis")
	}
	if detail, ok := rec.Stages[o.FailedStage]; !ok || detail.Status != "failed" {
		t.Errorf("FailedStage = %q, which is not a failed stage in the captured record", o.FailedStage)
	}
	if o.CostUSD <= 0 {
		t.Errorf("CostUSD = %f, want > 0 — a failed run still spent money", o.CostUSD)
	}
}

// The three decision states must be three distinct values with three distinct
// names. A bool here would make "we chose not to record a deferral" and "we
// chose not to record environmental noise" indistinguishable in logs and in
// every future caller.
func TestOutcomeDecision_ThreeDistinctStates(t *testing.T) {
	seen := map[string]outcomeDecision{}
	for _, d := range []outcomeDecision{outcomeRecord, outcomeSkipDeferred, outcomeSkipNetworkUnavailable} {
		name := d.String()
		if name == "" || name == "unset" {
			t.Errorf("decision %d has no distinct name (%q)", int(d), name)
		}
		if prev, dup := seen[name]; dup {
			t.Errorf("decisions %d and %d share the name %q", int(prev), int(d), name)
		}
		seen[name] = d
	}
	if outcomeUnset == outcomeRecord {
		t.Error("the zero value must not mean \"record it\" — a forgotten assignment would silently write")
	}
}

// The mapper's skip verdicts must be readable off a captured record with its
// classification fields flipped, proving the skips key on the record's own
// outcome_type / terminal_failure_kind rather than re-deriving the
// classification (#305 owns the deferral rule; this follows it).
func TestLearningOutcomeFor_SkipVerdictsFromRecordFields(t *testing.T) {
	base := loadCapturedRunRecord(t, "run-record-failed.json")

	deferred := base
	deferred.Outcome = "cancelled"
	deferred.TerminalFailureKind = ""
	deferred.OutcomeType = orchestrator.OutcomeTypeDeferred
	if _, d := learningOutcomeFor(deferred, nil, "acme/widget", time.Now()); d != outcomeSkipDeferred {
		t.Errorf("deferred record: decision = %s, want %s", d, outcomeSkipDeferred)
	}

	netdown := base
	netdown.TerminalFailureKind = orchestrator.TerminalKindNetworkUnavailable
	if _, d := learningOutcomeFor(netdown, nil, "acme/widget", time.Now()); d != outcomeSkipNetworkUnavailable {
		t.Errorf("network-unavailable record: decision = %s, want %s", d, outcomeSkipNetworkUnavailable)
	}
}
