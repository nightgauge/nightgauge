/**
 * Claude native Dynamic Workflows ("ultracode") offload backend (#3910).
 *
 * This is the shared core behind the optional `runWorkflow?()` hook (declared on
 * `ICliAdapter` in #3902) for BOTH the `ClaudeSdkAdapter` (Agent SDK Dynamic
 * Workflows) and the `ClaudeHeadlessAdapter` (`claude -p` ultracode mode). The
 * engine — not the adapter — owns orchestration: when an adapter offloads here,
 * the run still emits the canonical `schemaVersion-4` `WorkflowEvent` node tree
 * through the injected `WorkflowEventSink`, exactly as the portable
 * `SdkFanoutRunner` floor does (portable-first policy — the native path MATCHES
 * the floor, never the reverse).
 *
 * RESEARCH PREVIEW. The concrete native Dynamic Workflows execution API
 * (`agent()` / `parallel()` / `pipeline()` / `phase()` / `judge()` / `budget()`
 * for the Agent SDK; `--effort ultracode` / `--ultracode` for the CLI) is gated
 * behind Claude CLI/SDK **>= v2.1.154** and is not present in the SDK version
 * this repo currently pins. Rather than fabricate a fake API, this module
 * implements the full *structure* — the version gate, the sink-emitting driver,
 * the typed downgrade signal, and the native→`SubAgentNode.usage` mapping — and
 * marks the single point where the real native call is wired in with
 * `NATIVE INTEGRATION POINT`. Until that floor ships in the installed binary,
 * `runClaudeNativeWorkflow` throws {@link NativeWorkflowUnavailableError} so the
 * engine deterministically downgrades to `SdkFanoutRunner` — it NEVER silently
 * produces wrong results.
 *
 * @see docs/WORKFLOW_ORCHESTRATION.md § native backend
 * @see Issue #3910 — Claude native runWorkflow
 * @see Issue #3908 — WorkflowExecutor (reuses {@link supportsNativeWorkflow})
 */

import type { PreflightCommandRunner } from "../codexPreflight.js";
import {
  WORKFLOW_SCHEMA_VERSION,
  zeroUsage,
  type WorkflowAgentUsage,
  type WorkflowNodeStatus,
} from "../workflow/WorkflowEvent.js";
import { validateWorkflowSpec, type WorkflowSpec } from "../workflow/WorkflowSpec.js";
import { createSeqCounter, type WorkflowEventSink } from "../workflow/WorkflowEventSink.js";

/**
 * Minimum Claude CLI / Agent SDK version that exposes native Dynamic Workflows
 * ("ultracode"). Below this floor the native path is unavailable and the engine
 * downgrades to the portable `SdkFanoutRunner` floor. This is a HARD gate, not a
 * warning: engaging native workflows on an older binary would silently produce a
 * non-canonical (or empty) event tree.
 */
export const MIN_NATIVE_WORKFLOW_VERSION = "2.1.154";

/**
 * The ultracode keyword changed from `workflow` to `ultracode` at CLI v2.1.160.
 * Both keywords are accepted by the driver so a binary in the [2.1.154, 2.1.160)
 * window still engages the native path. The floor below which neither exists is
 * {@link MIN_NATIVE_WORKFLOW_VERSION}.
 */
export const ULTRACODE_KEYWORD_RENAME_VERSION = "2.1.160";

/** Env kill-switch honored by the native path (mirrors the orchestration config flag). */
export const DISABLE_WORKFLOWS_ENV = "CLAUDE_CODE_DISABLE_WORKFLOWS";

/**
 * Parse a dotted numeric version (e.g. "2.1.154", "v2.1.154-beta.1") into its
 * numeric components. Non-numeric leading `v` and any pre-release / build
 * suffix are ignored. Returns `null` when no `major.minor` can be read.
 */
export function parseVersion(raw: string | undefined | null): number[] | null {
  if (!raw) return null;
  const match = raw.trim().match(/(\d+)\.(\d+)(?:\.(\d+))?/);
  if (!match) return null;
  return [Number(match[1]), Number(match[2]), match[3] ? Number(match[3]) : 0];
}

/** Compare two parsed version component arrays. Returns <0, 0, or >0. */
function compareParsed(a: number[], b: number[]): number {
  for (let i = 0; i < Math.max(a.length, b.length); i++) {
    const na = a[i] ?? 0;
    const nb = b[i] ?? 0;
    if (na !== nb) return na - nb;
  }
  return 0;
}

/**
 * Exported predicate the `WorkflowExecutor` (#3908) reuses: is `version` at or
 * above the native-workflow floor (>= {@link MIN_NATIVE_WORKFLOW_VERSION})? An
 * unparseable / missing version is treated as BELOW the floor (fail-closed) so
 * an unknown binary always downgrades to the portable floor rather than guessing
 * it supports native workflows.
 *
 * @example
 *   supportsNativeWorkflow("2.1.154") // true  (exact floor)
 *   supportsNativeWorkflow("2.1.200") // true
 *   supportsNativeWorkflow("2.1.153") // false (one patch below)
 *   supportsNativeWorkflow(undefined) // false (fail-closed)
 */
export function supportsNativeWorkflow(version: string | undefined | null): boolean {
  const parsed = parseVersion(version);
  if (!parsed) return false;
  return compareParsed(parsed, parseVersion(MIN_NATIVE_WORKFLOW_VERSION)!) >= 0;
}

/**
 * Whether the binary at `version` uses the post-rename `ultracode` keyword
 * (>= {@link ULTRACODE_KEYWORD_RENAME_VERSION}) vs. the legacy `workflow`
 * keyword. Callers below the floor never reach this (gated by
 * {@link supportsNativeWorkflow}).
 */
export function ultracodeKeyword(version: string | undefined | null): "ultracode" | "workflow" {
  const parsed = parseVersion(version);
  if (!parsed) return "workflow";
  return compareParsed(parsed, parseVersion(ULTRACODE_KEYWORD_RENAME_VERSION)!) >= 0
    ? "ultracode"
    : "workflow";
}

/** Why the native workflow path was unavailable (drives the downgrade message). */
export type NativeWorkflowUnavailableReason =
  | "version-below-floor"
  | "version-undetectable"
  | "disabled-by-env"
  | "disabled-by-config"
  | "api-surface-unavailable";

/**
 * Typed downgrade signal. `runWorkflow` throws this (rather than returning a
 * partial tree) so the engine deterministically falls back to the
 * `SdkFanoutRunner` floor. It NEVER signals "ran but produced nothing" — every
 * instance means "did not run; use the floor".
 */
export class NativeWorkflowUnavailableError extends Error {
  constructor(
    public readonly reason: NativeWorkflowUnavailableReason,
    message: string,
    /** Detected version, when one was readable (for diagnostics). */
    public readonly detectedVersion?: string
  ) {
    super(message);
    this.name = "NativeWorkflowUnavailableError";
    if (Error.captureStackTrace) {
      Error.captureStackTrace(this, NativeWorkflowUnavailableError);
    }
  }
}

/** True when the env kill-switch forces the native path off. */
export function isNativeWorkflowDisabledByEnv(env: NodeJS.ProcessEnv = process.env): boolean {
  const raw = env[DISABLE_WORKFLOWS_ENV];
  if (raw === undefined) return false;
  const lower = raw.trim().toLowerCase();
  return lower === "true" || lower === "1" || lower === "yes";
}

/**
 * Detect the Claude CLI version via the injected preflight runner (`claude
 * --version`). Returns the raw version string or `null` when the runner is
 * absent / fails / prints no parseable version. The headless adapter uses this;
 * the SDK adapter reads the SDK package version instead.
 */
export async function detectClaudeCliVersion(
  runner: PreflightCommandRunner | undefined,
  cwd: string,
  command = "claude"
): Promise<string | null> {
  if (!runner) return null;
  try {
    const result = await runner(command, ["--version"], cwd);
    if (result.code !== 0) return null;
    const parsed = parseVersion(result.stdout);
    return parsed ? `${parsed[0]}.${parsed[1]}.${parsed[2]}` : null;
  } catch {
    return null;
  }
}

/**
 * Detect the installed Claude Agent SDK version from its package manifest. Used
 * by the SDK adapter (no CLI process to probe). Returns `null` when the version
 * cannot be read.
 *
 * Note: the Agent SDK pins its own `0.3.x` line; the **native Dynamic Workflows
 * floor** (>= v2.1.154) tracks the Claude **CLI/runtime** the SDK drives, which
 * the SDK reports separately once that surface ships. Until then this resolves
 * below the floor and the engine downgrades — by design.
 */
export async function detectClaudeSdkVersion(): Promise<string | null> {
  try {
    // The Agent SDK is ESM with an `exports` map that does NOT expose
    // `./package.json`, so resolve its main entry (path-only resolution works
    // for an ESM main from CJS) and walk up to the owning manifest. The SDK
    // builds to CommonJS, so the ambient `require` is anchored to this module.
    const { dirname, join } = await import("node:path");
    const { existsSync, readFileSync } = await import("node:fs");
    let dir = dirname(require.resolve("@anthropic-ai/claude-agent-sdk"));
    for (let i = 0; i < 6; i++) {
      const manifest = join(dir, "package.json");
      if (existsSync(manifest)) {
        const pkg = JSON.parse(readFileSync(manifest, "utf8")) as {
          name?: string;
          version?: string;
        };
        if (pkg.name === "@anthropic-ai/claude-agent-sdk") {
          return pkg.version ?? null;
        }
      }
      const parent = dirname(dir);
      if (parent === dir) break;
      dir = parent;
    }
    return null;
  } catch {
    return null;
  }
}

/** How the offload was reached (which Claude surface drives the native call). */
export type NativeWorkflowSurface = "agent-sdk" | "cli-ultracode";

/**
 * Result of the non-throwing native-workflow readiness preflight. `validateAuth`
 * calls this to record whether the adapter can engage native workflows (and why
 * not, when it can't) WITHOUT failing normal auth — a stale workflow version must
 * never block ordinary (non-orchestrated) execution. The same readiness drives
 * `runWorkflow`'s downgrade.
 */
export interface NativeWorkflowReadiness {
  /** True only when the version floor is met AND no kill-switch is set. */
  ready: boolean;
  /** Detected Claude CLI/SDK version, or `null` when undetectable. */
  detectedVersion: string | null;
  /** Why native workflows are unavailable; `undefined` when `ready`. */
  reason?: NativeWorkflowUnavailableReason;
  /** Human-readable explanation for logs / downgrade messages. */
  detail: string;
}

/**
 * Non-throwing native-workflow preflight: detect the version, apply the env +
 * config kill-switches, and apply the >= {@link MIN_NATIVE_WORKFLOW_VERSION}
 * floor — returning a readiness verdict instead of throwing. Adapters call this
 * from `validateAuth` so a stale workflow version downgrades the orchestration
 * mode to `sdk-fanout` rather than failing auth.
 *
 * @param detectedVersion already-detected version (caller probes via
 *   {@link detectClaudeCliVersion} / {@link detectClaudeSdkVersion})
 */
export function preflightNativeWorkflow(
  detectedVersion: string | null,
  options: { configDisabled?: boolean; env?: NodeJS.ProcessEnv } = {}
): NativeWorkflowReadiness {
  const env = options.env ?? process.env;

  if (isNativeWorkflowDisabledByEnv(env)) {
    return {
      ready: false,
      detectedVersion,
      reason: "disabled-by-env",
      detail: `${DISABLE_WORKFLOWS_ENV} set — native workflows off; engine uses SdkFanoutRunner`,
    };
  }
  if (options.configDisabled) {
    return {
      ready: false,
      detectedVersion,
      reason: "disabled-by-config",
      detail: "orchestration config disabled — native workflows off; engine uses SdkFanoutRunner",
    };
  }
  if (!detectedVersion) {
    return {
      ready: false,
      detectedVersion,
      reason: "version-undetectable",
      detail: `could not detect Claude version — cannot confirm native floor >= ${MIN_NATIVE_WORKFLOW_VERSION}; engine uses SdkFanoutRunner`,
    };
  }
  if (!supportsNativeWorkflow(detectedVersion)) {
    return {
      ready: false,
      detectedVersion,
      reason: "version-below-floor",
      detail: `Claude ${detectedVersion} below native floor ${MIN_NATIVE_WORKFLOW_VERSION} — engine uses SdkFanoutRunner`,
    };
  }
  return {
    ready: true,
    detectedVersion,
    detail: `Claude ${detectedVersion} >= ${MIN_NATIVE_WORKFLOW_VERSION} — native workflows available (research preview)`,
  };
}

/** Options for the shared native workflow driver. */
export interface ClaudeNativeWorkflowOptions {
  /** Which native surface to drive (Agent SDK builders vs. `claude -p` ultracode). */
  surface: NativeWorkflowSurface;
  /**
   * The detected Claude CLI/runtime version. The driver re-asserts the floor
   * (defense in depth — adapters also gate in `validateAuth`) and selects the
   * ultracode keyword from it.
   */
  detectedVersion: string | null;
  /** Process env (override for tests). */
  env?: NodeJS.ProcessEnv;
  /**
   * Whether the resolved orchestration config disabled the engine. When `true`
   * the driver downgrades with reason `disabled-by-config` before any native
   * call. The adapter passes the resolved value (never the raw optional).
   */
  configDisabled?: boolean;
}

/** ISO-8601 `now` helper, isolated so tests reading timestamps stay deterministic-ish. */
function nowIso(): string {
  return new Date().toISOString();
}

/**
 * Map whatever native progress/usage Claude reports for one agent onto the
 * canonical {@link WorkflowAgentUsage}. REQUIRED on every `SubAgentNode`. When
 * the native surface reports real token/cost figures they are carried through
 * with `estimated:false`; when a field is absent it is zero-filled and the whole
 * record is flagged `estimated:true` so the cost UI never shows a fabricated
 * exact number. This is the contract the acmeapp "zeros + estimated" gap
 * (#3914) depends on.
 */
export function mapNativeUsage(native: NativeAgentUsageReport | undefined): WorkflowAgentUsage {
  if (!native) {
    // No native usage reported at all → zeroed + estimated (never silently exact).
    return zeroUsage(true);
  }
  const hasAny =
    native.inputTokens !== undefined ||
    native.outputTokens !== undefined ||
    native.costUsd !== undefined;
  return {
    inputTokens: native.inputTokens ?? 0,
    outputTokens: native.outputTokens ?? 0,
    cacheReadTokens: native.cacheReadTokens ?? 0,
    cacheCreationTokens: native.cacheCreationTokens ?? 0,
    costUsd: native.costUsd ?? 0,
    // estimated:false ONLY when the native surface gave us real numbers AND
    // explicitly did not mark them as partial/estimated.
    estimated: native.estimated ?? !hasAny,
  };
}

/**
 * The raw per-agent usage a native workflow surface reports. This is the shape
 * the `NATIVE INTEGRATION POINT` below would populate from Claude's Agent SDK
 * `ModelUsage` (`inputTokens` / `outputTokens` / `costUSD`) or the CLI's
 * `result` JSON. Every field is optional because the research-preview surface
 * may report a subset; {@link mapNativeUsage} normalizes it.
 */
export interface NativeAgentUsageReport {
  inputTokens?: number;
  outputTokens?: number;
  cacheReadTokens?: number;
  cacheCreationTokens?: number;
  costUsd?: number;
  /** Provider-asserted estimate flag (overrides the derived one when present). */
  estimated?: boolean;
}

/**
 * Drive a `WorkflowSpec` against Claude's native Dynamic Workflows, emitting the
 * canonical node tree through `sink`. Shared by both Claude adapters.
 *
 * Order of operations (all BEFORE any native call, so a downgrade costs nothing):
 *   1. validate the spec (throws on an invalid/over-ceiling plan, like the floor);
 *   2. re-assert the disable kill-switch (env + config);
 *   3. re-assert the version floor (defense in depth);
 *   4. NATIVE INTEGRATION POINT — drive the real native surface, folding its
 *      progress onto the canonical tree. Until that surface ships in the pinned
 *      binary, throw {@link NativeWorkflowUnavailableError} so the engine
 *      downgrades to `SdkFanoutRunner`.
 *
 * @throws {NativeWorkflowUnavailableError} whenever the native path cannot run —
 *   the engine catches this and falls back to the portable floor.
 * @throws {Error} on an invalid spec (same contract as `runSdkFanout`).
 */
export async function runClaudeNativeWorkflow(
  spec: WorkflowSpec,
  sink: WorkflowEventSink,
  options: ClaudeNativeWorkflowOptions
): Promise<void> {
  const env = options.env ?? process.env;

  // (1) Validate up front — never engage native execution on a bad plan.
  const problems = validateWorkflowSpec(spec);
  if (problems.length > 0) {
    throw new Error(`invalid WorkflowSpec: ${problems.join("; ")}`);
  }

  // (2) Kill-switch: env wins, then resolved config.
  if (isNativeWorkflowDisabledByEnv(env)) {
    throw new NativeWorkflowUnavailableError(
      "disabled-by-env",
      `native Claude workflows disabled by ${DISABLE_WORKFLOWS_ENV} — downgrading to SdkFanoutRunner`,
      options.detectedVersion ?? undefined
    );
  }
  if (options.configDisabled) {
    throw new NativeWorkflowUnavailableError(
      "disabled-by-config",
      "native Claude workflows disabled by orchestration config — downgrading to SdkFanoutRunner",
      options.detectedVersion ?? undefined
    );
  }

  // (3) Version floor (defense in depth — the adapter also gates in validateAuth).
  if (!options.detectedVersion) {
    throw new NativeWorkflowUnavailableError(
      "version-undetectable",
      `could not detect Claude ${options.surface === "agent-sdk" ? "Agent SDK" : "CLI"} version ` +
        `— cannot confirm native workflow floor >= ${MIN_NATIVE_WORKFLOW_VERSION}; downgrading to SdkFanoutRunner`
    );
  }
  if (!supportsNativeWorkflow(options.detectedVersion)) {
    throw new NativeWorkflowUnavailableError(
      "version-below-floor",
      `Claude ${options.detectedVersion} is below the native workflow floor ` +
        `${MIN_NATIVE_WORKFLOW_VERSION} — downgrading to SdkFanoutRunner`,
      options.detectedVersion
    );
  }

  // (4) NATIVE INTEGRATION POINT.
  // ---------------------------------------------------------------------------
  // The version gate passed and the kill-switches are clear. THE single place
  // where the concrete native Dynamic Workflows surface is driven:
  //
  //   surface === "agent-sdk":
  //     const { agent, parallel, pipeline, phase, judge, budget } =
  //       await import("@anthropic-ai/claude-agent-sdk");
  //     // build the workflow from `spec.phases` (parallel(agents) per phase,
  //     // judge(...) per phase judge, budget(spec.budgetUsd) on the run), run it,
  //     // and for every native progress event emit a node via `emit*` below,
  //     // mapping native ModelUsage → mapNativeUsage(...).
  //
  //   surface === "cli-ultracode":
  //     // spawn `claude -p --effort <keyword> --output-format stream-json ...`
  //     // where <keyword> = ultracodeKeyword(options.detectedVersion), parse the
  //     // stream-json node/usage events and emit via `emit*` below.
  //
  // The emit* helpers below already produce a well-formed run/phase/agent tree
  // (and are exercised by tests via a manual native progress source). They are
  // what the real surface plugs into; only the bracketed lines above are
  // missing — the SDK pinned here (Agent SDK 0.3.x) does not yet export the
  // Dynamic Workflows builders or accept `--effort ultracode`. We do NOT
  // fabricate them: downgrade deterministically instead.
  // ---------------------------------------------------------------------------
  throw new NativeWorkflowUnavailableError(
    "api-surface-unavailable",
    `native Dynamic Workflows API surface is not available in the installed Claude ` +
      `${options.surface === "agent-sdk" ? "Agent SDK" : "CLI"} (research preview) — ` +
      `downgrading to SdkFanoutRunner`,
    options.detectedVersion
  );
}

/**
 * A native progress event the integration point folds onto the canonical tree.
 * Decoupling "what the native surface reports" from "what the sink emits" lets
 * the tree-shaping logic be unit-tested with a fake progress source while the
 * real surface is still a research preview. The engine only ever sees the
 * canonical `WorkflowEvent` tree these produce.
 */
export type NativeProgressEvent =
  | { kind: "phase-start"; phaseIndex: number }
  | { kind: "phase-end"; phaseIndex: number; failed: boolean }
  | { kind: "agent-start"; phaseIndex: number; agentIndex: number }
  | {
      kind: "agent-end";
      phaseIndex: number;
      agentIndex: number;
      failed: boolean;
      usage?: NativeAgentUsageReport;
      model?: string;
      outputRef?: string;
    };

/**
 * Emit a canonical run/phase/agent tree onto `sink` from a stream of native
 * progress events. This is the sink-shaping half of the {@link
 * runClaudeNativeWorkflow} integration point, extracted so it can be driven by
 * the real native surface OR by a fake progress source in tests — proving the
 * adapter emits a well-formed tree without depending on the (research-preview)
 * native API. The emitted tree matches `SdkFanoutRunner`'s shape exactly, except
 * `WorkflowRun.backend` is `"native-workflow"`.
 *
 * @returns the run's terminal status, for the caller's summary.
 */
export function emitNativeWorkflowTree(
  spec: WorkflowSpec,
  sink: WorkflowEventSink,
  progress: Iterable<NativeProgressEvent>,
  detectedVersion: string | null
): WorkflowNodeStatus {
  const nextSeq = createSeqCounter();
  const runId = spec.runId;
  const runNodeId = `run:${runId}`;
  const startedAt = nowIso();

  // Root WorkflowRun (running) — backend marks this as the native offload.
  sink.emit({
    schemaVersion: WORKFLOW_SCHEMA_VERSION,
    kind: "run",
    nodeId: runNodeId,
    parentId: null,
    seq: nextSeq(),
    ts: startedAt,
    status: "running",
    runId,
    issueNumber: spec.issueNumber,
    stage: spec.stage,
    backend: "native-workflow",
    startedAt,
    label: detectedVersion ? `claude ${detectedVersion} (native)` : "claude (native)",
  });

  const totalPhases = spec.phases.length;
  let anyAgentFailed = false;

  const phaseNodeId = (pIndex: number): string => `phase:${runId}:${pIndex}`;
  const agentNodeId = (pIndex: number, aIndex: number): string =>
    `agent:${runId}:${pIndex}:${aIndex}`;

  for (const ev of progress) {
    switch (ev.kind) {
      case "phase-start": {
        const phase = spec.phases[ev.phaseIndex];
        sink.emit({
          schemaVersion: WORKFLOW_SCHEMA_VERSION,
          kind: "phase",
          nodeId: phaseNodeId(ev.phaseIndex),
          parentId: runNodeId,
          seq: nextSeq(),
          ts: nowIso(),
          status: "running",
          name: phase.name,
          index: ev.phaseIndex,
          total: totalPhases,
          label: phase.name,
        });
        break;
      }
      case "agent-start": {
        const phase = spec.phases[ev.phaseIndex];
        const agent = phase.agents[ev.agentIndex];
        sink.emit({
          schemaVersion: WORKFLOW_SCHEMA_VERSION,
          kind: "agent",
          nodeId: agentNodeId(ev.phaseIndex, ev.agentIndex),
          parentId: phaseNodeId(ev.phaseIndex),
          seq: nextSeq(),
          ts: nowIso(),
          status: "running",
          agentId: agent.agentId,
          role: agent.role,
          provider: agent.provider ?? "claude",
          model: agent.model,
          usage: zeroUsage(),
          label: agent.agentId,
        });
        break;
      }
      case "agent-end": {
        const phase = spec.phases[ev.phaseIndex];
        const agent = phase.agents[ev.agentIndex];
        if (ev.failed) anyAgentFailed = true;
        const status: WorkflowNodeStatus = ev.failed ? "failed" : "succeeded";
        sink.emit({
          schemaVersion: WORKFLOW_SCHEMA_VERSION,
          kind: "agent",
          nodeId: agentNodeId(ev.phaseIndex, ev.agentIndex),
          parentId: phaseNodeId(ev.phaseIndex),
          seq: nextSeq(),
          ts: nowIso(),
          status,
          agentId: agent.agentId,
          role: agent.role,
          provider: agent.provider ?? "claude",
          model: ev.model ?? agent.model,
          // REQUIRED native usage, mapped from the real Claude report.
          usage: mapNativeUsage(ev.usage),
          terminalKind: ev.failed ? "error" : "success",
          outputRef: ev.outputRef,
          label: agent.agentId,
        });
        break;
      }
      case "phase-end": {
        const phase = spec.phases[ev.phaseIndex];
        sink.emit({
          schemaVersion: WORKFLOW_SCHEMA_VERSION,
          kind: "phase",
          nodeId: phaseNodeId(ev.phaseIndex),
          parentId: runNodeId,
          seq: nextSeq(),
          ts: nowIso(),
          status: ev.failed ? "failed" : "succeeded",
          name: phase.name,
          index: ev.phaseIndex,
          total: totalPhases,
          label: phase.name,
        });
        break;
      }
    }
  }

  const runStatus: WorkflowNodeStatus = anyAgentFailed ? "failed" : "succeeded";
  const finishedAt = nowIso();
  sink.emit({
    schemaVersion: WORKFLOW_SCHEMA_VERSION,
    kind: "run",
    nodeId: runNodeId,
    parentId: null,
    seq: nextSeq(),
    ts: finishedAt,
    status: runStatus,
    runId,
    issueNumber: spec.issueNumber,
    stage: spec.stage,
    backend: "native-workflow",
    startedAt,
    finishedAt,
    label: detectedVersion ? `claude ${detectedVersion} (native)` : "claude (native)",
  });

  return runStatus;
}
