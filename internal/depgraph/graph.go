// Package depgraph constructs a unified cross-repo dependency DAG from GitHub
// issues, computes topological execution waves, and finds the critical path.
// The graph package is pure (no side effects) — the builder is the only part
// that calls GitHub.
package depgraph

import (
	"fmt"
	"strings"
)

// NodeID uniquely identifies an issue across repositories.
type NodeID struct {
	Repo   string `json:"repo"`   // "nightgauge/nightgauge"
	Number int    `json:"number"` // 2370
}

// String returns the canonical "repo#number" key for this node.
func (id NodeID) String() string {
	return fmt.Sprintf("%s#%d", id.Repo, id.Number)
}

// Node represents an issue in the cross-repo dependency graph.
type Node struct {
	Repo        string   `json:"repo"`
	Number      int      `json:"number"`
	Title       string   `json:"title"`
	State       string   `json:"state"`       // OPEN, CLOSED
	BoardStatus string   `json:"boardStatus"` // Project board status: Ready, Backlog, In progress, etc.
	Size        string   `json:"size"`        // XS, S, M, L, XL
	Priority    string   `json:"priority"`    // P0, P1, P2, P3
	Labels      []string `json:"labels"`
	EpicNumber  int      `json:"epicNumber,omitempty"` // parent epic if sub-issue
	Weight      int      `json:"weight"`               // size weight for critical path
}

// ID returns the NodeID for this node.
func (n *Node) ID() NodeID {
	return NodeID{Repo: n.Repo, Number: n.Number}
}

// Edge represents a dependency between two issues.
type Edge struct {
	From       NodeID `json:"from"`       // blocked issue (depends on To)
	To         NodeID `json:"to"`         // blocking issue
	Type       string `json:"type"`       // "blockedBy", "crossRepo"
	Source     string `json:"source"`     // "graphql", "body_text", "structured_section", "depends_on"
	Resolvable bool   `json:"resolvable"` // false if target repo not in workspace
}

// Graph is the unified cross-repo dependency DAG.
type Graph struct {
	Nodes        map[string]*Node `json:"nodes"` // key = "repo#number"
	Edges        []Edge           `json:"edges"`
	Waves        [][]NodeID       `json:"waves"`            // topological waves
	CriticalPath []NodeID         `json:"criticalPath"`     // longest weighted path
	Cycles       [][]NodeID       `json:"cycles,omitempty"` // detected cycles
	Stats        GraphStats       `json:"stats"`
}

// GraphStats summarizes graph properties.
type GraphStats struct {
	TotalNodes        int `json:"totalNodes"`
	TotalEdges        int `json:"totalEdges"`
	Repos             int `json:"repos"`
	MaxDepth          int `json:"maxDepth"`          // number of waves
	CriticalLength    int `json:"criticalLength"`    // sum of weights on critical path
	DroppedItemsCount int `json:"droppedItemsCount"` // raw board nodes not added as graph nodes
}

// NewGraph creates an empty dependency graph.
func NewGraph() *Graph {
	return &Graph{
		Nodes: make(map[string]*Node),
	}
}

// NodeKey returns the canonical map key for a NodeID.
func (g *Graph) NodeKey(id NodeID) string {
	return id.String()
}

// AddNode adds an issue node to the graph. If a node with the same key
// already exists it is overwritten.
func (g *Graph) AddNode(node *Node) {
	if node == nil {
		return
	}
	key := g.NodeKey(node.ID())
	g.Nodes[key] = node
}

// AddEdge appends a dependency edge. Duplicates are NOT checked here;
// dedup is the caller's responsibility.
func (g *Graph) AddEdge(edge Edge) {
	g.Edges = append(g.Edges, edge)
}

// SizeWeight maps a size label string to a Fibonacci-ish weight.
func SizeWeight(size string) int {
	switch strings.ToUpper(strings.TrimSpace(size)) {
	case "XS":
		return 1
	case "S":
		return 2
	case "M":
		return 3
	case "L":
		return 5
	case "XL":
		return 8
	default:
		return 3 // default to medium
	}
}

// Adjacency returns the outgoing adjacency list (from → []to) built from
// g.Edges. Only edges whose From and To are present in g.Nodes are included.
func (g *Graph) Adjacency() map[string][]string {
	adj := make(map[string][]string)
	for _, e := range g.Edges {
		fromKey := g.NodeKey(e.From)
		toKey := g.NodeKey(e.To)
		if _, ok := g.Nodes[fromKey]; !ok {
			continue
		}
		if _, ok := g.Nodes[toKey]; !ok {
			continue
		}
		adj[fromKey] = append(adj[fromKey], toKey)
	}
	return adj
}

// ReverseAdjacency returns the reverse adjacency list (to → []from).
// Only edges whose endpoints are present in g.Nodes are included.
func (g *Graph) ReverseAdjacency() map[string][]string {
	rev := make(map[string][]string)
	for _, e := range g.Edges {
		fromKey := g.NodeKey(e.From)
		toKey := g.NodeKey(e.To)
		if _, ok := g.Nodes[fromKey]; !ok {
			continue
		}
		if _, ok := g.Nodes[toKey]; !ok {
			continue
		}
		rev[toKey] = append(rev[toKey], fromKey)
	}
	return rev
}

// ComputeStats updates g.Stats from the current Nodes, Edges, Waves, and
// CriticalPath.
func (g *Graph) ComputeStats() {
	repoSet := make(map[string]bool)
	for _, n := range g.Nodes {
		repoSet[n.Repo] = true
	}

	critLen := 0
	for _, id := range g.CriticalPath {
		key := g.NodeKey(id)
		if n, ok := g.Nodes[key]; ok {
			critLen += n.Weight
		}
	}

	g.Stats = GraphStats{
		TotalNodes:        len(g.Nodes),
		TotalEdges:        len(g.Edges),
		Repos:             len(repoSet),
		MaxDepth:          len(g.Waves),
		CriticalLength:    critLen,
		DroppedItemsCount: g.Stats.DroppedItemsCount, // preserve — set by BuildGraph before ComputeStats
	}
}
