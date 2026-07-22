package notify

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func init() {
	RetryDelay = 0 // no real sleeps in tests
}

func TestRedactURL(t *testing.T) {
	url := "https://discord.com/api/webhooks/1/tok"
	got := RedactURL("webhook POST failed: Post \""+url+"\": timeout", url)
	if strings.Contains(got, url) {
		t.Errorf("RedactURL left the URL in: %s", got)
	}
	if RedactURL("no url here", "") != "no url here" {
		t.Error("empty webhook should pass message through unchanged")
	}
}

func TestClampField(t *testing.T) {
	if got := ClampField("short", 200); got != "short" {
		t.Errorf("ClampField should not alter a short string, got %q", got)
	}
	long := strings.Repeat("x", 500)
	got := ClampField(long, 200)
	if len([]rune(got)) != 200 {
		t.Errorf("ClampField len = %d, want 200", len([]rune(got)))
	}
	if !strings.HasSuffix(got, "…") {
		t.Error("ClampField should append an ellipsis when truncating")
	}
}

func TestPostEmbeds_SendsPayload(t *testing.T) {
	var body []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	_, err := PostEmbeds(context.Background(), srv.Client(), srv.URL, []Embed{{
		Title: "hi", Color: ColorSuccess, Fields: []EmbedField{{Name: "n", Value: "v"}},
	}})
	if err != nil {
		t.Fatalf("PostEmbeds: %v", err)
	}
	var got Payload
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(got.Embeds) != 1 || got.Embeds[0].Title != "hi" {
		t.Errorf("payload = %+v", got)
	}
}

func TestPostEmbeds_RetriesTransient(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(http.StatusInternalServerError) // always 5xx → retried then fails
	}))
	defer srv.Close()

	delivered, err := PostEmbeds(context.Background(), srv.Client(), srv.URL, []Embed{{Title: "x"}})
	if err == nil {
		t.Fatal("expected error after exhausting retries")
	}
	if delivered != 0 {
		t.Errorf("delivered = %d, want 0 (first batch failed)", delivered)
	}
	if got := atomic.LoadInt32(&hits); got != MaxAttempts {
		t.Errorf("server hits = %d, want %d (retried)", got, MaxAttempts)
	}
}

func TestPostEmbeds_Permanent4xxNotRetried(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(http.StatusBadRequest) // 400 → permanent, no retry
	}))
	defer srv.Close()

	if _, err := PostEmbeds(context.Background(), srv.Client(), srv.URL, []Embed{{Title: "x"}}); err == nil {
		t.Fatal("expected permanent error")
	}
	if got := atomic.LoadInt32(&hits); got != 1 {
		t.Errorf("server hits = %d, want 1 (no retry on 4xx)", got)
	}
}

func TestPostEmbeds_ChunksOver10(t *testing.T) {
	var posts, totalEmbeds int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&posts, 1)
		var p Payload
		_ = json.NewDecoder(r.Body).Decode(&p)
		atomic.AddInt32(&totalEmbeds, int32(len(p.Embeds)))
		if len(p.Embeds) > MaxEmbedsPerMessage {
			t.Errorf("batch exceeded Discord cap: %d embeds", len(p.Embeds))
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	embeds := make([]Embed, 23) // → 3 batches (10 + 10 + 3)
	for i := range embeds {
		embeds[i] = Embed{Title: "e"}
	}
	delivered, err := PostEmbeds(context.Background(), srv.Client(), srv.URL, embeds)
	if err != nil {
		t.Fatalf("PostEmbeds: %v", err)
	}
	if delivered != 23 {
		t.Errorf("delivered = %d, want 23", delivered)
	}
	if got := atomic.LoadInt32(&posts); got != 3 {
		t.Errorf("expected 3 batched POSTs, got %d", got)
	}
	if got := atomic.LoadInt32(&totalEmbeds); got != 23 {
		t.Errorf("expected all 23 embeds delivered, got %d", got)
	}
}

func TestPostEmbeds_PartialBatchFailureReportsDelivered(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		n := atomic.AddInt32(&hits, 1)
		if n == 1 {
			w.WriteHeader(http.StatusNoContent) // batch 1 lands
			return
		}
		w.WriteHeader(http.StatusInternalServerError) // batch 2 fails
	}))
	defer srv.Close()

	embeds := make([]Embed, 13) // → batch 1 (10) lands, batch 2 (3) fails
	for i := range embeds {
		embeds[i] = Embed{Title: "e"}
	}
	delivered, err := PostEmbeds(context.Background(), srv.Client(), srv.URL, embeds)
	if err == nil {
		t.Fatal("expected an error from the failing second batch")
	}
	if delivered != 10 {
		t.Errorf("delivered = %d, want 10 (first batch landed before the failure)", delivered)
	}
}
