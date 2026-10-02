package ipc

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/orchestrator"
)

func removeRemoteRun(s *Server, params interface{}) (QueueRemoveRemoteRunResult, error) {
	raw, _ := json.Marshal(params)
	out, err := s.methods["queue.removeRemoteRun"](context.Background(), raw)
	if err != nil {
		return QueueRemoveRemoteRunResult{}, err
	}
	encoded, _ := json.Marshal(out)
	var result QueueRemoveRemoteRunResult
	if err := json.Unmarshal(encoded, &result); err != nil {
		return QueueRemoveRemoteRunResult{}, err
	}
	return result, nil
}

// queue.removeRemoteRun (#2344) takes only the cancelled run off the queue:
// the run's own waiting item is removed, the operator's item the run was
// attached to keeps its place without the run id, and nothing else moves. It
// answers {"removed": bool} and refuses a missing run id.
func TestQueueRemoveRemoteRun_Handler(t *testing.T) {
	sched := orchestrator.NewScheduler(nil, orchestrator.SchedulerConfig{})
	s := NewServer(nil, WithScheduler(sched))
	s.writer = &bytes.Buffer{}

	sched.QueueAddItem(
		orchestrator.QueueItem{Repo: "o/a", IssueNumber: 1, Title: "the run's own", RemoteRunID: "run-1"},
		orchestrator.QueueItem{Repo: "o/a", IssueNumber: 2, Title: "the operator's"},
		orchestrator.QueueItem{Repo: "o/b", IssueNumber: 1, Title: "same number, other repo"},
	)
	sched.QueueAddItem(orchestrator.QueueItem{Repo: "o/a", IssueNumber: 2, RemoteRunID: "run-2"})

	for _, params := range []interface{}{map[string]string{}, map[string]string{"remoteRunId": ""}} {
		if _, err := removeRemoteRun(s, params); err == nil || !strings.Contains(err.Error(), "remoteRunId is required") {
			t.Errorf("params %v: err = %v, want remoteRunId is required", params, err)
		}
	}
	if _, err := s.methods["queue.removeRemoteRun"](context.Background(), json.RawMessage(`{`)); err == nil {
		t.Error("malformed params were accepted")
	}

	for _, tc := range []struct {
		runID string
		want  bool
	}{{"run-1", true}, {"run-2", true}, {"run-1", false}, {"run-unknown", false}} {
		got, err := removeRemoteRun(s, map[string]string{"remoteRunId": tc.runID})
		if err != nil {
			t.Fatalf("%s: %v", tc.runID, err)
		}
		if got.Removed != tc.want {
			t.Errorf("%s: removed = %v, want %v", tc.runID, got.Removed, tc.want)
		}
	}
	raw, _ := s.methods["queue.removeRemoteRun"](context.Background(), json.RawMessage(`{"remoteRunId":"run-unknown"}`))
	if encoded, _ := json.Marshal(raw); string(encoded) != `{"removed":false}` {
		t.Errorf("result = %s, want {\"removed\":false}", encoded)
	}

	state := sched.GetState()
	if len(state.Items) != 2 {
		t.Fatalf("queue = %+v, want the operator's #2 and o/b#1", state.Items)
	}
	for _, item := range state.Items {
		if item.RemoteRunID != "" || item.RemoteRunAttached {
			t.Errorf("%s#%d still serves a run: %+v", item.Repo, item.IssueNumber, item)
		}
	}
}

// Without a scheduler the method fails rather than reporting nothing removed.
func TestQueueRemoveRemoteRun_HandlerWithoutScheduler(t *testing.T) {
	s := NewServer(nil)
	s.writer = &bytes.Buffer{}
	if _, err := removeRemoteRun(s, map[string]string{"remoteRunId": "run-1"}); err == nil {
		t.Fatal("no scheduler: err = nil, want an error")
	}
}
