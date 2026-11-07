package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/config"
	"github.com/nightgauge/nightgauge/internal/depgraph"
	gh "github.com/nightgauge/nightgauge/internal/github"
	"github.com/nightgauge/nightgauge/internal/layout/layouttest"
)

// Tests for the shared architecture-hold release (#2717). Approval used to
// retract the card and drop the Failed entry without touching the board, so
// the approved issue stayed In progress / In review — statuses the scheduler
// never dispatches — with nothing left to say it was stuck.

// releaseMove is one board write the release attempted.
type releaseMove struct {
	project int
	issue   int
	status  string
}

// releaseFixture is a scheduler whose forge answers the open- and merged-PR
// lists from the fixture maps (issue branch per PR number), with the board
// write captured by releaseMoveStatus.
type releaseFixture struct {
	as *AutonomousScheduler

	mu        sync.Mutex
	moves     []releaseMove
	failMoves int // the next failMoves writes fail
	openPRs   map[int]string
	mergedPRs map[int]string
	listFails bool
}

func (f *releaseFixture) recorded() []releaseMove {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]releaseMove(nil), f.moves...)
}

func newReleaseFixture(t *testing.T, repos []depgraph.RepoConfig) *releaseFixture {
	t.Helper()
	f := &releaseFixture{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.listFails {
			_, _ = io.WriteString(w, `{"errors":[{"message":"PR list unavailable"}]}`)
			return
		}
		switch {
		case strings.Contains(string(body), "MERGED"):
			nodes := []map[string]interface{}{}
			for n, head := range f.mergedPRs {
				nodes = append(nodes, map[string]interface{}{"number": n, "headRefName": head, "headRefOid": "abc"})
			}
			b, _ := json.Marshal(map[string]interface{}{"data": map[string]interface{}{
				"repository": map[string]interface{}{"pullRequests": map[string]interface{}{"nodes": nodes}},
			}})
			_, _ = w.Write(b)
		case strings.Contains(string(body), "pullRequests("):
			_, _ = io.WriteString(w, openPRPageJSON(f.openPRs, false, ""))
		case strings.Contains(string(body), "projectV2("):
			// holdIssue's onPipelineComplete schedules a best-effort
			// moveIssueToInProgress whose board read races drainBackground's
			// cancel. Answer it with a GraphQL error so it logs and returns
			// either way; release assertions read only releaseMoveStatus.
			_, _ = io.WriteString(w, `{"errors":[{"message":"board read not served by the release fixture"}]}`)
		default:
			t.Errorf("unexpected forge request: %s", body)
			_, _ = io.WriteString(w, `{"errors":[{"message":"unexpected"}]}`)
		}
	}))
	t.Cleanup(srv.Close)

	as := heldTestScheduler(t)
	as.workspaceRoot = layouttest.Repo(t)
	as.ghClient = gh.NewClientWithURL("test-token", srv.URL)
	as.repos = repos
	as.releaseMoveStatus = func(_ context.Context, _, _ string, project int, _ gh.OwnerType, issue int, status string) error {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.moves = append(f.moves, releaseMove{project: project, issue: issue, status: status})
		if f.failMoves > 0 {
			f.failMoves--
			return fmt.Errorf("forced board failure")
		}
		return nil
	}
	f.as = as
	return f
}

var releaseRepos = []depgraph.RepoConfig{{Owner: "acme", Name: "app", Project: 7}}

// holdIssue records an architecture-approval halt for acme/app#n.
func holdIssue(t *testing.T, as *AutonomousScheduler, n int) {
	t.Helper()
	addRunning(as, "acme/app", n, "High-impact decision")
	as.onPipelineComplete("acme/app", n, false, false,
		TerminalKindArchitectureApprovalRequired,
		"ARCHITECTURE APPROVAL REQUIRED — a human must approve this decision", false)
	as.drainBackground()
	if got := as.humanHoldFor("acme/app", n); got != HoldArchitectureApproval {
		t.Fatalf("precondition: hold = %q, want %q", got, HoldArchitectureApproval)
	}
}

func approvedNode(n int, status string) *depgraph.Node {
	return &depgraph.Node{
		Repo: "acme/app", Number: n, State: "OPEN", BoardStatus: status,
		Labels: []string{config.DefaultArchitectureApprovalLabel},
	}
}

func reconcileOnce(as *AutonomousScheduler, node *depgraph.Node) {
	as.reconcileStateAgainstGraph(holdTestGraph(node))
	// Join, not drain: drainBackground cancels first, aborting the move.
	as.waitBackground()
}

// A released hold moves the issue to Ready from either parked status, and the
// Failed entry is dropped only once the move landed.
func TestArchitectureRelease_MovesToReadyFromParkedStatuses(t *testing.T) {
	for _, status := range []string{"In progress", "In review"} {
		t.Run(status, func(t *testing.T) {
			buf := withCapturedLog(t)
			f := newReleaseFixture(t, releaseRepos)
			holdIssue(t, f.as, 10)

			reconcileOnce(f.as, approvedNode(10, status))

			moves := f.recorded()
			if len(moves) != 1 || moves[0] != (releaseMove{project: 7, issue: 10, status: "Ready"}) {
				t.Fatalf("board writes = %+v, want one move of #10 to Ready on project 7", moves)
			}
			if len(f.as.state.Failed) != 0 {
				t.Errorf("Failed = %+v, want the entry dropped after the move landed", f.as.state.Failed)
			}
			if !strings.Contains(buf.String(), "released acme/app#10 → Ready on 1 board(s)") {
				t.Errorf("no release log; got %q", buf.String())
			}
		})
	}
}

// The approval file releases the hold exactly as the label does.
func TestArchitectureRelease_ApprovalFileMovesToReady(t *testing.T) {
	f := newReleaseFixture(t, releaseRepos)
	holdIssue(t, f.as, 11)
	path := pipelineStatePath(f.as.workspaceRoot, "approval-11.json")
	if path == "" {
		t.Fatal("pipeline state dir unresolved")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"approved": true}`), 0o644); err != nil {
		t.Fatal(err)
	}

	reconcileOnce(f.as, &depgraph.Node{Repo: "acme/app", Number: 11, State: "OPEN", BoardStatus: "In progress"})

	if moves := f.recorded(); len(moves) != 1 || moves[0].status != "Ready" {
		t.Fatalf("board writes = %+v, want one move to Ready", moves)
	}
	if len(f.as.state.Failed) != 0 {
		t.Errorf("Failed = %+v, want the entry dropped", f.as.state.Failed)
	}
}

// An issue with its own open or merged PR is not moved, and its hold drops.
func TestArchitectureRelease_OwnPRIsNotMoved(t *testing.T) {
	cases := map[string]func(f *releaseFixture){
		"open":   func(f *releaseFixture) { f.openPRs = map[int]string{42: "feat/12-x"} },
		"merged": func(f *releaseFixture) { f.mergedPRs = map[int]string{43: "fix/12-y"} },
	}
	for name, seed := range cases {
		t.Run(name, func(t *testing.T) {
			buf := withCapturedLog(t)
			f := newReleaseFixture(t, releaseRepos)
			// Another issue's PR never counts.
			f.openPRs = map[int]string{50: "feat/99-other"}
			seed(f)
			holdIssue(t, f.as, 12)

			reconcileOnce(f.as, approvedNode(12, "In review"))

			if moves := f.recorded(); len(moves) != 0 {
				t.Fatalf("board writes = %+v, want none for an issue its own PR backs", moves)
			}
			if !strings.Contains(buf.String(), "NOT moving acme/app#12 to Ready — PR #") {
				t.Errorf("want the not-moved log naming the PR; got %q", buf.String())
			}
			if len(f.as.state.Failed) != 0 {
				t.Errorf("Failed = %+v, want the hold dropped", f.as.state.Failed)
			}
		})
	}
}

// An issue with an active run is not moved.
func TestArchitectureRelease_RunningIssueIsNotMoved(t *testing.T) {
	f := newReleaseFixture(t, releaseRepos)
	holdIssue(t, f.as, 13)
	addRunning(f.as, "acme/app", 13, "re-dispatched")

	reconcileOnce(f.as, approvedNode(13, "In progress"))

	if moves := f.recorded(); len(moves) != 0 {
		t.Fatalf("board writes = %+v, want none while a run is active", moves)
	}
}

// A failed board write is logged, never reported as a release, and retried on
// the next reconcile; the second attempt lands and only then releases.
func TestArchitectureRelease_FailedWriteRetriedNextReconcile(t *testing.T) {
	buf := withCapturedLog(t)
	f := newReleaseFixture(t, releaseRepos)
	f.failMoves = 1
	holdIssue(t, f.as, 14)

	reconcileOnce(f.as, approvedNode(14, "In progress"))

	if len(f.recorded()) != 1 {
		t.Fatalf("board writes = %+v, want one attempt", f.recorded())
	}
	if len(f.as.state.Failed) != 1 || !f.as.state.Failed[0].ReleasePending {
		t.Fatalf("Failed = %+v, want the entry kept as ReleasePending", f.as.state.Failed)
	}
	if strings.Contains(buf.String(), "released acme/app#14") {
		t.Fatalf("a failed write was reported as a release: %q", buf.String())
	}
	if !strings.Contains(buf.String(), "could not move acme/app#14 to Ready") {
		t.Errorf("want the failed write logged; got %q", buf.String())
	}
	// Still held against dispatch while the move is pending.
	if got := f.as.humanHoldFor("acme/app", 14); got != HoldArchitectureApproval {
		t.Errorf("hold = %q while the move is pending, want %q", got, HoldArchitectureApproval)
	}

	// The label is gone from this read: a pending release does not re-check it.
	reconcileOnce(f.as, &depgraph.Node{Repo: "acme/app", Number: 14, State: "OPEN", BoardStatus: "In progress"})

	if len(f.recorded()) != 2 {
		t.Fatalf("board writes = %+v, want the retry", f.recorded())
	}
	if len(f.as.state.Failed) != 0 {
		t.Errorf("Failed = %+v, want the entry dropped after the retry landed", f.as.state.Failed)
	}
	if !strings.Contains(buf.String(), "released acme/app#14 → Ready") {
		t.Errorf("no release log after the retry; got %q", buf.String())
	}
}

// An unreadable PR list is not "no PR": nothing moves and the entry stays.
func TestArchitectureRelease_UnreadablePRListKeepsHold(t *testing.T) {
	f := newReleaseFixture(t, releaseRepos)
	holdIssue(t, f.as, 15)
	f.mu.Lock()
	f.listFails = true
	f.mu.Unlock()

	reconcileOnce(f.as, approvedNode(15, "In progress"))

	if moves := f.recorded(); len(moves) != 0 {
		t.Fatalf("board writes = %+v, want none", moves)
	}
	if len(f.as.state.Failed) != 1 || !f.as.state.Failed[0].ReleasePending {
		t.Fatalf("Failed = %+v, want the entry kept as ReleasePending", f.as.state.Failed)
	}
}

// Every board that carries the repository is moved, not only the first.
func TestArchitectureRelease_MovesEveryBoard(t *testing.T) {
	f := newReleaseFixture(t, []depgraph.RepoConfig{
		{Owner: "acme", Name: "app", Project: 7},
		{Owner: "acme", Name: "app", Project: 9},
		{Owner: "acme", Name: "app", Project: 7}, // duplicate config entry
		{Owner: "acme", Name: "other", Project: 11},
	})
	holdIssue(t, f.as, 16)

	reconcileOnce(f.as, approvedNode(16, "In review"))

	got := map[int]int{}
	for _, m := range f.recorded() {
		got[m.project]++
	}
	if len(got) != 2 || got[7] != 1 || got[9] != 1 {
		t.Fatalf("moves per project = %v, want one each on 7 and 9", got)
	}
}

// The card's entry point marks the hold released and moves the board without
// waiting for the label to reach a graph read.
func TestReleaseArchitectureHold_CardStartsSharedRelease(t *testing.T) {
	f := newReleaseFixture(t, releaseRepos)
	holdIssue(t, f.as, 17)

	if !f.as.ReleaseArchitectureHold("acme/app", 17) {
		t.Fatal("ReleaseArchitectureHold found no hold")
	}
	f.as.waitBackground()

	if moves := f.recorded(); len(moves) != 1 || moves[0].status != "Ready" {
		t.Fatalf("board writes = %+v, want one move to Ready", moves)
	}
	if len(f.as.state.Failed) != 0 {
		t.Errorf("Failed = %+v, want the entry dropped", f.as.state.Failed)
	}
	if f.as.ReleaseArchitectureHold("acme/app", 99) {
		t.Error("ReleaseArchitectureHold reported a hold for an issue that has none")
	}
}

// A fresh architecture halt after an approval waits for a fresh approval.
func TestArchitectureRelease_NewHaltClearsReleasePending(t *testing.T) {
	f := newReleaseFixture(t, releaseRepos)
	f.failMoves = 1
	holdIssue(t, f.as, 18)
	reconcileOnce(f.as, approvedNode(18, "In progress"))
	if len(f.as.state.Failed) != 1 || !f.as.state.Failed[0].ReleasePending {
		t.Fatalf("precondition: Failed = %+v", f.as.state.Failed)
	}

	f.as.mu.Lock()
	f.as.recordFailureLocked("acme/app", 18, "t", time.Now().UTC().Format(time.RFC3339), "halt", TerminalKindArchitectureApprovalRequired)
	pending := f.as.state.Failed[0].ReleasePending
	f.as.mu.Unlock()
	if pending {
		t.Error("a new halt kept the earlier approval's ReleasePending")
	}
}
