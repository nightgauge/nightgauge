package orchestrator

import (
	"sync"
	"testing"

	"github.com/nightgauge/nightgauge/internal/layout/layouttest"
)

// The queue operations a queue item added in the cloud uses (#2440).

func cloudQueueScheduler(t *testing.T) (*Scheduler, *fakeTelemetry) {
	t.Helper()
	ft := &fakeTelemetry{}
	return &Scheduler{
		trustGate:        allWritersGate(),
		workspaceRoot:    layouttest.Repo(t),
		repoRunning:      make(map[string]int),
		mergeLocks:       make(map[string]*sync.Mutex),
		telemetrySvc:     ft,
		telemetryEnabled: true,
	}, ft
}

func TestQueueAddIfAbsent_AddsOnce(t *testing.T) {
	s, _ := cloudQueueScheduler(t)
	if !s.QueueAddIfAbsent(QueueItem{Repo: "acme/web", IssueNumber: 7, Title: "t"}) {
		t.Fatal("first add reported not added")
	}
	if s.QueueAddIfAbsent(QueueItem{Repo: "acme/web", IssueNumber: 7}) {
		t.Error("second add of the same issue reported added")
	}
	if !s.QueueAddIfAbsent(QueueItem{Repo: "acme/api", IssueNumber: 7}) {
		t.Error("the same number in another repository was refused")
	}
	if items := s.GetState().Items; len(items) != 2 || items[0].Status != "pending" || items[0].Position != 1 || items[1].Position != 2 {
		t.Errorf("items = %+v, want two pending items at positions 1 and 2", items)
	}
}

func TestQueueHas_MatchesRepositoryCaseInsensitively(t *testing.T) {
	s, _ := cloudQueueScheduler(t)
	s.QueueAddItem(QueueItem{Repo: "acme/web", IssueNumber: 7})
	if !s.QueueHas("ACME/Web", 7) {
		t.Error("QueueHas missed the item under another case")
	}
	if s.QueueHas("acme/api", 7) || s.QueueHas("acme/web", 8) {
		t.Error("QueueHas matched another repository's or issue's item")
	}
}

// Only a waiting item is taken back: one a dispatch took, or one paused, is
// left where it is.
func TestQueueWithdrawWaiting(t *testing.T) {
	s, _ := cloudQueueScheduler(t)
	s.QueueAddItem(
		QueueItem{Repo: "acme/web", IssueNumber: 1},
		QueueItem{Repo: "acme/web", IssueNumber: 2, Status: "processing"},
		QueueItem{Repo: "acme/web", IssueNumber: 3, Status: "paused"},
		QueueItem{Repo: "acme/web", IssueNumber: 4},
	)
	if !s.QueueWithdrawWaiting("acme/web", 1) {
		t.Error("a pending item was not withdrawn")
	}
	if s.QueueWithdrawWaiting("acme/web", 2) || s.QueueWithdrawWaiting("acme/web", 3) || s.QueueWithdrawWaiting("acme/web", 9) {
		t.Error("withdrew a processing, paused or absent item")
	}
	items := s.GetState().Items
	// Waiting items are renumbered from 1; a processing item keeps its slot's
	// position (recalculatePositions).
	if len(items) != 3 || items[0].IssueNumber != 2 || items[1].IssueNumber != 3 || items[1].Position != 1 ||
		items[2].IssueNumber != 4 || items[2].Position != 2 {
		t.Errorf("items = %+v, want #2, then #3 and #4 at positions 1 and 2", items)
	}
}

// The queue sync reports the priority an item was added with in the cloud,
// so the machine's row keeps it; any other item's priority still comes from
// its labels.
func TestQueueSync_ReportsTheRequestedPriority(t *testing.T) {
	s, ft := cloudQueueScheduler(t)
	s.QueueAddIfAbsent(QueueItem{Repo: "acme/web", IssueNumber: 1, Labels: []string{"priority:low"}, RequestedPriority: "critical"})
	s.QueueAddIfAbsent(QueueItem{Repo: "acme/web", IssueNumber: 2, Labels: []string{"priority:high"}})
	s.QueueAddIfAbsent(QueueItem{Repo: "acme/web", IssueNumber: 3})

	got := map[int]string{}
	for _, it := range ft.lastQueueSync {
		got[it.IssueNumber] = it.Priority
	}
	if got[1] != "critical" || got[2] != "high" || got[3] != "medium" {
		t.Errorf("synced priorities = %v, want 1:critical 2:high 3:medium", got)
	}
}
