package trace

import (
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/diagnostics"
	"github.com/nightgauge/nightgauge/internal/state"
)

func TestExportJoinsTraceRunRecordAndExitRecords(t *testing.T) {
	root := t.TempDir()
	runID := "01890a5d-ac96-774b-bcce-b302099a8057"

	w := NewWriter(root, runID, "nightgauge/nightgauge", 179)
	w.Emit(KindStageStart, "issue-pickup", StageStartPayload{Model: "haiku"})
	w.Emit(KindStageExit, "issue-pickup", StageExitPayload{Success: true})
	w.Emit(KindOutcome, "", OutcomePayload{Success: true, TotalCostUSD: 1.25})

	now := time.Now().UTC().Format(time.RFC3339)
	hw := state.NewHistoryWriter(root)
	if err := hw.WriteRecord(state.V2RunRecord{
		SchemaVersion: "3",
		RecordType:    "run",
		IssueNumber:   179,
		RunID:         runID,
		Repo:          "nightgauge/nightgauge",
		Outcome:       "complete",
		RecordedAt:    now,
	}); err != nil {
		t.Fatalf("WriteRecord: %v", err)
	}
	// A second record with a different run_id must not be joined.
	if err := hw.WriteRecord(state.V2RunRecord{
		SchemaVersion: "3",
		RecordType:    "run",
		IssueNumber:   180,
		RunID:         "01890a5d-ac96-774b-bcce-b30209999999",
		Outcome:       "failed",
		RecordedAt:    now,
	}); err != nil {
		t.Fatalf("WriteRecord: %v", err)
	}

	if err := diagnostics.WriteStageExitRecord(root, diagnostics.StageExitRecord{
		Repo: "nightgauge/nightgauge", Issue: 179, Stage: "issue-pickup",
		RunID: runID, Success: true,
	}); err != nil {
		t.Fatalf("WriteStageExitRecord: %v", err)
	}
	if err := diagnostics.WriteStageExitRecord(root, diagnostics.StageExitRecord{
		Repo: "nightgauge/nightgauge", Issue: 180, Stage: "issue-pickup",
		RunID: "01890a5d-ac96-774b-bcce-b30209999999", Success: false,
	}); err != nil {
		t.Fatalf("WriteStageExitRecord: %v", err)
	}

	doc, err := Export(root, runID)
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	if doc.RunID != runID || doc.Repo != "nightgauge/nightgauge" || doc.Issue != 179 {
		t.Errorf("doc header = (%q, %q, %d)", doc.RunID, doc.Repo, doc.Issue)
	}
	if len(doc.Events) != 3 {
		t.Errorf("doc.Events = %d, want 3", len(doc.Events))
	}
	if doc.RunRecord == nil {
		t.Fatal("doc.RunRecord = nil, want joined record")
	}
	if doc.RunRecord.IssueNumber != 179 || doc.RunRecord.RunID != runID {
		t.Errorf("joined wrong run record: issue=%d run_id=%q",
			doc.RunRecord.IssueNumber, doc.RunRecord.RunID)
	}
	if len(doc.ExitRecords) != 1 {
		t.Fatalf("doc.ExitRecords = %d, want 1 (other run filtered)", len(doc.ExitRecords))
	}
	if doc.ExitRecords[0].Issue != 179 {
		t.Errorf("exit record issue = %d, want 179", doc.ExitRecords[0].Issue)
	}

	// Ordering: events sorted by (ts, producer, seq).
	for i := 1; i < len(doc.Events); i++ {
		prev, cur := doc.Events[i-1], doc.Events[i]
		if cur.Ts < prev.Ts {
			t.Errorf("events out of order at %d: %s < %s", i, cur.Ts, prev.Ts)
		}
	}
}

func TestExportWithoutJoinTargets(t *testing.T) {
	root := t.TempDir()
	runID := "01890a5d-ac96-774b-bcce-b302099a8057"
	w := NewWriter(root, runID, "o/r", 1)
	w.Emit(KindStageStart, "issue-pickup", nil)

	doc, err := Export(root, runID)
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	if doc.RunRecord != nil {
		t.Error("RunRecord should be nil when no history matches")
	}
	if len(doc.ExitRecords) != 0 {
		t.Errorf("ExitRecords = %d, want 0", len(doc.ExitRecords))
	}
}

func TestExportMissingTraceErrors(t *testing.T) {
	if _, err := Export(t.TempDir(), "01890a5d-ac96-774b-bcce-b302099a8057"); err == nil {
		t.Fatal("Export on a run with no trace should error")
	}
}
