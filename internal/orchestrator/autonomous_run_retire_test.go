package orchestrator

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/depgraph"
	"github.com/nightgauge/nightgauge/internal/gittest"
	"github.com/nightgauge/nightgauge/internal/layout/layouttest"
	"github.com/nightgauge/nightgauge/internal/runstate"
	"github.com/nightgauge/nightgauge/internal/state"
)

type retireCall struct {
	repo   string
	issue  int
	reason string
}

func recordRetires(as *AutonomousScheduler) *[]retireCall {
	return recordRetiresSettled(as, func(int) bool { return true })
}

// recordRetiresSettled records each hook call and answers settled(issue).
func recordRetiresSettled(as *AutonomousScheduler, settled func(int) bool) *[]retireCall {
	var calls []retireCall
	as.OnRunNotInFlight(func(repo string, issue int, reason string) bool {
		calls = append(calls, retireCall{repo, issue, reason})
		return settled(issue)
	})
	return &calls
}

// liveRuns registers a provider listing the given acme/app issues.
func liveRuns(as *AutonomousScheduler, issues *[]int) {
	as.OnLiveRunIssues(func() []RunIssue {
		out := make([]RunIssue, 0, len(*issues))
		for _, n := range *issues {
			out = append(out, RunIssue{Repo: "acme/app", Number: n})
		}
		return out
	})
}

// ageClosures backdates every pending closure past the horizon.
func ageClosures(as *AutonomousScheduler) {
	for k, r := range as.closedRetirePending {
		r.since = time.Now().Add(-closedRetireHorizon - time.Minute)
		as.closedRetirePending[k] = r
	}
}

// retireRepo is a git repo with a forge origin and one non-terminal snapshot
// for issue, aged by age.
func retireRepo(t *testing.T, issue int, age time.Duration) string {
	t.Helper()
	root := worktreeRepo(t, issue+1000)
	gittest.Run(t, root, "remote", "add", "origin", "https://github.com/acme/app.git")
	dir := layouttest.PipelineDir(t, root)
	if err := state.NewRuntimeState("acme/app", issue, "item", testRunID()).Persist(dir); err != nil {
		t.Fatalf("persist: %v", err)
	}
	when := time.Now().Add(-age)
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if err := os.Chtimes(filepath.Join(dir, e.Name()), when, when); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func TestRunRetire_DeadSnapshotFiresTheHookOnce(t *testing.T) {
	t.Parallel()
	root := retireRepo(t, 799, 2*runstate.LivenessWindow)
	as := &AutonomousScheduler{state: &AutonomousState{}}
	calls := recordRetires(as)

	if _, ok := as.snapshotInFlightIssues([]string{root}); !ok {
		t.Fatal("scan must be determined")
	}
	if len(*calls) != 1 || (*calls)[0].issue != 799 || (*calls)[0].repo != "acme/app" ||
		(*calls)[0].reason == "" {
		t.Fatalf("calls = %+v, want one for acme/app#799", *calls)
	}
}

func TestRunRetire_FreshSnapshotFiresNothing(t *testing.T) {
	t.Parallel()
	root := retireRepo(t, 800, time.Minute)
	as := &AutonomousScheduler{state: &AutonomousState{}}
	calls := recordRetires(as)
	as.snapshotInFlightIssues([]string{root})
	if len(*calls) != 0 {
		t.Fatalf("a fresh snapshot must fire nothing, got %+v", *calls)
	}
}

func TestRunRetire_UndeterminedScanFiresNothing(t *testing.T) {
	t.Parallel()
	dead := retireRepo(t, 801, 2*runstate.LivenessWindow)
	notGit := t.TempDir()
	as := &AutonomousScheduler{state: &AutonomousState{}}
	calls := recordRetires(as)
	if _, ok := as.snapshotInFlightIssues([]string{dead, notGit}); ok {
		t.Fatal("a root that is not a work tree must undetermine the scan")
	}
	if len(*calls) != 0 {
		t.Fatalf("an undetermined scan must fire nothing, got %+v", *calls)
	}
}

func TestRunRetire_NoForgeOriginFiresNothing(t *testing.T) {
	t.Parallel()
	root := worktreeRepo(t, 1802)
	dir := layouttest.PipelineDir(t, root)
	if err := state.NewRuntimeState("acme/app", 802, "item", testRunID()).Persist(dir); err != nil {
		t.Fatal(err)
	}
	when := time.Now().Add(-2 * runstate.LivenessWindow)
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		_ = os.Chtimes(filepath.Join(dir, e.Name()), when, when)
	}
	as := &AutonomousScheduler{state: &AutonomousState{}}
	calls := recordRetires(as)
	as.snapshotInFlightIssues([]string{root})
	if len(*calls) != 0 {
		t.Fatalf("an empty slug would match every repo; must fire nothing, got %+v", *calls)
	}
}

// (a) AC 4/5: a dead closed run is settled on the first ask and reported
// exactly once across five cycles, the last past the horizon, although the
// issue stays in Completed.
// Serial: swaps package var reconcileExecGh (via heldTestScheduler).
func TestRunRetire_ClosedDeadRunReportedOnce(t *testing.T) {
	as := heldTestScheduler(t)
	as.state.Completed = []CompletedItem{{Repo: "acme/app", Number: 799}}
	live := []int{799}
	liveRuns(as, &live)
	calls := recordRetires(as)
	g := buildTestGraph([]*depgraph.Node{{Repo: "acme/app", Number: 799, State: "CLOSED"}}, nil)
	for i := 0; i < 4; i++ {
		as.reconcileStateAgainstGraph(g)
	}
	ageClosures(as)
	as.reconcileStateAgainstGraph(g)
	if len(*calls) != 1 || (*calls)[0].issue != 799 || (*calls)[0].reason != retireReasonClosed {
		t.Fatalf("calls = %+v, want exactly one closed-on-forge call for #799", *calls)
	}
}

// (b) A closure refused for a fresh lease is re-asked until the horizon, then
// never again, and is not re-added while the registry still lists it.
// Serial: swaps package var reconcileExecGh (via heldTestScheduler).
func TestRunRetire_FreshLeaseClosureStopsAtHorizon(t *testing.T) {
	as := heldTestScheduler(t)
	live := []int{805}
	liveRuns(as, &live)
	calls := recordRetiresSettled(as, func(int) bool { return false })
	g := buildTestGraph([]*depgraph.Node{{Repo: "acme/app", Number: 805, State: "CLOSED"}}, nil)
	as.reconcileStateAgainstGraph(g)
	as.reconcileStateAgainstGraph(g)
	if len(*calls) != 2 {
		t.Fatalf("an unsettled closure must be re-asked each cycle, got %+v", *calls)
	}
	ageClosures(as)
	*calls = nil
	for i := 0; i < 3; i++ {
		as.reconcileStateAgainstGraph(g)
	}
	if len(*calls) != 0 || len(as.closedRetirePending) != 0 {
		t.Fatalf("past the horizon: calls=%+v pending=%v", *calls, as.closedRetirePending)
	}
	// Once the registry stops listing it, the settled key is pruned.
	live = nil
	as.reconcileStateAgainstGraph(g)
	if len(as.closedRetireSettled) != 0 {
		t.Fatalf("settled key must be pruned once not live: %v", as.closedRetireSettled)
	}
}

// (c) AC 1: a closed issue in no scheduler list (a manual run) is reported.
// (d) An open node fires nothing.
// Serial: swaps package var reconcileExecGh (via heldTestScheduler).
func TestRunRetire_ClosedManualRunReportedOpenIgnored(t *testing.T) {
	as := heldTestScheduler(t)
	live := []int{810, 811}
	liveRuns(as, &live)
	calls := recordRetires(as)
	g := buildTestGraph([]*depgraph.Node{
		{Repo: "acme/app", Number: 810, State: "CLOSED"},
		{Repo: "acme/app", Number: 811, State: "OPEN", BoardStatus: "Ready"},
	}, nil)
	as.reconcileStateAgainstGraph(g)
	if len(*calls) != 1 || (*calls)[0].issue != 810 {
		t.Fatalf("calls = %+v, want one for #810 only", *calls)
	}
}

// (e) Two roots: a fresh snapshot for the fixture issue in A and a stale one
// in B fire nothing. Suppression is keyed by issue number alone, so a
// protected run in one repo hides a dead same-numbered run in another; the
// registry's liveness re-check makes that the safe (over-report) side.
func TestRunRetire_TwoRootsProtectedIssueFiresNothing(t *testing.T) {
	t.Parallel()
	fresh := retireRepo(t, 820, time.Minute)
	stale := retireRepo(t, 820, 2*runstate.LivenessWindow)
	as := &AutonomousScheduler{state: &AutonomousState{}}
	calls := recordRetires(as)
	if _, ok := as.snapshotInFlightIssues([]string{fresh, stale}); !ok {
		t.Fatal("scan must be determined")
	}
	if len(*calls) != 0 {
		t.Fatalf("an issue another root protects must fire nothing, got %+v", *calls)
	}
}

// A registry run whose repo is a bare or differently-cased name still matches
// its closed owner/name graph node and is reported (#3567 review).
// Serial: swaps package var reconcileExecGh (via heldTestScheduler).
func TestRunRetire_ClosedRunMatchesBareRepoName(t *testing.T) {
	for _, repo := range []string{"app", "Acme/App"} {
		as := heldTestScheduler(t)
		as.OnLiveRunIssues(func() []RunIssue { return []RunIssue{{Repo: repo, Number: 811}} })
		calls := recordRetires(as)
		g := buildTestGraph([]*depgraph.Node{{Repo: "acme/app", Number: 811, State: "CLOSED"}}, nil)
		as.reconcileStateAgainstGraph(g)
		if len(*calls) != 1 || (*calls)[0].issue != 811 {
			t.Fatalf("repo %q: calls = %+v, want one call for #811", repo, *calls)
		}
	}
}
