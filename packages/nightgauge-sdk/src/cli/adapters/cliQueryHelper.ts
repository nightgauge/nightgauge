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
 * @see Issue #1804 - The opencode branch verifies the plugin handshake
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
import type { AdapterActivity, NightgaugeAdapter } from "./ICliAdapter.js";
import type { OpenCodeRunConfig, OpenCodeRunConfigRequest } from "./OpenCodeAdapter.js";
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
import { OpenCodeHandshakeWatch, openCodeHandshakeFromEnv } from "./opencodeHandshake.js";
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
  /** The absolute worktree a query runs in when its options name no cwd. */
  worktree: string;
  /** The stage a query runs when its options name none. */
  stage?: string;
  /**
   * Obtains and checks one query's run config (`nightgauge opencode config`
   * by default), before anything is spawned.
   */
  runConfig: (request: Omit<OpenCodeRunConfigRequest, "repo">) => Promise<OpenCodeRunConfig>;
  /** The `opencode run` argv for a worktree. */
  argv: (worktree: string) => string[];
  /** Deletes a per-run root (`nightgauge opencode cleanup`). */
  cleanRunRoot: (runId: string) => Promise<void>;
  /** The environment the child's is curated from. */
  parentEnv: NodeJS.ProcessEnv;
  /** Spawns processes; default `node:child_process` spawn. */
  spawn?: SpawnFn;
  /**
   * Told of each {@link OPENCODE_ACTIVITY_EVENTS} event as the process prints
   * it, while it runs (#1657). The stream itself is still read only once the
   * process has ended.
   */
  onActivity?: (activity: AdapterActivity) => void;
}

/**
 * The OpenCode events that are signs of life while a stage runs (#1657):
 * one `step_start` and one `step_finish` per model step, and one `tool_use`
 * per completed tool call (opencode 1.18.30; the captures in
 * internal/execution/testdata/opencode_stream_*.jsonl). OpenCode prints
 * nothing while a step generates or a tool runs.
 */
export const OPENCODE_ACTIVITY_EVENTS: ReadonlySet<string> = new Set([
  "step_start",
  "step_finish",
  "tool_use",
]);

/**
 * Tell `onActivity` about one stdout line of a running opencode process when
 * it is an {@link OPENCODE_ACTIVITY_EVENTS} event. Only the event's type is
 * passed on. Never throws: a malformed line, or a callback that throws, is
 * ignored, so it can never change how the stage is run or read.
 */
export function forwardOpenCodeActivity(
  line: string,
  onActivity: ((activity: AdapterActivity) => void) | undefined
): void {
  if (onActivity === undefined || !line.includes('"type"')) return;
  try {
    const event = (JSON.parse(line) as { type?: unknown }).type;
    if (typeof event === "string" && OPENCODE_ACTIVITY_EVENTS.has(event)) {
      onActivity({ adapter: "opencode", event });
    }
  } catch {
    // Not an event line, or the callback failed: neither affects the stage.
  }
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

/** How long a failed handshake keeps killing the run's group (`openCodeHandshakeKillWindow`). */
export const OPENCODE_HANDSHAKE_KILL_WINDOW_MS = 1_000;
/** The spacing of those kills (`killProcessTreeUntilGone`'s interval). */
const OPENCODE_HANDSHAKE_KILL_INTERVAL_MS = 15;

/**
 * Kill a process group, then keep killing it every 15 ms for
 * {@link OPENCODE_HANDSHAKE_KILL_WINDOW_MS}: the TS twin of manager.go's
 * killProcessTreeUntilGone. One SIGKILL can miss a grandchild the stage forks
 * in the same instant (on macOS a fork under a pending group signal
 * completes and joins the group), which would then keep the run's stdout
 * open. The burst also stops once `done` says every holder of the run's
 * stdio has exited, so it never signals a group id that could be reused.
 */
export function killGroupUntilGone(
  pid: number | undefined,
  done: () => boolean,
  kill: (pid: number | undefined, signal: NodeJS.Signals) => void = killGroup,
  windowMs: number = OPENCODE_HANDSHAKE_KILL_WINDOW_MS
): void {
  kill(pid, "SIGKILL");
  const deadline = Date.now() + windowMs;
  const timer = setInterval(() => {
    if (done() || Date.now() >= deadline) {
      clearInterval(timer);
      return;
    }
    kill(pid, "SIGKILL");
  }, OPENCODE_HANDSHAKE_KILL_INTERVAL_MS);
  timer.unref?.();
}

/**
 * The opencode processes this process has running, by pid, each with what
 * aborts it. Each runs detached, in its own process group, so the terminal's
 * Ctrl-C never reaches it the way it reaches every other adapter's child, and
 * nothing would end it once this process has gone. So while any is live this
 * process's exit kills every group, and SIGINT, SIGTERM or SIGHUP aborts each
 * run as its abort signal would.
 */
const liveOpenCodeProcesses = new Map<number, () => void>();

/** The signals that end this process by default, and that a group outside the terminal's misses. */
const PARENT_SIGNALS: readonly NodeJS.Signals[] = ["SIGINT", "SIGTERM", "SIGHUP"];

function killLiveOpenCodeGroups(): void {
  for (const pid of liveOpenCodeProcesses.keys()) killGroup(pid, "SIGKILL");
}

function onParentSignal(signal: NodeJS.Signals): void {
  if (process.listenerCount(signal) > 1) {
    // Another handler owns the signal, and this process may live on: stop
    // each run as its abort would, and let the handler decide the rest.
    for (const abort of [...liveOpenCodeProcesses.values()]) abort();
    return;
  }
  // Nothing else handles it, so its default action, ending this process,
  // follows; no exit handler runs on that. Kill every group first, then raise
  // the signal again with no handler left.
  killLiveOpenCodeGroups();
  unwatchParent();
  process.kill(process.pid, signal);
}

const parentSignalHandlers = new Map(
  PARENT_SIGNALS.map((signal) => [signal, () => onParentSignal(signal)] as const)
);

function watchParent(): void {
  process.on("exit", killLiveOpenCodeGroups);
  for (const [signal, handler] of parentSignalHandlers) process.on(signal, handler);
}

function unwatchParent(): void {
  process.off("exit", killLiveOpenCodeGroups);
  for (const [signal, handler] of parentSignalHandlers) process.off(signal, handler);
}

/** Track one live opencode process; the function returned stops tracking it. */
function trackOpenCodeProcess(pid: number, abort: () => void): () => void {
  if (liveOpenCodeProcesses.size === 0) watchParent();
  liveOpenCodeProcesses.set(pid, abort);
  return () => {
    if (liveOpenCodeProcesses.delete(pid) && liveOpenCodeProcesses.size === 0) unwatchParent();
  };
}

/**
 * Run one opencode process in its own process group, with an argv array and
 * never a shell. `signal` aborting, or `timeoutMs` passing, kills the group:
 * SIGTERM, then SIGKILL after a grace period. Once the process exits, whatever
 * is left of its group is killed too, so no opencode child outlives the run.
 * While it runs, this process's exit or a terminating signal ends it too
 * ({@link liveOpenCodeProcesses}). Output is held in memory only; `maxOutput`
 * caps stdout.
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
    /** Sees each complete stdout line as it arrives; returning true kills the group at once. */
    onStdoutLine?: (line: string) => boolean;
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
      if (aborted) return;
      aborted = true;
      stop();
    };
    const untrack = child.pid === undefined ? () => {} : trackOpenCodeProcess(child.pid, onAbort);
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
    let partial = "";
    let closed = false;
    child.stdout?.on("data", (chunk: string) => {
      if (opts.onStdoutLine) {
        const lines = (partial + chunk).split("\n");
        partial = lines.pop() ?? "";
        for (const line of lines) {
          if (opts.onStdoutLine(line)) killGroupUntilGone(child.pid, () => closed);
        }
      }
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
      untrack();
      reject(err);
    });
    child.on("close", (code) => {
      closed = true;
      if (opts.onStdoutLine && partial !== "" && opts.onStdoutLine(partial)) {
        killGroup(child.pid, "SIGKILL");
      }
      if (timer) clearTimeout(timer);
      if (grace) clearTimeout(grace);
      opts.signal?.removeEventListener("abort", onAbort);
      untrack();
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
 * classify the run (opencodeStream.ts) and yield its drift warnings, its
 * redacted text and its result, or throw its redacted failure. `abortSignal`
 * (StageExecutor ties it to the stage's timeout and to the orchestrator's
 * stop) kills the run's whole process group, and so does this process exiting.
 */
async function* openCodeQuery(
  command: string,
  _args: readonly string[],
  run: OpenCodeQueryContext,
  queryOptions: SDKQueryOptions
): AsyncGenerator<SDKMessage> {
  const { model } = run;
  const spawnFn = run.spawn ?? spawn;
  const cwd = queryOptions.options?.cwd;
  const worktree = cwd !== undefined ? resolvePath(cwd) : run.worktree;
  const stage = queryOptions.options?.stage ?? run.stage;
  const maxTurns = queryOptions.options?.maxTurns;
  const runId = queryOptions.options?.runId;
  const skillDir = queryOptions.options?.skillDir;
  // The per-run config is the query's own: its stage, its turn budget and its
  // run's identity reach the verb, as the Go dispatch's RunOptions reach
  // PrepareRunRoot (#1648). A refusal here spawns nothing.
  const runConfig = await run.runConfig({
    model,
    worktree,
    ...(stage !== undefined && { stage }),
    ...(maxTurns !== undefined && { maxTurns }),
    ...(runId !== undefined && { runId }),
    ...(skillDir !== undefined && { skillDir }),
  });
  // A root the verb minted for this query alone (no run identity to share)
  // is deleted when the query ends, whatever its outcome; a run's shared
  // root is deleted when the run ends (PipelineOrchestrator.run), as the Go
  // scheduler deletes it at every terminal outcome (ADR-022 § 22).
  const mintedRoot = runConfig.runId !== runId;
  try {
    yield* openCodeStage(command, run, queryOptions, { worktree, stage, runConfig, spawnFn });
  } finally {
    if (mintedRoot) {
      await run.cleanRunRoot(runConfig.runId).catch((err: unknown) => {
        console.warn(
          `[opencode-adapter] the per-run root of run ${runConfig.runId} could not be deleted: ` +
            (err instanceof Error ? err.message : String(err))
        );
      });
    }
  }
}

/** One opencode stage under an obtained run config: spawn, verify, classify. */
async function* openCodeStage(
  command: string,
  run: OpenCodeQueryContext,
  queryOptions: SDKQueryOptions,
  q: { worktree: string; stage?: string; runConfig: OpenCodeRunConfig; spawnFn: SpawnFn }
): AsyncGenerator<SDKMessage> {
  const { model } = run;
  const { worktree, stage, runConfig, spawnFn } = q;
  // The binary the verb vetted, and the argv for this query's worktree.
  const spawnCommand = runConfig.binary || command;
  const spawnArgs = run.argv(worktree);
  const signal = queryOptions.options?.abortSignal;
  // The run config was checked to carry the handshake (checkRunConfig).
  const handshake = openCodeHandshakeFromEnv(runConfig.env, runConfig.pluginVersion);
  if (handshake === undefined) {
    throw new AdapterError(
      "the OpenCode run config carries no plugin handshake, so the run could not be verified",
      "CONFIG_INVALID",
      "OpenCode"
    );
  }
  const watch = new OpenCodeHandshakeWatch(handshake);
  // A fresh value per spawn, never logged and never on argv (ADR-022 § 18).
  const password = randomBytes(24).toString("base64url");
  const env: NodeJS.ProcessEnv = {
    ...curateOpenCodeChildEnv(
      withholdInherited(run.parentEnv, runConfig.envWithhold),
      model,
      runConfig.env
    ),
    [OPENCODE_CONFIG_CONTENT_ENV]: runConfig.configContent,
    [OPENCODE_SERVER_PASSWORD_ENV]: password,
    NIGHTGAUGE_ADAPTER: "opencode",
    NIGHTGAUGE_OUTPUT_FORMAT: "json",
    NIGHTGAUGE_DISPATCH_MODEL: model,
    ...(stage !== undefined && { NIGHTGAUGE_STAGE: stage }),
  };

  const result = await runOpenCodeProcess(spawnFn, spawnCommand, spawnArgs, {
    cwd: worktree,
    env,
    stdin: queryOptions.prompt,
    signal,
    onStdoutLine: (line) => {
      const kill = watch.observe(line);
      forwardOpenCodeActivity(line, run.onActivity);
      return kill;
    },
  });
  if (result.aborted) {
    throw new Error("opencode query aborted: its process group was killed");
  }
  // The plugin handshake (#1804, manager.go's twin): a run whose plugin did
  // not load, loaded late or left a foreign sentinel fails, whatever its exit
  // code, before anything it printed is read as a result.
  const handshakeFailure = watch.finish();
  if (handshakeFailure !== undefined) {
    throw new AdapterError(handshakeFailure, "VERSION_MISMATCH", "OpenCode");
  }

  // The values of the secrets the child held are removed from every line it
  // printed, stdout and stderr alike, before anything is read from it: the
  // model's text is yielded, so it is redacted as the failure text is
  // (ADR-022 § 22).
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
    fold: openCodeHelper(spawnFn, spawnCommand, runConfig.runDir, env, signal),
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
 * `parentEnv` less every variable `nightgauge opencode config` says the spawn
 * must not inherit (`env_withhold`), applied before the child's own curation
 * as the Go manager applies OpenCodeWithholdsEnv.
 */
function withholdInherited(
  parentEnv: NodeJS.ProcessEnv,
  withhold: OpenCodeRunConfig["envWithhold"]
): NodeJS.ProcessEnv {
  if (withhold === undefined) return parentEnv;
  const names = new Set(withhold.names);
  const kept: NodeJS.ProcessEnv = {};
  for (const [name, value] of Object.entries(parentEnv)) {
    if (names.has(name) || withhold.prefixes.some((p) => name.startsWith(p))) continue;
    kept[name] = value;
  }
  return kept;
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
