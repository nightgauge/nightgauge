package release

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// stubReleasesServer returns an httptest.Server that serves a fixed JSON
// payload at /repos/{owner}/{repo}/releases and records the most recent
// request URL on lastReq.
func stubReleasesServer(t *testing.T, payload string, status int, lastReq *string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/", func(w http.ResponseWriter, r *http.Request) {
		if lastReq != nil {
			*lastReq = r.URL.Path + "?" + r.URL.RawQuery
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(payload))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestFetch_HappyPath(t *testing.T) {
	payload := `[
		{"tag_name":"v2.1.75","name":"2.1.75","published_at":"2026-04-22T10:00:00Z","body":"- Added foo","html_url":"u1","prerelease":false,"draft":false},
		{"tag_name":"v2.1.74","name":"2.1.74","published_at":"2026-04-15T10:00:00Z","body":"- Fixed bar","html_url":"u2","prerelease":false,"draft":false}
	]`
	var lastReq string
	srv := stubReleasesServer(t, payload, 200, &lastReq)

	result, err := Fetch(context.Background(), Options{
		Source:  "anthropics/claude-code",
		BaseURL: srv.URL,
		Limit:   10,
	})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if result.V != SchemaVersion {
		t.Errorf("V = %d, want %d", result.V, SchemaVersion)
	}
	if got, want := len(result.Releases), 2; got != want {
		t.Errorf("Releases length = %d, want %d", got, want)
	}
	if result.Releases[0].TagName != "v2.1.75" {
		t.Errorf("first tag = %q, want v2.1.75", result.Releases[0].TagName)
	}
	if !strings.Contains(lastReq, "/repos/anthropics/claude-code/releases") {
		t.Errorf("request path = %q, want /repos/anthropics/claude-code/releases", lastReq)
	}
	if !strings.Contains(lastReq, "per_page=10") {
		t.Errorf("request query = %q, want per_page=10", lastReq)
	}
	if result.FetchedAt == "" {
		t.Errorf("FetchedAt is empty")
	}
}

func TestFetch_SinceFilter(t *testing.T) {
	payload := `[
		{"tag_name":"v2.1.75","published_at":"2026-04-22T10:00:00Z","body":""},
		{"tag_name":"v2.1.74","published_at":"2026-04-15T10:00:00Z","body":""},
		{"tag_name":"v2.1.73","published_at":"2026-04-08T10:00:00Z","body":""}
	]`
	srv := stubReleasesServer(t, payload, 200, nil)

	result, err := Fetch(context.Background(), Options{
		Source:  "x/y",
		BaseURL: srv.URL,
		Since:   "2.1.74",
	})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	// Only v2.1.75 is strictly newer than 2.1.74.
	if got, want := len(result.Releases), 1; got != want {
		t.Fatalf("filtered length = %d, want %d (releases=%+v)", got, want, result.Releases)
	}
	if result.Releases[0].TagName != "v2.1.75" {
		t.Errorf("tag = %q, want v2.1.75", result.Releases[0].TagName)
	}
	if result.Filtered != 2 {
		t.Errorf("Filtered = %d, want 2", result.Filtered)
	}
}

func TestFetch_EmptyResult(t *testing.T) {
	srv := stubReleasesServer(t, `[]`, 200, nil)
	result, err := Fetch(context.Background(), Options{Source: "x/y", BaseURL: srv.URL})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(result.Releases) != 0 {
		t.Errorf("Releases length = %d, want 0", len(result.Releases))
	}
}

func TestFetch_LimitDefault(t *testing.T) {
	srv := stubReleasesServer(t, `[]`, 200, nil)
	result, err := Fetch(context.Background(), Options{Source: "x/y", BaseURL: srv.URL, Limit: 0})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if result.Limit != DefaultLimit {
		t.Errorf("Limit = %d, want %d", result.Limit, DefaultLimit)
	}
}

func TestFetch_TransportError(t *testing.T) {
	// Non-routable address with a tight timeout.
	_, err := Fetch(context.Background(), Options{
		Source:     "x/y",
		BaseURL:    "http://127.0.0.1:1",
		HTTPClient: &http.Client{Timeout: 100 * time.Millisecond},
	})
	if err == nil {
		t.Fatalf("expected transport error, got nil")
	}
}

func TestFetch_NonOKStatus(t *testing.T) {
	srv := stubReleasesServer(t, `not found`, 404, nil)
	_, err := Fetch(context.Background(), Options{Source: "x/y", BaseURL: srv.URL})
	if err == nil {
		t.Fatalf("expected error for 404, got nil")
	}
	if !strings.Contains(err.Error(), "404") {
		t.Errorf("error = %q, want it to mention 404", err.Error())
	}
}

func TestFetch_MalformedJSON(t *testing.T) {
	srv := stubReleasesServer(t, `{not-json`, 200, nil)
	_, err := Fetch(context.Background(), Options{Source: "x/y", BaseURL: srv.URL})
	if err == nil {
		t.Fatalf("expected decode error, got nil")
	}
}

func TestFetch_BadSource(t *testing.T) {
	cases := []string{"", "no-slash", "/missing-owner", "missing-repo/", "a/b/c"}
	for _, src := range cases {
		t.Run(src, func(t *testing.T) {
			_, err := Fetch(context.Background(), Options{Source: src})
			if err == nil {
				t.Fatalf("expected error for source %q, got nil", src)
			}
		})
	}
}

func TestFetch_BadSince(t *testing.T) {
	srv := stubReleasesServer(t, `[]`, 200, nil)
	_, err := Fetch(context.Background(), Options{Source: "x/y", BaseURL: srv.URL, Since: "abc"})
	if err == nil {
		t.Fatalf("expected error for malformed --since, got nil")
	}
}

func TestFetch_BearerHeader(t *testing.T) {
	var sawAuth string
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/", func(w http.ResponseWriter, r *http.Request) {
		sawAuth = r.Header.Get("Authorization")
		_, _ = fmt.Fprint(w, `[]`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	_, err := Fetch(context.Background(), Options{
		Source:  "x/y",
		BaseURL: srv.URL,
		Token:   "abc123",
	})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if sawAuth != "Bearer abc123" {
		t.Errorf("Authorization = %q, want %q", sawAuth, "Bearer abc123")
	}
}

func TestFetch_SkipsDraftsAndPrereleases(t *testing.T) {
	// Codex-style payload: a stable rust-v tag + an alpha pre-release + a draft.
	// Only the stable release should survive (#4056). Also exercises the rust-v
	// prefix through the full fetch+filter path.
	payload := `[
		{"tag_name":"rust-v0.141.0","name":"0.141.0","published_at":"2026-06-18T04:00:00Z","body":"- Added x","html_url":"u1","prerelease":false,"draft":false},
		{"tag_name":"rust-v0.142.0-alpha.4","name":"alpha","published_at":"2026-06-19T04:00:00Z","body":"- wip","html_url":"u2","prerelease":true,"draft":false},
		{"tag_name":"rust-v0.143.0","name":"draft","published_at":"2026-06-20T04:00:00Z","body":"- draft","html_url":"u3","prerelease":false,"draft":true}
	]`
	srv := stubReleasesServer(t, payload, 200, nil)
	result, err := Fetch(context.Background(), Options{Source: "openai/codex", BaseURL: srv.URL, Limit: 10})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if got, want := len(result.Releases), 1; got != want {
		t.Fatalf("Releases length = %d, want %d (only stable rust-v0.141.0)", got, want)
	}
	if result.Releases[0].TagName != "rust-v0.141.0" {
		t.Errorf("tag = %q, want rust-v0.141.0", result.Releases[0].TagName)
	}
	if result.Filtered != 2 {
		t.Errorf("Filtered = %d, want 2 (alpha + draft)", result.Filtered)
	}
}

func TestFetch_SkipsPrereleaseEvenWhenNewerThanSince(t *testing.T) {
	// A pre-release whose core version is newer than --since must STILL be
	// excluded — the Draft/Prerelease skip runs before the semver compare.
	payload := `[
		{"tag_name":"rust-v0.142.0-alpha.4","prerelease":true,"draft":false,"published_at":"2026-06-19T04:00:00Z","body":""},
		{"tag_name":"rust-v0.141.0","prerelease":false,"draft":false,"published_at":"2026-06-18T04:00:00Z","body":""}
	]`
	srv := stubReleasesServer(t, payload, 200, nil)
	result, err := Fetch(context.Background(), Options{Source: "openai/codex", BaseURL: srv.URL, Since: "0.140.0", Limit: 10})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if got, want := len(result.Releases), 1; got != want {
		t.Fatalf("Releases length = %d, want 1 (stable only)", got)
	}
	if result.Releases[0].TagName != "rust-v0.141.0" {
		t.Errorf("tag = %q, want rust-v0.141.0", result.Releases[0].TagName)
	}
}
