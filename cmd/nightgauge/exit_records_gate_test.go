package main

import (
	"testing"

	"github.com/nightgauge/nightgauge/internal/diagnostics"
	"github.com/nightgauge/nightgauge/internal/gateledger"
)

func TestSummarizeGates(t *testing.T) {
	sum := summarizeGates([]diagnostics.StageExitRecord{
		{Issue: 7, RunID: "a", Stage: "feature-dev", ReviewPhases: diagnostics.ReviewPhasesRan, ElapsedMs: 1000, GateRuns: 1, GateSeconds: 100},
		{Issue: 7, RunID: "a", Stage: "feature-validate", GateRuns: 2, GateSeconds: 200, GateUnfinished: 1},
		{Issue: 7, RunID: "a", Stage: "feature-validate", GateRuns: 1, GateSeconds: 50, GateReused: 3},
		{Issue: 8, RunID: "b", Stage: "feature-dev", ReviewPhases: diagnostics.ReviewPhasesMissing, ElapsedMs: 500},
		{Issue: 9, RunID: "c", Stage: "feature-dev", ElapsedMs: 200},
	})
	if len(sum.Runs) != 3 || sum.Runs[0].Issue != 7 {
		t.Fatalf("runs: %+v", sum.Runs)
	}
	total := sum.Runs[0].Total
	if total.Runs != 4 || total.Seconds != 350 || total.Reused != 3 || total.Unfinished != 1 {
		t.Errorf("run total: %+v", total)
	}
	if v := sum.Runs[0].Stages["feature-validate"]; v.Runs != 3 || v.Stages != 2 {
		t.Errorf("stage total: %+v", v)
	}
	ran, missing, unknown := sum.FeatureDevReview[diagnostics.ReviewPhasesRan],
		sum.FeatureDevReview[diagnostics.ReviewPhasesMissing], sum.FeatureDevReview[""]
	if ran == nil || ran.ElapsedMs != 1000 || missing == nil || missing.ElapsedMs != 500 || unknown == nil || unknown.Stages != 1 {
		t.Errorf("split: %+v", sum.FeatureDevReview)
	}
}

// A stage exit written through the real writer carries the ledger's numbers, and
// the summary sums them: the whole path a retro reads.
func TestGateSummary_ReadsWhatTheWriterRecorded(t *testing.T) {
	root := gateRepo(t)
	t.Setenv("NIGHTGAUGE_ISSUE_NUMBER", "7")
	t.Setenv("NIGHTGAUGE_STAGE", "feature-validate")
	record(t, root, gateledger.Event{
		Event: gateledger.EventFinish, RunID: "r", Kind: gateledger.KindLocalGate,
		Tree: fingerprint(t, root), Verdict: gateledger.VerdictPass, Full: true, Seconds: 900,
	})
	if err := diagnostics.WriteStageExitRecord(root, diagnostics.StageExitRecord{
		Issue: 7, Stage: "feature-validate", RunID: "run", ElapsedMs: 60_000,
	}); err != nil {
		t.Fatal(err)
	}
	records, err := readExitRecordsSince(root, 7, "")
	if err != nil || len(records) != 1 {
		t.Fatalf("records = %v, err = %v", records, err)
	}
	if rec := records[0]; rec.GateRuns != 1 || rec.GateSeconds != 900 {
		t.Fatalf("exit record: runs=%d seconds=%d", rec.GateRuns, rec.GateSeconds)
	}
	if got := summarizeGates(records).Runs[0].Total.Seconds; got != 900 {
		t.Fatalf("summary seconds = %d", got)
	}
	if later, _ := readExitRecordsSince(root, 7, "2999-01-01"); len(later) != 0 {
		t.Fatalf("--since kept %d records", len(later))
	}
}
