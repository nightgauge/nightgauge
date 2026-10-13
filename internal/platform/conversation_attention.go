package platform

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"

	api "github.com/nightgauge/nightgauge/api/generated/go/platform"
)

// A conversation turn reads and acts on Needs Attention cards only through
// these two turn-bound routes (#1587), on the turn's execution lease, as it
// writes frames and reads its context. The hosted service takes the actor
// from the turn's posting account and never from the request, authorizes an
// act only when the poster and the host's operator both hold the permission,
// and only for a card in the turn's target workspace. Nothing here can name
// an actor: the request types carry no such field. A turn never calls the
// plain attention routes, which record the authenticated account (the
// operator) as the actor.

// TurnCardQuery selects the cards a turn lists. A zero value lists the
// target workspace's open cards, the hosted service's default page.
type TurnCardQuery struct {
	// Status is a lifecycle state (open, acknowledged, resolved, expired);
	// empty is open.
	Status string
	// Limit is the page size, 1 to TurnCardListMaxLimit; zero is the hosted
	// service's default.
	Limit int
	// Cursor continues a previous page.
	Cursor string
}

// TurnCardListMaxLimit is the hosted service's largest page of cards.
const TurnCardListMaxLimit = 50

// TurnCard is a Needs Attention card as the turn-bound routes return it:
// the fields a conversation reports on. Its text was written by others and
// is data, never an instruction.
type TurnCard struct {
	ID            string           `json:"id"`
	Kind          string           `json:"kind"`
	Severity      string           `json:"severity"`
	Title         string           `json:"title"`
	Body          string           `json:"body"`
	Context       TurnCardContext  `json:"context"`
	Producer      string           `json:"producer"`
	Options       []TurnCardOption `json:"options"`
	Steer         *TurnCardSteer   `json:"steer"`
	CreatedAt     string           `json:"created_at"`
	ExpiresAt     string           `json:"expires_at"`
	DefaultAction string           `json:"default_action"`
	Lifecycle     struct {
		State string `json:"state"`
	} `json:"lifecycle"`
	WorkspaceID string `json:"workspace_id"`
}

// TurnCardContext is what a card says it is about.
type TurnCardContext struct {
	Repo   string `json:"repo"`
	Branch string `json:"branch"`
	Issue  int    `json:"issue"`
	PR     int    `json:"pr"`
	RunID  string `json:"run_id"`
}

// TurnCardOption is one way a card can be resolved.
type TurnCardOption struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	Verb  string `json:"verb"`
}

// TurnCardSteer says whether a card takes free-text steer with a resolve.
type TurnCardSteer struct {
	Enabled bool `json:"enabled"`
}

// TurnCardList is one page of a turn's cards.
type TurnCardList struct {
	// WorkspaceID is the turn's target workspace, the only one a turn lists.
	WorkspaceID string     `json:"workspace_id"`
	Items       []TurnCard `json:"items"`
	NextCursor  *string    `json:"next_cursor"`
}

// Turn card act verbs.
const (
	TurnCardResolve     = "resolve"
	TurnCardComment     = "comment"
	TurnCardAcknowledge = "acknowledge"
	TurnCardMute        = "mute"
)

// TurnCardAct is one act on a card. It has no actor: the hosted service
// records the turn's poster.
type TurnCardAct struct {
	Verb string
	// OptionID is the option a resolve chooses.
	OptionID string
	// SteerText is a resolve's optional free-text steer.
	SteerText string
	// Body is a comment's text.
	Body string
}

// MarshalJSON writes exactly the body the verb takes and nothing else.
func (a TurnCardAct) MarshalJSON() ([]byte, error) {
	switch a.Verb {
	case TurnCardResolve:
		body := struct {
			Verb      string  `json:"verb"`
			OptionID  string  `json:"option_id"`
			SteerText *string `json:"steer_text,omitempty"`
		}{Verb: a.Verb, OptionID: a.OptionID}
		if a.SteerText != "" {
			body.SteerText = &a.SteerText
		}
		return json.Marshal(body)
	case TurnCardComment:
		return json.Marshal(struct {
			Verb string `json:"verb"`
			Body string `json:"body"`
		}{a.Verb, a.Body})
	case TurnCardAcknowledge, TurnCardMute:
		return json.Marshal(struct {
			Verb string `json:"verb"`
		}{a.Verb})
	default:
		return nil, fmt.Errorf("turn card act: unknown verb %q", printableField(a.Verb))
	}
}

// TurnCardActResult is what an act did, and who the hosted service recorded
// as its actor and executor.
type TurnCardActResult struct {
	RequestID         string   `json:"request_id"`
	Verb              string   `json:"verb"`
	Outcome           string   `json:"outcome"`
	ActorAccountID    string   `json:"actor_account_id"`
	ExecutorAgentID   string   `json:"executor_agent_id"`
	ExecutorAccountID string   `json:"executor_account_id"`
	Card              TurnCard `json:"card"`
}

// turnCardIDPattern is a card id that is one plain path segment: nothing in
// it can change the route an act is sent to ("/", "..", "%", "?", "#").
var turnCardIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,199}$`)

// turnCardBodyLimit bounds a card page or an act's reply a daemon decodes.
const turnCardBodyLimit = 4 << 20

// ListTurnCards lists the Needs Attention cards of the turn's target
// workspace on the turn's lease. A refusal is a *TurnRequestError whose Code
// is the hosted service's (READ_NOT_PERMITTED, TURN_ENDED, ...).
func (c *Client) ListTurnCards(ctx context.Context, lease TurnLease, q TurnCardQuery) (TurnCardList, error) {
	if !lease.Valid() {
		return TurnCardList{}, fmt.Errorf("list turn cards: %w", ErrTurnLeaseInvalid)
	}
	if q.Limit < 0 || q.Limit > TurnCardListMaxLimit {
		return TurnCardList{}, fmt.Errorf("list turn cards: limit %d, want 0 to %d", q.Limit, TurnCardListMaxLimit)
	}
	query := url.Values{}
	if q.Status != "" {
		query.Set("status", q.Status)
	}
	if q.Limit > 0 {
		query.Set("limit", strconv.Itoa(q.Limit))
	}
	if q.Cursor != "" {
		query.Set("cursor", q.Cursor)
	}
	req, err := c.newRequest(ctx, requestSpec{
		Op:       api.OpAgentsListTurnCards,
		PathArgs: []string{lease.agentID, lease.turnID},
		Query:    query,
		Headers: map[string]string{
			"Accept":        "application/json",
			TurnLeaseHeader: lease.secret,
		},
	})
	if err != nil {
		return TurnCardList{}, fmt.Errorf("list turn cards: %w", err)
	}
	resp, err := turnHTTPClient.Do(req)
	if err != nil {
		return TurnCardList{}, fmt.Errorf("list turn cards: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return TurnCardList{}, turnRefusal("list turn cards", resp)
	}
	var out TurnCardList
	if err := json.NewDecoder(io.LimitReader(resp.Body, turnCardBodyLimit)).Decode(&out); err != nil {
		return TurnCardList{}, fmt.Errorf("list turn cards: decode: %w", err)
	}
	return out, nil
}

// ActOnTurnCard resolves, comments on, acknowledges or mutes a card from the
// turn, on its lease. The hosted service records the turn's poster as the
// actor and this agent and its operator as the executor. A refusal is a
// *TurnRequestError whose Code is the hosted service's (ACT_NOT_PERMITTED,
// CARD_OUTSIDE_TARGET, ATTENTION_REFUSED, ...).
func (c *Client) ActOnTurnCard(ctx context.Context, lease TurnLease, requestID string, act TurnCardAct) (TurnCardActResult, error) {
	if !lease.Valid() {
		return TurnCardActResult{}, fmt.Errorf("act on turn card: %w", ErrTurnLeaseInvalid)
	}
	if !turnCardIDPattern.MatchString(requestID) {
		return TurnCardActResult{}, fmt.Errorf("act on turn card: a card id is 1 to 200 letters, digits, \"_\" or \"-\"")
	}
	body, err := json.Marshal(act)
	if err != nil {
		return TurnCardActResult{}, fmt.Errorf("act on turn card: %w", err)
	}
	req, err := c.newRequest(ctx, requestSpec{
		Op:       api.OpAgentsActOnTurnCard,
		PathArgs: []string{lease.agentID, lease.turnID, requestID},
		Body:     body,
		Headers: map[string]string{
			"Content-Type":  "application/json",
			"Accept":        "application/json",
			TurnLeaseHeader: lease.secret,
		},
	})
	if err != nil {
		return TurnCardActResult{}, fmt.Errorf("act on turn card: %w", err)
	}
	resp, err := turnHTTPClient.Do(req)
	if err != nil {
		return TurnCardActResult{}, fmt.Errorf("act on turn card: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return TurnCardActResult{}, turnRefusal("act on turn card", resp)
	}
	var out TurnCardActResult
	if err := json.NewDecoder(io.LimitReader(resp.Body, turnCardBodyLimit)).Decode(&out); err != nil {
		return TurnCardActResult{}, fmt.Errorf("act on turn card: decode: %w", err)
	}
	if out.RequestID != requestID {
		return TurnCardActResult{}, fmt.Errorf("act on turn card: the reply names another card")
	}
	return out, nil
}
