package cloudqueue

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/nightgauge/nightgauge/internal/credshape"
	"github.com/nightgauge/nightgauge/internal/platform"
)

// Outcome is what adding an item to the local queue found.
type Outcome int

const (
	// Queued: the issue is now in the local queue, added by this command.
	Queued Outcome = iota + 1
	// QueuedEpic: the issue is an epic, queued as its open sub-issues the
	// way every other route queues an epic.
	QueuedEpic
	// AlreadyQueued: the issue was in the local queue before this command.
	AlreadyQueued
	// AlreadyRunning: a run of the issue is under way on this daemon.
	AlreadyRunning
)

// Refusal is why the local queue did not take an item, worded for the
// requester: the hosted service shows it on the cloud queue row. Every other
// error is reported as a generic refusal and logged here in full.
type Refusal struct{ Reason string }

func (r *Refusal) Error() string { return r.Reason }

// Refuse returns a *Refusal with the formatted reason.
func Refuse(format string, args ...any) error {
	return &Refusal{Reason: fmt.Sprintf(format, args...)}
}

// Queue is this daemon's local queue. *ipc.Server implements it.
type Queue interface {
	// AddCloudItem adds item to the local queue unless it is already queued
	// or running here, or the queue cannot take it (a *Refusal).
	AddCloudItem(ctx context.Context, item Item) (Outcome, error)
	// WithdrawCloudItem takes back an item AddCloudItem queued, only while it
	// still waits for a slot, and reports whether it did.
	WithdrawCloudItem(item Item) bool
}

// Acker acknowledges a command with an outcome. *platform.CommandService
// implements it.
type Acker interface {
	ApplyAgentCommand(ctx context.Context, agentID, commandID, detail string) error
	AlreadyResolvedAgentCommand(ctx context.Context, agentID, commandID, detail string) error
	RejectAgentCommand(ctx context.Context, agentID, commandID, detail string) error
}

// Config is what a Handler needs.
type Config struct {
	Queue Queue
	Acks  Acker
	// Served lists the repositories this daemon declares on its agent
	// registration, the set the hosted service routes a `queue_add` by.
	Served func() ([]platform.AgentRepo, error)
	// Timeout bounds the handling of one command, the issue read and the
	// acknowledgement included. Zero is defaultTimeout.
	Timeout time.Duration
}

const (
	defaultTimeout = 2 * time.Minute
	// maxClaims bounds the command ids remembered for redelivery. Only an
	// answered command is ever forgotten, the oldest first.
	maxClaims = 1024
)

// Handler handles `queue_add` commands. One Handler serves the daemon's own
// command stream and the commands the extension hands over from its agent's
// stream (agent.queueAdd), so one claim covers a command on both.
type Handler struct {
	ctx context.Context
	cfg Config

	mu     sync.Mutex
	claims map[string]*claim
	order  []string
	wg     sync.WaitGroup
}

// claim is one command id this daemon has taken. A command is acted on once:
// a copy that arrives while the first is handled is dropped, and a copy that
// arrives after it was answered re-sends that answer without queueing again.
type claim struct {
	done    bool
	outcome string
	detail  string
}

// New returns a Handler whose work stops when ctx ends.
func New(ctx context.Context, cfg Config) *Handler {
	return &Handler{ctx: ctx, cfg: cfg, claims: map[string]*claim{}}
}

// HandleCommand is a platform.AgentCommandHandler. It returns at once: the
// issue read and the acknowledgement run on their own goroutine, so the
// command stream is never held.
func (h *Handler) HandleCommand(_ context.Context, agentID string, cmd platform.PendingCommand) {
	if cmd.Type != platform.QueueAddCommandType {
		return
	}
	if !validID(agentID) || !validID(cmd.ID) {
		log.Printf("[nightgauge] queue_add: a command with an unusable agent or command id was left unacknowledged")
		return
	}
	fresh, outcome, detail, ok := h.take(cmd.ID)
	if !ok {
		log.Printf("[nightgauge] queue_add %s: a copy is already being handled; this one is dropped", cmd.ID)
		return
	}
	h.wg.Add(1)
	go func() {
		defer h.wg.Done()
		if fresh {
			h.run(agentID, cmd)
			return
		}
		h.answerAgain(agentID, cmd.ID, outcome, detail)
	}()
}

// Wait blocks until every command taken so far has been answered.
func (h *Handler) Wait() { h.wg.Wait() }

// take claims a command id. fresh means this copy is the first and acts;
// otherwise a copy already answered returns that answer, and ok is false
// while the first copy is still being handled.
func (h *Handler) take(id string) (fresh bool, outcome, detail string, ok bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if c, seen := h.claims[id]; seen {
		if !c.done {
			return false, "", "", false
		}
		return false, c.outcome, c.detail, true
	}
	h.claims[id] = &claim{}
	h.order = append(h.order, id)
	h.forgetLocked()
	return true, "", "", true
}

// forgetLocked drops the oldest answered claims past maxClaims.
func (h *Handler) forgetLocked() {
	if len(h.order) <= maxClaims {
		return
	}
	kept := h.order[:0]
	excess := len(h.order) - maxClaims
	for _, id := range h.order {
		if excess > 0 && h.claims[id].done {
			delete(h.claims, id)
			excess--
			continue
		}
		kept = append(kept, id)
	}
	h.order = kept
}

func (h *Handler) settle(id, outcome, detail string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if c, ok := h.claims[id]; ok {
		c.done, c.outcome, c.detail = true, outcome, detail
	}
}

func (h *Handler) timeout() time.Duration {
	if h.cfg.Timeout > 0 {
		return h.cfg.Timeout
	}
	return defaultTimeout
}

// run decides one command, acknowledges it, and takes back an item it queued
// when the acknowledgement finds the command already taken.
func (h *Handler) run(agentID string, cmd platform.PendingCommand) {
	ctx, cancel := context.WithTimeout(h.ctx, h.timeout())
	defer cancel()
	item, outcome, detail, added := h.decide(ctx, cmd)
	detail = publicDetail(detail)
	err := h.ack(ctx, agentID, cmd.ID, outcome, detail)
	h.settle(cmd.ID, outcome, detail)
	switch {
	case err == nil:
		log.Printf("[nightgauge] queue_add %s: %s acknowledged %s", cmd.ID, describe(item), outcome)
	case errors.Is(err, platform.ErrAgentCommandAlreadyAcked):
		// Another connection of the agent answered first (another daemon or
		// window on this machine), or the command was cancelled or expired.
		// Either way this daemon's queue does not hold the item for it.
		if added && h.cfg.Queue.WithdrawCloudItem(item) {
			log.Printf("[nightgauge] queue_add %s: already acknowledged elsewhere; %s taken back off this queue", cmd.ID, item)
			return
		}
		log.Printf("[nightgauge] queue_add %s: already acknowledged elsewhere (%s stays as it is here)", cmd.ID, describe(item))
	default:
		log.Printf("[nightgauge] queue_add %s: acknowledging %s failed; a redelivery re-sends it without queueing again: %v", cmd.ID, outcome, err)
	}
}

// answerAgain re-sends the answer of a command already handled, for a copy
// the hosted service delivered again because the first answer did not reach
// it.
func (h *Handler) answerAgain(agentID, commandID, outcome, detail string) {
	ctx, cancel := context.WithTimeout(h.ctx, h.timeout())
	defer cancel()
	err := h.ack(ctx, agentID, commandID, outcome, detail)
	switch {
	case err == nil:
		log.Printf("[nightgauge] queue_add %s: redelivered; acknowledged %s again, nothing queued again", commandID, outcome)
	case errors.Is(err, platform.ErrAgentCommandAlreadyAcked):
		log.Printf("[nightgauge] queue_add %s: redelivered; it was already acknowledged", commandID)
	default:
		log.Printf("[nightgauge] queue_add %s: redelivered; acknowledging %s again failed: %v", commandID, outcome, err)
	}
}

func describe(item Item) string {
	if item.IssueNumber == 0 {
		return "the command"
	}
	return item.String()
}

// decide returns the acknowledgement for one command. added is true when this
// command put the issue in the local queue itself.
func (h *Handler) decide(ctx context.Context, cmd platform.PendingCommand) (item Item, outcome, detail string, added bool) {
	item, err := ParseItem(cmd.Payload)
	if err != nil {
		return Item{}, platform.AgentCommandRejectedOutcome, "invalid-command: " + err.Error(), false
	}
	declared, covered, err := h.covers(item)
	if err != nil {
		log.Printf("[nightgauge] queue_add %s: reading the workspace this daemon serves failed: %v", cmd.ID, err)
		return item, platform.AgentCommandRejectedOutcome, "cannot-queue: this agent could not read the workspace it serves", false
	}
	if !covered {
		return item, platform.AgentCommandRejectedOutcome,
			fmt.Sprintf("not-covered: this agent's workspace does not include %s", item.FullName()), false
	}
	// The queue keys an item by the repository as the workspace names it.
	item.Owner, item.Repo = declared.Owner, declared.Repo
	if h.cfg.Queue == nil {
		return item, platform.AgentCommandRejectedOutcome, "cannot-queue: this agent has no queue", false
	}
	result, err := h.cfg.Queue.AddCloudItem(ctx, item)
	if err != nil {
		var refusal *Refusal
		if errors.As(err, &refusal) {
			return item, platform.AgentCommandRejectedOutcome, refusal.Reason, false
		}
		log.Printf("[nightgauge] queue_add %s: queueing %s failed: %v", cmd.ID, item, err)
		return item, platform.AgentCommandRejectedOutcome, fmt.Sprintf("cannot-queue: this agent could not queue %s", item), false
	}
	switch result {
	case Queued:
		return item, platform.AgentCommandAppliedOutcome, "", true
	case QueuedEpic:
		return item, platform.AgentCommandAppliedOutcome, "queued as its open sub-issues", false
	case AlreadyQueued:
		return item, platform.AgentCommandAlreadyResolvedOutcome,
			fmt.Sprintf("already-queued: %s is already in this agent's queue", item), false
	case AlreadyRunning:
		return item, platform.AgentCommandAlreadyResolvedOutcome,
			fmt.Sprintf("already-running: %s is already running on this agent", item), false
	}
	log.Printf("[nightgauge] queue_add %s: the queue answered an unknown outcome %d", cmd.ID, result)
	return item, platform.AgentCommandRejectedOutcome, fmt.Sprintf("cannot-queue: this agent could not queue %s", item), false
}

// covers reports whether this daemon declares item's repository, and the
// repository as it declares it. GitHub names are case-insensitive.
func (h *Handler) covers(item Item) (platform.AgentRepo, bool, error) {
	if h.cfg.Served == nil {
		return platform.AgentRepo{}, false, nil
	}
	repos, err := h.cfg.Served()
	if err != nil {
		return platform.AgentRepo{}, false, err
	}
	for _, r := range repos {
		if strings.EqualFold(r.Owner, item.Owner) && strings.EqualFold(r.Repo, item.Repo) &&
			validName(r.Owner) && validName(r.Repo) {
			return r, true, nil
		}
	}
	return platform.AgentRepo{}, false, nil
}

func (h *Handler) ack(ctx context.Context, agentID, commandID, outcome, detail string) error {
	if h.cfg.Acks == nil {
		return errors.New("no acknowledgement path")
	}
	switch outcome {
	case platform.AgentCommandAppliedOutcome:
		return h.cfg.Acks.ApplyAgentCommand(ctx, agentID, commandID, detail)
	case platform.AgentCommandAlreadyResolvedOutcome:
		return h.cfg.Acks.AlreadyResolvedAgentCommand(ctx, agentID, commandID, detail)
	default:
		return h.cfg.Acks.RejectAgentCommand(ctx, agentID, commandID, detail)
	}
}

// publicDetail is detail as the hosted service may show it: credential
// shapes replaced through the shared registry, every control or format
// character replaced by a space, cut to the service's bound. The service
// drops a credential-shaped detail itself; this never sends one.
func publicDetail(detail string) string {
	detail = credshape.RedactEgress(detail)
	detail = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return ' '
		}
		return r
	}, detail)
	if len(detail) > platform.AgentCommandAckDetailMax {
		cut := platform.AgentCommandAckDetailMax
		for cut > 0 && !utf8.RuneStart(detail[cut]) {
			cut--
		}
		detail = detail[:cut]
	}
	return detail
}
