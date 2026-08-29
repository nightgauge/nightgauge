/**
 * The run's failed-stage predicate — one implementation, shared by the
 * classifier and every notifier (Issue #1109).
 *
 * This used to live in `services/DiscordService.ts`, reachable only from the
 * display layer. That placement is what let the outcome *classifier* run with
 * no view of the run's own stage list: the notifiers cross-checked the
 * contradiction and re-coloured the embed, while `outcome_type` stayed
 * `"productive"` in the durable record, in the calibration corpus and in the
 * health snapshot.
 *
 * It lives in `utils/` now so that the place where `outcome_type` is
 * *computed* and the places where it is *displayed* cannot drift into two
 * different answers to "did a stage fail?".
 */

/** Minimal shape of one stage entry — status is all this predicate reads. */
export interface StageStatusLike {
  status?: string;
}

/** Minimal shape of a pipeline state snapshot for the failed-stage count. */
export interface StagesSnapshotLike {
  stages?: Record<string, StageStatusLike | undefined | null>;
}

/**
 * Count the run's failed stages — the ground truth both the classifier and
 * the notifiers are cross-checked against.
 *
 * A retried stage is re-opened as `running` before it re-runs, so a stage that
 * failed and later succeeded is not counted here.
 */
export function countFailedStages(state: StagesSnapshotLike | null | undefined): number {
  return Object.values(state?.stages ?? {}).filter((s) => s?.status === "failed").length;
}
