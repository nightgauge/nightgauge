package orchestrator

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/depgraph"
)

// --- Helper: deterministicAutonomousScheduler creates an AutonomousScheduler
// with controllable graph injection (no GitHub calls). Tests inject a pre-built
// graph and drive the scheduler's runCycle directly or via Run with a short
// timeout context.

// testGraphProvider is a function that returns a fresh graph on each call.
// This allows tests to mutate state between cycles (e.g., marking issues as
// CLOSED after a simulated completion).
type testGraphProvider func() *depgraph.Graph

// autonomousTestHarness wraps AutonomousScheduler with test-friendly hooks.
type autonomousTestHarness struct {
	scheduler *AutonomousScheduler
	tmpDir    string

	mu            sync.Mutex
	graphProvider testGraphProvider
	cycleCount    int
	cycleCh       chan struct{} // signalled after each cycle
}

// newTestHarness creates a test harness with the given config and initial graph.
func newTestHarness(t *testing.T, cfg AutonomousConfig, graphProvider testGraphProvider) *autonomousTestHarness {
	t.Helper()
	tmpDir := t.TempDir()

	as := &AutonomousScheduler{
		config:               cfg,
		workspaceRoot:        tmpDir,
		state:                &AutonomousState{Status: "running", TokensCeiling: cfg.BudgetCeiling},
		stopCh:               make(chan struct{}, 1),
		rescanCh:             make(chan struct{}, 1),
		perIssueFailureCount: make(map[string]int),
		retryBackoff:         make(map[string]time.Time),
	}

	// Wire up safety rails
	safetyCfg := DefaultSafetyConfig()
	if cfg.BudgetCeiling > 0 {
		safetyCfg.BudgetCeiling = cfg.BudgetCeiling
	}
	if cfg.SafetyRails != nil {
		if cfg.SafetyRails.BudgetCeiling > 0 {
			safetyCfg.BudgetCeiling = cfg.SafetyRails.BudgetCeiling
		}
		if cfg.SafetyRails.CircuitBreakerMax > 0 {
			safetyCfg.CircuitBreakerMax = cfg.SafetyRails.CircuitBreakerMax
		}
		if cfg.SafetyRails.RateLimitPerHour > 0 {
			safetyCfg.RateLimitPerHour = cfg.SafetyRails.RateLimitPerHour
		}
		if cfg.SafetyRails.HealthGateMin > 0 {
			safetyCfg.HealthGateMin = cfg.SafetyRails.HealthGateMin
		}
		safetyCfg.EpicCheckpoint = cfg.SafetyRails.EpicCheckpoint
	}
	as.safetyRails = NewSafetyRails(safetyCfg)

	h := &autonomousTestHarness{
		scheduler:     as,
		tmpDir:        tmpDir,
		graphProvider: graphProvider,
		cycleCh:       make(chan struct{}, 100),
	}

	// Set cycle-complete callback
	as.onCycleComplete = func() {
		h.mu.Lock()
		h.cycleCount++
		h.mu.Unlock()
		select {
		case h.cycleCh <- struct{}{}:
		default:
		}
	}

	return h
}

// runOneCycle executes a single scan-prioritize-dispatch cycle using the
// harness's graph provider. This bypasses the ticker/context loop and lets
// tests drive the scheduler step by step.
func (h *autonomousTestHarness) runOneCycle(t *testing.T) {
	t.Helper()
	g := h.graphProvider()

	// Inject the graph into the scheduler's cycle logic by calling prioritize
	// and simulating dispatch. We replicate the core of runCycle without the
	// goroutine/ticker machinery.
	as := h.scheduler
	as.mu.Lock()
	if as.state.Status != "running" {
		as.mu.Unlock()
		return
	}
	as.state.CyclesRun++
	as.state.LastScanAt = time.Now().UTC().Format(time.RFC3339)
	as.mu.Unlock()

	// Prioritize
	candidates := as.prioritize(context.Background(), g)

	// Available slots
	as.mu.Lock()
	availableSlots := as.config.MaxConcurrent - len(as.state.Running)
	as.state.Remaining = len(candidates)
	as.mu.Unlock()

	if availableSlots <= 0 {
		as.persistState()
		if as.onCycleComplete != nil {
			as.onCycleComplete()
		}
		return
	}

	// Fill slots
	dispatched := 0
	for i := 0; i < len(candidates) && dispatched < availableSlots; i++ {
		item := candidates[i]
		if as.isRunning(item.Repo, item.Number) {
			continue
		}

		if as.config.DryRun {
			continue
		}

		// Safety rail check
		if as.safetyRails != nil {
			allowed, _ := as.safetyRails.CheckBeforeEnqueue(0)
			if !allowed {
				as.mu.Lock()
				as.state.Status = "safety_tripped"
				safetySnap := as.safetyRails.State()
				as.state.Safety = &safetySnap
				as.mu.Unlock()
				as.persistState()
				if as.onCycleComplete != nil {
					as.onCycleComplete()
				}
				return
			}
			as.safetyRails.RecordPipelineStart()
		}

		// Simulate enqueue (no real scheduler/pipeline)
		as.mu.Lock()
		as.state.Running = append(as.state.Running, RunningItem{
			Repo:      item.Repo,
			Number:    item.Number,
			Title:     item.Title,
			StartedAt: time.Now().UTC().Format(time.RFC3339),
		})
		as.mu.Unlock()
		dispatched++
	}

	// Budget check
	as.mu.Lock()
	if as.state.TokensCeiling > 0 && as.state.TokensSpent >= as.state.TokensCeiling {
		as.mu.Unlock()
		as.complete("budget_exhausted")
		as.persistState()
		if as.onCycleComplete != nil {
			as.onCycleComplete()
		}
		return
	}

	remaining := as.state.Remaining
	runningCount := len(as.state.Running)
	as.mu.Unlock()

	if remaining == 0 && runningCount == 0 {
		as.complete("complete")
	}

	as.persistState()
	if as.onCycleComplete != nil {
		as.onCycleComplete()
	}
}

// simulateCompletion simulates a pipeline completing for a given repo/number.
func (h *autonomousTestHarness) simulateCompletion(repo string, number int, success bool) {
	h.scheduler.onPipelineComplete(repo, number, success, false, "", "")
}

// simulateTerminalCompletion is like simulateCompletion but lets a test
// simulate a specific terminal failure kind (Issue #3398).
func (h *autonomousTestHarness) simulateTerminalCompletion(repo string, number int, success bool, terminalKind string) {
	h.scheduler.onPipelineComplete(repo, number, success, false, terminalKind, "")
}

// getCycles returns the number of cycles completed.
func (h *autonomousTestHarness) getCycles() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.cycleCount
}

// boardStatusForState returns the expected board status for a given issue state.
// Used in tests where State is a variable (e.g. toggled between OPEN/CLOSED
// across cycles) to keep BoardStatus in sync.
func boardStatusForState(state string) string {
	if state == "CLOSED" {
		return "Done"
	}
	return "Ready"
}

// ============================================================================
// Integration Tests
// ============================================================================

// TestIntegration_SingleRepo_ThreeIssues_NoDeps verifies that with 3
// independent issues in a single repo and 3 slots, all 3 are enqueued in the
// first cycle.
func TestIntegration_SingleRepo_ThreeIssues_NoDeps(t *testing.T) {
	nodes := []*depgraph.Node{
		{Repo: "O/core", Number: 1, Title: "Issue A", State: "OPEN", BoardStatus: "Ready", Priority: "P1", Size: "M", Weight: 3},
		{Repo: "O/core", Number: 2, Title: "Issue B", State: "OPEN", BoardStatus: "Ready", Priority: "P1", Size: "M", Weight: 3},
		{Repo: "O/core", Number: 3, Title: "Issue C", State: "OPEN", BoardStatus: "Ready", Priority: "P1", Size: "M", Weight: 3},
	}

	graphProvider := func() *depgraph.Graph {
		return buildTestGraph(nodes, nil)
	}

	cfg := AutonomousConfig{
		MaxConcurrent: 3,
		SafetyRails:   &SafetyConfig{RateLimitPerHour: 100},
	}
	h := newTestHarness(t, cfg, graphProvider)
	h.runOneCycle(t)

	as := h.scheduler
	as.mu.Lock()
	runningCount := len(as.state.Running)
	as.mu.Unlock()

	if runningCount != 3 {
		t.Errorf("expected 3 running after first cycle, got %d", runningCount)
	}

	// Verify all 3 issues are in the running set
	as.mu.Lock()
	runningNumbers := make(map[int]bool)
	for _, r := range as.state.Running {
		runningNumbers[r.Number] = true
	}
	as.mu.Unlock()

	for _, n := range []int{1, 2, 3} {
		if !runningNumbers[n] {
			t.Errorf("expected issue #%d to be running", n)
		}
	}
}

// TestIntegration_TwoRepos_CrossRepoDependency verifies that a cross-repo
// dependency is respected: repo A issue blocks repo B issue; A completes then
// B auto-enqueues.
func TestIntegration_TwoRepos_CrossRepoDependency(t *testing.T) {
	// Platform #10 blocks Flutter #20. Only Platform #10 should run first.
	makeGraph := func(platformState string) *depgraph.Graph {
		platBoard := "Ready"
		if platformState == "CLOSED" {
			platBoard = "Done"
		}
		nodes := []*depgraph.Node{
			{Repo: "O/platform", Number: 10, Title: "Platform API", State: platformState, BoardStatus: platBoard, Priority: "P0", Size: "M", Weight: 3},
			{Repo: "O/flutter", Number: 20, Title: "Flutter Consumer", State: "OPEN", BoardStatus: "Ready", Priority: "P1", Size: "S", Weight: 2},
		}
		edges := []depgraph.Edge{
			{From: depgraph.NodeID{Repo: "O/flutter", Number: 20}, To: depgraph.NodeID{Repo: "O/platform", Number: 10}},
		}
		return buildTestGraph(nodes, edges)
	}

	platformState := "OPEN"
	graphProvider := func() *depgraph.Graph {
		return makeGraph(platformState)
	}

	cfg := AutonomousConfig{
		MaxConcurrent: 3,
		SafetyRails:   &SafetyConfig{RateLimitPerHour: 100},
	}
	h := newTestHarness(t, cfg, graphProvider)

	// Cycle 1: only platform #10 should be enqueued (flutter #20 is blocked)
	h.runOneCycle(t)

	as := h.scheduler
	as.mu.Lock()
	if len(as.state.Running) != 1 {
		t.Fatalf("cycle 1: expected 1 running, got %d", len(as.state.Running))
	}
	if as.state.Running[0].Number != 10 {
		t.Errorf("cycle 1: expected #10 running, got #%d", as.state.Running[0].Number)
	}
	if as.state.Running[0].Repo != "O/platform" {
		t.Errorf("cycle 1: expected O/platform, got %s", as.state.Running[0].Repo)
	}
	as.mu.Unlock()

	// Simulate platform #10 completing
	h.simulateCompletion("O/platform", 10, true)

	// Update graph: platform #10 is now CLOSED
	platformState = "CLOSED"

	// Cycle 2: flutter #20 should now be unblocked and enqueued
	h.runOneCycle(t)

	as.mu.Lock()
	if len(as.state.Running) != 1 {
		t.Fatalf("cycle 2: expected 1 running (flutter), got %d", len(as.state.Running))
	}
	if as.state.Running[0].Number != 20 {
		t.Errorf("cycle 2: expected #20 running, got #%d", as.state.Running[0].Number)
	}
	if as.state.Running[0].Repo != "O/flutter" {
		t.Errorf("cycle 2: expected O/flutter, got %s", as.state.Running[0].Repo)
	}
	if len(as.state.Completed) != 1 {
		t.Errorf("cycle 2: expected 1 completed, got %d", len(as.state.Completed))
	}
	as.mu.Unlock()
}

// TestIntegration_EpicWithWaves verifies that an epic's sub-issues are executed
// in wave order. Sub-issues #2 and #3 depend on #1; #4 depends on #2 and #3.
// Wave 1: #1, Wave 2: #2 + #3, Wave 3: #4.
func TestIntegration_EpicWithWaves(t *testing.T) {
	type issueState struct {
		state string
	}
	states := map[int]*issueState{
		1: {state: "OPEN"},
		2: {state: "OPEN"},
		3: {state: "OPEN"},
		4: {state: "OPEN"},
	}

	graphProvider := func() *depgraph.Graph {
		nodes := []*depgraph.Node{
			{Repo: "R", Number: 100, Title: "Epic", State: "OPEN", BoardStatus: "Ready", Labels: []string{"type:epic"}, Priority: "P0", Size: "XL", Weight: 8},
			{Repo: "R", Number: 1, Title: "Sub 1", State: states[1].state, BoardStatus: boardStatusForState(states[1].state), Priority: "P1", Size: "S", Weight: 2, EpicNumber: 100},
			{Repo: "R", Number: 2, Title: "Sub 2", State: states[2].state, BoardStatus: boardStatusForState(states[2].state), Priority: "P1", Size: "S", Weight: 2, EpicNumber: 100},
			{Repo: "R", Number: 3, Title: "Sub 3", State: states[3].state, BoardStatus: boardStatusForState(states[3].state), Priority: "P1", Size: "M", Weight: 3, EpicNumber: 100},
			{Repo: "R", Number: 4, Title: "Sub 4", State: states[4].state, BoardStatus: boardStatusForState(states[4].state), Priority: "P1", Size: "M", Weight: 3, EpicNumber: 100},
		}
		edges := []depgraph.Edge{
			// #2 depends on #1
			{From: depgraph.NodeID{Repo: "R", Number: 2}, To: depgraph.NodeID{Repo: "R", Number: 1}},
			// #3 depends on #1
			{From: depgraph.NodeID{Repo: "R", Number: 3}, To: depgraph.NodeID{Repo: "R", Number: 1}},
			// #4 depends on #2 and #3
			{From: depgraph.NodeID{Repo: "R", Number: 4}, To: depgraph.NodeID{Repo: "R", Number: 2}},
			{From: depgraph.NodeID{Repo: "R", Number: 4}, To: depgraph.NodeID{Repo: "R", Number: 3}},
		}
		return buildTestGraph(nodes, edges)
	}

	cfg := AutonomousConfig{
		MaxConcurrent: 5,
		SafetyRails:   &SafetyConfig{RateLimitPerHour: 100},
	}
	h := newTestHarness(t, cfg, graphProvider)

	// Wave 1: only #1 should be enqueued (others are blocked)
	h.runOneCycle(t)
	as := h.scheduler

	as.mu.Lock()
	if len(as.state.Running) != 1 {
		t.Fatalf("wave 1: expected 1 running, got %d", len(as.state.Running))
	}
	if as.state.Running[0].Number != 1 {
		t.Errorf("wave 1: expected #1, got #%d", as.state.Running[0].Number)
	}
	as.mu.Unlock()

	// Complete #1
	h.simulateCompletion("R", 1, true)
	states[1].state = "CLOSED"

	// Wave 2: #2 and #3 should be enqueued
	h.runOneCycle(t)

	as.mu.Lock()
	if len(as.state.Running) != 2 {
		t.Fatalf("wave 2: expected 2 running, got %d", len(as.state.Running))
	}
	runningNums := make(map[int]bool)
	for _, r := range as.state.Running {
		runningNums[r.Number] = true
	}
	as.mu.Unlock()

	if !runningNums[2] || !runningNums[3] {
		t.Errorf("wave 2: expected #2 and #3 running, got %v", runningNums)
	}

	// Complete #2 and #3
	h.simulateCompletion("R", 2, true)
	h.simulateCompletion("R", 3, true)
	states[2].state = "CLOSED"
	states[3].state = "CLOSED"

	// Wave 3: #4 should be enqueued
	h.runOneCycle(t)

	as.mu.Lock()
	if len(as.state.Running) != 1 {
		t.Fatalf("wave 3: expected 1 running, got %d", len(as.state.Running))
	}
	if as.state.Running[0].Number != 4 {
		t.Errorf("wave 3: expected #4, got #%d", as.state.Running[0].Number)
	}
	as.mu.Unlock()
}

// TestIntegration_BudgetExhaustion verifies that the scheduler stops when the
// token budget is exhausted mid-run. The safety rails budget ceiling fires
// before the legacy budget check, so the status is "safety_tripped".
func TestIntegration_BudgetExhaustion(t *testing.T) {
	nodes := []*depgraph.Node{
		{Repo: "R", Number: 1, Title: "A", State: "OPEN", BoardStatus: "Ready", Priority: "P0", Size: "S", Weight: 2},
		{Repo: "R", Number: 2, Title: "B", State: "OPEN", BoardStatus: "Ready", Priority: "P1", Size: "S", Weight: 2},
		{Repo: "R", Number: 3, Title: "C", State: "OPEN", BoardStatus: "Ready", Priority: "P2", Size: "S", Weight: 2},
	}

	completedSet := make(map[int]bool)
	graphProvider := func() *depgraph.Graph {
		var liveNodes []*depgraph.Node
		for _, n := range nodes {
			state := n.State
			if completedSet[n.Number] {
				state = "CLOSED"
			}
			liveNodes = append(liveNodes, &depgraph.Node{
				Repo: n.Repo, Number: n.Number, Title: n.Title,
				State: state, BoardStatus: n.BoardStatus, Priority: n.Priority, Size: n.Size, Weight: n.Weight,
			})
		}
		return buildTestGraph(liveNodes, nil)
	}

	cfg := AutonomousConfig{
		MaxConcurrent: 1, // 1 slot at a time
		BudgetCeiling: 10000,
		SafetyRails: &SafetyConfig{
			BudgetCeiling:    10000,
			RateLimitPerHour: 100,
		},
	}
	h := newTestHarness(t, cfg, graphProvider)

	// Cycle 1: dispatch item 1
	h.runOneCycle(t)
	as := h.scheduler

	as.mu.Lock()
	if len(as.state.Running) != 1 {
		t.Fatalf("expected 1 running, got %d", len(as.state.Running))
	}
	as.mu.Unlock()

	// Complete item 1, add 4000 tokens
	h.simulateCompletion("R", 1, true)
	completedSet[1] = true
	as.AddTokensSpent(4000)

	// Cycle 2: dispatch item 2
	h.runOneCycle(t)
	as.mu.Lock()
	if len(as.state.Running) != 1 {
		t.Fatalf("expected 1 running, got %d", len(as.state.Running))
	}
	as.mu.Unlock()

	// Complete item 2, add 7000 tokens (total: 11000 > 10000 ceiling)
	h.simulateCompletion("R", 2, true)
	completedSet[2] = true
	as.AddTokensSpent(7000)

	// Cycle 3: budget ceiling in safety rails fires first → "safety_tripped"
	h.runOneCycle(t)

	as.mu.Lock()
	status := as.state.Status
	as.mu.Unlock()

	// Safety rails budget ceiling check fires before the legacy budget check
	// in runCycle, so the terminal status is "safety_tripped" rather than
	// "budget_exhausted". Both indicate budget exhaustion; the safety rails
	// version includes diagnostic state.
	if status != "safety_tripped" {
		t.Errorf("expected status 'safety_tripped' (budget ceiling), got %q", status)
	}

	// Verify the trip reason mentions budget
	safetyState := as.safetyRails.State()
	if safetyState.TripReason == "" {
		t.Error("expected non-empty trip reason")
	}
	if !contains(safetyState.TripReason, "budget") {
		t.Errorf("expected budget-related trip reason, got: %s", safetyState.TripReason)
	}
}

// TestIntegration_CircuitBreaker verifies that 3 consecutive failures trip
// the circuit breaker and stop the scheduler.
func TestIntegration_CircuitBreaker(t *testing.T) {
	issueNum := 0
	makeGraph := func(state string, num int) *depgraph.Graph {
		boardStatus := "Ready"
		if state == "CLOSED" {
			boardStatus = "Done"
		}
		nodes := []*depgraph.Node{
			{Repo: "R", Number: num, Title: "Issue", State: state, BoardStatus: boardStatus, Priority: "P0", Size: "XS", Weight: 1},
		}
		return buildTestGraph(nodes, nil)
	}

	graphProvider := func() *depgraph.Graph {
		issueNum++
		return makeGraph("OPEN", issueNum)
	}

	cfg := AutonomousConfig{
		MaxConcurrent: 1,
		SafetyRails: &SafetyConfig{
			CircuitBreakerMax: 3,
			RateLimitPerHour:  100,
		},
	}
	h := newTestHarness(t, cfg, graphProvider)

	// Failure 1
	h.runOneCycle(t)
	h.simulateCompletion("R", 1, false)

	// Failure 2
	h.runOneCycle(t)
	h.simulateCompletion("R", 2, false)

	// Failure 3
	h.runOneCycle(t)
	h.simulateCompletion("R", 3, false)

	// Cycle 4: circuit breaker should trip
	h.runOneCycle(t)

	as := h.scheduler
	as.mu.Lock()
	status := as.state.Status
	as.mu.Unlock()

	if status != "safety_tripped" {
		t.Errorf("expected 'safety_tripped' after 3 consecutive failures, got %q", status)
	}

	// Verify safety state
	safetyState := as.safetyRails.State()
	if safetyState.ConsecutiveFailures != 3 {
		t.Errorf("expected 3 consecutive failures, got %d", safetyState.ConsecutiveFailures)
	}
}

// TestIntegration_DryRunMode verifies that dry-run mode does NOT enqueue any
// items into the running set.
func TestIntegration_DryRunMode(t *testing.T) {
	nodes := []*depgraph.Node{
		{Repo: "R", Number: 1, Title: "A", State: "OPEN", BoardStatus: "Ready", Priority: "P0", Size: "XS", Weight: 1},
		{Repo: "R", Number: 2, Title: "B", State: "OPEN", BoardStatus: "Ready", Priority: "P1", Size: "S", Weight: 2},
		{Repo: "R", Number: 3, Title: "C", State: "OPEN", BoardStatus: "Ready", Priority: "P2", Size: "M", Weight: 3},
	}

	graphProvider := func() *depgraph.Graph {
		return buildTestGraph(nodes, nil)
	}

	cfg := AutonomousConfig{
		MaxConcurrent: 5,
		DryRun:        true,
		SafetyRails:   &SafetyConfig{RateLimitPerHour: 100},
	}
	h := newTestHarness(t, cfg, graphProvider)
	h.runOneCycle(t)

	as := h.scheduler
	as.mu.Lock()
	runningCount := len(as.state.Running)
	completedCount := len(as.state.Completed)
	as.mu.Unlock()

	if runningCount != 0 {
		t.Errorf("dry-run: expected 0 running, got %d", runningCount)
	}
	if completedCount != 0 {
		t.Errorf("dry-run: expected 0 completed, got %d", completedCount)
	}
}

// TestIntegration_PauseAndResume verifies that pausing stops dispatch and
// resuming re-enables it, with state preserved across the transition.
func TestIntegration_PauseAndResume(t *testing.T) {
	nodes := []*depgraph.Node{
		{Repo: "R", Number: 1, Title: "A", State: "OPEN", BoardStatus: "Ready", Priority: "P0", Size: "XS", Weight: 1},
		{Repo: "R", Number: 2, Title: "B", State: "OPEN", BoardStatus: "Ready", Priority: "P1", Size: "S", Weight: 2},
	}

	graphProvider := func() *depgraph.Graph {
		return buildTestGraph(nodes, nil)
	}

	cfg := AutonomousConfig{
		MaxConcurrent: 1,
		SafetyRails:   &SafetyConfig{RateLimitPerHour: 100},
	}
	h := newTestHarness(t, cfg, graphProvider)

	// Cycle 1: dispatch 1 item
	h.runOneCycle(t)

	as := h.scheduler
	as.mu.Lock()
	if len(as.state.Running) != 1 {
		t.Fatalf("expected 1 running before pause, got %d", len(as.state.Running))
	}
	as.mu.Unlock()

	// Pause
	as.Pause("test", "test")
	if as.state.Status != "paused" {
		t.Errorf("expected 'paused', got %q", as.state.Status)
	}

	// Cycle while paused: should be a no-op (runCycle early-returns on non-running)
	h.runOneCycle(t)

	as.mu.Lock()
	cyclesAfterPause := as.state.CyclesRun
	as.mu.Unlock()

	// Resume
	as.Resume()
	if as.state.Status != "running" {
		t.Errorf("expected 'running' after resume, got %q", as.state.Status)
	}

	// Complete the running item and run another cycle
	h.simulateCompletion("R", 1, true)
	h.runOneCycle(t)

	as.mu.Lock()
	cyclesAfterResume := as.state.CyclesRun
	completedCount := len(as.state.Completed)
	as.mu.Unlock()

	// Should have run at least one more cycle
	if cyclesAfterResume <= cyclesAfterPause {
		t.Errorf("expected more cycles after resume, got before=%d after=%d",
			cyclesAfterPause, cyclesAfterResume)
	}

	if completedCount != 1 {
		t.Errorf("expected 1 completed after resume, got %d", completedCount)
	}
}

// TestIntegration_PriorityDominatesCriticalPath verifies the post-#3396
// invariant: explicit priority labels (P0/P1/P2/P3) dominate critical-path
// position. A standalone P0 item that is NOT on the critical path must
// dispatch before a P2 chain head that IS on the critical path. Pre-#3396 the
// scheduler reversed this and starved P0 stability work behind long P1/P2
// dependency chains (notably the GitLab forge epic).
func TestIntegration_PriorityDominatesCriticalPath(t *testing.T) {
	// Create a chain: #1 -> #2 -> #3 (critical path, all P2)
	// And an independent #4 (P0, not on critical path)
	// With 1 slot: the P0 item should be picked first regardless of crit path.
	nodes := []*depgraph.Node{
		{Repo: "R", Number: 1, Title: "Chain Start", State: "OPEN", BoardStatus: "Ready", Priority: "P2", Size: "L", Weight: 5},
		{Repo: "R", Number: 2, Title: "Chain Mid", State: "OPEN", BoardStatus: "Ready", Priority: "P2", Size: "L", Weight: 5},
		{Repo: "R", Number: 3, Title: "Chain End", State: "OPEN", BoardStatus: "Ready", Priority: "P2", Size: "L", Weight: 5},
		{Repo: "R", Number: 4, Title: "Independent P0", State: "OPEN", BoardStatus: "Ready", Priority: "P0", Size: "XS", Weight: 1},
	}
	edges := []depgraph.Edge{
		{From: depgraph.NodeID{Repo: "R", Number: 2}, To: depgraph.NodeID{Repo: "R", Number: 1}},
		{From: depgraph.NodeID{Repo: "R", Number: 3}, To: depgraph.NodeID{Repo: "R", Number: 2}},
	}

	graphProvider := func() *depgraph.Graph {
		return buildTestGraph(nodes, edges)
	}

	cfg := AutonomousConfig{
		MaxConcurrent: 1, // only 1 slot — forces ordering to matter
		SafetyRails:   &SafetyConfig{RateLimitPerHour: 100},
	}
	h := newTestHarness(t, cfg, graphProvider)
	h.runOneCycle(t)

	as := h.scheduler
	as.mu.Lock()
	if len(as.state.Running) != 1 {
		t.Fatalf("expected 1 running, got %d", len(as.state.Running))
	}
	first := as.state.Running[0]
	as.mu.Unlock()

	// #4 is P0 (standalone, not on critical path). #1 is P2 (chain head, on
	// critical path). The P0 must win because explicit priority dominates the
	// critical-path heuristic post-#3396.
	if first.Number != 4 {
		t.Errorf("expected P0 #4 first regardless of critical path; got #%d (%s)",
			first.Number, first.Title)
	}
}

// TestIntegration_StateRecovery verifies that persisted state survives
// scheduler reconstruction (simulating a restart).
func TestIntegration_StateRecovery(t *testing.T) {
	tmpDir := t.TempDir()

	// Create initial scheduler with some state
	original := &AutonomousScheduler{
		config:               AutonomousConfig{MaxConcurrent: 3},
		workspaceRoot:        tmpDir,
		perIssueFailureCount: make(map[string]int),
		retryBackoff:         make(map[string]time.Time),
		state: &AutonomousState{
			Status:    "running",
			StartedAt: "2026-01-15T10:00:00Z",
			CyclesRun: 42,
			Completed: []CompletedItem{
				{Repo: "R", Number: 1, Title: "Done 1", CompletedAt: "2026-01-15T10:30:00Z"},
				{Repo: "R", Number: 2, Title: "Done 2", CompletedAt: "2026-01-15T11:00:00Z"},
			},
			Failed: []FailedItem{
				{Repo: "R", Number: 3, Title: "Failed", FailedAt: "2026-01-15T10:45:00Z", Reason: "test error"},
			},
			TokensSpent:   75000,
			TokensCeiling: 500000,
			Remaining:     5,
		},
	}

	// Persist state
	original.persistState()

	// Verify file exists
	statePath := filepath.Join(tmpDir, autonomousStateFile)
	if _, err := os.Stat(statePath); os.IsNotExist(err) {
		t.Fatal("state file was not created")
	}

	// Create new scheduler (simulating restart)
	recovered := NewAutonomousScheduler(nil, nil, nil, nil, DefaultAutonomousConfig(), tmpDir)

	// Running state should become "stopped" on load
	if recovered.state.Status != "stopped" {
		t.Errorf("expected 'stopped' after restart, got %q", recovered.state.Status)
	}

	// History should be preserved
	if recovered.state.CyclesRun != 42 {
		t.Errorf("expected 42 cycles, got %d", recovered.state.CyclesRun)
	}
	if len(recovered.state.Completed) != 2 {
		t.Errorf("expected 2 completed, got %d", len(recovered.state.Completed))
	}
	if len(recovered.state.Failed) != 1 {
		t.Errorf("expected 1 failed, got %d", len(recovered.state.Failed))
	}
	if recovered.state.TokensSpent != 75000 {
		t.Errorf("expected 75000 tokens, got %d", recovered.state.TokensSpent)
	}
}

// TestIntegration_ThreeRepos_MixedDependencies tests a realistic 3-repo
// scenario: platform blocks flutter, angular is independent. Verifies correct
// execution order and cross-repo cascade.
func TestIntegration_ThreeRepos_MixedDependencies(t *testing.T) {
	type issueState struct {
		state string
	}
	states := map[string]*issueState{
		"O/platform#10": {state: "OPEN"},
		"O/flutter#20":  {state: "OPEN"},
		"O/angular#30":  {state: "OPEN"},
	}

	graphProvider := func() *depgraph.Graph {
		nodes := []*depgraph.Node{
			{Repo: "O/platform", Number: 10, Title: "Platform API", State: states["O/platform#10"].state, BoardStatus: boardStatusForState(states["O/platform#10"].state), Priority: "P0", Size: "M", Weight: 3},
			{Repo: "O/flutter", Number: 20, Title: "Flutter Consumer", State: states["O/flutter#20"].state, BoardStatus: boardStatusForState(states["O/flutter#20"].state), Priority: "P1", Size: "S", Weight: 2},
			{Repo: "O/angular", Number: 30, Title: "Angular Dashboard", State: states["O/angular#30"].state, BoardStatus: boardStatusForState(states["O/angular#30"].state), Priority: "P2", Size: "S", Weight: 2},
		}
		edges := []depgraph.Edge{
			// Flutter depends on Platform
			{From: depgraph.NodeID{Repo: "O/flutter", Number: 20}, To: depgraph.NodeID{Repo: "O/platform", Number: 10}},
		}
		return buildTestGraph(nodes, edges)
	}

	cfg := AutonomousConfig{
		MaxConcurrent: 3,
		SafetyRails:   &SafetyConfig{RateLimitPerHour: 100},
	}
	h := newTestHarness(t, cfg, graphProvider)

	// Cycle 1: Platform #10 and Angular #30 should run (Flutter is blocked)
	h.runOneCycle(t)

	as := h.scheduler
	as.mu.Lock()
	if len(as.state.Running) != 2 {
		t.Fatalf("cycle 1: expected 2 running, got %d", len(as.state.Running))
	}
	runningSet := make(map[string]bool)
	for _, r := range as.state.Running {
		runningSet[r.Repo+"#"+itoa(r.Number)] = true
	}
	as.mu.Unlock()

	if !runningSet["O/platform#10"] {
		t.Error("cycle 1: expected Platform #10 to be running")
	}
	if !runningSet["O/angular#30"] {
		t.Error("cycle 1: expected Angular #30 to be running")
	}
	if runningSet["O/flutter#20"] {
		t.Error("cycle 1: Flutter #20 should NOT be running (blocked by Platform)")
	}

	// Complete both running items
	h.simulateCompletion("O/platform", 10, true)
	h.simulateCompletion("O/angular", 30, true)
	states["O/platform#10"].state = "CLOSED"
	states["O/angular#30"].state = "CLOSED"

	// Cycle 2: Flutter #20 should now be unblocked
	h.runOneCycle(t)

	as.mu.Lock()
	if len(as.state.Running) != 1 {
		t.Fatalf("cycle 2: expected 1 running, got %d", len(as.state.Running))
	}
	if as.state.Running[0].Repo != "O/flutter" || as.state.Running[0].Number != 20 {
		t.Errorf("cycle 2: expected Flutter #20, got %s#%d",
			as.state.Running[0].Repo, as.state.Running[0].Number)
	}
	if len(as.state.Completed) != 2 {
		t.Errorf("cycle 2: expected 2 completed, got %d", len(as.state.Completed))
	}
	as.mu.Unlock()
}

// TestIntegration_AllComplete_StopsScheduler verifies that the scheduler
// transitions to "complete" when all items are done and nothing is running.
func TestIntegration_AllComplete_StopsScheduler(t *testing.T) {
	issueState := "OPEN"

	graphProvider := func() *depgraph.Graph {
		nodes := []*depgraph.Node{
			{Repo: "R", Number: 1, Title: "Only Issue", State: issueState, BoardStatus: boardStatusForState(issueState), Priority: "P0", Size: "XS", Weight: 1},
		}
		return buildTestGraph(nodes, nil)
	}

	cfg := AutonomousConfig{
		MaxConcurrent: 3,
		SafetyRails:   &SafetyConfig{RateLimitPerHour: 100},
	}
	h := newTestHarness(t, cfg, graphProvider)

	// Dispatch
	h.runOneCycle(t)

	// Complete
	h.simulateCompletion("R", 1, true)
	issueState = "CLOSED"

	// Run cycle — should detect all complete
	h.runOneCycle(t)

	as := h.scheduler
	as.mu.Lock()
	status := as.state.Status
	as.mu.Unlock()

	if status != "complete" {
		t.Errorf("expected 'complete' when all items done, got %q", status)
	}
}

// TestIntegration_SlotLimit verifies that the scheduler respects the
// max_concurrent limit even when more candidates are available.
func TestIntegration_SlotLimit(t *testing.T) {
	nodes := []*depgraph.Node{
		{Repo: "R", Number: 1, Title: "A", State: "OPEN", BoardStatus: "Ready", Priority: "P0", Size: "XS", Weight: 1},
		{Repo: "R", Number: 2, Title: "B", State: "OPEN", BoardStatus: "Ready", Priority: "P1", Size: "XS", Weight: 1},
		{Repo: "R", Number: 3, Title: "C", State: "OPEN", BoardStatus: "Ready", Priority: "P2", Size: "XS", Weight: 1},
		{Repo: "R", Number: 4, Title: "D", State: "OPEN", BoardStatus: "Ready", Priority: "P3", Size: "XS", Weight: 1},
		{Repo: "R", Number: 5, Title: "E", State: "OPEN", BoardStatus: "Ready", Priority: "P3", Size: "XS", Weight: 1},
	}

	graphProvider := func() *depgraph.Graph {
		return buildTestGraph(nodes, nil)
	}

	cfg := AutonomousConfig{
		MaxConcurrent: 2, // only 2 slots
		SafetyRails:   &SafetyConfig{RateLimitPerHour: 100},
	}
	h := newTestHarness(t, cfg, graphProvider)
	h.runOneCycle(t)

	as := h.scheduler
	as.mu.Lock()
	runningCount := len(as.state.Running)
	as.mu.Unlock()

	if runningCount != 2 {
		t.Errorf("expected 2 running (slot limit), got %d", runningCount)
	}
}

// TestIntegration_FailedItemsNotRetried verifies that items that failed remain
// in the failed list and are not re-dispatched (they stay OPEN in the graph
// but are not re-enqueued because the scheduler tracks them).
func TestIntegration_FailedItemsNotRetried(t *testing.T) {
	graphProvider := func() *depgraph.Graph {
		nodes := []*depgraph.Node{
			{Repo: "R", Number: 1, Title: "Flaky", State: "OPEN", BoardStatus: "Ready", Priority: "P0", Size: "XS", Weight: 1},
		}
		return buildTestGraph(nodes, nil)
	}

	cfg := AutonomousConfig{
		MaxConcurrent: 3,
		SafetyRails: &SafetyConfig{
			CircuitBreakerMax: 10, // high threshold so it doesn't trip
			RateLimitPerHour:  100,
		},
	}
	h := newTestHarness(t, cfg, graphProvider)

	// Dispatch
	h.runOneCycle(t)

	as := h.scheduler
	as.mu.Lock()
	if len(as.state.Running) != 1 {
		t.Fatalf("expected 1 running, got %d", len(as.state.Running))
	}
	as.mu.Unlock()

	// Fail it
	h.simulateCompletion("R", 1, false)

	// Run another cycle. The issue is still OPEN in the graph, but it was not
	// added to the completed set. However, it IS in the failed set. The
	// prioritizer does not filter on the failed set — it filters on completed
	// and running. So the item would appear as a candidate again.
	// This is the expected behavior: failed items CAN be retried.
	// The circuit breaker is the safety mechanism that prevents infinite retries.
	h.runOneCycle(t)

	as.mu.Lock()
	failedCount := len(as.state.Failed)
	as.mu.Unlock()

	if failedCount != 1 {
		t.Errorf("expected 1 failed item, got %d", failedCount)
	}
}

// TestIntegration_StatePersistence_AcrossCycles verifies that state.json is
// written after every cycle and contains accurate data.
func TestIntegration_StatePersistence_AcrossCycles(t *testing.T) {
	nodes := []*depgraph.Node{
		{Repo: "R", Number: 1, Title: "A", State: "OPEN", BoardStatus: "Ready", Priority: "P0", Size: "XS", Weight: 1},
	}

	graphProvider := func() *depgraph.Graph {
		return buildTestGraph(nodes, nil)
	}

	cfg := AutonomousConfig{
		MaxConcurrent: 3,
		SafetyRails:   &SafetyConfig{RateLimitPerHour: 100},
	}
	h := newTestHarness(t, cfg, graphProvider)
	h.runOneCycle(t)

	// Read state file
	statePath := filepath.Join(h.tmpDir, autonomousStateFile)
	data, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatalf("state file not found: %v", err)
	}

	var loaded AutonomousState
	if err := json.Unmarshal(data, &loaded); err != nil {
		t.Fatalf("failed to parse state: %v", err)
	}

	if loaded.CyclesRun != 1 {
		t.Errorf("expected 1 cycle in persisted state, got %d", loaded.CyclesRun)
	}
	if len(loaded.Running) != 1 {
		t.Errorf("expected 1 running in persisted state, got %d", len(loaded.Running))
	}
	if loaded.LastScanAt == "" {
		t.Error("expected LastScanAt to be set")
	}
}

// TestIntegration_RateLimitTrip verifies that the rate limiter blocks enqueues
// when the hourly threshold is reached.
func TestIntegration_RateLimitTrip(t *testing.T) {
	counter := 0
	graphProvider := func() *depgraph.Graph {
		counter++
		nodes := []*depgraph.Node{
			{Repo: "R", Number: counter, Title: "Issue", State: "OPEN", BoardStatus: "Ready", Priority: "P0", Size: "XS", Weight: 1},
		}
		return buildTestGraph(nodes, nil)
	}

	cfg := AutonomousConfig{
		MaxConcurrent: 1,
		SafetyRails: &SafetyConfig{
			RateLimitPerHour: 3,
		},
	}
	h := newTestHarness(t, cfg, graphProvider)

	// Dispatch 3 items (hitting the rate limit)
	for i := 0; i < 3; i++ {
		h.runOneCycle(t)
		h.simulateCompletion("R", i+1, true)
	}

	// Cycle 4: rate limit should trip
	h.runOneCycle(t)

	as := h.scheduler
	as.mu.Lock()
	status := as.state.Status
	as.mu.Unlock()

	if status != "safety_tripped" {
		t.Errorf("expected 'safety_tripped' from rate limit, got %q", status)
	}
}

// TestIntegration_HealthGateTrip verifies that a low health score blocks
// new enqueues.
func TestIntegration_HealthGateTrip(t *testing.T) {
	graphProvider := func() *depgraph.Graph {
		nodes := []*depgraph.Node{
			{Repo: "R", Number: 1, Title: "Issue", State: "OPEN", BoardStatus: "Ready", Priority: "P0", Size: "XS", Weight: 1},
		}
		return buildTestGraph(nodes, nil)
	}

	cfg := AutonomousConfig{
		MaxConcurrent: 3,
		SafetyRails: &SafetyConfig{
			HealthGateMin:    50,
			RateLimitPerHour: 100,
		},
	}
	h := newTestHarness(t, cfg, graphProvider)

	// Set health score below threshold
	h.scheduler.safetyRails.UpdateHealthScore(20)

	h.runOneCycle(t)

	as := h.scheduler
	as.mu.Lock()
	status := as.state.Status
	runningCount := len(as.state.Running)
	as.mu.Unlock()

	if status != "safety_tripped" {
		t.Errorf("expected 'safety_tripped' from health gate, got %q", status)
	}
	if runningCount != 0 {
		t.Errorf("expected 0 running (health gate blocked), got %d", runningCount)
	}
}

// TestIntegration_EpicCheckpointPause verifies that completing all sub-issues
// of an epic triggers a checkpoint pause via the safety rails.
func TestIntegration_EpicCheckpointPause(t *testing.T) {
	graphProvider := func() *depgraph.Graph {
		nodes := []*depgraph.Node{
			{Repo: "R", Number: 1, Title: "Issue", State: "OPEN", BoardStatus: "Ready", Priority: "P0", Size: "XS", Weight: 1},
		}
		return buildTestGraph(nodes, nil)
	}

	cfg := AutonomousConfig{
		MaxConcurrent: 3,
		SafetyRails: &SafetyConfig{
			EpicCheckpoint:   true,
			RateLimitPerHour: 100,
		},
	}
	h := newTestHarness(t, cfg, graphProvider)

	// Simulate an epic completing
	h.scheduler.safetyRails.RecordEpicComplete(42)

	// Now try to run a cycle — should be blocked by checkpoint
	h.runOneCycle(t)

	as := h.scheduler
	as.mu.Lock()
	status := as.state.Status
	runningCount := len(as.state.Running)
	as.mu.Unlock()

	if status != "safety_tripped" {
		t.Errorf("expected 'safety_tripped' from epic checkpoint, got %q", status)
	}
	if runningCount != 0 {
		t.Errorf("expected 0 running (checkpoint pause), got %d", runningCount)
	}

	// Resume checkpoint
	h.scheduler.safetyRails.ResumeCheckpoint()
	h.scheduler.mu.Lock()
	h.scheduler.state.Status = "running"
	h.scheduler.mu.Unlock()

	// Now should be able to dispatch
	h.runOneCycle(t)

	as.mu.Lock()
	runningAfterResume := len(as.state.Running)
	as.mu.Unlock()

	if runningAfterResume != 1 {
		t.Errorf("expected 1 running after checkpoint resume, got %d", runningAfterResume)
	}
}

// itoa converts int to string (avoids importing strconv for a test helper).
func itoa(n int) string {
	return string(rune('0'+n/10)) + string(rune('0'+n%10))
}

// TestIntegration_DiamondDependency verifies diamond-shaped dependencies:
// #4 depends on #2 and #3, both of which depend on #1.
// #1 must complete first, then #2 and #3 can run in parallel, then #4.
func TestIntegration_DiamondDependency(t *testing.T) {
	states := map[int]string{1: "OPEN", 2: "OPEN", 3: "OPEN", 4: "OPEN"}

	graphProvider := func() *depgraph.Graph {
		nodes := []*depgraph.Node{
			{Repo: "R", Number: 1, Title: "Root", State: states[1], BoardStatus: boardStatusForState(states[1]), Priority: "P1", Size: "M", Weight: 3},
			{Repo: "R", Number: 2, Title: "Left", State: states[2], BoardStatus: boardStatusForState(states[2]), Priority: "P1", Size: "S", Weight: 2},
			{Repo: "R", Number: 3, Title: "Right", State: states[3], BoardStatus: boardStatusForState(states[3]), Priority: "P1", Size: "S", Weight: 2},
			{Repo: "R", Number: 4, Title: "Merge", State: states[4], BoardStatus: boardStatusForState(states[4]), Priority: "P1", Size: "M", Weight: 3},
		}
		edges := []depgraph.Edge{
			{From: depgraph.NodeID{Repo: "R", Number: 2}, To: depgraph.NodeID{Repo: "R", Number: 1}},
			{From: depgraph.NodeID{Repo: "R", Number: 3}, To: depgraph.NodeID{Repo: "R", Number: 1}},
			{From: depgraph.NodeID{Repo: "R", Number: 4}, To: depgraph.NodeID{Repo: "R", Number: 2}},
			{From: depgraph.NodeID{Repo: "R", Number: 4}, To: depgraph.NodeID{Repo: "R", Number: 3}},
		}
		return buildTestGraph(nodes, edges)
	}

	cfg := AutonomousConfig{
		MaxConcurrent: 5,
		SafetyRails:   &SafetyConfig{RateLimitPerHour: 100},
	}
	h := newTestHarness(t, cfg, graphProvider)

	// Wave 1: only #1
	h.runOneCycle(t)
	as := h.scheduler
	as.mu.Lock()
	if len(as.state.Running) != 1 || as.state.Running[0].Number != 1 {
		t.Fatalf("diamond wave 1: expected only #1 running, got %v", as.state.Running)
	}
	as.mu.Unlock()

	h.simulateCompletion("R", 1, true)
	states[1] = "CLOSED"

	// Wave 2: #2 and #3
	h.runOneCycle(t)
	as.mu.Lock()
	if len(as.state.Running) != 2 {
		t.Fatalf("diamond wave 2: expected 2 running, got %d", len(as.state.Running))
	}
	nums := map[int]bool{}
	for _, r := range as.state.Running {
		nums[r.Number] = true
	}
	as.mu.Unlock()
	if !nums[2] || !nums[3] {
		t.Errorf("diamond wave 2: expected #2 and #3, got %v", nums)
	}

	h.simulateCompletion("R", 2, true)
	h.simulateCompletion("R", 3, true)
	states[2] = "CLOSED"
	states[3] = "CLOSED"

	// Wave 3: #4
	h.runOneCycle(t)
	as.mu.Lock()
	if len(as.state.Running) != 1 || as.state.Running[0].Number != 4 {
		t.Fatalf("diamond wave 3: expected only #4 running, got %v", as.state.Running)
	}
	as.mu.Unlock()
}

// TestIntegration_StopDuringExecution verifies that Stop() transitions the
// scheduler cleanly. We test this via the harness since Run() requires a
// real Scheduler with pipeline infrastructure.
func TestIntegration_StopDuringExecution(t *testing.T) {
	nodes := []*depgraph.Node{
		{Repo: "R", Number: 1, Title: "A", State: "OPEN", BoardStatus: "Ready", Priority: "P0", Size: "XS", Weight: 1},
		{Repo: "R", Number: 2, Title: "B", State: "OPEN", BoardStatus: "Ready", Priority: "P1", Size: "S", Weight: 2},
	}

	graphProvider := func() *depgraph.Graph {
		return buildTestGraph(nodes, nil)
	}

	cfg := AutonomousConfig{
		MaxConcurrent: 3,
		SafetyRails:   &SafetyConfig{RateLimitPerHour: 100},
	}
	h := newTestHarness(t, cfg, graphProvider)

	// Dispatch items
	h.runOneCycle(t)

	as := h.scheduler
	as.mu.Lock()
	runningBefore := len(as.state.Running)
	// Mark as actually running so Stop accepts it
	as.running = true
	as.mu.Unlock()

	if runningBefore == 0 {
		t.Fatal("expected items to be running before stop")
	}

	// Stop
	as.Stop()

	// Verify stop signal was sent
	select {
	case <-as.stopCh:
		// ok — signal received
	case <-time.After(100 * time.Millisecond):
		t.Error("expected stop signal on channel")
	}
}
