package trace

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestFilePathRejectsUnsafeRunIDs(t *testing.T) {
	tests := []struct {
		name    string
		runID   string
		wantErr bool
	}{
		{"uuid v7", "01890a5d-ac96-774b-bcce-b302099a8057", false},
		{"remote id with underscore", "run_0189aabbccdd001122", false},
		{"path traversal", "../../etc/passwd", true},
		{"slash", "a/b", true},
		{"empty", "", true},
		{"too short", "abc", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := FilePath(t.TempDir(), tt.runID)
			if (err != nil) != tt.wantErr {
				t.Errorf("FilePath(%q) error = %v, wantErr %v", tt.runID, err, tt.wantErr)
			}
		})
	}
}

func TestNewWriterNilSafety(t *testing.T) {
	tests := []struct {
		name    string
		rootDir string
		runID   string
	}{
		{"empty root", "", "01890a5d-ac96-774b-bcce-b302099a8057"},
		{"empty run id", "/tmp/x", ""},
		{"unsafe run id", "/tmp/x", "../escape"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := NewWriter(tt.rootDir, tt.runID, "o/r", 1)
			if w != nil {
				t.Fatalf("NewWriter(%q, %q) = %v, want nil", tt.rootDir, tt.runID, w)
			}
			// Emitting on the nil writer must be a safe no-op.
			w.Emit(KindStageStart, "feature-dev", nil)
			if got := w.RunID(); got != "" {
				t.Errorf("nil writer RunID() = %q, want empty", got)
			}
		})
	}
}

func TestWriterEmitAndReadRun(t *testing.T) {
	root := t.TempDir()
	runID := "01890a5d-ac96-774b-bcce-b302099a8057"
	w := NewWriter(root, runID, "nightgauge/nightgauge", 179)
	if w == nil {
		t.Fatal("NewWriter returned nil for valid inputs")
	}

	w.Emit(KindStageStart, "issue-pickup", StageStartPayload{Model: "haiku"})
	w.Emit(KindModelRouting, "", ModelRoutingPayload{
		ForStage:  "feature-dev",
		Model:     "sonnet",
		Reasoning: "medium complexity (5/10) — balanced model",
		Alternatives: []RoutingAlternative{
			{Model: "haiku", TradeOff: "faster, cheaper, may miss edge cases"},
			{Model: "opus", TradeOff: "highest quality, ~3x cost"},
		},
		Trigger: "scheduler_pickup",
	})
	w.Emit(KindStageExit, "issue-pickup", StageExitPayload{Success: true, ExitCode: 0})

	events, err := ReadRun(root, runID)
	if err != nil {
		t.Fatalf("ReadRun: %v", err)
	}
	if len(events) != 3 {
		t.Fatalf("ReadRun returned %d events, want 3", len(events))
	}

	// Envelope invariants.
	for i, ev := range events {
		if ev.SchemaVersion != SchemaVersion {
			t.Errorf("event %d schema_version = %d, want %d", i, ev.SchemaVersion, SchemaVersion)
		}
		if ev.RunID != runID {
			t.Errorf("event %d run_id = %q, want %q", i, ev.RunID, runID)
		}
		if ev.Producer != ProducerGo {
			t.Errorf("event %d producer = %q, want %q", i, ev.Producer, ProducerGo)
		}
		if ev.Issue != 179 || ev.Repo != "nightgauge/nightgauge" {
			t.Errorf("event %d join keys = (%q, %d)", i, ev.Repo, ev.Issue)
		}
		if ev.Seq != int64(i+1) {
			t.Errorf("event %d seq = %d, want %d", i, ev.Seq, i+1)
		}
		if _, err := time.Parse(time.RFC3339Nano, ev.Ts); err != nil {
			t.Errorf("event %d ts %q not RFC3339Nano: %v", i, ev.Ts, err)
		}
	}

	// Decision rationale must survive the round trip as structured fields.
	routing := events[1]
	if routing.Kind != KindModelRouting {
		t.Fatalf("event 1 kind = %q, want %q", routing.Kind, KindModelRouting)
	}
	payload, ok := routing.Payload.(map[string]any)
	if !ok {
		t.Fatalf("routing payload type %T, want map", routing.Payload)
	}
	if payload["reasoning"] != "medium complexity (5/10) — balanced model" {
		t.Errorf("routing reasoning = %v", payload["reasoning"])
	}
	alts, ok := payload["alternatives"].([]any)
	if !ok || len(alts) != 2 {
		t.Fatalf("routing alternatives = %v, want 2 entries", payload["alternatives"])
	}
}

func TestWriterSeqResumesFromExistingFile(t *testing.T) {
	root := t.TempDir()
	runID := "01890a5d-ac96-774b-bcce-b302099a8057"

	w1 := NewWriter(root, runID, "o/r", 7)
	w1.Emit(KindStageStart, "issue-pickup", nil)
	w1.Emit(KindStageExit, "issue-pickup", nil)

	// A crash-restarted process creates a fresh writer over the same file:
	// seq must keep increasing, never regress.
	w2 := NewWriter(root, runID, "o/r", 7)
	w2.Emit(KindStageStart, "feature-planning", nil)

	events, err := ReadRun(root, runID)
	if err != nil {
		t.Fatalf("ReadRun: %v", err)
	}
	if len(events) != 3 {
		t.Fatalf("got %d events, want 3", len(events))
	}
	if events[2].Seq <= events[1].Seq {
		t.Errorf("resumed seq %d not greater than prior %d", events[2].Seq, events[1].Seq)
	}
}

func TestReadRunMissingFileReturnsEmpty(t *testing.T) {
	events, err := ReadRun(t.TempDir(), "01890a5d-ac96-774b-bcce-b302099a8057")
	if err != nil {
		t.Fatalf("ReadRun on missing file: %v", err)
	}
	if events != nil {
		t.Errorf("ReadRun = %v, want nil for missing file", events)
	}
}

func TestReadRunSkipsMalformedLines(t *testing.T) {
	root := t.TempDir()
	runID := "01890a5d-ac96-774b-bcce-b302099a8057"
	w := NewWriter(root, runID, "o/r", 1)
	w.Emit(KindStageStart, "issue-pickup", nil)

	path, err := FilePath(root, runID)
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("{not json\n"); err != nil {
		t.Fatal(err)
	}
	f.Close()
	w.Emit(KindStageExit, "issue-pickup", nil)

	events, err := ReadRun(root, runID)
	if err != nil {
		t.Fatalf("ReadRun: %v", err)
	}
	if len(events) != 2 {
		t.Errorf("got %d events, want 2 (malformed line skipped)", len(events))
	}
}

func TestSortEventsTotalOrder(t *testing.T) {
	events := []Event{
		{Ts: "2026-07-17T10:00:02Z", Producer: ProducerGo, Seq: 3},
		{Ts: "2026-07-17T10:00:01Z", Producer: ProducerSDK, Seq: 9},
		{Ts: "2026-07-17T10:00:01Z", Producer: ProducerGo, Seq: 2},
		{Ts: "2026-07-17T10:00:01Z", Producer: ProducerGo, Seq: 1},
	}
	SortEvents(events)
	want := []struct {
		producer string
		seq      int64
	}{
		{ProducerGo, 1},
		{ProducerGo, 2},
		{ProducerSDK, 9},
		{ProducerGo, 3},
	}
	for i, w := range want {
		if events[i].Producer != w.producer || events[i].Seq != w.seq {
			t.Errorf("position %d = (%s, %d), want (%s, %d)",
				i, events[i].Producer, events[i].Seq, w.producer, w.seq)
		}
	}
}

// TestSortEvents_WholeSecondBoundaryOrdersByTime is the regression guard for
// #226: an event whose timestamp lands exactly on a whole second must still
// sort BEFORE a later event with a fractional part in the same second. Both
// timestamps are formatted through the production tsLayout, so if Emit's layout
// ever regresses to time.RFC3339Nano (which trims trailing zeros) the constant
// changes with it and this test fails — the whole-second stamp ("…49Z") would
// then sort AFTER the fractional one ("…49.5Z") because 'Z' > '.', flipping
// both chronological order and the seq tiebreaker.
func TestSortEvents_WholeSecondBoundaryOrdersByTime(t *testing.T) {
	base := time.Date(2026, 7, 17, 10, 0, 49, 0, time.UTC) // exactly on a second
	earlier := base
	later := base.Add(500 * time.Millisecond)

	// Deliberately insert them out of order (later first) so the sort must move
	// the whole-second event ahead of the fractional one.
	events := []Event{
		{Ts: later.Format(tsLayout), Producer: ProducerGo, Seq: 2},
		{Ts: earlier.Format(tsLayout), Producer: ProducerGo, Seq: 1},
	}
	SortEvents(events)

	if events[0].Ts > events[1].Ts {
		t.Fatalf("not in chronological order after sort: %q then %q",
			events[0].Ts, events[1].Ts)
	}
	// The whole-second event (seq 1) must come first; same-producer seq stays
	// monotonically increasing in sorted order.
	if events[0].Seq != 1 || events[1].Seq != 2 {
		t.Errorf("seq order after sort = [%d, %d], want [1, 2] (chronological)",
			events[0].Seq, events[1].Seq)
	}
	if events[1].Seq <= events[0].Seq {
		t.Errorf("same-producer seq not monotonic after sort: %d then %d",
			events[0].Seq, events[1].Seq)
	}
}

func TestFindLatestRunIDForIssue(t *testing.T) {
	root := t.TempDir()

	older := NewWriter(root, "01890a5d-ac96-774b-bcce-b30209900001", "o/r", 42)
	older.Emit(KindStageStart, "issue-pickup", nil)
	otherIssue := NewWriter(root, "01890a5d-ac96-774b-bcce-b30209900002", "o/r", 99)
	otherIssue.Emit(KindStageStart, "issue-pickup", nil)
	newer := NewWriter(root, "01890a5d-ac96-774b-bcce-b30209900003", "o/r", 42)
	newer.Emit(KindStageStart, "issue-pickup", nil)

	// Ensure distinct mtimes so newest-first ordering is deterministic.
	newerPath, _ := FilePath(root, "01890a5d-ac96-774b-bcce-b30209900003")
	future := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(newerPath, future, future); err != nil {
		t.Fatal(err)
	}

	got, err := FindLatestRunIDForIssue(root, 42)
	if err != nil {
		t.Fatalf("FindLatestRunIDForIssue: %v", err)
	}
	if got != "01890a5d-ac96-774b-bcce-b30209900003" {
		t.Errorf("got %q, want the newest issue-42 run", got)
	}

	missing, err := FindLatestRunIDForIssue(root, 7)
	if err != nil {
		t.Fatalf("FindLatestRunIDForIssue(7): %v", err)
	}
	if missing != "" {
		t.Errorf("issue with no trace = %q, want empty", missing)
	}
}

func TestListRunIDsEmptyDir(t *testing.T) {
	ids, err := ListRunIDs(t.TempDir())
	if err != nil {
		t.Fatalf("ListRunIDs: %v", err)
	}
	if len(ids) != 0 {
		t.Errorf("ListRunIDs = %v, want empty", ids)
	}
}

func TestKindValidation(t *testing.T) {
	for _, k := range AllKinds() {
		if !IsValidKind(Kind(k)) {
			t.Errorf("declared kind %q not valid", k)
		}
	}
	if IsValidKind(Kind("nonsense")) {
		t.Error("IsValidKind accepted an undeclared kind")
	}
	if len(AllKinds()) != 12 {
		t.Errorf("taxonomy size = %d, want 12 (update ADR 013 if intentional)", len(AllKinds()))
	}
}

func TestDirLayout(t *testing.T) {
	root := t.TempDir()
	w := NewWriter(root, "01890a5d-ac96-774b-bcce-b302099a8057", "o/r", 1)
	w.Emit(KindStageStart, "issue-pickup", nil)
	want := filepath.Join(root, ".nightgauge", "pipeline", "trace", "01890a5d-ac96-774b-bcce-b302099a8057.jsonl")
	if _, err := os.Stat(want); err != nil {
		t.Errorf("expected trace file at %s: %v", want, err)
	}
}
