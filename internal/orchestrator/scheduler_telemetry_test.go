package orchestrator

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/execution"
	"github.com/nightgauge/nightgauge/internal/platform"
	"github.com/nightgauge/nightgauge/internal/state"
	"github.com/nightgauge/nightgauge/pkg/types"
)

// mockTelemetry records EmitPipelineEvent, PushPipelineRun, and SyncQueue calls
// for assertion.
type mockTelemetry struct {
	mu         sync.Mutex
	events     []platform.PipelineEvent
	runs       []state.V2RunRecord
	queueSyncs [][]platform.QueueSyncItem
}

func (m *mockTelemetry) EmitPipelineEvent(_ context.Context, event platform.PipelineEvent) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.events = append(m.events, event)
}

func (m *mockTelemetry) PushPipelineRun(_ context.Context, record state.V2RunRecord) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.runs = append(m.runs, record)
}

func (m *mockTelemetry) SyncQueue(_ context.Context, items []platform.QueueSyncItem) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.queueSyncs = append(m.queueSyncs, items)
}

// lastQueueSync returns the most recent queue snapshot pushed via SyncQueue.
func (m *mockTelemetry) lastQueueSync() ([]platform.QueueSyncItem, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.queueSyncs) == 0 {
		return nil, false
	}
	return m.queueSyncs[len(m.queueSyncs)-1], true
}

func (m *mockTelemetry) emittedTypes() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	types := make([]string, len(m.events))
	for i, e := range m.events {
		types[i] = e.EventType
	}
	return types
}

func (m *mockTelemetry) firstOfType(eventType string) (platform.PipelineEvent, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, e := range m.events {
		if e.EventType == eventType {
			return e, true
		}
	}
	return platform.PipelineEvent{}, false
}

// mockAlwaysFailStageRunner returns exit code 1 for every stage.
type mockAlwaysFailStageRunner struct{}

func (m *mockAlwaysFailStageRunner) RunStage(_ context.Context, _ StageRunParams) (*StageRunResult, error) {
	return &StageRunResult{ExitCode: 1}, fmt.Errorf("stage failed: mock error")
}

// mockAlwaysSucceedStageRunner returns success for every stage and writes
// a minimal output context file when one is expected. Mirrors what real
// stage runners do — without it, the scheduler's #2870 output-context
// validation would (correctly) flag the stage as failed.
type mockAlwaysSucceedStageRunner struct{}

func (m *mockAlwaysSucceedStageRunner) RunStage(_ context.Context, params StageRunParams) (*StageRunResult, error) {
	if params.OutputFile != "" {
		if err := os.MkdirAll(filepath.Dir(params.OutputFile), 0755); err == nil {
			_ = os.WriteFile(params.OutputFile, []byte(`{"ok":true}`), 0644)
		}
	}
	return &StageRunResult{
		ExitCode:     0,
		InputTokens:  100,
		OutputTokens: 50,
	}, nil
}

// writeSkillFile creates a minimal SKILL.md at the expected location for a stage.
func writeSkillFile(t *testing.T, workspaceRoot string, stageDir string) {
	t.Helper()
	dir := filepath.Join(workspaceRoot, "skills", stageDir)
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatalf("create skill dir: %v", err)
	}
	content := "---\nname: test-stage\nallowed-tools: Read Write\n---\n# Test Stage\n"
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(content), 0644); err != nil {
		t.Fatalf("write SKILL.md: %v", err)
	}
}

// buildTelemetryTestScheduler creates a minimal scheduler suitable for telemetry tests.
// It sets up skill files for the given stages in tmpDir.
func buildTelemetryTestScheduler(t *testing.T, tmpDir string, svc telemetryService, stageRunner StageRunner, stageDirs []string) *Scheduler {
	t.Helper()
	for _, dir := range stageDirs {
		writeSkillFile(t, tmpDir, dir)
	}

	issueSvc := newMockIssueSvc()
	// mockIssueSvc returns error for unknown issues — checkEpicCompletion logs and returns.

	s := &Scheduler{
		repoRunning:      make(map[string]int),
		mergeLocks:       make(map[string]*sync.Mutex),
		retryEngine:      NewRetryEngine(DefaultRetryConfig()),
		budgetEngine:     NewBudgetEnforcer(DefaultBudgetConfig()),
		ralphEngine:      NewRalphLoopController(DefaultRalphConfig()),
		issueSvc:         issueSvc,
		execMgr:          execution.NewManager(tmpDir, nil),
		stageRunner:      stageRunner,
		telemetrySvc:     svc,
		telemetryEnabled: true,
	}
	return s
}

func TestTelemetryStageStartedEmitted(t *testing.T) {
	tmpDir := t.TempDir()
	mock := &mockTelemetry{}
	s := buildTelemetryTestScheduler(t, tmpDir, mock, &mockAlwaysFailStageRunner{}, []string{
		"nightgauge-issue-pickup",
	})

	item := types.BoardItem{Number: 42, Repo: "nightgauge/test", ID: "item-42"}
	s.runPipeline(context.Background(), item)

	evt, ok := mock.firstOfType("stage_started")
	if !ok {
		t.Fatal("expected stage_started event, none recorded")
	}
	if evt.IssueNumber != 42 {
		t.Errorf("stage_started.IssueNumber = %d, want 42", evt.IssueNumber)
	}
	if evt.Stage != string(state.StageIssuePickup) {
		t.Errorf("stage_started.Stage = %q, want %q", evt.Stage, state.StageIssuePickup)
	}
	if evt.SchemaVersion != "1" {
		t.Errorf("stage_started.SchemaVersion = %q, want 1", evt.SchemaVersion)
	}
	if _, ok := evt.Metadata["model"]; !ok {
		t.Error("stage_started.Metadata missing 'model' key")
	}
}

func TestTelemetryStageErrorEmitted(t *testing.T) {
	tmpDir := t.TempDir()
	mock := &mockTelemetry{}
	s := buildTelemetryTestScheduler(t, tmpDir, mock, &mockAlwaysFailStageRunner{}, []string{
		"nightgauge-issue-pickup",
	})
	// Disable escalation so the error is terminal.
	s.retryEngine = NewRetryEngine(RetryConfig{MaxEscalationsPerStage: 0})

	item := types.BoardItem{Number: 42, Repo: "nightgauge/test", ID: "item-42"}
	s.runPipeline(context.Background(), item)

	evt, ok := mock.firstOfType("stage_error")
	if !ok {
		t.Fatal("expected stage_error event, none recorded")
	}
	if evt.IssueNumber != 42 {
		t.Errorf("stage_error.IssueNumber = %d, want 42", evt.IssueNumber)
	}
	if evt.Stage != string(state.StageIssuePickup) {
		t.Errorf("stage_error.Stage = %q, want %q", evt.Stage, state.StageIssuePickup)
	}
	if evt.SchemaVersion != "1" {
		t.Errorf("stage_error.SchemaVersion = %q, want 1", evt.SchemaVersion)
	}
	if _, ok := evt.Metadata["error"]; !ok {
		t.Error("stage_error.Metadata missing 'error' key")
	}
	if _, ok := evt.Metadata["exit_code"]; !ok {
		t.Error("stage_error.Metadata missing 'exit_code' key")
	}
}

func TestTelemetryStageErrorNotEmittedOnEscalation(t *testing.T) {
	tmpDir := t.TempDir()
	mock := &mockTelemetry{}
	// Use default retry config which allows escalations.
	s := buildTelemetryTestScheduler(t, tmpDir, mock, &mockAlwaysFailStageRunner{}, []string{
		"nightgauge-issue-pickup",
	})

	item := types.BoardItem{Number: 42, Repo: "nightgauge/test", ID: "item-42"}
	s.runPipeline(context.Background(), item)

	// When escalation occurs, the stage is retried. After max escalations the terminal
	// stage_error IS emitted. This test verifies the event count is exactly 1
	// (one terminal failure, not one per escalation attempt).
	var errCount int
	for _, e := range mock.events {
		if e.EventType == "stage_error" {
			errCount++
		}
	}
	if errCount != 1 {
		t.Errorf("want exactly 1 stage_error event, got %d", errCount)
	}
}

func TestTelemetryStageCompletedEmitted(t *testing.T) {
	tmpDir := t.TempDir()
	mock := &mockTelemetry{}
	// Only create the skill file for issue-pickup. After it succeeds (and the
	// mock writes the output context — see mockAlwaysSucceedStageRunner),
	// feature-planning will fail to load its skill file and the pipeline returns.
	// Either way, exactly one stage_completed event should be emitted.
	s := buildTelemetryTestScheduler(t, tmpDir, mock, &mockAlwaysSucceedStageRunner{}, []string{
		"nightgauge-issue-pickup",
	})

	item := types.BoardItem{Number: 42, Repo: "nightgauge/test", ID: "item-42"}
	s.runPipeline(context.Background(), item)

	evt, ok := mock.firstOfType("stage_completed")
	if !ok {
		t.Fatal("expected stage_completed event, none recorded")
	}
	if evt.IssueNumber != 42 {
		t.Errorf("stage_completed.IssueNumber = %d, want 42", evt.IssueNumber)
	}
	if evt.Stage != string(state.StageIssuePickup) {
		t.Errorf("stage_completed.Stage = %q, want %q", evt.Stage, state.StageIssuePickup)
	}
	if evt.SchemaVersion != "1" {
		t.Errorf("stage_completed.SchemaVersion = %q, want 1", evt.SchemaVersion)
	}
	for _, key := range []string{"input_tokens", "output_tokens", "model"} {
		if _, ok := evt.Metadata[key]; !ok {
			t.Errorf("stage_completed.Metadata missing %q key", key)
		}
	}
}

func TestTelemetryPipelineDoneEmittedViaDefer(t *testing.T) {
	tmpDir := t.TempDir()
	mock := &mockTelemetry{}
	// Pipeline fails immediately — defer still fires.
	s := buildTelemetryTestScheduler(t, tmpDir, mock, &mockAlwaysFailStageRunner{}, []string{
		"nightgauge-issue-pickup",
	})
	s.retryEngine = NewRetryEngine(RetryConfig{MaxEscalationsPerStage: 0})

	item := types.BoardItem{Number: 42, Repo: "nightgauge/test", ID: "item-42"}
	s.runPipeline(context.Background(), item)

	evt, ok := mock.firstOfType("pipeline_done")
	if !ok {
		t.Fatal("expected pipeline_done event, none recorded")
	}
	if evt.IssueNumber != 42 {
		t.Errorf("pipeline_done.IssueNumber = %d, want 42", evt.IssueNumber)
	}
	if evt.Stage != "" {
		t.Errorf("pipeline_done.Stage = %q, want empty string", evt.Stage)
	}
	if evt.SchemaVersion != "1" {
		t.Errorf("pipeline_done.SchemaVersion = %q, want 1", evt.SchemaVersion)
	}
	if _, ok := evt.Metadata["success"]; !ok {
		t.Error("pipeline_done.Metadata missing 'success' key")
	}
}

func TestTelemetryDisabledNoEventsEmitted(t *testing.T) {
	tmpDir := t.TempDir()
	mock := &mockTelemetry{}
	s := buildTelemetryTestScheduler(t, tmpDir, mock, &mockAlwaysFailStageRunner{}, []string{
		"nightgauge-issue-pickup",
	})
	// Disable telemetry — no events should be recorded.
	s.telemetryEnabled = false

	item := types.BoardItem{Number: 42, Repo: "nightgauge/test", ID: "item-42"}
	s.runPipeline(context.Background(), item)

	if len(mock.events) > 0 {
		t.Errorf("expected no telemetry events when disabled, got %d: %v", len(mock.events), mock.emittedTypes())
	}
}

func TestTelemetryNilServiceNoPanic(t *testing.T) {
	tmpDir := t.TempDir()
	// nil telemetrySvc with telemetryEnabled=true must not panic.
	s := buildTelemetryTestScheduler(t, tmpDir, nil, &mockAlwaysFailStageRunner{}, []string{
		"nightgauge-issue-pickup",
	})
	s.telemetrySvc = nil
	s.telemetryEnabled = true

	item := types.BoardItem{Number: 42, Repo: "nightgauge/test", ID: "item-42"}
	// Should not panic
	s.runPipeline(context.Background(), item)
}

func TestTelemetryServiceInterfaceSatisfied(t *testing.T) {
	// Compile-time assertion: *platform.TelemetryService satisfies telemetryService.
	// If this test file compiles, the interface is satisfied.
	var _ telemetryService = (*platform.TelemetryService)(nil)
}

// TestPersistQueueSyncsSnapshot verifies persistQueue mirrors the queue to the
// platform, mapping status/priority and filtering terminal items.
func TestPersistQueueSyncsSnapshot(t *testing.T) {
	tmpDir := t.TempDir()
	mock := &mockTelemetry{}
	s := buildTelemetryTestScheduler(t, tmpDir, mock, &mockAlwaysSucceedStageRunner{}, nil)
	s.workspaceRoot = tmpDir
	s.queue = []QueueItem{
		{IssueNumber: 10, Position: 1, Status: "processing", Repo: "nightgauge/test", Title: "Working", Labels: []string{"priority:high"}},
		{IssueNumber: 11, Position: 2, Status: "pending", Repo: "nightgauge/test", Title: "Queued"},
		{IssueNumber: 12, Position: 3, Status: "completed", Repo: "nightgauge/test", Title: "Done"},
	}

	s.persistQueue()

	items, ok := mock.lastQueueSync()
	if !ok {
		t.Fatal("expected a queue sync, none recorded")
	}
	if len(items) != 2 {
		t.Fatalf("want 2 synced items (completed filtered out), got %d: %+v", len(items), items)
	}

	// Item 10: processing + priority:high label.
	if items[0].IssueNumber != 10 || items[0].Status != "processing" || items[0].Priority != "high" {
		t.Errorf("item[0] = %+v, want issue 10 / processing / high", items[0])
	}
	if items[0].RepoFullName != "nightgauge/test" || items[0].Title != "Working" {
		t.Errorf("item[0] repo/title = %q/%q", items[0].RepoFullName, items[0].Title)
	}
	// Item 11: pending, no priority label → default medium.
	if items[1].IssueNumber != 11 || items[1].Status != "pending" || items[1].Priority != "medium" {
		t.Errorf("item[1] = %+v, want issue 11 / pending / medium", items[1])
	}
}

// TestPersistQueueNoSyncWhenTelemetryDisabled verifies the sync is gated by the
// telemetry flag.
func TestPersistQueueNoSyncWhenTelemetryDisabled(t *testing.T) {
	tmpDir := t.TempDir()
	mock := &mockTelemetry{}
	s := buildTelemetryTestScheduler(t, tmpDir, mock, &mockAlwaysSucceedStageRunner{}, nil)
	s.workspaceRoot = tmpDir
	s.telemetryEnabled = false
	s.queue = []QueueItem{{IssueNumber: 10, Position: 1, Status: "pending"}}

	s.persistQueue()

	if _, ok := mock.lastQueueSync(); ok {
		t.Error("expected no queue sync when telemetry disabled")
	}
}

// TestPersistQueueNilTelemetryNoPanic verifies persistQueue is safe with no
// telemetry service configured.
func TestPersistQueueNilTelemetryNoPanic(t *testing.T) {
	tmpDir := t.TempDir()
	s := buildTelemetryTestScheduler(t, tmpDir, nil, &mockAlwaysSucceedStageRunner{}, nil)
	s.telemetrySvc = nil
	s.workspaceRoot = tmpDir
	s.queue = []QueueItem{{IssueNumber: 10, Position: 1, Status: "pending"}}

	s.persistQueue() // must not panic
}

func TestQueueStatusToPlatform(t *testing.T) {
	cases := map[string]string{
		"processing": "processing",
		"pending":    "pending",
		"ready":      "pending",
		"paused":     "pending",
		"completed":  "",
		"failed":     "",
		"weird":      "",
	}
	for in, want := range cases {
		if got := queueStatusToPlatform(in); got != want {
			t.Errorf("queueStatusToPlatform(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestQueuePriorityFromLabels(t *testing.T) {
	cases := []struct {
		labels []string
		want   string
	}{
		{[]string{"priority:critical"}, "critical"},
		{[]string{"priority:high"}, "high"},
		{[]string{"priority:medium"}, "medium"},
		{[]string{"priority:low"}, "low"},
		{[]string{"type:feature"}, "medium"},
		{nil, "medium"},
	}
	for _, tc := range cases {
		if got := queuePriorityFromLabels(tc.labels); got != tc.want {
			t.Errorf("queuePriorityFromLabels(%v) = %q, want %q", tc.labels, got, tc.want)
		}
	}
}

// TestCacheHitRateInV2Record verifies that per-stage cache_hit_rate is correctly
// populated when recording V2 history via the history writer (Issue #2459).
func TestCacheHitRateInV2Record(t *testing.T) {
	rs := state.NewRuntimeState("nightgauge/nightgauge", 2459, "item-test")

	rs.BeginStage(state.StageIssuePickup)
	// 100 actual input + 50 cache_read → InputTokens=150, CacheRead=50
	rs.CompleteStageWithCost(0, 100, 200, 50, 0.01)

	rs.BeginStage(state.StageFeaturePlanning)
	// 300 actual input + 0 cache_read → InputTokens=300, CacheRead=0
	rs.CompleteStageWithCost(0, 300, 100, 0, 0.02)

	hw := state.NewHistoryWriter(t.TempDir())
	input := state.V2RunInput{Title: "cache hit rate test", Branch: "feat/2459"}
	record := hw.BuildV2Record(rs, true, "", input, time.Now())

	pickup, ok := record.Tokens.PerStage[string(state.StageIssuePickup)]
	if !ok {
		t.Fatal("issue-pickup per-stage tokens missing")
	}
	if pickup.CacheHitRate == nil {
		t.Fatal("issue-pickup CacheHitRate should not be nil")
	}
	wantPickup := 50.0 / 150.0
	if got := *pickup.CacheHitRate; got < wantPickup-0.001 || got > wantPickup+0.001 {
		t.Errorf("issue-pickup CacheHitRate = %f, want %.4f", got, wantPickup)
	}
	if pickup.CacheRead != 50 {
		t.Errorf("issue-pickup CacheRead = %d, want 50", pickup.CacheRead)
	}
	if pickup.Input != 100 {
		t.Errorf("issue-pickup Input = %d, want 100 (non-cached)", pickup.Input)
	}

	planning, ok := record.Tokens.PerStage[string(state.StageFeaturePlanning)]
	if !ok {
		t.Fatal("feature-planning per-stage tokens missing")
	}
	if planning.CacheHitRate == nil {
		t.Fatal("feature-planning CacheHitRate should not be nil when input > 0")
	}
	if got := *planning.CacheHitRate; got > 0.001 {
		t.Errorf("feature-planning CacheHitRate = %f, want 0.0 (no cache)", got)
	}
}
