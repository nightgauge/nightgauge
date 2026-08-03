// Tests for the #3253 triage gate used by promoteUnblockedOnStartup and
// promoteUnblockedToReady.
//
// The bug: promoteUnblockedOnStartup silently skipped Backlog items with no
// `blockedBy` on the rationale "no dependencies — should already be Ready
// if triaged." That heuristic was wrong: Priority + type label is itself a
// triage signal. Twelve fully-triaged P1 issues (#3216, #3217, …, #3231)
// sat in Backlog forever waiting for a manual promotion. The fix promotes
// any Backlog item that meets the new triage gate.
package orchestrator

import (
	"testing"

	"github.com/nightgauge/nightgauge/internal/depgraph"
)

func makeNode(number int, status string, priority string, labels []string, state string) *depgraph.Node {
	if state == "" {
		state = "OPEN"
	}
	return &depgraph.Node{
		Repo:        "nightgauge/nightgauge",
		Number:      number,
		State:       state,
		BoardStatus: status,
		Priority:    priority,
		Labels:      labels,
		// Trusted by default so pre-existing triage-gate tests (unrelated to
		// the #270 author-trust gate) keep asserting the triage logic alone.
		// Author-trust behavior is covered separately in author_trust_test.go
		// and autonomous_author_trust_test.go.
		AuthorAssociation: "OWNER",
	}
}

func graphWith(nodes ...*depgraph.Node) *depgraph.Graph {
	g := &depgraph.Graph{Nodes: map[string]*depgraph.Node{}, Edges: []depgraph.Edge{}}
	for _, n := range nodes {
		g.Nodes[n.ID().String()] = n
	}
	return g
}

func TestIsTriagedAndUnblocked_HappyPath(t *testing.T) {
	node := makeNode(3216, "Backlog", "P1", []string{"type:bug", "priority:high"}, "OPEN")
	g := graphWith(node)
	if !isTriagedAndUnblocked(node, g, g.Adjacency(), nil) {
		t.Error("triaged P1 + type:bug item with no blockers should be promotable")
	}
}

func TestIsTriagedAndUnblocked_RejectsClosedItems(t *testing.T) {
	node := makeNode(3216, "Backlog", "P1", []string{"type:bug"}, "CLOSED")
	g := graphWith(node)
	if isTriagedAndUnblocked(node, g, g.Adjacency(), nil) {
		t.Error("closed items must never be promoted")
	}
}

func TestIsTriagedAndUnblocked_RejectsNonBacklogStatus(t *testing.T) {
	for _, status := range []string{"Ready", "In progress", "In review", "Done", ""} {
		node := makeNode(3216, status, "P1", []string{"type:bug"}, "OPEN")
		g := graphWith(node)
		if isTriagedAndUnblocked(node, g, g.Adjacency(), nil) {
			t.Errorf("status %q should not be promoted (only Backlog → Ready)", status)
		}
	}
}

func TestIsTriagedAndUnblocked_RejectsMissingPriority(t *testing.T) {
	node := makeNode(3216, "Backlog", "", []string{"type:bug"}, "OPEN")
	g := graphWith(node)
	if isTriagedAndUnblocked(node, g, g.Adjacency(), nil) {
		t.Error("Backlog item with no Priority must NOT be promoted (untriaged)")
	}
}

func TestIsTriagedAndUnblocked_RejectsMissingTypeLabel(t *testing.T) {
	node := makeNode(3216, "Backlog", "P1", []string{"priority:high", "component:pipeline"}, "OPEN")
	g := graphWith(node)
	if isTriagedAndUnblocked(node, g, g.Adjacency(), nil) {
		t.Error("Backlog item with no type:* label must NOT be promoted")
	}
}

func TestIsTriagedAndUnblocked_RejectsEpics(t *testing.T) {
	node := makeNode(3216, "Backlog", "P1", []string{"type:epic"}, "OPEN")
	g := graphWith(node)
	if isTriagedAndUnblocked(node, g, g.Adjacency(), nil) {
		t.Error("epics are tracked not dispatched — must not be promoted")
	}
}

func TestIsTriagedAndUnblocked_RejectsItemsWithOpenBlockers(t *testing.T) {
	blocker := makeNode(100, "In progress", "P1", []string{"type:feature"}, "OPEN")
	blocked := makeNode(3216, "Backlog", "P1", []string{"type:bug"}, "OPEN")
	g := graphWith(blocker, blocked)
	g.Edges = []depgraph.Edge{
		{From: blocked.ID(), To: blocker.ID()},
	}
	adj := g.Adjacency()
	if isTriagedAndUnblocked(blocked, g, adj, nil) {
		t.Error("item with an OPEN blocker must NOT be promoted")
	}
}

func TestIsTriagedAndUnblocked_AcceptsItemsWithAllClosedBlockers(t *testing.T) {
	blocker := makeNode(100, "Done", "P1", []string{"type:feature"}, "CLOSED")
	blocked := makeNode(3216, "Backlog", "P1", []string{"type:bug"}, "OPEN")
	g := graphWith(blocker, blocked)
	g.Edges = []depgraph.Edge{
		{From: blocked.ID(), To: blocker.ID()},
	}
	adj := g.Adjacency()
	if !isTriagedAndUnblocked(blocked, g, adj, nil) {
		t.Error("item with all-closed blockers should be promoted")
	}
}

func TestIsTriagedAndUnblocked_AcceptsAllPriorityLevels(t *testing.T) {
	for _, p := range []string{"P0", "P1", "P2", "P3"} {
		node := makeNode(3216, "Backlog", p, []string{"type:bug"}, "OPEN")
		g := graphWith(node)
		if !isTriagedAndUnblocked(node, g, g.Adjacency(), nil) {
			t.Errorf("priority %q should be promoted", p)
		}
	}
}

func TestIsTriagedAndUnblocked_AcceptsVariousTypeLabels(t *testing.T) {
	for _, lbl := range []string{"type:bug", "type:feature", "type:chore", "type:docs"} {
		node := makeNode(3216, "Backlog", "P1", []string{lbl}, "OPEN")
		g := graphWith(node)
		if !isTriagedAndUnblocked(node, g, g.Adjacency(), nil) {
			t.Errorf("type label %q should pass the triage gate", lbl)
		}
	}
}

func TestIsTriagedAndUnblocked_HandlesNilNode(t *testing.T) {
	g := graphWith()
	if isTriagedAndUnblocked(nil, g, g.Adjacency(), nil) {
		t.Error("nil node must never be promoted")
	}
}

// TestTriageRejectionReason_MirrorsIsTriagedAndUnblocked (#288) asserts the
// diagnostic helper returns the expected first-failing-reason string for each
// rejection branch, and "" for the eligible case — used only for the
// LastPromotionRejectionReasons tally, never for gating.
func TestTriageRejectionReason_MirrorsIsTriagedAndUnblocked(t *testing.T) {
	t.Run("nil node", func(t *testing.T) {
		g := graphWith()
		if got := triageRejectionReason(nil, g, g.Adjacency(), nil); got != "not-open" {
			t.Errorf("nil node: got %q, want %q", got, "not-open")
		}
	})

	t.Run("closed item", func(t *testing.T) {
		node := makeNode(3216, "Backlog", "P1", []string{"type:bug"}, "CLOSED")
		g := graphWith(node)
		if got := triageRejectionReason(node, g, g.Adjacency(), nil); got != "not-open" {
			t.Errorf("closed item: got %q, want %q", got, "not-open")
		}
	})

	t.Run("non-backlog status", func(t *testing.T) {
		node := makeNode(3216, "Ready", "P1", []string{"type:bug"}, "OPEN")
		g := graphWith(node)
		if got := triageRejectionReason(node, g, g.Adjacency(), nil); got != "not-backlog" {
			t.Errorf("non-backlog status: got %q, want %q", got, "not-backlog")
		}
	})

	t.Run("missing priority", func(t *testing.T) {
		node := makeNode(3216, "Backlog", "", []string{"type:bug"}, "OPEN")
		g := graphWith(node)
		if got := triageRejectionReason(node, g, g.Adjacency(), nil); got != "no-priority" {
			t.Errorf("missing priority: got %q, want %q", got, "no-priority")
		}
	})

	t.Run("untrusted author", func(t *testing.T) {
		node := makeNode(3216, "Backlog", "P1", []string{"type:bug"}, "OPEN")
		node.AuthorAssociation = "NONE"
		g := graphWith(node)
		if got := triageRejectionReason(node, g, g.Adjacency(), []string{"OWNER", "MEMBER"}); got != "untrusted-author" {
			t.Errorf("untrusted author: got %q, want %q", got, "untrusted-author")
		}
	})

	t.Run("epic", func(t *testing.T) {
		node := makeNode(3216, "Backlog", "P1", []string{"type:epic"}, "OPEN")
		g := graphWith(node)
		if got := triageRejectionReason(node, g, g.Adjacency(), nil); got != "epic" {
			t.Errorf("epic: got %q, want %q", got, "epic")
		}
	})

	t.Run("missing type label", func(t *testing.T) {
		node := makeNode(3216, "Backlog", "P1", []string{"priority:high"}, "OPEN")
		g := graphWith(node)
		if got := triageRejectionReason(node, g, g.Adjacency(), nil); got != "no-type-label" {
			t.Errorf("missing type label: got %q, want %q", got, "no-type-label")
		}
	})

	t.Run("open blocker", func(t *testing.T) {
		blocker := makeNode(100, "In progress", "P1", []string{"type:feature"}, "OPEN")
		blocked := makeNode(3216, "Backlog", "P1", []string{"type:bug"}, "OPEN")
		g := graphWith(blocker, blocked)
		g.Edges = []depgraph.Edge{{From: blocked.ID(), To: blocker.ID()}}
		adj := g.Adjacency()
		if got := triageRejectionReason(blocked, g, adj, nil); got != "open-blocker" {
			t.Errorf("open blocker: got %q, want %q", got, "open-blocker")
		}
	})

	t.Run("eligible returns empty string", func(t *testing.T) {
		node := makeNode(3216, "Backlog", "P1", []string{"type:bug"}, "OPEN")
		g := graphWith(node)
		if got := triageRejectionReason(node, g, g.Adjacency(), nil); got != "" {
			t.Errorf("eligible node: got %q, want empty string", got)
		}
	})
}
