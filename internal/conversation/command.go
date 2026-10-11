// Package conversation is the daemon's conversation turn host (#1569,
// ADR-017): it receives the two turn command types on the daemon's own agent
// command stream, claims a turn before it runs anything, answers it on the
// workspace's own adapter, performance mode and effort, streams the redacted
// reply back as frames, and stops a turn on a cancel, reporting it cancelled
// only after its process group is verified dead.
//
// docs/CONVERSATION_TURNS.md states this side of the contract.
package conversation

import (
	"encoding/json"
	"fmt"
	"regexp"
	"time"
)

// Defaults the hosted service applies, used when a command omits a value.
// Every turn command carries the values that apply to it.
const (
	defaultTokenCeiling         = 200000
	defaultTurnTimeout          = 600 * time.Second
	defaultMaxDeltas            = 1000
	defaultMaxReplyBytes        = 256 << 10
	defaultCancelConfirmSeconds = 30
	maxIDLen                    = 100
)

// Repo is one repository the poster may read.
type Repo struct {
	Owner string `json:"owner"`
	Repo  string `json:"repo"`
}

// Limits are the turn's frame bounds.
type Limits struct {
	MaxDeltaBytes        int `json:"max_delta_bytes"`
	MaxDeltas            int `json:"max_deltas"`
	MaxReplyBytes        int `json:"max_reply_bytes"`
	MaxFramesPerRequest  int `json:"max_frames_per_request"`
	CancelConfirmSeconds int `json:"cancel_confirm_seconds"`
}

// TurnCommand is a `conversation_turn` payload. It carries ids and routing
// metadata, never message text: the text comes from the context read, on the
// turn's lease.
//
// Nothing here names a model, an adapter, a performance mode or an effort.
// The profile is resolved on the workspace; a payload that carries any of
// those fields has them ignored, because decoding drops every field this
// struct does not declare.
type TurnCommand struct {
	ConversationID    string    `json:"conversation_id"`
	TurnID            string    `json:"turn_id"`
	TargetWorkspaceID string    `json:"target_workspace_id"`
	PosterAccountID   string    `json:"poster_account_id"`
	PosterGitHubLogin *string   `json:"poster_github_login"`
	PosterIsOperator  bool      `json:"poster_is_operator"`
	ReadableRepos     []Repo    `json:"readable_repos"`
	Visibility        string    `json:"visibility"`
	DeadlineAt        time.Time `json:"deadline_at"`
	AckDeadlineAt     time.Time `json:"ack_deadline_at"`
	TokenCeiling      int       `json:"token_ceiling"`
	Limits            Limits    `json:"limits"`
}

// CancelCommand is a `conversation_turn_cancel` payload.
type CancelCommand struct {
	ConversationID string `json:"conversation_id"`
	TurnID         string `json:"turn_id"`
	TurnCommandID  string `json:"turn_command_id"`
	// Reason is `cancelled` when a teammate cancelled the turn. Every other
	// reason (timeout, chunk_limit, secret_detected, agent_lost,
	// token_ceiling) means the hosted service already ended it.
	Reason string `json:"reason"`
}

// CancelReasonTeammate is the reason of a cancel a teammate asked for: the
// only cancel that ends a turn `cancelled`.
const CancelReasonTeammate = "cancelled"

// ParseTurnCommand decodes and validates a turn payload, filling the hosted
// service's defaults for omitted bounds.
func ParseTurnCommand(raw json.RawMessage, now time.Time) (TurnCommand, error) {
	var c TurnCommand
	if err := json.Unmarshal(raw, &c); err != nil {
		return TurnCommand{}, fmt.Errorf("conversation_turn payload: %w", err)
	}
	if err := checkID("conversation_id", c.ConversationID); err != nil {
		return TurnCommand{}, err
	}
	if err := checkID("turn_id", c.TurnID); err != nil {
		return TurnCommand{}, err
	}
	if c.TokenCeiling <= 0 {
		c.TokenCeiling = defaultTokenCeiling
	}
	if c.DeadlineAt.IsZero() {
		c.DeadlineAt = now.Add(defaultTurnTimeout)
	}
	l := &c.Limits
	l.MaxDeltaBytes = bound(l.MaxDeltaBytes, 1, maxDeltaBytes, maxDeltaBytes)
	l.MaxDeltas = bound(l.MaxDeltas, 1, 1_000_000, defaultMaxDeltas)
	l.MaxReplyBytes = bound(l.MaxReplyBytes, 1, 64<<20, defaultMaxReplyBytes)
	l.MaxFramesPerRequest = bound(l.MaxFramesPerRequest, 1, maxFramesPerRequest, maxFramesPerRequest)
	l.CancelConfirmSeconds = bound(l.CancelConfirmSeconds, 1, 600, defaultCancelConfirmSeconds)
	return c, nil
}

// ParseCancelCommand decodes and validates a cancel payload.
func ParseCancelCommand(raw json.RawMessage) (CancelCommand, error) {
	var c CancelCommand
	if err := json.Unmarshal(raw, &c); err != nil {
		return CancelCommand{}, fmt.Errorf("conversation_turn_cancel payload: %w", err)
	}
	if err := checkID("turn_id", c.TurnID); err != nil {
		return CancelCommand{}, err
	}
	if err := checkID("turn_command_id", c.TurnCommandID); err != nil {
		return CancelCommand{}, err
	}
	return c, nil
}

// idPattern is what an id from the hosted service may be: it reaches URL
// paths and log lines, so nothing that could change either (a slash, a dot
// segment, a line break, an escape) gets through.
var idPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,100}$`)

func validID(v string) bool { return idPattern.MatchString(v) }

func checkID(field, v string) error {
	if !validID(v) {
		return fmt.Errorf("turn payload: %s must be 1 to %d of [A-Za-z0-9_-]", field, maxIDLen)
	}
	return nil
}

// bound returns v within [lo, hi], or def when v is unset (zero or less).
func bound(v, lo, hi, def int) int {
	switch {
	case v <= 0:
		return def
	case v < lo:
		return lo
	case v > hi:
		return hi
	}
	return v
}
