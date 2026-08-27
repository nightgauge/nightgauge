package doctor

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/cadence"
)

var testNow = time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)

// fixedProbes answers every automation the same way, so a test can drive the
// arm's reporting without a GitHub client or a state file.
func fixedProbes(e cadence.Evidence) map[cadence.EvidenceKind]cadenceProbe {
	p := func(context.Context, cadence.Automation) cadence.Evidence { return e }
	return map[cadence.EvidenceKind]cadenceProbe{
		cadence.EvidenceAutonomousState: p,
		cadence.EvidenceWorkflowRun:     p,
	}
}

// TestScheduledAutomations_ReportsStale is acceptance criterion 5: an entry
// whose evidence is beyond the threshold must be reported.
func TestScheduledAutomations_ReportsStale(t *testing.T) {
	item, warning := checkScheduledAutomations(context.Background(),
		fixedProbes(cadence.Evidence{EverRan: true, Newest: testNow.AddDate(0, 0, -400)}), nil, testNow)

	if item.OK {
		t.Error("every automation 400 days stale reported OK")
	}
	if !strings.Contains(warning, "scheduled-automation-stale") {
		t.Errorf("warning lacks the stable identifier: %q", warning)
	}
	if !strings.Contains(warning, "STOPPED") {
		t.Errorf("warning does not classify the verdict: %q", warning)
	}
}

// TestScheduledAutomations_FreshIsNotAFinding is the other half of criterion 5:
// move the timestamp inside the threshold and the arm must go quiet.
func TestScheduledAutomations_FreshIsNotAFinding(t *testing.T) {
	// Inside 3x the shortest registered interval (1h) — recent enough for all.
	item, warning := checkScheduledAutomations(context.Background(),
		fixedProbes(cadence.Evidence{EverRan: true, Newest: testNow.Add(-1 * time.Minute)}), nil, testNow)

	if !item.OK {
		t.Errorf("all-fresh automations reported a finding: %s", item.Error)
	}
	if warning != "" {
		t.Errorf("unexpected warning: %q", warning)
	}
}

// TestScheduledAutomations_SeparatesNeverRanFromStopped guards criterion 4 at
// the REPORTING layer, not just in Evaluate. The distinction is worthless if
// the arm flattens it back into one bucket on the way out.
func TestScheduledAutomations_SeparatesNeverRanFromStopped(t *testing.T) {
	probes := map[cadence.EvidenceKind]cadenceProbe{
		// The loop stopped 22 days ago — the real observed state.
		cadence.EvidenceAutonomousState: func(context.Context, cadence.Automation) cadence.Evidence {
			return cadence.Evidence{EverRan: true, Newest: testNow.AddDate(0, 0, -22)}
		},
		// The workflows have never fired at all — also the real observed state.
		cadence.EvidenceWorkflowRun: func(context.Context, cadence.Automation) cadence.Evidence {
			return cadence.Evidence{EverRan: false}
		},
	}

	item, warning := checkScheduledAutomations(context.Background(), probes, nil, testNow)
	if item.OK {
		t.Fatal("a stopped loop and three never-run workflows reported OK")
	}
	if !strings.Contains(warning, "NEVER RAN") {
		t.Errorf("warning does not name the never-ran class: %q", warning)
	}
	if !strings.Contains(warning, "STOPPED") {
		t.Errorf("warning does not name the stopped class: %q", warning)
	}
	if !strings.Contains(warning, "autonomous-loop") {
		t.Errorf("warning does not name the stopped automation: %q", warning)
	}
	// The two classes must not be merged into one list.
	never := strings.Index(warning, "NEVER RAN")
	stopped := strings.Index(warning, "STOPPED")
	if never > stopped {
		t.Error("NEVER RAN should be reported before STOPPED — a schedule that was never " +
			"valid is a different fix from one that died")
	}
}

// TestScheduledAutomations_ProbeErrorIsNotHealthy guards the fail-closed
// direction at the arm level.
func TestScheduledAutomations_ProbeErrorIsNotHealthy(t *testing.T) {
	item, warning := checkScheduledAutomations(context.Background(),
		fixedProbes(cadence.Evidence{Err: errors.New("api unreachable")}), nil, testNow)

	if item.OK {
		t.Error("automations whose freshness could not be determined reported OK — " +
			"'I could not look' must never render as 'it is fine'")
	}
	if !strings.Contains(warning, "UNVERIFIABLE") {
		t.Errorf("warning does not name the unverifiable class: %q", warning)
	}
}

// TestScheduledAutomations_MissingProbeIsUnverifiable guards the case where the
// registry gains an evidence kind nobody wired a probe for. Silently skipping
// it would mean adding an automation makes the check WEAKER.
func TestScheduledAutomations_MissingProbeIsUnverifiable(t *testing.T) {
	item, warning := checkScheduledAutomations(context.Background(),
		map[cadence.EvidenceKind]cadenceProbe{}, nil, testNow)

	if item.OK {
		t.Error("no probes registered and the arm still reported OK")
	}
	if !strings.Contains(warning, "UNVERIFIABLE") {
		t.Errorf("an unprobed evidence kind should be unverifiable: %q", warning)
	}
}

// --- the autonomous-state probe, against the real file shape ---

func TestAutonomousStateEvidence_ReadsLastScanAt(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, ".nightgauge", "autonomous")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]string{
		"status":     "stopped",
		"lastScanAt": "2026-08-05T11:32:33Z",
	})
	if err := os.WriteFile(filepath.Join(dir, "state.json"), body, 0o644); err != nil {
		t.Fatal(err)
	}

	got := autonomousStateEvidence(root)(context.Background(), cadence.Automation{})
	if got.Err != nil {
		t.Fatalf("probe errored: %v", got.Err)
	}
	if !got.EverRan {
		t.Error("a state file with a lastScanAt should count as having run")
	}
	want := time.Date(2026, 8, 5, 11, 32, 33, 0, time.UTC)
	if !got.Newest.Equal(want) {
		t.Errorf("Newest = %v, want %v", got.Newest, want)
	}

	// And it must actually be reported stale against the real registry entry.
	a, ok := cadence.ByID("autonomous-loop")
	if !ok {
		t.Fatal("autonomous-loop is not registered")
	}
	v := cadence.Evaluate(a, got, testNow, cadence.DefaultStaleMultiple)
	if v.Status != cadence.StatusStale {
		t.Errorf("a loop last scanned 2026-08-05, evaluated at 2026-08-27, is %q — want stale",
			v.Status)
	}
}

func TestAutonomousStateEvidence_MissingFileIsNeverRan(t *testing.T) {
	got := autonomousStateEvidence(t.TempDir())(context.Background(), cadence.Automation{})
	if got.Err != nil {
		t.Errorf("a missing state file is not an error: %v", got.Err)
	}
	if got.EverRan {
		t.Error("a workspace with no autonomous state has never run the loop")
	}
}

func TestAutonomousStateEvidence_UnparseableTimestampIsAnError(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, ".nightgauge", "autonomous")
	_ = os.MkdirAll(dir, 0o755)
	_ = os.WriteFile(filepath.Join(dir, "state.json"),
		[]byte(`{"lastScanAt":"not-a-time"}`), 0o644)

	got := autonomousStateEvidence(root)(context.Background(), cadence.Automation{})
	if got.Err == nil {
		t.Error("an unparseable lastScanAt must be an error, not silently 'never ran' — " +
			"the difference decides whether the operator looks at the clock or the loop")
	}
}

func TestWorkflowRunEvidence_NoClientIsAnError(t *testing.T) {
	got := workflowRunEvidence(nil, "o", "r")(context.Background(),
		cadence.Automation{ID: "x", Workflow: "ci.yml"})
	if got.Err == nil {
		t.Error("no GitHub client must be an error, not a healthy verdict")
	}
}
