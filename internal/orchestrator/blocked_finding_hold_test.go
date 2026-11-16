package orchestrator

import (
	"context"
	"os"
	"testing"

	"github.com/nightgauge/nightgauge/internal/attention"
	"github.com/nightgauge/nightgauge/internal/depgraph"
	"github.com/nightgauge/nightgauge/internal/layout/layouttest"
)

// #3081: a hold the scheduler places at pickup is never silent. A deferral
// with no open card raises one; a finding whose native blockedBy edges have
// all closed no longer defers; a Ready issue held only by a dependency closed
// without the validation marker raises a card naming it.

func openCardsByKey(t *testing.T, store *attention.Store, key string) []attention.DecisionRequest {
	t.Helper()
	reqs, err := store.List(attention.ListFilter{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	var out []attention.DecisionRequest
	for _, r := range reqs {
		if r.IdempotencyKey == key && !r.Lifecycle.State.IsTerminal() {
			out = append(out, r)
		}
	}
	return out
}

func nativeEdge(fromRepo string, from int, toRepo string, to int) depgraph.Edge {
	return depgraph.Edge{
		From: depgraph.NodeID{Repo: fromRepo, Number: from},
		To:   depgraph.NodeID{Repo: toRepo, Number: to},
		Type: "blockedBy", Source: "graphql", Resolvable: true,
	}
}

func TestBlockedFindingDeferral_RaisesCardWhenNoneIsOpen(t *testing.T) {
	root := layouttest.Repo(t)
	store := attention.New(root)
	as := &AutonomousScheduler{config: AutonomousConfig{}, state: &AutonomousState{},
		workspaceRoot: root, attention: store}
	writeBlockedFindingFile(t, root, 1255, validFinding1255)
	g := blockedFindingGraph()
	key := blockedFindingCardKey("O/app", 1255)

	as.prioritize(context.Background(), g)
	cards := openCardsByKey(t, store, key)
	if len(cards) != 1 {
		t.Fatalf("deferral raised %d open cards; want 1", len(cards))
	}
	if cards[0].FindOption("cleared") == nil || cards[0].FindOption("cleared").Verb != attention.VerbBlockedFindingClear {
		t.Fatalf("card does not offer Blocker resolved: %+v", cards[0].Options)
	}

	// Another deferral with the card open adds nothing.
	as.prioritize(context.Background(), g)
	if n := len(openCardsByKey(t, store, key)); n != 1 {
		t.Fatalf("second deferral left %d open cards; want 1", n)
	}

	// The operator dismisses it while the finding still holds: the next
	// deferral raises a card again instead of deferring silently.
	if _, err := store.Resolve(context.Background(), cards[0].ID, "leave", "test", "", "", nil); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if n := len(openCardsByKey(t, store, key)); n != 0 {
		t.Fatalf("precondition: %d open cards after resolve", n)
	}
	as.prioritize(context.Background(), g)
	if n := len(openCardsByKey(t, store, key)); n != 1 {
		t.Fatalf("deferral after the card was resolved left %d open cards; want 1", n)
	}
}

func TestBlockedFinding_ClearedWhenEveryNativeBlockerIsClosed(t *testing.T) {
	cases := []struct {
		name        string
		blockers    map[int]string // blocker number -> state
		wantCleared bool
	}{
		{"all closed", map[int]string{10: "CLOSED", 11: "CLOSED"}, true},
		{"one still open", map[int]string{10: "CLOSED", 11: "OPEN"}, false},
		{"no native edge", map[int]string{}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := layouttest.Repo(t)
			store := attention.New(root)
			as := &AutonomousScheduler{config: AutonomousConfig{}, workspaceRoot: root, attention: store,
				state: &AutonomousState{Failed: []FailedItem{{
					Repo: "O/app", Number: 1255, Kind: TerminalKindNotPipelineActionable, AttemptCount: 1,
				}}}}
			g := blockedFindingGraph()
			for n, st := range tc.blockers {
				g.Nodes[depgraph.NodeID{Repo: "O/app", Number: n}.String()] =
					&depgraph.Node{Repo: "O/app", Number: n, State: st, BoardStatus: "Done"}
				g.Edges = append(g.Edges, nativeEdge("O/app", 1255, "O/app", n))
			}
			path := writeBlockedFindingFile(t, root, 1255, validFinding1255)
			as.raiseBlockedFindingCard("O/app", 1255, blockedFindingRecord{}, nil)

			got := isCandidate(as.prioritize(context.Background(), g), "O/app", 1255)
			_, statErr := os.Stat(path)
			cleared := os.IsNotExist(statErr)
			if cleared != tc.wantCleared || got != tc.wantCleared {
				t.Fatalf("finding cleared=%v candidate=%v; want both %v", cleared, got, tc.wantCleared)
			}
			open := len(openCardsByKey(t, store, blockedFindingCardKey("O/app", 1255)))
			if tc.wantCleared && open != 0 {
				t.Fatalf("auto-clear left the Blocker resolved card open")
			}
			if !tc.wantCleared && open != 1 {
				t.Fatalf("a finding still deferring has %d open cards; want 1", open)
			}
		})
	}
}

func TestUnvalidatedDependency_RaisesCardNamingTheDependency(t *testing.T) {
	root := layouttest.Repo(t)
	store := attention.New(root)
	validated := false
	as := &AutonomousScheduler{config: AutonomousConfig{}, state: &AutonomousState{},
		workspaceRoot: root, attention: store,
		resolveValidationFn: func(_ context.Context, known map[string][]string, unknown []string) map[string]ValidationState {
			out := map[string]ValidationState{}
			for k := range known {
				out[k] = ValidationState{Program: true, Validated: validated}
			}
			return out
		}}
	g := &depgraph.Graph{
		Nodes: map[string]*depgraph.Node{
			"o/app#20": {Repo: "o/app", Number: 20, State: "OPEN", BoardStatus: "Ready"},
			"o/app#21": {Repo: "o/app", Number: 21, State: "CLOSED", BoardStatus: "Done",
				Labels: []string{"program:alpha"}},
		},
		Edges: []depgraph.Edge{nativeEdge("o/app", 20, "o/app", 21)},
	}
	key := keyUnvalidatedDependency("o/app#21")

	if isCandidate(as.prioritize(context.Background(), g), "o/app", 20) {
		t.Fatal("precondition: an issue held by an unvalidated dependency was a candidate")
	}
	cards := openCardsByKey(t, store, key)
	if len(cards) != 1 {
		t.Fatalf("unvalidated dependency raised %d cards; want 1", len(cards))
	}
	c := cards[0]
	if c.Context.Repo != "o/app" || c.Context.Issue != 21 {
		t.Fatalf("card does not name the dependency: %+v", c.Context)
	}
	if opt := c.FindOption("rerun-post-merge"); opt == nil || opt.Verb != attention.VerbIssueRerunPostMerge {
		t.Fatalf("card does not offer the post-merge hook: %+v", c.Options)
	}

	// Once the marker exists the hold clears and the card retracts.
	validated = true
	as.validatedDeps = nil
	if !isCandidate(as.prioritize(context.Background(), g), "o/app", 20) {
		t.Fatal("issue not released once its dependency validated")
	}
	if n := len(openCardsByKey(t, store, key)); n != 0 {
		t.Fatalf("card still open after the dependency validated (%d)", n)
	}
}

func TestUnvalidatedDependency_NoCardWhenAnotherDependencyAlsoHolds(t *testing.T) {
	deps := []string{"o/app#21", "o/app#22"}
	g := &depgraph.Graph{Nodes: map[string]*depgraph.Node{
		"o/app#21": {Repo: "o/app", Number: 21, State: "CLOSED", Labels: []string{"program:alpha"}},
		"o/app#22": {Repo: "o/app", Number: 22, State: "OPEN", BoardStatus: "In progress"},
	}}
	validation := map[string]ValidationState{"o/app#21": {Program: true}}
	if held := unvalidatedOnlyHolds(deps, g, nil, validation); held != nil {
		t.Fatalf("an issue also held by an open dependency got unvalidated-only holds %v", held)
	}
	if held := unvalidatedOnlyHolds(deps[:1], g, nil, validation); len(held) != 1 {
		t.Fatalf("held only by the unvalidated dependency: got %v", held)
	}
}
