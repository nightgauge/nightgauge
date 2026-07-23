/**
 * Token Parser - Parse Claude CLI stream-json output for token usage
 *
 * The Claude CLI with `--output-format stream-json` outputs newline-delimited
 * JSON messages. This module parses those messages to extract token usage
 * for display in the UI and accumulation in PipelineStateService.
 *
 * Message types from Claude CLI:
 * - assistant: Start of assistant response
 * - content_block_delta: Streaming text content
 * - result: Final message with usage statistics (THIS IS WHAT WE PARSE)
 * - error: Error messages
 *
 * @see docs/ARCHITECTURE_DIAGRAMS.md - Token Counting Data Flow
 */

import type { ExecutionAdapter } from "../config/schema";
import type { TokenUsageUpdate } from "../services/PipelineStateService";
import { computeStageCost } from "./computeStageCost";

/**
 * Parsed token usage from a single result message
 */
export interface ParsedTokenUsage {
  inputTokens: number;
  outputTokens: number;
  cacheReadTokens: number;
  cacheCreationTokens: number;
  costUsd: number;
  /**
   * How `costUsd` was resolved (Issue #3228). Populated by `TokenAccumulator`
   * when constructed with `(adapter, model)`; absent on raw stream-json parse
   * output and on legacy no-arg accumulator usage.
   */
  costSource?: "native" | "computed" | "unknown";
  /**
   * True when `costUsd` is a SESSION-CUMULATIVE total rather than a
   * per-message delta (Issue #256). Claude CLI `result` envelopes report
   * `total_cost_usd` for the whole conversation-to-date, so a process that
   * emits several envelopes (wind-down nudge turns, in-process
   * continuations) repeats prior spend in every envelope. Consumers that
   * aggregate must delta against the previous cumulative value instead of
   * summing. Token counts in the same envelope are per-invocation and
   * remain summable.
   */
  costCumulative?: boolean;
}

/**
 * Stream-json message types from Claude CLI
 */
export type StreamJsonMessageType =
  | "assistant"
  | "user"
  | "content_block_start"
  | "content_block_delta"
  | "content_block_stop"
  | "result"
  | "token:usage"
  | "error"
  | "system"
  | "rate_limit_event";

/**
 * Parsed tool result from a user message containing tool_result content blocks
 * @see Issue #1031 - Populate tool call telemetry
 */
export interface ParsedToolResult {
  toolUseId: string;
  content: string;
  isError: boolean;
}

/**
 * Rate limit event data from Claude CLI stream-json output.
 * Emitted when the CLI detects an API rate limit (HTTP 429).
 * @see Issue #2573 — Graceful rate-limit handling
 */
export interface RateLimitEventData {
  /** Unix epoch timestamp (seconds) when the rate limit resets */
  resetsAt: number;
  /** API rate limit category (e.g., "seven_day", "daily", "five_hour") */
  rateLimitType: string;
  /** Percentage of rate limit used (0-100) */
  utilization: number;
  /**
   * "allowed" — request served from existing budget
   * "allowed_warning" — approaching the limit
   * "limited" — hard limit hit; subsequent requests will block until reset
   */
  status: string;
  /** Whether overage rates apply to the current request */
  isUsingOverage: boolean;
  /**
   * "rejected" when the user has no overage available (no credits, plan
   * doesn't allow it, etc). When status="allowed" but overageStatus is
   * "rejected", the bucket is still serving the current request.
   *
   * The previous comment here (#3386) hypothesized that "subsequent ones
   * may stall waiting for reset" — that hypothesis did NOT manifest in
   * practice. Anthropic emits this payload as the steady-state for every
   * request on plans without overage enabled (the base bucket has many
   * hours of capacity left). #3448 confirmed via session logs that
   * overage rejection alone is NOT a reliable kill signal; the
   * skillRunner fast-fail trigger now keys exclusively on
   * `status === "limited"` (the base bucket actually exhausted).
   */
  overageStatus?: string;
  /**
   * Reason overage was disabled, e.g., "out_of_credits" (#3386). Present
   * even when `status === "allowed"`; do NOT treat this as evidence of
   * base-bucket exhaustion on its own (#3448).
   */
  overageDisabledReason?: string;
}

/**
 * Parsed stream-json message
 */
export interface ParsedStreamMessage {
  type: StreamJsonMessageType;
  content?: string;
  usage?: ParsedTokenUsage;
  /**
   * LIVE per-turn usage snapshot from an `assistant` message (#233).
   *
   * Deliberately a SEPARATE field from `usage`: the additive accumulation path
   * in skillRunner keys on `usage`, and assistant-message usage must NEVER be
   * summed (see below). This field feeds only the live in-stage estimator.
   *
   * The Anthropic CLI stream-json `assistant` event wraps a full Message whose
   * `usage.input_tokens` (and `cache_read_input_tokens`) report the FULL,
   * GROWING context for that turn — a snapshot, not an additive delta — while
   * `output_tokens` is that turn's output only. Consumers must treat input and
   * cache_read as latest-wins and output as summed. Cost is left 0 (assistant
   * messages carry no `total_cost_usd`); the estimator computes it via the
   * pricing table, and the terminal `result` envelope is authoritative.
   */
  incrementalUsage?: ParsedTokenUsage;
  error?: string;
  toolName?: string;
  toolInput?: unknown;
  /**
   * All tool_use blocks in an assistant message, in document order (Issue #3760).
   * The Claude CLI delivers tool calls inside complete `assistant` messages
   * (not `content_block_start` events), so `toolName`/`toolInput` above — which
   * only populate for `content_block_start` — miss them. This array exposes
   * every tool call so deterministic phase inference can observe tool activity.
   */
  toolUses?: { name: string; input: unknown }[];
  /** Session ID for conversation resumption (Issue #118) */
  sessionId?: string;
  /** Tool result extracted from user messages (Issue #1031) */
  toolResult?: ParsedToolResult;
  /** Rate limit event data (Issue #2573) */
  rateLimitEvent?: RateLimitEventData;
  /** System-event subtype (`init`, `model_refusal_fallback`, ...) (#91) */
  subtype?: string;
  /**
   * Model reported serving this event: `message.model` on assistant
   * messages, `model` on system/init. The LAST observed value is the
   * stage's served model — it diverges from the requested model when the
   * CLI's internal model_refusal_fallback fires (#91).
   */
  model?: string;
  /**
   * The CLI's silent model swap after a safety refusal (#91): the session
   * continues on `fallbackModel` and still exits 0. Attribution only.
   * See docs/spikes/fable-5-behavior-porting.md §8.3.
   */
  modelRefusalFallback?: {
    originalModel: string;
    fallbackModel: string;
    category?: string;
  };
}

/**
 * Parse a single line of stream-json output from Claude CLI
 *
 * @param line - A single line from Claude CLI stdout (should be valid JSON)
 * @returns Parsed message or null if line is not valid JSON
 *
 * @example
 * ```typescript
 * const line = '{"type":"result","result":{"usage":{"input_tokens":100}}}';
 * const parsed = parseStreamJsonLine(line);
 * if (parsed?.usage) {
 *   console.log(`Used ${parsed.usage.inputTokens} input tokens`);
 * }
 * ```
 */
export function parseStreamJsonLine(line: string): ParsedStreamMessage | null {
  // Skip empty lines
  const trimmed = line.trim();
  if (!trimmed) {
    return null;
  }

  try {
    const parsed = JSON.parse(trimmed);

    // Handle Nightgauge workflow agent events emitted by the packaged SDK CLI.
    // Codex runs are wrapped by the SDK, so skillRunner receives these events
    // instead of Codex's raw `turn.completed` envelope. A non-terminal running
    // event is a cumulative progress snapshot; a terminal event is the
    // authoritative stage total and must flow through the additive usage path.
    if (parsed.kind === "agent" && parsed.usage && typeof parsed.usage === "object") {
      const workflowUsage: ParsedTokenUsage = {
        inputTokens: parsed.usage.inputTokens ?? 0,
        outputTokens: parsed.usage.outputTokens ?? 0,
        cacheReadTokens: parsed.usage.cacheReadTokens ?? 0,
        cacheCreationTokens: parsed.usage.cacheCreationTokens ?? 0,
        costUsd: parsed.usage.costUsd ?? 0,
      };
      const terminal = parsed.status === "succeeded" || parsed.status === "failed";
      return {
        type: terminal ? "token:usage" : "assistant",
        ...(terminal ? { usage: workflowUsage } : { incrementalUsage: workflowUsage }),
        model: typeof parsed.model === "string" ? parsed.model : undefined,
      };
    }

    // Handle result messages (contain token usage and session_id)
    // Note: Claude CLI outputs usage directly on the message, not under .result
    if (parsed.type === "result") {
      const usage = parsed.usage;
      return {
        type: "result",
        usage: usage
          ? {
              inputTokens: usage.input_tokens ?? 0,
              outputTokens: usage.output_tokens ?? 0,
              cacheReadTokens: usage.cache_read_input_tokens ?? 0,
              cacheCreationTokens: usage.cache_creation_input_tokens ?? 0,
              costUsd: (() => {
                const cost = parsed.total_cost_usd ?? 0;
                if (cost === 0 && (usage.input_tokens > 0 || usage.output_tokens > 0)) {
                  console.warn(
                    `[tokenParser] WARNING: total_cost_usd missing but tokens present. ` +
                      `Tokens: ${usage.input_tokens}/${usage.output_tokens}, costUsd will default to 0`
                  );
                }
                return cost;
              })(),
              // total_cost_usd is the session's cumulative spend, not this
              // envelope's delta (#256) — aggregation must not sum it.
              costCumulative: true,
            }
          : undefined,
        // Extract session_id for conversation resumption (Issue #118)
        sessionId: parsed.session_id,
      };
    }

    // Handle Nightgauge SDK token events from Codex adapter JSON output
    if (parsed.type === "token:usage") {
      return {
        type: "token:usage",
        usage: {
          inputTokens: parsed.inputTokens ?? 0,
          outputTokens: parsed.outputTokens ?? 0,
          cacheReadTokens: parsed.cacheReadTokens ?? 0,
          cacheCreationTokens: parsed.cacheCreationTokens ?? 0,
          costUsd: parsed.costUsd ?? 0,
        },
      };
    }

    // Handle rate_limit_event messages (Issue #2573).
    //
    // Pre-#3386 this read fields from the top level, but the real Claude CLI
    // emits them nested under `rate_limit_info`:
    //
    //   {"type":"rate_limit_event","rate_limit_info":{...}}
    //
    // The flat-fields path is preserved as a fallback for older CLI builds
    // and existing tests that still write the flat shape.
    if (parsed.type === "rate_limit_event") {
      const info = parsed.rate_limit_info ?? parsed.rateLimitInfo ?? parsed;
      return {
        type: "rate_limit_event",
        rateLimitEvent: {
          resetsAt: info.resetsAt ?? 0,
          rateLimitType: info.rateLimitType ?? "unknown",
          utilization: info.utilization ?? 0,
          status: info.status ?? "limited",
          isUsingOverage: info.isUsingOverage ?? false,
          overageStatus: info.overageStatus,
          overageDisabledReason: info.overageDisabledReason,
        },
      };
    }

    // Handle error messages
    if (parsed.type === "error") {
      return {
        type: "error",
        error: parsed.error?.message ?? parsed.message ?? "Unknown error",
      };
    }

    // Handle content delta messages (streaming text)
    if (parsed.type === "content_block_delta" && parsed.delta) {
      return {
        type: "content_block_delta",
        content: parsed.delta.text ?? "",
      };
    }

    // Handle tool use messages
    if (parsed.type === "content_block_start" && parsed.content_block?.type === "tool_use") {
      return {
        type: "content_block_start",
        toolName: parsed.content_block.name,
        toolInput: parsed.content_block.input,
      };
    }

    // Handle user messages containing tool_result content blocks (Issue #1031)
    if (parsed.type === "user" && parsed.message?.content) {
      const contentArray = Array.isArray(parsed.message.content) ? parsed.message.content : [];
      for (const block of contentArray) {
        if (block.type === "tool_result" && typeof block.tool_use_id === "string") {
          // Extract result content: can be a string, an array of content blocks, or absent
          let resultContent = "";
          if (typeof block.content === "string") {
            resultContent = block.content;
          } else if (Array.isArray(block.content)) {
            resultContent = block.content
              .filter(
                (c: { type?: string; text?: string }) =>
                  c.type === "text" && typeof c.text === "string"
              )
              .map((c: { text: string }) => c.text)
              .join("");
          }

          return {
            type: "user" as StreamJsonMessageType,
            toolResult: {
              toolUseId: block.tool_use_id,
              content: resultContent,
              isError: block.is_error === true,
            },
          };
        }
      }
    }

    // System events (#91): `init` carries the canonicalized requested model;
    // `model_refusal_fallback` records the CLI's silent swap to a fallback
    // model after a safety refusal (the session continues and still exits 0).
    // See docs/spikes/fable-5-behavior-porting.md §8.3 for a captured event.
    if (parsed.type === "system") {
      const msg: ParsedStreamMessage = { type: "system" };
      if (typeof parsed.subtype === "string") {
        msg.subtype = parsed.subtype;
      }
      if (typeof parsed.model === "string" && parsed.model) {
        msg.model = parsed.model;
      }
      if (
        parsed.subtype === "model_refusal_fallback" &&
        typeof parsed.fallback_model === "string" &&
        parsed.fallback_model
      ) {
        msg.modelRefusalFallback = {
          originalModel: typeof parsed.original_model === "string" ? parsed.original_model : "",
          fallbackModel: parsed.fallback_model,
          category:
            typeof parsed.api_refusal_category === "string"
              ? parsed.api_refusal_category
              : undefined,
        };
        msg.model = parsed.fallback_model;
      }
      return msg;
    }

    // Handle assistant messages — extract text content for phase marker detection.
    // Claude CLI stream-json emits complete assistant messages (not content_block_delta),
    // so text content must be extracted here. Each assistant message contains content
    // blocks like [{type:"text",text:"..."}, {type:"tool_use",...}].
    if (parsed.type === "assistant" || parsed.type === "message_start") {
      // Served-model attribution (#91): every assistant message reports the
      // model that produced it, so after a refusal fallback these carry the
      // fallback model.
      const servedModel =
        typeof parsed.message?.model === "string" && parsed.message.model
          ? (parsed.message.model as string)
          : undefined;
      // Live per-turn usage snapshot (#233). Exposed as `incrementalUsage` (NOT
      // `usage`) so the additive accumulation path never sees it — assistant
      // usage is a growing-context snapshot, not a summable delta. costUsd:0
      // because assistant messages carry no total_cost_usd; the estimator
      // prices it from the pricing table. See the ParsedStreamMessage field doc.
      const rawUsage = parsed.message?.usage;
      const incrementalUsage: ParsedTokenUsage | undefined =
        rawUsage && typeof rawUsage === "object"
          ? {
              inputTokens: rawUsage.input_tokens ?? 0,
              outputTokens: rawUsage.output_tokens ?? 0,
              cacheReadTokens: rawUsage.cache_read_input_tokens ?? 0,
              cacheCreationTokens: rawUsage.cache_creation_input_tokens ?? 0,
              costUsd: 0,
            }
          : undefined;
      const contentBlocks = parsed.message?.content;
      if (Array.isArray(contentBlocks)) {
        const textParts: string[] = [];
        const toolUses: { name: string; input: unknown }[] = [];
        for (const block of contentBlocks) {
          if (block.type === "text" && typeof block.text === "string") {
            textParts.push(block.text);
          }
          // Collect every tool_use block so phase inference can observe tool
          // activity (Issue #3760) — the CLI delivers tool calls here, not via
          // content_block_start events.
          if (block.type === "tool_use" && typeof block.name === "string") {
            toolUses.push({ name: block.name, input: block.input });
          }
          // Deliberately NOT extracted: phase markers inside Bash tool_use
          // command inputs. The printf command echo is the same marker that
          // comes back in the tool_result stdout, so injecting it here made
          // every printf'd phase fire twice and phaseHistory double-count
          // (#217). Markers reach detection through exactly one channel per
          // emission: genuine assistant text blocks above, or the
          // tool_result path in skillRunner/streamOutputHandler.
        }
        if (textParts.length > 0 || toolUses.length > 0) {
          return {
            type: "assistant",
            content: textParts.length > 0 ? textParts.join("\n") : undefined,
            toolUses: toolUses.length > 0 ? toolUses : undefined,
            ...(servedModel ? { model: servedModel } : {}),
            ...(incrementalUsage ? { incrementalUsage } : {}),
          };
        }
      }
      return {
        type: "assistant",
        ...(servedModel ? { model: servedModel } : {}),
        ...(incrementalUsage ? { incrementalUsage } : {}),
      };
    }

    // Return generic type for other messages
    return {
      type: parsed.type as StreamJsonMessageType,
    };
  } catch {
    // Not valid JSON - might be plain text output
    return null;
  }
}

/**
 * Convert ParsedTokenUsage to TokenUsageUpdate format for PipelineStateService
 *
 * @param usage - Parsed token usage from stream-json
 * @returns Token usage update compatible with PipelineStateService
 */
export function toTokenUsageUpdate(usage: ParsedTokenUsage): TokenUsageUpdate {
  return {
    inputTokens: usage.inputTokens,
    outputTokens: usage.outputTokens,
    cacheReadTokens: usage.cacheReadTokens,
    cacheCreationTokens: usage.cacheCreationTokens,
    costUsd: usage.costUsd,
    costSource: usage.costSource,
  };
}

/**
 * Token accumulator for tracking usage across multiple messages
 *
 * Used to aggregate token counts when processing a stream of messages.
 *
 * When constructed with `(adapter, model)` (Issue #3228), `getTotal()`
 * resolves `costUsd` through `computeStageCost` so non-Claude adapters that
 * never emit a native `total_cost_usd` still produce a non-zero per-stage
 * cost. Constructed with no args, the accumulator behaves exactly as before
 * — `costUsd` aggregates `add()` inputs and `costSource` is absent.
 *
 * Cost aggregation is semantics-aware (Issue #256): usages flagged
 * `costCumulative` (Claude `result` envelopes, whose `total_cost_usd` is the
 * session's running total) contribute only their delta since the previous
 * envelope; unflagged usages (per-event adapter costs) sum as before.
 */
export class TokenAccumulator {
  private accumulated: ParsedTokenUsage = {
    inputTokens: 0,
    outputTokens: 0,
    cacheReadTokens: 0,
    cacheCreationTokens: 0,
    costUsd: 0,
  };
  private adapter?: ExecutionAdapter;
  private model?: string;
  /** True once any `add()` call carried a non-zero `costUsd`. Drives the
   *  `native` branch of `computeStageCost` so that pre-#3228 native paths
   *  (Claude stream-json) stay authoritative even when the table also
   *  has an entry. */
  private addedNative = false;
  private lastResolvedSource: "native" | "computed" | "unknown" | undefined;
  /**
   * Cumulative `total_cost_usd` of the most recent `costCumulative` envelope
   * (Issue #256). Result envelopes report session-cumulative cost, so when a
   * process emits several (wind-down nudges, in-process continuations) each
   * one repeats all prior spend — only the delta since the previous envelope
   * is new money. Bowlsheet #236 booked $100.47 for a stage that really cost
   * $23.67 because six cumulative envelopes were summed.
   */
  private lastCumulativeCostUsd = 0;

  constructor(adapter?: ExecutionAdapter, model?: string) {
    this.adapter = adapter;
    this.model = model;
  }

  /**
   * Add token usage from a new message
   */
  add(usage: ParsedTokenUsage): void {
    this.accumulated.inputTokens += usage.inputTokens;
    this.accumulated.outputTokens += usage.outputTokens;
    this.accumulated.cacheReadTokens += usage.cacheReadTokens;
    this.accumulated.cacheCreationTokens += usage.cacheCreationTokens;
    if (usage.costCumulative) {
      // Session-cumulative cost (#256): book the delta since the previous
      // envelope. A decrease means the underlying session was replaced, so
      // the new value is entirely fresh spend.
      const delta =
        usage.costUsd >= this.lastCumulativeCostUsd
          ? usage.costUsd - this.lastCumulativeCostUsd
          : usage.costUsd;
      this.accumulated.costUsd += delta;
      if (usage.costUsd > 0) {
        this.lastCumulativeCostUsd = usage.costUsd;
      }
    } else {
      this.accumulated.costUsd += usage.costUsd;
    }
    if (usage.costUsd > 0) {
      this.addedNative = true;
    }
  }

  /**
   * Re-point cost computation at the model actually observed serving the
   * stream (#91). Called by the SkillRunner when the served model diverges
   * from the requested one (the CLI's refusal fallback), so the computed
   * cost fallback prices the model that produced the tokens. No-op impact
   * on the native-cost path, which stays authoritative.
   */
  setModel(model: string): void {
    if (model) {
      this.model = model;
    }
  }

  /**
   * Get current accumulated usage. When the accumulator was constructed
   * with `(adapter, model)`, `costUsd` is resolved through
   * `computeStageCost` and `costSource` is set to the resolution step that
   * produced the value.
   */
  getTotal(): ParsedTokenUsage {
    if (!this.adapter || !this.model) {
      return { ...this.accumulated };
    }
    const result = computeStageCost(
      this.adapter,
      this.model,
      {
        input: this.accumulated.inputTokens,
        output: this.accumulated.outputTokens,
        cache_read: this.accumulated.cacheReadTokens,
        cache_creation: this.accumulated.cacheCreationTokens,
      },
      this.addedNative ? this.accumulated.costUsd : undefined
    );
    this.lastResolvedSource = result.source;
    return {
      ...this.accumulated,
      costUsd: result.cost_usd,
      costSource: result.source,
    };
  }

  /**
   * Returns the `source` label produced by the most recent `getTotal()`
   * call, or `undefined` if `getTotal()` has not been called or the
   * accumulator was constructed without `(adapter, model)`.
   */
  getCostSource(): "native" | "computed" | "unknown" | undefined {
    return this.lastResolvedSource;
  }

  /**
   * Reset accumulator to zero
   */
  reset(): void {
    this.accumulated = {
      inputTokens: 0,
      outputTokens: 0,
      cacheReadTokens: 0,
      cacheCreationTokens: 0,
      costUsd: 0,
    };
    this.addedNative = false;
    this.lastResolvedSource = undefined;
    this.lastCumulativeCostUsd = 0;
  }

  /**
   * Check if any tokens have been accumulated
   */
  hasTokens(): boolean {
    return (
      this.accumulated.inputTokens > 0 ||
      this.accumulated.outputTokens > 0 ||
      this.accumulated.cacheReadTokens > 0 ||
      this.accumulated.cacheCreationTokens > 0
    );
  }
}

/**
 * Live in-stage token/cost estimator (#233).
 *
 * ── WHY THIS IS SEPARATE FROM `TokenAccumulator` (accumulation semantics) ──
 *
 * The Anthropic CLI stream-json `assistant` event wraps a full Message whose
 * `usage.input_tokens` (and `cache_read_input_tokens`) report the FULL, GROWING
 * context for that turn — a SNAPSHOT of the whole conversation so far, NOT an
 * additive per-turn delta. `output_tokens` is that turn's output only. Feeding
 * assistant usage into the additive `TokenAccumulator.add()` (which is
 * purpose-built for the single terminal `result` envelope) would sum every
 * turn's full context and massively over-count input/cache.
 *
 * Therefore this estimator is:
 *   (a) LATEST-WINS for input + cache_read — each `observe()` overwrites, never
 *       sums, because every turn re-reports the growing context; and
 *   (b) SUMMED for output — `output_tokens` is a genuine per-turn delta.
 *   (c) Cost is pricing-table-computed via `computeStageCost` (assistant
 *       messages carry no native `total_cost_usd`).
 *
 * ── WHY AN INEXACT LIVE ESTIMATE IS SAFE ──
 *
 * (d) The terminal `type:"result"` envelope stays AUTHORITATIVE. It flows
 *     through `TokenAccumulator` (never this estimator) and RECONCILES —
 *     overwrites — the live estimate at stage end. So this estimator drives a
 *     live UI/telemetry preview only; it can never corrupt recorded totals.
 *
 * There are NO captured assistant-with-usage stream-json fixtures in the repo
 * to empirically confirm the exact per-turn semantics, so this design is
 * grounded in DOCUMENTED Anthropic streaming semantics rather than a live
 * capture. Because (d) reconciles at stage end, an inexact live estimate is
 * safe by construction — which is precisely why the growing-context risk is
 * routed here instead of into the authoritative accumulator.
 */
export class LiveStageEstimator {
  private latestInput = 0;
  private latestCacheRead = 0;
  private latestCacheCreation = 0;
  private summedOutput = 0;
  private observed = false;
  private adapter?: ExecutionAdapter;
  private model?: string;

  constructor(adapter?: ExecutionAdapter, model?: string) {
    this.adapter = adapter;
    this.model = model;
  }

  /**
   * Re-point cost computation at the model actually observed serving the stream
   * (#91), mirroring TokenAccumulator.setModel. No effect on token counts.
   */
  setModel(model: string): void {
    if (model) {
      this.model = model;
    }
  }

  /**
   * Observe one assistant-message usage snapshot. Input + cache_read are
   * latest-wins (growing-context snapshot); output accumulates per turn.
   */
  observe(usage: ParsedTokenUsage): void {
    this.latestInput = usage.inputTokens;
    this.latestCacheRead = usage.cacheReadTokens;
    this.latestCacheCreation = usage.cacheCreationTokens;
    this.summedOutput += usage.outputTokens;
    this.observed = true;
  }

  /** True once at least one snapshot has been observed. */
  hasObserved(): boolean {
    return this.observed;
  }

  /**
   * Current live estimate. `costUsd` is resolved through `computeStageCost`
   * when the estimator was constructed with `(adapter, model)`; otherwise it
   * stays 0. Always a preview — reconciled by the terminal `result` total.
   */
  estimate(): ParsedTokenUsage {
    const base: ParsedTokenUsage = {
      inputTokens: this.latestInput,
      outputTokens: this.summedOutput,
      cacheReadTokens: this.latestCacheRead,
      cacheCreationTokens: this.latestCacheCreation,
      costUsd: 0,
    };
    if (!this.adapter || !this.model) {
      return base;
    }
    // No native cost on assistant messages — always table-computed.
    const result = computeStageCost(this.adapter, this.model, {
      input: base.inputTokens,
      output: base.outputTokens,
      cache_read: base.cacheReadTokens,
      cache_creation: base.cacheCreationTokens,
    });
    return { ...base, costUsd: result.cost_usd, costSource: result.source };
  }
}

/**
 * The usage a stage should BOOK at terminal exit, plus whether it is the live
 * estimate booked as a kill-path fallback rather than the authoritative total.
 */
export interface BookedStageUsage {
  usage: ParsedTokenUsage;
  /**
   * True when {@link usage} is the {@link LiveStageEstimator} snapshot booked
   * because no terminal `result` envelope reconciled the stage (a mid-flight
   * kill), rather than the authoritative {@link TokenAccumulator} total.
   */
  estimated: boolean;
}

/**
 * Resolve the token/cost usage to BOOK for a stage at terminal exit (#296).
 *
 * The authoritative {@link TokenAccumulator} — reconciled from the CLI's
 * terminal `result` envelope — is the source of truth for a stage that ran to
 * completion. But a stage killed mid-flight (runaway / stall / budget / user
 * cancel) is SIGTERM'd before the CLI emits that envelope, so the accumulator
 * is empty and, pre-#296, the stage booked $0 while the kill log reported the
 * real burn (bowlsheet #262: the run recorded $3.70 while the kill log showed
 * $9.15 actually spent). The {@link LiveStageEstimator} DID observe that burn
 * from per-turn `assistant` messages, so it is the correct fallback.
 *
 * Precedence — authoritative first, live estimate only as a fallback, so this
 * never double-books (and so it respects the #256/#258 cumulative-cost delta
 * accounting the accumulator already performs):
 *   1. accumulator has tokens (a full OR partial `result` envelope landed) →
 *      book its total verbatim; the live estimate is discarded.
 *   2. accumulator empty AND the estimator observed ≥1 snapshot → book the live
 *      estimate, flagged `estimated: true`.
 *   3. nothing observed anywhere → `undefined` (unchanged terse zero record).
 *
 * Pure and deterministic — no I/O — so every stage-run close handler and the
 * unit suite share exactly ONE booking decision instead of divergent copies.
 */
export function resolveStageBookedUsage(
  accumulator: TokenAccumulator,
  estimator: LiveStageEstimator
): BookedStageUsage | undefined {
  if (accumulator.hasTokens()) {
    return { usage: accumulator.getTotal(), estimated: false };
  }
  if (estimator.hasObserved()) {
    return { usage: estimator.estimate(), estimated: true };
  }
  return undefined;
}

/**
 * Parse multiple lines of stream-json output
 *
 * Splits input by newlines and parses each line, filtering out invalid lines.
 *
 * @param output - Multi-line string from Claude CLI stdout
 * @returns Array of successfully parsed messages
 */
export function parseStreamJsonOutput(output: string): ParsedStreamMessage[] {
  return output
    .split("\n")
    .map(parseStreamJsonLine)
    .filter((msg): msg is ParsedStreamMessage => msg !== null);
}

/**
 * Extract token usage from a chunk of stream-json output
 *
 * Convenience function that parses output and extracts any token usage.
 *
 * @param output - Multi-line string from Claude CLI stdout
 * @returns Token usage if found in any result message, null otherwise
 */
export function extractTokenUsage(output: string): ParsedTokenUsage | null {
  const messages = parseStreamJsonOutput(output);

  for (const msg of messages) {
    if (msg.type === "result" && msg.usage) {
      return msg.usage;
    }
  }

  return null;
}

/**
 * Format token count for display (e.g., "1.5K" or "12.3K")
 *
 * @param tokens - Number of tokens
 * @returns Human-readable token count
 */
export function formatTokenCount(tokens: number): string {
  if (tokens < 1000) {
    return tokens.toString();
  }
  if (tokens < 10000) {
    return `${(tokens / 1000).toFixed(1)}K`;
  }
  return `${(tokens / 1000).toFixed(0)}K`;
}

/**
 * Format cost for display (e.g., "$0.0023" or "$1.50")
 *
 * @param costUsd - Cost in USD
 * @returns Human-readable cost
 */
export function formatCost(costUsd: number): string {
  if (costUsd < 0.01) {
    return `$${costUsd.toFixed(4)}`;
  }
  if (costUsd < 1) {
    return `$${costUsd.toFixed(3)}`;
  }
  return `$${costUsd.toFixed(2)}`;
}

/**
 * Format token usage for display in tree items
 *
 * @param usage - Token usage to format
 * @returns Formatted string like "1.5K tokens | $0.0023"
 */
export function formatTokenUsageDisplay(usage: ParsedTokenUsage): string {
  const totalTokens = usage.inputTokens + usage.outputTokens;
  return `${formatTokenCount(totalTokens)} tokens | ${formatCost(usage.costUsd)}`;
}

/**
 * Calculate wait duration in milliseconds until a rate limit resets.
 *
 * @param resetsAt - Unix epoch timestamp (seconds) when the rate limit resets
 * @returns Wait duration in milliseconds (0 if already past)
 *
 * @see Issue #2573 — Graceful rate-limit handling
 */
export function calculateRateLimitWait(resetsAt: number): number {
  return Math.max(0, resetsAt * 1000 - Date.now());
}

/**
 * Check if the rate limit status indicates a hard limit (execution must pause).
 *
 * @param status - Rate limit status from CLI ("allowed_warning" or "limited")
 * @returns true if execution should pause and wait
 */
export function isHardRateLimit(status: string): boolean {
  return status === "limited";
}

/**
 * Whether a `rate_limit_event` status indicates the quota bucket is under
 * pressure — approaching (`allowed_warning`) or at (`limited`) its hard limit.
 * This is the only kind of signal that may arm the quota fast-fail /
 * quota-exhausted classification path.
 *
 * A plain `status: "allowed"` is steady-state telemetry the Claude CLI emits on
 * nearly every run; it means the current request WAS served and carries no
 * quota-pressure information. Treating it as a quota signal mis-routed ordinary
 * idle stalls into the quota-exhausted path, which set a GLOBAL Anthropic-quota
 * cooldown derived from the bucket's NORMAL rolling reset (Issue #3825 — #3804's
 * `feature-validate` idled 15m during self-assessment after a healthy `allowed`
 * five_hour event and halted all autonomous dispatch for ~1h38m).
 *
 * @param status - Rate limit status from CLI ("allowed" | "allowed_warning" | "limited")
 * @returns true for "allowed_warning" or "limited"; false otherwise
 */
export function isQuotaPressureSignal(status: string): boolean {
  return status === "allowed_warning" || status === "limited";
}

/**
 * Detect Anthropic session / usage-limit messages.
 *
 * Unlike GitHub 429s (handled by the rate-limit circuit breaker) and the
 * structured `rate_limit_event` stream message (which carries a unix
 * `resetsAt`), the Claude CLI surfaces a hit session/usage limit as a plain
 * `{type:"result", is_error:true}` envelope whose text reads e.g.
 * "You've hit your session limit · resets 10:30am (America/Denver)".
 *
 * This is a TRANSIENT, self-clearing condition with a known reset time — it
 * must be routed into the environmental-quota recovery path
 * (`[rate-limit-quota-exhausted]`), NOT treated as a terminal stage failure.
 *
 * @see Issue #3792 — session-limit auto-recovery
 */
export function isAnthropicSessionLimit(message: string): boolean {
  return /\b(?:hit\s+your\s+)?(?:session|usage)\s+limit\b/i.test(message);
}

/**
 * Parse the reset time from an Anthropic session-limit message into a unix
 * epoch (seconds), or `undefined` when no time can be confidently extracted.
 *
 * Handles forms like "resets 10:30am (America/Denver)", "resets 3pm",
 * "resets at 10am". Resolves to the NEXT occurrence of that wall-clock time
 * (today if still in the future, else tomorrow) in the named IANA timezone
 * (falling back to the host timezone when none is given). Exactness is not
 * critical — the Go scheduler widens the cooldown to reset + grace and falls
 * back to a default floor when this returns `undefined`.
 *
 * @see Issue #3792
 */
export function parseSessionLimitResetsAt(
  message: string,
  now: Date = new Date()
): number | undefined {
  // "resets [at] 10:30am (America/Denver)" — capture hour, optional minute,
  // optional am/pm, optional "(Area/City)" timezone.
  const m = message.match(
    /resets?\s+(?:at\s+)?(\d{1,2})(?::(\d{2}))?\s*([ap]\.?m\.?)?\s*(?:\(([A-Za-z]+\/[A-Za-z_]+)\))?/i
  );
  if (!m) return undefined;

  let hour = Number(m[1]);
  const minute = m[2] ? Number(m[2]) : 0;
  const ampm = m[3]?.toLowerCase().replace(/\./g, "");
  const tz = m[4];
  if (!Number.isInteger(hour) || hour < 0 || hour > 23 || minute < 0 || minute > 59) {
    return undefined;
  }
  if (ampm === "pm" && hour < 12) hour += 12;
  if (ampm === "am" && hour === 12) hour = 0;

  // Resolve the wall-clock time in the target timezone to a unix epoch.
  // Offset is derived via Intl.formatToParts at a guessed instant — accurate
  // except within the hour of a DST transition, which the Go-side grace
  // window absorbs.
  const epochForDay = (dayOffset: number): number | undefined => {
    const base = new Date(now.getTime() + dayOffset * 86400000);
    if (!tz) {
      const d = new Date(base);
      d.setHours(hour, minute, 0, 0);
      return Math.floor(d.getTime() / 1000);
    }
    try {
      const dtf = new Intl.DateTimeFormat("en-US", {
        timeZone: tz,
        year: "numeric",
        month: "2-digit",
        day: "2-digit",
        hour: "2-digit",
        minute: "2-digit",
        second: "2-digit",
        hour12: false,
      });
      const p = Object.fromEntries(dtf.formatToParts(base).map((x) => [x.type, x.value]));
      const y = Number(p.year);
      const mo = Number(p.month);
      const da = Number(p.day);
      // UTC instant of the desired wall-clock, then correct by the zone offset
      // observed at `base`.
      const desiredUtc = Date.UTC(y, mo - 1, da, hour, minute, 0);
      const observedUtc = Date.UTC(
        y,
        mo - 1,
        da,
        Number(p.hour === "24" ? "0" : p.hour),
        Number(p.minute),
        Number(p.second)
      );
      const offsetMs = base.getTime() - observedUtc;
      return Math.floor((desiredUtc + offsetMs) / 1000);
    } catch {
      return undefined;
    }
  };

  const todayEpoch = epochForDay(0);
  if (todayEpoch === undefined) return undefined;
  const nowEpoch = Math.floor(now.getTime() / 1000);
  if (todayEpoch > nowEpoch) return todayEpoch;
  return epochForDay(1);
}

/**
 * Format a duration in milliseconds as a human-readable countdown string.
 *
 * @param ms - Duration in milliseconds
 * @returns Formatted string like "~12m", "~45s", or "~1h 5m"
 */
export function formatRateLimitCountdown(ms: number): string {
  if (ms <= 0) return "~0s";
  const totalSeconds = Math.ceil(ms / 1000);
  if (totalSeconds < 60) return `~${totalSeconds}s`;
  const minutes = Math.floor(totalSeconds / 60);
  if (minutes < 60) return `~${minutes}m`;
  const hours = Math.floor(minutes / 60);
  const remainingMinutes = minutes % 60;
  return remainingMinutes > 0 ? `~${hours}h ${remainingMinutes}m` : `~${hours}h`;
}
