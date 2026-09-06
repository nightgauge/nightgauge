package orchestrator

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/config"
	"github.com/nightgauge/nightgauge/internal/depgraph"
)

// Regression suite for #1486 — the graph reconcile re-admitted every still-OPEN
// Failed item, so a halt raised for a human was re-dispatched in the same cycle
// that raised it. Observed on the first autonomous day: an architecture-approval
// gate raised at 16:53:15.322 was re-admitted at 16:53:15.343 and dispatched at
// 16:53:18.575, spending $4.25 and 17.6 minutes changing an Android application
// id, an iOS xcconfig, fastlane and three workflows that nobody had approved.
//
// The fix is one field — FailedItem.Kind — plus the two readers that need it:
// the reconcile (which now holds a human-decision kind instead of re-admitting
// it) and the candidate filter (which excludes held items from the instant the
// halt is recorded, without waiting for the asynchronous board move).

func heldTestScheduler(t *testing.T) *AutonomousScheduler {
	t.Helper()
	stubReconcileGhUnreachable(t)
	as := newAutonomousForCascadeTest(t, 5, 30*time.Minute)
	as.workspaceRoot = t.TempDir()
	as.state.LifetimeIssueFailures = map[string]int{}
	as.perIssueFailureCount = map[string]int{}
	as.retryBackoff = map[string]retryPlan{}
	return as
}

func holdTestGraph(node *depgraph.Node) *depgraph.Graph {
	return buildTestGraph([]*depgraph.Node{node}, nil)
}

func isCandidate(candidates []CandidateItem, repo string, number int) bool {
	for _, c := range candidates {
		if c.Repo == repo && c.Number == number {
			return true
		}
	}
	return false
}

// TestArchitectureApprovalHalt_HeldByReconcileAndNotACandidate is the primary
// regression: raise the sideline through the real onPipelineComplete path, run
// the reconcile that used to undo it, and assert both halves — the entry stays
// in state.Failed, and the issue is not a dispatch candidate even though its
// board row still reads "Ready" (the sideline's move to In progress is
// asynchronous and has not landed, which is precisely how the bug reached
// dispatch).
func TestArchitectureApprovalHalt_HeldByReconcileAndNotACandidate(t *testing.T) {
	as := heldTestScheduler(t)

	addRunning(as, "acme/flutter", 530, "Rename the application id")
	as.onPipelineComplete("acme/flutter", 530, false, false,
		TerminalKindArchitectureApprovalRequired,
		"ARCHITECTURE APPROVAL REQUIRED — a human must approve this decision")
	as.drainBackground()

	if got := as.state.Failed[0].Kind; got != TerminalKindArchitectureApprovalRequired {
		t.Fatalf("FailedItem.Kind = %q, want %q — without the kind the reconcile cannot tell a halt from a crash",
			got, TerminalKindArchitectureApprovalRequired)
	}

	g := holdTestGraph(&depgraph.Node{Repo: "acme/flutter", Number: 530, State: "OPEN", BoardStatus: "Ready"})
	as.reconcileStateAgainstGraph(g)

	if len(as.state.Failed) != 1 {
		t.Fatalf("architecture-approval halt was re-admitted by the rescan (Failed is empty); only the approval label or an approval file may release it")
	}
	if got := as.humanHoldFor("acme/flutter", 530); got != HoldArchitectureApproval {
		t.Errorf("humanHoldFor = %q, want %q — the 'no re-dispatch' log line is read off this", got, HoldArchitectureApproval)
	}
	if isCandidate(as.prioritize(context.Background(), g), "acme/flutter", 530) {
		t.Fatalf("acme/flutter#530 is a dispatch candidate while awaiting architecture approval — this is the $4.25 unapproved run")
	}
}

// TestArchitectureApprovalHold_ReleasedByApprovalLabel — the human action the
// halt's own message names. The label is the grant `nightgauge approval-gate`
// reads, so applying it must re-admit the issue on the very next reconcile.
func TestArchitectureApprovalHold_ReleasedByApprovalLabel(t *testing.T) {
	as := heldTestScheduler(t)
	as.state.Failed = []FailedItem{{
		Repo: "acme/flutter", Number: 530, Title: "Rename the application id",
		FailedAt: "2026-09-05T16:53:15Z", Kind: TerminalKindArchitectureApprovalRequired,
	}}

	held := holdTestGraph(&depgraph.Node{Repo: "acme/flutter", Number: 530, State: "OPEN", BoardStatus: "Ready"})
	as.reconcileStateAgainstGraph(held)
	if len(as.state.Failed) != 1 {
		t.Fatalf("precondition: expected the halt to still be held before approval")
	}

	approved := holdTestGraph(&depgraph.Node{
		Repo: "acme/flutter", Number: 530, State: "OPEN", BoardStatus: "Ready",
		Labels: []string{"type:bug", config.DefaultArchitectureApprovalLabel},
	})
	as.reconcileStateAgainstGraph(approved)

	if len(as.state.Failed) != 0 {
		t.Fatalf("approval label applied but the issue is still held — the label is the way back in")
	}
	if !isCandidate(as.prioritize(context.Background(), approved), "acme/flutter", 530) {
		t.Errorf("approved issue is not a dispatch candidate; approval must requeue it")
	}
}

// TestArchitectureApprovalHold_ReleasedByApprovalFile covers the gate's second
// grant, so the reconcile and `nightgauge approval-gate` cannot disagree about
// what counts as approved.
func TestArchitectureApprovalHold_ReleasedByApprovalFile(t *testing.T) {
	as := heldTestScheduler(t)
	as.state.Failed = []FailedItem{{
		Repo: "acme/flutter", Number: 530, FailedAt: "2026-09-05T16:53:15Z",
		Kind: TerminalKindArchitectureApprovalRequired,
	}}

	dir := filepath.Join(as.workspaceRoot, ".nightgauge", "pipeline")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "approval-530.json"), []byte(`{"approved": true}`), 0o600); err != nil {
		t.Fatalf("write approval file: %v", err)
	}

	g := holdTestGraph(&depgraph.Node{Repo: "acme/flutter", Number: 530, State: "OPEN", BoardStatus: "Ready"})
	as.reconcileStateAgainstGraph(g)

	if len(as.state.Failed) != 0 {
		t.Fatalf("approval file grants the gate but the issue is still held")
	}
}

// TestArchitectureApprovalHold_UnapprovedFileDoesNotRelease — an approval file
// that says `false`, or malformed JSON, is not approval. The hold fails toward
// keeping the human in the loop.
func TestArchitectureApprovalHold_UnapprovedFileDoesNotRelease(t *testing.T) {
	as := heldTestScheduler(t)
	as.state.Failed = []FailedItem{{
		Repo: "acme/flutter", Number: 530, FailedAt: "2026-09-05T16:53:15Z",
		Kind: TerminalKindArchitectureApprovalRequired,
	}}
	dir := filepath.Join(as.workspaceRoot, ".nightgauge", "pipeline")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "approval-530.json"), []byte(`{"approved": false}`), 0o600); err != nil {
		t.Fatalf("write approval file: %v", err)
	}

	as.reconcileStateAgainstGraph(holdTestGraph(&depgraph.Node{
		Repo: "acme/flutter", Number: 530, State: "OPEN", BoardStatus: "Ready",
	}))

	if len(as.state.Failed) != 1 {
		t.Fatalf("an explicitly unapproved file released the hold; only {\"approved\": true} is a grant")
	}
}

// TestNotPipelineActionable_StaysParkedAcrossRescans is the dashboard#778
// regression: the park was undone two seconds after it was raised, and the
// re-dispatched run then discovered a blocked dependency and scheduled a retry.
func TestNotPipelineActionable_StaysParkedAcrossRescans(t *testing.T) {
	as := heldTestScheduler(t)

	addRunning(as, "acme/dashboard", 778, "Publish the privacy policy")
	as.onPipelineComplete("acme/dashboard", 778, false, false,
		TerminalKindNotPipelineActionable,
		"not pipeline-actionable — the issue's deliverable requires a human")
	as.drainBackground()

	g := holdTestGraph(&depgraph.Node{Repo: "acme/dashboard", Number: 778, State: "OPEN", BoardStatus: "Ready"})
	for i := 0; i < 3; i++ {
		as.reconcileStateAgainstGraph(g)
		if len(as.state.Failed) != 1 {
			t.Fatalf("rescan %d re-admitted a not-pipeline-actionable park; nothing the pipeline observes may release it", i+1)
		}
		if isCandidate(as.prioritize(context.Background(), g), "acme/dashboard", 778) {
			t.Fatalf("rescan %d made the parked issue a dispatch candidate", i+1)
		}
	}
	if got := as.humanHoldFor("acme/dashboard", 778); got != HoldOperatorResume {
		t.Errorf("humanHoldFor = %q, want %q", got, HoldOperatorResume)
	}
}

// TestNotPipelineActionable_ReleasedByExplicitResume — `autonomous resume` is
// the operator act the hold waits for.
func TestNotPipelineActionable_ReleasedByExplicitResume(t *testing.T) {
	as := heldTestScheduler(t)
	as.state.Status = "paused"
	as.state.Failed = []FailedItem{{
		Repo: "acme/dashboard", Number: 778, FailedAt: "2026-09-05T17:17:08Z",
		Kind: TerminalKindNotPipelineActionable,
	}}

	as.Resume()
	as.drainBackground()

	if len(as.state.Failed) != 0 {
		t.Fatalf("explicit resume left the park in place; resume is the named way back in")
	}
	g := holdTestGraph(&depgraph.Node{Repo: "acme/dashboard", Number: 778, State: "OPEN", BoardStatus: "Ready"})
	if !isCandidate(as.prioritize(context.Background(), g), "acme/dashboard", 778) {
		t.Errorf("resumed issue is not a dispatch candidate")
	}
}

// TestNotPipelineActionable_ReleasedByClearIssueFailures — the per-issue escape
// hatch. It must work even though this kind never increments
// LifetimeIssueFailures, which is the map every pre-#1486 return path keyed on.
func TestNotPipelineActionable_ReleasedByClearIssueFailures(t *testing.T) {
	as := heldTestScheduler(t)
	as.state.Failed = []FailedItem{{
		Repo: "acme/dashboard", Number: 778, FailedAt: "2026-09-05T17:17:08Z",
		Kind: TerminalKindNotPipelineActionable,
	}}

	cleared, _ := as.ClearIssueFailures("acme/dashboard#778")
	if cleared != 1 {
		t.Errorf("ClearIssueFailures reported %d cleared, want 1", cleared)
	}
	if len(as.state.Failed) != 0 {
		t.Fatalf("clear-failures left the park in place")
	}
}

// TestResume_DoesNotGrantArchitectureApproval — Resume means "go again", not "I
// reviewed this architecture". Releasing the approval hold here would buy back
// exactly the unapproved production-touching run #1486 reports.
func TestResume_DoesNotGrantArchitectureApproval(t *testing.T) {
	as := heldTestScheduler(t)
	as.state.Status = "paused"
	as.state.Failed = []FailedItem{{
		Repo: "acme/flutter", Number: 530, FailedAt: "2026-09-05T16:53:15Z",
		Kind: TerminalKindArchitectureApprovalRequired,
	}}

	as.Resume()
	as.drainBackground()

	if len(as.state.Failed) != 1 {
		t.Fatalf("Resume released an architecture-approval hold; only the label or the approval file may")
	}
}

// TestRetryableFailure_StillReadmitted pins the behaviour the fix must NOT
// change: an ordinary crash is still recovered by the rescan.
func TestRetryableFailure_StillReadmitted(t *testing.T) {
	as := heldTestScheduler(t)
	as.state.Failed = []FailedItem{
		{Repo: "acme/app", Number: 10, Kind: TerminalKindSubagentCrash, FailedAt: "2026-09-05T10:00:00Z"},
		{Repo: "acme/app", Number: 11, Kind: "", FailedAt: "2026-09-05T10:00:00Z"}, // pre-#1486 entry
	}

	as.reconcileStateAgainstGraph(buildTestGraph([]*depgraph.Node{
		{Repo: "acme/app", Number: 10, State: "OPEN", BoardStatus: "Ready"},
		{Repo: "acme/app", Number: 11, State: "OPEN", BoardStatus: "Ready"},
	}, nil))

	if len(as.state.Failed) != 0 {
		t.Fatalf("retryable failures were held: %+v — a missing kind must read as retryable, which is the pre-#1486 behaviour", as.state.Failed)
	}
}

// TestHoldForTerminalKind pins the classification itself, including the kinds
// that are unrecoverable by retry but are NOT human decision points.
func TestHoldForTerminalKind(t *testing.T) {
	cases := map[string]string{
		TerminalKindArchitectureApprovalRequired: HoldArchitectureApproval,
		TerminalKindNotPipelineActionable:        HoldOperatorResume,
		TerminalKindSubagentCrash:                HoldNone,
		TerminalKindStallKill:                    HoldNone,
		TerminalKindBlockedDependency:            HoldNone,
		TerminalKindBranchForked:                 HoldNone,
		TerminalKindCommitOrphaned:               HoldNone,
		TerminalKindPrMergeUnmerged:              HoldNone,
		"":                                       HoldNone,
	}
	for kind, want := range cases {
		if got := HoldForTerminalKind(kind); got != want {
			t.Errorf("HoldForTerminalKind(%q) = %q, want %q", kind, got, want)
		}
	}
}

// TestFailedItemStateRoundTrip covers persistence in both directions: a state
// file written by this binary round-trips the kind, and one written before the
// field existed loads with an empty kind (i.e. retryable) rather than failing.
func TestFailedItemStateRoundTrip(t *testing.T) {
	original := AutonomousState{Failed: []FailedItem{
		{Repo: "acme/flutter", Number: 530, FailedAt: "2026-09-05T16:53:15Z",
			AttemptCount: 1, Kind: TerminalKindArchitectureApprovalRequired},
		{Repo: "acme/app", Number: 10, FailedAt: "2026-09-05T10:00:00Z", AttemptCount: 2},
	}}
	blob, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var round AutonomousState
	if err := json.Unmarshal(blob, &round); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got := round.Failed[0].Kind; got != TerminalKindArchitectureApprovalRequired {
		t.Errorf("kind did not survive the round trip: got %q", got)
	}
	if got := round.Failed[1].Kind; got != "" {
		t.Errorf("unset kind round-tripped as %q, want empty", got)
	}
	if bytes := string(blob); !json.Valid(blob) || bytes == "" {
		t.Fatalf("invalid state encoding")
	}

	// A state file written before FailedItem.Kind existed — no `kind` key at
	// all. It must load, and its entries must read as retryable.
	legacy := `{"status":"running","failed":[{"repo":"acme/app","number":10,` +
		`"failedAt":"2026-09-05T10:00:00Z","reason":"pipeline failure","attemptCount":3}]}`
	var loaded AutonomousState
	if err := json.Unmarshal([]byte(legacy), &loaded); err != nil {
		t.Fatalf("legacy state failed to load: %v", err)
	}
	if len(loaded.Failed) != 1 {
		t.Fatalf("legacy state lost its failed entry")
	}
	if loaded.Failed[0].Kind != "" {
		t.Errorf("legacy entry got kind %q, want empty", loaded.Failed[0].Kind)
	}
	if got := HoldForTerminalKind(loaded.Failed[0].Kind); got != HoldNone {
		t.Errorf("legacy entry is held (%q); an absent field must mean retryable", got)
	}
	if loaded.Failed[0].AttemptCount != 3 {
		t.Errorf("legacy AttemptCount = %d, want 3", loaded.Failed[0].AttemptCount)
	}
}

// TestRecordFailureLocked_KindTracksLatestAttempt — the deduped row describes
// the newest attempt, so a halt after a crash holds the issue and a crash after
// an approval does not.
func TestRecordFailureLocked_KindTracksLatestAttempt(t *testing.T) {
	as := &AutonomousScheduler{state: &AutonomousState{}}
	as.recordFailureLocked("r", 1, "A", "2026-09-05T01:00:00Z", "crash", TerminalKindSubagentCrash)
	as.recordFailureLocked("r", 1, "A", "2026-09-05T01:00:01Z", "gate", TerminalKindArchitectureApprovalRequired)
	if got := as.state.Failed[0].Kind; got != TerminalKindArchitectureApprovalRequired {
		t.Errorf("Kind = %q, want the latest attempt's kind", got)
	}

	as.recordFailureLocked("r", 1, "A", "2026-09-05T01:00:02Z", "crash", TerminalKindSubagentCrash)
	if got := as.state.Failed[0].Kind; got != TerminalKindSubagentCrash {
		t.Errorf("Kind = %q, want the latest attempt's kind", got)
	}
}

// TestDedupeFailedItems_KindFollowsLatest — legacy duplicate rows (one per
// attempt) must not collapse a hold into a retryable entry.
func TestDedupeFailedItems_KindFollowsLatest(t *testing.T) {
	out := dedupeFailedItems([]FailedItem{
		{Repo: "r", Number: 1, FailedAt: "2026-09-05T01:00:00Z", Kind: TerminalKindSubagentCrash},
		{Repo: "r", Number: 1, FailedAt: "2026-09-05T01:00:01Z", Kind: TerminalKindArchitectureApprovalRequired},
	})
	if len(out) != 1 {
		t.Fatalf("expected 1 merged entry, got %d", len(out))
	}
	if out[0].Kind != TerminalKindArchitectureApprovalRequired {
		t.Errorf("merged Kind = %q, want the latest attempt's kind", out[0].Kind)
	}
}
