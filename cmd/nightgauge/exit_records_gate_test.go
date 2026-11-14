package main

import (
	"reflect"
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

// TestSummarizeGates_FirstGates pins the first-gate pass rate and the failing
// steps across runs (#2847): pass, fail[gofmt], fail[gofmt, publication].
func TestSummarizeGates_FirstGates(t *testing.T) {
	const gofmt, pub = "gofmt -l ./internal ./cmd", "Publication boundary"
	fv := "feature-validate"
	sum := summarizeGates([]diagnostics.StageExitRecord{
		{Issue: 1, RunID: "a", Stage: "feature-dev"}, // no gate: not the first gate
		{Issue: 1, RunID: "a", Stage: fv, GateRuns: 1, GateFirstVerdict: gateledger.VerdictPass},
		{Issue: 2, RunID: "b", Stage: fv, GateRuns: 2, GateFirstVerdict: gateledger.VerdictFail,
			GateFirstFailedSteps: []string{gofmt}},
		{Issue: 2, RunID: "b", Stage: "pr-create", GateRuns: 1, GateFirstVerdict: gateledger.VerdictPass},
		{Issue: 3, RunID: "c", Stage: fv, GateRuns: 2, GateFirstVerdict: gateledger.VerdictFail,
			GateFirstFailedSteps: []string{gofmt, pub}},
		{Issue: 4, RunID: "d", Stage: fv}, // no gate at all
	})
	if fg := sum.Runs[0].FirstGate; fg == nil || fg.Stage != fv || fg.Verdict != gateledger.VerdictPass {
		t.Errorf("run a first gate = %+v", fg)
	}
	if fg := sum.Runs[1].FirstGate; fg == nil || fg.Verdict != gateledger.VerdictFail {
		t.Errorf("run b first gate = %+v, want the earlier fail, not pr-create's pass", fg)
	}
	if sum.Runs[3].FirstGate != nil {
		t.Errorf("run d has a first gate: %+v", sum.Runs[3].FirstGate)
	}
	want := firstGateTotals{Runs: 3, Passed: 1, Failed: 2, PassRate: 1.0 / 3}
	if sum.FirstGateTotals != want {
		t.Errorf("totals = %+v, want %+v", sum.FirstGateTotals, want)
	}
	wantSteps := []failingStep{{Step: gofmt, FirstGates: 2, Runs: 2}, {Step: pub, FirstGates: 1, Runs: 1}}
	if !reflect.DeepEqual(sum.FailingSteps, wantSteps) {
		t.Errorf("failing steps = %+v, want %+v", sum.FailingSteps, wantSteps)
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
	record(t, root, gateledger.Event{
		Event: gateledger.EventFinish, RunID: "r2", Kind: gateledger.KindLocalGate,
		Tree: fingerprint(t, root), Verdict: gateledger.VerdictFail, Full: true,
		FailedSteps: []string{"Pipefail early-exit gate"},
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
	if rec := records[0]; rec.GateRuns != 2 || rec.GateSeconds != 900 || rec.GateFirstVerdict != gateledger.VerdictPass {
		t.Fatalf("exit record: runs=%d seconds=%d first=%q", rec.GateRuns, rec.GateSeconds, rec.GateFirstVerdict)
	}
	if got := summarizeGates(records).Runs[0].Total.Seconds; got != 900 {
		t.Fatalf("summary seconds = %d", got)
	}
	if later, _ := readExitRecordsSince(root, 7, "2999-01-01"); len(later) != 0 {
		t.Fatalf("--since kept %d records", len(later))
	}
}
