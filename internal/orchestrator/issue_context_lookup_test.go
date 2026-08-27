package orchestrator

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/nightgauge/nightgauge/internal/intelligence/learning"
	"github.com/nightgauge/nightgauge/internal/state"
	"github.com/nightgauge/nightgauge/pkg/types"
)

// writeIssueContext writes a well-formed issue-{N}.json — the shape the
// issue-pickup stage actually produces in the field — at the given root.
func writeIssueContext(t *testing.T, root string, issue int, devModel string, complexity int) {
	t.Helper()
	dir := filepath.Join(root, ".nightgauge", "pipeline")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	body := `{"routing":{"complexity_score":` + strconv.Itoa(complexity) +
		`,"path":"standard","pickup_recommendation":{"dev_model":"` + devModel + `"}}}`
	if err := os.WriteFile(filepath.Join(dir, "issue-"+strconv.Itoa(issue)+".json"), []byte(body), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
}

// TestLoadIssueContext_FindsGoManagerWorktree is the headline guard for #994.
//
// Every one of the corpus's rows carried complexityScore 0 and predictedModel
// "" for the life of the product, because this read looked only at the
// workspace root while the issue-pickup stage writes the file into the run's
// WORKTREE. The real files in the field do carry both values — they were simply
// never found.
//
// Deleting the Go-manager candidate must turn this red.
func TestLoadIssueContext_FindsGoManagerWorktree(t *testing.T) {
	root := t.TempDir()
	// The Go manager's layout: <repoRoot>/.nightgauge/worktrees/{repo}-issue-N
	wt := filepath.Join(root, ".nightgauge", "worktrees", "widget-issue-42")
	writeIssueContext(t, wt, 42, "sonnet", 3)

	// Deliberately NOT at the workspace root — that is the whole point.
	if _, err := os.Stat(filepath.Join(root, ".nightgauge", "pipeline", "issue-42.json")); err == nil {
		t.Fatal("fixture wrote to the workspace root; the test proves nothing")
	}

	score, path, model := loadIssueContext(root, "", "acme/widget", 42)
	if model != "sonnet" {
		t.Errorf("predictedModel = %q, want \"sonnet\" — the context exists only in the "+
			"worktree, which is where every real run puts it", model)
	}
	if score != 3 {
		t.Errorf("complexityScore = %d, want 3 — the same read poisons the size "+
			"prediction, not just the model one", score)
	}
	if path != "standard" {
		t.Errorf("routingPath = %q, want \"standard\"", path)
	}
}

// TestLoadIssueContext_FindsExtensionWorktree covers the other live layout.
func TestLoadIssueContext_FindsExtensionWorktree(t *testing.T) {
	root := t.TempDir()
	writeIssueContext(t, filepath.Join(root, ".worktrees", "issue-7"), 7, "opus", 5)

	score, _, model := loadIssueContext(root, "", "acme/widget", 7)
	if model != "opus" || score != 5 {
		t.Errorf("got model=%q score=%d, want opus/5 from the extension worktree layout", model, score)
	}
}

// TestLoadIssueContext_ExplicitWorktreeWins guards the ordering: when the
// caller knows the run's worktree, a stale file at the root must not shadow it.
func TestLoadIssueContext_ExplicitWorktreeWins(t *testing.T) {
	root := t.TempDir()
	writeIssueContext(t, root, 9, "haiku", 1)   // stale, at the root
	wt := filepath.Join(root, "somewhere-else") //
	writeIssueContext(t, wt, 9, "opus", 8)      // live, in the worktree

	_, _, model := loadIssueContext(root, wt, "acme/widget", 9)
	if model != "opus" {
		t.Errorf("predictedModel = %q, want \"opus\" — the run's own worktree must win "+
			"over a stale file at the workspace root", model)
	}
}

// TestLoadIssueContext_StillReadsThePlainRoot is the control: a run with no
// worktree at all must keep working exactly as before.
func TestLoadIssueContext_StillReadsThePlainRoot(t *testing.T) {
	root := t.TempDir()
	writeIssueContext(t, root, 11, "sonnet", 2)

	score, _, model := loadIssueContext(root, "", "acme/widget", 11)
	if model != "sonnet" || score != 2 {
		t.Errorf("got model=%q score=%d, want sonnet/2 from the plain root", model, score)
	}
}

// TestLoadIssueContext_MissingEverywhereIsZero guards the honest-empty case.
// A fabricated default here is what #304 removed, and the corpus cannot tell a
// fabricated prediction from a measured one.
func TestLoadIssueContext_MissingEverywhereIsZero(t *testing.T) {
	score, path, model := loadIssueContext(t.TempDir(), "", "acme/widget", 99)
	if score != 0 || path != "" || model != "" {
		t.Errorf("got score=%d path=%q model=%q, want all zero — a missing context must "+
			"not synthesise a prediction", score, path, model)
	}
}

// TestResolveIssueContextPath_FindsTheFileTheStagesUse guards the two callers
// that need the PATH rather than the contents. shouldReRoute used to stat the
// workspace root and return false on exactly the worktree-isolated runs whose
// prediction it was meant to repair; reRouteContext would have written a decoy
// file beside the real one.
func TestResolveIssueContextPath_FindsTheFileTheStagesUse(t *testing.T) {
	root := t.TempDir()
	wt := filepath.Join(root, ".nightgauge", "worktrees", "widget-issue-42")
	writeIssueContext(t, wt, 42, "sonnet", 3)

	got := resolveIssueContextPath(root, "", "acme/widget", 42)
	want := filepath.Join(wt, ".nightgauge", "pipeline", "issue-42.json")
	if got != want {
		t.Errorf("resolveIssueContextPath = %q, want %q", got, want)
	}

	if got := resolveIssueContextPath(t.TempDir(), "", "acme/widget", 42); got != "" {
		t.Errorf("expected \"\" when no candidate exists, got %q", got)
	}
}

// TestResolveOutcomePrediction_UpgradesFromTheWorktree is the guard for the
// SECOND half of #994: the prediction must be resolved at RECORDING time, not
// at pickup.
//
// At pickup the worktree does not exist and issue-pickup has not run, so both
// values are definitionally empty on a first run. Reading again at the
// recording boundary — the first moment the file is guaranteed to exist — is
// what turns a corpus that was 100% empty on both halves into one that records
// what the router actually predicted.
func TestResolveOutcomePrediction_UpgradesFromTheWorktree(t *testing.T) {
	root := t.TempDir()
	wt := filepath.Join(root, ".nightgauge", "worktrees", "widget-issue-42")
	writeIssueContext(t, wt, 42, "opus", 7)

	s := &Scheduler{}
	// The pickup values: what runPipeline actually had — nothing.
	score, model := s.resolveOutcomePrediction(root, "", "acme/widget", 42, 0, "")

	if model != "opus" {
		t.Errorf("predictedModel = %q, want \"opus\" — the context is in the worktree, "+
			"which is where every real run writes it", model)
	}
	if score != 7 {
		t.Errorf("complexityScore = %d, want 7 — the same read poisons the size "+
			"prediction too", score)
	}
}

// TestResolveOutcomePrediction_NeverDowngrades guards the safety direction. A
// re-read that finds nothing must keep whatever the caller already had, so a
// run whose context genuinely never appeared is no worse off than before.
func TestResolveOutcomePrediction_NeverDowngrades(t *testing.T) {
	s := &Scheduler{}
	score, model := s.resolveOutcomePrediction(t.TempDir(), "", "acme/widget", 42, 4, "sonnet")
	if score != 4 || model != "sonnet" {
		t.Errorf("got score=%d model=%q, want the pickup values 4/sonnet preserved when "+
			"no context exists anywhere", score, model)
	}
}

// TestResolveOutcomePrediction_UpgradesEachHalfIndependently guards against a
// naive "if either is set, take both" rule: a context carrying a model but no
// score must not discard a score the caller already had.
func TestResolveOutcomePrediction_UpgradesEachHalfIndependently(t *testing.T) {
	root := t.TempDir()
	// Model present, score zero.
	writeIssueContext(t, root, 5, "opus", 0)

	s := &Scheduler{}
	score, model := s.resolveOutcomePrediction(root, "", "acme/widget", 5, 6, "sonnet")
	if model != "opus" {
		t.Errorf("predictedModel = %q, want the fresher \"opus\"", model)
	}
	if score != 6 {
		t.Errorf("complexityScore = %d, want the caller's 6 preserved — the context "+
			"carried no score to upgrade it with", score)
	}
}

// TestRecordOutcome_RecordsThePredictionFromTheWorktree is the end-to-end guard,
// and it exists because a weaker one was not enough.
//
// The resolver tests above prove the LOGIC. They say nothing about whether
// recordOutcome calls it — and deleting that call left the whole suite green,
// which is the same unpinned-wiring shape that the rest of this fix is about.
// This drives recordOutcome and reads the row back out of the corpus, so
// removing the call site fails here.
func TestRecordOutcome_RecordsThePredictionFromTheWorktree(t *testing.T) {
	root := t.TempDir()
	// The context exists ONLY in the run's worktree — the real-world shape.
	wt := filepath.Join(root, ".nightgauge", "worktrees", "widget-issue-42")
	writeIssueContext(t, wt, 42, "sonnet", 3)

	s := &Scheduler{recordOutcomes: true}
	snap := &state.RuntimeState{IssueNumber: 42, WorktreeDir: wt}

	// Pickup values are empty, exactly as runPipeline has them on a first run.
	got := s.recordOutcome(
		types.BoardItem{Repo: "acme/widget", Number: 42, Size: "M"},
		snap, true, 0, "", root)

	if got == nil {
		t.Fatal("recordOutcome returned nil")
	}
	if got.PredictedModel == "" {
		t.Error("the recorded row carries no predictedModel — this is the state every " +
			"row in the real corpus was in for the life of the product")
	}

	outcomes, err := learning.NewRecorder(root).LoadAll()
	if err != nil {
		t.Fatalf("load corpus: %v", err)
	}
	if len(outcomes) != 1 {
		t.Fatalf("corpus has %d row(s), want 1", len(outcomes))
	}
	if outcomes[0].PredictedModel == "" {
		t.Error("the APPENDED row carries no predictedModel — the return value and the " +
			"corpus must agree")
	}
	if outcomes[0].ComplexityScore != 3 {
		t.Errorf("appended complexityScore = %d, want 3 — the same read poisons the size "+
			"prediction, and it is fixed by the same change",
			outcomes[0].ComplexityScore)
	}
}
