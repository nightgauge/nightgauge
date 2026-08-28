package notify

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
)

// MaxEmbedsPerMessage is Discord's hard cap on embeds in a single webhook
// message; exceeding it is a 400 (permanent) that drops the whole payload.
// DiscordSink.Post splits larger slices across multiple POSTs.
const MaxEmbedsPerMessage = 10

// DiscordPayload is a Discord webhook body. Exported so callers can assert on
// the exact wire shape in tests.
type DiscordPayload struct {
	Embeds []DiscordEmbed `json:"embeds"`
}

// DiscordEmbed is a single Discord embed — the wire shape, built from a Message.
type DiscordEmbed struct {
	Title       string              `json:"title"`
	Description string              `json:"description"`
	Color       int                 `json:"color"`
	Fields      []DiscordEmbedField `json:"fields,omitempty"`
	Footer      *DiscordFooter      `json:"footer,omitempty"`
	Timestamp   string              `json:"timestamp,omitempty"`
}

// DiscordEmbedField is one name/value row in an embed.
type DiscordEmbedField struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// DiscordFooter is the embed footer.
type DiscordFooter struct {
	Text string `json:"text"`
}

// DiscordSink delivers alerts to a Discord incoming webhook.
type DiscordSink struct {
	// WebhookURL is the full webhook URL. It IS the credential — it is never
	// logged, and every error this sink returns is scrubbed of it.
	WebhookURL string
	// Client is optional; nil means a 10s-timeout client.
	Client *http.Client
}

// Name implements Sink.
func (d *DiscordSink) Name() string { return "discord" }

// Redact implements Sink.
func (d *DiscordSink) Redact(s string) string { return RedactURL(s, d.WebhookURL) }

// Post renders msgs as Discord embeds and POSTs them, automatically splitting
// into batches of at most MaxEmbedsPerMessage so Discord never rejects an
// over-limit payload (a 400 that would drop every embed). Each batch retries
// transient failures (transport error, HTTP 429, HTTP 5xx) up to MaxAttempts;
// any 2xx (Discord returns 204) is success, a non-429 4xx is a permanent failure
// and is not retried. The returned error is scrubbed of the webhook URL (it
// carries the token); pass it through Redact too if you log the raw string. On a
// multi-batch send the first failing batch's error is returned (later batches are
// not attempted); the returned count is the number of messages in the batches
// that DID deliver, so a caller (e.g. the stuck-epic cooldown) can commit only
// what Discord actually received.
func (d *DiscordSink) Post(ctx context.Context, msgs []Message) (delivered int, err error) {
	if len(msgs) == 0 || d.WebhookURL == "" {
		return 0, nil
	}
	for i := 0; i < len(msgs); i += MaxEmbedsPerMessage {
		end := i + MaxEmbedsPerMessage
		if end > len(msgs) {
			end = len(msgs)
		}
		embeds := make([]DiscordEmbed, 0, end-i)
		for _, m := range msgs[i:end] {
			embeds = append(embeds, discordEmbedFor(m))
		}
		body, mErr := json.Marshal(DiscordPayload{Embeds: embeds})
		if mErr != nil {
			return delivered, fmt.Errorf("encode discord payload: %w", mErr)
		}
		if pErr := post(ctx, d.Client, d.WebhookURL, "discord", body); pErr != nil {
			return delivered, pErr
		}
		delivered = end // this batch landed
	}
	return delivered, nil
}

// discordEmbedFor translates a neutral Message into Discord's embed shape.
func discordEmbedFor(m Message) DiscordEmbed {
	e := DiscordEmbed{
		Title:       m.Title,
		Description: m.Description,
		Color:       m.Color,
		Timestamp:   m.Timestamp,
	}
	if len(m.Fields) > 0 {
		e.Fields = make([]DiscordEmbedField, 0, len(m.Fields))
		for _, f := range m.Fields {
			e.Fields = append(e.Fields, DiscordEmbedField{Name: f.Name, Value: f.Value})
		}
	}
	if m.Footer != "" {
		e.Footer = &DiscordFooter{Text: m.Footer}
	}
	return e
}
