/**
 * Tests for the sdk-fanout provider-execution bindings (#3911).
 *
 * Drives a fan-out through `makeSdkFanoutBindings` over a FAKE EphemeralExec (no
 * real CLI) and proves:
 *  (a) a provider that reports no usage payload yields zeroed,
 *      `estimated: true` usage, while one that reports real tokens (Codex via
 *      `turn.completed` since #4027) yields measured, `estimated: false` usage,
 *  (b) judges parse pass/fail/uncertain verdicts from provider output,
 *  (c) the runner's hard concurrency ceiling is respected when 7 units bind to
 *      Codex's 6-concurrent ceiling (the 7th queues),
 *  (d) exec failures map to precise terminal kinds (error vs timeout),
 *  (e) the default `adapterEphemeralExec` drains an adapter's query stream and
 *      reads reported tokens; the record is `estimated: false` for token-priced
 *      providers (Gemini) and stays `estimated: true` for Copilot's flat-rate
 *      subscription cost.
 */

import { describe, it, expect } from "vitest";
import {
  WORKFLOW_SCHEMA_VERSION,
  FANOUT_CEILING,
  isSubAgentNode,
  isJudgeVerdict,
  ArrayWorkflowEventSink,
  runSdkFanout,
  makeSdkFanoutBindings,
  adapterEphemeralExec,
  parseJudgeOutcome,
  EphemeralTimeoutError,
  type EphemeralExec,
  type WorkflowSpec,
  type SubAgentNode,
  type JudgeVerdict,
} from "../../cli/workflow/index.js";
import type { ICliAdapter, QueryFunctionOptions } from "../../cli/adapters/ICliAdapter.js";
import type { SDKMessage, SDKQueryFunction } from "../../orchestrator/StageExecutor.js";

/**
 * A minimal fake adapter standing in for Codex (or any sibling). The bindings
 * only call `createQueryFunction` via the DEFAULT exec; the fake-exec tests
 * never touch it, so it can be a stub for those.
 */
function fakeAdapter(name: ICliAdapter["name"], queryFn?: SDKQueryFunction): ICliAdapter {
  return {
    name,
    displayName: name,
    cliCommand: name,
    agentic: true,
    async validateAuth() {
      return "passed";
    },
    async createQueryFunction(_options?: QueryFunctionOptions) {
      if (!queryFn) throw new Error("createQueryFunction not stubbed");
      return queryFn;
    },
    getDefaultArgs() {
      return [];
    },
    getOrchestrationCapability() {
      return "sdk-fanout";
    },
    requiresDirectApiKey() {
      return false;
    },
  };
}

/** A spec with `agents` Codex agents and optional judges in one phase. */
function codexSpec(agents: number, judges = 0, over: Partial<WorkflowSpec> = {}): WorkflowSpec {
  return {
    schemaVersion: WORKFLOW_SCHEMA_VERSION,
    runId: "run-codex",
    issueNumber: 2587,
    phases: [
      {
        name: "find",
        agents: Array.from({ length: agents }, (_, i) => ({
          agentId: `codex-${i}`,
          prompt: `agent ${i}`,
          provider: "codex",
        })),
        judges: Array.from({ length: judges }, (_, i) => ({
          judgeId: `judge-${i}`,
          prompt: `judge ${i}`,
          provider: "codex",
        })),
      },
    ],
    ceiling: FANOUT_CEILING,
    ...over,
  };
}

describe("SdkFanoutExecutors (#3911)", () => {
  it("flags usage estimated:true when the provider reports no token payload", async () => {
    const adapter = fakeAdapter("codex");
    // This fake exec returns only text and NO `tokens` (e.g. Codex exited before
    // emitting a turn.completed usage payload) — so the record is honestly zeroed
    // and estimated, never a fabricated count.
    const exec: EphemeralExec = async ({ prompt }) => ({
      text: `result for: ${prompt}`,
    });
    const bindings = makeSdkFanoutBindings(adapter, { exec });

    const sink = new ArrayWorkflowEventSink();
    const summary = await runSdkFanout(codexSpec(3), sink, bindings);

    expect(summary.agentCount).toBe(3);
    expect(summary.agentsSucceeded).toBe(3);
    expect(summary.status).toBe("succeeded");
    // No usage payload was reported → the aggregate is estimated with zero cost.
    expect(summary.usage.estimated).toBe(true);
    expect(summary.usage.costUsd).toBe(0);

    const terminalAgents = sink
      .getEvents()
      .filter((e): e is SubAgentNode => isSubAgentNode(e) && e.status !== "running");
    expect(terminalAgents).toHaveLength(3);
    for (const a of terminalAgents) {
      expect(a.terminalKind).toBe("success");
      expect(a.provider).toBe("codex");
      // No token data → tokens are zero AND the record is flagged estimated
      // (never a fabricated count or cost).
      expect(a.usage.estimated).toBe(true);
      expect(a.usage.costUsd).toBe(0);
      expect(a.usage.inputTokens).toBe(0);
      expect(a.usage.outputTokens).toBe(0);
    }
  });

  it("flags usage estimated:false when Codex reports real turn.completed tokens (#4027)", async () => {
    const adapter = fakeAdapter("codex");
    // Codex now emits per-turn usage; the exec surfaces it as reported tokens.
    const exec: EphemeralExec = async ({ prompt }) => ({
      text: `result for: ${prompt}`,
      tokens: { inputTokens: 5000, outputTokens: 800, cacheReadTokens: 1200 },
    });
    const bindings = makeSdkFanoutBindings(adapter, { exec });

    const sink = new ArrayWorkflowEventSink();
    const summary = await runSdkFanout(codexSpec(2), sink, bindings);

    expect(summary.agentsSucceeded).toBe(2);
    // Real tokens reported → the aggregate is a measured (non-estimated) record.
    expect(summary.usage.estimated).toBe(false);

    const terminalAgents = sink
      .getEvents()
      .filter((e): e is SubAgentNode => isSubAgentNode(e) && e.status !== "running");
    expect(terminalAgents).toHaveLength(2);
    for (const a of terminalAgents) {
      expect(a.usage.estimated).toBe(false);
      expect(a.usage.inputTokens).toBe(5000);
      expect(a.usage.outputTokens).toBe(800);
      expect(a.usage.cacheReadTokens).toBe(1200);
      // Still no provider-reported USD — the platform prices the real tokens.
      expect(a.usage.costUsd).toBe(0);
    }
  });

  it("respects the 6-concurrency ceiling: 7 units bind, the 7th queues + a judge runs", async () => {
    const adapter = fakeAdapter("codex");
    let active = 0;
    let peak = 0;
    const tick = (): Promise<void> => new Promise((r) => setTimeout(r, 5));
    const exec: EphemeralExec = async ({ prompt }) => {
      active++;
      peak = Math.max(peak, active);
      await tick();
      active--;
      // A judge prompt resolves to a "fail" verdict; agents return plain text.
      return { text: prompt.includes("judge") ? '{"verdict":"fail"}' : prompt };
    };
    const bindings = makeSdkFanoutBindings(adapter, { exec });

    // 7 agents + 1 judge under Codex's 6-concurrent / 32-total ceiling: the 7th
    // agent must queue behind the limiter (peak in-flight never exceeds 6).
    const sink = new ArrayWorkflowEventSink();
    const summary = await runSdkFanout(codexSpec(7, 1), sink, bindings);

    expect(peak).toBeGreaterThan(1); // genuinely concurrent
    expect(peak).toBeLessThanOrEqual(FANOUT_CEILING.maxConcurrent); // 7th queued
    expect(FANOUT_CEILING.maxConcurrent).toBe(6);
    expect(summary.agentCount).toBe(7);
    expect(summary.agentsSucceeded).toBe(7);
    expect(summary.judgeCount).toBe(1);

    const judge = sink.getEvents().filter((e): e is JudgeVerdict => isJudgeVerdict(e))[0];
    expect(judge.verdict).toBe("fail");
    expect(judge.usage.estimated).toBe(true);
  });

  it("maps a thrown exec error to terminalKind 'error' and keeps the fan-out alive", async () => {
    const adapter = fakeAdapter("codex");
    let call = 0;
    const exec: EphemeralExec = async ({ prompt }) => {
      if (call++ === 1) throw new Error("codex exited non-zero");
      return { text: prompt };
    };
    const bindings = makeSdkFanoutBindings(adapter, { exec });

    const sink = new ArrayWorkflowEventSink();
    const summary = await runSdkFanout(codexSpec(3), sink, bindings);

    expect(summary.agentsFailed).toBe(1);
    expect(summary.agentsSucceeded).toBe(2);

    const failed = sink
      .getEvents()
      .filter((e): e is SubAgentNode => isSubAgentNode(e) && e.status === "failed");
    expect(failed).toHaveLength(1);
    expect(failed[0].terminalKind).toBe("error");
    expect(failed[0].usage.estimated).toBe(true);
  });

  it("maps an EphemeralTimeoutError to terminalKind 'timeout'", async () => {
    const adapter = fakeAdapter("codex");
    const exec: EphemeralExec = async () => {
      throw new EphemeralTimeoutError();
    };
    const bindings = makeSdkFanoutBindings(adapter, { exec });

    const sink = new ArrayWorkflowEventSink();
    const summary = await runSdkFanout(codexSpec(1), sink, bindings);

    expect(summary.agentsFailed).toBe(1);
    const failed = sink
      .getEvents()
      .filter((e): e is SubAgentNode => isSubAgentNode(e) && e.status === "failed");
    expect(failed[0].terminalKind).toBe("timeout");
  });

  it("rejects an over-ceiling fan-out (the runner throws, never truncates)", async () => {
    const adapter = fakeAdapter("codex");
    const exec: EphemeralExec = async ({ prompt }) => ({ text: prompt });
    const bindings = makeSdkFanoutBindings(adapter, { exec });

    // 33 agents exceeds the 32-total fan-out ceiling → validateWorkflowSpec fails.
    const sink = new ArrayWorkflowEventSink();
    await expect(runSdkFanout(codexSpec(33), sink, bindings)).rejects.toThrow(
      /exceeds ceiling|invalid WorkflowSpec/i
    );
    expect(sink.getEvents()).toHaveLength(0);
  });

  describe("parseJudgeOutcome", () => {
    it("parses a JSON verdict with confidence + rationale", () => {
      const out = parseJudgeOutcome(
        'noise {"verdict":"pass","confidence":0.9,"rationale":"looks done"} tail'
      );
      expect(out.verdict).toBe("pass");
      expect(out.confidence).toBe(0.9);
      expect(out.rationale).toBe("looks done");
    });

    it("falls back to a text scan for an explicit fail keyword", () => {
      expect(parseJudgeOutcome("This claim FAILED verification.").verdict).toBe("fail");
      expect(parseJudgeOutcome("Looks good, I accept it.").verdict).toBe("pass");
    });

    it("returns uncertain for ambiguous output (never a silent pass)", () => {
      expect(parseJudgeOutcome("hmm, not sure").verdict).toBe("uncertain");
      expect(parseJudgeOutcome("").verdict).toBe("uncertain");
    });

    it("drops an out-of-range confidence rather than trusting it", () => {
      const out = parseJudgeOutcome('{"verdict":"fail","confidence":5}');
      expect(out.verdict).toBe("fail");
      expect(out.confidence).toBeUndefined();
    });
  });

  describe("adapterEphemeralExec (default seam)", () => {
    it("drains an adapter query stream, reading reported tokens while flagging estimated", async () => {
      // A Gemini-style adapter that yields assistant text then a result message
      // carrying real token counts (but no real cost).
      async function* geminiLike(): AsyncGenerator<SDKMessage> {
        yield { type: "assistant", content: "partial " };
        yield { type: "assistant", content: "answer" };
        yield {
          type: "result",
          usage: {
            input_tokens: 120,
            output_tokens: 45,
            cache_read_input_tokens: 0,
            cache_creation_input_tokens: 0,
          },
          total_cost_usd: 0,
          model: "gemini-2.5-pro",
        };
      }
      const adapter = fakeAdapter("gemini", () => geminiLike());

      const result = await adapterEphemeralExec({
        adapter,
        prompt: "do the thing",
      });

      expect(result.text).toBe("partial answer");
      expect(result.model).toBe("gemini-2.5-pro");
      expect(result.tokens).toEqual({
        inputTokens: 120,
        outputTokens: 45,
        cacheReadTokens: 0,
        cacheCreationTokens: 0,
      });

      // Through the bindings, those reported tokens pass through and the record
      // is a measured one (estimated:false) since Gemini's cost is priced from
      // real tokens downstream (#4027). costUsd stays 0 (priced by the platform).
      const bindings = makeSdkFanoutBindings(adapter);
      const agentResult = await bindings.runAgent({
        agentId: "g0",
        prompt: "do the thing",
        provider: "gemini",
      });
      expect(agentResult.terminalKind).toBe("success");
      expect(agentResult.usage.inputTokens).toBe(120);
      expect(agentResult.usage.outputTokens).toBe(45);
      expect(agentResult.usage.estimated).toBe(false);
      expect(agentResult.usage.costUsd).toBe(0);
      expect(agentResult.model).toBe("gemini-2.5-pro");
    });

    it("keeps Copilot estimated:true even with reported tokens (flat-rate cost)", async () => {
      // Copilot reports token counts but prices by flat per-request subscription,
      // so the cost basis is an estimate regardless — the record stays flagged.
      async function* copilotLike(): AsyncGenerator<SDKMessage> {
        yield { type: "assistant", content: "done" };
        yield {
          type: "result",
          usage: {
            input_tokens: 300,
            output_tokens: 90,
            cache_read_input_tokens: 0,
            cache_creation_input_tokens: 0,
          },
          total_cost_usd: 0.04,
          model: "gpt-4o",
        };
      }
      const adapter = fakeAdapter("copilot", () => copilotLike());

      const bindings = makeSdkFanoutBindings(adapter);
      const agentResult = await bindings.runAgent({
        agentId: "c0",
        prompt: "do the thing",
        provider: "copilot",
      });

      expect(agentResult.terminalKind).toBe("success");
      expect(agentResult.usage.inputTokens).toBe(300);
      expect(agentResult.usage.outputTokens).toBe(90);
      // Real tokens, but flat-rate subscription cost → still estimated.
      expect(agentResult.usage.estimated).toBe(true);
    });
  });
});
