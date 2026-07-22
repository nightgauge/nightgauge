package gates

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// writeGateMetrics writes JSONL records to .nightgauge/health/gate-metrics.jsonl.
// Mirrors the format ReadGateMetricsForIssue parses.
func writeGateMetrics(t *testing.T, workspace string, issueNumber int, records []map[string]any) {
	t.Helper()
	dir := filepath.Join(workspace, ".nightgauge", "health")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
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
	ws := t.TempDir()
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
	ws := t.TempDir()
	writeGateMetrics(t, ws, 42, []map[string]any{
		{"gate_name": "build", "result": "pass"},
		{"gate_name": "lint", "result": "catch"},
	})

	gr := FeatureValidateGate{}.Verify(context.Background(), 42, ws)
	if gr.Passed {
		t.Fatalf("expected fail when any gate is catch")
	}
}

// TestFeatureValidateGate_SkillSaidSuccessButNoMetrics covers the canonical
// scenario: feature-validate exited 0 but emitted no quality-gate records,
// meaning it skipped the gates entirely.
func TestFeatureValidateGate_SkillSaidSuccessButNoMetrics(t *testing.T) {
	ws := t.TempDir()
	gr := FeatureValidateGate{}.Verify(context.Background(), 42, ws)
	if gr.Passed {
		t.Fatalf("expected fail when no gate-metrics records exist")
	}
}

// TestFeatureValidateGate_JudgeVerdict_Pass proves a passing adversarial judge
// verdict — written as the {gate_name:"judges", result:"pass"} record the TS
// gateMetricsWriter.appendJudgeVerdicts() emits (#3918) — flows through the
// EXISTING r.Result != "pass" loop without tripping the gate. This is the
// zero-Go-change contract: the judge gate is just another gate-metrics record.
func TestFeatureValidateGate_JudgeVerdict_Pass(t *testing.T) {
	ws := t.TempDir()
	writeGateMetrics(t, ws, 3918, []map[string]any{
		{"gate_name": "build", "result": "pass"},
		// Byte-for-byte the record the TS writer appends for a "pass" verdict.
		{"gate_name": "judges", "result": "pass"},
	})

	gr := FeatureValidateGate{}.Verify(context.Background(), 3918, ws)
	if !gr.Passed {
		t.Fatalf("expected pass when the judge verdict is pass; reason=%q evidence=%v", gr.Reason, gr.Evidence)
	}
	if gr.Kind != KindOK {
		t.Fatalf("expected KindOK on an all-pass run, got %q", gr.Kind)
	}
}

// TestFeatureValidateGate_JudgeVerdict_FailTripsGate is the load-bearing
// anti-hallucination proof: a rejected judge verdict — written as
// {gate_name:"judges", result:"fail"} by the TS gateMetricsWriter (#3918) —
// trips FeatureValidateGate.Verify() through its existing r.Result != "pass"
// loop and yields KindFail, with NO new Go struct and NO LLM in internal/. A
// hallucinated "done" the judge rejects therefore fails the deterministic gate.
func TestFeatureValidateGate_JudgeVerdict_FailTripsGate(t *testing.T) {
	ws := t.TempDir()
	writeGateMetrics(t, ws, 3918, []map[string]any{
		{"gate_name": "build", "result": "pass"},
		{"gate_name": "lint", "result": "pass"},
		{"gate_name": "unit-tests", "result": "pass"},
		// The judge rejected a "done" claim — every deterministic gate passed,
		// yet this lone fail record must still trip the gate.
		{"gate_name": "judges", "result": "fail", "error_summary": "claimed tests pass but suite was never run"},
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
