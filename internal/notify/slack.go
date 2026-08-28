package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"
)

// Slack incoming-webhook limits. Exceeding any of these is a 400, which drops
// the whole message, so the renderer clamps rather than relying on Slack to
// truncate.
// SlackWebhookHost is Slack's incoming-webhook host.
const SlackWebhookHost = "hooks.slack.com"

const (
	// MaxAttachmentsPerMessage bounds attachments in a single Slack POST.
	// SlackSink.Post splits larger slices across multiple POSTs.
	MaxAttachmentsPerMessage = 20
	// maxHeaderRunes is Slack's plain_text header limit.
	maxHeaderRunes = 150
	// maxSectionRunes is Slack's section text limit.
	maxSectionRunes = 3000
	// maxFieldRunes is Slack's per-field text limit.
	maxFieldRunes = 2000
	// maxFieldsPerSection is Slack's cap on fields in one section block; longer
	// field lists are chunked across additional section blocks.
	maxFieldsPerSection = 10
)

// slackPayload is a chat.postMessage body.
type slackPayload struct {
	Channel     string            `json:"channel"`
	Text        string            `json:"text"`
	Attachments []slackAttachment `json:"attachments"`
}

// slackAPIResponse is the subset of a Web API reply this sink reads. Slack
// reports API failures in a 200 body, never in the HTTP status.
type slackAPIResponse struct {
	OK    bool   `json:"ok"`
	Error string `json:"error"`
}

// slackAttachment carries the color bar Slack renders beside the blocks. Block
// Kit has no color of its own, so severity banding requires an attachment.
type slackAttachment struct {
	Color  string       `json:"color"`
	Blocks []slackBlock `json:"blocks"`
}

// slackBlock is one Block Kit block. Only the subset this sink emits is
// modeled: header, section (text or fields), and context.
type slackBlock struct {
	Type   string      `json:"type"`
	Text   *slackText  `json:"text,omitempty"`
	Fields []slackText `json:"fields,omitempty"`
	// Elements carries the context block's text runs (the footer line).
	Elements []slackText `json:"elements,omitempty"`
}

// slackText is a Block Kit text object.
type slackText struct {
	Type string `json:"type"` // "plain_text" or "mrkdwn"
	Text string `json:"text"`
}

// SlackSink delivers alerts to Slack via chat.postMessage (#1089).
//
// It renders the same Message the Discord sink renders, so an operator with both
// configured sees the same content in both places: the title becomes a header
// block, the description a mrkdwn section, each Field a Block Kit field, and the
// footer a context line. Severity survives as the attachment color bar, which is
// the only place Slack renders a color.
type SlackSink struct {
	// BotToken is the Slack bot token (xoxb-…). It IS the credential — it is
	// never logged, and every error this sink returns is scrubbed of it. The
	// same token the VSCode extension uses, so the integration needs exactly
	// one Slack credential rather than one per half (#1089).
	BotToken string
	// Channel is the target channel id (preferred) or #name.
	Channel string
	// Client is optional; nil means a 10s-timeout client.
	Client *http.Client
	// APIBase overrides the Slack Web API root. Empty means SlackAPIBase; it
	// exists so tests can point the sink at a local server, since the real
	// base is a fixed host rather than a configurable URL.
	APIBase string
}

// Name implements Sink.
func (s *SlackSink) Name() string { return "slack" }

// Redact implements Sink.
func (s *SlackSink) Redact(str string) string { return RedactURL(str, s.BotToken) }

// Post renders msgs as Slack attachments and POSTs them, splitting into batches
// of at most MaxAttachmentsPerMessage. Retry and failure semantics match the
// Discord sink exactly (transport/429/5xx retried up to MaxAttempts, other 4xx
// permanent); Slack answers a successful webhook POST with 200 and the body
// "ok". The returned count is the number of messages in the batches that DID
// deliver, and the returned error is scrubbed of the webhook URL.
func (s *SlackSink) Post(ctx context.Context, msgs []Message) (delivered int, err error) {
	if len(msgs) == 0 || s.BotToken == "" || s.Channel == "" {
		return 0, nil
	}
	for i := 0; i < len(msgs); i += MaxAttachmentsPerMessage {
		end := i + MaxAttachmentsPerMessage
		if end > len(msgs) {
			end = len(msgs)
		}
		atts := make([]slackAttachment, 0, end-i)
		for _, m := range msgs[i:end] {
			atts = append(atts, slackAttachmentFor(m))
		}
		body, mErr := json.Marshal(slackPayload{Channel: s.Channel, Text: fallbackText(msgs[i:end]), Attachments: atts})
		if mErr != nil {
			return delivered, fmt.Errorf("encode slack payload: %w", mErr)
		}
		if pErr := postSlackAPI(ctx, s.Client, s.apiBase(), s.BotToken, "chat.postMessage", body); pErr != nil {
			return delivered, pErr
		}
		delivered = end // this batch landed
	}
	return delivered, nil
}

// slackAttachmentFor translates a neutral Message into Slack's attachment +
// Block Kit shape.
func slackAttachmentFor(m Message) slackAttachment {
	blocks := make([]slackBlock, 0, 4)

	if m.Title != "" {
		blocks = append(blocks, slackBlock{
			Type: "header",
			Text: &slackText{Type: "plain_text", Text: ClampField(m.Title, maxHeaderRunes)},
		})
	}
	if m.Description != "" {
		blocks = append(blocks, slackBlock{
			Type: "section",
			Text: &slackText{Type: "mrkdwn", Text: ClampField(ToSlackMrkdwn(m.Description), maxSectionRunes)},
		})
	}
	// Slack caps a section at maxFieldsPerSection fields, so a long field list
	// becomes several section blocks rather than one over-limit block.
	for i := 0; i < len(m.Fields); i += maxFieldsPerSection {
		end := i + maxFieldsPerSection
		if end > len(m.Fields) {
			end = len(m.Fields)
		}
		texts := make([]slackText, 0, end-i)
		for _, f := range m.Fields[i:end] {
			texts = append(texts, slackText{
				Type: "mrkdwn",
				Text: ClampField(fmt.Sprintf("*%s*\n%s", ToSlackMrkdwn(f.Name), ToSlackMrkdwn(f.Value)), maxFieldRunes),
			})
		}
		blocks = append(blocks, slackBlock{Type: "section", Fields: texts})
	}
	if m.Footer != "" {
		blocks = append(blocks, slackBlock{
			Type:     "context",
			Elements: []slackText{{Type: "mrkdwn", Text: ClampField(ToSlackMrkdwn(m.Footer), maxFieldRunes)}},
		})
	}

	return slackAttachment{Color: SlackColor(m.Color), Blocks: blocks}
}

// SlackColor renders a Color* band as the "#rrggbb" string Slack attachments
// take. Discord consumes the same band as a plain int, so severity means the
// same thing in both providers.
func SlackColor(color int) string {
	return fmt.Sprintf("#%06x", color&0xFFFFFF)
}

// SlackAPIBase is the Slack Web API root. Only chat.postMessage is used.
const SlackAPIBase = "https://slack.com/api"

// postSlackAPI POSTs to a Slack Web API method with a bot token.
//
// Slack reports API-level failures in a 200 body (`{"ok":false,"error":...}`),
// never in the HTTP status, so the body is inspected rather than the status —
// a status-only check reports every rejection as a successful delivery. The
// shared retry policy still covers transport errors and 429/5xx; an API-level
// rejection is returned as a permanent error because retrying a bad token or a
// missing scope only burns rate limit.
func postSlackAPI(ctx context.Context, client *http.Client, apiBase, botToken, method string, body []byte) error {
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

		req, err := http.NewRequestWithContext(ctx, http.MethodPost, apiBase+"/"+method, bytes.NewReader(body))
		if err != nil {
			return fmt.Errorf("build request: %w", ScrubURLError(err))
		}
		req.Header.Set("Content-Type", "application/json; charset=utf-8")
		req.Header.Set("Authorization", "Bearer "+botToken)

		resp, err := client.Do(req)
		if err != nil {
			lastErr = ScrubURLError(err)
			continue // transport error — retry
		}

		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
			resp.Body.Close()
			lastErr = fmt.Errorf("slack returned %d", resp.StatusCode)
			continue // transient — retry
		}

		var api slackAPIResponse
		decErr := json.NewDecoder(resp.Body).Decode(&api)
		resp.Body.Close()
		if decErr != nil {
			return fmt.Errorf("slack %s: decode response: %w", method, decErr)
		}
		if !api.OK {
			return fmt.Errorf("slack %s rejected: %s (permanent)", method, api.Error)
		}
		return nil
	}
	return fmt.Errorf("after %d attempts: %w", MaxAttempts, lastErr)
}

// apiBase is the Web API root this sink posts to.
func (s *SlackSink) apiBase() string {
	if s.APIBase != "" {
		return s.APIBase
	}
	return SlackAPIBase
}

// fallbackText is the plain-text summary Slack shows in notifications and
// screen readers, where attachment blocks are not rendered. Uses the first
// message's title so a push notification says something useful.
func fallbackText(msgs []Message) string {
	for _, m := range msgs {
		if m.Title != "" {
			return ClampField(m.Title, maxHeaderRunes)
		}
	}
	return "Nightgauge alert"
}

var (
	// Markdown link -> Slack link. Runs before the bold rule so a bolded label
	// is unwrapped afterwards.
	mdLink   = regexp.MustCompile(`\[([^\]]+)\]\(([^)\s]+)\)`)
	mdBold   = regexp.MustCompile(`\*\*(.+?)\*\*`)
	mdStrike = regexp.MustCompile(`~~(.+?)~~`)
	// Code spans and fenced blocks, kept verbatim.
	codeSpan = regexp.MustCompile("(?s)```.*?```|`[^`\n]*`")
)

// ToSlackMrkdwn translates Discord/Mattermost-flavoured Markdown into Slack's
// mrkdwn dialect (#1089).
//
// Callers build one neutral Message for every provider. Discord and Mattermost
// parse `**bold**` and `[label](url)` natively; Slack does not — it uses
// `*bold*` and `<url|label>`, so untranslated text reaches the channel as
// literal punctuation. Slack accepts the payload either way and answers ok, so
// this is a silent rendering defect visible only by reading the channel: the
// release-watch alert shipped one for exactly that reason.
//
// Code spans and fenced blocks pass through untouched, because a `**` or a
// bracket inside a deploy command or a stack trace is literal text.
func ToSlackMrkdwn(text string) string {
	var b strings.Builder
	last := 0
	for _, loc := range codeSpan.FindAllStringIndex(text, -1) {
		b.WriteString(translateMrkdwn(text[last:loc[0]]))
		b.WriteString(text[loc[0]:loc[1]]) // verbatim
		last = loc[1]
	}
	b.WriteString(translateMrkdwn(text[last:]))
	return b.String()
}

func translateMrkdwn(s string) string {
	s = mdLink.ReplaceAllString(s, "<$2|$1>")
	s = mdBold.ReplaceAllString(s, "*$1*")
	s = mdStrike.ReplaceAllString(s, "~$1~")
	return s
}
