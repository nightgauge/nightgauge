package notify

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
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

// slackPayload is a Slack incoming-webhook body.
type slackPayload struct {
	Attachments []slackAttachment `json:"attachments"`
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

// SlackSink delivers alerts to a Slack incoming webhook (#1072).
//
// It renders the same Message the Discord sink renders, so an operator with both
// configured sees the same content in both places: the title becomes a header
// block, the description a mrkdwn section, each Field a Block Kit field, and the
// footer a context line. Severity survives as the attachment color bar, which is
// the only place Slack renders a color.
type SlackSink struct {
	// WebhookURL is the full hooks.slack.com incoming-webhook URL. It IS the
	// credential — it is never logged, and every error this sink returns is
	// scrubbed of it.
	WebhookURL string
	// Client is optional; nil means a 10s-timeout client.
	Client *http.Client
}

// Name implements Sink.
func (s *SlackSink) Name() string { return "slack" }

// Redact implements Sink.
func (s *SlackSink) Redact(str string) string { return RedactURL(str, s.WebhookURL) }

// Post renders msgs as Slack attachments and POSTs them, splitting into batches
// of at most MaxAttachmentsPerMessage. Retry and failure semantics match the
// Discord sink exactly (transport/429/5xx retried up to MaxAttempts, other 4xx
// permanent); Slack answers a successful webhook POST with 200 and the body
// "ok". The returned count is the number of messages in the batches that DID
// deliver, and the returned error is scrubbed of the webhook URL.
func (s *SlackSink) Post(ctx context.Context, msgs []Message) (delivered int, err error) {
	if len(msgs) == 0 || s.WebhookURL == "" {
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
		body, mErr := json.Marshal(slackPayload{Attachments: atts})
		if mErr != nil {
			return delivered, fmt.Errorf("encode slack payload: %w", mErr)
		}
		if pErr := post(ctx, s.Client, s.WebhookURL, "slack", body); pErr != nil {
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
			Text: &slackText{Type: "mrkdwn", Text: ClampField(m.Description, maxSectionRunes)},
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
				Text: ClampField(fmt.Sprintf("*%s*\n%s", f.Name, f.Value), maxFieldRunes),
			})
		}
		blocks = append(blocks, slackBlock{Type: "section", Fields: texts})
	}
	if m.Footer != "" {
		blocks = append(blocks, slackBlock{
			Type:     "context",
			Elements: []slackText{{Type: "mrkdwn", Text: ClampField(m.Footer, maxFieldRunes)}},
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
