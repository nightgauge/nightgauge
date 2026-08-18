/**
 * Progress-based runaway detection for pipeline stages (Issue #3783, #3851).
 *
 * Tracks forward-progress signals over a sliding time window. A stage is
 * considered stuck when no new *productive* progress signal arrives for
 * `noProgressWindowMs`. This replaces the dollar-ceiling kill path
 * (`checkCostCap`) with semantic progress awareness so cheap loops are stopped
 * quickly and expensive-but-active stages run to completion.
 *
 * ## Productive progress vs activity (Issue #3851)
 *
 * Issue #3811's feature-dev burned $112 by churning on a 9–18-skill refactor:
 * ~530 tool calls (333 Reads / 60 Edits / 137 Searches), re-reading the same
 * files 20-30×. Every few seconds it issued a *novel* tool signature, so the
 * old design — which treated any new `distinct_tool` signature as
 * window-advancing "progress" — never let the no-progress window elapse.
 *
 * The root-cause fix: distinguish **productive progress** (net forward motion
 * on the actual deliverable) from **activity** (tool calls, reads, greps,
 * duplicate edits). Only productive signals advance the no-progress window:
 *
 *   - `commit`       — a git commit was observed (net forward motion)
 *   - `file_change`  — a write to a NEW file path / NEW content
 *   - `phase_marker` — the skill self-reported a phase transition
 *   - `ci_progress`  — CI checks advanced
 *
 * `distinct_tool` (and duplicate `file_change` to an already-seen path) is
 * tracked ONLY for the churn detector below — it never advances the
 * PRODUCTIVE window.
 *
 * ## Activity gate on the no-progress kill (Issue #128)
 *
 * Productive signals are *artifact* signals. But the terminal phase of every
 * stage — final verification, scope-tidying, the self-assessment epilogue —
 * creates no artifacts by definition: it runs tests, reads files, reverts
 * out-of-scope edits. Under a productive-signals-only rule that phase is
 * indistinguishable from a wedged process, so the monitor was most likely to
 * fire exactly when a stage was closest to succeeding (and the kill was
 * maximally destructive, because `feature-dev` commits nothing — #1608).
 *
 * The fix is NOT a bigger window (that only delays the misfire); it is a
 * second clock. `lastActivityMs` tracks NOVEL tool invocations, and the plain
 * no-progress kill now requires BOTH clocks to be cold:
 *
 *   - no productive signal for `noProgressWindowMs`  (unchanged), AND
 *   - no novel tool call for `noProgressWindowMs`    (new).
 *
 * "Novel" is load-bearing: a process re-issuing the SAME tool signature is
 * spinning, not working, and its repeats are deduplicated before they reach
 * the activity clock — so an identical-call loop is still killed. A genuinely
 * wedged process makes no tool calls at all and is still killed on the same
 * schedule as before.
 *
 * ## Churn detector (Issue #3851)
 *
 * A non-converging stage spins through many distinct tool signatures while
 * making no productive progress. The churn detector kills when ALL hold:
 *   1. cost is past the activation floor (`minCostToActivateUsd`),
 *   2. distinct-tool signatures climbed by ≥ `churnToolThreshold` since the
 *      last productive signal, and
 *   3. no productive signal has arrived for at least `noProgressWindowMs`.
 *
 * The churn detector is deliberately NOT activity-gated — killing a stage that
 * is busy but not converging is its entire purpose (#3811: 530 tool calls, 0
 * commits, $112). It remains the bound on how long the activity gate above can
 * keep a non-productive stage alive, alongside the stage hard-cap and the
 * catastrophic cost backstop.
 *
 * This is deliberately conservative so a HEALTHY long stage (steady commits /
 * new-file writes / phase markers) is NEVER killed — only a stage that is
 * demonstrably active-but-not-progressing. See #2982 / #3840 for the
 * false-kill class this guards against.
 */

import type { KillCeiling } from "./killCeiling";
import { msLimit, usdLimit } from "./killCeiling";
import { collectToolCalls, type ParsedStreamMessage } from "./tokenParser";

export type ProgressSignalType =
  | "phase_marker" // <!-- phase:start ... --> detected in output (productive)
  | "file_change" // Write/Edit to a NEW path or git write command (productive iff new)
  | "ci_progress" // CI_PROGRESS: JSON line detected (productive)
  | "distinct_tool" // New unique tool signature — ACTIVITY ONLY, never advances window
  | "commit"; // git commit observed (productive)

/** Signal types that represent genuine forward motion on the deliverable. */
const PRODUCTIVE_SIGNALS: ReadonlySet<ProgressSignalType> = new Set([
  "commit",
  "phase_marker",
  "ci_progress",
  // file_change is productive ONLY for a new path — handled explicitly in recordSignal.
]);

export interface ProgressMonitorConfig {
  /** Window in ms with no PRODUCTIVE signal before a kill fires. Default 120_000 (2 min). */
  noProgressWindowMs: number;
  /** Minimum stage cost before the monitor activates. Default 0.50 USD. */
  minCostToActivateUsd: number;
  /** Cost threshold for a catastrophic backstop. Default 200 USD. */
  catastrophicLimitUsd: number;
  /** Master toggle. When false, check() always returns no-op. */
  enabled: boolean;
  /** When true (maximum performance mode), shouldKill is always false. */
  observeOnly: boolean;
  /**
   * Number of distinct-tool signatures that must accumulate with no
   * intervening productive signal before the churn detector fires (Issue
   * #3851). Default 40 — well above any healthy productive burst, matched to
   * the #3811 churn profile (530 tool calls / 0 commits). 0 disables churn
   * detection.
   */
  churnToolThreshold: number;
  /**
   * Cost floor (USD) above which the catastrophic backstop becomes a KILL in
   * unattended runs rather than warn-only (Issue #3851). When the catastrophic
   * limit is reached AND there is no productive progress in the window, the
   * stage is killed. 0 keeps the legacy warn-only behaviour.
   */
  catastrophicKill: boolean;
}

export interface ProgressCheckResult {
  shouldKill: boolean;
  shouldWarn: boolean;
  reason: string;
  signalsSeen: number;
  msSinceLastProgress: number;
  /** Number of PRODUCTIVE signals observed (commits / new files / phases / CI). */
  productiveSignals: number;
  /** Number of distinct tool signatures observed since the last productive signal. */
  churnSinceProgress: number;
  /**
   * Ms since the last NOVEL tool invocation (or construction) — the "is the
   * process alive?" clock the no-progress kill is gated on (Issue #128).
   */
  msSinceLastActivity: number;
  /**
   * The specific ceiling behind `shouldKill` / `shouldWarn`, with its
   * configured value (Issue #161). Undefined for the no-op outcomes (disabled,
   * below the cost activation floor, progress healthy, activity-gated) —
   * nothing was crossed. `reason` remains the human sentence; this is the
   * machine-readable identity that lands in the stage-exit record so a reader
   * never has to re-derive which limit fired.
   */
  ceiling?: KillCeiling;
}

export class ProgressMonitor {
  /** Wall-clock of the last PRODUCTIVE signal (advances the no-progress window). */
  private lastProgressMs: number;
  /**
   * Wall-clock of the last NOVEL tool invocation OR productive signal
   * (Issue #128). Repeated identical tool signatures are deduplicated before
   * they reach this clock, so a spin loop never refreshes it.
   */
  private lastActivityMs: number;
  private readonly distinctToolSigs = new Set<string>();
  /** Paths that have already received a write — re-writes are churn, not progress. */
  private readonly writtenPaths = new Set<string>();
  private totalSignals = 0;
  /** Count of productive signals (commit / new file / phase_marker / ci_progress). */
  private productiveSignals = 0;
  /** Distinct-tool signatures seen since the last productive signal (churn gauge). */
  private churnSinceProgress = 0;

  constructor(private readonly config: ProgressMonitorConfig) {
    this.lastProgressMs = Date.now();
    this.lastActivityMs = this.lastProgressMs;
  }

  /**
   * Quiet-activity window (Issue #128). Derived from `noProgressWindowMs` so
   * the monitor keeps exactly ONE tunable window — a second knob would let the
   * two clocks drift out of sync with nothing to reconcile them.
   */
  private get activityWindowMs(): number {
    return this.config.noProgressWindowMs;
  }

  /**
   * Record a signal.
   *
   * PRODUCTIVE signals (`commit`, `phase_marker`, `ci_progress`, and a
   * `file_change` to a NEW path) advance the no-progress window and reset the
   * churn gauge.
   *
   * ACTIVITY signals (`distinct_tool`, and a `file_change` to an
   * already-written path) are tracked for the churn detector ONLY and do NOT
   * advance the window. Repeated identical tool signatures are deduplicated.
   *
   * @param type  the signal type
   * @param sig   for `distinct_tool`: the unique tool signature; for
   *              `file_change`: the target file path (used to decide new vs dup)
   */
  recordSignal(type: ProgressSignalType, sig?: string): void {
    if (type === "distinct_tool") {
      // Activity only — never advances the window. Track distinct signatures
      // so the churn detector can see "lots of novel activity, no progress".
      if (!sig || this.distinctToolSigs.has(sig)) {
        return;
      }
      this.distinctToolSigs.add(sig);
      this.totalSignals++;
      this.churnSinceProgress++;
      // A NOVEL tool invocation proves the process is alive and doing
      // something new — it gates (but never satisfies) the no-progress kill.
      // Issue #128.
      this.lastActivityMs = Date.now();
      return;
    }

    if (type === "file_change") {
      // A write to a NEW path is productive; re-writing a path already touched
      // this stage is churn (the #3811 "edit the same 9 files 60×" pattern).
      const path = sig ?? "";
      if (path && this.writtenPaths.has(path)) {
        // Duplicate edit — treat as activity, not progress.
        this.totalSignals++;
        this.churnSinceProgress++;
        return;
      }
      if (path) {
        this.writtenPaths.add(path);
      }
      this.advanceProgress();
      return;
    }

    // commit / phase_marker / ci_progress — always productive.
    if (PRODUCTIVE_SIGNALS.has(type)) {
      this.advanceProgress();
      return;
    }

    // Unknown type — treat conservatively as activity only.
    this.totalSignals++;
  }

  /** Mark a productive signal: advance the window and reset the churn gauge. */
  private advanceProgress(): void {
    this.lastProgressMs = Date.now();
    // Productive progress is also activity — keep the two clocks consistent
    // so a stage that only ever emits productive signals is never treated as
    // "no tool activity" by the gate below (Issue #128).
    this.lastActivityMs = this.lastProgressMs;
    this.totalSignals++;
    this.productiveSignals++;
    this.churnSinceProgress = 0;
  }

  /**
   * Productive-progress accessor for the cost-ceiling gate (Issue #3851).
   *
   * The orchestrator's unattended budget/ceiling escalation consults this to
   * decide whether to escalate (progress healthy) or stop (progress flat).
   *
   * @returns the cumulative count of productive signals (commits / new files /
   *          phase markers / CI progress). The caller snapshots this before an
   *          escalation and compares against a later snapshot to compute the
   *          delta "since last escalation".
   */
  getProductiveProgressDelta(): number {
    return this.productiveSignals;
  }

  /** Ms since the last PRODUCTIVE signal (or construction). */
  get msSinceLastProductiveProgress(): number {
    return Date.now() - this.lastProgressMs;
  }

  /**
   * Ms since the last NOVEL tool invocation or productive signal (Issue #128).
   *
   * This is the "is the process alive?" clock. A stage in its terminal
   * verification / epilogue phase keeps this near zero while
   * {@link msSinceLastProductiveProgress} grows without bound; a wedged
   * process lets both grow together.
   */
  get msSinceLastActivity(): number {
    return Date.now() - this.lastActivityMs;
  }

  /**
   * Evaluate whether the stage should be killed or warned.
   *
   * Call from the 30-second stall ticker. O(1) — no I/O.
   */
  check(currentCostUsd: number): ProgressCheckResult {
    const base = {
      signalsSeen: this.totalSignals,
      productiveSignals: this.productiveSignals,
      churnSinceProgress: this.churnSinceProgress,
      msSinceLastActivity: this.msSinceLastActivity,
    };

    if (!this.config.enabled) {
      return {
        shouldKill: false,
        shouldWarn: false,
        reason: "disabled",
        msSinceLastProgress: 0,
        ...base,
      };
    }

    if (currentCostUsd < this.config.minCostToActivateUsd) {
      return {
        shouldKill: false,
        shouldWarn: false,
        reason: `cost $${currentCostUsd.toFixed(4)} below activation threshold $${this.config.minCostToActivateUsd}`,
        msSinceLastProgress: 0,
        ...base,
      };
    }

    const msSinceLastProgress = Date.now() - this.lastProgressMs;
    const windowExceeded = msSinceLastProgress > this.config.noProgressWindowMs;

    // Catastrophic cost backstop. Issue #3851 upgrades this from warn-only to a
    // KILL when `catastrophicKill` is set AND there is no productive progress in
    // the window — a stage that has burned $200+ with nothing to show for it is
    // a confirmed runaway. A stage still making productive progress is only
    // warned (we never blunt-kill healthy large work — #2982/#3840).
    if (
      this.config.catastrophicLimitUsd > 0 &&
      currentCostUsd >= this.config.catastrophicLimitUsd
    ) {
      const catastrophicCeiling: KillCeiling = {
        name: "progress-catastrophic-cost",
        limit: usdLimit(this.config.catastrophicLimitUsd),
        derivation:
          `pipeline.progress_runaway.catastrophic_limit_usd, ` +
          `no productive progress for > ${msLimit(this.config.noProgressWindowMs)}`,
      };
      if (this.config.catastrophicKill && !this.config.observeOnly && windowExceeded) {
        return {
          shouldKill: true,
          shouldWarn: false,
          reason:
            `Cost $${currentCostUsd.toFixed(2)} reached catastrophic limit ` +
            `$${this.config.catastrophicLimitUsd.toFixed(2)} with no productive progress for ` +
            `${Math.round(msSinceLastProgress / 1000)}s (catastrophic kill, Issue #3851)`,
          msSinceLastProgress,
          ceiling: catastrophicCeiling,
          ...base,
        };
      }
      return {
        shouldKill: false,
        shouldWarn: true,
        reason: `Cost $${currentCostUsd.toFixed(2)} reached catastrophic limit $${this.config.catastrophicLimitUsd.toFixed(2)} (warn-only backstop)`,
        msSinceLastProgress,
        ceiling: catastrophicCeiling,
        ...base,
      };
    }

    // ── Churn detector (Issue #3851) ──────────────────────────────────────
    // Lots of novel activity, no productive progress, past the cost floor.
    // This is the proximate guard for the #3811 churn (530 tool calls, 0
    // commits): distinct-tool signatures climbed past the threshold while the
    // productive window stayed flat. Gated on windowExceeded so a healthy
    // stage that just committed is never killed for a burst of reads.
    const churnDetected =
      this.config.churnToolThreshold > 0 &&
      this.churnSinceProgress >= this.config.churnToolThreshold &&
      windowExceeded;

    if (churnDetected && !this.config.observeOnly) {
      return {
        shouldKill: true,
        shouldWarn: false,
        reason:
          `Churn detected: ${this.churnSinceProgress} distinct tool calls with no productive ` +
          `progress (commits/new-files/phase) for ${Math.round(msSinceLastProgress / 1000)}s ` +
          `(threshold: ${this.config.churnToolThreshold}, Issue #3851)`,
        msSinceLastProgress,
        ceiling: {
          name: "progress-churn-tools",
          limit: `${this.config.churnToolThreshold} tools`,
          derivation:
            `pipeline.progress_runaway.churn_tool_threshold, ` +
            `no productive progress for > ${msLimit(this.config.noProgressWindowMs)}`,
        },
        ...base,
      };
    }

    if (!windowExceeded) {
      return {
        shouldKill: false,
        shouldWarn: false,
        reason: "progress_ok",
        msSinceLastProgress,
        ...base,
      };
    }

    // ── Activity gate (Issue #128) ────────────────────────────────────────
    // The productive window has elapsed, but the stage is still issuing NOVEL
    // tool calls — running tests, reading files, reverting an out-of-scope
    // edit, writing its self-assessment. That is the terminal phase of a
    // healthy stage, not a stall, and killing there destroys work no
    // downstream stage can recover (`feature-dev` commits nothing — #1608).
    // Defer the no-progress kill while the activity clock is warm. The churn
    // detector above, the stage hard-cap, and the catastrophic cost backstop
    // remain in force, so this defers the kill — it cannot disable it.
    const msSinceLastActivity = this.msSinceLastActivity;
    if (msSinceLastActivity <= this.activityWindowMs) {
      return {
        shouldKill: false,
        shouldWarn: false,
        reason:
          `No productive progress for ${Math.round(msSinceLastProgress / 1000)}s, but tool ` +
          `activity ${Math.round(msSinceLastActivity / 1000)}s ago ` +
          `(activity window: ${this.activityWindowMs / 1000}s) — working, not stalled ` +
          `(Issue #128)`,
        msSinceLastProgress,
        ...base,
      };
    }

    const reason =
      `No productive progress (commit / new file / phase / CI) for ` +
      `${Math.round(msSinceLastProgress / 1000)}s ` +
      `and no tool activity for ${Math.round(msSinceLastActivity / 1000)}s ` +
      `(window: ${this.config.noProgressWindowMs / 1000}s, productive signals: ${this.productiveSignals}, ` +
      `activity signals: ${this.totalSignals})`;

    const noProgressCeiling: KillCeiling = {
      name: "progress-no-progress-window",
      limit: msLimit(this.config.noProgressWindowMs),
      derivation:
        `pipeline.progress_runaway.no_progress_window_ms, ` +
        `applied to BOTH the productive and the activity clock (Issue #128)`,
    };

    // In observe-only mode (maximum performance mode) never kill — only warn
    // (kill demoted to warn for the whole no-progress condition, matching the
    // pre-#3851 observability contract).
    if (this.config.observeOnly) {
      return {
        shouldKill: false,
        shouldWarn: true,
        reason,
        msSinceLastProgress,
        ceiling: noProgressCeiling,
        ...base,
      };
    }

    // Window exceeded but churn below threshold: the stage is idle-ish but not
    // demonstrably churning. Kill on the no-progress window as before (#3783) —
    // the window is now measured against PRODUCTIVE signals, so a stage doing
    // real work (committing / writing new files) keeps resetting it.
    return {
      shouldKill: true,
      shouldWarn: false,
      reason,
      msSinceLastProgress,
      ceiling: noProgressCeiling,
      ...base,
    };
  }

  get hasObservedAnyProgress(): boolean {
    return this.totalSignals > 0;
  }

  /** True once at least one PRODUCTIVE signal (commit / new file / phase / CI) was seen. */
  get hasObservedProductiveProgress(): boolean {
    return this.productiveSignals > 0;
  }
}

/**
 * Feed the progress monitor from a parsed stream-json message (Issue #295).
 *
 * ── The bug this fixes ────────────────────────────────────────────────────
 * The Claude CLI delivers a tool call in TWO possible shapes and the runaway
 * monitor must classify BOTH:
 *
 *   - a `content_block_start` event  → the parser sets the SINGULAR
 *     `toolName` / `toolInput` fields. Only emitted with
 *     `--include-partial-messages`, which the pipeline does NOT pass.
 *   - a complete `assistant` message → the parser sets the PLURAL `toolUses[]`
 *     array (see tokenParser.ts). This is the ONLY shape emitted at runtime
 *     (the CLI is spawned with `--output-format stream-json --verbose`, no
 *     partial messages), so every real tool call arrives here.
 *
 * Before #295, skillRunner gated the entire signal-classification block on
 * `if (parsed?.toolName)` — the singular, `content_block_start`-only field.
 * At runtime that field is NEVER populated, so the feed NEVER fired:
 * `distinct_tool` / `file_change` / `commit` stayed at 0 for the whole stage.
 * The cost path survived (it reads the independent `result` / `usage`
 * branch), so a stage could burn real money and make 100+ tool calls while
 * the monitor recorded `activity signals: 0` — and a delegation-heavy
 * feature-dev whose productive-signal path also went quiet was then
 * false-killed by the no-progress runaway monitor (the dogfood mid-stage
 * SIGTERM run).
 *
 * Classifying from `toolUses[]` (and still honoring the singular field for the
 * partial-message shape) reconnects the feed. A single parsed line is either a
 * `content_block_start` (singular set) OR an `assistant` message (plural set),
 * never both, so no tool call is double-counted.
 *
 * @returns the number of tool_use events observed in this message. Callers
 *          maintain a running total to power the fail-open guard
 *          (`isBlindMonitorKill`): a monitor that has seen tool events yet
 *          recorded zero signals is disconnected, not watching a stalled
 *          agent, and must never fire the kill.
 */
export function recordToolCallProgress(
  monitor: ProgressMonitor,
  parsed: ParsedStreamMessage | null
): number {
  // Both delivery shapes, flattened by the one normaliser every consumer now
  // shares (#169). This function had its own copy of that collection step; two
  // copies is how the shapes drifted apart in the first place.
  const calls = collectToolCalls(parsed);

  for (const { name, input } of calls) {
    const toolInput = (input ?? {}) as Record<string, unknown>;
    const isWriteTool = name === "Write" || name === "Edit" || name === "MultiEdit";
    const filePath = typeof toolInput.file_path === "string" ? toolInput.file_path : "";

    if (isWriteTool && filePath) {
      // file_change is productive ONLY for a new path; the monitor treats a
      // duplicate edit to an already-touched path as churn.
      monitor.recordSignal("file_change", filePath);
    } else if (name === "Bash") {
      const cmdStr = typeof toolInput.command === "string" ? toolInput.command : "";
      // Detect a genuine commit (net forward motion). Exclude `--amend`
      // re-commits and `--dry-run` so churn cannot fake progress.
      const isCommit =
        /\bgit\s+commit\b/.test(cmdStr) && !/--amend\b/.test(cmdStr) && !/--dry-run\b/.test(cmdStr);
      if (isCommit) {
        monitor.recordSignal("commit");
      }
    }

    // Always record the distinct-tool signature for the churn detector. This
    // does NOT advance the no-progress window (Issue #3851) — but it DOES prove
    // to the monitor that the agent is alive (Issue #295).
    const inputPrefix = JSON.stringify(input ?? {}).slice(0, 200);
    monitor.recordSignal("distinct_tool", `${name}:${inputPrefix}`);
  }

  return calls.length;
}

/**
 * Fail-open guard for the runaway monitor (Issue #295): a blind monitor must
 * never shoot.
 *
 * When the parser has surfaced tool events (`parsedToolEventCount > 0`) yet the
 * monitor recorded ZERO signals (`signalsSeen === 0`), the parser→monitor feed
 * is disconnected — the agent is actively calling tools, the monitor simply
 * cannot see them. A runaway kill in that state is a false kill (the #262
 * class), so the caller must suppress it and log the discrepancy loudly.
 *
 * This is a defense-in-depth backstop, independent of the `recordToolCallProgress`
 * reconnection above: any future parser/stream-shape drift that silently
 * severs the feed can never again terminate a healthy stage — the worst case
 * degrades to "runaway detection is disabled for this stage," never "a working
 * stage is killed."
 */
export function isBlindMonitorKill(signalsSeen: number, parsedToolEventCount: number): boolean {
  return signalsSeen === 0 && parsedToolEventCount > 0;
}
