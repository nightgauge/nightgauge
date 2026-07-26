package orchestrator

import (
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/depgraph"
)

// TestDescribeEdgeSource pins the blocked-dispatch provenance label. A
// dependency edge created by a line of issue-body prose used to log
// identically to a real GitHub blockedBy relation, so an operator facing an
// idle fleet had no way to tell which one was holding it — removing the
// GraphQL relation changed nothing, because the body re-created the edge on
// every graph rebuild (#126).
func TestDescribeEdgeSource(t *testing.T) {
	const repo = "owner/repo"

	g := depgraph.NewGraph()
	for _, n := range []int{1, 2, 3, 4, 5} {
		g.AddNode(&depgraph.Node{Repo: repo, Number: n, State: "OPEN"})
	}

	from := g.NodeKey(depgraph.NodeID{Repo: repo, Number: 1})

	g.AddEdge(depgraph.Edge{
		From: depgraph.NodeID{Repo: repo, Number: 1},
		To:   depgraph.NodeID{Repo: repo, Number: 2},
		Type: "blockedBy", Source: "graphql",
	})
	g.AddEdge(depgraph.Edge{
		From: depgraph.NodeID{Repo: repo, Number: 1},
		To:   depgraph.NodeID{Repo: repo, Number: 3},
		Type: "crossRepo", Source: "structured_section",
		SourceLine: "- ⚠️ owner/repo#3 — store distribution",
	})
	g.AddEdge(depgraph.Edge{
		From: depgraph.NodeID{Repo: repo, Number: 1},
		To:   depgraph.NodeID{Repo: repo, Number: 4},
		Type: "crossRepo", Source: "depends_on",
	})

	tests := []struct {
		name     string
		to       int
		contains []string
	}{
		{
			name:     "real GitHub relation is named blockedBy",
			to:       2,
			contains: []string{"blockedBy"},
		},
		{
			name:     "body-derived edge names its source and the prose line",
			to:       3,
			contains: []string{"structured_section", "store distribution"},
		},
		{
			name:     "body-derived edge without a captured line still names its source",
			to:       4,
			contains: []string{"depends_on"},
		},
		{
			name:     "no edge between the nodes reports unknown rather than lying",
			to:       5,
			contains: []string{"unknown"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := describeEdgeSource(g, from, g.NodeKey(depgraph.NodeID{Repo: repo, Number: tc.to}))
			for _, want := range tc.contains {
				if !strings.Contains(got, want) {
					t.Errorf("describeEdgeSource(...#%d) = %q, want it to contain %q", tc.to, got, want)
				}
			}
		})
	}

	// A body-derived edge must never be mistaken for a real relation.
	bodyEdge := describeEdgeSource(g, from, g.NodeKey(depgraph.NodeID{Repo: repo, Number: 3}))
	if strings.Contains(bodyEdge, "blockedBy") {
		t.Errorf("body-derived edge %q must be distinguishable from a blockedBy relation", bodyEdge)
	}
}
