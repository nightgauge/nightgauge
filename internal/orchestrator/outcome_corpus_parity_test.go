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
	"path/filepath"
	"testing"

	"github.com/nightgauge/nightgauge/internal/intelligence/learning"
	"github.com/nightgauge/nightgauge/internal/state"
	"github.com/nightgauge/nightgauge/pkg/types"
)

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
	snap := state.NewRuntimeState(item.Repo, item.Number, "item-id")
	snap.BeginStage(state.StageFeatureDev)
	snap.CompleteStage(0, 10, 20, "claude-sonnet-5")

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
	snap := state.NewRuntimeState(item.Repo, item.Number, "item-id").Snapshot()

	if got := s.recordOutcome(item, snap, true, 3, "sonnet", ""); got != nil {
		t.Errorf("recordOutcome with no root returned %+v, want nil", got)
	}
}

// Predicted and actual size must be comparable, and unknown must be empty.
// Consumers test the two for equality; a predicted size in the complexity
// vocabulary against an actual size in the size:* label vocabulary reports a
// measured 0% forever, and an unscored run spelled "small" is
// indistinguishable from a genuinely small issue.
func TestRecordOutcome_SizeVocabularyMatchesTheIPCPath(t *testing.T) {
	tests := []struct {
		name              string
		score             int
		size              types.Size
		wantPredicted     string
		wantActual        string
		wantScoreRecorded int
	}{
		{"scored and labelled agree", 3, types.SizeM, "small", "small", 3},
		{"scored and labelled disagree", 5, types.SizeM, "medium", "small", 5},
		{"large issue", 8, types.SizeXL, "large", "large", 8},
		{"unscored run predicts nothing", 0, types.SizeM, "", "small", 0},
		{"unlabelled run has no actual", 5, "", "medium", "", 5},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			s := &Scheduler{recordOutcomes: true}
			item := types.BoardItem{Number: 79, Repo: "acme/widget", Size: tc.size}
			snap := state.NewRuntimeState(item.Repo, item.Number, "item-id").Snapshot()

			s.recordOutcome(item, snap, true, tc.score, "sonnet", root)

			got := readCorpus(t, root)
			if len(got) != 1 {
				t.Fatalf("expected 1 outcome, got %d", len(got))
			}
			if got[0].PredictedSize != tc.wantPredicted {
				t.Errorf("PredictedSize = %q, want %q", got[0].PredictedSize, tc.wantPredicted)
			}
			if got[0].ActualSize != tc.wantActual {
				t.Errorf("ActualSize = %q, want %q", got[0].ActualSize, tc.wantActual)
			}
			if got[0].ComplexityScore != tc.wantScoreRecorded {
				t.Errorf("ComplexityScore = %d, want %d", got[0].ComplexityScore, tc.wantScoreRecorded)
			}
		})
	}
}
