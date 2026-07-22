package depgraph

import (
	"testing"

	"github.com/nightgauge/nightgauge/pkg/types"
)

func TestNewGraph(t *testing.T) {
	g := NewGraph()
	if g == nil {
		t.Fatal("NewGraph returned nil")
	}
	if g.Nodes == nil {
		t.Fatal("Nodes map should be initialized")
	}
	if len(g.Nodes) != 0 {
		t.Errorf("expected 0 nodes, got %d", len(g.Nodes))
	}
}

func TestNodeKey(t *testing.T) {
	g := NewGraph()
	id := NodeID{Repo: "nightgauge/nightgauge", Number: 42}
	key := g.NodeKey(id)
	if key != "nightgauge/nightgauge#42" {
		t.Errorf("expected 'nightgauge/nightgauge#42', got %q", key)
	}
}

func TestAddNode(t *testing.T) {
	g := NewGraph()
	node := &Node{Repo: "nightgauge/nightgauge", Number: 1, Title: "Test"}
	g.AddNode(node)
	if len(g.Nodes) != 1 {
		t.Errorf("expected 1 node, got %d", len(g.Nodes))
	}
	key := g.NodeKey(node.ID())
	if _, ok := g.Nodes[key]; !ok {
		t.Errorf("node not found by key %q", key)
	}
}

func TestAddNilNode(t *testing.T) {
	g := NewGraph()
	g.AddNode(nil)
	if len(g.Nodes) != 0 {
		t.Errorf("nil node should not be added")
	}
}

func TestAddEdge(t *testing.T) {
	g := NewGraph()
	e := Edge{
		From: NodeID{Repo: "R", Number: 1},
		To:   NodeID{Repo: "R", Number: 2},
		Type: "blockedBy",
	}
	g.AddEdge(e)
	if len(g.Edges) != 1 {
		t.Errorf("expected 1 edge, got %d", len(g.Edges))
	}
}

func TestSizeWeight(t *testing.T) {
	tests := []struct {
		size     string
		expected int
	}{
		{"XS", 1},
		{"xs", 1},
		{"S", 2},
		{"M", 3},
		{"L", 5},
		{"XL", 8},
		{" xl ", 8},
		{"", 3},        // default
		{"unknown", 3}, // default
	}
	for _, tt := range tests {
		got := SizeWeight(tt.size)
		if got != tt.expected {
			t.Errorf("SizeWeight(%q) = %d, want %d", tt.size, got, tt.expected)
		}
	}
}

func TestSingleRepoDAG(t *testing.T) {
	// A → B → C (A blocked by B, B blocked by C)
	g := NewGraph()
	g.AddNode(&Node{Repo: "R", Number: 1, Title: "A", Size: "S", Weight: 2})
	g.AddNode(&Node{Repo: "R", Number: 2, Title: "B", Size: "M", Weight: 3})
	g.AddNode(&Node{Repo: "R", Number: 3, Title: "C", Size: "S", Weight: 2})

	// A depends on B, B depends on C
	g.AddEdge(Edge{From: NodeID{"R", 1}, To: NodeID{"R", 2}, Type: "blockedBy"})
	g.AddEdge(Edge{From: NodeID{"R", 2}, To: NodeID{"R", 3}, Type: "blockedBy"})

	waves, cycles := ComputeWaves(g)
	if len(cycles) != 0 {
		t.Errorf("expected no cycles, got %d", len(cycles))
	}
	if len(waves) != 3 {
		t.Fatalf("expected 3 waves, got %d: %v", len(waves), waves)
	}

	// Wave 0: C (no dependencies)
	if len(waves[0]) != 1 || waves[0][0].Number != 3 {
		t.Errorf("wave 0 should be [C], got %v", waves[0])
	}
	// Wave 1: B
	if len(waves[1]) != 1 || waves[1][0].Number != 2 {
		t.Errorf("wave 1 should be [B], got %v", waves[1])
	}
	// Wave 2: A
	if len(waves[2]) != 1 || waves[2][0].Number != 1 {
		t.Errorf("wave 2 should be [A], got %v", waves[2])
	}
}

func TestCrossRepoDAG(t *testing.T) {
	// Three repos: core, platform, flutter
	// core#1 depends on platform#10
	// core#2 depends on flutter#20
	// platform#10 and flutter#20 are independent
	g := NewGraph()
	g.AddNode(&Node{Repo: "O/core", Number: 1, Title: "Core 1", Size: "M", Weight: 3})
	g.AddNode(&Node{Repo: "O/core", Number: 2, Title: "Core 2", Size: "S", Weight: 2})
	g.AddNode(&Node{Repo: "O/platform", Number: 10, Title: "Platform 10", Size: "L", Weight: 5})
	g.AddNode(&Node{Repo: "O/flutter", Number: 20, Title: "Flutter 20", Size: "M", Weight: 3})

	g.AddEdge(Edge{From: NodeID{"O/core", 1}, To: NodeID{"O/platform", 10}, Type: "crossRepo"})
	g.AddEdge(Edge{From: NodeID{"O/core", 2}, To: NodeID{"O/flutter", 20}, Type: "crossRepo"})

	waves, cycles := ComputeWaves(g)
	if len(cycles) != 0 {
		t.Errorf("expected no cycles, got %d", len(cycles))
	}
	if len(waves) != 2 {
		t.Fatalf("expected 2 waves, got %d", len(waves))
	}

	// Wave 0: platform#10, flutter#20 (no deps)
	if len(waves[0]) != 2 {
		t.Errorf("wave 0 should have 2 nodes, got %d", len(waves[0]))
	}
	// Wave 1: core#1, core#2
	if len(waves[1]) != 2 {
		t.Errorf("wave 1 should have 2 nodes, got %d", len(waves[1]))
	}

	g.Waves = waves
	g.CriticalPath = ComputeCriticalPath(g)
	g.ComputeStats()

	if g.Stats.TotalNodes != 4 {
		t.Errorf("expected 4 total nodes, got %d", g.Stats.TotalNodes)
	}
	if g.Stats.TotalEdges != 2 {
		t.Errorf("expected 2 total edges, got %d", g.Stats.TotalEdges)
	}
	if g.Stats.Repos != 3 {
		t.Errorf("expected 3 repos, got %d", g.Stats.Repos)
	}
}

func TestCycleDetection(t *testing.T) {
	// A→B→C→A
	g := NewGraph()
	g.AddNode(&Node{Repo: "R", Number: 1, Title: "A", Weight: 1})
	g.AddNode(&Node{Repo: "R", Number: 2, Title: "B", Weight: 1})
	g.AddNode(&Node{Repo: "R", Number: 3, Title: "C", Weight: 1})

	g.AddEdge(Edge{From: NodeID{"R", 1}, To: NodeID{"R", 2}})
	g.AddEdge(Edge{From: NodeID{"R", 2}, To: NodeID{"R", 3}})
	g.AddEdge(Edge{From: NodeID{"R", 3}, To: NodeID{"R", 1}})

	_, cycles := ComputeWaves(g)
	if len(cycles) == 0 {
		t.Error("expected cycle to be detected")
	}
}

func TestOrphanedNodes(t *testing.T) {
	// 4 nodes, no edges — all should be in wave 0
	g := NewGraph()
	g.AddNode(&Node{Repo: "R", Number: 1, Title: "A", Weight: 1})
	g.AddNode(&Node{Repo: "R", Number: 2, Title: "B", Weight: 2})
	g.AddNode(&Node{Repo: "R", Number: 3, Title: "C", Weight: 3})
	g.AddNode(&Node{Repo: "R", Number: 4, Title: "D", Weight: 4})

	waves, cycles := ComputeWaves(g)
	if len(cycles) != 0 {
		t.Errorf("expected no cycles, got %d", len(cycles))
	}
	if len(waves) != 1 {
		t.Fatalf("expected 1 wave, got %d", len(waves))
	}
	if len(waves[0]) != 4 {
		t.Errorf("expected 4 nodes in wave 0, got %d", len(waves[0]))
	}
}

func TestCriticalPathComputation(t *testing.T) {
	// Linear chain: D(XL=8) → C(L=5) → B(M=3) → A(S=2)
	// Critical path should be D → C → B → A with total weight 18
	g := NewGraph()
	g.AddNode(&Node{Repo: "R", Number: 1, Title: "A", Size: "S", Weight: 2})
	g.AddNode(&Node{Repo: "R", Number: 2, Title: "B", Size: "M", Weight: 3})
	g.AddNode(&Node{Repo: "R", Number: 3, Title: "C", Size: "L", Weight: 5})
	g.AddNode(&Node{Repo: "R", Number: 4, Title: "D", Size: "XL", Weight: 8})

	g.AddEdge(Edge{From: NodeID{"R", 1}, To: NodeID{"R", 2}})
	g.AddEdge(Edge{From: NodeID{"R", 2}, To: NodeID{"R", 3}})
	g.AddEdge(Edge{From: NodeID{"R", 3}, To: NodeID{"R", 4}})

	cp := ComputeCriticalPath(g)
	if len(cp) != 4 {
		t.Fatalf("expected critical path of 4, got %d: %v", len(cp), cp)
	}
	// Should be D → C → B → A (execution order)
	if cp[0].Number != 4 {
		t.Errorf("critical path should start with D(#4), got #%d", cp[0].Number)
	}
	if cp[3].Number != 1 {
		t.Errorf("critical path should end with A(#1), got #%d", cp[3].Number)
	}

	g.CriticalPath = cp
	g.ComputeStats()
	if g.Stats.CriticalLength != 18 {
		t.Errorf("expected critical length 18, got %d", g.Stats.CriticalLength)
	}
}

func TestCriticalPathBranching(t *testing.T) {
	// Diamond: A depends on B and C, B depends on D, C depends on D
	// Weights: A=1, B=8, C=2, D=1
	// Critical path: D → B → A (weight 10)
	g := NewGraph()
	g.AddNode(&Node{Repo: "R", Number: 1, Title: "A", Weight: 1})
	g.AddNode(&Node{Repo: "R", Number: 2, Title: "B", Weight: 8})
	g.AddNode(&Node{Repo: "R", Number: 3, Title: "C", Weight: 2})
	g.AddNode(&Node{Repo: "R", Number: 4, Title: "D", Weight: 1})

	g.AddEdge(Edge{From: NodeID{"R", 1}, To: NodeID{"R", 2}}) // A depends on B
	g.AddEdge(Edge{From: NodeID{"R", 1}, To: NodeID{"R", 3}}) // A depends on C
	g.AddEdge(Edge{From: NodeID{"R", 2}, To: NodeID{"R", 4}}) // B depends on D
	g.AddEdge(Edge{From: NodeID{"R", 3}, To: NodeID{"R", 4}}) // C depends on D

	cp := ComputeCriticalPath(g)
	if len(cp) != 3 {
		t.Fatalf("expected critical path of 3, got %d: %v", len(cp), cp)
	}
	// D → B → A
	if cp[0].Number != 4 || cp[1].Number != 2 || cp[2].Number != 1 {
		t.Errorf("expected D→B→A, got %v", cp)
	}

	g.CriticalPath = cp
	g.ComputeStats()
	// D(1) + B(8) + A(1) = 10
	if g.Stats.CriticalLength != 10 {
		t.Errorf("expected critical length 10, got %d", g.Stats.CriticalLength)
	}
}

func TestWaveComputationParallelGroups(t *testing.T) {
	// A depends on C, B depends on C, D is independent
	// Wave 0: C, D
	// Wave 1: A, B
	g := NewGraph()
	g.AddNode(&Node{Repo: "R", Number: 1, Title: "A", Weight: 1})
	g.AddNode(&Node{Repo: "R", Number: 2, Title: "B", Weight: 1})
	g.AddNode(&Node{Repo: "R", Number: 3, Title: "C", Weight: 1})
	g.AddNode(&Node{Repo: "R", Number: 4, Title: "D", Weight: 1})

	g.AddEdge(Edge{From: NodeID{"R", 1}, To: NodeID{"R", 3}}) // A depends on C
	g.AddEdge(Edge{From: NodeID{"R", 2}, To: NodeID{"R", 3}}) // B depends on C

	waves, cycles := ComputeWaves(g)
	if len(cycles) != 0 {
		t.Errorf("expected no cycles")
	}
	if len(waves) != 2 {
		t.Fatalf("expected 2 waves, got %d", len(waves))
	}
	// Wave 0: C and D (no deps)
	if len(waves[0]) != 2 {
		t.Errorf("wave 0 should have 2 nodes, got %d", len(waves[0]))
	}
	// Wave 1: A and B
	if len(waves[1]) != 2 {
		t.Errorf("wave 1 should have 2 nodes, got %d", len(waves[1]))
	}
}

func TestEmptyGraph(t *testing.T) {
	g := NewGraph()
	waves, cycles := ComputeWaves(g)
	if waves != nil {
		t.Errorf("expected nil waves for empty graph")
	}
	if cycles != nil {
		t.Errorf("expected nil cycles for empty graph")
	}
	cp := ComputeCriticalPath(g)
	if cp != nil {
		t.Errorf("expected nil critical path for empty graph")
	}
}

func TestAdjacency(t *testing.T) {
	g := NewGraph()
	g.AddNode(&Node{Repo: "R", Number: 1})
	g.AddNode(&Node{Repo: "R", Number: 2})
	g.AddEdge(Edge{From: NodeID{"R", 1}, To: NodeID{"R", 2}})

	adj := g.Adjacency()
	if len(adj["R#1"]) != 1 || adj["R#1"][0] != "R#2" {
		t.Errorf("expected R#1 → [R#2], got %v", adj)
	}

	rev := g.ReverseAdjacency()
	if len(rev["R#2"]) != 1 || rev["R#2"][0] != "R#1" {
		t.Errorf("expected R#2 ← [R#1], got %v", rev)
	}
}

func TestAdjacencySkipsMissingNodes(t *testing.T) {
	g := NewGraph()
	g.AddNode(&Node{Repo: "R", Number: 1})
	// Edge to a node not in the graph
	g.AddEdge(Edge{From: NodeID{"R", 1}, To: NodeID{"R", 99}})

	adj := g.Adjacency()
	if len(adj["R#1"]) != 0 {
		t.Errorf("edge to missing node should be excluded, got %v", adj)
	}
}

func TestBuildGraphFromItems(t *testing.T) {
	items := []types.BoardItem{
		{
			Number: 1,
			Title:  "Issue A",
			State:  "OPEN",
			Repo:   "O/core",
			Size:   "M",
			BlockedBy: []types.BlockingRef{
				{Number: 2, Repo: "O/core", State: "OPEN"},
			},
		},
		{
			Number: 2,
			Title:  "Issue B",
			State:  "OPEN",
			Repo:   "O/core",
			Size:   "S",
		},
		{
			Number: 3,
			Title:  "Issue C",
			State:  "OPEN",
			Repo:   "O/platform",
			Size:   "L",
		},
	}

	bodies := map[string]string{
		"O/core#1": "Blocked by platform #3",
	}

	workspace := map[string]bool{
		"O/core":     true,
		"O/platform": true,
	}

	aliases := map[string]string{
		"platform": "O/platform",
		"core":     "O/core",
	}

	g := BuildGraphFromItems(items, bodies, workspace, aliases)

	if len(g.Nodes) != 3 {
		t.Errorf("expected 3 nodes, got %d", len(g.Nodes))
	}
	// Should have 2 edges: core#1 blockedBy core#2, core#1 crossRepo platform#3
	if len(g.Edges) != 2 {
		t.Errorf("expected 2 edges, got %d", len(g.Edges))
	}
	if len(g.Waves) == 0 {
		t.Error("expected waves to be computed")
	}
	if len(g.CriticalPath) == 0 {
		t.Error("expected critical path to be computed")
	}
}

func TestBuildGraphFromItemsSkipsPRs(t *testing.T) {
	items := []types.BoardItem{
		{Number: 1, Title: "Issue", State: "OPEN", Repo: "O/R", Size: "S"},
		{Number: 2, Title: "PR", State: "OPEN", Repo: "O/R", IsPR: true},
	}

	g := BuildGraphFromItems(items, nil, nil, nil)
	if len(g.Nodes) != 1 {
		t.Errorf("expected 1 node (PR should be skipped), got %d", len(g.Nodes))
	}
}

func TestEdgeResolvability(t *testing.T) {
	items := []types.BoardItem{
		{
			Number: 1,
			Title:  "Issue A",
			State:  "OPEN",
			Repo:   "O/core",
			BlockedBy: []types.BlockingRef{
				{Number: 10, Repo: "O/external", State: "OPEN"},
			},
		},
	}

	workspace := map[string]bool{
		"O/core": true,
		// O/external is NOT in workspace
	}

	g := BuildGraphFromItems(items, nil, workspace, nil)
	if len(g.Edges) != 1 {
		t.Fatalf("expected 1 edge, got %d", len(g.Edges))
	}
	if g.Edges[0].Resolvable {
		t.Error("edge to non-workspace repo should not be resolvable")
	}
}

func TestSplitOwnerName(t *testing.T) {
	if splitOwner("nightgauge/nightgauge") != "nightgauge" {
		t.Error("splitOwner failed")
	}
	if splitName("nightgauge/nightgauge") != "nightgauge" {
		t.Error("splitName failed")
	}
	if splitOwner("noslash") != "noslash" {
		t.Error("splitOwner with no slash should return input")
	}
	if splitName("noslash") != "noslash" {
		t.Error("splitName with no slash should return input")
	}
}

// -- ComputeWaves edge case tests --

func TestComputeWaves_DisconnectedComponents(t *testing.T) {
	// Two independent subgraphs: A→B and C→D
	// Wave 0: B, D (no dependencies)
	// Wave 1: A, C (each depends on one wave-0 node)
	g := NewGraph()
	g.AddNode(&Node{Repo: "R", Number: 1, Title: "A", Weight: 1})
	g.AddNode(&Node{Repo: "R", Number: 2, Title: "B", Weight: 1})
	g.AddNode(&Node{Repo: "R", Number: 3, Title: "C", Weight: 1})
	g.AddNode(&Node{Repo: "R", Number: 4, Title: "D", Weight: 1})

	g.AddEdge(Edge{From: NodeID{"R", 1}, To: NodeID{"R", 2}}) // A depends on B
	g.AddEdge(Edge{From: NodeID{"R", 3}, To: NodeID{"R", 4}}) // C depends on D

	waves, cycles := ComputeWaves(g)
	if len(cycles) != 0 {
		t.Errorf("expected no cycles, got %d", len(cycles))
	}
	if len(waves) != 2 {
		t.Fatalf("expected 2 waves, got %d: %v", len(waves), waves)
	}
	if len(waves[0]) != 2 {
		t.Errorf("wave 0 should have 2 nodes (B and D), got %d: %v", len(waves[0]), waves[0])
	}
	if len(waves[1]) != 2 {
		t.Errorf("wave 1 should have 2 nodes (A and C), got %d: %v", len(waves[1]), waves[1])
	}
}

func TestComputeWaves_MultipleCycles(t *testing.T) {
	// Two independent 2-node cycles: A↔B and C↔D
	// Both cycles should be detected.
	g := NewGraph()
	g.AddNode(&Node{Repo: "R", Number: 1, Title: "A", Weight: 1})
	g.AddNode(&Node{Repo: "R", Number: 2, Title: "B", Weight: 1})
	g.AddNode(&Node{Repo: "R", Number: 3, Title: "C", Weight: 1})
	g.AddNode(&Node{Repo: "R", Number: 4, Title: "D", Weight: 1})

	g.AddEdge(Edge{From: NodeID{"R", 1}, To: NodeID{"R", 2}}) // A→B
	g.AddEdge(Edge{From: NodeID{"R", 2}, To: NodeID{"R", 1}}) // B→A
	g.AddEdge(Edge{From: NodeID{"R", 3}, To: NodeID{"R", 4}}) // C→D
	g.AddEdge(Edge{From: NodeID{"R", 4}, To: NodeID{"R", 3}}) // D→C

	_, cycles := ComputeWaves(g)
	if len(cycles) == 0 {
		t.Error("expected at least one cycle to be detected")
	}
	// All 4 nodes must appear across the collected cycle groups.
	seenNodes := make(map[int]bool)
	for _, group := range cycles {
		for _, id := range group {
			seenNodes[id.Number] = true
		}
	}
	for _, num := range []int{1, 2, 3, 4} {
		if !seenNodes[num] {
			t.Errorf("node #%d was not reported in any cycle group", num)
		}
	}
}

func TestComputeWaves_SingleNode(t *testing.T) {
	g := NewGraph()
	g.AddNode(&Node{Repo: "R", Number: 1, Title: "Solo", Weight: 3})

	waves, cycles := ComputeWaves(g)
	if len(cycles) != 0 {
		t.Errorf("expected no cycles, got %d", len(cycles))
	}
	if len(waves) != 1 {
		t.Fatalf("expected 1 wave for single node, got %d", len(waves))
	}
	if len(waves[0]) != 1 || waves[0][0].Number != 1 {
		t.Errorf("wave 0 should contain the single node, got %v", waves[0])
	}
}

func TestComputeWaves_DeterministicOutput(t *testing.T) {
	// Same graph run twice must produce identical wave ordering.
	buildGraph := func() *Graph {
		g := NewGraph()
		g.AddNode(&Node{Repo: "R", Number: 1, Weight: 1})
		g.AddNode(&Node{Repo: "R", Number: 2, Weight: 1})
		g.AddNode(&Node{Repo: "R", Number: 3, Weight: 1})
		g.AddNode(&Node{Repo: "R", Number: 4, Weight: 1})
		g.AddEdge(Edge{From: NodeID{"R", 1}, To: NodeID{"R", 3}})
		g.AddEdge(Edge{From: NodeID{"R", 2}, To: NodeID{"R", 3}})
		g.AddEdge(Edge{From: NodeID{"R", 3}, To: NodeID{"R", 4}})
		return g
	}

	waves1, _ := ComputeWaves(buildGraph())
	waves2, _ := ComputeWaves(buildGraph())

	if len(waves1) != len(waves2) {
		t.Fatalf("wave count differs between runs: %d vs %d", len(waves1), len(waves2))
	}
	for i, w := range waves1 {
		if len(w) != len(waves2[i]) {
			t.Errorf("wave %d length differs: %d vs %d", i, len(w), len(waves2[i]))
			continue
		}
		for j, id := range w {
			if id != waves2[i][j] {
				t.Errorf("wave %d[%d]: %v vs %v", i, j, id, waves2[i][j])
			}
		}
	}
}

// -- ComputeCriticalPath edge case tests --

func TestComputeCriticalPath_SingleNode(t *testing.T) {
	g := NewGraph()
	g.AddNode(&Node{Repo: "R", Number: 1, Title: "Solo", Size: "L", Weight: 5})

	cp := ComputeCriticalPath(g)
	if len(cp) != 1 {
		t.Fatalf("expected critical path of length 1, got %d: %v", len(cp), cp)
	}
	if cp[0].Number != 1 {
		t.Errorf("expected node #1 on critical path, got #%d", cp[0].Number)
	}
}

func TestComputeCriticalPath_ZeroWeightNodes(t *testing.T) {
	// Nodes with Weight=0 should fall back to SizeWeight(size) during path selection.
	// Chain: C(XS) depends on B(L) depends on A(XL).
	// Execution order: A first (heaviest), then B, then C.
	// ComputeCriticalPath uses size-derived weights internally: A=8, B=5, C=1.
	// The full chain A→B→C (total=14) must win over any sub-path.
	//
	// Note: ComputeStats.CriticalLength sums n.Weight (the struct field),
	// so it will be 0 for these nodes — that is expected behaviour.
	// This test only verifies that the *path selection* uses the size fallback.
	g := NewGraph()
	g.AddNode(&Node{Repo: "R", Number: 1, Title: "C", Size: "XS"}) // Weight 0 — size fallback = 1
	g.AddNode(&Node{Repo: "R", Number: 2, Title: "B", Size: "L"})  // Weight 0 — size fallback = 5
	g.AddNode(&Node{Repo: "R", Number: 3, Title: "A", Size: "XL"}) // Weight 0 — size fallback = 8

	g.AddEdge(Edge{From: NodeID{"R", 1}, To: NodeID{"R", 2}}) // C depends on B
	g.AddEdge(Edge{From: NodeID{"R", 2}, To: NodeID{"R", 3}}) // B depends on A

	cp := ComputeCriticalPath(g)
	if len(cp) != 3 {
		t.Fatalf("expected critical path of length 3 (full chain), got %d: %v", len(cp), cp)
	}
	// Path must start at A(XL, #3) — the heaviest node drives the longest path.
	if cp[0].Number != 3 {
		t.Errorf("critical path should start at A(XL, #3), got #%d", cp[0].Number)
	}
	// Path must end at C(XS, #1).
	if cp[2].Number != 1 {
		t.Errorf("critical path should end at C(XS, #1), got #%d", cp[2].Number)
	}

	// ComputeStats sums n.Weight (the struct field), not the size-derived fallback.
	// For nodes with Weight=0, CriticalLength will be 0 — this is expected behaviour.
	g.CriticalPath = cp
	g.ComputeStats()
	if g.Stats.CriticalLength != 0 {
		t.Errorf("expected CriticalLength 0 (nodes have Weight=0 field), got %d", g.Stats.CriticalLength)
	}
}

func TestComputeCriticalPath_EqualWeightAlternatives(t *testing.T) {
	// Diamond: A depends on B and C; both B and C have equal weight (Weight=5).
	// D is the root with Weight=1. B→D, C→D, A→B, A→C.
	// Two paths: D→B→A (weight 1+5+2=8) and D→C→A (weight 1+5+2=8) — equal.
	// The algorithm must pick one deterministically.
	g := NewGraph()
	g.AddNode(&Node{Repo: "R", Number: 1, Title: "A", Weight: 2})
	g.AddNode(&Node{Repo: "R", Number: 2, Title: "B", Weight: 5})
	g.AddNode(&Node{Repo: "R", Number: 3, Title: "C", Weight: 5})
	g.AddNode(&Node{Repo: "R", Number: 4, Title: "D", Weight: 1})

	g.AddEdge(Edge{From: NodeID{"R", 1}, To: NodeID{"R", 2}}) // A depends on B
	g.AddEdge(Edge{From: NodeID{"R", 1}, To: NodeID{"R", 3}}) // A depends on C
	g.AddEdge(Edge{From: NodeID{"R", 2}, To: NodeID{"R", 4}}) // B depends on D
	g.AddEdge(Edge{From: NodeID{"R", 3}, To: NodeID{"R", 4}}) // C depends on D

	cp1 := ComputeCriticalPath(g)
	cp2 := ComputeCriticalPath(g)

	if len(cp1) != len(cp2) {
		t.Errorf("non-deterministic: different path lengths %d vs %d", len(cp1), len(cp2))
	}
	for i := range cp1 {
		if cp1[i] != cp2[i] {
			t.Errorf("non-deterministic at index %d: %v vs %v", i, cp1[i], cp2[i])
		}
	}

	// Both equal-weight paths lead through D and end at A.
	if cp1[0].Number != 4 {
		t.Errorf("critical path should start at D(#4), got #%d", cp1[0].Number)
	}
	if cp1[len(cp1)-1].Number != 1 {
		t.Errorf("critical path should end at A(#1), got #%d", cp1[len(cp1)-1].Number)
	}
}

func TestComputeCriticalPath_DisconnectedComponents(t *testing.T) {
	// Two independent chains: A→B (weights 3+2=5) and C→D→E (weights 1+2+8=11).
	// Critical path must span C→D→E.
	g := NewGraph()
	g.AddNode(&Node{Repo: "R", Number: 1, Title: "A", Weight: 3})
	g.AddNode(&Node{Repo: "R", Number: 2, Title: "B", Weight: 2})
	g.AddNode(&Node{Repo: "R", Number: 3, Title: "C", Weight: 1})
	g.AddNode(&Node{Repo: "R", Number: 4, Title: "D", Weight: 2})
	g.AddNode(&Node{Repo: "R", Number: 5, Title: "E", Weight: 8})

	g.AddEdge(Edge{From: NodeID{"R", 1}, To: NodeID{"R", 2}}) // A depends on B
	g.AddEdge(Edge{From: NodeID{"R", 3}, To: NodeID{"R", 4}}) // C depends on D
	g.AddEdge(Edge{From: NodeID{"R", 4}, To: NodeID{"R", 5}}) // D depends on E

	cp := ComputeCriticalPath(g)

	// The critical path must be E→D→C (total weight 11 > A chain weight 5).
	if len(cp) != 3 {
		t.Fatalf("expected critical path length 3 (E→D→C), got %d: %v", len(cp), cp)
	}
	if cp[0].Number != 5 {
		t.Errorf("critical path should start at E(#5), got #%d", cp[0].Number)
	}
	if cp[2].Number != 3 {
		t.Errorf("critical path should end at C(#3), got #%d", cp[2].Number)
	}

	g.CriticalPath = cp
	g.ComputeStats()
	if g.Stats.CriticalLength != 11 {
		t.Errorf("expected CriticalLength 11, got %d", g.Stats.CriticalLength)
	}
}

func TestComputeCriticalPath_DeterministicOutput(t *testing.T) {
	buildGraph := func() *Graph {
		g := NewGraph()
		g.AddNode(&Node{Repo: "R", Number: 1, Weight: 3})
		g.AddNode(&Node{Repo: "R", Number: 2, Weight: 5})
		g.AddNode(&Node{Repo: "R", Number: 3, Weight: 2})
		g.AddNode(&Node{Repo: "R", Number: 4, Weight: 1})
		g.AddEdge(Edge{From: NodeID{"R", 1}, To: NodeID{"R", 2}})
		g.AddEdge(Edge{From: NodeID{"R", 2}, To: NodeID{"R", 3}})
		g.AddEdge(Edge{From: NodeID{"R", 3}, To: NodeID{"R", 4}})
		return g
	}

	cp1 := ComputeCriticalPath(buildGraph())
	cp2 := ComputeCriticalPath(buildGraph())

	if len(cp1) != len(cp2) {
		t.Fatalf("critical path length differs between runs: %d vs %d", len(cp1), len(cp2))
	}
	for i := range cp1 {
		if cp1[i] != cp2[i] {
			t.Errorf("critical path[%d] differs: %v vs %v", i, cp1[i], cp2[i])
		}
	}
}

// -- Integration: full pipeline stats validation --

func TestComputeStats_FullPipeline(t *testing.T) {
	// Build graph, compute topology, then verify all stats are correct.
	// Topology: P→Q→R (linear chain across two repos) + independent S
	// Repos: "O/core" (P,R), "O/platform" (Q), "O/other" (S)
	g := NewGraph()
	g.AddNode(&Node{Repo: "O/core", Number: 1, Title: "R", Size: "S", Weight: 2})
	g.AddNode(&Node{Repo: "O/platform", Number: 2, Title: "Q", Size: "M", Weight: 3})
	g.AddNode(&Node{Repo: "O/core", Number: 3, Title: "P", Size: "L", Weight: 5})
	g.AddNode(&Node{Repo: "O/other", Number: 4, Title: "S", Size: "XS", Weight: 1})

	g.AddEdge(Edge{From: NodeID{"O/core", 1}, To: NodeID{"O/platform", 2}}) // R depends on Q
	g.AddEdge(Edge{From: NodeID{"O/platform", 2}, To: NodeID{"O/core", 3}}) // Q depends on P

	g.Waves, g.Cycles = ComputeWaves(g)
	g.CriticalPath = ComputeCriticalPath(g)
	g.ComputeStats()

	if g.Stats.TotalNodes != 4 {
		t.Errorf("expected TotalNodes 4, got %d", g.Stats.TotalNodes)
	}
	if g.Stats.TotalEdges != 2 {
		t.Errorf("expected TotalEdges 2, got %d", g.Stats.TotalEdges)
	}
	if g.Stats.Repos != 3 {
		t.Errorf("expected Repos 3 (core, platform, other), got %d", g.Stats.Repos)
	}
	// Waves: [P, S], [Q], [R] → MaxDepth = 3
	if g.Stats.MaxDepth != 3 {
		t.Errorf("expected MaxDepth 3, got %d", g.Stats.MaxDepth)
	}
	// Critical path P(5)→Q(3)→R(2) = 10, not including S(1)
	if g.Stats.CriticalLength != 10 {
		t.Errorf("expected CriticalLength 10 (P+Q+R), got %d", g.Stats.CriticalLength)
	}
	if len(g.Cycles) != 0 {
		t.Errorf("expected no cycles, got %d", len(g.Cycles))
	}
}
