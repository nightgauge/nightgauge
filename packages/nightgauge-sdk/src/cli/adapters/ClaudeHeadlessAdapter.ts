/**
 * Claude Headless Adapter - Uses Claude CLI in headless (non-interactive) mode.
 *
 * Authenticates via `claude auth status` and executes via `claude --print`.
 *
 * @see Issue #627 - Extract ICliAdapter interface & unify types
 * @see Issue #2596 - Standardize adapter error messages
 * @see Issue #3910 - Native Dynamic Workflows offload (research preview)
 */

import type { SDKQueryFunction } from "../../orchestrator/StageExecutor.js";
import type {
  ICliAdapter,
  OrchestrationCapability,
  ValidateAuthOptions,
  QueryFunctionOptions,
} from "./ICliAdapter.js";
import type { WorkflowSpec, WorkflowEventSink } from "../workflow/index.js";
import type { PreflightCommandRunner } from "../codexPreflight.js";
import { verifyCLIInstalled, validateCLIAuth } from "./validateCLIAuth.js";
import { createCliQueryFn, parseCliArgs } from "./cliQueryHelper.js";
import { throwTimeoutError } from "./errors.js";
import {
  detectClaudeCliVersion,
  preflightNativeWorkflow,
  runClaudeNativeWorkflow,
  type NativeWorkflowReadiness,
} from "./ClaudeNativeWorkflow.js";

const ADAPTER_NAME = "Claude Headless";
const CLAUDE_DOCS_URL = "https://docs.anthropic.com/en/docs/claude-code";
const CLAUDE_INSTALL_CMD = "brew install claude  # or: npm install -g @anthropic-ai/claude-code";
const AUTH_TIMEOUT_MS = 10_000;

export class ClaudeHeadlessAdapter implements ICliAdapter {
  readonly name = "claude-headless" as const;
  readonly displayName = "Claude Headless";
  readonly cliCommand = "claude";
  // Claude Code CLI — full tool loop (edit/shell/gh).
  readonly agentic = true;

  /**
   * Native-workflow readiness from the last `validateAuth`, for diagnostics /
   * the selection point. `runWorkflow` re-derives this authoritatively.
   */
  nativeWorkflowReadiness?: NativeWorkflowReadiness;

  async validateAuth(options?: ValidateAuthOptions): Promise<"passed"> {
    const cwd = options?.cwd ?? process.cwd();
    const runner = options?.runner;

    if (!runner) {
      // Without a runner we can't validate; assume passed for SDK direct usage
      return "passed";
    }

    // Verify CLI is installed.
    await verifyCLIInstalled({
      command: "claude",
      runner,
      cwd,
      adapterName: ADAPTER_NAME,
      installCmd: CLAUDE_INSTALL_CMD,
      docsUrl: CLAUDE_DOCS_URL,
    });

    // Native-workflow preflight: the headless adapter had NO version gate (#3910
    // adds one). Apply the >= v2.1.154 floor + the env/config kill-switches. This
    // does NOT hard-fail auth — a stale CLI just downgrades the orchestration
    // mode to `sdk-fanout`; ordinary `claude --print` execution still works.
    const detectedVersion = await detectClaudeCliVersion(runner, cwd);
    this.nativeWorkflowReadiness = preflightNativeWorkflow(detectedVersion);

    // Check auth status
    const authResult = await runner("claude", ["auth", "status"], cwd);
    if (authResult.code === 124) {
      throwTimeoutError(
        ADAPTER_NAME,
        "`claude auth status`",
        AUTH_TIMEOUT_MS,
        "This can happen in non-interactive contexts. Verify `claude auth status` works manually."
      );
    }
    if (authResult.code !== 0) {
      await validateCLIAuth({
        command: "claude",
        authSubcommands: [{ args: ["auth", "status"] }],
        runner,
        cwd,
        adapterName: ADAPTER_NAME,
        errorCategory: "AUTH_MISSING",
        loginHint: "claude auth login",
        docsUrl: CLAUDE_DOCS_URL,
      });
    }

    return "passed";
  }

  async createQueryFunction(_options?: QueryFunctionOptions): Promise<SDKQueryFunction> {
    const command = process.env.NIGHTGAUGE_CLAUDE_CLI_COMMAND ?? "claude";
    const args = parseCliArgs(process.env.NIGHTGAUGE_CLAUDE_CLI_ARGS, [
      "--print",
      "--output-format",
      "text",
    ]);

    return createCliQueryFn({ command, args, adapter: this.name });
  }

  getDefaultArgs(): string[] {
    return ["--print", "--output-format", "text"];
  }

  getOrchestrationCapability(): OrchestrationCapability {
    // Claude can offload to native Dynamic Workflows (runWorkflow below,
    // version-gated >= v2.1.154); when unmet the engine downgrades to the floor.
    return "native-workflow";
  }

  /**
   * Native Dynamic Workflows offload (#3910). Drives `claude -p` ultracode mode
   * (`--effort <keyword>`, where the keyword is `workflow` below v2.1.160 and
   * `ultracode` at/after it) when the CLI is >= v2.1.154 and the kill-switches
   * are clear, emitting the canonical `WorkflowEvent` tree through `sink`.
   *
   * Cross-process resume is NEVER delegated to Claude's same-session-only
   * journal — the engine journal (#3908) is authoritative. This method only
   * drives a single fan-out; resume is owned by the engine.
   *
   * Research preview: until ultracode ships in the installed CLI, this throws
   * `NativeWorkflowUnavailableError` and the engine falls back to
   * `SdkFanoutRunner` — never a silent or partial result.
   */
  async runWorkflow(
    spec: WorkflowSpec,
    sink: WorkflowEventSink,
    options?: QueryFunctionOptions
  ): Promise<void> {
    // The offload path has no injected preflight runner, so probe the version
    // via a spawn-backed `claude --version`. When it cannot be detected the
    // driver downgrades with `version-undetectable`.
    const command = process.env.NIGHTGAUGE_CLAUDE_CLI_COMMAND ?? "claude";
    const detectedVersion = await detectClaudeCliVersion(
      makeSpawnRunner(),
      options?.cwd ?? process.cwd(),
      command
    );
    await runClaudeNativeWorkflow(spec, sink, {
      surface: "cli-ultracode",
      detectedVersion,
    });
  }

  requiresDirectApiKey(): boolean {
    return false;
  }
}

/**
 * A minimal spawn-backed `claude --version` runner for `runWorkflow`'s version
 * probe (the offload path has no injected preflight runner). Mirrors the
 * `PreflightCommandRunner` shape so it composes with `detectClaudeCliVersion`.
 */
function makeSpawnRunner(): PreflightCommandRunner {
  return async (cmd, args, cwd) => {
    const { spawn } = await import("node:child_process");
    return new Promise((resolve) => {
      const child = spawn(cmd, args, { cwd, stdio: "pipe" });
      let stdout = "";
      let stderr = "";
      child.stdout.on("data", (d) => (stdout += String(d)));
      child.stderr.on("data", (d) => (stderr += String(d)));
      child.on("error", () => resolve({ code: 1, stdout, stderr }));
      child.on("close", (code) => resolve({ code: code ?? 1, stdout, stderr }));
    });
  };
}
