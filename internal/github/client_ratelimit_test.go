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
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shurcooL/graphql"

	types "github.com/nightgauge/nightgauge/internal/forge/types"
)

// graphQLProbeServer returns an httptest server that responds to any GraphQL
// query with a minimal valid `data` payload, optionally setting GitHub's
// X-RateLimit-* response headers. The server records the count of incoming
// requests so tests can assert "no GraphQL call dispatched" semantics.
//
// The probe call these tests drive is graphQLProbeService.RepoMetadata: the
// repository query issued straight through Client.query. A probe that CAN move
// takes this coverage with it when it does — #849 moved
// Client.GetRepositoryID to REST and these tests silently became REST-gate
// tests, duplicating TestREST_* below while leaving the GraphQL gate untested.
// RepoService.RepoMetadata was the probe until it, too, moved to REST
// (conditional GET); so the probe is now the GraphQL path itself, which
// cannot move.
func graphQLProbeServer(t *testing.T, headers map[string]string, calls *int32) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(calls, 1)
		for k, v := range headers {
			w.Header().Set(k, v)
		}
		w.Header().Set("Content-Type", "application/json")
		// Echo back a body that satisfies whatever struct shurcooL/graphql
		// asks for. The repository(owner:..., name:...) query path is the
		// only one we exercise from these tests.
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"data": map[string]interface{}{
				"repository": map[string]interface{}{
					"nameWithOwner":    "nightgauge/nightgauge",
					"owner":            map[string]interface{}{"login": "nightgauge"},
					"name":             "nightgauge",
					"defaultBranchRef": map[string]interface{}{"name": "main"},
				},
			},
		})
	}))
}

// captureLogger returns a gateLogger replacement that records each formatted
// message, plus a getter for inspection.
func captureLogger() (func(format string, args ...interface{}), func() []string) {
	var (
		mu   sync.Mutex
		logs []string
	)
	return func(format string, args ...interface{}) {
			mu.Lock()
			defer mu.Unlock()
			logs = append(logs, fmt.Sprintf(format, args...))
		}, func() []string {
			mu.Lock()
			defer mu.Unlock()
			out := make([]string, len(logs))
			copy(out, logs)
			return out
		}
}

// TestRateLimitFloor_DefaultAndOverride verifies the env var override path.
func TestRateLimitFloor_DefaultAndOverride(t *testing.T) {
	t.Run("default when unset", func(t *testing.T) {
		t.Setenv(rateLimitFloorEnv, "")
		if got := rateLimitFloor(); got != defaultRateLimitFloor {
			t.Errorf("got %d, want %d (default)", got, defaultRateLimitFloor)
		}
	})
	t.Run("override numeric", func(t *testing.T) {
		t.Setenv(rateLimitFloorEnv, "250")
		if got := rateLimitFloor(); got != 250 {
			t.Errorf("got %d, want 250", got)
		}
	})
	t.Run("zero is allowed (gating disabled)", func(t *testing.T) {
		t.Setenv(rateLimitFloorEnv, "0")
		if got := rateLimitFloor(); got != 0 {
			t.Errorf("got %d, want 0", got)
		}
	})
	t.Run("trims whitespace", func(t *testing.T) {
		t.Setenv(rateLimitFloorEnv, "  42  ")
		if got := rateLimitFloor(); got != 42 {
			t.Errorf("got %d, want 42", got)
		}
	})
	t.Run("negative falls back to default", func(t *testing.T) {
		t.Setenv(rateLimitFloorEnv, "-5")
		if got := rateLimitFloor(); got != defaultRateLimitFloor {
			t.Errorf("got %d, want %d", got, defaultRateLimitFloor)
		}
	})
	t.Run("non-numeric falls back to default", func(t *testing.T) {
		t.Setenv(rateLimitFloorEnv, "lots")
		if got := rateLimitFloor(); got != defaultRateLimitFloor {
			t.Errorf("got %d, want %d", got, defaultRateLimitFloor)
		}
	})
}

// TestRateLimitGate_TripsBelowFloor verifies the proactive gate: a fresh
// tracker reading below the floor with a future reset must short-circuit
// query() with ErrRateLimitGated and dispatch zero HTTP calls.
func TestRateLimitGate_TripsBelowFloor(t *testing.T) {
	var calls int32
	srv := graphQLProbeServer(t, nil, &calls)
	defer srv.Close()

	path := filepath.Join(t.TempDir(), "rate-limit.json")
	tr := NewSharedRateLimitTracker(path)
	resetAt := time.Now().Add(20 * time.Minute).Unix()
	if err := tr.Set("alice", ResourceGraphQL, &RateLimitInfo{Remaining: 50, Limit: 5000, ResetAt: resetAt}); err != nil {
		t.Fatalf("seed tracker: %v", err)
	}

	c := NewClientWithURL("test-token", srv.URL).WithRateLimitTracker(tr, "alice")
	logger, readLogs := captureLogger()
	c.gateLogger = logger

	t.Setenv(rateLimitFloorEnv, "100")

	_, err := (graphQLProbeService{c}).RepoMetadata(context.Background(), "nightgauge", "nightgauge")
	if err == nil {
		t.Fatal("expected ErrRateLimitGated, got nil")
	}
	if !errors.Is(err, ErrRateLimitGated) {
		t.Fatalf("expected errors.Is(ErrRateLimitGated), got %v", err)
	}
	if got := atomic.LoadInt32(&calls); got != 0 {
		t.Fatalf("expected 0 HTTP dispatches when gated, got %d", got)
	}

	logs := readLogs()
	if len(logs) != 1 {
		t.Fatalf("expected exactly one gate-decision log line, got %d: %v", len(logs), logs)
	}
	if !strings.Contains(logs[0], "rate limit gated") {
		t.Errorf("log missing 'rate limit gated': %q", logs[0])
	}
	if !strings.Contains(logs[0], "remaining=50") {
		t.Errorf("log missing remaining count: %q", logs[0])
	}
	if !strings.Contains(logs[0], "floor=100") {
		t.Errorf("log missing floor: %q", logs[0])
	}
	if !strings.Contains(logs[0], "reset_in=") {
		t.Errorf("log missing reset_in: %q", logs[0])
	}
}

// TestRateLimitGate_NoOpAboveFloor verifies that healthy quota readings allow
// the call through.
func TestRateLimitGate_NoOpAboveFloor(t *testing.T) {
	var calls int32
	srv := graphQLProbeServer(t, nil, &calls)
	defer srv.Close()

	path := filepath.Join(t.TempDir(), "rate-limit.json")
	tr := NewSharedRateLimitTracker(path)
	if err := tr.Set("alice", ResourceGraphQL, &RateLimitInfo{
		Remaining: 4500, Limit: 5000,
		ResetAt: time.Now().Add(30 * time.Minute).Unix(),
	}); err != nil {
		t.Fatalf("seed tracker: %v", err)
	}

	c := NewClientWithURL("test-token", srv.URL).WithRateLimitTracker(tr, "alice")
	t.Setenv(rateLimitFloorEnv, "100")

	meta, err := (graphQLProbeService{c}).RepoMetadata(context.Background(), "nightgauge", "nightgauge")
	if err != nil {
		t.Fatalf("expected success above floor, got: %v", err)
	}
	if meta.NameWithOwner == "" {
		t.Fatalf("expected repo metadata back")
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("expected exactly 1 dispatched call, got %d", got)
	}
}

// TestRateLimitGate_StaleExhaustionStillGates is the end-to-end form of the
// rule corrected on 2026-09-21. This test previously asserted the opposite —
// "a stale entry does NOT gate, since we have no recent confidence in the
// count" — and that rationale is the defect: staleness costs us confidence in
// HOW MUCH is left, but none at all in whether the window has RESET, and a
// below-floor reading can only move further down before it does. Because every
// short-lived process and every between-burst producer starts with an entry
// older than SharedTrackerMinCheckIntervalSecs, the old rule left the gate
// open essentially always; the machine's tracker was 7h44m stale while the
// account exhausted its GraphQL quota twice.
func TestRateLimitGate_StaleExhaustionStillGates(t *testing.T) {
	var calls int32
	srv := graphQLProbeServer(t, nil, &calls)
	defer srv.Close()

	path := filepath.Join(t.TempDir(), "rate-limit.json")
	tr := NewSharedRateLimitTracker(path)
	if err := tr.Set("alice", ResourceGraphQL, &RateLimitInfo{
		Remaining: 5, Limit: 5000,
		ResetAt: time.Now().Add(30 * time.Minute).Unix(),
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	// Backdate the entry so it is no longer fresh.
	file, err := tr.readLocked()
	if err != nil {
		t.Fatal(err)
	}
	file.Entries["alice|graphql"].CheckedAt = time.Now().Unix() - int64(SharedTrackerMinCheckIntervalSecs) - 30
	if err := tr.writeLocked(file); err != nil {
		t.Fatal(err)
	}

	c := NewClientWithURL("test-token", srv.URL).WithRateLimitTracker(tr, "alice")
	t.Setenv(rateLimitFloorEnv, "100")

	if _, err := (graphQLProbeService{c}).RepoMetadata(context.Background(), "nightgauge", "nightgauge"); err == nil {
		t.Fatal("stale below-floor entry inside its reset window must gate, got a dispatched call")
	}
	if got := atomic.LoadInt32(&calls); got != 0 {
		t.Fatalf("expected the call to be gated before dispatch, got %d dispatched", got)
	}
}

// TestRateLimitGate_NoOpWhenStaleAndNoReset keeps the "no recent confidence"
// no-op for the one state where it is still the right answer: an entry that
// carries no reset second at all, so there is nothing to say the window has
// not already turned over.
func TestRateLimitGate_NoOpWhenStaleAndNoReset(t *testing.T) {
	var calls int32
	srv := graphQLProbeServer(t, nil, &calls)
	defer srv.Close()

	path := filepath.Join(t.TempDir(), "rate-limit.json")
	tr := NewSharedRateLimitTracker(path)
	if err := tr.Set("alice", ResourceGraphQL, &RateLimitInfo{Remaining: 5, Limit: 5000}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	file, err := tr.readLocked()
	if err != nil {
		t.Fatal(err)
	}
	file.Entries["alice|graphql"].ResetAt = 0
	file.Entries["alice|graphql"].CheckedAt = time.Now().Unix() - int64(SharedTrackerMinCheckIntervalSecs) - 30
	if err := tr.writeLocked(file); err != nil {
		t.Fatal(err)
	}

	c := NewClientWithURL("test-token", srv.URL).WithRateLimitTracker(tr, "alice")
	t.Setenv(rateLimitFloorEnv, "100")

	if _, err := (graphQLProbeService{c}).RepoMetadata(context.Background(), "nightgauge", "nightgauge"); err != nil {
		t.Fatalf("stale entry with no reset must not gate: %v", err)
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("expected the call to dispatch, got %d", got)
	}
}

// TestRateLimitGate_NoOpWhenResetPassed verifies that even a low remaining
// count does not gate when the reset window has already elapsed (the next
// API response will replenish).
func TestRateLimitGate_NoOpWhenResetPassed(t *testing.T) {
	var calls int32
	srv := graphQLProbeServer(t, nil, &calls)
	defer srv.Close()

	path := filepath.Join(t.TempDir(), "rate-limit.json")
	tr := NewSharedRateLimitTracker(path)
	if err := tr.Set("alice", ResourceGraphQL, &RateLimitInfo{
		Remaining: 5, Limit: 5000,
		ResetAt: time.Now().Add(-1 * time.Minute).Unix(),
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	c := NewClientWithURL("test-token", srv.URL).WithRateLimitTracker(tr, "alice")
	t.Setenv(rateLimitFloorEnv, "100")

	if _, err := (graphQLProbeService{c}).RepoMetadata(context.Background(), "nightgauge", "nightgauge"); err != nil {
		t.Fatalf("expected pass-through when reset window elapsed, got %v", err)
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("expected dispatch when reset window elapsed, got %d", got)
	}
}

// TestRateLimitGate_NoOpWithoutTracker verifies clients without a tracker
// attached behave as before.
func TestRateLimitGate_NoOpWithoutTracker(t *testing.T) {
	var calls int32
	srv := graphQLProbeServer(t, nil, &calls)
	defer srv.Close()

	c := NewClientWithURL("test-token", srv.URL)
	if _, err := (graphQLProbeService{c}).RepoMetadata(context.Background(), "nightgauge", "nightgauge"); err != nil {
		t.Fatalf("untracked client must still work: %v", err)
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("expected 1 dispatched call, got %d", got)
	}
}

// TestRateLimitGate_EnvOverride verifies NIGHTGAUGE_GITHUB_RATELIMIT_FLOOR
// flips a borderline reading from "pass" to "gate" without rebuilding.
func TestRateLimitGate_EnvOverride(t *testing.T) {
	var calls int32
	srv := graphQLProbeServer(t, nil, &calls)
	defer srv.Close()

	path := filepath.Join(t.TempDir(), "rate-limit.json")
	tr := NewSharedRateLimitTracker(path)
	if err := tr.Set("alice", ResourceGraphQL, &RateLimitInfo{
		Remaining: 200, Limit: 5000,
		ResetAt: time.Now().Add(30 * time.Minute).Unix(),
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	c := NewClientWithURL("test-token", srv.URL).WithRateLimitTracker(tr, "alice")
	c.gateLogger = func(string, ...interface{}) {}

	// With the default floor (100), 200 is above → call passes.
	t.Setenv(rateLimitFloorEnv, "")
	if _, err := (graphQLProbeService{c}).RepoMetadata(context.Background(), "nightgauge", "nightgauge"); err != nil {
		t.Fatalf("default floor should let 200 through: %v", err)
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("expected 1 call before override, got %d", got)
	}

	// Raise the floor to 500 → now 200 is below → call gates.
	t.Setenv(rateLimitFloorEnv, "500")
	_, err := (graphQLProbeService{c}).RepoMetadata(context.Background(), "nightgauge", "nightgauge")
	if !errors.Is(err, ErrRateLimitGated) {
		t.Fatalf("expected ErrRateLimitGated under raised floor, got %v", err)
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("call count should not increase when gated, got %d", got)
	}
}

// TestHeaderInterceptor_FeedsTracker verifies the transport wrapper extracts
// X-RateLimit-* headers and persists them via SetFromHeaders.
func TestHeaderInterceptor_FeedsTracker(t *testing.T) {
	var calls int32
	resetAt := time.Now().Add(45 * time.Minute).Unix()
	srv := graphQLProbeServer(t, map[string]string{
		"X-RateLimit-Limit":     "5000",
		"X-RateLimit-Remaining": "4711",
		"X-RateLimit-Reset":     fmt.Sprintf("%d", resetAt),
	}, &calls)
	defer srv.Close()

	path := filepath.Join(t.TempDir(), "rate-limit.json")
	tr := NewSharedRateLimitTracker(path)

	c := NewClientWithURL("test-token", srv.URL).WithRateLimitTracker(tr, "alice")
	t.Setenv(rateLimitFloorEnv, "100")

	if _, err := (graphQLProbeService{c}).RepoMetadata(context.Background(), "nightgauge", "nightgauge"); err != nil {
		t.Fatalf("call: %v", err)
	}

	entry, fresh, err := tr.Get("alice", ResourceGraphQL)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if entry == nil {
		t.Fatalf("expected tracker populated from response headers")
	}
	if !fresh {
		t.Fatalf("just-written entry should be fresh")
	}
	if entry.Remaining != 4711 || entry.Limit != 5000 || entry.ResetAt != resetAt {
		t.Fatalf("header values not propagated: %+v", entry)
	}
}

// TestHeaderInterceptor_NoTrackerNoOp verifies the transport wrapper is a
// safe no-op when no tracker is attached.
func TestHeaderInterceptor_NoTrackerNoOp(t *testing.T) {
	var calls int32
	srv := graphQLProbeServer(t, map[string]string{
		"X-RateLimit-Limit":     "5000",
		"X-RateLimit-Remaining": "4500",
		"X-RateLimit-Reset":     "1700000000",
	}, &calls)
	defer srv.Close()

	c := NewClientWithURL("test-token", srv.URL)
	if _, err := (graphQLProbeService{c}).RepoMetadata(context.Background(), "nightgauge", "nightgauge"); err != nil {
		t.Fatalf("call: %v", err)
	}
	// No assertion on tracker — there isn't one. Just ensure no panic /
	// crash when the interceptor runs without a tracker.
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("expected 1 call, got %d", got)
	}
}

// TestHeaderInterceptor_SkipsResponsesWithoutHeaders verifies that responses
// missing the X-RateLimit-Remaining header are silently skipped (e.g.,
// non-API HTTP traffic) and don't pollute the tracker.
func TestHeaderInterceptor_SkipsResponsesWithoutHeaders(t *testing.T) {
	var calls int32
	srv := graphQLProbeServer(t, nil, &calls) // no headers set
	defer srv.Close()

	path := filepath.Join(t.TempDir(), "rate-limit.json")
	tr := NewSharedRateLimitTracker(path)

	c := NewClientWithURL("test-token", srv.URL).WithRateLimitTracker(tr, "alice")
	if _, err := (graphQLProbeService{c}).RepoMetadata(context.Background(), "nightgauge", "nightgauge"); err != nil {
		t.Fatalf("call: %v", err)
	}
	entry, _, err := tr.Get("alice", ResourceGraphQL)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if entry != nil {
		t.Fatalf("tracker should remain empty when no rate-limit headers are present, got %+v", entry)
	}
}

// TestHeaderInterceptor_NoDoubleCount verifies a single response produces a
// single tracker write — no duplication or accumulation across calls.
func TestHeaderInterceptor_NoDoubleCount(t *testing.T) {
	var calls int32
	resetAt := time.Now().Add(30 * time.Minute).Unix()
	// Two distinct readings: first 4500, then 4499 (one fewer remaining).
	// We assert the tracker reflects the *last* observed value, not a sum.
	var counter int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		n := atomic.AddInt32(&counter, 1)
		atomic.AddInt32(&calls, 1)
		remaining := 4500
		if n > 1 {
			remaining = 4499
		}
		w.Header().Set("X-RateLimit-Limit", "5000")
		w.Header().Set("X-RateLimit-Remaining", fmt.Sprintf("%d", remaining))
		w.Header().Set("X-RateLimit-Reset", fmt.Sprintf("%d", resetAt))
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"data": map[string]interface{}{"repository": map[string]interface{}{
				"nameWithOwner":    "nightgauge/nightgauge",
				"owner":            map[string]interface{}{"login": "nightgauge"},
				"name":             "nightgauge",
				"defaultBranchRef": map[string]interface{}{"name": "main"},
			}},
		})
	}))
	defer srv.Close()

	path := filepath.Join(t.TempDir(), "rate-limit.json")
	tr := NewSharedRateLimitTracker(path)
	c := NewClientWithURL("test-token", srv.URL).WithRateLimitTracker(tr, "alice")

	for i := 0; i < 2; i++ {
		if _, err := (graphQLProbeService{c}).RepoMetadata(context.Background(), "nightgauge", "nightgauge"); err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
	}

	entry, _, _ := tr.Get("alice", ResourceGraphQL)
	if entry == nil {
		t.Fatal("expected entry")
	}
	if entry.Remaining != 4499 {
		t.Errorf("expected last observation 4499 (no double-count), got %d", entry.Remaining)
	}
	if entry.Limit != 5000 {
		t.Errorf("Limit should track last observation, got %d", entry.Limit)
	}
}

// TestInstallHeaderInterceptor_Idempotent verifies repeated calls to install
// the interceptor do not double-wrap the transport.
func TestInstallHeaderInterceptor_Idempotent(t *testing.T) {
	c := NewClientWithToken("test-token")
	// NewClientWithToken already installs once; a second call should not
	// wrap again.
	c.installHeaderInterceptor()
	c.installHeaderInterceptor()

	rt, ok := c.http.Transport.(*rateLimitHeaderTransport)
	if !ok {
		t.Fatal("expected rateLimitHeaderTransport on the http client")
	}
	if _, doubled := rt.base.(*rateLimitHeaderTransport); doubled {
		t.Fatal("transport should not be double-wrapped")
	}
}

// TestHeadroomGate_MatchesClientDecisions is the regression guard on #1913's
// refactor: the gate moved out of *Client so a `gh` subprocess could pay it
// too, and the only thing that must not change in the move is WHEN it trips.
// Each row is asserted twice — through the client method and through the bare
// gate value — and the two answers must agree.
func TestHeadroomGate_MatchesClientDecisions(t *testing.T) {
	t.Setenv(rateLimitFloorEnv, "100")
	future := time.Now().Add(20 * time.Minute).Unix()

	tests := []struct {
		name      string
		entry     *RateLimitInfo
		checkedAt int64 // 0 = fresh (Set stamps now)
		wantGated bool
	}{
		{"no entry at all", nil, 0, false},
		{"healthy budget", &RateLimitInfo{Remaining: 4500, Limit: 5000, ResetAt: future}, 0, false},
		{"exactly at the floor", &RateLimitInfo{Remaining: 100, Limit: 5000, ResetAt: future}, 0, false},
		{"one below the floor", &RateLimitInfo{Remaining: 99, Limit: 5000, ResetAt: future}, 0, true},
		{"exhausted", &RateLimitInfo{Remaining: 0, Limit: 5000, ResetAt: future}, 0, true},
		{"below floor but the window already reset", &RateLimitInfo{Remaining: 5, Limit: 5000, ResetAt: time.Now().Add(-time.Minute).Unix()}, 0, false},
		// A stale below-floor reading STILL gates. ResetAt, not CheckedAt, is
		// the expiry for an exhaustion reading: until that second elapses the
		// budget cannot have recovered, however old the reading is. This row
		// asserted `false` until 2026-09-21, which is why the gate was dead
		// code for most of its wall-clock life — every short-lived process and
		// every between-burst producer starts with an entry older than 15s.
		{"below floor and stale, but the window has not reset", &RateLimitInfo{Remaining: 5, Limit: 5000, ResetAt: future}, time.Now().Add(-time.Hour).Unix(), true},
		// Freshness still governs when there is no reset to reason about.
		{"below floor, stale, and no reset recorded", &RateLimitInfo{Remaining: 5, Limit: 5000, ResetAt: 0}, time.Now().Add(-time.Hour).Unix(), false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "rate-limit.json")
			tr := NewSharedRateLimitTracker(path)
			if tc.entry != nil {
				if tc.checkedAt == 0 {
					if err := tr.Set("alice", ResourceGraphQL, tc.entry); err != nil {
						t.Fatalf("seed tracker: %v", err)
					}
				} else {
					// Written by hand: Set always stamps CheckedAt with now, and
					// the stale row is the one case that needs an older stamp.
					file := sharedTrackerFile{Version: sharedTrackerFileVersion, Entries: map[string]*SharedTrackerEntry{
						"alice|graphql": {
							Remaining: tc.entry.Remaining,
							Limit:     tc.entry.Limit,
							ResetAt:   tc.entry.ResetAt,
							CheckedAt: tc.checkedAt,
						},
					}}
					raw, merr := json.Marshal(file)
					if merr != nil {
						t.Fatalf("marshal tracker file: %v", merr)
					}
					if werr := os.WriteFile(path, raw, 0o644); werr != nil {
						t.Fatalf("write tracker file: %v", werr)
					}
				}
			}

			silent := func(string, ...interface{}) {}
			c := NewClientWithURL("test-token", "https://example.invalid/graphql").
				WithRateLimitTracker(tr, "alice")
			c.gateLogger = silent
			clientWait, clientGated := c.rateLimitResetWait(ResourceGraphQL)

			gateWait, gateGated := headroomGate{tracker: tr, user: "alice", logger: silent}.resetWait()

			if clientGated != tc.wantGated {
				t.Errorf("client gated = %v, want %v", clientGated, tc.wantGated)
			}
			if gateGated != clientGated {
				t.Errorf("extracted gate says %v, client says %v — the refactor changed behaviour",
					gateGated, clientGated)
			}
			// Both read the same clock a moment apart; a second of slack keeps
			// the comparison about the DECISION, not about scheduling.
			if diff := clientWait - gateWait; diff > time.Second || diff < -time.Second {
				t.Errorf("wait differs: client %s, gate %s", clientWait, gateWait)
			}
		})
	}
}

// graphQLProbeService issues one GraphQL repository query through the
// client's query path — the gate these tests exist to pin.
type graphQLProbeService struct{ c *Client }

func (p graphQLProbeService) RepoMetadata(ctx context.Context, owner, name string) (*types.Repo, error) {
	var q struct {
		Repository struct {
			NameWithOwner    graphql.String
			Owner            struct{ Login graphql.String }
			Name             graphql.String
			DefaultBranchRef *struct{ Name graphql.String }
		} `graphql:"repository(owner: $owner, name: $name)"`
	}
	vars := map[string]interface{}{"owner": graphql.String(owner), "name": graphql.String(name)}
	if err := p.c.Query(ctx, &q, vars); err != nil {
		return nil, err
	}
	return &types.Repo{NameWithOwner: string(q.Repository.NameWithOwner), Owner: string(q.Repository.Owner.Login), Name: string(q.Repository.Name)}, nil
}
