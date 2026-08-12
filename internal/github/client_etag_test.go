package github

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"
)

// etagServerState is the mutable state behind a test conditional-GET REST
// server: a body + ETag pair that changes on demand, a rate-limit remaining
// counter that only decrements on a real (non-304) response — mirroring
// GitHub's own behavior, where a confirmed conditional hit costs no budget —
// and a log of every request's If-None-Match header for assertions.
type etagServerState struct {
	mu        sync.Mutex
	body      string
	etag      string
	remaining int
	requests  int
	inms      []string // If-None-Match header value seen on each request, in order
}

func newETagTestServer(t *testing.T, state *etagServerState) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		state.mu.Lock()
		defer state.mu.Unlock()
		state.requests++
		state.inms = append(state.inms, r.Header.Get("If-None-Match"))

		resetAt := time.Now().Add(time.Hour).Unix()
		if r.Header.Get("If-None-Match") == state.etag {
			// Conditional hit: GitHub does not decrement remaining budget
			// for a 304 — the header carries the SAME remaining value as
			// the last real response.
			w.Header().Set("ETag", state.etag)
			w.Header().Set("X-RateLimit-Limit", "5000")
			w.Header().Set("X-RateLimit-Remaining", strconv.Itoa(state.remaining))
			w.Header().Set("X-RateLimit-Reset", strconv.FormatInt(resetAt, 10))
			w.WriteHeader(http.StatusNotModified)
			return
		}

		state.remaining--
		w.Header().Set("ETag", state.etag)
		w.Header().Set("X-RateLimit-Limit", "5000")
		w.Header().Set("X-RateLimit-Remaining", strconv.Itoa(state.remaining))
		w.Header().Set("X-RateLimit-Reset", strconv.FormatInt(resetAt, 10))
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(state.body))
	}))
	t.Cleanup(srv.Close)
	return NewClientWithURL("test-token", srv.URL+"/graphql")
}

// TestETagCache_ConditionalGETLifecycle covers the full Issue #486 lifecycle:
// first fetch stores the ETag, a second fetch to the same URL sends
// If-None-Match and is served from cache on 304, changed upstream data (200
// + a new ETag) replaces the cache, and 304 responses leave the rate-limit
// tracker's remaining-budget accounting untouched (pinned to exact values,
// not a >=0 check).
func TestETagCache_ConditionalGETLifecycle(t *testing.T) {
	state := &etagServerState{
		body:      `{"id":1,"value":"first"}`,
		etag:      `"etag-v1"`,
		remaining: 4999,
	}
	c := newETagTestServer(t, state)

	path := filepath.Join(t.TempDir(), "rate-limit.json")
	tr := NewSharedRateLimitTracker(path)
	c.WithRateLimitTracker(tr, "alice")

	ctx := context.Background()

	// 1) First fetch: no cache yet, must NOT send If-None-Match, stores the
	//    ETag, and the (real) response decrements remaining 4999 -> 4998.
	b1, err := c.restGet(ctx, "/repos/o/r")
	if err != nil {
		t.Fatalf("first fetch: %v", err)
	}
	if string(b1) != state.body {
		t.Fatalf("first fetch body = %q, want %q", b1, state.body)
	}
	state.mu.Lock()
	if state.requests != 1 {
		t.Fatalf("expected 1 request after first fetch, got %d", state.requests)
	}
	if state.inms[0] != "" {
		t.Fatalf("first fetch must not send If-None-Match, got %q", state.inms[0])
	}
	state.mu.Unlock()

	entry1, _, err := tr.Get("alice")
	if err != nil {
		t.Fatalf("tracker.Get after first fetch: %v", err)
	}
	if entry1 == nil || entry1.Remaining != 4998 {
		t.Fatalf("after first fetch: remaining = %+v, want 4998", entry1)
	}

	// 2) Second fetch to the SAME URL: cache hit → must send If-None-Match
	//    with the stored ETag; server returns 304; client serves the cached
	//    body verbatim; remaining stays 4998 (no decrement on 304).
	b2, err := c.restGet(ctx, "/repos/o/r")
	if err != nil {
		t.Fatalf("second fetch: %v", err)
	}
	if string(b2) != state.body {
		t.Fatalf("second fetch body = %q, want cached %q", b2, state.body)
	}
	state.mu.Lock()
	if state.requests != 2 {
		t.Fatalf("expected 2 requests after second fetch, got %d", state.requests)
	}
	if state.inms[1] != `"etag-v1"` {
		t.Fatalf("second fetch If-None-Match = %q, want %q", state.inms[1], `"etag-v1"`)
	}
	state.mu.Unlock()

	entry2, _, err := tr.Get("alice")
	if err != nil {
		t.Fatalf("tracker.Get after second fetch: %v", err)
	}
	if entry2 == nil || entry2.Remaining != 4998 {
		t.Fatalf("304 must not decrement budget: remaining = %+v, want 4998 (unchanged)", entry2)
	}

	// 3) Upstream data changes (new body + new ETag). Third fetch still
	//    sends the OLD If-None-Match (client doesn't know yet), gets a fresh
	//    200 with the new ETag, and the cache is replaced with the new pair.
	state.mu.Lock()
	state.body = `{"id":1,"value":"second"}`
	state.etag = `"etag-v2"`
	state.mu.Unlock()

	b3, err := c.restGet(ctx, "/repos/o/r")
	if err != nil {
		t.Fatalf("third fetch: %v", err)
	}
	if string(b3) != `{"id":1,"value":"second"}` {
		t.Fatalf("third fetch body = %q, want the new upstream body", b3)
	}
	state.mu.Lock()
	if state.requests != 3 {
		t.Fatalf("expected 3 requests after third fetch, got %d", state.requests)
	}
	if state.inms[2] != `"etag-v1"` {
		t.Fatalf("third fetch If-None-Match = %q, want the still-cached old ETag %q", state.inms[2], `"etag-v1"`)
	}
	state.mu.Unlock()

	entry3, _, err := tr.Get("alice")
	if err != nil {
		t.Fatalf("tracker.Get after third fetch: %v", err)
	}
	if entry3 == nil || entry3.Remaining != 4997 {
		t.Fatalf("a real 200 must decrement budget: remaining = %+v, want 4997", entry3)
	}

	// 4) Fourth fetch: cache now holds the NEW ETag from step 3 — confirms
	//    the replacement took effect. Sends If-None-Match: "etag-v2", gets
	//    304, serves the new cached body, remaining stays 4997.
	b4, err := c.restGet(ctx, "/repos/o/r")
	if err != nil {
		t.Fatalf("fourth fetch: %v", err)
	}
	if string(b4) != `{"id":1,"value":"second"}` {
		t.Fatalf("fourth fetch body = %q, want the new cached body", b4)
	}
	state.mu.Lock()
	if state.requests != 4 {
		t.Fatalf("expected 4 requests after fourth fetch, got %d", state.requests)
	}
	if state.inms[3] != `"etag-v2"` {
		t.Fatalf("fourth fetch If-None-Match = %q, want the replaced ETag %q", state.inms[3], `"etag-v2"`)
	}
	state.mu.Unlock()

	entry4, _, err := tr.Get("alice")
	if err != nil {
		t.Fatalf("tracker.Get after fourth fetch: %v", err)
	}
	if entry4 == nil || entry4.Remaining != 4997 {
		t.Fatalf("second 304 must not decrement budget: remaining = %+v, want 4997 (unchanged)", entry4)
	}
}

// TestETagCache_NoETagFallsBackSilently verifies that a GET endpoint which
// never returns an ETag is never cached: every call dispatches a full
// request, and no If-None-Match header is ever sent — the documented
// silent-fallback behavior for endpoints without ETag support (Issue #486).
func TestETagCache_NoETagFallsBackSilently(t *testing.T) {
	var (
		mu       sync.Mutex
		requests int
		inms     []string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		requests++
		inms = append(inms, r.Header.Get("If-None-Match"))
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(srv.Close)

	c := NewClientWithURL("test-token", srv.URL+"/graphql")
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		b, err := c.restGet(ctx, "/repos/o/r/no-etag")
		if err != nil {
			t.Fatalf("fetch %d: %v", i, err)
		}
		if string(b) != `{"ok":true}` {
			t.Fatalf("fetch %d body = %q", i, b)
		}
	}

	mu.Lock()
	defer mu.Unlock()
	if requests != 3 {
		t.Fatalf("expected 3 full requests (no caching without ETag), got %d", requests)
	}
	for i, v := range inms {
		if v != "" {
			t.Fatalf("request %d sent If-None-Match %q but server never issued an ETag", i, v)
		}
	}
}

// TestETagCache_DistinctURLsCachedIndependently verifies the cache is keyed
// per request URL, not shared globally — two different endpoints don't
// collide.
func TestETagCache_DistinctURLsCachedIndependently(t *testing.T) {
	var mu sync.Mutex
	seen := map[string][]string{} // path -> If-None-Match values seen
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen[r.URL.Path] = append(seen[r.URL.Path], r.Header.Get("If-None-Match"))
		mu.Unlock()
		etag := `"` + r.URL.Path + `-etag"`
		if r.Header.Get("If-None-Match") == etag {
			w.Header().Set("ETag", etag)
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", etag)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"path":"` + r.URL.Path + `"}`))
	}))
	t.Cleanup(srv.Close)

	c := NewClientWithURL("test-token", srv.URL+"/graphql")
	ctx := context.Background()

	for _, p := range []string{"/repos/o/r/a", "/repos/o/r/b", "/repos/o/r/a", "/repos/o/r/b"} {
		if _, err := c.restGet(ctx, p); err != nil {
			t.Fatalf("fetch %s: %v", p, err)
		}
	}

	mu.Lock()
	defer mu.Unlock()
	for _, p := range []string{"/repos/o/r/a", "/repos/o/r/b"} {
		vals, ok := seen[p]
		if !ok || len(vals) != 2 {
			t.Fatalf("path %s: expected 2 requests, got %v", p, vals)
		}
		if vals[0] != "" {
			t.Fatalf("path %s: first request must not send If-None-Match, got %q", p, vals[0])
		}
		wantETag := `"` + p + `-etag"`
		if vals[1] != wantETag {
			t.Fatalf("path %s: second request If-None-Match = %q, want %q", p, vals[1], wantETag)
		}
	}
}

// TestETagCache_BoundedEviction verifies the ETag cache is bounded: once the
// configured entry limit is reached, the oldest entry is evicted (FIFO) to
// make room for the newest, so a long-running process never grows this
// cache without bound (Issue #486).
func TestETagCache_BoundedEviction(t *testing.T) {
	var mu sync.Mutex
	inmByPath := map[string][]string{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		inmByPath[r.URL.Path] = append(inmByPath[r.URL.Path], r.Header.Get("If-None-Match"))
		mu.Unlock()
		etag := `"` + r.URL.Path + `-etag"`
		if r.Header.Get("If-None-Match") == etag {
			w.Header().Set("ETag", etag)
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", etag)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"path":"` + r.URL.Path + `"}`))
	}))
	t.Cleanup(srv.Close)

	c := NewClientWithURL("test-token", srv.URL+"/graphql")
	rt, ok := c.http.Transport.(*rateLimitHeaderTransport)
	if !ok {
		t.Fatal("expected rateLimitHeaderTransport on the http client")
	}
	// Shrink the cache to 3 entries so eviction is exercised without
	// hundreds of requests.
	rt.etags = newETagCache(3)

	ctx := context.Background()
	paths := []string{"/a", "/b", "/c", "/d"} // 4 distinct URLs, limit is 3
	for _, p := range paths {
		if _, err := c.restGet(ctx, p); err != nil {
			t.Fatalf("prime %s: %v", p, err)
		}
	}

	if got := len(rt.etags.entries); got != 3 {
		t.Fatalf("expected cache bounded at 3 entries, got %d: %v", got, rt.etags.order)
	}

	// /a was the first (oldest) insertion and must have been evicted to make
	// room for /d — a repeat GET to /a must NOT send If-None-Match.
	if _, err := c.restGet(ctx, "/a"); err != nil {
		t.Fatalf("re-fetch /a: %v", err)
	}
	mu.Lock()
	aReqs := inmByPath["/a"]
	mu.Unlock()
	if len(aReqs) != 2 {
		t.Fatalf("expected 2 requests total to /a, got %d", len(aReqs))
	}
	if aReqs[1] != "" {
		t.Fatalf("evicted entry /a must not send If-None-Match on re-fetch, got %q", aReqs[1])
	}

	// /d is the most recent insertion and must still be cached — a repeat
	// GET to /d sends If-None-Match and gets a 304.
	if _, err := c.restGet(ctx, "/d"); err != nil {
		t.Fatalf("re-fetch /d: %v", err)
	}
	mu.Lock()
	dReqs := inmByPath["/d"]
	mu.Unlock()
	if len(dReqs) != 2 {
		t.Fatalf("expected 2 requests total to /d, got %d", len(dReqs))
	}
	wantETag := `"/d-etag"`
	if dReqs[1] != wantETag {
		t.Fatalf("still-cached entry /d must send If-None-Match %q on re-fetch, got %q", wantETag, dReqs[1])
	}
}
