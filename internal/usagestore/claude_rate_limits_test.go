package usagestore

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func now() time.Time { return time.Date(2026, 8, 19, 15, 0, 0, 0, time.UTC) }

func reading(bucket string, util float64, resetsAt int64, observedAt time.Time) Reading {
	return Reading{
		RateLimitType: bucket,
		Utilization:   util,
		ResetsAt:      resetsAt,
		Status:        "allowed",
		ObservedAt:    observedAt,
	}
}

func TestRecordRoundTrips(t *testing.T) {
	store := New(t.TempDir())
	at := now()

	if err := store.Record([]Reading{reading("five_hour", 44, at.Add(time.Hour).Unix(), at)}, at); err != nil {
		t.Fatalf("Record: %v", err)
	}

	got := store.Readings(at)
	if len(got) != 1 || got[0].Utilization != 44 {
		t.Fatalf("Readings = %+v, want one five_hour reading at 44", got)
	}
}

// Neither writer can see the other's channel, so the merge key is ObservedAt:
// a statusline render must not clobber a fresher reading the extension's
// stream writer recorded a moment earlier, and vice versa.
func TestRecordKeepsTheNewerReadingPerBucket(t *testing.T) {
	store := New(t.TempDir())
	at := now()
	reset := at.Add(time.Hour).Unix()

	if err := store.Record([]Reading{reading("five_hour", 80, reset, at)}, at); err != nil {
		t.Fatalf("seed: %v", err)
	}
	// An older reading arriving late loses.
	if err := store.Record([]Reading{reading("five_hour", 10, reset, at.Add(-10*time.Minute))}, at); err != nil {
		t.Fatalf("stale write: %v", err)
	}
	if got := store.Readings(at); len(got) != 1 || got[0].Utilization != 80 {
		t.Fatalf("Readings = %+v, want the newer 80%% reading retained", got)
	}

	// A newer one wins.
	if err := store.Record([]Reading{reading("five_hour", 91, reset, at.Add(time.Minute))}, at); err != nil {
		t.Fatalf("fresh write: %v", err)
	}
	if got := store.Readings(at); len(got) != 1 || got[0].Utilization != 91 {
		t.Fatalf("Readings = %+v, want the newer 91%% reading", got)
	}
}

func TestRecordPreservesOtherBuckets(t *testing.T) {
	store := New(t.TempDir())
	at := now()
	reset := at.Add(48 * time.Hour).Unix()

	if err := store.Record([]Reading{reading("seven_day", 61, reset, at)}, at); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := store.Record([]Reading{reading("five_hour", 12, reset, at)}, at); err != nil {
		t.Fatalf("second bucket: %v", err)
	}
	if got := store.Readings(at); len(got) != 2 {
		t.Fatalf("Readings = %+v, want both buckets", got)
	}
}

// A reading past its own resetsAt is known-wrong, not stale: that window has
// refilled and this process cannot know the post-reset utilization.
func TestExpiredReadingsAreDroppedAndPruned(t *testing.T) {
	root := t.TempDir()
	store := New(root)
	at := now()

	if err := store.Record([]Reading{
		reading("five_hour", 90, at.Add(-time.Minute).Unix(), at.Add(-2*time.Hour)),
		reading("seven_day", 30, at.Add(48*time.Hour).Unix(), at),
	}, at.Add(-2*time.Hour)); err != nil {
		t.Fatalf("seed: %v", err)
	}

	if got := store.Readings(at); len(got) != 1 || got[0].RateLimitType != "seven_day" {
		t.Fatalf("Readings = %+v, want the expired five_hour dropped", got)
	}

	// And the next write prunes it from disk rather than carrying it forever.
	if err := store.Record([]Reading{reading("seven_day", 31, at.Add(48*time.Hour).Unix(), at)}, at); err != nil {
		t.Fatalf("prune write: %v", err)
	}
	data, err := os.ReadFile(store.Path())
	if err != nil {
		t.Fatalf("read store: %v", err)
	}
	if strings.Contains(string(data), "five_hour") {
		t.Errorf("expired bucket still on disk: %s", data)
	}
}

// A resetsAt of 0 means no reset time was carried; there is no clock for such
// a reading to expire against.
func TestReadingWithoutResetNeverExpires(t *testing.T) {
	r := reading("five_hour", 44, 0, now().Add(-30*24*time.Hour))
	if Expired(r, now()) {
		t.Error("a reading with no resetsAt expired on a clock it does not have")
	}
}

func TestLoadDiscardsWrongVersion(t *testing.T) {
	root := t.TempDir()
	store := New(root)
	if err := os.MkdirAll(filepath.Dir(store.Path()), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(store.Path(),
		[]byte(`{"version":999,"buckets":{"five_hour":{"rateLimitType":"five_hour","utilization":44,"resetsAt":0,"status":"allowed","observedAt":"2026-08-19T15:00:00Z"}}}`),
		0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if got := store.Readings(now()); len(got) != 0 {
		t.Fatalf("Readings = %+v, want nothing from a wrong-version file", got)
	}
}

func TestLoadDiscardsMalformedEntries(t *testing.T) {
	root := t.TempDir()
	store := New(root)
	if err := os.MkdirAll(filepath.Dir(store.Path()), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// One entry with no bucket name, one good.
	if err := os.WriteFile(store.Path(),
		[]byte(`{"version":1,"buckets":{"":{"utilization":44,"observedAt":"2026-08-19T15:00:00Z"},`+
			`"seven_day":{"rateLimitType":"seven_day","utilization":61,"resetsAt":0,"status":"allowed","observedAt":"2026-08-19T15:00:00Z"}}}`),
		0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	got := store.Readings(now())
	if len(got) != 1 || got[0].RateLimitType != "seven_day" {
		t.Fatalf("Readings = %+v, want only the well-formed entry", got)
	}
}

func TestLoadToleratesMissingAndCorruptFiles(t *testing.T) {
	store := New(t.TempDir())
	if got := store.Readings(now()); len(got) != 0 {
		t.Fatalf("Readings on a missing file = %+v, want empty", got)
	}
	if err := os.MkdirAll(filepath.Dir(store.Path()), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(store.Path(), []byte("{ not json"), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if got := store.Readings(now()); len(got) != 0 {
		t.Fatalf("Readings on a corrupt file = %+v, want empty", got)
	}
}

// The extension polls this file; a reader must never observe a half-written
// document, and it must be able to parse what Go wrote.
func TestWriteIsAtomicAndLeavesNoTempFiles(t *testing.T) {
	root := t.TempDir()
	store := New(root)
	at := now()

	if err := store.Record([]Reading{reading("five_hour", 44, at.Add(time.Hour).Unix(), at)}, at); err != nil {
		t.Fatalf("Record: %v", err)
	}
	entries, err := os.ReadDir(filepath.Dir(store.Path()))
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	for _, entry := range entries {
		if entry.Name() != "claude-rate-limits.json" {
			t.Errorf("leftover file in the store directory: %s", entry.Name())
		}
	}
}

// The TypeScript store owns this format; a Go write it cannot parse is the one
// failure mode neither side's own tests would catch.
func TestWrittenShapeMatchesTheTypeScriptStore(t *testing.T) {
	root := t.TempDir()
	store := New(root)
	at := now()

	if err := store.Record([]Reading{reading("five_hour", 44.5, 1787160000, at)}, at); err != nil {
		t.Fatalf("Record: %v", err)
	}
	data, err := os.ReadFile(store.Path())
	if err != nil {
		t.Fatalf("read: %v", err)
	}

	var parsed struct {
		Version int `json:"version"`
		Buckets map[string]struct {
			RateLimitType string  `json:"rateLimitType"`
			Utilization   float64 `json:"utilization"`
			ResetsAt      int64   `json:"resetsAt"`
			Status        string  `json:"status"`
			ObservedAt    string  `json:"observedAt"`
		} `json:"buckets"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if parsed.Version != 1 {
		t.Errorf("version = %d, want 1", parsed.Version)
	}
	entry, ok := parsed.Buckets["five_hour"]
	if !ok {
		t.Fatalf("no five_hour bucket in %s", data)
	}
	if entry.RateLimitType != "five_hour" || entry.Utilization != 44.5 ||
		entry.ResetsAt != 1787160000 || entry.Status != "allowed" {
		t.Errorf("entry = %+v, want the camelCase shape the TS store reads", entry)
	}
	// Date.toISOString() notation, so the two writers do not alternate between
	// offset and Z form on every write.
	if !strings.HasSuffix(entry.ObservedAt, "Z") {
		t.Errorf("observedAt = %q, want UTC Z notation", entry.ObservedAt)
	}
	// Two-space indent and a trailing newline, matching the TS writer.
	if !strings.HasSuffix(string(data), "}\n") || !strings.Contains(string(data), "\n  \"version\"") {
		t.Errorf("formatting differs from the TypeScript writer:\n%s", data)
	}
}

func TestRecordWithNoReadingsDoesNotCreateAFile(t *testing.T) {
	store := New(t.TempDir())
	if err := store.Record(nil, now()); err != nil {
		t.Fatalf("Record: %v", err)
	}
	if _, err := os.Stat(store.Path()); !os.IsNotExist(err) {
		t.Errorf("store created for an empty write: %v", err)
	}
}

func TestForAccountUsesHomeDirectory(t *testing.T) {
	t.Setenv("HOME", "/tmp/nightgauge-usagestore-home")
	store, err := ForAccount()
	if err != nil {
		t.Fatalf("ForAccount: %v", err)
	}
	want := filepath.Join("/tmp/nightgauge-usagestore-home", ".nightgauge/usage/claude-rate-limits.json")
	if store.Path() != want {
		t.Errorf("Path() = %q, want %q", store.Path(), want)
	}
}
