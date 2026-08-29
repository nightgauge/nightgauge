package platform

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// newEventStatusService returns an AnalyticsService whose pipeline-event
// endpoint always answers with `status`, plus a counter of POSTs received.
func newEventStatusService(t *testing.T, status int) (*AnalyticsService, *atomic.Int32, func()) {
	t.Helper()
	var posts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		posts.Add(1)
		w.WriteHeader(status)
	}))
	c, err := NewClient(Config{BaseURL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	c.setMode(ModeOnline)
	return NewAnalyticsService(c), &posts, srv.Close
}

func testEvent() PipelineEvent {
	return PipelineEvent{
		EventType: "stage_started",
		RunID:     "run-1103",
		Stage:     "feature-dev",
		Timestamp: time.Now(),
	}
}

// A permanent refusal must not be buffered: re-sending it cannot change the
// answer, and buffering it replays the failure on every flush tick forever
// while burying every other line in the daemon log (#1103).
func TestEmitPipelineEvent_PermanentRejectionIsNotBuffered(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusBadRequest} {
		svc, _, closeSrv := newEventStatusService(t, status)

		svc.EmitPipelineEvent(context.Background(), testEvent())
		waitForQuiescence()

		if got := svc.EventQueueCount(); got != 0 {
			t.Errorf("status %d: EventQueueCount() = %d, want 0 (permanent rejection must be dropped)", status, got)
		}
		closeSrv()
	}
}

// 5xx and 429 are timing verdicts, not verdicts about the request, so they keep
// their existing buffer-and-retry behaviour.
func TestEmitPipelineEvent_TransientFailureStillBuffers(t *testing.T) {
	for _, status := range []int{http.StatusServiceUnavailable, http.StatusTooManyRequests} {
		svc, _, closeSrv := newEventStatusService(t, status)

		svc.EmitPipelineEvent(context.Background(), testEvent())
		waitForQuiescence()

		if got := svc.EventQueueCount(); got != 1 {
			t.Errorf("status %d: EventQueueCount() = %d, want 1 (transient failure must still buffer)", status, got)
		}
		closeSrv()
	}
}

// The flush path must drop a permanently-rejected event rather than re-queuing
// it into the next tick — that re-queue is what made the retry loop eternal.
func TestFlush_PermanentRejectionIsNotRequeued(t *testing.T) {
	svc, posts, closeSrv := newEventStatusService(t, http.StatusForbidden)
	defer closeSrv()

	// Seed the queue directly, as an offline emit would have.
	svc.enqueueEvent(testEvent())
	svc.enqueueEvent(testEvent())
	if got := svc.EventQueueCount(); got != 2 {
		t.Fatalf("seed: EventQueueCount() = %d, want 2", got)
	}

	svc.FlushBuffered(context.Background())

	if got := svc.EventQueueCount(); got != 0 {
		t.Errorf("after flush: EventQueueCount() = %d, want 0 (403 must not be re-queued)", got)
	}

	// A second flush must not re-attempt anything: the queue is empty.
	before := posts.Load()
	svc.FlushBuffered(context.Background())
	if after := posts.Load(); after != before {
		t.Errorf("second flush issued %d more POST(s); a dropped event must not be retried", after-before)
	}
}

// The refusal is stated once per status, not once per event per tick.
func TestEmitPipelineEvent_RejectionReportedOncePerStatus(t *testing.T) {
	svc, _, closeSrv := newEventStatusService(t, http.StatusForbidden)
	defer closeSrv()

	if !svc.noteEventRejection(http.StatusForbidden) {
		t.Fatal("first rejection for a status must be reported")
	}
	for i := 0; i < 5; i++ {
		if svc.noteEventRejection(http.StatusForbidden) {
			t.Fatal("repeat rejection for the same status must be suppressed, not re-logged")
		}
	}
	if got := svc.eventRejectDropped; got != 6 {
		t.Errorf("eventRejectDropped = %d, want 6 (suppressed drops must still be counted)", got)
	}
	// A different status is a different verdict and is reported again.
	if !svc.noteEventRejection(http.StatusUnauthorized) {
		t.Error("a new status must be reported rather than suppressed")
	}
}

// waitForQuiescence gives EmitPipelineEvent's fire-and-forget goroutine time to
// finish. The method is deliberately non-blocking, so there is nothing to join.
func waitForQuiescence() {
	time.Sleep(250 * time.Millisecond)
}
