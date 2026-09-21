package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// fakeRateLimitServer answers GET /rate_limit with the given per-resource
// budgets and counts how many probes it served.
func fakeRateLimitServer(t *testing.T, resources map[string]ghRateLimitResource, probes *int32) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(probes, 1)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"resources": resources})
	}))
	t.Cleanup(srv.Close)
	return srv
}

// pointProbeAt routes the /rate_limit probe at srv and hands it a canned
// token, so no test ever depends on a real gh login or on api.github.com.
func pointProbeAt(t *testing.T, srv *httptest.Server) {
	t.Helper()
	origClient := ghSubprocessHTTPClient
	origToken := ghSubprocessToken
	ghSubprocessHTTPClient = &http.Client{
		Transport: rewriteHostTransport{target: srv.URL},
		Timeout:   5 * time.Second,
	}
	ghSubprocessToken = func(context.Context) (string, error) { return "test-token", nil }
	t.Cleanup(func() {
		ghSubprocessHTTPClient = origClient
		ghSubprocessToken = origToken
	})
}

// rewriteHostTransport sends every request to target regardless of its URL.
type rewriteHostTransport struct{ target string }

func (rt rewriteHostTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	clone := req.Clone(req.Context())
	target, err := http.NewRequest(req.Method, rt.target+"/rate_limit", nil)
	if err != nil {
		return nil, err
	}
	clone.URL = target.URL
	clone.Host = target.Host
	return http.DefaultTransport.RoundTrip(clone)
}

// stubGh replaces the `gh` binary with a script that prints stdout and exits
// with code, so these tests assert on the gate and the ledger record without
// requiring a real gh (or a real network) on the machine.
func stubGh(t *testing.T, stdout string, code int) {
	t.Helper()
	script := filepath.Join(t.TempDir(), "gh")
	body := fmt.Sprintf("#!/bin/sh\nprintf '%%s' '%s'\nexit %d\n", stdout, code)
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatalf("write gh stub: %v", err)
	}
	orig := ghBinary
	ghBinary = script
	t.Cleanup(func() { ghBinary = orig })
}

// isolateSharedTracker points the machine-wide headroom gate at a temp file so
// a developer's real ~/.nightgauge/rate-limit.json can never gate a test.
func isolateSharedTracker(t *testing.T, seed *RateLimitInfo) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "rate-limit.json")
	if seed != nil {
		if err := NewSharedRateLimitTracker(path).Set("", seed); err != nil {
			t.Fatalf("seed tracker: %v", err)
		}
	}
	orig := sharedHeadroomTrackerPath
	sharedHeadroomTrackerPath = func() (string, error) { return path, nil }
	t.Cleanup(func() { sharedHeadroomTrackerPath = orig })
	return path
}

// A `gh` call must land in the ledger with the frame an operator can act on,
// the resource that actually moved, and the budget observed right after it —
// the three facts AC1 asks for and the three a subprocess used to hide.
func TestRunGhSubprocess_RecordsLedgerEntry(t *testing.T) {
	isolateSharedTracker(t, nil)
	var probes int32
	pointProbeAt(t, fakeRateLimitServer(t, map[string]ghRateLimitResource{
		"core":    {Limit: 5000, Remaining: 4000, Reset: 111},
		"graphql": {Limit: 5000, Remaining: 3000, Reset: 222},
	}, &probes))
	stubGh(t, "ok", 0)
	ledgerPath := withTestLedger(t)

	// Seed a graphql baseline so the drop is observable, exactly as a prior
	// in-process call would have left it.
	activeAPILedger().record(APILedgerRecord{Kind: "graphql"}, "3200")

	out, err := RunGhSubprocess(context.Background(), "", "version")
	if err != nil {
		t.Fatalf("gh version: %v (out=%q)", err, out)
	}

	recs := readLedgerRecords(t, ledgerPath)
	if len(recs) != 2 {
		t.Fatalf("got %d ledger records, want 2 (baseline + subprocess)", len(recs))
	}
	rec := recs[1]
	if rec.Kind != "graphql" {
		t.Errorf("Kind = %q, want the resource that moved (graphql)", rec.Kind)
	}
	if rec.Remaining != 3000 || !rec.HeaderObserved {
		t.Errorf("Remaining/HeaderObserved = %d/%v, want 3000/true", rec.Remaining, rec.HeaderObserved)
	}
	if rec.Cost != 200 {
		t.Errorf("Cost = %d, want 200 (3200 → 3000)", rec.Cost)
	}
	if rec.Method != "version" || !strings.HasPrefix(rec.Path, "gh version") {
		t.Errorf("Method/Path = %q/%q, want the gh invocation", rec.Method, rec.Path)
	}
	if rec.Status != http.StatusOK {
		t.Errorf("Status = %d, want 200 for a gh call that exited 0", rec.Status)
	}
	if rec.PID != os.Getpid() {
		t.Errorf("PID = %d, want %d", rec.PID, os.Getpid())
	}
	// The first frame outside this package. A test in package github can only
	// ever see its own runner here (its own frame is skipped as a
	// pass-through, correctly); the real-caller attribution is asserted from
	// the five call sites, which live outside this package.
	if rec.Caller == "" || strings.Contains(rec.Caller, "internal/github") {
		t.Errorf("Caller = %q, want a frame outside this package", rec.Caller)
	}
	// Exactly one probe: the refresh must not recurse or double-sample.
	if got := atomic.LoadInt32(&probes); got != 1 {
		t.Errorf("%d /rate_limit probes, want exactly 1", got)
	}
}

// The gate is the point of AC2: a subprocess must be refused (or made to wait)
// on the same below-floor reading that stops an in-process call.
func TestRunGhSubprocess_PaysTheHeadroomGate(t *testing.T) {
	isolateSharedTracker(t, &RateLimitInfo{
		Remaining: 5, Limit: 5000, ResetAt: time.Now().Add(20 * time.Minute).Unix(),
	})
	t.Setenv(rateLimitFloorEnv, "100")
	// Fail-fast rather than sleeping out a 20-minute window in a unit test;
	// the wait path is covered by the client gate's own suite.
	t.Setenv(rateLimitNoWaitEnv, "1")
	stubGh(t, "ok", 0)

	_, err := RunGhSubprocess(context.Background(), "", "version")
	if !errors.Is(err, ErrRateLimitGated) {
		t.Fatalf("err = %v, want ErrRateLimitGated before gh ever runs", err)
	}
}

// A gated call must not run gh at all — the gate is there to spend nothing,
// not to record a refusal after the fact.
func TestRunGhSubprocess_GatedCallWritesNoRecord(t *testing.T) {
	isolateSharedTracker(t, &RateLimitInfo{
		Remaining: 5, Limit: 5000, ResetAt: time.Now().Add(20 * time.Minute).Unix(),
	})
	t.Setenv(rateLimitFloorEnv, "100")
	t.Setenv(rateLimitNoWaitEnv, "1")
	stubGh(t, "ok", 0)
	ledgerPath := withTestLedger(t)

	_, _ = RunGhSubprocess(context.Background(), "", "version")

	if recs := readLedgerRecords(t, ledgerPath); len(recs) != 0 {
		t.Fatalf("got %d records for a call that never ran, want 0", len(recs))
	}
}

// A healthy budget gates nothing.
func TestRunGhSubprocess_NoGateAboveFloor(t *testing.T) {
	isolateSharedTracker(t, &RateLimitInfo{
		Remaining: 4500, Limit: 5000, ResetAt: time.Now().Add(20 * time.Minute).Unix(),
	})
	t.Setenv(rateLimitFloorEnv, "100")
	var probes int32
	pointProbeAt(t, fakeRateLimitServer(t, map[string]ghRateLimitResource{
		"core": {Limit: 5000, Remaining: 4500, Reset: 1},
	}, &probes))
	stubGh(t, "ok", 0)
	withTestLedger(t)

	if _, err := RunGhSubprocess(context.Background(), "", "version"); err != nil {
		t.Fatalf("gh version: %v", err)
	}
}

// With the ledger off, the probe must not fire: instrumentation that runs when
// nobody is recording is pure latency on every gh call in the pipeline.
func TestRunGhSubprocess_NoLedgerNoProbe(t *testing.T) {
	isolateSharedTracker(t, nil)
	var probes int32
	pointProbeAt(t, fakeRateLimitServer(t, map[string]ghRateLimitResource{
		"core": {Remaining: 10},
	}, &probes))
	stubGh(t, "ok", 0)
	testLedgerOverride.Store((*apiLedger)(nil))
	t.Setenv(apiLedgerEnv, "0")

	if _, err := RunGhSubprocess(context.Background(), "", "version"); err != nil {
		t.Fatalf("gh version: %v", err)
	}
	if got := atomic.LoadInt32(&probes); got != 0 {
		t.Errorf("%d probes with the ledger off, want 0", got)
	}
}

func TestGhSubprocessResource(t *testing.T) {
	snap := map[string]ghRateLimitResource{
		"core":    {Remaining: 4000},
		"graphql": {Remaining: 3000},
		"search":  {Remaining: 30},
	}
	tests := []struct {
		name string
		prev map[string]int
		args []string
		want string
	}{
		{"the pool that moved wins", map[string]int{"core": 4000, "graphql": 3500}, []string{"pr", "view"}, "graphql"},
		{"largest drop wins", map[string]int{"core": 4100, "graphql": 3500}, []string{"api", "graphql"}, "graphql"},
		{"no baseline falls back to gh api graphql", nil, []string{"api", "graphql"}, "graphql"},
		{"no baseline falls back to rest for gh api", nil, []string{"api", "repos/o/r"}, "core"},
		{"no baseline falls back to graphql for object verbs", nil, []string{"pr", "view", "1"}, "graphql"},
		{"no baseline falls back to search", nil, []string{"search", "issues"}, "search"},
		{"no movement falls back rather than inventing one", map[string]int{"core": 4000}, []string{"pr", "view"}, "graphql"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			l := &apiLedger{prev: map[string]int{}, prevAt: map[string]time.Time{}}
			for k, v := range tc.prev {
				l.prev[k] = v
			}
			got, res := ghSubprocessResource(snap, l, tc.args)
			if got != tc.want {
				t.Fatalf("resource = %q, want %q", got, tc.want)
			}
			if res.Remaining != snap[tc.want].Remaining {
				t.Errorf("returned the wrong resource's budget: %d", res.Remaining)
			}
		})
	}
}
