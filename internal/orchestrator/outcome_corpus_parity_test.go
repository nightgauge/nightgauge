// Corpus parity between the two writers (#304).
//
// The learning corpus (.nightgauge/pipeline/history/outcomes.jsonl) is read by
// one set of consumers that cannot tell which path produced a row: the
// calibration / cost / reliability loop verdicts and `nightgauge learn tune`.
// So the two writers — Scheduler.recordOutcome here, and the Go side of
// pipeline.notifyComplete in internal/ipc — must agree on WHERE a row lands and
// WHAT each field means, or one corpus field carries two meanings with no
// discriminator.
package orchestrator

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/intelligence/learning"
	"github.com/nightgauge/nightgauge/internal/intelligence/tokens"
	"github.com/nightgauge/nightgauge/internal/state"
	"github.com/nightgauge/nightgauge/pkg/types"
)

// TestRecordOutcome_UsesPreMergeLinesCapturedAtPRCreate pins the autonomous
// writer's half of #369. The diff is created before pr-create exits and the
// terminal outcome is recorded while it is still available. A terminal-time
// diff would be circularly late: successful pr-merge runs have already landed
// on main by then and would all look like zero-line changes.
func TestRecordOutcome_UsesPreMergeLinesCapturedAtPRCreate(t *testing.T) {
	root := t.TempDir()
	runGit := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	runGit("init", "-b", "main")
	runGit("config", "user.email", "test@example.com")
	runGit("config", "user.name", "Test User")
	if err := os.WriteFile(filepath.Join(root, "change.txt"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit("add", ".")
	runGit("commit", "-m", "base")
	runGit("checkout", "-b", "fix/369-measure")
	if err := os.WriteFile(filepath.Join(root, "change.txt"), []byte("base\none\ntwo\nthree\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	s := &Scheduler{recordOutcomes: true}
	item := types.BoardItem{Number: 369, Repo: "acme/widget", Size: types.SizeM}
	runtime := state.NewRuntimeState(item.Repo, item.Number, "item-id", testRunID())
	runtime.BeginStage(state.StagePRCreate)
	s.writeStageExitRecord(item, state.StagePRCreate, runtime,
		&StageRunResult{ExitCode: 0}, 0, nil, 0, "haiku",
		0, 0, 0, time.Now().Add(-time.Second), root, "OPEN", "small")

	prediction := s.recordOutcome(item, runtime.Snapshot(), true, 3, "sonnet", root)
	got := readCorpus(t, root)
	if len(got) != 1 {
		t.Fatalf("expected 1 outcome, got %d", len(got))
	}
	if got[0].ActualSize != "small" {
		t.Errorf("ActualSize = %q, want small from the measured 3-line pre-merge diff", got[0].ActualSize)
	}
	if prediction == nil {
		t.Fatal("recordOutcome returned nil prediction")
	}
	hw := state.NewHistoryWriter(root)
	record := hw.BuildV2Record(runtime.Snapshot(), true, "", state.V2RunInput{
		Title: "measure actual size", BaseBranch: "main", ComplexityScore: 3,
	}, time.Now())
	record.OutcomePrediction = prediction // scheduler recordV2History ordering
	if err := hw.WriteV2Record(record, time.Now()); err != nil {
		t.Fatalf("WriteV2Record: %v", err)
	}
	if prediction.ActualSize != "small" {
		t.Errorf("V2 OutcomePrediction.ActualSize = %q, want small", prediction.ActualSize)
	}
}

func readCorpus(t *testing.T, root string) []learning.Outcome {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, ".nightgauge", "pipeline", "history", "outcomes.jsonl"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatalf("read outcomes.jsonl: %v", err)
	}
	var out []learning.Outcome
	dec := json.NewDecoder(bytes.NewReader(data))
	for dec.More() {
		var o learning.Outcome
		if err := dec.Decode(&o); err != nil {
			t.Fatalf("decode outcome: %v", err)
		}
		out = append(out, o)
	}
	return out
}

// The corpus lands in the run's TARGET repo, alongside the run record derived
// from the same result — never at the daemon's launch root. Pinning one
// learning.Recorder at scheduler construction rooted every row at the launch
// root, so in a multi-repo workspace a sibling repo's outcomes accumulated in
// the primary repo while its own corpus stayed empty and its run history (which
// #215/#232 already scope per-repo) described runs the corpus beside it never
// saw.
func TestRecordOutcome_LandsInTheRunsTargetRepo(t *testing.T) {
	launchRoot := t.TempDir()
	targetRepoRoot := t.TempDir()

	s := &Scheduler{recordOutcomes: true}
	item := types.BoardItem{Number: 77, Repo: "acme/widget", Size: types.SizeM}
	snap := state.NewRuntimeState(item.Repo, item.Number, "item-id", testRunID())
	snap.BeginStage(state.StageFeatureDev)
	snap.CompleteStage(0, tokens.TokenCounts{Input: 10, Output: 20}, "claude-sonnet-5")

	s.recordOutcome(item, snap.Snapshot(), true, 3, "sonnet", targetRepoRoot)

	if got := readCorpus(t, targetRepoRoot); len(got) != 1 {
		t.Fatalf("expected 1 outcome in the target repo, got %d", len(got))
	}
	if got := readCorpus(t, launchRoot); len(got) != 0 {
		t.Errorf("expected 0 outcomes at the launch root, got %d — the corpus must follow the run record into the target repo", len(got))
	}
}

// An empty runRoot must record NOTHING rather than falling back to some other
// root. "No resolvable root" and "recorded" cannot share a code path, or a
// misrouted run silently pollutes whatever root happened to be handy.
func TestRecordOutcome_NoRootRecordsNothing(t *testing.T) {
	s := &Scheduler{recordOutcomes: true}
	item := types.BoardItem{Number: 78, Repo: "acme/widget"}
	snap := state.NewRuntimeState(item.Repo, item.Number, "item-id", testRunID()).Snapshot()

	if got := s.recordOutcome(item, snap, true, 3, "sonnet", ""); got != nil {
		t.Errorf("recordOutcome with no root returned %+v, want nil", got)
	}
}

// The size PREDICTION is recorded only when the router actually had a size to
// predict from, and the ACTUAL half is never a second reading of that same
// input.
//
// PRESENCE FOLLOWS THE ROUTER'S OWN RESOLUTION ORDER — board Size field, then a
// size:* label, then absent (routing.resolveSize) — because the router scored
// the issue on exactly that term. Round 3 shared the helper but not its
// ARGUMENT: this writer passed the board field, the extension writer passed the
// label, so one corpus field had two presence rules and an issue with board
// Size=L and no label recorded "medium" here and "" there.
//
// The absence rule keys on the input, not on a score sentinel: routing clamps
// complexity scores to [1,8] and defaults a sizeless issue to the M base score,
// so a `score <= 0` guard never fires in the field and the ~95% of real issues
// with no size input would all record a fabricated bucket.
//
// This test drives ONE writer; the cross-writer assertion has to live on the
// other side of the import edge (internal/ipc imports orchestrator, not the
// reverse) — see TestLearningOutcomeFor_SizePresenceMatchesTheSchedulerWriter
// in internal/ipc.
func TestRecordOutcome_SizePresenceFollowsTheRoutersResolutionOrder(t *testing.T) {
	tests := []struct {
		name              string
		score             int
		size              types.Size
		labels            []string
		wantPredicted     string
		wantScoreRecorded int
	}{
		{"board-sized and scored", 3, types.SizeM, nil, "small", 3},
		{"board-sized and scored higher", 5, types.SizeM, nil, "medium", 5},
		{"large issue", 8, types.SizeXL, nil, "large", 8},
		{"unscored run predicts nothing", 0, types.SizeM, nil, "", 0},
		{"no size input predicts nothing, even at the default score", 3, "", nil, "", 3},
		// The label is the router's SECOND term, so it must count here too —
		// this writer used to ignore it, dropping the prediction for every
		// label-sized issue whose board field was unset.
		{"label-sized with no board field", 5, "", []string{"type:bug", "size:M"}, "medium", 5},
		{"board field wins over a disagreeing label", 8, types.SizeXL, []string{"size:XS"}, "large", 8},
		{"unrecognized label is not a size input", 5, "", []string{"size:HUGE"}, "", 5},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			s := &Scheduler{recordOutcomes: true}
			item := types.BoardItem{Number: 79, Repo: "acme/widget", Size: tc.size, Labels: tc.labels}
			snap := state.NewRuntimeState(item.Repo, item.Number, "item-id", testRunID()).Snapshot()

			s.recordOutcome(item, snap, true, tc.score, "sonnet", root)

			got := readCorpus(t, root)
			if len(got) != 1 {
				t.Fatalf("expected 1 outcome, got %d", len(got))
			}
			if got[0].PredictedSize != tc.wantPredicted {
				t.Errorf("PredictedSize = %q, want %q", got[0].PredictedSize, tc.wantPredicted)
			}
			// This table does not drive a pr-create exit, so ACTUAL size stays
			// absent. Rebucketing the issue's own size:* label would make the
			// pair a function of the same pre-run inputs the prediction came from
			// — a "measurement" no routing improvement could move (#304 round 2).
			if got[0].ActualSize != "" {
				t.Errorf("ActualSize = %q, want empty — the size:* label is an INPUT to the prediction, not a measurement of the run", got[0].ActualSize)
			}
			if got[0].ComplexityScore != tc.wantScoreRecorded {
				t.Errorf("ComplexityScore = %d, want %d", got[0].ComplexityScore, tc.wantScoreRecorded)
			}
		})
	}
}

// THE MODEL PAIR, on this writer. It used to be actualModel := predictedModel —
// a copy, so every row this writer ever wrote was a tautological routing HIT,
// in raw model-id vocabulary while the corpus's other writer wrote normalized
// bands. Both halves must now be independent, normalized, and absent when
// unknown.
func TestRecordOutcome_ModelPairIsMeasuredNotCopied(t *testing.T) {
	tests := []struct {
		name          string
		predicted     string
		devStageModel string
		wantPredicted string
		wantActual    string
	}{
		{"routed right", "sonnet", "claude-sonnet-5", "sonnet", "sonnet"},
		{"mis-routed", "sonnet", "claude-opus-5", "sonnet", "opus"},
		{"concrete id normalizes to the same band as the alias", "claude-sonnet-4-6", "claude-sonnet-5", "sonnet", "sonnet"},
		{"dev stage never reported a model", "sonnet", "", "sonnet", ""},
		{"no router prediction", "", "claude-sonnet-5", "", "sonnet"},
		// Adapter translation is not a routing decision (#340). Go dispatches
		// the BAND; the extension launches the provider id the band maps to and
		// reports it back, and the scheduler re-records the stage on it. A
		// strongest-band collapse reads gpt-5.6-sol ([opus, fable]) as "fable"
		// and books a MISS on every correctly-served codex run.
		{"codex served the predicted band", "opus", "gpt-5.6-sol", "opus", "opus"},
		{"gemini served the predicted band", "opus", "gemini-2.5-pro", "opus", "opus"},
		{"the same id under a fable prediction is a fable serve", "fable", "gpt-5.6-sol", "fable", "fable"},
		{"a genuinely weaker serve is still a miss", "opus", "gpt-5.6-terra", "opus", "sonnet"},
		// No registry band → no measurement. Recording the id verbatim (the
		// pre-#340 rule) guarantees a MISS the router never made.
		{"gemini default has no band", "opus", "gemini-2.0-flash", "opus", ""},
		{"a local model has no band", "sonnet", "my-local-llm-7b", "sonnet", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			s := &Scheduler{recordOutcomes: true}
			item := types.BoardItem{Number: 80, Repo: "acme/widget"}
			rs := state.NewRuntimeState(item.Repo, item.Number, "item-id", testRunID())
			if tc.devStageModel != "" {
				rs.RecordStageModel(state.StageFeatureDev, tc.devStageModel)
			}

			s.recordOutcome(item, rs.Snapshot(), true, 5, tc.predicted, root)

			got := readCorpus(t, root)
			if len(got) != 1 {
				t.Fatalf("expected 1 outcome, got %d", len(got))
			}
			if got[0].PredictedModel != tc.wantPredicted {
				t.Errorf("PredictedModel = %q, want %q", got[0].PredictedModel, tc.wantPredicted)
			}
			if got[0].ActualModel != tc.wantActual {
				t.Errorf("ActualModel = %q, want %q — actual must be what the %s stage SERVED, never a copy of the prediction",
					got[0].ActualModel, tc.wantActual, OutcomeModelStage)
			}
		})
	}
}

// loadIssueContext must not invent a prediction. A context file with no
// pickup_recommendation.dev_model used to yield "sonnet", which reached the
// corpus indistinguishable from a router that genuinely chose sonnet.
func TestLoadIssueContext_DoesNotFabricateAPrediction(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, ".nightgauge", "pipeline")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "issue-81.json"),
		[]byte(`{"routing":{"complexity_score":5,"path":"standard"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	score, _, model := loadIssueContext(root, 81)
	if score != 5 {
		t.Errorf("complexityScore = %d, want 5", score)
	}
	if model != "" {
		t.Errorf("predictedModel = %q, want empty — the context names no dev model, and a default recorded as a prediction is counted by every reader as a routing measurement", model)
	}
}
