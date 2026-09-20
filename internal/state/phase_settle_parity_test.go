package state

import (
	"testing"

	"github.com/nightgauge/nightgauge/internal/intelligence/tokens"
)

// Parity tests (#1885 AC5): the Go/CLI path (this package's BeginPhase /
// CompleteStage / CompleteStageWithCost) must produce the same status
// sequence for a replayed marker stream that
// packages/nightgauge-vscode/src/utils/phaseTracker.ts's onPhaseDetected /
// completeStagePhases produce on the extension/IPC path for the equivalent
// scenario, so the two paths cannot silently drift apart again:
//
//   - onPhaseDetected completes the stage's previously active phase before
//     starting the next one (mirrored by BeginPhase, AC1).
//   - completeStagePhases (called from onStageComplete, i.e. a SUCCESSFUL
//     boundary) completes the last active phase and back-fills every
//     PHASE_REGISTRY / PhaseRegistry name the stage never reported as
//     `unreported` (mirrored by settleStagePhasesLocked's success arm, AC2 +
//     AC4).
//   - There is no TS equivalent of an ABNORMAL boundary settling a phase
//     `abandoned` — that is Go/CLI-specific (#1009) — so the failure-path
//     replay below asserts against #1009's own contract rather than against
//     phaseTracker.ts, and deliberately does NOT run the back-fill (AC3).
func TestPhaseSettleParity_MarkerStreamReplay_Success(t *testing.T) {
	rs := NewRuntimeState("o/r", 1885, "item", "01a04662-0000-7000-8000-000000000010")
	rs.Stage = StageFeatureDev

	// Replay a marker stream that reports only phases 0, 1 and 8 — the same
	// partial-reporting shape #3760/#1850 observed in practice, where a model
	// emits some markers and infers/skips the rest.
	rs.BeginPhase(StageFeatureDev, "validate-environment", 0, 18)
	rs.BeginPhase(StageFeatureDev, "read-planning-context", 1, 18)
	rs.BeginPhase(StageFeatureDev, "implementation", 8, 18)
	rs.CompleteStage(0, tokens.TokenCounts{}, "sonnet", "claude")

	byName := map[string]string{}
	for _, p := range rs.PhaseHistory {
		byName[p.Name] = p.Status
	}

	// AC1: each later BeginPhase settled the previous one `complete`.
	if got := byName["validate-environment"]; got != "complete" {
		t.Errorf("validate-environment = %q, want complete (settled by the next BeginPhase)", got)
	}
	if got := byName["read-planning-context"]; got != "complete" {
		t.Errorf("read-planning-context = %q, want complete (settled by the next BeginPhase)", got)
	}
	// AC2: the last active phase is settled `complete` at the successful
	// stage boundary, not `abandoned`.
	if got := byName["implementation"]; got != "complete" {
		t.Errorf("implementation = %q, want complete (settled at the successful stage boundary)", got)
	}
	// AC4: every registry phase the stream never mentioned back-fills
	// `unreported`, and the denominator is the registry total.
	if got := byName["testing"]; got != "unreported" {
		t.Errorf("testing = %q, want unreported (never reported by the stream)", got)
	}
	if got, want := len(rs.PhaseHistory), len(PhaseRegistry[StageFeatureDev]); got != want {
		t.Errorf("PhaseHistory has %d records, want %d (registry total)", got, want)
	}
}

// TestPhaseSettleParity_MarkerStreamReplay_Failure is AC3: `abandoned` stays
// reachable, and only fires on an abnormal boundary — a non-zero exit here —
// with no back-fill, since a stage that did not finish did not get a fair
// chance to report the rest of its phases either.
func TestPhaseSettleParity_MarkerStreamReplay_Failure(t *testing.T) {
	rs := NewRuntimeState("o/r", 1885, "item", "01a04662-0000-7000-8000-000000000011")
	rs.Stage = StageFeatureDev

	rs.BeginPhase(StageFeatureDev, "validate-environment", 0, 18)
	rs.BeginPhase(StageFeatureDev, "read-planning-context", 1, 18)
	rs.BeginPhase(StageFeatureDev, "implementation", 8, 18)
	rs.CompleteStage(1, tokens.TokenCounts{}, "sonnet", "claude")

	byName := map[string]string{}
	for _, p := range rs.PhaseHistory {
		byName[p.Name] = p.Status
	}

	if got := byName["validate-environment"]; got != "complete" {
		t.Errorf("validate-environment = %q, want complete (settled by the next BeginPhase, independent of the eventual outcome)", got)
	}
	if got := byName["read-planning-context"]; got != "complete" {
		t.Errorf("read-planning-context = %q, want complete", got)
	}
	if got := byName["implementation"]; got != "abandoned" {
		t.Errorf("implementation = %q, want abandoned — the stage ended abnormally with it still open (#1009, AC3)", got)
	}
	if len(rs.PhaseHistory) != 3 {
		t.Errorf("PhaseHistory has %d records, want 3 — an abnormal boundary must not back-fill the registry", len(rs.PhaseHistory))
	}
}

// TestPhaseSettleParity_CompleteStageWithCost pins the specific regression
// named in the issue: CompleteStageWithCost — the completion path used
// whenever the CLI reports a native total_cost_usd, the common case for
// agentic stages — never called any settlement at all, so a stray running
// phase on that path was left "running" forever rather than even reaching
// "abandoned". It must now settle identically to CompleteStage.
func TestPhaseSettleParity_CompleteStageWithCost(t *testing.T) {
	rs := NewRuntimeState("o/r", 1885, "item", "01a04662-0000-7000-8000-000000000012")
	rs.Stage = StageFeatureDev

	rs.BeginPhase(StageFeatureDev, "implementation", 8, 18)
	rs.CompleteStageWithCost(0, 100, 50, 0, 0.01)

	byName := map[string]string{}
	for _, p := range rs.PhaseHistory {
		byName[p.Name] = p.Status
	}
	if got := byName["implementation"]; got != "complete" {
		t.Errorf("implementation = %q, want complete — CompleteStageWithCost must settle phases exactly like CompleteStage", got)
	}
	if got := byName["testing"]; got != "unreported" {
		t.Errorf("testing = %q, want unreported — CompleteStageWithCost must back-fill exactly like CompleteStage", got)
	}
	if got, want := len(rs.PhaseHistory), len(PhaseRegistry[StageFeatureDev]); got != want {
		t.Errorf("PhaseHistory has %d records, want %d (registry total)", got, want)
	}
}
