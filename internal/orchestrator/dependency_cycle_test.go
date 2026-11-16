package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/attention"
	"github.com/nightgauge/nightgauge/internal/depgraph"
	"github.com/nightgauge/nightgauge/internal/layout/layouttest"
)

// #3081: a cycle of open native blockedBy edges is detected, raised as one
// card, not raised again each pass, and cleared once an edge is removed.

func cycleGraph(edges ...depgraph.Edge) *depgraph.Graph {
	g := &depgraph.Graph{Nodes: map[string]*depgraph.Node{}}
	for n := 1; n <= 4; n++ {
		g.Nodes[depgraph.NodeID{Repo: "o/app", Number: n}.String()] =
			&depgraph.Node{Repo: "o/app", Number: n, State: "OPEN", BoardStatus: "Ready"}
	}
	g.Edges = edges
	return g
}

func TestDetectDependencyCycles_ThreeIssueCycleNormalised(t *testing.T) {
	a := nativeEdge("o/app", 1, "o/app", 2)
	b := nativeEdge("o/app", 2, "o/app", 3)
	c := nativeEdge("o/app", 3, "o/app", 1)
	tail := nativeEdge("o/app", 4, "o/app", 1) // into the cycle, not part of it
	prose := depgraph.Edge{From: depgraph.NodeID{Repo: "o/app", Number: 1}, To: depgraph.NodeID{Repo: "o/app", Number: 4},
		Type: "bodyDeclared", Source: "body_text"} // prose edges cannot be removed by the card

	want := []dependencyCycle{{
		Issues: []string{"o/app#1", "o/app#2", "o/app#3"},
		Edges: []cycleEdge{
			{Blocked: "o/app#1", Blocker: "o/app#2"},
			{Blocked: "o/app#2", Blocker: "o/app#3"},
			{Blocked: "o/app#3", Blocker: "o/app#1"},
		},
	}}
	for _, order := range [][]depgraph.Edge{{a, b, c, tail, prose}, {tail, c, prose, b, a}} {
		got := detectDependencyCycles(cycleGraph(order...))
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("cycles = %+v, want %+v", got, want)
		}
	}

	// A closed issue breaks the cycle; no edges, no cycle.
	g := cycleGraph(a, b, c)
	g.Nodes["o/app#2"].State = "CLOSED"
	if got := detectDependencyCycles(g); len(got) != 0 {
		t.Fatalf("a cycle through a closed issue was reported: %+v", got)
	}
	if got := detectDependencyCycles(cycleGraph(a, b)); len(got) != 0 {
		t.Fatalf("a chain was reported as a cycle: %+v", got)
	}
	if got := detectDependencyCycles(cycleGraph(nativeEdge("o/app", 4, "o/app", 4))); len(got) != 1 {
		t.Fatalf("a self-block was not reported: %+v", got)
	}

	// A cycle across repositories is one cycle.
	x := cycleGraph(nativeEdge("o/app", 1, "o/lib", 9), nativeEdge("o/lib", 9, "o/app", 1))
	x.Nodes["o/lib#9"] = &depgraph.Node{Repo: "o/lib", Number: 9, State: "OPEN"}
	got := detectDependencyCycles(x)
	if len(got) != 1 || !reflect.DeepEqual(got[0].Issues, []string{"o/app#1", "o/lib#9"}) {
		t.Fatalf("cross-repo cycle = %+v", got)
	}
}

func TestDependencyCycle_OneCardNotReraisedClearedOnEdgeRemoval(t *testing.T) {
	root := layouttest.Repo(t)
	store := attention.New(root)
	as := &AutonomousScheduler{config: AutonomousConfig{}, state: &AutonomousState{},
		workspaceRoot: root, attention: store}
	edges := []depgraph.Edge{
		nativeEdge("o/app", 1, "o/app", 2), nativeEdge("o/app", 2, "o/app", 3), nativeEdge("o/app", 3, "o/app", 1),
	}
	g := cycleGraph(edges...)

	openCycleCards := func() []attention.DecisionRequest {
		reqs, err := store.List(attention.ListFilter{})
		if err != nil {
			t.Fatal(err)
		}
		var out []attention.DecisionRequest
		for _, r := range reqs {
			if r.Producer == ProducerDependencyCycle && !r.Lifecycle.State.IsTerminal() {
				out = append(out, r)
			}
		}
		return out
	}
	created := func() int {
		entries, err := store.ReadJournal()
		if err != nil {
			t.Fatal(err)
		}
		n := 0
		for _, e := range entries {
			if e.Producer == ProducerDependencyCycle && e.Action == attention.ActionCreated {
				n++
			}
		}
		return n
	}

	for pass := 1; pass <= 3; pass++ {
		as.prioritize(context.Background(), g)
	}
	cards := openCycleCards()
	if len(cards) != 1 || created() != 1 {
		t.Fatalf("three passes over one cycle: %d open cards, %d created; want 1 and 1", len(cards), created())
	}
	card := cards[0]
	wantEdges := map[string]bool{"o/app#1>o/app#2": true, "o/app#2>o/app#3": true, "o/app#3>o/app#1": true}
	for _, o := range card.Options {
		if o.Verb != attention.VerbIssueRemoveBlockedBy {
			continue
		}
		edge := fmt.Sprintf("%v#%v>%v#%v", o.Args["blockedRepo"], o.Args["blockedNumber"],
			o.Args["blockerRepo"], o.Args["blockerNumber"])
		if !wantEdges[edge] {
			t.Fatalf("option %s removes %s, which is not a remaining cycle edge", o.ID, edge)
		}
		delete(wantEdges, edge)
	}
	if len(wantEdges) != 0 {
		t.Fatalf("card offers no removal for edges %v", wantEdges)
	}
	for _, issue := range []string{"o/app#1", "o/app#2", "o/app#3"} {
		if !strings.Contains(card.Body, issue) {
			t.Fatalf("card body does not name %s:\n%s", issue, card.Body)
		}
	}

	// The record doctor reads holds the cycle.
	dir := layouttest.PipelineDir(t, root)
	data, err := os.ReadFile(filepath.Join(dir, DependencyCyclesFile))
	if err != nil {
		t.Fatalf("cycle record: %v", err)
	}
	var rec struct {
		Cycles []dependencyCycle `json:"cycles"`
	}
	if err := json.Unmarshal(data, &rec); err != nil || len(rec.Cycles) != 1 {
		t.Fatalf("cycle record = %s (%v)", data, err)
	}

	// Removing one edge breaks the cycle: the card clears and the record empties.
	g.Edges = edges[:2]
	as.reconcileDependencyCycles(g, time.Now())
	if n := len(openCycleCards()); n != 0 {
		t.Fatalf("card still open after an edge was removed (%d)", n)
	}
	data, _ = os.ReadFile(filepath.Join(dir, DependencyCyclesFile))
	if err := json.Unmarshal(data, &rec); err != nil || len(rec.Cycles) != 0 {
		t.Fatalf("cycle record after removal = %s (%v)", data, err)
	}
}
