package orchestrator

import (
	"context"
	"testing"

	"github.com/nightgauge/nightgauge/internal/state"
	"github.com/nightgauge/nightgauge/pkg/types"
)

// A run the member queued private (#2400) carries private on its first stage
// event, on every later event and on its completion record.
func TestSchedulerPrivateQueueItemRunCarriesVisibility(t *testing.T) {
	stubReconcileGhUnreachable(t)
	tmpDir := t.TempDir()
	mock := &mockTelemetry{}
	s := buildTelemetryTestScheduler(t, tmpDir, mock, &mockAlwaysFailStageRunner{}, []string{
		"nightgauge-issue-pickup",
	})
	s.retryEngine = NewRetryEngine(RetryConfig{MaxEscalationsPerStage: 0})
	s.queue = []QueueItem{{Repo: "nightgauge/test", IssueNumber: 42, Status: "processing", Visibility: state.VisibilityPrivate}}

	s.runPipeline(context.Background(), types.BoardItem{Number: 42, Repo: "nightgauge/test", ID: "item-42"})

	first, ok := mock.firstOfType("stage_started")
	if !ok {
		t.Fatal("expected a stage_started event")
	}
	if first.Visibility != state.VisibilityPrivate {
		t.Errorf("first stage_started Visibility = %q, want private", first.Visibility)
	}
	mock.mu.Lock()
	defer mock.mu.Unlock()
	for _, e := range mock.events {
		if e.Visibility != state.VisibilityPrivate {
			t.Errorf("%s event Visibility = %q, want private", e.EventType, e.Visibility)
		}
	}
	if len(mock.runs) == 0 {
		t.Fatal("expected a completion record")
	}
	for _, r := range mock.runs {
		if r.Visibility != state.VisibilityPrivate {
			t.Errorf("completion record Visibility = %q, want private", r.Visibility)
		}
	}
}

// A run the autonomous scheduler starts from the board on its own has no
// queue item and names no visibility, which the service reads as team.
func TestSchedulerBoardPickedRunIsTeam(t *testing.T) {
	stubReconcileGhUnreachable(t)
	tmpDir := t.TempDir()
	mock := &mockTelemetry{}
	s := buildTelemetryTestScheduler(t, tmpDir, mock, &mockAlwaysFailStageRunner{}, []string{
		"nightgauge-issue-pickup",
	})
	s.retryEngine = NewRetryEngine(RetryConfig{MaxEscalationsPerStage: 0})

	s.runPipeline(context.Background(), types.BoardItem{Number: 43, Repo: "nightgauge/test", ID: "item-43"})

	mock.mu.Lock()
	defer mock.mu.Unlock()
	if len(mock.events) == 0 {
		t.Fatal("expected events")
	}
	for _, e := range mock.events {
		if e.Visibility != "" {
			t.Errorf("%s event Visibility = %q, want none", e.EventType, e.Visibility)
		}
	}
	for _, r := range mock.runs {
		if r.Visibility != "" {
			t.Errorf("completion record Visibility = %q, want none", r.Visibility)
		}
	}
}

// The queue snapshot pushed to the hosted service names a private item's
// visibility and omits a team item's.
func TestPersistQueueSyncCarriesVisibility(t *testing.T) {
	tmpDir := gitWorkspace(t)
	mock := &mockTelemetry{}
	s := buildTelemetryTestScheduler(t, tmpDir, mock, &mockAlwaysSucceedStageRunner{}, nil)
	s.workspaceRoot = tmpDir
	s.queue = []QueueItem{
		{IssueNumber: 10, Position: 1, Status: "pending", Repo: "nightgauge/test", Visibility: state.VisibilityPrivate},
		{IssueNumber: 11, Position: 2, Status: "pending", Repo: "nightgauge/test"},
		{IssueNumber: 12, Position: 3, Status: "pending", Repo: "nightgauge/test", Visibility: "public"},
	}

	s.persistQueue()

	items, ok := mock.lastQueueSync()
	if !ok || len(items) != 3 {
		t.Fatalf("want 3 synced items, got %+v", items)
	}
	if items[0].Visibility != state.VisibilityPrivate {
		t.Errorf("private item Visibility = %q, want private", items[0].Visibility)
	}
	if items[1].Visibility != "" {
		t.Errorf("team item Visibility = %q, want none", items[1].Visibility)
	}
	if items[2].Visibility != "" {
		t.Errorf("malformed item Visibility = %q, want none", items[2].Visibility)
	}
}

// A private request raises the waiting item it duplicates, and nothing
// lowers a private item.
func TestQueueAddRaisesVisibilityNeverLowers(t *testing.T) {
	tmpDir := gitWorkspace(t)
	s := buildTelemetryTestScheduler(t, tmpDir, &mockTelemetry{}, &mockAlwaysSucceedStageRunner{}, nil)
	s.workspaceRoot = tmpDir

	s.QueueAdd(QueueEntry{Repo: "nightgauge/test", IssueNumber: 1})
	s.QueueAddItem(QueueItem{Repo: "nightgauge/test", IssueNumber: 1, Visibility: state.VisibilityPrivate})
	if got := s.queueItemVisibility("nightgauge/test", 1); got != state.VisibilityPrivate {
		t.Errorf("after a private request, item Visibility = %q, want private", got)
	}

	s.QueueAdd(QueueEntry{Repo: "nightgauge/test", IssueNumber: 2, Visibility: state.VisibilityPrivate})
	s.QueueAdd(QueueEntry{Repo: "nightgauge/test", IssueNumber: 2})
	s.QueueAddItem(QueueItem{Repo: "nightgauge/test", IssueNumber: 2, Visibility: state.VisibilityTeam})
	if got := s.queueItemVisibility("nightgauge/test", 2); got != state.VisibilityPrivate {
		t.Errorf("after team requests, private item Visibility = %q, want private", got)
	}

	s.QueueAddItem(QueueItem{Repo: "nightgauge/test", IssueNumber: 3, Visibility: "public"})
	if got := s.queueItemVisibility("nightgauge/test", 3); got != "" {
		t.Errorf("malformed request Visibility = %q, want none", got)
	}
}
