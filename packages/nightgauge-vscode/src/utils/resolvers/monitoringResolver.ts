/**
 * Monitoring and observability configuration resolvers extracted from incrediConfig.ts.
 *
 * Covers stall detection, alerting, experiments, MCP tools, audit, supercharge
 * mode, large diff threshold, and context file size alerting.
 *
 * @see Issue #2742 - Refactor VSCode incrediConfig.ts into focused domain modules
 */

import * as fs from "node:fs";
import * as path from "node:path";
import * as vscode from "vscode";
import type { PipelineStage } from "@nightgauge/sdk";
import type { AuditConfig } from "@nightgauge/sdk";
import { resolveConfigPathSync, logDeprecationWarning } from "../configPathResolver";
import { readEffectiveConfigTextSync } from "../mergedConfigReader";
import type { DefaultModel } from "./modelResolver";
import type { ClaudeEffort } from "./stageResolver";
import type { ExecutionAdapter } from "../../config/schema";
import { CodexModelCatalogService } from "../../services/CodexModelCatalogService";
import type { ProgressMonitorConfig } from "../progressMonitor";

// ============================================================================
// Stall Detection (Issue #769, #1620, #2654, #2656)
// ============================================================================

/**
 * Default per-stage stall warning thresholds in seconds.
 *
 * These are the initial warning thresholds — follow-up warnings use
 * escalating intervals (2x, 3x, 4x of the threshold).
 *
 * @see Issue #769 - Configurable stall thresholds
 */
export const DEFAULT_STALL_THRESHOLDS: Record<string, number> = {
  "issue-pickup": 180,
  "feature-planning": 180,
  "feature-dev": 600,
  "feature-validate": 300,
  "pr-create": 180,
  "pr-merge": 420,
};

/**
 * Get per-stage stall warning thresholds from config or environment.
 *
 * Priority (per stage):
 * 1. Environment variable: NIGHTGAUGE_PIPELINE_STALL_THRESHOLD_{STAGE_UPPER}
 *    (e.g., NIGHTGAUGE_PIPELINE_STALL_THRESHOLD_FEATURE_DEV=600)
 * 2. Config file: pipeline.stall_thresholds.{stage}
 * 3. Default from DEFAULT_STALL_THRESHOLDS
 *
 * @param workspaceRoot - Workspace root path (optional, auto-detected if not provided)
 * @returns Record of stage name to threshold in seconds
 *
 * @see Issue #769 - Configurable stall thresholds
 */
export function getStallThresholds(workspaceRoot?: string): Record<string, number> {
  const thresholds: Record<string, number> = { ...DEFAULT_STALL_THRESHOLDS };

  // Check environment variable overrides per stage
  for (const stage of Object.keys(DEFAULT_STALL_THRESHOLDS)) {
    const envKey = `NIGHTGAUGE_PIPELINE_STALL_THRESHOLD_${stage.toUpperCase().replace(/-/g, "_")}`;
    const envValue = process.env[envKey];
    if (envValue) {
      const parsed = Number.parseInt(envValue, 10);
      if (!Number.isNaN(parsed) && parsed >= 30) {
        thresholds[stage] = parsed;
      }
    }
  }

  // Get workspace root
  const root = workspaceRoot ?? vscode.workspace.workspaceFolders?.[0]?.uri.fsPath;
  if (!root) {
    return thresholds;
  }

  try {
    // Resolve config path with fallback to legacy
    const pathResult = resolveConfigPathSync(root);
    if (!pathResult.exists) {
      return thresholds;
    }

    // Log deprecation warning if using legacy path
    if (pathResult.isLegacy) {
      logDeprecationWarning(pathResult.path);
    }

    // Read and parse config file (simple line parsing)
    const configContent = readEffectiveConfigTextSync(pathResult);
    const lines = configContent.split("\n");
    let inPipeline = false;
    let inStallThresholds = false;

    for (const line of lines) {
      const trimmed = line.trim();

      // Detect pipeline: section
      if (trimmed === "pipeline:") {
        inPipeline = true;
        continue;
      }

      // Detect stall_thresholds: subsection under pipeline
      if (inPipeline && trimmed === "stall_thresholds:") {
        inStallThresholds = true;
        continue;
      }

      // Exit sections on new top-level key
      if (trimmed && !trimmed.startsWith("#") && /^[a-z_]+:/.test(trimmed)) {
        if (!line.startsWith(" ")) {
          inPipeline = false;
          inStallThresholds = false;
        } else if (line.match(/^ {2}[a-z_]+:/)) {
          // New pipeline subsection (not stall_thresholds)
          inStallThresholds = false;
        }
      }

      // Parse stall threshold values (e.g., "feature-dev: 600")
      if (inStallThresholds) {
        const match = trimmed.match(/^([a-z][-a-z]*):\s*(\d+)/);
        if (match) {
          const [, stage, value] = match;
          const parsed = Number.parseInt(value, 10);
          // Only apply config value if no env var override for this stage
          const envKey = `NIGHTGAUGE_PIPELINE_STALL_THRESHOLD_${stage.toUpperCase().replace(/-/g, "_")}`;
          if (!process.env[envKey] && !Number.isNaN(parsed) && parsed >= 30) {
            thresholds[stage] = parsed;
          }
        }
      }
    }

    return thresholds;
  } catch (error) {
    console.error("Failed to read stall thresholds from nightgauge config:", error);
    return thresholds;
  }
}

/**
 * Per-stage default overrides for the stall kill multiplier (#3020).
 *
 * The global default is 8 (see DEFAULT_KILL_MULTIPLIER below). Some stages have
 * a tighter expected runtime profile and a longer multiplier just burns money
 * on a stuck subagent. Listed stages here use the per-stage value instead.
 *
 *   - feature-validate: 4 (was 8 → 40 min kill window). The original incident
 *     spent $18.96 on a feature-validate run that spun for 40 min before the
 *     kill fired. The healthy 95th-percentile of feature-validate is ~5 min;
 *     20 min (4 × 300s threshold) is plenty of headroom.
 *
 *   - pr-create: 4 (was 8 → 24 min kill window). Follow-up incident on
 *     issue #291 spent $6.29 on pr-create stalling for 24 min on a 21-line
 *     change. Healthy pr-create is <2 min (single `gh pr create` + label edits).
 *     12 min (4 × 180s threshold) absorbs slow `gh pr comment` blocks while
 *     killing genuinely stuck subagents in half the previous time.
 *
 * Stages not listed inherit the global default. Per-stage env/config overrides
 * (`NIGHTGAUGE_PIPELINE_STALL_KILL_MULTIPLIER_FEATURE_VALIDATE`,
 *  `pipeline.stall_kill_multipliers.<stage>`) win over both.
 */
const DEFAULT_PER_STAGE_KILL_MULTIPLIERS: Record<string, number> = {
  "feature-validate": 4,
  "pr-create": 4,
};

/**
 * Get the stall kill multiplier from config — for a specific stage when known.
 * When a stage exceeds this multiplier × its stall threshold, the process is
 * forcibly terminated. Returns 0 if auto-kill is disabled.
 *
 * Resolution order (per stage):
 *   1. Env var: NIGHTGAUGE_PIPELINE_STALL_KILL_MULTIPLIER_<STAGE_UPPER>
 *   2. Env var: NIGHTGAUGE_PIPELINE_STALL_KILL_MULTIPLIER (global)
 *   3. Config: pipeline.stall_kill_multipliers.<stage>
 *   4. Config: pipeline.stall_kill_multiplier (global)
 *   5. DEFAULT_PER_STAGE_KILL_MULTIPLIERS[stage]
 *   6. Global default (8).
 *
 * Default: 8. Gives generous headroom for slow-but-healthy stages. Derived
 * per-stage kill times are:
 *   - issue-pickup        180s × 8 = 24 min
 *   - feature-planning    180s × 8 = 24 min
 *   - feature-dev         600s × 8 = 80 min
 *   - feature-validate    300s × 4 = 20 min  (#3020 — was 40 min)
 *   - pr-create           180s × 4 = 12 min  (#3020 — was 24 min)
 *   - pr-merge            420s × 8 = 56 min
 *
 * Historically the global was 5, which produced false-positive kills on healthy
 * runs: 914s pr-create (≥15 min kill), 2111s pr-merge (≥35 min), 3012s
 * feature-dev (≥50 min), 1533s feature-validate (≥25 min). Raising to 8
 * absorbs these without neutering the guard on truly stuck subagents.
 *
 * @see Issue #1620 - Subagent stall auto-kill
 * @see Issue #3020 - Per-stage tightening for feature-validate
 */
export function getStallKillMultiplier(workspaceRoot?: string, stage?: string): number {
  // 1. Per-stage env override wins over everything.
  if (stage) {
    const stageEnvKey = `NIGHTGAUGE_PIPELINE_STALL_KILL_MULTIPLIER_${stage.toUpperCase().replace(/-/g, "_")}`;
    const stageEnvValue = process.env[stageEnvKey];
    if (stageEnvValue) {
      const parsed = Number.parseInt(stageEnvValue, 10);
      if (!Number.isNaN(parsed) && parsed >= 0) {
        return parsed;
      }
    }
  }
  return getStallKillMultiplierGlobal(workspaceRoot, stage);
}

function getStallKillMultiplierGlobal(workspaceRoot?: string, stage?: string): number {
  const DEFAULT_KILL_MULTIPLIER = 8;
  const perStageDefault = stage ? DEFAULT_PER_STAGE_KILL_MULTIPLIERS[stage] : undefined;

  // Environment variable override (global). Per-stage env var was already
  // consulted in the public getStallKillMultiplier() entry point.
  const envValue = process.env.NIGHTGAUGE_PIPELINE_STALL_KILL_MULTIPLIER;
  if (envValue) {
    const parsed = Number.parseInt(envValue, 10);
    if (!Number.isNaN(parsed) && parsed >= 0) {
      return parsed;
    }
  }

  const root = workspaceRoot ?? vscode.workspace.workspaceFolders?.[0]?.uri.fsPath;
  const fallback = perStageDefault ?? DEFAULT_KILL_MULTIPLIER;
  if (!root) {
    return fallback;
  }

  try {
    const pathResult = resolveConfigPathSync(root);
    if (!pathResult.exists) {
      return fallback;
    }

    const configContent = readEffectiveConfigTextSync(pathResult);
    const lines = configContent.split("\n");
    let inPipeline = false;
    let inStageMap = false;
    let globalFromYaml: number | undefined;
    let perStageFromYaml: number | undefined;

    for (const line of lines) {
      const trimmed = line.trim();

      if (trimmed === "pipeline:") {
        inPipeline = true;
        continue;
      }

      // Detect stall_kill_multipliers: subsection (#3020)
      if (inPipeline && trimmed === "stall_kill_multipliers:") {
        inStageMap = true;
        continue;
      }

      // Exit sections on new top-level key
      if (trimmed && !trimmed.startsWith("#") && /^[a-z_]+:/.test(trimmed)) {
        if (!line.startsWith(" ")) {
          inPipeline = false;
          inStageMap = false;
        } else if (line.match(/^ {2}[a-z_]+:/)) {
          // New pipeline subsection ends the stage map.
          inStageMap = false;
        }
      }

      // Per-stage YAML override (#3020): pipeline.stall_kill_multipliers.<stage>: N
      if (inStageMap && stage) {
        const stageMatch = trimmed.match(/^([a-z][-a-z]*):\s*(\d+)/);
        if (stageMatch && stageMatch[1] === stage) {
          const parsed = Number.parseInt(stageMatch[2], 10);
          if (!Number.isNaN(parsed) && parsed >= 0) {
            perStageFromYaml = parsed;
          }
        }
      }

      // Global YAML value
      if (inPipeline && !inStageMap) {
        const match = trimmed.match(/^stall_kill_multiplier:\s*(\d+)/);
        if (match) {
          const parsed = Number.parseInt(match[1], 10);
          if (!Number.isNaN(parsed) && parsed >= 0) {
            globalFromYaml = parsed;
          }
        }
      }
    }

    // Resolution: per-stage YAML > global YAML > per-stage default > global default.
    if (perStageFromYaml !== undefined) return perStageFromYaml;
    if (globalFromYaml !== undefined) return globalFromYaml;
    return fallback;
  } catch {
    return fallback;
  }
}

// ============================================================================
// Stall Idle Override (Issue #3484)
// ============================================================================

/**
 * Get the absolute idle-kill threshold in milliseconds from config or env.
 *
 * When set, this value replaces the computed `threshold × multiplier` as the
 * idle-kill gate in skillRunner.ts. When unset (returns undefined), the
 * existing multiplier-derived value is used unchanged.
 *
 * Resolution order:
 *   1. Env var: NIGHTGAUGE_PIPELINE_STALL_IDLE_MS (global)
 *   2. Config: pipeline.stall_idle_ms (global — no per-stage YAML for now)
 *   3. Returns undefined → caller uses threshold × multiplier
 *
 * @param workspaceRoot - Workspace root path (optional, auto-detected if not provided)
 * @returns Absolute idle threshold in ms, or undefined if not configured
 *
 * @see Issue #3484 — Fix model stall after tool result
 */
export function getStallIdleMs(workspaceRoot?: string): number | undefined {
  const envValue = process.env.NIGHTGAUGE_PIPELINE_STALL_IDLE_MS;
  if (envValue) {
    const parsed = Number.parseInt(envValue, 10);
    if (!Number.isNaN(parsed) && parsed >= 0) {
      return parsed;
    }
  }

  const root = workspaceRoot ?? vscode.workspace.workspaceFolders?.[0]?.uri.fsPath;
  if (!root) {
    return undefined;
  }

  try {
    const pathResult = resolveConfigPathSync(root);
    if (!pathResult.exists) {
      return undefined;
    }

    if (pathResult.isLegacy) {
      logDeprecationWarning(pathResult.path);
    }

    const configContent = readEffectiveConfigTextSync(pathResult);
    const lines = configContent.split("\n");
    let inPipeline = false;

    for (const line of lines) {
      const trimmed = line.trim();

      if (trimmed === "pipeline:") {
        inPipeline = true;
        continue;
      }

      if (trimmed && !trimmed.startsWith("#") && /^[a-z_]+:/.test(trimmed)) {
        if (!line.startsWith(" ")) {
          inPipeline = false;
        }
      }

      if (inPipeline) {
        const match = trimmed.match(/^stall_idle_ms:\s*(\d+)/);
        if (match) {
          const parsed = Number.parseInt(match[1], 10);
          if (!Number.isNaN(parsed) && parsed >= 0) {
            return parsed;
          }
        }
      }
    }

    return undefined;
  } catch {
    return undefined;
  }
}

/**
 * Default idle budget (ms) allowed after ANY rate-limit signal before the
 * quota fast-fail fires. 15 minutes — far below a stage's normal idle budget
 * (feature-dev default ≈ 80 min) but well above any legitimate silent pause,
 * so it only trips when the CLI is genuinely wedged behind a quota wall.
 *
 * @see Issue #3702 — soft quota signal (allowed_warning) preceded an 81-min hang.
 */
export const DEFAULT_QUOTA_SIGNAL_IDLE_MS = 900_000;

/**
 * Get the idle budget (ms) allowed after a rate-limit signal before the quota
 * fast-fail kills the stage.
 *
 * Unlike the aggressive 120s fast-fail (which only fires on `status: "limited"`),
 * this budget applies after ANY observed rate-limit signal — including a soft
 * `allowed_warning` that precedes the CLI hanging on a later hard-limited request
 * that never streams. The skillRunner caps it below the stage's normal idle
 * budget so a quota signal can only make a stage fail *faster*, never slower.
 *
 * Resolution order:
 *   1. Env var: NIGHTGAUGE_PIPELINE_QUOTA_SIGNAL_IDLE_MS
 *   2. Config: pipeline.quota_signal_idle_ms
 *   3. DEFAULT_QUOTA_SIGNAL_IDLE_MS (15 min)
 *
 * @param workspaceRoot - Workspace root path (optional, auto-detected if not provided)
 * @returns Idle budget in ms after a rate-limit signal
 * @see Issue #3702
 */
export function getQuotaSignalIdleMs(workspaceRoot?: string): number {
  const envValue = process.env.NIGHTGAUGE_PIPELINE_QUOTA_SIGNAL_IDLE_MS;
  if (envValue) {
    const parsed = Number.parseInt(envValue, 10);
    if (!Number.isNaN(parsed) && parsed >= 0) {
      return parsed;
    }
  }

  const root = workspaceRoot ?? vscode.workspace.workspaceFolders?.[0]?.uri.fsPath;
  if (!root) {
    return DEFAULT_QUOTA_SIGNAL_IDLE_MS;
  }

  try {
    const pathResult = resolveConfigPathSync(root);
    if (!pathResult.exists) {
      return DEFAULT_QUOTA_SIGNAL_IDLE_MS;
    }

    if (pathResult.isLegacy) {
      logDeprecationWarning(pathResult.path);
    }

    const configContent = readEffectiveConfigTextSync(pathResult);
    const lines = configContent.split("\n");
    let inPipeline = false;

    for (const line of lines) {
      const trimmed = line.trim();

      if (trimmed === "pipeline:") {
        inPipeline = true;
        continue;
      }

      if (trimmed && !trimmed.startsWith("#") && /^[a-z_]+:/.test(trimmed)) {
        if (!line.startsWith(" ")) {
          inPipeline = false;
        }
      }

      if (inPipeline) {
        const match = trimmed.match(/^quota_signal_idle_ms:\s*(\d+)/);
        if (match) {
          const parsed = Number.parseInt(match[1], 10);
          if (!Number.isNaN(parsed) && parsed >= 0) {
            return parsed;
          }
        }
      }
    }

    return DEFAULT_QUOTA_SIGNAL_IDLE_MS;
  } catch {
    return DEFAULT_QUOTA_SIGNAL_IDLE_MS;
  }
}

/**
 * Decide whether the quota fast-fail should fire on the current stall tick.
 *
 * Two independent triggers, both gated on idle-since-the-last-rate-limit-signal:
 *  1. Aggressive: a hard `status: "limited"` signal + idle ≥ exhaustedFastFailIdleMs (120s).
 *  2. Soft: ANY rate-limit signal seen + idle ≥ quotaSignalIdleBudgetMs (capped
 *     below the stage's normal idle budget). Catches a soft `allowed_warning`
 *     that precedes the CLI hanging on a later hard-limited request (#3702).
 *
 * Extracted as a pure function so this cost-sensitive decision is unit-testable
 * without spawning a subprocess.
 *
 * @see Issue #3702
 */
export function shouldQuotaFastFail(params: {
  /** true when the last rate-limit event had status === "limited". */
  quotaExhaustedSignalActive: boolean;
  /**
   * true when the last rate-limit event carried a quota-PRESSURE status
   * (`allowed_warning` or `limited`). A plain `allowed` event is steady-state
   * telemetry and must NOT set this — see `isQuotaPressureSignal` / Issue #3825.
   */
  anyQuotaSignalSeen: boolean;
  /** Idle ms measured since the last rate-limit signal (min of idle and signal age). */
  idleSinceQuotaSignalMs: number;
  /** Aggressive `status: "limited"` fast-fail threshold (default 120s). */
  exhaustedFastFailIdleMs: number;
  /** Soft-signal idle budget, already capped below the stage's stallKillMs. */
  quotaSignalIdleBudgetMs: number;
}): boolean {
  const {
    quotaExhaustedSignalActive,
    anyQuotaSignalSeen,
    idleSinceQuotaSignalMs,
    exhaustedFastFailIdleMs,
    quotaSignalIdleBudgetMs,
  } = params;
  if (quotaExhaustedSignalActive && idleSinceQuotaSignalMs >= exhaustedFastFailIdleMs) {
    return true;
  }
  if (anyQuotaSignalSeen && idleSinceQuotaSignalMs >= quotaSignalIdleBudgetMs) {
    return true;
  }
  return false;
}

// ============================================================================
// Stage Hard Cap Config (Issue #2871)
// ============================================================================

/**
 * Default per-stage hard caps in seconds.
 *
 * A hard cap overrides stall_kill_multiplier: the process is killed at this
 * absolute time regardless of the multiplier. 0 = disabled (no hard cap).
 *
 * Most stages have no default hard cap. The prior `pr-create: 300` default was
 * removed in Issue #2982 — it negated Issue #2973's fix by force-killing
 * opus/supercharge pr-create runs on large changesets after 5 minutes, even
 * though pr-create legitimately has long silent windows during `gh pr
 * create`, `gh pr comment`, and label edits (single Bash tool_use blocks
 * that produce no stdout until they complete). The calibrated stall-kill
 * (`max(max*2, warn*3)`, ~35 min from 282 historical runs) is the correct
 * bound. Users who want an explicit cap can still set one via env var or
 * `pipeline.stage_hard_caps.<stage>` in config.yaml.
 *
 * `feature-dev: 5400` (90 min) is a deliberately GENEROUS last-resort backstop
 * added in Issue #3851 after #3811's feature-dev churned for two 75/89-min runs
 * burning $112. CRITICAL: this cap is **progress-gated** in skillRunner — it
 * ONLY kills when the absolute elapsed cap is reached AND the ProgressMonitor
 * reports no productive progress (no commits / new-file writes / phase markers /
 * CI progress) in the no-progress window. A feature-dev stage steadily
 * committing at 91 minutes is NEVER killed (that would re-introduce the
 * #2982/#3840 false-kill class). 90 min is ~2.6× the calibrated ~35-min
 * stall-kill, so it never pre-empts a healthy long run.
 *
 * @see Issue #2871 — original hard cap
 * @see Issue #2973 — removed pr-create heartbeat early-kill
 * @see Issue #2982 — removed pr-create default hard cap
 * @see Issue #3851 — progress-gated feature-dev backstop (#3811 $112 runaway)
 */
export const DEFAULT_STAGE_HARD_CAPS: Record<string, number> = {
  "feature-dev": 5400, // 90 min — last-resort, progress-gated in skillRunner
};

/**
 * Get the hard cap kill time (in ms) for a given stage.
 *
 * Priority:
 * 1. Env var: NIGHTGAUGE_PIPELINE_STAGE_HARD_CAP_<STAGE_UPPER> (e.g., PR_CREATE=300)
 * 2. Config file: pipeline.stage_hard_caps.<stage>: 300
 * 3. Default from DEFAULT_STAGE_HARD_CAPS (0 for stages not listed)
 *
 * Returns 0 when no hard cap applies (multiplier-based kill is used as-is).
 *
 * @see Issue #2871 — pr-create stall diagnosis and hard cap
 */
export function getStageHardCapMs(stage: string, workspaceRoot?: string): number {
  const defaultCapSec = DEFAULT_STAGE_HARD_CAPS[stage] ?? 0;

  // Environment variable override
  const envKey = `NIGHTGAUGE_PIPELINE_STAGE_HARD_CAP_${stage.toUpperCase().replace(/-/g, "_")}`;
  const envValue = process.env[envKey];
  if (envValue) {
    const parsed = Number.parseInt(envValue, 10);
    if (!Number.isNaN(parsed) && parsed >= 0) {
      return parsed * 1000;
    }
  }

  const root = workspaceRoot ?? vscode.workspace.workspaceFolders?.[0]?.uri.fsPath;
  if (!root) {
    return defaultCapSec * 1000;
  }

  try {
    const pathResult = resolveConfigPathSync(root);
    if (!pathResult.exists) {
      return defaultCapSec * 1000;
    }

    const configContent = readEffectiveConfigTextSync(pathResult);
    const lines = configContent.split("\n");
    let inPipeline = false;
    let inHardCaps = false;

    for (const line of lines) {
      const trimmed = line.trim();

      if (trimmed === "pipeline:") {
        inPipeline = true;
        continue;
      }

      if (inPipeline && trimmed === "stage_hard_caps:") {
        inHardCaps = true;
        continue;
      }

      // Exit sections on new top-level key
      if (trimmed && !trimmed.startsWith("#") && /^[a-z_]+:/.test(trimmed)) {
        if (!line.startsWith(" ")) {
          inPipeline = false;
          inHardCaps = false;
        } else if (line.match(/^ {2}[a-z_]+:/)) {
          // New pipeline subsection — exit hard_caps
          inHardCaps = false;
        }
      }

      if (inHardCaps) {
        // Stage entries use quoted or unquoted keys: "pr-create": 300
        const match = trimmed.match(/^["']?([a-z][-a-z]*)["']?\s*:\s*(\d+)/);
        if (match && match[1] === stage) {
          const parsed = Number.parseInt(match[2], 10);
          if (!Number.isNaN(parsed) && parsed >= 0) {
            return parsed * 1000;
          }
        }
      }
    }

    return defaultCapSec * 1000;
  } catch {
    return defaultCapSec * 1000;
  }
}

// ============================================================================
// Stage Cost Caps (Issue #3002)
// ============================================================================

/**
 * Default per-stage cost caps in USD.
 *
 * When a stage's accumulated cost (`tokenAccumulator.getTotal().costUsd`)
 * exceeds the configured cap, the subagent is forcibly terminated using the
 * same SIGTERM/SIGKILL sequence as the stall-kill path. A missing entry or
 * a value of `0` means uncapped for that stage.
 *
 * Distinct from `BudgetEnforcer` (`pipeline.budget_mode` / `budget_grace_percent`),
 * which uses an estimate-vs-actual flow with a grace buffer. This cap is a
 * hard, deterministic ceiling with no grace and no prompt — failures are
 * terminal (no `budget-overrun-{N}.json` retry context written).
 *
 * Calibration (Issue #3208, 2026-05-06):
 *   Base caps are p95 × 2 (rounded to the nearest dollar) over the last 90
 *   days of `complete | cancelled` runs in `.nightgauge/pipeline/history`.
 *   The factor of 2 gives ~50% headroom above the typical-but-real productive
 *   cost — comfortably above the median run yet tight enough to cut off
 *   runaways well before $200+ outliers like the 2026-05-04 incident.
 *
 *   The base value is multiplied at runtime by `COST_CAP_MODEL_SCALE` so
 *   heavier model/effort combos (e.g. opus:high = 5.0×) get proportional
 *   headroom without separate per-model rows. See `getEffectiveStageCostCap`.
 *
 *   Derived from `npx tsx scripts/audit-stage-cost-distribution.ts` (n shown
 *   per stage; window = last 90 days):
 *
 *     stage             n     p50      p95      p99     p95×2   default
 *     issue-pickup     561   $0.33   $0.68    $0.82     $1      $1
 *     feature-planning 733   $1.12   $2.97    $4.44     $6      $6
 *     feature-dev      848   $2.51  $11.25   $24.84    $23     $23  (was $5)
 *     feature-validate 755   $0.91   $3.72   $11.86     $7      $7
 *     pr-create        828   $0.20   $1.56    $2.59     $3      $3
 *     pr-merge         841   $0.25   $2.25    $4.72     $4      $4
 *
 *   Stages with fewer than 20 samples in the window keep their previous
 *   default. All stages above had n ≥ 561.
 *
 * @see Issue #3002 — Per-stage cost circuit breaker (mechanism)
 * @see Issue #3180 — Mode-aware multiplier (`COST_CAP_MODEL_SCALE`)
 * @see Issue #3208 — Tune per-stage cost cap defaults (current calibration)
 */
export const DEFAULT_STAGE_COST_CAPS: Record<string, number> = {
  "issue-pickup": 1.0,
  "feature-planning": 6.0,
  "feature-dev": 23.0,
  "feature-validate": 7.0,
  "pr-create": 3.0,
  "pr-merge": 4.0,
};

/**
 * Mode-aware budget multiplier table.
 *
 * Base limits in `DEFAULT_STAGE_COST_CAPS` and `DEFAULT_SIZE_AWARE_BUDGETS`
 * are calibrated for Sonnet at medium effort. When the model router escalates
 * to Opus / high-effort (e.g. via `Maximum` performance-mode) per-message and
 * per-second cost climb steeply, which trips the same limits on legitimate
 * work. The multiplier widens both the cost-cap kill (#3002, #3180) and the
 * BudgetEnforcer hard-mode terminate path so heavier modes get proportional
 * headroom while runaways still terminate.
 *
 * Recalibration history:
 *   - #3180 first introduced the multiplier, anchored to a single $9.02
 *     incident (#3089) with a 3.0× factor for `opus:high` → $15 effective cap
 *     on a $5 base. That worked for ~$9 spends but undersized real MAXIMUM
 *     mode runs.
 *   - This bump (post-2026-05-04) anchors to two confirmed real-world
 *     terminations: feature-dev hit $25.31 final / $23.03 at-kill against the
 *     $15 cap, and pr-create hit $5.74 against the $4.50 BudgetEnforcer
 *     limit (size M, generous preset, hard mode + 50% grace). With
 *     `opus:high` = 5.0× the feature-dev effective cap becomes $25 (just at
 *     the observed peak) and the pr-create effective limit becomes
 *     $4.50 × 5.0 = $22.50 — comfortably above $5.74. We do NOT bump the
 *     base `opus` (medium effort) above 3.5× because medium-effort runs are
 *     considerably cheaper and a too-wide ceiling masks runaways.
 *
 * Each value is overridable via env var
 * `NIGHTGAUGE_BUDGET_MODEL_SCALE_<FAMILY>[_<EFFORT>]` (uppercase, eg
 * `NIGHTGAUGE_BUDGET_MODEL_SCALE_OPUS_HIGH=6.0`) for fast tuning without
 * a code change.
 *
 * Lookup order in {@link getCostCapModelScale}:
 *   1. env override `<FAMILY>_<EFFORT>` exact match
 *   2. `<model>:<effort>` table exact match
 *   3. env override `<FAMILY>` family match
 *   4. `<model>` family (haiku/sonnet/opus) match
 *   5. 1.0 fallback
 */
export const COST_CAP_MODEL_SCALE: Record<string, number> = {
  "haiku:high": 1.0,
  haiku: 1.0,
  "sonnet:high": 1.3,
  sonnet: 1.0,
  "opus:high": 5.0,
  // xhigh thinks longer than high on the same pricing, so it gets modestly
  // more headroom than the high key — never less, or a legitimately deeper
  // run would be killed earlier than a shallower one (#73).
  "opus:xhigh": 6.0,
  opus: 3.5,
  // Fable 5 is the premium frontier tier at ~2× Opus pricing, so its cost-cap
  // headroom is scaled ~2× the Opus values — otherwise a Sonnet-calibrated cap
  // would kill legitimate `frontier`-mode Fable runs prematurely.
  "fable:high": 10.0,
  "fable:xhigh": 12.0,
  fable: 7.0,
};

/**
 * Read an env-var override for the multiplier.
 *
 * Accepts `NIGHTGAUGE_BUDGET_MODEL_SCALE_<FAMILY>[_<EFFORT>]` and
 * returns the parsed positive float, or undefined when missing/invalid.
 * Pulled out so both the cost-cap and BudgetEnforcer paths share the exact
 * same precedence rules.
 */
function readBudgetScaleEnvOverride(family: string, effort?: string): number | undefined {
  const suffix = effort ? `${family.toUpperCase()}_${effort.toUpperCase()}` : family.toUpperCase();
  const raw = process.env[`NIGHTGAUGE_BUDGET_MODEL_SCALE_${suffix}`];
  if (raw === undefined || raw === "") return undefined;
  const parsed = Number.parseFloat(raw);
  if (Number.isNaN(parsed) || parsed <= 0) return undefined;
  return parsed;
}

/**
 * Resolve the multiplier for a given (model, effort) pair.
 *
 * `model` is matched on the family name (haiku / sonnet / opus / fable) anywhere
 * in the string so `claude-sonnet-4-6` and `sonnet`, or `claude-fable-5` and
 * `fable`, both resolve. Unknown models (e.g. external `gpt-5`, lm-studio)
 * return 1.0 — defensive default that preserves the configured cap as the
 * literal ceiling.
 *
 * Env-var overrides at every step of the lookup so a user can experimentally
 * widen `opus:high` without recompiling.
 */
export function getCostCapModelScale(model?: string, effort?: string): number {
  if (!model) return 1.0;
  const m = model.toLowerCase();
  const e = (effort ?? "").toLowerCase();

  let family: "haiku" | "sonnet" | "opus" | "fable" | undefined;
  if (m.includes("haiku")) family = "haiku";
  else if (m.includes("sonnet")) family = "sonnet";
  else if (m.includes("opus")) family = "opus";
  else if (m.includes("fable")) family = "fable";
  if (!family) return 1.0;

  if (e !== "") {
    const envEffort = readBudgetScaleEnvOverride(family, e);
    if (envEffort !== undefined) return envEffort;
    const keyed = COST_CAP_MODEL_SCALE[`${family}:${e}`];
    if (typeof keyed === "number") return keyed;
  }
  const envFam = readBudgetScaleEnvOverride(family);
  if (envFam !== undefined) return envFam;
  const fam = COST_CAP_MODEL_SCALE[family];
  return typeof fam === "number" ? fam : 1.0;
}

/**
 * Default mode multiplier table (Issue #3217).
 *
 * `efficiency` halves the calibrated baseline because cheaper models on a
 * cheaper effort tier should not get the same headroom Sonnet/medium gets.
 * `elevated` is identity (1.0×) — a guarantee that default users see no
 * change in cost-cap math vs. pre-#3217 behavior. `maximum` doubles the
 * baseline; combined with the existing `opus:high` 5.0× scale that yields a
 * 10× ceiling on top of the calibrated base, which lines up with the
 * `costHint: "≈ baseline × 4"` MAXIMUM claim plus headroom for the
 * observe-only Go path that catches runaways past the cap.
 *
 * Each value is overridable via:
 *   1. Env var `NIGHTGAUGE_COST_CAP_MODE_MULTIPLIER_<MODE>`
 *      (uppercase, e.g. `..._MAXIMUM=3.0`)
 *   2. Config file `pipeline.cost_cap_mode_multiplier.<mode>`
 *   3. This default table
 */
export const DEFAULT_COST_CAP_MODE_MULTIPLIER: Record<PerformanceMode, number> = {
  efficiency: 0.5,
  elevated: 1.0,
  maximum: 2.0,
  // Frontier runs Fable (~2× Opus) on the reasoning stages, so its per-stage
  // cost cap needs more headroom than maximum's Opus envelope.
  frontier: 3.0,
};

/**
 * Resolve the cost-cap multiplier for a given performance mode (Issue #3217).
 *
 * Composes multiplicatively atop the existing `(model, effort)` scale —
 * see {@link getEffectiveStageCostCap}. When `mode` is `undefined` returns
 * `1.0` so callers that don't yet thread mode through (or where mode is not
 * applicable) see identical math to pre-#3217 behavior.
 *
 * Lookup order:
 *   1. Env override `NIGHTGAUGE_COST_CAP_MODE_MULTIPLIER_<MODE>`
 *   2. Config `pipeline.cost_cap_mode_multiplier.<mode>` (line-by-line YAML
 *      parser, mirrors {@link getStageCostCapUsd})
 *   3. {@link DEFAULT_COST_CAP_MODE_MULTIPLIER} for the resolved mode
 *
 * Invalid env / config values (NaN, ≤ 0) fall through to the next layer so a
 * typo never zeroes out the cap.
 */
export function getCostCapModeMultiplier(
  mode: PerformanceMode | undefined,
  workspaceRoot?: string
): number {
  if (!mode) return 1.0;
  const fallback = DEFAULT_COST_CAP_MODE_MULTIPLIER[mode];

  // Env var override: NIGHTGAUGE_COST_CAP_MODE_MULTIPLIER_<MODE>
  const envKey = `NIGHTGAUGE_COST_CAP_MODE_MULTIPLIER_${mode.toUpperCase()}`;
  const envValue = process.env[envKey];
  if (envValue !== undefined && envValue !== "") {
    const parsed = Number.parseFloat(envValue);
    if (!Number.isNaN(parsed) && parsed > 0) {
      return parsed;
    }
  }

  const root = workspaceRoot ?? vscode.workspace.workspaceFolders?.[0]?.uri.fsPath;
  if (!root) {
    return fallback;
  }

  try {
    const pathResult = resolveConfigPathSync(root);
    if (!pathResult.exists) {
      return fallback;
    }

    const configContent = readEffectiveConfigTextSync(pathResult);
    const lines = configContent.split("\n");
    let inPipeline = false;
    let inMultiplier = false;

    for (const line of lines) {
      const trimmed = line.trim();

      if (trimmed === "pipeline:") {
        inPipeline = true;
        continue;
      }

      if (inPipeline && trimmed === "cost_cap_mode_multiplier:") {
        inMultiplier = true;
        continue;
      }

      // Exit sections on a new top-level / sibling key
      if (trimmed && !trimmed.startsWith("#") && /^[a-z_]+:/.test(trimmed)) {
        if (!line.startsWith(" ")) {
          inPipeline = false;
          inMultiplier = false;
        } else if (line.match(/^ {2}[a-z_]+:/)) {
          inMultiplier = false;
        }
      }

      if (inMultiplier) {
        // Mode entries: efficiency: 0.5  |  "maximum": 2.0
        const match = trimmed.match(/^["']?([a-z]+)["']?\s*:\s*([0-9]+(?:\.[0-9]+)?)/);
        if (match && match[1] === mode) {
          const parsed = Number.parseFloat(match[2]);
          if (!Number.isNaN(parsed) && parsed > 0) {
            return parsed;
          }
        }
      }
    }

    return fallback;
  } catch {
    return fallback;
  }
}

// ============================================================================
// Cost-Cap Provider Scale (Issue #3229)
// ============================================================================

/**
 * Default per-adapter cost-cap provider scale.
 *
 * Composes multiplicatively last in `getEffectiveStageCostCap`:
 *   `effectiveCap = baseCap × modelScale × modeMultiplier × providerScale`
 *
 * Seed values are the ratio of (adapter's typical-cost-per-stage) /
 * (Claude's typical-cost-per-stage) derived from the C1 pricing tables
 * (`providerPricing.ts`). `claude=1.0` keeps the PR #3209 calibrated
 * defaults bit-for-bit unchanged for default Claude users — the
 * regression-anchor invariant for AC #5 of Issue #3229.
 *
 * `0` is a deliberate, declarative sentinel meaning "this provider has
 * no meaningful per-token cost — switch to the time-based cap" (see
 * `getStageTimeCapMs`). It is distinct from `modelScale` and
 * `modeMultiplier` which both reject `0` as a typo because zeroing the
 * Claude cap by accident would silently kill all stage runs. Provider
 * scale accepts `0` only because local adapters (lm-studio, ollama) are
 * the explicit opt-in to time-cap mode — a typo there cannot land us
 * with a bogus $0 cap on a paid adapter.
 *
 * @see Issue #3229 — Provider-relative cost-cap defaults + override path
 * @see Issue #3217 — Mode multiplier (composes alongside)
 * @see Issue #3180 — Model scale (composes alongside)
 * @see providerPricing.ts — seed-ratio derivation source
 */
export const DEFAULT_COST_CAP_PROVIDER_SCALE: Record<ExecutionAdapter, number> = {
  claude: 1.0,
  codex: 0.7,
  gemini: 0.4,
  "gemini-sdk": 0.4,
  copilot: 0.2,
  "lm-studio": 0.0,
  ollama: 0.0,
};

/**
 * Read an env-var override for the provider scale.
 *
 * Accepts `NIGHTGAUGE_COST_CAP_PROVIDER_SCALE_<ADAPTER>` where the
 * adapter is uppercased and hyphens become underscores (so `gemini-sdk`
 * maps to `GEMINI_SDK` and `lm-studio` to `LM_STUDIO`).
 *
 * Returns the parsed non-negative float (including `0`), or undefined
 * when missing/invalid (NaN or negative).
 */
function readProviderScaleEnvOverride(adapter: ExecutionAdapter): number | undefined {
  const envKey = `NIGHTGAUGE_COST_CAP_PROVIDER_SCALE_${adapter.toUpperCase().replace(/-/g, "_")}`;
  const raw = process.env[envKey];
  if (raw === undefined || raw === "") return undefined;
  const parsed = Number.parseFloat(raw);
  if (Number.isNaN(parsed) || parsed < 0) return undefined;
  return parsed;
}

/**
 * Resolve the cost-cap provider scale for a given adapter (Issue #3229).
 *
 * Composes multiplicatively atop the existing model/mode multipliers in
 * {@link getEffectiveStageCostCap}.
 *
 * Lookup order:
 *   1. Env override `NIGHTGAUGE_COST_CAP_PROVIDER_SCALE_<ADAPTER>`
 *      (uppercased, hyphens → underscores)
 *   2. Config `pipeline.cost_cap_provider_scale.<adapter>` (line-by-line
 *      YAML parser, mirrors {@link getCostCapModeMultiplier})
 *   3. {@link DEFAULT_COST_CAP_PROVIDER_SCALE}[adapter]
 *   4. `1.0` for unknown adapters / undefined adapter (defensive default
 *      that preserves the configured cap as a literal ceiling)
 *
 * Invalid env / config values (NaN, negative) fall through to the next
 * layer. **`0` is accepted** at every layer — it is the explicit signal
 * "switch to time-based cap" for local adapters. This asymmetry vs.
 * `getCostCapModeMultiplier` / `getCostCapModelScale` (which reject `0`)
 * is deliberate, see ADR-002 in the issue knowledge base.
 */
export function getCostCapProviderScale(
  adapter: ExecutionAdapter | undefined,
  workspaceRoot?: string
): number {
  if (!adapter) return 1.0;
  const fallback = DEFAULT_COST_CAP_PROVIDER_SCALE[adapter] ?? 1.0;

  const envOverride = readProviderScaleEnvOverride(adapter);
  if (envOverride !== undefined) return envOverride;

  const root = workspaceRoot ?? vscode.workspace.workspaceFolders?.[0]?.uri.fsPath;
  if (!root) {
    return fallback;
  }

  try {
    const pathResult = resolveConfigPathSync(root);
    if (!pathResult.exists) {
      return fallback;
    }

    const configContent = readEffectiveConfigTextSync(pathResult);
    const lines = configContent.split("\n");
    let inPipeline = false;
    let inScale = false;

    for (const line of lines) {
      const trimmed = line.trim();

      if (trimmed === "pipeline:") {
        inPipeline = true;
        continue;
      }

      if (inPipeline && trimmed === "cost_cap_provider_scale:") {
        inScale = true;
        continue;
      }

      // Exit sections on a new top-level / sibling key
      if (trimmed && !trimmed.startsWith("#") && /^[a-z_]+:/.test(trimmed)) {
        if (!line.startsWith(" ")) {
          inPipeline = false;
          inScale = false;
        } else if (line.match(/^ {2}[a-z_]+:/)) {
          inScale = false;
        }
      }

      if (inScale) {
        // Adapter entries: gemini: 0.4 | "lm-studio": 0.0 | gemini-sdk: 0.4
        const match = trimmed.match(/^["']?([a-z][-a-z]*)["']?\s*:\s*([0-9]+(?:\.[0-9]+)?)/);
        if (match && match[1] === adapter) {
          const parsed = Number.parseFloat(match[2]);
          if (!Number.isNaN(parsed) && parsed >= 0) {
            return parsed;
          }
        }
      }
    }

    return fallback;
  } catch {
    return fallback;
  }
}

/**
 * Resolve the per-(provider, stage) cost-cap baseCap override (Issue #3229).
 *
 * Returns the explicit USD cap when
 * `pipeline.stage_cost_caps_per_provider.<adapter>.<stage>` is set, or
 * `undefined` when not set so callers fall through to the global
 * {@link getStageCostCapUsd}. Replaces only `baseCap` — model, mode, and
 * provider scales still compose on top of the override (semantic
 * symmetry with `pipeline.stage_cost_caps`, see ADR-003).
 *
 * Lookup order:
 *   1. Env override
 *      `NIGHTGAUGE_PIPELINE_STAGE_COST_CAP_PER_PROVIDER_<ADAPTER>_<STAGE>`
 *      (both segments uppercased, hyphens → underscores)
 *   2. Config `pipeline.stage_cost_caps_per_provider.<adapter>.<stage>`
 *   3. `undefined` (caller falls through to per-stage default)
 */
export function getStageCostCapPerProviderUsd(
  adapter: ExecutionAdapter | undefined,
  stage: string,
  workspaceRoot?: string
): number | undefined {
  if (!adapter) return undefined;

  const adapterUpper = adapter.toUpperCase().replace(/-/g, "_");
  const stageUpper = stage.toUpperCase().replace(/-/g, "_");
  const envKey = `NIGHTGAUGE_PIPELINE_STAGE_COST_CAP_PER_PROVIDER_${adapterUpper}_${stageUpper}`;
  const envValue = process.env[envKey];
  if (envValue !== undefined && envValue !== "") {
    const parsed = Number.parseFloat(envValue);
    if (!Number.isNaN(parsed) && parsed >= 0) {
      return parsed;
    }
  }

  const root = workspaceRoot ?? vscode.workspace.workspaceFolders?.[0]?.uri.fsPath;
  if (!root) {
    return undefined;
  }

  try {
    const pathResult = resolveConfigPathSync(root);
    if (!pathResult.exists) {
      return undefined;
    }

    const configContent = readEffectiveConfigTextSync(pathResult);
    const lines = configContent.split("\n");
    let inPipeline = false;
    let inOverrides = false;
    let currentAdapter: string | null = null;

    for (const line of lines) {
      const trimmed = line.trim();

      if (trimmed === "pipeline:") {
        inPipeline = true;
        continue;
      }

      if (inPipeline && trimmed === "stage_cost_caps_per_provider:") {
        inOverrides = true;
        currentAdapter = null;
        continue;
      }

      // Exit sections on a new sibling/top-level key. We're parsing a
      // 3-level structure (pipeline.stage_cost_caps_per_provider.<adapter>.<stage>)
      // so indent depth is meaningful:
      //   0 spaces  → top-level (exit pipeline + overrides)
      //   2 spaces  → pipeline subsection (exit overrides)
      //   4 spaces  → adapter key under overrides
      //   6 spaces  → stage entry under an adapter
      if (trimmed && !trimmed.startsWith("#") && /^["']?[a-z][-a-z_]*["']?\s*:/.test(trimmed)) {
        if (!line.startsWith(" ")) {
          inPipeline = false;
          inOverrides = false;
          currentAdapter = null;
        } else if (line.match(/^ {2}[a-z_]+:/) && !line.match(/^ {4}/)) {
          // New pipeline subsection — exit overrides
          inOverrides = false;
          currentAdapter = null;
        }
      }

      if (inOverrides) {
        // Adapter row at 4-space indent: "  gemini:" or "  \"lm-studio\":"
        const adapterMatch = line.match(/^ {4}["']?([a-z][-a-z]*)["']?\s*:\s*$/);
        if (adapterMatch) {
          currentAdapter = adapterMatch[1];
          continue;
        }

        // Stage row at 6-space indent under an adapter
        if (currentAdapter === adapter) {
          const stageMatch = line.match(
            /^ {6}["']?([a-z][-a-z]*)["']?\s*:\s*([0-9]+(?:\.[0-9]+)?)/
          );
          if (stageMatch && stageMatch[1] === stage) {
            const parsed = Number.parseFloat(stageMatch[2]);
            if (!Number.isNaN(parsed) && parsed >= 0) {
              return parsed;
            }
          }
        }
      }
    }

    return undefined;
  } catch {
    return undefined;
  }
}

// ============================================================================
// Stage Time Caps (Issue #3229)
// ============================================================================

/**
 * Default per-stage time caps in seconds.
 *
 * Empty by default — defaults will be calibrated from p95(elapsed) × 1.5
 * over historical runs in a separate audit issue. Until that lands, this
 * resolver only fires when the user explicitly opts in via
 * `pipeline.stage_time_caps.<stage>` or the matching env var.
 *
 * @see Issue #3229 — Wire the time-cap knob (computing defaults is
 *   out-of-scope per AC #4)
 */
export const DEFAULT_STAGE_TIME_CAPS: Record<string, number> = {};

/**
 * Get the per-stage time cap (in milliseconds).
 *
 * The time cap is the fallback hard ceiling for adapters where token
 * cost is structurally meaningless (`provider_scale=0`, e.g. lm-studio,
 * ollama). When `provider_scale=0` zeroes out `effectiveCap` in
 * {@link getEffectiveStageCostCap}, the caller (`skillRunner.ts`) ORs
 * this value with the existing `getStageHardCapMs` ticker — whichever is
 * smaller and `> 0` wins, leaving the absolute hard-cap escape hatch
 * intact.
 *
 * Lookup order:
 *   1. Env var: `NIGHTGAUGE_PIPELINE_STAGE_TIME_CAP_<STAGE_UPPER>`
 *      (seconds, e.g. `..._FEATURE_DEV=1800`)
 *   2. Config: `pipeline.stage_time_caps.<stage>: <seconds>`
 *   3. `DEFAULT_STAGE_TIME_CAPS` (currently empty → 0 = uncapped)
 *
 * @see Issue #3229 — Provider-relative cost-cap + time-cap knob
 */
export function getStageTimeCapMs(stage: string, workspaceRoot?: string): number {
  const defaultCapSec = DEFAULT_STAGE_TIME_CAPS[stage] ?? 0;

  const envKey = `NIGHTGAUGE_PIPELINE_STAGE_TIME_CAP_${stage.toUpperCase().replace(/-/g, "_")}`;
  const envValue = process.env[envKey];
  if (envValue !== undefined && envValue !== "") {
    const parsed = Number.parseInt(envValue, 10);
    if (!Number.isNaN(parsed) && parsed >= 0) {
      return parsed * 1000;
    }
  }

  const root = workspaceRoot ?? vscode.workspace.workspaceFolders?.[0]?.uri.fsPath;
  if (!root) {
    return defaultCapSec * 1000;
  }

  try {
    const pathResult = resolveConfigPathSync(root);
    if (!pathResult.exists) {
      return defaultCapSec * 1000;
    }

    const configContent = readEffectiveConfigTextSync(pathResult);
    const lines = configContent.split("\n");
    let inPipeline = false;
    let inTimeCaps = false;

    for (const line of lines) {
      const trimmed = line.trim();

      if (trimmed === "pipeline:") {
        inPipeline = true;
        continue;
      }

      if (inPipeline && trimmed === "stage_time_caps:") {
        inTimeCaps = true;
        continue;
      }

      if (trimmed && !trimmed.startsWith("#") && /^[a-z_]+:/.test(trimmed)) {
        if (!line.startsWith(" ")) {
          inPipeline = false;
          inTimeCaps = false;
        } else if (line.match(/^ {2}[a-z_]+:/)) {
          inTimeCaps = false;
        }
      }

      if (inTimeCaps) {
        const match = trimmed.match(/^["']?([a-z][-a-z]*)["']?\s*:\s*(\d+)/);
        if (match && match[1] === stage) {
          const parsed = Number.parseInt(match[2], 10);
          if (!Number.isNaN(parsed) && parsed >= 0) {
            return parsed * 1000;
          }
        }
      }
    }

    return defaultCapSec * 1000;
  } catch {
    return defaultCapSec * 1000;
  }
}

// ============================================================================
// Effective Cost Cap (Issues #3180, #3217, #3229)
// ============================================================================

/**
 * Resolve the effective cost cap for a stage, applying the (model, effort)
 * scale, the mode multiplier, and the provider scale (Issues #3180,
 * #3217, #3229).
 *
 * Composition is multiplicative:
 *   `effectiveCap = baseCap × modelScale × modeMultiplier × providerScale`
 *
 * Returns `{ baseCap, scale, modeMultiplier, providerScale, effectiveCap }`
 * so callers can log all five for observability.
 *
 * `baseCap` is resolved by trying `getStageCostCapPerProviderUsd(adapter,
 * stage, ...)` first (the per-(provider, stage) override path) and
 * falling through to `getStageCostCapUsd(stage, ...)` when no override is
 * present. Per ADR-003 the override replaces only `baseCap`, not
 * `effectiveCap` — model/mode/provider scales still compose on top.
 *
 * `baseCap === 0` means uncapped — `effectiveCap` is also 0 in that case
 * (no multiplier ever resurrects a disabled cap).
 *
 * `providerScale === 0` is the explicit "switch to time-based cap"
 * signal for local adapters (lm-studio, ollama); when it fires we
 * short-circuit `effectiveCap` to 0 and the caller routes to
 * `getStageTimeCapMs` for the hard-cap ticker.
 *
 * When `mode` / `adapter` are omitted, their multipliers default to
 * `1.0` — math identical to pre-#3229 (and pre-#3217) behavior. AC #5 of
 * Issue #3229 is anchored by this invariant: `claude × sonnet/medium ×
 * elevated` produces the byte-for-byte PR #3209 calibrated defaults.
 */
export function getEffectiveStageCostCap(
  stage: string,
  modelInfo?: { model?: string; effort?: string },
  workspaceRoot?: string,
  mode?: PerformanceMode,
  adapter?: ExecutionAdapter
): {
  baseCap: number;
  scale: number;
  modeMultiplier: number;
  providerScale: number;
  effectiveCap: number;
} {
  const overrideBase = getStageCostCapPerProviderUsd(adapter, stage, workspaceRoot);
  const baseCap = overrideBase ?? getStageCostCapUsd(stage, workspaceRoot);
  if (baseCap <= 0) {
    return {
      baseCap: 0,
      scale: 1.0,
      modeMultiplier: 1.0,
      providerScale: 1.0,
      effectiveCap: 0,
    };
  }
  const providerScale = getCostCapProviderScale(adapter, workspaceRoot);
  if (providerScale === 0) {
    // Explicit "switch to time-cap" signal (lm-studio / ollama). Skip
    // the model/mode multipliers entirely — they don't apply when the
    // cost-cap path is disabled in favor of time-based termination.
    return {
      baseCap,
      scale: 1.0,
      modeMultiplier: 1.0,
      providerScale: 0,
      effectiveCap: 0,
    };
  }
  const scale = getCostCapModelScale(modelInfo?.model, modelInfo?.effort);
  const modeMultiplier = getCostCapModeMultiplier(mode, workspaceRoot);
  return {
    baseCap,
    scale,
    modeMultiplier,
    providerScale,
    effectiveCap: baseCap * scale * modeMultiplier * providerScale,
  };
}

/**
 * Get the cost cap (in USD) for a given stage.
 *
 * Priority:
 * 1. Env var: NIGHTGAUGE_PIPELINE_STAGE_COST_CAP_<STAGE_UPPER>
 *    (e.g. NIGHTGAUGE_PIPELINE_STAGE_COST_CAP_FEATURE_DEV=5.00)
 * 2. Config file: pipeline.stage_cost_caps.<stage>: 5.00
 * 3. Default from DEFAULT_STAGE_COST_CAPS (0 for stages not listed)
 *
 * Returns `0` when no cap applies (the cost-cap check becomes a no-op).
 *
 * @see Issue #3002
 */
export function getStageCostCapUsd(stage: string, workspaceRoot?: string): number {
  const defaultCapUsd = DEFAULT_STAGE_COST_CAPS[stage] ?? 0;

  // Environment variable override
  const envKey = `NIGHTGAUGE_PIPELINE_STAGE_COST_CAP_${stage.toUpperCase().replace(/-/g, "_")}`;
  const envValue = process.env[envKey];
  if (envValue !== undefined && envValue !== "") {
    const parsed = Number.parseFloat(envValue);
    if (!Number.isNaN(parsed) && parsed >= 0) {
      return parsed;
    }
  }

  const root = workspaceRoot ?? vscode.workspace.workspaceFolders?.[0]?.uri.fsPath;
  if (!root) {
    return defaultCapUsd;
  }

  try {
    const pathResult = resolveConfigPathSync(root);
    if (!pathResult.exists) {
      return defaultCapUsd;
    }

    const configContent = readEffectiveConfigTextSync(pathResult);
    const lines = configContent.split("\n");
    let inPipeline = false;
    let inCostCaps = false;

    for (const line of lines) {
      const trimmed = line.trim();

      if (trimmed === "pipeline:") {
        inPipeline = true;
        continue;
      }

      if (inPipeline && trimmed === "stage_cost_caps:") {
        inCostCaps = true;
        continue;
      }

      // Exit sections on new top-level key
      if (trimmed && !trimmed.startsWith("#") && /^[a-z_]+:/.test(trimmed)) {
        if (!line.startsWith(" ")) {
          inPipeline = false;
          inCostCaps = false;
        } else if (line.match(/^ {2}[a-z_]+:/)) {
          // New pipeline subsection — exit cost_caps
          inCostCaps = false;
        }
      }

      if (inCostCaps) {
        // Stage entries use quoted or unquoted keys: "feature-dev": 23.00
        const match = trimmed.match(/^["']?([a-z][-a-z]*)["']?\s*:\s*([0-9]+(?:\.[0-9]+)?)/);
        if (match && match[1] === stage) {
          const parsed = Number.parseFloat(match[2]);
          if (!Number.isNaN(parsed) && parsed >= 0) {
            return parsed;
          }
        }
      }
    }

    return defaultCapUsd;
  } catch {
    return defaultCapUsd;
  }
}

// ============================================================================
// Cost Cap Tightness Warning (Issue #3276)
// ============================================================================

export interface CostCapTightnessDecision {
  shouldWarn: boolean;
  stage: string;
  effectiveCap: number;
  historicalMedian: number;
  threshold: number;
  multiplier: number;
  capEnvKey: string;
  capConfigPath: string;
  message: string;
}

const COST_CAP_WARNING_MIN_SAMPLES = 3;

/**
 * Checks whether a configured stage cost cap is below the warning threshold
 * (historicalMedian × multiplier). Returns a decision object — callers decide
 * whether to log, surface in dashboard, etc.
 *
 * Returns `shouldWarn: false` when:
 * - effectiveCap === 0 (uncapped — intentional)
 * - historicalMedian === 0 (no history)
 * - sampleCount < 3 (insufficient history)
 * - multiplier === 0 (warning disabled)
 *
 * @see Issue #3276
 */
export function checkCostCapTightness(
  stage: string,
  effectiveCap: number,
  historicalMedian: number,
  multiplier = 1.2,
  sampleCount = 0
): CostCapTightnessDecision {
  const capEnvKey = `NIGHTGAUGE_PIPELINE_STAGE_COST_CAP_${stage.toUpperCase().replace(/-/g, "_")}`;
  const capConfigPath = `pipeline.stage_cost_caps.${stage}`;
  const threshold = historicalMedian * multiplier;

  const shouldWarn =
    multiplier > 0 &&
    effectiveCap > 0 &&
    historicalMedian > 0 &&
    sampleCount >= COST_CAP_WARNING_MIN_SAMPLES &&
    effectiveCap < threshold;

  const message = shouldWarn
    ? [
        `[cost-cap-warning] Stage "${stage}" cost cap ($${effectiveCap.toFixed(2)}) is below`,
        `the warning threshold ($${threshold.toFixed(2)} = historical median`,
        `$${historicalMedian.toFixed(2)} × ${multiplier}).`,
        `The stage may be killed mid-run. To fix, increase the cap via:`,
        `  env:    ${capEnvKey}=<new_value>`,
        `  config: ${capConfigPath}: <new_value>`,
      ].join(" ")
    : "";

  return {
    shouldWarn,
    stage,
    effectiveCap,
    historicalMedian,
    threshold,
    multiplier,
    capEnvKey,
    capConfigPath,
    message,
  };
}

/**
 * Resolves the cost cap warning multiplier from env, config, or default (1.2).
 *
 * @see Issue #3276
 */
export function getCostCapWarningMultiplier(workspaceRoot?: string): number {
  const DEFAULT_MULTIPLIER = 1.2;

  const envValue = process.env["NIGHTGAUGE_PIPELINE_COST_CAP_WARNING_MULTIPLIER"];
  if (envValue !== undefined && envValue !== "") {
    const parsed = Number.parseFloat(envValue);
    if (!Number.isNaN(parsed) && parsed >= 0) {
      return parsed;
    }
  }

  const root = workspaceRoot ?? vscode.workspace.workspaceFolders?.[0]?.uri.fsPath;
  if (!root) return DEFAULT_MULTIPLIER;

  try {
    const pathResult = resolveConfigPathSync(root);
    if (!pathResult.exists) return DEFAULT_MULTIPLIER;

    const configContent = readEffectiveConfigTextSync(pathResult);
    const lines = configContent.split("\n");
    let inPipeline = false;

    for (const line of lines) {
      const trimmed = line.trim();
      if (trimmed === "pipeline:") {
        inPipeline = true;
        continue;
      }
      if (inPipeline && !line.startsWith(" ")) {
        inPipeline = false;
      }
      if (inPipeline) {
        const match = trimmed.match(/^cost_cap_warning_multiplier\s*:\s*([0-9]+(?:\.[0-9]+)?)/);
        if (match) {
          const parsed = Number.parseFloat(match[1]);
          if (!Number.isNaN(parsed) && parsed >= 0) return parsed;
        }
      }
    }
  } catch {
    // fall through to default
  }

  return DEFAULT_MULTIPLIER;
}

// ============================================================================
// Cost Warn Threshold + Runaway Ceiling Resolvers (Issue #3508)
// ============================================================================

const DEFAULT_COST_WARN_MULTIPLIER = 1.5;
const DEFAULT_RUNAWAY_CEILING_MULTIPLIER = 3.0;
const RUNAWAY_CEILING_FLOOR_USD = 75;

/**
 * Resolves the cost warn multiplier for a given stage.
 *
 * Priority:
 * 1. Env var: NIGHTGAUGE_PIPELINE_STAGE_COST_WARN_THRESHOLD_<STAGE_UPPER>
 *    (per-stage override)
 * 2. Config: pipeline.stage_cost_warn_thresholds.<stage>
 * 3. Env var: NIGHTGAUGE_PIPELINE_COST_WARN_MULTIPLIER (global)
 * 4. Config: pipeline.cost_warn_multiplier (global)
 * 5. Default: 1.5
 *
 * Returns 0 to disable warn toasts entirely.
 *
 * @see Issue #3508
 */
export function getStageCostWarnMultiplier(stage: string, workspaceRoot?: string): number {
  // Tier 1: per-stage env var override
  const stageEnvKey = `NIGHTGAUGE_PIPELINE_STAGE_COST_WARN_THRESHOLD_${stage.toUpperCase().replace(/-/g, "_")}`;
  const stageEnvValue = process.env[stageEnvKey];
  if (stageEnvValue !== undefined && stageEnvValue !== "") {
    const parsed = Number.parseFloat(stageEnvValue);
    if (!Number.isNaN(parsed) && parsed >= 0) return parsed;
  }

  // Tier 2 + 3 + 4: read config file, fall through to global env/default
  const root = workspaceRoot ?? vscode.workspace.workspaceFolders?.[0]?.uri.fsPath;
  if (root) {
    try {
      const pathResult = resolveConfigPathSync(root);
      if (pathResult.exists) {
        const configContent = readEffectiveConfigTextSync(pathResult);
        const lines = configContent.split("\n");
        let inPipeline = false;
        let inWarnThresholds = false;

        for (const line of lines) {
          const trimmed = line.trim();

          if (trimmed === "pipeline:") {
            inPipeline = true;
            continue;
          }
          if (!line.startsWith(" ")) {
            inPipeline = false;
            inWarnThresholds = false;
          }
          if (inPipeline && trimmed === "stage_cost_warn_thresholds:") {
            inWarnThresholds = true;
            continue;
          }
          if (inPipeline && line.match(/^ {2}[a-z_]+:/)) {
            inWarnThresholds = false;
          }
          if (inWarnThresholds) {
            const match = trimmed.match(/^["']?([a-z][-a-z]*)["']?\s*:\s*([0-9]+(?:\.[0-9]+)?)/);
            if (match && match[1] === stage) {
              const parsed = Number.parseFloat(match[2]);
              if (!Number.isNaN(parsed) && parsed >= 0) return parsed;
            }
          }
          if (inPipeline && !inWarnThresholds) {
            const match = trimmed.match(/^cost_warn_multiplier\s*:\s*([0-9]+(?:\.[0-9]+)?)/);
            if (match) {
              const parsed = Number.parseFloat(match[1]);
              if (!Number.isNaN(parsed) && parsed >= 0) return parsed;
            }
          }
        }
      }
    } catch {
      // fall through to global env / default
    }
  }

  // Tier 3: global env var
  const globalEnvValue = process.env["NIGHTGAUGE_PIPELINE_COST_WARN_MULTIPLIER"];
  if (globalEnvValue !== undefined && globalEnvValue !== "") {
    const parsed = Number.parseFloat(globalEnvValue);
    if (!Number.isNaN(parsed) && parsed >= 0) return parsed;
  }

  return DEFAULT_COST_WARN_MULTIPLIER;
}

/**
 * Resolves the runaway ceiling multiplier and computes the ceiling USD value.
 *
 * Priority:
 * 1. Env var: NIGHTGAUGE_PIPELINE_RUNAWAY_CEILING_MULTIPLIER
 * 2. Config: pipeline.runaway_ceiling_multiplier
 * 3. Default: 3.0
 *
 * Returns: Math.max($75, effectiveCap × multiplier)
 *
 * When effectiveCap is 0 (uncapped), returns 0 (ceiling disabled).
 *
 * @see Issue #3508
 */
export function getRunwayCeilingUsd(effectiveCap: number, workspaceRoot?: string): number {
  if (effectiveCap <= 0) return 0;

  let multiplier = DEFAULT_RUNAWAY_CEILING_MULTIPLIER;

  const envValue = process.env["NIGHTGAUGE_PIPELINE_RUNAWAY_CEILING_MULTIPLIER"];
  if (envValue !== undefined && envValue !== "") {
    const parsed = Number.parseFloat(envValue);
    if (!Number.isNaN(parsed) && parsed >= 1) {
      multiplier = parsed;
    }
  } else {
    const root = workspaceRoot ?? vscode.workspace.workspaceFolders?.[0]?.uri.fsPath;
    if (root) {
      try {
        const pathResult = resolveConfigPathSync(root);
        if (pathResult.exists) {
          const configContent = readEffectiveConfigTextSync(pathResult);
          const lines = configContent.split("\n");
          let inPipeline = false;
          for (const line of lines) {
            const trimmed = line.trim();
            if (trimmed === "pipeline:") {
              inPipeline = true;
              continue;
            }
            if (!line.startsWith(" ")) inPipeline = false;
            if (inPipeline) {
              const match = trimmed.match(
                /^runaway_ceiling_multiplier\s*:\s*([0-9]+(?:\.[0-9]+)?)/
              );
              if (match) {
                const parsed = Number.parseFloat(match[1]);
                if (!Number.isNaN(parsed) && parsed >= 1) {
                  multiplier = parsed;
                  break;
                }
              }
            }
          }
        }
      } catch {
        // fall through to default
      }
    }
  }

  return Math.max(RUNAWAY_CEILING_FLOOR_USD, effectiveCap * multiplier);
}

// ============================================================================
// Live Budget Evaluation Cadence (Issue #254)
// ============================================================================

/**
 * How often (ms) the orchestrator re-evaluates per-stage budget and run-ceiling
 * thresholds against the LIVE in-stage cost estimate (#233). Budget enforcement
 * used to fire only at stage end (cost arrived solely on the terminal `result`
 * envelope); driving it from the streamed `assistant` usage lets wind-down →
 * warn → terminate fire mid-stage. Throttled so a chatty stage (~1,571 usage
 * payloads observed) does not trigger a getState()/enforcement pass per turn.
 */
const DEFAULT_BUDGET_EVAL_CADENCE_MS = 5000;
/** Floor for a NON-ZERO configured cadence, so enforcement can never be armed
 *  to run on effectively every streamed message. `0` explicitly disables the
 *  throttle (evaluate on every snapshot) and bypasses the floor. */
const BUDGET_EVAL_CADENCE_FLOOR_MS = 1000;

/**
 * Resolve the live budget-evaluation cadence in milliseconds.
 *
 * Priority: env `NIGHTGAUGE_PIPELINE_BUDGET_EVAL_CADENCE_MS` → config
 * `pipeline.budget_eval_cadence_ms` → {@link DEFAULT_BUDGET_EVAL_CADENCE_MS}.
 * A configured `0` disables throttling; any other positive value is floored to
 * {@link BUDGET_EVAL_CADENCE_FLOOR_MS}. Mirrors {@link getRunwayCeilingUsd}'s
 * env/config/default resolution (regex-scanned yaml — no schema dependency).
 *
 * @see Issue #254 — streaming budget enforcement
 */
export function getBudgetEvalCadenceMs(workspaceRoot?: string): number {
  const clamp = (value: number): number =>
    value <= 0 ? 0 : Math.max(BUDGET_EVAL_CADENCE_FLOOR_MS, value);

  const envValue = process.env["NIGHTGAUGE_PIPELINE_BUDGET_EVAL_CADENCE_MS"];
  if (envValue !== undefined && envValue !== "") {
    const parsed = Number.parseInt(envValue, 10);
    if (!Number.isNaN(parsed) && parsed >= 0) {
      return clamp(parsed);
    }
    return DEFAULT_BUDGET_EVAL_CADENCE_MS;
  }

  const root = workspaceRoot ?? vscode.workspace.workspaceFolders?.[0]?.uri.fsPath;
  if (root) {
    try {
      const pathResult = resolveConfigPathSync(root);
      if (pathResult.exists) {
        const configContent = readEffectiveConfigTextSync(pathResult);
        const lines = configContent.split("\n");
        let inPipeline = false;
        for (const line of lines) {
          const trimmed = line.trim();
          if (trimmed === "pipeline:") {
            inPipeline = true;
            continue;
          }
          if (!line.startsWith(" ")) inPipeline = false;
          if (inPipeline) {
            const match = trimmed.match(/^budget_eval_cadence_ms\s*:\s*([0-9]+)/);
            if (match) {
              const parsed = Number.parseInt(match[1], 10);
              if (!Number.isNaN(parsed) && parsed >= 0) {
                return clamp(parsed);
              }
            }
          }
        }
      }
    } catch {
      // fall through to default
    }
  }

  return DEFAULT_BUDGET_EVAL_CADENCE_MS;
}

// ============================================================================
// Progress-Based Runaway Detection (Issue #3783)
// ============================================================================

const DEFAULT_NO_PROGRESS_WINDOW_MS = 120_000; // 2 minutes
const DEFAULT_MIN_COST_TO_ACTIVATE_USD = 0.5;
const DEFAULT_CATASTROPHIC_LIMIT_USD = 200;
/**
 * Distinct-tool signatures with no intervening productive signal before the
 * churn detector fires (Issue #3851). 40 is well above any healthy productive
 * burst yet far below the #3811 churn profile (530 tool calls / 0 commits).
 */
const DEFAULT_CHURN_TOOL_THRESHOLD = 40;

/**
 * Resolve progress-based runaway detection configuration.
 *
 * Priority:
 * 1. Env vars: NIGHTGAUGE_PIPELINE_PROGRESS_RUNAWAY_*
 * 2. Config: pipeline.progress_runaway.*
 * 3. Defaults
 *
 * When performanceMode is "maximum", observeOnly is forced true (no kills).
 *
 * `autonomous` (unattended runs) upgrades the catastrophic backstop from
 * warn-only to a progress-gated kill (Issue #3851): there is no human to click
 * "Stop" so an unattended stage that has burned the catastrophic limit with no
 * productive progress must self-terminate.
 *
 * @see Issue #3783 — Progress-based runaway detection
 * @see Issue #3851 — Gate defenses on productive progress; churn + catastrophic kill
 */
export function getProgressRunawayConfig(
  workspaceRoot?: string,
  _stage?: string,
  performanceMode?: string,
  autonomous?: boolean
): ProgressMonitorConfig {
  let enabled = true;
  let noProgressWindowMs = DEFAULT_NO_PROGRESS_WINDOW_MS;
  let minCostToActivateUsd = DEFAULT_MIN_COST_TO_ACTIVATE_USD;
  let churnToolThreshold = DEFAULT_CHURN_TOOL_THRESHOLD;
  let catastrophicLimitUsd = DEFAULT_CATASTROPHIC_LIMIT_USD;

  // Env var overrides
  const envEnabled = process.env["NIGHTGAUGE_PIPELINE_PROGRESS_RUNAWAY_ENABLED"];
  if (envEnabled === "false" || envEnabled === "0") enabled = false;
  else if (envEnabled === "true" || envEnabled === "1") enabled = true;

  const envWindow = process.env["NIGHTGAUGE_PIPELINE_PROGRESS_RUNAWAY_WINDOW_MS"];
  if (envWindow !== undefined && envWindow !== "") {
    const parsed = Number.parseInt(envWindow, 10);
    if (!Number.isNaN(parsed) && parsed >= 30_000) noProgressWindowMs = parsed;
  }

  const envMinCost = process.env["NIGHTGAUGE_PIPELINE_PROGRESS_RUNAWAY_MIN_COST_USD"];
  if (envMinCost !== undefined && envMinCost !== "") {
    const parsed = Number.parseFloat(envMinCost);
    if (!Number.isNaN(parsed) && parsed >= 0) minCostToActivateUsd = parsed;
  }

  const envCatastrophic =
    process.env["NIGHTGAUGE_PIPELINE_PROGRESS_RUNAWAY_CATASTROPHIC_LIMIT_USD"];
  if (envCatastrophic !== undefined && envCatastrophic !== "") {
    const parsed = Number.parseFloat(envCatastrophic);
    if (!Number.isNaN(parsed) && parsed >= 50) catastrophicLimitUsd = parsed;
  }

  const envChurn = process.env["NIGHTGAUGE_PIPELINE_PROGRESS_RUNAWAY_CHURN_THRESHOLD"];
  if (envChurn !== undefined && envChurn !== "") {
    const parsed = Number.parseInt(envChurn, 10);
    if (!Number.isNaN(parsed) && parsed >= 0) churnToolThreshold = parsed;
  }

  // Config file overrides (only if no env override already changed the default)
  const root = workspaceRoot ?? vscode.workspace.workspaceFolders?.[0]?.uri.fsPath;
  if (root) {
    try {
      const pathResult = resolveConfigPathSync(root);
      if (pathResult.exists) {
        const configContent = readEffectiveConfigTextSync(pathResult);
        const lines = configContent.split("\n");
        let inPipeline = false;
        let inProgressRunaway = false;
        for (const line of lines) {
          const trimmed = line.trim();
          if (trimmed === "pipeline:") {
            inPipeline = true;
            continue;
          }
          if (inPipeline && !line.startsWith(" ")) {
            inPipeline = false;
            inProgressRunaway = false;
          }
          if (inPipeline && trimmed === "progress_runaway:") {
            inProgressRunaway = true;
            continue;
          }
          if (inProgressRunaway && !line.startsWith("    ")) {
            inProgressRunaway = false;
          }
          if (inProgressRunaway) {
            const enabledMatch = trimmed.match(/^enabled\s*:\s*(true|false)/);
            if (enabledMatch && envEnabled === undefined) {
              enabled = enabledMatch[1] === "true";
            }
            const windowMatch = trimmed.match(/^no_progress_window_ms\s*:\s*(\d+)/);
            if (windowMatch && envWindow === undefined) {
              const parsed = Number.parseInt(windowMatch[1], 10);
              if (!Number.isNaN(parsed) && parsed >= 30_000) noProgressWindowMs = parsed;
            }
            const minCostMatch = trimmed.match(
              /^min_cost_to_activate_usd\s*:\s*([0-9]+(?:\.[0-9]+)?)/
            );
            if (minCostMatch && envMinCost === undefined) {
              const parsed = Number.parseFloat(minCostMatch[1]);
              if (!Number.isNaN(parsed) && parsed >= 0) minCostToActivateUsd = parsed;
            }
            const catMatch = trimmed.match(/^catastrophic_limit_usd\s*:\s*([0-9]+(?:\.[0-9]+)?)/);
            if (catMatch && envCatastrophic === undefined) {
              const parsed = Number.parseFloat(catMatch[1]);
              if (!Number.isNaN(parsed) && parsed >= 50) catastrophicLimitUsd = parsed;
            }
            const churnMatch = trimmed.match(/^churn_tool_threshold\s*:\s*(\d+)/);
            if (churnMatch && envChurn === undefined) {
              const parsed = Number.parseInt(churnMatch[1], 10);
              if (!Number.isNaN(parsed) && parsed >= 0) churnToolThreshold = parsed;
            }
          }
        }
      }
    } catch {
      // fall through to defaults
    }
  }

  // maximum performance mode → observe-only (never kill)
  const observeOnly = performanceMode === "maximum";

  // Catastrophic backstop becomes a progress-gated KILL in unattended runs
  // (Issue #3851) — no human is at the modal to stop a confirmed runaway. The
  // ProgressMonitor only acts on this when there is ALSO no productive progress
  // in the window, so healthy large work is never blunt-killed.
  const catastrophicKill = autonomous === true && !observeOnly;

  return {
    enabled,
    noProgressWindowMs,
    minCostToActivateUsd,
    catastrophicLimitUsd,
    observeOnly,
    churnToolThreshold,
    catastrophicKill,
  };
}

// ============================================================================
// Autonomous Stall Escalation Config (Issue #2656)
// ============================================================================

/**
 * Configuration for autonomous mode stall escalation.
 */
export interface AutonomousStallConfig {
  /** Whether escalation is enabled (default true) */
  escalationEnabled: boolean;
  /** Auto-abort timeout in ms for pause dialog (default 1800000 = 30min) */
  pauseTimeoutMs: number;
  /** Watchdog threshold in minutes before a green PR is considered stalled */
  stallDetectionMinutes: number;
  /** Auto-run `nightgauge pr merge <PR>` when a stalled PR is detected */
  autoRedispatchStalled: boolean;
}

const DEFAULT_STALL_PAUSE_TIMEOUT_MS = 30 * 60 * 1000; // 30 minutes
const DEFAULT_STALL_DETECTION_MINUTES = 60;

/**
 * Get autonomous stall escalation configuration.
 * Reads from config.yaml autonomous section, with env var overrides.
 */
export function getAutonomousStallConfig(workspaceRoot?: string): AutonomousStallConfig {
  // Environment variable overrides
  const envEnabled = process.env.NIGHTGAUGE_AUTONOMOUS_STALL_ESCALATION_ENABLED;
  if (envEnabled !== undefined) {
    const envTimeout = process.env.NIGHTGAUGE_AUTONOMOUS_STALL_PAUSE_TIMEOUT;
    const envMinutes = process.env.NIGHTGAUGE_AUTONOMOUS_STALL_DETECTION_MINUTES;
    const envAutoRedispatch = process.env.NIGHTGAUGE_AUTONOMOUS_AUTO_REDISPATCH_STALLED;
    return {
      escalationEnabled: envEnabled !== "false" && envEnabled !== "0",
      pauseTimeoutMs: envTimeout
        ? parseInt(envTimeout, 10) || DEFAULT_STALL_PAUSE_TIMEOUT_MS
        : DEFAULT_STALL_PAUSE_TIMEOUT_MS,
      stallDetectionMinutes: envMinutes
        ? parseInt(envMinutes, 10) || DEFAULT_STALL_DETECTION_MINUTES
        : DEFAULT_STALL_DETECTION_MINUTES,
      autoRedispatchStalled: envAutoRedispatch === "true" || envAutoRedispatch === "1",
    };
  }

  // Read from config.yaml
  try {
    if (!workspaceRoot) {
      return {
        escalationEnabled: true,
        pauseTimeoutMs: DEFAULT_STALL_PAUSE_TIMEOUT_MS,
        stallDetectionMinutes: DEFAULT_STALL_DETECTION_MINUTES,
        autoRedispatchStalled: false,
      };
    }
    const pathResult = resolveConfigPathSync(workspaceRoot);
    if (!pathResult.exists) {
      return {
        escalationEnabled: true,
        pauseTimeoutMs: DEFAULT_STALL_PAUSE_TIMEOUT_MS,
        stallDetectionMinutes: DEFAULT_STALL_DETECTION_MINUTES,
        autoRedispatchStalled: false,
      };
    }
    const raw = readEffectiveConfigTextSync(pathResult);
    // Simple YAML key extraction for autonomous section
    const escalationMatch = raw.match(/stall_escalation_enabled:\s*(true|false)/);
    const timeoutMatch = raw.match(/stall_pause_timeout:\s*(\d+)/);
    const stallDetectionMatch = raw.match(/stall_detection_minutes:\s*(\d+)/);
    const autoRedispatchMatch = raw.match(/auto_redispatch_stalled:\s*(true|false)/);
    return {
      escalationEnabled: escalationMatch ? escalationMatch[1] === "true" : true,
      pauseTimeoutMs: timeoutMatch ? parseInt(timeoutMatch[1], 10) : DEFAULT_STALL_PAUSE_TIMEOUT_MS,
      stallDetectionMinutes: stallDetectionMatch
        ? parseInt(stallDetectionMatch[1], 10)
        : DEFAULT_STALL_DETECTION_MINUTES,
      autoRedispatchStalled: autoRedispatchMatch ? autoRedispatchMatch[1] === "true" : false,
    };
  } catch {
    return {
      escalationEnabled: true,
      pauseTimeoutMs: DEFAULT_STALL_PAUSE_TIMEOUT_MS,
      stallDetectionMinutes: DEFAULT_STALL_DETECTION_MINUTES,
      autoRedispatchStalled: false,
    };
  }
}

// ============================================================================
// History-Calibrated Stall Thresholds (Issue #2654)
// ============================================================================

/**
 * Calibrated stall data for a single pipeline stage.
 * When isColdStart is true, killSec is 0 (auto-kill disabled).
 */
export interface CalibratedStallData {
  /** Warning threshold in seconds (from p95×1.5 or static default) */
  warnSec: number;
  /** Kill threshold in seconds (0 = disabled in cold start mode) */
  killSec: number;
  /** Source of the threshold value */
  source: "env" | "config" | "calibrated" | "static";
  /** True when count < minRuns — kill is disabled, warn uses static default */
  isColdStart: boolean;
}

/**
 * Module-level cache of pre-computed calibrated stall data.
 * Keyed by workspaceRoot. Populated by precomputeCalibratedStallThresholds().
 * Consumed synchronously by getCalibratedStallData() inside skillRunner.
 *
 * Issue #3216: cache is now keyed `[workspaceRoot][stage][mode]` so per-mode
 * calibration baselines are tracked independently. Each (stage, mode) cell
 * is computed against history filtered to that mode (via
 * `StageDurationAnalyzer.getStageStatsByMode`); empty cells fall back to the
 * same-stage `elevated` bucket where possible, otherwise cold-start.
 */
const _calibratedStallCache = new Map<
  string,
  Record<string, Partial<Record<PerformanceMode, CalibratedStallData>>>
>();

/**
 * Round a number of seconds UP to the nearest 30-second boundary.
 *
 * @example roundUpTo30s(601) → 630  (≈ 10.5 min)
 * @example roundUpTo30s(600) → 600  (exact boundary)
 * @example roundUpTo30s(599) → 600
 *
 * @see Issue #2654 - History-calibrated stall thresholds
 */
export function roundUpTo30s(seconds: number): number {
  return Math.ceil(seconds / 30) * 30;
}

/**
 * Compute the stall kill threshold from max observed duration and warning threshold.
 * Formula: max(max_observed × 2, warning × 3).
 * The warning×3 floor prevents the kill threshold from being too close to the warning.
 *
 * @param maxObservedSec - Maximum observed stage duration in seconds
 * @param warningSec - Warning threshold in seconds
 * @returns Kill threshold in seconds
 *
 * @see Issue #2654 - History-calibrated stall thresholds
 */
export function computeKillThreshold(maxObservedSec: number, warningSec: number): number {
  return Math.max(maxObservedSec * 2, warningSec * 3);
}

/**
 * Get the minimum number of successful runs required before history-calibrated
 * stall thresholds are activated. Below this count, cold start mode applies:
 * warning uses the static default, auto-kill is disabled.
 *
 * Configurable via pipeline.stall_calibration_min_runs in config.yaml.
 * Default: 10. Set to 0 to always use static defaults (disable calibration).
 *
 * @see Issue #2654 - History-calibrated stall thresholds
 */
export function getStallCalibrationMinRuns(workspaceRoot?: string): number {
  const DEFAULT_MIN_RUNS = 10;

  const root = workspaceRoot ?? vscode.workspace.workspaceFolders?.[0]?.uri.fsPath;
  if (!root) {
    return DEFAULT_MIN_RUNS;
  }

  try {
    const pathResult = resolveConfigPathSync(root);
    if (!pathResult.exists) {
      return DEFAULT_MIN_RUNS;
    }

    const configContent = readEffectiveConfigTextSync(pathResult);
    const lines = configContent.split("\n");
    let inPipeline = false;

    for (const line of lines) {
      const trimmed = line.trim();

      if (trimmed === "pipeline:") {
        inPipeline = true;
        continue;
      }

      // Exit pipeline section on new top-level key
      if (inPipeline && trimmed && !trimmed.startsWith("#") && !line.startsWith(" ")) {
        inPipeline = false;
      }

      if (inPipeline) {
        const match = trimmed.match(/^stall_calibration_min_runs:\s*(\d+)/);
        if (match) {
          const parsed = Number.parseInt(match[1], 10);
          if (!Number.isNaN(parsed) && parsed >= 0) {
            return parsed;
          }
        }
      }
    }

    return DEFAULT_MIN_RUNS;
  } catch {
    return DEFAULT_MIN_RUNS;
  }
}

/** Performance modes that get their own per-stage calibration bucket (issue #3216). */
const CALIBRATION_MODES: readonly PerformanceMode[] = [
  "efficiency",
  "elevated",
  "maximum",
  "frontier",
];

/**
 * Pre-compute history-calibrated stall thresholds for every (stage, mode) pair
 * and populate the module-level cache. Must be called before
 * runStageSkillHeadless() to enable calibrated thresholds. Safe to call
 * multiple times — idempotent.
 *
 * Issue #3216: per-mode bucketing. For each (stage, mode), history is
 * filtered to that mode (via `StageDurationAnalyzer.getStageStatsByMode`);
 * pre-#3215 records lacking `performance_mode` count toward the `elevated`
 * bucket only. When a (stage, mode) cell has fewer than `min_runs` samples,
 * we fall back to the same-stage `elevated` cell if it qualifies; otherwise
 * we cold-start (warn at static default, kill disabled).
 *
 * Calibration uses p95×1.5 (rounded to 30s) for the warning threshold and
 * max_observed×2 (minimum: warning×3) for the kill threshold.
 *
 * Override precedence is applied per stage:
 *   env var > config.yaml > calibrated > static default
 *
 * Env/config overrides apply to ALL modes for a given stage — they are global
 * stage overrides, not per-mode. This preserves the override semantics from
 * Issue #2654.
 *
 * @param workspaceRoot - Absolute path to the repository root
 *
 * @see Issue #2654 - History-calibrated stall thresholds
 * @see Issue #3216 - Calibration bucketing by (size, mode)
 */
export async function precomputeCalibratedStallThresholds(workspaceRoot: string): Promise<void> {
  // Avoid circular import at runtime — dynamic import resolved once per call.
  const { StageDurationAnalyzer } = await import("../StageDurationAnalyzer");

  const minRuns = getStallCalibrationMinRuns(workspaceRoot);
  // When min_runs = 0, calibration is disabled — use static defaults only.
  // The cache is still cleared so getCalibratedStallData returns undefined,
  // matching the pre-#3216 contract.
  if (minRuns === 0) {
    _calibratedStallCache.set(workspaceRoot, {});
    return;
  }

  const configThresholds = getStallThresholds(workspaceRoot); // env + config already applied
  const result: Record<string, Partial<Record<PerformanceMode, CalibratedStallData>>> = {};

  for (const stage of Object.keys(DEFAULT_STALL_THRESHOLDS)) {
    const staticDefaultSec = DEFAULT_STALL_THRESHOLDS[stage];
    const killMultiplier = getStallKillMultiplier(workspaceRoot, stage);

    // ---- env / config overrides apply to every mode for this stage ----
    const envKey = `NIGHTGAUGE_PIPELINE_STALL_THRESHOLD_${stage.toUpperCase().replace(/-/g, "_")}`;
    const hasEnvOverride = Boolean(process.env[envKey]);
    if (hasEnvOverride) {
      const warnSec = configThresholds[stage] ?? staticDefaultSec;
      const data: CalibratedStallData = {
        warnSec,
        killSec: killMultiplier > 0 ? warnSec * killMultiplier : 0,
        source: "env",
        isColdStart: false,
      };
      result[stage] = {};
      for (const mode of CALIBRATION_MODES) {
        result[stage][mode] = data;
      }
      continue;
    }

    const configThreshold = configThresholds[stage];
    const hasConfigOverride = configThreshold !== undefined && configThreshold !== staticDefaultSec;
    if (hasConfigOverride) {
      const warnSec = configThreshold;
      const data: CalibratedStallData = {
        warnSec,
        killSec: killMultiplier > 0 ? warnSec * killMultiplier : 0,
        source: "config",
        isColdStart: false,
      };
      result[stage] = {};
      for (const mode of CALIBRATION_MODES) {
        result[stage][mode] = data;
      }
      continue;
    }

    // ---- mode-filtered calibration ----
    // First pass: compute the per-mode raw result. Cold start when below
    // min_runs. Cell-level errors fall back to cold-start (matching #2654).
    const modeStats: Partial<Record<PerformanceMode, { warnSec: number; killSec: number } | null>> =
      {};
    for (const mode of CALIBRATION_MODES) {
      try {
        const stats = await StageDurationAnalyzer.getStageStatsByMode(
          workspaceRoot,
          stage,
          mode,
          30
        );
        if (stats && stats.count >= minRuns) {
          const p95Sec = stats.p95_ms / 1000;
          const maxSec = stats.max_ms / 1000;
          const warnSec = roundUpTo30s(p95Sec * 1.5);
          const killSec = computeKillThreshold(maxSec, warnSec);
          modeStats[mode] = { warnSec, killSec };
        } else {
          modeStats[mode] = null; // signals "no per-mode data"
        }
      } catch (err) {
        console.error(
          `[Nightgauge] precomputeCalibratedStallThresholds: failed for stage '${stage}' mode '${mode}':`,
          err
        );
        modeStats[mode] = null;
      }
    }

    const elevatedBucket = modeStats["elevated"];
    result[stage] = {};
    for (const mode of CALIBRATION_MODES) {
      const own = modeStats[mode];
      if (own) {
        result[stage][mode] = {
          warnSec: own.warnSec,
          killSec: own.killSec,
          source: "calibrated",
          isColdStart: false,
        };
        continue;
      }
      // Per-mode bucket lacks samples — fall back to elevated bucket if it
      // qualifies. This is the AC3 behavior (issue #3216).
      if (mode !== "elevated" && elevatedBucket) {
        result[stage][mode] = {
          warnSec: elevatedBucket.warnSec,
          killSec: elevatedBucket.killSec,
          source: "calibrated",
          isColdStart: false,
        };
        continue;
      }
      // Cold start — warn at static default, kill disabled
      result[stage][mode] = {
        warnSec: staticDefaultSec,
        killSec: 0,
        source: "static",
        isColdStart: true,
      };
    }
  }

  _calibratedStallCache.set(workspaceRoot, result);
}

/**
 * Get pre-computed calibrated stall data for a specific (stage, mode) bucket.
 *
 * Returns `undefined` when `precomputeCalibratedStallThresholds()` has not
 * been called for the given workspaceRoot, or when calibration is disabled
 * (`min_runs = 0`).
 *
 * Issue #3216: when `mode` is omitted, resolves via `getPerformanceMode()` —
 * the same source of truth used elsewhere in monitoringResolver. The `size`
 * parameter is reserved for future per-size keying and is currently ignored
 * (kept on the signature so callers can start threading it).
 *
 * @param workspaceRoot - Absolute path to the repository root
 * @param stage - Pipeline stage name (e.g., 'feature-dev')
 * @param mode - Performance mode (defaults to active mode resolved per workspace)
 * @param _size - Reserved for future per-size calibration keying; currently ignored
 * @returns Calibrated stall data or undefined if not pre-computed
 *
 * @see Issue #2654 - History-calibrated stall thresholds
 * @see Issue #3216 - Calibration bucketing by (size, mode)
 */
export function getCalibratedStallData(
  workspaceRoot: string,
  stage: string,
  mode?: PerformanceMode,
  _size?: string
): CalibratedStallData | undefined {
  const stageBuckets = _calibratedStallCache.get(workspaceRoot)?.[stage];
  if (!stageBuckets) return undefined;
  const resolvedMode: PerformanceMode = mode ?? getPerformanceMode(workspaceRoot);
  return stageBuckets[resolvedMode];
}

// ============================================================================
// Large Diff Threshold
// ============================================================================

/**
 * Get the lines-changed threshold at which pr-create escalates from haiku to sonnet.
 * When the diff exceeds this value, sonnet is used to avoid stalls on large PRs.
 * Returns 0 when escalation is disabled. Default: 500.
 */
export function getLargeDiffThreshold(workspaceRoot?: string): number {
  const DEFAULT_THRESHOLD = 500;

  const envValue = process.env.NIGHTGAUGE_PIPELINE_LARGE_DIFF_THRESHOLD;
  if (envValue) {
    const parsed = Number.parseInt(envValue, 10);
    if (!Number.isNaN(parsed) && parsed >= 0) {
      return parsed;
    }
  }

  const root = workspaceRoot ?? vscode.workspace.workspaceFolders?.[0]?.uri.fsPath;
  if (!root) {
    return DEFAULT_THRESHOLD;
  }

  try {
    const pathResult = resolveConfigPathSync(root);
    if (!pathResult.exists) {
      return DEFAULT_THRESHOLD;
    }

    const configContent = readEffectiveConfigTextSync(pathResult);
    const lines = configContent.split("\n");
    let inPipeline = false;

    for (const line of lines) {
      const trimmed = line.trim();

      if (trimmed === "pipeline:") {
        inPipeline = true;
        continue;
      }

      if (inPipeline && trimmed && !trimmed.startsWith("#") && !line.startsWith(" ")) {
        inPipeline = false;
      }

      if (inPipeline) {
        const match = trimmed.match(/^large_diff_threshold:\s*(\d+)/);
        if (match) {
          const parsed = Number.parseInt(match[1], 10);
          if (!Number.isNaN(parsed) && parsed >= 0) {
            return parsed;
          }
        }
      }
    }

    return DEFAULT_THRESHOLD;
  } catch {
    return DEFAULT_THRESHOLD;
  }
}

// ============================================================================
// Experiment Configuration (Issue #949)
// ============================================================================

/**
 * Experiment configuration from model_routing.experiment
 *
 * @see Issue #949 - A/B Testing Framework
 */
export interface ExperimentConfigResult {
  name: string;
  active: boolean;
  control: { model: DefaultModel; effort?: ClaudeEffort };
  treatment: { model: DefaultModel; effort?: ClaudeEffort };
  split_percent: number;
  target_stages?: string[];
  min_runs: number;
  /** Minimum runs per group before auto-evaluation triggers (Issue #1396). Default: 10 */
  observation_window: number;
  /** Minimum success_rate_delta for treatment to graduate (Issue #1396). Default: 0.05 */
  min_effect_size: number;
}

/**
 * Get the active experiment configuration from model_routing.experiment.
 *
 * Returns null if no experiment is configured or the experiment is not active.
 *
 * @param workspaceRoot - Workspace root path (optional, auto-detected)
 * @returns Experiment config or null
 *
 * @see Issue #949 - A/B Testing Framework
 */
export function getExperimentConfig(workspaceRoot?: string): ExperimentConfigResult | null {
  const root = workspaceRoot ?? vscode.workspace.workspaceFolders?.[0]?.uri.fsPath;
  if (!root) {
    return null;
  }

  try {
    const pathResult = resolveConfigPathSync(root);
    if (!pathResult.exists) {
      return null;
    }

    if (pathResult.isLegacy) {
      logDeprecationWarning(pathResult.path);
    }

    const configContent = readEffectiveConfigTextSync(pathResult);

    // Use yaml package for proper nested parsing
    // eslint-disable-next-line @typescript-eslint/no-require-imports
    const yaml = require("yaml") as { parse: (s: string) => unknown };
    const parsed = yaml.parse(configContent) as Record<string, unknown> | null;
    if (!parsed) {
      return null;
    }

    const modelRouting = parsed.model_routing as Record<string, unknown> | undefined;
    if (!modelRouting?.experiment) {
      return null;
    }

    const exp = modelRouting.experiment as Record<string, unknown>;
    if (!exp.name || !exp.active) {
      return null;
    }

    const validModels: DefaultModel[] = ["sonnet", "opus", "haiku"];
    const control = exp.control as Record<string, unknown> | undefined;
    const treatment = exp.treatment as Record<string, unknown> | undefined;

    if (!control?.model || !treatment?.model) {
      return null;
    }

    const controlModel = String(control.model) as DefaultModel;
    const treatmentModel = String(treatment.model) as DefaultModel;

    if (!validModels.includes(controlModel) || !validModels.includes(treatmentModel)) {
      return null;
    }

    const validEfforts: ClaudeEffort[] = ["low", "medium", "high"];
    const controlEffort = control.effort
      ? validEfforts.includes(String(control.effort) as ClaudeEffort)
        ? (String(control.effort) as ClaudeEffort)
        : undefined
      : undefined;
    const treatmentEffort = treatment.effort
      ? validEfforts.includes(String(treatment.effort) as ClaudeEffort)
        ? (String(treatment.effort) as ClaudeEffort)
        : undefined
      : undefined;

    return {
      name: String(exp.name),
      active: true,
      control: { model: controlModel, effort: controlEffort },
      treatment: { model: treatmentModel, effort: treatmentEffort },
      split_percent: typeof exp.split_percent === "number" ? exp.split_percent : 50,
      target_stages: Array.isArray(exp.target_stages) ? (exp.target_stages as string[]) : undefined,
      min_runs: typeof exp.min_runs === "number" ? exp.min_runs : 20,
      observation_window: typeof exp.observation_window === "number" ? exp.observation_window : 10,
      min_effect_size: typeof exp.min_effect_size === "number" ? exp.min_effect_size : 0.05,
    };
  } catch (error) {
    console.error("Failed to read experiment config:", error);
    return null;
  }
}

// ============================================================================
// Context File Size Alert Threshold (Issue #1009)
// ============================================================================

/**
 * Get the context file size alert threshold in bytes (Issue #1009)
 *
 * Returns the configured `pipeline.context_file_size_alert_threshold_bytes`
 * value, or the default of 102400 (100KB).
 *
 * @param workspaceRoot - Workspace root path (optional, auto-detected)
 * @returns Threshold in bytes
 */
export function getContextFileSizeAlertThreshold(workspaceRoot?: string): number {
  const DEFAULT_THRESHOLD = 102400; // 100KB

  // Check environment variable first
  const envThreshold = process.env.NIGHTGAUGE_PIPELINE_CONTEXT_FILE_SIZE_ALERT_THRESHOLD_BYTES;
  if (envThreshold) {
    const parsed = Number.parseInt(envThreshold, 10);
    if (!Number.isNaN(parsed) && parsed >= 0) {
      return parsed;
    }
  }

  const root = workspaceRoot ?? vscode.workspace.workspaceFolders?.[0]?.uri.fsPath;
  if (!root) {
    return DEFAULT_THRESHOLD;
  }

  try {
    const pathResult = resolveConfigPathSync(root);
    if (!pathResult.exists) {
      return DEFAULT_THRESHOLD;
    }

    if (pathResult.isLegacy) {
      logDeprecationWarning(pathResult.path);
    }

    const configContent = readEffectiveConfigTextSync(pathResult);
    const lines = configContent.split("\n");
    let inPipeline = false;

    for (const line of lines) {
      const trimmed = line.trim();

      if (trimmed === "pipeline:") {
        inPipeline = true;
        continue;
      }

      // Exit pipeline section on new top-level key
      if (inPipeline && trimmed && !line.startsWith(" ") && /^[a-z_]+:/.test(trimmed)) {
        inPipeline = false;
      }

      if (inPipeline && trimmed.startsWith("context_file_size_alert_threshold_bytes:")) {
        const value = trimmed.replace("context_file_size_alert_threshold_bytes:", "").trim();
        const parsed = Number.parseInt(value, 10);
        if (!Number.isNaN(parsed) && parsed >= 0) {
          return parsed;
        }
      }
    }
  } catch {
    // Non-critical: fall through to default
  }

  return DEFAULT_THRESHOLD;
}

// ============================================================================
// Alerting Configuration (Issue #1048)
// ============================================================================

export interface AlertingConfig {
  enabled: boolean;
  /** @deprecated Use cost_anomaly_min_usd instead. Mapped on read for backward compat. */
  cost_threshold_usd?: number;
  /** Multiplier on estimated cost — alert when actual > estimated × ratio (default: 2.0) */
  cost_anomaly_ratio: number;
  /** Minimum cost floor — alert only when actual exceeds this USD amount (default: 3.0) */
  cost_anomaly_min_usd: number;
  duration_threshold_minutes: number;
}

export const DEFAULT_ALERTING_CONFIG: AlertingConfig = {
  enabled: true,
  cost_anomaly_ratio: 2.0,
  cost_anomaly_min_usd: 3.0,
  duration_threshold_minutes: 32,
};

/**
 * Get pipeline alerting configuration.
 *
 * Reads from env vars → config.yaml → defaults.
 *
 * @see Issue #1048 - Automated cost/duration alerting
 * @see Issue #1335 - Replace flat cost threshold with ratio-based anomaly detection
 */
export function getAlertingConfig(workspaceRoot?: string): AlertingConfig {
  const config: AlertingConfig = { ...DEFAULT_ALERTING_CONFIG };

  // Environment variable overrides
  if (process.env.NIGHTGAUGE_PIPELINE_ALERTING_ENABLED !== undefined) {
    config.enabled = process.env.NIGHTGAUGE_PIPELINE_ALERTING_ENABLED !== "false";
  }
  if (process.env.NIGHTGAUGE_PIPELINE_ALERTING_COST_ANOMALY_RATIO) {
    const parsed = Number.parseFloat(process.env.NIGHTGAUGE_PIPELINE_ALERTING_COST_ANOMALY_RATIO);
    if (!Number.isNaN(parsed) && parsed >= 1) {
      config.cost_anomaly_ratio = parsed;
    }
  }
  if (process.env.NIGHTGAUGE_PIPELINE_ALERTING_COST_ANOMALY_MIN_USD) {
    const parsed = Number.parseFloat(process.env.NIGHTGAUGE_PIPELINE_ALERTING_COST_ANOMALY_MIN_USD);
    if (!Number.isNaN(parsed) && parsed >= 0) {
      config.cost_anomaly_min_usd = parsed;
    }
  }
  // Backward compat: COST_THRESHOLD_USD maps to cost_anomaly_min_usd
  if (process.env.NIGHTGAUGE_PIPELINE_ALERTING_COST_THRESHOLD_USD) {
    const parsed = Number.parseFloat(process.env.NIGHTGAUGE_PIPELINE_ALERTING_COST_THRESHOLD_USD);
    if (!Number.isNaN(parsed) && parsed >= 0) {
      config.cost_anomaly_min_usd = parsed;
    }
  }
  if (process.env.NIGHTGAUGE_PIPELINE_ALERTING_DURATION_THRESHOLD_MINUTES) {
    const parsed = Number.parseFloat(
      process.env.NIGHTGAUGE_PIPELINE_ALERTING_DURATION_THRESHOLD_MINUTES
    );
    if (!Number.isNaN(parsed) && parsed >= 0) {
      config.duration_threshold_minutes = parsed;
    }
  }

  // If any env vars set, return early
  if (
    process.env.NIGHTGAUGE_PIPELINE_ALERTING_ENABLED !== undefined ||
    process.env.NIGHTGAUGE_PIPELINE_ALERTING_COST_ANOMALY_RATIO ||
    process.env.NIGHTGAUGE_PIPELINE_ALERTING_COST_ANOMALY_MIN_USD ||
    process.env.NIGHTGAUGE_PIPELINE_ALERTING_COST_THRESHOLD_USD ||
    process.env.NIGHTGAUGE_PIPELINE_ALERTING_DURATION_THRESHOLD_MINUTES
  ) {
    return config;
  }

  // Read from config.yaml
  const root = workspaceRoot ?? vscode.workspace.workspaceFolders?.[0]?.uri.fsPath;
  if (!root) {
    return config;
  }

  try {
    const pathResult = resolveConfigPathSync(root);
    if (!pathResult.exists) {
      return config;
    }

    if (pathResult.isLegacy) {
      logDeprecationWarning(pathResult.path);
    }

    const configContent = readEffectiveConfigTextSync(pathResult);
    const lines = configContent.split("\n");
    let inPipeline = false;
    let inAlerting = false;

    for (const line of lines) {
      const trimmed = line.trim();

      if (trimmed === "pipeline:") {
        inPipeline = true;
        continue;
      }

      if (inPipeline && trimmed === "alerting:") {
        inAlerting = true;
        continue;
      }

      // Exit sections on new top-level key
      if (trimmed && !trimmed.startsWith("#") && /^[a-z_]+:/.test(trimmed)) {
        if (!line.startsWith(" ")) {
          inPipeline = false;
          inAlerting = false;
        } else if (line.match(/^ {2}[a-z_]+:/) && trimmed !== "alerting:") {
          inAlerting = false;
        }
      }

      if (inAlerting) {
        const match = trimmed.match(/^([a-z_]+):\s*(.+)$/);
        if (match) {
          const [, key, value] = match;
          switch (key) {
            case "enabled":
              config.enabled = value === "true";
              break;
            case "cost_anomaly_ratio": {
              const parsed = Number.parseFloat(value);
              if (!Number.isNaN(parsed) && parsed >= 1) {
                config.cost_anomaly_ratio = parsed;
              }
              break;
            }
            case "cost_anomaly_min_usd": {
              const parsed = Number.parseFloat(value);
              if (!Number.isNaN(parsed) && parsed >= 0) {
                config.cost_anomaly_min_usd = parsed;
              }
              break;
            }
            case "cost_threshold_usd": {
              // Backward compat: map to cost_anomaly_min_usd on read
              const parsed = Number.parseFloat(value);
              if (!Number.isNaN(parsed) && parsed >= 0) {
                config.cost_anomaly_min_usd = parsed;
              }
              break;
            }
            case "duration_threshold_minutes": {
              const parsed = Number.parseFloat(value);
              if (!Number.isNaN(parsed) && parsed >= 0) {
                config.duration_threshold_minutes = parsed;
              }
              break;
            }
          }
        }
      }
    }
  } catch {
    // Non-critical: fall through to defaults
  }

  return config;
}

// ============================================================================
// MCP Tools Config (Issue #1725, #1726)
// ============================================================================

/**
 * Read per-stage MCP tool overrides from pipeline.stages.<stage>.mcp_tools.
 *
 * Config.yaml format:
 * ```yaml
 * pipeline:
 *   stages:
 *     feature-dev:
 *       mcp_tools:
 *         - mcp__playwright__*
 *         - mcp__sentry__get_issue
 * ```
 *
 * Returns [] if not configured.
 *
 * @see Issue #1725
 */
export function getStageMcpTools(workspaceRoot: string, stage: PipelineStage): string[] {
  const pathResult = resolveConfigPathSync(workspaceRoot);
  if (!pathResult.exists) return [];

  try {
    const raw = readEffectiveConfigTextSync(pathResult);
    const lines = raw.split("\n");
    let inPipeline = false;
    let inStages = false;
    let inTargetStage = false;
    let inMcpTools = false;
    const results: string[] = [];

    for (const line of lines) {
      const trimmed = line.trim();
      if (trimmed.startsWith("#")) continue;

      if (trimmed === "pipeline:") {
        inPipeline = true;
        continue;
      }
      if (inPipeline && trimmed === "stages:") {
        inStages = true;
        continue;
      }
      if (inStages && trimmed === `${stage}:`) {
        inTargetStage = true;
        continue;
      }
      if (inTargetStage && trimmed === "mcp_tools:") {
        inMcpTools = true;
        continue;
      }

      if (inMcpTools) {
        if (trimmed.startsWith("- ")) {
          results.push(trimmed.slice(2).trim());
        } else if (trimmed && !trimmed.startsWith("-")) {
          break; // End of mcp_tools list
        }
      }
    }

    return results;
  } catch {
    return [];
  }
}

/**
 * Get the MCP tools configuration from config.yaml.
 *
 * Reads `pipeline.mcp-tools.global` and `pipeline.mcp-tools.stages.<stage>`
 * and returns the union of all applicable tools for the given stage.
 *
 * Resolution order (all merged via union):
 *   SKILL.md `mcp-tools` ∪ config.yaml `global` ∪ config.yaml `stages.<stage>`
 *
 * When no MCP tools are configured, returns an empty array (no tools added).
 *
 * @param workspaceRoot - Workspace root path (optional, auto-detected if not provided)
 * @param stage - Pipeline stage name for per-stage lookup (optional)
 * @returns Deduplicated array of MCP tool names from config.yaml
 *
 * @see Issue #1726
 */
export function getMcpToolsConfig(workspaceRoot?: string, stage?: string): string[] {
  const root = workspaceRoot ?? vscode.workspace.workspaceFolders?.[0]?.uri.fsPath;
  if (!root) {
    return [];
  }

  try {
    const pathResult = resolveConfigPathSync(root);
    if (!pathResult.exists) {
      return [];
    }

    if (pathResult.isLegacy) {
      logDeprecationWarning(pathResult.path);
    }

    const configContent = readEffectiveConfigTextSync(pathResult);
    const lines = configContent.split("\n");

    let inPipeline = false;
    let inMcpTools = false;
    let inGlobal = false;
    let inStages = false;
    let inTargetStage = false;

    const globalTools: string[] = [];
    const stageTools: string[] = [];

    for (const line of lines) {
      const trimmed = line.trim();

      // Skip comments and blank lines
      if (!trimmed || trimmed.startsWith("#")) {
        continue;
      }

      // Detect top-level section transitions
      if (/^[a-z_-]+:/.test(trimmed) && !line.startsWith(" ")) {
        inPipeline = trimmed === "pipeline:";
        inMcpTools = false;
        inGlobal = false;
        inStages = false;
        inTargetStage = false;
        continue;
      }

      if (!inPipeline) {
        continue;
      }

      // Detect `mcp-tools:` under pipeline (2-space indent)
      if (/^ {2}mcp-tools:/.test(line)) {
        inMcpTools = true;
        inGlobal = false;
        inStages = false;
        inTargetStage = false;
        continue;
      }

      // Exit mcp-tools on other pipeline-level keys (2-space indent)
      if (/^ {2}[a-z_-]+:/.test(line) && !line.startsWith("   ")) {
        inMcpTools = false;
        inGlobal = false;
        inStages = false;
        inTargetStage = false;
        continue;
      }

      if (!inMcpTools) {
        continue;
      }

      // Detect `global:` under mcp-tools (4-space indent)
      if (/^ {4}global:/.test(line)) {
        inGlobal = true;
        inStages = false;
        inTargetStage = false;
        continue;
      }

      // Detect `stages:` under mcp-tools (4-space indent)
      if (/^ {4}stages:/.test(line)) {
        inGlobal = false;
        inStages = true;
        inTargetStage = false;
        continue;
      }

      // Exit global/stages on other mcp-tools-level keys (4-space indent)
      if (/^ {4}[a-z_-]+:/.test(line) && !line.startsWith("     ")) {
        inGlobal = false;
        inStages = false;
        inTargetStage = false;
        continue;
      }

      // Collect global list items (6-space "  - " under global)
      if (inGlobal) {
        const listMatch = trimmed.match(/^-\s+(.+)$/);
        if (listMatch) {
          globalTools.push(listMatch[1].trim());
        }
        continue;
      }

      if (!inStages) {
        continue;
      }

      // Detect target stage key under stages (6-space indent)
      if (/^ {6}[a-z]/.test(line)) {
        const stageMatch = trimmed.match(/^([a-z][-a-z]*):\s*$/);
        if (stageMatch) {
          inTargetStage = stage !== undefined && stageMatch[1] === stage;
        }
        continue;
      }

      // Collect per-stage list items (8-space "  - " under stage key)
      if (inTargetStage) {
        const listMatch = trimmed.match(/^-\s+(.+)$/);
        if (listMatch) {
          stageTools.push(listMatch[1].trim());
        }
      }
    }

    // Return deduplicated union of global + stage-specific tools
    const combined = new Set([...globalTools, ...stageTools]);
    return Array.from(combined);
  } catch {
    return [];
  }
}

// ============================================================================
// Audit Configuration (Issue #1582)
// ============================================================================

/**
 * Read audit event client configuration from .nightgauge/config.yaml.
 *
 * Maps the VSCode-scoped `audit:` config section to the SDK's `AuditConfig`
 * type, supplying defaults for SDK fields not exposed in the YAML schema
 * (offlineQueueMaxSize, retryMaxAttempts, retryBackoffMs, timeoutMs).
 *
 * Audit is opt-in: returns `enabled: false` when no config is present.
 *
 * @see Issue #1582 - Pipeline execution audit trail emission
 */
export function getAuditConfig(workspaceRoot?: string): AuditConfig {
  const defaults: AuditConfig = {
    enabled: false,
    platformUrl: undefined,
    apiKey: undefined,
    batchSize: 50,
    flushIntervalMs: 30_000,
    offlineQueuePath: ".nightgauge/audit-queue.json",
    offlineQueueMaxSize: 10_000,
    retryMaxAttempts: 3,
    retryBackoffMs: 1_000,
    timeoutMs: 5_000,
  };

  // Check environment variable overrides first
  if (process.env.NIGHTGAUGE_AUDIT_ENABLED !== undefined) {
    defaults.enabled = process.env.NIGHTGAUGE_AUDIT_ENABLED === "true";
  }
  if (process.env.NIGHTGAUGE_AUDIT_PLATFORM_URL) {
    defaults.platformUrl = process.env.NIGHTGAUGE_AUDIT_PLATFORM_URL;
  }
  if (process.env.NIGHTGAUGE_AUDIT_API_KEY) {
    defaults.apiKey = process.env.NIGHTGAUGE_AUDIT_API_KEY;
  }

  const root = workspaceRoot ?? vscode.workspace.workspaceFolders?.[0]?.uri.fsPath;
  if (!root) {
    return defaults;
  }

  try {
    const pathResult = resolveConfigPathSync(root);
    if (!pathResult.exists) {
      return defaults;
    }

    if (pathResult.isLegacy) {
      logDeprecationWarning(pathResult.path);
    }

    const configContent = readEffectiveConfigTextSync(pathResult);
    const lines = configContent.split("\n");
    let inAudit = false;

    for (const line of lines) {
      const trimmed = line.trim();

      // Detect audit: top-level section
      if (trimmed === "audit:") {
        inAudit = true;
        continue;
      }

      // Exit audit section on new top-level key
      if (
        inAudit &&
        trimmed &&
        !trimmed.startsWith("#") &&
        /^[a-z_]+:/.test(trimmed) &&
        !line.startsWith(" ")
      ) {
        inAudit = false;
        continue;
      }

      if (!inAudit) continue;

      const match = trimmed.match(/^([a-z_]+):\s*(.+)$/);
      if (!match) continue;
      const [, key, value] = match;

      switch (key) {
        case "enabled":
          defaults.enabled = value === "true";
          break;
        case "platform_url":
        case "platformUrl":
          defaults.platformUrl = value.trim();
          break;
        case "api_key":
        case "apiKey":
          defaults.apiKey = value.trim();
          break;
        case "batch_size":
        case "batchSize": {
          const parsed = Number.parseInt(value, 10);
          if (!Number.isNaN(parsed) && parsed > 0) {
            defaults.batchSize = parsed;
          }
          break;
        }
        case "flush_interval_ms":
        case "flushIntervalMs": {
          const parsed = Number.parseInt(value, 10);
          if (!Number.isNaN(parsed) && parsed > 0) {
            defaults.flushIntervalMs = parsed;
          }
          break;
        }
        case "offline_queue_path":
        case "offlineQueuePath":
          defaults.offlineQueuePath = value.trim();
          break;
      }
    }
  } catch {
    // Non-critical — return defaults on any parse error
  }

  return defaults;
}

// ============================================================================
// Performance Mode (Issue #3009 — replaces Supercharge from #2433)
// ============================================================================

import { DEFAULT_PERFORMANCE_MODE, isPerformanceMode, type PerformanceMode } from "../modeProfiles";

const PERFORMANCE_MODE_STATE_FILENAME = "performance-mode.yaml";
const SUPERCHARGE_STATE_FILENAME = "supercharge.yaml";

function getPerformanceModeStatePath(workspaceRoot: string): string {
  return path.join(workspaceRoot, ".nightgauge", PERFORMANCE_MODE_STATE_FILENAME);
}

/**
 * Get the path to the legacy supercharge state file.
 * Used only by the migration helper — production read paths use the new
 * performance-mode.yaml file.
 */
export function getLegacySuperchargeStatePath(workspaceRoot: string): string {
  return path.join(workspaceRoot, ".nightgauge", SUPERCHARGE_STATE_FILENAME);
}

/**
 * Read the performance-mode state file. Returns the parsed mode or
 * `undefined` when the file does not exist or cannot be parsed.
 *
 * State file format (YAML):
 * ```yaml
 * mode: maximum
 * activated_at: "2026-04-25T10:00:00Z"
 * ```
 */
function readPerformanceModeStateFile(workspaceRoot: string): PerformanceMode | undefined {
  try {
    const statePath = getPerformanceModeStatePath(workspaceRoot);
    if (!fs.existsSync(statePath)) {
      return undefined;
    }
    const content = fs.readFileSync(statePath, "utf-8");
    const match = content.match(/^mode:\s*['"]?([a-z]+)['"]?\s*$/m);
    if (match && isPerformanceMode(match[1])) {
      return match[1];
    }
  } catch {
    // Non-critical — fall through to undefined
  }
  return undefined;
}

/**
 * Write the performance-mode state file.
 *
 * @param workspaceRoot - Workspace root path
 * @param mode - The mode to persist
 */
export function writePerformanceModeStateFile(workspaceRoot: string, mode: PerformanceMode): void {
  const statePath = getPerformanceModeStatePath(workspaceRoot);
  const timestamp = new Date().toISOString();
  const content = `mode: ${mode}\nactivated_at: "${timestamp}"\n`;
  fs.mkdirSync(path.dirname(statePath), { recursive: true });
  fs.writeFileSync(statePath, content, "utf-8");
}

/**
 * Resolve the active performance mode.
 *
 * Precedence:
 * 1. `NIGHTGAUGE_PERFORMANCE_MODE` env var (always wins).
 * 2. `.nightgauge/performance-mode.yaml` in the primary VS Code workspace.
 * 3. Same file under the passed-in `workspaceRoot` (for tests / multi-repo).
 * 4. `DEFAULT_PERFORMANCE_MODE` (`elevated`).
 *
 * The primary-workspace check matches today's Supercharge fallback — the
 * status-bar QuickPick writes to the primary workspace, but pipeline stages
 * may run under a different root (concurrent-pipeline worktrees #1621 or a
 * secondary repo in a multi-repo workspace).
 *
 * @see Issue #3009 - Performance mode selector
 */
export function getPerformanceMode(workspaceRoot?: string): PerformanceMode {
  const envVal = process.env.NIGHTGAUGE_PERFORMANCE_MODE?.trim().toLowerCase();
  if (envVal && isPerformanceMode(envVal)) {
    return envVal;
  }

  const primary = vscode.workspace.workspaceFolders?.[0]?.uri.fsPath;
  if (primary) {
    const fromPrimary = readPerformanceModeStateFile(primary);
    if (fromPrimary) {
      return fromPrimary;
    }
  }

  if (workspaceRoot && workspaceRoot !== primary) {
    const fromStage = readPerformanceModeStateFile(workspaceRoot);
    if (fromStage) {
      return fromStage;
    }
  }

  return DEFAULT_PERFORMANCE_MODE;
}

/**
 * @deprecated Issue #3009 — use `getPerformanceMode()` instead.
 * Kept for one release so external callers continue to compile. Returns
 * `true` only when the resolved mode is `maximum`.
 */
export function isSuperchargeModeActive(workspaceRoot?: string): boolean {
  return getPerformanceMode(workspaceRoot) === "maximum";
}

/**
 * @deprecated Issue #3009 — use `writePerformanceModeStateFile()` instead.
 * Maps `active=true` → `maximum`, `active=false` → `elevated`.
 */
export function writeSuperchargeStateFile(workspaceRoot: string, active: boolean): void {
  writePerformanceModeStateFile(workspaceRoot, active ? "maximum" : "elevated");
}

/**
 * Get the Codex model override for the active performance mode.
 *
 * Resolution order matches the legacy supercharge path (preserved for one
 * release): env var > `pipeline.performance_mode.maximum.codex_model`
 * (or legacy `pipeline.supercharge.codex_model`) > Codex daemon catalog.
 *
 * Returns `undefined` for `efficiency` and `elevated` so the caller falls
 * back to per-stage Codex resolution.
 */
export function getModeStageCodexModel(
  _stage: PipelineStage,
  mode: PerformanceMode,
  workspaceRoot?: string
): string | undefined {
  if (mode !== "maximum") {
    return undefined;
  }
  return getSuperchargeCodexModel(workspaceRoot);
}

/**
 * Get the model used in `maximum` mode (legacy supercharge envelope).
 *
 * Reads `pipeline.performance_mode.maximum.model` (or legacy
 * `pipeline.supercharge.model`) from config, defaulting to 'opus'.
 *
 * @param workspaceRoot - Workspace root path
 * @returns 'opus' (default) or 'sonnet' as configured
 */
export function getSuperchargeModel(workspaceRoot?: string): "opus" | "sonnet" {
  // Match isSuperchargeModeActive — the config lives in the primary VS Code
  // workspace, but stages may run under a different root. Prefer the primary
  // workspace so the configured model is honored everywhere.
  const primary = vscode.workspace.workspaceFolders?.[0]?.uri.fsPath;
  const root = primary ?? workspaceRoot;
  if (!root) {
    return "opus";
  }

  try {
    const pathResult = resolveConfigPathSync(root);
    if (!pathResult.exists) {
      return "opus";
    }
    const content = readEffectiveConfigTextSync(pathResult);
    const lines = content.split("\n");
    let inPipeline = false;
    let inSupercharge = false;

    for (const line of lines) {
      const trimmed = line.trim();

      if (trimmed === "pipeline:") {
        inPipeline = true;
        continue;
      }
      if (inPipeline && trimmed === "supercharge:") {
        inSupercharge = true;
        continue;
      }

      // Exit sections on new top-level or section-level key
      if (trimmed && !trimmed.startsWith("#") && /^[a-z_]+:/.test(trimmed)) {
        if (!line.startsWith(" ")) {
          inPipeline = false;
          inSupercharge = false;
        } else if (line.startsWith("  ") && !line.startsWith("    ")) {
          inSupercharge = false;
        }
      }

      if (inSupercharge) {
        const match = trimmed.match(/^model:\s*['"]?(opus|sonnet)['"]?/);
        if (match) {
          return match[1] as "opus" | "sonnet";
        }
      }
    }
  } catch {
    // Non-critical — fall through to default
  }

  return "opus";
}

/**
 * Get the Codex model to use in supercharge mode.
 *
 * Resolution order:
 * 1. `NIGHTGAUGE_SUPERCHARGE_CODEX_MODEL` env var (explicit override)
 * 2. `pipeline.supercharge.codex_model` in `.nightgauge/config.yaml`
 * 3. **Dynamic top-tier from the Codex daemon's model catalog**
 *    (`~/.codex/models_cache.json`). The daemon populates this cache based
 *    on the authenticated user's plan tier, so the first entry (after
 *    priority sort) is the strongest model the user is entitled to. This
 *    is why we don't hardcode a heavy-tier default — plan tiers and model
 *    lineups change independently of extension releases.
 * 4. `undefined` — caller falls back to the registry's opus tier
 *    (`CODEX_TIER_MODEL_MAP.opus`) via `resolveCodexPipelineModel("opus")`
 *    only when the daemon cache is absent or unreadable.
 *
 * @see CodexModelCatalogService — same source the settings page uses.
 */
export function getSuperchargeCodexModel(workspaceRoot?: string): string | undefined {
  const envModel = process.env.NIGHTGAUGE_SUPERCHARGE_CODEX_MODEL;
  if (envModel && envModel.trim()) {
    return envModel.trim();
  }

  const primary = vscode.workspace.workspaceFolders?.[0]?.uri.fsPath;
  const root = primary ?? workspaceRoot;

  if (root) {
    try {
      const pathResult = resolveConfigPathSync(root);
      if (pathResult.exists) {
        const content = readEffectiveConfigTextSync(pathResult);
        const lines = content.split("\n");
        let inPipeline = false;
        let inSupercharge = false;

        for (const line of lines) {
          const trimmed = line.trim();

          if (trimmed === "pipeline:") {
            inPipeline = true;
            continue;
          }
          if (inPipeline && trimmed === "supercharge:") {
            inSupercharge = true;
            continue;
          }

          if (trimmed && !trimmed.startsWith("#") && /^[a-z_]+:/.test(trimmed)) {
            if (!line.startsWith(" ")) {
              inPipeline = false;
              inSupercharge = false;
            } else if (line.startsWith("  ") && !line.startsWith("    ")) {
              inSupercharge = false;
            }
          }

          if (inSupercharge) {
            const match = trimmed.match(/^codex_model:\s*['"]?([^#"'\n]+?)['"]?(?:\s+#.*)?$/);
            if (match) {
              return match[1].trim();
            }
          }
        }
      }
    } catch {
      // Non-critical — fall through to dynamic discovery
    }
  }

  // Dynamic discovery via the Codex daemon's model cache — the same source
  // the settings page reads. The daemon populates this cache based on the
  // authenticated user's plan tier, so models[0] is the strongest entry
  // the user is entitled to (priority-sorted inside listModels()).
  try {
    const models = new CodexModelCatalogService().listModels();
    if (models.length > 0) {
      return models[0];
    }
  } catch {
    // Service unavailable — caller falls back to the registry's opus tier
    // (CODEX_TIER_MODEL_MAP.opus) via resolveCodexPipelineModel("opus").
  }

  return undefined;
}
