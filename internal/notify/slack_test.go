package notify

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func init() { RetryDelay = 0 }

// hookToken stands in for the credential segment of a webhook URL. The URL is
// assembled from parts rather than written as one literal: a whole fake webhook
// URL matches GitHub's secret-scanning pattern and blocks the push, and a test
// fixture that looks like a live credential is worth avoiding regardless.
const hookToken = "zzTESTTOKENzz"

// fakeBotToken stands in for a live credential. Assembled from parts so no
// literal in the source matches a secret-scanning pattern.
var fakeBotToken = "xoxb-" + hookToken

func TestSlackColor(t *testing.T) {
	for _, tc := range []struct {
		band int
		want string
	}{
		{ColorCritical, "#e03131"},
		{ColorHigh, "#f08c00"},
		{ColorNotable, "#1971c2"},
		{ColorSuccess, "#2f9e44"},
	} {
		if got := SlackColor(tc.band); got != tc.want {
			t.Errorf("SlackColor(%#x) = %q, want %q", tc.band, got, tc.want)
		}
	}
}

// The four severity bands must stay visually distinct in Slack, not just in
// Discord — a banding scheme that collapses to one color is indistinguishable
// from having no severity at all.
func TestSlackColor_BandsAreDistinct(t *testing.T) {
	seen := map[string]bool{}
	for _, b := range []int{ColorCritical, ColorHigh, ColorNotable, ColorSuccess} {
		c := SlackColor(b)
		if seen[c] {
			t.Fatalf("severity band %#x collides with an earlier band at %s", b, c)
		}
		seen[c] = true
	}
}

func TestSlackSink_SendsBlockKitPayload(t *testing.T) {
	var body []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ = io.ReadAll(r.Body)
		if ct := r.Header.Get("Content-Type"); ct != "application/json; charset=utf-8" {
			t.Errorf("Content-Type = %q", ct)
		}
		if auth := r.Header.Get("Authorization"); auth != "Bearer "+fakeBotToken {
			t.Errorf("Authorization = %q, want the bot token", auth)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	_, err := (&SlackSink{BotToken: fakeBotToken, Channel: "C0123", Client: srv.Client(), APIBase: srv.URL}).Post(context.Background(), []Message{{
		Title:       "Stalled epic",
		Description: "no eligible work",
		Color:       ColorHigh,
		Fields:      []Field{{Name: "#12 · thing", Value: "blocked"}},
		Footer:      "nightgauge",
	}})
	if err != nil {
		t.Fatalf("Post: %v", err)
	}

	var got slackPayload
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(got.Attachments) != 1 {
		t.Fatalf("attachments = %d, want 1", len(got.Attachments))
	}
	att := got.Attachments[0]
	if att.Color != "#f08c00" {
		t.Errorf("color = %q, want the high band", att.Color)
	}
	// header + description section + field section + context footer
	if len(att.Blocks) != 4 {
		t.Fatalf("blocks = %d, want 4: %+v", len(att.Blocks), att.Blocks)
	}
	if att.Blocks[0].Type != "header" || att.Blocks[0].Text.Text != "Stalled epic" {
		t.Errorf("header block = %+v", att.Blocks[0])
	}
	if att.Blocks[1].Type != "section" || att.Blocks[1].Text.Text != "no eligible work" {
		t.Errorf("description block = %+v", att.Blocks[1])
	}
	if len(att.Blocks[2].Fields) != 1 || !strings.Contains(att.Blocks[2].Fields[0].Text, "*#12 · thing*") {
		t.Errorf("field block = %+v", att.Blocks[2])
	}
	if att.Blocks[3].Type != "context" {
		t.Errorf("footer block = %+v", att.Blocks[3])
	}
}

// A message with no fields and no footer must not emit empty blocks — Slack
// rejects a section with neither text nor fields with a 400.
func TestSlackSink_OmitsEmptyBlocks(t *testing.T) {
	var body []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	if _, err := (&SlackSink{BotToken: fakeBotToken, Channel: "C0123", Client: srv.Client(), APIBase: srv.URL}).Post(
		context.Background(), []Message{{Title: "only a title"}}); err != nil {
		t.Fatalf("Post: %v", err)
	}
	var got slackPayload
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(got.Attachments[0].Blocks) != 1 {
		t.Errorf("blocks = %+v, want only the header", got.Attachments[0].Blocks)
	}
}

// Slack caps a section at 10 fields; a longer list must be chunked across
// sections rather than sent as one over-limit block (a 400 that drops the
// whole message).
func TestSlackSink_ChunksFieldsPerSection(t *testing.T) {
	var body []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	fields := make([]Field, 23)
	for i := range fields {
		fields[i] = Field{Name: "n", Value: "v"}
	}
	if _, err := (&SlackSink{BotToken: fakeBotToken, Channel: "C0123", Client: srv.Client(), APIBase: srv.URL}).Post(
		context.Background(), []Message{{Title: "t", Fields: fields}}); err != nil {
		t.Fatalf("Post: %v", err)
	}
	var got slackPayload
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, b := range got.Attachments[0].Blocks {
		if len(b.Fields) > maxFieldsPerSection {
			t.Errorf("section carries %d fields, over Slack's cap", len(b.Fields))
		}
	}
}

func TestSlackSink_ChunksOverAttachmentCap(t *testing.T) {
	batches := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		batches++
		body, _ := io.ReadAll(r.Body)
		var p slackPayload
		if err := json.Unmarshal(body, &p); err == nil && len(p.Attachments) > MaxAttachmentsPerMessage {
			t.Errorf("batch exceeded Slack cap: %d attachments", len(p.Attachments))
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	msgs := make([]Message, 45) // → 3 batches (20 + 20 + 5)
	for i := range msgs {
		msgs[i] = Message{Title: "m"}
	}
	delivered, err := (&SlackSink{BotToken: fakeBotToken, Channel: "C0123", Client: srv.Client(), APIBase: srv.URL}).Post(context.Background(), msgs)
	if err != nil {
		t.Fatalf("Post: %v", err)
	}
	if batches != 3 {
		t.Errorf("batches = %d, want 3", batches)
	}
	if delivered != 45 {
		t.Errorf("delivered = %d, want 45", delivered)
	}
}

func TestSlackSink_RetriesTransient(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls < 3 {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	delivered, err := (&SlackSink{BotToken: fakeBotToken, Channel: "C0123", Client: srv.Client(), APIBase: srv.URL}).Post(
		context.Background(), []Message{{Title: "x"}})
	if err != nil {
		t.Fatalf("Post: %v", err)
	}
	if calls != 3 {
		t.Errorf("calls = %d, want 3 (two 429s then success)", calls)
	}
	if delivered != 1 {
		t.Errorf("delivered = %d, want 1", delivered)
	}
}

func TestSlackSink_Permanent4xxNotRetried(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer srv.Close()

	if _, err := (&SlackSink{BotToken: fakeBotToken, Channel: "C0123", Client: srv.Client(), APIBase: srv.URL}).Post(
		context.Background(), []Message{{Title: "x"}}); err == nil {
		t.Fatal("expected an error on a permanent 400")
	}
	if calls != 1 {
		t.Errorf("calls = %d, want 1 (a 400 is permanent)", calls)
	}
}

// The bot token IS the Slack credential. It must never survive into a string a
// caller might log.
func TestSlackSink_RedactScrubsBotToken(t *testing.T) {
	sink := &SlackSink{BotToken: fakeBotToken, Channel: "C0123"}
	got := sink.Redact("auth failed for Bearer " + fakeBotToken)
	if strings.Contains(got, hookToken) {
		t.Fatalf("redacted string still carries the token: %s", got)
	}
	if !strings.Contains(got, "[redacted-webhook-url]") {
		t.Errorf("expected the placeholder, got %s", got)
	}
}

// A transport failure must not leak the URL through the *url.Error either —
// that error embeds the request URL by construction.
func TestSlackSink_TransportErrorDropsURL(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	client := srv.Client()
	srv.Close() // nothing is listening — force a transport error

	sink := &SlackSink{BotToken: fakeBotToken, Channel: "C0123", Client: client, APIBase: srv.URL}
	_, err := sink.Post(context.Background(), []Message{{Title: "x"}})
	if err == nil {
		t.Fatal("expected a transport error")
	}
	if strings.Contains(err.Error(), hookToken) {
		t.Fatalf("error leaked the bot token: %v", err)
	}
}

// ─── mrkdwn dialect ─────────────────────────────────────────────────────────

// Callers build one neutral Message for every provider. Discord and Mattermost
// parse this Markdown natively; Slack does not, and accepts the payload anyway
// — so an untranslated message is a SILENT rendering defect. The release-watch
// alert shipped one (#1089).
func TestToSlackMrkdwn(t *testing.T) {
	for name, tc := range map[string]struct{ in, want string }{
		"markdown link":      {"[the issue](https://e.test/42)", "<https://e.test/42|the issue>"},
		"bold":               {"**Feature Dev**", "*Feature Dev*"},
		"two bold runs":      {"**a** and **b**", "*a* and *b*"},
		"bold inside a link": {"[**Title**](https://u.test)", "<https://u.test|*Title*>"},
		"strikethrough":      {"~~gone~~", "~gone~"},
		"no markup":          {"plain text", "plain text"},
		// A ** or a bracket inside a command or a stack trace is literal text.
		"inline code span": {"run `npm **x**` now", "run `npm **x**` now"},
		"fenced block":     {"```\nkeep **this** [as](is)\n```", "```\nkeep **this** [as](is)\n```"},
		"code then markup": {"`a **b**` and **c**", "`a **b**` and *c*"},
	} {
		if got := ToSlackMrkdwn(tc.in); got != tc.want {
			t.Errorf("%s: ToSlackMrkdwn(%q) = %q, want %q", name, tc.in, got, tc.want)
		}
	}
}

// The exact shape release-watch builds — the alert that shipped broken.
func TestSlackSink_TranslatesFieldMarkdown(t *testing.T) {
	var body []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	sink := &SlackSink{BotToken: fakeBotToken, Channel: "C0123", Client: srv.Client(), APIBase: srv.URL}
	if _, err := sink.Post(context.Background(), []Message{{
		Title:       "Release alert",
		Description: "**2** high-impact changes",
		Fields:      []Field{{Name: "#42 · score 88", Value: "[Some finding](https://github.test/42)"}},
	}}); err != nil {
		t.Fatalf("Post: %v", err)
	}

	// Decode rather than string-match: encoding/json HTML-escapes the angle
	// brackets of a Slack link, so a raw substring check fails on output that
	// is in fact correct.
	var p slackPayload
	if err := json.Unmarshal(body, &p); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	blocks := p.Attachments[0].Blocks
	desc := blocks[1].Text.Text
	field := blocks[2].Fields[0].Text
	if strings.Contains(desc, "**") {
		t.Errorf("description still carries ** bold: %q", desc)
	}
	if desc != "*2* high-impact changes" {
		t.Errorf("description = %q", desc)
	}
	if strings.Contains(field, "](") {
		t.Fatalf("field still carries a Markdown link: %q", field)
	}
	if !strings.Contains(field, "<https://github.test/42|Some finding>") {
		t.Errorf("field link not translated to Slack syntax: %q", field)
	}
}

// Slack reports API failures in a 200 body. Trusting the HTTP status would
// count every rejection as a delivered alert.
func TestSlackSink_OkFalseInA200IsAFailure(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":false,"error":"channel_not_found"}`))
	}))
	defer srv.Close()

	sink := &SlackSink{BotToken: fakeBotToken, Channel: "C0123", Client: srv.Client(), APIBase: srv.URL}
	delivered, err := sink.Post(context.Background(), []Message{{Title: "x"}})
	if err == nil {
		t.Fatal("expected an error when Slack answers ok:false")
	}
	if !strings.Contains(err.Error(), "channel_not_found") {
		t.Errorf("error should name the Slack error code, got %v", err)
	}
	if delivered != 0 {
		t.Errorf("delivered = %d, want 0", delivered)
	}
	if calls != 1 {
		t.Errorf("calls = %d — an API-level rejection must not be retried", calls)
	}
}

// The payload must carry a plain-text fallback: Slack shows it in push
// notifications and to screen readers, where blocks are not rendered.
func TestSlackSink_SendsFallbackText(t *testing.T) {
	var body []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	sink := &SlackSink{BotToken: fakeBotToken, Channel: "C0123", Client: srv.Client(), APIBase: srv.URL}
	if _, err := sink.Post(context.Background(), []Message{{Title: "Stalled epic"}}); err != nil {
		t.Fatalf("Post: %v", err)
	}
	var p slackPayload
	if err := json.Unmarshal(body, &p); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if p.Text != "Stalled epic" {
		t.Errorf("fallback text = %q", p.Text)
	}
	if p.Channel != "C0123" {
		t.Errorf("channel = %q", p.Channel)
	}
}
