package orchestrator

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/depgraph"
)

// #2350: the epic cascade keyed a sub-issue's parent as {sub's repo, parent
// number}. A sub-issue whose epic lives in ANOTHER repository was therefore
// cascaded through the same-numbered issue in its own repository: the real
// epic's open blocker never held it, and an unrelated issue's blocker did.
// These fixtures put a sub-issue in o/app under the epic o/platform#20, next to
// an unrelated o/app#20, and check both directions in the dispatcher and the
// stuck-epic watchdog.
const (
	crossRepoApp      = "o/app"
	crossRepoPlatform = "o/platform"
)

func crossRepoSub() *depgraph.Node {
	sub := makeEpicTestNode(crossRepoApp, 21, "OPEN", "Ready", nil, 20)
	sub.Title = "sub-issue of o/platform#20"
	sub.EpicRepo = crossRepoPlatform
	return sub
}

func crossRepoEdge(fromRepo string, from int, toRepo string, to int) depgraph.Edge {
	return depgraph.Edge{
		From: depgraph.NodeID{Repo: fromRepo, Number: from},
		To:   depgraph.NodeID{Repo: toRepo, Number: to},
		Type: "blockedBy",
	}
}

func crossRepoCascadeScheduler() *AutonomousScheduler {
	return &AutonomousScheduler{
		config: AutonomousConfig{MaxConcurrent: 5},
		repos: []depgraph.RepoConfig{
			{Owner: "o", Name: "app", Project: 1},
			{Owner: "o", Name: "platform", Project: 1},
		},
		state: &AutonomousState{},
	}
}

// The real epic, in another repository, has an open blocker: its sub-issue is
// held even though the same-numbered issue in the sub-issue's own repository is
// open and unblocked.
func TestEpicCascade_CrossRepoParentWithAnOpenBlockerHoldsItsSub(t *testing.T) {
	g := buildTestGraph([]*depgraph.Node{
		makeEpicTestNode(crossRepoPlatform, 10, "OPEN", "In progress", nil, 0),
		makeEpicTestNode(crossRepoPlatform, 20, "OPEN", "In progress", []string{"type:epic"}, 0),
		makeEpicTestNode(crossRepoApp, 20, "OPEN", "In progress", nil, 0),
		crossRepoSub(),
	}, []depgraph.Edge{crossRepoEdge(crossRepoPlatform, 20, crossRepoPlatform, 10)})

	as := crossRepoCascadeScheduler()
	candidates := as.prioritize(context.Background(), g)

	if isCandidate(candidates, crossRepoApp, 21) {
		t.Fatalf("o/app#21 dispatched although its epic o/platform#20 is blocked by the open o/platform#10")
	}
	if got := as.state.LastRejectionReasons["blocked-by-epic-dep"]; got != 1 {
		t.Errorf("blocked-by-epic-dep = %d, want 1 (rejections %v)", got, as.state.LastRejectionReasons)
	}
}

// The unrelated same-numbered issue in the sub-issue's repository has an open
// blocker and the real epic has none: nothing holds the sub-issue.
func TestEpicCascade_SameNumberedIssueInTheSubsRepoDoesNotHoldIt(t *testing.T) {
	g := buildTestGraph([]*depgraph.Node{
		makeEpicTestNode(crossRepoPlatform, 20, "OPEN", "In progress", []string{"type:epic"}, 0),
		makeEpicTestNode(crossRepoApp, 11, "OPEN", "In progress", nil, 0),
		makeEpicTestNode(crossRepoApp, 20, "OPEN", "In progress", []string{"type:epic"}, 0),
		crossRepoSub(),
	}, []depgraph.Edge{crossRepoEdge(crossRepoApp, 20, crossRepoApp, 11)})

	as := crossRepoCascadeScheduler()
	candidates := as.prioritize(context.Background(), g)

	if !isCandidate(candidates, crossRepoApp, 21) {
		t.Fatalf("o/app#21 held by o/app#20's blocker, an issue that is not its epic (rejections %v)",
			as.state.LastRejectionReasons)
	}
}

func crossRepoStuckOpts() stuckEpicScanOpts {
	return stuckEpicScanOpts{
		now: time.Unix(1_700_000_000, 0), runningSet: map[string]bool{},
		isRecovering: noRecovery, failureReason: noReason,
	}
}

// The watchdog files a cross-repository sub-issue under its real epic and
// explains the hold by naming that epic, and its blocker, in their own
// repository: in a reason about o/app#21 a bare "#10" would read as o/app#10.
func TestStuckEpics_CrossRepoParentWithAnOpenBlockerIsStuck(t *testing.T) {
	g := buildTestGraph([]*depgraph.Node{
		makeEpicTestNode(crossRepoPlatform, 10, "OPEN", "In progress", nil, 0),
		makeEpicTestNode(crossRepoPlatform, 20, "OPEN", "In progress", []string{"type:epic"}, 0),
		makeEpicTestNode(crossRepoApp, 20, "OPEN", "In progress", []string{"type:epic"}, 0),
		crossRepoSub(),
	}, []depgraph.Edge{crossRepoEdge(crossRepoPlatform, 20, crossRepoPlatform, 10)})

	got := stuckEpicsFromGraph(g, crossRepoStuckOpts())
	if len(got) != 1 || got[0].Repo != crossRepoPlatform || got[0].Number != 20 {
		t.Fatalf("stuck epics = %+v, want only o/platform#20", got)
	}
	if len(got[0].Blockers) != 1 || got[0].Blockers[0].Number != 21 {
		t.Fatalf("o/platform#20 blockers = %+v, want its sub-issue o/app#21", got[0].Blockers)
	}
	const want = "(via epic o/platform#20) blocked by o/platform#10 (open)"
	if reason := got[0].Blockers[0].Reason; reason != want {
		t.Errorf("reason = %q, want %q", reason, want)
	}
}

// An unrelated same-numbered epic in the sub-issue's repository, blocked, must
// neither hold the sub-issue nor be reported stuck on its account.
func TestStuckEpics_SameNumberedEpicInTheSubsRepoDoesNotHoldIt(t *testing.T) {
	g := buildTestGraph([]*depgraph.Node{
		makeEpicTestNode(crossRepoPlatform, 20, "OPEN", "In progress", []string{"type:epic"}, 0),
		makeEpicTestNode(crossRepoApp, 11, "OPEN", "In progress", nil, 0),
		makeEpicTestNode(crossRepoApp, 20, "OPEN", "In progress", []string{"type:epic"}, 0),
		crossRepoSub(),
	}, []depgraph.Edge{crossRepoEdge(crossRepoApp, 20, crossRepoApp, 11)})

	sub := g.Nodes["o/app#21"]
	if !dispatchable(sub, g.Adjacency(), g, false, false) {
		t.Fatal("the watchdog's dispatch gate held o/app#21 through o/app#20, which is not its epic")
	}
	if got := stuckEpicsFromGraph(g, crossRepoStuckOpts()); len(got) != 0 {
		t.Fatalf("stuck epics = %+v, want none: o/platform#20's only sub-issue is dispatchable", got)
	}
}

// An epic's edge to its own sub-issue in another repository states
// containment, as one in its own repository does (#1937), and so does not gate.
func TestEpicCascadeDeps_RecognisesACrossRepoSubIssue(t *testing.T) {
	g := buildTestGraph([]*depgraph.Node{
		makeEpicTestNode(crossRepoPlatform, 20, "OPEN", "In progress", []string{"type:epic"}, 0),
		makeEpicTestNode(crossRepoApp, 20, "OPEN", "In progress", nil, 0),
		crossRepoSub(),
	}, []depgraph.Edge{
		crossRepoEdge(crossRepoPlatform, 20, crossRepoApp, 21),
		crossRepoEdge(crossRepoPlatform, 20, crossRepoApp, 20),
	})

	gating, ownSubs := epicCascadeDeps(g, g.Adjacency(), "o/platform#20")
	if len(ownSubs) != 1 || ownSubs[0] != "o/app#21" {
		t.Errorf("own sub-issues = %v, want [o/app#21]", ownSubs)
	}
	if len(gating) != 1 || gating[0] != "o/app#20" {
		t.Errorf("gating = %v, want [o/app#20], an issue that is not the epic's sub-issue", gating)
	}
}

// openBlockerRefs names a blocker in the reason's own repository as "#N" and
// any other as "owner/repo#N", the local ones first.
func TestOpenBlockerRefs_QualifiesBlockersInOtherRepositories(t *testing.T) {
	g := buildTestGraph([]*depgraph.Node{
		makeEpicTestNode(crossRepoApp, 7, "OPEN", "In progress", nil, 0),
		makeEpicTestNode(crossRepoApp, 3, "OPEN", "Ready", nil, 0),
		makeEpicTestNode(crossRepoPlatform, 10, "OPEN", "In progress", nil, 0),
		makeEpicTestNode("o/api", 2, "OPEN", "Ready", nil, 0),
		makeEpicTestNode(crossRepoPlatform, 11, "CLOSED", "Done", nil, 0),
	}, nil)
	keys := []string{"o/platform#10", "o/app#7", "o/api#2", "o/app#3", "o/platform#11"}
	got := strings.Join(openBlockerRefs(keys, g, crossRepoApp), ", ")
	if want := "#3, #7, o/api#2, o/platform#10"; got != want {
		t.Errorf("refs = %q, want %q", got, want)
	}
}
