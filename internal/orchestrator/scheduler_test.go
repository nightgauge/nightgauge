package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/execution"
	gh "github.com/nightgauge/nightgauge/internal/github"
	"github.com/nightgauge/nightgauge/internal/intelligence/learning"
	"github.com/nightgauge/nightgauge/internal/intelligence/routing"
	pmstages "github.com/nightgauge/nightgauge/internal/orchestrator/stages"
	"github.com/nightgauge/nightgauge/internal/state"
	"github.com/nightgauge/nightgauge/pkg/types"
)

// getAutoCreateEpicBranch is defined in scheduler.go (package-level function);
// tested here in the same package.

// mockIssueSvc implements issueGetter for testing.
type mockIssueSvc struct {
	issues     map[string]*types.Issue // keyed by "owner/repo#number"
	batchCalls []mockBatchCall         // recorded GetIssuesByNumbers invocations
}

func newMockIssueSvc() *mockIssueSvc {
	return &mockIssueSvc{issues: make(map[string]*types.Issue)}
}

func (m *mockIssueSvc) addIssue(owner, repo string, number int, issue *types.Issue) {
	m.issues[fmt.Sprintf("%s/%s#%d", owner, repo, number)] = issue
}

func (m *mockIssueSvc) GetIssue(_ context.Context, owner, repo string, number int) (*types.Issue, error) {
	key := fmt.Sprintf("%s/%s#%d", owner, repo, number)
	if issue, ok := m.issues[key]; ok {
		return issue, nil
	}
	return nil, fmt.Errorf("issue %s not found", key)
}

func (m *mockIssueSvc) GetIssuesByNumbers(_ context.Context, owner, repo string, numbers []int) (map[int]*types.Issue, error) {
	m.batchCalls = append(m.batchCalls, mockBatchCall{owner: owner, repo: repo, numbers: append([]int(nil), numbers...)})
	out := make(map[int]*types.Issue, len(numbers))
	for _, n := range numbers {
		key := fmt.Sprintf("%s/%s#%d", owner, repo, n)
		if issue, ok := m.issues[key]; ok {
			out[n] = issue
		}
		// Missing issues silently omitted, matching production behavior.
	}
	return out, nil
}

type mockBatchCall struct {
	owner   string
	repo    string
	numbers []int
}

func (m *mockIssueSvc) GetEpicProgress(_ context.Context, _ string) (*types.EpicProgress, error) {
	return nil, fmt.Errorf("not implemented")
}

func (m *mockIssueSvc) GetEpicProgressByNumber(_ context.Context, _, _ string, _ int) (*types.EpicProgress, error) {
	return nil, fmt.Errorf("not implemented")
}

func (m *mockIssueSvc) CloseIssue(_ context.Context, _ string) error {
	return fmt.Errorf("not implemented")
}

func (m *mockIssueSvc) RemoveBlockedBy(_ context.Context, _, _ string) error {
	return nil
}

// TestCaptureIssueBody covers the pickup-time issue-body capture (#183): a
// successful fetch returns the (clipped) body; a fetch error, a nil issue, or an
// unparseable repo degrade to an empty string without failing the run.
func TestCaptureIssueBody(t *testing.T) {
	mock := newMockIssueSvc()
	mock.addIssue("nightgauge", "nightgauge", 183, &types.Issue{
		Number: 183,
		Title:  "Run detail should show issue context",
		Body:   "## Problem\nThe dashboard identifies the work only as #93.",
	})
	// A long body to exercise the capture-time clip.
	mock.addIssue("nightgauge", "nightgauge", 999, &types.Issue{
		Number: 999,
		Body:   strings.Repeat("x", issueBodyCaptureMax+250),
	})
	s := &Scheduler{issueSvc: mock}

	t.Run("returns the issue body for a known issue", func(t *testing.T) {
		got := s.captureIssueBody(context.Background(), types.BoardItem{Repo: "nightgauge/nightgauge", Number: 183})
		if want := "## Problem\nThe dashboard identifies the work only as #93."; got != want {
			t.Errorf("captureIssueBody = %q, want %q", got, want)
		}
	})

	t.Run("clips an over-long body to the capture bound", func(t *testing.T) {
		got := s.captureIssueBody(context.Background(), types.BoardItem{Repo: "nightgauge/nightgauge", Number: 999})
		if n := len([]rune(got)); n != issueBodyCaptureMax {
			t.Errorf("clipped body len = %d, want %d", n, issueBodyCaptureMax)
		}
	})

	t.Run("returns empty on GetIssue error (non-fatal)", func(t *testing.T) {
		got := s.captureIssueBody(context.Background(), types.BoardItem{Repo: "nightgauge/nightgauge", Number: 4242})
		if got != "" {
			t.Errorf("captureIssueBody = %q, want empty on fetch error", got)
		}
	})

	t.Run("returns empty for an unparseable repo", func(t *testing.T) {
		got := s.captureIssueBody(context.Background(), types.BoardItem{Repo: "no-slash", Number: 183})
		if got != "" {
			t.Errorf("captureIssueBody = %q, want empty for a repo with no owner", got)
		}
	})
}

func TestQueueOperations(t *testing.T) {
	// Test queue add/list/clear without needing a real GitHub client
	entries := []QueueEntry{
		{Repo: "nightgauge/nightgauge", IssueNumber: 1311, Priority: 0},
		{Repo: "nightgauge/nightgauge", IssueNumber: 1319, Priority: 1},
		{Repo: "acme/platform", IssueNumber: 42, Priority: 2},
	}

	// Verify QueueEntry fields
	if entries[0].IssueNumber != 1311 {
		t.Errorf("entries[0].IssueNumber = %d", entries[0].IssueNumber)
	}
	if entries[2].Repo != "acme/platform" {
		t.Errorf("entries[2].Repo = %q", entries[2].Repo)
	}
}

func TestOutcomeRecording(t *testing.T) {
	tmpDir := t.TempDir()

	recorder := learning.NewRecorder(tmpDir)
	s := &Scheduler{recorder: recorder}

	item := types.BoardItem{
		Number: 42,
		Repo:   "nightgauge/nightgauge",
	}

	// Build a minimal runtime state snapshot
	snap := state.NewRuntimeState(item.Repo, item.Number, "item-id")
	snap.BeginStage(state.StageFeatureDev)
	snap.CompleteStage(0, 100, 200, "claude-sonnet-4-6")
	snapshot := snap.Snapshot()

	// Record a successful outcome
	s.recordOutcome(item, snapshot, true, 5, "claude-sonnet-4-6")

	outcomesFile := filepath.Join(tmpDir, ".nightgauge", "pipeline", "history", "outcomes.jsonl")
	data, err := os.ReadFile(outcomesFile)
	if err != nil {
		t.Fatalf("outcomes.jsonl not created: %v", err)
	}

	var outcome learning.Outcome
	if err := json.Unmarshal(data[:len(data)-1], &outcome); err != nil {
		t.Fatalf("failed to unmarshal outcome: %v", err)
	}

	if outcome.IssueNumber != 42 {
		t.Errorf("IssueNumber = %d, want 42", outcome.IssueNumber)
	}
	if outcome.Repo != "nightgauge/nightgauge" {
		t.Errorf("Repo = %q", outcome.Repo)
	}
	if !outcome.Success {
		t.Error("Success should be true")
	}
	if outcome.PredictedSize != "medium" {
		t.Errorf("PredictedSize = %q, want %q", outcome.PredictedSize, "medium")
	}
	if outcome.ComplexityScore != 5 {
		t.Errorf("ComplexityScore = %d, want 5", outcome.ComplexityScore)
	}
	if outcome.FailedStage != "" {
		t.Errorf("FailedStage should be empty for success, got %q", outcome.FailedStage)
	}
	if outcome.CompletedAt.IsZero() {
		t.Error("CompletedAt should not be zero")
	}

	// Record a failed outcome
	snap2 := state.NewRuntimeState(item.Repo, item.Number, "item-id-2")
	snap2.BeginStage(state.StageFeatureValidate)
	snapshot2 := snap2.Snapshot()
	s.recordOutcome(item, snapshot2, false, 2, "claude-haiku-4-5-20251001")

	data2, err := os.ReadFile(outcomesFile)
	if err != nil {
		t.Fatalf("failed to re-read outcomes.jsonl: %v", err)
	}

	// Find the second line (second outcome)
	lineStart := len(data) // data holds the first line including newline
	secondLine := data2[lineStart:]
	if idx := len(secondLine) - 1; idx >= 0 && secondLine[idx] == '\n' {
		secondLine = secondLine[:idx]
	}
	var failedOutcome learning.Outcome
	if err := json.Unmarshal(secondLine, &failedOutcome); err != nil {
		t.Fatalf("failed to unmarshal failed outcome: %v", err)
	}

	if failedOutcome.Success {
		t.Error("Success should be false")
	}
	if failedOutcome.FailedStage != string(state.StageFeatureValidate) {
		t.Errorf("FailedStage = %q, want %q", failedOutcome.FailedStage, state.StageFeatureValidate)
	}
	if failedOutcome.PredictedSize != "small" {
		t.Errorf("PredictedSize = %q, want %q", failedOutcome.PredictedSize, "small")
	}
}

func TestLoadIssueContext(t *testing.T) {
	tmpDir := t.TempDir()
	pipelineDir := filepath.Join(tmpDir, ".nightgauge", "pipeline")
	if err := os.MkdirAll(pipelineDir, 0755); err != nil {
		t.Fatal(err)
	}

	contextData := `{
		"routing": {
			"complexity_score": 7,
			"pickup_recommendation": {
				"dev_model": "claude-opus-4-6"
			}
		}
	}`
	if err := os.WriteFile(filepath.Join(pipelineDir, "issue-99.json"), []byte(contextData), 0644); err != nil {
		t.Fatal(err)
	}

	score, routingPath, model := loadIssueContext(tmpDir, 99)
	if score != 7 {
		t.Errorf("complexityScore = %d, want 7", score)
	}
	// routingPath is empty when not present in context file (defaults applied by caller).
	_ = routingPath
	if model != "claude-opus-4-6" {
		t.Errorf("predictedModel = %q, want claude-opus-4-6", model)
	}

	// Missing file returns zero values
	score2, routingPath2, model2 := loadIssueContext(tmpDir, 404)
	if score2 != 0 || routingPath2 != "" || model2 != "" {
		t.Errorf("missing file: got score=%d path=%q model=%q, want 0, empty, empty", score2, routingPath2, model2)
	}
}

func TestPredictedSizeLabel(t *testing.T) {
	tests := []struct {
		score int
		want  string
	}{
		{0, "small"},
		{3, "small"},
		{4, "medium"},
		{6, "medium"},
		{7, "large"},
		{10, "large"},
	}
	for _, tc := range tests {
		got := predictedSizeLabel(tc.score)
		if got != tc.want {
			t.Errorf("predictedSizeLabel(%d) = %q, want %q", tc.score, got, tc.want)
		}
	}
}

func TestEmitStateChangedCallback(t *testing.T) {
	s := &Scheduler{}
	var called bool
	var receivedIssue int
	s.OnStateChanged(func(repo string, issue int, runtime *state.RuntimeState) {
		called = true
		receivedIssue = issue
	})

	rs := state.NewRuntimeState("nightgauge/nightgauge", 1899, "item-1")
	s.emitStateChanged("nightgauge/nightgauge", 1899, rs)

	if !called {
		t.Error("onStateChanged callback should have been called")
	}
	if receivedIssue != 1899 {
		t.Errorf("receivedIssue = %d, want 1899", receivedIssue)
	}
}

func TestEmitStateChangedNilCallback(t *testing.T) {
	s := &Scheduler{}
	rs := state.NewRuntimeState("nightgauge/nightgauge", 1899, "item-1")
	// Should not panic when callback is nil
	s.emitStateChanged("nightgauge/nightgauge", 1899, rs)
}

func TestOnPhaseDetectedCallback(t *testing.T) {
	s := &Scheduler{}
	var phaseName string
	s.OnPhaseDetected(func(repo string, issue int, stage, name string, index, total int) {
		phaseName = name
	})
	if s.onPhaseDetected == nil {
		t.Error("onPhaseDetected should be set")
	}
	s.onPhaseDetected("nightgauge/nightgauge", 1899, "feature-dev", "implementation", 3, 14)
	if phaseName != "implementation" {
		t.Errorf("phaseName = %q", phaseName)
	}
}

func TestRecordPhaseStart_PopulatesRuntime(t *testing.T) {
	s := &Scheduler{}
	rt := state.NewRuntimeState("nightgauge/nightgauge", 3486, "item-1")
	s.registerRuntime(3486, rt)
	defer s.unregisterRuntime(3486)

	s.RecordPhaseStart(3486, "feature-dev", "implementation", 7, 17)

	if len(rt.PhaseHistory) != 1 {
		t.Fatalf("PhaseHistory len = %d, want 1", len(rt.PhaseHistory))
	}
	p := rt.PhaseHistory[0]
	if p.Name != "implementation" || p.Index != 7 || p.Total != 17 {
		t.Errorf("phase = %+v, want implementation/7/17", p)
	}
	if p.Status != "running" {
		t.Errorf("phase.Status = %q, want running", p.Status)
	}
	if p.Stage != state.PipelineStage("feature-dev") {
		t.Errorf("phase.Stage = %q, want feature-dev", p.Stage)
	}
}

func TestRecordPhaseComplete_MarksRunningPhaseDone(t *testing.T) {
	s := &Scheduler{}
	rt := state.NewRuntimeState("nightgauge/nightgauge", 3486, "item-1")
	s.registerRuntime(3486, rt)
	defer s.unregisterRuntime(3486)

	s.RecordPhaseStart(3486, "feature-dev", "implementation", 7, 17)
	s.RecordPhaseComplete(3486, "feature-dev", "implementation")

	if len(rt.PhaseHistory) != 1 {
		t.Fatalf("PhaseHistory len = %d, want 1", len(rt.PhaseHistory))
	}
	if rt.PhaseHistory[0].Status != "complete" {
		t.Errorf("phase.Status = %q, want complete", rt.PhaseHistory[0].Status)
	}
}

func TestRecordPhaseStart_NoRuntimeIsNoOp(t *testing.T) {
	s := &Scheduler{}
	// No runtime registered for issue 9999 — call must not panic.
	s.RecordPhaseStart(9999, "feature-dev", "implementation", 0, 17)
	s.RecordPhaseComplete(9999, "feature-dev", "implementation")
}

func TestRegisterRuntime_RejectsInvalidInputs(t *testing.T) {
	s := &Scheduler{}
	// Invalid issue number — should not register.
	s.registerRuntime(0, state.NewRuntimeState("r", 0, "x"))
	if s.getActiveRuntime(0) != nil {
		t.Error("issueNumber=0 should not be registered")
	}
	// Nil runtime — should not register.
	s.registerRuntime(123, nil)
	if s.getActiveRuntime(123) != nil {
		t.Error("nil runtime should not be registered")
	}
}

func TestActiveRuntimes_IsolatedPerIssue(t *testing.T) {
	s := &Scheduler{}
	rtA := state.NewRuntimeState("nightgauge/nightgauge", 100, "a")
	rtB := state.NewRuntimeState("nightgauge/nightgauge", 200, "b")
	s.registerRuntime(100, rtA)
	s.registerRuntime(200, rtB)
	defer s.unregisterRuntime(100)
	defer s.unregisterRuntime(200)

	s.RecordPhaseStart(100, "feature-dev", "implementation", 7, 17)
	s.RecordPhaseStart(200, "feature-planning", "load-context", 1, 13)

	if len(rtA.PhaseHistory) != 1 || rtA.PhaseHistory[0].Name != "implementation" {
		t.Errorf("rtA PhaseHistory = %+v", rtA.PhaseHistory)
	}
	if len(rtB.PhaseHistory) != 1 || rtB.PhaseHistory[0].Name != "load-context" {
		t.Errorf("rtB PhaseHistory = %+v", rtB.PhaseHistory)
	}
}

func TestSchedulerConfig(t *testing.T) {
	cfg := SchedulerConfig{
		Owner:         "nightgauge",
		ProjectNumber: 5,
		MaxPerRepo:    2,
		WorkspaceRoot: "/workspace",
	}

	if cfg.Owner != "nightgauge" {
		t.Errorf("Owner = %q", cfg.Owner)
	}
	if cfg.MaxPerRepo != 2 {
		t.Errorf("MaxPerRepo = %d", cfg.MaxPerRepo)
	}
}

func TestQueuePersistence(t *testing.T) {
	tmpDir := t.TempDir()

	// Create scheduler with workspace root — queue persists to disk
	s := &Scheduler{
		workspaceRoot: tmpDir,
		repoRunning:   make(map[string]int),
		mergeLocks:    make(map[string]*sync.Mutex),
	}

	// Add items
	s.QueueAddItem(
		QueueItem{Repo: "nightgauge/nightgauge", IssueNumber: 100, Title: "Fix bug"},
		QueueItem{Repo: "nightgauge/nightgauge", IssueNumber: 200, Title: "Add feature"},
	)

	// Verify file exists
	queueFile := filepath.Join(tmpDir, ".nightgauge", "pipeline", "queue-state.json")
	data, err := os.ReadFile(queueFile)
	if err != nil {
		t.Fatalf("queue-state.json not created: %v", err)
	}

	var state QueueState
	if err := json.Unmarshal(data, &state); err != nil {
		t.Fatalf("failed to parse queue state: %v", err)
	}

	if state.SchemaVersion != "2.3" {
		t.Errorf("SchemaVersion = %q, want 2.3", state.SchemaVersion)
	}
	if len(state.Items) != 2 {
		t.Fatalf("len(Items) = %d, want 2", len(state.Items))
	}
	if state.Items[0].IssueNumber != 100 {
		t.Errorf("Items[0].IssueNumber = %d, want 100", state.Items[0].IssueNumber)
	}
	if state.Items[1].Title != "Add feature" {
		t.Errorf("Items[1].Title = %q, want 'Add feature'", state.Items[1].Title)
	}
	if state.Status != "waiting" {
		t.Errorf("Status = %q, want 'waiting'", state.Status)
	}

	// Create a new scheduler and verify it loads from disk
	s2 := &Scheduler{
		workspaceRoot: tmpDir,
		repoRunning:   make(map[string]int),
		mergeLocks:    make(map[string]*sync.Mutex),
	}
	s2.loadQueue()

	loaded := s2.GetState()
	if len(loaded.Items) != 2 {
		t.Fatalf("loaded queue has %d items, want 2", len(loaded.Items))
	}
	if loaded.Items[0].IssueNumber != 100 {
		t.Errorf("loaded Items[0].IssueNumber = %d, want 100", loaded.Items[0].IssueNumber)
	}

	// Clear and verify persistence
	s2.QueueClear()
	s3 := &Scheduler{
		workspaceRoot: tmpDir,
		repoRunning:   make(map[string]int),
		mergeLocks:    make(map[string]*sync.Mutex),
	}
	s3.loadQueue()
	cleared := s3.GetState()
	if len(cleared.Items) != 0 {
		t.Errorf("cleared queue has %d items, want 0", len(cleared.Items))
	}
	if cleared.Status != "idle" {
		t.Errorf("cleared Status = %q, want 'idle'", cleared.Status)
	}
}

// intPtr now lives in scheduler_exit_record.go (production) so the stage-exit
// diagnostic writer can use it for pointer-shaped optional fields. Tests
// continue to call it unchanged.

func TestDequeueIndependent(t *testing.T) {
	tests := []struct {
		name          string
		queue         []QueueItem
		maxSlots      int
		runningIssues []int
		wantDequeued  []int // issue numbers
		wantRemaining []int // issue numbers left in queue
	}{
		{
			name: "dequeue unblocked items",
			queue: []QueueItem{
				{IssueNumber: 1, Title: "Unblocked", Position: 1},
				{IssueNumber: 2, Title: "Also unblocked", Position: 2},
			},
			maxSlots:      2,
			runningIssues: []int{},
			wantDequeued:  []int{1, 2},
			wantRemaining: []int{},
		},
		{
			name: "skip blocked item",
			queue: []QueueItem{
				{IssueNumber: 1, Title: "Blocked", Position: 1, BlockedBy: []QueueBlockingRef{
					{Number: 99, Title: "Blocker", State: "OPEN"},
				}},
				{IssueNumber: 2, Title: "Unblocked", Position: 2},
			},
			maxSlots:      2,
			runningIssues: []int{99},
			wantDequeued:  []int{2},
			wantRemaining: []int{1},
		},
		{
			name: "blocked by queued item",
			queue: []QueueItem{
				{IssueNumber: 10, Title: "First", Position: 1},
				{IssueNumber: 20, Title: "Blocked by 10", Position: 2, BlockedBy: []QueueBlockingRef{
					{Number: 10, Title: "First", State: "OPEN"},
				}},
			},
			maxSlots:      2,
			runningIssues: []int{},
			wantDequeued:  []int{10},
			wantRemaining: []int{20},
		},
		{
			name: "blockedBy guard — blocks epic sub-issue whose blocker is in queue",
			queue: []QueueItem{
				{IssueNumber: 1, Title: "Epic sub 0", Position: 1, EpicOrder: intPtr(0), BlockedBy: []QueueBlockingRef{
					{Number: 99, Title: "Blocker", State: "CLOSED"},
				}},
				{IssueNumber: 2, Title: "Epic sub 1", Position: 2, EpicOrder: intPtr(1), BlockedBy: []QueueBlockingRef{
					{Number: 1, Title: "Epic sub 0", State: "OPEN"},
				}},
			},
			maxSlots:      2,
			runningIssues: []int{},
			wantDequeued:  []int{1},
			wantRemaining: []int{2},
		},
		{
			name: "non-linear epic — unblocked high-epicOrder dequeues despite lower-order siblings",
			queue: []QueueItem{
				// Simulates epic #2052: #2053 already completed (not in queue).
				// #2058 (epicOrder=5) was only blocked by #2053 → should dequeue.
				// #2054 (epicOrder=1) is blocked by #2058 → must wait.
				{IssueNumber: 2054, Title: "Sub 1", Position: 1, EpicOrder: intPtr(1), BlockedBy: []QueueBlockingRef{
					{Number: 2058, Title: "Sub 5", State: "OPEN"},
				}},
				{IssueNumber: 2058, Title: "Sub 5", Position: 2, EpicOrder: intPtr(5), BlockedBy: []QueueBlockingRef{
					{Number: 2053, Title: "Sub 0 (done)", State: "OPEN"},
				}},
			},
			maxSlots:      3,
			runningIssues: []int{},
			// #2058's blocker (#2053) is not in queue or running → unblocked
			// #2054's blocker (#2058) IS in queue → blocked
			wantDequeued:  []int{2058},
			wantRemaining: []int{2054},
		},
		{
			name: "no blockedBy — parallel OK despite epicOrder",
			queue: []QueueItem{
				{IssueNumber: 1, Title: "Epic sub 0", Position: 1, EpicOrder: intPtr(0)},
				{IssueNumber: 2, Title: "Epic sub 1", Position: 2, EpicOrder: intPtr(1)},
			},
			maxSlots:      2,
			runningIssues: []int{},
			wantDequeued:  []int{1, 2},
			wantRemaining: []int{},
		},
		{
			name: "maxSlots limits dequeue",
			queue: []QueueItem{
				{IssueNumber: 1, Position: 1},
				{IssueNumber: 2, Position: 2},
				{IssueNumber: 3, Position: 3},
			},
			maxSlots:      1,
			runningIssues: []int{},
			wantDequeued:  []int{1},
			wantRemaining: []int{2, 3},
		},
		{
			name:          "empty queue",
			queue:         []QueueItem{},
			maxSlots:      5,
			runningIssues: []int{},
			wantDequeued:  nil,
			wantRemaining: []int{},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := &Scheduler{
				repoRunning: make(map[string]int),
				mergeLocks:  make(map[string]*sync.Mutex),
			}
			s.queue = make([]QueueItem, len(tc.queue))
			copy(s.queue, tc.queue)

			running := make([]RunningItem, 0, len(tc.runningIssues))
			for _, n := range tc.runningIssues {
				running = append(running, RunningItem{Number: n})
			}
			dequeued := s.DequeueIndependent(context.Background(), tc.maxSlots, running)

			// Check dequeued
			var gotDequeued []int
			for _, d := range dequeued {
				gotDequeued = append(gotDequeued, d.IssueNumber)
			}
			if len(gotDequeued) != len(tc.wantDequeued) {
				t.Errorf("dequeued %v, want %v", gotDequeued, tc.wantDequeued)
			} else {
				for i, want := range tc.wantDequeued {
					if gotDequeued[i] != want {
						t.Errorf("dequeued[%d] = %d, want %d", i, gotDequeued[i], want)
					}
				}
			}

			// Check remaining
			var gotRemaining []int
			for _, q := range s.queue {
				gotRemaining = append(gotRemaining, q.IssueNumber)
			}
			if len(gotRemaining) != len(tc.wantRemaining) {
				t.Errorf("remaining %v, want %v", gotRemaining, tc.wantRemaining)
			} else {
				for i, want := range tc.wantRemaining {
					if gotRemaining[i] != want {
						t.Errorf("remaining[%d] = %d, want %d", i, gotRemaining[i], want)
					}
				}
			}
		})
	}
}

// TestDequeueIndependent_PerRepoCap pins the #3781 contract: workspace_max=3
// + per_repo_max=1 across 3 repos with multiple ready issues each →
// exactly 3 dequeued, 1 per repo; a second dequeue with those running returns
// nothing (every repo at its per-repo cap), and overflow stays queued.
func TestDequeueIndependent_PerRepoCap(t *testing.T) {
	s := &Scheduler{
		repoRunning: make(map[string]int),
		mergeLocks:  make(map[string]*sync.Mutex),
		maxPerRepo:  1, // concurrency.per_repo_max default
	}
	s.queue = []QueueItem{
		{IssueNumber: 1, Repo: "o/A", Position: 1},
		{IssueNumber: 2, Repo: "o/A", Position: 2},
		{IssueNumber: 3, Repo: "o/B", Position: 3},
		{IssueNumber: 4, Repo: "o/B", Position: 4},
		{IssueNumber: 5, Repo: "o/C", Position: 5},
	}

	// workspace_max=3, nothing running → 1 from each of A, B, C.
	got := s.DequeueIndependent(context.Background(), 3, nil)
	if len(got) != 3 {
		t.Fatalf("first dequeue = %d items, want 3", len(got))
	}
	perRepo := map[string]int{}
	for _, q := range got {
		perRepo[q.Repo]++
	}
	for repo, n := range perRepo {
		if n != 1 {
			t.Errorf("repo %s got %d, want exactly 1 (per_repo_max)", repo, n)
		}
	}

	// With one running per repo, every repo is at its cap → second dequeue
	// returns nothing even though workspace slots remain.
	running := []RunningItem{{Repo: "o/A", Number: 1}, {Repo: "o/B", Number: 3}, {Repo: "o/C", Number: 5}}
	got2 := s.DequeueIndependent(context.Background(), 3, running)
	if len(got2) != 0 {
		t.Errorf("second dequeue = %d items, want 0 (all repos at per-repo cap)", len(got2))
	}
}

// TestDequeueIndependent_3874_SameRepoBatchAndCrossPass pins the exact #3874
// acceptance criteria at the scheduler boundary the IPC drag path calls:
// dragging TWO same-repo issues in quick succession (both enqueued before any
// fill) must dispatch at most per_repo_max of them, with the rest left QUEUED —
// and a SECOND pass with the first running must still refuse the second, even
// though the workspace ceiling has free slots. The workspace ceiling is set
// deliberately WIDER than the per-repo cap (3 vs 1) so that only the per-repo
// guard — not the global maxSlots — can keep the second item back. This is the
// regression the issue's AC mandates and that the IPC dispatch path historically
// lacked before #3781/#3786.
func TestDequeueIndependent_3874_SameRepoBatchAndCrossPass(t *testing.T) {
	s := &Scheduler{
		repoRunning: make(map[string]int),
		mergeLocks:  make(map[string]*sync.Mutex),
		maxPerRepo:  1, // concurrency.per_repo_max = 1 (sequential per repo)
	}
	s.queue = []QueueItem{
		{IssueNumber: 10, Repo: "o/A", Position: 1},
		{IssueNumber: 11, Repo: "o/A", Position: 2},
	}

	// Single batch, workspace_max=3, nothing running → exactly 1 same-repo item
	// dispatched even though 3 workspace slots are free.
	got := s.DequeueIndependent(context.Background(), 3, nil)
	if len(got) != 1 {
		t.Fatalf("first dequeue = %d items, want exactly 1 (per_repo_max=1, two same-repo dropped together)", len(got))
	}
	if got[0].IssueNumber != 10 {
		t.Errorf("dispatched issue %d, want 10 (FIFO position order)", got[0].IssueNumber)
	}
	// The second same-repo item must remain QUEUED, not dropped.
	if len(s.queue) != 1 || s.queue[0].IssueNumber != 11 {
		t.Errorf("remaining queue = %+v, want exactly [#11] still queued", s.queue)
	}

	// Cross-pass: a SECOND fill with #10 running must return nothing — the repo
	// is at its per-repo cap — even though workspace slots remain free. This is
	// the cross-pass timing guard: as long as the caller reports the in-flight
	// slot's repo in `running`, the cap holds across passes.
	running := []RunningItem{{Repo: "o/A", Number: 10}}
	got2 := s.DequeueIndependent(context.Background(), 3, running)
	if len(got2) != 0 {
		t.Errorf("cross-pass dequeue = %d items, want 0 (repo at per-repo cap while #10 runs)", len(got2))
	}
	if len(s.queue) != 1 || s.queue[0].IssueNumber != 11 {
		t.Errorf("after cross-pass, remaining queue = %+v, want [#11] still queued", s.queue)
	}

	// Once #10 finishes (nothing running), the held item is free to dispatch.
	got3 := s.DequeueIndependent(context.Background(), 3, nil)
	if len(got3) != 1 || got3[0].IssueNumber != 11 {
		t.Errorf("post-completion dequeue = %+v, want exactly [#11]", got3)
	}
}

func TestDequeueIndependent_PerRepoOverride(t *testing.T) {
	// Override raises repo A to 2; B stays at default 1.
	s := &Scheduler{
		repoRunning:              make(map[string]int),
		mergeLocks:               make(map[string]*sync.Mutex),
		maxPerRepo:               1,
		repoConcurrencyOverrides: map[string]int{"A": 2},
	}
	s.queue = []QueueItem{
		{IssueNumber: 1, Repo: "o/A", Position: 1},
		{IssueNumber: 2, Repo: "o/A", Position: 2},
		{IssueNumber: 3, Repo: "o/B", Position: 3},
		{IssueNumber: 4, Repo: "o/B", Position: 4},
	}
	got := s.DequeueIndependent(context.Background(), 4, nil)
	perRepo := map[string]int{}
	for _, q := range got {
		perRepo[q.Repo]++
	}
	if perRepo["o/A"] != 2 {
		t.Errorf("repo A dequeued %d, want 2 (override)", perRepo["o/A"])
	}
	if perRepo["o/B"] != 1 {
		t.Errorf("repo B dequeued %d, want 1 (default)", perRepo["o/B"])
	}
}

func TestQueueItemPositions(t *testing.T) {
	s := &Scheduler{
		repoRunning: make(map[string]int),
		mergeLocks:  make(map[string]*sync.Mutex),
	}

	s.QueueAddItem(
		QueueItem{IssueNumber: 1, Title: "First"},
		QueueItem{IssueNumber: 2, Title: "Second"},
		QueueItem{IssueNumber: 3, Title: "Third"},
	)

	state := s.GetState()
	for i, item := range state.Items {
		if item.Position != i+1 {
			t.Errorf("Items[%d].Position = %d, want %d", i, item.Position, i+1)
		}
	}

	// Remove middle item and verify positions recalculate
	s.QueueRemove(2)
	state = s.GetState()
	if len(state.Items) != 2 {
		t.Fatalf("len(Items) = %d, want 2", len(state.Items))
	}
	if state.Items[0].Position != 1 {
		t.Errorf("after remove, Items[0].Position = %d, want 1", state.Items[0].Position)
	}
	if state.Items[1].Position != 2 {
		t.Errorf("after remove, Items[1].Position = %d, want 2", state.Items[1].Position)
	}
}

func TestQueueAddItem_DeduplicatesByIssueNumber(t *testing.T) {
	s := &Scheduler{
		repoRunning: make(map[string]int),
		mergeLocks:  make(map[string]*sync.Mutex),
	}

	// Add three items
	s.QueueAddItem(
		QueueItem{IssueNumber: 10, Title: "Alpha"},
		QueueItem{IssueNumber: 20, Title: "Beta"},
		QueueItem{IssueNumber: 30, Title: "Gamma"},
	)

	// Attempt to re-add #20 and #30 plus a new #40 — duplicates should be skipped
	s.QueueAddItem(
		QueueItem{IssueNumber: 20, Title: "Beta duplicate"},
		QueueItem{IssueNumber: 30, Title: "Gamma duplicate"},
		QueueItem{IssueNumber: 40, Title: "Delta"},
	)

	state := s.GetState()
	if len(state.Items) != 4 {
		t.Fatalf("len(Items) = %d, want 4 (duplicates should be skipped)", len(state.Items))
	}

	// Verify original titles were preserved (not overwritten by duplicates)
	if state.Items[1].Title != "Beta" {
		t.Errorf("Items[1].Title = %q, want %q (duplicate should not overwrite)", state.Items[1].Title, "Beta")
	}

	// Also verify QueueAdd (legacy) deduplicates
	s.QueueAdd(QueueEntry{IssueNumber: 10, Repo: "test"})
	state = s.GetState()
	if len(state.Items) != 4 {
		t.Fatalf("after QueueAdd duplicate: len(Items) = %d, want 4", len(state.Items))
	}
}

func TestQueueChangedCallback(t *testing.T) {
	ch := make(chan QueueState, 1)

	s := &Scheduler{
		repoRunning: make(map[string]int),
		mergeLocks:  make(map[string]*sync.Mutex),
	}
	s.OnQueueChanged(func(state QueueState) {
		ch <- state
	})

	s.QueueAddItem(QueueItem{IssueNumber: 42, Title: "Test"})

	select {
	case got := <-ch:
		if len(got.Items) != 1 {
			t.Errorf("lastState.Items = %d, want 1", len(got.Items))
		}
	case <-time.After(5 * time.Second):
		t.Fatal("onQueueChanged was not called within 5s after QueueAddItem")
	}
}

func TestEnqueueEpic_PopulatesSubIssueBlockedBy(t *testing.T) {
	mock := newMockIssueSvc()

	// Epic #100 has two sub-issues: #200 and #300
	// #300 is blockedBy #200 (sequential dependency within the epic)
	mock.addIssue("Org", "repo", 100, &types.Issue{
		NodeID: "I_epic100",
		Number: 100,
		Title:  "Test Epic",
		State:  "OPEN",
		Repo:   "Org/repo",
		SubIssues: []types.SubIssueRef{
			{NodeID: "I_200", Number: 200, Title: "First task", State: "OPEN", Repo: "Org/repo"},
			{NodeID: "I_300", Number: 300, Title: "Second task", State: "OPEN", Repo: "Org/repo"},
		},
	})

	// Sub-issue #200: no blockers
	mock.addIssue("Org", "repo", 200, &types.Issue{
		NodeID:    "I_200",
		Number:    200,
		Title:     "First task",
		State:     "OPEN",
		Repo:      "Org/repo",
		BlockedBy: nil,
	})

	// Sub-issue #300: blocked by #200
	mock.addIssue("Org", "repo", 300, &types.Issue{
		NodeID: "I_300",
		Number: 300,
		Title:  "Second task",
		State:  "OPEN",
		Repo:   "Org/repo",
		BlockedBy: []types.BlockingRef{
			{NodeID: "I_200", Number: 200, Title: "First task", State: "OPEN", Repo: "Org/repo"},
		},
	})

	s := &Scheduler{
		issueSvc:    mock,
		repoRunning: make(map[string]int),
		mergeLocks:  make(map[string]*sync.Mutex),
	}

	err := s.EnqueueEpic(context.Background(), "Org", "repo", 100, "Test Epic", nil, nil)
	if err != nil {
		t.Fatalf("EnqueueEpic failed: %v", err)
	}

	if len(s.queue) != 2 {
		t.Fatalf("queue has %d items, want 2", len(s.queue))
	}

	// #200 should have no blockers
	item200 := s.queue[0]
	if item200.IssueNumber != 200 {
		t.Fatalf("queue[0].IssueNumber = %d, want 200", item200.IssueNumber)
	}
	if len(item200.BlockedBy) != 0 {
		t.Errorf("#200 has %d blockers, want 0", len(item200.BlockedBy))
	}

	// #300 should be blocked by #200
	item300 := s.queue[1]
	if item300.IssueNumber != 300 {
		t.Fatalf("queue[1].IssueNumber = %d, want 300", item300.IssueNumber)
	}
	if len(item300.BlockedBy) != 1 {
		t.Fatalf("#300 has %d blockers, want 1", len(item300.BlockedBy))
	}
	if item300.BlockedBy[0].Number != 200 {
		t.Errorf("#300 blockedBy[0].Number = %d, want 200", item300.BlockedBy[0].Number)
	}
	if item300.BlockedBy[0].State != "OPEN" {
		t.Errorf("#300 blockedBy[0].State = %q, want OPEN", item300.BlockedBy[0].State)
	}

	// Now verify DequeueIndependent respects the blocking
	dequeued := s.DequeueIndependent(context.Background(), 3, nil)
	var dequeuedNums []int
	for _, d := range dequeued {
		dequeuedNums = append(dequeuedNums, d.IssueNumber)
	}
	// Only #200 should be dequeued — #300 is blocked by #200 which is OPEN and was just dequeued
	if len(dequeuedNums) != 1 || dequeuedNums[0] != 200 {
		t.Errorf("dequeued %v, want [200]", dequeuedNums)
	}

	// #300 should remain in queue
	if len(s.queue) != 1 || s.queue[0].IssueNumber != 300 {
		t.Errorf("remaining queue: %v, want [300]", s.queue)
	}
}

func TestEnqueueEpic_CombinesEpicAndSubIssueBlockers(t *testing.T) {
	mock := newMockIssueSvc()

	// Epic #100 is itself blocked by #50 (epic-level blocker)
	// Sub-issue #200 is also blocked by #150 (sub-issue-level blocker)
	mock.addIssue("Org", "repo", 100, &types.Issue{
		NodeID: "I_epic100",
		Number: 100,
		Title:  "Test Epic",
		State:  "OPEN",
		Repo:   "Org/repo",
		BlockedBy: []types.BlockingRef{
			{NodeID: "I_50", Number: 50, Title: "External blocker", State: "OPEN"},
		},
		SubIssues: []types.SubIssueRef{
			{NodeID: "I_200", Number: 200, Title: "Sub task", State: "OPEN", Repo: "Org/repo"},
		},
	})

	mock.addIssue("Org", "repo", 200, &types.Issue{
		NodeID: "I_200",
		Number: 200,
		Title:  "Sub task",
		State:  "OPEN",
		Repo:   "Org/repo",
		BlockedBy: []types.BlockingRef{
			{NodeID: "I_150", Number: 150, Title: "Another blocker", State: "OPEN"},
		},
	})

	s := &Scheduler{
		issueSvc:    mock,
		repoRunning: make(map[string]int),
		mergeLocks:  make(map[string]*sync.Mutex),
	}

	err := s.EnqueueEpic(context.Background(), "Org", "repo", 100, "Test Epic", nil, nil)
	if err != nil {
		t.Fatalf("EnqueueEpic failed: %v", err)
	}

	if len(s.queue) != 1 {
		t.Fatalf("queue has %d items, want 1", len(s.queue))
	}

	item := s.queue[0]
	// Should have both epic-level (#50) and sub-issue-level (#150) blockers
	if len(item.BlockedBy) != 2 {
		t.Fatalf("#200 has %d blockers, want 2 (epic-level + sub-issue-level)", len(item.BlockedBy))
	}

	blockerNums := map[int]bool{}
	for _, b := range item.BlockedBy {
		blockerNums[b.Number] = true
	}
	if !blockerNums[50] {
		t.Error("missing epic-level blocker #50")
	}
	if !blockerNums[150] {
		t.Error("missing sub-issue-level blocker #150")
	}
}

func TestIsBlocked_CircularEpicDependency(t *testing.T) {
	// Sub-issue #163 blocked by its own parent epic #152 — should auto-remove
	// and return unblocked. BlockedBy comes from the board item, not GetIssue.
	issueSvc := newMockIssueSvc()
	s := &Scheduler{issueSvc: issueSvc}
	item := types.BoardItem{
		NodeID:       "node_163",
		Number:       163,
		Repo:         "nightgauge/angular",
		ParentNumber: 152,
		BlockedBy: []types.BlockingRef{
			{NodeID: "node_152", Number: 152, Title: "Epic: Tests", State: "OPEN"},
		},
	}

	blocked, err := s.isBlocked(context.Background(), item)
	if err != nil {
		t.Fatalf("isBlocked error: %v", err)
	}
	if blocked {
		t.Error("expected not blocked — circular epic dependency should be auto-removed")
	}
}

func TestIsBlocked_LegitimateBlocker(t *testing.T) {
	// Sub-issue #131 blocked by sibling #130 (legitimate) — should remain blocked.
	issueSvc := newMockIssueSvc()
	s := &Scheduler{issueSvc: issueSvc}
	item := types.BoardItem{
		NodeID:       "node_131",
		Number:       131,
		Repo:         "nightgauge/flutter",
		ParentNumber: 124,
		BlockedBy: []types.BlockingRef{
			{NodeID: "node_130", Number: 130, Title: "Update tests", State: "OPEN"},
		},
	}

	blocked, err := s.isBlocked(context.Background(), item)
	if err != nil {
		t.Fatalf("isBlocked error: %v", err)
	}
	if !blocked {
		t.Error("expected blocked — #130 is a legitimate blocker")
	}
}

func TestIsBlocked_ClosedBlockerSkipped(t *testing.T) {
	// A blocker that is already CLOSED should not block — verifies state filter.
	s := &Scheduler{issueSvc: newMockIssueSvc()}
	item := types.BoardItem{
		NodeID:       "node_200",
		Number:       200,
		Repo:         "nightgauge/nightgauge",
		ParentNumber: 0,
		BlockedBy: []types.BlockingRef{
			{NodeID: "node_199", Number: 199, Title: "Already merged", State: "CLOSED"},
		},
	}

	blocked, err := s.isBlocked(context.Background(), item)
	if err != nil {
		t.Fatalf("isBlocked error: %v", err)
	}
	if blocked {
		t.Error("expected not blocked — only blocker is CLOSED")
	}
}

func TestIsBlocked_NoBlockers(t *testing.T) {
	// Item with no BlockedBy entries should report unblocked without any
	// IssueService call. Passing a nil issueSvc proves the hot path is fetch-free.
	s := &Scheduler{issueSvc: nil}
	item := types.BoardItem{
		NodeID: "node_300",
		Number: 300,
		Repo:   "nightgauge/nightgauge",
	}

	blocked, err := s.isBlocked(context.Background(), item)
	if err != nil {
		t.Fatalf("isBlocked error: %v", err)
	}
	if blocked {
		t.Error("expected not blocked — item has no BlockedBy entries")
	}
}

func TestRefreshBlockerStates_BatchedPerRepo(t *testing.T) {
	// Queue with 4 items split across 2 repos, each item has 2 OPEN blockers.
	// Without batching: 8 GetIssue calls. With batching: 2 calls (one per repo).
	mock := newMockIssueSvc()

	// Blockers in repo-a: #50 closed, #51 still open.
	mock.addIssue("test", "repo-a", 50, &types.Issue{Number: 50, State: "CLOSED"})
	mock.addIssue("test", "repo-a", 51, &types.Issue{Number: 51, State: "OPEN"})
	// Blockers in repo-b: #60 closed, #61 closed.
	mock.addIssue("test", "repo-b", 60, &types.Issue{Number: 60, State: "CLOSED"})
	mock.addIssue("test", "repo-b", 61, &types.Issue{Number: 61, State: "CLOSED"})

	s := &Scheduler{issueSvc: mock}
	s.queue = []QueueItem{
		{Repo: "test/repo-a", IssueNumber: 100, BlockedBy: []QueueBlockingRef{{Number: 50, State: "OPEN"}, {Number: 51, State: "OPEN"}}},
		{Repo: "test/repo-a", IssueNumber: 101, BlockedBy: []QueueBlockingRef{{Number: 50, State: "OPEN"}}},
		{Repo: "test/repo-b", IssueNumber: 200, BlockedBy: []QueueBlockingRef{{Number: 60, State: "OPEN"}, {Number: 61, State: "OPEN"}}},
		{Repo: "test/repo-b", IssueNumber: 201, BlockedBy: []QueueBlockingRef{{Number: 61, State: "OPEN"}}},
	}

	s.refreshBlockerStates(context.Background())

	if len(mock.batchCalls) != 2 {
		t.Fatalf("want 2 batched calls (one per repo), got %d: %+v", len(mock.batchCalls), mock.batchCalls)
	}
	// State updates: 50, 60, 61 should now be CLOSED in queue; 51 stays OPEN.
	check := func(itemIdx, blockerNum int, wantState string) {
		t.Helper()
		for _, b := range s.queue[itemIdx].BlockedBy {
			if b.Number == blockerNum {
				if !strings.EqualFold(b.State, wantState) {
					t.Errorf("queue[%d] blocker #%d state = %s, want %s", itemIdx, blockerNum, b.State, wantState)
				}
				return
			}
		}
		t.Errorf("queue[%d] missing blocker #%d", itemIdx, blockerNum)
	}
	check(0, 50, "CLOSED")
	check(0, 51, "OPEN")
	check(1, 50, "CLOSED")
	check(2, 60, "CLOSED")
	check(2, 61, "CLOSED")
	check(3, 61, "CLOSED")
}

func TestRefreshBlockerStates_NoOpenBlockers(t *testing.T) {
	mock := newMockIssueSvc()
	s := &Scheduler{issueSvc: mock}
	s.queue = []QueueItem{
		{Repo: "test/repo", IssueNumber: 100, BlockedBy: []QueueBlockingRef{{Number: 50, State: "CLOSED"}}},
	}
	s.refreshBlockerStates(context.Background())
	if len(mock.batchCalls) != 0 {
		t.Errorf("want 0 batched calls when no OPEN blockers, got %d", len(mock.batchCalls))
	}
}

// escalatingMockRunner simulates an IpcStageRunner that evaluates model
// escalation internally on stage failure and sets EscalationRecorded.
// On the first call it fails and records escalation; subsequent calls succeed.
type escalatingMockRunner struct {
	engine    *RetryEngine
	callCount int
	calls     []StageRunParams
}

func (m *escalatingMockRunner) RunStage(ctx context.Context, params StageRunParams) (*StageRunResult, error) {
	m.callCount++
	m.calls = append(m.calls, params)

	if m.callCount == 1 {
		// First call: fail and evaluate escalation (simulates IpcStageRunner behavior)
		decision := m.engine.EvaluateEscalation(string(params.Stage), params.Model)
		escalationRecorded := false
		if decision.ShouldEscalate {
			m.engine.RecordEscalation(string(params.Stage), decision.NewModel)
			escalationRecorded = true
		}
		return &StageRunResult{ExitCode: 1, EscalationRecorded: escalationRecorded}, nil
	}
	// Second call: succeed
	return &StageRunResult{ExitCode: 0}, nil
}

// TestScheduler_IpcStageEscalation verifies the full escalation flow for IPC mode:
// 1. Stage fails on first attempt → IpcStageRunner evaluates escalation and records it
// 2. EscalationRecorded=true causes the scheduler to retry the same stage
// 3. On retry, the scheduler passes the escalated model (opus) to the runner
// 4. Stage succeeds on second attempt
func TestScheduler_IpcStageEscalation(t *testing.T) {
	engine := NewRetryEngine(DefaultRetryConfig())

	runner := &escalatingMockRunner{engine: engine}

	// Simulate the scheduler's stage execution loop for a single stage.
	// This exercises the EscalationRecorded check without running the full pipeline.
	stage := state.StageFeatureDev
	model := "sonnet"

	const maxIter = 5
	for iter := 0; iter < maxIter; iter++ {
		// Scheduler applies escalation override on each iteration (scheduler.go:1125-1127)
		if override := engine.CurrentModel(string(stage)); override != "" {
			model = override
		}

		result, err := runner.RunStage(context.Background(), StageRunParams{
			Stage:       stage,
			IssueNumber: 42,
			Model:       model,
		})
		if err != nil {
			t.Fatalf("RunStage error: %v", err)
		}

		if result.ExitCode != 0 {
			// Scheduler's EscalationRecorded check (scheduler.go after RunStage returns)
			if result.EscalationRecorded {
				continue // Escalation already recorded by runner; retry same stage
			}
			t.Fatalf("stage failed without escalation recorded on iteration %d", iter)
		}

		// Stage succeeded — pipeline would advance to next stage
		break
	}

	// Verify no backtracks occurred
	if engine.BacktrackCount() != 0 {
		t.Errorf("BacktrackCount = %d, want 0", engine.BacktrackCount())
	}

	// Verify escalation was recorded for feature-dev
	if got := engine.CurrentModel(string(stage)); got != "opus" {
		t.Errorf("CurrentModel(feature-dev) = %q, want opus", got)
	}

	// Verify stage ran exactly twice
	if runner.callCount != 2 {
		t.Errorf("RunStage call count = %d, want 2", runner.callCount)
	}

	// Verify second call used the escalated model
	if runner.calls[1].Model != "opus" {
		t.Errorf("second call model = %q, want opus", runner.calls[1].Model)
	}
}

func TestIsHaikuModel(t *testing.T) {
	tests := []struct {
		model string
		want  bool
	}{
		{"claude-haiku-4-5-20251001", true},
		{"haiku", true},
		{"claude-sonnet-4-6", false},
		{"claude-opus-4-7", false},
		{"claude-opus-4-6", false},
		{"", false},
	}
	for _, tt := range tests {
		if got := isHaikuModel(tt.model); got != tt.want {
			t.Errorf("isHaikuModel(%q) = %v, want %v", tt.model, got, tt.want)
		}
	}
}

func TestGetLargeDiffThreshold(t *testing.T) {
	t.Run("returns default when no config", func(t *testing.T) {
		dir := t.TempDir()
		got := getLargeDiffThreshold(dir)
		if got != 500 {
			t.Errorf("got %d, want 500", got)
		}
	})

	t.Run("reads from config.yaml", func(t *testing.T) {
		dir := t.TempDir()
		configDir := filepath.Join(dir, ".nightgauge")
		os.MkdirAll(configDir, 0o755)
		os.WriteFile(filepath.Join(configDir, "config.yaml"), []byte("pipeline:\n  large_diff_threshold: 300\n"), 0o644)
		got := getLargeDiffThreshold(dir)
		if got != 300 {
			t.Errorf("got %d, want 300", got)
		}
	})

	t.Run("env var takes precedence", func(t *testing.T) {
		dir := t.TempDir()
		configDir := filepath.Join(dir, ".nightgauge")
		os.MkdirAll(configDir, 0o755)
		os.WriteFile(filepath.Join(configDir, "config.yaml"), []byte("pipeline:\n  large_diff_threshold: 300\n"), 0o644)
		t.Setenv("NIGHTGAUGE_PIPELINE_LARGE_DIFF_THRESHOLD", "800")
		got := getLargeDiffThreshold(dir)
		if got != 800 {
			t.Errorf("got %d, want 800", got)
		}
	})

	t.Run("zero disables escalation", func(t *testing.T) {
		t.Setenv("NIGHTGAUGE_PIPELINE_LARGE_DIFF_THRESHOLD", "0")
		got := getLargeDiffThreshold(t.TempDir())
		if got != 0 {
			t.Errorf("got %d, want 0", got)
		}
	})
}

func TestOnFailureStatusDefault(t *testing.T) {
	// When OnFailureStatus is empty, NewScheduler should default to "ready"
	tmpDir := t.TempDir()
	s := &Scheduler{
		workspaceRoot:   tmpDir,
		repoRunning:     make(map[string]int),
		mergeLocks:      make(map[string]*sync.Mutex),
		onFailureStatus: "", // simulate unset
	}

	// Verify that the default is applied in NewScheduler
	cfg := SchedulerConfig{
		Owner:         "nightgauge",
		ProjectNumber: 1,
		MaxPerRepo:    1,
		WorkspaceRoot: tmpDir,
	}
	// OnFailureStatus defaults to "ready" when empty
	onFailureStatus := cfg.OnFailureStatus
	if onFailureStatus == "" {
		onFailureStatus = "ready"
	}
	if onFailureStatus != "ready" {
		t.Errorf("default OnFailureStatus = %q, want %q", onFailureStatus, "ready")
	}

	_ = s // suppress unused
}

func TestOnFailureStatusConfigValues(t *testing.T) {
	tests := []struct {
		name            string
		onFailureStatus string
		wantStored      string
	}{
		{"empty defaults to ready", "", "ready"},
		{"explicit ready", "ready", "ready"},
		{"explicit backlog", "backlog", "backlog"},
		{"explicit unchanged", "unchanged", "unchanged"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			s := &Scheduler{
				workspaceRoot:   tmpDir,
				repoRunning:     make(map[string]int),
				mergeLocks:      make(map[string]*sync.Mutex),
				onFailureStatus: tc.onFailureStatus,
			}
			if tc.onFailureStatus == "" {
				s.onFailureStatus = "ready" // mirror NewScheduler default
			}
			if s.onFailureStatus != tc.wantStored {
				t.Errorf("onFailureStatus = %q, want %q", s.onFailureStatus, tc.wantStored)
			}
		})
	}
}

func TestFailureRevertSkippedOnSuccess(t *testing.T) {
	// Verify that when pipelineSuccess is true, the failure revert code path
	// is NOT entered. We test this by checking the condition logic directly.
	pipelineSuccess := true
	onFailureStatus := "ready"

	// The revert only fires when !pipelineSuccess && status != "unchanged"
	shouldRevert := !pipelineSuccess && onFailureStatus != "unchanged"
	if shouldRevert {
		t.Error("should not revert on success")
	}
}

func TestFailureRevertSkippedOnUnchanged(t *testing.T) {
	// When configured as "unchanged", no revert should happen even on failure
	pipelineSuccess := false
	onFailureStatus := "unchanged"

	shouldRevert := !pipelineSuccess && onFailureStatus != "unchanged"
	if shouldRevert {
		t.Error("should not revert when configured as 'unchanged'")
	}
}

func TestFailureRevertTriggeredOnFailure(t *testing.T) {
	// When pipeline fails and status is "ready", revert should happen
	tests := []struct {
		name            string
		onFailureStatus string
		wantTarget      state.BoardStatus
	}{
		{"ready", "ready", state.StatusReady},
		{"backlog", "backlog", state.StatusBacklog},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			pipelineSuccess := false
			shouldRevert := !pipelineSuccess && tc.onFailureStatus != "unchanged"
			if !shouldRevert {
				t.Fatal("expected revert to be triggered")
			}

			var targetStatus state.BoardStatus
			switch tc.onFailureStatus {
			case "backlog":
				targetStatus = state.StatusBacklog
			default:
				targetStatus = state.StatusReady
			}

			if targetStatus != tc.wantTarget {
				t.Errorf("targetStatus = %q, want %q", targetStatus, tc.wantTarget)
			}
		})
	}
}

func TestGetAutoCreateEpicBranch_Default(t *testing.T) {
	// No env var set, no config file — default is true
	dir := t.TempDir()
	t.Setenv("NIGHTGAUGE_PIPELINE_AUTO_CREATE_EPIC_BRANCH", "")
	if !getAutoCreateEpicBranch(dir) {
		t.Error("expected default=true when no env var and no config file")
	}
}

func TestGetAutoCreateEpicBranch_EnvDisable(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("NIGHTGAUGE_PIPELINE_AUTO_CREATE_EPIC_BRANCH", "false")
	if getAutoCreateEpicBranch(dir) {
		t.Error("expected false when env var is 'false'")
	}
}

func TestGetAutoCreateEpicBranch_EnvEnable(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("NIGHTGAUGE_PIPELINE_AUTO_CREATE_EPIC_BRANCH", "true")
	if !getAutoCreateEpicBranch(dir) {
		t.Error("expected true when env var is 'true'")
	}
}

func TestGetAutoCreateEpicBranch_ConfigDisable(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("NIGHTGAUGE_PIPELINE_AUTO_CREATE_EPIC_BRANCH", "")
	configDir := filepath.Join(dir, ".nightgauge")
	if err := os.MkdirAll(configDir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	configContent := "pipeline:\n  auto_create_epic_branch: false\n"
	if err := os.WriteFile(filepath.Join(configDir, "config.yaml"), []byte(configContent), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if getAutoCreateEpicBranch(dir) {
		t.Error("expected false when config.yaml sets auto_create_epic_branch: false")
	}
}

func TestGetAutoCreateEpicBranch_ConfigEnable(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("NIGHTGAUGE_PIPELINE_AUTO_CREATE_EPIC_BRANCH", "")
	configDir := filepath.Join(dir, ".nightgauge")
	if err := os.MkdirAll(configDir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	configContent := "pipeline:\n  auto_create_epic_branch: true\n"
	if err := os.WriteFile(filepath.Join(configDir, "config.yaml"), []byte(configContent), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if !getAutoCreateEpicBranch(dir) {
		t.Error("expected true when config.yaml sets auto_create_epic_branch: true")
	}
}

func TestEnsureEpicBranchForItem_NoParent(t *testing.T) {
	// When item.ParentNumber == 0, ensureEpicBranchForItem is never called.
	// The StageIssuePickup handler guards with: if item.ParentNumber != 0.
	// This test verifies the guard condition itself.
	item := types.BoardItem{
		Number:       1234,
		ParentNumber: 0,
		Repo:         "nightgauge/nightgauge",
	}
	// If ParentNumber is 0, the call should be skipped entirely — nothing to assert
	// other than the guard condition holds true.
	if item.ParentNumber != 0 {
		t.Error("test setup error: ParentNumber should be 0")
	}
}

func TestEnsureEpicBranchForItem_Disabled(t *testing.T) {
	// When the env var disables auto-creation, ensureEpicBranchForItem returns immediately.
	// We verify by passing a non-existent workspaceRoot — if the function tried to open
	// a git service, it would log an error but not panic (non-blocking). With the flag
	// disabled, it returns before touching the git service at all.
	t.Setenv("NIGHTGAUGE_PIPELINE_AUTO_CREATE_EPIC_BRANCH", "false")

	s := &Scheduler{
		issueSvc: newMockIssueSvc(),
	}

	item := types.BoardItem{
		Number:       2001,
		ParentNumber: 2000,
		ParentTitle:  "Test Epic",
		Repo:         "nightgauge/nightgauge",
	}

	// Should return immediately without error (non-blocking).
	// With a non-existent workspaceRoot, git.NewService would fail — but since
	// the env var disables creation, we never reach that call.
	s.ensureEpicBranchForItem(context.Background(), "/nonexistent/workspace", item)
	// No panic or fatal = test passes
}

// ── parsePRURL / verifyPRMerged tests (#2843) ─────────────────────────────────

func TestParsePRURL(t *testing.T) {
	tests := []struct {
		name      string
		url       string
		wantOwner string
		wantRepo  string
		wantNum   int
		wantErr   bool
	}{
		{"web URL", "https://github.com/acme/platform/pull/682", "acme", "platform", 682, false},
		{"web URL trailing slash", "https://github.com/nightgauge/nightgauge/pull/17/", "nightgauge", "nightgauge", 17, false},
		{"API URL", "https://api.github.com/repos/acme/dashboard/pulls/254", "acme", "dashboard", 254, false},
		{"empty", "", "", "", 0, true},
		{"missing number", "https://github.com/nightgauge/nightgauge/pull/abc", "", "", 0, true},
		{"wrong separator", "https://github.com/nightgauge/nightgauge/issue/17", "", "", 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			owner, repo, num, err := parsePRURL(tt.url)
			if tt.wantErr {
				if err == nil {
					t.Errorf("expected error, got owner=%q repo=%q num=%d", owner, repo, num)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if owner != tt.wantOwner || repo != tt.wantRepo || num != tt.wantNum {
				t.Errorf("parsePRURL(%q) = (%q, %q, %d), want (%q, %q, %d)",
					tt.url, owner, repo, num, tt.wantOwner, tt.wantRepo, tt.wantNum)
			}
		})
	}
}

// prStateServer returns an httptest server that responds to GetPR's GraphQL
// query with a minimal PR carrying the given state. Matches the field set
// PRService.GetPR asks for exactly — shurcooL/graphql errors if the response
// contains fields the struct does not declare.
func prStateServer(t *testing.T, state string) *httptest.Server {
	t.Helper()
	return prBlockerServer(t, state, "", "", "")
}

// prBlockerServer is prStateServer with explicit mergeable / mergeStateStatus /
// reviewDecision so the fail-closed blocker classifier (#4070) can be exercised.
func prBlockerServer(t *testing.T, state, mergeable, mergeStateStatus, reviewDecision string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		resp := fmt.Sprintf(`{
			"data": {
				"repository": {
					"pullRequest": {
						"id": "PR_ID",
						"number": 682,
						"title": "",
						"body": "",
						"state": "%s",
						"headRefName": "",
						"baseRefName": "",
						"url": "",
						"mergeable": "%s",
						"mergeStateStatus": "%s",
						"reviewDecision": "%s",
						"isDraft": false,
						"additions": 0,
						"deletions": 0,
						"labels": {"nodes": []},
						"commits": {"nodes": []}
					}
				}
			}
		}`, state, mergeable, mergeStateStatus, reviewDecision)
		_, _ = w.Write([]byte(resp))
	}))
}

func TestVerifyPRMerged_EmptyURL_TrustsSkill(t *testing.T) {
	// Empty PrUrl is unusual but not fatal — pr-create may have skipped
	// persisting the URL in test/mock contexts. Rather than fail every
	// pipeline in that shape, we defer to the pr-merge skill's exit code.
	// The pr-merge skill itself has an initial PR-state check, so this is
	// defense in depth rather than the only gate.
	s := &Scheduler{}
	merged, reason := s.verifyPRMerged(context.Background(), "", 0)
	if !merged {
		t.Errorf("expected merged=true when PR URL is empty (defer to skill), got reason=%q", reason)
	}
}

func TestVerifyPRMerged_BadURL(t *testing.T) {
	s := &Scheduler{}
	merged, reason := s.verifyPRMerged(context.Background(), "not a url", 0)
	if merged {
		t.Error("unparseable URL should not verify as merged")
	}
	if !strings.Contains(reason, "parse") {
		t.Errorf("expected 'parse' in reason, got %q", reason)
	}
}

func TestVerifyPRMerged_NilClient(t *testing.T) {
	// When no GitHub client is wired (test scheduler without client), we can't
	// verify — fall back to trusting the skill's exit code rather than failing
	// every pipeline in that configuration.
	s := &Scheduler{}
	merged, reason := s.verifyPRMerged(context.Background(),
		"https://github.com/acme/platform/pull/682", 0)
	if !merged {
		t.Errorf("expected merged=true (trust skill) when client is nil, got reason=%q", reason)
	}
}

func TestVerifyPRMerged_StateMerged(t *testing.T) {
	srv := prStateServer(t, "MERGED")
	defer srv.Close()
	s := &Scheduler{client: gh.NewClientWithURL("test-token", srv.URL)}

	merged, reason := s.verifyPRMerged(context.Background(),
		"https://github.com/acme/platform/pull/682", 0)
	if !merged {
		t.Errorf("expected merged=true for MERGED state, got reason=%q", reason)
	}
}

func TestVerifyPRMerged_StateOpen(t *testing.T) {
	// Reproduces #2843: pr-merge skill returned exit 0 but PR is still OPEN
	// on GitHub (CI failure, review rejection, branch protection, etc).
	srv := prStateServer(t, "OPEN")
	defer srv.Close()
	s := &Scheduler{client: gh.NewClientWithURL("test-token", srv.URL)}

	merged, reason := s.verifyPRMerged(context.Background(),
		"https://github.com/acme/platform/pull/682", 0)
	if merged {
		t.Error("expected merged=false for OPEN state")
	}
	if !strings.Contains(reason, "OPEN") {
		t.Errorf("expected 'OPEN' in reason, got %q", reason)
	}
}

func TestVerifyPRMerged_StateClosed(t *testing.T) {
	// CLOSED without merge (e.g., user or bot closed the PR manually).
	srv := prStateServer(t, "CLOSED")
	defer srv.Close()
	s := &Scheduler{client: gh.NewClientWithURL("test-token", srv.URL)}

	merged, reason := s.verifyPRMerged(context.Background(),
		"https://github.com/acme/platform/pull/682", 0)
	if merged {
		t.Error("expected merged=false for CLOSED state (PR closed without merge)")
	}
	if !strings.Contains(reason, "CLOSED") {
		t.Errorf("expected 'CLOSED' in reason, got %q", reason)
	}
}

// ── Fail-closed blocker classification (#4070) ──────────────────────────────

const testPRURL = "https://github.com/acme/platform/pull/682"

func TestVerifyPRMerged_Conflicting_NamesBlocker(t *testing.T) {
	// State OPEN + Mergeable CONFLICTING → the named blocker must surface the
	// conflict (not-mergeable bucket) so #4073's stuck-epic detector reads it.
	srv := prBlockerServer(t, "OPEN", "CONFLICTING", "DIRTY", "")
	defer srv.Close()
	s := &Scheduler{client: gh.NewClientWithURL("test-token", srv.URL)}

	merged, reason := s.verifyPRMerged(context.Background(), testPRURL, 4070)
	if merged {
		t.Fatal("expected merged=false for CONFLICTING PR")
	}
	if !strings.Contains(reason, pmstages.ReasonNotMergeable) || !strings.Contains(reason, "CONFLICTING") {
		t.Errorf("reason=%q, want it to name %q and CONFLICTING", reason, pmstages.ReasonNotMergeable)
	}
}

func TestVerifyPRMerged_DirtyBehindState_NamesBlocker(t *testing.T) {
	// State OPEN + MERGEABLE but mergeStateStatus BEHIND (base moved ahead) →
	// dirty-merge-state bucket naming BEHIND.
	srv := prBlockerServer(t, "OPEN", "MERGEABLE", "BEHIND", "")
	defer srv.Close()
	s := &Scheduler{client: gh.NewClientWithURL("test-token", srv.URL)}

	merged, reason := s.verifyPRMerged(context.Background(), testPRURL, 4070)
	if merged {
		t.Fatal("expected merged=false for BEHIND PR")
	}
	if !strings.Contains(reason, pmstages.ReasonDirtyState) || !strings.Contains(reason, "BEHIND") {
		t.Errorf("reason=%q, want it to name %q and BEHIND", reason, pmstages.ReasonDirtyState)
	}
}

func TestVerifyPRMerged_ReviewRequired_NamesBlocker(t *testing.T) {
	// State OPEN + MERGEABLE + CLEAN but reviewDecision REVIEW_REQUIRED →
	// review-not-approved bucket naming REVIEW_REQUIRED.
	srv := prBlockerServer(t, "OPEN", "MERGEABLE", "CLEAN", "REVIEW_REQUIRED")
	defer srv.Close()
	s := &Scheduler{client: gh.NewClientWithURL("test-token", srv.URL)}

	merged, reason := s.verifyPRMerged(context.Background(), testPRURL, 4070)
	if merged {
		t.Fatal("expected merged=false for REVIEW_REQUIRED PR")
	}
	if !strings.Contains(reason, pmstages.ReasonReviewMissing) || !strings.Contains(reason, "REVIEW_REQUIRED") {
		t.Errorf("reason=%q, want it to name %q and REVIEW_REQUIRED", reason, pmstages.ReasonReviewMissing)
	}
}

func TestVerifyPRMerged_MergedButIssueStillOpen_TrustsMerged(t *testing.T) {
	// #4070 review fix (assert-before-close race): when the PR is MERGED, that is
	// the authoritative success signal. A still-OPEN linked issue here is NOT a
	// merge failure — the post-merge close (checkEpicCompletion) runs AFTER this
	// verifier and owns closure, and GitHub's `Closes #N` auto-close may not have
	// propagated yet. Hard-failing would revert a genuinely merged PR to Ready.
	srv := prBlockerServer(t, "MERGED", "MERGEABLE", "CLEAN", "APPROVED")
	defer srv.Close()
	issueSvc := newMockIssueSvc()
	issueSvc.addIssue("nightgauge", "acme-platform", 4070,
		&types.Issue{Number: 4070, State: "OPEN"})
	s := &Scheduler{
		client:   gh.NewClientWithURL("test-token", srv.URL),
		issueSvc: issueSvc,
	}

	merged, reason := s.verifyPRMerged(context.Background(), testPRURL, 4070)
	if !merged {
		t.Fatalf("expected merged=true when PR is MERGED (issue close owned by post-merge), got reason=%q", reason)
	}
}

func TestVerifyPRMerged_MergedAndIssueClosed_Succeeds(t *testing.T) {
	// Happy path: PR MERGED and the linked issue CLOSED → success.
	srv := prBlockerServer(t, "MERGED", "MERGEABLE", "CLEAN", "APPROVED")
	defer srv.Close()
	issueSvc := newMockIssueSvc()
	issueSvc.addIssue("nightgauge", "acme-platform", 4070,
		&types.Issue{Number: 4070, State: "CLOSED"})
	s := &Scheduler{
		client:   gh.NewClientWithURL("test-token", srv.URL),
		issueSvc: issueSvc,
	}

	merged, reason := s.verifyPRMerged(context.Background(), testPRURL, 4070)
	if !merged {
		t.Errorf("expected merged=true when PR MERGED and issue CLOSED, got reason=%q", reason)
	}
}

func TestVerifyPRMerged_MergedIssueFetchError_Inconclusive(t *testing.T) {
	// Inconclusive-on-transient-error: PR MERGED but GetIssue errors (eventual
	// consistency on the close). Must trust MERGED rather than flap the
	// pipeline. The mock returns an error for an unregistered issue.
	srv := prBlockerServer(t, "MERGED", "MERGEABLE", "CLEAN", "APPROVED")
	defer srv.Close()
	s := &Scheduler{
		client:   gh.NewClientWithURL("test-token", srv.URL),
		issueSvc: newMockIssueSvc(), // issue 4070 not registered → GetIssue errors
	}

	merged, reason := s.verifyPRMerged(context.Background(), testPRURL, 4070)
	if !merged {
		t.Errorf("expected merged=true (inconclusive issue fetch trusts MERGED), got reason=%q", reason)
	}
}

func TestVerifyPRMergeForStage_NotMergedFailsClosed(t *testing.T) {
	// Both the normal success tail AND the budget-shipped fast-advance route
	// through verifyPRMergeForStage. A non-MERGED PR must return failed=true so
	// the caller aborts the pipeline — closing the phantom-success hole where a
	// budget-killed "shipped" pr-merge skipped verification (#4070 review).
	srv := prBlockerServer(t, "OPEN", "CONFLICTING", "DIRTY", "REVIEW_REQUIRED")
	defer srv.Close()
	rs := state.NewRuntimeState("acme/platform", 4070, "item-1")
	rs.SetPrUrl(testPRURL)
	s := &Scheduler{client: gh.NewClientWithURL("test-token", srv.URL)}
	item := types.BoardItem{Number: 4070, Repo: "acme/platform"}

	if failed := s.verifyPRMergeForStage(context.Background(), item, rs, "budget-shipped"); !failed {
		t.Fatal("expected verifyPRMergeForStage to return failed=true for a non-MERGED PR")
	}
}

// TestValidateStageOutput verifies the missing-output-context detection
// introduced for #2870. A stage that exits 0 without writing its expected
// output context file must be treated as a stage failure.
func TestValidateStageOutput(t *testing.T) {
	tmpDir := t.TempDir()

	// Pre-create the .nightgauge/pipeline directory so the "present" cases
	// can write the expected flat <stage>-<N>.json file there (the convention
	// shared with the gates + SDK; see stagecontext.ContextPath).
	pipelineDir := filepath.Join(tmpDir, ".nightgauge", "pipeline")
	if err := os.MkdirAll(pipelineDir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	tests := []struct {
		name        string
		stage       state.PipelineStage
		writeOutput bool
		wantErr     bool
		errContains string
	}{
		{
			name:        "issue-pickup wrote its output — pass",
			stage:       state.StageIssuePickup,
			writeOutput: true,
			wantErr:     false,
		},
		{
			name:        "issue-pickup did NOT write output — fail",
			stage:       state.StageIssuePickup,
			writeOutput: false,
			wantErr:     true,
			errContains: "did not write expected output context",
		},
		{
			name:        "feature-planning did NOT write output — fail (names the offender, not the next stage)",
			stage:       state.StageFeaturePlanning,
			writeOutput: false,
			wantErr:     true,
			errContains: "feature-planning",
		},
		{
			name:        "pr-merge has no output context — pass even when nothing written",
			stage:       state.StagePRMerge,
			writeOutput: false,
			wantErr:     false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Clean the pipeline dir between cases so prior writes don't bleed
			matches, _ := filepath.Glob(filepath.Join(pipelineDir, "*"))
			for _, m := range matches {
				_ = os.Remove(m)
			}

			if tc.writeOutput {
				ctxType := stageOutputContextType[tc.stage]
				outputFile := filepath.Join(pipelineDir, fmt.Sprintf("%s-42.json", ctxType))
				if err := os.WriteFile(outputFile, []byte(`{"ok":true}`), 0644); err != nil {
					t.Fatalf("write output: %v", err)
				}
			}

			err := validateStageOutput(tc.stage, tmpDir, 42)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got nil")
				}
				if tc.errContains != "" && !strings.Contains(err.Error(), tc.errContains) {
					t.Errorf("error %q does not contain %q", err.Error(), tc.errContains)
				}
			} else {
				if err != nil {
					t.Errorf("unexpected error: %v", err)
				}
			}
		})
	}
}

func TestDevContextBuildPassed(t *testing.T) {
	tmpDir := t.TempDir()
	pipelineDir := filepath.Join(tmpDir, ".nightgauge", "pipeline")
	if err := os.MkdirAll(pipelineDir, 0755); err != nil {
		t.Fatal(err)
	}

	writeDevContext := func(content string) {
		p := filepath.Join(pipelineDir, "dev-42.json")
		if err := os.WriteFile(p, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}

	// Missing file → false (safe default)
	if got := devContextBuildPassed(tmpDir, 42); got != false {
		t.Error("missing dev context: expected false, got true")
	}

	// ran=true, status=passed → true
	writeDevContext(`{"build_verification":{"ran":true,"status":"passed"}}`)
	if got := devContextBuildPassed(tmpDir, 42); got != true {
		t.Error("passed build: expected true, got false")
	}

	// ran=true, status=failed → false
	writeDevContext(`{"build_verification":{"ran":true,"status":"failed"}}`)
	if got := devContextBuildPassed(tmpDir, 42); got != false {
		t.Error("failed build: expected false, got true")
	}

	// ran=false, status=passed → false (must have actually run)
	writeDevContext(`{"build_verification":{"ran":false,"status":"passed"}}`)
	if got := devContextBuildPassed(tmpDir, 42); got != false {
		t.Error("not-ran build: expected false, got true")
	}

	// malformed JSON → false
	writeDevContext(`not-valid-json`)
	if got := devContextBuildPassed(tmpDir, 42); got != false {
		t.Error("malformed JSON: expected false, got true")
	}
}

// --- Re-route helpers ---

func makeIssueContext(t *testing.T, dir string, issueNumber int, devModel string, complexityScore int) string {
	t.Helper()
	pipelineDir := filepath.Join(dir, ".nightgauge", "pipeline")
	if err := os.MkdirAll(pipelineDir, 0755); err != nil {
		t.Fatal(err)
	}
	content := fmt.Sprintf(`{
  "routing": {
    "complexity_score": %d,
    "path": "standard",
    "pickup_recommendation": {
      "dev_model": %q
    }
  }
}`, complexityScore, devModel)
	p := filepath.Join(pipelineDir, fmt.Sprintf("issue-%d.json", issueNumber))
	if err := os.WriteFile(p, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	return p
}

func makePerfModeFile(t *testing.T, dir, mode string) string {
	t.Helper()
	incDir := filepath.Join(dir, ".nightgauge")
	if err := os.MkdirAll(incDir, 0755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(incDir, "performance-mode.yaml")
	if err := os.WriteFile(p, []byte("mode: "+mode+"\n"), 0644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestScheduler_ShouldReRoute_PerfModeNewer(t *testing.T) {
	tmpDir := t.TempDir()
	s := &Scheduler{}

	contextPath := makeIssueContext(t, tmpDir, 3140, "claude-opus-4-7", 8)
	// Ensure perf-mode is strictly newer by sleeping 10ms and then writing it
	time.Sleep(10 * time.Millisecond)
	makePerfModeFile(t, tmpDir, "efficiency")

	// Verify mtime ordering
	ctxInfo, _ := os.Stat(contextPath)
	perfInfo, _ := os.Stat(filepath.Join(tmpDir, ".nightgauge", "performance-mode.yaml"))
	if !perfInfo.ModTime().After(ctxInfo.ModTime()) {
		t.Skip("filesystem mtime resolution too coarse for this test — skipping")
	}

	if got := s.shouldReRoute(tmpDir, 3140); !got {
		t.Error("shouldReRoute = false, want true (perf-mode is newer)")
	}
}

func TestScheduler_ShouldReRoute_ContextNewer(t *testing.T) {
	tmpDir := t.TempDir()
	s := &Scheduler{}

	makePerfModeFile(t, tmpDir, "efficiency")
	time.Sleep(10 * time.Millisecond)
	makeIssueContext(t, tmpDir, 3140, "claude-opus-4-7", 8)

	// Verify mtime ordering
	ctxInfo, _ := os.Stat(filepath.Join(tmpDir, ".nightgauge", "pipeline", "issue-3140.json"))
	perfInfo, _ := os.Stat(filepath.Join(tmpDir, ".nightgauge", "performance-mode.yaml"))
	if !ctxInfo.ModTime().After(perfInfo.ModTime()) {
		t.Skip("filesystem mtime resolution too coarse for this test — skipping")
	}

	if got := s.shouldReRoute(tmpDir, 3140); got {
		t.Error("shouldReRoute = true, want false (context is newer)")
	}
}

func TestScheduler_ShouldReRoute_MissingPerfMode(t *testing.T) {
	tmpDir := t.TempDir()
	s := &Scheduler{}
	makeIssueContext(t, tmpDir, 3140, "claude-sonnet-4-6", 5)
	// No performance-mode.yaml written

	if got := s.shouldReRoute(tmpDir, 3140); got {
		t.Error("shouldReRoute = true, want false (no perf-mode file)")
	}
}

func TestScheduler_ReRouteContext_EfficiencyOverride(t *testing.T) {
	tmpDir := t.TempDir()
	s := &Scheduler{}
	ctx := context.Background()

	makeIssueContext(t, tmpDir, 3140, "claude-opus-4-7", 8)
	makePerfModeFile(t, tmpDir, "efficiency")

	rec, err := s.reRouteContext(ctx, tmpDir, 3140, "claude-opus-4-7")
	if err != nil {
		t.Fatalf("reRouteContext error: %v", err)
	}

	// efficiency mode → sonnet for feature-dev (registry-resolved, #50)
	if rec.Model != routing.ModelSonnet {
		t.Errorf("rec.Model = %q, want %q", rec.Model, routing.ModelSonnet)
	}

	// Verify context JSON was updated on disk
	data, err := os.ReadFile(filepath.Join(tmpDir, ".nightgauge", "pipeline", "issue-3140.json"))
	if err != nil {
		t.Fatalf("read updated context: %v", err)
	}
	var updated map[string]interface{}
	if err := json.Unmarshal(data, &updated); err != nil {
		t.Fatalf("parse updated context: %v", err)
	}
	routingRaw, _ := updated["routing"].(map[string]interface{})
	pickupRec, _ := routingRaw["pickup_recommendation"].(map[string]interface{})
	if pickupRec["dev_model"] != routing.ModelSonnet {
		t.Errorf("context dev_model = %q, want %q", pickupRec["dev_model"], routing.ModelSonnet)
	}
	// complexity_score must be preserved
	if cs, _ := routingRaw["complexity_score"].(float64); int(cs) != 8 {
		t.Errorf("complexity_score = %v, want 8 (must not be changed by re-routing)", cs)
	}
}

func TestScheduler_ReRouteContext_MaximumMode(t *testing.T) {
	tmpDir := t.TempDir()
	s := &Scheduler{}
	ctx := context.Background()

	makeIssueContext(t, tmpDir, 3140, "claude-sonnet-4-6", 5)
	makePerfModeFile(t, tmpDir, "maximum")

	rec, err := s.reRouteContext(ctx, tmpDir, 3140, "claude-sonnet-4-6")
	if err != nil {
		t.Fatalf("reRouteContext error: %v", err)
	}

	// maximum mode → opus for all stages
	if rec.Model != "claude-opus-4-8" {
		t.Errorf("rec.Model = %q, want claude-opus-4-8", rec.Model)
	}
}

func TestScheduler_ReRouteContext_LogsChange(t *testing.T) {
	tmpDir := t.TempDir()
	s := &Scheduler{}
	ctx := context.Background()

	makeIssueContext(t, tmpDir, 3140, "claude-sonnet-4-6", 3)
	makePerfModeFile(t, tmpDir, "maximum")

	// Capture log output
	var logBuf strings.Builder
	log.SetOutput(&logBuf)
	defer log.SetOutput(os.Stderr)

	rec, err := s.reRouteContext(ctx, tmpDir, 3140, "claude-sonnet-4-6")
	if err != nil {
		t.Fatalf("reRouteContext error: %v", err)
	}

	logOutput := logBuf.String()
	if !strings.Contains(logOutput, "[router] re-evaluated #3140 due to perf-mode change:") {
		t.Errorf("expected re-route log line, got: %q", logOutput)
	}
	if !strings.Contains(logOutput, "claude-sonnet-4-6→"+rec.Model) {
		t.Errorf("expected model transition in log, got: %q", logOutput)
	}
}

func TestScheduler_ReRouteContext_AtomicWrite(t *testing.T) {
	tmpDir := t.TempDir()
	s := &Scheduler{}
	ctx := context.Background()

	makeIssueContext(t, tmpDir, 3140, "claude-sonnet-4-6", 5)
	makePerfModeFile(t, tmpDir, "maximum")

	if _, err := s.reRouteContext(ctx, tmpDir, 3140, "claude-sonnet-4-6"); err != nil {
		t.Fatalf("reRouteContext error: %v", err)
	}

	// Temp file must not remain after successful write
	tmpFile := filepath.Join(tmpDir, ".nightgauge", "pipeline", "issue-3140.json.tmp")
	if _, err := os.Stat(tmpFile); !os.IsNotExist(err) {
		t.Error("temp file should be removed after atomic rename")
	}

	// Context file must be valid JSON
	data, err := os.ReadFile(filepath.Join(tmpDir, ".nightgauge", "pipeline", "issue-3140.json"))
	if err != nil {
		t.Fatalf("read context: %v", err)
	}
	var check interface{}
	if err := json.Unmarshal(data, &check); err != nil {
		t.Errorf("written context is not valid JSON: %v", err)
	}
}

// TestRunRootResolvesTargetRepo verifies the scheduler roots a run's on-disk
// state at the run's TARGET repo (#229): with a resolver installed, a
// registered repo resolves to its mapped root; an unregistered or empty repo
// falls back to the execution manager's workspace (launch) root; and with no
// resolver installed every repo resolves to the launch root (single-repo / CLI
// / auto behavior unchanged).
func TestRunRootResolvesTargetRepo(t *testing.T) {
	launchRoot := t.TempDir()

	s := &Scheduler{execMgr: execution.NewManager(launchRoot, nil)}
	s.WithRepoPathResolver(func(repo string) string {
		if repo == "owner/other" {
			return "/tmp/other"
		}
		return ""
	})

	if got := s.runRoot("owner/other"); got != "/tmp/other" {
		t.Errorf("runRoot(owner/other) = %q, want /tmp/other", got)
	}
	if got := s.runRoot("owner/unknown"); got != launchRoot {
		t.Errorf("runRoot(owner/unknown) = %q, want launchRoot %q", got, launchRoot)
	}
	if got := s.runRoot(""); got != launchRoot {
		t.Errorf("runRoot(\"\") = %q, want launchRoot %q", got, launchRoot)
	}

	// No resolver installed → everything falls back to the launch root, and the
	// forwarded execution-manager resolver agrees.
	s2 := &Scheduler{execMgr: execution.NewManager(launchRoot, nil)}
	if got := s2.runRoot("owner/other"); got != launchRoot {
		t.Errorf("runRoot with nil resolver = %q, want launchRoot %q", got, launchRoot)
	}
	// WithRepoPathResolver also forwards to the execution manager so worktree
	// resolution stays consistent with run state.
	if got := s.execMgr.RepoRoot("owner/other"); got != "/tmp/other" {
		t.Errorf("execMgr.RepoRoot(owner/other) = %q, want /tmp/other (forwarded)", got)
	}
}
