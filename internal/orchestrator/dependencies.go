package orchestrator

import (
	"context"
	"fmt"
	"strings"

	"github.com/nightgauge/nightgauge/pkg/types"
)

// DependencyGraph represents blocking relationships between issues.
type DependencyGraph struct {
	nodes    map[int]*depNode
	issueSvc interface {
		GetIssue(ctx context.Context, owner, repo string, number int) (*types.Issue, error)
	}
}

type depNode struct {
	Number    int
	Repo      string
	State     string
	BlockedBy []int
	Blocking  []int
}

// NewDependencyGraph creates a dependency graph from a set of board items.
func NewDependencyGraph() *DependencyGraph {
	return &DependencyGraph{
		nodes: make(map[int]*depNode),
	}
}

// AddItem adds a board item and its blocking relationships to the graph.
func (g *DependencyGraph) AddItem(item types.BoardItem, blockedBy, blocking []types.BlockingRef) {
	node := &depNode{
		Number: item.Number,
		Repo:   item.Repo,
		State:  item.State,
	}
	for _, b := range blockedBy {
		node.BlockedBy = append(node.BlockedBy, b.Number)
	}
	for _, b := range blocking {
		node.Blocking = append(node.Blocking, b.Number)
	}
	g.nodes[item.Number] = node
}

// TopologicalSort returns issues in dependency order (unblocked first).
// Issues with no dependencies come first, followed by issues whose
// dependencies are all resolved.
func (g *DependencyGraph) TopologicalSort() []int {
	visited := make(map[int]bool)
	result := make([]int, 0, len(g.nodes))

	// Kahn's algorithm
	inDegree := make(map[int]int)
	for num := range g.nodes {
		inDegree[num] = 0
	}
	for _, node := range g.nodes {
		for _, dep := range node.BlockedBy {
			if _, exists := g.nodes[dep]; exists {
				inDegree[node.Number]++
			}
		}
	}

	// Start with nodes that have no dependencies
	var queue []int
	for num, degree := range inDegree {
		if degree == 0 {
			queue = append(queue, num)
		}
	}

	for len(queue) > 0 {
		num := queue[0]
		queue = queue[1:]

		if visited[num] {
			continue
		}
		visited[num] = true
		result = append(result, num)

		// Reduce in-degree of dependents
		if node, ok := g.nodes[num]; ok {
			for _, downstream := range node.Blocking {
				if _, exists := g.nodes[downstream]; exists {
					inDegree[downstream]--
					if inDegree[downstream] == 0 {
						queue = append(queue, downstream)
					}
				}
			}
		}
	}

	// Add any remaining nodes (cycles)
	for num := range g.nodes {
		if !visited[num] {
			result = append(result, num)
		}
	}

	return result
}

// IsBlocked returns true if the issue has any open blocker in the graph.
func (g *DependencyGraph) IsBlocked(number int) bool {
	node, ok := g.nodes[number]
	if !ok {
		return false
	}
	for _, dep := range node.BlockedBy {
		if depNode, exists := g.nodes[dep]; exists {
			if strings.EqualFold(depNode.State, "OPEN") {
				return true
			}
		}
	}
	return false
}

// GetBlockers returns the issue numbers that block the given issue.
func (g *DependencyGraph) GetBlockers(number int) []int {
	node, ok := g.nodes[number]
	if !ok {
		return nil
	}
	return node.BlockedBy
}

// Validate checks for circular dependencies.
func (g *DependencyGraph) Validate() error {
	sorted := g.TopologicalSort()
	if len(sorted) < len(g.nodes) {
		return fmt.Errorf("circular dependency detected: sorted %d of %d nodes", len(sorted), len(g.nodes))
	}
	return nil
}
