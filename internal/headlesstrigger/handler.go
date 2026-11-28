// Package headlesstrigger executes a platform `trigger` command in a
// `nightgauge serve` daemon that has no extension attached (#3586, step 1 of
// #1350). With an extension attached the daemon relays the trigger to it, as
// it always has; this package only runs when nobody else would.
//
// A trigger is answered once: refused with a reason (acked `rejected`), or
// acknowledged, which returns the platform run id. An acknowledged trigger
// sends a start event that creates that platform run (#3699), then runs here,
// and exactly one terminal status is reported for the run id when it ends.
package headlesstrigger

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/nightgauge/nightgauge/internal/credshape"
	"github.com/nightgauge/nightgauge/internal/orchestrator"
	"github.com/nightgauge/nightgauge/internal/platform"
	"github.com/nightgauge/nightgauge/internal/state"
)

// TriggerCommandType is the platform command that asks an agent to run an
// issue's pipeline.
const TriggerCommandType = "trigger"

// Acker answers a trigger. *platform.CommandService implements it.
type Acker interface {
	// AcknowledgeAgentCommand accepts the command and returns the run id the
	// platform assigned it, which the start event then creates.
	AcknowledgeAgentCommand(ctx context.Context, agentID, commandID string) (string, error)
	RejectAgentCommand(ctx context.Context, agentID, commandID, detail string) error
}

// Reporter opens and closes the platform's run. *platform.Client implements it.
type Reporter interface {
	ReportRemoteRunStart(ctx context.Context, remoteRunID, repo string, issueNumber int, visibility string) error
	ReportRemoteRunTerminal(ctx context.Context, remoteRunID string, status platform.RemoteRunStatus, visibility string) error
}

// Request is one accepted trigger, as the Runner executes it.
type Request struct {
	// Repo is "owner/name" as this daemon's workspace declares it.
	Repo        string
	IssueNumber int
	// RemoteRunID is the run id the ack returned: the platform's run this
	// local run serves. It is never the local run's id.
	RemoteRunID string
	// Adapter and Model are the requested pin, already validated; empty when
	// the trigger asked for none.
	Adapter string
	Model   string
	// Visibility is state.VisibilityPrivate for a private trigger, else empty.
	Visibility string
}

// Result is how a run the Runner started ended.
type Result struct {
	// Completed is true when the pipeline reached its successful end.
	Completed bool
	// TerminalKind is the run's terminal kind when it did not complete; empty
	// for a run that never dispatched.
	TerminalKind string
}

// Runner executes one accepted trigger to its end.
type Runner interface {
	Run(ctx context.Context, req Request) (Result, error)
}

// Config is what a Handler needs.
type Config struct {
	Acks    Acker
	Reports Reporter
	Runner  Runner
	// Served lists the repositories this daemon declares on its agent
	// registration.
	Served func() ([]platform.AgentRepo, error)
	// Busy reports whether the issue has a live or queued run on this daemon.
	Busy func(repo string, issueNumber int) bool
	// ValidatePin decides a requested adapter and model with the existing
	// remote-pin validation, returning the public refusal, or "" to accept.
	// It is called only when the trigger names an adapter or a model.
	ValidatePin func(repo, adapter, model string) string
	// Timeout bounds each acknowledgement, the start and the terminal report, retries
	// included. Zero is defaultTimeout.
	Timeout time.Duration
	// ReportRetryDelay is the first wait before re-sending a start or terminal
	// report that failed transiently; it doubles each attempt. Zero is
	// defaultReportRetryDelay.
	ReportRetryDelay time.Duration
}

const (
	defaultTimeout          = time.Minute
	defaultReportRetryDelay = time.Second
	// maxClaims bounds the command ids remembered for redelivery; the oldest
	// are forgotten first.
	maxClaims = 1024
)

// Refusal details. Each is the public reason the requester sees.
const (
	DetailInvalidCommand   = "invalid-command: a trigger needs owner, repo and issueNumber"
	DetailOperatorRequired = "operator-required: only this host's operator may start a run from a conversation"
	DetailInvalidPin       = "invalid-request: adapter and model must be strings"
	// DetailPinUnavailable: a pin was requested and no validation is wired.
	DetailPinUnavailable = "validation-unavailable"
	// DetailWorkspaceUnreadable: the workspace this daemon serves could not
	// be read, so whether it covers the repo is unknown.
	DetailWorkspaceUnreadable = "cannot-run: this agent could not read the workspace it serves"
)

// Handler executes `trigger` commands headlessly.
type Handler struct {
	ctx context.Context
	cfg Config

	mu       sync.Mutex
	claims   map[string]struct{}
	order    []string
	inFlight map[string]struct{} // "owner/repo#N", lower-cased
	wg       sync.WaitGroup
}

// New returns a Handler whose runs stop when ctx ends.
func New(ctx context.Context, cfg Config) *Handler {
	return &Handler{ctx: ctx, cfg: cfg, claims: map[string]struct{}{}, inFlight: map[string]struct{}{}}
}

// HandleCommand is a platform.AgentCommandHandler. It returns at once: the
// decision, the acknowledgement and the run happen on their own goroutine, so
// the command stream is never held.
func (h *Handler) HandleCommand(_ context.Context, agentID string, cmd platform.PendingCommand) {
	if cmd.Type != TriggerCommandType {
		return
	}
	if !validID(agentID) || !validID(cmd.ID) {
		log.Printf("[nightgauge] trigger: a command with an unusable agent or command id was left unacknowledged")
		return
	}
	if !h.claim(cmd.ID) {
		log.Printf("[nightgauge] trigger %s: redelivered; it was already handled here, nothing starts again", cmd.ID)
		return
	}
	h.wg.Add(1)
	go func() {
		defer h.wg.Done()
		h.handle(agentID, cmd)
	}()
}

// Wait blocks until every trigger taken so far has been answered and every
// run it started has ended and been reported.
func (h *Handler) Wait() { h.wg.Wait() }

// WaitFor is Wait bounded by d, and reports whether everything finished.
func (h *Handler) WaitFor(d time.Duration) bool {
	done := make(chan struct{})
	go func() { h.wg.Wait(); close(done) }()
	select {
	case <-done:
		return true
	case <-time.After(d):
		return false
	}
}

// claim takes a command id, and reports whether this delivery is the first.
func (h *Handler) claim(id string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, seen := h.claims[id]; seen {
		return false
	}
	h.claims[id] = struct{}{}
	h.order = append(h.order, id)
	if len(h.order) > maxClaims {
		delete(h.claims, h.order[0])
		h.order = h.order[1:]
	}
	return true
}

// forget drops a claim on a command that was never answered, so the
// platform's redelivery of it is handled as a first delivery.
func (h *Handler) forget(id string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, ok := h.claims[id]; !ok {
		return
	}
	delete(h.claims, id)
	for i, o := range h.order {
		if o == id {
			h.order = append(h.order[:i], h.order[i+1:]...)
			break
		}
	}
}

// Running reports whether repo's issue was accepted here and its run has not
// ended. The daemon's busy checks consult it, so a cloud queue_add cannot
// start a second pipeline on an issue a headless trigger is running.
func (h *Handler) Running(repo string, issueNumber int) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	_, ok := h.inFlight[strings.ToLower(fmt.Sprintf("%s#%d", repo, issueNumber))]
	return ok
}

// reserve marks the issue as running here, unless it already is.
func (h *Handler) reserve(key string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, busy := h.inFlight[key]; busy {
		return false
	}
	h.inFlight[key] = struct{}{}
	return true
}

func (h *Handler) release(key string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.inFlight, key)
}

func (h *Handler) timeout() time.Duration {
	if h.cfg.Timeout > 0 {
		return h.cfg.Timeout
	}
	return defaultTimeout
}

// payload is the trigger payload the extension's TriggerCommandHandler reads.
type payload struct {
	Owner              string          `json:"owner"`
	Repo               string          `json:"repo"`
	IssueNumber        int             `json:"issueNumber"`
	Adapter            json.RawMessage `json:"adapter"`
	Model              json.RawMessage `json:"model"`
	Visibility         json.RawMessage `json:"visibility"`
	ConversationTurnID json.RawMessage `json:"conversationTurnId"`
	PosterIsOperator   json.RawMessage `json:"posterIsOperator"`
}

// handle decides one trigger, answers it, and runs it when it was accepted.
func (h *Handler) handle(agentID string, cmd platform.PendingCommand) {
	req, key, detail := h.decide(cmd)
	if detail != "" {
		h.reject(agentID, cmd.ID, detail)
		return
	}
	defer h.release(key)

	ackCtx, cancel := context.WithTimeout(h.ctx, h.timeout())
	runID, err := h.cfg.Acks.AcknowledgeAgentCommand(ackCtx, agentID, cmd.ID)
	cancel()
	switch {
	case errors.Is(err, platform.ErrAgentCommandAlreadyAcked):
		log.Printf("[nightgauge] trigger %s: already acknowledged elsewhere; nothing runs here", cmd.ID)
		return
	case err != nil:
		// The command is still unanswered, so the platform delivers it again:
		// release the issue, then the claim, so that delivery is decided anew.
		log.Printf("[nightgauge] trigger %s: acknowledging failed; nothing runs, a redelivery is decided again: %v", cmd.ID, err)
		h.release(key)
		h.forget(cmd.ID)
		return
	case !runIDPattern.MatchString(runID):
		// A run that could never be closed on the platform is not started.
		log.Printf("[nightgauge] trigger %s: the ack returned no usable run id; nothing runs", cmd.ID)
		return
	}
	req.RemoteRunID = runID
	log.Printf("[nightgauge] trigger %s: %s#%d acknowledged for agent %s; running here (platform run %s)",
		cmd.ID, req.Repo, req.IssueNumber, agentID, runID)
	h.start(cmd.ID, req)
	h.run(cmd.ID, req)
}

// start creates the platform run req.RemoteRunID with its start event, so the
// terminal report has a run to close (#3699). A failure is logged and the run
// goes ahead: the work is still done locally.
func (h *Handler) start(commandID string, req Request) {
	if h.cfg.Reports == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(h.ctx), h.timeout())
	defer cancel()
	err := h.sendWithRetry(ctx, func(ctx context.Context) error {
		return h.cfg.Reports.ReportRemoteRunStart(ctx, req.RemoteRunID, req.Repo, req.IssueNumber, req.Visibility)
	})
	if err != nil {
		log.Printf("[nightgauge] trigger %s: starting platform run %s failed: %v", commandID, req.RemoteRunID, err)
		return
	}
	log.Printf("[nightgauge] trigger %s: platform run %s started", commandID, req.RemoteRunID)
}

// decide parses the trigger and applies the closed list of refusals in order.
// A non-empty detail is the refusal; otherwise key is the reserved issue,
// which the caller releases.
func (h *Handler) decide(cmd platform.PendingCommand) (req Request, key, detail string) {
	var p payload
	if err := json.Unmarshal(cmd.Payload, &p); err != nil ||
		strings.TrimSpace(p.Owner) == "" || strings.TrimSpace(p.Repo) == "" || p.IssueNumber <= 0 {
		return Request{}, "", DetailInvalidCommand
	}
	declared, covered, err := h.covers(p.Owner, p.Repo)
	if err != nil {
		log.Printf("[nightgauge] trigger %s: reading the workspace this daemon serves failed: %v", cmd.ID, err)
		return Request{}, "", DetailWorkspaceUnreadable
	}
	if !covered {
		return Request{}, "", fmt.Sprintf("not-covered: this agent's workspace does not include %s/%s", p.Owner, p.Repo)
	}
	// A conversation's run runs only for this host's operator (#1894).
	if (present(p.ConversationTurnID) || present(p.PosterIsOperator)) && string(p.PosterIsOperator) != "true" {
		return Request{}, "", DetailOperatorRequired
	}
	req = Request{
		Repo:        declared.Owner + "/" + declared.Repo,
		IssueNumber: p.IssueNumber,
	}
	// Only an exact "private" asks for a private run (#2400), as the
	// extension reads it; anything else is the default, team.
	if v, ok := optionalString(p.Visibility); ok && v == state.VisibilityPrivate {
		req.Visibility = state.VisibilityPrivate
	}
	if present(p.Adapter) || present(p.Model) {
		adapter, aok := optionalString(p.Adapter)
		model, mok := optionalString(p.Model)
		if !aok || !mok {
			return Request{}, "", DetailInvalidPin
		}
		if h.cfg.ValidatePin == nil {
			return Request{}, "", DetailPinUnavailable
		}
		if refusal := h.cfg.ValidatePin(req.Repo, adapter, model); refusal != "" {
			return Request{}, "", refusal
		}
		req.Adapter, req.Model = adapter, model
	}
	key = strings.ToLower(fmt.Sprintf("%s#%d", req.Repo, req.IssueNumber))
	busy := fmt.Sprintf("already-running: %s#%d already has a live or queued run on this agent", req.Repo, req.IssueNumber)
	if h.cfg.Busy != nil && h.cfg.Busy(req.Repo, req.IssueNumber) {
		return Request{}, "", busy
	}
	if !h.reserve(key) {
		return Request{}, "", busy
	}
	return req, key, ""
}

// covers reports whether this daemon declares owner/repo, and the repository
// as it declares it. GitHub names are case-insensitive.
func (h *Handler) covers(owner, repo string) (platform.AgentRepo, bool, error) {
	if h.cfg.Served == nil {
		return platform.AgentRepo{}, false, nil
	}
	repos, err := h.cfg.Served()
	if err != nil {
		return platform.AgentRepo{}, false, err
	}
	for _, r := range repos {
		if strings.EqualFold(r.Owner, owner) && strings.EqualFold(r.Repo, repo) && r.Owner != "" && r.Repo != "" {
			return r, true, nil
		}
	}
	return platform.AgentRepo{}, false, nil
}

func (h *Handler) reject(agentID, commandID, detail string) {
	ctx, cancel := context.WithTimeout(h.ctx, h.timeout())
	defer cancel()
	detail = publicDetail(detail)
	err := h.cfg.Acks.RejectAgentCommand(ctx, agentID, commandID, detail)
	switch {
	case err == nil:
		log.Printf("[nightgauge] trigger %s: rejected: %s", commandID, detail)
	case errors.Is(err, platform.ErrAgentCommandAlreadyAcked):
		log.Printf("[nightgauge] trigger %s: already acknowledged elsewhere", commandID)
	default:
		// Unanswered: a redelivery is decided again.
		log.Printf("[nightgauge] trigger %s: rejecting failed (%s): %v", commandID, detail, err)
		h.forget(commandID)
	}
}

// run executes req and reports its one terminal status for req.RemoteRunID.
func (h *Handler) run(commandID string, req Request) {
	var once sync.Once
	report := func(status platform.RemoteRunStatus) {
		once.Do(func() {
			// The report outlives a cancelled run: it is what tells the
			// platform the run ended.
			ctx, cancel := context.WithTimeout(context.WithoutCancel(h.ctx), h.timeout())
			defer cancel()
			if h.cfg.Reports == nil {
				log.Printf("[nightgauge] trigger %s: no report path; platform run %s not closed", commandID, req.RemoteRunID)
				return
			}
			if err := h.sendWithRetry(ctx, func(ctx context.Context) error {
				return h.cfg.Reports.ReportRemoteRunTerminal(ctx, req.RemoteRunID, status, req.Visibility)
			}); err != nil {
				log.Printf("[nightgauge] trigger %s: reporting %s for platform run %s failed: %v", commandID, status, req.RemoteRunID, err)
				return
			}
			log.Printf("[nightgauge] trigger %s: platform run %s reported %s", commandID, req.RemoteRunID, status)
		})
	}
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[nightgauge] trigger %s: the run panicked: %v", commandID, r)
			report(platform.RemoteRunFailure)
		}
	}()
	var (
		res Result
		err error
	)
	if h.cfg.Runner == nil {
		err = errors.New("no runner")
	} else {
		res, err = h.cfg.Runner.Run(h.ctx, req)
	}
	if err != nil {
		log.Printf("[nightgauge] trigger %s: the run of %s#%d ended with an error: %v", commandID, req.Repo, req.IssueNumber, err)
	}
	report(Status(h.ctx, res, err))
}

// sendWithRetry sends a start or terminal report, re-sending a transient
// failure with doubling backoff until it lands, the failure is permanent, or
// ctx ends.
func (h *Handler) sendWithRetry(ctx context.Context, send func(context.Context) error) error {
	delay := h.cfg.ReportRetryDelay
	if delay <= 0 {
		delay = defaultReportRetryDelay
	}
	for {
		err := send(ctx)
		if err == nil || platform.IsPermanentReportError(err) {
			return err
		}
		t := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			t.Stop()
			return fmt.Errorf("%w (gave up: %v)", err, ctx.Err())
		case <-t.C:
		}
		delay *= 2
	}
}

// Status maps a run's end to the one terminal status reported for it, in
// this precedence: success when the item completed or its terminal kind is
// not a failure (even if ctx was cancelled meanwhile); else cancelled when ctx
// was cancelled; else failure, including an item that never dispatched and a
// runner error.
func Status(ctx context.Context, res Result, err error) platform.RemoteRunStatus {
	if err == nil && (res.Completed || orchestrator.IsNonFailureTerminalKind(res.TerminalKind)) {
		return platform.RemoteRunSuccess
	}
	if ctx.Err() != nil {
		return platform.RemoteRunCancelled
	}
	return platform.RemoteRunFailure
}

// present reports whether a JSON field was sent. A JSON null counts as sent,
// as the extension's `!== undefined` reads it.
func present(raw json.RawMessage) bool { return len(raw) > 0 }

// optionalString reads an optional string field: absent is "", true; a
// non-string value is false.
func optionalString(raw json.RawMessage) (string, bool) {
	if !present(raw) {
		return "", true
	}
	var s string
	if string(raw) == "null" || json.Unmarshal(raw, &s) != nil {
		return "", false
	}
	return s, true
}

var idPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,100}$`)

// runIDPattern is the UUID the platform's ack returns and the terminal report
// requires.
var runIDPattern = regexp.MustCompile(
	`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

func validID(v string) bool { return idPattern.MatchString(v) }

// publicDetail is detail as the hosted service may show it: credential shapes
// redacted, control and format characters replaced by a space, cut to the
// service's bound on a UTF-8 boundary.
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
