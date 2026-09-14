/**
 * Shared CLI query helper - Creates SDKQueryFunction for CLI-based adapters.
 *
 * Extracted from adapterQuery.ts to avoid duplication across adapter implementations.
 * Supports multiple prompt delivery modes (stdin, positional argument) for
 * different CLI tools.
 *
 * @see Issue #627 - Extract ICliAdapter interface & unify types
 * @see Issue #1051 - Add positional prompt delivery and Gemini stream-json support
 * @see Issue #1637 - The opencode branch: process group, curated env, stream classification
 */

import { spawn } from "node:child_process";
import { readFile, unlink } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join, resolve as resolvePath } from "node:path";
import { randomBytes, randomUUID } from "node:crypto";
import type {
  SDKMessage,
  SDKQueryFunction,
  SDKQueryOptions,
} from "../../orchestrator/StageExecutor.js";
import type { NightgaugeAdapter } from "./ICliAdapter.js";
import type { OpenCodeRunConfig } from "./OpenCodeAdapter.js";
import { applyCodexSandboxProfile } from "./codexSandbox.js";
import {
  OPENCODE_CONFIG_CONTENT_ENV,
  OPENCODE_ISOLATION_XDG,
  OPENCODE_SERVER_PASSWORD_ENV,
  curateChildEnv,
  curateOpenCodeChildEnv,
} from "./childEnv.js";
import { AdapterError } from "./errors.js";
import { openCodeProviderEnv } from "./opencodeCatalog.js";
import { classifyOpenCodeRun, openCodeRedactor, type OpenCodeHelper } from "./opencodeStream.js";
import {
  summarizeCodexJsonOutput,
  summarizeGeminiStreamJsonOutput,
  summarizeCopilotOutput,
  type CodexJsonSummary,
  type CodexJsonUsage,
  type GeminiStreamJsonUsage,
  type CopilotOutputSummary,
} from "../adapterQuery.js";
import { summarizeGrokStream } from "./grokStream.js";
import { writeFile } from "node:fs/promises";

interface CommandResult {
  code: number;
  stdout: string;
  stderr: string;
}

export async function runCliCommand(
  command: string,
  args: string[],
  prompt: string,
  cwd: string,
  env: NodeJS.ProcessEnv
): Promise<CommandResult> {
  return new Promise((resolve, reject) => {
    const child = spawn(command, args, {
      cwd,
      env,
      stdio: "pipe",
    });

    let stdout = "";
    let stderr = "";

    child.stdout.on("data", (chunk) => {
      stdout += String(chunk);
    });

    child.stderr.on("data", (chunk) => {
      stderr += String(chunk);
    });

    child.on("error", reject);

    child.on("close", (code) => {
      resolve({ code: code ?? 1, stdout, stderr });
    });

    child.stdin.write(prompt);
    child.stdin.end();
  });
}

/**
 * Parse CLI args from an environment variable, falling back to defaults.
 */
export function parseCliArgs(value: string | undefined, fallback: string[]): string[] {
  if (!value) {
    return fallback;
  }

  const parsed = value
    .split(" ")
    .map((part) => part.trim())
    .filter((part) => part.length > 0);

  return parsed.length > 0 ? parsed : fallback;
}

/**
 * Prompt delivery mode for CLI-based adapters.
 *
 * - `'stdin'` (default): Writes the prompt to the child process stdin.
 *   Used by Claude Headless and Codex adapters.
 * - `'positional'`: Prepends the prompt as a positional argument to the args
 *   array and sends empty stdin. Used by Gemini CLI which accepts prompts as
 *   positional arguments (`gemini "prompt" --output-format stream-json`).
 */
export type PromptDelivery = "stdin" | "positional" | "prompt-file";

/**
 * Select the Codex output text from either the --output-last-message file
 * (preferred) or the JSONL summary displayText (fallback).
 *
 * The --output-last-message file contains only the final agent message text
 * and is more reliable than JSONL extraction. Falls back to the JSONL summary
 * when the file is missing or empty (e.g. Codex exited early).
 */
export function selectCodexOutput(
  fileContent: string | undefined,
  jsonlSummary: CodexJsonSummary
): string {
  if (fileContent !== undefined && fileContent.trim().length > 0) {
    return fileContent.trim();
  }
  return jsonlSummary.displayText;
}

/**
 * Create an SDKQueryFunction that spawns a CLI process.
 */
export function createCliQueryFn(options: {
  command: string;
  args: string[];
  adapter: NightgaugeAdapter;
  promptDelivery?: PromptDelivery;
  /** The opencode adapter's run: required for, and only read by, `adapter: "opencode"`. */
  openCode?: OpenCodeQueryContext;
}): SDKQueryFunction {
  const delivery = options.promptDelivery ?? "stdin";

  return async function* query(queryOptions): AsyncGenerator<SDKMessage> {
    if (options.adapter === "opencode") {
      if (!options.openCode) {
        throw new Error(
          "the opencode query needs its run context: build it with OpenCodeAdapter.createQueryFunction"
        );
      }
      yield* openCodeQuery(options.command, options.args, options.openCode, queryOptions);
      return;
    }
    const cwd = queryOptions.options?.cwd ?? process.cwd();
    // Least-privilege (#4094, F4): the spawned CLI — and every fan-out worker
    // routed through here — receives only the curated allowlist, never the full
    // parent process.env. A compromised/prompt-injected sub-agent cannot read a
    // secret it doesn't need.
    const env = curateChildEnv(process.env);
    const resumeSessionId = queryOptions.options?.resumeSessionId;

    // For Codex: inject --output-last-message to capture the final agent message
    // directly from a temp file, avoiding JSONL extraction for the primary output.
    let outputLastMessagePath: string | undefined;
    let promptFilePath: string | undefined;
    let baseArgs = options.args;
    if (options.adapter === "codex") {
      outputLastMessagePath = join(tmpdir(), `codex-output-${randomUUID()}.txt`);
      baseArgs = [...options.args, "--output-last-message", outputLastMessagePath];
    }

    // Build final args and stdin based on prompt delivery mode and resume state.
    // For Codex with NIGHTGAUGE_CODEX_RESUME_ENABLED=true, switch from
    // `exec` to `exec resume` when session ID is available (or `--last` as fallback).
    // `--sandbox` is not available on `exec resume`; use
    // `--dangerously-bypass-approvals-and-sandbox` for externally sandboxed envs.
    // @see Issue #1659
    let finalArgs: string[];
    let stdinPrompt: string;

    if (options.adapter === "codex" && process.env.NIGHTGAUGE_CODEX_RESUME_ENABLED === "true") {
      // Strip the standard base exec args so they are not duplicated when the
      // resume args re-add them below. Legacy `--full-auto`/`--sandbox`/
      // `danger-full-access` are still stripped defensively in case an
      // operator's NIGHTGAUGE_CODEX_CLI_ARGS override supplies them.
      const RESUME_STRIP = new Set([
        "exec",
        "--dangerously-bypass-approvals-and-sandbox",
        "--json",
        // `--ephemeral` is mutually exclusive with `exec resume` — strip it so a
        // stage that runs ephemeral on a fresh start can never carry it into a
        // resume invocation (the per-call resume trigger is not visible to the
        // adapter's base-args guard).
        "--ephemeral",
        "--full-auto",
        "--sandbox",
        "danger-full-access",
      ]);
      const extraArgs = baseArgs.filter((a) => !RESUME_STRIP.has(a));

      if (resumeSessionId) {
        // Resume with explicit thread ID: exec resume <threadId> - <base flags>
        // `-` tells Codex to read the prompt from stdin.
        finalArgs = [
          "exec",
          "resume",
          resumeSessionId,
          "-",
          "--dangerously-bypass-approvals-and-sandbox",
          "--json",
          ...extraArgs,
        ];
      } else {
        // Fallback: resume most-recent session when no explicit ID is available.
        finalArgs = [
          "exec",
          "resume",
          "--last",
          "-",
          "--dangerously-bypass-approvals-and-sandbox",
          "--json",
          ...extraArgs,
        ];
      }
      stdinPrompt = queryOptions.prompt;
    } else {
      // Standard execution path (resume disabled or non-Codex adapter).
      // For Codex, scope the filesystem sandbox to what the stage's allowed-tools
      // justify (#4026) — swaps the full-access bypass flag for a tighter
      // `--sandbox <mode> --ask-for-approval never` when the tools prove it safe.
      // No-op (full-access, unchanged) when tools imply shell/network or are
      // absent. The resume branch above can't sandbox (`--sandbox` is unsupported
      // on `exec resume`), so it stays full-access.
      const effectiveBaseArgs =
        options.adapter === "codex"
          ? applyCodexSandboxProfile(baseArgs, queryOptions.options?.allowedTools)
          : baseArgs;
      if (delivery === "positional") {
        finalArgs = [queryOptions.prompt, ...effectiveBaseArgs];
        stdinPrompt = "";
      } else if (delivery === "prompt-file") {
        promptFilePath = join(tmpdir(), `nightgauge-grok-prompt-${randomUUID()}.txt`);
        await writeFile(promptFilePath, queryOptions.prompt, "utf-8");
        finalArgs = ["--prompt-file", promptFilePath, ...effectiveBaseArgs];
        stdinPrompt = "";
      } else {
        finalArgs = effectiveBaseArgs;
        stdinPrompt = queryOptions.prompt;
      }
    }

    const result = await runCliCommand(options.command, finalArgs, stdinPrompt, cwd, env);
    if (promptFilePath) {
      unlink(promptFilePath).catch(() => {});
    }

    // Read and clean up the --output-last-message temp file (Codex only)
    let outputFileContent: string | undefined;
    if (outputLastMessagePath) {
      try {
        const raw = await readFile(outputLastMessagePath, "utf-8");
        outputFileContent = raw.trim().length > 0 ? raw : undefined;
      } catch {
        // File missing or unreadable — fall through to JSONL fallback
      } finally {
        unlink(outputLastMessagePath).catch(() => {});
      }
    }

    if (result.code !== 0) {
      throw new Error(
        `${options.adapter} runner command failed (${options.command} ${options.args.join(" ")}): ${
          result.stderr.trim() || `exit code ${result.code}`
        }`
      );
    }

    let output = result.stdout.trim();
    let geminiUsage: GeminiStreamJsonUsage | undefined;
    let codexUsage: CodexJsonUsage | undefined;
    // Session id for resume/attribution — set by codex (thread.started) or
    // copilot (Session ID footer line). @see Issue #1659, #52
    let sessionId: string | undefined;
    let copilotSummary: CopilotOutputSummary | undefined;
    let grokUsage:
      | {
          input_tokens: number;
          output_tokens: number;
          cache_read_input_tokens: number;
          cache_creation_input_tokens: number;
        }
      | undefined;
    let grokCostUsd: number | undefined;
    if (options.adapter === "codex") {
      // Always parse JSONL for failure detection signals; use file output as
      // primary source for displayText when available.
      const summary = summarizeCodexJsonOutput(result.stdout);
      output = selectCodexOutput(outputFileContent, summary).trim();
      sessionId = summary.sessionId;
      // Real per-turn token usage (Issue #4027): flows into the result message
      // below so the platform records actual Codex tokens instead of zeros.
      codexUsage = summary.usage;
      if (summary.hasExplicitFailure) {
        throw new Error(
          `codex runner reported stage failure despite exit code 0: ${
            summary.failureReason ?? "unknown failure"
          }`
        );
      }
    } else if (options.adapter === "gemini") {
      const summary = summarizeGeminiStreamJsonOutput(result.stdout);
      output = summary.displayText.trim();
      geminiUsage = summary.usage;
      if (summary.hasExplicitFailure) {
        throw new Error(
          `gemini runner reported stage failure: ${summary.failureReason ?? "unknown failure"}`
        );
      }
    } else if (options.adapter === "grok") {
      const grok = summarizeGrokStream(result.stdout + "\n" + result.stderr);
      output = grok.displayText.trim();
      sessionId = grok.sessionId;
      grokUsage = {
        input_tokens: grok.usage.input_tokens,
        output_tokens: grok.usage.output_tokens + grok.usage.reasoning_tokens,
        cache_read_input_tokens: grok.usage.cache_read_input_tokens,
        cache_creation_input_tokens: grok.usage.cache_creation_input_tokens,
      };
      grokCostUsd = grok.totalCostUsd;
      if (grok.isQuotaExhausted) {
        throw new Error(
          `[rate-limit-quota-exhausted] grok usage pool exhausted: ${grok.failureReason ?? "quota"}`
        );
      }
      if (grok.isAuthFailure) {
        throw new Error(`grok authentication failed: ${grok.failureReason ?? "not authenticated"}`);
      }
      if (grok.hasExplicitFailure) {
        throw new Error(
          `grok runner reported stage failure: ${grok.failureReason ?? "unknown failure"}`
        );
      }
    } else if (options.adapter === "copilot") {
      // Pass the requested model so the result attributes the served model even
      // when the stats footer omits a Model line (#52). Copilot has no
      // refusal-fallback, so the served model IS the requested one.
      const summary = summarizeCopilotOutput(result.stdout, process.env.NIGHTGAUGE_COPILOT_MODEL);
      output = summary.displayText.trim();
      copilotSummary = summary;
      sessionId = summary.sessionId;
      if (summary.hasExplicitFailure) {
        throw new Error(
          `copilot runner reported stage failure: ${summary.failureReason ?? "unknown failure"}`
        );
      }
    }

    if (output.length > 0) {
      yield {
        type: "assistant",
        subtype: "text",
        text: output,
      };
    }

    yield {
      type: "result",
      usage: geminiUsage ??
        codexUsage ??
        grokUsage ??
        copilotSummary?.usage ?? {
          input_tokens: 0,
          output_tokens: 0,
          cache_read_input_tokens: 0,
          cache_creation_input_tokens: 0,
        },
      total_cost_usd: grokCostUsd ?? copilotSummary?.estimatedCostUsd ?? 0,
      // Propagate model name for Copilot when available
      ...(copilotSummary?.usage?.model !== undefined && {
        model: copilotSummary.usage.model,
      }),
      // Propagate the session id for resume/attribution on backtrack retry.
      // StageExecutor reads this field and stores it as lastSessionId.
      // Set by codex (thread.started) or copilot (Session ID footer). @see #1659, #52
      ...(sessionId !== undefined && { session_id: sessionId }),
    };
  };
}

// ---------------------------------------------------------------------------
// opencode (#1637, ADR-022)
// ---------------------------------------------------------------------------

/** What an opencode query needs besides its command and argv; built by OpenCodeAdapter. */
export interface OpenCodeQueryContext {
  /** The dispatched `<provider>/<model>`, already checked. */
  model: string;
  /** The absolute worktree the run config was built for, and the run's --dir and cwd. */
  worktree: string;
  stage?: string;
  runConfig: OpenCodeRunConfig;
  /** The environment the child's is curated from. */
  parentEnv: NodeJS.ProcessEnv;
  /** Spawns processes; default `node:child_process` spawn. */
  spawn?: SpawnFn;
}

/** How long a post-run `opencode` helper (export, db) may run. */
const OPENCODE_HELPER_TIMEOUT_MS = 10_000;
/** How long the whole subagent roll-up of one stage may take. */
const OPENCODE_FOLD_BUDGET_MS = 120_000;
/** The most a helper may print; its output is held in memory only. */
const OPENCODE_HELPER_MAX_OUTPUT = 64 * 1024 * 1024;
/** How long an aborted run has after SIGTERM before its process group is killed. */
const OPENCODE_ABORT_GRACE_MS = 2_000;

type SpawnFn = typeof spawn;

interface ProcessResult {
  code: number;
  stdout: string;
  stderr: string;
  aborted: boolean;
  timedOut: boolean;
  overflow: boolean;
}

/** Signal a whole process group, falling back to the process alone where groups do not exist. */
function killGroup(pid: number | undefined, signal: NodeJS.Signals): void {
  if (pid === undefined) return;
  try {
    if (process.platform === "win32") process.kill(pid, signal);
    else process.kill(-pid, signal);
  } catch {
    // ESRCH: the group is already gone.
  }
}

/**
 * Run one opencode process in its own process group, with an argv array and
 * never a shell. `signal` aborting, or `timeoutMs` passing, kills the group:
 * SIGTERM, then SIGKILL after a grace period. Once the process exits, whatever
 * is left of its group is killed too, so no opencode child outlives the run.
 * Output is held in memory only; `maxOutput` caps stdout.
 */
function runOpenCodeProcess(
  spawnFn: SpawnFn,
  command: string,
  args: readonly string[],
  opts: {
    cwd: string;
    env: NodeJS.ProcessEnv;
    stdin?: string;
    signal?: AbortSignal;
    timeoutMs?: number;
    maxOutput?: number;
  }
): Promise<ProcessResult> {
  return new Promise((resolvePromise, reject) => {
    const child = spawnFn(command, [...args], {
      cwd: opts.cwd,
      env: opts.env,
      stdio: [opts.stdin === undefined ? "ignore" : "pipe", "pipe", "pipe"],
      detached: process.platform !== "win32",
    });
    let stdout = "";
    let stderr = "";
    let aborted = false;
    let timedOut = false;
    let overflow = false;
    let grace: ReturnType<typeof setTimeout> | undefined;
    const stop = () => {
      killGroup(child.pid, "SIGTERM");
      grace = setTimeout(() => killGroup(child.pid, "SIGKILL"), OPENCODE_ABORT_GRACE_MS);
    };
    const onAbort = () => {
      aborted = true;
      stop();
    };
    const timer =
      opts.timeoutMs === undefined
        ? undefined
        : setTimeout(() => {
            timedOut = true;
            killGroup(child.pid, "SIGKILL");
          }, opts.timeoutMs);
    if (opts.signal?.aborted) onAbort();
    else opts.signal?.addEventListener("abort", onAbort, { once: true });

    child.stdout?.setEncoding("utf-8");
    child.stderr?.setEncoding("utf-8");
    child.stdout?.on("data", (chunk: string) => {
      if (opts.maxOutput !== undefined && stdout.length + chunk.length > opts.maxOutput) {
        overflow = true;
        return;
      }
      stdout += chunk;
    });
    child.stderr?.on("data", (chunk: string) => {
      stderr += chunk;
    });
    child.on("exit", () => killGroup(child.pid, "SIGKILL"));
    child.on("error", (err) => {
      if (timer) clearTimeout(timer);
      if (grace) clearTimeout(grace);
      opts.signal?.removeEventListener("abort", onAbort);
      reject(err);
    });
    child.on("close", (code) => {
      if (timer) clearTimeout(timer);
      if (grace) clearTimeout(grace);
      opts.signal?.removeEventListener("abort", onAbort);
      resolvePromise({ code: code ?? 1, stdout, stderr, aborted, timedOut, overflow });
    });
    if (child.stdin) {
      child.stdin.on("error", () => {});
      child.stdin.end(opts.stdin ?? "");
    }
  });
}

/** The variables every post-run helper keeps: what finds the binary and the run's own database. */
const OPENCODE_HELPER_ENV_NAMES: ReadonlySet<string> = new Set([
  "PATH",
  "HOME",
  "TMPDIR",
  ...OPENCODE_ISOLATION_XDG,
]);

/**
 * The environment of a post-run helper, from the stage's: no credential, not
 * the forge token, the provider's key or the server password
 * (`openCodeHelperEnv` in Go).
 */
function helperEnv(stageEnv: NodeJS.ProcessEnv): NodeJS.ProcessEnv {
  const env: NodeJS.ProcessEnv = {};
  for (const [name, value] of Object.entries(stageEnv)) {
    if (OPENCODE_HELPER_ENV_NAMES.has(name) || name.startsWith("OPENCODE_DISABLE_")) {
      env[name] = value;
    }
  }
  return env;
}

/**
 * One opencode stage: spawn `opencode run` with the prompt on stdin, in its
 * own process group, under the child environment curated to the dispatched
 * provider plus the run's isolation variables and per-run config; then
 * classify the run (opencodeStream.ts) and yield its drift warnings, its text
 * and its result, or throw its redacted failure. `abortSignal` kills the
 * run's whole process group.
 */
async function* openCodeQuery(
  command: string,
  args: readonly string[],
  run: OpenCodeQueryContext,
  queryOptions: SDKQueryOptions
): AsyncGenerator<SDKMessage> {
  const { model, worktree, stage, runConfig } = run;
  const spawnFn = run.spawn ?? spawn;
  const cwd = queryOptions.options?.cwd;
  if (cwd !== undefined && resolvePath(cwd) !== worktree) {
    throw new AdapterError(
      `the OpenCode run config was built for ${worktree}, not ${resolvePath(cwd)}: create the ` +
        "query function for the worktree the stage runs in",
      "CONFIG_INVALID",
      "OpenCode"
    );
  }
  const signal = queryOptions.options?.abortSignal;
  // A fresh value per spawn, never logged and never on argv (ADR-022 § 18).
  const password = randomBytes(24).toString("base64url");
  const env: NodeJS.ProcessEnv = {
    ...curateOpenCodeChildEnv(run.parentEnv, model, runConfig.env),
    [OPENCODE_CONFIG_CONTENT_ENV]: runConfig.configContent,
    [OPENCODE_SERVER_PASSWORD_ENV]: password,
    NIGHTGAUGE_ADAPTER: "opencode",
    NIGHTGAUGE_OUTPUT_FORMAT: "json",
    NIGHTGAUGE_DISPATCH_MODEL: model,
    ...(stage !== undefined && { NIGHTGAUGE_STAGE: stage }),
  };

  const result = await runOpenCodeProcess(spawnFn, command, args, {
    cwd: worktree,
    env,
    stdin: queryOptions.prompt,
    signal,
  });
  if (result.aborted) {
    throw new Error("opencode query aborted: its process group was killed");
  }

  // The values of the secrets the child held are removed from everything it
  // printed before any of it reaches an error message (ADR-022 § 22).
  const secrets: Record<string, string | undefined> = {
    [OPENCODE_SERVER_PASSWORD_ENV]: password,
    GH_TOKEN: env.GH_TOKEN,
    GITHUB_TOKEN: env.GITHUB_TOKEN,
  };
  for (const name of openCodeProviderEnv(model)) secrets[name] = env[name];

  const summary = await classifyOpenCodeRun({
    stdout: result.stdout,
    stderr: result.stderr,
    exitCode: result.code,
    allowedTools: queryOptions.options?.allowedTools,
    dispatched: model,
    fold: openCodeHelper(spawnFn, command, runConfig.runDir, env, signal),
    redact: openCodeRedactor(secrets),
  });

  for (const marker of summary.driftMarkers) {
    yield { type: "warning", subtype: "opencode-drift", text: marker };
  }
  if (summary.failure !== undefined) {
    throw new Error(summary.failure);
  }
  const text = summary.stream.displayText.trim();
  if (text.length > 0) {
    yield { type: "assistant", subtype: "text", text };
  }
  yield {
    type: "result",
    usage: {
      input_tokens: summary.tokens.input,
      output_tokens: summary.tokens.output + summary.tokens.reasoning,
      cache_read_input_tokens: summary.tokens.cacheRead,
      cache_creation_input_tokens: summary.tokens.cacheWrite,
    },
    // ADR-022 § 3: a number only when stamped; an unpriced hosted stage is
    // undefined, never a fabricated 0.
    total_cost_usd: summary.costUsd,
    ...(summary.served !== undefined && {
      model: summary.served.model,
      model_provider: summary.served.provider,
      upstream_model: summary.served.upstream,
    }),
    peak_step_input_tokens: summary.peakStepInputTokens,
    usage_partial: summary.usagePartial,
    ...(summary.sessionId !== undefined && { session_id: summary.sessionId }),
  };
}

/**
 * The post-run helper: one `opencode` process per call, in its own process
 * group, from the run's root rather than the worktree, with only the
 * variables that point it at the run's database, under a per-call timeout and
 * one budget for the whole roll-up. Its output stays in memory.
 */
function openCodeHelper(
  spawnFn: SpawnFn,
  command: string,
  runDir: string,
  stageEnv: NodeJS.ProcessEnv,
  signal?: AbortSignal
): OpenCodeHelper {
  const env = helperEnv(stageEnv);
  const deadline = Date.now() + OPENCODE_FOLD_BUDGET_MS;
  return async (args) => {
    if (signal?.aborted) throw new Error(`opencode ${args[0]}: the query was aborted`);
    const timeoutMs = Math.min(OPENCODE_HELPER_TIMEOUT_MS, deadline - Date.now());
    if (timeoutMs <= 0) throw new Error(`opencode ${args[0]}: the fold's time budget is spent`);
    const r = await runOpenCodeProcess(spawnFn, command, args, {
      cwd: runDir,
      env,
      signal,
      timeoutMs,
      maxOutput: OPENCODE_HELPER_MAX_OUTPUT,
    });
    if (r.timedOut) throw new Error(`opencode ${args[0]} timed out and was killed`);
    if (r.overflow) {
      throw new Error(`opencode ${args[0]} printed more than ${OPENCODE_HELPER_MAX_OUTPUT} bytes`);
    }
    if (r.code !== 0) throw new Error(`opencode ${args[0]}: exit code ${r.code}`);
    return r.stdout;
  };
}
