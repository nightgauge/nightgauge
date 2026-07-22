/**
 * AutoRetroService - Automatic failure analysis after pipeline failures
 *
 * Performs deterministic failure classification and writes a structured retro
 * JSON to .nightgauge/retros/. Optionally creates GitHub issues for
 * actionable findings.
 *
 * Design rules:
 * - Static class, no constructor — mirrors HealthActionService pattern
 * - Fire-and-forget: never throws, all errors logged as warnings
 * - Never blocks pipeline completion
 * - Config read at invocation time from .nightgauge/config.yaml
 *
 * Classification model (rewritten in #3204):
 *
 *   1. Structured signals first. The skillRunner / OfflineManager / orchestrator
 *      emit deterministic log shapes (`Stage terminated due to stall detection
 *      auto-kill ... "stallKilled":true`, `[cost-cap-exceeded] Stage X
 *      terminated`, `[OfflineManager] degraded -> offline`, V3 RunRecord's
 *      `terminal_failure_kind` field). Those are read FIRST as authoritative
 *      signals. Each maps directly to a category — no string fishing required.
 *
 *   2. Source-tagged keyword matching second. Pre-#3204 the classifier ran a
 *      regex blob over `session_log + pipeline_context + history` joined into
 *      one string. That collided badly: `/type.*error/i` matched `TypeError:
 *      fetch failed` (post-failure DiscordService cleanup) AND `tsc error` from
 *      a real type error — indistinguishable. Now keyword passes are scoped to
 *      the source of the line (subagent stdout vs. extension cleanup logs) so
 *      noise in one cannot pollute the other.
 *
 *   3. Multi-finding output. A run can fail for multiple reasons (e.g. stall-kill
 *      *during* an infrastructure outage). The classifier returns every signal
 *      it finds, with the primary cause first.
 *
 *   4. State-aware overrides remain (shipped-but-overbudget for pr-merge runs
 *      where the PR actually merged out-of-band — see #3108).
 *
 * @see Issue #1408 - Auto-retro after pipeline failure
 * @see Issue #3204 - Auto-retro misclassifies network outages as 'validation-failure'
 */

import * as fs from "node:fs/promises";
import * as path from "node:path";
import type { Logger } from "../utils/logger";
import { IpcClient } from "./IpcClient";
import { getRepoIdentity } from "../utils/configPathResolver";

// ============================================================================
// Types
// ============================================================================

/**
 * Failure categories matching the retro skill taxonomy.
 *
 * Categories added in #3204 (signal-first classifier):
 *
 *   - `stall-kill` — skillRunner forcibly terminated the stage for idle
 *     timeout or hard-cap. The agent went silent (often Bash hang, infinite
 *     tool loop, or stop-hook deadlock). Distinguished from `timeout`, which
 *     historically conflated with skillRunner kills.
 *   - `cost-cap` — skillRunner forcibly terminated the stage for crossing
 *     the per-stage USD ceiling. Indicates a runaway loop that BudgetEnforcer
 *     could not catch via its estimate-vs-actual semantics.
 *   - `infrastructure-outage` — OfflineManager observed a transition to
 *     degraded/offline during the stage, or the session log contains DNS or
 *     transport-level failures concurrent with the kill. With #3203's
 *     pause-on-offline this should rarely classify a failed run, but it
 *     remains the correct signal when the outage outlasts patience.
 *   - `stop-hook-error` — the Claude CLI emitted a `stop-hook-error`
 *     notification mid-stage, after which the subagent went silent and was
 *     stall-killed. Indicates a CLI-level integration bug worth surfacing
 *     rather than burying under "stall".
 *
 * `shipped-but-overbudget` is a state-aware classification: the stage was
 * killed for budget overrun, but the PR actually merged out-of-band. The
 * pipeline should report success and the queue should NOT be cleared. See
 * #3108 for the incident this category was introduced for.
 *
 * @see skills/nightgauge-retro/SKILL.md
 */
export type RetroFailureCategory =
  | "budget-exceeded"
  | "shipped-but-overbudget"
  | "false-negative-shipped"
  | "state-management"
  | "ci-infrastructure"
  | "model-capability"
  | "timeout"
  | "validation-failure"
  | "stall-kill"
  | "cost-cap"
  | "infrastructure-outage"
  | "stop-hook-error"
  | "skill-no-op"
  | "merge-blocked"
  | "adapter-unavailable"
  | "no-adapter-available"
  | "unknown";

export interface RetroFinding {
  category: RetroFailureCategory;
  severity: "low" | "medium" | "high";
  summary: string;
  evidence: string[];
  recommendation: string;
  /** GitHub issue number, set when auto_create_issues created it */
  issueNumber?: number;
}

export interface AutoRetroResult {
  issueNumber: number;
  failedStage: string;
  findings: RetroFinding[];
  retroFile: string;
  issuesCreated: number;
  skippedReason?: string;
}

/** Config values for auto_retro block */
interface AutoRetroConfig {
  enabled: boolean;
  auto_create_issues: boolean;
  severity_threshold: "low" | "medium" | "high";
}

// ============================================================================
// Constants
// ============================================================================

const DEFAULT_CONFIG: AutoRetroConfig = {
  enabled: true,
  auto_create_issues: false,
  severity_threshold: "high",
};

const SEVERITY_RANK: Record<string, number> = { low: 0, medium: 1, high: 2 };

/**
 * Source tag for classifier evidence. Lets the keyword-pass distinguish
 * subagent stdout (where `tsc error` is a real validation failure) from
 * extension cleanup logs (where `TypeError: fetch failed` is post-failure
 * Discord-webhook noise that must NOT be classified as a validation failure).
 *
 * - `subagent`: lines that originated from the Claude/Codex/Gemini subprocess
 *   stdout/stderr captured by skillRunner.
 * - `extension`: lines from the extension's own logger (prefix `[INFO] `,
 *   `[WARN] `, `[ERROR] `) — orchestration, IPC, status updates, cleanup.
 * - `unknown`: source could not be determined; treat as low-confidence.
 */
type EvidenceSource = "subagent" | "extension" | "unknown";

interface TaggedLine {
  source: EvidenceSource;
  text: string;
}

/**
 * One structured signal found in evidence. Drives primary classification.
 * Each entry maps DIRECTLY to a category — no ambiguity, no regex blob.
 */
interface StructuredSignal {
  category: RetroFailureCategory;
  /** Short human-readable description for the finding's evidence list. */
  evidence: string;
  /** Severity hint; classifier may downgrade based on context. */
  severityHint?: "low" | "medium" | "high";
}

/**
 * Source-tagged keyword fallback. Each pattern declares which sources it
 * trusts. A pattern that only fires on `subagent` output cannot be tripped by
 * extension cleanup noise, and vice versa.
 */
interface ClassificationPattern {
  patterns: RegExp[];
  category: RetroFailureCategory;
  /** Sources where these patterns are meaningful. Empty = all sources. */
  sources?: EvidenceSource[];
}

const CLASSIFICATION_PATTERNS: ClassificationPattern[] = [
  // budget-exceeded: BudgetEnforcer messages live in extension logs
  {
    category: "budget-exceeded",
    patterns: [/budget exceeded/i, /token limit/i, /costUsd\s*>\s*budget/i, /cost.*exceeded/i],
    sources: ["extension"],
  },
  // state-management: pipeline-context schema problems — extension orchestration
  {
    category: "state-management",
    patterns: [
      /context file missing/i,
      /missing context file/i,
      /schema validation failed/i,
      /did not write expected output context/i,
    ],
    sources: ["extension"],
  },
  // ci-infrastructure: gh CLI output / workflow status — both subagent (gh run watch)
  // and extension (CI poll) emit these
  {
    category: "ci-infrastructure",
    patterns: [/CI checks failed/i, /workflow run failed/i, /gh run watch.*fail/i],
  },
  // model-capability: empty/garbled model responses — extension parses these
  {
    category: "model-capability",
    patterns: [
      /model returned empty/i,
      /unexpected output format/i,
      /repeated re-prompt/i,
      /output format.*invalid/i,
    ],
    sources: ["extension"],
  },
  // validation-failure: tests / typecheck — ONLY trust the subagent. The same
  // tokens appearing in extension stack traces (TypeError: fetch failed from
  // DiscordService) must NOT trigger this category. This was the #360 / #3204
  // misclassification root cause.
  {
    category: "validation-failure",
    patterns: [
      /\btsc error\b/i,
      /tests? failed/i,
      /\d+ failing\b/i,
      /build failed/i,
      /✗\s+\d+\s+failed/i,
    ],
    sources: ["subagent"],
  },
  // timeout: stage time-budget exhaustion (distinct from skillRunner stall-kill,
  // which is a finer-grained category handled by the structured-signal pass).
  // No source restriction because legacy / configurable timeout messages appear
  // in both extension orchestration logs and free-form text.
  {
    category: "timeout",
    patterns: [
      /timed out/i,
      /exceeded\s+(?:configured\s+)?timeout/i,
      /stage duration exceeded/i,
      /ci_timeout/i,
    ],
  },
];

/**
 * Inputs to a structured-signal extractor.
 *
 * Most extractors only need `text`. The wider signature was added in #3275 so
 * file-existence and stage-scoped extractors can inspect the source list and
 * the failed stage name without re-parsing the joined evidence string.
 */
interface ExtractorInput {
  text: string;
  sourcesAnalyzed: string[];
  failedStage: string;
}

/**
 * Time-correlation gate for the `stop-hook-error` extractor (#3275).
 *
 * The Claude CLI emits a routine `stop-hook-error` notification at the END of
 * every stage as part of its teardown — this notification is NOT a failure
 * cause. Pre-#3275 the extractor matched on string presence and won for
 * almost every failed run, masking the real cause (cost-cap, skill-no-op,
 * shipped-but-failed).
 *
 * The gate distinguishes the two cases:
 *
 *   - Genuine pre-failure stop-hook (the legitimate #3204 case): the
 *     stop-hook-error appears in the evidence corpus BEFORE the stage's
 *     terminal `"type":"result"` event — or no terminal result event ever
 *     landed at all (subagent went silent).
 *   - Routine end-of-stage teardown noise: the stop-hook-error appears
 *     AFTER the LAST `"type":"result"` event, which means the stage already
 *     produced its terminal result and the notification is irrelevant.
 *
 * Returns true when the stop-hook-error match should be treated as a real
 * signal (pre-result OR no result event); false when it is post-result noise.
 */
function isPreResultStopHook(text: string): boolean {
  const stopHookRe = /"key"\s*:\s*"stop-hook-error"|Stop hook error occurred/g;
  const resultRe = /"type"\s*:\s*"result"/g;

  let firstStopHookIdx = -1;
  const sm = stopHookRe.exec(text);
  if (sm) firstStopHookIdx = sm.index;
  if (firstStopHookIdx < 0) return false;

  let lastResultIdx = -1;
  let rm: RegExpExecArray | null;
  while ((rm = resultRe.exec(text)) !== null) {
    lastResultIdx = rm.index;
  }

  // No terminal result event: the run ended without finishing — stop-hook
  // is the genuine cause (the legitimate #3204 silent-hang signature).
  if (lastResultIdx < 0) return true;

  // Stop-hook fired BEFORE the terminal result: real pre-failure signal.
  // Otherwise the notification is post-result teardown noise.
  return firstStopHookIdx < lastResultIdx;
}

/**
 * Structured-signal extractors. Each returns 0-or-1 signal for a given
 * evidence corpus. Order matters when multiple signals are present: the FIRST
 * extractor that fires becomes the primary finding; the rest become secondary.
 *
 * The extractor signature accepts the wider {text, sourcesAnalyzed, failedStage}
 * input (added in #3275) so extractors can inspect the source list (for the
 * file-existence cost-cap signal) and the failed stage (for the pr-merge
 * skill-no-op signal). Most extractors only use `text`.
 */
const SIGNAL_EXTRACTORS: Array<(input: ExtractorInput) => StructuredSignal | null> = [
  // V3 RunRecord field — authoritative when present.
  ({ text }) => {
    const m = text.match(/"terminal_failure_kind"\s*:\s*"([a-z_]+)"/);
    if (!m) return null;
    const kind = m[1];
    const map: Record<string, RetroFailureCategory> = {
      stall_kill: "stall-kill",
      budget_exceeded: "budget-exceeded",
      validation_error: "validation-failure",
      subagent_crash: "stall-kill",
      orchestrator_crash: "state-management",
    };
    const cat = map[kind];
    if (!cat) return null;
    return {
      category: cat,
      evidence: `Run record terminal_failure_kind: ${kind}`,
      severityHint: cat === "stall-kill" ? "medium" : "high",
    };
  },
  // skillRunner stall-kill log line, idle OR hard-cap.
  ({ text }) => {
    if (
      /Stage terminated due to stall detection auto-kill/i.test(text) ||
      /\[stall-killed\]\s+\S+\s+terminated/i.test(text) ||
      /"stallKilled"\s*:\s*true/.test(text) ||
      /subagent process exceeded stall kill threshold/i.test(text)
    ) {
      return {
        category: "stall-kill",
        evidence: "skillRunner forcibly terminated the stage for idle/hard-cap stall",
        severityHint: "medium",
      };
    }
    return null;
  },
  // skillRunner cost-cap-exceeded log line.
  ({ text }) => {
    if (
      /\[cost-cap-exceeded\]\s+Stage\s+\S+\s+terminated/i.test(text) ||
      /cost cap exceeded/i.test(text)
    ) {
      return {
        category: "cost-cap",
        evidence: "Per-stage cost cap fired before budget enforcer could grace-out",
        severityHint: "high",
      };
    }
    return null;
  },
  // #3275 — Deterministic cost-cap signal via diagnostic-file presence.
  // skillRunner ALWAYS writes `<stage>-cost-capped.log` at kill time; the
  // collector includes that filename in `sourcesAnalyzed`. When the textual
  // log line was missed (rotated session log, kill before stdout flushed)
  // the file's presence alone is sufficient evidence.
  ({ sourcesAnalyzed, failedStage }) => {
    const diagFile = `${failedStage}-cost-capped.log`;
    if (!sourcesAnalyzed.includes(diagFile)) return null;
    return {
      category: "cost-cap",
      evidence: `Diagnostic file present: ${diagFile} (skillRunner cost-cap kill)`,
      severityHint: "high",
    };
  },
  // OfflineManager / DNS / transport-level outage during the run.
  ({ text }) => {
    if (
      /\[OfflineManager\][^\n]*->\s*offline/.test(text) ||
      /\[OfflineManager\][^\n]*degraded -> offline/.test(text) ||
      /\[OfflineManager\][^\n]*degraded → offline/.test(text) ||
      /dial tcp:\s*lookup\s+\S+:\s*no such host/.test(text) ||
      /getaddrinfo\s+ENOTFOUND/.test(text)
    ) {
      return {
        category: "infrastructure-outage",
        evidence:
          "Network outage observed during the run (OfflineManager transition or DNS failure)",
        severityHint: "low",
      };
    }
    return null;
  },
  // Issue #3231 — full fallback chain exhausted at stage start. The
  // dispatcher emits `[stage:no-adapter-available]` AFTER walking every
  // candidate in `getEffectiveFallbackChain`. Order matters here: this
  // extractor runs BEFORE the `[stage:adapter-unavailable]` one because
  // both envelopes share the `[stage:` prefix and a chain-exhausted run
  // is a strictly worse signal than primary-only-failed.
  ({ text }) => {
    const m = text.match(/\[stage:no-adapter-available\][^\n]*/);
    if (m) {
      return {
        category: "no-adapter-available",
        evidence: `skillRunner walked the full fallback chain and every candidate failed prereq: ${m[0]}`,
        severityHint: "high",
      };
    }
    return null;
  },
  // Issue #3223 — primary adapter prereq failed AND no fallback was walked
  // (chain disabled by `pipeline.disable_fallback`, empty effective chain,
  // or strict-mode opt-out). Distinct from no-adapter-available so retro
  // can recommend adjusting strict-mode vs. broadening the chain.
  ({ text }) => {
    const m = text.match(/\[stage:adapter-unavailable\][^\n]*/);
    if (m) {
      return {
        category: "adapter-unavailable",
        evidence: `skillRunner halted at stage start — primary adapter prereq failed and fallback was disabled or unavailable: ${m[0]}`,
        severityHint: "high",
      };
    }
    return null;
  },
  // #3924 — pr-merge `merge-blocked`: the PR genuinely cannot merge as-is
  // because of a deterministically-known blocker (a failing non-required
  // check → UNSTABLE, a required review, or a merge conflict). Declining to
  // merge a red PR is CORRECT behaviour, not a skill no-op — so this must
  // classify AHEAD of skill-no-op below. The orchestrator threads the blocker
  // reason into the terminal_reason source, e.g.
  // `blocked by failing check "Sync E2E (Docker)" (mergeStateStatus=UNSTABLE)`.
  ({ text, failedStage }) => {
    if (failedStage !== "pr-merge") return null;
    const m = text.match(
      /blocked by (?:failing check|required review|review|merge conflict|non-mergeable state)[^\n]*/i
    );
    if (m || /mergeStateStatus\s*[=:]\s*"?(?:UNSTABLE|BLOCKED|DIRTY|BEHIND)"?/i.test(text)) {
      return {
        category: "merge-blocked",
        evidence: m
          ? `pr-merge declined: PR ${m[0]}`
          : "pr-merge declined: PR is not in a mergeable state (failing check / required review / conflict)",
        severityHint: "medium",
      };
    }
    return null;
  },
  // #3275 / #3926 — `skill-no-op`: the stage's LLM path reported success but
  // the deterministic post-condition gate found the work never landed. Covers
  // pr-merge (PR not merged) and pr-create (no open PR exists). The pr-merge
  // regex now tolerates the interpolated PR number — the real orchestrator
  // message is `reported success but PR #73 is not merged`, which the
  // pre-#3926 pattern `/reported success but PR is not merged/i` could not
  // match (the `#73` defeated it), starving this extractor.
  ({ text, failedStage }) => {
    if (failedStage === "pr-merge") {
      if (
        /reported success but PR\s*#?\d*\s*is not merged/i.test(text) ||
        /post[-_ ]merge verification failed/i.test(text) ||
        /"pr_merged"\s*:\s*false/.test(text)
      ) {
        return {
          category: "skill-no-op",
          evidence:
            "pr-merge reported success but post-merge verification found the PR is not actually merged",
          severityHint: "high",
        };
      }
      return null;
    }
    if (failedStage === "pr-create") {
      if (
        /reported success but no (?:open )?PR exists/i.test(text) ||
        /post[-_ ]create verification (?:failed|FAILED)/i.test(text) ||
        /"pr_created"\s*:\s*false/.test(text)
      ) {
        return {
          category: "skill-no-op",
          evidence:
            "pr-create reported success but post-create verification found no open PR exists",
          severityHint: "high",
        };
      }
      return null;
    }
    return null;
  },
  // Claude CLI stop-hook-error notification — the #3204 incident signature.
  // Demoted to LAST in #3275 so any other structured signal wins, and
  // gated by `isPreResultStopHook` so routine post-result teardown noise
  // does not classify a run that already produced a terminal result.
  ({ text }) => {
    if (!/"key"\s*:\s*"stop-hook-error"/.test(text) && !/Stop hook error occurred/.test(text)) {
      return null;
    }
    if (!isPreResultStopHook(text)) return null;
    return {
      category: "stop-hook-error",
      evidence: "Claude CLI emitted a stop-hook-error notification before the stage went silent",
      severityHint: "medium",
    };
  },
];

// ============================================================================
// AutoRetroService
// ============================================================================

export class AutoRetroService {
  /**
   * Run automated retro analysis after a pipeline failure.
   *
   * This is a fire-and-forget method: callers should `void` the result.
   * All errors are caught and logged; never throws.
   *
   * @param workspaceRoot - Absolute path to repo root
   * @param issueNumber - Pipeline issue number
   * @param failedStage - Stage that failed
   * @param logger - Logger instance
   * @returns AutoRetroResult or null if skipped/failed
   */
  static async runAfterFailure(
    workspaceRoot: string,
    issueNumber: number,
    failedStage: string,
    logger: Logger,
    /**
     * The orchestrator's terminal failure reason (the Error message that
     * failed the pipeline), if known. This is the single most diagnostic
     * string for the run — e.g. `pr-merge reported success but PR #73 is not
     * merged (state: OPEN)` — and historically it was logged to the output
     * channel but written to no file the collector reads, starving the
     * classifier and producing the `unknown` epidemic (#3926). Threading it in
     * directly lets the structured extractors fire. Optional for backward
     * compatibility with call sites that have no reason in hand.
     */
    failureReason?: string
  ): Promise<AutoRetroResult | null> {
    try {
      // Step 1: Read config
      const config = await this.readConfig(workspaceRoot);

      if (!config.enabled) {
        logger.info("Auto-retro skipped (disabled in config)", { issueNumber });
        return {
          issueNumber,
          failedStage,
          findings: [],
          retroFile: "",
          issuesCreated: 0,
          skippedReason: "disabled",
        };
      }

      logger.info("Auto-retro analysis starting", { issueNumber, failedStage });

      // Step 2: Collect failure evidence
      const evidence = await this.collectEvidence(
        workspaceRoot,
        issueNumber,
        failedStage,
        logger,
        failureReason
      );

      // Step 3: Classify failure (pattern-match), then state-aware refinement
      // for the shipped-but-overbudget case (#3108): a budget-killed pr-merge
      // stage where the PR actually merged out-of-band should be reclassified
      // as a low-severity success rather than a high-severity budget overrun.
      const findings = this.classifyFailure(evidence, failedStage);
      if (failedStage === "pr-merge") {
        await this.applyShippedButOverbudgetOverride(findings, workspaceRoot, issueNumber, logger);
        // #3275 — Generalize the shipped-but-PR-merged override to any
        // dominant finding (not just budget-exceeded). Runs AFTER the
        // budget override so it does not double-classify.
        await this.applyFalseNegativeShippedOverride(findings, workspaceRoot, issueNumber, logger);
      }

      // Step 4: Ensure output directory
      const retrosDir = path.join(workspaceRoot, ".nightgauge", "retros");
      await fs.mkdir(retrosDir, { recursive: true });

      // Step 5: Write retro JSON
      const today = new Date().toISOString().slice(0, 10); // YYYY-MM-DD
      const retroFileName = `${today}_${issueNumber}_retro.json`;
      const retroFile = path.join(retrosDir, retroFileName);

      const retroPayload = {
        schema_version: "1.0",
        issue_number: issueNumber,
        failed_stage: failedStage,
        created_at: new Date().toISOString(),
        findings,
        sources_analyzed: evidence.sourcesAnalyzed,
      };

      await fs.writeFile(retroFile, JSON.stringify(retroPayload, null, 2), "utf-8");

      logger.info("Auto-retro written", {
        issueNumber,
        retroFile,
        findings: findings.length,
      });

      // Step 6: Auto-create issues if configured
      let issuesCreated = 0;
      if (config.auto_create_issues) {
        issuesCreated = await this.createIssuesForFindings(
          findings,
          config.severity_threshold,
          issueNumber,
          failedStage,
          workspaceRoot,
          logger
        );
      }

      return {
        issueNumber,
        failedStage,
        findings,
        retroFile,
        issuesCreated,
      };
    } catch (err) {
      logger.warn("Auto-retro analysis failed", {
        issueNumber,
        err: err instanceof Error ? err.message : String(err),
      });
      return null;
    }
  }

  // ============================================================================
  // Private helpers
  // ============================================================================

  /**
   * Read auto_retro config from .nightgauge/config.yaml.
   * Falls back to defaults if config is missing or unreadable.
   */
  private static async readConfig(workspaceRoot: string): Promise<AutoRetroConfig> {
    try {
      const configPath = path.join(workspaceRoot, ".nightgauge", "config.yaml");
      const content = await fs.readFile(configPath, "utf-8");
      return this.parseAutoRetroConfig(content);
    } catch {
      return { ...DEFAULT_CONFIG };
    }
  }

  /**
   * Parse auto_retro section from raw YAML content (line-based, no yaml dep).
   */
  static parseAutoRetroConfig(content: string): AutoRetroConfig {
    const config = { ...DEFAULT_CONFIG };
    const lines = content.split("\n");

    let inFeedbackLoop = false;
    let inAutoRetro = false;

    for (const line of lines) {
      const trimmed = line.trim();

      // Detect feedback_loop section
      if (trimmed === "feedback_loop:") {
        inFeedbackLoop = true;
        inAutoRetro = false;
        continue;
      }

      // Detect auto_retro sub-section (indented 2 spaces under feedback_loop)
      if (inFeedbackLoop && line.match(/^\s{2}auto_retro:\s*$/)) {
        inAutoRetro = true;
        continue;
      }

      // Exit feedback_loop on non-indented line
      if (inFeedbackLoop && !line.startsWith(" ") && !line.startsWith("\t") && trimmed.length > 0) {
        inFeedbackLoop = false;
        inAutoRetro = false;
      }

      // Exit auto_retro on 2-space-indented key (sibling of auto_retro)
      if (inAutoRetro && line.match(/^\s{2}\w/) && !line.match(/^\s{4}/)) {
        inAutoRetro = false;
      }

      if (inAutoRetro) {
        const enabledMatch = trimmed.match(/^enabled:\s*(true|false)/);
        if (enabledMatch) {
          config.enabled = enabledMatch[1] === "true";
        }

        const createIssuesMatch = trimmed.match(/^auto_create_issues:\s*(true|false)/);
        if (createIssuesMatch) {
          config.auto_create_issues = createIssuesMatch[1] === "true";
        }

        const thresholdMatch = trimmed.match(/^severity_threshold:\s*(low|medium|high)/);
        if (thresholdMatch) {
          config.severity_threshold = thresholdMatch[1] as "low" | "medium" | "high";
        }
      }
    }

    return config;
  }

  /**
   * Collect failure evidence from available sources.
   *
   * Returns both the joined text (for backwards-compatible regex matching)
   * AND the per-line tagged form (for source-aware classification).
   *
   * Each source is optional — missing sources are skipped silently.
   *
   * Sources collected (in order):
   *   1. Per-issue session log: `.nightgauge/logs/<date>_<issue>_session.log`
   *      — preferred when present (per-issue scope), falls back to dated
   *      whole-day logs.
   *   2. Pipeline context file for the failed stage.
   *   3. Daily execution-history JSONL — filtered to the matching
   *      issue_number (pre-#3204 took the LAST line which often belonged to a
   *      different concurrent slot).
   *   4. Stall and cost-cap diagnostic logs written by skillRunner at kill
   *      time, when present.
   */
  private static async collectEvidence(
    workspaceRoot: string,
    issueNumber: number,
    failedStage: string,
    logger: Logger,
    failureReason?: string
  ): Promise<{ text: string; sourcesAnalyzed: string[]; lines: TaggedLine[] }> {
    const parts: string[] = [];
    const sourcesAnalyzed: string[] = [];
    const lines: TaggedLine[] = [];

    // Source 0: the orchestrator's terminal failure reason (#3926). This is
    // the authoritative verdict for the run and the highest-signal text the
    // classifier can see. It is logged to the output channel but lives in no
    // file the other sources read — so without threading it in here, the
    // collector is starved and the run falls through to `unknown`. Pushed
    // FIRST so it leads the corpus; tagged `extension` (orchestrator origin).
    if (failureReason && failureReason.trim().length > 0) {
      const reason = failureReason.trim();
      parts.push(reason);
      sourcesAnalyzed.push("terminal_reason");
      lines.push({ source: "extension", text: reason });
    }

    // Source 1: per-issue session log first (preferred when available),
    // else the most recent dated log. The per-issue file is much smaller
    // and only contains lines tagged for this run.
    try {
      const logsDir = path.join(workspaceRoot, ".nightgauge", "logs");
      const logFiles = await fs.readdir(logsDir);
      const today = new Date().toISOString().slice(0, 10);
      const perIssue = `${today}_${issueNumber}_session.log`;
      let chosen: string | undefined;
      if (logFiles.includes(perIssue)) {
        chosen = perIssue;
      } else {
        const todayLogs = logFiles.filter((f) => f.includes(today)).sort();
        chosen = todayLogs[todayLogs.length - 1];
      }
      if (chosen) {
        const logContent = await fs.readFile(path.join(logsDir, chosen), "utf-8");
        // Scope the session log to the failed run's time window (#3247). The
        // per-issue session log accumulates lines from EVERY run of this
        // issue. Earlier runs may have produced events (stop-hook-error,
        // stall-killed, etc.) that have nothing to do with the failure being
        // analyzed now. Slice from the LAST stage-start marker for the failed
        // stage forward so the classifier only sees signals that belong to
        // this run.
        const scoped = this.scopeLogToFailedRun(logContent, failedStage);
        parts.push(scoped);
        sourcesAnalyzed.push("session_log");
        for (const line of scoped.split("\n")) {
          if (line.length === 0) continue;
          lines.push({ source: this.classifyLineSource(line), text: line });
        }
      }
    } catch {
      // Session logs not available — skip
    }

    // Source 2: Pipeline context for the failed stage
    try {
      const contextFile = path.join(
        workspaceRoot,
        ".nightgauge",
        "pipeline",
        `${failedStage}-${issueNumber}.json`
      );
      const contextContent = await fs.readFile(contextFile, "utf-8");
      parts.push(contextContent);
      sourcesAnalyzed.push("pipeline_context");
      // Pipeline context is structured JSON — tag as `extension` since it
      // originates from the orchestrator, not the subagent.
      lines.push({ source: "extension", text: contextContent });
    } catch {
      // Pipeline context not available — skip
    }

    // Source 3: JSONL history entry for THIS issue. Pre-#3204 this took the
    // last line of the file, which in concurrent-mode could belong to a
    // different slot's run. Now we filter by issue_number.
    try {
      const historyDir = path.join(workspaceRoot, ".nightgauge", "pipeline", "history");
      const historyFiles = await fs.readdir(historyDir);
      const jsonlFiles = historyFiles.filter((f) => f.endsWith(".jsonl")).sort();
      const lastFile = jsonlFiles[jsonlFiles.length - 1];
      if (lastFile) {
        const histContent = await fs.readFile(path.join(historyDir, lastFile), "utf-8");
        const matched = this.findRunRecordForIssue(histContent, issueNumber);
        if (matched) {
          parts.push(matched);
          sourcesAnalyzed.push("execution_history");
          lines.push({ source: "extension", text: matched });
        }
      }
    } catch {
      // History not available — skip
    }

    // Source 4: Stall/cost-cap diagnostic logs (written by skillRunner at
    // kill time). Surfacing these in evidence gives the user a direct
    // pointer for triage.
    for (const diagName of [`${failedStage}-stalled.log`, `${failedStage}-cost-capped.log`]) {
      try {
        const diagPath = path.join(
          workspaceRoot,
          ".nightgauge",
          "pipeline",
          "history",
          String(issueNumber),
          diagName
        );
        const diagContent = await fs.readFile(diagPath, "utf-8");
        parts.push(diagContent);
        sourcesAnalyzed.push(diagName);
        // Diagnostic logs contain captured subagent stdout/stderr — tag as
        // `subagent` so the validation-failure keyword pass can match
        // genuine `tsc error` text from inside the diagnostic.
        for (const line of diagContent.split("\n")) {
          if (line.length === 0) continue;
          lines.push({ source: "subagent", text: line });
        }
      } catch {
        // Diagnostic not present — fine, kill path may not have written one
      }
    }

    if (sourcesAnalyzed.length === 0) {
      logger.info("Auto-retro: no evidence sources found, using unknown category", { issueNumber });
    }

    return { text: parts.join("\n"), sourcesAnalyzed, lines };
  }

  /**
   * Best-effort source classification for a single log line. The extension's
   * own logger writes structured `[YYYY-MM-DDThh:mm:ss.sssZ] [LEVEL]` prefixes;
   * subagent stdout and tool-results are typically un-prefixed JSON or raw
   * text. This is a heuristic — when wrong, the worst case is that a single
   * line gets routed to the looser whole-text fallback.
   */
  private static classifyLineSource(line: string): EvidenceSource {
    // Extension logger format: `[ISO-timestamp] [LEVEL] ...`
    if (/^\[\d{4}-\d{2}-\d{2}T[^\]]+\]\s+\[(?:DEBUG|INFO|WARN|ERROR)\]/.test(line)) {
      // The extension also wraps subagent JSON in INFO lines like
      // `[INFO] [feature-dev] [#3204] {"type":"assistant",...}` — those
      // lines carry subagent output and should be tagged as such.
      if (/\{"type":"(?:assistant|user|system|result|tool_use|tool_result)"/.test(line)) {
        return "subagent";
      }
      return "extension";
    }
    return "unknown";
  }

  /**
   * Scope a per-issue session log to the failed run's time window (#3247).
   *
   * The per-issue session log file (`<date>_<issue>_session.log`) accumulates
   * lines from EVERY run of the same issue on that day. Earlier runs may
   * have emitted events that have nothing to do with the failure being
   * analyzed (e.g. a stop-hook-error from a prior run vs. a clean stall-kill
   * from the current run). Without scoping, the classifier matches signals
   * across runs and reports the wrong category.
   *
   * Strategy: find the LAST occurrence of the stage-start marker emitted by
   * skillRunner for the failed stage:
   *   `[ISO-ts] [INFO] [<stage>] [#<issue>] [skillRunner] Stage: <stage> | Model:`
   * and return only the slice from that line forward. If no marker is found,
   * returns the full content unchanged (best-effort — better to over-include
   * than to lose evidence entirely).
   */
  private static scopeLogToFailedRun(logContent: string, failedStage: string): string {
    const lines = logContent.split("\n");
    // Match: `[skillRunner] Stage: <failedStage> |` — present at every stage
    // start, regardless of mode/effort/model details that follow.
    const escapedStage = failedStage.replace(/[-/\\^$*+?.()|[\]{}]/g, "\\$&");
    const startPattern = new RegExp(`\\[skillRunner\\] Stage:\\s+${escapedStage}\\s*\\|`);
    let lastStart = -1;
    for (let i = lines.length - 1; i >= 0; i--) {
      if (startPattern.test(lines[i])) {
        lastStart = i;
        break;
      }
    }
    if (lastStart < 0) return logContent;
    return lines.slice(lastStart).join("\n");
  }

  /**
   * Find the run record in a daily JSONL whose `issue_number` matches.
   * Returns the latest matching line (so re-runs in the same day pick up
   * the most recent attempt). Returns null when no record matches.
   */
  private static findRunRecordForIssue(jsonlContent: string, issueNumber: number): string | null {
    const lines = jsonlContent.split("\n");
    const needle = `"issue_number":${issueNumber}`;
    for (let i = lines.length - 1; i >= 0; i--) {
      const line = lines[i];
      if (line.includes(needle)) return line;
    }
    return null;
  }

  /**
   * Classify failure using a two-pass approach (rewritten in #3204).
   *
   *   Pass 1 — Structured signals (authoritative).
   *     The skillRunner / OfflineManager / orchestrator emit deterministic
   *     log shapes. Each maps directly to a category. Multiple may fire on
   *     the same run (e.g. infrastructure-outage AND stall-kill). The FIRST
   *     extractor that fires is the primary finding; later signals become
   *     secondary findings (sorted highest severity first).
   *
   *   Pass 2 — Source-tagged keyword fallback (only if pass 1 is empty).
   *     Patterns are scoped to the source of the line they match
   *     (subagent stdout vs. extension cleanup logs) so cleanup-noise
   *     `TypeError: fetch failed` cannot trip the validation-failure path
   *     and a real subagent `tsc error` still does.
   *
   *   Pass 3 — `unknown` if neither yields a finding.
   *
   * Returned findings are de-duplicated by category and ordered:
   *   primary cause first, then secondary findings highest-severity first.
   */
  static classifyFailure(
    evidence: { text: string; sourcesAnalyzed: string[]; lines?: TaggedLine[] },
    failedStage: string
  ): RetroFinding[] {
    const text = evidence.text;
    const findings: RetroFinding[] = [];
    const seen = new Set<RetroFailureCategory>();

    // Pass 1: structured signals.
    const extractorInput: ExtractorInput = {
      text,
      sourcesAnalyzed: evidence.sourcesAnalyzed,
      failedStage,
    };
    for (const extractor of SIGNAL_EXTRACTORS) {
      const signal = extractor(extractorInput);
      if (!signal || seen.has(signal.category)) continue;
      seen.add(signal.category);
      findings.push(this.buildFinding(signal.category, failedStage, signal.evidence));
    }

    if (findings.length > 0) {
      return this.orderFindings(findings);
    }

    // Pass 2: source-tagged keyword matching. Use tagged lines if the
    // collector provided them, else fall back to whole-text matching for
    // patterns that aren't source-restricted.
    const tagged = evidence.lines;
    for (const { patterns, category, sources } of CLASSIFICATION_PATTERNS) {
      const matched = this.findFirstMatch(text, tagged, patterns, sources);
      if (matched && !seen.has(category)) {
        seen.add(category);
        findings.push(this.buildFinding(category, failedStage, `Pattern matched: ${matched}`));
        // Keyword pass returns a single finding (preserves pre-#3204 behavior
        // where the user-visible retro is one strongest candidate). Multi-
        // finding output requires a structured signal upstream.
        return this.orderFindings(findings);
      }
    }

    return [this.buildFinding("unknown", failedStage, "")];
  }

  /**
   * Source-tagged regex match. When `tagged` is provided, only test patterns
   * against lines whose source is in `allowedSources` (or any source if
   * `allowedSources` is empty/undefined). When `tagged` is absent, fall back
   * to a whole-text test, but only for unrestricted patterns — restricted
   * patterns silently skip rather than firing on potentially-wrong source.
   */
  private static findFirstMatch(
    fullText: string,
    tagged: TaggedLine[] | undefined,
    patterns: RegExp[],
    allowedSources?: EvidenceSource[]
  ): string | null {
    if (tagged && allowedSources && allowedSources.length > 0) {
      // `unknown`-tagged lines are always considered (the source classifier
      // could not determine origin; treat as wildcard rather than fail-close).
      // This preserves correct behavior for free-form text inputs while still
      // blocking extension-cleanup noise (which IS reliably tagged as
      // `extension`) from tripping subagent-only patterns.
      const allow = new Set([...allowedSources, "unknown" as EvidenceSource]);
      for (const line of tagged) {
        if (!allow.has(line.source)) continue;
        for (const p of patterns) {
          if (p.test(line.text)) return p.source;
        }
      }
      return null;
    }
    // No tagging available, OR pattern is unrestricted: fall back to whole-text
    // matching. Production paths in #3204 always pass tagged lines so the strict
    // source-aware behavior is the live one; this lenient path keeps the
    // standalone classifier API ergonomic for tests and one-off callers.
    for (const p of patterns) {
      if (p.test(fullText)) return p.source;
    }
    return null;
  }

  /**
   * Order findings: primary cause first (preserves the order signals fired,
   * which corresponds to the SIGNAL_EXTRACTORS list — V3 record kind first,
   * then stall-kill, cost-cap, infra outage, stop-hook). Within secondary
   * findings, prefer higher severity.
   */
  private static orderFindings(findings: RetroFinding[]): RetroFinding[] {
    if (findings.length <= 1) return findings;
    const [primary, ...rest] = findings;
    rest.sort((a, b) => (SEVERITY_RANK[b.severity] ?? 0) - (SEVERITY_RANK[a.severity] ?? 0));
    return [primary, ...rest];
  }

  /**
   * Build a RetroFinding for a given category.
   */
  private static buildFinding(
    category: RetroFailureCategory,
    failedStage: string,
    matchedPattern: string
  ): RetroFinding {
    const evidence: string[] = [];
    if (matchedPattern) {
      evidence.push(`Pattern matched: ${matchedPattern}`);
    }
    evidence.push(`Failed stage: ${failedStage}`);

    const summaries: Record<RetroFailureCategory, string> = {
      "budget-exceeded": "Pipeline terminated due to token or cost budget exhaustion",
      "shipped-but-overbudget":
        "Stage was budget-killed but the PR actually merged — work shipped successfully",
      "false-negative-shipped":
        "Stage was reported as failed but the PR actually merged out-of-band — work shipped successfully",
      "state-management": "Pipeline aborted due to missing or corrupt context file",
      "ci-infrastructure": "Pipeline failed due to external CI system failure",
      "model-capability": "Pipeline failed: model output did not meet task requirements",
      timeout: "Pipeline terminated after exceeding configured time limit",
      "validation-failure": "Pipeline blocked by failing tests, type errors, or build failure",
      "stall-kill":
        "skillRunner forcibly terminated the stage: subagent went silent past the idle or hard-cap threshold",
      "cost-cap":
        "skillRunner forcibly terminated the stage: per-stage cost cap exceeded before BudgetEnforcer grace-out",
      "infrastructure-outage":
        "Network outage observed during the run — DNS or transport failure on api.github.com / api.anthropic.com",
      "stop-hook-error":
        "Claude CLI emitted a stop-hook-error notification before the subagent went silent",
      "skill-no-op":
        "A stage reported success but its post-condition gate found the work never landed (pr-merge: PR not merged; pr-create: no open PR)",
      "merge-blocked":
        "pr-merge declined to merge — the PR is held open by a deterministically-known blocker (failing check, required review, or merge conflict)",
      "adapter-unavailable":
        "Stage halted at start — primary adapter prereq failed and fallback was disabled or the chain was empty",
      "no-adapter-available":
        "Every adapter in the fallback chain failed prereq at stage start — no adapter is available to run the stage",
      unknown: "Pipeline failure detected but category could not be determined",
    };

    const recommendations: Record<RetroFailureCategory, string> = {
      "budget-exceeded":
        "Review token budget configuration. Consider increasing budget or reducing scope.",
      "shipped-but-overbudget":
        "Treat as success — the PR merged before the budget kill landed. Tighten pr-merge auto-fix attempts and verify the post-merge fast-path is wired.",
      "false-negative-shipped":
        "Treat as success — the PR merged out-of-band. The pipeline reported failure but the work shipped. Investigate why the failed-stage post-condition didn't observe the merged state and tighten the verification path.",
      "state-management":
        "Check pipeline context file integrity. Re-run the failed stage after verifying context.",
      "ci-infrastructure": "Investigate CI system availability. Retry when CI is stable.",
      "model-capability":
        "Review stage prompt and model routing. Consider escalating to a more capable model.",
      timeout: "Increase stage timeout configuration or reduce workload for the failing stage.",
      "validation-failure":
        "Review failing tests or type errors. Fix implementation before re-running.",
      "stall-kill":
        "Open the stall diagnostic at .nightgauge/pipeline/history/<issue>/<stage>-stalled.log to see the last stdout/stderr captured before the kill. Common causes: a Bash command hung in the subagent, an infinite tool loop, or a stop-hook deadlock. Increase pipeline.stage_hard_caps if the stage is legitimately long; otherwise resume after addressing the hang.",
      "cost-cap":
        "The per-stage cost cap fired before BudgetEnforcer's estimate-vs-actual grace landed, which usually indicates a runaway tool loop. Inspect token usage by tool to find the loop, then either increase the cap (pipeline.stage_cost_caps) or fix the loop. Re-running without addressing the cause will hit the cap again.",
      "infrastructure-outage":
        "The run hit a network outage during execution. With pause-on-offline (#3203) the pipeline should automatically resume when connectivity returns. If it didn't, the outage outlasted patience or the bus didn't observe it — re-run when stable, and if this repeats consider raising pipeline.stage_hard_caps or filing against the connectivity bus.",
      "stop-hook-error":
        "The Claude CLI's stop-hook fired with an error and the subagent went silent. The most common source (#3234) is the nightgauge plugin's own Stop hook (`claude-plugins/nightgauge/hooks/stop-verification.sh`) failing because the `nightgauge` Go binary is not resolvable — typically when a stage runs in a worktree (`pipeline.worktree.enabled: true`) where `bin/nightgauge` was never built. Verify the binary is on PATH (`command -v nightgauge`) or in the canonical repo's `bin/`. After PR #3234 ships, the hook skips gracefully when the binary is unresolvable; if you still see this category, check `~/.claude/settings.json` for a user-defined Stop hook and inspect the failing hook's stderr.",
      "skill-no-op":
        "The stage's LLM path reported success but the deterministic post-condition gate found the work never landed (pr-merge: the PR is not merged; pr-create: no open PR exists). Inspect `.nightgauge/pipeline/<stage>-<N>.json` for the verification result, then verify the gate/fallback path in PR_MERGE_STAGE.md / PR_CREATE_STAGE.md. Re-running without addressing the gate will repeat the no-op.",
      "merge-blocked":
        "The PR cannot merge as-is and the pipeline correctly declined — this is not a pipeline bug. Resolve the named blocker on the PR: fix/re-run the failing check, satisfy the required review, or rebase a behind/conflicting branch. The issue is parked in 'In review'; re-queue once the PR is mergeable, or merge it manually if the failing check is non-blocking.",
      "adapter-unavailable":
        "The primary adapter resolved for this stage failed its prereq probe (auth, missing CLI, missing env var, etc.) and no fallback ran. Either fix the primary adapter's auth/install or — if you want automatic recovery — set `pipeline.disable_fallback: false` (the default) and configure `pipeline.adapter_fallback_chain` or `pipeline.stage_adapter_fallback.<stage>`.",
      "no-adapter-available":
        "Every adapter in the effective fallback chain failed prereq at stage start. Inspect the `adapters_tried=[…]` list in the envelope: at least one of those adapters needs a valid auth/install before the stage can run. Either broaden the chain (`pipeline.adapter_fallback_chain`) or fix one of the listed adapters' prereqs. See `nightgauge doctor --json` for per-adapter status.",
      unknown: "Review logs manually. Run /nightgauge:retro for AI-powered root cause analysis.",
    };

    const severities: Record<RetroFailureCategory, "low" | "medium" | "high"> = {
      "budget-exceeded": "high",
      "shipped-but-overbudget": "low",
      "false-negative-shipped": "low",
      "state-management": "high",
      "ci-infrastructure": "medium",
      "model-capability": "high",
      timeout: "medium",
      "validation-failure": "high",
      "stall-kill": "medium",
      "cost-cap": "high",
      "infrastructure-outage": "low",
      "stop-hook-error": "medium",
      "skill-no-op": "high",
      "merge-blocked": "medium",
      "adapter-unavailable": "high",
      "no-adapter-available": "high",
      unknown: "low",
    };

    return {
      category,
      severity: severities[category],
      summary: summaries[category],
      evidence,
      recommendation: recommendations[category],
    };
  }

  /**
   * Create GitHub issues for findings that meet the severity threshold.
   * Links created issues to the pipeline issue's parent epic (if any)
   * using GitHub's native sub-issue API.
   * Errors are caught and logged — never throws.
   */
  private static async createIssuesForFindings(
    findings: RetroFinding[],
    severityThreshold: "low" | "medium" | "high",
    pipelineIssueNumber: number,
    failedStage: string,
    workspaceRoot: string,
    logger: Logger
  ): Promise<number> {
    const thresholdRank = SEVERITY_RANK[severityThreshold] ?? 2;
    let issuesCreated = 0;

    // Look up parent epic for native sub-issue linking
    const epicInfo = await this.getParentEpicInfo(pipelineIssueNumber, workspaceRoot, logger);

    for (const finding of findings) {
      const findingRank = SEVERITY_RANK[finding.severity] ?? 0;
      if (findingRank < thresholdRank) {
        continue;
      }

      try {
        const title = `[Auto-Retro #${pipelineIssueNumber}] ${finding.summary}`;
        const body = [
          `## Auto-Retro Finding`,
          ``,
          `**Pipeline Issue**: #${pipelineIssueNumber}`,
          `**Failed Stage**: ${failedStage}`,
          `**Category**: \`${finding.category}\``,
          `**Severity**: ${finding.severity}`,
          ``,
          `## Summary`,
          finding.summary,
          ``,
          `## Evidence`,
          ...finding.evidence.map((e) => `- ${e}`),
          ``,
          `## Recommendation`,
          finding.recommendation,
          ``,
          `---`,
          `*Generated automatically by AutoRetroService (Issue #1408)*`,
        ].join("\n");

        const labels = ["type:bug", `priority:${finding.severity === "high" ? "high" : "medium"}`];
        const identity = await getRepoIdentity(workspaceRoot);
        if (!identity) continue;
        const ipc = IpcClient.getInstance();
        const created = await ipc.issueCreate(identity.owner, identity.repo, title, body, labels);

        const newIssueNumber = created?.number;
        if (newIssueNumber) {
          finding.issueNumber = newIssueNumber;

          // Link to parent epic via native sub-issue API
          if (epicInfo) {
            await this.linkSubIssueToEpic(
              epicInfo.parentNumber,
              newIssueNumber,
              epicInfo.owner,
              epicInfo.name,
              logger
            );
          }
        }

        issuesCreated++;
        logger.info("Auto-retro: created GitHub issue", {
          pipelineIssueNumber,
          category: finding.category,
          newIssueNumber,
        });
      } catch (err) {
        logger.warn("Auto-retro: failed to create GitHub issue", {
          pipelineIssueNumber,
          category: finding.category,
          err: err instanceof Error ? err.message : String(err),
        });
      }
    }

    return issuesCreated;
  }

  /**
   * State-aware override: if the failed stage is pr-merge, the dominant
   * finding is `budget-exceeded`, AND the PR for the issue is actually in
   * MERGED state, reclassify the finding as `shipped-but-overbudget`.
   *
   * Why this matters: pr-merge can burn $20+ in trailing LLM iterations after
   * the PR has already merged. Pattern-matching on the session log alone
   * misclassifies that as "budget exceeded — high severity" and clears the
   * pipeline queue, when the work actually shipped. See #3108.
   */
  static async applyShippedButOverbudgetOverride(
    findings: RetroFinding[],
    workspaceRoot: string,
    pipelineIssueNumber: number,
    logger: Logger
  ): Promise<void> {
    const budgetFinding = findings.find((f) => f.category === "budget-exceeded");
    if (!budgetFinding) return;

    const merged = await this.isPrMerged(workspaceRoot, pipelineIssueNumber, logger);
    if (!merged) return;

    logger.info("Auto-retro: reclassifying budget-exceeded as shipped-but-overbudget", {
      pipelineIssueNumber,
      prNumber: merged.prNumber,
    });

    budgetFinding.category = "shipped-but-overbudget";
    budgetFinding.severity = "low";
    budgetFinding.summary =
      "Stage was budget-killed but the PR actually merged — work shipped successfully";
    budgetFinding.evidence.push(`PR #${merged.prNumber} state: MERGED`);
    budgetFinding.recommendation =
      "Treat as success — the PR merged before the budget kill landed. Tighten pr-merge auto-fix attempts and verify the post-merge fast-path is wired.";
  }

  /**
   * State-aware override (#3275): generalize the shipped-but-PR-merged
   * reclassification beyond `budget-exceeded`. When the failed stage is
   * pr-merge, the PR for the issue is actually MERGED, AND the dominant
   * finding is anything other than the existing `shipped-but-overbudget`
   * (already handled by the budget override), reclassify the PRIMARY
   * finding as `false-negative-shipped` (low severity).
   *
   * Why this matters: pr-merge can be killed by stop-hook noise, cost-cap,
   * or any number of structural signals AFTER the gh fallback already
   * merged the PR. Without this override the run is reported as a
   * high-severity failure when the work actually shipped — the pipeline
   * queue is cleared and the team gets a misleading retro.
   *
   * Runs AFTER `applyShippedButOverbudgetOverride` so the budget path's
   * canonical category remains intact. Skips when the primary finding is
   * already a "shipped" category to avoid double-classification.
   */
  static async applyFalseNegativeShippedOverride(
    findings: RetroFinding[],
    workspaceRoot: string,
    pipelineIssueNumber: number,
    logger: Logger
  ): Promise<void> {
    if (findings.length === 0) return;
    const primary = findings[0];
    // Skip when the budget override already won — those are already classified
    // as a "shipped" success and should not be re-rewritten.
    if (
      primary.category === "shipped-but-overbudget" ||
      primary.category === "false-negative-shipped"
    ) {
      return;
    }

    const merged = await this.isPrMerged(workspaceRoot, pipelineIssueNumber, logger);
    if (!merged) return;

    logger.info("Auto-retro: reclassifying as false-negative-shipped (PR merged out-of-band)", {
      pipelineIssueNumber,
      prNumber: merged.prNumber,
      originalCategory: primary.category,
    });

    primary.evidence.push(`Original category: ${primary.category}`);
    primary.evidence.push(`PR #${merged.prNumber} state: MERGED`);
    primary.category = "false-negative-shipped";
    primary.severity = "low";
    primary.summary =
      "Stage was reported as failed but the PR actually merged out-of-band — work shipped successfully";
    primary.recommendation =
      "Treat as success — the PR merged out-of-band. The pipeline reported failure but the work shipped. Investigate why the failed-stage post-condition didn't observe the merged state and tighten the verification path.";
  }

  /**
   * Check whether the PR associated with the given issue is in MERGED state.
   * Returns the PR number on a confirmed merge, otherwise null. Defensive:
   * any failure to read state or call the IPC returns null (treat as "we
   * don't know — leave the original classification alone").
   */
  private static async isPrMerged(
    workspaceRoot: string,
    issueNumber: number,
    logger: Logger
  ): Promise<{ prNumber: number } | null> {
    try {
      const prContextPath = path.join(
        workspaceRoot,
        ".nightgauge",
        "pipeline",
        `pr-${issueNumber}.json`
      );
      let prNumber: number | undefined;
      try {
        const raw = await fs.readFile(prContextPath, "utf-8");
        const parsed = JSON.parse(raw) as { pr_number?: unknown };
        if (typeof parsed.pr_number === "number") {
          prNumber = parsed.pr_number;
        }
      } catch {
        return null;
      }
      if (!prNumber) return null;

      const identity = await getRepoIdentity(workspaceRoot);
      if (!identity) return null;

      const ipc = IpcClient.getInstance();
      const pr = (await ipc.prView(identity.owner, identity.repo, prNumber)) as {
        state?: unknown;
      } | null;
      const state = pr && typeof pr.state === "string" ? pr.state.toUpperCase() : null;
      if (state === "MERGED") {
        return { prNumber };
      }
      return null;
    } catch (err) {
      logger.debug("Auto-retro: PR merge check failed (treating as not merged)", {
        issueNumber,
        err: err instanceof Error ? err.message : String(err),
      });
      return null;
    }
  }

  /**
   * Look up the parent epic's node ID for a given issue.
   * Returns repo owner/name alongside the parent node ID so callers can
   * make follow-up GraphQL calls without re-detecting the repo.
   * Returns null if the issue has no parent or repo detection fails.
   */
  private static async getParentEpicInfo(
    issueNumber: number,
    workspaceRoot: string,
    logger: Logger
  ): Promise<{ parentNumber: number; owner: string; name: string } | null> {
    try {
      const identity = await getRepoIdentity(workspaceRoot);
      if (!identity) return null;

      const ipc = IpcClient.getInstance();
      const issue = await ipc.issueView(identity.owner, identity.repo, issueNumber);
      const parentNumber = issue?.parentIssueNumber;
      if (!parentNumber) return null;

      return {
        parentNumber,
        owner: identity.owner,
        name: identity.repo,
      };
    } catch {
      logger.debug("Auto-retro: could not look up parent epic", {
        issueNumber,
      });
      return null;
    }
  }

  /**
   * Link a newly created issue to a parent epic using the addSubIssue
   * GraphQL mutation. Fire-and-forget: errors are logged but never thrown.
   */
  private static async linkSubIssueToEpic(
    parentEpicNumber: number,
    childIssueNumber: number,
    owner: string,
    name: string,
    logger: Logger
  ): Promise<void> {
    try {
      const ipc = IpcClient.getInstance();
      await ipc.issueLinkSubIssue(owner, name, parentEpicNumber, childIssueNumber);
      logger.info("Auto-retro: linked issue to parent epic", {
        childIssueNumber,
      });
    } catch (err) {
      // Fire-and-forget: linking failure should not prevent issue creation
      logger.debug("Auto-retro: failed to link sub-issue to epic", {
        childIssueNumber,
        err: err instanceof Error ? err.message : String(err),
      });
    }
  }

  /**
   * Parse an issue number from a GitHub issue URL.
   * e.g. "https://github.com/nightgauge/nightgauge/issues/1234" → 1234
   */
  static parseIssueNumberFromUrl(url: string): number | undefined {
    const match = url.match(/\/issues\/(\d+)/);
    return match ? Number(match[1]) : undefined;
  }
}
