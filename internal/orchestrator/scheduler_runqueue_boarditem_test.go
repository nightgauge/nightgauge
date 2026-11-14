package orchestrator

// #908: RunQueue resolves one board item per queue entry, and must never
// report a forge failure as "this issue is not on the board".
//
// The two outcomes look similar and are not. Absence makes RunQueue SKIP the
// entry — queued work is silently dropped — so collapsing a transient fetch
// error into absence turns an outage into lost dispatches. The old whole-board
// scan kept them apart structurally (a failed ListItems returned an error; a
// successful read that matched nothing left item == nil). GetItem returns both
// through the same error value, so the distinction now depends on an
// errors.Is check that nothing else pins.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	gh "github.com/nightgauge/nightgauge/internal/github"
)

// emptyBoardServer answers every GraphQL query with a project holding no
// items, which is what GetItem turns into forge.ErrNotFound.
func emptyBoardServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"organization":{"projectV2":{"items":{"pageInfo":{"hasNextPage":false,"endCursor":null},"nodes":[]}}}}}`))
	}))
}

// brokenBoardServer fails every request. This is "I could not look", not
// "it is not there".
func brokenBoardServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "upstream exploded", http.StatusInternalServerError)
	}))
}

func runQueueOnce(t *testing.T, serverURL string) QueueRunSummary {
	t.Helper()
	client := gh.NewClientWithURL("test-token", serverURL)
	s := &Scheduler{
		trustGate:   allWritersGate(),
		client:      client,
		boardSvc:    gh.NewBoardService(client, "o", 1),
		owner:       "o",
		repoRunning: map[string]int{},
		queue:       []QueueItem{{Repo: "o/r", IssueNumber: 42, Status: "pending", Position: 1}},
	}
	summary, err := s.RunQueue(context.Background())
	if err != nil {
		t.Fatalf("RunQueue returned a pass-level error: %v", err)
	}
	if len(summary.Outcomes) != 1 {
		t.Fatalf("expected exactly 1 outcome for 1 queued entry, got %d", len(summary.Outcomes))
	}
	return summary
}

func TestRunQueue_MissingBoardItemIsReportedAsNotFound(t *testing.T) {
	srv := emptyBoardServer(t)
	defer srv.Close()

	got := runQueueOnce(t, srv.URL).Outcomes[0]

	if got.Kind != QueueOutcomeNotDispatched {
		t.Errorf("Kind = %q, want %q", got.Kind, QueueOutcomeNotDispatched)
	}
	if got.Detail != "not found on project board" {
		t.Errorf("Detail = %q, want %q — an issue genuinely absent from the board must still read as absent", got.Detail, "not found on project board")
	}
}

func TestRunQueue_BoardFetchFailureIsNotReportedAsNotFound(t *testing.T) {
	srv := brokenBoardServer(t)
	defer srv.Close()

	got := runQueueOnce(t, srv.URL).Outcomes[0]

	if got.Kind != QueueOutcomeNotDispatched {
		t.Errorf("Kind = %q, want %q", got.Kind, QueueOutcomeNotDispatched)
	}
	if got.Detail == "not found on project board" {
		t.Fatal("a forge failure was recorded as \"not found on project board\" — " +
			"absence makes RunQueue skip the entry, so an outage reported as absence " +
			"silently drops queued work. Branch on errors.Is(err, forge.ErrNotFound) " +
			"instead of treating every GetItem error as absence")
	}
	if !strings.HasPrefix(got.Detail, "board fetch failed:") {
		t.Errorf("Detail = %q, want a \"board fetch failed: …\" record", got.Detail)
	}
}

// #3278: an item RunQueue does not run is kept as dropped, with the reason
// `queue list` shows, instead of vanishing. It is not run again, does not
// count as queued, and `queue add` re-queues it.
func TestRunQueue_NotDispatchedItemIsKeptAsDropped(t *testing.T) {
	srv := emptyBoardServer(t)
	defer srv.Close()
	client := gh.NewClientWithURL("test-token", srv.URL)
	s := &Scheduler{
		trustGate:   allWritersGate(),
		client:      client,
		boardSvc:    gh.NewBoardService(client, "o", 1),
		owner:       "o",
		repoRunning: map[string]int{},
		queue:       []QueueItem{{Repo: "o/r", IssueNumber: 42, Status: "pending", Position: 1}},
	}
	if _, err := s.RunQueue(context.Background()); err != nil {
		t.Fatal(err)
	}
	list := s.QueueList()
	if len(list) != 1 || list[0].Status != QueueStatusDropped || list[0].DropReason != "not found on project board" {
		t.Fatalf("queue after run = %+v, want #42 dropped: not found on project board", list)
	}
	if s.QueueHas("o/r", 42) || len(s.QueuePendingItems()) != 0 {
		t.Error("a dropped item still counts as queued work")
	}

	again, err := s.RunQueue(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(again.Outcomes) != 0 || len(s.QueueList()) != 1 {
		t.Fatalf("second run outcomes=%v queue=%v: a dropped item must be neither run nor lost", again.Outcomes, s.QueueList())
	}

	s.QueueAdd(QueueEntry{Repo: "o/r", IssueNumber: 42})
	if l := s.QueueList(); len(l) != 1 || l[0].Status != "pending" || l[0].DropReason != "" {
		t.Fatalf("re-added item = %+v, want pending with no drop reason", l)
	}

	s.QueueClear()
	if len(s.QueueList()) != 0 {
		t.Fatal("queue clear left items")
	}
}

// A dropped item is never dequeued for dispatch.
func TestDequeueIndependent_SkipsDropped(t *testing.T) {
	s := &Scheduler{
		trustGate:   allWritersGate(),
		repoRunning: map[string]int{},
		queue:       []QueueItem{{Repo: "o/r", IssueNumber: 7, Status: QueueStatusDropped, DropReason: "has open blockers", Position: 1}},
	}
	if got := s.DequeueIndependent(context.Background(), 3, nil); len(got) != 0 {
		t.Fatalf("dequeued %+v, want nothing", got)
	}
}
