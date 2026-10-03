package orchestrator

import (
	"context"
	"sync"
	"testing"

	"github.com/nightgauge/nightgauge/internal/layout/layouttest"
	"github.com/nightgauge/nightgauge/pkg/types"
)

// A window reload or close drops only the items its dispatches took (#2396).
// Every waiting item stays in the persisted queue for the next window: the
// operator's pending, ready and paused items, a pending re-queue of an issue
// whose run is ending, and the platform runs waiting items carry.
func TestQueueDropProcessing_KeepsEveryWaitingItem(t *testing.T) {
	root := layouttest.Repo(t)
	s := &Scheduler{
		workspaceRoot: root,
		repoRunning:   make(map[string]int),
		mergeLocks:    make(map[string]*sync.Mutex),
	}
	s.QueueAddItem(
		QueueItem{Repo: "o/a", IssueNumber: 1, Title: "running", RemoteRunID: "run-running"},
		QueueItem{Repo: "o/a", IssueNumber: 2, Title: "triggered", RemoteRunID: "run-queued"},
		QueueItem{Repo: "o/b", IssueNumber: 3, Title: "deferred"},
		QueueItem{Repo: "o/b", IssueNumber: 4, Title: "ready"},
		QueueItem{Repo: "o/b", IssueNumber: 5, Title: "running too"},
	)
	s.mu.Lock()
	s.queue[0].Status = "processing"
	s.queue[2].Status = "paused"
	s.queue[3].Status = "ready"
	s.queue[4].Status = "processing"
	// The operator queued #1 again while its run was in flight.
	s.queue = append(s.queue, QueueItem{Repo: "o/a", IssueNumber: 1, Title: "again", Status: "pending"})
	s.mu.Unlock()

	if got := s.QueueDropProcessing(); got != 2 {
		t.Fatalf("QueueDropProcessing() = %d, want the 2 processing items", got)
	}

	// The next window's daemon reads what this one persisted.
	next := &Scheduler{
		workspaceRoot: root,
		repoRunning:   make(map[string]int),
		mergeLocks:    make(map[string]*sync.Mutex),
	}
	next.loadQueue()
	items := next.GetState().Items
	want := []struct {
		number int
		title  string
		status string
		runID  string
	}{
		{2, "triggered", "pending", "run-queued"},
		{3, "deferred", "paused", ""},
		{4, "ready", "ready", ""},
		{1, "again", "pending", ""},
	}
	if len(items) != len(want) {
		t.Fatalf("persisted queue = %+v, want %d waiting items", items, len(want))
	}
	for i, w := range want {
		it := items[i]
		if it.IssueNumber != w.number || it.Title != w.title || it.Status != w.status || it.RemoteRunID != w.runID {
			t.Errorf("item %d = #%d %q %s run=%q, want #%d %q %s run=%q",
				i, it.IssueNumber, it.Title, it.Status, it.RemoteRunID, w.number, w.title, w.status, w.runID)
		}
		if it.Position != i+1 {
			t.Errorf("item %d position = %d, want %d", i, it.Position, i+1)
		}
	}

	if got := s.QueueDropProcessing(); got != 0 {
		t.Errorf("a second drop removed %d items, want 0", got)
	}
}

// blockingIssueSvc holds the blocker refresh of a dequeue until released, so
// a test can land a drop while that dequeue is under way.
type blockingIssueSvc struct {
	*mockEpicIssueSvc
	entered chan struct{}
	release chan struct{}
}

func (b *blockingIssueSvc) GetIssuesByNumbersWithoutRelations(ctx context.Context, owner, repo string, numbers []int) (map[int]*types.Issue, error) {
	b.entered <- struct{}{}
	<-b.release
	return b.mockEpicIssueSvc.GetIssuesByNumbersWithoutRelations(ctx, owner, repo, numbers)
}

// A dequeue under way when the window's drop lands dequeues nothing: an item
// it marked processing after the drop would never be released once the
// window is gone. A dequeue that starts after the drop is not refused.
func TestQueueDropProcessing_RefusesADequeueUnderWay(t *testing.T) {
	svc := &blockingIssueSvc{
		mockEpicIssueSvc: newMockEpicIssueSvc(),
		entered:          make(chan struct{}, 1),
		release:          make(chan struct{}),
	}
	svc.addIssue("o", "a", 99, &types.Issue{Number: 99, State: "CLOSED"})
	s := &Scheduler{
		issueSvc:    svc,
		repoRunning: make(map[string]int),
		mergeLocks:  make(map[string]*sync.Mutex),
	}
	s.QueueAddItem(QueueItem{
		Repo: "o/a", IssueNumber: 1, Title: "blocked until refreshed",
		BlockedBy: []QueueBlockingRef{{Number: 99, State: "OPEN"}},
	})

	got := make(chan []QueueItem, 1)
	go func() { got <- s.DequeueIndependent(context.Background(), 1, nil) }()
	<-svc.entered // the dequeue is reading blockers, outside the lock
	if dropped := s.QueueDropProcessing(); dropped != 0 {
		t.Fatalf("drop removed %d items, want 0: nothing was dispatched yet", dropped)
	}
	close(svc.release)

	if items := <-got; len(items) != 0 {
		t.Fatalf("a dequeue under way when the drop landed dequeued %+v, want nothing", items)
	}
	for _, it := range s.GetState().Items {
		if it.Status == "processing" {
			t.Fatalf("item #%d marked processing after the drop", it.IssueNumber)
		}
	}

	// Not a latch: the next dequeue takes the item.
	svc.entered = make(chan struct{}, 1)
	if items := s.DequeueIndependent(context.Background(), 1, nil); len(items) != 1 || items[0].IssueNumber != 1 {
		t.Fatalf("dequeue after the drop = %+v, want #1", items)
	}
}
