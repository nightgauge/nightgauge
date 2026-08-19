package hooks

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/usagestore"
)

func fixedNow() time.Time {
	return time.Date(2026, 8, 19, 15, 0, 0, 0, time.UTC)
}

// payload builds a statusLine payload with the given rate-limit block spliced
// in verbatim, so a test can assert on shapes the Go structs do not model.
func payload(rateLimits string) []byte {
	base := `{"model":{"display_name":"Opus 5"},"workspace":{"current_dir":"/repos/nightgauge"}`
	if rateLimits != "" {
		base += `,"rate_limits":` + rateLimits
	}
	return []byte(base + "}")
}

func readStore(t *testing.T, root string) map[string]usagestore.Reading {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, ".nightgauge/usage/claude-rate-limits.json"))
	if err != nil {
		t.Fatalf("read store: %v", err)
	}
	var parsed struct {
		Version int                           `json:"version"`
		Buckets map[string]usagestore.Reading `json:"buckets"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("parse store: %v", err)
	}
	if parsed.Version != usagestore.StoreVersion {
		t.Fatalf("store version = %d, want %d", parsed.Version, usagestore.StoreVersion)
	}
	return parsed.Buckets
}

func TestClaudeStatusLineRecordsBothBuckets(t *testing.T) {
	root := t.TempDir()
	now := fixedNow()
	reset5 := now.Add(2 * time.Hour).Unix()
	reset7 := now.Add(72 * time.Hour).Unix()

	line, err := ClaudeStatusLine(
		payload(fmt.Sprintf(
			`{"five_hour":{"used_percentage":44.2,"resets_at":%d},"seven_day":{"used_percentage":61.8,"resets_at":%d}}`,
			reset5, reset7)),
		ClaudeStatusLineOptions{AccountRoot: root}, now)
	if err != nil {
		t.Fatalf("ClaudeStatusLine: %v", err)
	}

	buckets := readStore(t, root)
	if len(buckets) != 2 {
		t.Fatalf("buckets = %d, want 2: %+v", len(buckets), buckets)
	}
	if got := buckets["five_hour"].Utilization; got != 44.2 {
		t.Errorf("five_hour utilization = %v, want 44.2", got)
	}
	if got := buckets["seven_day"].ResetsAt; got != reset7 {
		t.Errorf("seven_day resetsAt = %d, want %d", got, reset7)
	}
	// The statusLine payload carries no vendor status field, and inferring one
	// from the percentage would attribute a threshold this code chose to the
	// vendor.
	if got := buckets["five_hour"].Status; got != statusLineStatus {
		t.Errorf("five_hour status = %q, want %q", got, statusLineStatus)
	}
	if !strings.Contains(line, "5h 44%") || !strings.Contains(line, "7d 62%") {
		t.Errorf("line = %q, want both windows", line)
	}
	// The weekly reset is the one shown: the five-hour window refills often
	// enough that its countdown is noise.
	if !strings.Contains(line, "resets 3d") {
		t.Errorf("line = %q, want the weekly reset countdown", line)
	}
}

func TestClaudeStatusLineRecordsOneBucket(t *testing.T) {
	root := t.TempDir()
	now := fixedNow()

	if _, err := ClaudeStatusLine(
		payload(fmt.Sprintf(`{"five_hour":{"used_percentage":7,"resets_at":%d}}`, now.Add(time.Hour).Unix())),
		ClaudeStatusLineOptions{AccountRoot: root}, now); err != nil {
		t.Fatalf("ClaudeStatusLine: %v", err)
	}

	buckets := readStore(t, root)
	if len(buckets) != 1 {
		t.Fatalf("buckets = %d, want 1: %+v", len(buckets), buckets)
	}
	if _, ok := buckets["seven_day"]; ok {
		t.Error("seven_day recorded from a payload that did not carry it")
	}
}

// A measured zero — "you have used none of this window" — is a real answer and
// must be recorded, unlike an absent field which says nothing at all.
func TestClaudeStatusLineRecordsMeasuredZero(t *testing.T) {
	root := t.TempDir()
	now := fixedNow()

	if _, err := ClaudeStatusLine(
		payload(`{"five_hour":{"used_percentage":0,"resets_at":0}}`),
		ClaudeStatusLineOptions{AccountRoot: root}, now); err != nil {
		t.Fatalf("ClaudeStatusLine: %v", err)
	}

	buckets := readStore(t, root)
	if got, ok := buckets["five_hour"]; !ok || got.Utilization != 0 {
		t.Fatalf("five_hour = %+v (present=%v), want a recorded zero", got, ok)
	}
}

func TestClaudeStatusLineIgnoresBucketWithoutPercentage(t *testing.T) {
	root := t.TempDir()
	now := fixedNow()

	line, err := ClaudeStatusLine(
		payload(`{"five_hour":{"resets_at":123}}`),
		ClaudeStatusLineOptions{AccountRoot: root}, now)
	if err != nil {
		t.Fatalf("ClaudeStatusLine: %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(root, ".nightgauge/usage/claude-rate-limits.json")); !os.IsNotExist(statErr) {
		t.Errorf("store written for a bucket with no percentage: %v", statErr)
	}
	if strings.Contains(line, "5h") {
		t.Errorf("line = %q, want no usage segment", line)
	}
}

// A non-subscriber, and a subscriber before the session's first API response,
// both send no rate_limits block. That is a normal state, not a failure.
func TestClaudeStatusLineWithoutRateLimits(t *testing.T) {
	root := t.TempDir()

	line, err := ClaudeStatusLine(payload(""), ClaudeStatusLineOptions{AccountRoot: root}, fixedNow())
	if err != nil {
		t.Fatalf("ClaudeStatusLine: %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(root, ".nightgauge/usage/claude-rate-limits.json")); !os.IsNotExist(statErr) {
		t.Errorf("store written with no rate_limits block: %v", statErr)
	}
	if line != "Opus 5 · nightgauge" {
		t.Errorf("line = %q, want the model and directory only", line)
	}
}

// An empty status line reads to the operator as though Claude Code broke, so
// even an unparseable payload has to produce output and exit cleanly.
func TestClaudeStatusLineToleratesMalformedPayload(t *testing.T) {
	root := t.TempDir()

	line, err := ClaudeStatusLine([]byte("not json at all"), ClaudeStatusLineOptions{AccountRoot: root}, fixedNow())
	if err != nil {
		t.Fatalf("ClaudeStatusLine returned an error for a malformed payload: %v", err)
	}
	if line != "" {
		t.Errorf("line = %q, want empty (nothing was parseable)", line)
	}
}

func TestClaudeStatusLineDelegatePassthrough(t *testing.T) {
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Skip("no /bin/sh")
	}
	root := t.TempDir()
	now := fixedNow()

	line, err := ClaudeStatusLine(
		payload(fmt.Sprintf(`{"seven_day":{"used_percentage":50,"resets_at":%d}}`, now.Add(time.Hour).Unix())),
		ClaudeStatusLineOptions{AccountRoot: root, Delegate: "printf 'my own line'"}, now)
	if err != nil {
		t.Fatalf("ClaudeStatusLine: %v", err)
	}
	if line != "my own line" {
		t.Errorf("line = %q, want the delegate's output", line)
	}
	// Recording is not conditional on rendering: adopting the feed must not
	// cost the operator their status line, and keeping their status line must
	// not cost them the feed.
	if len(readStore(t, root)) != 1 {
		t.Error("delegate suppressed the recording")
	}
}

func TestClaudeStatusLineDelegateReceivesPayload(t *testing.T) {
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Skip("no /bin/sh")
	}
	root := t.TempDir()

	line, err := ClaudeStatusLine(payload(""),
		ClaudeStatusLineOptions{AccountRoot: root, Delegate: "cat"}, fixedNow())
	if err != nil {
		t.Fatalf("ClaudeStatusLine: %v", err)
	}
	if !strings.Contains(line, `"display_name":"Opus 5"`) {
		t.Errorf("delegate stdout = %q, want the original payload", line)
	}
}

func TestClaudeStatusLineFallsBackWhenDelegateFails(t *testing.T) {
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Skip("no /bin/sh")
	}
	root := t.TempDir()

	line, err := ClaudeStatusLine(payload(""),
		ClaudeStatusLineOptions{AccountRoot: root, Delegate: "exit 3"}, fixedNow())
	if err != nil {
		t.Fatalf("ClaudeStatusLine: %v", err)
	}
	if line != "Opus 5 · nightgauge" {
		t.Errorf("line = %q, want nightgauge's own line after a delegate failure", line)
	}
}

// An unwritable store costs the footer one reading it will re-observe on the
// next render. It must not cost the operator their status line.
func TestClaudeStatusLineSurvivesUnwritableStore(t *testing.T) {
	root := filepath.Join(t.TempDir(), "blocked")
	if err := os.WriteFile(root, []byte("not a directory"), 0o600); err != nil {
		t.Fatalf("seed blocker: %v", err)
	}
	now := fixedNow()

	line, err := ClaudeStatusLine(
		payload(`{"five_hour":{"used_percentage":10,"resets_at":0}}`),
		ClaudeStatusLineOptions{AccountRoot: root}, now)
	if err != nil {
		t.Fatalf("ClaudeStatusLine: %v", err)
	}
	if !strings.Contains(line, "5h 10%") {
		t.Errorf("line = %q, want the usage segment despite the write failure", line)
	}
}

func TestFormatRemaining(t *testing.T) {
	for _, tc := range []struct {
		d    time.Duration
		want string
	}{
		{75 * time.Hour, "3d 3h"},
		{48 * time.Hour, "2d"},
		{3*time.Hour + 14*time.Minute, "3h 14m"},
		{2 * time.Hour, "2h"},
		{12 * time.Minute, "12m"},
		{30 * time.Second, "<1m"},
	} {
		if got := formatRemaining(tc.d); got != tc.want {
			t.Errorf("formatRemaining(%v) = %q, want %q", tc.d, got, tc.want)
		}
	}
}
