package orchestrator

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/hooks"
	"github.com/nightgauge/nightgauge/pkg/types"
)

// The PR-recheck sweep (#2607). The fixture is revertFixture: one repository,
// O/a, whose open-PR list answers from the given pages (a nil page fails the
// read) and whose board refuses every write. The PR state and the post-merge
// path go through the sweep's seams.

// recheckSeams installs a PR-state answer and a counting post-merge fake.
func recheckSeams(as *AutonomousScheduler, prState func() (string, error)) (postMerges, stateReads *atomic.Int32) {
	postMerges, stateReads = &atomic.Int32{}, &atomic.Int32{}
	as.prStateFn = func(context.Context, string, string, int) (string, error) {
		stateReads.Add(1)
		return prState()
	}
	as.postMergeFn = func(context.Context, types.BoardItem, int) hooks.PostMergeResult {
		postMerges.Add(1)
		return hooks.PostMergeResult{IssueClosed: true}
	}
	return postMerges, stateReads
}

func pendingRecheck(as *AutonomousScheduler, repo string, issue int) (PRRecheckItem, bool) {
	as.mu.Lock()
	defer as.mu.Unlock()
	for _, e := range as.state.PendingPRRechecks {
		if e.Repo == repo && e.Number == issue {
			return e, true
		}
	}
	return PRRecheckItem{}, false
}

func TestReconcilePRRechecks_UnreadablePRKeepsTheEntry(t *testing.T) {
	buf := withCapturedLog(t)
	as, writes, _ := revertFixture(t)
	postMerges, _ := recheckSeams(as, func() (string, error) { return "", errors.New("dial tcp: no route to host") })
	as.enqueuePRRecheck("O/a", 1, 42, TerminalKindApiConnectionLost)

	as.reconcilePRRechecks(context.Background())

	if _, ok := pendingRecheck(as, "O/a", 1); !ok {
		t.Fatal("the entry was dropped although the forge never answered")
	}
	if n := postMerges.Load(); n != 0 {
		t.Errorf("post-merge ran %d times without a MERGED answer", n)
	}
	if n := writes.Load(); n != 0 {
		t.Errorf("board requests = %d, want 0", n)
	}
	if !strings.Contains(buf.String(), "still unreadable") {
		t.Errorf("want a log naming the unread PR; got %q", buf.String())
	}
}

func TestReconcilePRRechecks_MergedRunsPostMergeOnceAndRecordsCompletion(t *testing.T) {
	as, _, _ := revertFixture(t)
	postMerges, _ := recheckSeams(as, func() (string, error) { return "MERGED", nil })
	as.state.Failed = []FailedItem{{Repo: "O/a", Number: 1, Title: "the issue", Kind: TerminalKindApiConnectionLost}}
	as.retryBackoff = map[string]retryPlan{"O/a#1": {}}
	as.lastEpicRollupSweepAt = time.Now()
	as.enqueuePRRecheck("O/a", 1, 42, TerminalKindApiConnectionLost)

	as.reconcilePRRechecks(context.Background())

	if n := postMerges.Load(); n != 1 {
		t.Fatalf("post-merge ran %d times, want 1", n)
	}
	if _, ok := pendingRecheck(as, "O/a", 1); ok {
		t.Error("the entry survived a MERGED answer")
	}
	if len(as.state.Failed) != 0 {
		t.Errorf("Failed = %+v, want empty after the merge", as.state.Failed)
	}
	if _, ok := as.retryBackoff["O/a#1"]; ok {
		t.Error("the retry backoff survived the merge; the issue would be re-dispatched")
	}
	if len(as.state.Completed) != 1 || as.state.Completed[0].Title != "the issue" {
		t.Errorf("Completed = %+v, want one entry carrying the Failed title", as.state.Completed)
	}
	if !as.lastEpicRollupSweepAt.IsZero() {
		t.Error("the rollup pace was not reset; other boards would wait an interval for Done")
	}

	as.reconcilePRRechecks(context.Background())
	if n := postMerges.Load(); n != 1 {
		t.Errorf("a second sweep re-ran post-merge: %d runs", n)
	}
}

// An open PR on a transient kind goes to Ready (#2554), so the re-dispatch
// resumes at pr-merge on the same PR rather than opening a second one.
func TestReconcilePRRechecks_OpenPRRevertsToReady(t *testing.T) {
	buf := withCapturedLog(t)
	as, writes, _ := revertFixture(t, map[int]string{42: "feat/1-x"})
	postMerges, _ := recheckSeams(as, func() (string, error) { return "OPEN", nil })
	as.enqueuePRRecheck("O/a", 1, 42, TerminalKindApiConnectionLost)

	as.reconcilePRRechecks(context.Background())

	got := buf.String()
	if !strings.Contains(got, "after transient api_connection_lost (open PR #42)") {
		t.Errorf("want the transient Ready revert naming PR #42; got %q", got)
	}
	if strings.Contains(got, "move-to-in-review:") {
		t.Errorf("a transient open-PR entry moved to In review: %q", got)
	}
	if writes.Load() == 0 {
		t.Error("no board write was attempted")
	}
	if n := postMerges.Load(); n != 0 {
		t.Errorf("post-merge ran %d times for an open PR", n)
	}
	if _, ok := pendingRecheck(as, "O/a", 1); ok {
		t.Error("the entry survived an answered revert")
	}
}

func TestReconcilePRRechecks_ClosedUnmergedPRReverts(t *testing.T) {
	buf := withCapturedLog(t)
	as, _, _ := revertFixture(t, map[int]string{})
	postMerges, _ := recheckSeams(as, func() (string, error) { return "CLOSED", nil })
	as.enqueuePRRecheck("O/a", 1, 42, TerminalKindApiConnectionLost)

	as.reconcilePRRechecks(context.Background())

	if !strings.Contains(buf.String(), "back to Ready") {
		t.Errorf("want the Ready revert for a closed, unmerged PR; got %q", buf.String())
	}
	if n := postMerges.Load(); n != 0 {
		t.Errorf("post-merge ran %d times for an unmerged PR", n)
	}
	if _, ok := pendingRecheck(as, "O/a", 1); ok {
		t.Error("the entry survived an answered revert")
	}
}

// With no PR on record the open-PR list decides, and an unread list keeps the
// entry for the next scan.
func TestReconcilePRRechecks_NoPRAndUnreadListKeepsTheEntry(t *testing.T) {
	as, writes, _ := revertFixture(t)
	_, stateReads := recheckSeams(as, func() (string, error) { return "MERGED", nil })
	as.enqueuePRRecheck("O/a", 1, 0, "")

	as.reconcilePRRechecks(context.Background())

	if _, ok := pendingRecheck(as, "O/a", 1); !ok {
		t.Fatal("the entry was dropped although the open-PR list was never read")
	}
	if n := stateReads.Load(); n != 0 {
		t.Errorf("PR state read %d times for an entry with no PR", n)
	}
	if n := writes.Load(); n != 0 {
		t.Errorf("board requests = %d, want 0", n)
	}
}

// A running issue follows its own PR; the sweep must not move its board row.
func TestReconcilePRRechecks_SkipsARunningIssue(t *testing.T) {
	as, writes, _ := revertFixture(t, map[int]string{})
	postMerges, stateReads := recheckSeams(as, func() (string, error) { return "MERGED", nil })
	as.enqueuePRRecheck("O/a", 1, 42, TerminalKindApiConnectionLost)
	as.state.Running = []RunningItem{{Repo: "O/a", Number: 1}}

	as.reconcilePRRechecks(context.Background())

	if stateReads.Load() != 0 || postMerges.Load() != 0 || writes.Load() != 0 {
		t.Errorf("a running issue was rechecked: reads=%d post-merges=%d writes=%d",
			stateReads.Load(), postMerges.Load(), writes.Load())
	}
	if _, ok := pendingRecheck(as, "O/a", 1); !ok {
		t.Error("the entry was dropped while the issue ran")
	}
}

func TestEnqueuePRRecheck_OneEntryPerIssueKeepingTheKnownPR(t *testing.T) {
	as, _, _ := revertFixture(t)
	as.enqueuePRRecheck("O/a", 1, 42, TerminalKindApiConnectionLost)
	as.enqueuePRRecheck("O/a", 1, 0, "")

	if n := len(as.state.PendingPRRechecks); n != 1 {
		t.Fatalf("entries = %d, want 1", n)
	}
	e := as.state.PendingPRRechecks[0]
	if e.PRNumber != 42 || e.Kind != TerminalKindApiConnectionLost {
		t.Errorf("entry = %+v, want PR #42 and the transient kind kept", e)
	}
}

// A run that succeeds saw its own PR, so its pending recheck is dropped.
func TestOnPipelineComplete_SuccessDropsThePendingRecheck(t *testing.T) {
	as, _, _ := revertFixture(t)
	as.enqueuePRRecheck("O/a", 1, 42, TerminalKindApiConnectionLost)
	as.state.Running = []RunningItem{{Repo: "O/a", Number: 1}}

	as.onPipelineComplete("O/a", 1, true, false, "", "", false)
	as.drainBackground()

	if _, ok := pendingRecheck(as, "O/a", 1); ok {
		t.Error("a successful run left its recheck queued")
	}
}

// The scan cycle dispatches the sweep detached, one at a time: a second scan
// while a sweep is still waiting on the forge starts nothing.
func TestStartPRRecheckSweep_OneSweepInFlight(t *testing.T) {
	as, _, _ := revertFixture(t)
	release := make(chan struct{})
	_, stateReads := recheckSeams(as, func() (string, error) {
		<-release
		return "", errors.New("still offline")
	})
	as.enqueuePRRecheck("O/a", 1, 42, TerminalKindApiConnectionLost)

	as.startPRRecheckSweep()
	as.startPRRecheckSweep()
	close(release)
	as.waitBackground()

	if n := stateReads.Load(); n != 1 {
		t.Errorf("PR state reads = %d, want 1: a second sweep started while one was in flight", n)
	}
	as.startPRRecheckSweep()
	as.waitBackground()
	if n := stateReads.Load(); n != 2 {
		t.Errorf("PR state reads = %d, want 2: the guard was not released after the sweep", n)
	}
}
