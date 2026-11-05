package hooks

import (
	"encoding/json"
	"strings"
	"testing"
)

func gateInput(t *testing.T, tool string, ti map[string]any) GateInput {
	t.Helper()
	raw, err := json.Marshal(ti)
	if err != nil {
		t.Fatal(err)
	}
	return GateInput{ToolName: tool, ToolInput: raw}
}

func TestGateBackground(t *testing.T) {
	const limit = 1800
	for _, tc := range []struct {
		name     string
		tool     string
		input    map[string]any
		limit    int
		declared []string
		block    bool
	}{
		{"foreground ci-local denied", "Bash", map[string]any{"command": "bash scripts/ci-local.sh"}, limit, nil, true},
		{"foreground gate-run denied", "Bash", map[string]any{"command": "nightgauge ci gate-run"}, limit, nil, true},
		{"foreground pre-push via $BINARY denied", "Bash", map[string]any{"command": `"$BINARY" pre-push validate 7 --target main`}, limit, nil, true},
		{"background short timeout denied", "Bash", map[string]any{"command": "bash scripts/ci-local.sh", "run_in_background": true, "timeout": 600000}, limit, nil, true},
		{"background adequate timeout allowed", "Bash", map[string]any{"command": "bash scripts/ci-local.sh 2>&1 | tee /tmp/g.log", "run_in_background": true, "timeout": 1800000}, limit, nil, false},
		{"unrelated command allowed", "Bash", map[string]any{"command": "go test ./internal/hooks/"}, limit, nil, false},
		{"ampersand in foreground denied", "Bash", map[string]any{"command": "nohup bash scripts/ci-local.sh > /tmp/g.log 2>&1 &"}, limit, nil, true},
		{"ampersand even with background flag denied", "Bash", map[string]any{"command": "./scripts/ci-local.sh &", "run_in_background": true, "timeout": 1800000}, limit, nil, true},
		{"configured limit raises the bar", "Bash", map[string]any{"command": "nightgauge ci gate-run", "run_in_background": true, "timeout": 1800000}, 3600, nil, true},
		{"no limit needs the ceiling", "Bash", map[string]any{"command": "nightgauge ci gate-run", "run_in_background": true, "timeout": 7200000}, 0, nil, false},
		{"declared gate denied", "Bash", map[string]any{"command": "make verify"}, limit, []string{"make", "verify"}, true},
		{"cat of the script allowed", "Bash", map[string]any{"command": "cat scripts/ci-local.sh"}, limit, nil, false},
		{"grep for the script allowed", "Bash", map[string]any{"command": "grep -n ci-local.sh scripts/ci-local.sh"}, limit, nil, false},
		{"quoted mention allowed", "Bash", map[string]any{"command": `echo "run bash scripts/ci-local.sh &"`}, limit, nil, false},
		{"non-Bash tool allowed", "Read", map[string]any{"file_path": "scripts/ci-local.sh"}, limit, nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := evaluateGateBackground(gateInput(t, tc.tool, tc.input), tc.limit, tc.declared)
			if (got.Decision == "block") != tc.block {
				t.Fatalf("decision = %+v, want block=%v", got, tc.block)
			}
			if tc.block && !strings.Contains(got.Reason, "run_in_background: true") {
				t.Errorf("deny reason does not say how to start the gate: %s", got.Reason)
			}
		})
	}
}

func TestGateBackground_BadJSONAllowed(t *testing.T) {
	if d := EvaluateGateBackground([]byte("{not json")); d.Decision != "allow" {
		t.Fatalf("bad JSON must fail open, got %+v", d)
	}
}
