// Package notify is the shared alert-delivery layer for every Go-side sink
// (release-watch findings, stuck-epic detection, ready-to-ship). It owns the
// provider-neutral message shape, the transient-retry POST and the credential
// scrubbing, so callers never re-implement (or mis-handle) a webhook token.
//
// Callers build []Message and hand it to a Sink. Sink implementations own the
// wire format: DiscordSink renders embeds, SlackSink renders Block Kit
// attachments. A caller with both configured uses MultiSink and never learns
// which providers are behind it (#1072).
package notify

import (
	"bytes"
	"context"
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

// Severity color bands (RGB ints), exported for callers that pick a band by
// severity. Discord takes the int directly; Slack renders it as "#rrggbb".
const (
	ColorCritical = 0xE03131 // red
	ColorHigh     = 0xF08C00 // amber
	ColorNotable  = 0x1971C2 // blue
	ColorSuccess  = 0x2F9E44 // green
)

// Message is one provider-neutral alert. It is the intersection of what every
// supported provider can render: a title, a body, a severity color, name/value
// rows, a footer and an optional timestamp. Sinks translate it; callers never
// build a provider-specific payload.
type Message struct {
	Title       string
	Description string
	Color       int // one of the Color* bands
	Fields      []Field
	Footer      string
	Timestamp   string // RFC3339; empty means "no timestamp"
}

// Field is one name/value row in a Message.
type Field struct {
	Name  string
	Value string
}

// Sink delivers alerts to one destination.
//
// Post returns the number of messages that actually landed, so a caller with
// per-message bookkeeping (e.g. the stuck-epic re-alert cooldown) can commit
// only what the provider received. A partial failure returns both a non-zero
// count and an error.
//
// Redact scrubs this sink's credential out of an arbitrary string, so a caller
// can log an error without leaking a webhook token.
type Sink interface {
	Post(ctx context.Context, msgs []Message) (delivered int, err error)
	Redact(s string) string
	Name() string
}

// MultiSink fans one alert out to every configured sink.
//
// Delivery is independent per sink: one provider being down never suppresses
// another, which is the whole point of configuring two. The returned count is
// the highest any single sink delivered — the caller's bookkeeping asks "did
// message i get out at all?", and it did if any sink took it. Errors from every
// failing sink are joined so a log line names each one.
type MultiSink []Sink

// Post delivers msgs to every sink, collecting rather than short-circuiting
// errors.
func (m MultiSink) Post(ctx context.Context, msgs []Message) (int, error) {
	var best int
	var errs []error
	for _, s := range m {
		n, err := s.Post(ctx, msgs)
		if n > best {
			best = n
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", s.Name(), err))
		}
	}
	return best, errors.Join(errs...)
}

// Redact applies every sink's redaction, so a joined error carrying two
// providers' URLs is scrubbed of both.
func (m MultiSink) Redact(s string) string {
	for _, sink := range m {
		s = sink.Redact(s)
	}
	return s
}

// Name lists the sinks behind this fan-out, for log lines.
func (m MultiSink) Name() string {
	if len(m) == 0 {
		return "none"
	}
	names := make([]string, 0, len(m))
	for _, s := range m {
		names = append(names, s.Name())
	}
	return strings.Join(names, "+")
}

// post POSTs body to webhookURL with the shared retry policy: transport errors,
// HTTP 429 and HTTP 5xx are retried up to MaxAttempts with a linear backoff; any
// 2xx is success; every other 4xx is permanent and is not retried. client may be
// nil (a 10s-timeout client is used). provider names the destination in error
// strings. The returned error never contains the URL (it carries the token).
func post(ctx context.Context, client *http.Client, webhookURL, provider string, body []byte) error {
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
			lastErr = fmt.Errorf("%s returned %d", provider, resp.StatusCode)
			continue // transient — retry
		default:
			return fmt.Errorf("%s returned %d (permanent)", provider, resp.StatusCode)
		}
	}
	return fmt.Errorf("after %d attempts: %w", MaxAttempts, lastErr)
}

// ScrubURLError unwraps a *url.Error so the returned error keeps the transport
// cause (e.g. "connect: connection refused") but DROPS the request URL — which,
// for both a Discord and a Slack webhook, embeds the secret token.
func ScrubURLError(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) && ue.Err != nil {
		return fmt.Errorf("%s request failed: %w", ue.Op, ue.Err)
	}
	return err
}

// RedactURL replaces any occurrence of webhookURL in msg with a placeholder —
// defense-in-depth so the credential can never reach logs even if a future error
// path embeds it. Pairs with ScrubURLError. An empty webhookURL is a no-op, so a
// caller with only one provider configured can call it unconditionally.
func RedactURL(msg, webhookURL string) string {
	if webhookURL == "" {
		return msg
	}
	return strings.ReplaceAll(msg, webhookURL, "[redacted-webhook-url]")
}

// ClampField truncates s to at most maxRunes runes (appending an ellipsis when
// cut) so a pathological title can never push a payload past a provider's
// per-field character limit.
func ClampField(s string, maxRunes int) string {
	r := []rune(s)
	if len(r) <= maxRunes {
		return s
	}
	return string(r[:maxRunes-1]) + "…"
}
