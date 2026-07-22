/**
 * Tests for the canonical workflow contract (#3904).
 *
 * Proves the schemaVersion-4 event tree composes, the spec validator catches
 * the cases the runner must reject, and the reference sink folds the stream
 * into the live tree (last write wins by seq).
 */

import { describe, it, expect } from "vitest";
import {
  WORKFLOW_SCHEMA_VERSION,
  CLAUDE_CEILING,
  FANOUT_CEILING,
  ABSOLUTE_CEILING,
  plannedAgentCount,
  validateWorkflowSpec,
  ArrayWorkflowEventSink,
  createSeqCounter,
  zeroUsage,
  isWorkflowRun,
  isSubAgentNode,
  isJudgeVerdict,
  type WorkflowRun,
  type WorkflowPhase,
  type SubAgentNode,
  type JudgeVerdict,
  type WorkflowSpec,
  type WorkflowAgentUsage,
} from "../../cli/workflow/index.js";

const usage = (over: Partial<WorkflowAgentUsage> = {}): WorkflowAgentUsage => ({
  ...zeroUsage(),
  inputTokens: 100,
  outputTokens: 50,
  costUsd: 0.01,
  ...over,
});

function makeSpec(over: Partial<WorkflowSpec> = {}): WorkflowSpec {
  return {
    schemaVersion: WORKFLOW_SCHEMA_VERSION,
    runId: "run-1",
    issueNumber: 42,
    phases: [
      {
        name: "find",
        agents: [
          { agentId: "a1", prompt: "find bugs" },
          { agentId: "a2", prompt: "find perf issues" },
        ],
        judges: [{ judgeId: "j1", prompt: "is it real?" }],
      },
    ],
    ceiling: FANOUT_CEILING,
    ...over,
  };
}

describe("workflow contract (#3904)", () => {
  it("exposes schema version 4 and the documented ceilings", () => {
    expect(WORKFLOW_SCHEMA_VERSION).toBe(4);
    expect(CLAUDE_CEILING).toEqual({ maxConcurrent: 16, maxTotal: 1000 });
    expect(FANOUT_CEILING).toEqual({ maxConcurrent: 6, maxTotal: 32 });
    // The absolute hard cap is the largest documented provider ceiling.
    expect(ABSOLUTE_CEILING).toEqual(CLAUDE_CEILING);
  });

  it("the four node kinds compose into a tree with type guards", () => {
    const run: WorkflowRun = {
      schemaVersion: WORKFLOW_SCHEMA_VERSION,
      kind: "run",
      nodeId: "run-1",
      parentId: null,
      seq: 0,
      ts: "2026-06-03T00:00:00.000Z",
      status: "running",
      runId: "run-1",
      backend: "sdk-fanout",
      startedAt: "2026-06-03T00:00:00.000Z",
    };
    const phase: WorkflowPhase = {
      schemaVersion: WORKFLOW_SCHEMA_VERSION,
      kind: "phase",
      nodeId: "p1",
      parentId: "run-1",
      seq: 1,
      ts: "2026-06-03T00:00:01.000Z",
      status: "running",
      name: "find",
      index: 0,
      total: 1,
    };
    const agent: SubAgentNode = {
      schemaVersion: WORKFLOW_SCHEMA_VERSION,
      kind: "agent",
      nodeId: "a1",
      parentId: "p1",
      seq: 2,
      ts: "2026-06-03T00:00:02.000Z",
      status: "succeeded",
      agentId: "a1",
      provider: "codex",
      usage: usage({ estimated: true }),
      terminalKind: "success",
    };
    const judge: JudgeVerdict = {
      schemaVersion: WORKFLOW_SCHEMA_VERSION,
      kind: "judge",
      nodeId: "j1",
      parentId: "p1",
      seq: 3,
      ts: "2026-06-03T00:00:03.000Z",
      status: "succeeded",
      judgeId: "j1",
      provider: "claude",
      target: "a1",
      verdict: "pass",
      confidence: 0.9,
      usage: usage(),
    };

    expect(isWorkflowRun(run)).toBe(true);
    expect(isSubAgentNode(agent)).toBe(true);
    expect(isJudgeVerdict(judge)).toBe(true);
    expect(isWorkflowRun(phase)).toBe(false);
    // The agent carries non-zero REQUIRED usage with an honest estimated flag.
    expect(agent.usage.costUsd).toBeGreaterThan(0);
    expect(agent.usage.estimated).toBe(true);
    expect(judge.target).toBe(agent.nodeId);
  });

  describe("validateWorkflowSpec", () => {
    it("accepts a well-formed spec", () => {
      expect(validateWorkflowSpec(makeSpec())).toEqual([]);
    });

    it("rejects a schema-version mismatch", () => {
      const spec = makeSpec({ schemaVersion: 3 as never });
      expect(validateWorkflowSpec(spec).join(" ")).toMatch(/schemaVersion/i);
    });

    it("rejects a spec with no phases", () => {
      expect(validateWorkflowSpec(makeSpec({ phases: [] })).join(" ")).toMatch(/no phases/i);
    });

    it("rejects a fan-out that exceeds the ceiling.maxTotal", () => {
      const spec = makeSpec({ ceiling: { maxConcurrent: 2, maxTotal: 2 } });
      // 2 agents + 1 judge = 3 planned > 2
      expect(validateWorkflowSpec(spec).join(" ")).toMatch(/exceeds ceiling/i);
    });

    it("plannedAgentCount counts agents + judges across phases", () => {
      expect(plannedAgentCount(makeSpec())).toBe(3);
    });

    // Security review #3916 (F1): the per-spec ceiling is caller-supplied, so an
    // over-large or non-integer ceiling must be rejected — it cannot be used to
    // raise the hard process/concurrency cap above the absolute limit.
    it("rejects a ceiling.maxTotal above the absolute hard cap", () => {
      const spec = makeSpec({
        phases: [{ name: "find", agents: [{ agentId: "a1", prompt: "p" }] }],
        ceiling: { maxConcurrent: 4, maxTotal: ABSOLUTE_CEILING.maxTotal + 1 },
      });
      expect(validateWorkflowSpec(spec).join(" ")).toMatch(/exceeds the absolute hard cap/i);
    });

    it("rejects a ceiling.maxConcurrent above the absolute hard cap", () => {
      const spec = makeSpec({
        phases: [{ name: "find", agents: [{ agentId: "a1", prompt: "p" }] }],
        ceiling: { maxConcurrent: ABSOLUTE_CEILING.maxConcurrent + 1, maxTotal: 2 },
      });
      expect(validateWorkflowSpec(spec).join(" ")).toMatch(/exceeds the absolute hard cap/i);
    });

    it("accepts a ceiling exactly at the absolute hard cap", () => {
      const spec = makeSpec({
        phases: [{ name: "find", agents: [{ agentId: "a1", prompt: "p" }] }],
        ceiling: { ...ABSOLUTE_CEILING },
      });
      expect(validateWorkflowSpec(spec)).toEqual([]);
    });

    it("rejects a non-integer or non-positive ceiling", () => {
      const fractional = makeSpec({
        phases: [{ name: "find", agents: [{ agentId: "a1", prompt: "p" }] }],
        ceiling: { maxConcurrent: 2.5, maxTotal: 4 },
      });
      expect(validateWorkflowSpec(fractional).join(" ")).toMatch(/positive integers/i);

      const zero = makeSpec({
        phases: [{ name: "find", agents: [{ agentId: "a1", prompt: "p" }] }],
        ceiling: { maxConcurrent: 0, maxTotal: 4 },
      });
      expect(validateWorkflowSpec(zero).join(" ")).toMatch(/positive integers/i);
    });
  });

  describe("ArrayWorkflowEventSink", () => {
    it("collects emissions and folds to latest-by-node (last write wins by seq)", () => {
      const seq = createSeqCounter();
      const sink = new ArrayWorkflowEventSink();
      const base = {
        schemaVersion: WORKFLOW_SCHEMA_VERSION as typeof WORKFLOW_SCHEMA_VERSION,
        kind: "agent" as const,
        nodeId: "a1",
        parentId: "p1",
        ts: "2026-06-03T00:00:00.000Z",
        agentId: "a1",
        provider: "claude",
        usage: usage(),
      };
      sink.emit({ ...base, seq: seq(), status: "running" });
      sink.emit({ ...base, seq: seq(), status: "succeeded", terminalKind: "success" });

      expect(sink.getEvents()).toHaveLength(2);
      const latest = sink.latestByNode();
      expect(latest.size).toBe(1);
      expect(latest.get("a1")?.status).toBe("succeeded");
    });
  });

  it("createSeqCounter is monotonic and dense", () => {
    const seq = createSeqCounter();
    expect([seq(), seq(), seq()]).toEqual([0, 1, 2]);
  });
});
