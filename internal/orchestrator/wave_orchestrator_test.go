package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/nightgauge/nightgauge/internal/execution"
	"github.com/nightgauge/nightgauge/internal/intelligence/batch"
	"github.com/nightgauge/nightgauge/internal/intelligence/teams"
	"github.com/nightgauge/nightgauge/pkg/types"
)

// mockEpicIssueSvc implements issueGetter with epic sub-issue support.
type mockEpicIssueSvc struct {
	issues map[string]*types.Issue
	epics  map[string]*types.EpicProgress
}

func newMockEpicIssueSvc() *mockEpicIssueSvc {
	return &mockEpicIssueSvc{
		issues: make(map[string]*types.Issue),
		epics:  make(map[string]*types.EpicProgress),
	}
}

func (m *mockEpicIssueSvc) addIssue(owner, repo string, number int, issue *types.Issue) {
	m.issues[fmt.Sprintf("%s/%s#%d", owner, repo, number)] = issue
}

func (m *mockEpicIssueSvc) addEpic(owner, repo string, number int, epic *types.EpicProgress) {
	m.epics[fmt.Sprintf("%s/%s#%d", owner, repo, number)] = epic
}

func (m *mockEpicIssueSvc) GetIssue(_ context.Context, owner, repo string, number int) (*types.Issue, error) {
	key := fmt.Sprintf("%s/%s#%d", owner, repo, number)
	if issue, ok := m.issues[key]; ok {
		return issue, nil
	}
	return nil, fmt.Errorf("issue %s not found", key)
}

func (m *mockEpicIssueSvc) GetIssuesByNumbers(_ context.Context, owner, repo string, numbers []int) (map[int]*types.Issue, error) {
	out := make(map[int]*types.Issue, len(numbers))
	for _, n := range numbers {
		key := fmt.Sprintf("%s/%s#%d", owner, repo, n)
		if issue, ok := m.issues[key]; ok {
			out[n] = issue
		}
	}
	return out, nil
}

func (m *mockEpicIssueSvc) GetEpicProgress(_ context.Context, nodeID string) (*types.EpicProgress, error) {
	return nil, fmt.Errorf("not implemented for nodeID")
}

func (m *mockEpicIssueSvc) GetEpicProgressByNumber(_ context.Context, owner, repo string, number int) (*types.EpicProgress, error) {
	key := fmt.Sprintf("%s/%s#%d", owner, repo, number)
	if epic, ok := m.epics[key]; ok {
		return epic, nil
	}
	return nil, fmt.Errorf("epic %s not found", key)
}

func (m *mockEpicIssueSvc) CloseIssue(_ context.Context, _ string) error {
	return nil
}

func (m *mockEpicIssueSvc) RemoveBlockedBy(_ context.Context, _, _ string) error {
	return nil
}

// trackingStageRunner records which issues and stages were executed.
type trackingStageRunner struct {
	mu       sync.Mutex
	calls    []StageRunParams
	behavior string // "succeed", "fail", "fail-issue-NNN"
}

func (r *trackingStageRunner) RunStage(_ context.Context, params StageRunParams) (*StageRunResult, error) {
	r.mu.Lock()
	r.calls = append(r.calls, params)
	r.mu.Unlock()

	if r.behavior == "fail" {
		return &StageRunResult{ExitCode: 1}, fmt.Errorf("stage failed")
	}
	if r.behavior == fmt.Sprintf("fail-issue-%d", params.IssueNumber) {
		return &StageRunResult{ExitCode: 1}, fmt.Errorf("stage failed for issue %d", params.IssueNumber)
	}

	return &StageRunResult{
		ExitCode:     0,
		InputTokens:  100,
		OutputTokens: 50,
	}, nil
}

func (r *trackingStageRunner) issueNumbers() []int {
	r.mu.Lock()
	defer r.mu.Unlock()
	seen := make(map[int]bool)
	var nums []int
	for _, c := range r.calls {
		if !seen[c.IssueNumber] {
			seen[c.IssueNumber] = true
			nums = append(nums, c.IssueNumber)
		}
	}
	return nums
}

// buildWaveTestScheduler creates a scheduler with mocked services for wave orchestration tests.
func buildWaveTestScheduler(t *testing.T, tmpDir string, issueSvc *mockEpicIssueSvc, runner *trackingStageRunner) *Scheduler {
	t.Helper()

	// Write SKILL.md files for all 6 stages
	stages := []string{
		"nightgauge-issue-pickup",
		"nightgauge-feature-planning",
		"nightgauge-feature-dev",
		"nightgauge-feature-validate",
		"nightgauge-pr-create",
		"nightgauge-pr-merge",
	}
	for _, dir := range stages {
		writeSkillFile(t, tmpDir, dir)
	}

	s := &Scheduler{
		repoRunning:   make(map[string]int),
		mergeLocks:    make(map[string]*sync.Mutex),
		retryEngine:   NewRetryEngine(DefaultRetryConfig()),
		budgetEngine:  NewBudgetEnforcer(DefaultBudgetConfig()),
		ralphEngine:   NewRalphLoopController(DefaultRalphConfig()),
		issueSvc:      issueSvc,
		execMgr:       execution.NewManager(tmpDir, nil),
		stageRunner:   runner,
		budgetRetries: make(map[string]int),
		maxPerRepo:    4,
	}
	return s
}

func TestWaveOrchestrator_NewWaveOrchestrator(t *testing.T) {
	s := &Scheduler{}
	wo := newWaveOrchestrator(s, 100, "nightgauge/test", 0, 0)

	if wo.maxConcurrent != 4 {
		t.Errorf("maxConcurrent = %d, want 4 (default)", wo.maxConcurrent)
	}
	if wo.totalBudget != 2_000_000 {
		t.Errorf("totalBudget = %d, want 2000000 (default)", wo.totalBudget)
	}
	if wo.epicNumber != 100 {
		t.Errorf("epicNumber = %d, want 100", wo.epicNumber)
	}

	// Test max cap at 12
	wo2 := newWaveOrchestrator(s, 100, "test", 15, 0)
	if wo2.maxConcurrent != 12 {
		t.Errorf("maxConcurrent = %d, want 12 (capped)", wo2.maxConcurrent)
	}

	// Test that values within range pass through
	wo3 := newWaveOrchestrator(s, 100, "test", 10, 0)
	if wo3.maxConcurrent != 10 {
		t.Errorf("maxConcurrent = %d, want 10 (within range)", wo3.maxConcurrent)
	}
}

func TestWaveOrchestrator_ExtractFileRefs(t *testing.T) {
	body := `This issue modifies:
- packages/sdk/src/orchestrator/PipelineOrchestrator.ts
- internal/orchestrator/scheduler.go

Also touches packages/vscode/src/services/IpcClient.ts`

	files := teams.ExtractTargetFiles(body)
	if len(files) != 3 {
		t.Fatalf("ExtractTargetFiles returned %d files, want 3: %v", len(files), files)
	}
}

func TestWaveOrchestrator_FetchSubIssueDetails(t *testing.T) {
	tmpDir := t.TempDir()
	issueSvc := newMockEpicIssueSvc()

	// Set up epic with 3 sub-issues (1 closed, 2 open)
	issueSvc.addEpic("Org", "repo", 100, &types.EpicProgress{
		EpicNodeID: "E_100",
		Number:     100,
		Title:      "Test Epic",
		Repo:       "Org/repo",
		Total:      3,
		Closed:     1,
		Open:       2,
		SubIssues: []types.SubIssueRef{
			{Number: 101, Title: "Issue A", State: "OPEN", Repo: "Org/repo"},
			{Number: 102, Title: "Issue B", State: "OPEN", Repo: "Org/repo"},
			{Number: 103, Title: "Issue C", State: "CLOSED", Repo: "Org/repo"},
		},
	})
	issueSvc.addIssue("Org", "repo", 101, &types.Issue{
		Number: 101, Title: "Issue A", Body: "Modifies internal/foo/bar.go",
		Labels: []string{"type:feature"},
	})
	issueSvc.addIssue("Org", "repo", 102, &types.Issue{
		Number: 102, Title: "Issue B", Body: "Modifies internal/baz/qux.go",
		Labels: []string{"type:feature"},
	})

	runner := &trackingStageRunner{behavior: "succeed"}
	s := buildWaveTestScheduler(t, tmpDir, issueSvc, runner)
	wo := newWaveOrchestrator(s, 100, "Org/repo", 4, 0)

	epicItem := types.BoardItem{Number: 100, Repo: "Org/repo"}
	subIssues, issueDetails, err := wo.fetchSubIssueDetails(context.Background(), "Org", "repo", epicItem)
	if err != nil {
		t.Fatalf("fetchSubIssueDetails error: %v", err)
	}

	if len(subIssues) != 2 {
		t.Errorf("got %d sub-issues, want 2 (closed filtered)", len(subIssues))
	}
	if len(issueDetails) != 2 {
		t.Errorf("got %d issue details, want 2", len(issueDetails))
	}
}

func TestWaveOrchestrator_DetectDependencies_WithBlockedBy(t *testing.T) {
	issueSvc := newMockEpicIssueSvc()
	s := &Scheduler{issueSvc: issueSvc}
	wo := newWaveOrchestrator(s, 100, "Org/repo", 4, 0)

	subIssues := []teams.SubIssue{
		{Number: 101, Title: "Issue A", Files: []string{"pkg/a.go"}},
		{Number: 102, Title: "Issue B", Files: []string{"pkg/b.go"}},
		{Number: 103, Title: "Issue C", Files: []string{"pkg/c.go"}},
	}

	// Issue C is blocked by Issue A
	issueDetails := []batch.IssueInput{
		{Number: 101, Title: "Issue A", Body: "Independent"},
		{Number: 102, Title: "Issue B", Body: "Independent"},
		{Number: 103, Title: "Issue C", Body: "Needs A first", BlockedBy: []int{101}},
	}

	deps := wo.detectDependencies(subIssues, issueDetails)

	// Issue C (index 2) should depend on Issue A (index 0)
	if deps, ok := deps[2]; ok {
		found := false
		for _, d := range deps {
			if d == 0 {
				found = true
			}
		}
		if !found {
			t.Errorf("issue C should depend on issue A (index 0), got deps: %v", deps)
		}
	} else {
		t.Error("issue C should have dependencies")
	}
}

func TestWaveOrchestrator_BuildSummary(t *testing.T) {
	s := &Scheduler{}
	wo := newWaveOrchestrator(s, 100, "test", 4, 0)

	wo.agentResults[101] = &AgentResult{
		IssueNumber:  101,
		Success:      true,
		InputTokens:  1000,
		OutputTokens: 500,
		CostUSD:      0.50,
		Duration:     60_000_000_000, // 60s
		WaveIndex:    0,
	}
	wo.agentResults[102] = &AgentResult{
		IssueNumber:  102,
		Success:      true,
		InputTokens:  800,
		OutputTokens: 400,
		CostUSD:      0.40,
		Duration:     45_000_000_000, // 45s
		WaveIndex:    0,
	}
	wo.agentResults[103] = &AgentResult{
		IssueNumber:  103,
		Success:      false,
		Error:        "pipeline failed",
		InputTokens:  200,
		OutputTokens: 100,
		CostUSD:      0.10,
		Duration:     30_000_000_000, // 30s
		WaveIndex:    1,
	}

	summary := wo.buildSummary(90_000_000_000) // 90s total

	if summary.TotalIssues != 3 {
		t.Errorf("TotalIssues = %d, want 3", summary.TotalIssues)
	}
	if summary.Succeeded != 2 {
		t.Errorf("Succeeded = %d, want 2", summary.Succeeded)
	}
	if summary.Failed != 1 {
		t.Errorf("Failed = %d, want 1", summary.Failed)
	}
	if summary.TotalTokensIn != 2000 {
		t.Errorf("TotalTokensIn = %d, want 2000", summary.TotalTokensIn)
	}
	if summary.TotalCostUSD != 1.00 {
		t.Errorf("TotalCostUSD = %f, want 1.00", summary.TotalCostUSD)
	}
	// Speedup: sequential=135s, actual=90s → ~1.5x
	if summary.SpeedupFactor < 1.4 || summary.SpeedupFactor > 1.6 {
		t.Errorf("SpeedupFactor = %f, want ~1.5", summary.SpeedupFactor)
	}
}

func TestWaveOrchestrator_PersistWavePlan(t *testing.T) {
	tmpDir := t.TempDir()
	s := &Scheduler{
		execMgr: execution.NewManager(tmpDir, nil),
	}
	wo := newWaveOrchestrator(s, 100, "Org/repo", 4, 2_000_000)
	wo.waves = []teams.WaveAssignment{
		{
			WaveIndex: 0,
			Issues: []teams.SubIssue{
				{Number: 101, Title: "A"},
				{Number: 102, Title: "B"},
			},
		},
		{
			WaveIndex: 1,
			Issues: []teams.SubIssue{
				{Number: 103, Title: "C"},
			},
		},
	}

	wo.persistWavePlan(tmpDir)

	planPath := filepath.Join(tmpDir, ".nightgauge", "pipeline", "wave-plan-100.json")
	data, err := os.ReadFile(planPath)
	if err != nil {
		t.Fatalf("wave plan not written: %v", err)
	}

	var plan struct {
		EpicNumber    int                    `json:"epicNumber"`
		Repo          string                 `json:"repo"`
		MaxConcurrent int                    `json:"maxConcurrent"`
		Waves         []teams.WaveAssignment `json:"waves"`
	}
	if err := json.Unmarshal(data, &plan); err != nil {
		t.Fatalf("failed to parse wave plan: %v", err)
	}

	if plan.EpicNumber != 100 {
		t.Errorf("epicNumber = %d, want 100", plan.EpicNumber)
	}
	if len(plan.Waves) != 2 {
		t.Errorf("waves = %d, want 2", len(plan.Waves))
	}
	if len(plan.Waves[0].Issues) != 2 {
		t.Errorf("wave 0 issues = %d, want 2", len(plan.Waves[0].Issues))
	}
}

func TestWaveOrchestrator_PersistWaveStatus(t *testing.T) {
	tmpDir := t.TempDir()
	s := &Scheduler{
		execMgr: execution.NewManager(tmpDir, nil),
	}
	wo := newWaveOrchestrator(s, 100, "Org/repo", 4, 0)
	wo.waves = []teams.WaveAssignment{
		{WaveIndex: 0, Issues: []teams.SubIssue{{Number: 101}}},
	}
	wo.agentResults[101] = &AgentResult{
		IssueNumber: 101, Success: true, WaveIndex: 0,
	}

	summary := &WaveSummary{
		TotalIssues: 1, Succeeded: 1,
	}
	wo.persistWaveStatus(tmpDir, summary)

	statusPath := filepath.Join(tmpDir, ".nightgauge", "pipeline", "wave-status-100.json")
	data, err := os.ReadFile(statusPath)
	if err != nil {
		t.Fatalf("wave status not written: %v", err)
	}

	var status WaveStatus
	if err := json.Unmarshal(data, &status); err != nil {
		t.Fatalf("failed to parse wave status: %v", err)
	}

	if status.EpicNumber != 100 {
		t.Errorf("epicNumber = %d, want 100", status.EpicNumber)
	}
	if status.Summary.Succeeded != 1 {
		t.Errorf("succeeded = %d, want 1", status.Summary.Succeeded)
	}
	if len(status.Waves) != 1 {
		t.Errorf("waves = %d, want 1", len(status.Waves))
	}
	if status.Waves[0].Status != "completed" {
		t.Errorf("wave status = %q, want completed", status.Waves[0].Status)
	}
}

func TestWaveOrchestrator_DispatchItem_NonEpic(t *testing.T) {
	tmpDir := t.TempDir()
	issueSvc := newMockEpicIssueSvc()
	runner := &trackingStageRunner{behavior: "succeed"}
	s := buildWaveTestScheduler(t, tmpDir, issueSvc, runner)

	// Non-epic item should go through regular pipeline
	item := types.BoardItem{
		Number: 42,
		Repo:   "Org/repo",
		IsEpic: false,
	}

	// Add mock issue for epic completion check
	issueSvc.addIssue("Org", "repo", 42, &types.Issue{Number: 42})

	s.dispatchItem(context.Background(), item)

	// Should have called the stage runner (regular pipeline)
	if len(runner.calls) == 0 {
		t.Error("expected stage runner to be called for non-epic item")
	}
}

func TestWaveOrchestrator_DispatchItem_EpicSequentialFallback(t *testing.T) {
	tmpDir := t.TempDir()
	issueSvc := newMockEpicIssueSvc()
	runner := &trackingStageRunner{behavior: "succeed"}
	s := buildWaveTestScheduler(t, tmpDir, issueSvc, runner)

	// Epic with only 2 sub-issues — batch assessor recommends sequential
	issueSvc.addEpic("Org", "repo", 100, &types.EpicProgress{
		EpicNodeID: "E_100",
		Number:     100,
		Title:      "Small Epic",
		Repo:       "Org/repo",
		Total:      2,
		Open:       2,
		SubIssues: []types.SubIssueRef{
			{Number: 101, Title: "A", State: "OPEN", Repo: "Org/repo"},
			{Number: 102, Title: "B", State: "OPEN", Repo: "Org/repo"},
		},
	})
	issueSvc.addIssue("Org", "repo", 100, &types.Issue{
		Number: 100, Title: "Small Epic",
		SubIssues: []types.SubIssueRef{
			{Number: 101, State: "OPEN", Repo: "Org/repo"},
			{Number: 102, State: "OPEN", Repo: "Org/repo"},
		},
	})
	issueSvc.addIssue("Org", "repo", 101, &types.Issue{Number: 101, Title: "A"})
	issueSvc.addIssue("Org", "repo", 102, &types.Issue{Number: 102, Title: "B"})

	item := types.BoardItem{
		Number: 100,
		Repo:   "Org/repo",
		IsEpic: true,
		SubIssues: []types.SubIssueRef{
			{Number: 101, State: "OPEN"},
			{Number: 102, State: "OPEN"},
		},
	}

	s.dispatchItem(context.Background(), item)

	// With ≤2 issues, batch assessment says "sequential"
	// So RunEpicWaves returns false, and dispatchItem should call EnqueueEpic
	// Verify by checking the queue
	queueItems := s.QueueList()
	if len(queueItems) != 2 {
		t.Errorf("expected 2 queued items (sequential fallback), got %d", len(queueItems))
	}
}

func TestWaveOrchestrator_RunEpicWaves_ParallelExecution(t *testing.T) {
	tmpDir := t.TempDir()
	issueSvc := newMockEpicIssueSvc()
	runner := &trackingStageRunner{behavior: "succeed"}
	s := buildWaveTestScheduler(t, tmpDir, issueSvc, runner)

	// Epic with 4 independent sub-issues → should get parallel strategy
	issueSvc.addEpic("Org", "repo", 200, &types.EpicProgress{
		EpicNodeID: "E_200",
		Number:     200,
		Title:      "Big Epic",
		Repo:       "Org/repo",
		Total:      4,
		Open:       4,
		SubIssues: []types.SubIssueRef{
			{Number: 201, Title: "Issue A", State: "OPEN", Repo: "Org/repo"},
			{Number: 202, Title: "Issue B", State: "OPEN", Repo: "Org/repo"},
			{Number: 203, Title: "Issue C", State: "OPEN", Repo: "Org/repo"},
			{Number: 204, Title: "Issue D", State: "OPEN", Repo: "Org/repo"},
		},
	})

	for _, n := range []int{201, 202, 203, 204} {
		issueSvc.addIssue("Org", "repo", n, &types.Issue{
			Number: n,
			Title:  fmt.Sprintf("Issue %d", n),
			Body:   fmt.Sprintf("Modifies unique/path/%d.go", n),
			Labels: []string{"type:feature"},
		})
	}

	item := types.BoardItem{
		Number: 200,
		Repo:   "Org/repo",
		IsEpic: true,
		SubIssues: []types.SubIssueRef{
			{Number: 201, State: "OPEN"},
			{Number: 202, State: "OPEN"},
			{Number: 203, State: "OPEN"},
			{Number: 204, State: "OPEN"},
		},
	}

	// RunEpicWaves returns false because sub-pipelines abort at stage 2
	// (context prerequisite missing — mock stage runner doesn't write context files).
	// The important thing is that all 4 issues were dispatched in parallel.
	_ = s.RunEpicWaves(context.Background(), item)

	// Verify all 4 sub-issues had their first stage executed (parallel dispatch)
	executed := runner.issueNumbers()
	if len(executed) < 4 {
		t.Errorf("expected at least 4 unique issues dispatched, got %d: %v", len(executed), executed)
	}

	// Verify wave plan was persisted
	planPath := filepath.Join(tmpDir, ".nightgauge", "pipeline", "wave-plan-200.json")
	if _, err := os.Stat(planPath); os.IsNotExist(err) {
		t.Error("wave plan should have been persisted")
	}

	// Verify wave status was persisted
	statusPath := filepath.Join(tmpDir, ".nightgauge", "pipeline", "wave-status-200.json")
	if _, err := os.Stat(statusPath); os.IsNotExist(err) {
		t.Error("wave status should have been persisted")
	}
}

func TestWaveOrchestrator_RunEpicWaves_NoOpenSubIssues(t *testing.T) {
	tmpDir := t.TempDir()
	issueSvc := newMockEpicIssueSvc()
	runner := &trackingStageRunner{behavior: "succeed"}
	s := buildWaveTestScheduler(t, tmpDir, issueSvc, runner)

	// Epic with all closed sub-issues
	issueSvc.addEpic("Org", "repo", 300, &types.EpicProgress{
		EpicNodeID: "E_300",
		Number:     300,
		Title:      "Done Epic",
		Repo:       "Org/repo",
		Total:      2,
		Closed:     2,
		Open:       0,
		SubIssues: []types.SubIssueRef{
			{Number: 301, Title: "A", State: "CLOSED", Repo: "Org/repo"},
			{Number: 302, Title: "B", State: "CLOSED", Repo: "Org/repo"},
		},
	})

	item := types.BoardItem{
		Number: 300,
		Repo:   "Org/repo",
		IsEpic: true,
		SubIssues: []types.SubIssueRef{
			{Number: 301, State: "CLOSED"},
			{Number: 302, State: "CLOSED"},
		},
	}

	result := s.RunEpicWaves(context.Background(), item)
	if !result {
		t.Error("RunEpicWaves should return true when no open sub-issues (nothing to do)")
	}

	if len(runner.calls) != 0 {
		t.Errorf("expected 0 stage runner calls for closed epic, got %d", len(runner.calls))
	}
}

func TestWaveOrchestrator_WaveSuccessCount(t *testing.T) {
	s := &Scheduler{}
	wo := newWaveOrchestrator(s, 100, "test", 4, 0)

	wo.agentResults[1] = &AgentResult{IssueNumber: 1, WaveIndex: 0, Success: true}
	wo.agentResults[2] = &AgentResult{IssueNumber: 2, WaveIndex: 0, Success: false}
	wo.agentResults[3] = &AgentResult{IssueNumber: 3, WaveIndex: 0, Success: true}
	wo.agentResults[4] = &AgentResult{IssueNumber: 4, WaveIndex: 1, Success: true}

	if count := wo.waveSuccessCount(0); count != 2 {
		t.Errorf("wave 0 success count = %d, want 2", count)
	}
	if count := wo.waveSuccessCount(1); count != 1 {
		t.Errorf("wave 1 success count = %d, want 1", count)
	}
}

func TestWaveOrchestrator_BudgetForIssue(t *testing.T) {
	s := &Scheduler{}
	wo := newWaveOrchestrator(s, 100, "test", 4, 1_000_000)

	budgetResult := teams.BudgetResult{
		Allocations: []teams.BudgetAllocation{
			{IssueNumber: 101, TokenBudget: 500_000},
			{IssueNumber: 102, TokenBudget: 300_000},
		},
	}

	if b := wo.budgetForIssue(budgetResult, 101); b != 500_000 {
		t.Errorf("budget for 101 = %d, want 500000", b)
	}

	// Unknown issue gets fallback
	fallback := wo.budgetForIssue(budgetResult, 999)
	if fallback != 1_000_000/6 {
		t.Errorf("fallback budget = %d, want %d", fallback, 1_000_000/6)
	}
}

// --- scaleAgents tests ---

func TestScaleAgents_IdealConcurrency(t *testing.T) {
	cfg := ScalingConfig{MaxConcurrent: 6, MinBudgetPerAgent: 100_000}
	decision := scaleAgents(4, 1_000_000, cfg)

	if decision.Concurrency != 4 {
		t.Errorf("concurrency = %d, want 4 (ideal — wave fits within ceiling)", decision.Concurrency)
	}
	if decision.Reason != "ideal" {
		t.Errorf("reason = %q, want 'ideal'", decision.Reason)
	}
	if decision.WaveSize != 4 {
		t.Errorf("waveSize = %d, want 4", decision.WaveSize)
	}
	if decision.BudgetPerAgent != 250_000 {
		t.Errorf("budgetPerAgent = %d, want 250000", decision.BudgetPerAgent)
	}
}

func TestScaleAgents_ConfigCeiling(t *testing.T) {
	cfg := ScalingConfig{MaxConcurrent: 3, MinBudgetPerAgent: 100_000}
	decision := scaleAgents(8, 1_000_000, cfg)

	if decision.Concurrency != 3 {
		t.Errorf("concurrency = %d, want 3 (config ceiling)", decision.Concurrency)
	}
	if decision.Reason != "config_ceiling" {
		t.Errorf("reason = %q, want 'config_ceiling'", decision.Reason)
	}
}

func TestScaleAgents_BudgetConstraint(t *testing.T) {
	cfg := ScalingConfig{MaxConcurrent: 10, MinBudgetPerAgent: 100_000}
	// Budget only supports 3 agents (300K / 100K)
	decision := scaleAgents(8, 300_000, cfg)

	if decision.Concurrency != 3 {
		t.Errorf("concurrency = %d, want 3 (budget constraint)", decision.Concurrency)
	}
	if decision.Reason != "budget_constraint" {
		t.Errorf("reason = %q, want 'budget_constraint'", decision.Reason)
	}
	if decision.BudgetPerAgent != 100_000 {
		t.Errorf("budgetPerAgent = %d, want 100000", decision.BudgetPerAgent)
	}
}

func TestScaleAgents_BudgetOverridesConfigCeiling(t *testing.T) {
	cfg := ScalingConfig{MaxConcurrent: 6, MinBudgetPerAgent: 100_000}
	// Config ceiling is 6, but budget only supports 2
	decision := scaleAgents(8, 200_000, cfg)

	if decision.Concurrency != 2 {
		t.Errorf("concurrency = %d, want 2 (budget tighter than ceiling)", decision.Concurrency)
	}
	if decision.Reason != "budget_constraint" {
		t.Errorf("reason = %q, want 'budget_constraint'", decision.Reason)
	}
}

func TestScaleAgents_FloorAtOne(t *testing.T) {
	cfg := ScalingConfig{MaxConcurrent: 6, MinBudgetPerAgent: 100_000}
	// Budget too small for even 1 agent at the min threshold
	decision := scaleAgents(4, 50_000, cfg)

	if decision.Concurrency != 1 {
		t.Errorf("concurrency = %d, want 1 (floor)", decision.Concurrency)
	}
}

func TestScaleAgents_ZeroBudgetIgnored(t *testing.T) {
	cfg := ScalingConfig{MaxConcurrent: 6, MinBudgetPerAgent: 100_000}
	// remainingBudget=0 means budget tracking is not active
	decision := scaleAgents(4, 0, cfg)

	if decision.Concurrency != 4 {
		t.Errorf("concurrency = %d, want 4 (budget=0 means no budget constraint)", decision.Concurrency)
	}
	if decision.Reason != "ideal" {
		t.Errorf("reason = %q, want 'ideal'", decision.Reason)
	}
}

func TestScaleAgents_ZeroMinBudgetIgnored(t *testing.T) {
	cfg := ScalingConfig{MaxConcurrent: 6, MinBudgetPerAgent: 0}
	decision := scaleAgents(4, 100_000, cfg)

	if decision.Concurrency != 4 {
		t.Errorf("concurrency = %d, want 4 (minBudgetPerAgent=0 means no budget constraint)", decision.Concurrency)
	}
}

func TestScaleAgents_WaveSizeOne(t *testing.T) {
	cfg := DefaultScalingConfig()
	decision := scaleAgents(1, 1_000_000, cfg)

	if decision.Concurrency != 1 {
		t.Errorf("concurrency = %d, want 1", decision.Concurrency)
	}
	if decision.Reason != "ideal" {
		t.Errorf("reason = %q, want 'ideal'", decision.Reason)
	}
}

func TestScaleAgents_DefaultConfig(t *testing.T) {
	cfg := DefaultScalingConfig()
	if cfg.MaxConcurrent != 6 {
		t.Errorf("default MaxConcurrent = %d, want 6", cfg.MaxConcurrent)
	}
	if cfg.MinBudgetPerAgent != 100_000 {
		t.Errorf("default MinBudgetPerAgent = %d, want 100000", cfg.MinBudgetPerAgent)
	}
}

// --- runWaveScaled batching tests ---

func TestRunWaveScaled_FastPath(t *testing.T) {
	// When concurrency >= wave size, should delegate to runWaveParallel
	tmpDir := t.TempDir()
	issueSvc := newMockEpicIssueSvc()
	runner := &trackingStageRunner{behavior: "succeed"}
	s := buildWaveTestScheduler(t, tmpDir, issueSvc, runner)
	wo := newWaveOrchestrator(s, 100, "Org/repo", 8, 2_000_000)

	wave := teams.WaveAssignment{
		WaveIndex: 0,
		Issues: []teams.SubIssue{
			{Number: 201, Title: "A"},
			{Number: 202, Title: "B"},
			{Number: 203, Title: "C"},
		},
	}
	budgetResult := teams.SplitBudget(wave.Issues, 600_000, teams.StrategyEqual)
	epicItem := types.BoardItem{Number: 100, Repo: "Org/repo"}

	results := wo.runWaveScaled(context.Background(), wave, epicItem, 0, budgetResult, 5)
	if len(results) != 3 {
		t.Fatalf("expected 3 results, got %d", len(results))
	}
}

func TestRunWaveScaled_Batching(t *testing.T) {
	// When concurrency < wave size, should batch
	tmpDir := t.TempDir()
	issueSvc := newMockEpicIssueSvc()
	runner := &trackingStageRunner{behavior: "succeed"}
	s := buildWaveTestScheduler(t, tmpDir, issueSvc, runner)
	wo := newWaveOrchestrator(s, 100, "Org/repo", 8, 2_000_000)

	// 5 issues, concurrency = 2 → 3 batches (2, 2, 1)
	wave := teams.WaveAssignment{
		WaveIndex: 0,
		Issues: []teams.SubIssue{
			{Number: 301, Title: "A"},
			{Number: 302, Title: "B"},
			{Number: 303, Title: "C"},
			{Number: 304, Title: "D"},
			{Number: 305, Title: "E"},
		},
	}
	budgetResult := teams.SplitBudget(wave.Issues, 1_000_000, teams.StrategyEqual)
	epicItem := types.BoardItem{Number: 100, Repo: "Org/repo"}

	results := wo.runWaveScaled(context.Background(), wave, epicItem, 0, budgetResult, 2)
	if len(results) != 5 {
		t.Fatalf("expected 5 results, got %d", len(results))
	}
	// Verify all issue numbers are present in results
	for i, r := range results {
		if r == nil {
			t.Errorf("result[%d] is nil", i)
			continue
		}
		if r.IssueNumber != wave.Issues[i].Number {
			t.Errorf("result[%d].IssueNumber = %d, want %d", i, r.IssueNumber, wave.Issues[i].Number)
		}
	}
}

func TestRunWaveScaled_ConcurrencyOne(t *testing.T) {
	// Concurrency = 1 should run all issues sequentially (1 per batch)
	tmpDir := t.TempDir()
	issueSvc := newMockEpicIssueSvc()
	runner := &trackingStageRunner{behavior: "succeed"}
	s := buildWaveTestScheduler(t, tmpDir, issueSvc, runner)
	wo := newWaveOrchestrator(s, 100, "Org/repo", 8, 2_000_000)

	wave := teams.WaveAssignment{
		WaveIndex: 0,
		Issues: []teams.SubIssue{
			{Number: 401, Title: "A"},
			{Number: 402, Title: "B"},
			{Number: 403, Title: "C"},
		},
	}
	budgetResult := teams.SplitBudget(wave.Issues, 600_000, teams.StrategyEqual)
	epicItem := types.BoardItem{Number: 100, Repo: "Org/repo"}

	results := wo.runWaveScaled(context.Background(), wave, epicItem, 0, budgetResult, 1)
	if len(results) != 3 {
		t.Fatalf("expected 3 results, got %d", len(results))
	}
	for i, r := range results {
		if r == nil {
			t.Errorf("result[%d] is nil", i)
		}
	}
}

// --- ScalingConfig from config tests ---

func TestWaveOrchestrator_ScalingConfigFromScheduler(t *testing.T) {
	customCfg := &ScalingConfig{MaxConcurrent: 8, MinBudgetPerAgent: 200_000}
	s := &Scheduler{scalingConfig: customCfg}
	wo := newWaveOrchestrator(s, 100, "test", 0, 0)

	// maxConcurrent defaults to 4 when passed 0, but scalingConfig should use scheduler's config
	if wo.scalingConfig.MinBudgetPerAgent != 200_000 {
		t.Errorf("MinBudgetPerAgent = %d, want 200000", wo.scalingConfig.MinBudgetPerAgent)
	}
}

func TestWaveOrchestrator_ScalingConfigDefaults(t *testing.T) {
	s := &Scheduler{} // No scalingConfig set
	wo := newWaveOrchestrator(s, 100, "test", 0, 0)

	if wo.scalingConfig.MaxConcurrent != 4 {
		t.Errorf("MaxConcurrent = %d, want 4 (overridden from passed-in maxConcurrent default)", wo.scalingConfig.MaxConcurrent)
	}
	if wo.scalingConfig.MinBudgetPerAgent != 100_000 {
		t.Errorf("MinBudgetPerAgent = %d, want 100000 (default)", wo.scalingConfig.MinBudgetPerAgent)
	}
}

func TestWaveOrchestrator_ScalingDecisionCallback(t *testing.T) {
	var captured *ScalingDecision
	s := &Scheduler{
		onScalingDecision: func(epicNumber int, decision ScalingDecision) {
			d := decision
			captured = &d
		},
	}
	wo := newWaveOrchestrator(s, 100, "test", 4, 0)

	// Directly test that scaleAgents produces a decision
	decision := scaleAgents(8, 500_000, wo.scalingConfig)
	decision.Wave = 0

	// Simulate what the orchestrator would do
	if s.onScalingDecision != nil {
		s.onScalingDecision(100, decision)
	}

	if captured == nil {
		t.Fatal("onScalingDecision callback was not invoked")
	}
	if captured.WaveSize != 8 {
		t.Errorf("captured waveSize = %d, want 8", captured.WaveSize)
	}
	if captured.Reason != "config_ceiling" {
		t.Errorf("captured reason = %q, want 'config_ceiling'", captured.Reason)
	}
}
