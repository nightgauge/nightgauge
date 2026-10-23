/**
 * PhaseTimeoutManager — Deterministic phase timeout and stale detection
 *
 * Monitors phase execution time within pipeline stages. For each phase that
 * starts, two independent timers are armed:
 *
 * 1. **Hard timeout** — fires `onPhaseTimeout` when the total phase wall-clock
 *    time exceeds the per-phase-type limit. The timeout value is read from
 *    `PhaseTimeoutConfig.defaults[phaseType]` and can be further overridden
 *    per stage via `PhaseTimeoutConfig.per_stage`.
 *
 * 2. **Stale detection** — fires `onPhaseStale` when no output activity is
 *    observed for `stale_detection_ms` milliseconds. Call `resetActivityTimer()`
 *    from the skill runner on every token or tool-call event to keep the stale
 *    timer from firing.
 *
 * Both timers are automatically cancelled when `completePhase()` is called.
 * Neither timer fires if `config.enabled` is `false`.
 *
 * ## Design notes
 *
 * Timeout enforcement is fully deterministic — no AI involvement. The manager
 * only fires events; it is the subscriber's responsibility (HeadlessOrchestrator)
 * to decide whether to cancel, retry, or escalate.
 *
 * @see Issue #1187 - Pipeline phase cancel/timeout monitoring
 * @see docs/ARCHITECTURE.md - Deterministic vs Probabilistic Architecture
 */

import * as vscode from "vscode";
import type { PipelineStage } from "@nightgauge/sdk";

// ---------------------------------------------------------------------------
// Public types
// ---------------------------------------------------------------------------

/**
 * Configuration for phase timeout and stale detection behaviour.
 *
 * Mirrors the `pipeline.phase_timeouts` section of `.nightgauge/config.yaml`.
 */
export interface PhaseTimeoutConfig {
  /** Master enable/disable switch — when false, no timers are armed. */
  enabled: boolean;

  /**
   * Duration in milliseconds without any output before `onPhaseStale` fires.
   * Default: 300 000 ms (5 minutes).
   */
  stale_detection_ms: number;

  /**
   * Maximum number of automatic retries before the phase is escalated.
   * The manager tracks this value but does not retry — the orchestrator does.
   */
  max_auto_retries: number;

  /**
   * Default hard timeout values per phase type (milliseconds).
   *
   * Resolved via `classifyPhase()` keyword matching before falling back to
   * per-stage overrides.
   */
  defaults: {
    /** Fast context-loading phases — default 2 minutes. */
    context: number;
    /** Code generation / implementation phases — default 10 minutes. */
    implementation: number;
    /** Build, test, and validation phases — default 8 minutes. */
    testing: number;
    /** Context write / planning output phases — default 3 minutes. */
    context_write: number;
  };

  /**
   * Optional per-stage, per-phase-name timeout overrides.
   *
   * Keys are pipeline stage names; values are maps of phase name → timeout ms.
   * Takes precedence over `defaults` when a matching entry is found.
   *
   * @example
   * ```yaml
   * per_stage:
   *   feature-dev:
   *     implement-core: 900000   # 15 min override for this specific phase
   * ```
   */
  per_stage: Record<string, Record<string, number>>;
}

/**
 * Broad category that a phase name maps to via keyword matching.
 *
 * Used to select the appropriate default hard timeout when no per-stage
 * override exists.
 */
export type PhaseType = "context" | "implementation" | "testing" | "context_write";

/**
 * Payload emitted by the `onPhaseTimeout` event.
 */
export interface PhaseTimeoutEvent {
  /** The pipeline stage in which the phase is running. */
  stage: PipelineStage;
  /** Human-readable phase name as emitted by the skill. */
  phaseName: string;
  /** Resolved phase type used to select the timeout threshold. */
  phaseType: PhaseType;
  /** Elapsed wall-clock time in milliseconds since the phase started. */
  elapsedMs: number;
}

/**
 * Payload emitted by the `onPhaseStale` event.
 */
export interface PhaseStaleEvent {
  /** The pipeline stage in which the phase is running. */
  stage: PipelineStage;
  /** Human-readable phase name as emitted by the skill. */
  phaseName: string;
  /** Resolved phase type used to select the timeout threshold. */
  phaseType: PhaseType;
  /** Duration in milliseconds since the last recorded output activity. */
  inactivityMs: number;
}

// ---------------------------------------------------------------------------
// Defaults
// ---------------------------------------------------------------------------

/**
 * Safe conservative defaults that work for all phase types.
 *
 * Values are calibrated against empirical pipeline run data; they represent
 * generous upper bounds rather than tight p90 budgets, since the purpose of
 * phase timeouts is to catch genuinely hung agents, not to terminate healthy
 * long-running phases prematurely.
 */
export const DEFAULT_PHASE_TIMEOUT_CONFIG: PhaseTimeoutConfig = {
  enabled: true,
  stale_detection_ms: 300_000, // 5 minutes
  max_auto_retries: 2,
  defaults: {
    context: 120_000, // 2 minutes
    implementation: 600_000, // 10 minutes
    testing: 480_000, // 8 minutes
    context_write: 180_000, // 3 minutes
  },
  per_stage: {},
};

// ---------------------------------------------------------------------------
// Keyword tables for phase classification
// ---------------------------------------------------------------------------

/**
 * Keyword → PhaseType mapping used by `classifyPhase()`.
 *
 * Each entry is a tuple of [substring, PhaseType]. The first match wins.
 * Keywords are matched against the lowercased phase name.
 */
const PHASE_KEYWORDS: ReadonlyArray<[string, PhaseType]> = [
  // implementation keywords — checked first so 'write-code' beats 'write'
  ["implement", "implementation"],
  ["create", "implementation"],
  ["write-code", "implementation"],
  ["produce", "implementation"],
  ["parallel", "implementation"],

  // context_write — 'write' must appear AFTER 'write-code' to avoid stealing it
  ["write-context", "context_write"],
  ["write-planning", "context_write"],
  ["write", "context_write"],
  ["save", "context_write"],
  ["output", "context_write"],
  ["record", "context_write"],
  ["persist", "context_write"],
  ["summarize", "context_write"],
  ["complete", "context_write"],
  ["signal", "context_write"],

  // testing keywords
  ["test", "testing"],
  ["validate", "testing"],
  ["verify", "testing"],
  ["build", "testing"],
  ["ralph", "testing"],

  // context keywords (catch-all fallback group — keep last)
  ["context", "context"],
  ["load", "context"],
  ["read", "context"],
  ["batch-detection", "context"],
];

// ---------------------------------------------------------------------------
// Standalone phase classification
// ---------------------------------------------------------------------------

/**
 * Classify a phase name into a PhaseType using keyword heuristics.
 *
 * Exported as a standalone function so callers (and tests) can use it
 * without instantiating a PhaseTimeoutManager.
 *
 * @param phaseName - Raw kebab-case phase name from skill output marker.
 * @returns The resolved PhaseType. Falls back to `'implementation'` for unknown names.
 */
export function classifyPhase(phaseName: string): PhaseType {
  const lower = phaseName.toLowerCase();
  for (const [keyword, type] of PHASE_KEYWORDS) {
    if (lower.includes(keyword)) {
      return type;
    }
  }
  // Default fallback — treat unknown phases as implementation (conservative timeout)
  return "implementation";
}

// ---------------------------------------------------------------------------
// Internal state shape
// ---------------------------------------------------------------------------

interface ActivePhaseState {
  stage: PipelineStage;
  phaseName: string;
  phaseType: PhaseType;
  startedAt: number;
  lastActivityAt: number;
  hardTimeoutHandle: ReturnType<typeof setTimeout>;
  staleTimeoutHandle: ReturnType<typeof setTimeout>;
}

// ---------------------------------------------------------------------------
// PhaseTimeoutManager
// ---------------------------------------------------------------------------

/**
 * PhaseTimeoutManager — arms and manages per-phase timeout timers.
 *
 * ### Lifecycle
 *
 * ```
 * manager.startPhase(stage, 'implement-core')
 *   // arms hard timeout + stale detection timer
 *
 * // on each skill output token / tool call:
 * manager.resetActivityTimer()
 *   // resets stale detection timer only
 *
 * manager.completePhase(stage, 'implement-core')
 *   // cancels both timers
 *
 * manager.dispose()
 *   // cancels all live timers and disposes event emitters
 * ```
 *
 * ### Event subscriptions
 *
 * ```typescript
 * manager.onPhaseTimeout(e => {
 *   // HeadlessOrchestrator: cancel stage, emit warning, optionally retry
 * });
 *
 * manager.onPhaseStale(e => {
 *   // HeadlessOrchestrator: show warning notification, optionally cancel
 * });
 * ```
 *
 * @see Issue #1187 - Pipeline phase cancel/timeout monitoring
 */
export class PhaseTimeoutManager implements vscode.Disposable {
  private readonly config: PhaseTimeoutConfig;

  // Active phases keyed by `${stage}::${phaseName}`
  private readonly activePhases = new Map<string, ActivePhaseState>();

  // Event emitters
  private readonly _onPhaseTimeout = new vscode.EventEmitter<PhaseTimeoutEvent>();
  private readonly _onPhaseStale = new vscode.EventEmitter<PhaseStaleEvent>();

  /**
   * Fired when a phase exceeds its hard timeout threshold.
   *
   * The orchestrator should treat this as a signal to cancel the current
   * stage process and surface an error to the user.
   */
  readonly onPhaseTimeout = this._onPhaseTimeout.event;

  /**
   * Fired when no output activity is observed for `stale_detection_ms`.
   *
   * The orchestrator may choose to warn the user or cancel/retry the stage.
   * Activity is reset by calling `resetActivityTimer()`.
   */
  readonly onPhaseStale = this._onPhaseStale.event;

  /**
   * @param config - Timeout configuration. Defaults to `DEFAULT_PHASE_TIMEOUT_CONFIG`
   *   when not provided.
   */
  constructor(config?: Partial<PhaseTimeoutConfig>) {
    this.config = config
      ? {
          ...DEFAULT_PHASE_TIMEOUT_CONFIG,
          ...config,
          defaults: {
            ...DEFAULT_PHASE_TIMEOUT_CONFIG.defaults,
            ...config.defaults,
          },
          per_stage: {
            ...DEFAULT_PHASE_TIMEOUT_CONFIG.per_stage,
            ...config.per_stage,
          },
        }
      : DEFAULT_PHASE_TIMEOUT_CONFIG;
  }

  // ---------------------------------------------------------------------------
  // Public API
  // ---------------------------------------------------------------------------

  /**
   * Classify a phase name into a broad {@link PhaseType} by keyword matching.
   *
   * The phase name is lowercased and tested against `PHASE_KEYWORDS` in order.
   * The first matching keyword wins. Falls back to `'context'` when no keyword
   * matches.
   *
   * @param phaseName - Raw phase name from the skill output marker.
   * @returns The resolved PhaseType.
   *
   * @example
   * ```typescript
   * classifyPhase('implement-core-logic') // → 'implementation'
   * classifyPhase('run-tests')            // → 'testing'
   * classifyPhase('write-context-file')   // → 'context_write'
   * classifyPhase('load-repo-map')        // → 'context'
   * classifyPhase('unknown-phase')        // → 'implementation' (fallback)
   * ```
   */
  classifyPhase(phaseName: string): PhaseType {
    return classifyPhase(phaseName);
  }

  /**
   * Resolve the hard timeout (ms) for a given stage + phase name combination.
   *
   * Resolution order:
   * 1. `per_stage[stage][phaseName]` — explicit override
   * 2. `defaults[phaseType]` — type-based default
   *
   * @param stage - The pipeline stage.
   * @param phaseName - The phase name.
   * @param phaseType - The resolved phase type.
   * @returns Timeout in milliseconds.
   */
  resolveHardTimeout(stage: PipelineStage, phaseName: string, phaseType: PhaseType): number {
    const perStageOverrides = this.config.per_stage[stage];
    if (perStageOverrides !== undefined && perStageOverrides[phaseName] !== undefined) {
      return perStageOverrides[phaseName];
    }
    return this.config.defaults[phaseType];
  }

  /**
   * Arm timeout and stale detection timers for a new phase.
   *
   * If a phase with the same `stage + phaseName` key is already active, its
   * existing timers are cancelled before new ones are armed (safe to call on
   * retry).
   *
   * No-op when `config.enabled` is `false`.
   *
   * @param stage - The pipeline stage in which the phase is executing.
   * @param phaseName - The phase name as emitted by the skill.
   */
  startPhase(stage: PipelineStage, phaseName: string): void {
    if (!this.config.enabled) {
      return;
    }

    const key = this._phaseKey(stage, phaseName);

    // Cancel all active phases for this stage — only one phase runs at a time
    // per stage. This ensures the previous phase's timers don't fire after
    // a new phase begins on the same stage.
    for (const [activeKey, state] of this.activePhases) {
      if (state.stage === stage && activeKey !== key) {
        this._cancelTimers(activeKey);
        this.activePhases.delete(activeKey);
      }
    }

    // Cancel any stale timers for the same key (safe retry path)
    this._cancelTimers(key);

    const phaseType = this.classifyPhase(phaseName);
    const hardTimeoutMs = this.resolveHardTimeout(stage, phaseName, phaseType);
    const now = Date.now();

    const hardTimeoutHandle = setTimeout(() => {
      this._onPhaseTimeout.fire({
        stage,
        phaseName,
        phaseType,
        elapsedMs: Date.now() - now,
      });
    }, hardTimeoutMs);

    const staleTimeoutHandle = this._armStaleTimer(stage, phaseName, phaseType, now);

    this.activePhases.set(key, {
      stage,
      phaseName,
      phaseType,
      startedAt: now,
      lastActivityAt: now,
      hardTimeoutHandle,
      staleTimeoutHandle,
    });
  }

  /**
   * Cancel all pending timers for the given phase.
   *
   * Should be called from `PhaseTracker.completeStagePhases()` (or equivalent)
   * when a phase marker transition or stage completion is observed.
   *
   * No-op when `config.enabled` is `false` or when the phase is not tracked.
   *
   * @param stage - The pipeline stage.
   * @param phaseName - The phase name to complete.
   */
  completePhase(stage: PipelineStage, phaseName: string): void {
    if (!this.config.enabled) {
      return;
    }
    const key = this._phaseKey(stage, phaseName);
    this._cancelTimers(key);
    this.activePhases.delete(key);
  }

  /**
   * Reset the stale detection timer for the currently active phase.
   *
   * Call this from the skill runner on every output token or tool-call event
   * to prevent false-positive stale alerts during normal (but verbose) phases.
   *
   * When multiple phases are active simultaneously (e.g., parallel execution),
   * activity is reset for **all** active phases since it is not possible to
   * attribute a token stream to a specific phase without richer context.
   *
   * No-op when `config.enabled` is `false` or when no phases are active.
   */
  resetActivityTimer(): void {
    if (!this.config.enabled || this.activePhases.size === 0) {
      return;
    }

    const now = Date.now();

    for (const [key, state] of this.activePhases) {
      // Cancel the existing stale timer
      clearTimeout(state.staleTimeoutHandle);

      // Update last activity timestamp
      state.lastActivityAt = now;

      // Arm a fresh stale timer
      const newStaleHandle = this._armStaleTimer(
        state.stage,
        state.phaseName,
        state.phaseType,
        now
      );

      this.activePhases.set(key, {
        ...state,
        lastActivityAt: now,
        staleTimeoutHandle: newStaleHandle,
      });
    }
  }

  /**
   * Cancel all active timers and dispose event emitters.
   *
   * Must be called when the extension deactivates or when the manager is no
   * longer needed to avoid timer leaks.
   */
  dispose(): void {
    for (const key of this.activePhases.keys()) {
      this._cancelTimers(key);
    }
    this.activePhases.clear();

    this._onPhaseTimeout.dispose();
    this._onPhaseStale.dispose();
  }

  // ---------------------------------------------------------------------------
  // Internal helpers
  // ---------------------------------------------------------------------------

  /**
   * Build the map key used to track an active phase.
   */
  private _phaseKey(stage: PipelineStage, phaseName: string): string {
    return `${stage}::${phaseName}`;
  }

  /**
   * Cancel both timers for a phase key and remove the entry.
   *
   * Does not delete from `activePhases` — callers decide whether to delete.
   */
  private _cancelTimers(key: string): void {
    const state = this.activePhases.get(key);
    if (!state) {
      return;
    }
    clearTimeout(state.hardTimeoutHandle);
    clearTimeout(state.staleTimeoutHandle);
  }

  /**
   * Arm a new stale detection timer and return its handle.
   *
   * The timer fires `onPhaseStale` after `stale_detection_ms` milliseconds.
   * The `lastActivityAt` reference time is captured at call time.
   *
   * @param stage - Pipeline stage.
   * @param phaseName - Phase name.
   * @param phaseType - Resolved phase type.
   * @param activityRef - Timestamp used to compute `inactivityMs` when the timer fires.
   * @returns The timer handle.
   */
  private _armStaleTimer(
    stage: PipelineStage,
    phaseName: string,
    phaseType: PhaseType,
    activityRef: number
  ): ReturnType<typeof setTimeout> {
    return setTimeout(() => {
      this._onPhaseStale.fire({
        stage,
        phaseName,
        phaseType,
        inactivityMs: Date.now() - activityRef,
      });
    }, this.config.stale_detection_ms);
  }
}
