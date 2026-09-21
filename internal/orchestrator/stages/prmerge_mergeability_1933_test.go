package stages

import (
	"context"
	"strings"
	"testing"
)

// Issue #1933 — a PR is punted to the LLM eleven seconds in because GitHub has
// not computed mergeability yet.
//
// GitHub computes `mergeable` asynchronously and answers UNKNOWN for the first
// seconds after a PR is created. pr-merge starts seconds after pr-create, so
// the first snapshot of a pipeline-authored PR routinely has no verdict — and
// UNKNOWN was read as a hard negative twice over: Decide punts on
// `Mergeable != "MERGEABLE"` at its FIRST test, and MergeBlockedByPendingCI
// demands MERGEABLE too, so the #297 CI wait never got its turn at all. The LLM
// then babysat CI to green, the single largest line in the run.
//
// The race is real but not certain: a run whose first snapshot happens to
// arrive after GitHub finished computing takes the deterministic path and looks
// fine. These tests pin the losing side of the race, which is the side that
// costs money.

// newRunnerWithSeqMergeability builds a runner over scripted snapshots with no
// polling delay and an explicit mergeability budget. The EC fetch is
// single-shot so each View drains exactly one scripted snapshot.
func newRunnerWithSeqMergeability(seq *sequenceGh, prNumber, mergeabilityMax, ciPollMax int) *DeterministicRunner {
	r := NewDeterministicRunnerWithClient(seq)
	r.pollInterval = 0
	r.pollMax = 1
	r.ciPollInterval = 0
	r.ciPollMax = ciPollMax
	r.mergeabilityPollInterval = 0
	r.mergeabilityPollMax = mergeabilityMax
	r.prContextRead = func(_ string, _ int) (int, error) { return prNumber, nil }
	return r
}

func TestDeterministicRunner_MergeabilityUnknown_WaitsThenMerges(t *testing.T) {
	unknown := PRViewSnapshot{State: "OPEN", Mergeable: mergeableUnknown, MergeStateStatus: "UNKNOWN", ReviewDecision: "APPROVED"}
	clean := PRViewSnapshot{State: "OPEN", Mergeable: "MERGEABLE", MergeStateStatus: "CLEAN", ReviewDecision: "APPROVED", StatusCheckRollup: []PRStatusCheckRow{{Name: "ci", Conclusion: "SUCCESS"}}}
	merged := PRViewSnapshot{State: "MERGED"}
	seq := &sequenceGh{responses: []sequenceResp{
		{snap: unknown}, // initial fetch → GitHub still computing
		{snap: unknown}, // mergeability poll 1 → still computing
		{snap: clean},   // mergeability poll 2 → verdict landed, CI already green
		{snap: merged},  // post-merge EC re-poll
	}}
	r := newRunnerWithSeqMergeability(seq, 42, 5, 5)

	res, err := r.Run(context.Background(), 100, "owner/repo", "/tmp")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if res.Path != PathMerged {
		t.Fatalf("Path = %q (reason %q), want merged — an UNKNOWN mergeability must be waited for, not punted (#1933)", res.Path, res.Reason)
	}
	if seq.mergeCalls != 1 {
		t.Errorf("Merge call count = %d, want exactly 1", seq.mergeCalls)
	}
}

// The load-bearing one: the whole cost of #1933 was that an unresolved verdict
// denied the #297 CI wait its turn. Here the verdict lands on a PR whose CI is
// still pending, so the run must end up in the CI wait and merge from there —
// not punt.
func TestDeterministicRunner_MergeabilityUnknown_ThenCIWaitGetsItsTurn(t *testing.T) {
	unknown := PRViewSnapshot{State: "OPEN", Mergeable: mergeableUnknown, MergeStateStatus: "UNKNOWN", ReviewDecision: "APPROVED"}
	pending := PRViewSnapshot{State: "OPEN", Mergeable: "MERGEABLE", MergeStateStatus: "BLOCKED", ReviewDecision: "APPROVED", StatusCheckRollup: []PRStatusCheckRow{{Name: "ci", Conclusion: ""}}}
	clean := PRViewSnapshot{State: "OPEN", Mergeable: "MERGEABLE", MergeStateStatus: "CLEAN", ReviewDecision: "APPROVED", StatusCheckRollup: []PRStatusCheckRow{{Name: "ci", Conclusion: "SUCCESS"}}}
	merged := PRViewSnapshot{State: "MERGED"}
	seq := &sequenceGh{responses: []sequenceResp{
		{snap: unknown}, // initial fetch
		{snap: pending}, // mergeability poll 1 → verdict landed; CI still running
		{snap: pending}, // CI wait poll 1
		{snap: clean},   // CI wait poll 2 → green
		{snap: merged},  // post-merge EC re-poll
	}}
	r := newRunnerWithSeqMergeability(seq, 42, 5, 5)

	res, err := r.Run(context.Background(), 100, "owner/repo", "/tmp")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if res.Path != PathMerged {
		t.Fatalf("Path = %q (reason %q), want merged — after mergeability resolves the #297 CI wait must get its turn (#1933)", res.Path, res.Reason)
	}
	if seq.mergeCalls != 1 {
		t.Errorf("Merge call count = %d, want exactly 1", seq.mergeCalls)
	}
}

func TestDeterministicRunner_MergeabilityUnknown_TimesOut_PuntsNamingTheAbsence(t *testing.T) {
	unknown := PRViewSnapshot{State: "OPEN", Mergeable: mergeableUnknown, MergeStateStatus: "UNKNOWN", ReviewDecision: "APPROVED"}
	seq := &sequenceGh{responses: []sequenceResp{{snap: unknown}}} // never resolves
	r := newRunnerWithSeqMergeability(seq, 42, 3, 5)

	res, err := r.Run(context.Background(), 100, "owner/repo", "/tmp")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if res.Path != PathPunt {
		t.Fatalf("Path = %q, want punt once the mergeability budget expires", res.Path)
	}
	if res.Reason != ReasonMergeabilityUnresolved {
		t.Errorf("Reason = %q, want %q", res.Reason, ReasonMergeabilityUnresolved)
	}
	// The distinction is the point: an absent verdict must never be reported as
	// a conflict the runner diagnosed.
	if strings.Contains(res.Reason, ReasonNotMergeable) {
		t.Errorf("Reason %q reads as a real not-mergeable verdict; UNKNOWN is the ABSENCE of one (#1933)", res.Reason)
	}
	if seq.mergeCalls != 0 {
		t.Errorf("Merge call count = %d, want 0 — nothing may merge without a verdict", seq.mergeCalls)
	}
}

// A real conflict must keep punting immediately. The wait is scoped to the
// absence of a verdict; CONFLICTING is a verdict, and waiting on it would only
// delay the LLM path that has to resolve it.
func TestDeterministicRunner_Conflicting_PuntsWithoutWaiting(t *testing.T) {
	conflicting := PRViewSnapshot{State: "OPEN", Mergeable: "CONFLICTING", MergeStateStatus: "DIRTY", ReviewDecision: "APPROVED"}
	seq := &sequenceGh{responses: []sequenceResp{{snap: conflicting}}}
	r := newRunnerWithSeqMergeability(seq, 42, 5, 5)

	res, err := r.Run(context.Background(), 100, "owner/repo", "/tmp")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if res.Path != PathPunt {
		t.Fatalf("Path = %q, want punt on a real conflict", res.Path)
	}
	if !strings.Contains(res.Reason, ReasonNotMergeable) || !strings.Contains(res.Reason, "CONFLICTING") {
		t.Errorf("Reason = %q, want a not-mergeable verdict naming CONFLICTING", res.Reason)
	}
	// One View for the initial fetch and nothing more: a verdict is not waited on.
	if seq.viewCalls != 1 {
		t.Errorf("View call count = %d, want 1 — a real CONFLICTING verdict must not enter the mergeability wait", seq.viewCalls)
	}
}

// A PR that leaves OPEN while the wait is running has a verdict of its own.
func TestWaitForMergeability_StopsWhenPRLeavesOpen(t *testing.T) {
	unknown := PRViewSnapshot{State: "OPEN", Mergeable: mergeableUnknown}
	merged := PRViewSnapshot{State: "MERGED"}
	seq := &sequenceGh{responses: []sequenceResp{{snap: unknown}, {snap: merged}}}
	r := newRunnerWithSeqMergeability(seq, 42, 10, 5)

	got, err := r.waitForMergeability(context.Background(), seq, 42, unknown)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if got.State != "MERGED" {
		t.Errorf("State = %q, want MERGED", got.State)
	}
	if seq.viewCalls > 2 {
		t.Errorf("View call count = %d, want the wait to stop as soon as the PR left OPEN", seq.viewCalls)
	}
}

// The budget, not the first bad response, bounds the wait.
func TestWaitForMergeability_TransientErrorDoesNotEndTheWait(t *testing.T) {
	unknown := PRViewSnapshot{State: "OPEN", Mergeable: mergeableUnknown}
	resolved := PRViewSnapshot{State: "OPEN", Mergeable: "MERGEABLE", MergeStateStatus: "CLEAN"}
	seq := &sequenceGh{responses: []sequenceResp{
		{err: context.DeadlineExceeded}, // transient
		{snap: resolved},
	}}
	r := newRunnerWithSeqMergeability(seq, 42, 5, 5)

	got, err := r.waitForMergeability(context.Background(), seq, 42, unknown)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if got.Mergeable != "MERGEABLE" {
		t.Errorf("Mergeable = %q, want MERGEABLE — a transient fetch error must not end the wait", got.Mergeable)
	}
}
