package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/nightgauge/nightgauge/internal/platform"
)

// The attention tools list and act on the Needs Attention cards of the
// turn's target workspace (#1587). Every call goes to the hosted service's
// turn-scoped routes on this turn's execution lease, never to the plain
// attention routes: the hosted service takes the actor from the turn's
// posting account, authorizes an act only when the poster and the host's
// operator both hold the permission, and only for a card in the turn's
// target workspace. Nothing the daemon sends can name an actor. A viewer's
// act, or any other refused act, comes back as a typed error the transcript
// shows. The local attention store is never touched from a turn.

// Attention is the hosted service's turn-scoped attention routes, bound to
// one turn's lease.
type Attention interface {
	ListCards(ctx context.Context, q platform.TurnCardQuery) (platform.TurnCardList, error)
	ActOnCard(ctx context.Context, cardID string, act platform.TurnCardAct) (platform.TurnCardActResult, error)
}

const (
	// maxActsPerTurn bounds how many card acts one turn may make.
	maxActsPerTurn = 5
	// maxCardTextChars is the hosted service's bound on steer and comment
	// text.
	maxCardTextChars = 2000
	// maxCardBodyChars bounds a card's body as the list shows it.
	maxCardBodyChars = 2000
	// attentionTimeout bounds one hosted call.
	attentionTimeout = 20 * time.Second
)

var (
	cardIDPattern   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,199}$`)
	optionIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,99}$`)
	cursorPattern   = regexp.MustCompile(`^[A-Za-z0-9_.:=+/-]{1,500}$`)
	hostedCode      = regexp.MustCompile(`^[A-Z][A-Z_]{0,39}$`)
)

// cardStates are the lifecycle states list_attention_cards filters on.
var cardStates = []string{"open", "acknowledged", "resolved", "expired"}

// cardVerbs are the acts act_on_attention_card makes.
var cardVerbs = []string{platform.TurnCardResolve, platform.TurnCardComment, platform.TurnCardAcknowledge, platform.TurnCardMute}

// listAttentionCards lists the target workspace's cards.
func (s *Toolset) listAttentionCards(ctx context.Context, raw json.RawMessage) (string, *Error) {
	var a struct {
		Status string `json:"status"`
		Limit  *int   `json:"limit"`
		Cursor string `json:"cursor"`
	}
	if err := decode(raw, &a); err != nil {
		return "", err
	}
	q := platform.TurnCardQuery{Status: a.Status, Cursor: a.Cursor}
	if a.Status != "" && !contains(cardStates, a.Status) {
		return "", fail(CodeInvalidArguments, "status must be one of "+strings.Join(cardStates, ", "))
	}
	if a.Limit != nil {
		if *a.Limit < 1 || *a.Limit > platform.TurnCardListMaxLimit {
			return "", fail(CodeInvalidArguments, fmt.Sprintf("limit must be 1 to %d", platform.TurnCardListMaxLimit))
		}
		q.Limit = *a.Limit
	}
	if a.Cursor != "" && !cursorPattern.MatchString(a.Cursor) {
		return "", fail(CodeInvalidArguments, "cursor must be one a previous page returned")
	}
	if s.env.Attention == nil {
		return "", fail(CodeHostedUnavailable, "the hosted service's attention routes are not available to this turn")
	}
	ctx, cancel := context.WithTimeout(ctx, attentionTimeout)
	defer cancel()
	page, err := s.env.Attention.ListCards(ctx, q)
	if err != nil {
		return "", hostedErr(err)
	}
	type option struct {
		ID    string `json:"id"`
		Label string `json:"label"`
	}
	type card struct {
		ID            string   `json:"id"`
		Kind          string   `json:"kind"`
		Severity      string   `json:"severity"`
		State         string   `json:"state"`
		Title         string   `json:"title"`
		Body          string   `json:"body,omitempty"`
		Repo          string   `json:"repo,omitempty"`
		Issue         int      `json:"issue,omitempty"`
		PullRequest   int      `json:"pull_request,omitempty"`
		RunID         string   `json:"run_id,omitempty"`
		Options       []option `json:"options"`
		DefaultOption string   `json:"default_option,omitempty"`
		TakesSteer    bool     `json:"takes_steer,omitempty"`
		CreatedAt     string   `json:"created_at,omitempty"`
		ExpiresAt     string   `json:"expires_at,omitempty"`
	}
	cards := make([]card, 0, len(page.Items))
	withheld := 0
	for _, c := range page.Items {
		// The hosted service lists only the turn's target workspace; a card
		// of any other is withheld whatever it says (defense in depth).
		if page.WorkspaceID == "" || c.WorkspaceID != page.WorkspaceID {
			withheld++
			continue
		}
		opts := make([]option, 0, len(c.Options))
		for _, o := range c.Options {
			opts = append(opts, option{ID: o.ID, Label: o.Label})
		}
		cards = append(cards, card{
			ID: c.ID, Kind: c.Kind, Severity: c.Severity, State: c.Lifecycle.State, Title: c.Title,
			Body: clip(c.Body, maxCardBodyChars), Repo: c.Context.Repo, Issue: c.Context.Issue, PullRequest: c.Context.PR,
			RunID: c.Context.RunID, Options: opts, DefaultOption: c.DefaultAction, TakesSteer: c.Steer != nil && c.Steer.Enabled,
			CreatedAt: c.CreatedAt, ExpiresAt: c.ExpiresAt,
		})
	}
	content := map[string]any{"workspace_id": page.WorkspaceID, "cards": cards}
	if withheld > 0 {
		content["withheld_outside_workspace"] = withheld
	}
	if page.NextCursor != nil {
		content["next_cursor"] = *page.NextCursor
	}
	return untrusted("the hosted service's Needs Attention cards", content), nil
}

// hostedErr is the error the model and the transcript are shown for a
// hosted call that failed: the hosted service's own code, lower-cased, when
// it refused, and hosted_unavailable when it could not be reached.
func hostedErr(err error) *Error {
	var refused *platform.TurnRequestError
	if errors.As(err, &refused) && hostedCode.MatchString(refused.Code) {
		return fail(strings.ToLower(refused.Code), hostedRefusal(refused.Code))
	}
	if errors.As(err, &refused) {
		return fail(CodeHostedRefused, fmt.Sprintf("the hosted service refused the call (HTTP %d); nothing changed", refused.Status))
	}
	return fail(CodeHostedUnavailable, "the hosted service could not be reached; nothing is known to have changed")
}

// hostedRefusal explains a hosted refusal code.
func hostedRefusal(code string) string {
	switch code {
	case "ACT_NOT_PERMITTED":
		return "the poster's role, or the host operator's, does not permit acting on that card; nothing changed"
	case "CARD_OUTSIDE_TARGET":
		return "that card is not in this turn's workspace; nothing changed"
	case "ATTENTION_REFUSED":
		return "the card refused the act: it may already be resolved, or the option may not be one of its own; nothing changed"
	case "READ_NOT_PERMITTED":
		return "the poster, or the host operator, may not read this workspace's cards"
	case "TURN_ENDED":
		return "this turn is no longer running on the hosted service"
	case "NOT_FOUND":
		return "no such card in this workspace, or this turn is not this host's"
	case "LEASE_INVALID":
		return "the hosted service refused this turn's lease"
	case "VALIDATION_ERROR":
		return "the hosted service refused the arguments"
	default:
		return "the hosted service refused the call"
	}
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
