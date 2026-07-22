/**
 * Tests for the WorkflowEvent Zod parse boundary (#3919).
 *
 * The single `parseWorkflowEvent` call replaces the old `PipelineEvent` string
 * matching (#3714): valid v4 node emissions parse, everything else (legacy flat
 * events, audit events, wrong schemaVersion, missing required usage) is rejected.
 */

import { describe, it, expect } from "vitest";
import { parseWorkflowEvent } from "../../src/schemas/workflowEvent";

const baseUsage = {
  inputTokens: 10,
  outputTokens: 5,
  cacheReadTokens: 0,
  cacheCreationTokens: 0,
  costUsd: 0.001,
  estimated: false,
};

describe("parseWorkflowEvent", () => {
  it("parses a valid run node", () => {
    const node = parseWorkflowEvent({
      schemaVersion: 4,
      kind: "run",
      nodeId: "run:42",
      parentId: null,
      seq: 0,
      ts: "2026-05-23T10:00:00Z",
      status: "running",
      runId: "run:42",
      issueNumber: 42,
      backend: "sdk-fanout",
      startedAt: "2026-05-23T10:00:00Z",
    });
    expect(node?.kind).toBe("run");
    expect(node?.nodeId).toBe("run:42");
  });

  it("parses a valid agent node with required usage", () => {
    const node = parseWorkflowEvent({
      schemaVersion: 4,
      kind: "agent",
      nodeId: "agent:42:finder",
      parentId: "phase:42:find",
      seq: 3,
      ts: "2026-05-23T10:00:01Z",
      status: "succeeded",
      agentId: "finder",
      provider: "codex",
      usage: baseUsage,
      terminalKind: "success",
    });
    expect(node?.kind).toBe("agent");
  });

  it("parses a valid judge node", () => {
    const node = parseWorkflowEvent({
      schemaVersion: 4,
      kind: "judge",
      nodeId: "judge:42:j1",
      parentId: "phase:42:find",
      seq: 4,
      ts: "2026-05-23T10:00:02Z",
      status: "succeeded",
      judgeId: "j1",
      provider: "claude",
      target: "agent:42:finder",
      verdict: "pass",
      usage: baseUsage,
    });
    expect(node?.kind).toBe("judge");
  });

  it("rejects a legacy flat pipeline.* event", () => {
    expect(
      parseWorkflowEvent({
        type: "pipeline.stage.started",
        runId: "run-1",
        stage: "feature-dev",
        timestamp: "2026-05-23T10:00:00Z",
      })
    ).toBeNull();
  });

  it("rejects an audit event", () => {
    expect(
      parseWorkflowEvent({ type: "audit_issue_created", id: "e1", action: "audit_issue_created" })
    ).toBeNull();
  });

  it("rejects a node with the wrong schemaVersion", () => {
    expect(
      parseWorkflowEvent({
        schemaVersion: 3,
        kind: "run",
        nodeId: "run:42",
        parentId: null,
        seq: 0,
        ts: "2026-05-23T10:00:00Z",
        status: "running",
        runId: "run:42",
        backend: "sdk-fanout",
        startedAt: "2026-05-23T10:00:00Z",
      })
    ).toBeNull();
  });

  it("rejects an agent node missing required usage", () => {
    expect(
      parseWorkflowEvent({
        schemaVersion: 4,
        kind: "agent",
        nodeId: "agent:42:finder",
        parentId: "phase:42:find",
        seq: 3,
        ts: "2026-05-23T10:00:01Z",
        status: "succeeded",
        agentId: "finder",
        provider: "codex",
        // usage intentionally absent
      })
    ).toBeNull();
  });
});
