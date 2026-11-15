package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"
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

// --since takes a UTC day or an RFC 3339 instant (#2847). The instant scopes a
// first-gate measurement to the runs of the build merged at that time, so a
// same-day record from before it must not count.
func TestReadExitRecordsSince_DayAndInstant(t *testing.T) {
	root := gateRepo(t)
	dir, err := diagnostics.ExitRecordsDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	files := map[string][]string{
		"2026-11-14.jsonl": {"2026-11-14T23:45:00Z"},
		"2026-11-15.jsonl": {"2026-11-15T01:00:00Z", "2026-11-15T05:00:00.5Z", "not-a-time"},
		"2026-11-16.jsonl": {"2026-11-16T00:00:00Z"},
	}
	for name, stamps := range files {
		var body []byte
		for i, ts := range stamps {
			line, err := json.Marshal(diagnostics.StageExitRecord{
				Issue: 7, Stage: "feature-validate", RunID: name + string(rune('a'+i)), Timestamp: ts,
			})
			if err != nil {
				t.Fatal(err)
			}
			body = append(append(body, line...), '\n')
		}
		if err := os.WriteFile(filepath.Join(dir, name), body, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	stampsOf := func(since string) []string {
		t.Helper()
		recs, err := readExitRecordsSince(root, 0, since)
		if err != nil {
			t.Fatalf("since %q: %v", since, err)
		}
		out := make([]string, 0, len(recs))
		for _, r := range recs {
			out = append(out, r.Timestamp)
		}
		sort.Strings(out)
		return out
	}
	cases := []struct {
		since string
		want  []string
	}{
		{"", []string{"2026-11-14T23:45:00Z", "2026-11-15T01:00:00Z", "2026-11-15T05:00:00.5Z", "2026-11-16T00:00:00Z", "not-a-time"}},
		{"2026-11-15", []string{"2026-11-15T01:00:00Z", "2026-11-15T05:00:00.5Z", "2026-11-16T00:00:00Z", "not-a-time"}},
		{"2026-11-15T03:00:00Z", []string{"2026-11-15T05:00:00.5Z", "2026-11-16T00:00:00Z"}},
		// An offset instant is compared in UTC, and a record AT the instant counts.
		{"2026-11-15T03:00:00+02:00", []string{"2026-11-15T01:00:00Z", "2026-11-15T05:00:00.5Z", "2026-11-16T00:00:00Z"}},
		// The instant's UTC day, not its local day, picks the first file.
		{"2026-11-15T00:30:00+01:00", []string{"2026-11-14T23:45:00Z", "2026-11-15T01:00:00Z", "2026-11-15T05:00:00.5Z", "2026-11-16T00:00:00Z"}},
	}
	for _, c := range cases {
		if got := stampsOf(c.since); !reflect.DeepEqual(got, c.want) {
			t.Errorf("since %q = %v, want %v", c.since, got, c.want)
		}
	}
	// A run that spans the instant is left out whole: its 01:00 failed first
	// gate must not drop away and leave its 05:00 pass as the first gate.
	span := []byte{}
	for _, ts := range []string{"2026-11-15T01:00:00Z", "2026-11-15T05:00:00Z"} {
		line, err := json.Marshal(diagnostics.StageExitRecord{
			Issue: 8, Stage: "feature-validate", RunID: "span", Timestamp: ts,
		})
		if err != nil {
			t.Fatal(err)
		}
		span = append(append(span, line...), '\n')
	}
	f, err := os.OpenFile(filepath.Join(dir, "2026-11-15.jsonl"), os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write(span); err != nil {
		t.Fatal(err)
	}
	f.Close()
	recs, err := readExitRecordsSince(root, 8, "2026-11-15T03:00:00Z")
	if err != nil || len(recs) != 0 {
		t.Errorf("a run spanning the instant: got %d records (%v), want none", len(recs), err)
	}
	if recs, _ := readExitRecordsSince(root, 8, "2026-11-15T00:00:00Z"); len(recs) != 2 {
		t.Errorf("a run wholly after the instant: got %d records, want 2", len(recs))
	}
	for _, bad := range []string{"2026-11", "yesterday", "2026-11-15 03:00"} {
		if _, err := readExitRecordsSince(root, 0, bad); err == nil {
			t.Errorf("since %q: want an error", bad)
		}
	}
}
