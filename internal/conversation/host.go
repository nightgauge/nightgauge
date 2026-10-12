package conversation

import (
	"bufio"
	"context"
	"errors"
	"log"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"

	"github.com/nightgauge/nightgauge/internal/execution"
	"github.com/nightgauge/nightgauge/internal/platform"
)

// Platform is the hosted service as the turn host uses it. NewPlatform wraps
// a *platform.Client.
type Platform interface {
	// ClaimTurn acknowledges a turn command; only a nil error authorizes
	// running it.
	ClaimTurn(ctx context.Context, agentID, commandID, turnID string) (platform.TurnClaim, error)
	// RejectCommand acknowledges a command `rejected` with detail.
	RejectCommand(ctx context.Context, agentID, commandID, detail string) error
	// ApplyCommand acknowledges a command `applied`.
	ApplyCommand(ctx context.Context, agentID, commandID string) error
	ReadTurnContext(ctx context.Context, lease platform.TurnLease) (platform.TurnContext, error)
	PostTurnFrames(ctx context.Context, lease platform.TurnLease, frames []platform.TurnFrame) (platform.TurnFramesResult, error)
}

type clientPlatform struct {
	client   *platform.Client
	commands *platform.CommandService
}

// NewPlatform is the hosted service reached through client.
func NewPlatform(client *platform.Client) Platform {
	return clientPlatform{client: client, commands: platform.NewCommandService(client)}
}

func (p clientPlatform) ClaimTurn(ctx context.Context, agentID, commandID, turnID string) (platform.TurnClaim, error) {
	return p.commands.ClaimTurn(ctx, agentID, commandID, turnID)
}

func (p clientPlatform) RejectCommand(ctx context.Context, agentID, commandID, detail string) error {
	return p.commands.RejectAgentCommand(ctx, agentID, commandID, detail)
}

func (p clientPlatform) ApplyCommand(ctx context.Context, agentID, commandID string) error {
	return p.commands.ApplyAgentCommand(ctx, agentID, commandID, "")
}

func (p clientPlatform) ReadTurnContext(ctx context.Context, lease platform.TurnLease) (platform.TurnContext, error) {
	return p.client.ReadTurnContext(ctx, lease)
}

func (p clientPlatform) PostTurnFrames(ctx context.Context, lease platform.TurnLease, frames []platform.TurnFrame) (platform.TurnFramesResult, error) {
	return p.client.PostTurnFrames(ctx, lease, frames)
}

// Owner reports whether a turn for targetWorkspaceID is this daemon's to
// serve. Several daemons on one machine share one agent, so every one of them
// receives each turn; only the daemon serving the target workspace may claim
// it, and the others leave it alone. A turn that is not this daemon's is
// never acknowledged, not even to refuse it.
type Owner func(ctx context.Context, agentID, targetWorkspaceID string) (bool, error)

// Config configures a Host.
type Config struct {
	// WorkspaceRoot is the workspace the daemon serves: its config resolves
	// every turn's execution profile.
	WorkspaceRoot string
	Platform      Platform
	// Owner decides which turns are this daemon's. Nil claims every turn
	// addressed to the agent.
	Owner Owner
	// Classify gives a finished turn its retention class; the mutation
	// floor applies over its answer. Nil is the documented rule table.
	Classify Classifier
	// IsMutating reports whether a tool changes state, for the mutation
	// floor. Nil counts every tool as mutating.
	IsMutating func(tool string) bool
	// MaxConcurrent bounds the turns running at once; a turn past it is
	// refused. Zero means defaultMaxConcurrent.
	MaxConcurrent int
}

const (
	defaultMaxConcurrent = 4
	// timeoutMargin ends a turn this long before its hosted deadline, so its
	// terminal report lands while the turn still runs there.
	timeoutMargin = 5 * time.Second
	// maxTurnDuration bounds a deadline the command names.
	maxTurnDuration = 30 * time.Minute
	// stopGrace is how long a stopped turn gets after SIGTERM before its
	// group is killed.
	stopGrace = 2 * time.Second
	// cancelMargin is kept back from the cancel confirmation window for the
	// acknowledgement itself.
	cancelMargin = 3 * time.Second
	// verifyWindow bounds the wait for a finished turn's group to be gone.
	verifyWindow = 10 * time.Second
	// endPostTimeout bounds the terminal report's post.
	endPostTimeout = 15 * time.Second
	// finishedRetention keeps a finished turn findable for a late cancel.
	finishedRetention = 10 * time.Minute
	// commandMemory is how long a handled command id is remembered.
	commandMemory = time.Hour
	// contextReadAttempts retries a context read that failed in transit.
	contextReadAttempts = 3
	// maxLineBytes bounds one line of the adapter's stream.
	maxLineBytes = 16 << 20
)

// Stop reasons. The first stop of a turn wins.
const (
	stopTeammate     = "teammate-cancel"
	stopPlatform     = "platform-ended"
	stopTimeout      = "timeout"
	stopTokenCeiling = "token-ceiling"
	stopShutdown     = "shutdown"
	stopHostError    = "host-error"
)

// Host answers the conversation turns addressed to this daemon's agent.
type Host struct {
	ctx      context.Context
	cfg      Config
	planner  planner
	interval time.Duration
	now      func() time.Time
	// onSpawn, when set, sees each turn's process group as it is spawned.
	onSpawn func(pgid int)

	mu sync.Mutex
	// commands are the command ids claimed, being claimed, or refused,
	// with when: a redelivered command is never acted on twice.
	commands map[string]time.Time
	turns    map[string]*turn
	wg       sync.WaitGroup
}

// NewHost returns a host whose turns live as long as ctx: when it ends, every
// running turn is stopped.
func NewHost(ctx context.Context, cfg Config) *Host {
	if cfg.MaxConcurrent <= 0 {
		cfg.MaxConcurrent = defaultMaxConcurrent
	}
	if cfg.Classify == nil {
		cfg.Classify = classifyByRules
	}
	if cfg.IsMutating == nil {
		cfg.IsMutating = everyToolMutates
	}
	h := &Host{
		ctx:      ctx,
		cfg:      cfg,
		planner:  defaultPlanner(exec.LookPath),
		interval: defaultFlushInterval,
		now:      time.Now,
		commands: map[string]time.Time{},
		turns:    map[string]*turn{},
	}
	go func() {
		<-ctx.Done()
		h.mu.Lock()
		for _, t := range h.turns {
			if t.finishedAt.IsZero() {
				t.stop(stopShutdown)
			}
		}
		h.mu.Unlock()
	}()
	return h
}

// HandleCommand takes a turn command from the agent command stream. It never
// blocks the stream: claiming and running a turn, and confirming a cancel,
// each happen on their own goroutine. Its signature is
// platform.AgentCommandHandler.
func (h *Host) HandleCommand(_ context.Context, agentID string, cmd platform.PendingCommand) {
	if agentID == "" || !validID(cmd.ID) {
		return
	}
	switch cmd.Type {
	case platform.ConversationTurnCommandType:
		h.wg.Add(1)
		go func() {
			defer h.wg.Done()
			h.handleTurn(agentID, cmd)
		}()
	case platform.ConversationTurnCancelCommandType:
		h.wg.Add(1)
		go func() {
			defer h.wg.Done()
			h.handleCancel(agentID, cmd)
		}()
	}
}

// Wait returns once every turn and cancel the host started has finished.
func (h *Host) Wait() { h.wg.Wait() }

// ShutdownWait bounds how long a daemon waits for its turns after its context
// ended: each is stopped, its group killed and verified gone, and its report
// sent.
const ShutdownWait = stopGrace + verifyWindow + endPostTimeout + 3*time.Second

// WaitFor is Wait bounded by d. It reports whether every turn finished.
func (h *Host) WaitFor(d time.Duration) bool {
	done := make(chan struct{})
	go func() {
		h.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return true
	case <-time.After(d):
		return false
	}
}

// turn is one turn this host claimed or is claiming.
type turn struct {
	cmd       TurnCommand
	commandID string
	agentID   string

	mu         sync.Mutex
	claimed    bool
	stopReason string
	proc       *turnProcess
	stopped    chan struct{} // closed by the first stop
	gone       chan struct{} // closed once the process is gone, or never existed
	dead       bool          // the group was verified gone; valid once gone is closed
	done       chan struct{} // closed once the turn is over here
	finishedAt time.Time
}

func newTurn(cmd TurnCommand, commandID, agentID string) *turn {
	return &turn{
		cmd: cmd, commandID: commandID, agentID: agentID,
		stopped: make(chan struct{}),
		gone:    make(chan struct{}),
		done:    make(chan struct{}),
	}
}

// stop records why the turn stops, the first time, and signals its process
// group: SIGTERM, then SIGKILL after stopGrace.
func (t *turn) stop(reason string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.stopReason != "" {
		return
	}
	t.stopReason = reason
	close(t.stopped)
	t.signalLocked()
}

func (t *turn) signalLocked() {
	if t.proc == nil {
		return // not spawned yet, or reaped: its pgid may belong to another group now
	}
	proc := t.proc
	proc.signal(syscall.SIGTERM)
	time.AfterFunc(stopGrace, func() {
		t.mu.Lock()
		defer t.mu.Unlock()
		if t.proc == proc {
			proc.signal(syscall.SIGKILL)
		}
	})
}

// detach forgets the process once its leader is reaped: from then on only
// verifyGone, which signals the group only while it exists, may signal it.
func (t *turn) detach() {
	t.mu.Lock()
	t.proc = nil
	t.mu.Unlock()
}

// attach records the spawned process. A stop that came first reaches it now.
func (t *turn) attach(p *turnProcess) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.proc = p
	if t.stopReason != "" {
		t.signalLocked()
	}
}

func (t *turn) reason() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.stopReason
}

func (t *turn) markClaimed() {
	t.mu.Lock()
	t.claimed = true
	t.mu.Unlock()
}

func (t *turn) wasClaimed() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.claimed
}

// markGone records that the turn's process is gone (or never existed) and
// forgets it, so no later stop can signal a process-group id the system may
// have handed to another group.
func (t *turn) markGone(dead bool) {
	t.mu.Lock()
	t.dead = dead
	t.proc = nil
	t.mu.Unlock()
	close(t.gone)
}

func (t *turn) verifiedDead() bool {
	select {
	case <-t.gone:
	default:
		return false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.dead
}

// handleTurn decides whether the turn is this daemon's, refuses one it
// cannot serve, claims one it can, and runs it. Nothing is spawned before the
// claim succeeded, and a command is claimed at most once.
func (h *Host) handleTurn(agentID string, pc platform.PendingCommand) {
	if !h.reserve(pc.ID) {
		return // claimed, refused or being claimed already: a redelivery never runs
	}
	tc, err := ParseTurnCommand(pc.Payload, h.now())
	if err != nil {
		// Left unacknowledged, not refused: without a readable target this
		// daemon cannot tell the turn is its own, and another daemon on the
		// agent may serve it. The hosted service ends it at its ack deadline.
		log.Printf("conversation: turn command %s is unreadable — left alone: %v", pc.ID, err)
		return
	}
	if h.cfg.Owner != nil {
		owned, err := h.cfg.Owner(h.ctx, agentID, tc.TargetWorkspaceID)
		if err != nil || !owned {
			if err != nil {
				log.Printf("conversation: turn %s: could not tell whether this daemon serves its workspace — left alone: %v", tc.TurnID, err)
			}
			h.release(pc.ID)
			return
		}
	}
	p, err := h.planner.plan(h.cfg.WorkspaceRoot)
	if err != nil {
		detail := "host-error: the turn could not be prepared"
		var r *refusal
		if errors.As(err, &r) {
			detail = r.detail
		}
		log.Printf("conversation: turn %s refused: %s", tc.TurnID, detail)
		h.reject(agentID, pc.ID, detail)
		return
	}
	t := newTurn(tc, pc.ID, agentID)
	if !h.register(t) {
		log.Printf("conversation: turn %s refused: %d turns already running, or this turn is", tc.TurnID, h.cfg.MaxConcurrent)
		h.reject(agentID, pc.ID, "host-busy: this host is running as many turns as it takes")
		return
	}
	defer h.finish(t)

	claim, err := h.cfg.Platform.ClaimTurn(h.ctx, agentID, pc.ID, tc.TurnID)
	if err != nil {
		var refused *platform.TurnRequestError
		if !errors.As(err, &refused) || !refused.Terminal() {
			// The acknowledgement may not have arrived: a redelivery may
			// claim it, and the hosted service answers ALREADY_ACKED if
			// this one did.
			h.release(pc.ID)
		}
		log.Printf("conversation: turn %s not claimed — not run: %v", tc.TurnID, err)
		h.unregister(t)
		t.markGone(true)
		return
	}
	t.markClaimed()
	note := ""
	if p.effortNote != "" {
		note = " (" + p.effortNote + ")"
	}
	log.Printf("conversation: turn %s claimed — running on %s, model %s, effort %q%s",
		tc.TurnID, p.profile.Adapter, p.model, p.effort, note)
	h.run(t, p, claim)
}

// reserve marks a command as being claimed, or reports that it already is.
func (h *Host) reserve(commandID string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, seen := h.commands[commandID]; seen {
		return false
	}
	h.commands[commandID] = h.now()
	return true
}

func (h *Host) release(commandID string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.commands, commandID)
}

// register records a turn about to be claimed, so a cancel that arrives while
// the claim is in flight finds it. It refuses one past MaxConcurrent.
func (h *Host) register(t *turn) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	for id, at := range h.commands {
		if h.now().Sub(at) > commandMemory {
			delete(h.commands, id)
		}
	}
	running := 0
	for id, other := range h.turns {
		if other.finishedAt.IsZero() {
			running++
		} else if h.now().Sub(other.finishedAt) > finishedRetention {
			delete(h.turns, id)
		}
	}
	if running >= h.cfg.MaxConcurrent {
		return false
	}
	if held := h.turns[t.cmd.TurnID]; held != nil && held.finishedAt.IsZero() {
		return false // a running turn already has this id
	}
	h.turns[t.cmd.TurnID] = t
	return true
}

// unregister forgets a turn that was never claimed here: a cancel for it
// belongs to whichever host claimed it.
func (h *Host) unregister(t *turn) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.turns[t.cmd.TurnID] == t {
		delete(h.turns, t.cmd.TurnID)
	}
}

func (h *Host) finish(t *turn) {
	h.mu.Lock()
	t.finishedAt = h.now()
	h.mu.Unlock()
	close(t.done)
}

func (h *Host) reject(agentID, commandID, detail string) {
	ctx, cancel := context.WithTimeout(h.ctx, endPostTimeout)
	defer cancel()
	if err := h.cfg.Platform.RejectCommand(ctx, agentID, commandID, detail); err != nil {
		log.Printf("conversation: refusing command %s failed: %v", commandID, err)
	}
}

// run executes a claimed turn: reads its context on the lease, spawns the
// adapter, streams the reply, enforces the timeout and the token ceiling, and
// sends the terminal report.
func (h *Host) run(t *turn, p plan, claim platform.TurnClaim) {
	lease := claim.Lease
	deadline := t.cmd.DeadlineAt
	if !claim.DeadlineAt.IsZero() && claim.DeadlineAt.Before(deadline) {
		deadline = claim.DeadlineAt
	}
	if limit := h.now().Add(maxTurnDuration); deadline.After(limit) {
		deadline = limit
	}
	post := func(ctx context.Context, frames []platform.TurnFrame) (platform.TurnFramesResult, error) {
		return h.cfg.Platform.PostTurnFrames(ctx, lease, frames)
	}
	out := newPoster(h.ctx, post, t.cmd.Limits, deadline, h.interval, func(err error) {
		log.Printf("conversation: turn %s is over on the hosted service — stopping: %v", t.cmd.TurnID, err)
		t.stop(stopPlatform)
	})
	report := func(end *platform.TurnEnd) {
		// Not h.ctx alone: a turn stopped by the daemon's shutdown still
		// gets its report out, bounded.
		ctx, cancel := context.WithTimeout(context.WithoutCancel(h.ctx), endPostTimeout)
		defer cancel()
		if err := out.finish(ctx, end); err != nil && end != nil {
			log.Printf("conversation: turn %s: terminal report not delivered: %v", t.cmd.TurnID, err)
		}
	}
	profile := &platform.TurnExecutionProfile{
		Adapter:            p.profile.Adapter,
		AdapterDisplayName: p.profile.AdapterDisplayName,
		PerformanceMode:    p.profile.PerformanceMode,
	}
	record := newToolRecord(h.cfg.IsMutating)
	failed := func(reason string) *platform.TurnEnd {
		return &platform.TurnEnd{State: platform.TurnStateFailed, FailureReason: reason, Profile: profile,
			RetentionClass: h.classify(t, platform.TurnStateFailed, record.calls)}
	}

	message, err := h.readMessage(lease, t.cmd.TurnID)
	if err != nil {
		t.markGone(true)
		if errors.Is(err, errTurnGone) {
			report(nil)
			return
		}
		log.Printf("conversation: turn %s: %v", t.cmd.TurnID, err)
		report(failed(platform.TurnFailureHostError))
		return
	}

	select {
	case <-t.stopped:
		// Stopped while claiming or reading: nothing was spawned, so
		// nothing can still run.
		t.markGone(true)
		h.reportStopped(t, report, profile, nil, nil)
		return
	default:
	}

	dir, err := os.MkdirTemp("", "nightgauge-turn-")
	if err != nil {
		t.markGone(true)
		report(failed(platform.TurnFailureHostError))
		return
	}
	defer os.RemoveAll(dir)
	proc, err := startTurnProcess(p, dir, message)
	if err != nil {
		log.Printf("conversation: turn %s: the adapter did not start: %v", t.cmd.TurnID, err)
		t.markGone(true)
		report(failed(platform.TurnFailureHostError))
		return
	}
	if h.onSpawn != nil {
		h.onSpawn(proc.pgid)
	}
	t.attach(proc)
	timer := time.AfterFunc(turnBudget(deadline, h.now()), func() { t.stop(stopTimeout) })
	defer timer.Stop()
	go func() {
		// A descendant that left the group can hold stdout open after the
		// group is dead; closing the read end ends the reader so the turn
		// cannot hang on it.
		select {
		case <-t.stopped:
			select {
			case <-time.After(stopGrace + time.Second):
				_ = proc.stdout.Close()
			case <-t.gone:
			}
		case <-t.gone:
		}
	}()

	acc := &execution.TokenAccumulator{}
	var effortActive *bool
	success := false
	scanner := bufio.NewScanner(proc.stdout)
	scanner.Buffer(make([]byte, 0, 64<<10), maxLineBytes)
	for scanner.Scan() {
		line := scanner.Text()
		record.observe(line)
		event, _ := acc.ParseStreamLine(line)
		if event == nil {
			continue
		}
		if event.Type == "system" && event.Subtype == "init" && event.PerTurnEffortActive != nil {
			effortActive = event.PerTurnEffortActive
		}
		if _, text, ok := event.TextDelta(); ok && text != "" {
			out.write(text)
		}
		if event.Type == "result" {
			success = event.Subtype == "success"
		}
		if turnTokens(acc) > t.cmd.TokenCeiling {
			t.stop(stopTokenCeiling)
		}
	}
	if err := scanner.Err(); err != nil {
		// Nothing reads the stream any more, so the process must not
		// outlive this point.
		log.Printf("conversation: turn %s: reading the adapter's stream failed: %v", t.cmd.TurnID, err)
		t.stop(stopHostError)
	}
	waitErr := proc.cmd.Wait()
	t.detach()
	dead := proc.verifyGone(time.Now().Add(verifyWindow))
	t.markGone(dead)
	if !dead {
		log.Printf("conversation: turn %s: its process group %d is still alive after the turn ended", t.cmd.TurnID, proc.pgid)
	}

	profile.Effort = p.effort
	if p.effort != "" && effortActive != nil && !*effortActive {
		log.Printf("conversation: turn %s: the adapter did not apply effort %s", t.cmd.TurnID, p.effort)
		profile.Effort = ""
	}
	usage := &platform.TurnUsage{
		InputTokens:  acc.InputTokens + acc.CacheCreated + acc.CacheRead,
		OutputTokens: acc.OutputTokens,
	}
	if t.reason() != "" {
		h.reportStopped(t, report, profile, usage, record.calls)
		return
	}
	if waitErr != nil || !success {
		log.Printf("conversation: turn %s: the adapter failed (exit: %v, result success: %v)", t.cmd.TurnID, waitErr, success)
		end := failed(platform.TurnFailureHostError)
		end.Usage = usage
		report(end)
		return
	}
	report(&platform.TurnEnd{
		State: platform.TurnStateCompleted, Profile: profile, Usage: usage,
		RetentionClass: h.classify(t, platform.TurnStateCompleted, record.calls),
	})
}

// classify is the retention class a finished turn reports, logged with the
// rule that decided it.
func (h *Host) classify(t *turn, state string, calls []ToolCall) string {
	c := retain(h.cfg.Classify, TurnOutcome{State: state, ToolCalls: calls})
	log.Printf("conversation: turn %s is %s by rule %s (%d tool calls)", t.cmd.TurnID, c.Class, c.Rule, len(calls))
	return c.Class
}

// reportStopped sends the terminal report of a stopped turn. A turn the
// hosted service ended gets none; a teammate's cancel is reported cancelled
// only when the process group was verified gone, and otherwise not at all,
// so the hosted service ends it cancel_unconfirmed.
func (h *Host) reportStopped(t *turn, report func(*platform.TurnEnd), profile *platform.TurnExecutionProfile, usage *platform.TurnUsage, calls []ToolCall) {
	end := &platform.TurnEnd{Profile: profile, Usage: usage}
	switch t.reason() {
	case stopTeammate:
		if !t.verifiedDead() {
			report(nil)
			return
		}
		end.State = platform.TurnStateCancelled
	case stopTimeout:
		end.State, end.FailureReason = platform.TurnStateFailed, platform.TurnFailureTimeout
	case stopTokenCeiling:
		end.State, end.FailureReason = platform.TurnStateFailed, platform.TurnFailureTokenCeiling
	case stopShutdown, stopHostError:
		// Failed after the claim: never `refused`, which means the host
		// declined the turn (#2449).
		end.State, end.FailureReason = platform.TurnStateFailed, platform.TurnFailureHostError
	default:
		report(nil)
		return
	}
	end.RetentionClass = h.classify(t, end.State, calls)
	report(end)
}

// errTurnGone is a context read refused because the turn is over or not this
// agent's: nothing more is reported.
var errTurnGone = errors.New("the turn is not running on the hosted service")

// readMessage reads the turn's context on its lease and renders the stdin
// message, retrying a read that failed in transit.
func (h *Host) readMessage(lease platform.TurnLease, turnID string) (string, error) {
	var read platform.TurnContext
	var err error
	for attempt := 0; attempt < contextReadAttempts; attempt++ {
		if attempt > 0 {
			select {
			case <-h.ctx.Done():
				return "", h.ctx.Err()
			case <-time.After(time.Duration(attempt) * 500 * time.Millisecond):
			}
		}
		read, err = h.cfg.Platform.ReadTurnContext(h.ctx, lease)
		if err == nil {
			break
		}
		var refused *platform.TurnRequestError
		if errors.As(err, &refused) && refused.Terminal() {
			return "", errTurnGone
		}
	}
	if err != nil {
		return "", err
	}
	return renderMessage(read, turnID)
}

// turnBudget is how long a turn may run: until timeoutMargin before its
// deadline, or until the deadline itself when that leaves less than the
// margin.
func turnBudget(deadline, now time.Time) time.Duration {
	left := deadline.Sub(now)
	if left > 2*timeoutMargin {
		return left - timeoutMargin
	}
	if left < 0 {
		return 0
	}
	return left
}

// turnTokens is every token the turn's model processed: its prompt (fresh,
// cache-written and cache-read) and its output. The ceiling bounds that sum,
// and the terminal report states the same counts.
func turnTokens(acc *execution.TokenAccumulator) int {
	return acc.InputTokens + acc.CacheCreated + acc.CacheRead + acc.OutputTokens
}

// handleCancel stops the turn a cancel names and acknowledges the cancel
// `applied` only once the turn's process group is verified gone. A cancel for
// a turn this host never claimed is left alone: another daemon sharing the
// agent may hold it.
func (h *Host) handleCancel(agentID string, pc platform.PendingCommand) {
	c, err := ParseCancelCommand(pc.Payload)
	if err != nil {
		log.Printf("conversation: cancel command %s is malformed — ignored: %v", pc.ID, err)
		return
	}
	h.mu.Lock()
	t := h.turns[c.TurnID]
	h.mu.Unlock()
	if t == nil || t.commandID != c.TurnCommandID || t.agentID != agentID ||
		(c.ConversationID != "" && c.ConversationID != t.cmd.ConversationID) {
		return
	}
	if !h.reserve(pc.ID) {
		return
	}
	reason := stopPlatform
	if c.Reason == CancelReasonTeammate {
		reason = stopTeammate
	}
	t.stop(reason)

	window := time.Duration(t.cmd.Limits.CancelConfirmSeconds)*time.Second - cancelMargin
	if window <= 0 {
		window = time.Second
	}
	// Wait for the turn to finish, so a cancelled turn's own report (with
	// its profile and tokens) lands before the acknowledgement; past the
	// window, a verified-dead group is still confirmed.
	select {
	case <-t.done:
	case <-time.After(window):
	case <-h.ctx.Done():
	}
	if !t.wasClaimed() {
		// The claim failed: the turn is not this host's to confirm.
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(h.ctx), endPostTimeout)
	defer cancel()
	if t.verifiedDead() {
		if err := h.cfg.Platform.ApplyCommand(ctx, agentID, pc.ID); err != nil && !isTurnOver(err) {
			log.Printf("conversation: turn %s: confirming its cancel failed: %v", c.TurnID, err)
		}
		return
	}
	log.Printf("conversation: turn %s: its process group could not be verified gone — cancel not confirmed", c.TurnID)
	if err := h.cfg.Platform.RejectCommand(ctx, agentID, pc.ID, "cancel-unconfirmed: the turn's process could not be verified gone"); err != nil && !isTurnOver(err) {
		log.Printf("conversation: turn %s: refusing its cancel failed: %v", c.TurnID, err)
	}
}

// isTurnOver reports a refusal that only says the turn already ended.
func isTurnOver(err error) bool {
	return errors.Is(err, platform.ErrTurnEnded) || errors.Is(err, platform.ErrTurnAlreadyAcked)
}
