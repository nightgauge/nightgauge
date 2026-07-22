package recovery

import (
	pmstages "github.com/nightgauge/nightgauge/internal/orchestrator/stages"
)

// Default builds the canonical FailureRecovery registry with all eight actions
// registered in priority order. The order is load-bearing: when two
// predicates overlap (e.g. SkillExitedWithoutMerging and StallKilledOnPRMerge
// both match a pr-merge KindNoOp with PR>0 — but the latter additionally
// requires terminal_kind=stall_kill), the more specific action MUST come
// first.
//
// Wiring contract:
//
//   - prMergeRunner: shared with the scheduler's deterministic-first hook.
//     SkillExitedWithoutMerging, StallKilledOnPRMerge, and BranchOutOfDate all
//     consume it — BranchOutOfDate re-runs it to merge the rebased PR after CI
//     re-passes, claiming recovery only on PathMerged.
//   - prCreateRunner: shared with the scheduler's deterministic-first hook.
//
// workspaceRoot is read for `.nightgauge/config.yaml` to determine the
// per-run cap. Pass empty string to use the constant default.
func Default(workspaceRoot string, prMergeRunner pmstages.PRMergeRunner, prCreateRunner pmstages.PRCreateRunner) *Registry {
	cap := GetMaxAttemptsPerRun(workspaceRoot)

	actions := []RecoveryAction{
		// Stall-kill on pr-merge fires first — it requires the most specific
		// signal (terminal_kind=stall_kill).
		NewStallKilledOnPRMerge(prMergeRunner),
	}

	// Conflict-recovery fires BEFORE branch-out-of-date (when enabled). A pr-merge
	// KindNoOp whose evidence names a CONFLICT is a genuine content collision the
	// plain rebase in branch-out-of-date cannot resolve — it needs the LLM dev
	// stage. This action emits a CONFLICT_RESOLUTION_NEEDED feedback signal and
	// defers (Recovered=false + FollowUpStageCanResume + BacktrackTargetStage) so
	// the scheduler rewinds the pipeline to feature-dev on the SAME branch (#4072).
	// First-match-wins: conflict ≠ plain BEHIND, so it must precede branch-out-of-
	// date. When disabled (pipeline.recovery.conflict_recovery.enabled: false), a
	// conflict falls through to branch-out-of-date / triage.
	if GetConflictRecoveryEnabled(workspaceRoot) {
		actions = append(actions, NewConflictRecoveryLoop(GetConflictMaxDevRedispatch(workspaceRoot)))
	}

	actions = append(actions,
		// Branch-out-of-date is MORE specific than the generic skill-no-op
		// recovery: it requires BEHIND/DIRTY merge-state evidence (emitted by
		// PrMergeGate), and it RE-VALIDATES — rebase → wait for CI on the
		// rebased head → re-run prMergeRunner → recover only on PathMerged. It
		// MUST precede SkillExitedWithoutMerging (which matches ANY pr-merge
		// KindNoOp and would otherwise shadow it, re-running the runner that
		// just punted on BEHIND). #4071.
		NewBranchOutOfDate(prMergeRunner),
		NewSkillExitedWithoutMerging(prMergeRunner),
		NewSkillExitedWithoutCreatingPR(prCreateRunner),
		NewCICheckTransientlyFailed(),
		NewStaleProjectStatus(),
		// PipelineHealBase fires last — it's the most aggressive action
		// (creates a cross-cutting PR against main). The narrow
		// inherited-only marker keeps it from competing with the earlier
		// pr-merge no-op actions.
		NewPipelineHealBase(workspaceRoot),
	)

	return New(cap, actions...)
}
