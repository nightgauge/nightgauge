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

	if dropped, kept := s.QueueDropProcessing(nil, nil); dropped != 2 || kept != 0 {
		t.Fatalf("QueueDropProcessing() = %d dropped, %d kept; want the 2 processing items dropped", dropped, kept)
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

	if dropped, kept := s.QueueDropProcessing(nil, nil); dropped != 0 || kept != 0 {
		t.Errorf("a second drop removed %d and kept %d items, want 0 and 0", dropped, kept)
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
// window is gone. Nor does any later one: the daemon is the window's own
// child, and a dequeue the window sends after its drop (a fill that waited
// for its turn, a pipeline's auto-start) would strand its items the same way.
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
	if dropped, kept := s.QueueDropProcessing(nil, nil); dropped != 0 || kept != 0 {
		t.Fatalf("drop removed %d and kept %d items, want 0 and 0: nothing was dispatched yet", dropped, kept)
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

	// A latch: a dequeue that arrives after the drop takes nothing either, and
	// the item waits, pending, for the next window's daemon.
	if items := s.DequeueIndependentFor(context.Background(), "late", 1, nil); len(items) != 0 {
		t.Fatalf("dequeue after the drop = %+v, want nothing", items)
	}
	items := s.GetState().Items
	if len(items) != 1 || items[0].IssueNumber != 1 || items[0].Status != "pending" {
		t.Fatalf("queue after the drop = %+v, want #1 still pending", items)
	}
}

// newDropTestScheduler is a scheduler over a persisted queue with nothing
// blocking a dequeue.
func newDropTestScheduler(t *testing.T, root string) *Scheduler {
	t.Helper()
	return &Scheduler{
		workspaceRoot: root,
		issueSvc:      newMockEpicIssueSvc(),
		repoRunning:   make(map[string]int),
		mergeLocks:    make(map[string]*sync.Mutex),
		maxPerRepo:    10,
	}
}

// A reload while a fill is still starting its batch (#2396): one dequeue marks
// every item it returns processing at once, and the window starts them one at
// a time. The items it had not begun to start are waiting work, so the drop
// puts them back to waiting, each serving the platform run its dispatch
// served, and removes only the item whose start was under way.
func TestQueueDropProcessing_HandsBackTheDispatchesNotStarted(t *testing.T) {
	root := layouttest.Repo(t)
	s := newDropTestScheduler(t, root)
	s.QueueAddItem(
		QueueItem{Repo: "o/a", IssueNumber: 1, Title: "starting"},
		QueueItem{Repo: "o/a", IssueNumber: 2, Title: "waits its turn"},
		QueueItem{Repo: "o/a", IssueNumber: 3, Title: "triggered", RemoteRunID: "run-3"},
		QueueItem{Repo: "o/a", IssueNumber: 4, Title: "operator's, a run attached in the window"},
		QueueItem{Repo: "o/a", IssueNumber: 5, Title: "still queued"},
	)
	if taken := s.DequeueIndependentFor(context.Background(), "fill-1", 4, nil); len(taken) != 4 {
		t.Fatalf("dequeue = %+v, want 4 items", taken)
	}

	dropped, kept := s.QueueDropProcessing([]QueueHandBack{
		{Repo: "o/a", IssueNumber: 2},
		{Repo: "o/a", IssueNumber: 3, RemoteRunID: "run-3"},
		{Repo: "o/a", IssueNumber: 4, RemoteRunID: "run-4", RemoteRunAttached: true},
		{Repo: "o/a", IssueNumber: 9}, // names no processing item: ignored
	}, nil)
	if dropped != 1 || kept != 3 {
		t.Fatalf("QueueDropProcessing() = %d dropped, %d kept; want 1 and 3", dropped, kept)
	}

	next := newDropTestScheduler(t, root)
	next.loadQueue()
	want := []struct {
		number   int
		runID    string
		attached bool
	}{{2, "", false}, {3, "run-3", false}, {4, "run-4", true}, {5, "", false}}
	items := next.GetState().Items
	if len(items) != len(want) {
		t.Fatalf("persisted queue = %+v, want %d waiting items", items, len(want))
	}
	for i, w := range want {
		it := items[i]
		if it.IssueNumber != w.number || it.Status != "pending" || it.RemoteRunID != w.runID || it.RemoteRunAttached != w.attached {
			t.Errorf("item %d = #%d %s run=%q attached=%v, want #%d pending run=%q attached=%v",
				i, it.IssueNumber, it.Status, it.RemoteRunID, it.RemoteRunAttached, w.number, w.runID, w.attached)
		}
		if it.Position != i+1 {
			t.Errorf("item %d position = %d, want %d", i, it.Position, i+1)
		}
	}
}

// A dequeue Go answered before the drop landed, whose answer the window never
// read (#2396): the window cannot name its items, only its dispatch token, and
// they go back to waiting. An answered dequeue's items, and an item marked
// with no token, go with the runs that end.
func TestQueueDropProcessing_HandsBackAnUnansweredDequeue(t *testing.T) {
	root := layouttest.Repo(t)
	s := newDropTestScheduler(t, root)
	s.QueueAddItem(
		QueueItem{Repo: "o/a", IssueNumber: 1, Title: "answered, running"},
		QueueItem{Repo: "o/b", IssueNumber: 1, Title: "no token, running"},
		QueueItem{Repo: "o/a", IssueNumber: 2, Title: "unanswered", RemoteRunID: "run-2"},
		QueueItem{Repo: "o/a", IssueNumber: 3, Title: "unanswered too"},
	)
	if taken := s.DequeueIndependentFor(context.Background(), "fill-1", 1, nil); len(taken) != 1 || taken[0].IssueNumber != 1 {
		t.Fatalf("first dequeue = %+v, want o/a#1", taken)
	}
	if taken := s.DequeueIndependent(context.Background(), 1, nil); len(taken) != 1 || taken[0].Repo != "o/b" {
		t.Fatalf("second dequeue = %+v, want o/b#1", taken)
	}
	if taken := s.DequeueIndependentFor(context.Background(), "fill-2", 2, nil); len(taken) != 2 {
		t.Fatalf("third dequeue = %+v, want 2 items", taken)
	}

	dropped, kept := s.QueueDropProcessing(nil, []string{"fill-2", ""})
	if dropped != 2 || kept != 2 {
		t.Fatalf("QueueDropProcessing() = %d dropped, %d kept; want 2 and 2", dropped, kept)
	}
	items := s.GetState().Items
	if len(items) != 2 || items[0].IssueNumber != 2 || items[0].RemoteRunID != "run-2" || items[1].IssueNumber != 3 {
		t.Fatalf("queue = %+v, want #2 (with its run) and #3", items)
	}
	for _, it := range items {
		if it.Status != "pending" {
			t.Errorf("#%d status = %s, want pending", it.IssueNumber, it.Status)
		}
	}
}
