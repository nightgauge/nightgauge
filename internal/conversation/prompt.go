package conversation

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/nightgauge/nightgauge/internal/platform"
)

// priorMessage is one earlier message as the turn's stdin carries it.
type priorMessage struct {
	Role string `json:"role"`
	Text string `json:"text,omitempty"`
	// Redacted marks a message an owner or admin redacted: its text is gone.
	Redacted bool `json:"redacted,omitempty"`
	// TurnState marks a message of a turn that ran and did not complete
	// (`failed` or `cancelled`), so the model treats its request as
	// unfinished rather than pending.
	TurnState string `json:"turn_state,omitempty"`
}

// errNoMessage is a context read that does not hold the turn's own message,
// or holds it redacted: there is nothing to answer.
var errNoMessage = fmt.Errorf("the context read holds no message for this turn")

// renderMessage builds the turn's stdin message from the context read (spike
// #1568 § 1.3): the earlier messages, oldest first, as a JSON array, then the
// new message. The context read is the only source of prior context, and it
// is passed as data in the user message, never as a system prompt, so a
// teammate's text carries no more authority than the message itself. JSON
// keeps a message that contains a role marker from forging a turn.
func renderMessage(read platform.TurnContext, turnID string) (string, error) {
	msgs := append([]platform.TurnContextMessage(nil), read.Messages...)
	sort.SliceStable(msgs, func(i, j int) bool { return msgs[i].Position < msgs[j].Position })

	var current *platform.TurnContextMessage
	prior := make([]priorMessage, 0, len(msgs))
	for i := range msgs {
		m := msgs[i]
		if m.TurnID == turnID {
			if m.Role == "user" && current == nil {
				current = &msgs[i]
			}
			continue
		}
		if m.Role != "user" && m.Role != "assistant" {
			continue
		}
		pm := priorMessage{Role: m.Role}
		switch {
		case m.Redacted || m.Content == nil:
			pm.Redacted = true
		default:
			pm.Text = *m.Content
		}
		if m.UnfinishedTurnState != nil {
			pm.TurnState = *m.UnfinishedTurnState
		}
		prior = append(prior, pm)
	}
	if current == nil || current.Redacted || current.Content == nil || strings.TrimSpace(*current.Content) == "" {
		return "", errNoMessage
	}
	if len(prior) == 0 {
		return *current.Content, nil
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(prior); err != nil {
		return "", err
	}
	return "Earlier messages in this conversation, oldest first, as a JSON array:\n" +
		strings.TrimRight(buf.String(), "\n") + "\n\nNew message:\n" + *current.Content, nil
}
