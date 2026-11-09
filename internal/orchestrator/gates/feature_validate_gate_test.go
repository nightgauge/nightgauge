package gates

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/layout"
	"github.com/nightgauge/nightgauge/internal/layout/layouttest"
	"github.com/nightgauge/nightgauge/internal/state"
)

// writeGateMetrics writes JSONL records to the checkout's
// health/gate-metrics.jsonl (ADR-024 § 7), making workspace a git repository
// first when it is not one. Mirrors the format ReadGateMetricsForIssue parses.
func writeGateMetrics(t *testing.T, workspace string, issueNumber int, records []map[string]any) {
	t.Helper()
	dir := layouttest.MkCheckoutSubdir(t, workspace, layout.CheckoutHealth)
	f, err := os.Create(filepath.Join(dir, "gate-metrics.jsonl"))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	defer f.Close()
	for _, r := range records {
		// Inject required schema fields if absent.
		if _, ok := r["schema_version"]; !ok {
			r["schema_version"] = "1"
		}
		if _, ok := r["timestamp"]; !ok {
			r["timestamp"] = "2026-05-07T00:00:00Z"
		}
		if _, ok := r["issue_number"]; !ok {
			r["issue_number"] = issueNumber
		}
		data, err := json.Marshal(r)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		if _, err := f.Write(append(data, '\n')); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
}

func TestFeatureValidateGate_Pass(t *testing.T) {
	ws := layouttest.Repo(t)
	writeGateMetrics(t, ws, 42, []map[string]any{
		{"gate_name": "build", "result": "pass"},
		{"gate_name": "lint", "result": "pass"},
		{"gate_name": "unit-tests", "result": "pass"},
	})

	gr := FeatureValidateGate{}.Verify(context.Background(), 42, ws)
	if !gr.Passed {
		t.Fatalf("expected pass; reason=%q evidence=%v", gr.Reason, gr.Evidence)
	}
}

func TestFeatureValidateGate_Fail_OneCatch(t *testing.T) {
	ws := layouttest.Repo(t)
	writeGateMetrics(t, ws, 42, []map[string]any{
		{"gate_name": "build", "result": "pass"},
		{"gate_name": "lint", "result": "catch"},
	})

	gr := FeatureValidateGate{}.Verify(context.Background(), 42, ws)
	if gr.Passed {
		t.Fatalf("expected fail when any gate is catch")
	}
	if gr.TerminalKind != TerminalKindValidationFailed {
		t.Errorf("TerminalKind = %q, want %q", gr.TerminalKind, TerminalKindValidationFailed)
	}
}

// TestFeatureValidateGate_SkillSaidSuccessButNoMetrics covers the canonical
// scenario: feature-validate exited 0 but emitted no quality-gate records,
// meaning it skipped the gates entirely.
func TestFeatureValidateGate_SkillSaidSuccessButNoMetrics(t *testing.T) {
	ws := layouttest.Repo(t)
	gr := FeatureValidateGate{}.Verify(context.Background(), 42, ws)
	if gr.Passed {
		t.Fatalf("expected fail when no gate-metrics records exist")
	}
	if gr.TerminalKind != "" {
		t.Errorf("TerminalKind = %q, want empty (KindNoOp falls back to prose)", gr.TerminalKind)
	}
}

// TestFeatureValidateGate_JudgeVerdict_Pass proves a passing adversarial judge
// verdict — written as the {gate_name:"judges", result:"pass"} record the TS
// the SDK appendJudgeVerdicts() emits (#3918) — flows through the
// EXISTING r.Result != "pass" loop without tripping the gate. This is the
// zero-Go-change contract: the judge gate is just another gate-metrics record.
func TestFeatureValidateGate_JudgeVerdict_Pass(t *testing.T) {
	ws := layouttest.Repo(t)
	writeGateMetrics(t, ws, 3918, []map[string]any{
		{"gate_name": "build", "result": "pass"},
		// The fields the gate reads from the record the TS writer appends for a
		// "pass" verdict.
		{"gate_name": "judges", "result": "pass", "attempt_id": "run-1"},
	})

	gr := FeatureValidateGate{}.Verify(context.Background(), 3918, ws)
	if !gr.Passed {
		t.Fatalf("expected pass when the judge verdict is pass; reason=%q evidence=%v", gr.Reason, gr.Evidence)
	}
	if gr.Kind != KindOK {
		t.Fatalf("expected KindOK on an all-pass run, got %q", gr.Kind)
	}
}

// The evidence counts collapsed verdicts and raw rows, and says which is which.
func TestFeatureValidateGate_EvidenceNamesVerdictsAndRows(t *testing.T) {
	ws := layouttest.Repo(t)
	writeGateMetrics(t, ws, 3918, []map[string]any{
		{"gate_name": "build", "result": "catch"},
		{"gate_name": "build", "result": "pass"},
		{"gate_name": "lint", "result": "pass"},
	})

	gr := FeatureValidateGate{}.Verify(context.Background(), 3918, ws)
	if !gr.Passed {
		t.Fatalf("expected pass; reason=%q evidence=%v", gr.Reason, gr.Evidence)
	}
	if !slices.Contains(gr.Evidence, "verdicts=2 rows=3") {
		t.Fatalf("expected evidence to contain %q, got %v", "verdicts=2 rows=3", gr.Evidence)
	}
}

// TestFeatureValidateGate_JudgeVerdict_FailTripsGate is the load-bearing
// anti-hallucination proof: a rejected judge verdict — written as
// {gate_name:"judges", result:"fail"} by the TS gateMetricsWriter (#3918) —
// trips FeatureValidateGate.Verify() through its existing r.Result != "pass"
// loop and yields KindFail, with NO new Go struct and NO LLM in internal/. A
// hallucinated "done" the judge rejects therefore fails the deterministic gate.
func TestFeatureValidateGate_JudgeVerdict_FailTripsGate(t *testing.T) {
	ws := layouttest.Repo(t)
	writeGateMetrics(t, ws, 3918, []map[string]any{
		{"gate_name": "build", "result": "pass"},
		{"gate_name": "lint", "result": "pass"},
		{"gate_name": "unit-tests", "result": "pass"},
		// The judge rejected a "done" claim — every deterministic gate passed,
		// yet this lone fail record must still trip the gate.
		{"gate_name": "judges", "result": "fail", "attempt_id": "run-1", "error_summary": "claimed tests pass but suite was never run"},
	})

	gr := FeatureValidateGate{}.Verify(context.Background(), 3918, ws)
	if gr.Passed {
		t.Fatalf("expected fail: a rejected judge verdict must trip the gate even when every build/lint/test gate passed")
	}
	if gr.Kind != KindFail {
		t.Fatalf("expected KindFail on a rejected judge verdict, got %q", gr.Kind)
	}
	// The failing judge record must surface in the evidence as judges=fail.
	var sawJudgeFail bool
	for _, e := range gr.Evidence {
		if e == "judges=fail" {
			sawJudgeFail = true
		}
	}
	if !sawJudgeFail {
		t.Fatalf("expected evidence to contain %q, got %v", "judges=fail", gr.Evidence)
	}
}

// interruptedMarker is the row the SDK appendJudgeVerdicts writes when a
// feature-validate run stopped before any judge verdict (#2880).
func interruptedMarker(attemptID string) map[string]any {
	r := map[string]any{
		"gate_name":     "judges",
		"result":        "fail",
		"error_summary": "[judges-interrupted] feature-validate workflow stopped before any judge verdict",
	}
	if attemptID != "" {
		r["attempt_id"] = attemptID
	}
	return r
}

// #2880: a run interrupted before any judge verdict is not "no judges". Its
// marker trips the gate although every deterministic gate passed.
func TestFeatureValidateGate_JudgeInterruptedMarker_TripsGate(t *testing.T) {
	for _, attempt := range []string{"run-1", ""} {
		ws := layouttest.Repo(t)
		writeGateMetrics(t, ws, 2880, []map[string]any{
			{"gate_name": "build", "result": "pass"},
			{"gate_name": "lint", "result": "pass"},
			interruptedMarker(attempt),
		})

		gr := FeatureValidateGate{}.Verify(context.Background(), 2880, ws)
		if gr.Passed || gr.Kind != KindFail {
			t.Fatalf("attempt %q: expected KindFail, got passed=%v kind=%q", attempt, gr.Passed, gr.Kind)
		}
		if !slices.Contains(gr.Evidence, "judges=fail") {
			t.Fatalf("attempt %q: expected evidence to contain judges=fail, got %v", attempt, gr.Evidence)
		}
	}
}

// #2880: a marker stamped with its run's id is an attempt, so a later complete
// attempt whose judges all passed supersedes it and a retry can clear it.
func TestFeatureValidateGate_JudgeInterruptedMarker_SupersededByLaterAttempt(t *testing.T) {
	ws := layouttest.Repo(t)
	writeGateMetrics(t, ws, 2880, []map[string]any{
		{"gate_name": "build", "result": "pass"},
		interruptedMarker("run-1"),
		{"gate_name": "judges", "result": "pass", "attempt_id": "run-2"},
		{"gate_name": "judges", "result": "pass", "attempt_id": "run-2"},
	})

	gr := FeatureValidateGate{}.Verify(context.Background(), 2880, ws)
	if !gr.Passed {
		t.Fatalf("expected the later complete attempt to supersede the marker; reason=%q evidence=%v", gr.Reason, gr.Evidence)
	}
}

// #2880: a partial run writes its judges without attempt_id, so its passes can
// never supersede an earlier failing attempt.
func TestFeatureValidateGate_PartialJudges_DoNotSupersedeFailingAttempt(t *testing.T) {
	ws := layouttest.Repo(t)
	writeGateMetrics(t, ws, 2880, []map[string]any{
		{"gate_name": "build", "result": "pass"},
		{"gate_name": "judges", "result": "fail", "attempt_id": "run-1", "error_summary": "claim not reproduced"},
		{"gate_name": "judges", "result": "pass"},
		{"gate_name": "judges", "result": "pass"},
	})

	gr := FeatureValidateGate{}.Verify(context.Background(), 2880, ws)
	if gr.Passed || gr.Kind != KindFail {
		t.Fatalf("expected KindFail: a partial run's passes must not clear run-1's fail; passed=%v kind=%q", gr.Passed, gr.Kind)
	}
}

// #2880: the gate releases an interrupted marker on the reader side once a
// later attempt, by any completion path, records a quality-gate verdict; it
// does not depend on that path writing a judges row.
func TestFeatureValidateGate_JudgeInterruptedMarker_ReleasedByLaterAttempt(t *testing.T) {
	none := func(attemptID string) map[string]any {
		return map[string]any{
			"gate_name":     "judges",
			"result":        "pass",
			"attempt_id":    attemptID,
			"error_summary": "[judges-none] feature-validate attempt completed with no judge verdict",
		}
	}
	retry := []map[string]any{
		{"gate_name": "build", "result": "pass"},
		{"gate_name": "lint", "result": "pass"},
	}
	cases := []struct {
		name  string
		after []map[string]any
		pass  bool
		fail  string
	}{
		{"single-agent retry", append(slices.Clone(retry), none("sa-2")), true, ""},
		{"orchestration disabled retry", retry, true, ""},
		{"judge-less spec retry", append(slices.Clone(retry), none("run-2")), true, ""},
		{"no later attempt", nil, false, "judges=fail"},
		{"only later attempt-less judges", []map[string]any{
			{"gate_name": "judges", "result": "pass"},
		}, false, "judges=fail"},
		{"later retry fails its own gate", []map[string]any{
			{"gate_name": "build", "result": "pass"},
			{"gate_name": "lint", "result": "fail"},
		}, false, "lint=fail"},
	}
	for _, tc := range cases {
		for _, attempt := range []string{"run-1", ""} {
			t.Run(tc.name+"/attempt="+attempt, func(t *testing.T) {
				ws := layouttest.Repo(t)
				rows := []map[string]any{
					{"gate_name": "build", "result": "pass"},
					{"gate_name": "lint", "result": "pass"},
					interruptedMarker(attempt),
				}
				writeGateMetrics(t, ws, 2880, append(rows, tc.after...))

				gr := FeatureValidateGate{}.Verify(context.Background(), 2880, ws)
				if tc.pass {
					if !gr.Passed {
						t.Fatalf("expected pass; reason=%q evidence=%v", gr.Reason, gr.Evidence)
					}
					return
				}
				if gr.Passed || gr.Kind != KindFail {
					t.Fatalf("expected KindFail, got passed=%v kind=%q", gr.Passed, gr.Kind)
				}
				if !slices.Contains(gr.Evidence, tc.fail) {
					t.Fatalf("expected evidence to contain %s, got %v", tc.fail, gr.Evidence)
				}
				if tc.fail != "judges=fail" && slices.Contains(gr.Evidence, "judges=fail") {
					t.Fatalf("released marker must not trip the gate; evidence=%v", gr.Evidence)
				}
			})
		}
	}
}

// #2880: the reader-side release applies only to the interrupted marker. An
// ordinary failing judges row still stands after a later quality-gate row.
func TestFeatureValidateGate_FailingJudge_NotReleasedByLaterGateRow(t *testing.T) {
	ws := layouttest.Repo(t)
	writeGateMetrics(t, ws, 2880, []map[string]any{
		{"gate_name": "judges", "result": "fail", "attempt_id": "run-1", "error_summary": "claim not reproduced"},
		{"gate_name": "build", "result": "pass"},
	})

	gr := FeatureValidateGate{}.Verify(context.Background(), 2880, ws)
	if gr.Passed || !slices.Contains(gr.Evidence, "judges=fail") {
		t.Fatalf("expected judges=fail to stand; passed=%v evidence=%v", gr.Passed, gr.Evidence)
	}
}

// The test marker's prefix must be the one the gate releases.
func TestInterruptedMarkerPrefixMatchesState(t *testing.T) {
	if !strings.HasPrefix(interruptedMarker("")["error_summary"].(string), state.JudgesInterruptedMarker) {
		t.Fatal("interruptedMarker prefix drifted from state.JudgesInterruptedMarker")
	}
}

// #2671: the #2661 validate context — five met verdicts under
// acceptance_criteria — fails the gate naming both keys; the correct
// {index, text, status, evidence} shape passes unchanged.
func TestFeatureValidateGate_MisplacedACVerdicts(t *testing.T) {
	metrics := []map[string]any{{"gate_name": "build", "result": "pass"}}
	var wrong []map[string]any
	for i := 1; i <= 5; i++ {
		wrong = append(wrong, map[string]any{"index": i, "verdict": "met"})
	}
	ws := layouttest.Repo(t)
	writeGateMetrics(t, ws, 2661, metrics)
	writeValidateArtifact(t, ws, 2661, map[string]any{"validation_status": "passed", "acceptance_criteria": wrong})
	gr := FeatureValidateGate{}.Verify(context.Background(), 2661, ws)
	if gr.Passed || gr.TerminalKind != TerminalKindValidationError {
		t.Fatalf("passed=%v kind=%q, want fail/%q", gr.Passed, gr.TerminalKind, TerminalKindValidationError)
	}
	joined := strings.Join(gr.Evidence, "\n")
	for _, want := range []string{"acceptance_criteria:", "acceptance_criteria_verdicts", "status"} {
		if !strings.Contains(joined, want) {
			t.Errorf("evidence missing %q: %s", want, joined)
		}
	}

	ok := layouttest.Repo(t)
	writeGateMetrics(t, ok, 2661, metrics)
	writeValidateArtifact(t, ok, 2661, map[string]any{"validation_status": "passed",
		"acceptance_criteria_verdicts": []map[string]any{{"index": 0, "text": "x", "status": "met", "evidence": "diff"}}})
	if gr := (FeatureValidateGate{}).Verify(context.Background(), 2661, ok); !gr.Passed {
		t.Fatalf("correct shape must pass; reason=%q evidence=%v", gr.Reason, gr.Evidence)
	}
}

// droppedMarker builds the failing judges row the SDK writes for a dropped
// judge attempt (#2944).
func droppedMarker(attemptID string) map[string]any {
	r := map[string]any{
		"gate_name":     "judges",
		"result":        "fail",
		"error_summary": "[judges-dropped] feature-validate workflow declared 2 judge(s) but recorded no verdict",
	}
	if attemptID != "" {
		r["attempt_id"] = attemptID
	}
	return r
}

// #2944: a dropped judge attempt fails with a reason naming the drop, not the
// generic quality-gate failure and never "no judges".
func TestFeatureValidateGate_JudgeDroppedMarker_FailsWithDroppedReason(t *testing.T) {
	for _, attempt := range []string{"run-1", ""} {
		ws := layouttest.Repo(t)
		writeGateMetrics(t, ws, 2944, []map[string]any{
			{"gate_name": "build", "result": "pass"},
			droppedMarker(attempt),
		})

		gr := FeatureValidateGate{}.Verify(context.Background(), 2944, ws)
		if gr.Passed || gr.Kind != KindFail {
			t.Fatalf("attempt %q: expected KindFail, got passed=%v kind=%q", attempt, gr.Passed, gr.Kind)
		}
		if !strings.Contains(gr.Reason, "a judge attempt was dropped") {
			t.Fatalf("attempt %q: expected the dropped reason, got %q", attempt, gr.Reason)
		}
		if !slices.Contains(gr.Evidence, "judges=fail") {
			t.Fatalf("attempt %q: expected judges=fail evidence, got %v", attempt, gr.Evidence)
		}
	}
}

// #2944: a later attempt's quality-gate row releases a dropped marker, as it
// releases an interrupted one.
func TestFeatureValidateGate_JudgeDroppedMarker_ReleasedByLaterGateRow(t *testing.T) {
	ws := layouttest.Repo(t)
	writeGateMetrics(t, ws, 2944, []map[string]any{
		droppedMarker("run-1"),
		{"gate_name": "adversarial-review", "result": "pass"},
	})

	gr := FeatureValidateGate{}.Verify(context.Background(), 2944, ws)
	if !gr.Passed {
		t.Fatalf("expected a later gate row to release the marker; reason=%q evidence=%v", gr.Reason, gr.Evidence)
	}
}

// #2944: a run with no judges rows at all still passes unchanged.
func TestFeatureValidateGate_NoJudgeRows_StillPasses(t *testing.T) {
	ws := layouttest.Repo(t)
	writeGateMetrics(t, ws, 2944, []map[string]any{{"gate_name": "build", "result": "pass"}})

	gr := FeatureValidateGate{}.Verify(context.Background(), 2944, ws)
	if !gr.Passed {
		t.Fatalf("expected pass with no judges rows; reason=%q", gr.Reason)
	}
}

// #2944: droppedMarker's prefix must match the Go constant the gate keys on.
func TestDroppedMarkerHelperMatchesState(t *testing.T) {
	if !strings.HasPrefix(droppedMarker("")["error_summary"].(string), state.JudgesDroppedMarker) {
		t.Fatal("droppedMarker prefix drifted from state.JudgesDroppedMarker")
	}
}
