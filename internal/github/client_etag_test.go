package github

import (
	"bytes"
	"context"
	"io"
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
// request, no If-None-Match header is ever PRESENT (not merely empty — the
// distinction matters because a mutant that caches under an empty ETag would
// still make Header.Get return "" while actually sending the header), and no
// entry is ever created for it (Issue #486; kills mutant M5, which caches
// every 200 regardless of whether it carried an ETag).
func TestETagCache_NoETagFallsBackSilently(t *testing.T) {
	var (
		mu        sync.Mutex
		requests  int
		inmCounts []int // len(r.Header.Values("If-None-Match")) per request: 0 = header absent
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		requests++
		inmCounts = append(inmCounts, len(r.Header.Values("If-None-Match")))
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
	for i, n := range inmCounts {
		if n != 0 {
			t.Fatalf("request %d sent an If-None-Match header (present, count=%d) but server never issued an ETag", i, n)
		}
	}

	rt, ok := c.http.Transport.(*rateLimitHeaderTransport)
	if !ok {
		t.Fatal("expected rateLimitHeaderTransport on the http client")
	}
	if n := len(rt.etags.entries); n != 0 {
		t.Fatalf("an ETag-less endpoint must never be cached, got %d entries: %v", n, rt.etags.order)
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

// TestETagCache_OversizedBodyStreamsWithoutFullBuffering verifies the
// transport bounds its OWN read at etagCacheMaxBodyBytes+1 bytes instead of
// buffering an entire oversized response before deciding not to cache it
// (Issue #486 must-fix): the caller still receives every byte of an
// oversized ETag'd response, byte-for-byte, the response is never cached,
// and a second GET to the same URL sends no If-None-Match. This is what lets
// a caller with its own bounded reader (e.g. CIService.fetchLogContent's
// io.LimitReader over a multi-hundred-MB CI log archive) actually bound its
// memory use instead of the transport reading the whole archive first.
func TestETagCache_OversizedBodyStreamsWithoutFullBuffering(t *testing.T) {
	body := bytes.Repeat([]byte("x"), etagCacheMaxBodyBytes+1024)
	var (
		mu   sync.Mutex
		inms []string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		inms = append(inms, r.Header.Get("If-None-Match"))
		mu.Unlock()
		w.Header().Set("ETag", `"big-etag"`)
		w.Header().Set("Content-Type", "application/octet-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)

	c := NewClientWithURL("test-token", srv.URL+"/graphql")
	ctx := context.Background()

	got, err := c.restGet(ctx, "/repos/o/r/big")
	if err != nil {
		t.Fatalf("first fetch: %v", err)
	}
	if len(got) != len(body) || !bytes.Equal(got, body) {
		t.Fatalf("caller received %d bytes (want %d, byte-for-byte)", len(got), len(body))
	}

	rt, ok := c.http.Transport.(*rateLimitHeaderTransport)
	if !ok {
		t.Fatal("expected rateLimitHeaderTransport on the http client")
	}
	if n := len(rt.etags.entries); n != 0 {
		t.Fatalf("an oversized response must never be cached, got %d entries", n)
	}

	if _, err := c.restGet(ctx, "/repos/o/r/big"); err != nil {
		t.Fatalf("second fetch: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(inms) != 2 {
		t.Fatalf("expected 2 requests, got %d", len(inms))
	}
	if inms[1] != "" {
		t.Fatalf("second GET to an oversized, never-cached response must send no If-None-Match, got %q", inms[1])
	}
}

// TestETagCache_304ReplayRestoresRepresentationHeaders verifies a 304 replay
// restores the cached 200's representation headers (Issue #486 must-fix): a
// header the 304 never echoes (e.g. Link, Content-Type — a 304 is only
// obliged to echo a handful of headers per RFC 9110 §15.4.5) survives the
// replay from the cached 200, while a header the 304 DOES carry (Date) wins
// over the cached value — proving this is an absent-only restore, not a
// blind overwrite (a blind overwrite would push a stale value over a fresh
// one, which for X-RateLimit-* would corrupt the rate-limit tracker).
func TestETagCache_304ReplayRestoresRepresentationHeaders(t *testing.T) {
	var (
		mu  sync.Mutex
		hit bool
	)
	const etag = `"scopes-etag"`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.Header.Get("If-None-Match") == etag {
			hit = true
			w.Header().Set("ETag", etag)
			w.Header().Set("Date", "Wed, 01 Jan 2026 00:00:00 GMT")
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", etag)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Link", `<https://api.github.com/repos/o/r?page=2>; rel="next"`)
		w.Header().Set("Date", "Tue, 31 Dec 2025 00:00:00 GMT")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(srv.Close)

	c := NewClientWithURL("test-token", srv.URL+"/graphql")
	ctx := context.Background()

	req1, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/repos/o/r/headers", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp1, err := c.http.Do(req1)
	if err != nil {
		t.Fatalf("first fetch: %v", err)
	}
	resp1.Body.Close()
	if got := resp1.Header.Get("Content-Type"); got != "application/json" {
		t.Fatalf("first fetch Content-Type = %q", got)
	}
	if got := resp1.Header.Get("Link"); got == "" {
		t.Fatal("first fetch missing Link header")
	}
	if got := resp1.Header.Get("Date"); got != "Tue, 31 Dec 2025 00:00:00 GMT" {
		t.Fatalf("first fetch Date = %q", got)
	}

	req2, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/repos/o/r/headers", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp2, err := c.http.Do(req2)
	if err != nil {
		t.Fatalf("second fetch: %v", err)
	}
	resp2.Body.Close()

	mu.Lock()
	gotHit := hit
	mu.Unlock()
	if !gotHit {
		t.Fatal("expected a 304 on the second fetch")
	}
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("replayed status = %d, want 200", resp2.StatusCode)
	}
	if got := resp2.Header.Get("Content-Type"); got != "application/json" {
		t.Fatalf("replayed Content-Type = %q, want it restored from the cached 200", got)
	}
	if got := resp2.Header.Get("Link"); got == "" {
		t.Fatal("replayed response missing Link header restored from the cached 200")
	}
	if got := resp2.Header.Get("Date"); got != "Wed, 01 Jan 2026 00:00:00 GMT" {
		t.Fatalf("replayed Date = %q, want the 304's OWN Date to win over the cached 200's Date", got)
	}
}

// TestETagCache_NonGETNeverConditional pins the GET-only guard (Issue #486
// should-fix): a non-GET request (a REST POST) must never be conditionally
// cached even when the server returns an ETag on every response — dropping
// the method guard would let unrelated POST bodies collide on one cache key,
// since every GraphQL POST in this package targets the identical /graphql
// URL.
func TestETagCache_NonGETNeverConditional(t *testing.T) {
	var (
		mu       sync.Mutex
		methods  []string
		inmCount []int
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		methods = append(methods, r.Method)
		inmCount = append(inmCount, len(r.Header.Values("If-None-Match")))
		mu.Unlock()
		w.Header().Set("ETag", `"post-etag"`)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(srv.Close)

	c := NewClientWithURL("test-token", srv.URL+"/graphql")
	ctx := context.Background()

	for i := 0; i < 2; i++ {
		if _, err := c.restPost(ctx, "/repos/o/r/thing", map[string]any{"a": 1}); err != nil {
			t.Fatalf("post %d: %v", i, err)
		}
	}

	mu.Lock()
	defer mu.Unlock()
	if len(methods) != 2 {
		t.Fatalf("expected 2 POST requests, got %d", len(methods))
	}
	for i, m := range methods {
		if m != http.MethodPost {
			t.Fatalf("request %d method = %q, want POST", i, m)
		}
		if inmCount[i] != 0 {
			t.Fatalf("request %d sent an If-None-Match header on a POST, count=%d", i, inmCount[i])
		}
	}

	rt, ok := c.http.Transport.(*rateLimitHeaderTransport)
	if !ok {
		t.Fatal("expected rateLimitHeaderTransport on the http client")
	}
	if n := len(rt.etags.entries); n != 0 {
		t.Fatalf("a POST response must never be cached, got %d entries", n)
	}
}

// TestETagCache_RedirectTargetHostNeverCached verifies the ETag layer is
// scoped to the client's own API host (Issue #486 should-fix): a GET that
// 302-redirects to a different host — mirroring GitHub's Actions-logs ->
// blob-storage redirect — never sends If-None-Match to that other host and
// never occupies a cache slot for it, even though the redirected response
// itself carries an ETag.
func TestETagCache_RedirectTargetHostNeverCached(t *testing.T) {
	var (
		mu   sync.Mutex
		inms []string
	)
	blob := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		inms = append(inms, r.Header.Get("If-None-Match"))
		mu.Unlock()
		w.Header().Set("ETag", `"blob-etag"`)
		w.Header().Set("Content-Type", "application/octet-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("blob-content"))
	}))
	t.Cleanup(blob.Close)

	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, blob.URL+"/download", http.StatusFound)
	}))
	t.Cleanup(api.Close)

	c := NewClientWithURL("test-token", api.URL+"/graphql")
	ctx := context.Background()

	for i := 0; i < 2; i++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, api.URL+"/repos/o/r/logs", nil)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := c.http.Do(req)
		if err != nil {
			t.Fatalf("fetch %d: %v", i, err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if string(body) != "blob-content" {
			t.Fatalf("fetch %d body = %q", i, body)
		}
	}

	mu.Lock()
	defer mu.Unlock()
	if len(inms) != 2 {
		t.Fatalf("expected 2 requests to the redirect target, got %d", len(inms))
	}
	for i, v := range inms {
		if v != "" {
			t.Fatalf("redirect-target request %d sent If-None-Match %q — a different host must never be cached", i, v)
		}
	}

	rt, ok := c.http.Transport.(*rateLimitHeaderTransport)
	if !ok {
		t.Fatal("expected rateLimitHeaderTransport on the http client")
	}
	if n := len(rt.etags.entries); n != 0 {
		t.Fatalf("a different-host response must never be cached, got %d entries: %v", n, rt.etags.order)
	}
}

// TestETagCache_LRUEvictsLeastRecentlyUsed verifies eviction promotes on
// both get and set-refresh (Issue #486 should-fix, FIFO -> LRU): re-reading
// the OLDEST entry before inserting one more must save it from eviction, and
// the entry that was never re-read (the genuinely least-recently-used one)
// is evicted instead. This fails under FIFO-by-insertion-order and passes
// only under LRU.
func TestETagCache_LRUEvictsLeastRecentlyUsed(t *testing.T) {
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
	rt.etags = newETagCache(3)

	ctx := context.Background()
	// Prime 3 URLs to the limit: insertion order is now [/a, /b, /c].
	for _, p := range []string{"/a", "/b", "/c"} {
		if _, err := c.restGet(ctx, p); err != nil {
			t.Fatalf("prime %s: %v", p, err)
		}
	}

	// Re-read the OLDEST entry (/a). Under LRU this promotes it to the
	// most-recently-used end; under FIFO this has no effect on order.
	if _, err := c.restGet(ctx, "/a"); err != nil {
		t.Fatalf("re-read /a: %v", err)
	}

	// Insert one more distinct URL, forcing an eviction.
	if _, err := c.restGet(ctx, "/e"); err != nil {
		t.Fatalf("insert /e: %v", err)
	}

	// /a was just re-read: it must survive (a repeat GET still carries a
	// cached If-None-Match — this is the 3rd request to /a).
	if _, err := c.restGet(ctx, "/a"); err != nil {
		t.Fatalf("re-fetch /a: %v", err)
	}
	mu.Lock()
	aReqs := append([]string(nil), inmByPath["/a"]...)
	mu.Unlock()
	if len(aReqs) != 3 {
		t.Fatalf("expected 3 requests to /a (prime, re-read, re-fetch), got %d: %v", len(aReqs), aReqs)
	}
	if aReqs[2] == "" {
		t.Fatal("/a must still be cached after being re-read (LRU-promoted) — expected a non-empty If-None-Match on the 3rd request")
	}

	// /b was never re-read: it is the genuinely least-recently-used entry
	// and must have been evicted to make room for /e.
	if _, err := c.restGet(ctx, "/b"); err != nil {
		t.Fatalf("re-fetch /b: %v", err)
	}
	mu.Lock()
	bReqs := append([]string(nil), inmByPath["/b"]...)
	mu.Unlock()
	if len(bReqs) != 2 {
		t.Fatalf("expected 2 requests to /b (prime, re-fetch), got %d: %v", len(bReqs), bReqs)
	}
	if bReqs[1] != "" {
		t.Fatalf("/b (least-recently-used) must have been evicted, got non-empty If-None-Match %q", bReqs[1])
	}
}

// TestETagCache_ByteBudgetEvictsIndependentlyOfEntryCount verifies the cache
// also evicts under byte pressure, independent of the entry-count cap (Issue
// #486 should-fix): a small byteLimit forces eviction after 2 entries even
// though the entry-count cap is a generous 10.
func TestETagCache_ByteBudgetEvictsIndependentlyOfEntryCount(t *testing.T) {
	var mu sync.Mutex
	inmByPath := map[string][]string{}
	payload := `{"padding":"01234567890123456789012345678901234"}`
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
		_, _ = w.Write([]byte(payload))
	}))
	t.Cleanup(srv.Close)

	c := NewClientWithURL("test-token", srv.URL+"/graphql")
	rt, ok := c.http.Transport.(*rateLimitHeaderTransport)
	if !ok {
		t.Fatal("expected rateLimitHeaderTransport on the http client")
	}
	rt.etags = newETagCache(10)            // entry-count cap is generous...
	rt.etags.byteLimit = len(payload) + 20 // ...but the byte budget isn't: room for ~1 entry.

	ctx := context.Background()
	if _, err := c.restGet(ctx, "/x"); err != nil {
		t.Fatalf("prime /x: %v", err)
	}
	if _, err := c.restGet(ctx, "/y"); err != nil {
		t.Fatalf("prime /y: %v", err)
	}

	if got := len(rt.etags.entries); got != 1 {
		t.Fatalf("expected byte budget to bound the cache at 1 entry (well under the 10-entry cap), got %d: %v", got, rt.etags.order)
	}

	// /y is the most recent insertion and must still be cached. Checked via
	// a conditional-hit re-fetch (304, no set() call) BEFORE touching /x
	// below — re-fetching the evicted /x re-inserts it and would itself
	// evict /y under this tiny byte budget, so order matters here.
	if _, err := c.restGet(ctx, "/y"); err != nil {
		t.Fatalf("re-fetch /y: %v", err)
	}
	mu.Lock()
	yReqs := append([]string(nil), inmByPath["/y"]...)
	mu.Unlock()
	wantETag := `"/y-etag"`
	if len(yReqs) != 2 || yReqs[1] != wantETag {
		t.Fatalf("expected /y still cached (If-None-Match %q on re-fetch), got %v", wantETag, yReqs)
	}

	// /x was evicted for byte pressure: a re-fetch must not carry
	// If-None-Match.
	if _, err := c.restGet(ctx, "/x"); err != nil {
		t.Fatalf("re-fetch /x: %v", err)
	}
	mu.Lock()
	xReqs := append([]string(nil), inmByPath["/x"]...)
	mu.Unlock()
	if len(xReqs) != 2 || xReqs[1] != "" {
		t.Fatalf("expected /x evicted under byte pressure (no If-None-Match on re-fetch), got %v", xReqs)
	}
}
