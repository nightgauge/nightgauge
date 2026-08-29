package ipc

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/orchestrator"
)

// callQueueAdd invokes the registered queue.add handler in process.
func callQueueAdd(t *testing.T, s *Server, params map[string]interface{}) (interface{}, error) {
	t.Helper()
	raw, err := json.Marshal(params)
	if err != nil {
		t.Fatalf("marshal params: %v", err)
	}
	handler, ok := s.methods["queue.add"]
	if !ok {
		t.Fatal("queue.add is not registered")
	}
	return handler(t.Context(), raw)
}

// TestQueueAddOverIPC_RefusesHumanOnlyLabel closes the CLI/IPC disagreement in
// #1146: `nightgauge queue add` refused an issue carrying an
// autonomous.exclude_labels label, while the same operation over IPC (the
// dashboard trigger, manual "Add to Queue", retry and remote commands all land
// here) accepted it silently. Both now resolve the set from
// Scheduler.ExcludeLabels().
func TestQueueAddOverIPC_RefusesHumanOnlyLabel(t *testing.T) {
	root := t.TempDir()
	sched := orchestrator.NewScheduler(nil, orchestrator.SchedulerConfig{
		WorkspaceRoot: root,
		ExcludeLabels: []string{"owner-action"},
	})
	s := NewServer(nil, WithScheduler(sched))

	_, err := callQueueAdd(t, s, map[string]interface{}{
		"owner":       "Org",
		"repo":        "repo",
		"issueNumber": 401,
		"title":       "Rotate the leaked token",
		"labels":      []string{"owner-action"},
	})
	if err == nil {
		t.Fatal("queue.add accepted an issue carrying a human-only label; want a refusal")
	}
	if !strings.Contains(err.Error(), "owner-action") {
		t.Errorf("error %q does not name the matched label", err)
	}
	if got := sched.QueueList(); len(got) != 0 {
		t.Fatalf("queue has %d items after the refusal, want 0: %+v", len(got), got)
	}
}

// TestQueueAddOverIPC_AcceptsNonExcludedLabel proves the door check is not
// over-broad — an ordinary issue still queues.
func TestQueueAddOverIPC_AcceptsNonExcludedLabel(t *testing.T) {
	root := t.TempDir()
	sched := orchestrator.NewScheduler(nil, orchestrator.SchedulerConfig{
		WorkspaceRoot: root,
		ExcludeLabels: []string{"owner-action"},
	})
	s := NewServer(nil, WithScheduler(sched))

	if _, err := callQueueAdd(t, s, map[string]interface{}{
		"owner":       "Org",
		"repo":        "repo",
		"issueNumber": 402,
		"title":       "Ordinary work",
		"labels":      []string{"type:bug"},
	}); err != nil {
		t.Fatalf("queue.add refused an ordinary issue: %v", err)
	}
	got := sched.QueueList()
	if len(got) != 1 || got[0].IssueNumber != 402 {
		t.Fatalf("queue = %+v, want exactly #402", got)
	}
}

// TestQueueAddOverIPC_UnsetConfigStillRefusesTheDefault pins that the door
// check and the dequeue backstop resolve the label set from the same place:
// with no configured list, Scheduler.ExcludeLabels() yields the
// ["owner-action"] default rather than an empty set that would disable the
// check.
func TestQueueAddOverIPC_UnsetConfigStillRefusesTheDefault(t *testing.T) {
	root := t.TempDir()
	sched := orchestrator.NewScheduler(nil, orchestrator.SchedulerConfig{WorkspaceRoot: root})
	s := NewServer(nil, WithScheduler(sched))

	// Mixed case: matching is case-insensitive.
	if _, err := callQueueAdd(t, s, map[string]interface{}{
		"owner":       "Org",
		"repo":        "repo",
		"issueNumber": 401,
		"labels":      []string{"Owner-Action"},
	}); err == nil {
		t.Fatal("queue.add accepted a default-excluded label with no configured list; want a refusal")
	}
	if got := sched.QueueList(); len(got) != 0 {
		t.Fatalf("queue has %d items after the refusal, want 0", len(got))
	}
}
