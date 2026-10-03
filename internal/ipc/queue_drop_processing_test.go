package ipc

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/orchestrator"
)

// queue.dropProcessing (#2396) is what a window reload or close does to the
// queue: the item a dispatch took goes with its run, every waiting item stays,
// and the answer is {"dropped": n}.
func TestQueueDropProcessing_Handler(t *testing.T) {
	sched := orchestrator.NewScheduler(nil, orchestrator.SchedulerConfig{})
	s := NewServer(nil, WithScheduler(sched))
	s.writer = &bytes.Buffer{}

	sched.QueueAddItem(
		orchestrator.QueueItem{Repo: "o/a", IssueNumber: 1, Title: "dispatched"},
		orchestrator.QueueItem{Repo: "o/a", IssueNumber: 2, Title: "triggered", RemoteRunID: "run-2"},
	)
	if taken := sched.DequeueIndependent(context.Background(), 1, nil); len(taken) != 1 || taken[0].IssueNumber != 1 {
		t.Fatalf("dequeue = %+v, want #1", taken)
	}

	out, err := s.methods["queue.dropProcessing"](context.Background(), nil)
	if err != nil {
		t.Fatalf("queue.dropProcessing: %v", err)
	}
	if encoded, _ := json.Marshal(out); string(encoded) != `{"dropped":1,"kept":0}` {
		t.Errorf("result = %s, want {\"dropped\":1,\"kept\":0}", encoded)
	}
	items := sched.GetState().Items
	if len(items) != 1 || items[0].IssueNumber != 2 || items[0].RemoteRunID != "run-2" || items[0].Status != "pending" {
		t.Errorf("queue = %+v, want only the waiting #2 with its platform run", items)
	}
}

func TestQueueDropProcessing_HandlerWithoutAScheduler(t *testing.T) {
	s := NewServer(nil)
	s.writer = &bytes.Buffer{}
	if _, err := s.methods["queue.dropProcessing"](context.Background(), nil); err == nil || !strings.Contains(err.Error(), errSchedulerNotConfigured) {
		t.Errorf("err = %v, want %q", err, errSchedulerNotConfigured)
	}
}

// The window names the dispatches it had not begun to start, and the dequeues
// whose answer it never read, by the dispatch token it passed to
// queue.dequeueIndependent: their items go back to waiting (#2396).
func TestQueueDropProcessing_HandlerHandsBack(t *testing.T) {
	sched := orchestrator.NewScheduler(nil, orchestrator.SchedulerConfig{})
	s := NewServer(nil, WithScheduler(sched))
	s.writer = &bytes.Buffer{}

	sched.QueueAddItem(
		orchestrator.QueueItem{Repo: "o/a", IssueNumber: 1, Title: "waits its turn"},
		orchestrator.QueueItem{Repo: "o/b", IssueNumber: 2, Title: "answer unread"},
	)
	dequeue := func(params string) {
		t.Helper()
		out, err := s.methods["queue.dequeueIndependent"](context.Background(), json.RawMessage(params))
		if err != nil {
			t.Fatalf("queue.dequeueIndependent: %v", err)
		}
		if items, _ := out.([]orchestrator.QueueItem); len(items) != 1 {
			t.Fatalf("dequeue %s = %+v, want one item", params, out)
		}
	}
	dequeue(`{"maxSlots":1,"runningItems":[],"dispatch":"fill-1"}`)
	dequeue(`{"maxSlots":1,"runningItems":[{"repo":"o/a","number":1}],"dispatch":"fill-2"}`)

	out, err := s.methods["queue.dropProcessing"](context.Background(), json.RawMessage(
		`{"handBack":[{"repo":"o/a","issueNumber":1,"remoteRunId":"run-1","remoteRunAttached":true}],"unanswered":["fill-2"]}`))
	if err != nil {
		t.Fatalf("queue.dropProcessing: %v", err)
	}
	if encoded, _ := json.Marshal(out); string(encoded) != `{"dropped":0,"kept":2}` {
		t.Errorf("result = %s, want {\"dropped\":0,\"kept\":2}", encoded)
	}
	items := sched.GetState().Items
	if len(items) != 2 || items[0].Status != "pending" || items[0].RemoteRunID != "run-1" || !items[0].RemoteRunAttached ||
		items[1].Status != "pending" || items[1].Repo != "o/b" {
		t.Errorf("queue = %+v, want both items waiting again, #1 serving run-1", items)
	}

	// The window is gone: the daemon dispatches nothing more.
	out, err = s.methods["queue.dequeueIndependent"](context.Background(), json.RawMessage(`{"maxSlots":2,"runningItems":[]}`))
	if err != nil {
		t.Fatalf("queue.dequeueIndependent after the drop: %v", err)
	}
	if items, _ := out.([]orchestrator.QueueItem); len(items) != 0 {
		t.Errorf("dequeue after the drop = %+v, want nothing", out)
	}

	if _, err := s.methods["queue.dropProcessing"](context.Background(), json.RawMessage(`{"handBack":"x"}`)); err == nil {
		t.Error("malformed params were accepted")
	}
}
