package ipc

import (
	"fmt"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/layout/layouttest"
	"github.com/nightgauge/nightgauge/internal/state"
)

// pipeline.recordBacktrack persists the backtrack on the run's snapshot, and
// the run record built from that snapshot carries it (#3081).
func TestRecordBacktrack_PersistsIntoRunRecord(t *testing.T) {
	workspaceRoot := layouttest.Repo(t)
	s := NewServer(nil, WithWorkspaceRoot(workspaceRoot))
	stateDir := layouttest.PipelineDir(t, workspaceRoot)
	runID := newTestRunID()

	start := fmt.Sprintf(`{"repo":"nightgauge/acmeapp","issueNumber":3081,"stage":"feature-validate","status":"running","runId":%q}`, runID)
	if _, err := s.methods["pipeline.notifyStageTransition"](t.Context(), []byte(start)); err != nil {
		t.Fatalf("notifyStageTransition: %v", err)
	}
	bt := fmt.Sprintf(`{"repo":"nightgauge/acmeapp","issueNumber":3081,"fromStage":"feature-validate","toStage":"feature-dev","signalType":"TEST_FAILURE","reason":"unit tests red","runId":%q}`, runID)
	if _, err := s.methods["pipeline.recordBacktrack"](t.Context(), []byte(bt)); err != nil {
		t.Fatalf("recordBacktrack: %v", err)
	}

	snap, err := state.LoadSnapshotByIdentity(stateDir, 3081, runID)
	if err != nil {
		t.Fatalf("load snapshot: %v", err)
	}
	rec := state.NewHistoryWriter(workspaceRoot).BuildV2Record(snap, false, "", state.V2RunInput{}, time.Now())
	if len(rec.Backtracks) != 1 {
		t.Fatalf("record backtracks = %+v, want one", rec.Backtracks)
	}
	got := rec.Backtracks[0]
	if got.FromStage != "feature-validate" || got.ToStage != "feature-dev" ||
		got.SignalType != "TEST_FAILURE" || got.Reason != "unit tests red" || got.At == "" {
		t.Fatalf("record backtrack = %+v", got)
	}
}

func TestRecordBacktrack_RefusesMissingRunIDAndUnknownStages(t *testing.T) {
	workspaceRoot := layouttest.Repo(t)
	s := NewServer(nil, WithWorkspaceRoot(workspaceRoot))
	stateDir := layouttest.PipelineDir(t, workspaceRoot)
	runID := newTestRunID()
	start := fmt.Sprintf(`{"repo":"nightgauge/acmeapp","issueNumber":1,"stage":"feature-validate","status":"running","runId":%q}`, runID)
	if _, err := s.methods["pipeline.notifyStageTransition"](t.Context(), []byte(start)); err != nil {
		t.Fatalf("notifyStageTransition: %v", err)
	}
	verb := s.methods["pipeline.recordBacktrack"]
	for name, params := range map[string]string{
		"no runId":       `{"repo":"nightgauge/acmeapp","issueNumber":1,"fromStage":"feature-validate","toStage":"feature-dev"}`,
		"blank from":     fmt.Sprintf(`{"repo":"nightgauge/acmeapp","issueNumber":1,"fromStage":"","toStage":"feature-dev","runId":%q}`, runID),
		"unknown stage":  fmt.Sprintf(`{"repo":"nightgauge/acmeapp","issueNumber":1,"fromStage":"feature-validate","toStage":"not-a-stage","runId":%q}`, runID),
		"blank to stage": fmt.Sprintf(`{"repo":"nightgauge/acmeapp","issueNumber":1,"fromStage":"feature-validate","toStage":"","runId":%q}`, runID),
	} {
		if _, err := verb(t.Context(), []byte(params)); err == nil {
			t.Errorf("%s: recordBacktrack must be refused", name)
		}
	}
	snap, err := state.LoadSnapshotByIdentity(stateDir, 1, runID)
	if err != nil {
		t.Fatalf("load snapshot: %v", err)
	}
	if len(snap.Backtracks) != 0 {
		t.Fatalf("a refused call recorded backtracks: %+v", snap.Backtracks)
	}
}
