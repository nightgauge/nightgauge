/**
 * ContextAssembler — Build and manage pipeline stage context files
 *
 * Centralizes all context file I/O that was previously embedded in
 * HeadlessOrchestrator: path resolution, waiting for files to appear,
 * Zod schema validation, schema repair, fallback context generation,
 * and reading context files for downstream use.
 *
 * @see Issue #2770 — HeadlessOrchestrator decomposition (Part 3)
 * @see Issue #637  — Context file handoff validation
 * @see Issue #2552 — Pipeline context schema self-correction
 */

import * as fs from "fs";
import * as path from "path";
import { execFile } from "child_process";
import { promisify } from "util";

// #2884: Subprocess calls in this module run in extension-host code paths
// that block the VSCode UI thread. ContextAssembler especially shells out
// to `npm run build` (~30s) and `npx vitest run` (~60s) — sync versions
// freeze the editor for the entire duration. Use the async helper.
const execFileAsync = promisify(execFile);
import type { PipelineStage } from "@nightgauge/sdk";
import {
  IssueContextSchema,
  PlanningContextSchema,
  DevContextSchema,
  ValidateContextSchema,
  PRContextSchema,
  applyDeliverablePolicy,
  deliverableKindForStage,
  stampPolicyMarker,
  summarizePolicy,
  type PolicyOutcome,
} from "@nightgauge/sdk";
import { type ZodSchema, type ZodError } from "zod";
import type { Logger } from "../../utils/logger";
import type {
  RepositoryContextLoader,
  ContextFileType,
} from "../../services/RepositoryContextLoader";
import type { SkillLoader } from "../skills/SkillLoader";
import { runStageSkillHeadless, type SkillRunResult } from "../../utils/skillRunner";
import { formatZodErrorsForPrompt } from "../../utils/zodErrorFormatter";
import {
  makeRoutingDecision,
  buildPickupRecommendation,
  DEFAULT_ROUTING_CONFIG,
  type RoutingConfig,
} from "../../utils/routingDecision";
import { analyzeChange } from "../../utils/changeAnalyzer";
import type { IssueMetadata } from "@nightgauge/sdk";
import { BinaryResolver } from "../../services/BinaryResolver";
import { STAGE_OUTPUT_CONTEXT_TYPE } from "./stageContextFiles";

/** One open blockedBy dependency discovered by the deterministic check (#189). */
export interface OpenDependencyRef {
  number: number;
  title?: string;
  state: string;
  repo?: string;
}

/**
 * Result of deterministic context generation. For issue-pickup —
 * the PRIMARY deterministic-first path (#2614) — `blockedBy` is populated
 * (with generated=false) when the issue has OPEN blockedBy dependencies:
 * the caller must defer pickup rather than fall through to the LLM (#189).
 */
export interface DeterministicContextResult {
  generated: boolean;
  blockedBy?: OpenDependencyRef[];
}

// ---------------------------------------------------------------------------
// Stage → context-file-type mappings (moved from HeadlessOrchestrator)
// ---------------------------------------------------------------------------

/**
 * Re-exported from ./stageContextFiles, which is now the single home of the
 * stage -> deliverable-file mapping (#1143). Kept exported here so existing
 * importers do not have to move.
 */
export { STAGE_OUTPUT_CONTEXT_TYPE };

/**
 * Maps skill stages to their corresponding Zod validation schema.
 * @see Issue #1180 — Deterministic skill output validation
 */
export const STAGE_OUTPUT_SCHEMA: Partial<Record<PipelineStage, ZodSchema>> = {
  "issue-pickup": IssueContextSchema,
  "feature-planning": PlanningContextSchema,
  "feature-dev": DevContextSchema,
  "feature-validate": ValidateContextSchema,
  "pr-create": PRContextSchema,
};

/**
 * Maps each skill stage to its prerequisite stage's output context file.
 * @see Issue #1181 — Stage pre-condition validation
 */
export const STAGE_INPUT_PREREQUISITES: Partial<
  Record<PipelineStage, { stage: PipelineStage; contextType: ContextFileType }>
> = {
  "feature-planning": { stage: "issue-pickup", contextType: "issue" },
  "feature-dev": { stage: "feature-planning", contextType: "planning" },
  "feature-validate": { stage: "feature-dev", contextType: "dev" },
  "pr-create": { stage: "feature-validate", contextType: "validate" },
  "pr-merge": { stage: "pr-create", contextType: "pr" },
};

/**
 * Stages whose output context file is optional (warning, not failure).
 * @see Issue #637, Issue #1608
 */
export const OPTIONAL_CONTEXT_STAGES: ReadonlySet<PipelineStage> = new Set([]);

/**
 * Final consistency grace window applied after the main post-stage wait.
 *
 * Some stage runners exit a moment before their context file becomes visible
 * on disk. Keep the window short so genuinely missing handoff files still fail
 * promptly, while late materialization avoids a false-negative pipeline stop.
 */
const MAX_FINAL_CONSISTENCY_WAIT_MS = 1000;

// ---------------------------------------------------------------------------
// Test-runner detection (#3114)
// ---------------------------------------------------------------------------

export type TestRunner = "vitest" | "jest" | "angular" | "playwright" | "none" | "unknown";

/**
 * Detect the repo's primary unit-test runner from worktree files.
 *
 * Used by the validate-stage fallback so it doesn't fabricate test failures
 * when the repo doesn't use vitest. Order matters: a repo with both vitest
 * and angular configs (rare) is treated as vitest because vitest is what
 * the fallback can actually execute. Playwright/none never get auto-run.
 */
export function detectTestRunner(workspaceRoot: string): TestRunner {
  let pkg: { scripts?: Record<string, string>; devDependencies?: Record<string, string> };
  try {
    const raw = fs.readFileSync(path.join(workspaceRoot, "package.json"), "utf-8");
    pkg = JSON.parse(raw);
  } catch {
    // No package.json — give up on auto-running tests.
    return "unknown";
  }

  const testScript = pkg.scripts?.test ?? "";
  const devDeps = pkg.devDependencies ?? {};

  const has = (file: string) => fs.existsSync(path.join(workspaceRoot, file));
  const hasVitestConfig =
    has("vitest.config.ts") ||
    has("vitest.config.js") ||
    has("vitest.config.mjs") ||
    has("vite.config.ts") ||
    has("vite.config.js");

  if (testScript.includes("vitest") || "vitest" in devDeps || hasVitestConfig) {
    return "vitest";
  }
  if (
    testScript.includes("jest") ||
    "jest" in devDeps ||
    has("jest.config.js") ||
    has("jest.config.ts")
  ) {
    return "jest";
  }
  if (testScript.includes("ng test") || has("angular.json")) {
    return "angular";
  }
  if (testScript.includes("playwright") || "@playwright/test" in devDeps) {
    return "playwright";
  }
  if (!testScript || testScript.includes("no test specified")) {
    return "none";
  }
  return "unknown";
}

// ---------------------------------------------------------------------------
// Return types
// ---------------------------------------------------------------------------

/** Result returned by validateStageContextOutput() */
export interface ValidationResult {
  /** null means validation passed (or stage has no output schema) */
  error: Error | null;
}

// ---------------------------------------------------------------------------
// ContextAssembler
// ---------------------------------------------------------------------------

/**
 * ContextAssembler — manages pipeline context file I/O, validation, and fallbacks.
 *
 * HeadlessOrchestrator creates one instance per orchestrator instance and
 * keeps it up to date via setter methods when per-run state (workspace root,
 * repo override, routing config) changes.
 */
export class ContextAssembler {
  // Per-run mutable state (updated by HeadlessOrchestrator setters before each run)
  private contextFileWaitMs = 5000;
  private repoOverride: string | undefined;
  private routingConfig: RoutingConfig = DEFAULT_ROUTING_CONFIG;
  private forceFullPipeline = false;
  private cachedIssueMetadata: IssueMetadata | null = null;
  private stageRepairAttempts = new Map<
    string,
    { attempted: boolean; succeeded: boolean; attempts_count: number }
  >();

  constructor(
    private logger: Logger,
    /**
     * Function that returns the current working directory for skill execution.
     * Called at usage time (not construction) so worktree overrides and pinned
     * workspace roots are respected for concurrent pipeline execution.
     */
    private workspaceRootProvider: () => string,
    private contextLoader?: RepositoryContextLoader | null,
    private skillLoader?: SkillLoader | null,
    /**
     * Returns the run identity the owning orchestrator currently holds, or
     * undefined when it holds none (ADR-017 step 3, #370).
     *
     * Read at usage time, not construction: the assembler outlives any one
     * run, and a schema-repair re-invocation must export the identity of the
     * run that is executing NOW — not the one that was installed when the
     * orchestrator was built. Undefined is a real answer (a manual/no-run
     * invocation), and the exporter turns it into a DELETED
     * NIGHTGAUGE_RUN_ID rather than an inherited one.
     */
    private runIdProvider?: () => string | undefined
  ) {}

  // ---------------------------------------------------------------------------
  // Setters for per-run mutable state
  // ---------------------------------------------------------------------------

  setContextLoader(cl: RepositoryContextLoader | null): void {
    this.contextLoader = cl;
  }

  setSkillLoader(sl: SkillLoader | null): void {
    this.skillLoader = sl;
  }

  setContextFileWaitMs(ms: number): void {
    this.contextFileWaitMs = ms;
  }

  setRepoOverride(repo: string | undefined): void {
    this.repoOverride = repo;
  }

  setRoutingConfig(config: RoutingConfig): void {
    this.routingConfig = config;
  }

  setForceFullPipeline(force: boolean): void {
    this.forceFullPipeline = force;
  }

  setCachedIssueMetadata(metadata: IssueMetadata | null): void {
    this.cachedIssueMetadata = metadata;
  }

  /** Reset per-run state at the start of each pipeline run. */
  clearSessionState(): void {
    this.stageRepairAttempts.clear();
    this.cachedIssueMetadata = null;
  }

  // ---------------------------------------------------------------------------
  // Path resolution
  // ---------------------------------------------------------------------------

  /**
   * Get the filesystem path for a context file.
   *
   * When a contextLoader is set, delegates to it for multi-repo workspace
   * routing. Otherwise builds the path from the current workspace root.
   *
   * @see Issue #327 — Repository-scoped context loading
   * @see Issue #1629 — Worktree path isolation
   */
  /**
   * Overwrite `planning.ac_reconcile` from the deterministic report on disk.
   *
   * `nightgauge preflight ac-reconcile` writes a complete seven-field report to
   * `.nightgauge/pipeline/ac-reconcile-{N}.json`. Before #1011 the only
   * instruction to get it into `planning-{N}.json` was prose in the planning
   * SKILL.md, and the shell phase that ran the reconciler exposed just three
   * scalars — so the model re-typed the block by hand and dropped
   * `acceptance_criteria`, `main_sha` and `evaluated_at`.
   *
   * Those are exactly the three fields `PlanningContextSchema` requires. The
   * assembler logged the mismatch and continued (correct policy, applied to a
   * file the next stage depends on), so `feature-dev` and `feature-validate`
   * received a handoff with no machine-readable acceptance criteria at all.
   *
   * The report is the source of truth and whatever the model wrote is discarded
   * unconditionally — single writer, no merge, no "keep the richer of the two".
   * The two neighbouring fields in the same phase (`recalled_decisions`,
   * `dependency_analysis`) already work this way; this one was the exception.
   *
   * A missing or unreadable report yields `null`, never a partial object: a
   * partial block is the defect being fixed, so producing one here would
   * reintroduce it from the other side.
   */
  private spliceACReconcile(issueNumber: number, parsed: unknown, contextPath: string): void {
    if (!parsed || typeof parsed !== "object") {
      return;
    }
    const target = parsed as Record<string, unknown>;
    let report: unknown = null;
    try {
      const reportPath = this.getContextPath("ac-reconcile", issueNumber);
      if (fs.existsSync(reportPath)) {
        report = JSON.parse(fs.readFileSync(reportPath, "utf-8"));
      }
    } catch (err) {
      this.logger.warn("ac_reconcile report unreadable — writing null", {
        issueNumber,
        error: err instanceof Error ? err.message : String(err),
      });
      report = null;
    }

    if (!report || typeof report !== "object") {
      target.ac_reconcile = null;
      this.persistSplicedContext(contextPath, target, issueNumber);
      return;
    }

    target.ac_reconcile = report;
    const criteria = (report as Record<string, unknown>).acceptance_criteria;
    this.logger.info("Spliced ac_reconcile from the deterministic report", {
      issueNumber,
      acCount: Array.isArray(criteria) ? criteria.length : 0,
      aggregateStatus: (report as Record<string, unknown>).aggregate_status,
    });
    this.persistSplicedContext(contextPath, target, issueNumber);
  }

  /**
   * Write the spliced object back so the bytes on disk and the object validated
   * in memory agree — `feature-dev` reads the FILE, not this object.
   */
  private persistSplicedContext(
    contextPath: string,
    target: Record<string, unknown>,
    issueNumber: number
  ): void {
    try {
      fs.writeFileSync(contextPath, JSON.stringify(target, null, 2));
    } catch (err) {
      this.logger.warn("Could not persist the spliced planning context", {
        issueNumber,
        contextPath,
        error: err instanceof Error ? err.message : String(err),
      });
    }
  }

  getContextPath(type: ContextFileType, issueNumber: number): string {
    if (this.contextLoader) {
      return this.contextLoader.getContextFile(type, issueNumber);
    }
    const workspaceRoot = this.workspaceRootProvider();
    return path.join(workspaceRoot, ".nightgauge", "pipeline", `${type}-${issueNumber}.json`);
  }

  // ---------------------------------------------------------------------------
  // File waiting
  // ---------------------------------------------------------------------------

  /**
   * Wait briefly for a context file to appear after a stage reports success.
   *
   * Absorbs short file-system eventual-write races. Returns immediately when
   * the file is present; polls at a rate proportional to maxWaitMs.
   *
   * @see Issue #637 — Generalized from waitForIssueContextFile()
   */
  async waitForContextFile(
    type: ContextFileType,
    issueNumber: number,
    maxWaitMs: number
  ): Promise<string | null> {
    const contextPath = this.getContextPath(type, issueNumber);
    if (fs.existsSync(contextPath)) {
      return contextPath;
    }

    if (maxWaitMs <= 0) {
      return null;
    }

    const startedAt = Date.now();
    const pollIntervalMs = Math.min(200, Math.max(50, Math.floor(maxWaitMs / 20)));

    while (Date.now() - startedAt < maxWaitMs) {
      await new Promise((resolve) => setTimeout(resolve, pollIntervalMs));
      if (fs.existsSync(contextPath)) {
        this.logger.warn("Context file appeared after post-stage delay", {
          type,
          issueNumber,
          contextPath,
          waitMs: Date.now() - startedAt,
        });
        return contextPath;
      }
    }

    return null;
  }

  // ---------------------------------------------------------------------------
  // Validation
  // ---------------------------------------------------------------------------

  /**
   * Validate that a stage wrote its expected output context file.
   *
   * Returns a ValidationResult with error=null when validation passes or is
   * not applicable (bookends, pr-merge). On schema failure, optionally attempts
   * repair via SkillLoader and runStageSkillHeadless.
   *
   * @see Issue #637 — Context file handoff validation
   * @see Issue #1180 — Zod schema validation
   * @see Issue #2552 — Context schema self-correction
   */
  async validateStageContextOutput(
    stage: PipelineStage,
    issueNumber: number,
    repairConfig?: { enabled: boolean; max_attempts: number }
  ): Promise<ValidationResult> {
    const expectedType = STAGE_OUTPUT_CONTEXT_TYPE[stage];
    if (!expectedType) {
      return { error: null };
    }

    let contextPath =
      (await this.waitForContextFile(expectedType, issueNumber, this.contextFileWaitMs)) ??
      this.getContextPath(expectedType, issueNumber);

    let contextExists = fs.existsSync(contextPath);
    if (!contextExists) {
      contextExists = this.recoverMisnamedContextFile(expectedType, issueNumber, contextPath);
    }

    if (!contextExists) {
      const lateContextPath = await this.waitForLateContextFile(expectedType, issueNumber);
      if (lateContextPath) {
        contextPath = lateContextPath;
        contextExists = true;
      }
    }

    if (!contextExists) {
      contextExists = this.recoverMisnamedContextFile(expectedType, issueNumber, contextPath);
    }

    if (contextExists) {
      const schema = STAGE_OUTPUT_SCHEMA[stage];
      if (schema) {
        let parsed: unknown;
        try {
          const raw = fs.readFileSync(contextPath, "utf-8");
          parsed = JSON.parse(raw);
        } catch (err) {
          const parseMsg =
            `${stage} wrote context file but it contains invalid JSON: ${contextPath}. ` +
            `Error: ${err instanceof Error ? err.message : String(err)}`;
          if (OPTIONAL_CONTEXT_STAGES.has(stage)) {
            this.logger.warn(parseMsg, { stage, issueNumber, contextPath });
            return { error: null };
          }
          this.logger.error(parseMsg, { stage, issueNumber, contextPath });
          return { error: new Error(parseMsg) };
        }

        // #1182: ONE policy governs this validator and the Go post-condition
        // gate. Before this, the same defect was a warning here and a
        // run-ender there — `files_changed` as an array warned in the
        // prerequisite read and destroyed a $6.12 implementation at the gate,
        // and nothing about the WORK differed between the two. The policy
        // decides by defect: repair what is totally repairable, quarantine an
        // advisory field whose value was never written, fail what a consumer
        // genuinely cannot read. Warn-and-continue is gone as a category.
        const kind = deliverableKindForStage(stage);
        if (kind) {
          const policy: PolicyOutcome = applyDeliverablePolicy(kind, parsed);
          if (!policy.ok) {
            const detail = summarizePolicy(policy)
              .map((line) => `  - ${line}`)
              .join("\n");
            const msg =
              `${stage} deliverable does not match the contract and no total repair exists: ` +
              `${contextPath}\n${detail}`;
            this.logger.error(msg, { stage, issueNumber, contextPath });
            return { error: new Error(msg) };
          }
          if (policy.changed) {
            stampPolicyMarker(policy, new Date());
            parsed = policy.doc;
            // Record the repair where a human and the learning corpus can see
            // it. A repair that is not written back is a repair the next stage
            // does not get, and a bad emitter nobody ever notices (#1176).
            try {
              fs.writeFileSync(contextPath, `${JSON.stringify(policy.doc, null, 2)}\n`, "utf-8");
            } catch (writeErr) {
              this.logger.warn("Deliverable policy: could not write the repair back", {
                stage,
                issueNumber,
                contextPath,
                error: writeErr instanceof Error ? writeErr.message : String(writeErr),
              });
            }
            this.logger.warn(
              "Deliverable policy applied — the stage emitted a non-conforming shape",
              {
                stage,
                issueNumber,
                contextPath,
                verdict: policy.verdict,
                untrustworthy: policy.untrustworthy,
                notes: `\n${summarizePolicy(policy)
                  .map((line) => `  - ${line}`)
                  .join("\n")}`,
              }
            );
          }
        }

        // #1011: the orchestrator, not the model, writes planning.ac_reconcile.
        // Must run BEFORE safeParse — the three fields the model drops are the
        // three the schema requires, and repairing after validation would only
        // silence the warning without fixing what the next stage reads.
        if (stage === "feature-planning") {
          this.spliceACReconcile(issueNumber, parsed, contextPath);
        }

        const result = schema.safeParse(parsed);
        if (!result.success) {
          const issues = result.error.issues
            .map((i) => `  - ${i.path.join(".")}: ${i.message}`)
            .join("\n");

          // Attempt schema repair if configured
          const existingRepair = this.stageRepairAttempts.get(stage);
          const currentAttempts = existingRepair?.attempts_count ?? 0;

          if (repairConfig?.enabled && currentAttempts < repairConfig.max_attempts) {
            this.logger.info("Context schema repair: attempting repair", {
              stage,
              issueNumber,
              attempt: currentAttempts + 1,
              maxAttempts: repairConfig.max_attempts,
              errorCount: result.error.issues.length,
            });

            const repairResult = await this.attemptContextSchemaRepair(
              stage,
              issueNumber,
              schema,
              result.error,
              contextPath,
              currentAttempts + 1
            );

            const repairAttempt = {
              attempted: true,
              succeeded: repairResult.succeeded,
              attempts_count: currentAttempts + 1,
            };
            this.stageRepairAttempts.set(stage, repairAttempt);

            if (repairResult.succeeded) {
              this.logger.info("Context schema repair: succeeded — validation errors cleared", {
                stage,
                issueNumber,
                attempt: currentAttempts + 1,
              });
              return { error: null };
            } else {
              this.logger.warn(
                "Context schema repair: failed — falling through to warn-and-continue",
                {
                  stage,
                  issueNumber,
                  attempt: currentAttempts + 1,
                  repairErrors: repairResult.errorSummary,
                }
              );
              this.logger.warn("Context file has schema mismatches (non-fatal, continuing)", {
                stage,
                issueNumber,
                contextPath,
                issues: `\n${issues}`,
              });
              return { error: null };
            }
          } else {
            this.logger.warn("Context file has schema mismatches (non-fatal, continuing)", {
              stage,
              issueNumber,
              contextPath,
              issues: `\n${issues}`,
            });
            return { error: null };
          }
        }

        this.logger.info("Context file validated after stage (schema passed)", {
          stage,
          issueNumber,
          contextPath,
        });
      } else {
        this.logger.info("Context file validated after stage", {
          stage,
          issueNumber,
          contextPath,
        });
      }
      return { error: null };
    }

    if (OPTIONAL_CONTEXT_STAGES.has(stage)) {
      this.logger.warn("Optional context file missing after stage (non-fatal)", {
        stage,
        issueNumber,
        expectedPath: contextPath,
      });
      return { error: null };
    }

    this.logger.error("Stage exited without writing expected output context file", {
      stage,
      issueNumber,
      expectedPath: contextPath,
    });

    const isPrCreate = stage === "pr-create";
    const missingContextMsg = isPrCreate
      ? `${stage} reported success but context file not found: ${contextPath}. ` +
        `Check if a PR was actually created on GitHub: gh pr list --head <branch> --state all. ` +
        `If no PR exists, the pr-create stage may have exited without invoking gh pr create. ` +
        `Re-run the pr-create stage or create the PR manually.`
      : `${stage} reported success but context file not found: ${contextPath}. ` +
        `The subagent may have exited early without producing output. ` +
        `The next stage requires this file for context handoff.`;

    return {
      error: new Error(missingContextMsg),
    };
  }

  // ---------------------------------------------------------------------------
  // Schema repair
  // ---------------------------------------------------------------------------

  /**
   * Attempt to repair a context file by re-invoking the stage with Zod error
   * details appended to the original skill prompt.
   *
   * @see Issue #2552 — Pipeline context schema self-correction
   */
  private async attemptContextSchemaRepair(
    stage: PipelineStage,
    issueNumber: number,
    schema: ZodSchema,
    zodError: ZodError,
    contextPath: string,
    attemptNumber: number
  ): Promise<{ succeeded: boolean; errorSummary?: string }> {
    try {
      // Step 1: Read the original skill file content via SkillLoader
      const skillResult = this.skillLoader?.loadSkillContent(stage);
      if (!skillResult) {
        this.logger.warn("Context schema repair: skill file not found, skipping repair", {
          stage,
        });
        return { succeeded: false, errorSummary: "Skill file not found" };
      }

      // Step 2: Build repair prompt by appending error-fix instructions
      const errorInstructions = formatZodErrorsForPrompt(zodError);
      const repairSuffix =
        `\n\n---\n` +
        `CONTEXT SCHEMA REPAIR ATTEMPT (attempt ${attemptNumber})\n\n` +
        errorInstructions +
        `\n\nIMPORTANT: Re-write ONLY the JSON context file at: ${contextPath}\n` +
        `Do NOT re-implement code changes or re-run tests. Focus exclusively on ` +
        `producing a correctly-formatted JSON context file that matches the expected schema.`;

      const repairSkillContent = skillResult.content + repairSuffix;

      // Step 3: Re-invoke the stage via runStageSkillHeadless
      const workspaceRoot = this.workspaceRootProvider();
      const repairSucceeded = await new Promise<boolean>((resolve) => {
        const handle = runStageSkillHeadless(
          stage,
          issueNumber,
          {
            onComplete: (result: SkillRunResult) => {
              resolve(result.success);
            },
            onStdout: (_data: string) => {
              // Repair output is not surfaced to the UI — it runs silently
            },
            onStderr: (_data: string) => {
              // Repair stderr is not surfaced to the UI
            },
            onError: (err: Error) => {
              this.logger.warn("Context schema repair: stage subprocess error", {
                stage,
                issueNumber,
                error: err.message,
              });
              resolve(false);
            },
          },
          this.cachedIssueMetadata ?? undefined,
          undefined, // batchContext
          undefined, // skipToPhase
          undefined, // modelOverride
          undefined, // pauseAutoRouting
          workspaceRoot,
          undefined, // modelOverrideSource
          repairSkillContent,
          undefined, // autonomousMode
          undefined, // warnThresholdUsd
          undefined, // targetRepoOverride
          // ADR-017 step 3: the repair re-invocation belongs to the SAME run
          // as the stage it is repairing, so it exports that run's identity.
          this.runIdProvider?.()
        );

        // Safety: if the process handle has no process (error during setup),
        // the onComplete callback may not fire. Guard with a timeout.
        if (!handle.process) {
          resolve(false);
        }
      });

      if (!repairSucceeded) {
        return { succeeded: false, errorSummary: "Stage re-invocation failed" };
      }

      // Step 4: Wait for the repaired context file and re-validate
      const repairedContextPath =
        (await this.waitForContextFile(
          STAGE_OUTPUT_CONTEXT_TYPE[stage]!,
          issueNumber,
          this.contextFileWaitMs
        )) ?? contextPath;

      if (!fs.existsSync(repairedContextPath)) {
        return { succeeded: false, errorSummary: "Repaired context file not found" };
      }

      let repairedParsed: unknown;
      try {
        const raw = fs.readFileSync(repairedContextPath, "utf-8");
        repairedParsed = JSON.parse(raw);
      } catch (err) {
        return {
          succeeded: false,
          errorSummary: `Repaired file is invalid JSON: ${err instanceof Error ? err.message : String(err)}`,
        };
      }

      const repairedResult = schema.safeParse(repairedParsed);
      if (repairedResult.success) {
        return { succeeded: true };
      }

      const remainingIssues = repairedResult.error.issues
        .map((i) => `${i.path.join(".")}: ${i.message}`)
        .join("; ");
      return { succeeded: false, errorSummary: `Still has errors: ${remainingIssues}` };
    } catch (err) {
      this.logger.warn("Context schema repair: unexpected error during repair attempt", {
        stage,
        issueNumber,
        error: err,
      });
      return {
        succeeded: false,
        errorSummary: `Unexpected error: ${err instanceof Error ? err.message : String(err)}`,
      };
    }
  }

  // ---------------------------------------------------------------------------
  // Read context file
  // ---------------------------------------------------------------------------

  /**
   * Read and parse a context file, returning the raw object.
   *
   * Returns null when the file does not exist or cannot be parsed.
   * Used by downstream stages to read planning hints, issue metadata, etc.
   */
  readContextFile(type: ContextFileType, issueNumber: number): Record<string, unknown> | null {
    const contextPath = this.getContextPath(type, issueNumber);
    if (!fs.existsSync(contextPath)) {
      return null;
    }
    try {
      const raw = fs.readFileSync(contextPath, "utf-8");
      return JSON.parse(raw) as Record<string, unknown>;
    } catch {
      return null;
    }
  }

  // ---------------------------------------------------------------------------
  // Deterministic context generation
  // ---------------------------------------------------------------------------

  /**
   * Generate a deterministic context file for a stage. For issue-pickup this
   * is the PRIMARY execution path (deterministic-first, #2614) — historically
   * misnamed "fallback", which misled telemetry and debugging (#189); for the
   * other stages it is the safety net used when the AI subagent exits without
   * writing its output context file.
   *
   * Delegates to the appropriate stage-specific generator.
   *
   * @returns generated=true when the context file was written. For
   *   issue-pickup, `blockedBy` is set (and generated=false) when the issue
   *   has OPEN blockedBy dependencies — the caller must DEFER pickup, not
   *   fall through to the LLM (#189 fail-closed).
   * @see Issue #697 — Subagent exits early without producing output
   */
  async generateDeterministicContext(
    stage: PipelineStage,
    issueNumber: number
  ): Promise<DeterministicContextResult> {
    switch (stage) {
      case "issue-pickup":
        return this.generateDeterministicIssueContext(issueNumber);
      case "feature-planning":
        return { generated: await this.generateDeterministicPlanningContext(issueNumber) };
      case "feature-dev":
        return { generated: await this.generateDeterministicDevContext(issueNumber) };
      case "feature-validate":
        return { generated: await this.generateDeterministicValidateContext(issueNumber) };
      case "pr-create":
        return { generated: await this.generateDeterministicPrContext(issueNumber) };
      default:
        this.logger.warn("generateDeterministicContext: no generator for stage", { stage });
        return { generated: false };
    }
  }

  // ---------------------------------------------------------------------------
  // Private helpers
  // ---------------------------------------------------------------------------

  /** Return ['--repo', override] when a repo override is set, or [] otherwise. */
  private ghRepoArgs(): string[] {
    return this.repoOverride ? ["--repo", this.repoOverride] : [];
  }

  /**
   * Recover a misnamed context file (e.g., pr-{PR_NUMBER}.json vs pr-{ISSUE_NUMBER}.json).
   * @see Issue #870
   */
  private recoverMisnamedContextFile(
    type: ContextFileType,
    issueNumber: number,
    expectedPath: string
  ): boolean {
    try {
      const dir = path.dirname(expectedPath);
      const prefix = `${type}-`;
      const candidates = fs
        .readdirSync(dir)
        .filter(
          (f) => f.startsWith(prefix) && f.endsWith(".json") && f !== path.basename(expectedPath)
        );

      for (const candidate of candidates) {
        const candidatePath = path.join(dir, candidate);
        try {
          const content = fs.readFileSync(candidatePath, "utf-8");
          const parsed = JSON.parse(content) as { issue_number?: number };
          if (parsed.issue_number === issueNumber) {
            fs.renameSync(candidatePath, expectedPath);
            this.logger.warn(
              "Recovered misnamed context file — agent used wrong number in filename",
              {
                type,
                issueNumber,
                originalFile: candidate,
                renamedTo: path.basename(expectedPath),
              }
            );
            return true;
          }
        } catch {
          // Skip unreadable/unparseable candidates
        }
      }
    } catch {
      // Directory read failure — cannot recover
    }
    return false;
  }

  /**
   * Perform one short final wait after the main post-stage timeout expires.
   *
   * This catches late file materialization without weakening the handoff
   * contract for genuinely missing files.
   */
  private async waitForLateContextFile(
    type: ContextFileType,
    issueNumber: number
  ): Promise<string | null> {
    if (this.contextFileWaitMs <= 0) {
      return null;
    }

    const finalWaitMs = Math.min(
      MAX_FINAL_CONSISTENCY_WAIT_MS,
      Math.max(100, Math.floor(this.contextFileWaitMs / 2))
    );
    const lateContextPath = await this.waitForContextFile(type, issueNumber, finalWaitMs);

    if (lateContextPath && fs.existsSync(lateContextPath)) {
      this.logger.warn("Context file appeared during final consistency check", {
        type,
        issueNumber,
        contextPath: lateContextPath,
        waitMs: finalWaitMs,
      });
      return lateContextPath;
    }

    return null;
  }

  /**
   * Parse structured sections from a GitHub issue body.
   * Mirrors the logic in write-issue-context.sh.
   */
  private parseIssueBodySections(
    body: string,
    repository?: string
  ): {
    summary: string;
    userStory: string;
    acceptanceCriteria: string[];
    technicalNotes: string[];
    parentIssue: number | null;
    /**
     * The owner/repo of a `Part of` reference that names a DIFFERENT repository
     * (#1058). Null when the parent is local or absent. Surfaced so "the parent
     * lives in another repo" is distinguishable from "there is no parent".
     */
    parentForeignRepo: string | null;
  } {
    if (!body) {
      return {
        summary: "",
        userStory: "",
        acceptanceCriteria: [],
        technicalNotes: [],
        parentIssue: null,
        parentForeignRepo: null,
      };
    }

    // #1058 — accept the qualified cross-repo form, but only claim the number
    // when it names THIS repository.
    //
    // `.claude/rules/scripts.md` mandates `Part of <owner>/<repo>#<n>` for
    // cross-repo sub-issues, and this parser required `#` to follow `Part of `
    // immediately — so the mandated form was the one form it could not see.
    //
    // Widening the regex alone would be WRONG. `native_parent` is an issue
    // number in this repository; issue numbers are not unique across a
    // workspace, so lifting `205` out of `otherorg/other-repo#205` would point
    // at an unrelated local issue. A foreign parent is deliberately still
    // `null` — but it is now reported as such rather than being indistinguish-
    // able from "no parent at all".
    const parentRef = body.match(/Part of\s+(?:([\w.-]+\/[\w.-]+))?#(\d+)/i);
    const parentQualifier = parentRef?.[1] ?? null;
    const parentIsLocal =
      !!parentRef &&
      (!parentQualifier ||
        (!!repository && parentQualifier.toLowerCase() === repository.toLowerCase()));
    const parentIssue = parentIsLocal ? parseInt(parentRef![2], 10) : null;
    const parentForeignRepo = parentRef && !parentIsLocal ? parentQualifier : null;

    const extractSection = (headers: string[]): string => {
      const lines = body.split("\n");
      let capturing = false;
      const result: string[] = [];

      for (const line of lines) {
        if (/^## /.test(line)) {
          if (capturing) break;
          const matchesHeader = headers.some(
            (h) => line === `## ${h}` || line.startsWith(`## ${h} `)
          );
          if (matchesHeader) {
            capturing = true;
            continue;
          }
        }
        if (capturing) {
          const trimmed = line.trim();
          if (trimmed.length > 0) result.push(trimmed);
        }
      }

      return result.join("\n");
    };

    const summary = extractSection(["Summary", "Description", "Overview"]);
    const userStory = extractSection(["User Story", "User story"]);
    const acRaw = extractSection(["Acceptance Criteria", "Acceptance criteria", "AC"]);
    const techNotesRaw = extractSection([
      "Technical Notes",
      "Technical notes",
      "Tech Notes",
      "Implementation Notes",
    ]);

    const acceptanceCriteria = acRaw
      .split("\n")
      .filter((line) => /^\s*-\s*\[[ xX]\]\s+/.test(line))
      .map((line) => line.replace(/^\s*-\s*\[[ xX]\]\s+/, "").trim())
      .filter((item) => item.length > 0);

    const technicalNotes = techNotesRaw
      .split("\n")
      .filter((line) => /^\s*-\s+/.test(line))
      .map((line) => line.replace(/^\s*-\s+/, "").trim())
      .filter((item) => item.length > 0);

    return {
      summary,
      userStory,
      acceptanceCriteria,
      technicalNotes,
      parentIssue,
      parentForeignRepo,
    };
  }

  // ---------------------------------------------------------------------------
  // Stage-specific fallback generators
  // ---------------------------------------------------------------------------

  private async generateDeterministicIssueContext(
    issueNumber: number
  ): Promise<DeterministicContextResult> {
    const workspaceRoot = this.workspaceRootProvider();
    const contextPath = this.getContextPath("issue", issueNumber);
    const execOptions = {
      encoding: "utf-8" as const,
      cwd: workspaceRoot,
      timeout: 15000,
    };

    try {
      const { stdout: branchRaw } = await execFileAsync(
        "git",
        ["branch", "--show-current"],
        execOptions
      );
      const branch = branchRaw.trim();
      if (!branch) {
        this.logger.error("Cannot generate deterministic context: no current branch");
        return { generated: false };
      }

      let title = `Issue #${issueNumber}`;
      let labels: string[] = [];
      let issueType = "feature";
      let body = "";
      try {
        const { stdout: issueJsonRaw } = await execFileAsync(
          "gh",
          [
            "issue",
            "view",
            String(issueNumber),
            "--json",
            "title,labels,body",
            ...this.ghRepoArgs(),
          ],
          execOptions
        );
        const issueJson = issueJsonRaw.trim();
        const parsed = JSON.parse(issueJson) as {
          title?: string;
          labels?: Array<{ name: string }>;
          body?: string;
        };
        title = parsed.title ?? title;
        labels = (parsed.labels ?? []).map((l) => l.name);
        body = parsed.body ?? "";

        if (labels.some((l) => l.includes("bug"))) issueType = "bug";
        else if (labels.some((l) => l.includes("docs") || l.includes("documentation")))
          issueType = "docs";
        else if (labels.some((l) => l.includes("refactor"))) issueType = "refactor";
        else if (labels.some((l) => l.includes("chore"))) issueType = "chore";
      } catch (err) {
        this.logger.warn("Deterministic context: gh issue fetch failed, using defaults", {
          issueNumber,
          err: err instanceof Error ? err.message : String(err),
        });
      }

      let repository = this.repoOverride ?? "";
      if (!repository) {
        try {
          const { stdout: repoRaw } = await execFileAsync(
            "gh",
            ["repo", "view", "--json", "nameWithOwner", "-q", ".nameWithOwner"],
            execOptions
          );
          repository = repoRaw.trim();
        } catch {
          // Non-critical
        }
      }

      // #1058: repository identity must be resolved BEFORE the body is parsed,
      // so a qualified `Part of owner/repo#N` can be compared against this repo.
      const sections = this.parseIssueBodySections(body, repository);
      const analysis = analyzeChange(labels, title);

      let baseBranch = "main";
      let nativeParent: number | null = null;
      const branchContextPath = path.join(workspaceRoot, ".nightgauge", "plans", ".branch-context");
      try {
        if (fs.existsSync(branchContextPath)) {
          const content = fs.readFileSync(branchContextPath, "utf-8");
          const baseMatch = content.match(/BASE_BRANCH=(\S+)/);
          if (baseMatch?.[1]) baseBranch = baseMatch[1];

          const parentMatch = content.match(/PARENT_ISSUE=(\d+)/);
          if (parentMatch?.[1]) nativeParent = parseInt(parentMatch[1], 10);
        }
      } catch {
        // Use default
      }

      if (nativeParent === null && sections.parentIssue !== null) {
        nativeParent = sections.parentIssue;
      }

      if (nativeParent !== null && !baseBranch.startsWith("epic/")) {
        try {
          const { stdout: lsRemoteRaw } = await execFileAsync(
            "git",
            ["ls-remote", "--heads", "origin", `epic/${nativeParent}-*`],
            execOptions
          );
          const lsRemoteOut2 = lsRemoteRaw.trim();
          const epicBranch = (lsRemoteOut2.split("\n")[0]?.split(/\s+/)[1] ?? "").replace(
            "refs/heads/",
            ""
          );
          if (epicBranch) {
            baseBranch = epicBranch;
            this.logger.info("Fallback context: detected epic branch from remote", {
              issueNumber,
              nativeParent,
              epicBranch,
            });
          }
        } catch {
          // Non-critical: fall back to main
        }
      }

      const routingConfig: RoutingConfig = {
        ...DEFAULT_ROUTING_CONFIG,
        ...this.routingConfig,
        forceFullPipeline: this.forceFullPipeline,
      };
      const decision = makeRoutingDecision(analysis, routingConfig);

      const fallbackContext = {
        schema_version: "1.3",
        issue_number: issueNumber,
        repository,
        title,
        type: issueType,
        branch,
        base_branch: baseBranch,
        native_parent: nativeParent,
        requirements: {
          summary: sections.summary || `Issue #${issueNumber}: ${title}`,
          user_story: sections.userStory || "",
          acceptance_criteria:
            sections.acceptanceCriteria.length > 0 ? sections.acceptanceCriteria : ([] as string[]),
          technical_notes:
            sections.technicalNotes.length > 0 ? sections.technicalNotes : ([] as string[]),
        },
        labels,
        milestone: "",
        routing: {
          change_type: analysis.changeType,
          task_type: analysis.taskType,
          complexity_score: analysis.complexityScore,
          suggested_route: analysis.suggestedRoute,
          skip_stages: analysis.skipStages,
          rationale: analysis.rationale,
          estimated_time_minutes: analysis.estimatedTimeMinutes,
          pickup_recommendation: buildPickupRecommendation(decision, analysis.complexityScore),
        },
        dependencies: {
          blockedBy: [] as number[],
          blocks: [] as number[],
          enforcement_override: false,
        },
        _deterministic: true,
        created_at: new Date().toISOString(),
      };

      // Dependency enforcement on the PRIMARY path (#189): this generator
      // used to hard-code blockedBy: [] — GitHub's native blockedBy edges
      // were never consulted, so nothing structural prevented picking up an
      // issue whose blockers were still open (the dogfood pr-merge deadlock
      // carried a cross-repo ORDERING HAZARD that survived only as prose).
      // Run the same deterministic check the LLM path performs (`hook check-deps`)
      // and FAIL CLOSED when open dependencies exist.
      const deps = await this.checkIssueDependencies(issueNumber, execOptions);
      if (deps) {
        fallbackContext.dependencies.blockedBy = deps.open.map((d) => d.number);
        if (deps.hasOpen) {
          this.logger.warn(
            "Deterministic issue-pickup: issue has OPEN blockedBy dependencies — deferring pickup (fail closed, #189)",
            {
              issueNumber,
              blockedBy: deps.open.map(
                (d) => `${d.repo ? d.repo + "#" : "#"}${d.number} (${d.state})`
              ),
            }
          );
          return { generated: false, blockedBy: deps.open };
        }
      }

      const contextDir = path.dirname(contextPath);
      if (!fs.existsSync(contextDir)) {
        fs.mkdirSync(contextDir, { recursive: true });
      }
      fs.writeFileSync(contextPath, JSON.stringify(fallbackContext, null, 2), "utf-8");

      this.logger.info(
        "Generated deterministic issue context (issue-pickup subagent did not write file)",
        {
          issueNumber,
          contextPath,
          branch,
          title,
          parsedSections: {
            hasSummary: sections.summary.length > 0,
            hasUserStory: sections.userStory.length > 0,
            acCount: sections.acceptanceCriteria.length,
            techNotesCount: sections.technicalNotes.length,
            parentDetected: sections.parentIssue,
            // #1058: a parent in another repository is not "no parent". Logged
            // separately so the third state is visible instead of collapsing
            // into null.
            parentForeignRepo: sections.parentForeignRepo,
          },
        }
      );

      return { generated: true };
    } catch (err) {
      this.logger.error("Failed to generate deterministic issue context", {
        issueNumber,
        err: err instanceof Error ? err.message : String(err),
      });
      return { generated: false };
    }
  }

  /**
   * Run the deterministic dependency check (`nightgauge hook check-deps`,
   * which reads GitHub's native blockedBy edges) for the issue. Returns null
   * when the check could not run (binary unresolved, command failed) — the
   * caller keeps the empty-dependency default rather than blocking pickup on
   * infrastructure hiccups; only a POSITIVE open-dependency answer fails
   * closed (#189).
   */
  private async checkIssueDependencies(
    issueNumber: number,
    execOptions: { encoding: "utf-8"; cwd: string; timeout: number }
  ): Promise<{ hasOpen: boolean; open: OpenDependencyRef[] } | null> {
    try {
      const resolver = BinaryResolver.fromVSCode();
      const binary = await resolver.resolve();
      if (!binary) {
        this.logger.warn(
          "Deterministic issue-pickup: nightgauge binary unresolved — skipping dependency check",
          { issueNumber }
        );
        return null;
      }

      const args = ["hook", "check-deps", String(issueNumber)];
      let slug = this.repoOverride ?? "";
      if (!slug.includes("/")) {
        try {
          const { stdout } = await execFileAsync(
            "gh",
            ["repo", "view", "--json", "nameWithOwner", "-q", ".nameWithOwner"],
            execOptions
          );
          slug = stdout.trim();
        } catch {
          slug = "";
        }
      }
      if (slug.includes("/")) {
        const [owner, repo] = slug.split("/");
        args.push("--owner", owner, "--repo", repo);
      }

      const { stdout } = await execFileAsync(binary, args, { ...execOptions, timeout: 30000 });
      const parsed = JSON.parse(stdout) as {
        has_open_dependencies?: boolean;
        open_dependencies?: Array<{
          number: number;
          title?: string;
          state?: string;
          repo?: string;
        }>;
      };
      const open = (parsed.open_dependencies ?? []).map((d) => ({
        number: d.number,
        title: d.title,
        state: d.state ?? "OPEN",
        repo: d.repo,
      }));
      return { hasOpen: parsed.has_open_dependencies === true, open };
    } catch (err) {
      this.logger.warn("Deterministic issue-pickup: dependency check failed — continuing without", {
        issueNumber,
        err: err instanceof Error ? err.message : String(err),
      });
      return null;
    }
  }

  private async generateDeterministicPrContext(issueNumber: number): Promise<boolean> {
    const workspaceRoot = this.workspaceRootProvider();
    const contextPath = this.getContextPath("pr", issueNumber);
    const execOptions = {
      encoding: "utf-8" as const,
      cwd: workspaceRoot,
      timeout: 15000,
    };

    try {
      const { stdout: branchRaw } = await execFileAsync(
        "git",
        ["branch", "--show-current"],
        execOptions
      );
      const branch = branchRaw.trim();
      if (!branch) {
        this.logger.error("Cannot generate fallback pr context: no current branch");
        return false;
      }

      let prNumber = 0;
      let prUrl = "";
      let prTitle = "";
      let baseBranch = "main";
      let prState: "open" | "merged" = "open";

      try {
        for (const searchState of ["open", "merged"] as const) {
          const { stdout: prJsonRaw } = await execFileAsync(
            "gh",
            [
              "pr",
              "list",
              "--head",
              branch,
              "--state",
              searchState,
              "--json",
              "number,url,title,baseRefName",
              "--limit",
              "1",
            ],
            execOptions
          );
          const prJson = prJsonRaw.trim();
          const prs = JSON.parse(prJson) as Array<{
            number: number;
            url: string;
            title: string;
            baseRefName: string;
          }>;

          if (prs.length > 0) {
            prNumber = prs[0].number;
            prUrl = prs[0].url;
            prTitle = prs[0].title;
            baseBranch = prs[0].baseRefName;
            prState = searchState;
            break;
          }
        }

        if (prNumber === 0) {
          this.logger.warn(
            "No open or merged PR found for branch — cannot generate fallback pr context",
            { branch, issueNumber }
          );
          return false;
        }
      } catch (err) {
        this.logger.error("Failed to query PR metadata for fallback", {
          issueNumber,
          branch,
          err: err instanceof Error ? err.message : String(err),
        });
        return false;
      }

      const fallbackContext = {
        schema_version: "1.0",
        issue_number: issueNumber,
        pr_number: prNumber,
        pr_url: prUrl,
        title: prTitle,
        base_branch: baseBranch,
        status: prState === "merged" ? "merged" : "open",
        reviewers: [] as string[],
        preflight_results: {
          json_validation: "skipped",
          yaml_validation: "skipped",
          version_consistency: "skipped",
          security_scan: "skipped",
          coverage_check: "skipped",
        },
        ci_status: {
          monitored: false,
          checks_passed: 0,
          checks_failed: 0,
          overall_status: "pending",
        },
        _deterministic: true,
        created_at: new Date().toISOString(),
      };

      const contextDir = path.dirname(contextPath);
      if (!fs.existsSync(contextDir)) {
        fs.mkdirSync(contextDir, { recursive: true });
      }
      fs.writeFileSync(contextPath, JSON.stringify(fallbackContext, null, 2), "utf-8");

      this.logger.info(
        "Generated deterministic pr context (pr-create subagent did not write file)",
        { issueNumber, contextPath, prNumber, prUrl, baseBranch }
      );

      return true;
    } catch (err) {
      this.logger.error("Failed to generate fallback pr context", {
        issueNumber,
        err: err instanceof Error ? err.message : String(err),
      });
      return false;
    }
  }

  private async generateDeterministicValidateContext(issueNumber: number): Promise<boolean> {
    const workspaceRoot = this.workspaceRootProvider();
    const contextPath = this.getContextPath("validate", issueNumber);
    const execOptions = {
      encoding: "utf-8" as const,
      cwd: workspaceRoot,
      timeout: 30000,
    };

    try {
      const { stdout: branchRaw } = await execFileAsync(
        "git",
        ["branch", "--show-current"],
        execOptions
      );
      const branch = branchRaw.trim();
      if (!branch) {
        this.logger.error("Cannot generate fallback validate context: no current branch");
        return false;
      }

      let commitSha = "";
      try {
        const { stdout: shaRaw } = await execFileAsync("git", ["rev-parse", "HEAD"], execOptions);
        commitSha = shaRaw.trim();
      } catch {
        // Non-critical
      }

      let filesChanged = 0;
      try {
        let diffStat: string;
        try {
          const { stdout: diffRaw } = await execFileAsync(
            "git",
            ["diff", "--stat", "HEAD~1"],
            execOptions
          );
          diffStat = diffRaw.trim();
        } catch {
          const { stdout: diffRaw } = await execFileAsync(
            "git",
            ["diff", "--stat", "HEAD"],
            execOptions
          );
          diffStat = diffRaw.trim();
        }
        const lines = diffStat.split("\n").filter((l) => l.trim().length > 0);
        filesChanged = Math.max(0, lines.length - 1);
      } catch {
        // Non-critical
      }

      // #3114: detect the repo's test runner before deciding what to execute.
      // Previously this hardcoded `npx vitest run`, which produced false
      // `validation_status: "failed"` for Angular/Jest/Karma repos that have
      // no vitest specs. CI is the actual gate — the fallback should not
      // fabricate failures from a wrong command.
      const runner = detectTestRunner(workspaceRoot);

      let buildSuccess = true;
      let buildOutput = "";
      try {
        // CRITICAL #2884: this used to be execFileSync("npm", ["run", "build"]) —
        // a 30s+ blocking call that froze the VSCode editor for the entire build.
        const { stdout: buildRaw } = await execFileAsync("npm", ["run", "build"], {
          ...execOptions,
          timeout: 60000,
        });
        buildOutput = buildRaw.trim();
        buildSuccess = !buildOutput.includes("error TS") && !buildOutput.includes("Build failed");
      } catch (err: unknown) {
        const execErr = err as { stdout?: Buffer | string; stderr?: Buffer | string };
        buildOutput = String(execErr.stdout ?? "") + String(execErr.stderr ?? "");
        buildSuccess = false;
      }

      let testsRan = false;
      let testsPass: boolean | null = null;
      let testsPassed = 0;
      let testsFailed = 0;
      let testsSkipped = 0;
      let testsReason: string | undefined;

      if (runner === "vitest") {
        testsRan = true;
        try {
          let testOutput: string;
          try {
            // CRITICAL #2884: async to keep VSCode UI responsive (60s+ command).
            const { stdout: testRaw } = await execFileAsync(
              "npx",
              ["vitest", "run", "--reporter=json"],
              { ...execOptions, timeout: 120000 }
            );
            testOutput = testRaw.trim();
          } catch (err: unknown) {
            testOutput = String((err as { stdout?: Buffer | string }).stdout ?? "");
          }
          const jsonMatch = testOutput.match(/\{[\s\S]*"testResults"[\s\S]*\}/);
          if (jsonMatch) {
            const parsed = JSON.parse(jsonMatch[0]) as {
              numPassedTests?: number;
              numFailedTests?: number;
              numPendingTests?: number;
              success?: boolean;
            };
            testsPassed = parsed.numPassedTests ?? 0;
            testsFailed = parsed.numFailedTests ?? 0;
            testsSkipped = parsed.numPendingTests ?? 0;
            testsPass = parsed.success ?? testsFailed === 0;
          } else {
            // No JSON in output — vitest may have found no specs. Don't fail.
            testsPass = null;
            testsReason = "vitest produced no JSON report (no specs?) — CI is the gate";
          }
        } catch {
          testsPass = false;
        }
      } else {
        testsReason = `runner=${runner} not auto-runnable in fallback — CI is the gate`;
      }

      // #3114: only fail validation when the build failed or vitest reported
      // failures. Skipping tests in the fallback is NOT a failure — the real
      // subagent already ran them, and CI will run them again.
      const validationStatus = !buildSuccess || testsPass === false ? "failed" : "passed";

      const fallbackContext = {
        schema_version: "2.1",
        issue_number: issueNumber,
        validation_status: validationStatus,
        build: {
          ran: true,
          passed: buildSuccess,
          command: "npm run build",
        },
        unit_tests: {
          ran: testsRan,
          passed: testsPass,
          framework: runner,
          tests_run: testsPassed + testsFailed + testsSkipped,
          tests_passed: testsPassed,
          ...(testsReason ? { reason: testsReason } : {}),
        },
        integration_tests: { ran: false, passed: null, reason: "fallback — not executed" },
        e2e_tests: { ran: false, passed: null, reason: "fallback — not executed" },
        commit_sha: commitSha,
        branch,
        files_changed: filesChanged,
        dead_code_warnings: [],
        _deterministic: true,
        created_at: new Date().toISOString(),
      };

      const contextDir = path.dirname(contextPath);
      if (!fs.existsSync(contextDir)) {
        fs.mkdirSync(contextDir, { recursive: true });
      }
      fs.writeFileSync(contextPath, JSON.stringify(fallbackContext, null, 2), "utf-8");

      this.logger.info(
        "Generated deterministic validate context (feature-validate subagent did not write file)",
        {
          issueNumber,
          contextPath,
          validationStatus,
          buildSuccess,
          runner,
          testsRan,
          testsPass,
        }
      );

      return true;
    } catch (err) {
      this.logger.error("Failed to generate fallback validate context", {
        issueNumber,
        err: err instanceof Error ? err.message : String(err),
      });
      return false;
    }
  }

  private async generateDeterministicDevContext(issueNumber: number): Promise<boolean> {
    const workspaceRoot = this.workspaceRootProvider();
    const contextPath = this.getContextPath("dev", issueNumber);
    const execOptions = {
      encoding: "utf-8" as const,
      cwd: workspaceRoot,
      timeout: 30000,
    };

    try {
      let baseBranch = "main";
      try {
        const planningPath = this.getContextPath("planning", issueNumber);
        if (fs.existsSync(planningPath)) {
          const planning = JSON.parse(fs.readFileSync(planningPath, "utf-8")) as {
            base_branch?: string;
          };
          if (planning.base_branch) {
            baseBranch = planning.base_branch;
          }
        }
      } catch {
        // Fall back to main
      }

      // RESOLVE THE REMOTE TIP FIRST, AND NEVER INVENT A BASE (#1241).
      //
      // This used to verify the bare local branch (`main`) and, failing that,
      // fall back to `HEAD~1`. Both readings answer a DIFFERENT question than
      // the one the deliverable claims to answer, and both fail toward
      // over-reporting:
      //
      //  - A local `main` in a long-lived checkout lags `origin/main` by every
      //    commit merged since the last `git pull`. `main...HEAD` then lists
      //    the files of OTHER issues' already-merged work as this stage's
      //    output. That is what happened in the specimen run: feature-dev
      //    correctly wrote nothing, this generator diffed against a `main`
      //    five squash-merges stale, and stamped a handoff claiming six
      //    modified files across two workflows and four pages. The Go gate's
      //    ground truth (internal/ci.DefaultDiffBases — `origin/main` FIRST)
      //    saw the empty tree, read the fabricated claim, and convicted the
      //    stage of `dev_produced_no_changes`: an agent-class failure that
      //    halted the whole repository over a receipt this function wrote.
      //  - `HEAD~1` names the previous commit whatever it is. In a fresh
      //    worktree branched off the default branch that is a stranger's merge
      //    commit, so the "cannot resolve a base" path produced the most
      //    confident wrong answer of all.
      //
      // The base list is now `origin/<base>` then `<base>`, in that order —
      // byte-for-byte the Go side's DefaultDiffBases ladder, because two
      // resolvers disagreeing about which ref is the base is exactly the
      // dual-path-drift class docs/FAILURE_TAXONOMY.md names, and here the
      // disagreement is not cosmetic: it is the difference between a gate
      // passing and a repository halting.
      //
      // When NOTHING resolves the file list stays EMPTY. That mirrors
      // inspectDevWork's fail-open contract (`Determined: false` → cannot
      // verify, do not accuse): an empty list makes the gate report its honest
      // "dev context records zero file changes" no-op, while a fabricated one
      // makes it report a stage that lied about work it did. The second is
      // worse in both directions — it accuses the stage AND hides the real
      // condition.
      const diffBases = [`origin/${baseBranch}`, baseBranch];
      let resolvedBase: string | null = null;
      for (const candidate of diffBases) {
        try {
          await execFileAsync(
            "git",
            ["rev-parse", "--verify", `${candidate}^{commit}`],
            execOptions
          );
          resolvedBase = candidate;
          break;
        } catch {
          // Try the next candidate.
        }
      }

      const created: string[] = [];
      const modified: string[] = [];
      const deleted: string[] = [];

      if (resolvedBase === null) {
        this.logger.warn(
          "Deterministic dev context: no base ref resolved — recording an EMPTY file list rather than inventing one (#1241)",
          { issueNumber, tried: diffBases }
        );
      }

      try {
        let diffOutput = "";
        if (resolvedBase !== null) {
          const { stdout: diffRaw } = await execFileAsync(
            "git",
            ["diff", "--name-status", `${resolvedBase}...HEAD`],
            execOptions
          );
          diffOutput = diffRaw.trim();
        }

        if (diffOutput) {
          for (const line of diffOutput.split("\n")) {
            const match = line.match(/^([AMDRC])\t(.+)/);
            if (match) {
              const [, status, filePath] = match;
              if (status === "A") created.push(filePath);
              else if (status === "M" || status === "R" || status === "C") modified.push(filePath);
              else if (status === "D") deleted.push(filePath);
            }
          }
        }
      } catch {
        // Non-critical — continue with empty arrays
      }

      const fallbackContext = {
        schema_version: "1.6",
        issue_number: issueNumber,
        commit_sha: null,
        files_changed: { created, modified, deleted },
        build_verification: { ran: false, status: "skipped", commands_run: [] },
        tests_status: { passed: 0, failed: 0, coverage: null, test_command: "" },
        quality_checks: {
          code_standards: "skipped",
          security_review: "skipped",
          type_check: "skipped",
          dead_code_scan: "not_run",
        },
        feedback: [],
        retry_count: 0,
        retry_reasons: [],
        _deterministic: true,
        created_at: new Date().toISOString(),
      };

      const contextDir = path.dirname(contextPath);
      if (!fs.existsSync(contextDir)) {
        fs.mkdirSync(contextDir, { recursive: true });
      }
      fs.writeFileSync(contextPath, JSON.stringify(fallbackContext, null, 2), "utf-8");

      this.logger.info(
        "Generated deterministic dev context (feature-dev subagent did not write file)",
        {
          issueNumber,
          contextPath,
          filesCreated: created.length,
          filesModified: modified.length,
          filesDeleted: deleted.length,
        }
      );

      return true;
    } catch (err) {
      this.logger.error("Failed to generate fallback dev context", {
        issueNumber,
        err: err instanceof Error ? err.message : String(err),
      });
      return false;
    }
  }

  private async generateDeterministicPlanningContext(issueNumber: number): Promise<boolean> {
    const workspaceRoot = this.workspaceRootProvider();
    const contextPath = this.getContextPath("planning", issueNumber);

    try {
      const issueContextPath = this.getContextPath("issue", issueNumber);
      let title = `Issue #${issueNumber}`;
      let acceptanceCriteria: string[] = [];
      let technicalNotes: string[] = [];
      let sizeLabel: string | undefined;
      let typeLabel: string | undefined;
      let priorityLabel: string | undefined;

      if (fs.existsSync(issueContextPath)) {
        try {
          const raw = JSON.parse(fs.readFileSync(issueContextPath, "utf-8"));
          title = raw.title ?? title;
          acceptanceCriteria = raw.acceptance_criteria ?? [];
          technicalNotes = raw.technical_notes ?? [];
          const labels: string[] = raw.labels ?? [];
          sizeLabel = labels.find((l: string) => l.startsWith("size:"))?.replace("size:", "");
          typeLabel = labels.find((l: string) => l.startsWith("type:"))?.replace("type:", "");
          priorityLabel = labels
            .find((l: string) => l.startsWith("priority:"))
            ?.replace("priority:", "");
        } catch {
          // Non-critical — continue with defaults
        }
      }

      const approach =
        acceptanceCriteria.length > 0
          ? `Implement: ${title}. Acceptance criteria: ${acceptanceCriteria.slice(0, 3).join("; ")}`
          : `Implement: ${title}`;

      const planDir = path.join(workspaceRoot, ".nightgauge", "plans");
      if (!fs.existsSync(planDir)) {
        fs.mkdirSync(planDir, { recursive: true });
      }
      const planFile = path.join(planDir, `PLAN-${issueNumber}.md`);
      const planContent = [
        `# Plan for #${issueNumber}: ${title}`,
        "",
        "## Approach",
        approach,
        "",
        ...(acceptanceCriteria.length > 0
          ? ["## Acceptance Criteria", ...acceptanceCriteria.map((ac) => `- ${ac}`), ""]
          : []),
        ...(technicalNotes.length > 0
          ? ["## Technical Notes", ...technicalNotes.map((tn) => `- ${tn}`), ""]
          : []),
        "_Generated deterministically — feature-planning subagent did not write context._",
      ].join("\n");
      fs.writeFileSync(planFile, planContent, "utf-8");

      const fallbackContext = {
        schema_version: "1.5",
        issue_number: issueNumber,
        plan_file: planFile,
        approach,
        files_to_create: [],
        files_to_modify: [],
        complexity_assessment: {
          size_label: sizeLabel ?? null,
          type_label: typeLabel ?? null,
          priority_label: priorityLabel ?? null,
        },
        _deterministic: true,
        created_at: new Date().toISOString(),
      };

      const contextDir = path.dirname(contextPath);
      if (!fs.existsSync(contextDir)) {
        fs.mkdirSync(contextDir, { recursive: true });
      }
      fs.writeFileSync(contextPath, JSON.stringify(fallbackContext, null, 2), "utf-8");

      this.logger.info(
        "Generated deterministic planning context (feature-planning subagent did not write file)",
        {
          issueNumber,
          contextPath,
          planFile,
          acceptanceCriteriaCount: acceptanceCriteria.length,
          technicalNotesCount: technicalNotes.length,
        }
      );

      return true;
    } catch (err) {
      this.logger.error("Failed to generate fallback planning context", {
        issueNumber,
        err: err instanceof Error ? err.message : String(err),
      });
      return false;
    }
  }
}
