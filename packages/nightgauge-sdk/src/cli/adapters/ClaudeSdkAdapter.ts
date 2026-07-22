/**
 * Claude SDK Adapter - Uses the Anthropic Claude Agent SDK directly.
 *
 * Requires ANTHROPIC_API_KEY. Does not spawn a CLI process.
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
import { AdapterError } from "./errors.js";
import {
  detectClaudeSdkVersion,
  preflightNativeWorkflow,
  runClaudeNativeWorkflow,
  type NativeWorkflowReadiness,
} from "./ClaudeNativeWorkflow.js";

const ADAPTER_NAME = "Claude SDK";
const CLAUDE_SDK_DOCS_URL = "https://docs.anthropic.com/en/api/getting-started";

export class ClaudeSdkAdapter implements ICliAdapter {
  readonly name = "claude-sdk" as const;
  readonly displayName = "Claude SDK";
  readonly cliCommand = "claude";
  // Claude Agent SDK — full tool loop (edit/shell/gh).
  readonly agentic = true;

  /**
   * Native-workflow readiness from the last `validateAuth`, for diagnostics /
   * the selection point. `runWorkflow` re-derives this authoritatively, so a
   * stale field never engages native execution by mistake.
   */
  nativeWorkflowReadiness?: NativeWorkflowReadiness;

  async validateAuth(_options?: ValidateAuthOptions): Promise<"passed"> {
    // SDK adapter validates via API key presence
    if (!process.env.ANTHROPIC_API_KEY?.trim()) {
      throw new AdapterError(
        "No Anthropic API key found.\n" +
          "Set the following environment variable:\n" +
          "  ANTHROPIC_API_KEY — get a key at console.anthropic.com/settings/keys\n" +
          `Docs: ${CLAUDE_SDK_DOCS_URL}`,
        "AUTH_MISSING",
        ADAPTER_NAME,
        CLAUDE_SDK_DOCS_URL
      );
    }

    // Native-workflow preflight: detect the SDK version and apply the version
    // floor + kill-switches. This does NOT hard-fail auth — a stale workflow
    // version simply downgrades the orchestration mode to `sdk-fanout`. The
    // native path is a research-preview acceleration backend, never a gate on
    // ordinary (non-orchestrated) execution.
    const detectedVersion = await detectClaudeSdkVersion();
    this.nativeWorkflowReadiness = preflightNativeWorkflow(detectedVersion);

    return "passed";
  }

  async createQueryFunction(_options?: QueryFunctionOptions): Promise<SDKQueryFunction> {
    try {
      // Optional peer by design: SDK-library consumers may opt into Anthropic's
      // separately licensed Agent SDK, but Nightgauge distributions do not
      // bundle or redistribute it.
      const sdk = await import("@anthropic-ai/claude-agent-sdk");
      return sdk.query as unknown as SDKQueryFunction;
    } catch (error) {
      const missingPeer =
        error instanceof Error &&
        (error.message.includes("@anthropic-ai/claude-agent-sdk") ||
          ("code" in error && error.code === "ERR_MODULE_NOT_FOUND"));
      if (!missingPeer) throw error;
      throw new AdapterError(
        "Claude Agent SDK mode is an optional integration and is not bundled with Nightgauge.\n" +
          "Install @anthropic-ai/claude-agent-sdk in the consuming project after reviewing " +
          "Anthropic's license and commercial terms, or use claude-headless with the Claude CLI.",
        "BINARY_NOT_FOUND",
        ADAPTER_NAME,
        CLAUDE_SDK_DOCS_URL
      );
    }
  }

  getDefaultArgs(): string[] {
    return [];
  }

  getOrchestrationCapability(): OrchestrationCapability {
    // Claude can offload to native Dynamic Workflows (runWorkflow below,
    // version-gated >= v2.1.154); when unmet the engine downgrades to the floor.
    return "native-workflow";
  }

  /**
   * Native Dynamic Workflows offload (#3910). Drives the Agent SDK Dynamic
   * Workflows surface (`agent()`/`parallel()`/`pipeline()`/`phase()`/`judge()`/
   * `budget()`) when the installed SDK is >= v2.1.154 and the kill-switches are
   * clear, emitting the canonical `WorkflowEvent` tree through `sink`.
   *
   * Research preview: until that surface ships in the pinned SDK, this throws
   * `NativeWorkflowUnavailableError` and the engine falls back to
   * `SdkFanoutRunner` — never a silent or partial result.
   */
  async runWorkflow(
    spec: WorkflowSpec,
    sink: WorkflowEventSink,
    _options?: QueryFunctionOptions
  ): Promise<void> {
    const detectedVersion = await detectClaudeSdkVersion();
    await runClaudeNativeWorkflow(spec, sink, {
      surface: "agent-sdk",
      detectedVersion,
    });
  }

  requiresDirectApiKey(): boolean {
    return true;
  }
}
