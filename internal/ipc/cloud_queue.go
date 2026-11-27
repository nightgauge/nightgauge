package ipc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/nightgauge/nightgauge/internal/cloudqueue"
	gh "github.com/nightgauge/nightgauge/internal/github"
	"github.com/nightgauge/nightgauge/internal/orchestrator"
	"github.com/nightgauge/nightgauge/internal/platform"
	ibqueue "github.com/nightgauge/nightgauge/internal/queue"
	"github.com/nightgauge/nightgauge/pkg/types"
)

// A queue item added in the cloud (#2440, ADR-018). The hosted service
// carries it to an agent covering its repository as a `queue_add` command;
// internal/cloudqueue decides and acknowledges it, and this file is the local
// queue it adds to. *Server implements cloudqueue.Queue.

var _ cloudqueue.Queue = (*Server)(nil)

// SetQueueAddHandler sets the handler agent.queueAdd hands a `queue_add` to:
// the same one the daemon's own command stream uses, so one claim covers a
// command whichever stream delivered it. nil refuses the hand-over.
func (s *Server) SetQueueAddHandler(h platform.AgentCommandHandler) {
	if h == nil {
		s.queueAddHandler.Store(nil)
		return
	}
	s.queueAddHandler.Store(&h)
}

// handleAgentQueueAdd takes a `queue_add` the extension received on its own
// agent's command stream. The extension's agent and the daemon's are two
// agents, and the hosted service routes a `queue_add` to either one that
// covers the repository, so the extension hands its copy here instead of
// queueing it itself. The handler acknowledges it under p.AgentID.
func (s *Server) handleAgentQueueAdd(ctx context.Context, raw json.RawMessage) (interface{}, error) {
	var p AgentQueueAddParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, fmt.Errorf("agent.queueAdd: parse params: %w", err)
	}
	if p.AgentID == "" || p.CommandID == "" {
		return nil, errors.New("agent.queueAdd: agentId and commandId are required")
	}
	h := s.queueAddHandler.Load()
	if h == nil {
		return nil, errors.New("agent.queueAdd: this daemon takes no queue items from the cloud (no platform configured)")
	}
	(*h)(ctx, p.AgentID, platform.PendingCommand{ID: p.CommandID, Type: platform.QueueAddCommandType, Payload: p.Payload})
	return AgentQueueAddResult{Accepted: true}, nil
}

// AddCloudItem adds an issue a `queue_add` carried to this daemon's local
// queue, on the same terms as every other route: an issue with a human-only
// label (autonomous.exclude_labels) is refused, as `queue.add` refuses it, and
// an epic is queued as its open sub-issues. The issue is read from GitHub
// first, for its title, labels and state; one that cannot be read is refused
// rather than queued unchecked.
func (s *Server) AddCloudItem(ctx context.Context, item cloudqueue.Item) (cloudqueue.Outcome, error) {
	if s.scheduler == nil {
		return 0, cloudqueue.Refuse("cannot-queue: this agent has no queue running")
	}
	repo := item.FullName()
	if s.scheduler.QueueHas(repo, item.IssueNumber) {
		return cloudqueue.AlreadyQueued, nil
	}
	if s.issueRunningHere(repo, item.IssueNumber) {
		return cloudqueue.AlreadyRunning, nil
	}
	issue, err := s.readIssueForCloudQueue(ctx, item)
	if err != nil || issue == nil {
		log.Printf("[nightgauge] queue_add: reading %s failed: %v", item, err)
		return 0, cloudqueue.Refuse("issue-unreadable: this agent could not read %s from GitHub; nothing was queued", item)
	}
	if issue.State != "" && !strings.EqualFold(issue.State, "OPEN") {
		return 0, cloudqueue.Refuse("issue-closed: %s is closed; nothing was queued", item)
	}
	if issue.LabelsTruncated {
		return 0, cloudqueue.Refuse("labels-unreadable: %s has more labels than one read returns, so its human-only labels cannot be checked; nothing was queued", item)
	}
	if label, excluded := orchestrator.ExcludedLabelMatch(issue.Labels, s.scheduler.ExcludeLabels()); excluded {
		return 0, cloudqueue.Refuse("human-only-label: %s carries %q (autonomous.exclude_labels); nothing was queued", item, label)
	}
	if ibqueue.IsEpic(issue.Labels) {
		if err := s.scheduler.EnqueueEpic(ctx, item.Owner, item.Repo, item.IssueNumber, issue.Title, issue.Labels, nil, ""); err != nil {
			return 0, err
		}
		return cloudqueue.QueuedEpic, nil
	}
	// Adding persists the queue, which starts the queue sync that lists the
	// item as this machine's row.
	if !s.scheduler.QueueAddIfAbsent(orchestrator.QueueItem{
		Repo:              repo,
		IssueNumber:       item.IssueNumber,
		Title:             issue.Title,
		Labels:            issue.Labels,
		RequestedPriority: item.Priority,
	}) {
		return cloudqueue.AlreadyQueued, nil
	}
	return cloudqueue.Queued, nil
}

// WithdrawCloudItem takes back an item AddCloudItem queued while it still
// waits for a slot.
func (s *Server) WithdrawCloudItem(item cloudqueue.Item) bool {
	if s.scheduler == nil {
		return false
	}
	return s.scheduler.QueueWithdrawWaiting(item.FullName(), item.IssueNumber)
}

// issueRunningHere reports whether a run of repo's issue is in this daemon's
// run registry: every run, queued, manual or autonomous, reports to it. A run
// whose lease is stale still counts, so an issue is never queued twice over a
// run that is only quiet.
func (s *Server) issueRunningHere(repo string, issueNumber int) bool {
	for _, r := range s.RunningPipelinesSnapshot(time.Now()).Runs {
		if r.IssueNumber == issueNumber && strings.EqualFold(r.Repo, repo) {
			return true
		}
	}
	return false
}

func (s *Server) readIssueForCloudQueue(ctx context.Context, item cloudqueue.Item) (*types.Issue, error) {
	if s.readCloudIssue != nil {
		return s.readCloudIssue(ctx, item.Owner, item.Repo, item.IssueNumber)
	}
	c, err := s.resolveClientForRequest(ctx, "", item.Owner, item.Repo)
	if err != nil {
		return nil, err
	}
	return gh.NewIssueService(c).GetIssueWithRelations(ctx, item.Owner, item.Repo, item.IssueNumber, gh.NoRelations)
}
