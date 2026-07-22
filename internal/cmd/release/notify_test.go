package release

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/nightgauge/nightgauge/internal/notify"
)

func init() {
	// Make retry backoff instant in tests.
	notify.RetryDelay = 0
}

// writeLog writes a creation-log JSON to a temp file and returns its path.
func writeLog(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "creation-log.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write log: %v", err)
	}
	return path
}

const twoHighScoreLog = `{
  "schema_version": "1.0",
  "provider": "claude-code",
  "source": "anthropics/claude-code",
  "run_started_at": "2026-06-19T12:00:00Z",
  "new_version": "2.1.75",
  "since_version": "2.1.74",
  "status": "completed",
  "issues_created": [
    {"number": 101, "title": "New model claude-opus-9", "url": "https://x/101", "score": 88},
    {"number": 102, "title": "Breaking: tool API change", "url": "https://x/102", "score": 75}
  ]
}`

func TestNotifyFindings_HappyPath(t *testing.T) {
	var gotBody []byte
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		if ct := r.Header.Get("Content-Type"); ct != "application/json" {
			t.Errorf("content-type = %q, want application/json", ct)
		}
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusNoContent) // Discord's success status
	}))
	defer srv.Close()

	res, err := NotifyFindings(context.Background(), NotifyOptions{
		LogPath:    writeLog(t, twoHighScoreLog),
		WebhookURL: srv.URL,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.Sent || res.Skipped {
		t.Fatalf("want Sent, got %+v", res)
	}
	if res.Eligible != 2 || res.Routed != 2 {
		t.Errorf("eligible/routed = %d/%d, want 2/2", res.Eligible, res.Routed)
	}
	if got := atomic.LoadInt32(&hits); got != 1 {
		t.Errorf("server hits = %d, want 1", got)
	}

	// Verify the embed payload shape + highest-score-first ordering.
	var payload notify.Payload
	if err := json.Unmarshal(gotBody, &payload); err != nil {
		t.Fatalf("decode posted body: %v", err)
	}
	if len(payload.Embeds) != 1 {
		t.Fatalf("embeds = %d, want 1", len(payload.Embeds))
	}
	embed := payload.Embeds[0]
	if embed.Title != "🔔 Release alert: claude-code 2.1.75" {
		t.Errorf("title = %q", embed.Title)
	}
	if embed.Timestamp != "2026-06-19T12:00:00Z" {
		t.Errorf("timestamp = %q, want the run_started_at (deterministic)", embed.Timestamp)
	}
	if embed.Color != notify.ColorCritical {
		t.Errorf("color = %#x, want critical (top score 88)", embed.Color)
	}
	if len(embed.Fields) != 2 {
		t.Fatalf("fields = %d, want 2", len(embed.Fields))
	}
	if embed.Fields[0].Name != "#101 · score 88" {
		t.Errorf("field[0] = %q, want the highest-score finding first", embed.Fields[0].Name)
	}
}

func TestNotifyFindings_CapsAtMaxItems(t *testing.T) {
	const log = `{
      "provider": "codex", "source": "openai/codex", "new_version": "1.0.0",
      "since_version": "0.9.0", "run_started_at": "2026-06-19T12:00:00Z",
      "issues_created": [
        {"number": 1, "title": "a", "url": "u1", "score": 90},
        {"number": 2, "title": "b", "url": "u2", "score": 85},
        {"number": 3, "title": "c", "url": "u3", "score": 80},
        {"number": 4, "title": "d", "url": "u4", "score": 75},
        {"number": 5, "title": "e", "url": "u5", "score": 72}
      ]}`
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	res, err := NotifyFindings(context.Background(), NotifyOptions{
		LogPath: writeLog(t, log), WebhookURL: srv.URL, MaxItems: 3,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Eligible != 5 || res.Routed != 3 {
		t.Errorf("eligible/routed = %d/%d, want 5/3", res.Eligible, res.Routed)
	}
	var payload notify.Payload
	_ = json.Unmarshal(gotBody, &payload)
	if n := len(payload.Embeds[0].Fields); n != 3 {
		t.Errorf("fields = %d, want 3 (capped)", n)
	}
	if desc := payload.Embeds[0].Description; !containsAll(desc, "5 high-impact", "top 3") {
		t.Errorf("description = %q, want eligible count + top-N note", desc)
	}
}

func TestNotifyFindings_BelowThresholdSkips(t *testing.T) {
	const log = `{"provider":"p","new_version":"1","issues_created":[
      {"number":1,"title":"minor","url":"u","score":40}]}`
	res, err := NotifyFindings(context.Background(), NotifyOptions{
		LogPath: writeLog(t, log), WebhookURL: "http://unused",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.Skipped || res.Sent || res.Routed != 0 {
		t.Errorf("want skipped/0, got %+v", res)
	}
}

func TestNotifyFindings_NoWebhookSkips(t *testing.T) {
	res, err := NotifyFindings(context.Background(), NotifyOptions{
		LogPath: writeLog(t, twoHighScoreLog), WebhookURL: "",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.Skipped || res.Sent {
		t.Errorf("want skipped (sink disabled), got %+v", res)
	}
	if res.Reason == "" {
		t.Error("expected a skip reason")
	}
}

func TestNotifyFindings_DryRunDoesNotPost(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	res, err := NotifyFindings(context.Background(), NotifyOptions{
		LogPath: writeLog(t, twoHighScoreLog), WebhookURL: srv.URL, DryRun: true,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Sent || !res.Skipped {
		t.Errorf("want skipped (dry-run), got %+v", res)
	}
	if got := atomic.LoadInt32(&hits); got != 0 {
		t.Errorf("server hits = %d, want 0 (dry-run must not POST)", got)
	}
}

func TestNotifyFindings_WebhookFailureIsBestEffort(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(http.StatusInternalServerError) // transient → retried
	}))
	defer srv.Close()

	res, err := NotifyFindings(context.Background(), NotifyOptions{
		LogPath: writeLog(t, twoHighScoreLog), WebhookURL: srv.URL,
	})
	if err != nil {
		t.Fatalf("best-effort: webhook failure must NOT return an error, got %v", err)
	}
	if res.Sent {
		t.Error("Sent should be false on webhook failure")
	}
	if res.Reason == "" {
		t.Error("expected a failure reason")
	}
	if got := atomic.LoadInt32(&hits); got != notify.MaxAttempts {
		t.Errorf("server hits = %d, want %d (retried)", got, notify.MaxAttempts)
	}
}

func TestNotifyFindings_TransportFailureDoesNotLeakWebhookURL(t *testing.T) {
	// The webhook URL IS the Discord credential. On a transport failure Go's
	// *url.Error embeds the full URL; the result Reason is printed to CI logs via
	// --json, so it must NOT contain the token/host. Port 1 => connection refused.
	const secretURL = "http://127.0.0.1:1/api/webhooks/123456789/SECRETTOKEN_AbC-dEf"
	res, err := NotifyFindings(context.Background(), NotifyOptions{
		LogPath: writeLog(t, twoHighScoreLog), WebhookURL: secretURL,
	})
	if err != nil {
		t.Fatalf("best-effort: must not return an error, got %v", err)
	}
	if res.Sent {
		t.Error("Sent should be false on transport failure")
	}
	for _, leak := range []string{"SECRETTOKEN_AbC-dEf", "123456789", "/api/webhooks/", secretURL} {
		if strings.Contains(res.Reason, leak) {
			t.Errorf("Reason leaks %q: %s", leak, res.Reason)
		}
	}
	// It should still carry a useful (non-secret) transport cause.
	if res.Reason == "" {
		t.Error("expected a non-empty failure reason")
	}
}


func TestNotifyFindings_PermanentStatusNotRetried(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(http.StatusBadRequest) // 4xx (not 429) → permanent
	}))
	defer srv.Close()

	res, err := NotifyFindings(context.Background(), NotifyOptions{
		LogPath: writeLog(t, twoHighScoreLog), WebhookURL: srv.URL,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Sent {
		t.Error("Sent should be false on a 4xx")
	}
	if got := atomic.LoadInt32(&hits); got != 1 {
		t.Errorf("server hits = %d, want 1 (4xx must not be retried)", got)
	}
}

func TestNotifyFindings_UnreadableLogIsHardError(t *testing.T) {
	_, err := NotifyFindings(context.Background(), NotifyOptions{
		LogPath: filepath.Join(t.TempDir(), "does-not-exist.json"), WebhookURL: "http://x",
	})
	if err == nil {
		t.Fatal("expected a hard error for a missing creation-log")
	}
}

func TestNotifyFindings_MalformedLogIsHardError(t *testing.T) {
	_, err := NotifyFindings(context.Background(), NotifyOptions{
		LogPath: writeLog(t, "{not json"), WebhookURL: "http://x",
	})
	if err == nil {
		t.Fatal("expected a hard error for a malformed creation-log")
	}
}

func TestColorForScore(t *testing.T) {
	cases := []struct {
		score int
		want  int
	}{
		{90, notify.ColorCritical},
		{85, notify.ColorCritical},
		{84, notify.ColorHigh},
		{70, notify.ColorHigh},
		{69, notify.ColorNotable},
	}
	for _, c := range cases {
		if got := colorForScore(c.score); got != c.want {
			t.Errorf("colorForScore(%d) = %#x, want %#x", c.score, got, c.want)
		}
	}
}

func containsAll(s string, subs ...string) bool {
	for _, sub := range subs {
		if !strings.Contains(s, sub) {
			return false
		}
	}
	return true
}
