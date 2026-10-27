package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/nightgauge/nightgauge/internal/depgraph"
	gh "github.com/nightgauge/nightgauge/internal/github"
	"github.com/nightgauge/nightgauge/internal/layout/layouttest"
	"github.com/nightgauge/nightgauge/internal/state"
)

// The failed-run revert must never put an issue in Ready on top of its own
// open PR (#1707). Every terminal kind that reaches it can end a run in a
// stage after pr-create (a stall-kill, an operator stop or an API outage
// during pr-merge), and Ready is the only dispatchable status.
//
// The fake forge answers the open-PR list from the fixture and every board
// request with an error, so a board write fails after it is attempted. Which
// write was attempted is read off the log, as the sidelineHalt tests do;
// writes counts board requests, so zero proves no write was attempted at all.

// openPRPageJSON renders one page of a repository's open PRs, each "number
// head-branch" pair, as the open-PR list reads it. A head written
// "fork:<branch>" is a branch in another repository (isCrossRepository).
func openPRPageJSON(prs map[int]string, hasNext bool, cursor string) string {
	nodes := make([]map[string]interface{}, 0, len(prs))
	for n, head := range prs {
		branch, fork := strings.CutPrefix(head, "fork:")
		nodes = append(nodes, map[string]interface{}{"number": n, "headRefName": branch, "isCrossRepository": fork})
	}
	b, _ := json.Marshal(map[string]interface{}{"data": map[string]interface{}{
		"repository": map[string]interface{}{"pullRequests": map[string]interface{}{
			"pageInfo": map[string]interface{}{"hasNextPage": hasNext, "endCursor": cursor},
			"nodes":    nodes,
		}},
	}})
	return string(b)
}

// answerNoOpenPRs answers an open-PR list request with an empty list and
// reports whether r was one.
func answerNoOpenPRs(w http.ResponseWriter, r *http.Request) bool {
	body, _ := io.ReadAll(r.Body)
	if !strings.Contains(string(body), "pullRequests(") {
		return false
	}
	_, _ = io.WriteString(w, openPRPageJSON(nil, false, ""))
	return true
}

// revertFixture returns a scheduler whose one repository, O/a, has a project
// board. pages are the open-PR list's pages in order; a nil page fails that
// read. writes counts every other request, all of which the forge refuses.
func revertFixture(t *testing.T, pages ...map[int]string) (as *AutonomousScheduler, writes, prReads *atomic.Int32) {
	t.Helper()
	writes, prReads = &atomic.Int32{}, &atomic.Int32{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(string(body), "pullRequests(") {
			i := int(prReads.Add(1)) - 1
			if i >= len(pages) || pages[i] == nil {
				_, _ = io.WriteString(w, `{"errors":[{"message":"open PR list unavailable"}]}`)
				return
			}
			last := i == len(pages)-1
			_, _ = io.WriteString(w, openPRPageJSON(pages[i], !last, fmt.Sprintf("cursor-%d", i+1)))
			return
		}
		writes.Add(1)
		_, _ = io.WriteString(w, `{"errors":[{"message":"forced failure"}]}`)
	}))
	t.Cleanup(srv.Close)
	repos := []depgraph.RepoConfig{{Owner: "O", Name: "a", Project: 7}}
	as = NewAutonomousScheduler(nil, gh.NewClientWithURL("test-token", srv.URL), repos, nil, DefaultAutonomousConfig(), layouttest.Repo(t))
	as.trustGateOverride = allWritersGate()
	return as, writes, prReads
}

func TestRevertFailedIssueStatus_OpenPRMovesToInReviewNotReady(t *testing.T) {
	buf := withCapturedLog(t)
	// The issue's PR is on the second page: "no PR" must not be read off the first.
	as, writes, prReads := revertFixture(t,
		map[int]string{43: "feat/2-other"},
		map[int]string{42: "feat/1-x"},
	)

	as.revertFailedIssueStatus(as.backgroundContext(), "O/a", 1, "")

	got := buf.String()
	if strings.Contains(got, "back to Ready") {
		t.Errorf("an issue its open PR backs was moved toward Ready: %q", got)
	}
	if !strings.Contains(got, "its PR #42 is open") || !strings.Contains(got, "move-to-in-review:") {
		t.Errorf("want the In review move, naming PR #42; got log: %q", got)
	}
	if n := prReads.Load(); n != 2 {
		t.Errorf("open-PR pages read = %d, want 2", n)
	}
	if writes.Load() == 0 {
		t.Error("no board write was attempted; the issue should move to In review")
	}
}

// A PR for another issue does not back this one, and an issue the extension
// parked in In review without a PR still returns to Ready: the board status is
// not consulted.
func TestRevertFailedIssueStatus_NoOwnPRStillRevertsToReady(t *testing.T) {
	buf := withCapturedLog(t)
	as, writes, _ := revertFixture(t, map[int]string{43: "feat/2-other", 44: "feat/10-ten"})

	as.revertFailedIssueStatus(as.backgroundContext(), "O/a", 1, "")

	got := buf.String()
	if !strings.Contains(got, "back to Ready") {
		t.Errorf("want the Ready revert for an issue no PR backs; got log: %q", got)
	}
	if strings.Contains(got, "move-to-in-review:") {
		t.Errorf("an issue no PR backs was moved to In review: %q", got)
	}
	if writes.Load() == 0 {
		t.Error("no board write was attempted")
	}
}

// A fork's PR is not the issue's own, whatever its branch is called: anyone
// who can open a pull request can name a branch feat/<n>-…, and counting it
// would let them keep a failed issue out of Ready (#1707).
func TestRevertFailedIssueStatus_ForkPRDoesNotBackTheIssue(t *testing.T) {
	buf := withCapturedLog(t)
	as, writes, _ := revertFixture(t, map[int]string{42: "fork:feat/1-x"})

	as.revertFailedIssueStatus(as.backgroundContext(), "O/a", 1, "")

	got := buf.String()
	if strings.Contains(got, "move-to-in-review:") || strings.Contains(got, "PR #42") {
		t.Errorf("a fork's PR named after the issue kept it out of Ready: %q", got)
	}
	if !strings.Contains(got, "back to Ready") {
		t.Errorf("want the Ready revert; got log: %q", got)
	}
	if writes.Load() == 0 {
		t.Error("no board write was attempted")
	}
}

// The in-review reconcile, the blocked-PR sweep, the dependency evidence gate
// and sidelineHalt all read their open PRs from openPRMergeStatesForRepo, so a
// fork's PR must not reach any of them either (#1707).
func TestOpenPRMergeStatesForRepo_IgnoresForkPRs(t *testing.T) {
	var jsonFields string
	stubReconcileGh(t, func(_ context.Context, args ...string) ([]byte, error) {
		for i, a := range args {
			if a == "--json" && i+1 < len(args) {
				jsonFields = args[i+1]
			}
		}
		return []byte(`[
			{"number":10,"headRefName":"feat/1-own","isCrossRepository":false,"mergeStateStatus":"BLOCKED"},
			{"number":11,"headRefName":"feat/2-forked","isCrossRepository":true,"mergeStateStatus":"BLOCKED"}
		]`), nil
	})
	as := &AutonomousScheduler{config: AutonomousConfig{}, state: &AutonomousState{}}

	states, ok := as.openPRMergeStatesForRepo(context.Background(), "O/app")
	if !ok {
		t.Fatal("openPRMergeStatesForRepo failed")
	}
	if !strings.Contains(jsonFields, "isCrossRepository") {
		t.Errorf("gh pr list --json %q does not ask whether a PR comes from a fork", jsonFields)
	}
	if pr, found := states[1]; !found || pr.Number != 10 {
		t.Errorf("issue 1's own PR = %+v (found %v), want PR #10", pr, found)
	}
	if pr, found := states[2]; found {
		t.Errorf("a fork's PR #%d was taken as issue 2's own", pr.Number)
	}
}

// An unanswered PR query is not permission to revert, even when an earlier
// page answered: the issue is left where it is, and the log says why.
func TestRevertFailedIssueStatus_PRQueryFailureLeavesStatusUnchanged(t *testing.T) {
	for name, pages := range map[string][]map[int]string{
		"first page": {nil},
		"later page": {{43: "feat/2-other"}, nil},
	} {
		t.Run(name, func(t *testing.T) {
			buf := withCapturedLog(t)
			as, writes, _ := revertFixture(t, pages...)

			as.revertFailedIssueStatus(as.backgroundContext(), "O/a", 1, "")

			if n := writes.Load(); n != 0 {
				t.Errorf("board requests = %d, want 0: no status may be written without the PR answer", n)
			}
			got := buf.String()
			if !strings.Contains(got, "NOT moving O/a#1 to Ready") || !strings.Contains(got, "could not list") {
				t.Errorf("want a log naming the unread PR list; got: %q", got)
			}
		})
	}
}

// The whole path: a non-transient terminal kind that can end a run in
// pr-merge, after pr-create opened the PR, must not send the issue to Ready.
// Transient kinds go to Ready on purpose (#2554), covered below.
func TestOnPipelineComplete_FailureAfterPRCreate_DoesNotRevertToReady(t *testing.T) {
	for _, kind := range []string{TerminalKindOperatorStop, ""} {
		t.Run("kind="+kind, func(t *testing.T) {
			buf := withCapturedLog(t)
			as, _, _ := revertFixture(t, map[int]string{42: "feat/1-x"})
			as.state.Running = []RunningItem{{Repo: "O/a", Number: 1, Title: "t"}}

			as.onPipelineComplete("O/a", 1, false, false, kind, "", false)
			// Join without cancelling: the revert's board work must complete
			// for its choice of status to be observable.
			as.waitBackground()

			got := buf.String()
			if strings.Contains(got, "back to Ready") {
				t.Errorf("failure %q after pr-create moved the issue toward Ready on top of its open PR: %q", kind, got)
			}
			if !strings.Contains(got, "move-to-in-review:") {
				t.Errorf("want the In review move; got log: %q", got)
			}
		})
	}
}

// Startup orphan recovery returns a crashed session's running items to Ready,
// and a session can crash in pr-merge, after pr-create opened the PR: it takes
// the same open-PR evidence as the failed-run revert (#1707).
func TestRecoverOrphanedRunningItems_RespectsTheIssuesOwnOpenPR(t *testing.T) {
	cases := []struct {
		name      string
		pages     []map[int]string
		wantLog   []string
		denyLog   []string
		wantWrite bool
	}{
		{
			name:      "own open PR moves to In review",
			pages:     []map[int]string{{42: "feat/1-x"}},
			wantLog:   []string{"its PR #42 is open", "move-to-in-review:"},
			denyLog:   []string{"back to Ready", "→ Ready"},
			wantWrite: true,
		},
		{
			name:      "a fork's PR does not back the issue",
			pages:     []map[int]string{{42: "fork:feat/1-x"}},
			wantLog:   []string{"back to Ready"},
			denyLog:   []string{"move-to-in-review:"},
			wantWrite: true,
		},
		{
			name:    "unread PR list leaves the status unchanged",
			pages:   []map[int]string{nil},
			wantLog: []string{"NOT moving O/a#1 to Ready", "could not list"},
			denyLog: []string{"back to Ready", "move-to-in-review:"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			buf := withCapturedLog(t)
			as, writes, _ := revertFixture(t, tc.pages...)

			as.recoverOrphanedRunningItems(context.Background(), []RunningItem{{Repo: "O/a", Number: 1}})

			got := buf.String()
			for _, want := range tc.wantLog {
				if !strings.Contains(got, want) {
					t.Errorf("log lacks %q: %q", want, got)
				}
			}
			for _, deny := range tc.denyLog {
				if strings.Contains(got, deny) {
					t.Errorf("log has %q: %q", deny, got)
				}
			}
			if wrote := writes.Load() > 0; wrote != tc.wantWrite {
				t.Errorf("board write attempted = %v, want %v", wrote, tc.wantWrite)
			}
			if n := len(as.state.Running); n != 0 {
				t.Errorf("state.Running has %d items after recovery, want 0", n)
			}
		})
	}
}

// #1496: a failed issue whose only PR is merged ends in Ready, the status the
// revert-status line names. A merged PR is not in the open list, so it does
// not back the issue, and no writer may put the issue in In review.
func TestRevertFailedIssueStatus_MergedPROnlyEndsReady(t *testing.T) {
	buf := withCapturedLog(t)
	as, writes, _ := revertFixture(t, map[int]string{})

	as.revertFailedIssueStatus(as.backgroundContext(), "O/a", 312, "")

	got := buf.String()
	if !strings.Contains(got, "revert-status:") || !strings.Contains(got, "back to Ready") {
		t.Errorf("want the Ready revert for an issue only a merged PR backs; got log: %q", got)
	}
	if strings.Contains(got, "move-to-in-review:") {
		t.Errorf("an issue with no open PR was moved to In review: %q", got)
	}
	if n := writes.Load(); n == 0 {
		t.Error("no board write was attempted")
	}
}

// moveIssueToInReview requires the open PR as evidence (#1496): without one it
// writes nothing at all, so "In review" never stands for a PR that is not there.
func TestMoveIssueToInReview_RequiresOpenPREvidence(t *testing.T) {
	buf := withCapturedLog(t)
	as, writes, prReads := revertFixture(t)

	as.moveIssueToInReview(as.backgroundContext(), "O/a", 1, 0)

	if n := writes.Load(); n != 0 {
		t.Errorf("board requests = %d, want 0 — In review was written with no open PR", n)
	}
	if n := prReads.Load(); n != 0 {
		t.Errorf("open-PR reads = %d, want 0", n)
	}
	if got := buf.String(); !strings.Contains(got, "no open PR backs it") {
		t.Errorf("want the refusal logged; got %q", got)
	}

	as.moveIssueToInReview(as.backgroundContext(), "O/a", 1, 42)
	if writes.Load() == 0 {
		t.Error("an open PR's In review move was not attempted")
	}
}

// A transient failure on an open PR returns the issue to Ready (#2554): the
// retry backoff gates the re-dispatch and the resume path runs pr-merge on the
// PR it already has. #1501's runaway kill mid-gate otherwise sat in In review
// with nothing left to re-run it.
func TestRevertFailedIssueStatus_TransientKindWithOpenPRMovesToReady(t *testing.T) {
	for _, kind := range []string{TerminalKindRunawayProgress, TerminalKindStallKill, TerminalKindApiOverloaded} {
		t.Run(kind, func(t *testing.T) {
			buf := withCapturedLog(t)
			as, writes, _ := revertFixture(t, map[int]string{42: "feat/1-x"})

			as.revertFailedIssueStatus(as.backgroundContext(), "O/a", 1, kind)

			got := buf.String()
			if strings.Contains(got, "move-to-in-review:") {
				t.Errorf("a transient %s on an open PR was moved to In review: %q", kind, got)
			}
			if !strings.Contains(got, "back to Ready after transient "+kind) || !strings.Contains(got, "open PR #42") {
				t.Errorf("want the Ready move naming %s and PR #42; got log: %q", kind, got)
			}
			if writes.Load() == 0 {
				t.Error("no board write was attempted")
			}
		})
	}
}

// Non-transient kinds keep the #1707 behaviour: an open PR means In review.
func TestRevertFailedIssueStatus_NonTransientKindWithOpenPRStaysInReview(t *testing.T) {
	for _, kind := range []string{TerminalKindOperatorStop, TerminalKindValidationFailed, ""} {
		t.Run(kind, func(t *testing.T) {
			buf := withCapturedLog(t)
			as, _, _ := revertFixture(t, map[int]string{42: "feat/1-x"})

			as.revertFailedIssueStatus(as.backgroundContext(), "O/a", 1, kind)

			got := buf.String()
			if strings.Contains(got, "Ready after transient") {
				t.Errorf("non-transient %q on an open PR was moved toward Ready: %q", kind, got)
			}
			if !strings.Contains(got, "move-to-in-review:") {
				t.Errorf("want the In review move for %q; got log: %q", kind, got)
			}
		})
	}
}

// The whole path for #1501: a runaway kill in a resumed pr-merge takes the
// stall-kill route (fixed backoff, no lifetime charge) and returns the issue to
// Ready on top of its open PR, where the resume path re-runs pr-merge (#2554).
func TestOnPipelineComplete_TransientFailureAfterPRCreate_RevertsToReady(t *testing.T) {
	for _, kind := range []string{TerminalKindRunawayProgress, TerminalKindStallKill, TerminalKindStreamIdleTimeout} {
		t.Run("kind="+kind, func(t *testing.T) {
			buf := withCapturedLog(t)
			as, _, _ := revertFixture(t, map[int]string{42: "feat/1-x"})
			as.state.Running = []RunningItem{{Repo: "O/a", Number: 1, Title: "t"}}

			as.onPipelineComplete("O/a", 1, false, false, kind, "", false)
			as.waitBackground()

			got := buf.String()
			if strings.Contains(got, "move-to-in-review:") {
				t.Errorf("transient %q after pr-create was moved to In review: %q", kind, got)
			}
			if !strings.Contains(got, "Ready after transient "+kind) {
				t.Errorf("want the Ready move after transient %s; got log: %q", kind, got)
			}
			as.mu.Lock()
			_, planned := as.retryBackoff["O/a#1"]
			lifetime := as.state.LifetimeIssueFailures["O/a#1"]
			as.mu.Unlock()
			if !planned {
				t.Errorf("no retry plan scheduled for transient %s", kind)
			}
			if lifetime != 0 {
				t.Errorf("transient %s charged %d lifetime failure(s), want 0", kind, lifetime)
			}
		})
	}
}

// #2607: an unread open-PR list queues the issue for the PR-recheck sweep and
// asks no human to move it by hand; the sweep retries once the forge answers.
func TestRevertFailedIssueStatus_PRQueryFailureQueuesARecheck(t *testing.T) {
	buf := withCapturedLog(t)
	as, _, _ := revertFixture(t)

	if answered := as.revertFailedIssueStatus(as.backgroundContext(), "O/a", 1, TerminalKindApiConnectionLost); answered {
		t.Error("revert reported an answer although the open-PR list was never read")
	}

	if e, ok := pendingRecheck(as, "O/a", 1); !ok || e.Kind != TerminalKindApiConnectionLost {
		t.Errorf("pending rechecks = %+v, want O/a#1 with the run's kind", as.state.PendingPRRechecks)
	}
	if got := buf.String(); strings.Contains(got, "by hand") || !strings.Contains(got, "will re-check once the forge answers") {
		t.Errorf("want the re-check log and no human instruction; got %q", got)
	}
}

func TestRecoverOrphanedRunningItems_PRQueryFailureQueuesARecheck(t *testing.T) {
	buf := withCapturedLog(t)
	as, _, _ := revertFixture(t)

	as.recoverOrphanedRunningItems(context.Background(), []RunningItem{{Repo: "O/a", Number: 1}})

	if _, ok := pendingRecheck(as, "O/a", 1); !ok {
		t.Error("an orphan whose open-PR list was unread was not queued for a recheck")
	}
	if strings.Contains(buf.String(), "by hand") {
		t.Errorf("recovery still asks a human to move the issue: %q", buf.String())
	}
}

// A transient kill after pr-create records the run's PR; any other kind, or a
// run with no PR, records nothing.
func TestRecordTransientRunPR(t *testing.T) {
	cases := []struct {
		name   string
		kind   string
		prURL  string
		wantPR int
	}{
		{"transient with a PR", TerminalKindApiConnectionLost, "https://github.com/O/a/pull/42", 42},
		{"transient without a PR", TerminalKindApiConnectionLost, "", 0},
		{"non-transient with a PR", TerminalKindOperatorStop, "https://github.com/O/a/pull/42", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			as, _, _ := revertFixture(t)
			rt := state.NewRuntimeState("O/a", 1, "item-1", testRunID())
			if tc.prURL != "" {
				rt.SetPrUrl(tc.prURL)
			}

			as.recordTransientRunPR("O/a", 1, rt, tc.kind)

			e, ok := pendingRecheck(as, "O/a", 1)
			if tc.wantPR == 0 {
				if ok {
					t.Errorf("queued %+v, want nothing", e)
				}
				return
			}
			if !ok || e.PRNumber != tc.wantPR {
				t.Errorf("pending recheck = %+v (found %v), want PR #%d", e, ok, tc.wantPR)
			}
		})
	}
}

// The merged PR closes the issue and it leaves the board graph; its Failed
// entry must outlive that while the recheck is pending, and only then.
func TestReconcileStateAgainstGraph_KeepsAFailureWithAPendingRecheck(t *testing.T) {
	as, _, _ := revertFixture(t)
	as.state.Failed = []FailedItem{
		{Repo: "O/a", Number: 1, Kind: TerminalKindApiConnectionLost},
		{Repo: "O/a", Number: 2, Kind: TerminalKindApiConnectionLost},
	}
	as.enqueuePRRecheck("O/a", 1, 42, TerminalKindApiConnectionLost)

	as.reconcileStateAgainstGraph(depgraph.NewGraph())

	if len(as.state.Failed) != 1 || as.state.Failed[0].Number != 1 {
		t.Errorf("Failed = %+v, want only O/a#1, whose recheck is pending", as.state.Failed)
	}
}
