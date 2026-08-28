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

var fakeSlackURL = "https://" + SlackWebhookHost + "/services/T00000000/B00000000/" + hookToken

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
		if ct := r.Header.Get("Content-Type"); ct != "application/json" {
			t.Errorf("Content-Type = %q", ct)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()

	_, err := (&SlackSink{WebhookURL: srv.URL, Client: srv.Client()}).Post(context.Background(), []Message{{
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
	}))
	defer srv.Close()

	if _, err := (&SlackSink{WebhookURL: srv.URL, Client: srv.Client()}).Post(
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
	}))
	defer srv.Close()

	fields := make([]Field, 23)
	for i := range fields {
		fields[i] = Field{Name: "n", Value: "v"}
	}
	if _, err := (&SlackSink{WebhookURL: srv.URL, Client: srv.Client()}).Post(
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
	}))
	defer srv.Close()

	msgs := make([]Message, 45) // → 3 batches (20 + 20 + 5)
	for i := range msgs {
		msgs[i] = Message{Title: "m"}
	}
	delivered, err := (&SlackSink{WebhookURL: srv.URL, Client: srv.Client()}).Post(context.Background(), msgs)
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
	}))
	defer srv.Close()

	delivered, err := (&SlackSink{WebhookURL: srv.URL, Client: srv.Client()}).Post(
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

	if _, err := (&SlackSink{WebhookURL: srv.URL, Client: srv.Client()}).Post(
		context.Background(), []Message{{Title: "x"}}); err == nil {
		t.Fatal("expected an error on a permanent 400")
	}
	if calls != 1 {
		t.Errorf("calls = %d, want 1 (a 400 is permanent)", calls)
	}
}

// The webhook URL IS the Slack credential. It must never survive into a string
// a caller might log — neither the token segment nor the URL as a whole.
func TestSlackSink_RedactScrubsWebhookURL(t *testing.T) {
	sink := &SlackSink{WebhookURL: fakeSlackURL}
	got := sink.Redact("POST to " + fakeSlackURL + " failed")
	if strings.Contains(got, hookToken) {
		t.Fatalf("redacted string still carries the token segment: %s", got)
	}
	if strings.Contains(got, fakeSlackURL) {
		t.Fatalf("redacted string still carries the webhook URL: %s", got)
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

	sink := &SlackSink{WebhookURL: srv.URL + "/services/T0/B0/SECRETTOKEN", Client: client}
	_, err := sink.Post(context.Background(), []Message{{Title: "x"}})
	if err == nil {
		t.Fatal("expected a transport error")
	}
	if strings.Contains(err.Error(), "SECRETTOKEN") {
		t.Fatalf("error leaked the webhook token: %v", err)
	}
}
