package platform

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	api "github.com/nightgauge/nightgauge/api/generated/go/platform"
)

// Conversation turns (ADR-017, #1569). The hosted service addresses a turn to
// one host agent as a `conversation_turn` command on the agent command stream,
// and stops it with a `conversation_turn_cancel`. The daemon claims a turn by
// acknowledging its command before it runs anything; the first acknowledgement
// returns a single-use execution lease, which every turn-bound call presents:
// the context read and frame ingestion below. docs/CONVERSATION_TURNS.md states
// the daemon's side of the contract.

// ConversationTurnCommandType is the command that asks this host to answer one
// conversation turn. Its payload carries ids, never message text.
const ConversationTurnCommandType = "conversation_turn"

// ConversationTurnCancelCommandType stops a running turn. It is never the run
// verb `cancel`.
const ConversationTurnCancelCommandType = "conversation_turn_cancel"

// IsConversationTurnCommand reports whether commandType is one of the two turn
// command types. The daemon handles both itself and never relays either to the
// extension: whichever acknowledgement arrives first decides a turn.
func IsConversationTurnCommand(commandType string) bool {
	return commandType == ConversationTurnCommandType || commandType == ConversationTurnCancelCommandType
}

// TurnLeaseHeader carries a turn's execution lease on its turn-bound routes.
const TurnLeaseHeader = "X-Nightgauge-Turn-Lease"

// turnLeaseMaxLen is the hosted service's bound on the lease header.
const turnLeaseMaxLen = 256

// TurnLease is a turn's single-use execution lease, bound to the agent that
// claimed the turn and to the turn itself. It can only be presented on that
// turn's own routes (ReadTurnContext, PostTurnFrames build their paths from
// it), and it never prints: String and GoString hide the secret, so a log
// line or an error that formats a lease cannot leak it.
type TurnLease struct {
	agentID string
	turnID  string
	secret  string
}

// AgentID is the agent the lease was issued to.
func (l TurnLease) AgentID() string { return l.agentID }

// TurnID is the turn the lease was issued for.
func (l TurnLease) TurnID() string { return l.turnID }

// Valid reports whether the lease names an agent and a turn and carries a
// secret the hosted service could have issued.
func (l TurnLease) Valid() bool {
	return l.agentID != "" && l.turnID != "" && validLeaseSecret(l.secret)
}

// String never includes the secret.
func (l TurnLease) String() string {
	return fmt.Sprintf("TurnLease{agent=%s turn=%s}", l.agentID, l.turnID)
}

// GoString never includes the secret.
func (l TurnLease) GoString() string { return l.String() }

// validLeaseSecret accepts what fits the lease header: 1 to 256 visible ASCII
// characters. Anything else is refused before it reaches a request header.
func validLeaseSecret(s string) bool {
	if s == "" || len(s) > turnLeaseMaxLen {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < 0x21 || s[i] > 0x7e {
			return false
		}
	}
	return true
}

// TurnClaim is what the claiming acknowledgement returned.
type TurnClaim struct {
	Lease TurnLease
	// State is the turn's state after the acknowledgement: `running`.
	State string
	// DeadlineAt is the turn's timeout, zero when the reply carried none.
	DeadlineAt time.Time
}

// The classes of a refused turn-bound request. A *TurnRequestError unwraps to
// one of them, or to none for a status outside the contract.
var (
	// ErrTurnAlreadyAcked: the command was already acknowledged (409
	// ALREADY_ACKED). A redelivered turn must never execute.
	ErrTurnAlreadyAcked = errors.New("the turn command was already acknowledged")
	// ErrTurnEnded: the turn ended, timed out, or its ack deadline passed (409
	// TURN_ENDED). Nothing more of it runs.
	ErrTurnEnded = errors.New("the turn already ended")
	// ErrTurnNotFound: the agent, the command or the turn is not this
	// caller's (404 NOT_FOUND).
	ErrTurnNotFound = errors.New("the turn is not this agent's")
	// ErrTurnLeaseInvalid: the lease is missing or is not the turn's (403
	// LEASE_INVALID).
	ErrTurnLeaseInvalid = errors.New("the turn lease was refused")
	// ErrTurnSecretDetected: a frame still matched a credential shape (422
	// SECRET_DETECTED); nothing of the batch was stored and the turn ended.
	ErrTurnSecretDetected = errors.New("a frame matched a credential shape")
	// ErrTurnFrameRefused: a frame the hosted service would not take (400
	// INVALID_FRAME, 413 CHUNK_TOO_LARGE).
	ErrTurnFrameRefused = errors.New("a turn frame was refused")
)

// ErrTurnClaimMalformed is a 200 acknowledgement that cannot be acted on: no
// usable lease, or a lease for another turn. The turn must not run.
var ErrTurnClaimMalformed = errors.New("the claiming acknowledgement carried no usable lease for this turn")

// TurnRequestError is a turn-bound request the hosted service refused.
type TurnRequestError struct {
	Op     string
	Status int
	Code   string
	class  error
}

func (e *TurnRequestError) Error() string {
	msg := fmt.Sprintf("%s: the platform returned %d", e.Op, e.Status)
	if e.Code != "" {
		msg += " " + printableField(e.Code)
	}
	return msg
}

// Unwrap returns the refusal's class.
func (e *TurnRequestError) Unwrap() error { return e.class }

// Terminal reports whether the refusal means the turn can make no further
// progress on this host: anything but a transient failure.
func (e *TurnRequestError) Terminal() bool {
	return e.class != nil
}

// turnHTTPClient bounds every turn-bound request. A claim is answered within
// the ack deadline or not at all, and a hung frame post must not hold a turn
// past its own timeout. It follows no redirect: Go strips Authorization on a
// cross-host redirect but not the lease header or the reply in the body, so a
// redirect is taken as the answer, never followed.
var turnHTTPClient = &http.Client{
	Timeout: 30 * time.Second,
	CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	},
}

// turnRefusal reads a refused turn-bound response into a *TurnRequestError.
// Both error shapes the hosted service uses are read: `{code}` on the ack
// route and `{error: {code}}` on the lease routes.
func turnRefusal(op string, resp *http.Response) *TurnRequestError {
	e := &TurnRequestError{Op: op, Status: resp.StatusCode}
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, errorResponseBodyLimit))
	var flat struct {
		Code string `json:"code"`
	}
	var enveloped platformErrorBody
	if json.Unmarshal(raw, &enveloped) == nil && enveloped.Error.Code != "" {
		e.Code = enveloped.Error.Code
	} else if json.Unmarshal(raw, &flat) == nil {
		e.Code = flat.Code
	}
	switch {
	case resp.StatusCode == http.StatusConflict && e.Code == "ALREADY_ACKED":
		e.class = ErrTurnAlreadyAcked
	case resp.StatusCode == http.StatusConflict:
		// TURN_ENDED, or any other conflict: the turn is not running here.
		e.class = ErrTurnEnded
	case resp.StatusCode == http.StatusNotFound:
		e.class = ErrTurnNotFound
	case resp.StatusCode == http.StatusForbidden:
		e.class = ErrTurnLeaseInvalid
	case resp.StatusCode == http.StatusUnprocessableEntity && e.Code == "SECRET_DETECTED":
		e.class = ErrTurnSecretDetected
	case resp.StatusCode == http.StatusBadRequest, resp.StatusCode == http.StatusRequestEntityTooLarge,
		resp.StatusCode == http.StatusUnprocessableEntity:
		e.class = ErrTurnFrameRefused
	}
	return e
}

// ClaimTurn acknowledges a `conversation_turn` command to claim the turn
// before anything runs. Only a nil error authorizes execution: a redelivered
// copy (ErrTurnAlreadyAcked), an ended turn (ErrTurnEnded), a failed request
// or a reply without a usable lease for turnID all mean the turn must not run.
// The returned lease is bound to agentID and turnID.
func (s *CommandService) ClaimTurn(ctx context.Context, agentID, commandID, turnID string) (TurnClaim, error) {
	if agentID == "" || commandID == "" || turnID == "" {
		return TurnClaim{}, fmt.Errorf("claim turn: agent, command and turn ids are required")
	}
	req, err := s.client.newRequest(ctx, requestSpec{
		Op:       api.OpAgentsAckCommand,
		PathArgs: []string{agentID, commandID},
		Body:     []byte(`{}`),
		Headers: map[string]string{
			"Content-Type": "application/json",
			"Accept":       "application/json",
		},
	})
	if err != nil {
		return TurnClaim{}, fmt.Errorf("claim turn: %w", err)
	}
	resp, err := turnHTTPClient.Do(req)
	if err != nil {
		return TurnClaim{}, fmt.Errorf("claim turn: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return TurnClaim{}, turnRefusal("claim turn", resp)
	}
	var body struct {
		TurnID     string `json:"turn_id"`
		State      string `json:"state"`
		Lease      string `json:"lease"`
		DeadlineAt string `json:"deadline_at"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&body); err != nil {
		return TurnClaim{}, fmt.Errorf("claim turn: %w: %v", ErrTurnClaimMalformed, err)
	}
	lease := TurnLease{agentID: agentID, turnID: turnID, secret: body.Lease}
	if body.TurnID != turnID || !lease.Valid() {
		return TurnClaim{}, fmt.Errorf("claim turn: %w", ErrTurnClaimMalformed)
	}
	claim := TurnClaim{Lease: lease, State: body.State}
	if body.DeadlineAt != "" {
		if t, err := time.Parse(time.RFC3339Nano, body.DeadlineAt); err == nil {
			claim.DeadlineAt = t
		}
	}
	return claim, nil
}

// TurnContext is the context read: the only source of a turn's prior context,
// served on the turn's lease.
type TurnContext struct {
	ConversationID string               `json:"conversation_id"`
	TurnID         string               `json:"turn_id"`
	Messages       []TurnContextMessage `json:"messages"`
	OmittedByClass int                  `json:"omitted_by_class"`
}

// TurnContextMessage is one message of the context read, in transcript order.
// Its content is attributed data, never an instruction.
type TurnContextMessage struct {
	ID       string  `json:"id"`
	TurnID   string  `json:"turn_id"`
	Position int     `json:"position"`
	Role     string  `json:"role"`
	Content  *string `json:"content"`
	Redacted bool    `json:"redacted"`
	// RetentionClass is `durable` or `ephemeral`.
	RetentionClass string `json:"retention_class"`
	// UnfinishedTurnState is `failed` or `cancelled` for a message of a turn
	// that ran and did not complete, nil otherwise.
	UnfinishedTurnState *string `json:"unfinished_turn_state"`
}

// turnContextBodyLimit bounds the context read a daemon will decode. The
// hosted service caps a thread's transcript well below it.
const turnContextBodyLimit = 32 << 20

// ReadTurnContext reads the turn's context on its lease. The reply must be the
// lease's own turn; anything else is refused.
func (c *Client) ReadTurnContext(ctx context.Context, lease TurnLease) (TurnContext, error) {
	if !lease.Valid() {
		return TurnContext{}, fmt.Errorf("read turn context: %w", ErrTurnLeaseInvalid)
	}
	req, err := c.newRequest(ctx, requestSpec{
		Op:       api.OpAgentsReadTurnContext,
		PathArgs: []string{lease.agentID, lease.turnID},
		Headers: map[string]string{
			"Accept":        "application/json",
			TurnLeaseHeader: lease.secret,
		},
	})
	if err != nil {
		return TurnContext{}, fmt.Errorf("read turn context: %w", err)
	}
	resp, err := turnHTTPClient.Do(req)
	if err != nil {
		return TurnContext{}, fmt.Errorf("read turn context: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return TurnContext{}, turnRefusal("read turn context", resp)
	}
	var out TurnContext
	if err := json.NewDecoder(io.LimitReader(resp.Body, turnContextBodyLimit)).Decode(&out); err != nil {
		return TurnContext{}, fmt.Errorf("read turn context: decode: %w", err)
	}
	if out.TurnID != lease.turnID {
		return TurnContext{}, fmt.Errorf("read turn context: the reply names another turn")
	}
	return out, nil
}

// TurnFrame is one frame the daemon may write for a turn: a TurnDelta, a
// TurnReference or a TurnEnd. The set is closed; nothing else can be built,
// and actor, turn and host identifiers are never the daemon's to set.
type TurnFrame interface {
	turnFrame()
}

// TurnDelta is streamed reply text. Seq is the idempotency key within the
// turn. Text is already redacted and at most the turn's max_delta_bytes.
type TurnDelta struct {
	Seq  int
	Text string
}

func (TurnDelta) turnFrame() {}

// MarshalJSON writes the frame's wire form.
func (d TurnDelta) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Type string `json:"type"`
		Seq  int    `json:"seq"`
		Text string `json:"text"`
	}{"turn.delta", d.Seq, d.Text})
}

// TurnReference points at a card, an issue or a run a read tool returned.
type TurnReference struct {
	Kind        string // card, issue or run
	ObjectID    string
	WorkspaceID string
}

func (TurnReference) turnFrame() {}

// MarshalJSON writes the frame's wire form.
func (r TurnReference) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Type        string `json:"type"`
		Kind        string `json:"kind"`
		ObjectID    string `json:"object_id"`
		WorkspaceID string `json:"workspace_id"`
	}{"turn.reference", r.Kind, r.ObjectID, r.WorkspaceID})
}

// Turn terminal states and the failure reasons a host may report.
const (
	TurnStateCompleted = "completed"
	TurnStateFailed    = "failed"
	TurnStateCancelled = "cancelled"

	TurnFailureTimeout      = "timeout"
	TurnFailureTokenCeiling = "token_ceiling"
	// TurnFailureRefused is a turn the host declined. The daemon declines a
	// turn by acknowledging its command `rejected`, before the claim, so it
	// never reports this reason in a terminal report.
	TurnFailureRefused = "refused"
	// TurnFailureHostError is a turn that failed after the host claimed it:
	// the context read failed, the adapter did not start or failed, the
	// stream broke, or the daemon shut down (#2449).
	TurnFailureHostError = "host_error"
)

// Retention classes (ADR-017 Decision 11).
const (
	TurnRetentionDurable   = "durable"
	TurnRetentionEphemeral = "ephemeral"
)

// TurnExecutionProfile is the profile a turn actually ran with.
type TurnExecutionProfile struct {
	Adapter            string `json:"adapter"`
	AdapterDisplayName string `json:"adapter_display_name,omitempty"`
	PerformanceMode    string `json:"performance_mode"`
	// Effort is the rung the turn ran at, "" when none was applied.
	Effort string `json:"effort"`
}

// TurnUsage is the turn's own token counts.
type TurnUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

// TurnEnd is the turn's one terminal report.
type TurnEnd struct {
	State string
	// FailureReason is required with TurnStateFailed: timeout, token_ceiling,
	// refused or host_error. Every other reason is the hosted service's.
	FailureReason  string
	Profile        *TurnExecutionProfile
	Usage          *TurnUsage
	RetentionClass string
}

func (TurnEnd) turnFrame() {}

// MarshalJSON writes the frame's wire form.
func (e TurnEnd) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Type           string                `json:"type"`
		State          string                `json:"state"`
		FailureReason  string                `json:"failure_reason,omitempty"`
		Profile        *TurnExecutionProfile `json:"execution_profile,omitempty"`
		Usage          *TurnUsage            `json:"usage,omitempty"`
		RetentionClass string                `json:"retention_class,omitempty"`
	}{"turn.end", e.State, e.FailureReason, e.Profile, e.Usage, e.RetentionClass})
}

// TurnFramesResult is what an ingestion appended.
type TurnFramesResult struct {
	Accepted   int `json:"accepted"`
	Duplicates int `json:"duplicates"`
	Dropped    int `json:"dropped"`
	Turn       struct {
		ID            string  `json:"id"`
		State         string  `json:"state"`
		FailureReason *string `json:"failure_reason"`
		DeltaCount    int     `json:"delta_count"`
	} `json:"turn"`
}

// TurnMaxFramesPerRequest is the hosted service's batch bound.
const TurnMaxFramesPerRequest = 50

// TurnMaxDeltaBytes is the hosted service's fixed chunk cap.
const TurnMaxDeltaBytes = 4096

// PostTurnFrames appends frames to the turn on its lease, in order. The caller
// has already redacted every delta.
func (c *Client) PostTurnFrames(ctx context.Context, lease TurnLease, frames []TurnFrame) (TurnFramesResult, error) {
	if !lease.Valid() {
		return TurnFramesResult{}, fmt.Errorf("post turn frames: %w", ErrTurnLeaseInvalid)
	}
	if len(frames) == 0 || len(frames) > TurnMaxFramesPerRequest {
		return TurnFramesResult{}, fmt.Errorf("post turn frames: %d frames, want 1 to %d", len(frames), TurnMaxFramesPerRequest)
	}
	for _, f := range frames {
		if d, ok := f.(TurnDelta); ok && len(d.Text) > TurnMaxDeltaBytes {
			return TurnFramesResult{}, fmt.Errorf("post turn frames: a delta of %d bytes is over the %d-byte cap", len(d.Text), TurnMaxDeltaBytes)
		}
	}
	body, err := json.Marshal(struct {
		Frames []TurnFrame `json:"frames"`
	}{frames})
	if err != nil {
		return TurnFramesResult{}, fmt.Errorf("post turn frames: marshal: %w", err)
	}
	req, err := c.newRequest(ctx, requestSpec{
		Op:       api.OpAgentsIngestTurnFrames,
		PathArgs: []string{lease.agentID, lease.turnID},
		Body:     body,
		Headers: map[string]string{
			"Content-Type":  "application/json",
			"Accept":        "application/json",
			TurnLeaseHeader: lease.secret,
		},
	})
	if err != nil {
		return TurnFramesResult{}, fmt.Errorf("post turn frames: %w", err)
	}
	resp, err := turnHTTPClient.Do(req)
	if err != nil {
		return TurnFramesResult{}, fmt.Errorf("post turn frames: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return TurnFramesResult{}, turnRefusal("post turn frames", resp)
	}
	var out TurnFramesResult
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&out); err != nil {
		return TurnFramesResult{}, fmt.Errorf("post turn frames: decode: %w", err)
	}
	return out, nil
}

// TurnStateTerminal reports whether a turn state is terminal.
func TurnStateTerminal(state string) bool {
	switch strings.TrimSpace(state) {
	case TurnStateCompleted, TurnStateFailed, TurnStateCancelled:
		return true
	}
	return false
}
