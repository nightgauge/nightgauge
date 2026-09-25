package ipc

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/nightgauge/nightgauge/internal/orchestrator"
	"github.com/nightgauge/nightgauge/internal/state"
)

// newRemotePinServer is a server with a real scheduler and a fake
// validateRemotePin that accepts exactly one pair.
func newRemotePinServer(t *testing.T) (*Server, *int32) {
	t.Helper()
	s := NewServer(nil)
	s.SetScheduler(orchestrator.NewScheduler(nil, orchestrator.SchedulerConfig{WorkspaceRoot: t.TempDir()}))
	var calls int32
	s.validateRemotePin = func(adapter, model string, deps orchestrator.RemotePinDeps) error {
		atomic.AddInt32(&calls, 1)
		if err := orchestrator.ValidateRemotePinShape(adapter, model); err != nil {
			return err
		}
		if adapter == "opencode" && model == "lmstudio/qwen/qwen3.8-27b" {
			return nil
		}
		return &orchestrator.RemotePinRefusal{
			Public: "model-not-in-catalog",
			Reason: "model " + model + " is not in this machine's opencode catalog (see /home/someone/.config)",
		}
	}
	return s, &calls
}

func callPinMethod(t *testing.T, s *Server, method string, params any) (any, error) {
	t.Helper()
	raw, _ := json.Marshal(params)
	return s.methods[method](context.Background(), raw)
}

func TestQueueValidatePin(t *testing.T) {
	s, calls := newRemotePinServer(t)
	res, err := callPinMethod(t, s, "queue.validatePin", QueueValidatePinParams{Adapter: "opencode", Model: "lmstudio/qwen/qwen3.8-27b"})
	if err != nil || !res.(QueueValidatePinResult).OK {
		t.Fatalf("valid pair: %v %v", res, err)
	}
	res, err = callPinMethod(t, s, "queue.validatePin", QueueValidatePinParams{Adapter: "opencode", Model: "lmstudio/other"})
	if err != nil {
		t.Fatalf("a refusal is a result, not an error: %v", err)
	}
	// The ack detail is the public category; the full reason stays local.
	if r := res.(QueueValidatePinResult); r.OK || r.Reason != "model-not-in-catalog" {
		t.Fatalf("refused pair = %+v", r)
	}
	if *calls != 2 {
		t.Errorf("validator called %d times, want 2", *calls)
	}
}

func TestQueueAddCarriesTheRequestedPin(t *testing.T) {
	s, _ := newRemotePinServer(t)
	if _, err := callPinMethod(t, s, "queue.add", QueueAddParams{
		Owner: "o", Repo: "r", IssueNumber: 7, Adapter: "opencode", Model: "lmstudio/qwen/qwen3.8-27b",
	}); err != nil {
		t.Fatalf("queue.add: %v", err)
	}
	a, m, queued := s.scheduler.QueueItemRequestedPin("o/r", 7)
	if !queued || a != "opencode" || m != "lmstudio/qwen/qwen3.8-27b" {
		t.Fatalf("queued item pin = %q %q queued=%v", a, m, queued)
	}

	// Without the fields the item is queued exactly as before: no pin.
	if _, err := callPinMethod(t, s, "queue.add", QueueAddParams{Owner: "o", Repo: "r", IssueNumber: 8}); err != nil {
		t.Fatalf("queue.add: %v", err)
	}
	if a, m, queued := s.scheduler.QueueItemRequestedPin("o/r", 8); !queued || a != "" || m != "" {
		t.Fatalf("unpinned item = %q %q queued=%v", a, m, queued)
	}
}

func TestQueueAddRefusesABadPinAndQueuesNothing(t *testing.T) {
	for _, tc := range []struct {
		name string
		p    QueueAddParams
		want string
	}{
		{"leading dash", QueueAddParams{Owner: "o", Repo: "r", IssueNumber: 9, Adapter: "opencode", Model: "--auto"}, "must not start with '-'"},
		{"unknown adapter", QueueAddParams{Owner: "o", Repo: "r", IssueNumber: 9, Adapter: "nope"}, "not one this machine can run"},
		{"model without adapter", QueueAddParams{Owner: "o", Repo: "r", IssueNumber: 9, Model: "lmstudio/x"}, "without an adapter"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := newRemotePinServer(t)
			_, err := callPinMethod(t, s, "queue.add", tc.p)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v, want %q", err, tc.want)
			}
			if items := s.scheduler.GetState().Items; len(items) != 0 {
				t.Fatalf("queue holds %d items after a refusal", len(items))
			}
		})
	}
}

// A pin for an issue already queued is refused, never silently dropped by
// QueueAddItem's duplicate skip.
func TestQueueAddRefusesAPinForAnAlreadyQueuedIssue(t *testing.T) {
	s, _ := newRemotePinServer(t)
	if _, err := callPinMethod(t, s, "queue.add", QueueAddParams{Owner: "o", Repo: "r", IssueNumber: 7}); err != nil {
		t.Fatal(err)
	}
	_, err := callPinMethod(t, s, "queue.add", QueueAddParams{Owner: "o", Repo: "r", IssueNumber: 7, Adapter: "opencode", Model: "lmstudio/qwen/qwen3.8-27b"})
	if err == nil || !strings.Contains(err.Error(), "already queued") {
		t.Fatalf("got %v, want an already-queued refusal", err)
	}
}

// The extension runs a triggered issue's stages itself; its run record learns
// the requested pair from the queue item, once, and only the run that serves
// that trigger (its remoteRunId matches the item's) — never another run of the
// same issue.
func TestExtensionRunRecordsTheRequestedPin(t *testing.T) {
	s, _ := newRemotePinServer(t)
	if _, err := callPinMethod(t, s, "queue.add", QueueAddParams{Owner: "o", Repo: "r", IssueNumber: 7, Adapter: "opencode", Model: "lmstudio/qwen/qwen3.8-27b", RemoteRunID: "remote-run-7"}); err != nil {
		t.Fatal(err)
	}
	rt := state.NewRuntimeState("o/r", 7, "", "")
	s.seedRequestedPin(rt, "o/r", 7, "")
	if a, _ := rt.RequestedPin(); a != "" {
		t.Fatalf("a run that serves no trigger inherited the pin %q", a)
	}
	s.seedRequestedPin(rt, "o/r", 7, "remote-run-other")
	if a, _ := rt.RequestedPin(); a != "" {
		t.Fatalf("a run serving another trigger inherited the pin %q", a)
	}
	s.seedRequestedPin(rt, "o/r", 7, "remote-run-7")
	rt.RecordStageAdapter(state.StageFeatureDev, "claude") // a later hop
	s.seedRequestedPin(rt, "o/r", 7, "remote-run-7")
	if a, m := rt.RequestedPin(); a != "opencode" || m != "lmstudio/qwen/qwen3.8-27b" {
		t.Fatalf("requested pin = %q %q", a, m)
	}
	other := state.NewRuntimeState("o/r", 8, "", "")
	s.seedRequestedPin(other, "o/r", 8, "remote-run-7")
	if a, _ := other.RequestedPin(); a != "" {
		t.Fatalf("an unqueued issue got pin %q", a)
	}
}

// queue.add runs the full validation, not only the shape half: a pair the
// machine cannot serve is refused and nothing is queued.
func TestQueueAddRunsTheFullValidation(t *testing.T) {
	s, calls := newRemotePinServer(t)
	_, err := callPinMethod(t, s, "queue.add", QueueAddParams{Owner: "o", Repo: "r", IssueNumber: 9, Adapter: "opencode", Model: "lmstudio/other"})
	if err == nil || !strings.Contains(err.Error(), "not in this machine's opencode catalog") {
		t.Fatalf("got %v, want the full validator's refusal", err)
	}
	if *calls != 1 {
		t.Errorf("validator called %d times, want 1", *calls)
	}
	if items := s.scheduler.GetState().Items; len(items) != 0 {
		t.Fatalf("queue holds %d items after a refusal", len(items))
	}
}

// queue.validatePin refuses a pin for an issue that is already queued, so the
// trigger is acked rejected before an accepted ack could promise the pin.
func TestQueueValidatePinRefusesAnAlreadyQueuedIssue(t *testing.T) {
	s, calls := newRemotePinServer(t)
	if _, err := callPinMethod(t, s, "queue.add", QueueAddParams{Owner: "o", Repo: "r", IssueNumber: 7}); err != nil {
		t.Fatal(err)
	}
	res, err := callPinMethod(t, s, "queue.validatePin", QueueValidatePinParams{
		Adapter: "opencode", Model: "lmstudio/qwen/qwen3.8-27b", Owner: "o", Repo: "r", IssueNumber: 7,
	})
	if err != nil {
		t.Fatal(err)
	}
	if r := res.(QueueValidatePinResult); r.OK || r.Reason != "already-queued" {
		t.Fatalf("result = %+v, want already-queued", r)
	}
	if *calls != 0 {
		t.Errorf("the validator ran %d times for an already-queued issue", *calls)
	}
	res, _ = callPinMethod(t, s, "queue.validatePin", QueueValidatePinParams{
		Adapter: "opencode", Model: "lmstudio/qwen/qwen3.8-27b", Owner: "o", Repo: "r", IssueNumber: 8,
	})
	if r := res.(QueueValidatePinResult); !r.OK {
		t.Fatalf("an unqueued issue was refused: %+v", r)
	}
}

// Two pinned queue.add calls for the same issue racing each other queue
// exactly one item: the check and the insert are one locked step.
func TestQueueAddPinnedCheckAndInsertIsAtomic(t *testing.T) {
	s, _ := newRemotePinServer(t)
	const n = 32
	var wg sync.WaitGroup
	var mu sync.Mutex
	ok := 0
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := callPinMethod(t, s, "queue.add", QueueAddParams{Owner: "o", Repo: "r", IssueNumber: 7, Adapter: "opencode", Model: "lmstudio/qwen/qwen3.8-27b"}); err == nil {
				mu.Lock()
				ok++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if ok != 1 {
		t.Fatalf("%d concurrent pinned queue.add calls succeeded, want exactly 1", ok)
	}
	if items := s.scheduler.GetState().Items; len(items) != 1 {
		t.Fatalf("queue holds %d items, want 1", len(items))
	}
}
