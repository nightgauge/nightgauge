package platform

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/state"
)

// TestAnalyticsService_SendGate pins the consent gate every write path asks:
// with it closed nothing is posted or buffered, a flush drops what was
// buffered while it was open, and opening it again lets writes through.
func TestAnalyticsService_SendGate(t *testing.T) {
	var posts int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost || r.Method == http.MethodPut {
			atomic.AddInt32(&posts, 1)
		}
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()

	c, err := NewClient(Config{BaseURL: srv.URL, AgentID: "00000000-0000-4000-8000-000000000001"})
	if err != nil {
		t.Fatal(err)
	}
	svc := NewAnalyticsService(c)

	// Buffer one of each while offline and allowed, as a run did before the
	// user turned telemetry off.
	var allowed atomic.Bool
	allowed.Store(true)
	svc.SetSendGate(allowed.Load)
	c.setMode(ModeOffline)
	svc.PushPipelineRun(context.Background(), ExecutionHistoryRunRecord{IssueNumber: 1})
	svc.EmitPipelineEvent(context.Background(), PipelineEvent{IssueNumber: 1, EventType: "stage_started", Stage: "feature-dev", Timestamp: time.Now(), SchemaVersion: "1"})
	svc.Ingest(context.Background(), "", 1, []AnalyticsEvent{{Type: "pipeline_execution", Timestamp: time.Now()}})
	waitFor(t, "one buffered item of each kind", func() bool {
		return svc.RunQueueCount() == 1 && svc.EventQueueCount() == 1 && svc.BufferedCount() == 1
	})

	// Telemetry off: nothing new is sent or buffered, and the flush drops the
	// buffered items instead of sending them.
	allowed.Store(false)
	c.setMode(ModeOnline)
	svc.PushPipelineRun(context.Background(), ExecutionHistoryRunRecord{IssueNumber: 2})
	svc.EmitPipelineEvent(context.Background(), PipelineEvent{IssueNumber: 2, EventType: "stage_started", Stage: "feature-dev", Timestamp: time.Now(), SchemaVersion: "1"})
	svc.Ingest(context.Background(), "", 2, []AnalyticsEvent{{Type: "pipeline_execution", Timestamp: time.Now()}})
	svc.SyncQueue(context.Background(), QueueSyncPayload{MachineID: "m", Origin: "local_cli", Items: []QueueSyncItem{{IssueNumber: 2, Title: "t", Status: "pending"}}})
	if res := svc.SyncTelemetry(context.Background(), []state.V2RunRecord{{IssueNumber: 2}}, "o/r"); res.Synced != 0 || len(res.Errors) == 0 {
		t.Errorf("SyncTelemetry with the gate closed = %+v, want nothing synced and a reason", res)
	}
	if n := svc.FlushBuffered(context.Background()); n != 0 {
		t.Errorf("FlushBuffered with the gate closed sent %d items, want 0", n)
	}
	if svc.RunQueueCount()+svc.EventQueueCount()+svc.BufferedCount() != 0 {
		t.Errorf("buffers after a closed-gate flush: runs %d events %d batches %d, want all dropped",
			svc.RunQueueCount(), svc.EventQueueCount(), svc.BufferedCount())
	}
	time.Sleep(100 * time.Millisecond)
	if n := atomic.LoadInt32(&posts); n != 0 {
		t.Fatalf("%d requests reached the platform with the gate closed, want 0", n)
	}

	// Telemetry on again: writes go through.
	allowed.Store(true)
	svc.PushPipelineRun(context.Background(), ExecutionHistoryRunRecord{IssueNumber: 3})
	waitFor(t, "one post", func() bool { return atomic.LoadInt32(&posts) == 1 })
}

// TestAnalyticsService_NoGateAllows pins the default: a service nobody gated
// (the CLI's explicit `pipeline backfill`) sends.
func TestAnalyticsService_NoGateAllows(t *testing.T) {
	var posts int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&posts, 1)
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()
	c, err := NewClient(Config{BaseURL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	c.setMode(ModeOnline)
	svc := NewAnalyticsService(c)
	svc.PushPipelineRun(context.Background(), ExecutionHistoryRunRecord{IssueNumber: 1})
	waitFor(t, "one post", func() bool { return atomic.LoadInt32(&posts) == 1 })
}

// TestAnalyticsService_RunGate pins the pipeline-run stream on top of the send
// gate: with it closed a run record is neither posted nor buffered, buffered
// records are dropped at the next flush, and the run history sync refuses,
// while the other kinds of write still go through.
func TestAnalyticsService_RunGate(t *testing.T) {
	var runPosts, otherPosts int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost || r.Method == http.MethodPut {
			if r.URL.Path == "/v1/telemetry/pipeline-run" {
				atomic.AddInt32(&runPosts, 1)
			} else {
				atomic.AddInt32(&otherPosts, 1)
			}
		}
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()

	c, err := NewClient(Config{BaseURL: srv.URL, AgentID: "00000000-0000-4000-8000-000000000001"})
	if err != nil {
		t.Fatal(err)
	}
	svc := NewAnalyticsService(c)
	svc.SetSendGate(func() bool { return true })
	var runs atomic.Bool
	runs.Store(true)
	svc.SetRunGate(runs.Load)

	// A record buffered offline while the stream was on.
	c.setMode(ModeOffline)
	svc.PushPipelineRun(context.Background(), ExecutionHistoryRunRecord{IssueNumber: 1})
	waitFor(t, "one buffered run record", func() bool { return svc.RunQueueCount() == 1 })

	// The stream off: no new record, the buffered one dropped, the sync
	// refused; a queue snapshot still goes.
	runs.Store(false)
	c.setMode(ModeOnline)
	svc.PushPipelineRun(context.Background(), ExecutionHistoryRunRecord{IssueNumber: 2})
	if res := svc.SyncTelemetry(context.Background(), []state.V2RunRecord{{IssueNumber: 2}}, "o/r"); res.Synced != 0 || len(res.Errors) == 0 {
		t.Errorf("SyncTelemetry with the stream off = %+v, want nothing synced and a reason", res)
	}
	svc.FlushBuffered(context.Background())
	if n := svc.RunQueueCount(); n != 0 {
		t.Errorf("%d run records still buffered after a flush with the stream off, want 0", n)
	}
	svc.SyncQueue(context.Background(), QueueSyncPayload{MachineID: "m", Origin: "local_cli", Items: []QueueSyncItem{{IssueNumber: 2, Title: "t", Status: "pending"}}})
	waitFor(t, "the queue snapshot", func() bool { return atomic.LoadInt32(&otherPosts) == 1 })
	time.Sleep(100 * time.Millisecond)
	if n := atomic.LoadInt32(&runPosts); n != 0 {
		t.Fatalf("%d run records reached the platform with the stream off, want 0", n)
	}

	// The stream on again: records go through.
	runs.Store(true)
	svc.PushPipelineRun(context.Background(), ExecutionHistoryRunRecord{IssueNumber: 3})
	waitFor(t, "one run record", func() bool { return atomic.LoadInt32(&runPosts) == 1 })
}
