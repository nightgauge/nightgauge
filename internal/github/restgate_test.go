package github

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type gateTransport struct {
	inFlight, peak atomic.Int32
	hold           time.Duration
	respond        func(n int32, req *http.Request) (int, http.Header, string)
	calls          atomic.Int32
	mu             sync.Mutex
	at             []time.Time
}

func (g *gateTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	n := g.calls.Add(1)
	g.mu.Lock()
	g.at = append(g.at, time.Now())
	g.mu.Unlock()
	cur := g.inFlight.Add(1)
	for {
		p := g.peak.Load()
		if cur <= p || g.peak.CompareAndSwap(p, cur) {
			break
		}
	}
	time.Sleep(g.hold)
	g.inFlight.Add(-1)
	status, hdr, body := 200, http.Header{}, "{}"
	if g.respond != nil {
		status, hdr, body = g.respond(n, req)
	}
	return &http.Response{StatusCode: status, Header: hdr, Body: io.NopCloser(strings.NewReader(body)), Request: req}, nil
}

// Every REST request in the process shares one bound on requests in flight,
// however many clients and goroutines issue them.
func TestRESTGate_BoundsRequestsInFlightAcrossClients(t *testing.T) {
	tr := &gateTransport{hold: 20 * time.Millisecond}
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		c := NewClientWithHTTPClient(&http.Client{Transport: tr})
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, _, err := c.restDoStatus(context.Background(), http.MethodGet, "/repos/o/r", nil); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if p := tr.peak.Load(); p > restMaxInFlight {
		t.Fatalf("peak in flight = %d, want <= %d", p, restMaxInFlight)
	}
}

// One caller's Retry-After holds every other caller's next request too.
func TestRESTGate_RetryAfterPausesEveryCaller(t *testing.T) {
	tr := &gateTransport{respond: func(n int32, req *http.Request) (int, http.Header, string) {
		if n == 1 {
			return 429, http.Header{"Retry-After": []string{"1"}}, `{"message":"You have exceeded a secondary rate limit"}`
		}
		return 200, http.Header{}, "{}"
	}}
	a := NewClientWithHTTPClient(&http.Client{Transport: tr})
	b := NewClientWithHTTPClient(&http.Client{Transport: tr})
	start := time.Now()
	done := make(chan struct{})
	go func() {
		defer close(done)
		if _, _, err := a.restDoStatus(context.Background(), http.MethodGet, "/a", nil); err != nil {
			t.Error(err)
		}
	}()
	for tr.calls.Load() == 0 {
		time.Sleep(time.Millisecond)
	}
	time.Sleep(50 * time.Millisecond) // a has received its 429 and set the pause
	if _, _, err := b.restDoStatus(context.Background(), http.MethodGet, "/b", nil); err != nil {
		t.Fatal(err)
	}
	<-done
	tr.mu.Lock()
	defer tr.mu.Unlock()
	for i, at := range tr.at[1:] {
		if at.Sub(start) < 900*time.Millisecond {
			t.Fatalf("request %d went out %s after the 429, inside the shared 1s pause", i+2, at.Sub(start))
		}
	}
}

// A gateway error is retried once, then returned.
func TestRESTDo_RetriesAGatewayErrorOnce(t *testing.T) {
	prev := gatewayRetryDelay
	gatewayRetryDelay = func() time.Duration { return time.Millisecond }
	t.Cleanup(func() { gatewayRetryDelay = prev })

	tr := &gateTransport{respond: func(n int32, _ *http.Request) (int, http.Header, string) {
		if n == 1 {
			return 502, http.Header{}, "bad gateway"
		}
		return 200, http.Header{}, "{}"
	}}
	c := NewClientWithHTTPClient(&http.Client{Transport: tr})
	if _, status, err := c.restDoStatus(context.Background(), http.MethodGet, "/x", nil); err != nil || status != 200 {
		t.Fatalf("status %d err %v, want the retry's 200", status, err)
	}

	always := &gateTransport{respond: func(int32, *http.Request) (int, http.Header, string) { return 503, http.Header{}, "down" }}
	c = NewClientWithHTTPClient(&http.Client{Transport: always})
	if _, status, _ := c.restDoStatus(context.Background(), http.MethodGet, "/x", nil); status != 503 || always.calls.Load() != 2 {
		t.Fatalf("status %d after %d calls, want 503 after exactly 2", status, always.calls.Load())
	}

	// A write is never repeated: GitHub may have applied it behind the
	// gateway that dropped the response.
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete} {
		write := &gateTransport{respond: func(int32, *http.Request) (int, http.Header, string) { return 502, http.Header{}, "bad gateway" }}
		c = NewClientWithHTTPClient(&http.Client{Transport: write})
		if _, status, _ := c.restDoStatus(context.Background(), method, "/x", map[string]string{"k": "v"}); status != 502 || write.calls.Load() != 1 {
			t.Fatalf("%s: status %d after %d calls, want 502 after exactly 1", method, status, write.calls.Load())
		}
	}
}

// The transport's ETag layer keys by Accept as well as URL: one URL answers a
// different representation per media type, and a 304 must replay the one the
// caller asked for.
func TestETagLayer_KeysByAcceptHeader(t *testing.T) {
	var inm []string
	tr := &gateTransport{respond: func(_ int32, req *http.Request) (int, http.Header, string) {
		inm = append(inm, req.Header.Get("If-None-Match"))
		return 200, http.Header{"Etag": []string{`"` + req.Header.Get("Accept") + `"`}}, req.Header.Get("Accept")
	}}
	c := NewClientWithHTTPClient(&http.Client{Transport: tr})
	get := func(accept string) string {
		req, _ := http.NewRequest(http.MethodGet, "https://api.github.com/repos/o/r/contents/f", nil)
		req.Header.Set("Accept", accept)
		resp, err := c.http.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return string(b)
	}
	get("application/vnd.github.raw")
	if body := get("application/vnd.github+json"); body != "application/vnd.github+json" {
		t.Fatalf("second Accept was served %q", body)
	}
	if inm[1] != "" {
		t.Fatalf("the JSON request carried the raw representation's validator %q", inm[1])
	}
}
