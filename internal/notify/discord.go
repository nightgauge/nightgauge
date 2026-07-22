// Package notify is the single Discord-webhook delivery primitive shared by all
// Go-side alert sinks (release-watch findings #4058/#4063, stuck-epic detection
// #4073, …). It owns the embed payload shape, the transient-retry POST, and the
// credential-scrubbing so callers never re-implement (or mis-handle) the webhook
// token.
package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// RetryDelay is the base delay between webhook POST retries. A package var (not a
// const) so tests can set it to 0 and run fast.
var RetryDelay = 750 * time.Millisecond

// MaxAttempts bounds the webhook POST retries (transient 429/5xx/transport).
const MaxAttempts = 3

// MaxEmbedsPerMessage is Discord's hard cap on embeds in a single webhook
// message; exceeding it is a 400 (permanent) that drops the whole payload.
// PostEmbeds splits larger slices across multiple POSTs.
const MaxEmbedsPerMessage = 10

// Discord embed color bands (RGB ints), exported for callers that pick a band by
// severity.
const (
	ColorCritical = 0xE03131 // red
	ColorHigh     = 0xF08C00 // amber
	ColorNotable  = 0x1971C2 // blue
	ColorSuccess  = 0x2F9E44 // green
)

// Payload is a Discord webhook body.
type Payload struct {
	Embeds []Embed `json:"embeds"`
}

// Embed is a single Discord embed.
type Embed struct {
	Title       string       `json:"title"`
	Description string       `json:"description"`
	Color       int          `json:"color"`
	Fields      []EmbedField `json:"fields,omitempty"`
	Footer      *Footer      `json:"footer,omitempty"`
	Timestamp   string       `json:"timestamp,omitempty"`
}

// EmbedField is one name/value row in an embed.
type EmbedField struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// Footer is the embed footer.
type Footer struct {
	Text string `json:"text"`
}

// PostEmbeds POSTs embeds to webhookURL, automatically splitting into batches of
// at most MaxEmbedsPerMessage so Discord never rejects an over-limit payload (a
// 400 that would drop every embed). Each batch retries transient failures
// (transport error, HTTP 429, HTTP 5xx) up to MaxAttempts; any 2xx (Discord
// returns 204) is success, a non-429 4xx is a permanent failure and is not
// retried. client may be nil (a 10s-timeout client is used). The returned error
// is scrubbed of the webhook URL (it carries the token); pass it through
// RedactURL too if you log the raw string. On a multi-batch send the first
// failing batch's error is returned (later batches are not attempted); the
// returned count is the number of embeds in the batches that DID deliver, so a
// caller (e.g. the stuck-epic cooldown) can commit only what Discord actually
// received.
func PostEmbeds(ctx context.Context, client *http.Client, webhookURL string, embeds []Embed) (delivered int, err error) {
	if len(embeds) == 0 {
		return 0, nil
	}
	for i := 0; i < len(embeds); i += MaxEmbedsPerMessage {
		end := i + MaxEmbedsPerMessage
		if end > len(embeds) {
			end = len(embeds)
		}
		body, mErr := json.Marshal(Payload{Embeds: embeds[i:end]})
		if mErr != nil {
			return delivered, fmt.Errorf("encode discord payload: %w", mErr)
		}
		if pErr := post(ctx, client, webhookURL, body); pErr != nil {
			return delivered, pErr
		}
		delivered = end // this batch landed
	}
	return delivered, nil
}

func post(ctx context.Context, client *http.Client, webhookURL string, body []byte) error {
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}

	var lastErr error
	for attempt := 1; attempt <= MaxAttempts; attempt++ {
		if attempt > 1 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(RetryDelay * time.Duration(attempt-1)):
			}
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodPost, webhookURL, bytes.NewReader(body))
		if err != nil {
			return fmt.Errorf("build request: %w", ScrubURLError(err))
		}
		req.Header.Set("Content-Type", "application/json")

		resp, err := client.Do(req)
		if err != nil {
			lastErr = ScrubURLError(err) // drop the URL (carries the webhook token)
			continue                     // transport error — retry
		}
		resp.Body.Close() // drain+close so the connection can be reused

		switch {
		case resp.StatusCode >= 200 && resp.StatusCode < 300:
			return nil
		case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500:
			lastErr = fmt.Errorf("discord returned %d", resp.StatusCode)
			continue // transient — retry
		default:
			return fmt.Errorf("discord returned %d (permanent)", resp.StatusCode)
		}
	}
	return fmt.Errorf("after %d attempts: %w", MaxAttempts, lastErr)
}

// ScrubURLError unwraps a *url.Error so the returned error keeps the transport
// cause (e.g. "connect: connection refused") but DROPS the request URL — which,
// for a Discord webhook, embeds the secret token.
func ScrubURLError(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) && ue.Err != nil {
		return fmt.Errorf("%s request failed: %w", ue.Op, ue.Err)
	}
	return err
}

// RedactURL replaces any occurrence of webhookURL in msg with a placeholder —
// defense-in-depth so the credential can never reach logs even if a future error
// path embeds it. Pairs with ScrubURLError.
func RedactURL(msg, webhookURL string) string {
	if webhookURL == "" {
		return msg
	}
	return strings.ReplaceAll(msg, webhookURL, "[redacted-webhook-url]")
}

// ClampField truncates s to at most maxRunes runes (appending an ellipsis when
// cut) so a pathological title can never push a Discord embed field value past
// the 1024-char limit.
func ClampField(s string, maxRunes int) string {
	r := []rune(s)
	if len(r) <= maxRunes {
		return s
	}
	return string(r[:maxRunes-1]) + "…"
}
