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
	if encoded, _ := json.Marshal(out); string(encoded) != `{"dropped":1}` {
		t.Errorf("result = %s, want {\"dropped\":1}", encoded)
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
