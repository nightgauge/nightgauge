/**
 * Shared CLI query helper - Creates SDKQueryFunction for CLI-based adapters.
 *
 * Extracted from adapterQuery.ts to avoid duplication across adapter implementations.
 * Supports multiple prompt delivery modes (stdin, positional argument) for
 * different CLI tools.
 *
 * @see Issue #627 - Extract ICliAdapter interface & unify types
 * @see Issue #1051 - Add positional prompt delivery and Gemini stream-json support
 */

import { spawn } from "node:child_process";
import { readFile, unlink } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { randomUUID } from "node:crypto";
import type { SDKMessage, SDKQueryFunction } from "../../orchestrator/StageExecutor.js";
import type { IncrediAdapter } from "./ICliAdapter.js";
import { applyCodexSandboxProfile } from "./codexSandbox.js";
import { curateChildEnv } from "./childEnv.js";
import {
  summarizeCodexJsonOutput,
  summarizeGeminiStreamJsonOutput,
  summarizeCopilotOutput,
  type CodexJsonSummary,
  type CodexJsonUsage,
  type GeminiStreamJsonUsage,
  type CopilotOutputSummary,
} from "../adapterQuery.js";

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
export type PromptDelivery = "stdin" | "positional";

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
  adapter: IncrediAdapter;
  promptDelivery?: PromptDelivery;
}): SDKQueryFunction {
  const delivery = options.promptDelivery ?? "stdin";

  return async function* query(queryOptions): AsyncGenerator<SDKMessage> {
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
      finalArgs =
        delivery === "positional" ? [queryOptions.prompt, ...effectiveBaseArgs] : effectiveBaseArgs;
      stdinPrompt = delivery === "positional" ? "" : queryOptions.prompt;
    }

    const result = await runCliCommand(options.command, finalArgs, stdinPrompt, cwd, env);

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
        copilotSummary?.usage ?? {
          input_tokens: 0,
          output_tokens: 0,
          cache_read_input_tokens: 0,
          cache_creation_input_tokens: 0,
        },
      total_cost_usd: copilotSummary?.estimatedCostUsd ?? 0,
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
