package orchestrator

import (
	"testing"

	"github.com/nightgauge/nightgauge/pkg/types"
)

func TestTopologicalSort_NoDeps(t *testing.T) {
	g := NewDependencyGraph()
	g.AddItem(types.BoardItem{Number: 1, State: "OPEN"}, nil, nil)
	g.AddItem(types.BoardItem{Number: 2, State: "OPEN"}, nil, nil)
	g.AddItem(types.BoardItem{Number: 3, State: "OPEN"}, nil, nil)

	sorted := g.TopologicalSort()
	if len(sorted) != 3 {
		t.Errorf("sorted = %d items, want 3", len(sorted))
	}
}

func TestTopologicalSort_LinearChain(t *testing.T) {
	g := NewDependencyGraph()
	// 1 → 2 → 3 (1 blocks 2, 2 blocks 3)
	g.AddItem(types.BoardItem{Number: 1, State: "OPEN"}, nil,
		[]types.BlockingRef{{Number: 2}})
	g.AddItem(types.BoardItem{Number: 2, State: "OPEN"},
		[]types.BlockingRef{{Number: 1}},
		[]types.BlockingRef{{Number: 3}})
	g.AddItem(types.BoardItem{Number: 3, State: "OPEN"},
		[]types.BlockingRef{{Number: 2}}, nil)

	sorted := g.TopologicalSort()
	if len(sorted) != 3 {
		t.Fatalf("sorted = %d items, want 3", len(sorted))
	}
	if sorted[0] != 1 {
		t.Errorf("first should be 1 (no deps), got %d", sorted[0])
	}
	// 2 must come before 3
	idx2, idx3 := indexOf(sorted, 2), indexOf(sorted, 3)
	if idx2 > idx3 {
		t.Errorf("2 should come before 3, got order: %v", sorted)
	}
}

func TestIsBlocked(t *testing.T) {
	g := NewDependencyGraph()
	g.AddItem(types.BoardItem{Number: 1, State: "OPEN"}, nil, nil)
	g.AddItem(types.BoardItem{Number: 2, State: "OPEN"},
		[]types.BlockingRef{{Number: 1}}, nil)

	if g.IsBlocked(1) {
		t.Error("1 should not be blocked")
	}
	if !g.IsBlocked(2) {
		t.Error("2 should be blocked by 1")
	}
}

func TestIsBlocked_ClosedBlocker(t *testing.T) {
	g := NewDependencyGraph()
	g.AddItem(types.BoardItem{Number: 1, State: "CLOSED"}, nil, nil)
	g.AddItem(types.BoardItem{Number: 2, State: "OPEN"},
		[]types.BlockingRef{{Number: 1}}, nil)

	if g.IsBlocked(2) {
		t.Error("2 should not be blocked (blocker 1 is closed)")
	}
}

func TestGetBlockers(t *testing.T) {
	g := NewDependencyGraph()
	g.AddItem(types.BoardItem{Number: 5, State: "OPEN"},
		[]types.BlockingRef{{Number: 3}, {Number: 4}}, nil)

	blockers := g.GetBlockers(5)
	if len(blockers) != 2 {
		t.Errorf("blockers = %d, want 2", len(blockers))
	}
}

func TestValidate_NoCycle(t *testing.T) {
	g := NewDependencyGraph()
	g.AddItem(types.BoardItem{Number: 1, State: "OPEN"}, nil,
		[]types.BlockingRef{{Number: 2}})
	g.AddItem(types.BoardItem{Number: 2, State: "OPEN"},
		[]types.BlockingRef{{Number: 1}}, nil)

	if err := g.Validate(); err != nil {
		t.Errorf("should not have cycle: %v", err)
	}
}

func TestPriorityRank(t *testing.T) {
	tests := []struct {
		priority types.Priority
		want     int
	}{
		{types.PriorityP0, 0},
		{types.PriorityP1, 1},
		{types.PriorityP2, 2},
		{types.PriorityP3, 3},
		{"", 4},
	}

	for _, tt := range tests {
		got := priorityRank(tt.priority)
		if got != tt.want {
			t.Errorf("priorityRank(%q) = %d, want %d", tt.priority, got, tt.want)
		}
	}
}

func TestSplitOwnerRepo(t *testing.T) {
	tests := []struct {
		input     string
		wantOwner string
		wantRepo  string
	}{
		{"nightgauge/nightgauge", "nightgauge", "nightgauge"},
		{"nightgauge", "", "nightgauge"},
	}

	for _, tt := range tests {
		owner, repo := splitOwnerRepo(tt.input)
		if owner != tt.wantOwner || repo != tt.wantRepo {
			t.Errorf("splitOwnerRepo(%q) = (%q, %q), want (%q, %q)",
				tt.input, owner, repo, tt.wantOwner, tt.wantRepo)
		}
	}
}

func indexOf(s []int, val int) int {
	for i, v := range s {
		if v == val {
			return i
		}
	}
	return -1
}
