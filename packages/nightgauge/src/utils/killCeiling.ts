/**
 * Self-describing kill ceilings for stage terminations (Issue #161).
 *
 * ## The gap this closes
 *
 * Before this module a guard kill recorded only `signal_source` — a coarse
 * label naming the *closure* that delivered SIGTERM ("runaway-progress",
 * "stall-kill", "hard-cap"). Several distinct limits funnel into each of those
 * labels, so the record could not answer the only question that matters during
 * triage: **which ceiling fired, and what was it set to?**
 *
 * Issue #161 is the proof. Three stages were killed with
 * `signal_source=runaway-progress` at 1800s / 2400s / 2400s. Reading the whole
 * resolver chain — `DEFAULT_STAGE_HARD_CAPS`, `stage_time_caps`,
 * `no_progress_window_ms`, the cost ceiling — did not identify the source,
 * because the actual ceiling (the Nx stall-threshold escalation, `stall warn
 * threshold × NX_RUNAWAY_KILL_MULTIPLE`) is a *derived* value that appears
 * nowhere in config. The kill has to say so itself; deducing it after the fact
 * is not a diagnostic strategy.
 *
 * ## Contract
 *
 * Every kill path that enforces a configured limit constructs a
 * {@link KillCeiling} and the runner forwards it to the stage-exit record as
 * `kill_ceiling` (the stable name) and `kill_ceiling_value` (the resolved
 * limit plus how it was derived). Paths that are NOT limit enforcement — an
 * operator abort, an external signal, a crash — carry no ceiling.
 *
 * @see docs/STAGE_EXIT_DIAGNOSTIC.md — on-disk record schema
 */

/**
 * Stable identifier of the limit that terminated a stage.
 *
 * These names are a diagnostic contract: they appear verbatim in exit records
 * and are grepped in retros. Renaming one silently breaks historical queries,
 * so treat the set as append-mostly.
 */
export type KillCeilingName =
  /** ProgressMonitor: no productive signal for `no_progress_window_ms`. */
  | "progress-no-progress-window"
  /** ProgressMonitor: `churn_tool_threshold` distinct tools, no productive signal. */
  | "progress-churn-tools"
  /** ProgressMonitor: `catastrophic_limit_usd` reached with no productive progress. */
  | "progress-catastrophic-cost"
  /** skillRunner: elapsed reached `stall warn threshold × NX_RUNAWAY_KILL_MULTIPLE`. */
  | "nx-stall-multiple"
  /** skillRunner: elapsed reached `pipeline.stage_hard_caps.<stage>`. */
  | "stage-hard-cap"
  /** skillRunner: elapsed reached `pipeline.stage_time_caps.<stage>` (zero-cost adapters). */
  | "stage-time-cap"
  /** skillRunner: idle past the quota fast-fail budget after a rate-limit signal. */
  | "quota-fast-fail-idle"
  /** skillRunner: idle past `stallKillMs` with no subprocess output. */
  | "stall-idle";

/**
 * A ceiling that fired, in enough detail to be actionable without reading code.
 */
export interface KillCeiling {
  /** Stable identifier of the limit. */
  name: KillCeilingName;
  /**
   * The resolved limit in its own units — `"2400000ms"`, `"$200.00"`,
   * `"40 tools"`. This is the number the stage was measured against.
   */
  limit: string;
  /**
   * How `limit` was reached: the constants, config keys, and resolved inputs
   * that composed it. This is the load-bearing half for a derived ceiling —
   * `2400000ms` alone sends the reader back to the resolver chain, whereas
   * `stall warn threshold 300s (source: static) × NX_RUNAWAY_KILL_MULTIPLE=8`
   * ends the investigation.
   */
  derivation: string;
}

/** Render a millisecond limit for {@link KillCeiling.limit}. */
export function msLimit(ms: number): string {
  return `${Math.round(ms)}ms`;
}

/** Render a USD limit for {@link KillCeiling.limit}. */
export function usdLimit(usd: number): string {
  return `$${usd.toFixed(2)}`;
}

/**
 * Flatten a ceiling into the single `kill_ceiling_value` record field.
 *
 * Kept as one field (rather than two more IPC parameters) because limit and
 * derivation are never useful apart — a reader who has one always wants the
 * other.
 */
export function formatKillCeilingValue(ceiling: KillCeiling): string {
  return ceiling.derivation ? `${ceiling.limit} (${ceiling.derivation})` : ceiling.limit;
}
