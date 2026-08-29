package orchestrator

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/depgraph"
	"github.com/nightgauge/nightgauge/internal/dockercompose"
	"github.com/nightgauge/nightgauge/internal/state"
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
				composeIn(root, 921), // live worktree
				composeIn(root, 922), // orphaned
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
				composeIn(root, 931),
				composeIn(root, 932),
				composeIn(root, 933),
			),
			composeTeardown: recordingTeardown(&torn),
		},
		// #932 IS running but its worktree has already been reclaimed by the
		// post-merge cleanup that ran moments ago. Only state.Running sees it.
		state: &AutonomousState{Running: []RunningItem{{Repo: "acme/app", Number: 932}}},
	}

	as.sweepOrphanedComposeProjects(context.Background())

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
	as.sweepOrphanedComposeProjects(context.Background()) // must not panic
}

func TestSweepOrphanedCompose_NilStateSkipsLoudly(t *testing.T) {
	root := worktreeRepo(t, 941)

	var torn []string
	as := &AutonomousScheduler{
		scheduler: &Scheduler{
			workspaceRoot:   root,
			composeLister:   listing(composeIn(root, 942)),
			composeTeardown: recordingTeardown(&torn),
		},
	}

	out := captureLog(t, func() { as.sweepOrphanedComposeProjects(context.Background()) })

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

	out := captureLog(t, func() { as.sweepOrphanedComposeProjects(context.Background()) })

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
	root := worktreeRepo(t, 961)
	// ConfigFiles under root: the candidate bound (#442) skips a project whose
	// compose files the workspace cannot vouch for, and docker's real `ls`
	// output carries the field.
	cf := func(n string) string { return filepath.Join(root, ".worktrees", n, "docker-compose.yml") }
	logPath := installRecordingDocker(t, `[{"Name":"issue-961","Status":"running(1)","ConfigFiles":"`+cf("issue-961")+`"},`+
		`{"Name":"issue-962","Status":"running(1)","ConfigFiles":"`+cf("issue-962")+`"}]`)

	as := &AutonomousScheduler{
		scheduler: &Scheduler{workspaceRoot: root},
		state:     &AutonomousState{},
	}

	as.sweepOrphanedComposeProjects(context.Background())

	calls := dockerCalls(t, logPath)
	if !strings.Contains(calls, "compose ls") {
		t.Fatalf("the reconcile never listed compose projects through the default seam; docker calls:\n%s", calls)
	}
	if !strings.Contains(calls, "compose -p issue-962 down") {
		t.Errorf("the orphaned stack was never torn down through the default seam; docker calls:\n%s", calls)
	}
	// issue-961 IS in the ls fixture above (it was not, once — so this assertion
	// could not fail, and deleting both halves of the in-flight union left it
	// passing). It is listed AND holds a live worktree, so the protection is now
	// exercised end-to-end through the default seams.
	if !strings.Contains(calls, "compose ls") {
		t.Fatalf("no compose ls in the call log; docker calls:\n%s", calls)
	}
	if strings.Contains(calls, "compose -p issue-961 down") {
		t.Errorf("tore down the stack of an issue holding a live worktree; docker calls:\n%s", calls)
	}
	// Sanity: the shim is on PATH for this process only.
	if _, err := os.Stat(logPath); err != nil {
		t.Fatalf("recording docker never ran: %v", err)
	}
}

// TestAutonomousCycle_ComposeReconcileRunsBeforeTheWorktreeSweep is #410's
// ordering correction, and the reason it is a correctness fix rather than a
// preference.
//
// The merged-worktree sweep protects with as.state.Running ONLY, so the only
// worktrees it can reclaim belong to runs Running does not cover — an operator's
// `queue run`, an interactive run, a previous incarnation's run (startup recovery
// sets Running to nil). That is precisely the population whose compose protection
// is the active-worktree half of the union. Run the sweep first and the union is
// rebuilt from a directory the same cycle just deleted, so the teardown runs
// `down -v` — which removes named volumes nothing recovers — on a live run's
// stack.
//
// The fixture is that exact state: a worktree whose branch content is already on
// origin/main (the post-merge tail the sweep exists to reclaim), Running empty,
// and a compose stack for the same issue.
func TestAutonomousCycle_ComposeReconcileRunsBeforeTheWorktreeSweep(t *testing.T) {
	root, wt := mergedWorktreeRepo(t, 802)

	var torn []string
	as := NewAutonomousScheduler(
		&Scheduler{
			workspaceRoot:   root,
			composeLister:   listing(composeIn(root, 802)),
			composeTeardown: recordingTeardown(&torn),
		},
		nil, nil, nil, DefaultAutonomousConfig(), t.TempDir(),
	)
	as.state.Status = "running"
	as.buildGraphFn = func(context.Context) (*depgraph.Graph, error) {
		return buildTestGraph(nil, nil), nil
	}

	as.runCycle(context.Background())

	for _, name := range torn {
		if name == "issue-802" {
			t.Errorf("the cycle tore down #802's stack: the worktree sweep ran first and destroyed the only evidence protecting it; torn = %v", torn)
		}
	}
	// Sanity that the fixture really is the dangerous one: the sweep DID reclaim
	// that worktree in this cycle. Without this the test could pass because
	// nothing happened at all.
	if _, err := os.Stat(wt); !os.IsNotExist(err) {
		t.Fatalf("fixture did not exercise the race — the worktree sweep left %s in place (err=%v)", wt, err)
	}
}

// TestSweepOrphanedCompose_SnapshotArmProtectsARunWithNoWorktree makes the
// protection independent of the directory instead of merely re-ordered.
//
// A live run that this process did not dispatch AND whose worktree is legitimately
// gone (its own post-merge cleanup ran, the stack teardown is still pending) is
// invisible to both original halves of the union. The machine-wide snapshot scan
// — the same source #410 made authoritative for the CLI sweep — is what sees it.
func TestSweepOrphanedCompose_SnapshotArmProtectsARunWithNoWorktree(t *testing.T) {
	root := worktreeRepo(t, 971) // #971 has a worktree; #972 deliberately does not

	// The snapshot is written through the state package's own writer, at the
	// repo's canonical pipeline dir.
	rs := state.NewRuntimeState("acme/app", 972, "item-972", testRunID())
	rs.SetProcess(os.Getpid(), filepath.Join(root, ".worktrees", "issue-972"))
	if err := rs.Persist(state.PipelineStateDir(root)); err != nil {
		t.Fatalf("persist snapshot: %v", err)
	}

	var torn []string
	as := &AutonomousScheduler{
		scheduler: &Scheduler{
			workspaceRoot: root,
			composeLister: listing(
				composeIn(root, 972),
				composeIn(root, 973),
			),
			composeTeardown: recordingTeardown(&torn),
		},
		state: &AutonomousState{},
	}

	as.sweepOrphanedComposeProjects(context.Background())

	for _, name := range torn {
		if name == "issue-972" {
			t.Error("tore down a live run's stack that only its runtime snapshot could vouch for — the union depends on a directory another pass may delete")
		}
	}
	if len(torn) != 1 || torn[0] != "issue-973" {
		t.Errorf("expected exactly the orphan to go, got %v", torn)
	}
}

// TestSweepOrphanedCompose_UnreadableSnapshotSourceVetoesTheTeardown: the
// snapshot scan is a PROTECTION source, so failing to read it is undetermined,
// not empty.
//
// The fixture isolates that half deliberately — a perfectly readable git repo
// (so `git worktree list` succeeds and the active-worktree half is DETERMINED)
// whose pipeline state path is not a directory. Before the snapshot half was
// unioned in, this state tore the stack down on a set nobody could read.
func TestSweepOrphanedCompose_UnreadableSnapshotSourceVetoesTheTeardown(t *testing.T) {
	root := worktreeRepo(t, 991)
	if err := os.MkdirAll(filepath.Join(root, ".nightgauge"), 0o755); err != nil {
		t.Fatal(err)
	}
	// A FILE where the state directory belongs: ReadDir fails with ENOTDIR, which
	// is a read failure rather than "this repo never ran the pipeline".
	if err := os.WriteFile(filepath.Join(root, ".nightgauge", "pipeline"), []byte("not a dir\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	var torn []string
	as := &AutonomousScheduler{
		scheduler: &Scheduler{
			workspaceRoot:   root,
			composeLister:   listing(composeIn(root, 992)),
			composeTeardown: recordingTeardown(&torn),
		},
		state: &AutonomousState{},
	}

	out := captureLog(t, func() { as.sweepOrphanedComposeProjects(context.Background()) })

	if len(torn) != 0 {
		t.Errorf("tore down %v although a protection source was unreadable; log:\n%s", torn, out)
	}
	if !strings.Contains(out, "undetermined") {
		t.Errorf("the skip does not name the cause; got:\n%s", out)
	}
}
