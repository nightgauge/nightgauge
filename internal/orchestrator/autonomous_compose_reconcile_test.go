package orchestrator

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/depgraph"
	"github.com/nightgauge/nightgauge/internal/dockercompose"
)

// #410 gap 2. The compose reconcile was re-homed from NewScheduler → loadQueue
// (where it destroyed containers as a construction side effect) onto the
// autonomous reconcile cycle, which is the one process holding an authoritative
// in-flight set for the runs whose state it tears down.

// TestSweepOrphanedCompose_RunsOnTheAutonomousCycle proves the reconcile did not
// simply DISAPPEAR when it was taken off the construction path. Removing a
// destructive pass is easy to get wrong in the silent direction: the constructor
// stops deleting, nothing else starts, and stale stacks squat host ports forever
// with no log line saying so.
//
// Driven through runCycle with a fresh graph — the pacing gate the neighbouring
// sweeps use — so the assertion covers the wiring, not just the receiver.
func TestSweepOrphanedCompose_RunsOnTheAutonomousCycle(t *testing.T) {
	root := worktreeRepo(t, 921)

	var torn []string
	as := NewAutonomousScheduler(
		&Scheduler{
			workspaceRoot: root,
			composeLister: listing(
				dockercompose.Project{Name: "issue-921", IssueNumber: 921}, // live worktree
				dockercompose.Project{Name: "issue-922", IssueNumber: 922}, // orphaned
			),
			composeTeardown: recordingTeardown(&torn),
		},
		nil, nil, nil, DefaultAutonomousConfig(), t.TempDir(),
	)
	as.state.Status = "running"
	as.buildGraphFn = func(context.Context) (*depgraph.Graph, error) {
		return buildTestGraph(nil, nil), nil
	}

	as.runCycle(context.Background())

	if len(torn) != 1 || torn[0] != "issue-922" {
		t.Fatalf("the autonomous cycle did not reconcile compose orphans; torn = %v (want exactly [issue-922])", torn)
	}
}

// TestSweepOrphanedCompose_UnionProtectsBothPopulations pins the union. Each half
// covers a population the other structurally cannot see, and taking either alone
// runs `down -v` — which removes named volumes nothing recovers — against a live
// run.
func TestSweepOrphanedCompose_UnionProtectsBothPopulations(t *testing.T) {
	// #931 has a registered worktree but is NOT in state.Running: it is a run
	// this process did not dispatch (previous incarnation, or another host's
	// cross-repo run). Only the worktree scan sees it.
	root := worktreeRepo(t, 931)

	var torn []string
	as := &AutonomousScheduler{
		scheduler: &Scheduler{
			workspaceRoot: root,
			composeLister: listing(
				dockercompose.Project{Name: "issue-931", IssueNumber: 931},
				dockercompose.Project{Name: "issue-932", IssueNumber: 932},
				dockercompose.Project{Name: "issue-933", IssueNumber: 933},
			),
			composeTeardown: recordingTeardown(&torn),
		},
		// #932 IS running but its worktree has already been reclaimed by the
		// post-merge cleanup that ran moments ago. Only state.Running sees it.
		state: &AutonomousState{Running: []RunningItem{{Repo: "acme/app", Number: 932}}},
	}

	as.sweepOrphanedComposeProjects()

	for _, name := range torn {
		if name == "issue-931" {
			t.Error("tore down a run this process did not dispatch — state.Running alone is not the in-flight set")
		}
		if name == "issue-932" {
			t.Error("tore down a running run whose worktree was already reclaimed — the worktree scan alone is not the in-flight set")
		}
	}
	// The guard must not disable the reconcile: the real orphan still goes.
	if len(torn) != 1 || torn[0] != "issue-933" {
		t.Errorf("expected exactly the orphaned project to be torn down, got %v", torn)
	}
}

// TestSweepOrphanedCompose_NoSchedulerIsNoOp / _NilStateSkipsLoudly mirror the
// merged-worktree sweep's two guards on the same two dereferences. A nil state is
// not an empty one — it is half the protection, unread.
func TestSweepOrphanedCompose_NoSchedulerIsNoOp(t *testing.T) {
	as := &AutonomousScheduler{state: &AutonomousState{}}
	as.sweepOrphanedComposeProjects() // must not panic
}

func TestSweepOrphanedCompose_NilStateSkipsLoudly(t *testing.T) {
	root := worktreeRepo(t, 941)

	var torn []string
	as := &AutonomousScheduler{
		scheduler: &Scheduler{
			workspaceRoot:   root,
			composeLister:   listing(dockercompose.Project{Name: "issue-942", IssueNumber: 942}),
			composeTeardown: recordingTeardown(&torn),
		},
	}

	out := captureLog(t, func() { as.sweepOrphanedComposeProjects() })

	if len(torn) != 0 {
		t.Errorf("tore down %v on an unreadable in-flight set — that is `down -v` against a possibly-live run", torn)
	}
	if !strings.Contains(out, composeReconcileLogPrefix+": WARN") {
		t.Errorf("the skip is not loud, or not filed under this reconcile's own prefix; got %q", out)
	}
	if !strings.Contains(out, "autonomous state unavailable") {
		t.Errorf("skip log does not name the cause; got %q", out)
	}
}

// TestSweepOrphanedCompose_UndeterminedTearsDownNothing is the #296 guard on the
// path that now runs in production. One unreadable root means the active-worktree
// half of the union was never obtained, and every listed project would be torn
// down on the strength of a set nobody read.
func TestSweepOrphanedCompose_UndeterminedTearsDownNothing(t *testing.T) {
	var torn []string
	as := &AutonomousScheduler{
		scheduler: &Scheduler{
			workspaceRoot:   unreadableRoot(t),
			composeLister:   listing(dockercompose.Project{Name: "issue-951", IssueNumber: 951}),
			composeTeardown: recordingTeardown(&torn),
		},
		state: &AutonomousState{},
	}

	out := captureLog(t, func() { as.sweepOrphanedComposeProjects() })

	if len(torn) != 0 {
		t.Errorf("tore down %v on an undetermined worktree set, got log:\n%s", torn, out)
	}
	if !strings.Contains(out, "undetermined") {
		t.Errorf("the skip does not name the cause; got %q", out)
	}
}

// TestSweepOrphanedCompose_ProductionDefaultsReachDocker is the counterweight to
// TestNewScheduler_ConstructionTearsDownNoContainers. That test asserts the
// constructor invokes docker ZERO times, which a fixed constructor and a deleted
// feature both satisfy. This one asserts the production DEFAULT seams — no
// injected lister, no injected teardown — still reach the docker CLI from the
// autonomous receiver, so "nothing called docker" can never be the whole story.
func TestSweepOrphanedCompose_ProductionDefaultsReachDocker(t *testing.T) {
	logPath := installRecordingDocker(t, `[{"Name":"issue-962","Status":"running(1)"}]`)
	root := worktreeRepo(t, 961)

	as := &AutonomousScheduler{
		scheduler: &Scheduler{workspaceRoot: root},
		state:     &AutonomousState{},
	}

	as.sweepOrphanedComposeProjects()

	calls := dockerCalls(t, logPath)
	if !strings.Contains(calls, "compose ls") {
		t.Fatalf("the reconcile never listed compose projects through the default seam; docker calls:\n%s", calls)
	}
	if !strings.Contains(calls, "compose -p issue-962 down") {
		t.Errorf("the orphaned stack was never torn down through the default seam; docker calls:\n%s", calls)
	}
	if strings.Contains(calls, "issue-961") {
		t.Errorf("tore down the stack of an issue holding a live worktree; docker calls:\n%s", calls)
	}
	// Sanity: the shim is on PATH for this process only.
	if _, err := os.Stat(logPath); err != nil {
		t.Fatalf("recording docker never ran: %v", err)
	}
}
