/**
 * SdkFanoutExecutors — the provider-execution bindings that drive the
 * `sdk-fanout` participants (Codex + gemini / gemini-sdk / copilot / lm-studio /
 * ollama) through the portable `SdkFanoutRunner` floor (epic #3899, #3911).
 *
 * `runSdkFanout` is execution-agnostic: it takes a `{ runAgent, runJudge }`
 * bindings object and owns concurrency, the ceiling, and the event tree. This
 * module is the other half — it turns an `ICliAdapter` (Codex et al.) into those
 * bindings by running ONE EPHEMERAL agent per fanned-out unit:
 *
 *   - Codex:   `codex exec --ephemeral` (its adapter appends `--ephemeral` for
 *              stateless stages; the fan-out always runs stateless units).
 *   - others:  the adapter's own `createQueryFunction(...)` (Gemini CLI/SDK,
 *              Copilot CLI, LM Studio, Ollama) — each a single-shot exec.
 *
 * Honest usage (#3914, #4027): none of the sdk-fanout providers report
 * Claude-grade real USD cost, so `costUsd` is always left at 0 (the platform
 * prices real tokens downstream). Token counts come from the provider's result
 * message when it reports them — Codex DOES now emit per-turn usage via
 * `turn.completed` (#4027, superseding spike #2587's "no usage" finding) and
 * Gemini reports prompt/candidate counts. The `estimated` flag reflects token
 * authenticity: it is `false` when the provider reported a real usage payload
 * and `true` when usage is genuinely absent (zeros) or the cost basis is itself
 * an estimate (Copilot's flat per-request subscription pricing). Counts are
 * never invented.
 *
 * Seam: execution is injected behind {@link EphemeralExec} so the runner and
 * these bindings are unit-testable with a FAKE exec — no real CLI is spawned in
 * tests. The default exec ({@link adapterEphemeralExec}) drives the adapter's
 * query function and drains its message stream.
 *
 * @see docs/WORKFLOW_ORCHESTRATION.md
 * @see Issue #3911
 */

import {
  zeroUsage,
  type WorkflowAgentUsage,
  type WorkflowJudgeVerdict,
  type WorkflowTerminalKind,
} from "./WorkflowEvent.js";
import type { WorkflowAgentSpec, WorkflowJudgeSpec } from "./WorkflowSpec.js";
import {
  AgentExecutionError,
  type AgentExecutionResult,
  type JudgeExecutionResult,
  type WorkflowExecutorBindings,
} from "./SdkFanoutRunner.js";
import type { ICliAdapter, QueryFunctionOptions } from "../adapters/ICliAdapter.js";

/**
 * Raw outcome of running ONE ephemeral unit (agent or judge) through a provider.
 * Provider-neutral: the {@link EphemeralExec} seam returns this, and the bindings
 * map it onto the canonical `WorkflowAgentUsage` + terminal kind.
 */
export interface EphemeralExecResult {
  /** Final assistant text the provider produced (empty string when none). */
  text: string;
  /**
   * Token counts the provider reported, if any. Codex now reports per-turn
   * usage (#4027, superseding spike #2587); Gemini reports prompt/candidate
   * counts. Omitted only when the provider emitted no usage payload. The
   * per-run USD cost is never reported by these providers (priced downstream).
   */
  tokens?: {
    inputTokens?: number;
    outputTokens?: number;
    cacheReadTokens?: number;
    cacheCreationTokens?: number;
  };
  /** Model the provider actually ran, when it echoes one. */
  model?: string;
}

/**
 * The injected execution seam: run a single ephemeral provider unit for `prompt`
 * and resolve with its raw outcome, or reject to signal failure. The bindings
 * classify a rejection's {@link WorkflowTerminalKind} (timeout vs error) from the
 * thrown error; throw an {@link EphemeralTimeoutError} to mark a timeout.
 *
 * Tests pass a fake `EphemeralExec`; production uses {@link adapterEphemeralExec}.
 */
export type EphemeralExec = (input: {
  adapter: ICliAdapter;
  prompt: string;
  model?: string;
  /** Owning pipeline stage, propagated to the adapter's query options. */
  stage?: string;
  cwd?: string;
}) => Promise<EphemeralExecResult>;

/**
 * Error an {@link EphemeralExec} throws to mark a unit as timed out rather than a
 * generic error, so the terminal node carries `terminalKind: "timeout"`.
 */
export class EphemeralTimeoutError extends Error {
  constructor(message = "ephemeral execution timed out") {
    super(message);
    this.name = "EphemeralTimeoutError";
  }
}

/**
 * Build a `WorkflowAgentUsage` from a provider outcome. `costUsd` is always 0 —
 * no sdk-fanout provider reports a real per-run USD figure; the platform prices
 * the real token counts downstream (cost derivation keys on `costUsd > 0`, never
 * on the `estimated` flag, so flagging a record non-estimated never makes it read
 * as "free"). The `estimated` flag reflects TOKEN AUTHENTICITY, matching the
 * `ClaudeNativeWorkflow` precedent (`estimated: !hasAny`):
 *
 *  - `false` when the provider reported a real usage payload (Codex via
 *    `turn.completed` since #4027; Gemini via stats) — the counts are measured.
 *  - `true` when usage is genuinely absent (zeros), OR for Copilot, whose cost
 *    basis is a flat per-request subscription estimate rather than priced tokens
 *    — so its record stays flagged even when token counts are present.
 *
 * Token counts pass through when reported and stay zero otherwise — never
 * fabricated. @see Issue #4027
 */
function usageFromExec(result: EphemeralExecResult, adapter: ICliAdapter): WorkflowAgentUsage {
  const t = result.tokens ?? {};
  const inputTokens = t.inputTokens ?? 0;
  const outputTokens = t.outputTokens ?? 0;
  const cacheReadTokens = t.cacheReadTokens ?? 0;
  const cacheCreationTokens = t.cacheCreationTokens ?? 0;

  const reportedRealTokens =
    result.tokens !== undefined &&
    (inputTokens > 0 || outputTokens > 0 || cacheReadTokens > 0 || cacheCreationTokens > 0);
  // Copilot prices by flat subscription request, not by token, so its cost is
  // inherently an estimate even when token counts are present.
  const costIsFlatRateEstimate = adapter.name === "copilot";

  return {
    inputTokens,
    outputTokens,
    cacheReadTokens,
    cacheCreationTokens,
    costUsd: 0,
    estimated: !reportedRealTokens || costIsFlatRateEstimate,
  };
}

/** Map a thrown exec error to a precise terminal kind. */
function terminalKindFromError(err: unknown): WorkflowTerminalKind {
  return err instanceof EphemeralTimeoutError ? "timeout" : "error";
}

/**
 * Drive an adapter's own query function for ONE ephemeral unit and collect its
 * final text + reported usage. This is the default {@link EphemeralExec}.
 *
 * For Codex the adapter appends `--ephemeral` for stateless stages; fan-out
 * units are always stateless, so the executor pins a stateless stage unless the
 * caller overrides it. Every adapter yields a trailing `result` message carrying
 * `usage` / `total_cost_usd` / `model`; we read tokens from there (omitting them
 * when the provider reported none, e.g. Codex) and never read `total_cost_usd`
 * as a real cost — usage is always estimated.
 */
export const adapterEphemeralExec: EphemeralExec = async ({
  adapter,
  prompt,
  model,
  stage,
  cwd,
}): Promise<EphemeralExecResult> => {
  const queryOptions: QueryFunctionOptions = { cwd, stage };
  const query = await adapter.createQueryFunction(queryOptions);

  let text = "";
  let tokens: EphemeralExecResult["tokens"];
  let resolvedModel = model;

  for await (const message of query({ prompt, options: { model, cwd } })) {
    const record = message as Record<string, unknown>;
    if (message.type === "result") {
      const usage = (record.usage ?? {}) as Record<string, number | undefined>;
      tokens = {
        inputTokens: usage.input_tokens ?? 0,
        outputTokens: usage.output_tokens ?? 0,
        cacheReadTokens: usage.cache_read_input_tokens ?? 0,
        cacheCreationTokens: usage.cache_creation_input_tokens ?? 0,
      };
      if (typeof record.model === "string") {
        resolvedModel = record.model;
      }
    } else {
      // Accumulate any assistant text across the message types adapters use
      // (`text`, `content`); the last `result` message is not text.
      const chunk = record.text ?? record.content;
      if (typeof chunk === "string") {
        text += chunk;
      }
    }
  }

  return { text, tokens, model: resolvedModel };
};

/** A judge's structured outcome, parsed from its provider's text. */
interface JudgeOutcome {
  verdict: WorkflowJudgeVerdict;
  confidence?: number;
  rationale?: string;
}

/**
 * Parse an adversarial judge's verdict from its provider output. Judges have no
 * native verdict channel, so we read a small JSON object — `{ verdict, confidence,
 * rationale }` — when the provider emits one, else fall back to scanning the text
 * for a pass/fail signal. An unreadable/ambiguous judgement is `uncertain` (never
 * silently a pass — the judge exists to refute "done" claims).
 */
export function parseJudgeOutcome(text: string): JudgeOutcome {
  const trimmed = text.trim();

  // Preferred: a JSON object the judge prompt asked for.
  const jsonStart = trimmed.indexOf("{");
  const jsonEnd = trimmed.lastIndexOf("}");
  if (jsonStart >= 0 && jsonEnd > jsonStart) {
    try {
      const parsed = JSON.parse(trimmed.slice(jsonStart, jsonEnd + 1)) as {
        verdict?: unknown;
        confidence?: unknown;
        rationale?: unknown;
      };
      const verdict = normalizeVerdict(parsed.verdict);
      if (verdict) {
        const confidence =
          typeof parsed.confidence === "number" && parsed.confidence >= 0 && parsed.confidence <= 1
            ? parsed.confidence
            : undefined;
        const rationale = typeof parsed.rationale === "string" ? parsed.rationale : undefined;
        return { verdict, confidence, rationale };
      }
    } catch {
      // fall through to text scan
    }
  }

  // Fallback: scan the text for an explicit verdict keyword.
  const lower = trimmed.toLowerCase();
  if (/\bfail(ed|ure)?\b/.test(lower) || /\breject(ed)?\b/.test(lower)) {
    return { verdict: "fail", rationale: trimmed || undefined };
  }
  if (/\bpass(ed)?\b/.test(lower) || /\baccept(ed)?\b/.test(lower)) {
    return { verdict: "pass", rationale: trimmed || undefined };
  }
  return { verdict: "uncertain", rationale: trimmed || undefined };
}

/** Coerce an arbitrary `verdict` field to a `WorkflowJudgeVerdict`, or undefined. */
function normalizeVerdict(value: unknown): WorkflowJudgeVerdict | undefined {
  if (typeof value !== "string") return undefined;
  const v = value.trim().toLowerCase();
  if (v === "pass" || v === "fail" || v === "uncertain") return v;
  return undefined;
}

/** Options for {@link makeSdkFanoutBindings}. */
export interface SdkFanoutBindingsOptions {
  /**
   * Injected execution seam. Defaults to {@link adapterEphemeralExec} (drives the
   * adapter's real query function); tests pass a fake to avoid spawning a CLI.
   */
  exec?: EphemeralExec;
  /** Owning pipeline stage, propagated to the adapter's ephemeral query options. */
  stage?: string;
  /** Working directory for the ephemeral exec. */
  cwd?: string;
}

/**
 * Build the `{ runAgent, runJudge }` bindings that `runSdkFanout` uses to drive
 * `adapter` (Codex / gemini / gemini-sdk / copilot / lm-studio / ollama) over
 * ephemeral exec.
 *
 * - `runAgent` runs one ephemeral agent and returns its honest (estimated) usage
 *   + terminal kind. A thrown exec error is folded into an
 *   {@link AgentExecutionError} so the failed terminal node still carries usage
 *   and a precise {@link WorkflowTerminalKind} (timeout vs error), per #3914.
 * - `runJudge` runs one ephemeral judge and parses its verdict
 *   (pass/fail/uncertain) from the provider output; an exec failure yields an
 *   `uncertain` verdict with estimated usage — a judge that could not run never
 *   silently passes a "done" claim.
 *
 * The agent's `provider` pin is ignored here: this binding always runs `adapter`.
 * The engine selects the adapter per provider upstream; these bindings are the
 * single-provider execution surface.
 */
export function makeSdkFanoutBindings(
  adapter: ICliAdapter,
  options: SdkFanoutBindingsOptions = {}
): WorkflowExecutorBindings {
  const exec = options.exec ?? adapterEphemeralExec;
  const { stage, cwd } = options;

  return {
    async runAgent(agent: WorkflowAgentSpec): Promise<AgentExecutionResult> {
      try {
        const result = await exec({
          adapter,
          prompt: agent.prompt,
          model: agent.model,
          stage,
          cwd,
        });
        return {
          usage: usageFromExec(result, adapter),
          terminalKind: "success",
          model: result.model ?? agent.model,
          outputRef: undefined,
        };
      } catch (err) {
        // Carry honest estimated usage + a precise terminal kind through the
        // failure so the terminal node never emits a generic "error" with no
        // signal about whether the unit timed out (#3914, fan-out side).
        throw new AgentExecutionError(
          terminalKindFromError(err),
          zeroUsage(true),
          err instanceof Error ? err.message : String(err)
        );
      }
    },

    async runJudge(judge: WorkflowJudgeSpec): Promise<JudgeExecutionResult> {
      const result = await exec({
        adapter,
        prompt: judge.prompt,
        stage,
        cwd,
      });
      const outcome = parseJudgeOutcome(result.text);
      return {
        verdict: outcome.verdict,
        confidence: outcome.confidence,
        rationale: outcome.rationale,
        usage: usageFromExec(result, adapter),
      };
    },
  };
}
