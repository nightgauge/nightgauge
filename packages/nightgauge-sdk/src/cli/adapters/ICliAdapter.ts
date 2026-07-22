/**
 * ICliAdapter - Unified interface for all CLI execution adapters.
 *
 * Each adapter encapsulates its own:
 * - Auth validation logic
 * - Query function creation (spawning CLI or importing SDK)
 * - Default CLI arguments
 * - Capability declarations
 *
 * @see Issue #627 - Extract ICliAdapter interface & unify types
 */

import type { SDKQueryFunction } from "../../orchestrator/StageExecutor.js";
import type { PreflightCommandRunner } from "../codexPreflight.js";
import type {
  OrchestrationCapability,
  WorkflowSpec,
  WorkflowEventSink,
} from "../workflow/index.js";

// Re-export so concrete adapters can import the orchestration capability type
// from the adapter contract they implement (#3902).
export type { OrchestrationCapability } from "../workflow/index.js";

/**
 * Canonical adapter name.
 *
 * Single source of truth for both SDK and VSCode layers.
 * The VSCode ExecutionAdapter type is derived from this.
 */
export type IncrediAdapter =
  | "claude-sdk"
  | "claude-headless"
  | "codex"
  | "gemini"
  | "gemini-sdk"
  | "lm-studio" // Issue #2058 — LM Studio local inference
  | "ollama" // Issue #2591 — Ollama local LLM inference
  | "copilot"; // Issue #1941 epic / #1942 implementation — GitHub Copilot CLI adapter

/**
 * Options passed to createQueryFunction.
 */
export interface QueryFunctionOptions {
  cwd?: string;
  /** Session ID for `codex exec resume` — propagated at call time via SDKQueryOptions. @see Issue #1659 */
  resumeSessionId?: string;
  /**
   * Pipeline stage name (e.g., 'feature-dev', 'issue-pickup').
   * Used by adapters to select stage-appropriate execution flags.
   */
  stage?: string;
}

/**
 * Options passed to validateAuth.
 */
export interface ValidateAuthOptions {
  runner?: PreflightCommandRunner;
  cwd?: string;
}

/**
 * Unified adapter interface that all CLI adapters must implement.
 */
export interface ICliAdapter {
  /** Canonical adapter identifier */
  readonly name: IncrediAdapter;

  /** Human-readable display name */
  readonly displayName: string;

  /** CLI command used to invoke this adapter (e.g., 'claude', 'codex') */
  readonly cliCommand: string;

  /**
   * TRUE when this adapter drives a real agentic tool loop (edit files, run
   * shell, call gh) — a hard requirement for pipeline stage dispatch (#57).
   * Chat-completion-only adapters (fetch/SSE with zero tool handling:
   * gemini-sdk, ollama, lm-studio) declare FALSE: a pipeline stage dispatched
   * to them would emit prose instead of commits. They remain first-class for
   * the eval harness / judge / summarization surfaces, which gate nothing.
   * Orthogonal to {@link getOrchestrationCapability} — codex is agentic yet
   * `sdk-fanout`.
   */
  readonly agentic: boolean;

  /** Validate that the adapter's CLI tool is authenticated and available. */
  validateAuth(options?: ValidateAuthOptions): Promise<"passed">;

  /** Create an SDKQueryFunction that delegates to this adapter. */
  createQueryFunction(options?: QueryFunctionOptions): Promise<SDKQueryFunction>;

  /** Return the default CLI arguments for headless execution. */
  getDefaultArgs(): string[];

  /**
   * Declare how this adapter participates in multi-agent orchestration (#3902).
   * `native-workflow` adapters MAY additionally implement `runWorkflow?()` as an
   * acceleration backend; everything else is driven through the portable
   * `SdkFanoutRunner` floor. Replaces the verified-dead 4-boolean
   * `AdapterCapabilities`.
   */
  getOrchestrationCapability(): OrchestrationCapability;

  /**
   * Optional native workflow offload (implemented in #3910). The engine
   * offloads a fan-out here only when this adapter declares `native-workflow`,
   * exposes this method, AND passes its version preflight — otherwise it falls
   * back to the `SdkFanoutRunner` floor (graceful downgrade). The run emits the
   * canonical `WorkflowEvent` tree through `sink`; the engine owns the tree, so
   * there is no return value.
   */
  runWorkflow?(
    spec: WorkflowSpec,
    sink: WorkflowEventSink,
    options?: QueryFunctionOptions
  ): Promise<void>;

  /** Whether this adapter requires a direct API key (e.g., ANTHROPIC_API_KEY). */
  requiresDirectApiKey(): boolean;
}
