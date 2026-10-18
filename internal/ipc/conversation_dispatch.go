package ipc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"regexp"
	"strconv"

	"github.com/nightgauge/nightgauge/internal/attention"
)

// A conversation turn that starts work dispatches a run to this host
// (#1894). Two things here bound it:
//
//   - The executor. The daemon hands a dispatched run's trigger to the
//     extension on its stdio transport, which runs the workspace's
//     pipelines (#2335). Without one the trigger would sit unacknowledged
//     until it expired, so a turn dispatches only once the extension said it
//     executes relayed triggers (agent.attachExecutor). A socket client
//     never receives the relay, so it cannot say so.
//   - The operator. Only a turn the host's operator posted dispatches a run,
//     on the operator's own credential chain. A teammate's request is raised
//     here as a Needs Attention card whose run starts only when the operator
//     approves it on this machine: a resolution the hosted service relays,
//     from the dashboard or a conversation, never starts it, because the
//     relay cannot tell the operator from any teammate who may act on cards.

// socketTransportKey marks a request that arrived on the socket transport.
type socketTransportKey struct{}

// withSocketTransport marks ctx as a socket request's.
func withSocketTransport(ctx context.Context) context.Context {
	return context.WithValue(ctx, socketTransportKey{}, true)
}

// onSocketTransport reports whether ctx is a socket request's.
func onSocketTransport(ctx context.Context) bool {
	v, _ := ctx.Value(socketTransportKey{}).(bool)
	return v
}

// handleAgentAttachExecutor records that the extension on the stdio
// transport executes the triggers this daemon relays to it.
func (s *Server) handleAgentAttachExecutor(ctx context.Context, _ json.RawMessage) (interface{}, error) {
	if onSocketTransport(ctx) {
		return nil, errors.New("agent.attachExecutor: only the extension on the daemon's stdio transport receives relayed triggers")
	}
	if !s.executorAttached.Swap(true) {
		log.Printf("[nightgauge] the extension executes relayed triggers: conversation turns may dispatch runs to this host")
	}
	return AgentAttachExecutorResult{Attached: true}, nil
}

// ExecutorAttached reports whether an extension that executes relayed
// triggers is attached to this daemon (#1894).
func (s *Server) ExecutorAttached() bool { return s.executorAttached.Load() }

// ProducerConversationRunProposal raises a teammate's request, made in a
// conversation, for a run on this host (#1894).
const ProducerConversationRunProposal = "conversation-run-proposal"

// Run-proposal card options.
const (
	runProposalStart   = "start"
	runProposalDismiss = "dismiss"
)

// ConversationRunProposal is a teammate's request for a run: ids only, never
// message text.
type ConversationRunProposal struct {
	Owner, Repo     string
	IssueNumber     int
	PosterAccountID string
	PosterLogin     string
	ConversationID  string
	TurnID          string
}

var (
	proposalOwner = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,38})$`)
	proposalRepo  = regexp.MustCompile(`^[A-Za-z0-9._-]{1,100}$`)
	proposalID    = regexp.MustCompile(`^[A-Za-z0-9_-]{1,100}$`)
)

// errRelayedRunProposal refuses a relayed resolution that would start a run
// a teammate proposed.
var errRelayedRunProposal = errors.New("a run a teammate proposed in a conversation starts only when this " +
	"host's operator approves it on this machine; a relayed resolution does not start it")

// RaiseConversationRunProposal raises p as a Needs Attention card for this
// host's operator and returns its id. One open card per issue: a repeated
// request refreshes it. Its options are to queue the issue here (the
// trusted `queue.add`) or dismiss it, and it dismisses itself on expiry.
func (s *Server) RaiseConversationRunProposal(p ConversationRunProposal) (string, error) {
	if !proposalOwner.MatchString(p.Owner) || !proposalRepo.MatchString(p.Repo) || p.Repo == "." || p.Repo == ".." ||
		p.IssueNumber < 1 || !proposalID.MatchString(p.PosterAccountID) ||
		(p.PosterLogin != "" && !proposalOwner.MatchString(p.PosterLogin)) {
		return "", errors.New("run proposal: invalid repository, issue or poster")
	}
	repo := p.Owner + "/" + p.Repo
	if !s.isConfiguredRepo(repo) {
		return "", errors.New("run proposal: repo is not configured in this workspace")
	}
	store := s.attentionStore()
	if store == nil {
		return "", errors.New("run proposal: attention store not configured")
	}
	id, err := attention.NewID()
	if err != nil {
		return "", fmt.Errorf("run proposal: %w", err)
	}
	ref := repo + "#" + strconv.Itoa(p.IssueNumber)
	who := "account " + p.PosterAccountID
	if p.PosterLogin != "" {
		who = "@" + p.PosterLogin + " (" + who + ")"
	}
	req := attention.DecisionRequest{
		ID:             id,
		IdempotencyKey: ProducerConversationRunProposal + ":" + ref,
		Kind:           attention.KindApprove,
		Severity:       attention.SeverityFYI,
		Title:          "Start a run on " + ref + "?",
		Body: who + " asked in a conversation to start a pipeline run on " + ref + ". A teammate's " +
			"turn cannot start one, because the run would use your credentials. Approve it here, in " +
			"VS Code or with the CLI, to queue the issue on this machine; approving it from the dashboard " +
			"or a conversation does not start it.",
		Context:  attention.Context{Repo: repo, Issue: p.IssueNumber},
		Producer: ProducerConversationRunProposal,
		Options: []attention.Option{
			// The title is a placeholder, as a remote trigger's is: the run
			// reads the issue itself, and nothing the poster wrote is carried.
			{ID: runProposalStart, Label: "Queue the run here", Verb: attention.VerbQueueAdd,
				Args: map[string]any{"title": "Issue #" + strconv.Itoa(p.IssueNumber)}},
			{ID: runProposalDismiss, Label: "Dismiss", Verb: attention.VerbNoop},
		},
		DefaultAction: runProposalDismiss,
	}
	_, liveID, err := store.Raise(req)
	if err != nil {
		return "", fmt.Errorf("run proposal: %w", err)
	}
	if liveID == "" {
		liveID = id
	}
	log.Printf("[nightgauge] conversation %s turn %s: %s proposed a run on %s to the operator as card %s",
		p.ConversationID, p.TurnID, p.PosterAccountID, ref, liveID)
	return liveID, nil
}

// refuseRelayedRunProposal refuses a relayed resolution of a run proposal
// that would start its run. Dismissing it from anywhere is allowed.
func refuseRelayedRunProposal(store *attention.Store, requestID, optionID string) error {
	req, ok, err := store.Get(requestID)
	if err != nil {
		// Unreadable: refused, never resolved on a guess.
		return fmt.Errorf("attention_resolve: %w", err)
	}
	if !ok || req.Producer != ProducerConversationRunProposal {
		return nil
	}
	if opt := req.FindOption(optionID); opt != nil && opt.Verb == attention.VerbNoop {
		return nil
	}
	return errRelayedRunProposal
}
