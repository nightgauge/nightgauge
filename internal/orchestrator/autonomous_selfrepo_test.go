package orchestrator

// Tests for the self-repo dispatch guard (#292): autonomous must refuse to
// dispatch an issue belonging to the repository that built the RUNNING
// binary — a stage editing that repo can be destroyed by the unfixed version
// of itself (#289). The refusal must be visible (exactly one fyi card), never
// a silent skip; sibling repos dispatch normally; an explicit override
// exists; and an unknown self-identity disables the guard instead of
// refusing blindly.

import (
	"context"
	"testing"

	"github.com/nightgauge/nightgauge/internal/attention"
	"github.com/nightgauge/nightgauge/internal/depgraph"
)

func newSelfRepoTestScheduler(t *testing.T, selfSlug string, allow bool) *AutonomousScheduler {
	t.Helper()
	as := &AutonomousScheduler{
		config:       AutonomousConfig{MaxConcurrent: 5, AllowSelfRepo: allow},
		state:        &AutonomousState{},
		selfRepoSlug: selfSlug,
		attention:    attention.New(t.TempDir()),
	}
	t.Cleanup(as.drainBackground) // backstop; see newAutonomousForCascadeTest
	return as
}

func selfRepoTestNodes() []*depgraph.Node {
	return []*depgraph.Node{
		{Repo: "octocat/toolrepo", Number: 292, Title: "Fix the tool", State: "OPEN", BoardStatus: "Ready", Priority: "P0", Size: "XS", Weight: 1},
		{Repo: "octocat/sibling", Number: 7, Title: "Sibling work", State: "OPEN", BoardStatus: "Ready", Priority: "P1", Size: "M", Weight: 3},
	}
}

func TestPrioritize_RefusesSelfRepoIssue(t *testing.T) {
	as := newSelfRepoTestScheduler(t, "octocat/toolrepo", false)
	g := buildTestGraph(selfRepoTestNodes(), nil)

	candidates := as.prioritize(context.Background(), g)

	if len(candidates) != 1 {
		t.Fatalf("expected 1 candidate (self-repo refused), got %d", len(candidates))
	}
	if candidates[0].Repo != "octocat/sibling" || candidates[0].Number != 7 {
		t.Errorf("expected sibling #7 to dispatch normally, got %s#%d", candidates[0].Repo, candidates[0].Number)
	}

	reqs, err := as.Attention().List(attention.ListFilter{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(reqs) != 1 {
		t.Fatalf("expected exactly one refusal card, got %d", len(reqs))
	}
	card := reqs[0]
	if card.Producer != producerSelfRepoRefusal {
		t.Errorf("card producer = %q, want %q", card.Producer, producerSelfRepoRefusal)
	}
	if card.Severity != attention.SeverityFYI {
		t.Errorf("card severity = %q, want fyi — the refusal must inform, not block the fleet", card.Severity)
	}
	if !card.Standing || card.Fingerprint == "" {
		t.Errorf("card must be standing with a fingerprint (standing=%v fingerprint=%q) — it re-raises every scan otherwise", card.Standing, card.Fingerprint)
	}
	if card.Context.Repo != "octocat/toolrepo" || card.Context.Issue != 292 {
		t.Errorf("card context = %s#%d, want octocat/toolrepo#292", card.Context.Repo, card.Context.Issue)
	}
}

// Re-scanning the same refused issue must update one card in place, never
// grow the inbox.
func TestPrioritize_SelfRepoRefusalNeverGrowsInbox(t *testing.T) {
	as := newSelfRepoTestScheduler(t, "octocat/toolrepo", false)
	g := buildTestGraph(selfRepoTestNodes(), nil)

	for i := 0; i < 4; i++ {
		as.prioritize(context.Background(), g)
	}
	reqs, err := as.Attention().List(attention.ListFilter{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(reqs) != 1 {
		t.Fatalf("four scans of one refused issue produced %d cards, want 1", len(reqs))
	}
}

// The card retracts itself when the condition stops being observed (issue
// closed/moved) — a stale refusal card is noise.
func TestPrioritize_SelfRepoRefusalRetractsWhenIssueGone(t *testing.T) {
	as := newSelfRepoTestScheduler(t, "octocat/toolrepo", false)
	as.prioritize(context.Background(), buildTestGraph(selfRepoTestNodes(), nil))

	if reqs, _ := as.Attention().List(attention.ListFilter{}); len(reqs) != 1 {
		t.Fatalf("precondition: expected 1 card, got %d", len(reqs))
	}

	// Next scan: the self-repo issue is gone (closed / off the board).
	remaining := []*depgraph.Node{
		{Repo: "octocat/sibling", Number: 7, Title: "Sibling work", State: "OPEN", BoardStatus: "Ready", Priority: "P1", Size: "M", Weight: 3},
	}
	as.prioritize(context.Background(), buildTestGraph(remaining, nil))

	reqs, err := as.Attention().List(attention.ListFilter{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	for _, r := range reqs {
		if r.Producer == producerSelfRepoRefusal && r.Lifecycle.State == attention.StateOpen {
			t.Errorf("refusal card still open after the issue stopped being observed: %+v", r)
		}
	}
}

// The documented escape hatch: autonomous.allow_self_repo / --allow-self-repo
// dispatches the issue and raises nothing.
func TestPrioritize_SelfRepoAllowedByOverride(t *testing.T) {
	as := newSelfRepoTestScheduler(t, "octocat/toolrepo", true)
	g := buildTestGraph(selfRepoTestNodes(), nil)

	candidates := as.prioritize(context.Background(), g)
	if len(candidates) != 2 {
		t.Fatalf("expected 2 candidates with allow_self_repo, got %d", len(candidates))
	}
	if reqs, _ := as.Attention().List(attention.ListFilter{}); len(reqs) != 0 {
		t.Errorf("override must not raise refusal cards, got %d", len(reqs))
	}
}

// Unknown self identity ("" — resolution failed) disables the guard rather
// than refusing blindly: refusing nothing real is safe, refusing everything
// that shares a name with nothing is not a behavior.
func TestPrioritize_SelfRepoUnknownIdentityDisablesGuard(t *testing.T) {
	as := newSelfRepoTestScheduler(t, "", false)
	g := buildTestGraph(selfRepoTestNodes(), nil)

	candidates := as.prioritize(context.Background(), g)
	if len(candidates) != 2 {
		t.Fatalf("expected 2 candidates with unknown self identity, got %d", len(candidates))
	}
}

// Defense-in-depth: every dispatch path funnels through enqueueItem, so even
// a candidate that slipped past the prioritize gate is refused before any
// state mutation — Running stays empty and the refusal is carded.
func TestEnqueueItem_RefusesSelfRepo(t *testing.T) {
	as := newSelfRepoTestScheduler(t, "octocat/toolrepo", false)

	as.enqueueItem(context.Background(), CandidateItem{Repo: "octocat/toolrepo", Number: 292, Title: "Fix the tool"})

	if len(as.state.Running) != 0 {
		t.Fatalf("self-repo item must not enter Running, got %+v", as.state.Running)
	}
	reqs, err := as.Attention().List(attention.ListFilter{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(reqs) != 1 || reqs[0].Producer != producerSelfRepoRefusal {
		t.Fatalf("expected exactly one self-repo refusal card, got %+v", reqs)
	}
}

// Case-insensitive slug match: GitHub slugs are case-insensitive and the
// origin remote's casing may differ from the board's.
func TestPrioritize_SelfRepoMatchIsCaseInsensitive(t *testing.T) {
	as := newSelfRepoTestScheduler(t, "OctoCat/ToolRepo", false)
	g := buildTestGraph(selfRepoTestNodes(), nil)

	candidates := as.prioritize(context.Background(), g)
	if len(candidates) != 1 {
		t.Fatalf("expected 1 candidate (case-insensitive refusal), got %d", len(candidates))
	}
}
