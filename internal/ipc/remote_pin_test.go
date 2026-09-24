package ipc

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/orchestrator"
	"github.com/nightgauge/nightgauge/internal/state"
)

// newRemotePinServer is a server with a real scheduler and a fake
// validateRemotePin that accepts exactly one pair.
func newRemotePinServer(t *testing.T) (*Server, *int) {
	t.Helper()
	s := NewServer(nil)
	s.SetScheduler(orchestrator.NewScheduler(nil, orchestrator.SchedulerConfig{WorkspaceRoot: t.TempDir()}))
	calls := 0
	s.validateRemotePin = func(adapter, model string) error {
		calls++
		if adapter == "opencode" && model == "lmstudio/qwen/qwen3.8-27b" {
			return nil
		}
		return errors.New("model " + model + " is not in this machine's opencode catalog")
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
	if r := res.(QueueValidatePinResult); r.OK || !strings.Contains(r.Reason, "not in this machine's opencode catalog") {
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
// the requested pair from the queue item, once.
func TestExtensionRunRecordsTheRequestedPin(t *testing.T) {
	s, _ := newRemotePinServer(t)
	if _, err := callPinMethod(t, s, "queue.add", QueueAddParams{Owner: "o", Repo: "r", IssueNumber: 7, Adapter: "opencode", Model: "lmstudio/qwen/qwen3.8-27b"}); err != nil {
		t.Fatal(err)
	}
	rt := state.NewRuntimeState("o/r", 7, "", "")
	s.seedRequestedPin(rt, "o/r", 7)
	rt.RecordStageAdapter(state.StageFeatureDev, "claude") // a later hop
	s.seedRequestedPin(rt, "o/r", 7)
	if a, m := rt.RequestedPin(); a != "opencode" || m != "lmstudio/qwen/qwen3.8-27b" {
		t.Fatalf("requested pin = %q %q", a, m)
	}
	other := state.NewRuntimeState("o/r", 8, "", "")
	s.seedRequestedPin(other, "o/r", 8)
	if a, _ := other.RequestedPin(); a != "" {
		t.Fatalf("an unqueued issue got pin %q", a)
	}
}
