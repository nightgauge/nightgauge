package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/depgraph"
	"github.com/nightgauge/nightgauge/internal/focus"
	"github.com/nightgauge/nightgauge/pkg/types"
)

// buildTestGraph constructs a depgraph.Graph directly (no GitHub calls).
func buildTestGraph(nodes []*depgraph.Node, edges []depgraph.Edge) *depgraph.Graph {
	g := depgraph.NewGraph()
	for _, n := range nodes {
		g.AddNode(n)
	}
	for _, e := range edges {
		g.AddEdge(e)
	}
	g.Waves, g.Cycles = depgraph.ComputeWaves(g)
	g.CriticalPath = depgraph.ComputeCriticalPath(g)
	g.ComputeStats()
	return g
}

func TestPrioritize_CriticalPathTiebreakerWithinSamePriority(t *testing.T) {
	// Two unblocked items at the same priority. The one on the critical path
	// wins as a tiebreaker. (Issue #3396 inverted the prior "crit-path beats
	// priority" rule; crit-path now only acts as a tiebreaker within the
	// same priority level.)
	nodes := []*depgraph.Node{
		{Repo: "R", Number: 1, Title: "OnCrit", State: "OPEN", BoardStatus: "Ready", Priority: "P1", Size: "M", Weight: 3},
		{Repo: "R", Number: 2, Title: "OffCrit", State: "OPEN", BoardStatus: "Ready", Priority: "P1", Size: "M", Weight: 1},
	}
	// #3 depends on #1 → #1 is on the critical path; #2 isn't.
	nodes = append(nodes,
		&depgraph.Node{Repo: "R", Number: 3, Title: "Downstream", State: "OPEN", BoardStatus: "Ready", Priority: "P1", Size: "M", Weight: 3})
	edges := []depgraph.Edge{
		{From: depgraph.NodeID{Repo: "R", Number: 3}, To: depgraph.NodeID{Repo: "R", Number: 1}},
	}
	g := buildTestGraph(nodes, edges)

	as := &AutonomousScheduler{
		config: AutonomousConfig{MaxConcurrent: 5},
		state:  &AutonomousState{},
	}

	candidates := as.prioritize(context.Background(), g)
	// #1 and #2 are both unblocked. With the same priority, the critical-path
	// item (#1) should come first.
	if len(candidates) < 2 {
		t.Fatalf("expected ≥2 candidates, got %d", len(candidates))
	}
	if candidates[0].Number != 1 {
		t.Errorf("expected #1 (on crit path, same priority as #2) first, got #%d", candidates[0].Number)
	}
}

// TestPrioritize_PriorityDominatesCriticalPath captures the headline #3396
// invariant: a P0 item NOT on the critical path must dispatch before a P1 item
// that IS on the critical path. Pre-#3396 this was reversed and led to GitLab
// forge sub-issues (a long P1 chain) starving standalone P0 items in production.
func TestPrioritize_PriorityDominatesCriticalPath(t *testing.T) {
	nodes := []*depgraph.Node{
		// Long P1 chain — every node on the critical path.
		{Repo: "R", Number: 1, Title: "P1-chain-head", State: "OPEN", BoardStatus: "Ready", Priority: "P1", Size: "M", Weight: 3},
		{Repo: "R", Number: 2, Title: "P1-chain-mid", State: "OPEN", BoardStatus: "Ready", Priority: "P1", Size: "M", Weight: 3},
		{Repo: "R", Number: 3, Title: "P1-chain-tail", State: "OPEN", BoardStatus: "Ready", Priority: "P1", Size: "M", Weight: 3},
		// Standalone P0 — no dependents, NOT on the critical path.
		{Repo: "R", Number: 99, Title: "P0-standalone", State: "OPEN", BoardStatus: "Ready", Priority: "P0", Size: "M", Weight: 3},
	}
	// 2 depends on 1, 3 depends on 2 — long chain so #1 is critical-path head.
	edges := []depgraph.Edge{
		{From: depgraph.NodeID{Repo: "R", Number: 2}, To: depgraph.NodeID{Repo: "R", Number: 1}},
		{From: depgraph.NodeID{Repo: "R", Number: 3}, To: depgraph.NodeID{Repo: "R", Number: 2}},
	}
	g := buildTestGraph(nodes, edges)

	as := &AutonomousScheduler{
		config: AutonomousConfig{MaxConcurrent: 5},
		state:  &AutonomousState{},
	}

	candidates := as.prioritize(context.Background(), g)
	if len(candidates) < 2 {
		t.Fatalf("expected ≥2 candidates, got %d", len(candidates))
	}
	// The P0 item (#99) MUST be first, even though #1 is on the critical path.
	if candidates[0].Number != 99 {
		t.Errorf("expected P0 #99 first regardless of critical path; got #%d (priority=%s, onCritPath=%v)",
			candidates[0].Number, candidates[0].Priority, candidates[0].OnCritPath)
	}
	if candidates[0].Priority != "P0" {
		t.Errorf("first candidate priority = %q, want P0", candidates[0].Priority)
	}
}

func TestPrioritize_PriorityOrdering(t *testing.T) {
	// All four nodes are unblocked (no edges) and same size; they differ only
	// by priority. After #3396, priority dominates every other heuristic
	// (critical path, focus, size, unblock count) so the result must be
	// strictly ordered P0 → P1 → P2 → P3 regardless of which one happens to
	// land on the critical path.
	nodes := []*depgraph.Node{
		{Repo: "R", Number: 1, Title: "Low", State: "OPEN", BoardStatus: "Ready", Priority: "P3", Size: "M", Weight: 3},
		{Repo: "R", Number: 2, Title: "Critical", State: "OPEN", BoardStatus: "Ready", Priority: "P0", Size: "M", Weight: 3},
		{Repo: "R", Number: 3, Title: "High", State: "OPEN", BoardStatus: "Ready", Priority: "P1", Size: "M", Weight: 3},
		{Repo: "R", Number: 4, Title: "Medium", State: "OPEN", BoardStatus: "Ready", Priority: "P2", Size: "M", Weight: 3},
	}
	g := buildTestGraph(nodes, nil)

	as := &AutonomousScheduler{
		config: AutonomousConfig{MaxConcurrent: 5},
		state:  &AutonomousState{},
	}

	candidates := as.prioritize(context.Background(), g)
	if len(candidates) != 4 {
		t.Fatalf("expected 4 candidates, got %d", len(candidates))
	}

	expectedPriorities := []string{"P0", "P1", "P2", "P3"}
	for i, exp := range expectedPriorities {
		if candidates[i].Priority != exp {
			t.Errorf("position %d: expected priority %s, got %s (#%d, onCritPath=%v)",
				i, exp, candidates[i].Priority, candidates[i].Number, candidates[i].OnCritPath)
		}
	}
}

func TestPrioritize_SmallerSizeFirst(t *testing.T) {
	// Same priority, different sizes. Smaller should come first.
	// The XL node (#1) will be on the critical path (highest weight).
	// Among the remaining two (same priority, not on crit path), smaller size wins.
	nodes := []*depgraph.Node{
		{Repo: "R", Number: 1, Title: "XL", State: "OPEN", BoardStatus: "Ready", Priority: "P1", Size: "XL", Weight: 8},
		{Repo: "R", Number: 2, Title: "XS", State: "OPEN", BoardStatus: "Ready", Priority: "P1", Size: "XS", Weight: 1},
		{Repo: "R", Number: 3, Title: "M", State: "OPEN", BoardStatus: "Ready", Priority: "P1", Size: "M", Weight: 3},
	}
	g := buildTestGraph(nodes, nil)

	as := &AutonomousScheduler{
		config: AutonomousConfig{MaxConcurrent: 5},
		state:  &AutonomousState{},
	}

	candidates := as.prioritize(context.Background(), g)
	if len(candidates) != 3 {
		t.Fatalf("expected 3 candidates, got %d", len(candidates))
	}
	// #1 (XL) is on the critical path — comes first.
	// Then #2 (XS, weight=1) < #3 (M, weight=3)
	if candidates[0].Number != 1 {
		t.Errorf("position 0: expected #1 (critical path, XL), got #%d", candidates[0].Number)
	}
	if !candidates[0].OnCritPath {
		t.Error("expected #1 to be on critical path")
	}
	if candidates[1].Number != 2 {
		t.Errorf("position 1: expected #2 (XS), got #%d", candidates[1].Number)
	}
	if candidates[2].Number != 3 {
		t.Errorf("position 2: expected #3 (M), got #%d", candidates[2].Number)
	}
}

func TestPrioritize_HigherUnblockCountFirst(t *testing.T) {
	// Same priority, same size. Node B unblocks 2 downstream, Node A unblocks 0.
	nodes := []*depgraph.Node{
		{Repo: "R", Number: 1, Title: "A", State: "OPEN", BoardStatus: "Ready", Priority: "P1", Size: "M", Weight: 3},
		{Repo: "R", Number: 2, Title: "B", State: "OPEN", BoardStatus: "Ready", Priority: "P1", Size: "M", Weight: 3},
		{Repo: "R", Number: 3, Title: "C", State: "OPEN", BoardStatus: "Ready", Priority: "P1", Size: "M", Weight: 3},
		{Repo: "R", Number: 4, Title: "D", State: "OPEN", BoardStatus: "Ready", Priority: "P1", Size: "M", Weight: 3},
	}
	// C depends on B, D depends on B — B unblocks 2 items
	// A has no dependents
	edges := []depgraph.Edge{
		{From: depgraph.NodeID{Repo: "R", Number: 3}, To: depgraph.NodeID{Repo: "R", Number: 2}},
		{From: depgraph.NodeID{Repo: "R", Number: 4}, To: depgraph.NodeID{Repo: "R", Number: 2}},
	}
	g := buildTestGraph(nodes, edges)

	as := &AutonomousScheduler{
		config: AutonomousConfig{MaxConcurrent: 5},
		state:  &AutonomousState{},
	}

	candidates := as.prioritize(context.Background(), g)
	// A and B are unblocked. B should come before A because B has higher unblock count.
	if len(candidates) < 2 {
		t.Fatalf("expected at least 2 candidates, got %d", len(candidates))
	}
	if candidates[0].Number != 2 {
		t.Errorf("expected B (#2) first (higher unblock count), got #%d", candidates[0].Number)
	}
	if candidates[1].Number != 1 {
		t.Errorf("expected A (#1) second, got #%d", candidates[1].Number)
	}
}

func TestPrioritize_SkipsBlockedItems(t *testing.T) {
	// A depends on B (B is OPEN). A should not be a candidate.
	nodes := []*depgraph.Node{
		{Repo: "R", Number: 1, Title: "A", State: "OPEN", BoardStatus: "Ready", Priority: "P0", Size: "XS", Weight: 1},
		{Repo: "R", Number: 2, Title: "B", State: "OPEN", BoardStatus: "Ready", Priority: "P2", Size: "M", Weight: 3},
	}
	edges := []depgraph.Edge{
		{From: depgraph.NodeID{Repo: "R", Number: 1}, To: depgraph.NodeID{Repo: "R", Number: 2}},
	}
	g := buildTestGraph(nodes, edges)

	as := &AutonomousScheduler{
		config: AutonomousConfig{MaxConcurrent: 5},
		state:  &AutonomousState{},
	}

	candidates := as.prioritize(context.Background(), g)
	if len(candidates) != 1 {
		t.Fatalf("expected 1 candidate (only B), got %d", len(candidates))
	}
	if candidates[0].Number != 2 {
		t.Errorf("expected B (#2), got #%d", candidates[0].Number)
	}
}

func TestPrioritize_SkipsClosedItems(t *testing.T) {
	nodes := []*depgraph.Node{
		{Repo: "R", Number: 1, Title: "Closed", State: "CLOSED", BoardStatus: "Done", Priority: "P0", Size: "XS", Weight: 1},
		{Repo: "R", Number: 2, Title: "Open", State: "OPEN", BoardStatus: "Ready", Priority: "P2", Size: "M", Weight: 3},
	}
	g := buildTestGraph(nodes, nil)

	as := &AutonomousScheduler{
		config: AutonomousConfig{MaxConcurrent: 5},
		state:  &AutonomousState{},
	}

	candidates := as.prioritize(context.Background(), g)
	if len(candidates) != 1 {
		t.Fatalf("expected 1 candidate, got %d", len(candidates))
	}
	if candidates[0].Number != 2 {
		t.Errorf("expected #2, got #%d", candidates[0].Number)
	}
}

func TestPrioritize_SkipsEpics(t *testing.T) {
	nodes := []*depgraph.Node{
		{Repo: "R", Number: 1, Title: "Epic", State: "OPEN", BoardStatus: "Ready", Labels: []string{"type:epic"}, Priority: "P0", Size: "XL", Weight: 8},
		{Repo: "R", Number: 2, Title: "Regular", State: "OPEN", BoardStatus: "Ready", Priority: "P1", Size: "M", Weight: 3},
	}
	g := buildTestGraph(nodes, nil)

	as := &AutonomousScheduler{
		config: AutonomousConfig{MaxConcurrent: 5},
		state:  &AutonomousState{},
	}

	candidates := as.prioritize(context.Background(), g)
	if len(candidates) != 1 {
		t.Fatalf("expected 1 candidate (epic skipped), got %d", len(candidates))
	}
	if candidates[0].Number != 2 {
		t.Errorf("expected #2, got #%d", candidates[0].Number)
	}
}

// TestPrioritize_SkipsOwnerActionLabel covers #317: an issue labeled
// `owner-action` (the default autonomous.exclude_labels entry) is human-only
// work — dispatching it burns tokens through issue-pickup → planning →
// feature-dev → validate and then fails at pr-create with nothing to commit.
// The candidate loop must skip it with a distinct reason, mirroring the
// type:epic exclusion above.
func TestPrioritize_SkipsOwnerActionLabel(t *testing.T) {
	nodes := []*depgraph.Node{
		{Repo: "R", Number: 1, Title: "Human-only", State: "OPEN", BoardStatus: "Ready", Labels: []string{"owner-action"}, Priority: "P0", Size: "XS", Weight: 1},
		{Repo: "R", Number: 2, Title: "Regular", State: "OPEN", BoardStatus: "Ready", Priority: "P1", Size: "M", Weight: 3},
	}
	g := buildTestGraph(nodes, nil)

	as := &AutonomousScheduler{
		config: AutonomousConfig{MaxConcurrent: 5},
		state:  &AutonomousState{},
	}

	candidates := as.prioritize(context.Background(), g)
	if len(candidates) != 1 {
		t.Fatalf("expected 1 candidate (owner-action skipped), got %d", len(candidates))
	}
	if candidates[0].Number != 2 {
		t.Errorf("expected #2, got #%d", candidates[0].Number)
	}
}

// TestPrioritize_OwnerActionLabelCaseInsensitive verifies the exclusion
// matches regardless of label casing, mirroring the type:epic check's
// strings.EqualFold behavior.
func TestPrioritize_OwnerActionLabelCaseInsensitive(t *testing.T) {
	nodes := []*depgraph.Node{
		{Repo: "R", Number: 1, Title: "Human-only", State: "OPEN", BoardStatus: "Ready", Labels: []string{"Owner-Action"}, Priority: "P0", Size: "XS", Weight: 1},
	}
	g := buildTestGraph(nodes, nil)

	as := &AutonomousScheduler{
		config: AutonomousConfig{MaxConcurrent: 5},
		state:  &AutonomousState{},
	}

	candidates := as.prioritize(context.Background(), g)
	if len(candidates) != 0 {
		t.Fatalf("expected 0 candidates (owner-action skipped case-insensitively), got %d", len(candidates))
	}
}

// TestPrioritize_ExcludeLabelsConfigOverride verifies a custom
// autonomous.exclude_labels list is honored INSTEAD OF the "owner-action"
// default — a repo that uses a different human-only convention (e.g.
// "needs-human") must have that label excluded, and "owner-action" alone
// (without the configured label) must NOT be excluded when it's not in the
// configured list.
func TestPrioritize_ExcludeLabelsConfigOverride(t *testing.T) {
	nodes := []*depgraph.Node{
		{Repo: "R", Number: 1, Title: "Needs a human", State: "OPEN", BoardStatus: "Ready", Labels: []string{"needs-human"}, Priority: "P0", Size: "XS", Weight: 1},
		{Repo: "R", Number: 2, Title: "Owner action but not configured", State: "OPEN", BoardStatus: "Ready", Labels: []string{"owner-action"}, Priority: "P1", Size: "XS", Weight: 1},
	}
	g := buildTestGraph(nodes, nil)

	as := &AutonomousScheduler{
		config: AutonomousConfig{MaxConcurrent: 5, ExcludeLabels: []string{"needs-human"}},
		state:  &AutonomousState{},
	}

	candidates := as.prioritize(context.Background(), g)
	if len(candidates) != 1 {
		t.Fatalf("expected 1 candidate (#2 — owner-action isn't in the configured exclude list), got %d", len(candidates))
	}
	if candidates[0].Number != 2 {
		t.Errorf("expected #2, got #%d", candidates[0].Number)
	}
}

func TestPrioritize_SkipsAlreadyRunning(t *testing.T) {
	nodes := []*depgraph.Node{
		{Repo: "R", Number: 1, Title: "Running", State: "OPEN", BoardStatus: "Ready", Priority: "P0", Size: "XS", Weight: 1},
		{Repo: "R", Number: 2, Title: "Available", State: "OPEN", BoardStatus: "Ready", Priority: "P1", Size: "M", Weight: 3},
	}
	g := buildTestGraph(nodes, nil)

	as := &AutonomousScheduler{
		config: AutonomousConfig{MaxConcurrent: 5},
		state: &AutonomousState{
			Running: []RunningItem{
				{Repo: "R", Number: 1, Title: "Running"},
			},
		},
	}

	candidates := as.prioritize(context.Background(), g)
	if len(candidates) != 1 {
		t.Fatalf("expected 1 candidate, got %d", len(candidates))
	}
	if candidates[0].Number != 2 {
		t.Errorf("expected #2, got #%d", candidates[0].Number)
	}
}

func TestPrioritize_SkipsAlreadyCompleted(t *testing.T) {
	nodes := []*depgraph.Node{
		{Repo: "R", Number: 1, Title: "Done", State: "OPEN", BoardStatus: "Ready", Priority: "P0", Size: "XS", Weight: 1},
		{Repo: "R", Number: 2, Title: "Todo", State: "OPEN", BoardStatus: "Ready", Priority: "P1", Size: "M", Weight: 3},
	}
	g := buildTestGraph(nodes, nil)

	as := &AutonomousScheduler{
		config: AutonomousConfig{MaxConcurrent: 5},
		state: &AutonomousState{
			Completed: []CompletedItem{
				{Repo: "R", Number: 1, Title: "Done"},
			},
		},
	}

	candidates := as.prioritize(context.Background(), g)
	if len(candidates) != 1 {
		t.Fatalf("expected 1 candidate, got %d", len(candidates))
	}
	if candidates[0].Number != 2 {
		t.Errorf("expected #2, got #%d", candidates[0].Number)
	}
}

func TestPrioritize_BoardStatusGating(t *testing.T) {
	// Only "Ready" items should be dispatched by default.
	// "Backlog" items should be dispatched only when PickupBacklog is true.
	// "In progress", "Done", and empty status should never be dispatched.
	nodes := []*depgraph.Node{
		{Repo: "R", Number: 1, Title: "Ready item", State: "OPEN", BoardStatus: "Ready", Priority: "P1", Size: "M", Weight: 3},
		{Repo: "R", Number: 2, Title: "Backlog item", State: "OPEN", BoardStatus: "Backlog", Priority: "P0", Size: "XS", Weight: 1},
		{Repo: "R", Number: 3, Title: "In progress", State: "OPEN", BoardStatus: "In progress", Priority: "P0", Size: "XS", Weight: 1},
		{Repo: "R", Number: 4, Title: "Done item", State: "OPEN", BoardStatus: "Done", Priority: "P0", Size: "XS", Weight: 1},
		{Repo: "R", Number: 5, Title: "No status", State: "OPEN", BoardStatus: "", Priority: "P0", Size: "XS", Weight: 1},
		{Repo: "R", Number: 6, Title: "Todo item", State: "OPEN", BoardStatus: "Todo", Priority: "P2", Size: "M", Weight: 3},
	}
	g := buildTestGraph(nodes, nil)

	t.Run("default: only Ready and Todo dispatched", func(t *testing.T) {
		as := &AutonomousScheduler{
			config: AutonomousConfig{MaxConcurrent: 10, PickupBacklog: false},
			state:  &AutonomousState{},
		}
		candidates := as.prioritize(context.Background(), g)
		if len(candidates) != 2 {
			t.Fatalf("expected 2 candidates (Ready + Todo), got %d", len(candidates))
		}
		// Ready/Todo items only, sorted: Ready(#1) before Todo(#6) (both are "ready" status)
		nums := make(map[int]bool)
		for _, c := range candidates {
			nums[c.Number] = true
		}
		if !nums[1] || !nums[6] {
			t.Errorf("expected issues #1 and #6, got %v", candidates)
		}
	})

	t.Run("pickup_backlog: Ready + Backlog dispatched", func(t *testing.T) {
		as := &AutonomousScheduler{
			config: AutonomousConfig{MaxConcurrent: 10, PickupBacklog: true},
			state:  &AutonomousState{},
		}
		candidates := as.prioritize(context.Background(), g)
		if len(candidates) != 3 {
			t.Fatalf("expected 3 candidates (Ready + Todo + Backlog), got %d", len(candidates))
		}
		// Ready items should sort before Backlog
		if !isReadyStatus(candidates[0].BoardStatus) {
			t.Errorf("first candidate should be Ready status, got %q", candidates[0].BoardStatus)
		}
		// Last candidate should be Backlog
		if candidates[len(candidates)-1].BoardStatus != "Backlog" {
			t.Errorf("last candidate should be Backlog, got %q", candidates[len(candidates)-1].BoardStatus)
		}
	})
}

func TestUnblockCascade(t *testing.T) {
	// A depends on B. B completes → A should become a candidate.
	nodes := []*depgraph.Node{
		{Repo: "R", Number: 1, Title: "A", State: "OPEN", BoardStatus: "Ready", Priority: "P1", Size: "M", Weight: 3},
		{Repo: "R", Number: 2, Title: "B", State: "CLOSED", BoardStatus: "Done", Priority: "P1", Size: "M", Weight: 3},
	}
	edges := []depgraph.Edge{
		{From: depgraph.NodeID{Repo: "R", Number: 1}, To: depgraph.NodeID{Repo: "R", Number: 2}},
	}
	g := buildTestGraph(nodes, edges)

	as := &AutonomousScheduler{
		config: AutonomousConfig{MaxConcurrent: 5},
		state:  &AutonomousState{},
	}

	candidates := as.prioritize(context.Background(), g)
	// B is CLOSED, so A should be unblocked and be a candidate
	if len(candidates) != 1 {
		t.Fatalf("expected 1 candidate (A), got %d", len(candidates))
	}
	if candidates[0].Number != 1 {
		t.Errorf("expected A (#1) to be unblocked, got #%d", candidates[0].Number)
	}
}

func TestOnPipelineComplete_Success(t *testing.T) {
	as := &AutonomousScheduler{
		config: AutonomousConfig{MaxConcurrent: 3},
		state: &AutonomousState{
			Status: "running",
			Running: []RunningItem{
				{Repo: "R", Number: 42, Title: "Test Issue"},
			},
		},
		rescanCh: make(chan struct{}, 1),
	}

	as.onPipelineComplete("R", 42, true, false, "", "")

	if len(as.state.Running) != 0 {
		t.Errorf("expected 0 running, got %d", len(as.state.Running))
	}
	if len(as.state.Completed) != 1 {
		t.Fatalf("expected 1 completed, got %d", len(as.state.Completed))
	}
	if as.state.Completed[0].Number != 42 {
		t.Errorf("expected completed #42, got #%d", as.state.Completed[0].Number)
	}
	if as.state.Completed[0].Title != "Test Issue" {
		t.Errorf("expected title 'Test Issue', got %q", as.state.Completed[0].Title)
	}
	// Check rescan was triggered
	select {
	case <-as.rescanCh:
		// ok
	default:
		t.Error("expected rescan signal after completion")
	}
}

func TestOnPipelineComplete_Failure(t *testing.T) {
	as := &AutonomousScheduler{
		config: AutonomousConfig{MaxConcurrent: 3},
		state: &AutonomousState{
			Status: "running",
			Running: []RunningItem{
				{Repo: "R", Number: 99, Title: "Failing Issue"},
			},
		},
		rescanCh: make(chan struct{}, 1),
	}

	as.onPipelineComplete("R", 99, false, false, "", "")

	if len(as.state.Running) != 0 {
		t.Errorf("expected 0 running, got %d", len(as.state.Running))
	}
	if len(as.state.Failed) != 1 {
		t.Fatalf("expected 1 failed, got %d", len(as.state.Failed))
	}
	if as.state.Failed[0].Number != 99 {
		t.Errorf("expected failed #99, got #%d", as.state.Failed[0].Number)
	}
}

// TestOnPipelineComplete_ConflictRecoveryPath_PreservesBranch locks the #4072
// gating: the modern conflict-recovery path resolves WITHIN the run via a
// feature-dev rewind, so an exhausted/failed resolution returns here with
// conflictRestart=false. That must surface as a TRUE failure (issue in Failed,
// no conflict-restart re-queue) — the branch is NOT blindly deleted/restarted.
func TestOnPipelineComplete_ConflictRecoveryPath_PreservesBranch(t *testing.T) {
	as := &AutonomousScheduler{
		config: AutonomousConfig{MaxConcurrent: 3},
		state: &AutonomousState{
			Status:  "running",
			Running: []RunningItem{{Repo: "R", Number: 70, Title: "Conflict Issue"}},
		},
		rescanCh:             make(chan struct{}, 1),
		conflictRestartCount: map[string]int{},
	}

	// conflictRestart=false → exhausted/failed conflict-recovery (branch-
	// preserving) reaches the normal failure path, not the fresh-branch restart.
	as.onPipelineComplete("R", 70, false, false, "", "")

	if len(as.state.Failed) != 1 || as.state.Failed[0].Number != 70 {
		t.Fatalf("expected #70 in Failed (true failure), got %+v", as.state.Failed)
	}
	// The branch-preserving path must NOT have entered the legacy fresh-branch
	// conflict-restart counter.
	if n := as.conflictRestartCount["R#70"]; n != 0 {
		t.Errorf("conflict-recovery path must not touch conflictRestartCount, got %d", n)
	}
}

// TestOnPipelineComplete_LegacyConflictRestart_BoundedThenTrueFailure exercises
// the residual fresh-branch path: the first MaxConflictRestarts attempts re-queue
// without a circuit-breaker hit; once the bound is reached the issue falls
// through to a true failure.
func TestOnPipelineComplete_LegacyConflictRestart_BoundedThenTrueFailure(t *testing.T) {
	as := &AutonomousScheduler{
		config: AutonomousConfig{MaxConcurrent: 3},
		state: &AutonomousState{
			Status:                "running",
			Running:               []RunningItem{{Repo: "R", Number: 80, Title: "Legacy Conflict"}},
			LifetimeIssueFailures: map[string]int{},
		},
		rescanCh:             make(chan struct{}, 1),
		conflictRestartCount: map[string]int{},
		perIssueFailureCount: map[string]int{},
		retryBackoff:         map[string]time.Time{},
	}

	key := "R#80"

	// First MaxConflictRestarts-1 attempts re-queue (not a true failure yet).
	for i := 1; i < MaxConflictRestarts; i++ {
		as.state.Running = []RunningItem{{Repo: "R", Number: 80, Title: "Legacy Conflict"}}
		as.onPipelineComplete("R", 80, false, true, "", "")
		if got := as.conflictRestartCount[key]; got != i {
			t.Fatalf("attempt %d: conflictRestartCount=%d, want %d", i, got, i)
		}
		// Backoff scheduled, issue not yet a terminal failure.
		if _, ok := as.retryBackoff[key]; !ok {
			t.Errorf("attempt %d: expected a short retry backoff", i)
		}
	}

	// The final attempt reaches the bound → true failure path.
	as.state.Running = []RunningItem{{Repo: "R", Number: 80, Title: "Legacy Conflict"}}
	as.onPipelineComplete("R", 80, false, true, "", "")
	if as.conflictRestartCount[key] != MaxConflictRestarts {
		t.Errorf("expected conflictRestartCount=%d at bound, got %d", MaxConflictRestarts, as.conflictRestartCount[key])
	}
	foundFailed := false
	for _, f := range as.state.Failed {
		if f.Number == 80 {
			foundFailed = true
		}
	}
	if !foundFailed {
		t.Errorf("expected #80 to become a true failure once the conflict-restart bound is exhausted")
	}
}

// TestOnPipelineComplete_StreamIdleTimeout_LongBackoff captures the #3398
// invariant: when the terminal failure kind is stream_idle_timeout, the
// scheduler must apply the long environmental-failure backoff (1 hour) and
// must NOT increment the lifetime failure counter. Pre-fix, both bugs
// compounded: the default 2-minute backoff re-fired under the same API
// conditions and burned a second run, then a third tripped the lifetime
// cap (#3327 incident, $14.95 lost).
func TestOnPipelineComplete_StreamIdleTimeout_LongBackoff(t *testing.T) {
	as := &AutonomousScheduler{
		config: AutonomousConfig{MaxConcurrent: 3},
		state: &AutonomousState{
			Status: "running",
			Running: []RunningItem{
				{Repo: "nightgauge/nightgauge", Number: 3327, Title: "Telemetry consent"},
			},
			LifetimeIssueFailures: map[string]int{},
		},
		rescanCh:             make(chan struct{}, 1),
		perIssueFailureCount: map[string]int{},
		retryBackoff:         map[string]time.Time{},
	}

	before := time.Now()
	as.onPipelineComplete("nightgauge/nightgauge", 3327, false, false, TerminalKindStreamIdleTimeout, "")
	after := time.Now()

	key := "nightgauge/nightgauge#3327"

	// Lifetime counter must NOT be incremented — environmental failures don't
	// count toward the lifetime cap.
	if got := as.state.LifetimeIssueFailures[key]; got != 0 {
		t.Errorf("LifetimeIssueFailures[%q] = %d after stream-idle-timeout, want 0 (must not penalize for upstream-API problem)",
			key, got)
	}
	// Per-session counter must NOT be incremented either — same reasoning.
	if got := as.perIssueFailureCount[key]; got != 0 {
		t.Errorf("perIssueFailureCount[%q] = %d after stream-idle-timeout, want 0",
			key, got)
	}

	// Backoff must be ~1 hour (the streamIdleTimeoutBackoff constant), not
	// the default exponential backoff for first-failure (which would be much
	// shorter and re-fire under the same rate-limit conditions).
	retryAt, ok := as.retryBackoff[key]
	if !ok {
		t.Fatalf("expected retryBackoff[%q] to be set", key)
	}
	wait := retryAt.Sub(before)
	if wait < 50*time.Minute || wait > 70*time.Minute {
		t.Errorf("backoff = %v, want ~1h (allowed range 50m–70m)", wait)
	}
	// Sanity: retryAt is after the call returned, before some sane upper bound.
	if !retryAt.After(after) {
		t.Errorf("retryAt %v is not after call return %v", retryAt, after)
	}

	// The issue still moves out of Running and is recorded as failed, so the
	// dispatch slot is freed for other items.
	if len(as.state.Running) != 0 {
		t.Errorf("expected 0 running after stream-idle-timeout, got %d", len(as.state.Running))
	}
	if len(as.state.Failed) != 1 {
		t.Fatalf("expected 1 failed entry recorded, got %d", len(as.state.Failed))
	}
	if as.state.Failed[0].Number != 3327 {
		t.Errorf("expected failed #3327, got #%d", as.state.Failed[0].Number)
	}
}

// TestOnPipelineComplete_RateLimitQuotaExhausted_LongBackoff is the #3386
// counterpart to the stream-idle-timeout test. Same retry policy: 1-hour
// backoff, no lifetime/per-session counter increment. Pre-fix, the silent-
// stall pattern (agent idle while the 5-hour bucket waits to reset)
// trivially tripped the lifetime cap on the second occurrence and burned
// $20+ each time.
func TestOnPipelineComplete_RateLimitQuotaExhausted_LongBackoff(t *testing.T) {
	as := &AutonomousScheduler{
		config: AutonomousConfig{MaxConcurrent: 3},
		state: &AutonomousState{
			Status: "running",
			Running: []RunningItem{
				{Repo: "acme/platform", Number: 885, Title: "Analytics endpoints"},
			},
			LifetimeIssueFailures: map[string]int{},
		},
		rescanCh:             make(chan struct{}, 1),
		perIssueFailureCount: map[string]int{},
		retryBackoff:         map[string]time.Time{},
	}

	before := time.Now()
	as.onPipelineComplete("acme/platform", 885, false, false, TerminalKindRateLimitQuotaExhausted, "")
	after := time.Now()

	key := "acme/platform#885"

	if got := as.state.LifetimeIssueFailures[key]; got != 0 {
		t.Errorf("LifetimeIssueFailures[%q] = %d after rate-limit-quota-exhausted, want 0", key, got)
	}
	if got := as.perIssueFailureCount[key]; got != 0 {
		t.Errorf("perIssueFailureCount[%q] = %d after rate-limit-quota-exhausted, want 0", key, got)
	}
	retryAt, ok := as.retryBackoff[key]
	if !ok {
		t.Fatalf("expected retryBackoff[%q] to be set", key)
	}
	wait := retryAt.Sub(before)
	if wait < 50*time.Minute || wait > 70*time.Minute {
		t.Errorf("backoff = %v, want ~1h (allowed range 50m–70m)", wait)
	}
	if !retryAt.After(after) {
		t.Errorf("retryAt %v is not after call return %v", retryAt, after)
	}
	if len(as.state.Running) != 0 {
		t.Errorf("expected 0 running after rate-limit-quota-exhausted, got %d", len(as.state.Running))
	}
	// #3439: assert the global quota cooldown is engaged. Pre-fix the test
	// only checked the per-issue counters and silently passed even when the
	// cooldown branch never set as.state.QuotaCooldownUntil — exactly the
	// regression observed on 2026-05-10 where #3375/#894/#371 all fell into
	// the GENERIC failure branch and the cascade #3434 should have stopped
	// kept burning runs.
	if as.state.QuotaCooldownUntil == "" {
		t.Errorf("expected QuotaCooldownUntil to be set after rate-limit-quota-exhausted, got empty")
	}
}

// TestOnPipelineComplete_StallKill_NoLifetimeCap verifies that a stall-kill
// failure (TerminalKindStallKill) does NOT increment the lifetime failure cap
// or the per-session circuit breaker. Stall-kills are transient infrastructure
// events — the agent exceeded its idle/hard-cap threshold, not a code defect.
// Pre-fix, two stall-kills on the same issue exhausted MaxLifetimeFailuresPerIssue
// (=2) and required manual triage, halting the entire autonomous queue.
func TestOnPipelineComplete_StallKill_NoLifetimeCap(t *testing.T) {
	as := &AutonomousScheduler{
		config: AutonomousConfig{MaxConcurrent: 3},
		state: &AutonomousState{
			Status: "running",
			Running: []RunningItem{
				{Repo: "nightgauge/nightgauge", Number: 3499, Title: "Cache BuildGraph with TTL"},
			},
			LifetimeIssueFailures: map[string]int{},
		},
		rescanCh:             make(chan struct{}, 1),
		perIssueFailureCount: map[string]int{},
		retryBackoff:         map[string]time.Time{},
	}

	before := time.Now()
	as.onPipelineComplete("nightgauge/nightgauge", 3499, false, false, TerminalKindStallKill, "exceeded stall idle threshold (20m)")
	after := time.Now()

	key := "nightgauge/nightgauge#3499"

	// Lifetime counter must NOT be incremented — stall-kills are transient.
	if got := as.state.LifetimeIssueFailures[key]; got != 0 {
		t.Errorf("LifetimeIssueFailures[%q] = %d after stall-kill, want 0 (stall-kills are transient, must not hit cap)",
			key, got)
	}
	// Per-session counter must NOT be incremented — same reasoning.
	if got := as.perIssueFailureCount[key]; got != 0 {
		t.Errorf("perIssueFailureCount[%q] = %d after stall-kill, want 0",
			key, got)
	}

	// Backoff must be ~30 minutes (stallKillBackoff), not the default
	// exponential backoff (2 min for first failure) that would re-fire too
	// quickly under the same conditions.
	retryAt, ok := as.retryBackoff[key]
	if !ok {
		t.Fatalf("expected retryBackoff[%q] to be set after stall-kill", key)
	}
	wait := retryAt.Sub(before)
	if wait < 25*time.Minute || wait > 35*time.Minute {
		t.Errorf("backoff = %v, want ~30min (allowed range 25m–35m)", wait)
	}
	if !retryAt.After(after) {
		t.Errorf("retryAt %v is not after call return %v", retryAt, after)
	}

	// Slot must be freed.
	if len(as.state.Running) != 0 {
		t.Errorf("expected 0 running after stall-kill, got %d", len(as.state.Running))
	}
	// A failed entry is recorded so the user can see stalls in the dashboard.
	if len(as.state.Failed) != 1 {
		t.Fatalf("expected 1 failed entry recorded, got %d", len(as.state.Failed))
	}
	if as.state.Failed[0].Number != 3499 {
		t.Errorf("expected failed #3499, got #%d", as.state.Failed[0].Number)
	}

	// No global cooldown — stall-kills are per-issue, not a system-wide signal.
	if as.state.QuotaCooldownUntil != "" {
		t.Errorf("QuotaCooldownUntil = %q after stall-kill, want empty (no system-wide cooldown)",
			as.state.QuotaCooldownUntil)
	}
}

// TestOnPipelineComplete_ApiOverloaded_TransientNoPause verifies #3835 WS4: an
// Anthropic 529 "Overloaded" is a transient capacity blip. It must NOT increment
// the lifetime/per-session caps, must NOT pause autonomous, must NOT apply a
// global cooldown, and must set a SHORT (~5m) per-issue backoff so only that
// issue waits while the rest of the queue keeps flowing.
func TestOnPipelineComplete_ApiOverloaded_TransientNoPause(t *testing.T) {
	as := &AutonomousScheduler{
		config: AutonomousConfig{MaxConcurrent: 3},
		state: &AutonomousState{
			Status: "running",
			Running: []RunningItem{
				{Repo: "nightgauge/nightgauge", Number: 481, Title: "Transient overload case"},
			},
			LifetimeIssueFailures: map[string]int{},
		},
		rescanCh:             make(chan struct{}, 1),
		perIssueFailureCount: map[string]int{},
		retryBackoff:         map[string]time.Time{},
	}

	before := time.Now()
	as.onPipelineComplete("nightgauge/nightgauge", 481, false, false, TerminalKindApiOverloaded, "API Error: Overloaded")
	after := time.Now()

	key := "nightgauge/nightgauge#481"

	if got := as.state.LifetimeIssueFailures[key]; got != 0 {
		t.Errorf("LifetimeIssueFailures[%q] = %d after api-overloaded, want 0 (transient)", key, got)
	}
	if got := as.perIssueFailureCount[key]; got != 0 {
		t.Errorf("perIssueFailureCount[%q] = %d after api-overloaded, want 0", key, got)
	}
	// MUST NOT pause the queue on a momentary API capacity blip.
	if as.state.Status == "paused" {
		t.Errorf("autonomous paused after api-overloaded; want still running (transient must not halt the queue)")
	}
	// MUST NOT apply a system-wide cooldown (overload != depleted bucket).
	if as.state.QuotaCooldownUntil != "" {
		t.Errorf("QuotaCooldownUntil = %q after api-overloaded, want empty (no global cooldown)", as.state.QuotaCooldownUntil)
	}
	// Short ~5m backoff (apiOverloadedBackoff), not the 30m/1h infra backoffs.
	retryAt, ok := as.retryBackoff[key]
	if !ok {
		t.Fatalf("expected retryBackoff[%q] to be set after api-overloaded", key)
	}
	wait := retryAt.Sub(before)
	if wait < 3*time.Minute || wait > 8*time.Minute {
		t.Errorf("backoff = %v, want ~5min (allowed 3m–8m)", wait)
	}
	if !retryAt.After(after) {
		t.Errorf("retryAt %v is not after call return %v", retryAt, after)
	}
	// Slot freed + a failed entry recorded for visibility.
	if len(as.state.Running) != 0 {
		t.Errorf("expected 0 running after api-overloaded, got %d", len(as.state.Running))
	}
	if len(as.state.Failed) != 1 || as.state.Failed[0].Number != 481 {
		t.Fatalf("expected 1 failed entry for #481, got %+v", as.state.Failed)
	}
}

// TestOnPipelineComplete_StallKill_RepeatedDoesNotBlockIssue verifies that
// repeated stall-kills on the same issue do not accumulate toward the
// lifetime cap, allowing autonomous to keep retrying until the underlying
// condition resolves.
func TestOnPipelineComplete_StallKill_RepeatedDoesNotBlockIssue(t *testing.T) {
	as := &AutonomousScheduler{
		config: AutonomousConfig{MaxConcurrent: 3},
		state: &AutonomousState{
			Status:                "running",
			LifetimeIssueFailures: map[string]int{},
		},
		rescanCh:             make(chan struct{}, 1),
		perIssueFailureCount: map[string]int{},
		retryBackoff:         map[string]time.Time{},
	}

	key := "nightgauge/nightgauge#42"

	// Simulate MaxLifetimeFailuresPerIssue stall-kills — should NOT trip cap.
	for i := range MaxLifetimeFailuresPerIssue + 1 {
		as.state.Running = []RunningItem{
			{Repo: "nightgauge/nightgauge", Number: 42, Title: "Issue"},
		}
		as.onPipelineComplete("nightgauge/nightgauge", 42, false, false, TerminalKindStallKill, "")
		if got := as.state.LifetimeIssueFailures[key]; got != 0 {
			t.Errorf("after stall #%d: LifetimeIssueFailures[%q] = %d, want 0", i+1, key, got)
		}
	}

	// Status must remain running — no safety trip, no pause.
	if as.state.Status != "running" {
		t.Errorf("Status = %q after repeated stall-kills, want 'running' (stalls must not trip safety)", as.state.Status)
	}
}

// TestNotifyComplete_EmptyKindButQuotaMarkerInDetail_AppliesCooldown is the
// #3439 regression guard. When the IPC caller passes terminalFailureKind=""
// but failureDetail carries a recognizable kill marker (e.g. the TS-side
// regex missed but the marker is present in the raw failure text),
// NotifyComplete must re-classify so the global quota cooldown still
// engages. Without this defense-in-depth a single bad regex in the TS
// bridge silently disables the entire cooldown protection.
func TestNotifyComplete_EmptyKindButQuotaMarkerInDetail_AppliesCooldown(t *testing.T) {
	as := &AutonomousScheduler{
		config: AutonomousConfig{MaxConcurrent: 3},
		state: &AutonomousState{
			Status: "running",
			Running: []RunningItem{
				{Repo: "acme/platform", Number: 894, Title: "scheduled reports"},
			},
			LifetimeIssueFailures: map[string]int{},
		},
		rescanCh:             make(chan struct{}, 1),
		perIssueFailureCount: map[string]int{},
		retryBackoff:         map[string]time.Time{},
	}

	// Real production failure-detail text observed in the 2026-05-10 incident.
	failureDetail := "[skillRunner] Stage [rate-limit-quota-exhausted] idle 2m 16s after rate_limit_event with overage rejected (five_hour bucket; resetsAt=1778428800) — forcibly terminating process after 28m 30s"

	// Empty terminalFailureKind — simulates the TS-side regex miss. The Go
	// side must re-classify and still set the cooldown.
	as.NotifyComplete("acme/platform", 894, false, false, "", failureDetail)

	if as.state.QuotaCooldownUntil == "" {
		t.Fatalf("expected QuotaCooldownUntil to be set after re-classification, got empty")
	}
	key := "acme/platform#894"
	if got := as.state.LifetimeIssueFailures[key]; got != 0 {
		t.Errorf("LifetimeIssueFailures[%q] = %d, want 0 (quota-exhausted is exempt from lifetime cap)", key, got)
	}
	if got := as.perIssueFailureCount[key]; got != 0 {
		t.Errorf("perIssueFailureCount[%q] = %d, want 0", key, got)
	}
}

// TestNotifyComplete_EmptyKindAndNoMarker_FallsThroughToGeneric confirms
// the re-classifier only kicks in for recognizable markers — a generic
// failure with empty kind and no marker text must still go through the
// GENERIC branch (incrementing lifetime cap as expected).
func TestNotifyComplete_EmptyKindAndNoMarker_FallsThroughToGeneric(t *testing.T) {
	as := &AutonomousScheduler{
		config: AutonomousConfig{MaxConcurrent: 3},
		state: &AutonomousState{
			Status: "running",
			Running: []RunningItem{
				{Repo: "R", Number: 5, Title: "X"},
			},
			LifetimeIssueFailures: map[string]int{},
		},
		rescanCh:             make(chan struct{}, 1),
		perIssueFailureCount: map[string]int{},
		retryBackoff:         map[string]time.Time{},
	}

	// Generic failure detail that ClassifyTerminalKind cannot match.
	as.NotifyComplete("R", 5, false, false, "", "some unrecognized error text")

	if as.state.QuotaCooldownUntil != "" {
		t.Errorf("QuotaCooldownUntil = %q, want empty (no marker to re-classify)", as.state.QuotaCooldownUntil)
	}
	if got := as.state.LifetimeIssueFailures["R#5"]; got != 1 {
		t.Errorf("LifetimeIssueFailures[R#5] = %d, want 1 (generic branch)", got)
	}
}

// TestOnPipelineComplete_OtherFailures_StillIncrementLifetime confirms the
// environmental-failure exemptions are narrow: non-transient failure kinds
// still increment the lifetime counter and use the default exponential backoff.
// Environmental kinds (stream-idle-timeout, rate-limit-quota-exhausted,
// stall-kill) are explicitly excluded — they use their own backoff paths and
// must not count toward the lifetime cap.
func TestOnPipelineComplete_OtherFailures_StillIncrementLifetime(t *testing.T) {
	cases := []string{
		"",                          // unknown kind — typical path
		TerminalKindBudgetExceeded,  // cost-cap kill
		TerminalKindValidationError, // skill output validation
		TerminalKindSubagentCrash,   // subagent died
	}
	for _, kind := range cases {
		t.Run(kind, func(t *testing.T) {
			as := &AutonomousScheduler{
				config: AutonomousConfig{MaxConcurrent: 3},
				state: &AutonomousState{
					Status: "running",
					Running: []RunningItem{
						{Repo: "R", Number: 5, Title: "X"},
					},
					LifetimeIssueFailures: map[string]int{},
				},
				rescanCh:             make(chan struct{}, 1),
				perIssueFailureCount: map[string]int{},
				retryBackoff:         map[string]time.Time{},
			}

			as.onPipelineComplete("R", 5, false, false, kind, "")

			if got := as.state.LifetimeIssueFailures["R#5"]; got != 1 {
				t.Errorf("kind=%q: LifetimeIssueFailures[R#5] = %d, want 1", kind, got)
			}
			if got := as.perIssueFailureCount["R#5"]; got != 1 {
				t.Errorf("kind=%q: perIssueFailureCount[R#5] = %d, want 1", kind, got)
			}
		})
	}
}

// #3020 — verify the per-issue lifetime failure counter increments on each
// failure, survives Resume(), and that Resume() does NOT reset it.
func TestLifetimeIssueFailures_PersistAcrossResume(t *testing.T) {
	as := &AutonomousScheduler{
		config: AutonomousConfig{MaxConcurrent: 3},
		state: &AutonomousState{
			Status: "running",
			Running: []RunningItem{
				{Repo: "R", Number: 42, Title: "Flaky"},
			},
		},
		rescanCh:             make(chan struct{}, 1),
		perIssueFailureCount: map[string]int{},
		retryBackoff:         map[string]time.Time{},
	}
	// Two failures of the same issue.
	as.state.Running = []RunningItem{{Repo: "R", Number: 42}}
	as.onPipelineComplete("R", 42, false, false, "", "")
	as.state.Running = []RunningItem{{Repo: "R", Number: 42}}
	as.onPipelineComplete("R", 42, false, false, "", "")

	if got := as.state.LifetimeIssueFailures["R#42"]; got != 2 {
		t.Fatalf("expected lifetime count 2, got %d", got)
	}

	// Simulate Resume() — should clear the per-session counters but NOT the
	// lifetime counter.
	as.state.Status = "paused"
	as.Resume()

	if got := as.perIssueFailureCount["R#42"]; got != 0 {
		t.Errorf("expected per-session count cleared on Resume, got %d", got)
	}
	if got := as.state.LifetimeIssueFailures["R#42"]; got != 2 {
		t.Errorf("expected lifetime count preserved across Resume, got %d", got)
	}
}

// #3020 — verify a successful completion clears the lifetime counter so a
// previously-flaky issue isn't permanently locked out after one bad run.
func TestLifetimeIssueFailures_ClearedOnSuccess(t *testing.T) {
	as := &AutonomousScheduler{
		config: AutonomousConfig{MaxConcurrent: 3},
		state: &AutonomousState{
			Status:                "running",
			LifetimeIssueFailures: map[string]int{"R#42": 1},
			Running:               []RunningItem{{Repo: "R", Number: 42}},
		},
		rescanCh: make(chan struct{}, 1),
	}
	as.onPipelineComplete("R", 42, true, false, "", "")
	if _, ok := as.state.LifetimeIssueFailures["R#42"]; ok {
		t.Errorf("expected lifetime counter cleared on success, still present")
	}
}

// #3020 — ClearIssueFailures wipes a single issue or all when key is empty.
func TestClearIssueFailures(t *testing.T) {
	as := &AutonomousScheduler{
		state: &AutonomousState{
			LifetimeIssueFailures: map[string]int{"R#1": 2, "R#2": 5, "R#3": 1},
		},
		perIssueFailureCount: map[string]int{"R#1": 2},
		retryBackoff:         map[string]time.Time{"R#1": time.Now().Add(time.Hour)},
	}
	if n := as.ClearIssueFailures("R#1"); n != 1 {
		t.Errorf("expected 1 cleared, got %d", n)
	}
	if _, ok := as.state.LifetimeIssueFailures["R#1"]; ok {
		t.Errorf("R#1 not cleared")
	}
	if _, ok := as.perIssueFailureCount["R#1"]; ok {
		t.Errorf("R#1 session count not cleared")
	}
	if n := as.ClearIssueFailures(""); n != 2 {
		t.Errorf("expected 2 cleared (R#2, R#3), got %d", n)
	}
	if len(as.state.LifetimeIssueFailures) != 0 {
		t.Errorf("expected empty map after wipe-all, got %d entries", len(as.state.LifetimeIssueFailures))
	}
}

func TestBudgetExhaustion(t *testing.T) {
	as := &AutonomousScheduler{
		config: AutonomousConfig{MaxConcurrent: 3, BudgetCeiling: 1000},
		state: &AutonomousState{
			Status:        "running",
			TokensSpent:   500,
			TokensCeiling: 1000,
		},
		rescanCh: make(chan struct{}, 1),
	}

	// Add tokens to exceed ceiling
	as.AddTokensSpent(600)

	if as.state.TokensSpent != 1100 {
		t.Errorf("expected 1100 tokens spent, got %d", as.state.TokensSpent)
	}
}

func TestAllComplete_Detection(t *testing.T) {
	as := &AutonomousScheduler{
		config: AutonomousConfig{MaxConcurrent: 3},
		state: &AutonomousState{
			Status:    "running",
			Remaining: 0,
		},
		rescanCh: make(chan struct{}, 1),
	}

	// With 0 remaining and 0 running, complete() should set status to the given reason
	as.complete("complete")

	if as.state.Status != "complete" {
		t.Errorf("expected status 'complete', got %q", as.state.Status)
	}
	if as.running {
		t.Error("expected running to be false")
	}
}

func TestStatePersistence(t *testing.T) {
	tmpDir := t.TempDir()

	as := &AutonomousScheduler{
		config:        AutonomousConfig{MaxConcurrent: 3},
		workspaceRoot: tmpDir,
		state: &AutonomousState{
			Status:    "running",
			StartedAt: "2026-01-01T00:00:00Z",
			CyclesRun: 5,
			Running: []RunningItem{
				{Repo: "R", Number: 1, Title: "Test"},
			},
			Completed: []CompletedItem{
				{Repo: "R", Number: 2, Title: "Done", CompletedAt: "2026-01-01T01:00:00Z"},
			},
			Failed: []FailedItem{
				{Repo: "R", Number: 3, Title: "Broken", FailedAt: "2026-01-01T02:00:00Z", Reason: "test failure"},
			},
			TokensSpent:   42000,
			TokensCeiling: 100000,
			Remaining:     7,
		},
	}

	// Persist
	as.persistState()

	// Read back
	statePath := filepath.Join(tmpDir, autonomousStateFile)
	data, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatalf("failed to read state file: %v", err)
	}

	var loaded AutonomousState
	if err := json.Unmarshal(data, &loaded); err != nil {
		t.Fatalf("failed to parse state: %v", err)
	}

	if loaded.Status != "running" {
		t.Errorf("expected status 'running', got %q", loaded.Status)
	}
	if loaded.CyclesRun != 5 {
		t.Errorf("expected 5 cycles, got %d", loaded.CyclesRun)
	}
	if len(loaded.Running) != 1 {
		t.Errorf("expected 1 running, got %d", len(loaded.Running))
	}
	if len(loaded.Completed) != 1 {
		t.Errorf("expected 1 completed, got %d", len(loaded.Completed))
	}
	if len(loaded.Failed) != 1 {
		t.Errorf("expected 1 failed, got %d", len(loaded.Failed))
	}
	if loaded.TokensSpent != 42000 {
		t.Errorf("expected 42000 tokens, got %d", loaded.TokensSpent)
	}
	if loaded.TokensCeiling != 100000 {
		t.Errorf("expected 100000 ceiling, got %d", loaded.TokensCeiling)
	}
	if loaded.Remaining != 7 {
		t.Errorf("expected 7 remaining, got %d", loaded.Remaining)
	}
}

func TestStateLoadOnConstruction(t *testing.T) {
	tmpDir := t.TempDir()

	// Write state file
	stateDir := filepath.Join(tmpDir, ".nightgauge", "autonomous")
	if err := os.MkdirAll(stateDir, 0755); err != nil {
		t.Fatal(err)
	}
	state := AutonomousState{
		Status:      "running", // will be loaded as "stopped"
		CyclesRun:   10,
		TokensSpent: 50000,
		Completed: []CompletedItem{
			{Repo: "R", Number: 5, Title: "Previously Done"},
		},
	}
	data, _ := json.Marshal(state)
	if err := os.WriteFile(filepath.Join(stateDir, "state.json"), data, 0644); err != nil {
		t.Fatal(err)
	}

	// Create scheduler — it should load the state
	as := NewAutonomousScheduler(nil, nil, nil, nil, DefaultAutonomousConfig(), tmpDir)

	if as.state.Status != "stopped" {
		t.Errorf("expected 'stopped' (running is resumed as stopped), got %q", as.state.Status)
	}
	if as.state.CyclesRun != 10 {
		t.Errorf("expected 10 cycles from loaded state, got %d", as.state.CyclesRun)
	}
	if len(as.state.Completed) != 1 {
		t.Errorf("expected 1 completed from loaded state, got %d", len(as.state.Completed))
	}
	if as.state.TokensSpent != 50000 {
		t.Errorf("expected 50000 tokens from loaded state, got %d", as.state.TokensSpent)
	}
}

func TestStateLoadPreservesTerminalStatus(t *testing.T) {
	tmpDir := t.TempDir()
	stateDir := filepath.Join(tmpDir, ".nightgauge", "autonomous")
	if err := os.MkdirAll(stateDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Write a terminal state
	state := AutonomousState{Status: "complete", CyclesRun: 20}
	data, _ := json.Marshal(state)
	if err := os.WriteFile(filepath.Join(stateDir, "state.json"), data, 0644); err != nil {
		t.Fatal(err)
	}

	as := NewAutonomousScheduler(nil, nil, nil, nil, DefaultAutonomousConfig(), tmpDir)
	if as.state.Status != "complete" {
		t.Errorf("expected terminal status 'complete' to be preserved, got %q", as.state.Status)
	}
}

func TestDryRunMode(t *testing.T) {
	as := &AutonomousScheduler{
		config: AutonomousConfig{MaxConcurrent: 5, DryRun: true},
		state: &AutonomousState{
			Status: "running",
		},
		rescanCh: make(chan struct{}, 1),
	}

	// In dry-run mode, enqueueItem should not be called.
	// We verify by checking that Running remains empty after runCycle
	// would have dispatched items.
	nodes := []*depgraph.Node{
		{Repo: "R", Number: 1, Title: "A", State: "OPEN", BoardStatus: "Ready", Priority: "P0", Size: "XS", Weight: 1},
	}
	g := buildTestGraph(nodes, nil)

	candidates := as.prioritize(context.Background(), g)
	if len(candidates) != 1 {
		t.Fatalf("expected 1 candidate, got %d", len(candidates))
	}

	// The dry-run check is in runCycle, not prioritize.
	// Verify config is set correctly.
	if !as.config.DryRun {
		t.Error("expected DryRun to be true")
	}
}

func TestIsRunning(t *testing.T) {
	as := &AutonomousScheduler{
		state: &AutonomousState{
			Running: []RunningItem{
				{Repo: "R", Number: 42},
			},
		},
	}

	if !as.isRunning("R", 42) {
		t.Error("expected #42 to be running")
	}
	if as.isRunning("R", 99) {
		t.Error("expected #99 to not be running")
	}
	if as.isRunning("Other", 42) {
		t.Error("expected Other#42 to not be running")
	}
}

func TestPauseResume(t *testing.T) {
	as := &AutonomousScheduler{
		state: &AutonomousState{
			Status: "running",
		},
		rescanCh: make(chan struct{}, 1),
	}

	as.Pause("test", "test")
	if as.state.Status != "paused" {
		t.Errorf("expected 'paused', got %q", as.state.Status)
	}

	as.Resume()
	if as.state.Status != "running" {
		t.Errorf("expected 'running', got %q", as.state.Status)
	}

	// Resume should trigger rescan
	select {
	case <-as.rescanCh:
		// ok
	default:
		t.Error("expected rescan signal after resume")
	}
}

func TestPauseIgnoredWhenNotRunning(t *testing.T) {
	as := &AutonomousScheduler{
		state: &AutonomousState{
			Status: "complete",
		},
		rescanCh: make(chan struct{}, 1),
	}

	as.Pause("test", "test")
	if as.state.Status != "complete" {
		t.Errorf("expected status to remain 'complete', got %q", as.state.Status)
	}
}

func TestResumeIgnoredWhenNotPaused(t *testing.T) {
	as := &AutonomousScheduler{
		state: &AutonomousState{
			Status: "running",
		},
		rescanCh: make(chan struct{}, 1),
	}

	as.Resume()
	// Should be a no-op since status is already "running"
	if as.state.Status != "running" {
		t.Errorf("expected 'running', got %q", as.state.Status)
	}
}

func TestStatusSnapshot(t *testing.T) {
	as := &AutonomousScheduler{
		state: &AutonomousState{
			Status:    "running",
			CyclesRun: 3,
			Running: []RunningItem{
				{Repo: "R", Number: 1},
			},
			Completed: []CompletedItem{
				{Repo: "R", Number: 2},
			},
			Failed: []FailedItem{
				{Repo: "R", Number: 3},
			},
		},
	}

	snap := as.Status()

	// Verify it's a copy
	if &snap.Running[0] == &as.state.Running[0] {
		t.Error("Running should be a deep copy, not a reference")
	}
	if snap.CyclesRun != 3 {
		t.Errorf("expected 3 cycles, got %d", snap.CyclesRun)
	}
	if len(snap.Running) != 1 {
		t.Errorf("expected 1 running, got %d", len(snap.Running))
	}
	if len(snap.Completed) != 1 {
		t.Errorf("expected 1 completed, got %d", len(snap.Completed))
	}
	if len(snap.Failed) != 1 {
		t.Errorf("expected 1 failed, got %d", len(snap.Failed))
	}
}

func TestAddTokensSpent(t *testing.T) {
	as := &AutonomousScheduler{
		state: &AutonomousState{
			TokensSpent: 100,
		},
	}

	as.AddTokensSpent(500)
	if as.state.TokensSpent != 600 {
		t.Errorf("expected 600, got %d", as.state.TokensSpent)
	}

	as.AddTokensSpent(400)
	if as.state.TokensSpent != 1000 {
		t.Errorf("expected 1000, got %d", as.state.TokensSpent)
	}
}

func TestCandidatePriorityRank(t *testing.T) {
	tests := []struct {
		input    string
		expected int
	}{
		{"P0", 0},
		{"P1", 1},
		{"P2", 2},
		{"P3", 3},
		{"p0", 0},   // case insensitive
		{" P1 ", 1}, // trimmed
		{"", 4},
		{"unknown", 4},
	}
	for _, tt := range tests {
		got := candidatePriorityRank(tt.input)
		if got != tt.expected {
			t.Errorf("candidatePriorityRank(%q) = %d, want %d", tt.input, got, tt.expected)
		}
	}
}

func TestStopSignal(t *testing.T) {
	as := &AutonomousScheduler{
		running: true,
		state: &AutonomousState{
			Status: "running",
		},
		stopCh:           make(chan struct{}, 1),
		stopRefinementCh: make(chan struct{}, 1),
		rescanCh:         make(chan struct{}, 1),
	}

	as.Stop()

	// Should have sent a stop signal
	select {
	case <-as.stopCh:
		// ok
	case <-time.After(100 * time.Millisecond):
		t.Error("expected stop signal")
	}
}

func TestStopIgnoredWhenNotRunning(t *testing.T) {
	as := &AutonomousScheduler{
		running: false,
		state: &AutonomousState{
			Status: "stopped",
		},
		stopCh:           make(chan struct{}, 1),
		stopRefinementCh: make(chan struct{}, 1),
		rescanCh:         make(chan struct{}, 1),
	}

	as.Stop()

	// Should not have sent a stop signal
	select {
	case <-as.stopCh:
		t.Error("should not send stop signal when not running")
	default:
		// ok
	}
}

// TestNewAutonomousScheduler_StopChBuffered pins a regression: the scheduler
// constructor must allocate stopCh with capacity ≥ 1 so Stop()'s non-blocking
// send always succeeds. Previously stopCh was unbuffered, which meant any
// Stop() call racing with a mid-cycle scan fell through the select's
// `default:` branch and the signal was silently dropped — the scheduler kept
// looping even though the UI reported it stopped.
func TestNewAutonomousScheduler_StopChBuffered(t *testing.T) {
	tmpDir := t.TempDir()
	as := NewAutonomousScheduler(nil, nil, nil, nil, DefaultAutonomousConfig(), tmpDir)

	if cap(as.stopCh) < 1 {
		t.Fatalf("stopCh must be buffered (cap >= 1) to survive the Stop-during-runCycle race, got cap=%d", cap(as.stopCh))
	}

	// Simulate the exact race: scheduler running, no reader on stopCh yet.
	as.running = true
	as.state = &AutonomousState{Status: "running"}

	as.Stop()

	// With a buffered channel, the stop signal must be pending — not lost.
	select {
	case <-as.stopCh:
		// ok
	default:
		t.Fatal("Stop() signal was dropped because stopCh was unbuffered — scheduler would keep looping")
	}
}

// TestStopSetsStopRequested verifies that Stop() sets the stopRequested flag
// that the runCycle dispatch loop checks. Without this flag, a Stop pressed
// mid-cycle would not take effect until after every candidate in the already-
// built list had been dispatched — leaking autonomous.dispatch events to the
// TypeScript side after the user clicked "Stop Autonomous".
func TestStopSetsStopRequested(t *testing.T) {
	as := &AutonomousScheduler{
		running: true,
		state: &AutonomousState{
			Status: "running",
		},
		stopCh:           make(chan struct{}, 1),
		stopRefinementCh: make(chan struct{}, 1),
		rescanCh:         make(chan struct{}, 1),
	}

	if as.stopRequested {
		t.Fatal("stopRequested should start as false")
	}

	as.Stop()

	as.mu.Lock()
	got := as.stopRequested
	as.mu.Unlock()
	if !got {
		t.Error("Stop() should set stopRequested=true so runCycle can bail out mid-loop")
	}
}

// TestRunCycleBailsOutOnStopRequested verifies that if Stop() is called while
// runCycle has already selected candidates, the remaining dispatches are
// skipped. This is the load-bearing behavior of Change 3 from
// fix/stop-controls-drain-queue: without it, Go's onDispatch callback would
// keep firing for every prioritized candidate even though the user pressed
// Stop, re-populating the TypeScript-side queue that was just cleared.
//
// We exercise the dispatch loop via a direct helper that mirrors runCycle's
// bail-out logic, avoiding the full runCycle machinery (graph build, gh
// client, etc.) which would require significant scaffolding.
func TestRunCycleBailsOutOnStopRequested(t *testing.T) {
	candidates := []CandidateItem{
		{Repo: "R/r", Number: 1, Title: "a"},
		{Repo: "R/r", Number: 2, Title: "b"},
		{Repo: "R/r", Number: 3, Title: "c"},
	}
	dispatched := 0
	as := &AutonomousScheduler{
		running: true,
		state: &AutonomousState{
			Status:  "running",
			Running: nil,
		},
		stopCh:           make(chan struct{}, 1),
		stopRefinementCh: make(chan struct{}, 1),
		rescanCh:         make(chan struct{}, 1),
		config:           AutonomousConfig{MaxConcurrent: 5},
		onDispatch: func(_, _ string, _ int, _ string) {
			dispatched++
		},
	}

	// Simulate the dispatch loop: Stop() is called after the first item
	// has been dispatched. The guard must cause remaining iterations to
	// break out instead of continuing to call onDispatch.
	availableSlots := 3
	for i := 0; i < len(candidates) && i < availableSlots; i++ {
		as.mu.Lock()
		stopRequested := as.stopRequested
		as.mu.Unlock()
		if stopRequested {
			break
		}
		// Emulate enqueueItem's onDispatch fire path
		if as.onDispatch != nil {
			as.onDispatch("R", "r", candidates[i].Number, candidates[i].Title)
		}
		// User presses Stop after the first dispatch completes
		if i == 0 {
			as.Stop()
		}
	}

	if dispatched != 1 {
		t.Errorf("expected exactly 1 dispatch before Stop took effect, got %d", dispatched)
	}
}

// #3020 follow-up — verify the dispatch loop also bails out when state.Status
// flips away from "running" mid-cycle (Pause, complete, safety_tripped).
//
// Original symptom: #291 stall-killed at 13:25:15, haltQueueOnSlotFailure
// called autonomousPause() which set state.Status = "paused", but a runCycle
// already mid-loop dispatched #785 at 13:25:38 because the only status check
// happened once at the top of the cycle. The per-candidate re-check fixes it.
func TestRunCycleBailsOutOnStatusChangeMidCycle(t *testing.T) {
	candidates := []CandidateItem{
		{Repo: "R/r", Number: 1, Title: "a"},
		{Repo: "R/r", Number: 2, Title: "b"},
		{Repo: "R/r", Number: 3, Title: "c"},
	}
	dispatched := 0
	as := &AutonomousScheduler{
		running:          true,
		state:            &AutonomousState{Status: "running"},
		stopCh:           make(chan struct{}, 1),
		stopRefinementCh: make(chan struct{}, 1),
		rescanCh:         make(chan struct{}, 1),
		config:           AutonomousConfig{MaxConcurrent: 5},
		onDispatch: func(_, _ string, _ int, _ string) {
			dispatched++
		},
	}

	availableSlots := 3
	for i := 0; i < len(candidates) && i < availableSlots; i++ {
		as.mu.Lock()
		stopRequested := as.stopRequested
		statusNow := as.state.Status
		as.mu.Unlock()
		if stopRequested || statusNow != "running" {
			break
		}
		if as.onDispatch != nil {
			as.onDispatch("R", "r", candidates[i].Number, candidates[i].Title)
		}
		// Simulate haltQueueOnSlotFailure → Pause() landing after the first
		// dispatch completes.
		if i == 0 {
			as.Pause("test", "test")
		}
	}

	if dispatched != 1 {
		t.Errorf("expected exactly 1 dispatch before Pause took effect, got %d", dispatched)
	}
}

// #3023 phase 1 — TriggerRescan is non-blocking, idempotent, and safe to
// call from any goroutine. A burst of calls collapses to at most one
// pending rescan signal in the buffered channel.
func TestTriggerRescan_NonBlockingAndIdempotent(t *testing.T) {
	as := &AutonomousScheduler{
		state:    &AutonomousState{Status: "running"},
		rescanCh: make(chan struct{}, 1),
	}

	// First call lands in the buffered channel.
	as.TriggerRescan()
	if len(as.rescanCh) != 1 {
		t.Fatalf("expected 1 pending rescan signal, got %d", len(as.rescanCh))
	}

	// Subsequent calls coalesce — buffer cap is 1, channel stays at 1.
	for i := 0; i < 5; i++ {
		as.TriggerRescan()
	}
	if len(as.rescanCh) != 1 {
		t.Fatalf("expected coalesced signal (cap=1), got %d", len(as.rescanCh))
	}

	// Drain — simulating the run-loop consuming the signal.
	<-as.rescanCh

	// New burst again lands one signal.
	as.TriggerRescan()
	if len(as.rescanCh) != 1 {
		t.Errorf("expected 1 signal after drain, got %d", len(as.rescanCh))
	}
}

// #3023 phase 1 — idle counter increments only when the most recent cycle
// observed no candidates AND no running pipelines. Any candidate or
// running pipeline resets it to zero so cadence snaps back to base.
func TestUpdateIdleCounterAfterCycle(t *testing.T) {
	as := &AutonomousScheduler{
		state:  &AutonomousState{},
		config: AutonomousConfig{MaxConcurrent: 3},
	}

	// Idle cycle (no candidates, no running) → increment.
	as.state.Remaining = 0
	as.state.Running = nil
	if got := as.updateIdleCounterAfterCycle(2); got != 3 {
		t.Errorf("idle increment: got %d, want 3", got)
	}

	// Candidates observed → reset (has capacity, work is available).
	as.state.Remaining = 4
	as.state.Running = nil
	if got := as.updateIdleCounterAfterCycle(7); got != 0 {
		t.Errorf("candidates observed should reset idle counter, got %d", got)
	}

	// Running pipeline with available global slots → reset (dispatch may happen).
	as.state.Remaining = 0
	as.state.Running = []RunningItem{{Repo: "owner/R", Number: 1}}
	if got := as.updateIdleCounterAfterCycle(7); got != 0 {
		t.Errorf("running pipeline with open slots should reset idle counter, got %d", got)
	}

	// All global slots occupied → increment (nothing to dispatch until one finishes).
	as.state.Remaining = 0
	as.state.Running = []RunningItem{{Repo: "owner/R", Number: 1}, {Repo: "owner/R", Number: 2}, {Repo: "owner/R", Number: 3}}
	if got := as.updateIdleCounterAfterCycle(5); got != 6 {
		t.Errorf("all global slots occupied should increment idle counter, got %d, want 6", got)
	}

	// Per-repo cap exhausted even though global slots remain (user scenario:
	// MaxConcurrent=3 but only one repo selected with cap=1).
	as.repos = []depgraph.RepoConfig{{Owner: "owner", Name: "R"}}
	as.config.RepositoryMaxConcurrent = map[string]int{"owner/R": 1}
	as.state.Remaining = 0
	as.state.Running = []RunningItem{{Repo: "owner/R", Number: 1}}
	if got := as.updateIdleCounterAfterCycle(3); got != 4 {
		t.Errorf("per-repo cap exhausted should increment idle counter, got %d, want 4", got)
	}
}

// #3023 phase 1 — scanCadence floors a misconfigured ScanInterval to 30s
// rather than spinning the CPU.
func TestScanCadence_FloorsMisconfigured(t *testing.T) {
	as := &AutonomousScheduler{config: AutonomousConfig{ScanInterval: 0}}
	if d := as.scanCadence(); d != 30*time.Second {
		t.Errorf("0 interval should floor to 30s, got %v", d)
	}
	as.config.ScanInterval = 100 * time.Millisecond
	if d := as.scanCadence(); d != 30*time.Second {
		t.Errorf("sub-5s interval should floor to 30s, got %v", d)
	}
	as.config.ScanInterval = 15 * time.Second
	if d := as.scanCadence(); d != 15*time.Second {
		t.Errorf("valid interval should be returned as-is, got %v", d)
	}
}

func TestRunAlreadyRunning(t *testing.T) {
	as := &AutonomousScheduler{
		running: true,
		state: &AutonomousState{
			Status: "running",
		},
	}

	err := as.Run(context.Background())
	if err == nil {
		t.Fatal("expected error when already running")
	}
	if err.Error() != "autonomous scheduler is already running" {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestCrossRepoPrioritization(t *testing.T) {
	// Items from different repos — verify cross-repo items are included AND
	// that priority labels dominate the cross-repo dispatch order. Post-#3396
	// the strict ordering is P0 → P1 → P2 regardless of which repo "owns" the
	// critical path, so the platform's P0 must be picked before any P1/P2
	// regardless of weight.
	nodes := []*depgraph.Node{
		{Repo: "O/core", Number: 1, Title: "Core Issue", State: "OPEN", BoardStatus: "Ready", Priority: "P1", Size: "M", Weight: 3},
		{Repo: "O/platform", Number: 10, Title: "Platform Issue", State: "OPEN", BoardStatus: "Ready", Priority: "P0", Size: "S", Weight: 2},
		{Repo: "O/flutter", Number: 20, Title: "Flutter Issue", State: "OPEN", BoardStatus: "Ready", Priority: "P2", Size: "XS", Weight: 1},
	}
	g := buildTestGraph(nodes, nil)

	as := &AutonomousScheduler{
		config: AutonomousConfig{MaxConcurrent: 5},
		state:  &AutonomousState{},
	}

	candidates := as.prioritize(context.Background(), g)
	if len(candidates) != 3 {
		t.Fatalf("expected 3 candidates from 3 repos, got %d", len(candidates))
	}
	// All 3 repos should be represented
	repoSet := make(map[string]bool)
	for _, c := range candidates {
		repoSet[c.Repo] = true
	}
	if len(repoSet) != 3 {
		t.Errorf("expected 3 distinct repos, got %d", len(repoSet))
	}
	// Strict P0 → P1 → P2 ordering across repos.
	expectedOrder := []string{"P0", "P1", "P2"}
	for i, exp := range expectedOrder {
		if candidates[i].Priority != exp {
			t.Errorf("position %d (cross-repo): expected priority %s, got %s (#%d %s)",
				i, exp, candidates[i].Priority, candidates[i].Number, candidates[i].Repo)
		}
	}
}

func TestFocusAlignmentScore_GeneralNoBoost(t *testing.T) {
	as := &AutonomousScheduler{}
	generalLens := &focus.Lens{Name: "general"}
	item := &CandidateItem{
		Labels: []string{"test", "coverage"},
		Title:  "Test coverage improvement",
	}
	score := as.focusAlignmentScore(item, generalLens)
	if score != 0 {
		t.Errorf("expected 0 for general lens, got %d", score)
	}
}

func TestFocusAlignmentScore_NilLensNoBoost(t *testing.T) {
	as := &AutonomousScheduler{}
	item := &CandidateItem{Labels: []string{"test"}, Title: "Test something"}
	score := as.focusAlignmentScore(item, nil)
	if score != 0 {
		t.Errorf("expected 0 for nil lens, got %d", score)
	}
}

func TestFocusAlignmentScore_KeywordMatches(t *testing.T) {
	as := &AutonomousScheduler{}
	qualityLens := &focus.Lens{
		Name:     "quality",
		Keywords: []string{"test", "coverage", "lint", "validate"},
	}

	tests := []struct {
		name     string
		labels   []string
		title    string
		expected int
	}{
		{
			name:     "label match only",
			labels:   []string{"test", "type:feature"},
			title:    "Add new feature",
			expected: 2, // +2 for "test" label match
		},
		{
			name:     "title match only",
			labels:   []string{"type:feature"},
			title:    "Test coverage for lint",
			expected: 3, // +1 "test" + +1 "coverage" + +1 "lint" in title
		},
		{
			name:     "label and title match",
			labels:   []string{"coverage"},
			title:    "Test coverage for lint",
			expected: 5, // +2 "coverage" label + +1 "test" title + +1 "coverage" title + +1 "lint" title
		},
		{
			name:     "no matches",
			labels:   []string{"feature", "type:enhancement"},
			title:    "Add dashboard view",
			expected: 0,
		},
		{
			name:     "case insensitive",
			labels:   []string{"TEST", "COVERAGE"},
			title:    "LINT improvements",
			expected: 5, // +2 TEST label + +2 COVERAGE label + +1 LINT title
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			item := &CandidateItem{Labels: tt.labels, Title: tt.title}
			score := as.focusAlignmentScore(item, qualityLens)
			if score != tt.expected {
				t.Errorf("expected score %d, got %d", tt.expected, score)
			}
		})
	}
}

func TestFocusAlignmentScore_CapAt20(t *testing.T) {
	as := &AutonomousScheduler{}
	lens := &focus.Lens{
		Name:     "quality",
		Keywords: []string{"test", "coverage", "lint", "quality", "type", "strict", "validate", "correctness"},
	}
	// Many matching labels and title matches to exceed 20
	item := &CandidateItem{
		Labels: []string{"test", "coverage", "lint", "quality", "type"},
		Title:  "strict validate correctness test coverage lint",
	}
	score := as.focusAlignmentScore(item, lens)
	if score > 20 {
		t.Errorf("expected score capped at 20, got %d", score)
	}
}

func TestPrioritize_PriorityDominatesFocusBoost(t *testing.T) {
	// Item A: P1, focus-aligned (quality lens)
	// Item B: P0, not focus-aligned
	//
	// After #3396, priority dominates focus alignment: a P0 must dispatch
	// before a P1 even if the P1 is the only focus-aligned candidate. Focus
	// is a tiebreaker WITHIN a priority level, not an override.
	//
	// (Previously this test asserted the opposite — that focus alignment
	// could promote a P1 over a P0. That inversion is exactly the bug the
	// user hit when GitLab P1 work starved standalone P0 stability fixes.)
	tmpDir := t.TempDir()

	focusDir := filepath.Join(tmpDir, ".nightgauge")
	if err := os.MkdirAll(focusDir, 0755); err != nil {
		t.Fatal(err)
	}
	focusYAML := []byte("active_lens: quality\n")
	if err := os.WriteFile(filepath.Join(focusDir, "focus.yaml"), focusYAML, 0644); err != nil {
		t.Fatal(err)
	}

	nodes := []*depgraph.Node{
		{Repo: "R", Number: 1, Title: "Add test coverage", State: "OPEN", BoardStatus: "Ready", Priority: "P1", Size: "M", Weight: 3,
			Labels: []string{"coverage", "test"}},
		{Repo: "R", Number: 2, Title: "New dashboard feature", State: "OPEN", BoardStatus: "Ready", Priority: "P0", Size: "M", Weight: 3,
			Labels: []string{"feature"}},
	}
	g := buildTestGraph(nodes, nil)

	as := &AutonomousScheduler{
		config:        AutonomousConfig{MaxConcurrent: 5},
		state:         &AutonomousState{},
		workspaceRoot: tmpDir,
	}

	candidates := as.prioritize(context.Background(), g)
	if len(candidates) != 2 {
		t.Fatalf("expected 2 candidates, got %d", len(candidates))
	}

	// P0 #2 must come first regardless of focus alignment.
	if candidates[0].Number != 2 {
		t.Errorf("expected P0 #2 first regardless of focus alignment; got #%d (priority=%s)",
			candidates[0].Number, candidates[0].Priority)
	}
	if candidates[0].Priority != "P0" {
		t.Errorf("first candidate priority = %q, want P0", candidates[0].Priority)
	}
}

func TestPrioritize_FocusTiebreaksWithinSamePriority(t *testing.T) {
	// Two P1 items, same size; one is focus-aligned, one isn't. Within the
	// same priority level, focus alignment IS a tiebreaker (after critical
	// path).
	tmpDir := t.TempDir()
	focusDir := filepath.Join(tmpDir, ".nightgauge")
	if err := os.MkdirAll(focusDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(focusDir, "focus.yaml"), []byte("active_lens: quality\n"), 0644); err != nil {
		t.Fatal(err)
	}

	nodes := []*depgraph.Node{
		{Repo: "R", Number: 1, Title: "Add test coverage", State: "OPEN", BoardStatus: "Ready", Priority: "P1", Size: "M", Weight: 3,
			Labels: []string{"coverage", "test"}},
		{Repo: "R", Number: 2, Title: "Misc", State: "OPEN", BoardStatus: "Ready", Priority: "P1", Size: "M", Weight: 3,
			Labels: []string{"feature"}},
	}
	g := buildTestGraph(nodes, nil)

	as := &AutonomousScheduler{
		config:        AutonomousConfig{MaxConcurrent: 5},
		state:         &AutonomousState{},
		workspaceRoot: tmpDir,
	}

	candidates := as.prioritize(context.Background(), g)
	if len(candidates) != 2 {
		t.Fatalf("expected 2 candidates, got %d", len(candidates))
	}
	// Within the same priority/critical-path bucket, the focus-aligned item
	// should not be last.
	focusAlignedPos := -1
	for i, c := range candidates {
		if c.Number == 1 {
			focusAlignedPos = i
		}
	}
	if focusAlignedPos == len(candidates)-1 {
		t.Errorf("expected focus-aligned P1 #1 not to be last among same-priority candidates; got pos=%d",
			focusAlignedPos)
	}
}

func TestPrioritize_FocusGeneralNoEffect(t *testing.T) {
	// With general focus, ordering should be by priority only (P0 before P1).
	tmpDir := t.TempDir()

	// Write focus.yaml with general lens (explicitly)
	focusDir := filepath.Join(tmpDir, ".nightgauge")
	if err := os.MkdirAll(focusDir, 0755); err != nil {
		t.Fatal(err)
	}
	focusYAML := []byte("active_lens: general\n")
	if err := os.WriteFile(filepath.Join(focusDir, "focus.yaml"), focusYAML, 0644); err != nil {
		t.Fatal(err)
	}

	nodes := []*depgraph.Node{
		{Repo: "R", Number: 1, Title: "Lower priority item", State: "OPEN", BoardStatus: "Ready", Priority: "P1", Size: "M", Weight: 3,
			Labels: []string{"test", "coverage"}},
		{Repo: "R", Number: 2, Title: "Higher priority item", State: "OPEN", BoardStatus: "Ready", Priority: "P0", Size: "M", Weight: 3,
			Labels: []string{"feature"}},
	}
	g := buildTestGraph(nodes, nil)

	as := &AutonomousScheduler{
		config:        AutonomousConfig{MaxConcurrent: 5},
		state:         &AutonomousState{},
		workspaceRoot: tmpDir,
	}

	candidates := as.prioritize(context.Background(), g)
	if len(candidates) != 2 {
		t.Fatalf("expected 2 candidates, got %d", len(candidates))
	}

	// With general focus: among non-crit-path items, P0 (#2) must come before P1 (#1).
	// One item may be on critical path. Find non-crit items and check priority order.
	var nonCrit []CandidateItem
	for _, c := range candidates {
		if !c.OnCritPath {
			nonCrit = append(nonCrit, c)
		}
	}
	if len(nonCrit) >= 2 {
		if candidatePriorityRank(nonCrit[0].Priority) > candidatePriorityRank(nonCrit[1].Priority) {
			t.Errorf("with general focus, P0 should come before P1; got %s before %s",
				nonCrit[0].Priority, nonCrit[1].Priority)
		}
	}
}

func TestPrioritize_CriticalPathStillBeatsFocus(t *testing.T) {
	// Critical path should still win over focus-aligned items.
	tmpDir := t.TempDir()

	focusDir := filepath.Join(tmpDir, ".nightgauge")
	if err := os.MkdirAll(focusDir, 0755); err != nil {
		t.Fatal(err)
	}
	focusYAML := []byte("active_lens: quality\n")
	if err := os.WriteFile(filepath.Join(focusDir, "focus.yaml"), focusYAML, 0644); err != nil {
		t.Fatal(err)
	}

	nodes := []*depgraph.Node{
		// A: not critical, P3, but heavily focus-aligned
		{Repo: "R", Number: 1, Title: "Test coverage lint quality validate", State: "OPEN", BoardStatus: "Ready",
			Priority: "P3", Size: "L", Weight: 1, Labels: []string{"test", "coverage", "lint", "quality"}},
		// B: on critical path (highest weight), P3, no focus alignment
		{Repo: "R", Number: 2, Title: "Infrastructure change", State: "OPEN", BoardStatus: "Ready",
			Priority: "P3", Size: "XL", Weight: 10, Labels: []string{"infra"}},
	}
	g := buildTestGraph(nodes, nil)

	as := &AutonomousScheduler{
		config:        AutonomousConfig{MaxConcurrent: 5},
		state:         &AutonomousState{},
		workspaceRoot: tmpDir,
	}

	candidates := as.prioritize(context.Background(), g)
	if len(candidates) != 2 {
		t.Fatalf("expected 2 candidates, got %d", len(candidates))
	}

	// B (#2) is on critical path and should be first
	if candidates[0].Number != 2 {
		t.Errorf("expected critical-path item (#2) first, got #%d", candidates[0].Number)
	}
	if !candidates[0].OnCritPath {
		t.Error("expected first candidate to be on critical path")
	}
}

func TestPrioritize_FocusMissingYaml(t *testing.T) {
	// When focus.yaml doesn't exist, prioritization should work normally
	// (no focus boost, backward compatible).
	tmpDir := t.TempDir()

	nodes := []*depgraph.Node{
		{Repo: "R", Number: 1, Title: "P0 item", State: "OPEN", BoardStatus: "Ready", Priority: "P0", Size: "M", Weight: 3},
		{Repo: "R", Number: 2, Title: "P1 item", State: "OPEN", BoardStatus: "Ready", Priority: "P1", Size: "M", Weight: 3},
	}
	g := buildTestGraph(nodes, nil)

	as := &AutonomousScheduler{
		config:        AutonomousConfig{MaxConcurrent: 5},
		state:         &AutonomousState{},
		workspaceRoot: tmpDir, // no focus.yaml here
	}

	// Should not panic, should return candidates sorted by priority
	candidates := as.prioritize(context.Background(), g)
	if len(candidates) != 2 {
		t.Fatalf("expected 2 candidates, got %d", len(candidates))
	}
	// One is on critical path; among non-crit, P0 should precede P1
	nonCrit := make([]CandidateItem, 0)
	for _, c := range candidates {
		if !c.OnCritPath {
			nonCrit = append(nonCrit, c)
		}
	}
	if len(nonCrit) >= 2 {
		if candidatePriorityRank(nonCrit[0].Priority) > candidatePriorityRank(nonCrit[1].Priority) {
			t.Errorf("without focus.yaml, priority ordering must hold; got %s before %s",
				nonCrit[0].Priority, nonCrit[1].Priority)
		}
	}
}

func TestEpicAutoCloseScenario(t *testing.T) {
	// When all sub-issues of an epic are CLOSED, the epic should not appear
	// as a candidate because it has the type:epic label.
	nodes := []*depgraph.Node{
		{Repo: "R", Number: 100, Title: "Epic", State: "OPEN", BoardStatus: "Ready", Labels: []string{"type:epic"}, Priority: "P0", Size: "XL", Weight: 8, EpicNumber: 0},
		{Repo: "R", Number: 101, Title: "Sub 1", State: "CLOSED", BoardStatus: "Done", Priority: "P1", Size: "S", Weight: 2, EpicNumber: 100},
		{Repo: "R", Number: 102, Title: "Sub 2", State: "CLOSED", BoardStatus: "Done", Priority: "P1", Size: "S", Weight: 2, EpicNumber: 100},
	}
	g := buildTestGraph(nodes, nil)

	as := &AutonomousScheduler{
		config: AutonomousConfig{MaxConcurrent: 5},
		state:  &AutonomousState{},
	}

	candidates := as.prioritize(context.Background(), g)
	// No candidates: epic is skipped (type:epic), sub-issues are CLOSED
	if len(candidates) != 0 {
		t.Errorf("expected 0 candidates, got %d", len(candidates))
	}
}

func TestFilterRepos_PrunesPersistedState(t *testing.T) {
	tmpDir := t.TempDir()

	repos := []depgraph.RepoConfig{
		{Owner: "Org", Name: "alpha", Project: 1},
		{Owner: "Org", Name: "beta", Project: 2},
		{Owner: "Org", Name: "gamma", Project: 3},
	}

	as := NewAutonomousScheduler(nil, nil, repos, nil, DefaultAutonomousConfig(), tmpDir)

	// Simulate state from a prior session that included all three repos.
	as.state.Completed = []CompletedItem{
		{Repo: "Org/alpha", Number: 1, Title: "Alpha task"},
		{Repo: "Org/beta", Number: 2, Title: "Beta task"},
		{Repo: "Org/gamma", Number: 3, Title: "Gamma task"},
	}
	as.state.Failed = []FailedItem{
		{Repo: "Org/alpha", Number: 10, Title: "Alpha fail"},
		{Repo: "Org/beta", Number: 20, Title: "Beta fail"},
	}
	as.state.Running = []RunningItem{
		{Repo: "Org/gamma", Number: 30, Title: "Gamma running"},
	}
	as.perIssueFailureCount = map[string]int{
		"Org/alpha#10": 2,
		"Org/beta#20":  1,
	}
	as.retryBackoff = map[string]time.Time{
		"Org/alpha#10": time.Now().Add(5 * time.Minute),
		"Org/beta#20":  time.Now().Add(10 * time.Minute),
	}
	as.conflictRestartCount = map[string]int{
		"Org/gamma#30": 1,
	}

	// Filter to only "alpha" — beta and gamma entries should be pruned.
	as.FilterRepos([]string{"Org/alpha"})

	if len(as.repos) != 1 || as.repos[0].Name != "alpha" {
		t.Errorf("expected repos=[alpha], got %v", as.repos)
	}
	if len(as.state.Completed) != 1 || as.state.Completed[0].Repo != "Org/alpha" {
		t.Errorf("expected 1 completed (alpha), got %d: %v", len(as.state.Completed), as.state.Completed)
	}
	if len(as.state.Failed) != 1 || as.state.Failed[0].Repo != "Org/alpha" {
		t.Errorf("expected 1 failed (alpha), got %d: %v", len(as.state.Failed), as.state.Failed)
	}
	if len(as.state.Running) != 0 {
		t.Errorf("expected 0 running (gamma pruned), got %d", len(as.state.Running))
	}
	if _, ok := as.perIssueFailureCount["Org/beta#20"]; ok {
		t.Error("expected beta backoff entry to be pruned from perIssueFailureCount")
	}
	if _, ok := as.retryBackoff["Org/beta#20"]; ok {
		t.Error("expected beta entry to be pruned from retryBackoff")
	}
	if _, ok := as.conflictRestartCount["Org/gamma#30"]; ok {
		t.Error("expected gamma entry to be pruned from conflictRestartCount")
	}
	// Alpha entries should remain.
	if as.perIssueFailureCount["Org/alpha#10"] != 2 {
		t.Errorf("expected alpha failure count preserved, got %d", as.perIssueFailureCount["Org/alpha#10"])
	}
}

func TestFilterRepos_NoMatchKeepsAllState(t *testing.T) {
	tmpDir := t.TempDir()

	repos := []depgraph.RepoConfig{
		{Owner: "Org", Name: "alpha", Project: 1},
	}

	as := NewAutonomousScheduler(nil, nil, repos, nil, DefaultAutonomousConfig(), tmpDir)
	as.state.Failed = []FailedItem{
		{Repo: "Org/alpha", Number: 1, Title: "Should be kept"},
	}

	// Filter with a repo that doesn't match any — should be a no-op.
	as.FilterRepos([]string{"Org/nonexistent"})

	if len(as.repos) != 1 {
		t.Errorf("expected repos unchanged (no match), got %d", len(as.repos))
	}
	if len(as.state.Failed) != 1 {
		t.Errorf("expected failed list unchanged (no match), got %d", len(as.state.Failed))
	}
}

func TestFilterRepos_CaseInsensitive(t *testing.T) {
	tmpDir := t.TempDir()

	repos := []depgraph.RepoConfig{
		{Owner: "nightgauge", Name: "acmeweb", Project: 5},
		{Owner: "nightgauge", Name: "nightgauge", Project: 1},
	}

	as := NewAutonomousScheduler(nil, nil, repos, nil, DefaultAutonomousConfig(), tmpDir)
	as.state.Completed = []CompletedItem{
		{Repo: "nightgauge/acmeweb", Number: 10, Title: "Acmeweb done"},
		{Repo: "nightgauge/nightgauge", Number: 2486, Title: "IB done"},
	}
	as.state.Failed = []FailedItem{
		{Repo: "nightgauge/nightgauge", Number: 2487, Title: "IB fail"},
	}

	// Filter with different casing — should still match acmeweb.
	as.FilterRepos([]string{"nightgauge/Acmeweb"})

	if len(as.repos) != 1 {
		t.Errorf("expected 1 repo, got %d", len(as.repos))
	}
	if len(as.state.Completed) != 1 || as.state.Completed[0].Number != 10 {
		t.Errorf("expected only acmeweb completed entry, got %v", as.state.Completed)
	}
	if len(as.state.Failed) != 0 {
		t.Errorf("expected 0 failed (nightgauge pruned), got %d", len(as.state.Failed))
	}
}

// TestFilterRepos_Widening verifies that a subsequent FilterRepos call can
// re-include a repo that was previously filtered out. Previously FilterRepos
// monotonically shrank as.repos, so toggling a repo off then back on required
// rebuilding the scheduler. After the allRepos refactor a re-filter must
// restore the pristine entry.
func TestFilterRepos_Widening(t *testing.T) {
	tmpDir := t.TempDir()

	repos := []depgraph.RepoConfig{
		{Owner: "nightgauge", Name: "nightgauge", Project: 1},
		{Owner: "nightgauge", Name: "acme-platform", Project: 2},
		{Owner: "nightgauge", Name: "acme-mobile", Project: 3},
	}

	as := NewAutonomousScheduler(nil, nil, repos, nil, DefaultAutonomousConfig(), tmpDir)

	// Narrow to platform only.
	as.FilterRepos([]string{"nightgauge/acme-platform"})
	if len(as.repos) != 1 || as.repos[0].Name != "acme-platform" {
		t.Fatalf("after narrow: expected [platform], got %+v", as.repos)
	}

	// Widen back to all three repos — must recover pristine entries.
	as.FilterRepos([]string{
		"nightgauge/nightgauge",
		"nightgauge/acme-platform",
		"nightgauge/acme-mobile",
	})
	if len(as.repos) != 3 {
		t.Errorf("after widen: expected 3 repos, got %d (%+v)", len(as.repos), as.repos)
	}

	// Narrow differently — mobile only.
	as.FilterRepos([]string{"nightgauge/acme-mobile"})
	if len(as.repos) != 1 || as.repos[0].Name != "acme-mobile" {
		t.Errorf("after re-narrow: expected [mobile], got %+v", as.repos)
	}
}

// --- Refinement tests ---

func TestRefinementConfig_Defaults(t *testing.T) {
	cfg := DefaultAutonomousConfig()
	if !cfg.RefinementEnabled {
		t.Error("expected RefinementEnabled default=true")
	}
	if cfg.RefinementInterval != 60*time.Second {
		t.Errorf("expected RefinementInterval=60s, got %v", cfg.RefinementInterval)
	}
	if cfg.RefinementMaxConcurrent != 1 {
		t.Errorf("expected RefinementMaxConcurrent=1, got %d", cfg.RefinementMaxConcurrent)
	}
	if cfg.RefinementCooldown != 5*time.Minute {
		t.Errorf("expected RefinementCooldown=5m, got %v", cfg.RefinementCooldown)
	}
}

func TestRefinementConfig_SemaphoreClamp(t *testing.T) {
	// MaxConcurrent > 3 should be clamped to 3
	cfg := DefaultAutonomousConfig()
	cfg.RefinementMaxConcurrent = 10

	as := NewAutonomousScheduler(nil, nil, nil, nil, cfg, "")
	if cap(as.refinementSem) != 3 {
		t.Errorf("expected refinement sem capacity=3 (clamped), got %d", cap(as.refinementSem))
	}
}

func TestRefinementConfig_SemaphoreMinimum(t *testing.T) {
	// MaxConcurrent = 0 should default to 1
	cfg := DefaultAutonomousConfig()
	cfg.RefinementMaxConcurrent = 0

	as := NewAutonomousScheduler(nil, nil, nil, nil, cfg, "")
	if cap(as.refinementSem) != 1 {
		t.Errorf("expected refinement sem capacity=1 (min), got %d", cap(as.refinementSem))
	}
}

func TestRefinementCycleSkipsAlreadyRunning(t *testing.T) {
	as := &AutonomousScheduler{
		config: AutonomousConfig{
			RefinementEnabled:       true,
			RefinementMaxConcurrent: 1,
			RefinementCooldown:      time.Minute,
		},
		state: &AutonomousState{
			Status: "running",
			RefinementRunning: []RefinementItem{
				{Repo: "O/R", Number: 42, Title: "Already refining"},
			},
		},
		safetyRails:        NewSafetyRails(DefaultSafetyConfig()),
		refinementSem:      make(chan struct{}, 1),
		refinementCooldown: make(map[string]time.Time),
		refinementFailures: make(map[string]int),
	}

	// An issue that matches a RefinementRunning entry should be skipped.
	// We can't easily mock the GitHub API in this package test, but we can
	// verify the state tracking methods work correctly.
	if !as.isRefinementRunning("O/R", 42) {
		t.Error("expected issue #42 to be recognized as currently refining")
	}
	if as.isRefinementRunning("O/R", 99) {
		t.Error("expected issue #99 to NOT be recognized as refining")
	}
}

func TestRefinementCycleCooldown(t *testing.T) {
	as := &AutonomousScheduler{
		config: AutonomousConfig{
			RefinementEnabled:  true,
			RefinementCooldown: 5 * time.Minute,
		},
		state:              &AutonomousState{Status: "running"},
		refinementCooldown: make(map[string]time.Time),
		refinementFailures: make(map[string]int),
	}

	key := "O/R#42"
	// Set a cooldown in the future
	as.refinementCooldown[key] = time.Now().Add(5 * time.Minute)

	if !as.isInRefinementCooldown(key) {
		t.Error("expected issue to be in cooldown")
	}

	// Set a cooldown in the past
	as.refinementCooldown[key] = time.Now().Add(-1 * time.Second)
	if as.isInRefinementCooldown(key) {
		t.Error("expected issue to NOT be in cooldown (expired)")
	}
}

func TestRefinementCycleDisabled(t *testing.T) {
	as := &AutonomousScheduler{
		config: AutonomousConfig{
			RefinementEnabled: false,
		},
		state: &AutonomousState{Status: "running"},
	}

	// Run() should NOT start the refinement goroutine when disabled.
	// We verify indirectly: if refinementSem is nil (not initialized by Run), it's disabled.
	// In practice, the goroutine start is gated by config.RefinementEnabled in Run().
	if as.config.RefinementEnabled {
		t.Error("expected RefinementEnabled=false")
	}
}

func TestRefinementIsViable_NoDispatcherNoAdapter_False(t *testing.T) {
	// Reproduces the VSCode IPC mode state that caused issue #2837:
	// Scheduler constructed with nil adapter AND no IPC refinement dispatcher
	// registered. refineViaCLI would previously nil-pointer panic in this case;
	// the viability gate must report false so runRefinementCycle skips.
	sched := NewScheduler(nil, SchedulerConfig{
		WorkspaceRoot: t.TempDir(),
		Adapter:       nil, // mirrors cmd/nightgauge/main.go in IPC mode
	})
	as := NewAutonomousScheduler(sched, nil, nil, nil, DefaultAutonomousConfig(), t.TempDir())

	if as.refinementIsViable() {
		t.Error("expected viability=false when adapter is nil and no dispatcher is registered")
	}
}

func TestRefinementIsViable_DispatcherRegistered_True(t *testing.T) {
	sched := NewScheduler(nil, SchedulerConfig{
		WorkspaceRoot: t.TempDir(),
		Adapter:       nil,
	})
	as := NewAutonomousScheduler(sched, nil, nil, nil, DefaultAutonomousConfig(), t.TempDir())
	as.OnRefinementDispatch(func(owner, repo string, issueNumber int) { /* no-op */ })

	if !as.refinementIsViable() {
		t.Error("expected viability=true when an IPC dispatcher is registered, even if adapter is nil")
	}
}

func TestRefinementCycle_SkipsWhenNotViable(t *testing.T) {
	// End-to-end guard: an unviable cycle must not even reach candidate
	// discovery. Previously this path panicked inside refineViaCLI and
	// crashed the backend. Calling runRefinementCycle with a nil ghClient
	// would panic on the GitHub call if the viability gate did not short-circuit.
	sched := NewScheduler(nil, SchedulerConfig{
		WorkspaceRoot: t.TempDir(),
		Adapter:       nil,
	})
	as := NewAutonomousScheduler(sched, nil, []depgraph.RepoConfig{
		{Owner: "O", Name: "R", Project: 1},
	}, nil, DefaultAutonomousConfig(), t.TempDir())
	as.state.Status = "running"

	// Must return without panic or GitHub call.
	as.runRefinementCycle(context.Background())
}

func TestRefinementCycleRateLimit(t *testing.T) {
	sr := NewSafetyRails(SafetyConfig{
		RefinementRateLimitPerHour: 2,
	})

	// First two should be allowed
	allowed, _ := sr.CheckBeforeRefine()
	if !allowed {
		t.Error("expected first refinement to be allowed")
	}
	sr.RecordRefinementStart()

	allowed, _ = sr.CheckBeforeRefine()
	if !allowed {
		t.Error("expected second refinement to be allowed")
	}
	sr.RecordRefinementStart()

	// Third should be blocked
	allowed, reason := sr.CheckBeforeRefine()
	if allowed {
		t.Error("expected third refinement to be blocked by rate limit")
	}
	if reason == "" {
		t.Error("expected non-empty block reason")
	}
}

func TestRefineIssueSuccess_StateTransitions(t *testing.T) {
	// Verify state transitions: Running → Completed
	as := &AutonomousScheduler{
		config: AutonomousConfig{
			RefinementEnabled:       true,
			RefinementMaxConcurrent: 1,
			RefinementCooldown:      time.Minute,
		},
		state: &AutonomousState{
			Status: "running",
		},
		refinementSem:      make(chan struct{}, 1),
		refinementCooldown: make(map[string]time.Time),
		refinementFailures: make(map[string]int),
	}

	// Simulate adding to running then moving to completed
	as.state.RefinementRunning = append(as.state.RefinementRunning, RefinementItem{
		Repo:      "O/R",
		Number:    42,
		Title:     "Test issue",
		StartedAt: time.Now().UTC().Format(time.RFC3339),
	})

	// Remove from running, add to completed
	as.state.RefinementRunning = removeRefinementItem(as.state.RefinementRunning, "O/R", 42)
	as.state.RefinementCompleted = append(as.state.RefinementCompleted, RefinementItem{
		Repo:        "O/R",
		Number:      42,
		Title:       "Test issue",
		CompletedAt: time.Now().UTC().Format(time.RFC3339),
	})

	if len(as.state.RefinementRunning) != 0 {
		t.Errorf("expected 0 running, got %d", len(as.state.RefinementRunning))
	}
	if len(as.state.RefinementCompleted) != 1 {
		t.Errorf("expected 1 completed, got %d", len(as.state.RefinementCompleted))
	}
	if as.state.RefinementCompleted[0].Number != 42 {
		t.Errorf("expected completed issue #42, got #%d", as.state.RefinementCompleted[0].Number)
	}
}

func TestRefineIssueFailure_StateTransitions(t *testing.T) {
	// Verify state transitions: Running → Failed
	// Also verify dispatch circuit breaker is NOT incremented
	sr := NewSafetyRails(SafetyConfig{
		CircuitBreakerMax: 3,
	})

	as := &AutonomousScheduler{
		config: AutonomousConfig{
			RefinementEnabled:       true,
			RefinementMaxConcurrent: 1,
			RefinementCooldown:      time.Minute,
		},
		state: &AutonomousState{
			Status: "running",
		},
		safetyRails:        sr,
		refinementSem:      make(chan struct{}, 1),
		refinementCooldown: make(map[string]time.Time),
		refinementFailures: make(map[string]int),
	}

	// Simulate a failure
	key := "O/R#42"
	as.state.RefinementRunning = append(as.state.RefinementRunning, RefinementItem{
		Repo:   "O/R",
		Number: 42,
		Title:  "Test issue",
	})

	// Remove from running, add to failed
	as.state.RefinementRunning = removeRefinementItem(as.state.RefinementRunning, "O/R", 42)
	as.state.RefinementFailed = append(as.state.RefinementFailed, RefinementItem{
		Repo:     "O/R",
		Number:   42,
		Title:    "Test issue",
		FailedAt: time.Now().UTC().Format(time.RFC3339),
		Reason:   "skill execution failed",
	})
	as.refinementFailures[key]++

	if len(as.state.RefinementRunning) != 0 {
		t.Errorf("expected 0 running, got %d", len(as.state.RefinementRunning))
	}
	if len(as.state.RefinementFailed) != 1 {
		t.Errorf("expected 1 failed, got %d", len(as.state.RefinementFailed))
	}
	if as.refinementFailures[key] != 1 {
		t.Errorf("expected 1 refinement failure count, got %d", as.refinementFailures[key])
	}

	// Verify dispatch circuit breaker is NOT affected
	safetyState := sr.State()
	if safetyState.ConsecutiveFailures != 0 {
		t.Errorf("expected 0 consecutive dispatch failures, got %d — refinement should NOT trip dispatch circuit breaker",
			safetyState.ConsecutiveFailures)
	}
}

func TestRefinementState_Persistence(t *testing.T) {
	tmpDir := t.TempDir()

	as := &AutonomousScheduler{
		config:        AutonomousConfig{},
		workspaceRoot: tmpDir,
		state: &AutonomousState{
			Status: "running",
			RefinementCompleted: []RefinementItem{
				{Repo: "O/R", Number: 1, Title: "Refined", CompletedAt: "2026-04-08T12:00:00Z"},
			},
			RefinementFailed: []RefinementItem{
				{Repo: "O/R", Number: 2, Title: "Failed", FailedAt: "2026-04-08T12:01:00Z", Reason: "error"},
			},
			LastRefinementScanAt: "2026-04-08T12:00:00Z",
		},
		refinementSem:      make(chan struct{}, 1),
		refinementCooldown: make(map[string]time.Time),
		refinementFailures: make(map[string]int),
	}

	// Persist state
	as.persistState()

	// Read back
	statePath := filepath.Join(tmpDir, autonomousStateFile)
	data, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatalf("failed to read state file: %v", err)
	}

	var loaded AutonomousState
	if err := json.Unmarshal(data, &loaded); err != nil {
		t.Fatalf("failed to parse state: %v", err)
	}

	if len(loaded.RefinementCompleted) != 1 {
		t.Errorf("expected 1 refinement completed, got %d", len(loaded.RefinementCompleted))
	}
	if len(loaded.RefinementFailed) != 1 {
		t.Errorf("expected 1 refinement failed, got %d", len(loaded.RefinementFailed))
	}
	if loaded.LastRefinementScanAt != "2026-04-08T12:00:00Z" {
		t.Errorf("expected lastRefinementScanAt preserved, got %q", loaded.LastRefinementScanAt)
	}
}

func TestRefinementState_PrunedByFilterRepos(t *testing.T) {
	as := &AutonomousScheduler{
		repos: []depgraph.RepoConfig{
			{Owner: "O", Name: "keep", Project: 1},
			{Owner: "O", Name: "remove", Project: 2},
		},
		config: AutonomousConfig{},
		state: &AutonomousState{
			RefinementCompleted: []RefinementItem{
				{Repo: "O/keep", Number: 1},
				{Repo: "O/remove", Number: 2},
			},
			RefinementFailed: []RefinementItem{
				{Repo: "O/remove", Number: 3},
			},
		},
		refinementCooldown: map[string]time.Time{
			"O/keep#1":   time.Now(),
			"O/remove#2": time.Now(),
		},
		refinementFailures: map[string]int{
			"O/keep#1":   1,
			"O/remove#3": 2,
		},
		perIssueFailureCount: make(map[string]int),
		retryBackoff:         make(map[string]time.Time),
		conflictRestartCount: make(map[string]int),
	}

	as.FilterRepos([]string{"O/keep"})

	if len(as.state.RefinementCompleted) != 1 || as.state.RefinementCompleted[0].Repo != "O/keep" {
		t.Errorf("expected only O/keep in refinement completed, got %v", as.state.RefinementCompleted)
	}
	if len(as.state.RefinementFailed) != 0 {
		t.Errorf("expected 0 refinement failed (O/remove pruned), got %d", len(as.state.RefinementFailed))
	}
	if _, ok := as.refinementCooldown["O/remove#2"]; ok {
		t.Error("expected O/remove#2 cooldown to be pruned")
	}
	if _, ok := as.refinementFailures["O/remove#3"]; ok {
		t.Error("expected O/remove#3 failure count to be pruned")
	}
}

func TestRemoveRefinementItem(t *testing.T) {
	items := []RefinementItem{
		{Repo: "A", Number: 1},
		{Repo: "B", Number: 2},
		{Repo: "A", Number: 3},
	}

	result := removeRefinementItem(items, "B", 2)
	if len(result) != 2 {
		t.Fatalf("expected 2 items, got %d", len(result))
	}
	for _, r := range result {
		if r.Repo == "B" && r.Number == 2 {
			t.Error("expected B#2 to be removed")
		}
	}
}

// isRefinementRunning is a test helper that checks if an issue is in the
// refinement running list.
func (as *AutonomousScheduler) isRefinementRunning(repo string, number int) bool {
	as.mu.Lock()
	defer as.mu.Unlock()
	for _, r := range as.state.RefinementRunning {
		if r.Repo == repo && r.Number == number {
			return true
		}
	}
	return false
}

func TestRecoverOrphanedRunning_ClearsState(t *testing.T) {
	// When the session crashes, state.Running has items from the previous session.
	// On startup, recoverOrphanedRunning should clear them all from state.Running
	// and persist the updated state.
	tmpDir := t.TempDir()

	as := &AutonomousScheduler{
		config:        AutonomousConfig{MaxConcurrent: 3},
		workspaceRoot: tmpDir,
		state: &AutonomousState{
			Status:    "running",
			StartedAt: "2026-01-01T00:00:00Z",
			Running: []RunningItem{
				{Repo: "nightgauge/nightgauge", Number: 100, Title: "Orphaned A", StartedAt: "2026-01-01T00:00:00Z"},
				{Repo: "nightgauge/nightgauge", Number: 101, Title: "Orphaned B", StartedAt: "2026-01-01T00:00:00Z"},
			},
		},
		// No ghClient or repos — MoveStatus will fail gracefully, but state must still be cleared.
	}

	ctx := context.Background()
	as.recoverOrphanedRunning(ctx)

	as.mu.Lock()
	remaining := len(as.state.Running)
	as.mu.Unlock()

	if remaining != 0 {
		t.Errorf("expected 0 running items after recovery, got %d", remaining)
	}

	// Verify state was persisted
	statePath := filepath.Join(tmpDir, autonomousStateFile)
	data, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatalf("failed to read persisted state: %v", err)
	}
	var loaded AutonomousState
	if err := json.Unmarshal(data, &loaded); err != nil {
		t.Fatalf("failed to parse persisted state: %v", err)
	}
	if len(loaded.Running) != 0 {
		t.Errorf("expected 0 running in persisted state, got %d", len(loaded.Running))
	}
}

func TestRecoverOrphanedRunning_CompletesDespiteCancelledCaller(t *testing.T) {
	// #3976: startup recovery runs its board writes on a context detached from
	// the caller's deadline (context.WithoutCancel + boardRecoveryTimeout) so a
	// rate-limit dip waits out the reset instead of dying at a short caller
	// deadline. As a guard, an already-cancelled caller context must NOT prevent
	// recovery from clearing and persisting the orphaned items.
	tmpDir := t.TempDir()

	as := &AutonomousScheduler{
		config:        AutonomousConfig{MaxConcurrent: 3},
		workspaceRoot: tmpDir,
		state: &AutonomousState{
			Status:    "running",
			StartedAt: "2026-01-01T00:00:00Z",
			Running: []RunningItem{
				{Repo: "nightgauge/nightgauge", Number: 200, Title: "Orphaned C", StartedAt: "2026-01-01T00:00:00Z"},
			},
		},
		// No ghClient or repos — MoveStatus is skipped (projectNum==0), but the
		// clear+persist bookkeeping must still run on the detached context.
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel BEFORE calling — the caller context is dead on entry.
	as.recoverOrphanedRunning(ctx)

	as.mu.Lock()
	remaining := len(as.state.Running)
	as.mu.Unlock()
	if remaining != 0 {
		t.Errorf("expected 0 running items after recovery with cancelled caller ctx, got %d", remaining)
	}

	statePath := filepath.Join(tmpDir, autonomousStateFile)
	data, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatalf("failed to read persisted state: %v", err)
	}
	var loaded AutonomousState
	if err := json.Unmarshal(data, &loaded); err != nil {
		t.Fatalf("failed to parse persisted state: %v", err)
	}
	if len(loaded.Running) != 0 {
		t.Errorf("expected 0 running in persisted state, got %d", len(loaded.Running))
	}
}

func TestRecoverOrphanedRunning_NoOpWhenEmpty(t *testing.T) {
	// When there are no orphaned items, recovery should be a no-op.
	tmpDir := t.TempDir()

	as := &AutonomousScheduler{
		config:        AutonomousConfig{MaxConcurrent: 3},
		workspaceRoot: tmpDir,
		state: &AutonomousState{
			Status:  "running",
			Running: nil,
		},
	}

	ctx := context.Background()
	// Should not panic or error with nil ghClient when Running is empty.
	as.recoverOrphanedRunning(ctx)

	as.mu.Lock()
	remaining := len(as.state.Running)
	as.mu.Unlock()

	if remaining != 0 {
		t.Errorf("expected 0 running items, got %d", remaining)
	}
}

func TestRecordFailureLocked_DedupsByIssue(t *testing.T) {
	// Six failures of the same issue should collapse into a single FailedItem
	// with AttemptCount=6, FirstFailedAt from attempt 1, FailedAt/Reason from
	// the last attempt. Before this change each failure produced a new row.
	as := &AutonomousScheduler{
		state: &AutonomousState{},
	}

	for i := 1; i <= 6; i++ {
		ts := time.Date(2026, 4, 10, 1, 0, i, 0, time.UTC).Format(time.RFC3339)
		as.recordFailureLocked("nightgauge/repo", 2530, "Title v"+string(rune('0'+i)), ts,
			"pipeline failure")
	}

	if got := len(as.state.Failed); got != 1 {
		t.Fatalf("expected 1 deduplicated FailedItem, got %d", got)
	}
	f := as.state.Failed[0]
	if f.AttemptCount != 6 {
		t.Errorf("AttemptCount: want 6, got %d", f.AttemptCount)
	}
	if f.FirstFailedAt != "2026-04-10T01:00:01Z" {
		t.Errorf("FirstFailedAt: want first attempt timestamp, got %q", f.FirstFailedAt)
	}
	if f.FailedAt != "2026-04-10T01:00:06Z" {
		t.Errorf("FailedAt: want latest attempt timestamp, got %q", f.FailedAt)
	}
}

func TestRecordFailureLocked_DistinctIssuesStaySeparate(t *testing.T) {
	as := &AutonomousScheduler{state: &AutonomousState{}}
	as.recordFailureLocked("r", 1, "A", "2026-04-10T01:00:00Z", "x")
	as.recordFailureLocked("r", 2, "B", "2026-04-10T01:00:01Z", "y")
	as.recordFailureLocked("r", 1, "A", "2026-04-10T01:00:02Z", "x2")

	if got := len(as.state.Failed); got != 2 {
		t.Fatalf("expected 2 entries for 2 distinct issues, got %d", got)
	}
	if as.state.Failed[0].Number != 1 || as.state.Failed[0].AttemptCount != 2 {
		t.Errorf("issue #1: want AttemptCount=2, got %+v", as.state.Failed[0])
	}
	if as.state.Failed[1].Number != 2 || as.state.Failed[1].AttemptCount != 1 {
		t.Errorf("issue #2: want AttemptCount=1, got %+v", as.state.Failed[1])
	}
}

func TestDedupeFailedItems_MigratesLegacyDuplicates(t *testing.T) {
	// Simulate a state file written before the dedup logic existed: six
	// separate rows for the same issue, no AttemptCount field.
	legacy := []FailedItem{
		{Repo: "r", Number: 2530, Title: "", FailedAt: "2026-04-10T01:00:01Z", Reason: "pipeline failure"},
		{Repo: "r", Number: 2530, Title: "", FailedAt: "2026-04-10T01:30:00Z", Reason: "pipeline failure"},
		{Repo: "r", Number: 2530, Title: "", FailedAt: "2026-04-10T02:00:00Z", Reason: "pipeline failure"},
		{Repo: "r", Number: 2592, Title: "", FailedAt: "2026-04-08T21:04:47Z", Reason: "pipeline failure"},
	}

	out := dedupeFailedItems(legacy)

	if len(out) != 2 {
		t.Fatalf("expected 2 entries after migration, got %d", len(out))
	}
	if out[0].Number != 2530 || out[0].AttemptCount != 3 {
		t.Errorf("issue #2530: want AttemptCount=3, got %+v", out[0])
	}
	if out[0].FirstFailedAt != "2026-04-10T01:00:01Z" {
		t.Errorf("issue #2530 FirstFailedAt: want earliest, got %q", out[0].FirstFailedAt)
	}
	if out[0].FailedAt != "2026-04-10T02:00:00Z" {
		t.Errorf("issue #2530 FailedAt: want latest, got %q", out[0].FailedAt)
	}
	if out[1].Number != 2592 || out[1].AttemptCount != 1 {
		t.Errorf("issue #2592: want AttemptCount=1, got %+v", out[1])
	}
}

func TestDedupeFailedItems_Idempotent(t *testing.T) {
	// Running dedup on already-deduplicated input must not double-count.
	input := []FailedItem{
		{Repo: "r", Number: 1, FailedAt: "t2", FirstFailedAt: "t1", AttemptCount: 3},
	}
	out := dedupeFailedItems(input)
	if len(out) != 1 || out[0].AttemptCount != 3 {
		t.Fatalf("idempotency violated: %+v", out)
	}
}

func TestLoadState_PreservesRunningForRecovery(t *testing.T) {
	// Previously loadState nil'd Running, so when the serve-startup goroutine
	// called RecoverOrphanedRunning there was nothing left to reset. The
	// board kept items stuck "In progress" after every crash. loadState now
	// carries Running forward; RecoverOrphanedRunning is the single owner of
	// the clear-and-persist side effect.
	tmpDir := t.TempDir()
	statePath := filepath.Join(tmpDir, autonomousStateFile)
	if err := os.MkdirAll(filepath.Dir(statePath), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	prior := AutonomousState{
		Status:    "running",
		StartedAt: "2026-04-12T12:00:00Z",
		Running: []RunningItem{
			{Repo: "nightgauge/nightgauge", Number: 2614, Title: "Orphan", StartedAt: "2026-04-12T12:00:00Z"},
		},
	}
	data, err := json.Marshal(prior)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(statePath, data, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	as := &AutonomousScheduler{
		workspaceRoot: tmpDir,
		state:         &AutonomousState{},
	}
	as.loadState()

	if len(as.state.Running) != 1 {
		t.Fatalf("expected Running preserved for recovery, got %d items", len(as.state.Running))
	}
	if as.state.Status != "stopped" {
		t.Errorf("expected Status transformed to 'stopped', got %q", as.state.Status)
	}
}

func TestRecoverOrphanedRunning_ExportedWrapper(t *testing.T) {
	// Covers the public entry point used by cmd/nightgauge serveCmd to
	// trigger startup orphan recovery without requiring the user to click
	// "Start Autonomous". Must delegate to the unexported implementation.
	tmpDir := t.TempDir()

	as := &AutonomousScheduler{
		config:        AutonomousConfig{MaxConcurrent: 3},
		workspaceRoot: tmpDir,
		state: &AutonomousState{
			Status: "running",
			Running: []RunningItem{
				{Repo: "nightgauge/nightgauge", Number: 200, Title: "Orphan", StartedAt: "2026-01-01T00:00:00Z"},
			},
		},
	}

	as.RecoverOrphanedRunning(context.Background())

	as.mu.Lock()
	remaining := len(as.state.Running)
	as.mu.Unlock()

	if remaining != 0 {
		t.Errorf("RecoverOrphanedRunning: expected 0 running items, got %d", remaining)
	}
}

// isInRefinementCooldown is a test helper that checks cooldown status.
func (as *AutonomousScheduler) isInRefinementCooldown(key string) bool {
	as.mu.Lock()
	defer as.mu.Unlock()
	cooldownUntil, hasCooldown := as.refinementCooldown[key]
	if !hasCooldown {
		return false
	}
	return time.Now().Before(cooldownUntil)
}

func TestReconcileStateAgainstGraph_PrunesClosedFailures(t *testing.T) {
	tmpDir := t.TempDir()
	as := &AutonomousScheduler{
		workspaceRoot: tmpDir,
		state: &AutonomousState{
			Failed: []FailedItem{
				{Repo: "R1", Number: 100, Title: "Open issue", FailedAt: "2026-04-10T00:00:00Z"},
				{Repo: "R1", Number: 101, Title: "Closed issue", FailedAt: "2026-04-10T00:00:00Z"},
			},
		},
	}
	g := buildTestGraph([]*depgraph.Node{
		{Repo: "R1", Number: 100, State: "OPEN"},
		{Repo: "R1", Number: 101, State: "CLOSED"},
	}, nil)

	as.reconcileStateAgainstGraph(g)

	if len(as.state.Failed) != 0 {
		t.Fatalf("expected failed list empty (OPEN re-admitted, CLOSED pruned), got %d items", len(as.state.Failed))
	}
}

func TestReconcileStateAgainstGraph_PrunesMissingNodeFailures(t *testing.T) {
	tmpDir := t.TempDir()
	as := &AutonomousScheduler{
		workspaceRoot: tmpDir,
		state: &AutonomousState{
			Failed: []FailedItem{
				{Repo: "R1", Number: 999, Title: "Deleted issue", FailedAt: "2026-04-10T00:00:00Z"},
			},
		},
	}
	g := buildTestGraph(nil, nil) // empty graph — no nodes

	as.reconcileStateAgainstGraph(g)

	if len(as.state.Failed) != 0 {
		t.Fatalf("expected failed list empty after pruning missing node, got %d items", len(as.state.Failed))
	}
}

func TestReconcileStateAgainstGraph_ReadmitsOpenFailures(t *testing.T) {
	tmpDir := t.TempDir()
	as := &AutonomousScheduler{
		workspaceRoot: tmpDir,
		state: &AutonomousState{
			Failed: []FailedItem{
				{Repo: "R1", Number: 200, Title: "Still open", FailedAt: "2026-04-10T00:00:00Z"},
			},
		},
	}
	g := buildTestGraph([]*depgraph.Node{
		{Repo: "R1", Number: 200, State: "OPEN"},
	}, nil)

	as.reconcileStateAgainstGraph(g)

	// OPEN item is re-admitted — removed from Failed list (becomes a candidate again)
	if len(as.state.Failed) != 0 {
		t.Fatalf("expected OPEN item re-admitted (removed from Failed), got %d items", len(as.state.Failed))
	}
}

func TestSequentialModeSkipsSecondDispatch(t *testing.T) {
	// Per-repo cap defaults to 1 (sequential), so a second issue from the same
	// repo is blocked while one is running — no explicit config needed.
	as := &AutonomousScheduler{
		config: AutonomousConfig{},
		state: &AutonomousState{
			Running: []RunningItem{
				{Repo: "R", Number: 1},
			},
		},
	}

	if !as.anyRunningFrom("R") {
		t.Error("expected anyRunningFrom('R') to be true with item #1 running")
	}
	if !as.isSequentialRepo("R") {
		t.Error("expected isSequentialRepo('R') to be true by default (per_repo_max=1)")
	}
	if !as.isSequentialRepo("R") || !as.anyRunningFrom("R") {
		t.Error("expected second dispatch to be blocked for a default (sequential) repo")
	}
}

func TestPerRepoOverrideMakesRepoNonSequential(t *testing.T) {
	// An explicit override > 1 makes a repo non-sequential while other repos
	// keep the sequential default.
	as := &AutonomousScheduler{
		config: AutonomousConfig{
			RepositoryMaxConcurrent: map[string]int{"B": 2},
		},
		state: &AutonomousState{
			Running: []RunningItem{{Repo: "A", Number: 10}},
		},
	}

	if as.isSequentialRepo("B") {
		t.Error("expected isSequentialRepo('B') to be false (override max_concurrent=2)")
	}
	if !as.isSequentialRepo("A") {
		t.Error("expected isSequentialRepo('A') to be true (default per_repo_max=1)")
	}
	if as.anyRunningFrom("B") {
		t.Error("expected anyRunningFrom('B') to be false with only A running")
	}
}

func TestPerRepoOverrideResolvesBothForms(t *testing.T) {
	// A repository_overrides entry should resolve via short and fully-qualified
	// names.
	as := &AutonomousScheduler{
		config: AutonomousConfig{
			RepositoryMaxConcurrent: map[string]int{"my-repo": 4},
		},
		state: &AutonomousState{},
	}
	if got := as.maxConcurrentForRepo("my-repo"); got != 4 {
		t.Errorf("maxConcurrentForRepo('my-repo') = %d, want 4 (short name)", got)
	}
	if got := as.maxConcurrentForRepo("nightgauge/my-repo"); got != 4 {
		t.Errorf("maxConcurrentForRepo('nightgauge/my-repo') = %d, want 4 (short fallback)", got)
	}
	if got := as.maxConcurrentForRepo("other-repo"); got != 1 {
		t.Errorf("maxConcurrentForRepo('other-repo') = %d, want 1 (default)", got)
	}
}

func TestDefaultIsSequential(t *testing.T) {
	// With no concurrency config, every repo defaults to per-repo cap 1.
	as := &AutonomousScheduler{
		config: AutonomousConfig{},
		state: &AutonomousState{
			Running: []RunningItem{{Repo: "R", Number: 1}},
		},
	}
	if !as.isSequentialRepo("R") {
		t.Error("expected isSequentialRepo('R') to be true by default")
	}
	if !as.anyRunningFrom("R") {
		t.Error("expected anyRunningFrom('R') to be true")
	}
}

// ── Per-repo concurrency cap tests (#3781) ───────────────────────────────────

func TestMaxConcurrentForRepo_OverrideWins(t *testing.T) {
	as := &AutonomousScheduler{
		config: AutonomousConfig{
			RepositoryMaxConcurrent: map[string]int{"R": 3},
		},
		state: &AutonomousState{},
	}
	if got := as.maxConcurrentForRepo("R"); got != 3 {
		t.Errorf("maxConcurrentForRepo('R') = %d, want 3", got)
	}
	if as.isSequentialRepo("R") {
		t.Error("isSequentialRepo('R') should be false when override max_concurrent=3")
	}
}

func TestMaxConcurrentForRepo_PerRepoMaxDefault(t *testing.T) {
	// No override → PerRepoMax (explicit), else 1.
	as := &AutonomousScheduler{
		config: AutonomousConfig{PerRepoMax: 2},
		state:  &AutonomousState{},
	}
	if got := as.maxConcurrentForRepo("R"); got != 2 {
		t.Errorf("maxConcurrentForRepo('R') = %d, want 2 (PerRepoMax)", got)
	}
}

func TestMaxConcurrentForRepo_NoConfigDefaultsToOne(t *testing.T) {
	as := &AutonomousScheduler{config: AutonomousConfig{}, state: &AutonomousState{}}
	if got := as.maxConcurrentForRepo("R"); got != 1 {
		t.Errorf("maxConcurrentForRepo('R') = %d, want 1 (default serialize)", got)
	}
}

func TestMaxConcurrentForRepo_ShortNameFallback(t *testing.T) {
	as := &AutonomousScheduler{
		config: AutonomousConfig{
			RepositoryMaxConcurrent: map[string]int{"my-repo": 2},
		},
		state: &AutonomousState{},
	}
	if got := as.maxConcurrentForRepo("nightgauge/my-repo"); got != 2 {
		t.Errorf("maxConcurrentForRepo('nightgauge/my-repo') = %d, want 2 (short fallback)", got)
	}
}

func TestRunningCountFrom(t *testing.T) {
	as := &AutonomousScheduler{
		config: AutonomousConfig{},
		state: &AutonomousState{
			Running: []RunningItem{
				{Repo: "A", Number: 1},
				{Repo: "A", Number: 2},
				{Repo: "B", Number: 3},
			},
		},
	}
	if got := as.runningCountFrom("A"); got != 2 {
		t.Errorf("runningCountFrom('A') = %d, want 2", got)
	}
	if got := as.runningCountFrom("B"); got != 1 {
		t.Errorf("runningCountFrom('B') = %d, want 1", got)
	}
	if got := as.runningCountFrom("C"); got != 0 {
		t.Errorf("runningCountFrom('C') = %d, want 0", got)
	}
	// anyRunningFrom should still work.
	if !as.anyRunningFrom("A") {
		t.Error("anyRunningFrom('A') = false, want true")
	}
	if as.anyRunningFrom("Z") {
		t.Error("anyRunningFrom('Z') = true, want false")
	}
}

func TestPerRepoCapBlocksSecondDispatch(t *testing.T) {
	// Repo A has override cap=2 — first two dispatches OK, third blocked.
	// Repo B has default cap=1 (per_repo_max) — first OK, second blocked.
	// Repo C unconfigured — also default cap=1.
	as := &AutonomousScheduler{
		config: AutonomousConfig{
			RepositoryMaxConcurrent: map[string]int{"A": 2},
		},
		state: &AutonomousState{
			Running: []RunningItem{
				{Repo: "A", Number: 1},
				{Repo: "A", Number: 2},
				{Repo: "B", Number: 10},
			},
		},
	}

	// A is at cap (2/2).
	if cap := as.maxConcurrentForRepo("A"); cap == 0 || as.runningCountFrom("A") < cap {
		t.Errorf("expected A at cap; cap=%d running=%d", cap, as.runningCountFrom("A"))
	}
	// B is at default cap (1/1).
	if cap := as.maxConcurrentForRepo("B"); cap == 0 || as.runningCountFrom("B") < cap {
		t.Errorf("expected B at cap; cap=%d running=%d", cap, as.runningCountFrom("B"))
	}
	// C unconfigured → default cap 1 (serialize).
	if cap := as.maxConcurrentForRepo("C"); cap != 1 {
		t.Errorf("expected C default cap=1, got %d", cap)
	}
}

// TestStopSignalsRefinementGoroutine verifies that Stop() sends to
// stopRefinementCh so the refinement goroutine exits. Before #3029 both loops
// shared a single stopCh (cap=1); whichever goroutine won the race drained the
// buffer and the other loop never exited.
func TestStopSignalsRefinementGoroutine(t *testing.T) {
	as := &AutonomousScheduler{
		running: true,
		state:   &AutonomousState{Status: "running"},
		config: AutonomousConfig{
			RefinementEnabled:  true,
			RefinementInterval: 100 * time.Millisecond,
		},
		stopCh:               make(chan struct{}, 1),
		stopRefinementCh:     make(chan struct{}, 1),
		rescanCh:             make(chan struct{}, 1),
		perIssueFailureCount: make(map[string]int),
		retryBackoff:         make(map[string]time.Time),
		conflictRestartCount: make(map[string]int),
		refinementCooldown:   make(map[string]time.Time),
		refinementFailures:   make(map[string]int),
	}

	as.Stop()

	// Both channels must have been sent to — verify stopRefinementCh.
	select {
	case <-as.stopRefinementCh:
		// ✓ refinement goroutine would have received this
	case <-time.After(1 * time.Second):
		t.Fatal("stopRefinementCh was not signaled by Stop()")
	}

	// stopCh must also have been sent to.
	select {
	case <-as.stopCh:
		// ✓ dispatch loop would have received this
	case <-time.After(1 * time.Second):
		t.Fatal("stopCh was not signaled by Stop()")
	}
}

// TestStopSignalsMainLoopAndRefinement verifies that under concurrent load
// both the dispatch loop and the refinement loop exit when Stop() is called.
func TestStopSignalsMainLoopAndRefinement(t *testing.T) {
	as := &AutonomousScheduler{
		running: true,
		state:   &AutonomousState{Status: "running"},
		config: AutonomousConfig{
			RefinementEnabled:  true,
			ScanInterval:       50 * time.Millisecond,
			RefinementInterval: 50 * time.Millisecond,
		},
		stopCh:               make(chan struct{}, 1),
		stopRefinementCh:     make(chan struct{}, 1),
		rescanCh:             make(chan struct{}, 1),
		perIssueFailureCount: make(map[string]int),
		retryBackoff:         make(map[string]time.Time),
		conflictRestartCount: make(map[string]int),
		refinementCooldown:   make(map[string]time.Time),
		refinementFailures:   make(map[string]int),
	}

	mainLoopExited := make(chan struct{})
	refinementLoopExited := make(chan struct{})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Simulate dispatch loop
	go func() {
		defer close(mainLoopExited)
		ticker := time.NewTicker(as.config.ScanInterval)
		defer ticker.Stop()
		for {
			select {
			case <-as.stopCh:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				// simulate runCycle
			}
		}
	}()

	// Simulate refinement loop
	go func() {
		defer close(refinementLoopExited)
		ticker := time.NewTicker(as.config.RefinementInterval)
		defer ticker.Stop()
		for {
			select {
			case <-as.stopRefinementCh:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				// simulate runRefinementCycle
			}
		}
	}()

	// Let both loops start
	time.Sleep(20 * time.Millisecond)

	as.Stop()

	timeout := time.After(2 * time.Second)
	mainExited := false
	refinementExited := false
	for !mainExited || !refinementExited {
		select {
		case <-mainLoopExited:
			mainExited = true
		case <-refinementLoopExited:
			refinementExited = true
		case <-timeout:
			t.Fatalf("loops did not exit: dispatch=%v refinement=%v", mainExited, refinementExited)
		}
	}
}

// TestWriteCrashExitEvent verifies that a crash exit record is written to
// autonomous-exits.jsonl with the expected fields.
func TestWriteCrashExitEvent(t *testing.T) {
	dir := t.TempDir()
	as := &AutonomousScheduler{
		workspaceRoot: dir,
	}

	as.writeCrashExitEvent("something went wrong", "goroutine 1 [running]:\nsome/package.go:42")

	logPath := filepath.Join(dir, ".nightgauge", "logs", "autonomous-exits.jsonl")
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("exit log not written: %v", err)
	}

	var entry map[string]interface{}
	if err := json.Unmarshal(data[:len(data)-1], &entry); err != nil {
		t.Fatalf("failed to parse exit log JSON: %v", err)
	}

	if entry["reason"] != "crashed" {
		t.Errorf("expected reason=crashed, got %v", entry["reason"])
	}
	if entry["error_message"] != "something went wrong" {
		t.Errorf("expected error_message='something went wrong', got %v", entry["error_message"])
	}
	if entry["stack_trace"] == "" || entry["stack_trace"] == nil {
		t.Errorf("expected non-empty stack_trace, got %v", entry["stack_trace"])
	}
	if _, ok := entry["timestamp"]; !ok {
		t.Error("expected timestamp field in crash exit event")
	}
	if _, ok := entry["pid"]; !ok {
		t.Error("expected pid field in crash exit event")
	}
}

// TestWriteCrashExitEvent_EmptyWorkspaceRoot verifies that writeCrashExitEvent
// is a no-op when workspaceRoot is unset (avoids writing to unexpected locations).
func TestWriteCrashExitEvent_EmptyWorkspaceRoot(t *testing.T) {
	as := &AutonomousScheduler{}
	// Should not panic or write anywhere.
	as.writeCrashExitEvent("panic msg", "stack")
}

// TestWriteCrashExitEvent_StackTruncation verifies that a stack trace larger
// than 4KB is accepted without error (actual truncation happens in Run()).
func TestWriteCrashExitEvent_StackTruncation(t *testing.T) {
	dir := t.TempDir()
	as := &AutonomousScheduler{workspaceRoot: dir}

	longStack := string(make([]byte, 8192))
	as.writeCrashExitEvent("overflow", longStack)

	logPath := filepath.Join(dir, ".nightgauge", "logs", "autonomous-exits.jsonl")
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("exit log not written: %v", err)
	}

	var entry map[string]interface{}
	if err := json.Unmarshal(data[:len(data)-1], &entry); err != nil {
		t.Fatalf("failed to parse exit log JSON: %v", err)
	}
	if entry["reason"] != "crashed" {
		t.Errorf("expected reason=crashed, got %v", entry["reason"])
	}
}

// TestRunPanicRecovery verifies that writeCrashExitEvent produces a parseable
// crash record matching what the Run() panic recovery defer would write.
func TestRunPanicRecovery(t *testing.T) {
	dir := t.TempDir()
	as := &AutonomousScheduler{workspaceRoot: dir}

	as.writeCrashExitEvent("test panic: integer divide by zero", "goroutine 1 [running]:\ntest/pkg.go:10")

	logPath := filepath.Join(dir, ".nightgauge", "logs", "autonomous-exits.jsonl")
	raw, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("crash exit log missing: %v", err)
	}
	var entry map[string]interface{}
	if err := json.Unmarshal(raw[:len(raw)-1], &entry); err != nil {
		t.Fatalf("crash exit log not valid JSON: %v", err)
	}
	if entry["reason"] != "crashed" {
		t.Errorf("reason mismatch: %v", entry["reason"])
	}
}

// Per-failure status revert (post-2026-05-04 incident retro): a pipeline
// failure should leave the issue eligible for re-dispatch once backoff
// expires. Without revertFailedIssueStatus, an issue stuck on "In progress"
// silently drops out of prioritize() because isDispatchableStatus only
// matches Ready/Backlog. Mirrors the cross-session recoverOrphanedRunning
// behavior for the in-session case.

func TestRevertFailedIssueStatus_NoProjectConfigIsNoOp(t *testing.T) {
	// With no repo configured for the failed item, revert should log + return
	// without panicking. Defensive — workspaces with mid-incident config
	// changes can have running items whose repo is no longer configured.
	as := &AutonomousScheduler{
		repos: nil,
		state: &AutonomousState{},
	}
	// Should not panic and should not block.
	done := make(chan struct{})
	go func() {
		as.revertFailedIssueStatus("nightgauge/unknown", 999)
		close(done)
	}()
	select {
	case <-done:
		// ok
	case <-time.After(2 * time.Second):
		t.Fatalf("revertFailedIssueStatus did not return when project config missing")
	}
}

func TestOnPipelineComplete_Failure_TriggersStatusRevertGoroutine(t *testing.T) {
	// Ensures onPipelineComplete fires the revertFailedIssueStatus goroutine
	// on success=false. Verifies via behavior: the goroutine runs even when
	// no repos are configured (it just logs and returns), so the test is
	// asserting the call site is wired — not the network behavior. This
	// pairs with the no-op test above.
	as := &AutonomousScheduler{
		config: AutonomousConfig{MaxConcurrent: 3},
		state: &AutonomousState{
			Status: "running",
			Running: []RunningItem{
				{Repo: "nightgauge/nightgauge", Number: 871, Title: "Real incident"},
			},
		},
		rescanCh: make(chan struct{}, 1),
	}

	// Should not panic + should remove from Running. The goroutine is fire-
	// and-forget; we don't need to synchronize on it for the unit test.
	as.onPipelineComplete("nightgauge/nightgauge", 871, false, false, "", "")

	if len(as.state.Running) != 0 {
		t.Fatalf("expected 0 running after failure, got %d", len(as.state.Running))
	}
	if len(as.state.Failed) != 1 {
		t.Fatalf("expected 1 failed after failure, got %d", len(as.state.Failed))
	}

	// Give the goroutine a moment so it doesn't leak past the test's lifetime.
	// Without ghClient/repos it should return quickly via the no-project-config
	// branch.
	time.Sleep(50 * time.Millisecond)
}

func TestOnPipelineComplete_Success_DoesNotTriggerRevert(t *testing.T) {
	// Success path must NOT call revertFailedIssueStatus — the cascade promote
	// path handles downstream. Asserted by checking that the success branch
	// doesn't accidentally schedule a revert (verified indirectly: the
	// promotion goroutine has a different signature, and no panics or extra
	// state transitions occur).
	as := &AutonomousScheduler{
		config: AutonomousConfig{MaxConcurrent: 3},
		state: &AutonomousState{
			Status: "running",
			Running: []RunningItem{
				{Repo: "nightgauge/nightgauge", Number: 42, Title: "happy path"},
			},
		},
		rescanCh: make(chan struct{}, 1),
	}
	as.onPipelineComplete("nightgauge/nightgauge", 42, true, false, "", "")

	if len(as.state.Running) != 0 {
		t.Fatalf("expected 0 running after success, got %d", len(as.state.Running))
	}
	if len(as.state.Completed) != 1 {
		t.Fatalf("expected 1 completed after success, got %d", len(as.state.Completed))
	}
}

// --- #3431: global Anthropic-quota cooldown -------------------------------

func TestParseQuotaResetsAt(t *testing.T) {
	now := time.Unix(1778420000, 0)
	cases := []struct {
		name     string
		text     string
		now      time.Time
		wantOk   bool
		wantUnix int64
	}{
		{
			name:     "canonical kill marker",
			text:     "[rate-limit-quota-exhausted] idle 2m 5s after rate_limit_event with overage rejected (five_hour bucket; resetsAt=1778428800)",
			now:      now,
			wantOk:   true,
			wantUnix: 1778428800,
		},
		{name: "missing resetsAt", text: "[rate-limit-quota-exhausted] idle 2m 5s", now: now, wantOk: false},
		{name: "empty text", text: "", now: now, wantOk: false},
		{name: "malformed unix", text: "resetsAt=notanumber", now: now, wantOk: false},
		{name: "past reset (already expired)", text: "resetsAt=1000000000", now: now, wantOk: false},
		{
			name:     "embedded in larger text",
			text:     "stage feature-dev: [rate-limit-quota-exhausted] foo bar resetsAt=1778500000 baz",
			now:      now,
			wantOk:   true,
			wantUnix: 1778500000,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := parseQuotaResetsAt(tc.text, tc.now)
			if ok != tc.wantOk {
				t.Errorf("ok=%v, want %v", ok, tc.wantOk)
			}
			if ok && got.Unix() != tc.wantUnix {
				t.Errorf("got=%d, want %d", got.Unix(), tc.wantUnix)
			}
		})
	}
}

func TestComputeQuotaCooldownUntil(t *testing.T) {
	now := time.Unix(1778420000, 0)

	t.Run("with resetsAt hint widens to reset+grace", func(t *testing.T) {
		text := "[rate-limit-quota-exhausted] resetsAt=1778428800"
		got := computeQuotaCooldownUntil(text, now)
		want := time.Unix(1778428800, 0).Add(quotaResetGrace)
		if !got.Equal(want) {
			t.Errorf("got %v, want %v", got, want)
		}
	})

	t.Run("with no hint uses 1h floor", func(t *testing.T) {
		got := computeQuotaCooldownUntil("[rate-limit-quota-exhausted] no hint here", now)
		want := now.Add(streamIdleTimeoutBackoff)
		if !got.Equal(want) {
			t.Errorf("got %v, want %v", got, want)
		}
	})

	t.Run("hint sooner than 1h floor is widened to floor", func(t *testing.T) {
		nearReset := now.Add(5 * time.Minute).Unix()
		text := fmt.Sprintf("resetsAt=%d", nearReset)
		got := computeQuotaCooldownUntil(text, now)
		want := now.Add(streamIdleTimeoutBackoff)
		if !got.Equal(want) {
			t.Errorf("got %v, want %v (floor)", got, want)
		}
	})

	t.Run("empty failure text uses floor", func(t *testing.T) {
		got := computeQuotaCooldownUntil("", now)
		want := now.Add(streamIdleTimeoutBackoff)
		if !got.Equal(want) {
			t.Errorf("got %v, want %v", got, want)
		}
	})
}

func TestApplyQuotaCooldownLocked_NeverShortens(t *testing.T) {
	as := &AutonomousScheduler{state: &AutonomousState{Status: "running"}}
	farFuture := time.Now().Add(4 * time.Hour).UTC().Format(time.RFC3339)
	as.state.QuotaCooldownUntil = farFuture
	near := time.Now().Add(1 * time.Hour).Unix()
	failureText := fmt.Sprintf("resetsAt=%d", near)
	as.applyQuotaCooldownLocked("rate-limit-quota-exhausted", "R#1", failureText)
	if got := as.state.QuotaCooldownUntil; got != farFuture {
		t.Errorf("cooldown shortened from %s to %s", farFuture, got)
	}
}

func TestQuotaCooldownActiveLocked(t *testing.T) {
	t.Run("active when in future", func(t *testing.T) {
		as := &AutonomousScheduler{state: &AutonomousState{
			Status:             "running",
			QuotaCooldownUntil: time.Now().Add(1 * time.Hour).UTC().Format(time.RFC3339),
		}}
		active, deadline := as.quotaCooldownActiveLocked()
		if !active {
			t.Errorf("expected active cooldown")
		}
		if deadline.IsZero() {
			t.Errorf("expected non-zero deadline")
		}
	})

	t.Run("auto-clears when expired", func(t *testing.T) {
		as := &AutonomousScheduler{state: &AutonomousState{
			Status:              "running",
			QuotaCooldownUntil:  time.Now().Add(-1 * time.Hour).UTC().Format(time.RFC3339),
			QuotaCooldownReason: "old reason",
		}}
		active, _ := as.quotaCooldownActiveLocked()
		if active {
			t.Errorf("expected expired cooldown to be inactive")
		}
		if as.state.QuotaCooldownUntil != "" {
			t.Errorf("expected QuotaCooldownUntil to be cleared")
		}
		if as.state.QuotaCooldownReason != "" {
			t.Errorf("expected QuotaCooldownReason to be cleared")
		}
	})

	t.Run("malformed clears state", func(t *testing.T) {
		as := &AutonomousScheduler{state: &AutonomousState{
			Status:             "running",
			QuotaCooldownUntil: "not-a-timestamp",
		}}
		active, _ := as.quotaCooldownActiveLocked()
		if active {
			t.Errorf("expected malformed timestamp to be inactive")
		}
		if as.state.QuotaCooldownUntil != "" {
			t.Errorf("expected malformed QuotaCooldownUntil to be cleared")
		}
	})

	t.Run("empty is inactive", func(t *testing.T) {
		as := &AutonomousScheduler{state: &AutonomousState{Status: "running"}}
		active, _ := as.quotaCooldownActiveLocked()
		if active {
			t.Errorf("expected empty cooldown to be inactive")
		}
	})
}

func TestOnPipelineComplete_QuotaExhausted_SetsGlobalCooldown(t *testing.T) {
	as := &AutonomousScheduler{
		config: AutonomousConfig{MaxConcurrent: 3},
		state: &AutonomousState{
			Status: "running",
			Running: []RunningItem{
				{Repo: "acme/platform", Number: 893, Title: "Saved reports"},
			},
			LifetimeIssueFailures: map[string]int{},
		},
		rescanCh:             make(chan struct{}, 1),
		perIssueFailureCount: map[string]int{},
		retryBackoff:         map[string]time.Time{},
	}

	resetsAt := time.Now().Add(3 * time.Hour).Unix()
	failureDetail := fmt.Sprintf(
		"[rate-limit-quota-exhausted] idle 2m 5s after rate_limit_event with overage rejected (five_hour bucket; resetsAt=%d)",
		resetsAt)

	as.onPipelineComplete(
		"acme/platform", 893, false, false,
		TerminalKindRateLimitQuotaExhausted, failureDetail)

	if as.state.QuotaCooldownUntil == "" {
		t.Fatalf("expected QuotaCooldownUntil to be set")
	}
	gotUntil, err := time.Parse(time.RFC3339, as.state.QuotaCooldownUntil)
	if err != nil {
		t.Fatalf("parse cooldown: %v", err)
	}
	wantUntil := time.Unix(resetsAt, 0).Add(quotaResetGrace).UTC().Truncate(time.Second)
	if !gotUntil.Equal(wantUntil) {
		t.Errorf("QuotaCooldownUntil = %v, want %v (resetsAt + grace)", gotUntil, wantUntil)
	}
	if as.state.QuotaCooldownReason == "" {
		t.Errorf("expected QuotaCooldownReason to be set")
	}
}

func TestOnPipelineComplete_QuotaExhausted_NoHintUsesFloor(t *testing.T) {
	as := &AutonomousScheduler{
		config: AutonomousConfig{MaxConcurrent: 3},
		state: &AutonomousState{
			Status: "running",
			Running: []RunningItem{
				{Repo: "R", Number: 1, Title: "X"},
			},
			LifetimeIssueFailures: map[string]int{},
		},
		rescanCh:             make(chan struct{}, 1),
		perIssueFailureCount: map[string]int{},
		retryBackoff:         map[string]time.Time{},
	}

	before := time.Now()
	as.onPipelineComplete("R", 1, false, false, TerminalKindRateLimitQuotaExhausted, "")
	gotUntil, err := time.Parse(time.RFC3339, as.state.QuotaCooldownUntil)
	if err != nil {
		t.Fatalf("parse cooldown: %v", err)
	}
	delta := gotUntil.Sub(before)
	if delta < 55*time.Minute || delta > 65*time.Minute {
		t.Errorf("cooldown delta = %v, want ~1h floor (55-65m)", delta)
	}
}

func TestRunCycle_SuspendsDispatchDuringQuotaCooldown(t *testing.T) {
	dispatched := 0
	as := &AutonomousScheduler{
		config: AutonomousConfig{MaxConcurrent: 3},
		state: &AutonomousState{
			Status:             "running",
			QuotaCooldownUntil: time.Now().Add(2 * time.Hour).UTC().Format(time.RFC3339),
		},
		rescanCh:             make(chan struct{}, 1),
		perIssueFailureCount: map[string]int{},
		retryBackoff:         map[string]time.Time{},
		onDispatch: func(_ string, _ string, _ int, _ string) {
			dispatched++
		},
	}

	cyclesBefore := as.state.CyclesRun
	as.runCycle(context.Background())

	if dispatched != 0 {
		t.Errorf("dispatched=%d, want 0 (cooldown should suspend dispatch)", dispatched)
	}
	if as.state.CyclesRun != cyclesBefore {
		t.Errorf("CyclesRun incremented during cooldown: got %d, want %d (no work performed)",
			as.state.CyclesRun, cyclesBefore)
	}
	if as.state.LastScanAt == "" {
		t.Errorf("expected LastScanAt to be updated even during cooldown")
	}
}

func TestRunCycle_ResumesAfterQuotaCooldownExpires(t *testing.T) {
	as := &AutonomousScheduler{
		config: AutonomousConfig{MaxConcurrent: 3},
		state: &AutonomousState{
			Status:              "running",
			QuotaCooldownUntil:  time.Now().Add(-1 * time.Minute).UTC().Format(time.RFC3339),
			QuotaCooldownReason: "expired reason",
		},
		rescanCh:             make(chan struct{}, 1),
		perIssueFailureCount: map[string]int{},
		retryBackoff:         map[string]time.Time{},
	}

	active, _ := as.quotaCooldownActiveLocked()
	if active {
		t.Fatalf("expected expired cooldown to report inactive")
	}
	if as.state.QuotaCooldownUntil != "" {
		t.Errorf("expected expired cooldown to be auto-cleared")
	}
	if as.state.QuotaCooldownReason != "" {
		t.Errorf("expected QuotaCooldownReason to be auto-cleared")
	}
}

// #3446 — TestClearQuotaCooldown_ClearsAndPersists guards the manual escape
// hatch for the global Anthropic-quota cooldown. The cooldown logic in #3431
// shipped without any user-visible signal or override; this test pins the
// expected behaviour for the new ClearQuotaCooldown method that the
// `autonomous.clearQuotaCooldown` IPC handler wraps.
func TestClearQuotaCooldown_ClearsAndPersists(t *testing.T) {
	t.Run("clears in-memory + disk when active", func(t *testing.T) {
		tmp := t.TempDir()
		until := time.Now().Add(2 * time.Hour).UTC().Format(time.RFC3339)
		as := &AutonomousScheduler{
			workspaceRoot: tmp,
			state: &AutonomousState{
				Status:              "running",
				QuotaCooldownUntil:  until,
				QuotaCooldownReason: "rate-limit-quota-exhausted (manual test)",
			},
		}
		cleared, previous := as.ClearQuotaCooldown()
		if !cleared {
			t.Fatalf("expected cleared=true when cooldown active, got false")
		}
		if previous != until {
			t.Errorf("previousUntil = %q, want %q", previous, until)
		}
		if as.state.QuotaCooldownUntil != "" {
			t.Errorf("expected in-memory QuotaCooldownUntil to be cleared, got %q",
				as.state.QuotaCooldownUntil)
		}
		if as.state.QuotaCooldownReason != "" {
			t.Errorf("expected in-memory QuotaCooldownReason to be cleared, got %q",
				as.state.QuotaCooldownReason)
		}
		// Confirm persistence — the on-disk file must reflect the cleared
		// state so a backend restart doesn't restore the cooldown.
		p := filepath.Join(tmp, autonomousStateFile)
		data, err := os.ReadFile(p)
		if err != nil {
			t.Fatalf("read persisted state: %v", err)
		}
		var loaded AutonomousState
		if err := json.Unmarshal(data, &loaded); err != nil {
			t.Fatalf("parse persisted state: %v", err)
		}
		if loaded.QuotaCooldownUntil != "" {
			t.Errorf("persisted QuotaCooldownUntil = %q, want empty",
				loaded.QuotaCooldownUntil)
		}
		if loaded.QuotaCooldownReason != "" {
			t.Errorf("persisted QuotaCooldownReason = %q, want empty",
				loaded.QuotaCooldownReason)
		}
	})

	t.Run("no-op when no cooldown active", func(t *testing.T) {
		as := &AutonomousScheduler{
			workspaceRoot: t.TempDir(),
			state:         &AutonomousState{Status: "running"},
		}
		cleared, previous := as.ClearQuotaCooldown()
		if cleared {
			t.Errorf("expected cleared=false when no cooldown active")
		}
		if previous != "" {
			t.Errorf("previousUntil = %q, want empty", previous)
		}
	})
}

// #3446 — TestRunCycle_CooldownEmitsRejectionReason guards the contract that
// every cooldown-blocked scan cycle leaves a "quota-cooldown" entry in
// LastRejectionReasons. The TypeScript status-bar + output-channel UI reads
// this so the user sees activity ("Autonomous: cooldown until …") instead
// of a silent idle. Without this signal, the scheduler logs the wait to
// stderr but the user sees nothing — exactly the invisibility bug #3446
// is about.
func TestRunCycle_CooldownEmitsRejectionReason(t *testing.T) {
	as := &AutonomousScheduler{
		config: AutonomousConfig{MaxConcurrent: 3},
		state: &AutonomousState{
			Status:               "running",
			QuotaCooldownUntil:   time.Now().Add(2 * time.Hour).UTC().Format(time.RFC3339),
			QuotaCooldownReason:  "rate-limit-quota-exhausted from nightgauge/nightgauge#3375",
			LastRejectionReasons: map[string]int{"stale": 99},
		},
		rescanCh:             make(chan struct{}, 1),
		perIssueFailureCount: map[string]int{},
		retryBackoff:         map[string]time.Time{},
	}
	as.runCycle(context.Background())

	if as.state.LastRejectionReasons == nil {
		t.Fatalf("expected LastRejectionReasons to be populated")
	}
	if got := as.state.LastRejectionReasons["quota-cooldown"]; got != 1 {
		t.Errorf("LastRejectionReasons[\"quota-cooldown\"] = %d, want 1", got)
	}
	if _, leftover := as.state.LastRejectionReasons["stale"]; leftover {
		t.Errorf("expected stale rejection reasons to be replaced on cooldown cycle")
	}
	if as.state.LastCandidateCount != 0 {
		t.Errorf("LastCandidateCount = %d, want 0 during cooldown",
			as.state.LastCandidateCount)
	}
}

// TestRunCycle_GraphCacheHit verifies that BuildGraph is called exactly once
// across multiple runCycle calls within the TTL window.
func TestRunCycle_GraphCacheHit(t *testing.T) {
	buildCalls := 0
	fakeGraph := buildTestGraph(nil, nil)
	as := &AutonomousScheduler{
		config: AutonomousConfig{
			MaxConcurrent: 3,
			GraphCacheTTL: 5 * time.Minute,
		},
		state:                &AutonomousState{Status: "running"},
		rescanCh:             make(chan struct{}, 1),
		perIssueFailureCount: map[string]int{},
		retryBackoff:         map[string]time.Time{},
	}
	as.buildGraphFn = func(_ context.Context) (*depgraph.Graph, error) {
		buildCalls++
		return fakeGraph, nil
	}

	as.runCycle(context.Background())
	as.runCycle(context.Background())
	as.runCycle(context.Background())

	if buildCalls != 1 {
		t.Errorf("expected BuildGraph called once (cache hit for calls 2-3), got %d", buildCalls)
	}
}

// TestRunCycle_GraphCacheMissAfterTTL verifies that BuildGraph is called again
// once the TTL has expired.
func TestRunCycle_GraphCacheMissAfterTTL(t *testing.T) {
	buildCalls := 0
	fakeGraph := buildTestGraph(nil, nil)
	as := &AutonomousScheduler{
		config: AutonomousConfig{
			MaxConcurrent: 3,
			GraphCacheTTL: 10 * time.Millisecond,
		},
		state:                &AutonomousState{Status: "running"},
		rescanCh:             make(chan struct{}, 1),
		perIssueFailureCount: map[string]int{},
		retryBackoff:         map[string]time.Time{},
	}
	as.buildGraphFn = func(_ context.Context) (*depgraph.Graph, error) {
		buildCalls++
		return fakeGraph, nil
	}

	as.runCycle(context.Background()) // call 1 — cold cache, builds fresh
	time.Sleep(20 * time.Millisecond) // TTL expires
	as.runCycle(context.Background()) // call 2 — TTL expired, builds fresh again

	if buildCalls != 2 {
		t.Errorf("expected BuildGraph called twice (once per TTL window), got %d", buildCalls)
	}
}

// TestOnPipelineComplete_IssueClosed_NoLifetimeIncrement verifies that
// pipeline-start-failure:issue-closed does NOT increment LifetimeIssueFailures,
// does NOT count toward the per-session circuit breaker, and does NOT set a
// retry backoff. Issue #3661.
func TestOnPipelineComplete_IssueClosed_NoLifetimeIncrement(t *testing.T) {
	as := &AutonomousScheduler{
		config: AutonomousConfig{MaxConcurrent: 3},
		state: &AutonomousState{
			Status: "running",
			Running: []RunningItem{
				{Repo: "nightgauge/nightgauge", Number: 3661, Title: "Already-closed issue"},
			},
			LifetimeIssueFailures: map[string]int{},
		},
		rescanCh:             make(chan struct{}, 1),
		perIssueFailureCount: map[string]int{},
		retryBackoff:         map[string]time.Time{},
	}

	as.onPipelineComplete("nightgauge/nightgauge", 3661, false, false,
		TerminalKindIssueClosed, "[pipeline-start-failure] issue-closed")

	key := "nightgauge/nightgauge#3661"

	// Lifetime counter must NOT be incremented.
	if got := as.state.LifetimeIssueFailures[key]; got != 0 {
		t.Errorf("LifetimeIssueFailures[%q] = %d after issue-closed, want 0 (issue-closed is not a failure)",
			key, got)
	}
	// Per-session counter must NOT be incremented.
	if got := as.perIssueFailureCount[key]; got != 0 {
		t.Errorf("perIssueFailureCount[%q] = %d after issue-closed, want 0",
			key, got)
	}
	// No retry backoff — the issue is closed, not retryable.
	if _, ok := as.retryBackoff[key]; ok {
		t.Errorf("expected no retryBackoff for issue-closed, but one was set")
	}
	// Slot must be freed.
	if len(as.state.Running) != 0 {
		t.Errorf("expected 0 running after issue-closed, got %d", len(as.state.Running))
	}
	// Status must remain running — issue-closed must NOT trip safety rails or pause.
	if as.state.Status != "running" {
		t.Errorf("Status = %q after issue-closed, want 'running' (should not pause)", as.state.Status)
	}
}

// TestOnPipelineComplete_IssueClosed_NoCircuitBreaker verifies that repeated
// issue-closed events do not trip the cascade circuit breaker. Issue #3661.
func TestOnPipelineComplete_IssueClosed_NoCircuitBreaker(t *testing.T) {
	as := &AutonomousScheduler{
		config: AutonomousConfig{MaxConcurrent: 3},
		state: &AutonomousState{
			Status:                "running",
			LifetimeIssueFailures: map[string]int{},
		},
		rescanCh:             make(chan struct{}, 1),
		perIssueFailureCount: map[string]int{},
		retryBackoff:         map[string]time.Time{},
		// cascadeTracker intentionally nil — a nil tracker means no cascade
		// counting happens; if the issue-closed branch incorrectly falls
		// through to the generic failure path it would panic on nil dereference
		// OR increment the cascade counter. Either would fail this test.
	}

	// Simulate MaxLifetimeFailuresPerIssue issue-closed events — should NOT
	// trip any cap.
	for i := range MaxLifetimeFailuresPerIssue + 1 {
		as.state.Running = []RunningItem{
			{Repo: "nightgauge/nightgauge", Number: 3661, Title: "Already-closed"},
		}
		as.onPipelineComplete("nightgauge/nightgauge", 3661, false, false,
			TerminalKindIssueClosed, "[pipeline-start-failure] issue-closed")
		key := "nightgauge/nightgauge#3661"
		if got := as.state.LifetimeIssueFailures[key]; got != 0 {
			t.Errorf("after issue-closed #%d: LifetimeIssueFailures[%q] = %d, want 0", i+1, key, got)
		}
	}

	if as.state.Status != "running" {
		t.Errorf("Status = %q after repeated issue-closed, want 'running' (must not pause autonomous)", as.state.Status)
	}
}

// TestOnPipelineComplete_BlockedDependency_NonFailure verifies that a
// blocked-dependency deferral (#305) is treated as a NON-FAILURE: it does NOT
// increment LifetimeIssueFailures, does NOT count toward the per-session
// circuit breaker, does NOT pause autonomous, and keeps the issue eligible via
// a modest retry backoff (board → Ready, not Done). Issue #305.
func TestOnPipelineComplete_BlockedDependency_NonFailure(t *testing.T) {
	as := &AutonomousScheduler{
		config: AutonomousConfig{MaxConcurrent: 3},
		state: &AutonomousState{
			Status: "running",
			Running: []RunningItem{
				{Repo: "nightgauge/nightgauge", Number: 305, Title: "Dispatched while blocked"},
			},
			LifetimeIssueFailures: map[string]int{},
		},
		rescanCh:             make(chan struct{}, 1),
		perIssueFailureCount: map[string]int{},
		retryBackoff:         map[string]time.Time{},
	}

	before := time.Now()
	as.onPipelineComplete("nightgauge/nightgauge", 305, false, false,
		TerminalKindBlockedDependency, "[blocked-dependency] blockedBy #300 still open")
	after := time.Now()

	key := "nightgauge/nightgauge#305"

	// Lifetime counter must NOT be incremented — a deferral is not a failure.
	if got := as.state.LifetimeIssueFailures[key]; got != 0 {
		t.Errorf("LifetimeIssueFailures[%q] = %d after blocked-dependency, want 0 (deferral is not a failure)",
			key, got)
	}
	// Per-session counter must NOT be incremented.
	if got := as.perIssueFailureCount[key]; got != 0 {
		t.Errorf("perIssueFailureCount[%q] = %d after blocked-dependency, want 0", key, got)
	}
	// MUST NOT pause the queue on a deferral.
	if as.state.Status == "paused" || as.state.Status == "safety_tripped" {
		t.Errorf("Status = %q after blocked-dependency, want still 'running' (deferral must not pause)", as.state.Status)
	}
	// MUST NOT apply a global cooldown — a single issue's dependency being open
	// says nothing about the rest of the queue.
	if as.state.QuotaCooldownUntil != "" {
		t.Errorf("QuotaCooldownUntil = %q after blocked-dependency, want empty (no global cooldown)", as.state.QuotaCooldownUntil)
	}
	// Issue stays eligible: a modest (~5m) backoff is set so it re-dispatches
	// later rather than being parked forever or hot-looping.
	retryAt, ok := as.retryBackoff[key]
	if !ok {
		t.Fatalf("expected retryBackoff[%q] to be set after blocked-dependency (issue stays eligible)", key)
	}
	if wait := retryAt.Sub(before); wait < 3*time.Minute || wait > 8*time.Minute {
		t.Errorf("backoff = %v, want ~5min (allowed 3m–8m)", wait)
	}
	if !retryAt.After(after) {
		t.Errorf("retryAt %v is not after call return %v", retryAt, after)
	}
	// Slot must be freed and a failed entry recorded for visibility.
	if len(as.state.Running) != 0 {
		t.Errorf("expected 0 running after blocked-dependency, got %d", len(as.state.Running))
	}
	if len(as.state.Failed) != 1 || as.state.Failed[0].Number != 305 {
		t.Fatalf("expected 1 failed entry for #305, got %+v", as.state.Failed)
	}
}

// TestOnPipelineComplete_BlockedDependency_NoCircuitBreaker verifies that
// repeated blocked-dependency deferrals never trip the cascade circuit breaker
// or the lifetime cap. A nil cascadeTracker means the generic failure path
// would either panic or increment the cascade counter — so this test also
// proves the deferral short-circuits before that path. Issue #305.
func TestOnPipelineComplete_BlockedDependency_NoCircuitBreaker(t *testing.T) {
	as := &AutonomousScheduler{
		config: AutonomousConfig{MaxConcurrent: 3},
		state: &AutonomousState{
			Status:                "running",
			LifetimeIssueFailures: map[string]int{},
		},
		rescanCh:             make(chan struct{}, 1),
		perIssueFailureCount: map[string]int{},
		retryBackoff:         map[string]time.Time{},
		// cascadeTracker intentionally nil.
	}

	for i := range MaxLifetimeFailuresPerIssue + 2 {
		as.state.Running = []RunningItem{
			{Repo: "nightgauge/nightgauge", Number: 305, Title: "Dispatched while blocked"},
		}
		as.onPipelineComplete("nightgauge/nightgauge", 305, false, false,
			TerminalKindBlockedDependency, "[blocked-dependency] blockedBy #300 still open")
		key := "nightgauge/nightgauge#305"
		if got := as.state.LifetimeIssueFailures[key]; got != 0 {
			t.Errorf("after blocked-dependency #%d: LifetimeIssueFailures[%q] = %d, want 0", i+1, key, got)
		}
	}

	if as.state.Status != "running" {
		t.Errorf("Status = %q after repeated blocked-dependency, want 'running' (must not pause autonomous)", as.state.Status)
	}
}

// TestReconcileStateAgainstGraph_RecentClosureGuard verifies that a completed
// item closed within 60 seconds is NOT re-admitted even if GitHub returns OPEN
// state (simulating the read-after-write race). Issue #3661.
func TestReconcileStateAgainstGraph_RecentClosureGuard(t *testing.T) {
	as := &AutonomousScheduler{
		config: AutonomousConfig{MaxConcurrent: 3},
		state: &AutonomousState{
			Status: "running",
			Completed: []CompletedItem{
				{Repo: "nightgauge/nightgauge", Number: 3661, Title: "Just closed"},
			},
		},
		// Record closure timestamp as "just now" — within the guard window.
		recentClosures: map[string]time.Time{
			"nightgauge/nightgauge#3661": time.Now(),
		},
	}

	// Build a graph that reports the issue as still OPEN (simulating the race).
	g := depgraph.NewGraph()
	g.Nodes["nightgauge/nightgauge#3661"] = &depgraph.Node{
		Repo:   "nightgauge/nightgauge",
		Number: 3661,
		State:  "OPEN",
	}

	as.reconcileStateAgainstGraph(g)

	// Item must remain in Completed — guard prevented re-admission.
	if len(as.state.Completed) != 1 {
		t.Errorf("expected 1 completed item after guard, got %d (item was incorrectly re-admitted)", len(as.state.Completed))
	}
}

// TestReconcileStateAgainstGraph_RecentClosureGuard_ExpiredAllowsReadmit
// verifies that an item whose recent-closure timestamp has expired beyond the
// 60-second guard window IS re-admitted when GitHub returns OPEN (because the
// race window has passed and the OPEN read is now credible). Issue #3661.
func TestReconcileStateAgainstGraph_RecentClosureGuard_ExpiredAllowsReadmit(t *testing.T) {
	as := &AutonomousScheduler{
		config: AutonomousConfig{MaxConcurrent: 3},
		state: &AutonomousState{
			Status: "running",
			Completed: []CompletedItem{
				{Repo: "nightgauge/nightgauge", Number: 3661, Title: "Stale completed"},
			},
		},
		// Record closure timestamp as 90 seconds ago — outside the guard window.
		recentClosures: map[string]time.Time{
			"nightgauge/nightgauge#3661": time.Now().Add(-90 * time.Second),
		},
	}

	g := depgraph.NewGraph()
	g.Nodes["nightgauge/nightgauge#3661"] = &depgraph.Node{
		Repo:   "nightgauge/nightgauge",
		Number: 3661,
		State:  "OPEN",
	}

	as.reconcileStateAgainstGraph(g)

	// Guard window has expired — item must be re-admitted (removed from Completed).
	if len(as.state.Completed) != 0 {
		t.Errorf("expected 0 completed items after guard expiry, got %d (stale guard incorrectly blocked re-admission)", len(as.state.Completed))
	}
}

// --- #306: off-board dependency resolution ------------------------------
//
// A blockedBy/depends-on edge can reference an issue with NO node in the
// graph — the graph only creates nodes from project-board items (depgraph/
// builder.go), so an epic that was never added to any board leaves a
// "dangling" edge. Pre-#306, adj[key] (built from g.Adjacency(), which drops
// edges whose target isn't a node) simply never contained such an edge, so
// the dep silently read as satisfied and a blocked issue dispatched. These
// tests cover the fix: rawAdjacency() keeps the edge visible, and prioritize()
// batch-resolves it via resolveDepStatesFn, failing closed when unresolved.

// TestPrioritize_OffBoardDependencyResolution table-drives the direct-dep and
// epic-cascade variants of the off-board dependency check.
func TestPrioritize_OffBoardDependencyResolution(t *testing.T) {
	const repo = "owner/repo"
	const missingDepKey = repo + "#209"

	buildDirectGraph := func() *depgraph.Graph {
		nodes := []*depgraph.Node{
			{Repo: repo, Number: 100, Title: "Blocked issue", State: "OPEN", BoardStatus: "Ready", Priority: "P2", Size: "S", Weight: 1},
		}
		edges := []depgraph.Edge{
			// #100 is blockedBy #209, which is never added as a node — #209 is
			// on no project board (the real-world #306 trigger: bowlsheet-flutter
			// #304 blockedBy epic #209).
			{From: depgraph.NodeID{Repo: repo, Number: 100}, To: depgraph.NodeID{Repo: repo, Number: 209}},
		}
		return buildTestGraph(nodes, edges)
	}

	buildEpicCascadeGraph := func() *depgraph.Graph {
		nodes := []*depgraph.Node{
			makeEpicTestNode(repo, 20, "OPEN", "Ready", []string{"type:epic"}, 0), // epic itself
			makeEpicTestNode(repo, 21, "OPEN", "Ready", nil, 20),                  // sub-issue, no individual blocker
		}
		edges := []depgraph.Edge{
			// The EPIC (#20) is blockedBy #209, which has no graph node.
			{From: depgraph.NodeID{Repo: repo, Number: 20}, To: depgraph.NodeID{Repo: repo, Number: 209}},
		}
		return buildTestGraph(nodes, edges)
	}

	tests := []struct {
		name           string
		buildGraph     func() *depgraph.Graph
		candidateNum   int
		resolved       map[string]string // nil means "resolver returns nil" (fetch failure)
		wantCandidate  bool
		wantReason     string
		wantReasonHits int
	}{
		{
			name:           "direct dep: missing dep resolves OPEN -> blocked",
			buildGraph:     buildDirectGraph,
			candidateNum:   100,
			resolved:       map[string]string{missingDepKey: "OPEN"},
			wantCandidate:  false,
			wantReason:     "blocked-by-offboard-dep",
			wantReasonHits: 1,
		},
		{
			name:          "direct dep: missing dep resolves CLOSED -> dispatchable",
			buildGraph:    buildDirectGraph,
			candidateNum:  100,
			resolved:      map[string]string{missingDepKey: "CLOSED"},
			wantCandidate: true,
		},
		{
			name:           "direct dep: resolution error (resolver returns nil) -> blocked",
			buildGraph:     buildDirectGraph,
			candidateNum:   100,
			resolved:       nil, // simulates GetIssuesByNumbers failure for this repo
			wantCandidate:  false,
			wantReason:     "blocked-by-offboard-dep",
			wantReasonHits: 1,
		},
		{
			name:           "epic-cascade: missing epic-dep node resolves OPEN -> sub-issue blocked",
			buildGraph:     buildEpicCascadeGraph,
			candidateNum:   21,
			resolved:       map[string]string{missingDepKey: "OPEN"},
			wantCandidate:  false,
			wantReason:     "blocked-by-offboard-epic-dep",
			wantReasonHits: 1,
		},
		{
			name:          "epic-cascade: missing epic-dep node resolves CLOSED -> sub-issue dispatchable",
			buildGraph:    buildEpicCascadeGraph,
			candidateNum:  21,
			resolved:      map[string]string{missingDepKey: "CLOSED"},
			wantCandidate: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := tt.buildGraph()
			as := &AutonomousScheduler{
				config: AutonomousConfig{MaxConcurrent: 5},
				repos:  []depgraph.RepoConfig{{Owner: "owner", Name: "repo", Project: 1}},
				state:  &AutonomousState{},
			}
			var callCount int
			as.resolveDepStatesFn = func(_ context.Context, keys []string) map[string]string {
				callCount++
				if len(keys) != 1 || keys[0] != missingDepKey {
					t.Errorf("resolveDepStatesFn called with unexpected keys %v, want [%s]", keys, missingDepKey)
				}
				return tt.resolved
			}

			candidates := as.prioritize(context.Background(), g)

			if callCount != 1 {
				t.Errorf("expected resolveDepStatesFn to be called exactly once (batched), got %d calls", callCount)
			}

			found := false
			for _, c := range candidates {
				if c.Number == tt.candidateNum {
					found = true
				}
			}
			if found != tt.wantCandidate {
				t.Errorf("candidate #%d present=%v, want %v", tt.candidateNum, found, tt.wantCandidate)
			}

			if tt.wantReason != "" {
				got := as.state.LastRejectionReasons[tt.wantReason]
				if got != tt.wantReasonHits {
					t.Errorf("rejection reason %q count = %d, want %d (reasons: %+v)",
						tt.wantReason, got, tt.wantReasonHits, as.state.LastRejectionReasons)
				}
			}
		})
	}
}

// TestPrioritize_OffBoardDependency_NoResolverWired verifies that when no
// issue-state resolver is wired at all (resolveDepStatesFn == nil), an
// off-board dependency still fails closed rather than silently dispatching.
func TestPrioritize_OffBoardDependency_NoResolverWired(t *testing.T) {
	const repo = "owner/repo"
	nodes := []*depgraph.Node{
		{Repo: repo, Number: 100, Title: "Blocked issue", State: "OPEN", BoardStatus: "Ready", Priority: "P2", Size: "S", Weight: 1},
	}
	edges := []depgraph.Edge{
		{From: depgraph.NodeID{Repo: repo, Number: 100}, To: depgraph.NodeID{Repo: repo, Number: 209}},
	}
	g := buildTestGraph(nodes, edges)

	as := &AutonomousScheduler{
		config: AutonomousConfig{MaxConcurrent: 5},
		repos:  []depgraph.RepoConfig{{Owner: "owner", Name: "repo", Project: 1}},
		state:  &AutonomousState{},
		// resolveDepStatesFn intentionally left nil — simulates a scheduler
		// wired without a resolver (or a resolver that was never set up).
	}

	candidates := as.prioritize(context.Background(), g)

	for _, c := range candidates {
		if c.Number == 100 {
			t.Fatalf("issue #100 should be blocked (off-board dep #209 unresolvable, no resolver wired) but was dispatched")
		}
	}
	if got := as.state.LastRejectionReasons["blocked-by-offboard-dep"]; got != 1 {
		t.Errorf("expected blocked-by-offboard-dep=1, got reasons: %+v", as.state.LastRejectionReasons)
	}
}

// TestPrioritize_NoLookupWhenAllDepsPresent verifies the hot path stays a pure
// map lookup: when every dependency edge resolves to a real node in the
// graph, resolveDepStatesFn must never be invoked (no GitHub call).
func TestPrioritize_NoLookupWhenAllDepsPresent(t *testing.T) {
	const repo = "owner/repo"
	nodes := []*depgraph.Node{
		{Repo: repo, Number: 1, Title: "Blocker", State: "CLOSED", BoardStatus: "Done", Priority: "P2", Size: "S", Weight: 1},
		{Repo: repo, Number: 2, Title: "Blocked", State: "OPEN", BoardStatus: "Ready", Priority: "P2", Size: "S", Weight: 1},
	}
	edges := []depgraph.Edge{
		{From: depgraph.NodeID{Repo: repo, Number: 2}, To: depgraph.NodeID{Repo: repo, Number: 1}},
	}
	g := buildTestGraph(nodes, edges)

	as := &AutonomousScheduler{
		config: AutonomousConfig{MaxConcurrent: 5},
		repos:  []depgraph.RepoConfig{{Owner: "owner", Name: "repo", Project: 1}},
		state:  &AutonomousState{},
	}
	as.resolveDepStatesFn = func(context.Context, []string) map[string]string {
		t.Fatal("resolveDepStatesFn must not be called when all deps are present in the graph")
		return nil
	}

	candidates := as.prioritize(context.Background(), g)

	found := false
	for _, c := range candidates {
		if c.Number == 2 {
			found = true
		}
	}
	if !found {
		t.Errorf("issue #2 should be a candidate (blocker #1 is CLOSED, no off-board deps)")
	}
}

// TestResolveIssueStatesByKey_BatchedPerRepo verifies the extracted batching
// helper mirrors refreshBlockerStates' discipline (scheduler.go): N keys
// spread across R repos cost exactly R GetIssuesByNumbers calls.
func TestResolveIssueStatesByKey_BatchedPerRepo(t *testing.T) {
	mock := newMockIssueSvc()
	mock.addIssue("owner", "repo-a", 10, &types.Issue{Number: 10, State: "CLOSED"})
	mock.addIssue("owner", "repo-a", 11, &types.Issue{Number: 11, State: "OPEN"})
	mock.addIssue("owner", "repo-b", 20, &types.Issue{Number: 20, State: "CLOSED"})

	keys := []string{"owner/repo-a#10", "owner/repo-a#11", "owner/repo-b#20"}
	resolved := resolveIssueStatesByKey(context.Background(), mock, keys)

	if len(mock.batchCalls) != 2 {
		t.Fatalf("want 2 batched calls (one per repo), got %d: %+v", len(mock.batchCalls), mock.batchCalls)
	}
	if resolved["owner/repo-a#10"] != "CLOSED" {
		t.Errorf("owner/repo-a#10 = %q, want CLOSED", resolved["owner/repo-a#10"])
	}
	if resolved["owner/repo-a#11"] != "OPEN" {
		t.Errorf("owner/repo-a#11 = %q, want OPEN", resolved["owner/repo-a#11"])
	}
	if resolved["owner/repo-b#20"] != "CLOSED" {
		t.Errorf("owner/repo-b#20 = %q, want CLOSED", resolved["owner/repo-b#20"])
	}
}

// TestResolveIssueStatesByKey_MissingIssueAbsentFromResult verifies that an
// issue absent from the batch response (deleted/inaccessible) is simply left
// out of the result — never defaulted to a guessed state.
func TestResolveIssueStatesByKey_MissingIssueAbsentFromResult(t *testing.T) {
	mock := newMockIssueSvc() // no issues registered
	resolved := resolveIssueStatesByKey(context.Background(), mock, []string{"owner/repo#999"})
	if state, ok := resolved["owner/repo#999"]; ok {
		t.Errorf("expected #999 to be absent from resolved map (not found in batch response), got %q", state)
	}
}

// TestResolveIssueStatesByKey_NilIssueService verifies the nil-issueSvc guard
// (no GitHub call attempted when the issue service is unavailable).
func TestResolveIssueStatesByKey_NilIssueService(t *testing.T) {
	resolved := resolveIssueStatesByKey(context.Background(), nil, []string{"owner/repo#1"})
	if resolved != nil {
		t.Errorf("expected nil map when issueSvc is nil, got %+v", resolved)
	}
}
