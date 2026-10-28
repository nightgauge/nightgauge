package ipc

import (
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/state"
)

// TestPhaseTransition_SkipAndFailReachTheRecord pins the wire half of #1026.
//
// The extension's skipPhase updated its own state and sent nothing; failPhase
// was an empty body. Both are fixed on the TypeScript side — but the server's
// switch handled only "start" and "complete", so even a correct sender would
// have been silently ignored. Fixing one side without the other leaves the GUI
// and the durable record exactly as unable to agree as before.
func TestPhaseTransition_SkipAndFailReachTheRecord(t *testing.T) {
	root := t.TempDir()
	s := NewServer(nil, WithWorkspaceRoot(root))

	const (
		repo  = "acme/platform"
		issue = 1026
	)
	runID := newTestRunID()

	// Establish the run so the phase messages are addressed to a live runtime.
	mustCall(t, s, "pipeline.notifyStageTransition", PipelineNotifyStageTransitionParams{
		Repo: repo, IssueNumber: issue, Stage: "feature-dev", Status: "running", RunID: runID,
	})

	mustCall(t, s, "pipeline.notifyPhaseTransition", PipelineNotifyPhaseTransitionParams{
		Repo: repo, IssueNumber: issue, Stage: "feature-dev", Name: "run-tests",
		Index: 3, Total: 18, EventType: "skip", RunID: runID,
	})
	mustCall(t, s, "pipeline.notifyPhaseTransition", PipelineNotifyPhaseTransitionParams{
		Repo: repo, IssueNumber: issue, Stage: "feature-dev", Name: "sync-project-status",
		Index: 2, Total: 18, EventType: "fail", RunID: runID,
	})

	rt, _ := s.currentRunForIssue(repo, issue)
	if rt == nil {
		t.Fatal("no runtime registered for the run")
	}

	byName := map[string]string{}
	for _, p := range rt.rs.PhaseHistory {
		byName[p.Name] = p.Status
	}
	if byName["run-tests"] != "skipped" {
		t.Errorf("run-tests = %q, want skipped — the record could not learn about a skip at all before #1026",
			byName["run-tests"])
	}
	if byName["sync-project-status"] != "failed" {
		t.Errorf("sync-project-status = %q, want failed — a failing phase used to be indistinguishable from a running one",
			byName["sync-project-status"])
	}
}

// TestPhaseTransition_UnknownEventTypeIsRefused pins the missing default.
//
// The switch had none, so an unrecognised eventType returned {"status":"ok"}
// having done nothing: a caller could not tell "recorded" from "silently
// discarded". That is how a whole vocabulary went missing with no surface
// reporting a problem, and it is the property that stops the next one going the
// same way.
func TestPhaseTransition_UnknownEventTypeIsRefused(t *testing.T) {
	root := t.TempDir()
	s := NewServer(nil, WithWorkspaceRoot(root))

	const (
		repo  = "acme/platform"
		issue = 1026
	)
	runID := newTestRunID()
	mustCall(t, s, "pipeline.notifyStageTransition", PipelineNotifyStageTransitionParams{
		Repo: repo, IssueNumber: issue, Stage: "feature-dev", Status: "running", RunID: runID,
	})

	err := callRunVerb(t, s, "pipeline.notifyPhaseTransition", PipelineNotifyPhaseTransitionParams{
		Repo: repo, IssueNumber: issue, Stage: "feature-dev", Name: "implementation",
		Index: 1, Total: 5, EventType: "not-a-transition", RunID: runID,
	})
	if err == nil {
		t.Fatal("an unrecognised eventType was ACCEPTED — the caller cannot tell recorded from discarded")
	}
	if !strings.Contains(err.Error(), "unknown eventType") {
		t.Errorf("the refusal does not name the problem: %v", err)
	}
}

// TestPhaseTransition_StartAndCompleteStillWork is the control.
func TestPhaseTransition_StartAndCompleteStillWork(t *testing.T) {
	root := t.TempDir()
	s := NewServer(nil, WithWorkspaceRoot(root))

	const (
		repo  = "acme/platform"
		issue = 1026
	)
	runID := newTestRunID()
	mustCall(t, s, "pipeline.notifyStageTransition", PipelineNotifyStageTransitionParams{
		Repo: repo, IssueNumber: issue, Stage: "feature-dev", Status: "running", RunID: runID,
	})

	mustCall(t, s, "pipeline.notifyPhaseTransition", PipelineNotifyPhaseTransitionParams{
		Repo: repo, IssueNumber: issue, Stage: "feature-dev", Name: "implementation",
		Index: 1, Total: 5, EventType: "start", RunID: runID,
	})
	mustCall(t, s, "pipeline.notifyPhaseTransition", PipelineNotifyPhaseTransitionParams{
		Repo: repo, IssueNumber: issue, Stage: "feature-dev", Name: "implementation",
		Index: 1, Total: 5, EventType: "complete", RunID: runID,
	})

	rt, _ := s.currentRunForIssue(repo, issue)
	if rt == nil {
		t.Fatal("no runtime registered")
	}
	var found *state.PhaseRecord
	for i := range rt.rs.PhaseHistory {
		if rt.rs.PhaseHistory[i].Name == "implementation" {
			found = &rt.rs.PhaseHistory[i]
		}
	}
	if found == nil || found.Status != "complete" {
		t.Errorf("implementation = %+v, want a completed record", found)
	}
}

// TestPhaseTransition_UnreportedReachesTheRecord pins the wire half of #1246.
//
// The extension's end-of-stage back-fill now distinguishes "the stage decided
// not to run this" from "the stage ended without ever saying". The split is
// worth nothing unless the durable record can carry it: a server that dropped
// "unreported" — or worse, quietly mapped it onto "skipped" — would leave the
// run record making the same false claim the tree used to make, which is the
// exact asymmetry #1026 set out to end.
func TestPhaseTransition_UnreportedReachesTheRecord(t *testing.T) {
	root := t.TempDir()
	s := NewServer(nil, WithWorkspaceRoot(root))

	const (
		repo  = "acme/platform"
		issue = 1246
	)
	runID := newTestRunID()

	mustCall(t, s, "pipeline.notifyStageTransition", PipelineNotifyStageTransitionParams{
		Repo: repo, IssueNumber: issue, Stage: "feature-dev", Status: "running", RunID: runID,
	})

	// A real skip and an unreported back-fill, side by side.
	mustCall(t, s, "pipeline.notifyPhaseTransition", PipelineNotifyPhaseTransitionParams{
		Repo: repo, IssueNumber: issue, Stage: "feature-dev", Name: "e2e-testing",
		Index: 10, Total: 18, EventType: "skip", RunID: runID,
	})
	mustCall(t, s, "pipeline.notifyPhaseTransition", PipelineNotifyPhaseTransitionParams{
		Repo: repo, IssueNumber: issue, Stage: "feature-dev", Name: "testing",
		Index: 9, Total: 18, EventType: "unreported", RunID: runID,
	})

	rt, _ := s.currentRunForIssue(repo, issue)
	if rt == nil {
		t.Fatal("no runtime registered for the run")
	}

	byName := map[string]string{}
	for _, p := range rt.rs.PhaseHistory {
		byName[p.Name] = p.Status
	}
	if byName["testing"] != "unreported" {
		t.Errorf("testing = %q, want unreported — a back-fill is an absence of evidence, not a decision",
			byName["testing"])
	}
	if byName["e2e-testing"] != "skipped" {
		t.Errorf("e2e-testing = %q, want skipped — a deliberate skip must stay distinguishable",
			byName["e2e-testing"])
	}
}

// TestPhaseTransition_ObservedUpgradesOnlyUnreported pins the wire half of
// #1887's extension route. The legacy pipeline back-fills silence as
// `unreported` at stage exit, before its gate runs, so the reconciler's
// verdicts arrive after the back-fill. "observed" must upgrade that absence of
// evidence and must leave a marker-recorded outcome alone, or the replay either
// does nothing (the 4/14 the issue reports) or overwrites real evidence.
func TestPhaseTransition_ObservedUpgradesOnlyUnreported(t *testing.T) {
	root := t.TempDir()
	s := NewServer(nil, WithWorkspaceRoot(root))

	const (
		repo  = "acme/platform"
		issue = 1887
	)
	runID := newTestRunID()
	mustCall(t, s, "pipeline.notifyStageTransition", PipelineNotifyStageTransitionParams{
		Repo: repo, IssueNumber: issue, Stage: "feature-dev", Status: "running", RunID: runID,
	})

	send := func(name, eventType, status string, index int) {
		mustCall(t, s, "pipeline.notifyPhaseTransition", PipelineNotifyPhaseTransitionParams{
			Repo: repo, IssueNumber: issue, Stage: "feature-dev", Name: name,
			Index: index, Total: 18, EventType: eventType, Status: status, RunID: runID,
		})
	}
	send("testing", "unreported", "", 8)
	send("e2e-testing", "skip", "", 9)
	send("testing", "observed", "complete", 8)
	send("e2e-testing", "observed", "complete", 9)
	send("write-dev-context", "observed", "complete", 13)

	rt, _ := s.currentRunForIssue(repo, issue)
	if rt == nil {
		t.Fatal("no runtime registered for the run")
	}
	byName := map[string]string{}
	for _, p := range rt.rs.PhaseHistory {
		byName[p.Name] = p.Status
	}
	if byName["testing"] != "complete" {
		t.Errorf("testing = %q, want complete — proof must replace an unreported back-fill", byName["testing"])
	}
	if byName["e2e-testing"] != "skipped" {
		t.Errorf("e2e-testing = %q, want skipped — a recorded outcome keeps it", byName["e2e-testing"])
	}
	if byName["write-dev-context"] != "complete" {
		t.Errorf("write-dev-context = %q, want complete — a phase with no record is appended", byName["write-dev-context"])
	}

	err := callRunVerb(t, s, "pipeline.notifyPhaseTransition", PipelineNotifyPhaseTransitionParams{
		Repo: repo, IssueNumber: issue, Stage: "feature-dev", Name: "testing",
		Index: 8, Total: 18, EventType: "observed", Status: "running", RunID: runID,
	})
	if err == nil || !strings.Contains(err.Error(), "observed needs status") {
		t.Errorf("observed with status running: err = %v, want a refusal", err)
	}
}
