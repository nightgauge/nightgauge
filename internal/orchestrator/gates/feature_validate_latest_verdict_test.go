package gates

import (
	"context"
	"os"
	"reflect"
	"testing"

	"github.com/nightgauge/nightgauge/internal/layout"
	"github.com/nightgauge/nightgauge/internal/layout/layouttest"
	"github.com/nightgauge/nightgauge/internal/state"
)

// TestFeatureValidateGate_LatestVerdict pins the #2746 rule: the last verdict
// per gate_name, in file order, decides.
func TestFeatureValidateGate_LatestVerdict(t *testing.T) {
	const gate = "adversarial-review"
	tests := []struct {
		name     string
		records  []map[string]any
		wantPass bool
		wantKind Kind
		evidence []string
	}{
		{
			name: "catch then pass passes",
			records: []map[string]any{
				{"gate_name": gate, "result": "catch"},
				{"gate_name": gate, "result": "pass"},
			},
			wantPass: true, wantKind: KindOK,
		},
		{
			name: "pass then catch fails",
			records: []map[string]any{
				{"gate_name": gate, "result": "pass"},
				{"gate_name": gate, "result": "catch"},
			},
			wantKind: KindFail, evidence: []string{gate + "=catch"},
		},
		{
			name: "a later judge's pass does not erase an earlier judge's fail",
			records: []map[string]any{
				{"gate_name": "judges", "result": "fail", "attempt_id": "a"},
				{"gate_name": "judges", "result": "pass", "attempt_id": "a"},
			},
			wantKind: KindFail, evidence: []string{"judges=fail"},
		},
		{
			name: "any failing judge in the latest attempt fails",
			records: []map[string]any{
				{"gate_name": "judges", "result": "fail", "attempt_id": "a"},
				{"gate_name": "judges", "result": "pass", "attempt_id": "b"},
				{"gate_name": "judges", "result": "fail", "attempt_id": "b"},
				{"gate_name": "judges", "result": "pass", "attempt_id": "b"},
			},
			wantKind: KindFail, evidence: []string{"judges=fail"},
		},
		{
			name: "a later attempt with every judge passing clears an earlier fail",
			records: []map[string]any{
				{"gate_name": "judges", "result": "fail", "attempt_id": "a"},
				{"gate_name": "judges", "result": "pass", "attempt_id": "b"},
				{"gate_name": "judges", "result": "pass", "attempt_id": "b"},
			},
			wantPass: true, wantKind: KindOK,
		},
		{
			name: "judge rows with no attempt stay fail-closed",
			records: []map[string]any{
				{"gate_name": "judges", "result": "fail"},
				{"gate_name": "judges", "result": "pass"},
			},
			wantKind: KindFail, evidence: []string{"judges=fail"},
		},
		{
			name: "an attempt does not clear a judge fail with no attempt",
			records: []map[string]any{
				{"gate_name": "judges", "result": "fail"},
				{"gate_name": "judges", "result": "pass", "attempt_id": "b"},
			},
			wantKind: KindFail, evidence: []string{"judges=fail"},
		},
		{
			name: "a pass with no attempt does not clear an attempt's fail",
			records: []map[string]any{
				{"gate_name": "judges", "result": "fail", "attempt_id": "a"},
				{"gate_name": "judges", "result": "pass"},
			},
			wantKind: KindFail, evidence: []string{"judges=fail"},
		},
		{
			name: "an empty judge result does not end the latest attempt",
			records: []map[string]any{
				{"gate_name": "judges", "result": "fail", "attempt_id": "a"},
				{"gate_name": "judges", "attempt_id": "b"},
			},
			wantKind: KindFail, evidence: []string{"judges=fail"},
		},
		{
			name: "gate names are judged independently",
			records: []map[string]any{
				{"gate_name": gate, "result": "catch"},
				{"gate_name": "build", "result": "pass"},
			},
			wantKind: KindFail, evidence: []string{gate + "=catch"},
		},
		{
			name: "pass for one gate does not clear another",
			records: []map[string]any{
				{"gate_name": gate, "result": "catch"},
				{"gate_name": "verify-ui", "result": "catch"},
				{"gate_name": gate, "result": "pass"},
			},
			wantKind: KindFail, evidence: []string{"verify-ui=catch"},
		},
		{
			name: "empty result does not supersede a catch",
			records: []map[string]any{
				{"gate_name": gate, "result": "catch"},
				{"gate_name": gate, "result": ""},
			},
			wantKind: KindFail, evidence: []string{gate + "=catch"},
		},
		{
			name: "empty result does not supersede a pass",
			records: []map[string]any{
				{"gate_name": gate, "result": "pass"},
				{"gate_name": gate},
			},
			wantPass: true, wantKind: KindOK,
		},
		{
			name: "nameless pass does not clear nameless catch",
			records: []map[string]any{
				{"result": "catch"},
				{"result": "pass"},
			},
			wantKind: KindFail, evidence: []string{"=catch"},
		},
		{
			name:     "only empty-result rows fails rather than reading as a no-op",
			records:  []map[string]any{{"gate_name": gate}},
			wantKind: KindFail, evidence: []string{"1 gate-metrics row(s) carry no result"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ws := layouttest.Repo(t)
			writeGateMetrics(t, ws, 42, tc.records)

			gr := FeatureValidateGate{}.Verify(context.Background(), 42, ws)
			if gr.Passed != tc.wantPass || gr.Kind != tc.wantKind {
				t.Fatalf("Passed=%v Kind=%q, want %v %q; reason=%q evidence=%v",
					gr.Passed, gr.Kind, tc.wantPass, tc.wantKind, gr.Reason, gr.Evidence)
			}
			if tc.evidence != nil && !reflect.DeepEqual(gr.Evidence, tc.evidence) {
				t.Fatalf("evidence = %v, want %v", gr.Evidence, tc.evidence)
			}
		})
	}
}

// An unparsable later line supersedes nothing, in either direction.
func TestFeatureValidateGate_MalformedLaterLine(t *testing.T) {
	for _, tc := range []struct {
		first    string
		wantPass bool
	}{{"catch", false}, {"pass", true}} {
		t.Run(tc.first, func(t *testing.T) {
			ws := layouttest.Repo(t)
			writeGateMetrics(t, ws, 42, []map[string]any{
				{"gate_name": "adversarial-review", "result": tc.first},
			})
			path, err := layout.CheckoutPath(ws, state.GateMetricsName)
			if err != nil {
				t.Fatalf("path: %v", err)
			}
			f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
			if err != nil {
				t.Fatalf("open: %v", err)
			}
			if _, err := f.WriteString(`{"gate_name":"adversarial-review","result":"pass"` + "\n"); err != nil {
				t.Fatalf("write: %v", err)
			}
			f.Close()

			gr := FeatureValidateGate{}.Verify(context.Background(), 42, ws)
			if gr.Passed != tc.wantPass {
				t.Fatalf("Passed=%v, want %v; evidence=%v", gr.Passed, tc.wantPass, gr.Evidence)
			}
		})
	}
}

// The retry case end to end: a worktree holding an earlier attempt's catch
// fails, then passes in place once the new attempt records pass.
func TestFeatureValidateGate_RetryClearsEarlierCatch(t *testing.T) {
	ws := layouttest.Repo(t)
	if err := state.AppendGateMetric(ws, 42, "adversarial-review", "catch", "x", "2026-10-31T00:00:00Z"); err != nil {
		t.Fatalf("append catch: %v", err)
	}
	if gr := (FeatureValidateGate{}).Verify(context.Background(), 42, ws); gr.Passed {
		t.Fatal("first attempt: gate passed on a catch")
	}

	if err := state.AppendGateMetric(ws, 42, "adversarial-review", "pass", "", "2026-10-31T00:01:00Z"); err != nil {
		t.Fatalf("append pass: %v", err)
	}
	gr := FeatureValidateGate{}.Verify(context.Background(), 42, ws)
	if !gr.Passed {
		t.Fatalf("retry: gate failed after pass; reason=%q evidence=%v", gr.Reason, gr.Evidence)
	}
}
