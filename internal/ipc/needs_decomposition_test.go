package ipc

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/nightgauge/nightgauge/internal/layout/layouttest"
	"github.com/nightgauge/nightgauge/internal/orchestrator"
	"github.com/nightgauge/nightgauge/internal/orchestrator/gates"
	"github.com/nightgauge/nightgauge/internal/state"
)

type decompositionCall struct {
	repo   string
	issue  int
	detail string
}

// failPlanningWithGate runs an extension run whose feature-planning stage fails
// on the given gate result, and returns the record and the seam's calls.
func failPlanningWithGate(t *testing.T, runID string, gate state.StageGateResult, errMsg string) (state.V2RunRecord, []decompositionCall) {
	t.Helper()
	dir := layouttest.Repo(t)
	s := NewServer(nil, WithWorkspaceRoot(dir))
	var mu sync.Mutex
	var calls []decompositionCall
	s.applyNeedsDecompositionOverride = func(_ context.Context, repo string, issue int, detail string) error {
		mu.Lock()
		defer mu.Unlock()
		calls = append(calls, decompositionCall{repo, issue, detail})
		return nil
	}
	transition := s.methods["pipeline.notifyStageTransition"]
	if _, err := transition(t.Context(), []byte(`{"repo":"nightgauge/acmeapp","issueNumber":3684,"stage":"feature-planning","status":"running","runId":"`+runID+`"}`)); err != nil {
		t.Fatalf("notifyStageTransition(running): %v", err)
	}
	raw, err := json.Marshal(PipelineRecordStageGateResultParams{
		Repo: "nightgauge/acmeapp", IssueNumber: 3684, Stage: "feature-planning", RunID: runID, Result: gate,
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if _, err := s.methods["pipeline.recordStageGateResult"](t.Context(), raw); err != nil {
		t.Fatalf("recordStageGateResult: %v", err)
	}
	if _, err := transition(t.Context(), []byte(`{"repo":"nightgauge/acmeapp","issueNumber":3684,"stage":"feature-planning","status":"failed","error":`+strconvQuote(errMsg)+`,"runId":"`+runID+`"}`)); err != nil {
		t.Fatalf("notifyStageTransition(failed): %v", err)
	}
	if _, err := s.methods["pipeline.notifyComplete"](t.Context(), []byte(`{"repo":"nightgauge/acmeapp","issueNumber":3684,"success":false,"totalDurationMs":5,"failureDetail":`+strconvQuote(errMsg)+`,"runId":"`+runID+`"}`)); err != nil {
		t.Fatalf("notifyComplete: %v", err)
	}
	s.needsDecompositionWG.Wait()
	records := readHistoryRecords(t, dir)
	if len(records) != 1 {
		t.Fatalf("want one run record, got %d", len(records))
	}
	mu.Lock()
	defer mu.Unlock()
	return records[0], calls
}

// An extension run stopped by the planning gate as needs_decomposition labels
// and comments through the forge action, with the gate's evidence as detail,
// even though the extension's own error text carries none of it (#3684).
func TestNotifyComplete_NeedsDecompositionLabelsAndComments(t *testing.T) {
	gate := state.StageGateResult{
		GateName:     "feature-planning",
		Passed:       false,
		Reason:       gates.NeedsDecompositionMarker + " plan lists 31 files, over max_planned_files=25",
		Evidence:     []string{"planned_files=31", "max_planned_files=25", "plan_size=XL", "issue_size=S"},
		Timestamp:    "2026-11-28T00:00:00Z",
		Kind:         string(gates.KindFail),
		TerminalKind: gates.TerminalKindNeedsDecomposition,
	}
	rec, calls := failPlanningWithGate(t, "019001c1-0000-7000-8000-000000003684", gate, "stage feature-planning failed")
	if rec.TerminalFailureKind != orchestrator.TerminalKindNeedsDecomposition {
		t.Fatalf("TerminalFailureKind = %q, want %q", rec.TerminalFailureKind, orchestrator.TerminalKindNeedsDecomposition)
	}
	if len(calls) != 1 {
		t.Fatalf("forge action called %d times, want 1", len(calls))
	}
	c := calls[0]
	if c.repo != "nightgauge/acmeapp" || c.issue != 3684 {
		t.Errorf("called for %s#%d, want nightgauge/acmeapp#3684", c.repo, c.issue)
	}
	for _, want := range []string{"planned_files=31", "max_planned_files=25", "plan_size=XL", "issue_size=S"} {
		if !strings.Contains(c.detail, want) {
			t.Errorf("detail %q lacks %q", c.detail, want)
		}
	}
}

// Any other failure does not label or comment.
func TestNotifyComplete_OtherFailureDoesNotLabelForDecomposition(t *testing.T) {
	gate := state.StageGateResult{
		GateName:  "feature-planning",
		Passed:    false,
		Reason:    "planning-3684.json missing",
		Timestamp: "2026-11-28T00:00:00Z",
		Kind:      string(gates.KindFail),
	}
	rec, calls := failPlanningWithGate(t, "019001c1-0001-7000-8000-000000003684", gate, "[stall-kill] idle 1800s")
	if rec.TerminalFailureKind == orchestrator.TerminalKindNeedsDecomposition {
		t.Fatalf("TerminalFailureKind = %q for a non-decomposition failure", rec.TerminalFailureKind)
	}
	if len(calls) != 0 {
		t.Errorf("forge action called %d times for a non-decomposition failure", len(calls))
	}
}

func TestNeedsDecompositionDetail(t *testing.T) {
	ev := []string{"planned_files=3", "plan_size=L"}
	cases := []struct {
		name    string
		results []state.StageGateResult
		want    string
	}{
		{"no gate result falls back to errMsg", nil, "boom"},
		{"reason and evidence joined", []state.StageGateResult{{Reason: "r", Evidence: ev}}, "r; planned_files=3; plan_size=L"},
		{"last result wins", []state.StageGateResult{{Reason: "old"}, {Reason: "new", Evidence: ev[:1]}}, "new; planned_files=3"},
		{"reason without evidence", []state.StageGateResult{{Reason: "r"}}, "r"},
		{"empty result falls back to errMsg", []state.StageGateResult{{}}, "boom"},
		{"evidence without reason", []state.StageGateResult{{Evidence: ev}}, "planned_files=3; plan_size=L"},
	}
	for _, tc := range cases {
		if got := needsDecompositionDetail(tc.results, "boom"); got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
}

// A malformed repo or issue never reaches the forge.
func TestLabelForDecomposition_SkipsMalformedTarget(t *testing.T) {
	called := 0
	s := &Server{applyNeedsDecompositionOverride: func(context.Context, string, int, string) error {
		called++
		return nil
	}}
	s.labelForDecomposition("", 3684, "x")
	s.labelForDecomposition("acme/repo", 0, "x")
	s.needsDecompositionWG.Wait()
	if called != 0 {
		t.Errorf("forge action called %d times for a malformed target", called)
	}
}
