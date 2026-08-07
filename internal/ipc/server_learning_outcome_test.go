// Tests for the learning/calibration outcome written at the extension terminal
// funnel (#304).
//
// Before this, the outcome corpus (.nightgauge/pipeline/history/outcomes.jsonl)
// had exactly ONE writer: scheduler.recordOutcome, reachable only from
// Scheduler.runPipeline. Extension runs go ConcurrentPipelineManager →
// HeadlessOrchestrator → pipeline.notifyComplete and never enter that loop, so
// in the mode the product is actually operated in nothing recorded an outcome —
// while HeadlessOrchestrator's own comment asserted the Go side "records
// outcomes ... on every pipeline completion".
//
// These tests pin the corpus write at the IPC seam, pin BOTH deliberate
// non-recording states separately, and — because "a file exists" was never the
// bar — pin every field where a degenerate value is indistinguishable from a
// real one: the corpus this fix replaces was 8/8 model-less, 8/8
// complexityScore 0, 8/8 predictedSize "small" and 8/8 actualSize absent.
package ipc

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/intelligence/learning"
	"github.com/nightgauge/nightgauge/internal/orchestrator"
	"github.com/nightgauge/nightgauge/internal/state"
)

// outcomesPath is the corpus for one repo root. Per-repo, NOT workspace-scoped:
// the outcome is derived from the run record, and a run's persisted state lands
// in its target repo (#215/#232). loop-verdicts and `learn tune` read one
// --workdir and read that root's run history beside it.
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

// loadCapturedFixture reads one redacted, REAL artifact from
// internal/ipc/testdata/outcome-gap (see that directory's README.md for
// provenance and scripts/capture-outcome-gap-fixture.sh for capture).
func loadCapturedFixture(t *testing.T, name string, into any) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "outcome-gap", name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	if err := json.Unmarshal(data, into); err != nil {
		t.Fatalf("decode fixture %s: %v", name, err)
	}
}

func loadCapturedRunRecord(t *testing.T, name string) state.V2RunRecord {
	t.Helper()
	var rec state.V2RunRecord
	loadCapturedFixture(t, name, &rec)
	return rec
}

// capturedClassification returns the classification the captured
// issue-context.json fixture produces through the production loader — i.e. the
// real routing prediction (complexity score, predicted dev model, size label)
// a real run was picked up under.
func capturedClassification(t *testing.T, issueNumber int) issueClassification {
	t.Helper()
	root := t.TempDir()
	writeCapturedIssueContext(t, root, issueNumber)
	cls := loadIssueClassification(root, "", issueNumber)
	if cls.ComplexityScore <= 0 || cls.PredictedModel == "" || cls.Size == "" {
		t.Fatalf("captured issue-context fixture is degenerate: %+v", cls)
	}
	return cls
}

// writeCapturedIssueContext plants the captured issue-{N}.json under root,
// renumbered for the run under test. This is the file the notifyComplete
// handler reads for labels, size, complexity score and predicted model — the
// fixture is real captured routing data, not an invented shape (#166).
func writeCapturedIssueContext(t *testing.T, root string, issueNumber int) {
	t.Helper()
	var ctx map[string]any
	loadCapturedFixture(t, "issue-context.json", &ctx)
	ctx["issue_number"] = issueNumber

	dir := filepath.Join(root, ".nightgauge", "pipeline")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir pipeline dir: %v", err)
	}
	data, err := json.Marshal(ctx)
	if err != nil {
		t.Fatalf("encode issue context: %v", err)
	}
	path := filepath.Join(dir, fmt.Sprintf("issue-%d.json", issueNumber))
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write issue context: %v", err)
	}
}

// THE KEY REGRESSION TEST (#304). An extension-path run that reaches
// pipeline.notifyComplete must append exactly one learning outcome — and that
// outcome must carry real signal, not an all-zero husk.
//
// Against unfixed code this fails at the existence assertion: internal/ipc did
// not import the learning package at all. Against the FIRST fix attempt it
// still fails, on complexityScore/predictedSize/actualSize — the fields that
// stayed degenerate.
func TestNotifyComplete_WritesLearningOutcome(t *testing.T) {
	dir := t.TempDir()
	writeCapturedIssueContext(t, dir, 304)
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

	// Non-empty assertions, one per field where a degenerate value is the
	// pre-fix failure mode. A record that exists but reads all-zero is the same
	// blindness with a file on disk.
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
		t.Error("PredictedModel is empty — issue-304.json carries routing.pickup_recommendation.dev_model, so the router's prediction must reach the corpus (every pre-#304 record was model-less)")
	}
	if o.ActualModel == "" {
		t.Error("ActualModel is empty — the run reported a served model")
	}
	// complexityScore 0 is IN RANGE (SizeBucketForScore(0)=="small"), so an
	// unrecorded score is indistinguishable from a genuinely trivial issue. The
	// score is in the same issue-{N}.json the handler already opens for labels.
	if o.ComplexityScore <= 0 {
		t.Errorf("ComplexityScore = %d, want > 0 — the run's issue-304.json carries routing.complexity_score, and 0 is indistinguishable from a trivial issue (#304)", o.ComplexityScore)
	}
	// PredictedSize: the captured context carries size:M AND a real complexity
	// score, so the router made a size prediction and it must be recorded in the
	// small|medium|large vocabulary both writers share.
	if o.PredictedSize == "" {
		t.Error("PredictedSize is empty despite a size:* label and a known complexity score")
	}
	if !isSizeBucket(o.PredictedSize) {
		t.Errorf("PredictedSize = %q — not a small|medium|large bucket; the size:* vocabulary (XS|S|M|L|XL) can never equal the bucket vocabulary the other writer uses, so calibration would report a measured 0%% forever", o.PredictedSize)
	}
	// ActualSize: no lines-changed measurement reaches this boundary, so the
	// honest value is ABSENT. It must never be re-derived from the issue's
	// size:* label — that is one of the same pre-run inputs PredictedSize comes
	// from, so the "accuracy" would measure the scoring arithmetic. This run is
	// exactly the case that exposed it: size:M + priority:critical scores 5, so
	// the label-derived version books a MISS ("medium" vs "small") for a run the
	// router sized correctly.
	if o.ActualSize != "" {
		t.Errorf("ActualSize = %q, want empty — nothing here measures how big the change actually was, and rebucketing the size:* label makes the pair circular (#304)", o.ActualSize)
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
	if records[0].Routing.ComplexityScore != o.ComplexityScore {
		t.Errorf("RunRecord.Routing.ComplexityScore = %d, outcome says %d — the two sinks disagree",
			records[0].Routing.ComplexityScore, o.ComplexityScore)
	}
	if records[0].OutcomePrediction == nil {
		t.Fatal("RunRecord.OutcomePrediction is nil — the predicted-vs-actual routing fields never reached the record")
	}
	if got := records[0].OutcomePrediction.PredictedModel; got != o.PredictedModel {
		t.Errorf("RunRecord.OutcomePrediction.PredictedModel = %q, outcome says %q — the two sinks disagree", got, o.PredictedModel)
	}
	if got := records[0].OutcomePrediction.ActualModel; got != o.ActualModel {
		t.Errorf("RunRecord.OutcomePrediction.ActualModel = %q, outcome says %q — the two sinks disagree", got, o.ActualModel)
	}
}

// THE FLAGSHIP MATCH/MISS PAIR (#304). The corpus's only measurable pair is
// predicted-vs-actual MODEL, and the captured fixtures make both verdicts
// reachable: a run routed to sonnet that ran feature-dev on sonnet is a HIT, and
// the same run with a different model served is a MISS.
//
// This is the test that fails if either round-2 defect comes back. Reintroduce
// the tautology (actual := predicted) and the mis-routed case reports a match.
// Reintroduce dominant-cost attribution and the mis-routed case reports a match
// too, because the run's most expensive stage is not the one the prediction is
// about.
func TestLearningOutcomeFor_ModelPairMatchesWhenRoutedRight_MissesWhenNot(t *testing.T) {
	base := loadCapturedRunRecord(t, "run-record.json")
	cls := capturedClassification(t, base.IssueNumber)
	if cls.PredictedModel != "sonnet" {
		t.Fatalf("precondition: captured context predicts %q, expected sonnet", cls.PredictedModel)
	}

	// The run as captured: the router said sonnet, feature-dev served
	// claude-sonnet-5. Correctly routed → the pair must AGREE.
	hit, decision := learningOutcomeFor(base, cls, nil, "acme/widget", time.Now())
	if decision != outcomeRecord {
		t.Fatalf("decision = %s, want %s", decision, outcomeRecord)
	}
	if hit.PredictedModel == "" || hit.ActualModel == "" {
		t.Fatalf("unmeasurable pair: predicted=%q actual=%q", hit.PredictedModel, hit.ActualModel)
	}
	if hit.PredictedModel != hit.ActualModel {
		t.Errorf("correctly-routed run records a MISS: predicted %q, actual %q — the calibration loop can never report a hit and no routing improvement could move it",
			hit.PredictedModel, hit.ActualModel)
	}

	// Now mis-route it: the router still said sonnet, but the implementation
	// stage ran on opus. The pair must DISAGREE.
	misrouted := loadCapturedRunRecord(t, "run-record.json")
	dev := misrouted.Stages[string(orchestrator.OutcomeModelStage)]
	if dev.ModelSelection == nil {
		t.Fatalf("precondition: captured record has no %s model_selection", orchestrator.OutcomeModelStage)
	}
	dev.ModelSelection = &state.V2ModelSelect{Model: "claude-opus-5", Source: dev.ModelSelection.Source}
	misrouted.Stages[string(orchestrator.OutcomeModelStage)] = dev

	miss, _ := learningOutcomeFor(misrouted, cls, nil, "acme/widget", time.Now())
	if miss.ActualModel != "opus" {
		t.Errorf("ActualModel = %q, want %q — actual must be the model the implementation stage served", miss.ActualModel, "opus")
	}
	if miss.PredictedModel == miss.ActualModel {
		t.Errorf("mis-routed run records a MATCH: predicted %q, actual %q — a routing miss the corpus cannot see is the tautology this field had before (#304)",
			miss.PredictedModel, miss.ActualModel)
	}
}

func isSizeBucket(v string) bool {
	return v == "small" || v == "medium" || v == "large"
}

// The corpus must land in the run's TARGET repo — the same root as the run
// record it is derived from (#215/#232) — NOT in s.workspaceRoot.
//
// s.workspaceRoot is not a workspace root at all: it is a mutable pointer to
// the workspace's ACTIVE repo, reassigned by workspace.setRoot whenever the
// extension's resolveActiveRepository picks a different repo (in a multi-repo
// workspace, whichever repo owns the focused editor). Rooting the corpus there
// files repo B's outcome under repo A the moment the operator clicks into a
// different file, and repo B's corpus stays empty forever.
//
// Against the pre-fix code this fails on the FIRST assertion: the outcome
// lands under the active repo, and the target repo has no corpus at all.
func TestNotifyComplete_RecordsOutcomeInTargetRepoNotActiveRepo(t *testing.T) {
	activeRepoRoot := t.TempDir() // what workspace.setRoot last pointed at
	targetRepoRoot := t.TempDir() // the repo this run actually belongs to
	writeCapturedIssueContext(t, targetRepoRoot, 3044)

	s := NewServer(nil, WithWorkspaceRoot(activeRepoRoot))
	s.RegisterRepo("acme", "widget", targetRepoRoot)

	transition := s.methods["pipeline.notifyStageTransition"]
	complete := s.methods["pipeline.notifyComplete"]

	if _, err := transition(t.Context(), []byte(`{"repo":"acme/widget","issueNumber":3044,"stage":"feature-dev","status":"running","model":"claude-sonnet-5"}`)); err != nil {
		t.Fatalf("notifyStageTransition(running): %v", err)
	}
	if _, err := transition(t.Context(), []byte(`{"repo":"acme/widget","issueNumber":3044,"stage":"feature-dev","status":"complete","model":"claude-sonnet-5","inputTokens":10,"outputTokens":20,"costUsd":0.5}`)); err != nil {
		t.Fatalf("notifyStageTransition(complete): %v", err)
	}
	if _, err := complete(t.Context(), []byte(`{"repo":"acme/widget","issueNumber":3044,"success":true,"totalDurationMs":900}`)); err != nil {
		t.Fatalf("notifyComplete: %v", err)
	}

	outcomes, exists := readOutcomes(t, targetRepoRoot)
	if !exists {
		t.Fatalf("no corpus in the run's TARGET repo (%s) — the outcome was filed under a different repo than the run record it came from (#215/#304)", targetRepoRoot)
	}
	if len(outcomes) != 1 {
		t.Fatalf("expected exactly one outcome in the target repo, got %d", len(outcomes))
	}
	if outcomes[0].Repo != "acme/widget" {
		t.Errorf("Repo = %q, want %q", outcomes[0].Repo, "acme/widget")
	}

	if _, exists := readOutcomes(t, activeRepoRoot); exists {
		t.Errorf("the ACTIVE repo (%s) must have no corpus — repo A would accumulate repo B's runs and both corpora would describe runs their own history files never saw", activeRepoRoot)
	}

	// Both sinks in one place: the run record went to the target repo too.
	if got := len(readHistoryRecords(t, targetRepoRoot)); got != 1 {
		t.Errorf("expected 1 RunRecord in the target repo, got %d", got)
	}
	if got := len(readHistoryRecords(t, activeRepoRoot)); got != 0 {
		t.Errorf("expected 0 RunRecords in the active repo, got %d", got)
	}
}

// Predicted and actual must be about the SAME STAGE.
//
// predictedModel is routing.pickup_recommendation.dev_model — the router's
// choice for the implementation stage. So actualModel must be what THAT stage
// served. Attributing the run to whichever stage dominated its COST compares two
// different quantities: this run spends $6.00 on an opus feature-validate while
// feature-dev, the stage the prediction is about, never reported a model at all.
// Cost attribution books the run as an opus routing MISS against a sonnet
// prediction — a plausible-looking value that measures a stage the prediction
// never referred to, and unlike an empty one it is never logged.
//
// Against the round-2 code this fails with ActualModel = "opus".
func TestNotifyComplete_AttributesModelToTheStageThePredictionIsAbout(t *testing.T) {
	dir := t.TempDir()
	writeCapturedIssueContext(t, dir, 3045)
	s := NewServer(nil, WithWorkspaceRoot(dir))

	transition := s.methods["pipeline.notifyStageTransition"]
	complete := s.methods["pipeline.notifyComplete"]

	// feature-dev runs but never reports a served model (a non-Claude adapter,
	// or resolved-then-killed) — so the run measures NOTHING about the model the
	// prediction was for.
	for _, msg := range []string{
		`{"repo":"nightgauge/acmeapp","issueNumber":3045,"stage":"feature-dev","status":"running"}`,
		`{"repo":"nightgauge/acmeapp","issueNumber":3045,"stage":"feature-dev","status":"complete","inputTokens":10,"outputTokens":10,"costUsd":0.001}`,
		`{"repo":"nightgauge/acmeapp","issueNumber":3045,"stage":"feature-validate","status":"running","model":"claude-opus-5"}`,
		`{"repo":"nightgauge/acmeapp","issueNumber":3045,"stage":"feature-validate","status":"complete","model":"claude-opus-5","inputTokens":5000,"outputTokens":9000,"costUsd":6.00}`,
		`{"repo":"nightgauge/acmeapp","issueNumber":3045,"stage":"pr-merge","status":"running","model":"claude-haiku-4-5-20251001"}`,
		`{"repo":"nightgauge/acmeapp","issueNumber":3045,"stage":"pr-merge","status":"complete","model":"claude-haiku-4-5-20251001","inputTokens":20,"outputTokens":30,"costUsd":0.01}`,
	} {
		if _, err := transition(t.Context(), []byte(msg)); err != nil {
			t.Fatalf("notifyStageTransition: %v\nmsg=%s", err, msg)
		}
	}
	if _, err := complete(t.Context(), []byte(`{"repo":"nightgauge/acmeapp","issueNumber":3045,"success":true,"totalDurationMs":5000}`)); err != nil {
		t.Fatalf("notifyComplete: %v", err)
	}

	outcomes, exists := readOutcomes(t, dir)
	if !exists || len(outcomes) != 1 {
		t.Fatalf("expected exactly one learning outcome (exists=%v, n=%d)", exists, len(outcomes))
	}
	o := outcomes[0]
	if o.ActualModel != "" {
		t.Errorf("ActualModel = %q, want empty — %s served no model, and naming the run's most expensive stage instead records a routing miss for a stage the prediction was never about",
			o.ActualModel, orchestrator.OutcomeModelStage)
	}
	if o.PredictedModel != "sonnet" {
		t.Errorf("PredictedModel = %q, want sonnet — the prediction is still known and worth recording", o.PredictedModel)
	}
	if o.CostUSD < 6 {
		t.Errorf("CostUSD = %f, want ≈6.011 — the run's spend is still recorded in full for the cost loop", o.CostUSD)
	}
}

// The refusal fallback is a real measurement and must survive, scoped to the
// implementation stage: when the CLI silently retried a safety-refused
// feature-dev turn on another model, the fallback is what produced the code
// (#91). A swap in some OTHER stage is not evidence about the dev prediction.
func TestServedDevModel_RefusalFallbackIsScopedToTheDevStage(t *testing.T) {
	rec := loadCapturedRunRecord(t, "run-record.json")
	rec.Stages = map[string]state.V2StageDetail{} // force the runtime fallback path

	snap := state.NewRuntimeState("acme/widget", 1, "item")
	snap.RecordStageModel(state.StagePRMerge, "claude-haiku-4-5-20251001")
	snap.RecordModelRefusalFallback(state.StagePRMerge, "claude-sonnet-5", "claude-haiku-4-5-20251001", "safety")
	if got := servedDevModel(rec, snap.Snapshot()); got != "" {
		t.Errorf("servedDevModel = %q, want empty — a pr-merge refusal swap says nothing about the model feature-dev served", got)
	}

	snap.RecordStageModel(state.StageFeatureDev, "claude-sonnet-5")
	snap.RecordModelRefusalFallback(state.StageFeatureDev, "claude-sonnet-5", "claude-opus-5", "safety")
	if got := servedDevModel(rec, snap.Snapshot()); got != "opus" {
		t.Errorf("servedDevModel = %q, want %q — the model that actually served the refused dev turn is the one that produced the output (#91)", got, "opus")
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
	// No issue-{N}.json exists for this run: the prediction fields must be
	// EMPTY, not defaulted. An absent value is excluded from the accuracy
	// denominators; a fabricated one is counted as a measurement.
	if o.PredictedModel != "" {
		t.Errorf("PredictedModel = %q, want empty — no issue context exists, and inventing a default (the scheduler's loadIssueContext returns \"sonnet\") is a fabricated prediction", o.PredictedModel)
	}
	if o.PredictedSize != "" {
		t.Errorf("PredictedSize = %q, want empty — an unscored run must not be spelled like a real bucket", o.PredictedSize)
	}
	if o.ComplexityScore != 0 {
		t.Errorf("ComplexityScore = %d, want 0 for an unscored run", o.ComplexityScore)
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

// Fixture-driven (#166): the mapper is exercised against a REAL captured
// extension-path run record plus a REAL captured routing classification. It
// must produce a NON-DEGENERATE outcome — the existing corpus is 8/8
// predictedModel:"", complexityScore:0, predictedSize:"small", actualSize
// absent, and shipping a fix that reproduces that shape would look done and
// change nothing measurable.
func TestLearningOutcomeFor_FromCapturedRunRecord(t *testing.T) {
	rec := loadCapturedRunRecord(t, "run-record.json")
	cls := capturedClassification(t, rec.IssueNumber)
	// The captured record predates this fix, so its own routing/size fields are
	// the degenerate values #304 is about. Project the classification onto it
	// exactly as the notifyComplete handler now does before deriving.
	rec.Routing.ComplexityScore = cls.ComplexityScore
	rec.Size = &cls.Size

	o, decision := learningOutcomeFor(rec, cls, nil, "acme/widget", time.Now())
	if decision != outcomeRecord {
		t.Fatalf("decision = %s, want %s for a completed real run", decision, outcomeRecord)
	}

	if !o.Success {
		t.Errorf("Success = false, but the captured record's outcome is %q", rec.Outcome)
	}
	if o.PredictedModel == "" {
		t.Error("PredictedModel is empty — the captured issue context carries routing.pickup_recommendation.dev_model")
	}
	if o.ActualModel == "" {
		t.Error("ActualModel is empty — the captured record carries stages[*].model_selection.model, so the mapper reproduced the degenerate pre-#304 corpus shape")
	}
	if o.ComplexityScore <= 0 {
		t.Errorf("ComplexityScore = %d, want > 0", o.ComplexityScore)
	}
	if !isSizeBucket(o.PredictedSize) {
		t.Errorf("PredictedSize=%q — must use the small|medium|large bucket vocabulary", o.PredictedSize)
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

	// Attribution follows the PREDICTION's stage. The captured record's
	// implementation stage is the one the run must be booked against.
	want := orchestrator.OutcomeModelBand(stageModel(rec, string(orchestrator.OutcomeModelStage)))
	if o.ActualModel != want {
		t.Errorf("ActualModel = %q, want %q (the band the captured record's %s stage served)",
			o.ActualModel, want, orchestrator.OutcomeModelStage)
	}
}

// The captured record's OWN degenerate fields must map to EMPTY, never to a
// plausible-looking default. This is the fixture as captured — size null,
// complexity_score 0 — i.e. exactly what the pre-fix handler wrote.
func TestLearningOutcomeFor_UnknownSizeAndScoreStayEmpty(t *testing.T) {
	rec := loadCapturedRunRecord(t, "run-record.json")
	if rec.Size != nil || rec.Routing.ComplexityScore != 0 {
		t.Fatalf("precondition: captured record should carry size=null / complexity_score=0, got size=%v score=%d",
			rec.Size, rec.Routing.ComplexityScore)
	}

	o, decision := learningOutcomeFor(rec, issueClassification{}, nil, "acme/widget", time.Now())
	if decision != outcomeRecord {
		t.Fatalf("decision = %s, want %s", decision, outcomeRecord)
	}
	if o.PredictedSize != "" {
		t.Errorf("PredictedSize = %q, want \"\" — SizeBucketForScore(0)==\"small\" is why every pre-#304 record is indistinguishable from a genuinely small issue", o.PredictedSize)
	}
	if o.ActualSize != "" {
		t.Errorf("ActualSize = %q, want \"\" — the record carries no size:* label", o.ActualSize)
	}
	if o.PredictedModel != "" {
		t.Errorf("PredictedModel = %q, want \"\" — no issue context, so there is no router prediction to record", o.PredictedModel)
	}
	// ...but the ACTUAL model is knowable from the record and must be recorded.
	if o.ActualModel == "" {
		t.Error("ActualModel is empty — the served model is on the record regardless of whether an issue context exists")
	}
}

// The size prediction is recorded only when the router had a size to predict
// from, and the actual half is never re-derived from that same input.
//
// The absence rule keys on the INPUT, not on a score sentinel: complexity scores
// are clamped to [1,8] and default to the M base score (3) for an unlabelled
// issue, so a `score <= 0` guard is dead code and ~95% of real runs record a
// fabricated "small" through it.
func TestOutcomePredictedSize_AbsenceComesFromMissingInputs(t *testing.T) {
	if got := orchestrator.OutcomePredictedSize("", 3); got != "" {
		t.Errorf("no size:* label with the DEFAULT score 3 = %q, want \"\" — that score's size term is the router's default, not a prediction; a guard on score<=0 never fires because nothing writes 0", got)
	}
	if got := orchestrator.OutcomePredictedSize("M", 0); got != "" {
		t.Errorf("unscored run = %q, want \"\"", got)
	}
	if got := orchestrator.OutcomePredictedSize("HUGE", 5); got != "" {
		t.Errorf("unrecognized size label = %q, want \"\"", got)
	}
	for label, want := range map[string]string{"XS": "small", "M": "medium", "XL": "large"} {
		if got := orchestrator.OutcomePredictedSize(label, map[string]int{"XS": 1, "M": 5, "XL": 8}[label]); got != want {
			t.Errorf("OutcomePredictedSize(%q, …) = %q, want %q", label, got, want)
		}
	}
}

// OutcomeModelBand has three outcomes and they must stay three: a registry-known
// reference collapses to its band (so predicted "sonnet" and actual
// "claude-sonnet-5" are comparable), an unknown model is passed through
// VERBATIM (a user-defined local model is still attribution), and only an
// absent model yields "". Collapsing "unknown model" into "" would relabel real
// attribution as missing data.
func TestOutcomeModelBand_KnownUnknownAndAbsentAreDistinct(t *testing.T) {
	if got := orchestrator.OutcomeModelBand("claude-sonnet-5"); got != "sonnet" {
		t.Errorf("OutcomeModelBand(%q) = %q, want %q — a concrete id must normalize onto its band", "claude-sonnet-5", got, "sonnet")
	}
	if got := orchestrator.OutcomeModelBand("sonnet"); got != "sonnet" {
		t.Errorf("OutcomeModelBand(%q) = %q, want %q — the router's alias is already a band", "sonnet", got, "sonnet")
	}
	const local = "my-local-llm-7b"
	if got := orchestrator.OutcomeModelBand(local); got != local {
		t.Errorf("OutcomeModelBand(%q) = %q, want it passed through — an unregistered model is still attribution, and dropping it to \"\" reports real data as missing", local, got)
	}
	if got := orchestrator.OutcomeModelBand(""); got != "" {
		t.Errorf("OutcomeModelBand(\"\") = %q, want \"\"", got)
	}
}

// The captured FAILED extension-path record must map to a failure outcome that
// names its dying stage, derived from the record alone (no live runtime).
func TestLearningOutcomeFor_FromCapturedFailedRunRecord(t *testing.T) {
	rec := loadCapturedRunRecord(t, "run-record-failed.json")

	o, decision := learningOutcomeFor(rec, issueClassification{}, nil, "acme/widget", time.Now())
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
	if o.ActualModel == "" {
		t.Error("ActualModel is empty — the captured record's stages carry served models")
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
	if _, d := learningOutcomeFor(deferred, issueClassification{}, nil, "acme/widget", time.Now()); d != outcomeSkipDeferred {
		t.Errorf("deferred record: decision = %s, want %s", d, outcomeSkipDeferred)
	}

	netdown := base
	netdown.TerminalFailureKind = orchestrator.TerminalKindNetworkUnavailable
	if _, d := learningOutcomeFor(netdown, issueClassification{}, nil, "acme/widget", time.Now()); d != outcomeSkipNetworkUnavailable {
		t.Errorf("network-unavailable record: decision = %s, want %s", d, outcomeSkipNetworkUnavailable)
	}
}
