/**
 * PipelineOrchestrator - Main orchestrator for Nightgauge pipeline
 *
 * Manages the execution of pipeline stages using the Claude Agent SDK.
 * Implements context-isolated execution as described in ARCHITECTURE.md.
 *
 * @see docs/ARCHITECTURE.md for architectural overview
 * @see docs/CONTEXT_ARCHITECTURE.md for context file flow
 */

import * as fs from "fs";
import { EventBus, PipelineRunEmitter, PipelineStage } from "../events/EventBus.js";
import { TokenTracker } from "../tracking/TokenTracker.js";
import { ContextManager } from "../context/ContextManager.js";
import {
  StageExecutor,
  buildStagePrompt,
  loadStageSkill,
  type SDKQueryFunction,
  type SDKMessage,
} from "./StageExecutor.js";
import {
  resolveOrchestrationConfig,
  prefersNativeOffload,
  type OrchestrationConfig,
  type ResolvedOrchestrationConfig,
  type OrchestrationStage,
} from "../cli/workflow/OrchestrationConfig.js";
import { parseOrchestrationFrontmatter } from "../cli/workflow/parseOrchestrationFrontmatter.js";
import type { WorkflowSpec } from "../cli/workflow/WorkflowSpec.js";
import type { WorkflowExecutorBindings } from "../cli/workflow/SdkFanoutRunner.js";
import { WorkflowExecutor, createNodeJournalFs, type JournalFs } from "./WorkflowExecutor.js";
import type { ICliAdapter } from "../cli/adapters/ICliAdapter.js";
import { TraceRecorder } from "../events/traceRecorder.js";
import { RunStateManager, uuidV7 } from "../context/RunStateManager.js";

/**
 * Default pipeline stages in execution order
 */
export const DEFAULT_STAGES: PipelineStage[] = [
  "issue-pickup",
  "feature-planning",
  "feature-dev",
  "feature-validate",
  "pr-create",
  "pr-merge",
];

/**
 * Stages that require user approval before continuing
 */
export const APPROVAL_STAGES: PipelineStage[] = ["feature-planning"];

/**
 * Configuration for PipelineOrchestrator
 */
export interface PipelineConfig {
  /** Base path for context files (default: '.nightgauge/pipeline') */
  contextPath?: string;
  /** Base path for plan files (default: '.nightgauge/plans') */
  plansPath?: string;
  /** Base path for skill files (default: 'skills') */
  skillsPath?: string;
  /** Default model to use (default: 'sonnet') */
  defaultModel?: "sonnet" | "opus" | "haiku";
  /**
   * CLI provider identifier (claude | codex | gemini | …) for the single-agent
   * execution path. Threaded to `StageExecutor.execute` so provider-aware
   * steering (AGENTS.md #4028 / GEMINI.md #1055) and the system-prompt preset
   * are provisioned correctly on the SDK-CLI path. Undefined → Claude defaults
   * (CLI adapters ignore `systemPrompt`). Distinct from `workflowAdapter` (the
   * resolved fan-out INSTANCE). @see Issue #4038
   */
  adapter?: string;
  /** Pipeline stages to run (default: all stages) */
  stages?: PipelineStage[];
  /** Max turns per stage (default: 50) */
  maxTurnsPerStage?: number;
  /** Working directory for SDK calls */
  cwd?: string;
  /** Skip approval prompts and auto-approve (for CI mode) */
  autoApprove?: boolean;
  /** Global timeout in milliseconds (default: 3600000 = 1 hour) */
  globalTimeoutMs?: number;
  /** Per-stage timeout in milliseconds (default: 900000 = 15 minutes) */
  stageTimeoutMs?: number;
  /** Maximum backtracks per pipeline run (default: 1, 0 disables) @see Issue #1342 */
  maxBacktracks?: number;
  /**
   * Multi-agent orchestration knobs (epic #3899). Off by default — the engine is
   * opt-in while the epic lands. The orchestrator resolves this once via
   * {@link resolveOrchestrationConfig} and surfaces the single resolved value as
   * `orchestrationConfig`. @see Issue #3901
   */
  orchestration?: OrchestrationConfig;
  /**
   * The resolved CLI adapter INSTANCE (#3902). `selectExecutor` reads its
   * `getOrchestrationCapability()` to pick the fan-out backend. Absent → a
   * workflow-eligible stage has no orchestration-capable adapter and falls back
   * to the single-agent path (the AC4 graceful downgrade). Distinct from the
   * CLI's string `adapter` identifier. @see Issue #3913
   */
  workflowAdapter?: ICliAdapter;
  /**
   * Provider execution bindings the `WorkflowExecutor` fans out through. Required
   * to actually run a fan-out; absent → workflow-eligible stages fall back to
   * single-agent. Kept off `queryFn` so the floor stays provider-pluggable.
   */
  workflowBindings?: WorkflowExecutorBindings;
  /**
   * Filesystem seam for the workflow durable journal. Defaults to the Node
   * `fs/promises`-backed journal; injected in tests. @see Issue #3908
   */
  workflowJournalFs?: JournalFs;
}

/**
 * The executor `selectExecutor` resolves for a stage. `single-agent` keeps the
 * unchanged `StageExecutor` path; `workflow` carries the compiled
 * {@link WorkflowSpec} and the engine that runs it. This is the ONE place the
 * pipeline decides between the two paths. @see Issue #3913
 */
export type ExecutorSelection =
  { kind: "single-agent" } | { kind: "workflow"; spec: WorkflowSpec; executor: WorkflowExecutor };

/**
 * Result of a pipeline run
 */
export interface PipelineResult {
  issueNumber: number;
  stagesCompleted: PipelineStage[];
  stagesFailed: PipelineStage[];
  totalDurationMs: number;
  usage: ReturnType<TokenTracker["getTotalUsage"]>;
  success: boolean;
}

/**
 * Result of a single stage run
 */
export interface StageResult {
  stage: PipelineStage;
  issueNumber: number;
  success: boolean;
  durationMs: number;
  messages: SDKMessage[];
  error?: Error;
}

/**
 * PipelineOrchestrator class - the main entry point for SDK usage
 *
 * @example
 * ```typescript
 * import { query } from '@anthropic-ai/claude-agent-sdk';
 * import { PipelineOrchestrator } from '@nightgauge/sdk';
 *
 * const orchestrator = new PipelineOrchestrator(query, {
 *   defaultModel: 'sonnet',
 * });
 *
 * // Subscribe to workflow phase nodes (stages map to first-level phases)
 * orchestrator.events.on('phase', (node) => {
 *   console.log(`Phase ${node.name} → ${node.status}`);
 * });
 *
 * // Run full pipeline
 * const result = await orchestrator.run(42);
 * console.log(`Pipeline ${result.success ? 'succeeded' : 'failed'}`);
 * console.log(`Total cost: $${result.usage.costUsd.toFixed(4)}`);
 * ```
 */
export class PipelineOrchestrator {
  readonly events: EventBus;
  readonly usage: TokenTracker;
  readonly context: ContextManager;

  /**
   * The single resolved orchestration config (epic #3899). Every knob is
   * concrete — consumers (`selectExecutor`, `WorkflowExecutor`) read this and
   * never see `undefined`. @see Issue #3901
   */
  readonly orchestrationConfig: ResolvedOrchestrationConfig;

  private executor: StageExecutor;
  /** Active per-run workflow emitter — re-created at the start of each run / stage. */
  private emitter: PipelineRunEmitter;
  private config: Required<
    Omit<PipelineConfig, "workflowAdapter" | "workflowBindings" | "workflowJournalFs" | "adapter">
  > &
    // `adapter` stays optional — undefined is the Claude/default path (#4038).
    Pick<PipelineConfig, "adapter">;
  /** Resolved adapter instance — drives `selectExecutor`'s backend choice (#3902). */
  private readonly workflowAdapter?: ICliAdapter;
  /** Provider execution bindings the `WorkflowExecutor` fans out through (#3905). */
  private readonly workflowBindings?: WorkflowExecutorBindings;
  /** Durable-journal filesystem seam; lazily defaulted to the Node-backed one. */
  private workflowJournalFs?: JournalFs;
  /**
   * The engine `selectExecutor` routes workflow-eligible stages to. Built lazily
   * (and only once) the first time a fan-out is actually selected, and only when
   * an adapter + bindings are present. @see Issue #3908 / #3913
   */
  private workflowExecutor?: WorkflowExecutor;
  private abortController: AbortController | null = null;
  private isRunning: boolean = false;
  private currentStage: PipelineStage | null = null;
  private approvalResolver: ((approved: boolean) => void) | null = null;

  /** Backtrack engine state (Issue #1342) */
  private backtrackCount: number = 0;
  private traversedEdges: Set<string> = new Set();

  /**
   * Per-run lifecycle trace writer (ADR 013 / Issue #180). Created at the
   * start of each run(); null between runs. Fail-open — never blocks a stage.
   */
  private traceRecorder: TraceRecorder | null = null;

  /** Session IDs per stage for resume-aware backtrack retry. @see Issue #1659 */
  private stageSessionIds: Map<string, string> = new Map();

  constructor(queryFn: SDKQueryFunction, config?: PipelineConfig) {
    this.events = new EventBus();
    this.usage = new TokenTracker();
    this.context = new ContextManager(config?.contextPath ?? ".nightgauge/pipeline");

    this.config = {
      contextPath: config?.contextPath ?? ".nightgauge/pipeline",
      plansPath: config?.plansPath ?? ".nightgauge/plans",
      skillsPath: config?.skillsPath ?? "skills",
      defaultModel: config?.defaultModel ?? "sonnet",
      adapter: config?.adapter,
      stages: config?.stages ?? DEFAULT_STAGES,
      maxTurnsPerStage: config?.maxTurnsPerStage ?? 50,
      cwd: config?.cwd ?? process.cwd(),
      autoApprove: config?.autoApprove ?? false,
      globalTimeoutMs: config?.globalTimeoutMs ?? 3600000, // 1 hour
      stageTimeoutMs: config?.stageTimeoutMs ?? 900000, // 15 minutes
      maxBacktracks: config?.maxBacktracks ?? 1,
      orchestration: config?.orchestration ?? {},
    };

    this.workflowAdapter = config?.workflowAdapter;
    this.workflowBindings = config?.workflowBindings;
    this.workflowJournalFs = config?.workflowJournalFs;

    this.orchestrationConfig = resolveOrchestrationConfig(this.config.orchestration);

    // Seeded with a placeholder emitter (issue 0); each run / standalone stage
    // swaps in a fresh emitter so node ids and `seq` are scoped to that run.
    this.emitter = new PipelineRunEmitter(
      this.events,
      0,
      "sdk-fanout",
      this.config.adapter ?? "claude"
    );
    this.executor = new StageExecutor(this.usage, this.emitter, queryFn);
  }

  /**
   * Install a fresh per-run workflow emitter for an issue and wire it into the
   * executor. Returns the emitter so the caller can drive run-level lifecycle.
   */
  private newEmitter(issueNumber: number): PipelineRunEmitter {
    this.emitter = new PipelineRunEmitter(
      this.events,
      issueNumber,
      "sdk-fanout",
      this.config.adapter ?? "claude"
    );
    this.executor.setEmitter(this.emitter);
    return this.emitter;
  }

  /**
   * Stages that are ALWAYS single-agent / deterministic and are never fanned out,
   * by design — `pr-create` and `pr-merge` are deterministic phase nodes (their
   * skills declare no `orchestration:` block). Listing them here makes the
   * guarantee explicit at the selection point even if a block were ever added.
   *
   * @see docs/WORKFLOW_ORCHESTRATION.md § Safety & guardrails
   */
  private static readonly ALWAYS_SINGLE_AGENT: ReadonlySet<PipelineStage> = new Set([
    "pr-create",
    "pr-merge",
  ]);

  /**
   * The ONE selection point (#3913). Given a stage, decide whether it runs via
   * the multi-agent `WorkflowExecutor` fan-out or the single-agent
   * `StageExecutor` path. Both `runStage` and `runStageStreaming` route through
   * here, so there is exactly one place the pipeline branches.
   *
   * The single-agent path is chosen (unchanged behaviour) when ANY of:
   * - orchestration is disabled (`config.disabled`, the default), OR
   * - the stage is a deterministic `pr-create` / `pr-merge` stage, OR
   * - the stage SKILL declares no usable `orchestration:` frontmatter, OR
   * - no orchestration-capable adapter + bindings are wired (graceful AC4
   *   downgrade — a workflow-eligible stage with no backend never hard-fails).
   *
   * Otherwise the stage's frontmatter is compiled into a {@link WorkflowSpec}
   * (phases/agents/judges/ceiling) honoring `prefer_native_offload` and the
   * budget cap, and the `WorkflowExecutor` is returned to run it. The executor
   * itself clamps the spec's ceiling to the config's `max_agents` /
   * `max_concurrency`, so even a synthetic 1000-agent frontmatter cannot exceed
   * the run budget or the hard provider ceiling.
   */
  async selectExecutor(stage: PipelineStage, issueNumber: number): Promise<ExecutorSelection> {
    // Off by default, and pr-create/pr-merge are never fanned out.
    if (this.orchestrationConfig.disabled || PipelineOrchestrator.ALWAYS_SINGLE_AGENT.has(stage)) {
      return { kind: "single-agent" };
    }

    // No adapter + bindings means there is no fan-out backend to run on — fall
    // back to single-agent rather than failing (native-workflow → sdk-fanout →
    // single-agent chain bottoms out here).
    const executor = await this.getWorkflowExecutor();
    if (!executor) {
      return { kind: "single-agent" };
    }

    // Read the SAME skill content `buildStagePrompt` loads, and compile its
    // `orchestration:` block. No block (or no usable units) → single-agent.
    const { skillContent } = await loadStageSkill(stage, this.config.skillsPath);
    const spec = parseOrchestrationFrontmatter(skillContent, {
      runId: `wf-${issueNumber}-${stage}`,
      issueNumber,
      stage,
      preferNativeOffload: prefersNativeOffload(
        this.orchestrationConfig,
        stage as OrchestrationStage
      ),
      // Surface the run budget cap (#3901) onto the spec so the executor enforces
      // it; `0` (uncapped) leaves it off the spec.
      budgetUsd: this.orchestrationConfig.max_usd,
    });
    if (!spec) {
      return { kind: "single-agent" };
    }

    return { kind: "workflow", spec, executor };
  }

  /**
   * Lazily build (once) the `WorkflowExecutor`. Returns `undefined` when no
   * adapter + bindings are wired — the caller then takes the single-agent path.
   */
  private async getWorkflowExecutor(): Promise<WorkflowExecutor | undefined> {
    if (this.workflowExecutor) return this.workflowExecutor;
    if (!this.workflowAdapter || !this.workflowBindings) return undefined;
    // Default to the Node `fs/promises`-backed journal on first fan-out unless a
    // seam was injected (tests inject an in-memory one).
    if (!this.workflowJournalFs) {
      this.workflowJournalFs = await createNodeJournalFs();
    }
    this.workflowExecutor = new WorkflowExecutor({
      adapter: this.workflowAdapter,
      config: this.orchestrationConfig,
      bindings: this.workflowBindings,
      tokenTracker: this.usage,
      fs: this.workflowJournalFs,
      journalDir: this.config.contextPath,
    });
    return this.workflowExecutor;
  }

  /**
   * Run a stage via the multi-agent `WorkflowExecutor`, bracketed by the
   * pipeline's own phase/agent nodes so the stage still appears as one phase in
   * the canonical tree while its fan-out sub-nodes stream through the same sink.
   */
  private async runStageWorkflow(
    stage: PipelineStage,
    selection: Extract<ExecutorSelection, { kind: "workflow" }>
  ): Promise<void> {
    this.emitter.stageStarted(stage);
    try {
      // The EventBus is itself the canonical WorkflowEventSink, so the fan-out's
      // sub-agent/judge nodes fold into the same live tree as the stage node.
      const result = await selection.executor.execute(selection.spec, this.events);
      const failed = result.summary.status === "failed";
      if (failed) {
        this.emitter.stageFailed(stage, "error");
        throw new Error(`workflow stage '${stage}' failed (status=${result.summary.status})`);
      }
      this.emitter.stageCompleted(stage);
    } catch (error) {
      this.emitter.stageFailed(stage, "error");
      throw error instanceof Error ? error : new Error(String(error));
    }
  }

  /**
   * Read feedback signals from a completed stage's context file.
   * @see Issue #1342 - Orchestrator Backtrack Engine
   */
  private readFeedbackSignals(
    stage: PipelineStage,
    issueNumber: number
  ): Array<{
    signal_type: string;
    severity: string;
    backtrack_target_stage?: string | null;
    rationale: string;
    emitted_by_stage: string;
    evidence: string[];
  }> {
    const contextTypeMap: Partial<Record<PipelineStage, string>> = {
      "feature-dev": "dev",
      "feature-validate": "validate",
    };

    const contextType = contextTypeMap[stage];
    if (!contextType) return [];

    try {
      const contextPath = `${this.config.contextPath}/${contextType}-${issueNumber}.json`;
      if (!fs.existsSync(contextPath)) return [];

      const content = fs.readFileSync(contextPath, "utf-8");
      const parsed = JSON.parse(content);

      if (!Array.isArray(parsed.feedback) || parsed.feedback.length === 0) {
        return [];
      }

      return parsed.feedback.filter(
        (signal: {
          severity: string;
          backtrack_target_stage?: string | null;
          signal_type: string;
        }) =>
          signal.severity === "blocking" &&
          signal.backtrack_target_stage != null &&
          signal.signal_type !== "MODEL_ESCALATION_NEEDED"
      );
    } catch {
      return [];
    }
  }

  /**
   * Evaluate whether a backtrack is allowed.
   * @see Issue #1342 - Orchestrator Backtrack Engine
   */
  private evaluateBacktrack(
    signal: {
      backtrack_target_stage?: string | null;
      signal_type: string;
      rationale: string;
    },
    currentStage: PipelineStage
  ): boolean {
    const targetStage = signal.backtrack_target_stage!;
    const maxBacktracks = this.config.maxBacktracks;

    // Backtrack is a control-plane decision, not a workflow node. A rewind
    // simply re-runs earlier stages, which re-emit their phase nodes (same
    // nodeId, higher seq → last-write-wins on the tree).
    if (this.backtrackCount >= maxBacktracks) {
      return false;
    }

    const edgeKey = `${currentStage}->${targetStage}`;
    if (this.traversedEdges.has(edgeKey)) {
      return false;
    }

    return true;
  }

  /**
   * Execute a backtrack: write the feedback file and rewind.
   * @returns Index of the target stage for loop rewinding.
   * @see Issue #1342 - Orchestrator Backtrack Engine
   */
  private executeBacktrack(
    signal: {
      backtrack_target_stage?: string | null;
      signal_type: string;
      rationale: string;
      emitted_by_stage: string;
      evidence: string[];
      severity: string;
    },
    currentStage: PipelineStage,
    issueNumber: number
  ): number {
    const targetStage = signal.backtrack_target_stage as PipelineStage;
    const edgeKey = `${currentStage}->${targetStage}`;
    this.traversedEdges.add(edgeKey);
    this.backtrackCount++;

    // Lifecycle trace (#180): persist the backtrack decision with its
    // rationale and evidence BEFORE the transient feedback file is written —
    // this record survives pr-merge context cleanup.
    this.traceRecorder?.backtrack({
      fromStage: currentStage,
      targetStage,
      signalType: signal.signal_type,
      rationale: signal.rationale,
      evidence: signal.evidence,
      trigger: "feedback",
    });

    // Write feedback-{N}.json
    try {
      const feedbackPath = `${this.config.contextPath}/feedback-${issueNumber}.json`;
      const feedbackContext = {
        schema_version: "1.0",
        issue_number: issueNumber,
        signals: [signal],
        created_at: new Date().toISOString(),
      };
      fs.writeFileSync(feedbackPath, JSON.stringify(feedbackContext, null, 2));
    } catch {
      // Non-critical
    }

    return this.config.stages.indexOf(targetStage);
  }

  /**
   * Run the full pipeline for an issue
   *
   * Executes each configured stage in order, with approval gates
   * for stages in APPROVAL_STAGES.
   */
  async run(issueNumber: number): Promise<PipelineResult> {
    if (this.isRunning) {
      throw new Error("Pipeline is already running");
    }

    const startTime = Date.now();
    this.isRunning = true;
    this.abortController = new AbortController();

    // One workflow emitter (root WorkflowRun) drives the whole run; internal
    // runStage() calls reuse it so `seq` stays monotonic across stages.
    const emitter = this.newEmitter(issueNumber);
    emitter.runStarted();

    // Lifecycle trace (#180): join the run-state run_id when one exists so
    // SDK events interleave with the Go writer's; a standalone SDK run with
    // no run-state gets a locally generated UUID v7 (same fallback the Go
    // scheduler applies) so the run is still traced coherently.
    const runStateRunId = await new RunStateManager(this.config.contextPath)
      .read()
      .then((s) => s?.run_id ?? null)
      .catch(() => null);
    this.traceRecorder = TraceRecorder.open({
      pipelineDir: this.config.contextPath,
      runId: runStateRunId ?? uuidV7(),
      issue: issueNumber,
    });

    const stagesCompleted: PipelineStage[] = [];
    const stagesFailed: PipelineStage[] = [];
    this.backtrackCount = 0;
    this.traversedEdges.clear();
    this.stageSessionIds.clear();

    try {
      for (let stageIdx = 0; stageIdx < this.config.stages.length; stageIdx++) {
        const stage = this.config.stages[stageIdx];
        if (this.abortController.signal.aborted) {
          break;
        }

        this.currentStage = stage;

        // Approval gating is a control-plane decision (no workflow node). In CI
        // mode it is implicit; interactive mode blocks on the resolver.
        if (APPROVAL_STAGES.includes(stage) && !this.config.autoApprove) {
          const approved = await this.waitForApproval();
          if (!approved) {
            break;
          }
        }

        // On backtrack retry, pass the session ID from the prior run of this stage
        // so Codex can resume rather than starting from scratch. @see Issue #1659
        const priorSessionId = this.stageSessionIds.get(`${issueNumber}:${stage}`);
        const result = await this.runStage(stage, issueNumber, {
          resumeSessionId: priorSessionId,
        });

        if (result.success) {
          // Capture Codex thread ID from this stage for use on backtrack retry.
          // @see Issue #1659
          const sessionId = this.executor.getLastSessionId();
          if (sessionId) {
            this.stageSessionIds.set(`${issueNumber}:${stage}`, sessionId);
          }

          // --- BACKTRACK ENGINE (Issue #1342) ---
          const feedbackSignals = this.readFeedbackSignals(stage, issueNumber);
          if (feedbackSignals.length > 0) {
            const signal = feedbackSignals[0];
            const canBacktrack = this.evaluateBacktrack(signal, stage);
            if (canBacktrack) {
              const targetIdx = this.executeBacktrack(signal, stage, issueNumber);
              stageIdx = targetIdx - 1; // Loop increments
              continue;
            }
          }

          stagesCompleted.push(stage);
        } else {
          stagesFailed.push(stage);
          break;
        }
      }

      const totalDurationMs = Date.now() - startTime;
      const success = stagesFailed.length === 0;
      emitter.runFinished(success ? "succeeded" : "failed");

      return {
        issueNumber,
        stagesCompleted,
        stagesFailed,
        totalDurationMs,
        usage: this.usage.getTotalUsage(),
        success,
      };
    } finally {
      // Drain the trace recorder's append chain before the run is considered
      // finished (fail-open: flush never throws past the recorder).
      await this.traceRecorder?.flush();
      this.traceRecorder = null;
      this.isRunning = false;
      this.currentStage = null;
      this.abortController = null;
      this.backtrackCount = 0;
      this.traversedEdges.clear();
      this.stageSessionIds.clear();
    }
  }

  /**
   * Run a single pipeline stage
   *
   * @param resumeSessionId - Codex thread ID for `exec resume` on backtrack retry. @see Issue #1659
   */
  async runStage(
    stage: PipelineStage,
    issueNumber: number,
    options?: { resumeSessionId?: string }
  ): Promise<StageResult> {
    const startTime = Date.now();
    const messages: SDKMessage[] = [];

    // Standalone invocation (outside a full run): give the stage its own
    // single-phase workflow run so its phase/agent nodes are well-formed.
    if (!this.isRunning) {
      this.newEmitter(issueNumber);
    }

    try {
      // The ONE selection point: fan-out via the WorkflowExecutor, or the
      // unchanged single-agent StageExecutor path.
      const selection = await this.selectExecutor(stage, issueNumber);
      if (selection.kind === "workflow") {
        await this.runStageWorkflow(stage, selection);
        return {
          stage,
          issueNumber,
          success: true,
          durationMs: Date.now() - startTime,
          messages,
        };
      }

      const prompt = await buildStagePrompt(stage, issueNumber, this.config.skillsPath);

      for await (const message of this.executor.execute({
        stage,
        issueNumber,
        prompt,
        model: this.config.defaultModel,
        adapter: this.config.adapter,
        maxTurns: this.config.maxTurnsPerStage,
        cwd: this.config.cwd,
        timeoutMs: this.config.stageTimeoutMs,
        resumeSessionId: options?.resumeSessionId,
      })) {
        messages.push(message);
      }

      return {
        stage,
        issueNumber,
        success: true,
        durationMs: Date.now() - startTime,
        messages,
      };
    } catch (error) {
      return {
        stage,
        issueNumber,
        success: false,
        durationMs: Date.now() - startTime,
        messages,
        error: error instanceof Error ? error : new Error(String(error)),
      };
    }
  }

  /**
   * Run a single stage as an async generator (streaming)
   */
  async *runStageStreaming(stage: PipelineStage, issueNumber: number): AsyncGenerator<SDKMessage> {
    if (!this.isRunning) {
      this.newEmitter(issueNumber);
    }

    // The ONE selection point (same as runStage). A fanned-out stage has no
    // single-agent SDKMessage stream to yield — its fan-out drives the canonical
    // node tree through the EventBus sink, so we run it to completion and yield
    // nothing here. Consumers observe the fan-out via `orchestrator.events`.
    const selection = await this.selectExecutor(stage, issueNumber);
    if (selection.kind === "workflow") {
      await this.runStageWorkflow(stage, selection);
      return;
    }

    const prompt = await buildStagePrompt(stage, issueNumber, this.config.skillsPath);

    yield* this.executor.execute({
      stage,
      issueNumber,
      prompt,
      model: this.config.defaultModel,
      adapter: this.config.adapter,
      maxTurns: this.config.maxTurnsPerStage,
      cwd: this.config.cwd,
      timeoutMs: this.config.stageTimeoutMs,
    });
  }

  /**
   * Approve continuation of an approval-gated stage.
   */
  approve(): void {
    if (this.approvalResolver) {
      this.approvalResolver(true);
      this.approvalResolver = null;
    }
  }

  /**
   * Reject continuation of an approval-gated stage.
   */
  reject(): void {
    if (this.approvalResolver) {
      this.approvalResolver(false);
      this.approvalResolver = null;
    }
  }

  /**
   * Skip the current approval-gated stage and continue pipeline.
   *
   * Emits the stage's phase node as `skipped` and continues to the next stage.
   */
  skip(_issueNumber: number): void {
    if (this.approvalResolver && this.currentStage) {
      this.emitter.stageSkipped(this.currentStage);
      // Lifecycle trace (#180): stage-skip execution is an orchestrator-level
      // decision the Go layer never sees.
      this.traceRecorder?.stageSkip(
        this.currentStage,
        "operator",
        "approval-gated stage skipped by operator"
      );
      this.approvalResolver(true); // Continue pipeline
      this.approvalResolver = null;
    }
  }

  /**
   * Stop the currently running pipeline
   */
  async stop(): Promise<void> {
    if (this.abortController) {
      this.abortController.abort();
    }
    if (this.approvalResolver) {
      this.approvalResolver(false);
      this.approvalResolver = null;
    }
  }

  /**
   * Check if the pipeline is currently running
   */
  getIsRunning(): boolean {
    return this.isRunning;
  }

  /**
   * Get the currently executing stage
   */
  getCurrentStage(): PipelineStage | null {
    return this.currentStage;
  }

  /**
   * Wait for user approval
   */
  private waitForApproval(): Promise<boolean> {
    return new Promise((resolve) => {
      this.approvalResolver = resolve;
    });
  }

  /**
   * Get the pipeline configuration. The workflow wiring (`workflowAdapter` /
   * `workflowBindings` / `workflowJournalFs`) is intentionally excluded — it is
   * runtime plumbing, not user-facing config.
   */
  getConfig(): Readonly<
    Required<
      Omit<PipelineConfig, "workflowAdapter" | "workflowBindings" | "workflowJournalFs" | "adapter">
    > &
      Pick<PipelineConfig, "adapter">
  > {
    return { ...this.config };
  }

  /**
   * Clean up context files for an issue
   */
  async cleanup(issueNumber: number): Promise<string[]> {
    return this.context.cleanup(issueNumber);
  }
}
