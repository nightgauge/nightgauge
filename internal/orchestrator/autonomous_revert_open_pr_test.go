package orchestrator

import (
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
// head-branch" pair, as the open-PR list reads it.
func openPRPageJSON(prs map[int]string, hasNext bool, cursor string) string {
	nodes := make([]map[string]interface{}, 0, len(prs))
	for n, head := range prs {
		nodes = append(nodes, map[string]interface{}{"number": n, "headRefName": head})
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
	return as, writes, prReads
}

func TestRevertFailedIssueStatus_OpenPRMovesToInReviewNotReady(t *testing.T) {
	buf := withCapturedLog(t)
	// The issue's PR is on the second page: "no PR" must not be read off the first.
	as, writes, prReads := revertFixture(t,
		map[int]string{43: "feat/2-other"},
		map[int]string{42: "feat/1-x"},
	)

	as.revertFailedIssueStatus(as.backgroundContext(), "O/a", 1)

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

	as.revertFailedIssueStatus(as.backgroundContext(), "O/a", 1)

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

			as.revertFailedIssueStatus(as.backgroundContext(), "O/a", 1)

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

// The whole path: a terminal kind that can end a run in pr-merge, after
// pr-create opened the PR, must not send the issue to Ready.
func TestOnPipelineComplete_FailureAfterPRCreate_DoesNotRevertToReady(t *testing.T) {
	for _, kind := range []string{TerminalKindStallKill, TerminalKindOperatorStop, TerminalKindStreamIdleTimeout, ""} {
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
