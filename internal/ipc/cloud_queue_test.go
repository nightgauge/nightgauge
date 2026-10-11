package ipc

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/cloudqueue"
	"github.com/nightgauge/nightgauge/internal/orchestrator"
	"github.com/nightgauge/nightgauge/internal/platform"
	"github.com/nightgauge/nightgauge/pkg/types"
)

// The local queue a queue item added in the cloud goes to (#2440).

func cloudQueueServer(t *testing.T, issue *types.Issue, readErr error) (*Server, *orchestrator.Scheduler, *int) {
	t.Helper()
	sched := orchestrator.NewScheduler(nil, orchestrator.SchedulerConfig{
		WorkspaceRoot: t.TempDir(),
		ExcludeLabels: []string{"owner-action"},
	})
	s := NewServer(nil, WithScheduler(sched))
	reads := 0
	s.readCloudIssue = func(_ context.Context, owner, repo string, number int) (*types.Issue, error) {
		reads++
		if owner != "acme" || repo != "web" || number != 637 {
			t.Errorf("read %s/%s#%d, want acme/web#637", owner, repo, number)
		}
		return issue, readErr
	}
	return s, sched, &reads
}

var cloudItem = cloudqueue.Item{Owner: "acme", Repo: "web", IssueNumber: 637, Priority: "high"}

func openIssue(labels ...string) *types.Issue {
	return &types.Issue{Number: 637, Title: "Make the queue reach the agent", State: "OPEN", Labels: labels}
}

func TestAddCloudItem_QueuesTheIssueWithItsTitleLabelsAndPriority(t *testing.T) {
	s, sched, _ := cloudQueueServer(t, openIssue("type:bug"), nil)

	got, err := s.AddCloudItem(context.Background(), cloudItem)
	if err != nil || got != cloudqueue.Queued {
		t.Fatalf("AddCloudItem = %v, %v; want Queued", got, err)
	}
	items := sched.GetState().Items
	if len(items) != 1 {
		t.Fatalf("queue = %+v, want one item", items)
	}
	it := items[0]
	if it.Repo != "acme/web" || it.IssueNumber != 637 || it.Title != "Make the queue reach the agent" ||
		it.RequestedPriority != "high" || it.Status != "pending" || len(it.Labels) != 1 {
		t.Errorf("item = %+v", it)
	}
}

func TestAddCloudItem_AlreadyQueuedOrRunningIsNotReadOrQueued(t *testing.T) {
	s, sched, reads := cloudQueueServer(t, openIssue(), nil)
	sched.QueueAddItem(orchestrator.QueueItem{Repo: "Acme/Web", IssueNumber: 637})
	if got, err := s.AddCloudItem(context.Background(), cloudItem); err != nil || got != cloudqueue.AlreadyQueued {
		t.Errorf("queued already: AddCloudItem = %v, %v; want AlreadyQueued", got, err)
	}

	s2, sched2, reads2 := cloudQueueServer(t, openIssue(), nil)
	installRun(t, s2, "acme/web", 637, time.Now().Add(-2*livenessWindow))
	if got, err := s2.AddCloudItem(context.Background(), cloudItem); err != nil || got != cloudqueue.AlreadyRunning {
		t.Errorf("running (stale lease): AddCloudItem = %v, %v; want AlreadyRunning", got, err)
	}
	if *reads != 0 || *reads2 != 0 || len(sched2.GetState().Items) != 0 {
		t.Errorf("read the issue %d/%d times, queued %d; want none", *reads, *reads2, len(sched2.GetState().Items))
	}
}

// Every refusal is a *cloudqueue.Refusal naming the issue, and queues nothing.
func TestAddCloudItem_Refusals(t *testing.T) {
	for _, tc := range []struct {
		name    string
		issue   *types.Issue
		readErr error
		want    string
	}{
		{"unreadable", nil, errors.New("Could not resolve to an Issue"), "issue-unreadable: "},
		{"closed", &types.Issue{Number: 637, State: "CLOSED"}, nil, "issue-closed: "},
		{"human-only label", openIssue("owner-action"), nil, `human-only-label: acme/web#637 carries "owner-action"`},
		{"truncated labels", &types.Issue{Number: 637, State: "OPEN", LabelsTruncated: true}, nil, "labels-unreadable: "},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, sched, _ := cloudQueueServer(t, tc.issue, tc.readErr)
			_, err := s.AddCloudItem(context.Background(), cloudItem)
			var refusal *cloudqueue.Refusal
			if !errors.As(err, &refusal) || !strings.HasPrefix(refusal.Reason, tc.want) {
				t.Fatalf("err = %v, want a refusal starting %q", err, tc.want)
			}
			if strings.Contains(refusal.Reason, "resolve") {
				t.Errorf("refusal %q carries the GitHub error", refusal.Reason)
			}
			if n := len(sched.GetState().Items); n != 0 {
				t.Errorf("queued %d items after a refusal", n)
			}
		})
	}

	s := NewServer(nil)
	if _, err := s.AddCloudItem(context.Background(), cloudItem); err == nil {
		t.Error("a daemon with no queue accepted an item")
	}
}

func TestWithdrawCloudItem_OnlyAWaitingItem(t *testing.T) {
	s, sched, _ := cloudQueueServer(t, openIssue(), nil)
	if _, err := s.AddCloudItem(context.Background(), cloudItem); err != nil {
		t.Fatal(err)
	}
	if !s.WithdrawCloudItem(cloudItem) || len(sched.GetState().Items) != 0 {
		t.Error("a waiting item was not withdrawn")
	}
	sched.QueueAddItem(orchestrator.QueueItem{Repo: "acme/web", IssueNumber: 637, Status: "processing"})
	if s.WithdrawCloudItem(cloudItem) {
		t.Error("withdrew an item a dispatch had taken")
	}
}

// agent.queueAdd hands the extension's copy to the same handler as the
// daemon's own stream, under the agent the extension names.
func TestAgentQueueAdd_HandsTheCommandToTheHandler(t *testing.T) {
	s := NewServer(nil)
	params, _ := json.Marshal(AgentQueueAddParams{AgentID: "agent-ext", CommandID: "cmd-1", Payload: json.RawMessage(`{"owner":"acme"}`)})
	if _, err := s.handleAgentQueueAdd(context.Background(), params); err == nil {
		t.Error("accepted with no handler set")
	}

	var got []string
	s.SetQueueAddHandler(func(_ context.Context, agentID string, cmd platform.PendingCommand) {
		got = append(got, agentID+" "+cmd.ID+" "+cmd.Type+" "+string(cmd.Payload))
	})
	res, err := s.handleAgentQueueAdd(context.Background(), params)
	if err != nil || res != (AgentQueueAddResult{Accepted: true}) {
		t.Fatalf("agent.queueAdd = %v, %v", res, err)
	}
	if len(got) != 1 || got[0] != `agent-ext cmd-1 queue_add {"owner":"acme"}` {
		t.Errorf("handler got %v", got)
	}

	bad, _ := json.Marshal(AgentQueueAddParams{CommandID: "cmd-2"})
	if _, err := s.handleAgentQueueAdd(context.Background(), bad); err == nil || len(got) != 1 {
		t.Error("a hand-over with no agent id reached the handler")
	}
	if _, ok := s.methods["agent.queueAdd"]; !ok {
		t.Error("agent.queueAdd is not registered")
	}
}
