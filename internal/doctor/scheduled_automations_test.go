package doctor

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/nightgauge/nightgauge/internal/config"
	"github.com/nightgauge/nightgauge/internal/layout"
	"github.com/nightgauge/nightgauge/internal/layout/layouttest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/cadence"
)

// coreScope is the core repo with autonomous mode in use: every built-in applies.
var coreScope = cadence.Scope{Repo: cadence.CoreRepo, Autonomous: true}

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
	fs, _ := scheduledAutomationFindings(context.Background(),
		fixedProbes(cadence.Evidence{EverRan: true, Newest: testNow.AddDate(0, 0, -400)}), coreScope, nil, nil, nil, testNow)
	warning := findingsText(fs)

	if len(fs) == 0 {
		t.Error("every automation 400 days stale reported OK")
	}
	if !strings.Contains(warning, codeAutomationStopped) {
		t.Errorf("warning lacks the stable identifier: %q", warning)
	}
	if !strings.Contains(warning, codeAutomationStopped) {
		t.Errorf("warning does not classify the verdict: %q", warning)
	}
}

// TestScheduledAutomations_FreshIsNotAFinding is the other half of criterion 5:
// move the timestamp inside the threshold and the arm must go quiet.
func TestScheduledAutomations_FreshIsNotAFinding(t *testing.T) {
	// Inside 3x the shortest registered interval (1h) — recent enough for all.
	fs, _ := scheduledAutomationFindings(context.Background(),
		fixedProbes(cadence.Evidence{EverRan: true, Newest: testNow.Add(-1 * time.Minute)}), coreScope, nil, nil, nil, testNow)
	warning := findingsText(fs)

	if len(fs) != 0 {
		t.Errorf("all-fresh automations reported a finding: %s", warning)
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

	fs, _ := scheduledAutomationFindings(context.Background(), probes, coreScope, nil, nil, nil, testNow)
	warning := findingsText(fs)
	if len(fs) == 0 {
		t.Fatal("a stopped loop and three never-run workflows reported OK")
	}
	if !strings.Contains(warning, codeAutomationNeverRan) {
		t.Errorf("warning does not name the never-ran class: %q", warning)
	}
	if !strings.Contains(warning, codeAutomationStopped) {
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
	fs, _ := scheduledAutomationFindings(context.Background(),
		fixedProbes(cadence.Evidence{Err: errors.New("api unreachable")}), coreScope, nil, nil, nil, testNow)
	warning := findingsText(fs)

	if len(fs) == 0 {
		t.Error("automations whose freshness could not be determined reported OK — " +
			"'I could not look' must never render as 'it is fine'")
	}
	if !strings.Contains(warning, codeAutomationUnverifiable) {
		t.Errorf("warning does not name the unverifiable class: %q", warning)
	}
}

// TestScheduledAutomations_MissingProbeIsUnverifiable guards the case where the
// registry gains an evidence kind nobody wired a probe for. Silently skipping
// it would mean adding an automation makes the check WEAKER.
func TestScheduledAutomations_MissingProbeIsUnverifiable(t *testing.T) {
	fs, _ := scheduledAutomationFindings(context.Background(),
		map[cadence.EvidenceKind]cadenceProbe{}, coreScope, nil, nil, nil, testNow)
	warning := findingsText(fs)

	if len(fs) == 0 {
		t.Error("no probes registered and the arm still reported OK")
	}
	if !strings.Contains(warning, codeAutomationUnverifiable) {
		t.Errorf("an unprobed evidence kind should be unverifiable: %q", warning)
	}
}

// --- the autonomous-state probe, against the real file shape ---

func TestAutonomousStateEvidence_ReadsLastScanAt(t *testing.T) {
	root := layouttest.Repo(t)
	dir := layouttest.MkCheckoutSubdir(t, root, "autonomous")
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
	got := autonomousStateEvidence(layouttest.Repo(t))(context.Background(), cadence.Automation{})
	if got.Err != nil {
		t.Errorf("a missing state file is not an error: %v", got.Err)
	}
	if got.EverRan {
		t.Error("a workspace with no autonomous state has never run the loop")
	}
}

func TestAutonomousStateEvidence_UnparseableTimestampIsAnError(t *testing.T) {
	root := layouttest.Repo(t)
	dir := layouttest.MkCheckoutSubdir(t, root, "autonomous")
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

// TestScheduledAutomations_ConsumerRepoHasNoBuiltins is #2199: a consumer repo
// with no automations.cadence and autonomous mode off must not inherit core's
// own release workflow or autonomous loop.
func TestScheduledAutomations_ConsumerRepoHasNoBuiltins(t *testing.T) {
	fs, detail := scheduledAutomationFindings(context.Background(),
		fixedProbes(cadence.Evidence{EverRan: false}), cadence.Scope{Repo: "acme/tiny"}, nil, nil, nil, testNow)
	warning := findingsText(fs)
	if len(fs) != 0 {
		t.Fatalf("consumer repo reported a finding: findings=%+v warning=%q", fs, warning)
	}
	if !strings.Contains(detail, "none registered") {
		t.Errorf("detail = %q, want \"none registered\"", detail)
	}
}

func TestCadenceScope_AutonomousFromStateFile(t *testing.T) {
	root := layouttest.Repo(t)
	if got := cadenceScope(nil, root); got.Autonomous || got.Repo != "" {
		t.Fatalf("empty workspace scope = %+v", got)
	}
	dir := layouttest.MkCheckoutSubdir(t, root, "autonomous")
	if err := os.WriteFile(filepath.Join(dir, "state.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !cadenceScope(nil, root).Autonomous {
		t.Error("a workspace whose autonomous loop has run must keep autonomous-loop in scope")
	}
}

// TestCadenceScope_AutonomousHonoursEnabledRepos is #2218: a machine-wide
// autonomous block limited to other repos must not put autonomous-loop in
// scope for this one.
func TestCadenceScope_AutonomousHonoursEnabledRepos(t *testing.T) {
	tests := []struct {
		name    string
		enabled []string
		want    bool
	}{
		{"other repo only", []string{"nightgauge"}, false},
		{"empty list means every repo", nil, true},
		{"short name expanded against owner", []string{"tiny"}, true},
		{"qualified name, case-insensitive", []string{"ACME/Tiny"}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &config.Config{Owner: "acme", DefaultRepo: "tiny",
				Autonomous: &config.AutonomousConfig{EnabledRepos: tt.enabled}}
			if got := cadenceScope(cfg, t.TempDir()).Autonomous; got != tt.want {
				t.Errorf("Autonomous = %v, want %v", got, tt.want)
			}
		})
	}
}

// byIDProbes answers each automation by its ID.
func byIDProbes(ev map[string]cadence.Evidence) map[cadence.EvidenceKind]cadenceProbe {
	p := func(_ context.Context, a cadence.Automation) cadence.Evidence { return ev[a.ID] }
	return map[cadence.EvidenceKind]cadenceProbe{
		cadence.EvidenceAutonomousState: p,
		cadence.EvidenceWorkflowRun:     p,
	}
}

// TestAutomationFindings_ThreeDistinctCodes: never ran, stopped 17d ago and
// unverifiable are three codes with three causes.
func TestAutomationFindings_ThreeDistinctCodes(t *testing.T) {
	probes := byIDProbes(map[string]cadence.Evidence{
		"autonomous-loop":  {EverRan: true, Newest: testNow.AddDate(0, 0, -17)},
		"release-workflow": {EverRan: false},
		"nightly":          {Err: errors.New("api unreachable")},
	})
	declared := []cadence.ConfigAutomation{{ID: "nightly", Interval: "24h", Workflow: "nightly.yml"}}
	fs, _ := scheduledAutomationFindings(context.Background(), probes, coreScope, declared, nil, func() error { return nil }, testNow)

	got := map[string]Finding{}
	for _, f := range fs {
		got[f.Evidence["automation"]] = f
	}
	cases := map[string]string{
		"release-workflow": codeAutomationNeverRan,
		"autonomous-loop":  codeAutomationStopped,
		"nightly":          codeAutomationUnverifiable,
	}
	causes := map[string]bool{}
	for id, code := range cases {
		f, ok := got[id]
		if !ok {
			t.Fatalf("no finding for %s: %s", id, findingsText(fs))
		}
		if f.Code != code || f.Severity != SeverityWarning {
			t.Errorf("%s: %s/%s, want %s warning", id, f.Code, f.Severity, code)
		}
		causes[f.Cause] = true
	}
	if len(causes) != 3 {
		t.Errorf("the three conditions share a cause: %v", causes)
	}
	stopped := got["autonomous-loop"]
	if stopped.Evidence["last_ran"] == "" || stopped.Evidence["expected_interval"] == "" {
		t.Errorf("stopped evidence lacks last-run or cadence: %v", stopped.Evidence)
	}
	kinds := map[RemedyKind]bool{}
	for _, r := range stopped.Remedies {
		kinds[r.Kind] = true
		if r.Kind == RemedyConfirm && r.Verb != verbAutomationRestart {
			t.Errorf("restart remedy verb %q", r.Verb)
		}
	}
	if !kinds[RemedyConfirm] || !kinds[RemedyManual] {
		t.Errorf("stopped automation remedies = %+v, want confirm restart and manual pause", stopped.Remedies)
	}
}

// TestAutomationFindings_PauseTurnsStoppedIntoInfo: a recorded pause makes the
// stopped finding info (still reported), and removing it restores the warning.
func TestAutomationFindings_PauseTurnsStoppedIntoInfo(t *testing.T) {
	root := layouttest.Repo(t)
	probes := byIDProbes(map[string]cadence.Evidence{
		"autonomous-loop": {EverRan: true, Newest: testNow.AddDate(0, 0, -17)},
	})
	scope := cadence.Scope{Autonomous: true}
	run := func() Finding {
		t.Helper()
		pauses, err := LoadAutomationPauses(root)
		if err != nil {
			t.Fatalf("load pauses: %v", err)
		}
		fs, _ := scheduledAutomationFindings(context.Background(), probes, scope, nil, pauses, nil, testNow)
		if len(fs) != 1 {
			t.Fatalf("want one finding, got %s", findingsText(fs))
		}
		return fs[0]
	}

	before := run()
	if before.Severity != SeverityWarning {
		t.Fatalf("unpaused stopped automation is %s, want warning", before.Severity)
	}
	if err := PauseAutomation(root, "autonomous-loop", "maintenance", testNow); err != nil {
		t.Fatalf("pause: %v", err)
	}
	if info, err := os.Stat(layouttest.CheckoutPath(t, root, layout.CheckoutAutomationPauses)); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("pause record missing or not 0600: %v", err)
	}
	paused := run()
	if paused.Severity != SeverityInfo || paused.Code != codeAutomationStopped {
		t.Errorf("paused finding = %s/%s, want info %s", paused.Code, paused.Severity, codeAutomationStopped)
	}
	if paused.Evidence["pause_reason"] != "maintenance" || paused.Fingerprint != before.Fingerprint {
		t.Errorf("paused finding lost its reason or identity: %+v", paused)
	}
	if found, err := ResumeAutomation(root, "autonomous-loop"); err != nil || !found {
		t.Fatalf("resume: found=%v err=%v", found, err)
	}
	if after := run(); after.Severity != SeverityWarning {
		t.Errorf("resumed automation is %s, want warning", after.Severity)
	}
}
