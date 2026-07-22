package telemetry

import (
	"bufio"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

func readEvents(t *testing.T, root string) []Event {
	t.Helper()
	f, err := os.Open(Path(root))
	if err != nil {
		t.Fatalf("open events: %v", err)
	}
	defer f.Close()
	var events []Event
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		var ev Event
		if err := json.Unmarshal(scanner.Bytes(), &ev); err != nil {
			t.Fatalf("unmarshal: %v (%q)", err, scanner.Text())
		}
		events = append(events, ev)
	}
	return events
}

func TestEmit_AutofillsTimestampAndStage(t *testing.T) {
	root := t.TempDir()
	t.Setenv("NIGHTGAUGE_STAGE", "feature-dev")
	t.Setenv("NIGHTGAUGE_TELEMETRY_REDACT_QUERIES", "")

	if err := Emit(root, Event{Type: EventScaffold, Scope: "issue:42", IssueNumber: 42}); err != nil {
		t.Fatalf("emit: %v", err)
	}

	events := readEvents(t, root)
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	got := events[0]
	if got.Type != EventScaffold {
		t.Fatalf("type: %q", got.Type)
	}
	if got.Stage != "feature-dev" {
		t.Fatalf("stage: %q", got.Stage)
	}
	if _, err := time.Parse(time.RFC3339, got.Timestamp); err != nil {
		t.Fatalf("timestamp not RFC3339: %v (%q)", err, got.Timestamp)
	}
}

func TestEmit_StageFallsBackToUnknown(t *testing.T) {
	root := t.TempDir()
	t.Setenv("NIGHTGAUGE_STAGE", "")

	if err := Emit(root, Event{Type: EventRead}); err != nil {
		t.Fatalf("emit: %v", err)
	}
	events := readEvents(t, root)
	if events[0].Stage != "unknown" {
		t.Fatalf("expected stage=unknown, got %q", events[0].Stage)
	}
}

func TestEmit_TruncatesQuerySummary(t *testing.T) {
	root := t.TempDir()
	t.Setenv("NIGHTGAUGE_TELEMETRY_REDACT_QUERIES", "")
	long := strings.Repeat("x", QuerySummaryMaxChars*2)
	if err := Emit(root, Event{Type: EventRecall, QuerySummary: long}); err != nil {
		t.Fatalf("emit: %v", err)
	}
	events := readEvents(t, root)
	if got := len(events[0].QuerySummary); got != QuerySummaryMaxChars {
		t.Fatalf("expected query_summary truncated to %d chars, got %d", QuerySummaryMaxChars, got)
	}
}

func TestEmit_RedactsQuerySummary(t *testing.T) {
	root := t.TempDir()
	t.Setenv("NIGHTGAUGE_TELEMETRY_REDACT_QUERIES", "1")
	if err := Emit(root, Event{Type: EventRecall, QuerySummary: "secret data here"}); err != nil {
		t.Fatalf("emit: %v", err)
	}
	events := readEvents(t, root)
	if events[0].QuerySummary != "<redacted>" {
		t.Fatalf("expected redacted, got %q", events[0].QuerySummary)
	}
}

func TestEmit_RedactionSkippedWhenQueryEmpty(t *testing.T) {
	root := t.TempDir()
	t.Setenv("NIGHTGAUGE_TELEMETRY_REDACT_QUERIES", "1")
	if err := Emit(root, Event{Type: EventStats}); err != nil {
		t.Fatalf("emit: %v", err)
	}
	events := readEvents(t, root)
	if events[0].QuerySummary != "" {
		t.Fatalf("expected empty query_summary, got %q", events[0].QuerySummary)
	}
}

func TestEmit_RoundTripAllOptionalFields(t *testing.T) {
	root := t.TempDir()
	t.Setenv("NIGHTGAUGE_STAGE", "feature-validate")
	t.Setenv("NIGHTGAUGE_TELEMETRY_REDACT_QUERIES", "")
	hit := 2
	count := 5
	in := Event{
		Type:         EventRecallHit,
		Scope:        "issue:101",
		IssueNumber:  101,
		Path:         "/abs/path/to/decisions.md",
		QuerySummary: "short query",
		RecallID:     "recall-2026-05-15-1234",
		HitIndex:     &hit,
		ResultCount:  &count,
		DurationMs:   42,
		Status:       "success",
	}
	if err := Emit(root, in); err != nil {
		t.Fatalf("emit: %v", err)
	}
	events := readEvents(t, root)
	got := events[0]
	if got.RecallID != in.RecallID || got.Path != in.Path || got.DurationMs != in.DurationMs {
		t.Fatalf("round-trip mismatch: %+v", got)
	}
	if got.HitIndex == nil || *got.HitIndex != hit {
		t.Fatalf("hit_index lost: %+v", got.HitIndex)
	}
	if got.ResultCount == nil || *got.ResultCount != count {
		t.Fatalf("result_count lost: %+v", got.ResultCount)
	}
}

func TestEmit_AllEventTypesAccepted(t *testing.T) {
	root := t.TempDir()
	t.Setenv("NIGHTGAUGE_STAGE", "test")
	for _, name := range AllEventTypes() {
		if err := Emit(root, Event{Type: EventType(name)}); err != nil {
			t.Fatalf("emit %s: %v", name, err)
		}
	}
	events := readEvents(t, root)
	if got, want := len(events), len(AllEventTypes()); got != want {
		t.Fatalf("expected %d events, got %d", want, got)
	}
}

func TestIsValidEventType(t *testing.T) {
	if !IsValidEventType(EventGraduate) {
		t.Fatalf("graduate should be valid")
	}
	if IsValidEventType(EventType("bogus")) {
		t.Fatalf("bogus should be invalid")
	}
}

func TestPath_Stable(t *testing.T) {
	got := Path("/root")
	if !strings.HasSuffix(got, "/.nightgauge/pipeline/history/knowledge-events.jsonl") {
		t.Fatalf("unexpected path: %s", got)
	}
}

func TestIsEnabled_PassThrough(t *testing.T) {
	if IsEnabled(true) != true {
		t.Fatalf("true pass-through failed")
	}
	if IsEnabled(false) != false {
		t.Fatalf("false pass-through failed")
	}
}
