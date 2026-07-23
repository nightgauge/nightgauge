/**
 * Adapter query function creation.
 *
 * Delegates to the AdapterRegistry instead of inline switch/if logic.
 * Adapter-specific output summarizers (Codex JSONL, Gemini stream-json)
 * are co-located here as shared utilities used by the CLI query helper.
 *
 * @see Issue #627 - Extract ICliAdapter interface & unify types
 * @see Issue #1051 - Upgrade GeminiAdapter CLI flags and stream-json parsing
 */

import type { SDKQueryFunction } from "../orchestrator/StageExecutor.js";
import type { IncrediAdapter } from "./adapter.js";
import type { QueryFunctionOptions } from "./adapters/ICliAdapter.js";
import { defaultRegistry } from "./adapters/AdapterRegistry.js";

/**
 * Token usage parsed from Codex's `turn.completed` event, normalized onto the
 * same shape the SDK result message uses for every provider.
 *
 * Codex `--json` emits `{"type":"turn.completed","usage":{"input_tokens":N,
 * "cached_input_tokens":N,"output_tokens":N}}`. Following the OpenAI convention,
 * Codex's `input_tokens` is the FULL prompt count (cache-INCLUSIVE) and
 * `cached_input_tokens` is the cached subset of it.
 *
 * This codebase's convention (matching Claude's API) is the opposite:
 * `input_tokens` is the NON-cached prompt portion and `cache_read_input_tokens`
 * the cached subset, treated as DISJOINT pools that sum to the total (see
 * `totalTokens()` / `cacheHitRate()` in analysis/health). So we normalize by
 * subtracting the cached subset out of the headline total before storing — a
 * direct mapping would double-count the cached tokens downstream. Codex has no
 * cache-creation channel, so that field is always 0.
 *
 * @see Issue #4027 — supersedes spike #2587's "no usage" finding (Codex now
 *   emits per-turn usage; verified against captured fixtures).
 */
export interface CodexJsonUsage {
  /** Non-cached prompt tokens (Codex's cache-inclusive total minus the cached subset). */
  input_tokens: number;
  output_tokens: number;
  /** Cached prompt tokens (Codex's `cached_input_tokens`). */
  cache_read_input_tokens: number;
  cache_creation_input_tokens: number;
}

export interface CodexJsonSummary {
  displayText: string;
  hasExplicitFailure: boolean;
  failureReason?: string;
  /** Codex thread ID from thread.started event — used for session resume. @see Issue #1659 */
  sessionId?: string;
  /**
   * Real token usage summed across all `turn.completed` events in the output,
   * or `undefined` when Codex emitted no usage payload (e.g. an early exit).
   * @see Issue #4027
   */
  usage?: CodexJsonUsage;
}

export function summarizeCodexJsonOutput(output: string): CodexJsonSummary {
  const lines = output
    .split("\n")
    .map((line) => line.trim())
    .filter((line) => line.length > 0);

  let lastAgentMessage = "";
  let sessionId: string | undefined;
  let usage: CodexJsonUsage | undefined;
  const failedCommands: string[] = [];
  const criticalFailures: string[] = [];
  const githubConnectivityFailures: string[] = [];
  let hasContextWriteSignal = false;
  let hasIssuePickupCompleteSignal = false;

  const benignFailureCommandPattern = /\b(rg|grep)\b/;
  const stageWrapperCommandPattern =
    /configs\/(codex|gemini)\/commands\/(issue-pickup|feature-planning|feature-dev|feature-validate|pr-create|pr-merge)\.sh\b/;
  // Skills invoke the Go binary as `nightgauge`, `"$BINARY"`, or
  // `bin/nightgauge` depending on resolution path — match all three.
  const binaryPattern = /(?:nightgauge|"\$BINARY"|\$BINARY|bin\/nightgauge)/;
  const projectStatusSyncCommandPattern = new RegExp(
    binaryPattern.source + String.raw`\s+project\s+sync-status\b`
  );
  const criticalCommandPattern = new RegExp(
    String.raw`\b(git\s+(checkout|switch|branch|pull|push)|gh\s+(api|issue|repo|project)\b|` +
      binaryPattern.source +
      String.raw`\s+project\s+(move-status|add)\b|` +
      binaryPattern.source +
      String.raw`\s+hook\s+check-deps\b|` +
      binaryPattern.source +
      String.raw`\s+issue\s+create-sub\b)\b`
  );
  const criticalOutputPattern =
    /(error connecting to api\.github\.com|cannot write inside `?\.git`?|could not determine repository owner\/name|permission denied|operation not permitted|failed to fetch dependencies)/i;
  const contextWriteSignalPattern = /context file written:\s*\.nightgauge\/pipeline\//i;
  const issuePickupCompleteSignalPattern =
    /pipeline state updated:\s*issue-pickup\s*[→>-]\s*complete/i;

  for (const line of lines) {
    try {
      const event = JSON.parse(line) as Record<string, unknown>;

      // Extract session/thread ID for resume capability (Issue #1659).
      // Codex 0.98.0 emits {"type":"thread.started","thread_id":"<uuid>"} as the
      // first JSONL event. This ID is used by `codex exec resume <thread_id>`.
      if (event.type === "thread.started" && typeof event.thread_id === "string") {
        sessionId = event.thread_id;
      }

      // Real token usage (Issue #4027). Codex emits one `turn.completed` per turn
      // carrying a `usage` payload; `codex exec` is single-turn but `exec resume`
      // adds turns, so sum across every usage payload to total the invocation's
      // consumption. A `turn.completed` with no `usage` (early exit) is ignored.
      if (event.type === "turn.completed") {
        const turnUsage = event.usage as Record<string, unknown> | undefined;
        if (turnUsage) {
          // Coerce non-numeric/negative values to 0 — never fabricate counts.
          const rawInput = Math.max(
            0,
            typeof turnUsage.input_tokens === "number" ? turnUsage.input_tokens : 0
          );
          const rawOutput = Math.max(
            0,
            typeof turnUsage.output_tokens === "number" ? turnUsage.output_tokens : 0
          );
          const rawCached = Math.max(
            0,
            typeof turnUsage.cached_input_tokens === "number" ? turnUsage.cached_input_tokens : 0
          );
          // Codex's input_tokens is cache-inclusive; clamp the cached subset to
          // the prompt total (guards a malformed cached > input payload) and
          // store only the non-cached remainder as input_tokens so totalTokens()
          // and cacheHitRate() treat the two pools as disjoint (see CodexJsonUsage).
          const cached = Math.min(rawCached, rawInput);
          const nonCachedInput = rawInput - cached;
          usage = {
            input_tokens: (usage?.input_tokens ?? 0) + nonCachedInput,
            output_tokens: (usage?.output_tokens ?? 0) + rawOutput,
            cache_read_input_tokens: (usage?.cache_read_input_tokens ?? 0) + cached,
            cache_creation_input_tokens: 0,
          };
        }
      }

      if (event.type === "item.completed") {
        const item = event.item as Record<string, unknown> | undefined;
        if (!item) {
          continue;
        }

        if (item.type === "agent_message") {
          const text = item.text;
          if (typeof text === "string" && text.trim().length > 0) {
            lastAgentMessage = text.trim();
          }
          continue;
        }

        if (item.type === "command_execution") {
          const command = typeof item.command === "string" ? item.command : "(unknown command)";
          const outputText =
            typeof item.aggregated_output === "string" ? item.aggregated_output : "";

          if (contextWriteSignalPattern.test(outputText)) {
            hasContextWriteSignal = true;
          }

          if (issuePickupCompleteSignalPattern.test(outputText)) {
            hasIssuePickupCompleteSignal = true;
          }

          if (item.status === "failed") {
            if (command.length > 0) {
              failedCommands.push(command);
            }

            const isBenign =
              benignFailureCommandPattern.test(command) && outputText.trim().length === 0;
            if (isBenign) {
              continue;
            }

            // Nested stage-wrapper invocations can report failed command events
            // even when the outer stage finishes and writes required context.
            // Avoid treating these wrapper failures as critical by default.
            if (stageWrapperCommandPattern.test(command)) {
              continue;
            }

            // Project board sync hooks are informational and should not fail a
            // completed stage when repository metadata or board access is
            // unavailable in the current environment.
            if (projectStatusSyncCommandPattern.test(command)) {
              continue;
            }

            const isCriticalCommand = criticalCommandPattern.test(command);
            const isCriticalOutput = criticalOutputPattern.test(outputText);
            if (isCriticalCommand || isCriticalOutput) {
              const detail = outputText.trim().split("\n")[0] || command;
              criticalFailures.push(`${command} :: ${detail}`);
              if (/error connecting to api\.github\.com/i.test(outputText)) {
                githubConnectivityFailures.push(command);
              }
            }
          }
        }
      }
    } catch {
      // Non-JSON output is allowed; keep parsing other lines.
    }
  }

  const failurePattern =
    /\b(execution halted|halted at|cannot continue|stopping here|stage aborted|pipeline .*halted)\b/i;
  const hasMessageFailure = failurePattern.test(lastAgentMessage);
  const hasRecoverySignal =
    hasContextWriteSignal ||
    hasIssuePickupCompleteSignal ||
    /context file written/i.test(lastAgentMessage);
  const hasCriticalCommandFailure = criticalFailures.length > 0 && !hasRecoverySignal;
  const hasGitHubConnectivityFailure = githubConnectivityFailures.length > 0;
  const hasExplicitFailure = hasMessageFailure || hasCriticalCommandFailure;
  const failureReason = hasMessageFailure
    ? lastAgentMessage
    : hasCriticalCommandFailure
      ? hasGitHubConnectivityFailure
        ? `GitHub API connectivity failure (api.github.com) detected. This stage requires GitHub access. Failed command(s): ${githubConnectivityFailures
            .slice(-2)
            .join(" | ")}`
        : `Critical command failures detected: ${criticalFailures.slice(-3).join(" | ")}`
      : undefined;

  const displayText =
    lastAgentMessage ||
    (failedCommands.length > 0
      ? `Codex command failures detected: ${failedCommands.slice(-3).join(" | ")}`
      : output.trim());

  return {
    displayText,
    hasExplicitFailure,
    failureReason,
    sessionId,
    usage,
  };
}

// ---------------------------------------------------------------------------
// Gemini stream-json output summarizer
// ---------------------------------------------------------------------------

export interface GeminiStreamJsonUsage {
  input_tokens: number;
  output_tokens: number;
  cache_read_input_tokens: number;
  cache_creation_input_tokens: number;
}

interface GeminiStreamJsonSummary {
  displayText: string;
  hasExplicitFailure: boolean;
  failureReason?: string;
  usage?: GeminiStreamJsonUsage;
}

/**
 * Parse Gemini CLI `--output-format stream-json` NDJSON output.
 *
 * The Gemini CLI emits newline-delimited JSON events with these types:
 *   init     – session metadata (session_id, model)
 *   message  – user/assistant message chunks (role, content, delta?)
 *   tool_use – tool call requests
 *   tool_result – tool execution results (status: success|error)
 *   error    – warnings and errors (severity: warning|error)
 *   result   – final outcome (status: success|error, stats?)
 *
 * @see https://github.com/google-gemini/gemini-cli (packages/core/src/output/types.ts)
 * @see Issue #1051
 */
export function summarizeGeminiStreamJsonOutput(output: string): GeminiStreamJsonSummary {
  const lines = output
    .split("\n")
    .map((line) => line.trim())
    .filter((line) => line.length > 0);

  let lastAssistantMessage = "";
  const errors: string[] = [];
  let resultStatus: string | undefined;
  let resultErrorMessage: string | undefined;
  let resultUsage: GeminiStreamJsonUsage | undefined;

  for (const line of lines) {
    try {
      const event = JSON.parse(line) as Record<string, unknown>;
      const eventType = event.type;

      if (eventType === "message") {
        const role = event.role;
        const content = event.content;
        const isDelta = event.delta === true;
        if (
          role === "assistant" &&
          typeof content === "string" &&
          content.trim().length > 0 &&
          !isDelta
        ) {
          lastAssistantMessage = content.trim();
        }
      } else if (eventType === "error") {
        const severity = event.severity;
        const message = typeof event.message === "string" ? event.message : "unknown error";
        if (severity === "error") {
          errors.push(message);
        }
      } else if (eventType === "result") {
        if (typeof event.status === "string") {
          resultStatus = event.status;
        }
        const resultError = event.error as { message?: string; type?: string } | undefined;
        if (resultError && typeof resultError.message === "string") {
          resultErrorMessage = resultError.message;
        }
        const stats = event.stats as Record<string, number> | undefined;
        if (stats) {
          // Gemini's prompt token count is cache-INCLUSIVE (it already contains
          // the cached subset), so store only the non-cached remainder as
          // input_tokens to keep input/cache disjoint in totalTokens() — clamp
          // the cached subset to the prompt total and clamp negatives. Mirrors
          // the #4027 Codex normalization. (#4036)
          const rawInput = Math.max(stats.input_tokens ?? stats.input ?? 0, 0);
          const rawCached = Math.max(stats.cached ?? 0, 0);
          const cached = Math.min(rawCached, rawInput);
          resultUsage = {
            input_tokens: rawInput - cached,
            output_tokens: Math.max(stats.output_tokens ?? 0, 0),
            cache_read_input_tokens: cached,
            cache_creation_input_tokens: 0,
          };
        }
      }
    } catch {
      // Non-JSON lines are allowed; continue parsing.
    }
  }

  const failurePattern =
    /\b(execution halted|halted at|cannot continue|stopping here|stage aborted|pipeline .*halted)\b/i;
  const hasMessageFailure = failurePattern.test(lastAssistantMessage);
  const hasResultFailure = resultStatus === "error";
  // Only treat error-severity events as failures when the final result
  // is not an explicit success — error events that were recovered from
  // should not fail an otherwise-successful run.
  const hasErrorEvents = errors.length > 0 && resultStatus !== "success";
  const hasExplicitFailure = hasMessageFailure || hasResultFailure || hasErrorEvents;

  const failureReason = hasMessageFailure
    ? lastAssistantMessage
    : hasResultFailure && resultErrorMessage
      ? resultErrorMessage
      : hasErrorEvents
        ? errors.slice(-3).join(" | ")
        : undefined;

  const displayText =
    lastAssistantMessage ||
    (hasExplicitFailure && failureReason
      ? `Gemini stage failure: ${failureReason}`
      : output.trim());

  return {
    displayText,
    hasExplicitFailure,
    failureReason,
    usage: resultUsage,
  };
}

// ---------------------------------------------------------------------------
// Copilot CLI output summarizer
// ---------------------------------------------------------------------------

/**
 * Estimated USD cost per Copilot premium request.
 *
 * Copilot is subscription-based — there is no true marginal dollar cost per
 * request — so this is a labeled estimate, derived from Copilot Pro
 * (~$10/month for 300 premium requests ≈ $0.033/req, rounded up to $0.04).
 * Business tiers vary. Real accounting multiplies this by the ACTUAL
 * premium-request count parsed from the CLI's stats footer (#52), not a flat
 * per-invocation guess.
 */
export const COPILOT_PREMIUM_REQUEST_COST_USD = 0.04;

export interface CopilotUsage {
  /** Copilot reports no token counts — always 0. The billable unit is premium_requests. */
  input_tokens: number;
  /** Copilot reports no token counts — always 0. The billable unit is premium_requests. */
  output_tokens: number;
  /**
   * Premium requests consumed, parsed from the stats footer's
   * "Total usage est: N Premium requests" line (#52). The real billable unit,
   * replacing the prior flat "always 1 per invocation" guess. May be 0 (a
   * cached/no-op turn) or, for some models, fractional.
   */
  premium_requests: number;
  /** Model that served this request (the requested `--model`, echoed when the footer includes it). */
  model?: string;
}

export interface CopilotOutputSummary {
  displayText: string;
  hasExplicitFailure: boolean;
  failureReason?: string;
  usage?: CopilotUsage;
  /** Copilot session id from the "Session ID: <uuid>" footer line, when present. */
  sessionId?: string;
  /** Estimated USD cost — always labeled as an estimate for subscription-based pricing. */
  estimatedCostUsd: number;
}

/** "N Premium requests" from the footer's usage line (singular/plural, fractional). */
const COPILOT_PREMIUM_REQUESTS_RE = /([\d]+(?:\.[\d]+)?)\s+premium\s+requests?\b/i;
/** "Session ID: <id>" footer line. */
const COPILOT_SESSION_ID_RE = /^\s*session id:\s*(\S+)/im;
/** Optional "Model: <id>" footer line (not present in all CLI versions). */
const COPILOT_MODEL_RE = /^\s*model:\s*(\S+)/im;
/** Lines that belong to the stats footer (anchored, case-insensitive). */
const COPILOT_FOOTER_LINE_RE =
  /^(session id|started|last modified|duration|working directory|usage|total usage|total duration|total code changes|total premium)\b/i;

/** True when a line is pure decoration (box drawing / rules — no alphanumerics). */
function isCopilotDecorationLine(line: string): boolean {
  return line.length > 0 && !/[A-Za-z0-9]/.test(line);
}

/**
 * Strip the trailing stats footer that the Copilot CLI prints by default (the
 * adapter deliberately omits `-s` so the footer's premium-request count is
 * available for accounting). Walks up from the end removing footer-field,
 * decoration, and blank lines until the agent's response body is reached.
 */
function stripCopilotFooter(output: string): string {
  const lines = output.replace(/\r/g, "").split("\n");
  let end = lines.length;
  while (end > 0) {
    const l = lines[end - 1].trim();
    if (l === "" || COPILOT_FOOTER_LINE_RE.test(l) || isCopilotDecorationLine(l)) {
      end--;
    } else {
      break;
    }
  }
  return lines.slice(0, end).join("\n").trim();
}

/**
 * Parse GitHub Copilot CLI output into a normalized summary.
 *
 * Unlike Codex/Gemini (structured NDJSON), the Copilot CLI emits the agent's
 * response as plain text followed by a human-readable stats footer, e.g.:
 *
 *   Session ID: 221b5571-3998-47e1-b57a-552cf9078947
 *   Duration: 50s
 *   Usage: Total usage est: 3 Premium requests
 *   Total code changes: 12 lines added, 4 lines removed
 *
 * The footer carries the CLI's own premium-request estimate — the real billable
 * unit — and a session id. This parser extracts both, strips the footer from
 * the displayed text, and derives cost from the ACTUAL premium-request count
 * (#52). When no footer usage line is present (e.g. `-s` output or an early
 * exit) usage is left `undefined` and cost is 0 — mirroring Codex's "unobserved
 * → undefined" convention rather than fabricating a count.
 *
 * @param output    Raw stdout from the Copilot CLI.
 * @param requestedModel The `--model` the adapter requested; used as the served
 *   model when the footer omits a Model line (Copilot has no refusal-fallback,
 *   so the served model IS the requested one).
 * @see Issue #52 — copilot stream parser, model control, real cost accounting
 */
export function summarizeCopilotOutput(
  output: string,
  requestedModel?: string
): CopilotOutputSummary {
  const displayText = stripCopilotFooter(output) || output.trim();

  const sessionMatch = output.match(COPILOT_SESSION_ID_RE);
  const sessionId = sessionMatch ? sessionMatch[1] : undefined;

  const modelMatch = output.match(COPILOT_MODEL_RE);
  const model = modelMatch?.[1] ?? (requestedModel?.trim() || undefined);

  let usage: CopilotUsage | undefined;
  const premiumMatch = output.match(COPILOT_PREMIUM_REQUESTS_RE);
  if (premiumMatch) {
    const premiumRequests = Number.parseFloat(premiumMatch[1]);
    if (Number.isFinite(premiumRequests) && premiumRequests >= 0) {
      usage = {
        input_tokens: 0,
        output_tokens: 0,
        premium_requests: premiumRequests,
        model,
      };
    }
  }

  // --- Failure detection (same pattern as Gemini/Codex) ---
  const failurePattern =
    /\b(execution halted|halted at|cannot continue|stopping here|stage aborted|pipeline .*halted)\b/i;
  const hasExplicitFailure = failurePattern.test(displayText);
  const failureReason = hasExplicitFailure ? displayText : undefined;

  const estimatedCostUsd = usage ? usage.premium_requests * COPILOT_PREMIUM_REQUEST_COST_USD : 0;

  return {
    displayText,
    hasExplicitFailure,
    failureReason,
    usage,
    sessionId,
    estimatedCostUsd,
  };
}

/**
 * Create an SDKQueryFunction for the given adapter.
 *
 * Delegates to the adapter's own createQueryFunction() via the registry.
 */
export async function createAdapterQueryFunction(
  adapter: IncrediAdapter,
  options?: QueryFunctionOptions
): Promise<SDKQueryFunction> {
  return defaultRegistry.get(adapter).createQueryFunction(options);
}
