/**
 * Live cell executor for the model-eval runner (Issue #4174 follow-up).
 *
 * The real counterpart to the mock executor: it actually spawns a model against
 * a task inside the isolated workspace, runs the task's deterministic checks in
 * that workspace, and returns real telemetry (tokens/latency from the CLI,
 * pass/fail from the gates). This is what makes `--mode live` differentiate
 * model quality — weaker models fail gates that stronger models pass, so
 * correctness / verdict / attempts / cost / latency all diverge per model,
 * instead of the mock's fabricated-uniform pass.
 *
 * Two side-effecting boundaries are injected so the executor is fully
 * unit-testable without a real CLI, network, or toolchain:
 *   - `spawn` : one adapter CLI invocation (stdin prompt → stdout).
 *   - `exec`  : `npm install` + each check command in the workspace.
 *
 * Adapter parameterization (Issue #107): the executor is provider-neutral. The
 * cell's model resolves to a registry provider, which selects an
 * {@link EvalAdapterProfile} that owns the CLI flag shape, the reasoning wiring
 * (Claude's extended-thinking keywords in the prompt vs OpenAI's
 * `model_reasoning_effort` flag), and the stdout parsing. So a `codex` cell runs
 * `codex exec --json …` and a `claude` cell runs `claude --print …` through the
 * same code path — replacing the previously hardcoded Claude flag shape that
 * blocked every non-Claude adapter from the live lane.
 *
 * Live runs cost real API money and require ambient adapter auth; they are
 * never exercised in CI (mock is the CI default).
 *
 * @see docs/decisions/011-model-eval-system.md
 * @see docs/MODEL_EVALUATION.md — live-mode operation
 * @see ./evalAdapters.ts — per-adapter spawn profiles (#107)
 */

import { getModelDescriptor, providerForAdapter } from "./modelRegistry.js";
import type { CellExecution, EvalCellExecutor, EvalWorkspace } from "./modelEvalRunner.js";
import type {
  CheckCommand,
  EvalMatrixCell,
  EvalTask,
  GateResult,
  Provider,
  ReasoningLevel,
  TokenUsage,
} from "./modelEvalSchemas.js";
import {
  resolveEvalAdapterProfile,
  type EvalAdapterProfile,
  type SpawnTelemetry,
} from "./evalAdapters.js";
import { applyPromptVariant, resolveVariant, type PromptVariant } from "./promptVariants.js";
import { runJudgeWithReliabilityGuard, type EvalJudge } from "./qualityScorer.js";
import { defaultExec, type ExecFn } from "./worktreeWorkspace.js";

/** Result of one spawned adapter CLI invocation. */
export interface CliSpawnResult {
  stdout: string;
  stderr: string;
  code: number;
}

/**
 * Injectable spawn for one adapter CLI invocation. Tests pass a fake; production
 * uses {@link defaultCliSpawn}. Provider-neutral: the command + args are supplied
 * by the resolved {@link EvalAdapterProfile}, so this boundary only pipes a prompt
 * to a process and buffers its stdout. Command is passed first to mirror the
 * skill-eval `SpawnFn` convention.
 */
export type CliSpawnFn = (
  command: string,
  args: string[],
  prompt: string,
  cwd: string,
  timeoutMs: number
) => Promise<CliSpawnResult>;

/** 15 min per attempt — real coding tasks run tools and can be slow. */
const DEFAULT_SPAWN_TIMEOUT_MS = 900_000;

export interface LiveCellExecutorOptions {
  /**
   * Force a specific execution adapter (e.g. `codex`, `claude-headless`) instead
   * of deriving it from the cell model's registry provider. Accepts any layer's
   * adapter vocabulary; resolved through the registry's adapter→provider map.
   */
  adapter?: string;
  /**
   * Explicit CLI command override. When set it wins over the adapter profile's
   * env var / default (e.g. a pinned `claude` binary). Omit to let each profile
   * resolve its own command.
   */
  command?: string;
  /**
   * Max model invocations per cell (a bounded RALPH loop). After a non-green
   * attempt the failing check output is fed back for a fix. Default 1
   * (single-shot) — every extra attempt roughly multiplies real API cost.
   */
  maxAttempts?: number;
  /** Package manager for dependency install (default `npm`). */
  packageManager?: string;
  /** Install args (default `["install", "--no-audit", "--no-fund"]`). */
  installArgs?: string[];
  /** Skip the pre-run dependency install (e.g. fixtures with no deps). */
  skipInstall?: boolean;
  /** Per-invocation timeout for the model spawn. */
  timeoutMs?: number;
  /** Injected model spawn — tests pass a fake; production uses the default. */
  spawn?: CliSpawnFn;
  /** Injected shell boundary for install + checks (default: child_process). */
  exec?: ExecFn;
  /** Optional progress sink (per attempt). */
  onLog?: (message: string) => void;
  /**
   * Builds a subjective judge for a cell, bound to its (still-live) workspace.
   * When provided, the executor grades the produced work after the attempts loop
   * and returns the verdict for the runner to fold into the composite score.
   * Return `null` to skip judging a given cell. Omit to disable judging entirely
   * (deterministic gates only).
   */
  judgeFactory?: (
    task: EvalTask,
    cell: EvalMatrixCell,
    workspace: EvalWorkspace
  ) => EvalJudge | null;
  /** Judge samples for the reliability guard (default 3). Each sample costs. */
  judgeSamples?: number;
  /** Std-dev (points) above which a judged dimension is flagged low-confidence. */
  judgeVarianceThreshold?: number;
  /**
   * Loaded prompt-variant definitions keyed by name (#72). A cell whose
   * `prompt_variant` is not `baseline` has its overlay applied to the task
   * instruction before the prompt is assembled; a cell referencing a variant
   * missing from this map throws (harness config error → error verdict).
   */
  variants?: ReadonlyMap<string, PromptVariant>;
}

/**
 * `EvalCellExecutor` that runs a real model against a real task in the cell's
 * isolated workspace, then grades it with the task's deterministic checks.
 */
export class LiveCellExecutor implements EvalCellExecutor {
  private readonly adapter?: string;
  private readonly commandOverride?: string;
  private readonly maxAttempts: number;
  private readonly packageManager: string;
  private readonly installArgs: string[];
  private readonly skipInstall: boolean;
  private readonly timeoutMs: number;
  private readonly spawn: CliSpawnFn;
  private readonly exec: ExecFn;
  private readonly onLog?: (message: string) => void;
  private readonly judgeFactory?: LiveCellExecutorOptions["judgeFactory"];
  private readonly judgeSamples: number;
  private readonly judgeVarianceThreshold?: number;
  private readonly variants?: ReadonlyMap<string, PromptVariant>;

  constructor(options: LiveCellExecutorOptions = {}) {
    this.adapter = options.adapter;
    this.commandOverride = options.command;
    this.maxAttempts = Math.max(1, options.maxAttempts ?? 1);
    this.packageManager = options.packageManager ?? "npm";
    this.installArgs = options.installArgs ?? ["install", "--no-audit", "--no-fund"];
    this.skipInstall = options.skipInstall ?? false;
    this.timeoutMs = options.timeoutMs ?? DEFAULT_SPAWN_TIMEOUT_MS;
    this.spawn = options.spawn ?? defaultCliSpawn;
    this.exec = options.exec ?? defaultExec;
    this.onLog = options.onLog;
    this.judgeFactory = options.judgeFactory;
    this.judgeSamples = Math.max(1, options.judgeSamples ?? 3);
    this.judgeVarianceThreshold = options.judgeVarianceThreshold;
    this.variants = options.variants;
  }

  /**
   * Provider for a cell: an explicit `adapter` option wins (mapped through the
   * registry's adapter→provider table), else the model's own registry provider,
   * else anthropic (an unknown model id preserves the historical Claude default).
   */
  private resolveProvider(cell: EvalMatrixCell): Provider {
    if (this.adapter) return providerForAdapter(this.adapter);
    return getModelDescriptor(cell.model_id)?.provider ?? "anthropic";
  }

  async execute(
    task: EvalTask,
    cell: EvalMatrixCell,
    workspace: EvalWorkspace
  ): Promise<CellExecution> {
    const provider = this.resolveProvider(cell);
    const profile: EvalAdapterProfile = resolveEvalAdapterProfile(provider);
    const descriptor = getModelDescriptor(cell.model_id, provider);
    // Invoke by concrete version so the CLI resolves exactly what we price.
    const model = descriptor?.concrete_version ?? cell.model_id;
    const label = descriptor?.display_name ?? model;
    const command = profile.resolveCommand(this.commandOverride);
    // Reasoning is expressed exactly once per adapter — in the prompt (Claude)
    // or in the CLI args (Codex). The directive is "" for flag-based adapters.
    const reasoningDirective = profile.reasoningPromptDirective(cell.reasoning);
    const args = profile.buildArgs(model, cell.reasoning);
    const cwd = workspace.dir;

    // Prompt-variant overlay (#72): transform the instruction ONCE, up front,
    // so every attempt (initial + retries) runs under the same variant text.
    // Baseline resolves to the untouched on-disk instruction.
    const instruction = applyPromptVariant(
      task.instruction,
      resolveVariant(this.variants, cell.prompt_variant)
    );

    // 1. Prepare the toolchain so the task's build/test checks can run. A broken
    //    install is harness failure (→ error verdict via the runner), not a
    //    model quality signal, so we throw rather than record a fail.
    if (!this.skipInstall) {
      const install = await this.exec(this.packageManager, this.installArgs, { cwd });
      if (install.code !== 0) {
        throw new Error(
          `dependency install failed (exit ${install.code}): ${tail(install.stderr || install.stdout)}`
        );
      }
    }

    // 2. Bounded attempts loop: run the model, run checks, retry with the
    //    failing-check output as feedback until green or attempts exhausted.
    const tokens: TokenUsage = { input: 0, output: 0, cache_read: 0, cache_creation: 0 };
    let latency = 0;
    let gates: GateResult[] = [];
    let attempt = 0;
    let green = false;

    while (attempt < this.maxAttempts && !green) {
      attempt++;
      const prompt =
        attempt === 1
          ? initialPrompt(instruction, reasoningDirective)
          : retryPrompt(instruction, gates, reasoningDirective);
      this.onLog?.(`[${label}] ${task.id} attempt ${attempt}/${this.maxAttempts}`);

      const startedAt = Date.now();
      const res = await this.spawn(command, args, prompt, cwd, this.timeoutMs);
      const wallClockMs = Date.now() - startedAt;
      if (res.code !== 0) {
        throw new Error(
          `${profile.adapter} invocation failed (exit ${res.code}): ${tail(res.stderr) || "no stderr"}`
        );
      }
      const telemetry = profile.parseResult(res.stdout);
      if (!telemetry) {
        throw new Error(`could not parse ${profile.adapter} result for ${task.id} (${label})`);
      }
      accumulateTokens(tokens, telemetry.usage);
      // Prefer the CLI's own reported duration; fall back to measured wall time
      // for adapters (e.g. Codex) whose output carries no duration.
      latency += telemetry.durationMs > 0 ? telemetry.durationMs : wallClockMs;

      // Grade with the deterministic checks. A task with no checks falls back to
      // the model's own success signal.
      gates =
        task.checks.length > 0
          ? await runChecks(task.checks, cwd, this.exec)
          : [{ name: "completed", passed: !telemetry.isError }];
      green = gates.every((g) => g.passed);
    }

    // 3. Subjective grading (optional) — run the LLM judge while the workspace is
    //    still alive, N times through the reliability guard. Judge cost/latency is
    //    grading overhead and is intentionally NOT added to the model's telemetry.
    let judge: CellExecution["judge"];
    const grader = this.judgeFactory?.(task, cell, workspace) ?? null;
    if (grader) {
      this.onLog?.(`[${label}] ${task.id} judging (${this.judgeSamples}×)`);
      try {
        const { verdict, lowConfidence } = await runJudgeWithReliabilityGuard(grader, task.rubric, {
          samples: this.judgeSamples,
          varianceThreshold: this.judgeVarianceThreshold,
        });
        judge = { verdict, lowConfidence: [...lowConfidence] };
      } catch (err) {
        // A flaky grader must never void a valid implementation measurement — keep
        // the real gate results and fall back to deterministic-only scoring.
        this.onLog?.(
          `[${label}] ${task.id} judge skipped: ${err instanceof Error ? err.message : String(err)}`
        );
      }
    }

    return {
      verdict: green ? "pass" : "fail",
      tokens,
      latency_ms: Math.round(latency),
      attempts_to_green: attempt,
      gate_results: gates,
      model_version_label: label,
      stage: pickStage(task),
      judge,
    };
  }
}

// Both prompt builders take the (possibly variant-overlaid, #72) instruction
// text rather than the task, so the overlay is applied in exactly one place.
// The trailing `reasoningDirective` is the adapter's prompt-borne reasoning
// budget (empty when the adapter steers reasoning through a CLI flag instead).
function initialPrompt(instruction: string, reasoningDirective: string): string {
  return (
    [
      "You are implementing a software task in the current working directory.",
      "Make all necessary changes directly in these files. When you are done, the",
      "project must build and every test must pass. Do not weaken or delete tests",
      "to make them pass.",
      "",
      "Task:",
      instruction,
    ].join("\n") + reasoningDirective
  );
}

function retryPrompt(instruction: string, gates: GateResult[], reasoningDirective: string): string {
  const failing = gates
    .filter((g) => !g.passed)
    .map((g) => `- ${g.name}: ${g.detail ?? "failed"}`)
    .join("\n");
  return (
    [
      "Your previous attempt did not pass all checks. Fix the code in the current",
      "working directory so every check passes. Do not weaken or delete the",
      "checks or tests.",
      "",
      "Failing checks:",
      failing,
      "",
      "Original task:",
      instruction,
    ].join("\n") + reasoningDirective
  );
}

/** Run each check in the workspace; a gate passes when its exit code matches. */
async function runChecks(checks: CheckCommand[], cwd: string, exec: ExecFn): Promise<GateResult[]> {
  const results: GateResult[] = [];
  for (const check of checks) {
    const r = await exec("bash", ["-c", check.command], { cwd });
    const passed = r.code === check.expect_exit_code;
    results.push({
      name: check.name,
      passed,
      detail: passed ? undefined : tail(r.stderr || r.stdout) || `exit ${r.code}`,
    });
  }
  return results;
}

/** Prefer the primary implementation stage for the recorded `stage` field. */
function pickStage(task: EvalTask): CellExecution["stage"] {
  return task.target_stages.includes("feature-dev") ? "feature-dev" : task.target_stages[0];
}

function accumulateTokens(acc: TokenUsage, usage: TokenUsage): void {
  acc.input += usage.input;
  acc.output += usage.output;
  acc.cache_read += usage.cache_read;
  acc.cache_creation += usage.cache_creation;
}

/** Keep the trailing `max` chars of a (trimmed) string for compact gate detail. */
function tail(s: string, max = 600): string {
  const t = s.trim();
  return t.length <= max ? t : "…" + t.slice(-max);
}

/**
 * Default spawn: pipe the prompt to the adapter CLI over stdin and buffer stdout.
 * Unlike the skill-eval runner it resolves with the exit code (never rejects on a
 * non-zero exit) so the executor decides how to classify the outcome; only a
 * timeout or spawn error rejects.
 */
export const defaultCliSpawn: CliSpawnFn = (command, args, prompt, cwd, timeoutMs) =>
  import("node:child_process").then(
    ({ spawn }) =>
      new Promise<CliSpawnResult>((resolvePromise, reject) => {
        const child = spawn(command, args, { cwd, stdio: ["pipe", "pipe", "pipe"] });
        let stdout = "";
        let stderr = "";
        let settled = false;

        const timer = setTimeout(() => {
          if (settled) return;
          settled = true;
          child.kill("SIGTERM");
          reject(new Error(`${command} invocation timed out after ${timeoutMs}ms`));
        }, timeoutMs);

        child.stdout?.on("data", (chunk) => (stdout += chunk.toString()));
        child.stderr?.on("data", (chunk) => (stderr += chunk.toString()));
        child.on("error", (err) => {
          if (settled) return;
          settled = true;
          clearTimeout(timer);
          reject(err);
        });
        child.on("close", (code) => {
          if (settled) return;
          settled = true;
          clearTimeout(timer);
          resolvePromise({ stdout, stderr, code: code ?? 1 });
        });

        child.stdin?.write(prompt);
        child.stdin?.end();
      })
  );
