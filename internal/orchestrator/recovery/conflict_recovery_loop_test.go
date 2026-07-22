package recovery

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/orchestrator/gates"
	"github.com/nightgauge/nightgauge/internal/state"
)

// writeConflictContext drops a well-formed conflict-context-{issue}.json into
// the workspace's pipeline dir and returns the workspace root.
func writeConflictContext(t *testing.T, issue, pr int, branch string, files []string) string {
	t.Helper()
	ws := t.TempDir()
	dir := filepath.Join(ws, ".nightgauge", "pipeline")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	cf := make([]map[string]string, 0, len(files))
	for _, f := range files {
		cf = append(cf, map[string]string{"path": f, "ours": "ours-" + f, "theirs": "theirs-" + f})
	}
	doc := map[string]interface{}{
		"schema_version":    "1.0",
		"issue_number":      issue,
		"pr_number":         pr,
		"branch":            branch,
		"base_ref":          "main",
		"conflicting_files": cf,
	}
	data, _ := json.MarshalIndent(doc, "", "  ")
	if err := os.WriteFile(filepath.Join(dir, "conflict-context-"+strconv.Itoa(issue)+".json"), data, 0o644); err != nil {
		t.Fatalf("write context: %v", err)
	}
	return ws
}

func readFeedbackSignals(t *testing.T, ws string, issue int) feedbackOnDisk {
	t.Helper()
	p := filepath.Join(ws, ".nightgauge", "pipeline", "feedback-"+strconv.Itoa(issue)+".json")
	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read feedback: %v", err)
	}
	var fb feedbackOnDisk
	if err := json.Unmarshal(data, &fb); err != nil {
		t.Fatalf("parse feedback: %v", err)
	}
	return fb
}

// TestAction_ConflictRecoveryLoop_Matches_AndEmitsFeedback is the happy path:
// a pr-merge conflict no-op with a conflict-context file present → the action
// writes a CONFLICT_RESOLUTION_NEEDED signal into feedback-{N}.json targeting
// feature-dev, declines in-place recovery (Recovered=false), and returns
// FollowUpStageCanResume so the scheduler rewinds.
func TestAction_ConflictRecoveryLoop_Matches_AndEmitsFeedback(t *testing.T) {
	ws := writeConflictContext(t, 143, 200, "feat/143-thing", []string{"internal/foo.go", "internal/bar.go"})

	a := NewConflictRecoveryLoop(2)
	failure := StageFailure{
		Stage:       state.StagePRMerge,
		GateKind:    gates.KindNoOp,
		Workspace:   ws,
		IssueNumber: 143,
		PRNumber:    200,
		Reason:      "PR still not mergeable after conflict resolution (status: CONFLICTING)",
		Evidence:    []string{"mergeStateStatus=DIRTY", "conflict in internal/foo.go"},
	}

	if !a.Matches(failure) {
		t.Fatalf("expected match")
	}
	res := a.Execute(context.Background(), failure)
	if res.Recovered {
		t.Errorf("conflict recovery is deterministic — it must NOT claim Recovered=true (LLM dev stage resolves)")
	}
	if res.FollowUp != FollowUpStageCanResume {
		t.Errorf("FollowUp = %q, want %q so the scheduler rewinds", res.FollowUp, FollowUpStageCanResume)
	}

	fb := readFeedbackSignals(t, ws, 143)
	found := false
	for _, s := range fb.Signals {
		if s.SignalType == "CONFLICT_RESOLUTION_NEEDED" {
			found = true
			if s.BacktrackTargetStage != "feature-dev" {
				t.Errorf("signal target = %q, want feature-dev", s.BacktrackTargetStage)
			}
			if s.Severity != "blocking" {
				t.Errorf("severity = %q, want blocking", s.Severity)
			}
		}
	}
	if !found {
		t.Errorf("expected a CONFLICT_RESOLUTION_NEEDED signal in feedback-143.json, got %+v", fb.Signals)
	}
}

// TestAction_ConflictRecoveryLoop_MergesIntoExistingFeedback verifies the new
// signal is appended without clobbering a sibling feature-validate signal.
func TestAction_ConflictRecoveryLoop_MergesIntoExistingFeedback(t *testing.T) {
	ws := writeConflictContext(t, 50, 60, "feat/50", []string{"a.go"})
	// Pre-seed feedback-50.json with a feature-validate signal.
	existing := feedbackOnDisk{
		SchemaVersion: "1.0",
		IssueNumber:   50,
		Signals: []feedbackSignalOnDisk{{
			SignalType:           "PLAN_REVISION_NEEDED",
			EmittedByStage:       "feature-validate",
			BacktrackTargetStage: "feature-dev",
			Rationale:            "tests failed",
			Evidence:             []string{"TestFoo"},
			Severity:             "blocking",
		}},
	}
	data, _ := json.MarshalIndent(existing, "", "  ")
	_ = os.WriteFile(filepath.Join(ws, ".nightgauge", "pipeline", "feedback-50.json"), data, 0o644)

	a := NewConflictRecoveryLoop(2)
	res := a.Execute(context.Background(), StageFailure{
		Stage: state.StagePRMerge, GateKind: gates.KindNoOp, Workspace: ws,
		IssueNumber: 50, PRNumber: 60, Reason: "conflict", Evidence: []string{"conflict in a.go"},
	})
	if res.FollowUp != FollowUpStageCanResume {
		t.Fatalf("FollowUp = %q, want resume", res.FollowUp)
	}

	fb := readFeedbackSignals(t, ws, 50)
	if len(fb.Signals) != 2 {
		t.Fatalf("expected 2 signals (validate + conflict), got %d: %+v", len(fb.Signals), fb.Signals)
	}
	var hasValidate, hasConflict bool
	for _, s := range fb.Signals {
		if s.SignalType == "PLAN_REVISION_NEEDED" {
			hasValidate = true
		}
		if s.SignalType == "CONFLICT_RESOLUTION_NEEDED" {
			hasConflict = true
		}
	}
	if !hasValidate || !hasConflict {
		t.Errorf("expected both signals preserved; validate=%v conflict=%v", hasValidate, hasConflict)
	}
}

// TestAction_ConflictRecoveryLoop_NoMatch_FallsThrough covers the false-positive
// guards: wrong stage, gate OK, no conflict token, no workspace.
func TestAction_ConflictRecoveryLoop_NoMatch_FallsThrough(t *testing.T) {
	a := NewConflictRecoveryLoop(2)
	cases := []struct {
		name string
		f    StageFailure
	}{
		{"wrong stage", StageFailure{Stage: state.StagePRCreate, GateKind: gates.KindNoOp, Workspace: "/x", IssueNumber: 1, Reason: "conflict"}},
		{"gate ok", StageFailure{Stage: state.StagePRMerge, GateKind: gates.KindOK, Workspace: "/x", IssueNumber: 1, Reason: "conflict"}},
		{"no conflict token", StageFailure{Stage: state.StagePRMerge, GateKind: gates.KindNoOp, Workspace: "/x", IssueNumber: 1, Reason: "BEHIND", Evidence: []string{"mergeStateStatus=BEHIND"}}},
		{"no workspace", StageFailure{Stage: state.StagePRMerge, GateKind: gates.KindNoOp, IssueNumber: 1, Reason: "conflict"}},
		{"no issue number", StageFailure{Stage: state.StagePRMerge, GateKind: gates.KindNoOp, Workspace: "/x", Reason: "conflict"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if a.Matches(c.f) {
				t.Errorf("expected no match for %s", c.name)
			}
		})
	}
}

// TestAction_ConflictRecoveryLoop_NoContext_Escalates: the signal matched but
// conflict-context-{N}.json is missing → escalate to human triage rather than
// spin a context-less dev re-dispatch.
func TestAction_ConflictRecoveryLoop_NoContext_Escalates(t *testing.T) {
	ws := t.TempDir() // empty workspace — no conflict-context file
	a := NewConflictRecoveryLoop(2)
	failure := StageFailure{
		Stage: state.StagePRMerge, GateKind: gates.KindNoOp, Workspace: ws,
		IssueNumber: 99, PRNumber: 7, Reason: "rebase conflict with no markers",
		Evidence: []string{"conflict"},
	}
	if !a.Matches(failure) {
		t.Fatalf("expected match (reason mentions conflict)")
	}
	res := a.Execute(context.Background(), failure)
	if res.Recovered {
		t.Error("missing context cannot recover")
	}
	if res.FollowUp != FollowUpHumanTriageRequired {
		t.Errorf("FollowUp = %q, want human triage", res.FollowUp)
	}
}

// TestAction_ConflictRecoveryLoop_ExhaustsAndEscalates: once the feedback file
// carries more than max_dev_redispatch CONFLICT_RESOLUTION_NEEDED signals (each
// distinct pr-merge conflict failure appends one), the action escalates with the
// specific files instead of looping forever. With bound=2, the 3rd conflict
// (3 signals) is beyond the bound.
func TestAction_ConflictRecoveryLoop_ExhaustsAndEscalates(t *testing.T) {
	ws := writeConflictContext(t, 12, 34, "feat/12", []string{"x.go", "y.go"})
	// Pre-seed 3 prior conflict signals — the skill already wrote the signal for
	// this (3rd) failure, so the count is beyond the bound of 2.
	sig := feedbackSignalOnDisk{SignalType: "CONFLICT_RESOLUTION_NEEDED", EmittedByStage: "pr-merge", BacktrackTargetStage: "feature-dev", Severity: "blocking"}
	fb := feedbackOnDisk{
		SchemaVersion: "1.1",
		IssueNumber:   12,
		Signals:       []feedbackSignalOnDisk{sig, sig, sig},
	}
	data, _ := json.MarshalIndent(fb, "", "  ")
	_ = os.WriteFile(filepath.Join(ws, ".nightgauge", "pipeline", "feedback-12.json"), data, 0o644)

	a := NewConflictRecoveryLoop(2)
	res := a.Execute(context.Background(), StageFailure{
		Stage: state.StagePRMerge, GateKind: gates.KindNoOp, Workspace: ws,
		IssueNumber: 12, PRNumber: 34, Reason: "conflict", Evidence: []string{"conflict in x.go"},
	})
	if res.FollowUp != FollowUpHumanTriageRequired {
		t.Errorf("FollowUp = %q, want human triage once exhausted", res.FollowUp)
	}
	// Evidence must name the specific conflicting files.
	joined := strings.Join(res.Evidence, " ")
	if !strings.Contains(joined, "x.go") || !strings.Contains(joined, "y.go") {
		t.Errorf("exhaustion evidence must name the conflicting files; got %v", res.Evidence)
	}
}

// TestDefaultRegistry_ConflictRoutesToConflictRecovery locks the ordering: a
// pr-merge no-op whose evidence names a CONFLICT must route to
// conflict-recovery-loop, ahead of branch-out-of-date.
func TestDefaultRegistry_ConflictRoutesToConflictRecovery(t *testing.T) {
	reg := Default("", nil, nil)
	conflict := StageFailure{
		Stage:       state.StagePRMerge,
		GateKind:    gates.KindNoOp,
		Workspace:   "/ws",
		IssueNumber: 100,
		Reason:      "PR #100 still not mergeable after conflict resolution (status: CONFLICTING)",
		Evidence:    []string{"pr=100", "mergeStateStatus=DIRTY", "conflict in foo.go"},
	}
	var first string
	for _, act := range reg.Actions() {
		if act.Matches(conflict) {
			first = act.Name()
			break
		}
	}
	if first != "conflict-recovery-loop" {
		t.Errorf("a conflict pr-merge no-op must route to conflict-recovery-loop first, got %q", first)
	}
}
