package recovery

import (
	"context"
	"testing"

	"github.com/nightgauge/nightgauge/internal/orchestrator/gates"
	"github.com/nightgauge/nightgauge/internal/state"
)

// conflictNoOp is the StageFailure shape the scheduler builds when pr-merge hits
// an unresolvable rebase conflict (evidence names a conflict; KindNoOp).
func conflictNoOp() StageFailure {
	return StageFailure{
		Stage:       state.StagePRMerge,
		GateKind:    gates.KindNoOp,
		Workspace:   "/ws",
		IssueNumber: 4072,
		PRNumber:    100,
		Reason:      "pr-merge rebase hit a conflict on branch feat/x",
		Evidence:    []string{"pr=100", "conflict in src/foo.go"},
	}
}

// TestDefaultRegistry_ConflictRecoveryDisabled confirms the enabled knob is
// wired: with NIGHTGAUGE_CONFLICT_RECOVERY_ENABLED=false, Default() does not
// register the conflict-recovery action at all, so a conflict falls through to
// the generic pr-merge no-op handling.
func TestDefaultRegistry_ConflictRecoveryDisabled(t *testing.T) {
	t.Setenv(EnvConflictRecoveryEnabled, "false")
	reg := Default("", nil, nil)

	for _, a := range reg.Actions() {
		if a.Name() == "conflict-recovery-loop" {
			t.Fatalf("conflict-recovery-loop must NOT be registered when disabled")
		}
	}

	// A conflicting no-op must now be handled by some other action (or none),
	// never by the absent conflict-recovery-loop.
	var first string
	for _, a := range reg.Actions() {
		if a.Matches(conflictNoOp()) {
			first = a.Name()
			break
		}
	}
	if first == "conflict-recovery-loop" {
		t.Fatalf("disabled conflict-recovery-loop must not match")
	}
}

// TestRegistry_ConflictRecoveryBypassesGlobalCap locks the #4072 review fix: a
// conflicting pr-merge no-op must still route to conflict-recovery-loop even when
// the global per-run cap is already exhausted by unrelated recoveries — the
// conflict loop is bounded independently by max_dev_redispatch. A non-conflict
// no-op at the same cap must NOT recover (the cap still gates it).
func TestRegistry_ConflictRecoveryBypassesGlobalCap(t *testing.T) {
	ws := writeConflictContext(t, 4072, 100, "feat/x", []string{"internal/foo.go"})
	reg := New(2, NewConflictRecoveryLoop(2), NewSkillExitedWithoutMerging(nil))

	// IsCapExempt wiring.
	if !reg.IsCapExempt("conflict-recovery-loop") {
		t.Fatal("conflict-recovery-loop must be cap-exempt")
	}
	if reg.IsCapExempt("skill-exited-without-merging") {
		t.Fatal("skill-exited-without-merging must NOT be cap-exempt")
	}

	conflict := StageFailure{
		Stage: state.StagePRMerge, GateKind: gates.KindNoOp, Workspace: ws,
		IssueNumber: 4072, PRNumber: 100,
		Reason:   "PR not mergeable (CONFLICTING)",
		Evidence: []string{"mergeStateStatus=DIRTY", "conflict in internal/foo.go"},
	}
	// attemptsSoFar == cap: the conflict action must still fire (cap-exempt).
	if _, matched := reg.TryRecover(context.Background(), conflict, 2); !matched {
		t.Error("conflict-recovery must fire past the global cap (cap-exempt)")
	}

	// A plain non-conflict no-op at the same cap must be gated (no recovery).
	plain := StageFailure{
		Stage: state.StagePRMerge, GateKind: gates.KindNoOp, Workspace: ws,
		IssueNumber: 4072, PRNumber: 100,
		Reason:   "PR #100 is not MERGED (state=OPEN, mergeStateStatus=BLOCKED)",
		Evidence: []string{"pr=100", "mergeStateStatus=BLOCKED"},
	}
	if _, matched := reg.TryRecover(context.Background(), plain, 2); matched {
		t.Error("non-cap-exempt action must be gated by the global cap")
	}
}

// TestGetConflictRecoveryEnabled_EnvOverride locks the env precedence and default
// for the enabled knob.
func TestGetConflictRecoveryEnabled_EnvOverride(t *testing.T) {
	cases := []struct {
		val  string
		want bool
	}{
		{"false", false},
		{"0", false},
		{"off", false},
		{"true", true},
		{"1", true},
		{"on", true},
	}
	for _, c := range cases {
		t.Setenv(EnvConflictRecoveryEnabled, c.val)
		if got := GetConflictRecoveryEnabled(""); got != c.want {
			t.Errorf("GetConflictRecoveryEnabled(%q) = %v, want %v", c.val, got, c.want)
		}
	}

	// Unset → default true.
	t.Setenv(EnvConflictRecoveryEnabled, "")
	if !GetConflictRecoveryEnabled("") {
		t.Errorf("GetConflictRecoveryEnabled default must be %v", DefaultConflictRecoveryEnabled)
	}
}

// TestDefaultRegistry_BehindRoutesToBranchOutOfDate locks the #4071 ordering
// fix: a BEHIND/DIRTY pr-merge no-op (as emitted by PrMergeGate's enriched
// evidence) must be handled by branch-out-of-date — which rebases and
// re-validates — NOT by the generic skill-exited-without-merging catch-all,
// which matches ANY pr-merge no-op and would just re-run the runner that already
// punted on BEHIND. branch-out-of-date must therefore be registered BEFORE
// skill-exited-without-merging.
func TestDefaultRegistry_BehindRoutesToBranchOutOfDate(t *testing.T) {
	reg := Default("", nil, nil)

	// The exact StageFailure shape the scheduler builds from the gate for a
	// BEHIND sibling PR (evidence carries mergeStateStatus=BEHIND).
	behind := StageFailure{
		Stage:     state.StagePRMerge,
		GateKind:  gates.KindNoOp,
		Workspace: "/ws",
		Reason:    "PR #100 is not MERGED (state=OPEN, mergeStateStatus=BEHIND)",
		Evidence:  []string{"pr=100", "state=OPEN", "mergeStateStatus=BEHIND", "mergeable=MERGEABLE", "reviewDecision=APPROVED"},
	}

	var first string
	for _, a := range reg.Actions() {
		if a.Matches(behind) {
			first = a.Name()
			break
		}
	}
	if first != "branch-out-of-date" {
		t.Errorf("a BEHIND pr-merge no-op must route to branch-out-of-date first, got %q", first)
	}
}

// TestDefaultRegistry_PlainNoOpRoutesToSkillExited confirms the ordering change
// did not steal the generic case: a plain unflipped PR (no BEHIND/DIRTY token)
// still falls through to skill-exited-without-merging.
func TestDefaultRegistry_PlainNoOpRoutesToSkillExited(t *testing.T) {
	reg := Default("", nil, nil)

	plain := StageFailure{
		Stage:     state.StagePRMerge,
		GateKind:  gates.KindNoOp,
		Workspace: "/ws",
		PRNumber:  100,
		Reason:    "PR #100 is not MERGED (state=OPEN, mergeStateStatus=BLOCKED)",
		Evidence:  []string{"pr=100", "state=OPEN", "mergeStateStatus=BLOCKED"},
	}

	var first string
	for _, a := range reg.Actions() {
		if a.Matches(plain) {
			first = a.Name()
			break
		}
	}
	if first != "skill-exited-without-merging" {
		t.Errorf("a plain (non-BEHIND) pr-merge no-op must route to skill-exited-without-merging, got %q", first)
	}
}
