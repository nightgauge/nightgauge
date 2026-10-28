/**
 * Per-change outcome metrics (#1284).
 *
 * Health dimensions score stage executions; these three functions count
 * changes instead. All are pure and report `null` rather than a fabricated 0
 * when there is nothing to measure. Their results are surfaced as unscored
 * metrics in `stage-effectiveness` and `reliability`.
 */

import type { ExecutionHistoryRecord } from "../types.js";
import type { BacktrackEntry, PlanComparisonEntry, SurvivalVerdictEntry } from "./types.js";

/** A plan fidelity below this Jaccard overlap counts as "low". */
export const LOW_PLAN_FIDELITY = 0.5;

export interface PerChangeFirstPass {
  /** Issues with a successful `pr-merge` stage record. */
  mergedChanges: number;
  /** Merged issues with no retry, no repeated stage and no backtrack. */
  firstPassChanges: number;
  rate: number | null;
}

export interface PlanFidelity {
  /** Changes with a readable plan, scored by Jaccard overlap. */
  scored: number;
  /** Changes with no readable plan: excluded, never scored as 0. */
  noPlan: number;
  meanFidelity: number | null;
  /** Share of scored changes below {@link LOW_PLAN_FIDELITY}. */
  lowFidelityShare: number | null;
}

export interface ChangeFailureRate {
  /** Terminal verdicts that prove something: survived, reverted, broke. */
  finalized: number;
  /** reverted + broke */
  failures: number;
  /** Still inside the observation window. */
  excludedPending: number;
  /** Terminal but uninformative. */
  excludedUnobserved: number;
  rate: number | null;
}

/**
 * Share of merged issues that reached merge first time: every stage record
 * has `retries === 0`, no stage appears twice (a re-run), and no backtrack
 * event names the issue. A backtracked stage collapses to one run-record
 * entry, so backtracks must come from the trace.
 */
export function computePerChangeFirstPass(
  executionHistory: ExecutionHistoryRecord[],
  backtracks: BacktrackEntry[]
): PerChangeFirstPass {
  const byIssue = new Map<number, ExecutionHistoryRecord[]>();
  for (const r of executionHistory) {
    const list = byIssue.get(r.issueNumber);
    if (list) list.push(r);
    else byIssue.set(r.issueNumber, [r]);
  }
  const backtracked = new Set(backtracks.map((b) => b.issueNumber));

  let mergedChanges = 0;
  let firstPassChanges = 0;
  for (const [issue, records] of byIssue) {
    if (!records.some((r) => r.stage === "pr-merge" && r.success)) continue;
    mergedChanges++;
    const stages = new Set(records.map((r) => r.stage));
    const clean =
      records.every((r) => r.retries === 0) &&
      stages.size === records.length &&
      !backtracked.has(issue);
    if (clean) firstPassChanges++;
  }

  return {
    mergedChanges,
    firstPassChanges,
    rate: mergedChanges > 0 ? firstPassChanges / mergedChanges : null,
  };
}

function normalisePath(p: string): string {
  return p.trim().replace(/\\/g, "/").replace(/^\.\//, "").replace(/^\/+/, "");
}

/** Jaccard overlap |P∩M| / |P∪M| between planned and merged file sets. */
function jaccard(planned: string[], merged: string[]): number {
  const p = new Set(planned.map(normalisePath).filter(Boolean));
  const m = new Set(merged.map(normalisePath).filter(Boolean));
  const union = new Set([...p, ...m]);
  if (union.size === 0) return 1;
  let intersection = 0;
  for (const f of p) if (m.has(f)) intersection++;
  return intersection / union.size;
}

/**
 * Overlap between what each plan named and what its merge touched. A change
 * with no readable plan, or whose merge file list could not be read, is
 * counted in `noPlan` and excluded.
 */
export function computePlanFidelity(planComparisons: PlanComparisonEntry[]): PlanFidelity {
  const scores: number[] = [];
  let noPlan = 0;
  for (const c of planComparisons) {
    if (c.plannedFiles === null || c.mergedFiles.length === 0) {
      noPlan++;
      continue;
    }
    scores.push(jaccard(c.plannedFiles, c.mergedFiles));
  }
  if (scores.length === 0) {
    return { scored: 0, noPlan, meanFidelity: null, lowFidelityShare: null };
  }
  const sum = scores.reduce((a, b) => a + b, 0);
  return {
    scored: scores.length,
    noPlan,
    meanFidelity: sum / scores.length,
    lowFidelityShare: scores.filter((s) => s < LOW_PLAN_FIDELITY).length / scores.length,
  };
}

/**
 * `(reverted + broke) / (survived + reverted + broke)`. Pending records are
 * not successes yet and `unobserved` proves nothing, so both are excluded
 * and counted.
 */
export function computeChangeFailureRate(survival: SurvivalVerdictEntry[]): ChangeFailureRate {
  let finalized = 0;
  let failures = 0;
  let excludedPending = 0;
  let excludedUnobserved = 0;
  for (const s of survival) {
    switch (s.verdict) {
      case "pending":
        excludedPending++;
        break;
      case "unobserved":
        excludedUnobserved++;
        break;
      case "reverted":
      case "broke":
        failures++;
        finalized++;
        break;
      case "survived":
        finalized++;
        break;
    }
  }
  return {
    finalized,
    failures,
    excludedPending,
    excludedUnobserved,
    rate: finalized > 0 ? failures / finalized : null,
  };
}
