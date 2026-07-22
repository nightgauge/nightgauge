/**
 * Streaming budget enforcement helpers (Issue #254).
 *
 * Budget/ceiling enforcement historically evaluated only at STAGE END, because
 * cost arrived solely on the terminal Claude CLI `result` envelope
 * (`tokenParser` populates `usage` only there). Every threshold — wind-down,
 * warning, stage cap, run ceiling — therefore fired in one burst after the work
 * was already done, making the "commit and wrap up" wind-down signal useless
 * (it could only arrive once the stage had finished — bowlsheet-flutter #236).
 *
 * Since #233 the extension harvests per-turn `assistant` usage into a
 * `LiveStageEstimator`, so cost can be ESTIMATED while a stage is still running.
 * These pure helpers let the orchestrator drive the SAME
 * `BudgetEnforcer` / `PipelineBudgetCeiling` decision logic from that live
 * stream, so wind-down → warn → terminate fire mid-stage, in order.
 *
 * Two invariants keep the live path safe:
 *   1. The live path ENFORCES but never BOOKS. Only the authoritative terminal
 *      `result` envelope books cost (via `TokenAccumulator`), so the recorded
 *      totals are unchanged and there is no double-booking.
 *   2. Every side-effect is latched (fires at most once). Re-evaluating with the
 *      authoritative terminal cost after a live threshold already crossed is a
 *      no-op — {@link nextBudgetActions} returns no new actions.
 *
 * Pure and deterministic — no vscode, no process, no I/O — so the streaming
 * evaluation is unit-testable in isolation.
 *
 * @see budgetEnforcer.ts       — per-stage wind-down/warn/terminate decision
 * @see pipelineBudgetCeiling.ts — run-level ceiling decision
 * @see tokenParser.ts          — LiveStageEstimator (#233), TokenAccumulator (#256)
 */

import type { BudgetEnforcementDecision } from "./budgetEnforcer";

/**
 * The "has already fired" state for the three per-stage budget phases. The
 * orchestrator holds these as separate flags (`windDownSignalWritten`,
 * `budgetWarningEmitted`, `budgetTerminated`); this bundles them so the
 * once-only decision is one tested function instead of three inline guards
 * that the live and terminal paths could drift apart on.
 */
export interface BudgetLatchState {
  windDownFired: boolean;
  warnFired: boolean;
  terminated: boolean;
}

/**
 * Which per-stage budget side-effects are NEWLY triggered by this evaluation.
 * Each is `true` at most once across a stage's lifetime because it is gated on
 * the corresponding latch.
 */
export interface BudgetStreamActions {
  fireWindDown: boolean;
  fireWarn: boolean;
  fireTerminate: boolean;
}

/**
 * Given a {@link BudgetEnforcementDecision} (from `BudgetEnforcer.checkBudget`)
 * and the current latch state, return which side-effects are newly triggered.
 *
 * This is the single source of truth for "fire once, in phase order" shared by
 * the terminal (authoritative-cost) and live (estimated-cost) evaluation paths.
 * `BudgetEnforcer` already guarantees the three flags are mutually exclusive
 * per phase (wind-down below budget, warn between budget and limit, terminate
 * above limit), so as live cost rises the actions fire wind-down → warn →
 * terminate in order, each exactly once.
 */
export function nextBudgetActions(
  decision: Pick<BudgetEnforcementDecision, "shouldWindDown" | "shouldWarn" | "shouldTerminate">,
  latch: BudgetLatchState
): BudgetStreamActions {
  return {
    fireWindDown: decision.shouldWindDown && !latch.windDownFired,
    fireWarn: decision.shouldWarn && !latch.warnFired,
    fireTerminate: decision.shouldTerminate && !latch.terminated,
  };
}

/**
 * Compute the live pipeline-cumulative cost to feed the run-level ceiling
 * during a stage, without double-counting.
 *
 * The pipeline ceiling is cumulative across all stages. `bookedPipelineCostUsd`
 * is what `PipelineStateService` has recorded so far; `liveStageCostUsd` is the
 * current stage's cost (the live estimate mid-stage, the authoritative total at
 * stage end); `stageBookedCostUsd` is how much of THIS stage has already been
 * booked into the pipeline total.
 *
 *   live pipeline total = booked pipeline total + (this stage's not-yet-booked cost)
 *
 * - Mid-stage nothing is booked for the current stage (`stageBookedCostUsd = 0`),
 *   so the ceiling sees prior stages' booked cost plus this stage's live
 *   estimate — the true live pipeline total.
 * - At stage end the terminal envelope has already booked the stage
 *   (`stageBookedCostUsd === liveStageCostUsd`), so the extra term is 0 and the
 *   ceiling sees exactly the booked total — no double-count.
 *
 * `Math.max(0, …)` guards the case where the authoritative booked cost lands
 * slightly above the final live estimate.
 */
export function livePipelineCostUsd(
  bookedPipelineCostUsd: number,
  liveStageCostUsd: number,
  stageBookedCostUsd: number
): number {
  const booked = Number.isFinite(bookedPipelineCostUsd) ? bookedPipelineCostUsd : 0;
  const live = Number.isFinite(liveStageCostUsd) ? liveStageCostUsd : 0;
  const stageBooked = Number.isFinite(stageBookedCostUsd) ? stageBookedCostUsd : 0;
  return booked + Math.max(0, live - stageBooked);
}

/**
 * Throttle gate for live budget evaluation. Returns true when at least
 * `cadenceMs` has elapsed since the last emission, so the orchestrator
 * evaluates budgets on a bounded cadence instead of on every streamed
 * `assistant` message (a single dogfood stage produced ~1,571 usage payloads).
 * A non-positive cadence disables throttling (evaluate every snapshot).
 */
export function shouldEmitSnapshot(nowMs: number, lastEmitMs: number, cadenceMs: number): boolean {
  if (cadenceMs <= 0) return true;
  return nowMs - lastEmitMs >= cadenceMs;
}
