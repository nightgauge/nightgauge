package github

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/forge"
)

// rawGraphQLServer answers queryRaw's requests with answer(n), n counting from
// 1, and the backoff's rateLimit probe with a budget that has room, so a retry
// waits the shortest backoff. It counts only the requests queryRaw sent.
func rawGraphQLServer(t *testing.T, answer func(n int32, w http.ResponseWriter)) (*httptest.Server, *int32) {
	t.Helper()
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(string(body), "rateLimit") {
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"data": map[string]interface{}{
				"rateLimit": map[string]interface{}{
					"remaining": 4000,
					"resetAt":   time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
				},
			}})
			return
		}
		answer(atomic.AddInt32(&calls, 1), w)
	}))
	t.Cleanup(srv.Close)
	return srv, &calls
}

const rateLimitedBody = `{"errors":[{"type":"RATE_LIMITED","message":"API rate limit exceeded for user ID 1."}]}`

// Below the floor, the forge graphql pass-through sends nothing and says why.
func TestExecuteGraphQL_BelowFloorSendsNothing(t *testing.T) {
	t.Setenv(rateLimitFloorEnv, "100")
	srv, calls := rawGraphQLServer(t, func(_ int32, w http.ResponseWriter) {
		_, _ = io.WriteString(w, `{"data":{"viewer":{"login":"octocat"}}}`)
	})
	tr := NewSharedRateLimitTracker(filepath.Join(t.TempDir(), "rate-limit.json"))
	if err := tr.Set("alice", ResourceGraphQL, &RateLimitInfo{
		Remaining: 5, Limit: 5000, ResetAt: time.Now().Add(time.Hour).Unix(),
	}); err != nil {
		t.Fatalf("seed tracker: %v", err)
	}
	c := NewClientWithURL("test-token", srv.URL).WithRateLimitTracker(tr, "alice")
	c.gateLogger = func(string, ...interface{}) {}

	raw, err := c.ExecuteGraphQL(context.Background(), `query { viewer { login } }`, nil)
	if !errors.Is(err, ErrRateLimitGated) {
		t.Fatalf("err = %v, want ErrRateLimitGated", err)
	}
	if raw != nil {
		t.Fatalf("returned %q alongside a gated call", raw)
	}
	if got := atomic.LoadInt32(calls); got != 0 {
		t.Fatalf("requests = %d, want 0 below the floor", got)
	}
}

// A 200 whose GraphQL errors are rate-limited is retried, as Client.query
// retries it, and the retry's answer is returned.
func TestQueryRaw_RetriesRateLimitedBody(t *testing.T) {
	srv, calls := rawGraphQLServer(t, func(n int32, w http.ResponseWriter) {
		if n == 1 {
			_, _ = io.WriteString(w, rateLimitedBody)
			return
		}
		_, _ = io.WriteString(w, `{"data":{"viewer":{"login":"octocat"}}}`)
	})
	c := NewClientWithURL("test-token", srv.URL)

	raw, err := c.queryRaw(context.Background(), `query { viewer { login } }`, nil)
	if err != nil {
		t.Fatalf("queryRaw: %v", err)
	}
	if !strings.Contains(string(raw), "octocat") {
		t.Fatalf("body = %s, want the retry's answer", raw)
	}
	if got := atomic.LoadInt32(calls); got != 2 {
		t.Fatalf("requests = %d, want 2 (one rate-limited, one retry)", got)
	}
}

// A rate-limited answer that outlasts the retries is a typed rate-limit error,
// whatever shape it came in.
func TestQueryRaw_ExhaustedRetriesAreTyped(t *testing.T) {
	cases := []struct {
		name   string
		answer func(w http.ResponseWriter)
	}{
		{"403 rate limit", func(w http.ResponseWriter) {
			w.WriteHeader(http.StatusForbidden)
			_, _ = io.WriteString(w, `{"message":"API rate limit exceeded for user ID 1."}`)
		}},
		{"403 secondary limit with Retry-After", func(w http.ResponseWriter) {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusForbidden)
			_, _ = io.WriteString(w, `{"message":"You have exceeded a secondary rate limit."}`)
		}},
		{"429", func(w http.ResponseWriter) {
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = io.WriteString(w, `{"message":"too many requests"}`)
		}},
		{"200 RATE_LIMITED", func(w http.ResponseWriter) {
			_, _ = io.WriteString(w, rateLimitedBody)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			srv, calls := rawGraphQLServer(t, func(_ int32, w http.ResponseWriter) { tc.answer(w) })
			c := NewClientWithURL("test-token", srv.URL)

			raw, err := c.queryRaw(context.Background(), `query { viewer { login } }`, nil)
			if !errors.Is(err, forge.ErrRateLimited) {
				t.Fatalf("err = %v, want forge.ErrRateLimited", err)
			}
			if raw != nil {
				t.Fatalf("returned %q alongside the error", raw)
			}
			if got := atomic.LoadInt32(calls); got != maxRetries+1 {
				t.Fatalf("requests = %d, want %d", got, maxRetries+1)
			}
		})
	}
}

// A 403 with no rate-limit signal is a permission failure: it is not retried
// and does not read as a rate limit.
func TestQueryRaw_Genuine403IsNotARateLimit(t *testing.T) {
	srv, calls := rawGraphQLServer(t, func(_ int32, w http.ResponseWriter) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, `{"message":"Resource not accessible by integration"}`)
	})
	c := NewClientWithURL("test-token", srv.URL)

	_, err := c.queryRaw(context.Background(), `query { viewer { login } }`, nil)
	if err == nil || errors.Is(err, forge.ErrRateLimited) {
		t.Fatalf("err = %v, want an error that is not forge.ErrRateLimited", err)
	}
	if !strings.Contains(err.Error(), "status 403") {
		t.Fatalf("err = %v, want it to name the status", err)
	}
	if got := atomic.LoadInt32(calls); got != 1 {
		t.Fatalf("requests = %d, want 1", got)
	}
}
