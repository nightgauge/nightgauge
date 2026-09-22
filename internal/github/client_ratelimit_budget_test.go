package github

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// writeTrackerFile seeds a tracker file by hand. Set always stamps CheckedAt
// with now, and every case here turns on a stamp that is deliberately older.
func writeTrackerFile(t *testing.T, path string, entries map[string]*SharedTrackerEntry) {
	t.Helper()
	raw, err := json.Marshal(sharedTrackerFile{Version: sharedTrackerFileVersion, Entries: entries})
	if err != nil {
		t.Fatalf("marshal tracker file: %v", err)
	}
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatalf("write tracker file: %v", err)
	}
}

// TestGateHoldsOnStaleExhaustion is the regression for the burn observed on
// 2026-09-21: the machine's tracker had not been written for 7h44m while the
// account exhausted its GraphQL quota twice, because a reading older than
// SharedTrackerMinCheckIntervalSecs was discarded as "no data" and the gate
// opened onto a budget already known to be spent.
func TestGateHoldsOnStaleExhaustion(t *testing.T) {
	t.Setenv(rateLimitFloorEnv, "100")
	path := filepath.Join(t.TempDir(), "rate-limit.json")
	reset := time.Now().Add(18 * time.Minute).Unix()
	writeTrackerFile(t, path, map[string]*SharedTrackerEntry{
		"alice|graphql": {Remaining: 0, Limit: 5000, ResetAt: reset,
			CheckedAt: time.Now().Add(-7*time.Hour - 44*time.Minute).Unix()},
	})

	wait, gated := headroomGate{
		tracker:  NewSharedRateLimitTracker(path),
		user:     "alice",
		resource: ResourceGraphQL,
		logger:   func(string, ...interface{}) {},
	}.resetWait()

	if !gated {
		t.Fatal("gate opened on a 7h44m-old reading of remaining=0 — this is the burn")
	}
	if wait <= 0 || wait > 20*time.Minute {
		t.Errorf("wait = %s, want a positive duration inside the reset window", wait)
	}
}

// TestGateReleasesOnceTheWindowResets is the negative control for the test
// above: the same stale entry must STOP gating the moment its reset elapses,
// or the fix would replace a gate that never fires with one that never opens.
func TestGateReleasesOnceTheWindowResets(t *testing.T) {
	t.Setenv(rateLimitFloorEnv, "100")
	path := filepath.Join(t.TempDir(), "rate-limit.json")
	writeTrackerFile(t, path, map[string]*SharedTrackerEntry{
		"alice|graphql": {Remaining: 0, Limit: 5000,
			ResetAt:   time.Now().Add(-time.Second).Unix(),
			CheckedAt: time.Now().Add(-time.Hour).Unix()},
	})

	if _, gated := (headroomGate{
		tracker:  NewSharedRateLimitTracker(path),
		user:     "alice",
		resource: ResourceGraphQL,
		logger:   func(string, ...interface{}) {},
	}).resetWait(); gated {
		t.Fatal("gate held past its own reset second — callers would never proceed")
	}
}

// TestBudgetIsSharedAcrossTrackerKeys covers the second half of the burn: the
// IPC server's default client wires the tracker with an empty user while the
// resolver wires it with the gh username, so ONE account reached the file
// under two keys, each seeing only its own share of one pool.
func TestBudgetIsSharedAcrossTrackerKeys(t *testing.T) {
	t.Setenv(rateLimitFloorEnv, "100")
	path := filepath.Join(t.TempDir(), "rate-limit.json")
	reset := time.Now().Add(12 * time.Minute).Unix()
	now := time.Now().Unix()
	writeTrackerFile(t, path, map[string]*SharedTrackerEntry{
		// What the default client last saw: looks healthy.
		"default|graphql": {Remaining: 3799, Limit: 5000, ResetAt: reset, CheckedAt: now},
		// What the resolved per-user client last saw on the SAME window.
		"alice|graphql": {Remaining: 4, Limit: 5000, ResetAt: reset, CheckedAt: now},
	})

	if _, gated := (headroomGate{
		tracker:  NewSharedRateLimitTracker(path),
		user:     "", // collapses to "default" — internal/ipc/server.go
		resource: ResourceGraphQL,
		logger:   func(string, ...interface{}) {},
	}).resetWait(); !gated {
		t.Fatal("default-keyed gate ignored an exhaustion recorded under the same account's other key")
	}
}

// TestBudgetIgnoresAnotherAccountsWindow bounds the rule above. A genuinely
// different account is on its own reset window and must not gate this one,
// or one idle org login would stall every other.
func TestBudgetIgnoresAnotherAccountsWindow(t *testing.T) {
	t.Setenv(rateLimitFloorEnv, "100")
	path := filepath.Join(t.TempDir(), "rate-limit.json")
	now := time.Now().Unix()
	writeTrackerFile(t, path, map[string]*SharedTrackerEntry{
		"alice|graphql": {Remaining: 4200, Limit: 5000, ResetAt: time.Now().Add(30 * time.Minute).Unix(), CheckedAt: now},
		"bob|graphql":   {Remaining: 0, Limit: 5000, ResetAt: time.Now().Add(9 * time.Minute).Unix(), CheckedAt: now},
	})

	if _, gated := (headroomGate{
		tracker:  NewSharedRateLimitTracker(path),
		user:     "alice",
		resource: ResourceGraphQL,
		logger:   func(string, ...interface{}) {},
	}).resetWait(); gated {
		t.Fatal("alice gated on bob's exhaustion — different reset windows are different pools")
	}
}

// TestGetBudgetReportsTheGoverningEntry asserts the tracker-level contract the
// gate depends on, so a future caller reads the same rule.
func TestGetBudgetReportsTheGoverningEntry(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rate-limit.json")
	reset := time.Now().Add(15 * time.Minute).Unix()
	now := time.Now().Unix()
	writeTrackerFile(t, path, map[string]*SharedTrackerEntry{
		"default|graphql": {Remaining: 3799, Limit: 5000, ResetAt: reset, CheckedAt: now},
		"alice|graphql":   {Remaining: 12, Limit: 5000, ResetAt: reset, CheckedAt: now},
		"other|graphql":   {Remaining: 1, Limit: 5000, ResetAt: reset + 3600, CheckedAt: now},
	})
	tr := NewSharedRateLimitTracker(path)

	entry, _, err := tr.GetBudget("default", ResourceGraphQL)
	if err != nil {
		t.Fatalf("GetBudget: %v", err)
	}
	if entry.Remaining != 12 {
		t.Errorf("governing remaining = %d, want 12 (the lowest on this reset window)", entry.Remaining)
	}

	// Get is unchanged: it still answers "what is under MY key", which is what
	// the writers and the doctor check want.
	own, _, err := tr.Get("default", ResourceGraphQL)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if own.Remaining != 3799 {
		t.Errorf("Get(default) = %d, want its own 3799 — GetBudget must not change Get", own.Remaining)
	}
}
