package trace

import (
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/diagnostics"
	"github.com/nightgauge/nightgauge/internal/intelligence/survival"
	"github.com/nightgauge/nightgauge/internal/layout/layouttest"
	"github.com/nightgauge/nightgauge/internal/state"
)

func TestExportJoinsTraceRunRecordAndExitRecords(t *testing.T) {
	root := layouttest.Repo(t)
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
	root := layouttest.Repo(t)
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
	if _, err := Export(layouttest.Repo(t), "01890a5d-ac96-774b-bcce-b302099a8057"); err == nil {
		t.Fatal("Export on a run with no trace should error")
	}
}

const joinRun = "01890a5d-ac96-774b-bcce-b30209900021"

func writeJoinRun(t *testing.T, root string) {
	t.Helper()
	writeRawRun(t, root, joinRun, "o/r", 1283, "2026-07-01T10:00:00.000Z", "2026-07-01T11:00:00.000Z")
}

func appendSurvival(t *testing.T, root string, rec survival.Record) {
	t.Helper()
	if _, err := survival.NewStore(root).Append(rec); err != nil {
		t.Fatalf("append survival: %v", err)
	}
}

func TestExportJoinsMergeAndSurvival(t *testing.T) {
	root := layouttest.Repo(t)
	writeJoinRun(t, root)
	// Outside the window (before the run), another issue, another repo: none join.
	appendSurvival(t, root, survival.NewPending("o/r", 1283, 1, "sha-before", "2026-07-01T09:00:00Z", "main"))
	appendSurvival(t, root, survival.NewPending("o/r", 9999, 2, "sha-other-issue", "2026-07-01T11:05:00Z", "main"))
	appendSurvival(t, root, survival.NewPending("x/y", 1283, 3, "sha-other-repo", "2026-07-01T11:05:00Z", "main"))
	rec := survival.NewPending("o/r", 1283, 42, "sha-match", "2026-07-01T11:20:00Z", "main")
	rec.MergedBy = "octo-maintainer"
	appendSurvival(t, root, rec)

	doc, err := Export(root, joinRun)
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	m := doc.Merge
	if m.Status != JoinJoined || m.MergeCommitSHA != "sha-match" || m.PRNumber != 42 {
		t.Fatalf("merge = %+v, want joined sha-match PR 42", m)
	}
	if m.MergedBy != "octo-maintainer" || m.MergedByStatus != JoinJoined {
		t.Errorf("merged_by = %q (%s), want octo-maintainer joined", m.MergedBy, m.MergedByStatus)
	}
	if doc.Survival.Status != JoinJoined || doc.Survival.Verdict != "pending" || doc.Survival.Reason == "" {
		t.Errorf("survival = %+v, want joined pending with a not-finalized reason", doc.Survival)
	}
	if doc.IssueRuns.Status != JoinJoined || len(doc.IssueRuns.RunIDs) != 1 || doc.IssueRuns.RunIDs[0] != joinRun {
		t.Errorf("issue_runs = %+v, want [%s]", doc.IssueRuns, joinRun)
	}
}

func TestExportMergeActorNotCapturedIsUnavailable(t *testing.T) {
	root := layouttest.Repo(t)
	writeJoinRun(t, root)
	appendSurvival(t, root, survival.NewPending("o/r", 1283, 42, "sha-old", "2026-07-01T10:30:00Z", "main"))

	doc, err := Export(root, joinRun)
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	if doc.Merge.Status != JoinJoined {
		t.Fatalf("merge status = %s, want joined", doc.Merge.Status)
	}
	if doc.Merge.MergedBy != "" || doc.Merge.MergedByStatus != JoinUnavailable || doc.Merge.MergedByReason == "" {
		t.Errorf("merge = %+v, want merged_by unavailable with a reason, never invented", doc.Merge)
	}
}

func TestExportWithoutMergeSaysUnavailable(t *testing.T) {
	root := layouttest.Repo(t)
	writeJoinRun(t, root)
	// A merge past the 30m grace window does not join.
	appendSurvival(t, root, survival.NewPending("o/r", 1283, 42, "sha-late", "2026-07-01T11:45:00Z", "main"))

	doc, err := Export(root, joinRun)
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	if doc.Merge.Status != JoinUnavailable || doc.Merge.Reason == "" ||
		doc.Survival.Status != JoinUnavailable || doc.Survival.Reason == "" {
		t.Errorf("merge=%+v survival=%+v, want both unavailable with reasons", doc.Merge, doc.Survival)
	}
}
