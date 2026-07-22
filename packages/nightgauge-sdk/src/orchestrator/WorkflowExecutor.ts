/**
 * WorkflowExecutor — the engine that drives one `WorkflowSpec` end-to-end
 * (epic #3899, Wave 2 — #3908). Sibling to `StageExecutor`.
 *
 * Given a `WorkflowSpec`, an `ICliAdapter`, and the resolved orchestration
 * config, it:
 *
 *  1. **Resolves the backend.** Offload to `adapter.runWorkflow?()` ONLY when the
 *     adapter declares `native-workflow`, actually exposes `runWorkflow`,
 *     `prefer_native_offload` is on for the run's stage, AND a pluggable version
 *     preflight passes. Otherwise it drives the portable `runSdkFanout` floor.
 *     Both paths emit the identical canonical `schemaVersion-4` `WorkflowEvent`
 *     tree, so every consumer (UI forwarders, the durable journal) is
 *     backend-agnostic.
 *
 *  2. **Owns budget for BOTH paths.** It honors `budgetUsd` (spec, else config
 *     `max_usd`) by short-circuiting the fan-out once the aggregated
 *     `SubAgentNode.usage.costUsd` would exceed the cap, emitting a deterministic
 *     `budget-exceeded` terminal. It tightens the spec's ceiling with config
 *     `max_agents` / `max_concurrency` and the {@link ABSOLUTE_CEILING} (these
 *     only ever LOWER the provider safety ceiling, never raise it). The fan-out
 *     is gated against the Go-bridged ratelimit/cooldown quota state (#3909) via
 *     `runSdkFanout`'s `quotaProvider` seam, so a 1000-agent run respects
 *     remaining quota/cooldown.
 *
 *  3. **Records nested per-node usage** on an injected `TokenTracker` so a
 *     fanned-out run's cost rolls up into the pipeline totals.
 *
 *  4. **Writes a durable append-only journal** (one record per `WorkflowEvent`
 *     emission) under `.nightgauge/pipeline/workflow-{runId}.jsonl` so a
 *     crashed/killed run can `resume(runId)`: the journal is replayed to rebuild
 *     the node tree and only pending/running nodes are re-dispatched. Completed
 *     nodes replay their `outputRef` from disk. A node-level liveness heartbeat
 *     lets stale-slot recovery distinguish a wedged run from one still making
 *     progress.
 *
 * SECURITY (see docs/security/WORKFLOW_FANOUT_SECURITY.md — F-series):
 *  - The hard process/concurrency ceiling is a SAFETY control, never a budget
 *    knob; it is clamped to {@link ABSOLUTE_CEILING} before any spawn.
 *  - Replayed `outputRef` is UNTRUSTED input. It is treated as an OPAQUE handle:
 *    never `eval`'d, never used as a path. It is shape- and size-validated on
 *    resume (`sanitizeOutputRef`) and rejected if it looks like a path-traversal
 *    or exceeds the size bound.
 *
 * All side-effecting seams (clock, filesystem, the quota provider, the version
 * preflight) are injected so the executor is fully unit-testable without real
 * adapters, real CLIs, or a real disk.
 *
 * @see docs/WORKFLOW_ORCHESTRATION.md
 * @see Issue #3908
 */

import type { ICliAdapter } from "../cli/adapters/ICliAdapter.js";
import {
  zeroUsage,
  type WorkflowAgentUsage,
  type WorkflowEvent,
  type WorkflowNode,
  type SubAgentNode,
  type JudgeVerdict,
  type OrchestrationCapability,
} from "../cli/workflow/WorkflowEvent.js";
import {
  plannedAgentCount,
  validateWorkflowSpec,
  type WorkflowAgentSpec,
  type WorkflowJudgeSpec,
  type WorkflowSpec,
} from "../cli/workflow/WorkflowSpec.js";
import type { WorkflowEventSink } from "../cli/workflow/WorkflowEventSink.js";
import {
  runSdkFanout,
  type AgentExecutionResult,
  type JudgeExecutionResult,
  type WorkflowExecutorBindings,
  type WorkflowRunSummary,
} from "../cli/workflow/SdkFanoutRunner.js";
import {
  prefersNativeOffload,
  type ResolvedOrchestrationConfig,
  type OrchestrationStage,
} from "../cli/workflow/OrchestrationConfig.js";
import type { QuotaStateProvider } from "../cli/workflow/WorkflowQuotaGate.js";
import type { TokenTracker } from "../tracking/TokenTracker.js";

/**
 * The absolute upper bound any fan-out may request, regardless of the spec or
 * config. This is the safety backstop above every provider ceiling
 * (`CLAUDE_CEILING` 16/1000 is the largest). A spec or config asking for more is
 * CLAMPED, never honored — the executor never spawns unbounded processes even if
 * a misconfiguration or a malicious spec asks it to.
 */
export const ABSOLUTE_CEILING = { maxConcurrent: 16, maxTotal: 1000 } as const;

/**
 * The maximum size (in bytes) of a single replayed `outputRef` handle. A handle
 * larger than this on resume is rejected as untrusted/oversized rather than
 * loaded — defends against a journal poisoned with a giant blob.
 */
export const MAX_OUTPUT_REF_BYTES = 8 * 1024;

/**
 * A version preflight predicate: returns whether an adapter's native
 * `runWorkflow` offload is safe to use right now. The real Claude preflight (a
 * CLI version check ≥ v2.1.154) lands in #3910; until then inject a predicate or
 * rely on the default-deny so the executor uses the proven floor.
 */
export type VersionPreflight = (adapter: ICliAdapter) => boolean | Promise<boolean>;

/** Default preflight: deny native offload. The real check arrives with #3910. */
export const DENY_NATIVE_PREFLIGHT: VersionPreflight = () => false;

/** The two backends the executor can resolve to. */
export type WorkflowBackend = "native-offload" | "sdk-fanout";

/** The minimal async filesystem seam the journal needs. Injected for tests. */
export interface JournalFs {
  mkdir(dir: string): Promise<void>;
  /** Append one already-newline-terminated record to the journal file. */
  appendFile(file: string, data: string): Promise<void>;
  /** Read the whole journal back. Resolves `null` when the file does not exist. */
  readFile(file: string): Promise<string | null>;
}

/** A monotonic clock seam (ms + ISO) so tests control time and heartbeats. */
export interface Clock {
  /** Wall-clock ms (for heartbeat freshness). */
  now(): number;
  /** ISO 8601 timestamp (for event ts). */
  iso(): string;
}

/** The default clock — real wall clock. */
export const SYSTEM_CLOCK: Clock = {
  now: () => Date.now(),
  iso: () => new Date().toISOString(),
};

/** One durable journal record: a node emission plus a liveness heartbeat. */
export interface JournalRecord {
  /** The node emission, verbatim, so the tree replays exactly. */
  event: WorkflowEvent;
  /** Wall-clock ms when this record was written — the node-level heartbeat. */
  heartbeatMs: number;
}

/** Everything the executor needs to run, with every side-effect seam injected. */
export interface WorkflowExecutorDeps {
  adapter: ICliAdapter;
  /** The RESOLVED orchestration config (#3901) — never the raw optional. */
  config: ResolvedOrchestrationConfig;
  /** Provider execution for the portable floor. Required for the fan-out path. */
  bindings: WorkflowExecutorBindings;
  /** Per-node usage rolls up here. */
  tokenTracker?: TokenTracker;
  /**
   * Injected bridge to the Go ratelimit/cooldown quota state (#3909). Passed
   * straight through to `runSdkFanout` so a large fan-out into an exhausted
   * quota is DEFERRED (status "skipped", zero agents) instead of dispatched.
   * Absent the bridge, the fan-out runs unconditionally (the hard ceiling still
   * applies) — graceful degradation per the issue note.
   */
  quotaProvider?: QuotaStateProvider;
  /** Native-offload version preflight; defaults to deny (#3910 lands the real one). */
  versionPreflight?: VersionPreflight;
  /** Filesystem seam for the durable journal. */
  fs: JournalFs;
  /** Clock seam (ms + ISO). Defaults to the system clock. */
  clock?: Clock;
  /** Base dir for the journal. Defaults to `.nightgauge/pipeline`. */
  journalDir?: string;
}

/** What `execute()` / `resume()` return. */
export interface WorkflowExecutionResult {
  runId: string;
  backend: WorkflowBackend;
  summary: WorkflowRunSummary;
  /** True when the run stopped early because the USD budget would be exceeded. */
  budgetStopped: boolean;
  /**
   * True when the quota gate DEFERRED the whole fan-out (#3909): no agents
   * spawned and `summary.status` is "skipped".
   */
  quotaDeferred: boolean;
  /** Aggregated USD cost across every agent and judge. */
  totalCostUsd: number;
  /** Path of the durable journal written for this run. */
  journalPath: string;
}

const DEFAULT_JOURNAL_DIR = ".nightgauge/pipeline";

/**
 * Clamp a spec's ceiling to the {@link ABSOLUTE_CEILING} and the config's
 * `max_agents` / `max_concurrency` (each of which only LOWERS the ceiling — `0`
 * means "use the provider ceiling"). Returns a NEW spec — never mutates the
 * input. This is a safety control: a spec asking for more than the bound is
 * clamped, not honored.
 */
export function clampSpecCeiling(
  spec: WorkflowSpec,
  config: Pick<ResolvedOrchestrationConfig, "max_agents" | "max_concurrency">
): WorkflowSpec {
  let maxConcurrent = Math.min(spec.ceiling.maxConcurrent, ABSOLUTE_CEILING.maxConcurrent);
  let maxTotal = Math.min(spec.ceiling.maxTotal, ABSOLUTE_CEILING.maxTotal);
  if (config.max_concurrency > 0) {
    maxConcurrent = Math.min(maxConcurrent, config.max_concurrency);
  }
  if (config.max_agents > 0) {
    maxTotal = Math.min(maxTotal, config.max_agents);
  }
  return { ...spec, ceiling: { maxConcurrent, maxTotal } };
}

/**
 * Validate and sanitize an UNTRUSTED `outputRef` read back from the journal on
 * resume. The handle is OPAQUE: it is never `eval`'d and never used as a
 * filesystem path. We reject anything that is not a bounded, single-line,
 * non-path-traversing string. Returns the sanitized handle, or `undefined` when
 * the input is absent or fails validation (the caller then replays the node
 * without an output handle rather than trusting poisoned data).
 *
 * @see docs/security/WORKFLOW_FANOUT_SECURITY.md — F-series outputRef guarantees
 */
export function sanitizeOutputRef(raw: unknown): string | undefined {
  if (typeof raw !== "string") return undefined;
  // Size bound (defends against a journal poisoned with a giant blob).
  if (Buffer.byteLength(raw, "utf8") > MAX_OUTPUT_REF_BYTES) return undefined;
  // Opaque handle: non-empty, single line, no control chars, no whitespace.
  if (raw.length === 0) return undefined;
  // eslint-disable-next-line no-control-regex
  if (/[\u0000-\u0020\u007f]/.test(raw)) return undefined;
  // No path traversal — the handle must never be interpretable as a path that
  // escapes a sandbox. Reject absolute paths, parent refs, and home expansion.
  if (raw.includes("..") || raw.startsWith("/") || raw.startsWith("~")) return undefined;
  // Reject a Windows drive-letter or UNC path too.
  if (/^[a-zA-Z]:[\\/]/.test(raw) || raw.startsWith("\\\\")) return undefined;
  return raw;
}

/**
 * Decide which backend a run resolves to. Native offload is chosen ONLY when all
 * four conditions hold; otherwise the portable floor is used (graceful
 * downgrade). The decision is pure given its inputs (the preflight is awaited by
 * the caller and passed in here as a boolean) so it is trivially unit-testable.
 */
export function resolveBackend(args: {
  capability: OrchestrationCapability;
  hasRunWorkflow: boolean;
  prefersNativeOffload: boolean;
  preflightPassed: boolean;
}): WorkflowBackend {
  const native =
    args.capability === "native-workflow" &&
    args.hasRunWorkflow &&
    args.prefersNativeOffload &&
    args.preflightPassed;
  return native ? "native-offload" : "sdk-fanout";
}

/** Thrown when orchestration is globally disabled. */
export class OrchestrationDisabledError extends Error {
  constructor() {
    super("orchestration is disabled (config.disabled = true) — refusing to run a workflow");
    this.name = "OrchestrationDisabledError";
  }
}

/** A terminal agent/judge emission — the nodes that carry REQUIRED usage. */
type TerminalUsageNode = SubAgentNode | JudgeVerdict;

/**
 * A sink that wraps a downstream sink and ALSO mirrors every emission to the
 * durable journal (with a heartbeat), rolls per-node usage into a budget
 * accumulator, and records per-node usage on a `TokenTracker`. It is the
 * executor's single write boundary: both backends emit through it, so journal +
 * heartbeat + cost roll-up happen identically regardless of which backend ran.
 */
class JournalingSink implements WorkflowEventSink {
  private pending: Promise<void> = Promise.resolve();

  constructor(
    private readonly downstream: WorkflowEventSink,
    private readonly fs: JournalFs,
    private readonly journalFile: string,
    private readonly clock: Clock,
    private readonly onTerminalUsage?: (event: TerminalUsageNode) => void
  ) {}

  emit(event: WorkflowEvent): void {
    // Forward to the downstream consumer first (UI/forwarder), then durably
    // append. Appends are serialized through `pending` so records land in
    // emission order even though `emit` is sync and append is async.
    this.downstream.emit(event);

    // Roll per-node usage on the terminal emission of an agent/judge (usage is
    // REQUIRED on those nodes).
    if ((event.kind === "agent" || event.kind === "judge") && event.status !== "running") {
      this.onTerminalUsage?.(event);
    }

    const record: JournalRecord = { event, heartbeatMs: this.clock.now() };
    const line = JSON.stringify(record) + "\n";
    this.pending = this.pending.then(() => this.fs.appendFile(this.journalFile, line));
  }

  async flush(): Promise<void> {
    await this.pending;
    if (this.downstream.flush) await this.downstream.flush();
  }
}

/**
 * The provider-neutral workflow engine. One instance drives one run (or resumes
 * one). It owns backend resolution, budget enforcement, quota gating, the
 * durable journal, and cross-process resume.
 */
export class WorkflowExecutor {
  private readonly adapter: ICliAdapter;
  private readonly config: ResolvedOrchestrationConfig;
  private readonly bindings: WorkflowExecutorBindings;
  private readonly tokenTracker?: TokenTracker;
  private readonly quotaProvider?: QuotaStateProvider;
  private readonly versionPreflight: VersionPreflight;
  private readonly fs: JournalFs;
  private readonly clock: Clock;
  private readonly journalDir: string;

  constructor(deps: WorkflowExecutorDeps) {
    this.adapter = deps.adapter;
    this.config = deps.config;
    this.bindings = deps.bindings;
    this.tokenTracker = deps.tokenTracker;
    this.quotaProvider = deps.quotaProvider;
    this.versionPreflight = deps.versionPreflight ?? DENY_NATIVE_PREFLIGHT;
    this.fs = deps.fs;
    this.clock = deps.clock ?? SYSTEM_CLOCK;
    this.journalDir = deps.journalDir ?? DEFAULT_JOURNAL_DIR;
  }

  /** The durable journal path for a run. */
  journalPathFor(runId: string): string {
    // Manual join (forward slashes) — the journal path is provider-neutral and
    // never platform-specific. runId is engine-generated, not user input.
    const base = this.journalDir.endsWith("/") ? this.journalDir.slice(0, -1) : this.journalDir;
    return `${base}/workflow-${runId}.jsonl`;
  }

  /**
   * Resolve the backend for a spec. Awaits the version preflight only when the
   * cheaper structural conditions already hold (native capability + runWorkflow
   * present + prefer-offload on) — we never pay for a preflight we can't use.
   */
  private async resolveBackendFor(spec: WorkflowSpec): Promise<WorkflowBackend> {
    const capability = this.adapter.getOrchestrationCapability();
    const hasRunWorkflow = typeof this.adapter.runWorkflow === "function";

    // `prefer_native_offload` is per-stage in the resolved config. The spec's
    // own `preferNativeOffload` (when set) overrides the config for that run.
    const prefers =
      spec.preferNativeOffload ??
      (spec.stage ? prefersNativeOffload(this.config, spec.stage as OrchestrationStage) : false);

    // Short-circuit: don't run the preflight unless everything else qualifies.
    if (capability !== "native-workflow" || !hasRunWorkflow || !prefers) {
      return "sdk-fanout";
    }
    let preflightPassed: boolean;
    try {
      preflightPassed = await this.versionPreflight(this.adapter);
    } catch {
      // A throwing preflight is a failed preflight — downgrade to the floor.
      preflightPassed = false;
    }
    return resolveBackend({
      capability,
      hasRunWorkflow,
      prefersNativeOffload: prefers,
      preflightPassed,
    });
  }

  /**
   * The effective USD budget: spec `budgetUsd` overrides config `max_usd`. A
   * value of `0` (config default) means "uncapped" → returns `undefined`.
   */
  private budgetFor(spec: WorkflowSpec): number | undefined {
    const fromSpec = spec.budgetUsd;
    if (typeof fromSpec === "number" && fromSpec > 0) return fromSpec;
    if (this.config.max_usd > 0) return this.config.max_usd;
    return undefined;
  }

  /**
   * Execute a workflow spec end-to-end. Resolves the backend, enforces budget +
   * quota + the absolute ceiling, journals every emission, and returns a result.
   *
   * @throws OrchestrationDisabledError if `config.disabled`.
   * @throws if the (clamped) spec fails `validateWorkflowSpec`.
   */
  async execute(spec: WorkflowSpec, sink: WorkflowEventSink): Promise<WorkflowExecutionResult> {
    if (this.config.disabled) {
      throw new OrchestrationDisabledError();
    }

    // Clamp to the absolute safety ceiling + any config cap BEFORE validating, so
    // a spec asking for more than the bound is rejected if its planned count
    // exceeds the clamped ceiling, never silently over-spawned.
    const clamped = clampSpecCeiling(spec, this.config);
    const problems = validateWorkflowSpec(clamped);
    if (problems.length > 0) {
      throw new Error(`invalid WorkflowSpec: ${problems.join("; ")}`);
    }

    const backend = await this.resolveBackendFor(clamped);
    const journalFile = this.journalPathFor(clamped.runId);
    await this.ensureJournalDir(journalFile);

    return this.drive(clamped, sink, backend, journalFile, new Set());
  }

  /**
   * Resume a crashed/killed run from its durable journal. Replays the journal to
   * rebuild the node tree, re-emits the historical events into `sink` (so a
   * fresh consumer sees the full tree), then re-dispatches ONLY the nodes that
   * never reached a terminal state. Completed agent nodes replay their
   * (sanitized) `outputRef` instead of re-running.
   *
   * @throws OrchestrationDisabledError if `config.disabled`.
   * @throws if no journal exists for `runId`.
   */
  async resume(
    runId: string,
    spec: WorkflowSpec,
    sink: WorkflowEventSink
  ): Promise<WorkflowExecutionResult> {
    if (this.config.disabled) {
      throw new OrchestrationDisabledError();
    }

    const journalFile = this.journalPathFor(runId);
    const raw = await this.fs.readFile(journalFile);
    if (raw === null) {
      throw new Error(`no journal to resume for run ${runId} at ${journalFile}`);
    }

    const { latestByNode, terminalNodeIds } = replayJournal(raw);

    // Re-emit the historical tree into the (fresh) downstream sink so a consumer
    // attaching on resume sees the complete tree, then continues live. These
    // replayed emissions are NOT re-journaled (they already exist on disk). The
    // journal is UNTRUSTED on resume: every agent node's `outputRef` is run
    // through `sanitizeOutputRef` before re-emission so a poisoned handle (a
    // path-traversal etc.) is dropped, never surfaced to a consumer.
    for (const node of orderBySeq([...latestByNode.values()])) {
      sink.emit(sanitizeReplayedNode(node));
    }

    const clamped = clampSpecCeiling(spec, this.config);
    const problems = validateWorkflowSpec(clamped);
    if (problems.length > 0) {
      throw new Error(`invalid WorkflowSpec: ${problems.join("; ")}`);
    }

    const backend = await this.resolveBackendFor(clamped);
    await this.ensureJournalDir(journalFile);

    // Drive only the not-yet-terminal nodes; terminal agent nodes replay their
    // sanitized outputRef from the journal instead of re-running.
    return this.drive(clamped, sink, backend, journalFile, terminalNodeIds, latestByNode);
  }

  /** Ensure the journal directory exists (idempotent). */
  private async ensureJournalDir(journalFile: string): Promise<void> {
    const slash = journalFile.lastIndexOf("/");
    const dir = slash > 0 ? journalFile.slice(0, slash) : this.journalDir;
    await this.fs.mkdir(dir);
  }

  /**
   * The single drive path shared by `execute` and `resume`. Wraps the injected
   * bindings with budget gating + outputRef replay, journals every emission, and
   * dispatches to the resolved backend.
   *
   * `terminalNodeIds` are agent/judge nodes already terminal (skip on resume).
   * `priorNodes` carries the replayed tree so a skipped agent can replay its
   * sanitized `outputRef`.
   */
  private async drive(
    spec: WorkflowSpec,
    sink: WorkflowEventSink,
    backend: WorkflowBackend,
    journalFile: string,
    terminalNodeIds: ReadonlySet<string>,
    priorNodes?: ReadonlyMap<string, WorkflowNode>
  ): Promise<WorkflowExecutionResult> {
    const budget = this.budgetFor(spec);

    // Budget state shared by every spawn. `accruedCostUsd` is the live total; a
    // spawn is short-circuited once one more agent WOULD exceed the cap.
    let accruedCostUsd = 0;
    let budgetStopped = false;

    const journaling = new JournalingSink(sink, this.fs, journalFile, this.clock, (event) => {
      accruedCostUsd += event.usage.costUsd;
      this.tokenTracker?.recordWorkflowNode({
        nodeId: event.nodeId,
        stage: spec.stage,
        inputTokens: event.usage.inputTokens,
        outputTokens: event.usage.outputTokens,
        cacheReadTokens: event.usage.cacheReadTokens,
        cacheCreationTokens: event.usage.cacheCreationTokens,
        costUsd: event.usage.costUsd,
        estimated: event.usage.estimated,
      });
    });

    // Wrap the injected bindings so EACH spawn (a) on resume replays a terminal
    // node's sanitized outputRef instead of re-running, and (b) is short-circuited
    // when the budget is (or would be) exceeded. This makes budget + resume apply
    // identically to BOTH backends: the native offload also runs through these
    // bindings when present.
    const guarded = this.guardBindings({
      budget,
      terminalNodeIds,
      priorNodes,
      getAccruedCost: () => accruedCostUsd,
      onBudgetStop: () => {
        budgetStopped = true;
      },
    });

    let summary: WorkflowRunSummary;
    if (backend === "native-offload" && this.adapter.runWorkflow) {
      summary = await this.runNativeOffload(spec, journaling);
    } else {
      // Pass the quota provider through: the floor consults the Go-bridged
      // ratelimit/cooldown state and DEFERS a large fan-out into an exhausted
      // quota (status "skipped", zero agents). Absent a provider it runs
      // unconditionally — the hard ceiling still applies.
      summary = await runSdkFanout(spec, journaling, guarded, {
        quotaProvider: this.quotaProvider,
      });
    }

    await journaling.flush();

    const quotaDeferred = summary.quotaGate?.deferred ?? false;
    return {
      runId: spec.runId,
      backend,
      summary,
      budgetStopped,
      quotaDeferred,
      totalCostUsd: accruedCostUsd,
      journalPath: journalFile,
    };
  }

  /**
   * Run the native offload backend. The adapter emits the canonical tree through
   * our journaling sink; we collect those emissions to fold a summary so the
   * native path returns the SAME `WorkflowRunSummary` shape as the floor.
   *
   * NOTE: the adapter's `runWorkflow` signature (#3902) takes (spec, sink,
   * options) and does not take bindings. Until #3910 lands a real offload,
   * `resolveBackendFor` keeps this path off by default (deny preflight), so this
   * method is exercised only when a test/adapter opts in.
   */
  private async runNativeOffload(
    spec: WorkflowSpec,
    sink: WorkflowEventSink
  ): Promise<WorkflowRunSummary> {
    const collector = new SummaryCollectingSink(sink);
    await this.adapter.runWorkflow!(spec, collector, { stage: spec.stage });
    if (collector.flush) await collector.flush();
    return collector.toSummary(spec.runId);
  }

  /**
   * Wrap the injected provider bindings with budget gating and resume replay.
   * Returns new bindings the backend drives.
   */
  private guardBindings(ctx: {
    budget: number | undefined;
    terminalNodeIds: ReadonlySet<string>;
    priorNodes?: ReadonlyMap<string, WorkflowNode>;
    getAccruedCost: () => number;
    onBudgetStop: () => void;
  }): WorkflowExecutorBindings {
    const { bindings } = this;

    // Resume replay: map agentId → its terminal node from the prior journal so a
    // resumed run can skip an already-completed agent and replay its sanitized
    // outputRef instead of re-running it.
    const terminalAgentByAgentId = new Map<string, AgentExecutionResult>();
    if (ctx.priorNodes) {
      for (const node of ctx.priorNodes.values()) {
        if (node.kind === "agent" && ctx.terminalNodeIds.has(node.nodeId)) {
          terminalAgentByAgentId.set(node.agentId, {
            usage: node.usage,
            terminalKind: node.terminalKind ?? "success",
            outputRef: sanitizeOutputRef(node.outputRef),
            model: node.model,
          });
        }
      }
    }

    const budgetExhausted = (): boolean =>
      typeof ctx.budget === "number" && ctx.getAccruedCost() >= ctx.budget;
    const wouldExceedBudget = (costUsd: number): boolean =>
      typeof ctx.budget === "number" && ctx.getAccruedCost() + costUsd > ctx.budget;

    return {
      runAgent: async (agent: WorkflowAgentSpec): Promise<AgentExecutionResult> => {
        // Resume: replay a completed agent's sanitized output instead of running.
        const replay = terminalAgentByAgentId.get(agent.agentId);
        if (replay) {
          return replay;
        }

        // Budget already exhausted by prior terminals → do not even run; emit a
        // deterministic budget-exceeded terminal (zero further cost).
        if (budgetExhausted()) {
          ctx.onBudgetStop();
          return { usage: zeroUsage(true), terminalKind: "budget-exceeded" };
        }

        // Run the agent, then if its real cost would push us over the cap,
        // surface a deterministic budget-exceeded terminal. We check post-hoc
        // against the real cost (the executor cannot know cost before running).
        // An over-cap result's cost is NOT banked: the budget is a hard cap, so
        // we zero the terminal's usage (keeping `estimated`) so the aggregated
        // `totalCostUsd` can never exceed `budgetUsd`. The accrual itself is
        // added by the JournalingSink on the (now-zeroed) terminal emission.
        const result = await bindings.runAgent(agent);
        if (result.terminalKind === "success" && wouldExceedBudget(result.usage.costUsd)) {
          ctx.onBudgetStop();
          return {
            ...result,
            terminalKind: "budget-exceeded",
            usage: zeroUsage(result.usage.estimated),
          };
        }
        return result;
      },

      runJudge: async (
        judge: WorkflowJudgeSpec,
        targetNodeId: string
      ): Promise<JudgeExecutionResult> => {
        // Judges consume budget like any agent — skip once the cap is reached.
        if (budgetExhausted()) {
          ctx.onBudgetStop();
          return { verdict: "uncertain", usage: zeroUsage(true) };
        }
        return bindings.runJudge(judge, targetNodeId);
      },
    };
  }
}

/**
 * A sink that mirrors emissions to a downstream sink AND folds them into a run
 * summary — used to give the native-offload path the same `WorkflowRunSummary`
 * return shape as the floor without the adapter computing one.
 */
class SummaryCollectingSink implements WorkflowEventSink {
  private agentCount = 0;
  private judgeCount = 0;
  private agentsSucceeded = 0;
  private agentsFailed = 0;
  private usage = zeroUsage();
  private status: WorkflowRunSummary["status"] = "running";
  private readonly seenAgent = new Set<string>();
  private readonly seenJudge = new Set<string>();

  constructor(private readonly downstream: WorkflowEventSink) {}

  emit(event: WorkflowEvent): void {
    this.downstream.emit(event);
    if (event.kind === "agent" && event.status !== "running") {
      if (!this.seenAgent.has(event.nodeId)) {
        this.seenAgent.add(event.nodeId);
        this.agentCount += 1;
        if (event.status === "succeeded") this.agentsSucceeded += 1;
        else this.agentsFailed += 1;
        this.usage = addUsage(this.usage, event.usage);
      }
    } else if (event.kind === "judge" && event.status !== "running") {
      if (!this.seenJudge.has(event.nodeId)) {
        this.seenJudge.add(event.nodeId);
        this.judgeCount += 1;
        this.usage = addUsage(this.usage, event.usage);
      }
    } else if (event.kind === "run" && event.status !== "running") {
      this.status = event.status;
    }
  }

  flush(): Promise<void> {
    return this.downstream.flush?.() ?? Promise.resolve();
  }

  toSummary(runId: string): WorkflowRunSummary {
    return {
      runId,
      status:
        this.status === "running" ? (this.agentsFailed > 0 ? "failed" : "succeeded") : this.status,
      agentCount: this.agentCount,
      judgeCount: this.judgeCount,
      agentsSucceeded: this.agentsSucceeded,
      agentsFailed: this.agentsFailed,
      phases: [],
      usage: this.usage,
    };
  }
}

/** Sum two usage records; `estimated` is sticky-true if either side estimated. */
function addUsage(a: WorkflowAgentUsage, b: WorkflowAgentUsage): WorkflowAgentUsage {
  return {
    inputTokens: a.inputTokens + b.inputTokens,
    outputTokens: a.outputTokens + b.outputTokens,
    cacheReadTokens: a.cacheReadTokens + b.cacheReadTokens,
    cacheCreationTokens: a.cacheCreationTokens + b.cacheCreationTokens,
    costUsd: a.costUsd + b.costUsd,
    estimated: a.estimated || b.estimated,
  };
}

/** Order nodes by their monotonic seq (stable for equal seq). */
function orderBySeq(nodes: WorkflowNode[]): WorkflowNode[] {
  return [...nodes].sort((a, b) => a.seq - b.seq);
}

/**
 * Sanitize a node read back from the UNTRUSTED journal before it is re-emitted
 * on resume. Today only an agent node's `outputRef` is attacker-influenceable;
 * it is run through {@link sanitizeOutputRef} and dropped if it fails (rather
 * than surfacing a poisoned handle to a consumer). All other fields are inert
 * data the consumer already trusts from the live stream.
 */
function sanitizeReplayedNode(node: WorkflowNode): WorkflowNode {
  if (node.kind !== "agent" || node.outputRef === undefined) return node;
  const safe = sanitizeOutputRef(node.outputRef);
  if (safe === node.outputRef) return node;
  return { ...node, outputRef: safe };
}

/**
 * Replay a raw journal (newline-delimited {@link JournalRecord}s) into the live
 * node tree. Returns the latest state per node (last write wins by `seq`) and
 * the set of nodeIds that reached a terminal (non-running) agent/judge state —
 * those are the nodes a resume must NOT re-dispatch.
 *
 * Malformed lines are skipped rather than throwing: a partially-flushed final
 * line from a crash must not wedge a resume.
 */
export function replayJournal(raw: string): {
  latestByNode: Map<string, WorkflowNode>;
  terminalNodeIds: Set<string>;
  latestHeartbeatMs: number;
} {
  const latestByNode = new Map<string, WorkflowNode>();
  const terminalNodeIds = new Set<string>();
  let latestHeartbeatMs = 0;

  for (const line of raw.split("\n")) {
    const trimmed = line.trim();
    if (!trimmed) continue;
    let record: JournalRecord;
    try {
      record = JSON.parse(trimmed) as JournalRecord;
    } catch {
      // Torn final line from a crash — skip it.
      continue;
    }
    const event = record?.event;
    if (!event || typeof event.nodeId !== "string" || typeof event.seq !== "number") {
      continue;
    }
    if (typeof record.heartbeatMs === "number") {
      latestHeartbeatMs = Math.max(latestHeartbeatMs, record.heartbeatMs);
    }
    const prior = latestByNode.get(event.nodeId);
    if (!prior || event.seq >= prior.seq) {
      latestByNode.set(event.nodeId, event);
    }
    // An agent/judge in a non-running state is terminal → skip on resume.
    if ((event.kind === "agent" || event.kind === "judge") && event.status !== "running") {
      terminalNodeIds.add(event.nodeId);
    }
  }

  return { latestByNode, terminalNodeIds, latestHeartbeatMs };
}

/**
 * Whether a run's journal shows it is still alive (a fresh heartbeat) so a
 * stale-slot recovery sweep MUST NOT SIGTERM it. A run is "live" when its latest
 * journal heartbeat is within `staleAfterMs` of `nowMs` AND at least one node is
 * still running. This is the node-level liveness signal: a long fan-out that is
 * still making progress is not mistaken for a wedged slot.
 */
export function isRunLive(
  raw: string,
  nowMs: number,
  staleAfterMs: number
): { live: boolean; runningNodeCount: number; ageMs: number } {
  const { latestByNode, latestHeartbeatMs } = replayJournal(raw);
  let runningNodeCount = 0;
  for (const node of latestByNode.values()) {
    if (node.status === "running") runningNodeCount += 1;
  }
  const ageMs = nowMs - latestHeartbeatMs;
  const live = runningNodeCount > 0 && ageMs <= staleAfterMs;
  return { live, runningNodeCount, ageMs };
}

/**
 * Pre-validate that a spec's planned fan-out fits under the absolute ceiling and
 * the config caps WITHOUT mutating it. Returns the clamped ceiling and whether
 * the spec's planned count would still fit. Useful for a caller (router) that
 * wants to reject a too-large spec before constructing an executor.
 */
export function fitsUnderAbsoluteCeiling(
  spec: WorkflowSpec,
  config: Pick<ResolvedOrchestrationConfig, "max_agents" | "max_concurrency">
): { fits: boolean; planned: number; clampedMaxTotal: number } {
  const clamped = clampSpecCeiling(spec, config);
  const planned = plannedAgentCount(spec);
  return {
    fits: planned <= clamped.ceiling.maxTotal,
    planned,
    clampedMaxTotal: clamped.ceiling.maxTotal,
  };
}

/**
 * The default Node `fs/promises`-backed journal filesystem. Used in production;
 * tests inject an in-memory `JournalFs`.
 */
export async function createNodeJournalFs(): Promise<JournalFs> {
  const nodeFs = await import("node:fs/promises");
  return {
    async mkdir(dir: string): Promise<void> {
      await nodeFs.mkdir(dir, { recursive: true });
    },
    async appendFile(file: string, data: string): Promise<void> {
      await nodeFs.appendFile(file, data, "utf8");
    },
    async readFile(file: string): Promise<string | null> {
      try {
        return await nodeFs.readFile(file, "utf8");
      } catch (err) {
        if ((err as NodeJS.ErrnoException).code === "ENOENT") return null;
        throw err;
      }
    },
  };
}
