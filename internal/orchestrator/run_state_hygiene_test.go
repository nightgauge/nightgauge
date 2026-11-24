package orchestrator

import (
	"github.com/nightgauge/nightgauge/internal/state"

	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	stagecontext "github.com/nightgauge/nightgauge/internal/execution/context"
	"github.com/nightgauge/nightgauge/internal/gittest"
	"github.com/nightgauge/nightgauge/internal/runstate"
)

// A run-state record another issue left behind is not adopted (#3081).
func TestAdoptableRunStateID_RefusesAnotherIssuesRecord(t *testing.T) {
	t.Parallel()

	id, err := newRunID()
	if err != nil {
		t.Fatalf("newRunID: %v", err)
	}
	rs := &runstate.RunState{RunID: id, IssueNumber: 1}

	if got, why := adoptableRunStateID(rs, 2); got != "" || why == "" {
		t.Fatalf("issue 2 adopted issue 1's run id: got %q why %q", got, why)
	}
	if got, why := adoptableRunStateID(rs, 1); got != id || why != "" {
		t.Fatalf("issue 1 did not adopt its own run id: got %q why %q", got, why)
	}
}

func TestAdoptableRunStateID_RefusesNonIdentity(t *testing.T) {
	t.Parallel()

	rs := &runstate.RunState{RunID: "not-a-uuid", IssueNumber: 3}
	if got, why := adoptableRunStateID(rs, 3); got != "" || why == "" {
		t.Fatalf("non-identity run id adopted: got %q why %q", got, why)
	}
	if got, why := adoptableRunStateID(nil, 3); got != "" || why != "" {
		t.Fatalf("nil record: got %q why %q", got, why)
	}
}

// A stale dev-N.json is gone, and kept as an archive, once feature-dev starts.
func TestArchivePriorHandoff_MovesStaleHandoff(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	dev := filepath.Join(dir, "dev-42.json")
	if err := os.WriteFile(dev, []byte(`{"issue_number":42}`), 0o644); err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1700000000, 0)

	archived, err := archivePriorHandoff(dev, now)
	if err != nil {
		t.Fatalf("archive: %v", err)
	}
	if _, err := os.Stat(dev); !os.IsNotExist(err) {
		t.Fatalf("dev-42.json still present after archive: %v", err)
	}
	want := filepath.Join(dir, "dev-42.1700000000.prior.json")
	if archived != want {
		t.Fatalf("archive path = %q, want %q", archived, want)
	}
	if b, err := os.ReadFile(want); err != nil || string(b) != `{"issue_number":42}` {
		t.Fatalf("archive content = %q, %v", b, err)
	}

	// Nothing to archive is not an error.
	if got, err := archivePriorHandoff(dev, now); got != "" || err != nil {
		t.Fatalf("second archive: %q, %v", got, err)
	}
}

func TestStartsPastFeatureDev(t *testing.T) {
	t.Parallel()

	stages := []state.PipelineStage{state.StageIssuePickup, state.StageFeaturePlanning,
		state.StageFeatureDev, state.StageFeatureValidate}
	cases := map[int]bool{0: false, 2: false, 3: true}
	for idx, want := range cases {
		if got := startsPastFeatureDev(stages, idx); got != want {
			t.Errorf("startsPastFeatureDev(start=%d) = %v, want %v", idx, got, want)
		}
	}
	if startsPastFeatureDev([]state.PipelineStage{state.StageFeatureValidate}, 0) {
		t.Error("a stage list without feature-dev must not report past it")
	}
}

// A card retry starts a fresh attempt: the earlier attempt's dev, validate,
// quality-review and feedback handoffs are archived, and the files the run
// regenerates or still needs are left untouched (#3565).
func TestArchivePriorAttemptHandoffs_RetryClearsStaleHandoffs(t *testing.T) {
	t.Parallel()

	ws := t.TempDir()
	gittest.Run(t, ws, "init", "-b", "main")
	path := func(name string) string {
		p, err := stagecontext.ContextPath(ws, 7, name)
		if err != nil {
			t.Fatalf("ContextPath(%s): %v", name, err)
		}
		return p
	}
	old := time.Now().Add(-time.Hour)
	stale := []string{"dev", "validate", "quality-review", "feedback"}
	if err := os.MkdirAll(filepath.Dir(path("dev")), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range stale {
		if err := os.WriteFile(path(name), []byte(`{"issue_number":7,"signals":[]}`), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path(name), old, old); err != nil {
			t.Fatal(err)
		}
	}
	kept := map[string][]byte{}
	for _, name := range []string{"planning", "issue", "ac-reconcile"} {
		kept[path(name)] = []byte(`{"keep":"` + name + `"}`)
	}
	kept[filepath.Join(ws, "plans", "7-x.md")] = []byte("# plan\n")
	kept[filepath.Join(ws, "knowledge", "features", "7-x", "PRD.md")] = []byte("# prd\n")
	for p, b := range kept {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, b, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	archived, err := archivePriorAttemptHandoffs(ws, 7, time.Unix(1000, 0))
	if err != nil {
		t.Fatalf("archivePriorAttemptHandoffs: %v", err)
	}
	if len(archived) != len(stale) {
		t.Fatalf("archived %d handoffs, want %d: %v", len(archived), len(stale), archived)
	}
	for _, name := range stale {
		if _, err := os.Stat(path(name)); !os.IsNotExist(err) {
			t.Errorf("%s-7.json still readable after retry archive (err=%v)", name, err)
		}
		a := filepath.Join(filepath.Dir(path(name)), name+"-7.1000.prior.json")
		if _, err := os.Stat(a); err != nil {
			t.Errorf("archive %s missing: %v", a, err)
		}
	}
	for p, want := range kept {
		got, err := os.ReadFile(p)
		if err != nil || string(got) != string(want) {
			t.Errorf("%s changed: got %q err %v", p, got, err)
		}
	}
}

// The retry's operator steer survives the feedback archive; every signal an
// earlier stage emitted does not (#3565).
func TestArchivePriorAttemptHandoffs_KeepsOperatorSteer(t *testing.T) {
	t.Parallel()

	ws := t.TempDir()
	gittest.Run(t, ws, "init", "-b", "main")
	fb, err := stagecontext.ContextPath(ws, 9, "feedback")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(fb), 0o755); err != nil {
		t.Fatal(err)
	}
	in := FeedbackContext{SchemaVersion: "1", IssueNumber: 9, Signals: []FeedbackSignal{
		{SignalType: OperatorSteerSignalType, Rationale: "use the cache", Severity: "warning"},
		{SignalType: "PLAN_REVISION_NEEDED", Rationale: "stale", Severity: "blocking"},
	}}
	data, _ := json.Marshal(in)
	if err := os.WriteFile(fb, data, 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := archivePriorAttemptHandoffs(ws, 9, time.Unix(2000, 0)); err != nil {
		t.Fatalf("archivePriorAttemptHandoffs: %v", err)
	}
	got, err := os.ReadFile(fb)
	if err != nil {
		t.Fatalf("feedback with a steer was not rewritten: %v", err)
	}
	var out FeedbackContext
	if err := json.Unmarshal(got, &out); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(fb), "feedback-9.2000.prior.json")); err != nil {
		t.Errorf("original feedback not archived: %v", err)
	}
	if out.IssueNumber != 9 || len(out.Signals) == 1 && out.Signals[0].Rationale != "use the cache" {
		t.Errorf("rewrite lost the steer or its header: %+v", out)
	}
	if len(out.Signals) != 1 || out.Signals[0].SignalType != OperatorSteerSignalType {
		t.Fatalf("kept signals = %+v, want only the operator steer", out.Signals)
	}
}

func TestStartsAtOrBeforeFeaturePlanning(t *testing.T) {
	t.Parallel()

	stages := []state.PipelineStage{state.StageIssuePickup, state.StageFeaturePlanning,
		state.StageFeatureDev, state.StageFeatureValidate}
	for idx, want := range []bool{true, true, false, false} {
		if got := startsAtOrBeforeFeaturePlanning(stages, idx); got != want {
			t.Errorf("startsAtOrBeforeFeaturePlanning(start=%d) = %v, want %v", idx, got, want)
		}
	}
	if startsAtOrBeforeFeaturePlanning([]state.PipelineStage{state.StageIssuePickup, state.StageFeatureDev}, 0) {
		t.Error("a pipeline with no planning stage must not archive at run start")
	}
}

// A malformed feedback file is archived and reported, never silently kept.
func TestArchivePriorAttemptHandoffs_MalformedFeedbackReported(t *testing.T) {
	t.Parallel()

	ws := t.TempDir()
	gittest.Run(t, ws, "init", "-b", "main")
	fb, err := stagecontext.ContextPath(ws, 5, "feedback")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(fb), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fb, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	archived, err := archivePriorAttemptHandoffs(ws, 5, time.Unix(3000, 0))
	if err == nil {
		t.Fatal("malformed feedback was archived without an error")
	}
	if len(archived) != 1 {
		t.Fatalf("archived = %v, want the malformed feedback file", archived)
	}
	if _, err := os.Stat(fb); !os.IsNotExist(err) {
		t.Errorf("malformed feedback still readable (err=%v)", err)
	}
}
