/**
 * Tests for the Claude native Dynamic Workflows ("ultracode") offload (#3910).
 *
 * Proves:
 *  (a) the exported version-gate predicate (>= v2.1.154 true, below false,
 *      undetectable → false / fail-closed) the WorkflowExecutor (#3908) reuses;
 *  (b) the ultracode keyword rename gate (workflow < v2.1.160 <= ultracode);
 *  (c) every downgrade path of `runClaudeNativeWorkflow` throws the typed
 *      `NativeWorkflowUnavailableError` (env disable, config disable, stale
 *      version, undetectable, and the research-preview api-surface path) so the
 *      engine falls back to SdkFanoutRunner — never a silent/partial result;
 *  (d) the non-throwing `preflightNativeWorkflow` readiness verdict;
 *  (e) native usage mapping onto `SubAgentNode.usage` (REQUIRED, estimated flag);
 *  (f) `emitNativeWorkflowTree` emits a well-formed run/phase/agent tree (correct
 *      parentId / monotonic seq / native backend) via a fake sink.
 */

import { describe, it, expect } from "vitest";
import {
  MIN_NATIVE_WORKFLOW_VERSION,
  ULTRACODE_KEYWORD_RENAME_VERSION,
  supportsNativeWorkflow,
  ultracodeKeyword,
  parseVersion,
  preflightNativeWorkflow,
  isNativeWorkflowDisabledByEnv,
  mapNativeUsage,
  emitNativeWorkflowTree,
  runClaudeNativeWorkflow,
  NativeWorkflowUnavailableError,
  type NativeProgressEvent,
} from "../../../cli/adapters/ClaudeNativeWorkflow.js";
import {
  WORKFLOW_SCHEMA_VERSION,
  ArrayWorkflowEventSink,
  isWorkflowRun,
  isSubAgentNode,
  type WorkflowSpec,
  type WorkflowRun,
  type SubAgentNode,
} from "../../../cli/workflow/index.js";

function makeSpec(over: Partial<WorkflowSpec> = {}): WorkflowSpec {
  return {
    schemaVersion: WORKFLOW_SCHEMA_VERSION,
    runId: "run-native-1",
    issueNumber: 3910,
    ceiling: { maxConcurrent: 16, maxTotal: 1000 },
    phases: [
      {
        name: "find",
        agents: [
          { agentId: "a0", prompt: "p0", provider: "claude" },
          { agentId: "a1", prompt: "p1", provider: "claude" },
        ],
      },
    ],
    ...over,
  };
}

// ---------------------------------------------------------------------------
// Version-gate predicate (AC: >= 2.1.154 true, below false)
// ---------------------------------------------------------------------------

describe("supportsNativeWorkflow()", () => {
  it("floor constant is 2.1.154", () => {
    expect(MIN_NATIVE_WORKFLOW_VERSION).toBe("2.1.154");
  });

  it.each(["2.1.154", "2.1.155", "2.1.200", "2.2.0", "3.0.0", "v2.1.154"])(
    "returns true at/above the floor: %s",
    (v) => {
      expect(supportsNativeWorkflow(v)).toBe(true);
    }
  );

  it.each(["2.1.153", "2.1.0", "2.0.999", "1.9.9", "0.3.153"])(
    "returns false below the floor: %s",
    (v) => {
      expect(supportsNativeWorkflow(v)).toBe(false);
    }
  );

  it.each([undefined, null, "", "not-a-version", "abc"])(
    "fails closed (false) for unparseable/missing version: %s",
    (v) => {
      expect(supportsNativeWorkflow(v as string | undefined | null)).toBe(false);
    }
  );

  it("tolerates pre-release / build suffixes", () => {
    expect(supportsNativeWorkflow("2.1.154-beta.1")).toBe(true);
    expect(supportsNativeWorkflow("2.1.153-rc.9")).toBe(false);
  });
});

describe("parseVersion()", () => {
  it("parses major.minor.patch, defaulting patch to 0", () => {
    expect(parseVersion("2.1.154")).toEqual([2, 1, 154]);
    expect(parseVersion("v2.1")).toEqual([2, 1, 0]);
  });
  it("returns null for unparseable input", () => {
    expect(parseVersion("xyz")).toBeNull();
    expect(parseVersion(undefined)).toBeNull();
  });
});

// ---------------------------------------------------------------------------
// ultracode keyword rename gate
// ---------------------------------------------------------------------------

describe("ultracodeKeyword()", () => {
  it("rename version constant is 2.1.160", () => {
    expect(ULTRACODE_KEYWORD_RENAME_VERSION).toBe("2.1.160");
  });
  it("uses 'workflow' in [2.1.154, 2.1.160)", () => {
    expect(ultracodeKeyword("2.1.154")).toBe("workflow");
    expect(ultracodeKeyword("2.1.159")).toBe("workflow");
  });
  it("uses 'ultracode' at/after 2.1.160", () => {
    expect(ultracodeKeyword("2.1.160")).toBe("ultracode");
    expect(ultracodeKeyword("2.2.0")).toBe("ultracode");
  });
});

// ---------------------------------------------------------------------------
// Env kill-switch detection
// ---------------------------------------------------------------------------

describe("isNativeWorkflowDisabledByEnv()", () => {
  it.each(["true", "1", "yes", "TRUE", " Yes "])("treats %s as disabled", (raw) => {
    expect(isNativeWorkflowDisabledByEnv({ CLAUDE_CODE_DISABLE_WORKFLOWS: raw })).toBe(true);
  });
  it.each([undefined, "", "false", "0", "no"])("treats %s as enabled", (raw) => {
    const env = raw === undefined ? {} : { CLAUDE_CODE_DISABLE_WORKFLOWS: raw };
    expect(isNativeWorkflowDisabledByEnv(env)).toBe(false);
  });
});

// ---------------------------------------------------------------------------
// Non-throwing readiness preflight
// ---------------------------------------------------------------------------

describe("preflightNativeWorkflow()", () => {
  it("ready when version meets the floor and no kill-switch", () => {
    const r = preflightNativeWorkflow("2.1.200", { env: {} });
    expect(r.ready).toBe(true);
    expect(r.reason).toBeUndefined();
    expect(r.detectedVersion).toBe("2.1.200");
  });

  it("not ready (version-below-floor) for a stale version", () => {
    const r = preflightNativeWorkflow("2.1.100", { env: {} });
    expect(r.ready).toBe(false);
    expect(r.reason).toBe("version-below-floor");
  });

  it("not ready (version-undetectable) when version is null", () => {
    const r = preflightNativeWorkflow(null, { env: {} });
    expect(r.ready).toBe(false);
    expect(r.reason).toBe("version-undetectable");
  });

  it("not ready (disabled-by-env) regardless of version", () => {
    const r = preflightNativeWorkflow("9.9.9", { env: { CLAUDE_CODE_DISABLE_WORKFLOWS: "1" } });
    expect(r.ready).toBe(false);
    expect(r.reason).toBe("disabled-by-env");
  });

  it("not ready (disabled-by-config) regardless of version", () => {
    const r = preflightNativeWorkflow("9.9.9", { env: {}, configDisabled: true });
    expect(r.ready).toBe(false);
    expect(r.reason).toBe("disabled-by-config");
  });
});

// ---------------------------------------------------------------------------
// Downgrade-signal path (AC: stale version downgrades to SdkFanoutRunner)
// ---------------------------------------------------------------------------

describe("runClaudeNativeWorkflow() downgrade signals", () => {
  const sink = () => new ArrayWorkflowEventSink();

  it("throws NativeWorkflowUnavailableError(disabled-by-env)", async () => {
    await expect(
      runClaudeNativeWorkflow(makeSpec(), sink(), {
        surface: "agent-sdk",
        detectedVersion: "9.9.9",
        env: { CLAUDE_CODE_DISABLE_WORKFLOWS: "true" },
      })
    ).rejects.toMatchObject({
      name: "NativeWorkflowUnavailableError",
      reason: "disabled-by-env",
    });
  });

  it("throws NativeWorkflowUnavailableError(disabled-by-config)", async () => {
    await expect(
      runClaudeNativeWorkflow(makeSpec(), sink(), {
        surface: "cli-ultracode",
        detectedVersion: "9.9.9",
        env: {},
        configDisabled: true,
      })
    ).rejects.toMatchObject({ reason: "disabled-by-config" });
  });

  it("throws NativeWorkflowUnavailableError(version-below-floor) for a stale version", async () => {
    const err = await runClaudeNativeWorkflow(makeSpec(), sink(), {
      surface: "agent-sdk",
      detectedVersion: "2.1.100",
      env: {},
    }).catch((e) => e);
    expect(err).toBeInstanceOf(NativeWorkflowUnavailableError);
    expect(err.reason).toBe("version-below-floor");
    expect(err.detectedVersion).toBe("2.1.100");
  });

  it("throws NativeWorkflowUnavailableError(version-undetectable) when version is null", async () => {
    await expect(
      runClaudeNativeWorkflow(makeSpec(), sink(), {
        surface: "cli-ultracode",
        detectedVersion: null,
        env: {},
      })
    ).rejects.toMatchObject({ reason: "version-undetectable" });
  });

  it("throws NativeWorkflowUnavailableError(api-surface-unavailable) at/above the floor (research preview)", async () => {
    // Version gate PASSES, kill-switches clear → the only remaining downgrade is
    // the research-preview native API being unavailable in the pinned binary.
    await expect(
      runClaudeNativeWorkflow(makeSpec(), sink(), {
        surface: "agent-sdk",
        detectedVersion: "2.1.154",
        env: {},
      })
    ).rejects.toMatchObject({ reason: "api-surface-unavailable" });
  });

  it("emits NOTHING on a downgrade — the floor owns the whole tree", async () => {
    const s = sink();
    await runClaudeNativeWorkflow(makeSpec(), s, {
      surface: "agent-sdk",
      detectedVersion: "2.1.100",
      env: {},
    }).catch(() => {});
    expect(s.getEvents()).toHaveLength(0);
  });

  it("throws a plain Error (not a downgrade) on an invalid spec", async () => {
    const bad = makeSpec({ phases: [] });
    const err = await runClaudeNativeWorkflow(bad, sink(), {
      surface: "agent-sdk",
      detectedVersion: "2.1.200",
      env: {},
    }).catch((e) => e);
    expect(err).toBeInstanceOf(Error);
    expect(err).not.toBeInstanceOf(NativeWorkflowUnavailableError);
    expect(String(err.message)).toContain("invalid WorkflowSpec");
  });
});

// ---------------------------------------------------------------------------
// Native usage mapping onto SubAgentNode.usage (REQUIRED, estimated flag)
// ---------------------------------------------------------------------------

describe("mapNativeUsage()", () => {
  it("carries real Claude usage with estimated:false", () => {
    const u = mapNativeUsage({ inputTokens: 1200, outputTokens: 340, costUsd: 0.045 });
    expect(u.inputTokens).toBe(1200);
    expect(u.outputTokens).toBe(340);
    expect(u.costUsd).toBe(0.045);
    expect(u.estimated).toBe(false);
  });

  it("zero-fills and flags estimated:true when no usage is reported", () => {
    const u = mapNativeUsage(undefined);
    expect(u.estimated).toBe(true);
    expect(u.inputTokens).toBe(0);
    expect(u.costUsd).toBe(0);
  });

  it("honors a provider-asserted estimate flag even with numbers", () => {
    const u = mapNativeUsage({ inputTokens: 10, estimated: true });
    expect(u.inputTokens).toBe(10);
    expect(u.estimated).toBe(true);
  });
});

// ---------------------------------------------------------------------------
// emitNativeWorkflowTree() — well-formed run/phase/agent tree via fake sink
// ---------------------------------------------------------------------------

describe("emitNativeWorkflowTree()", () => {
  const spec = makeSpec();

  const progress: NativeProgressEvent[] = [
    { kind: "phase-start", phaseIndex: 0 },
    { kind: "agent-start", phaseIndex: 0, agentIndex: 0 },
    { kind: "agent-start", phaseIndex: 0, agentIndex: 1 },
    {
      kind: "agent-end",
      phaseIndex: 0,
      agentIndex: 0,
      failed: false,
      usage: { inputTokens: 100, outputTokens: 20, costUsd: 0.01 },
      model: "claude-opus",
      outputRef: "ref-0",
    },
    { kind: "agent-end", phaseIndex: 0, agentIndex: 1, failed: false },
    { kind: "phase-end", phaseIndex: 0, failed: false },
  ];

  it("emits a root run with backend 'native-workflow' (open then terminal)", () => {
    const sink = new ArrayWorkflowEventSink();
    const status = emitNativeWorkflowTree(spec, sink, progress, "2.1.200");
    expect(status).toBe("succeeded");

    const runs = sink.getEvents().filter(isWorkflowRun) as WorkflowRun[];
    expect(runs).toHaveLength(2);
    expect(runs[0].status).toBe("running");
    expect(runs[1].status).toBe("succeeded");
    for (const r of runs) {
      expect(r.backend).toBe("native-workflow");
      expect(r.parentId).toBeNull();
      expect(r.schemaVersion).toBe(WORKFLOW_SCHEMA_VERSION);
    }
  });

  it("nests phases under the run and agents under the phase (correct parentId)", () => {
    const sink = new ArrayWorkflowEventSink();
    emitNativeWorkflowTree(spec, sink, progress, "2.1.200");
    const latest = sink.latestByNode();

    const runNode = latest.get("run:run-native-1")!;
    const phaseNode = latest.get("phase:run-native-1:0")!;
    const agent0 = latest.get("agent:run-native-1:0:0")! as SubAgentNode;
    const agent1 = latest.get("agent:run-native-1:0:1")! as SubAgentNode;

    expect(phaseNode.parentId).toBe(runNode.nodeId);
    expect(agent0.parentId).toBe(phaseNode.nodeId);
    expect(agent1.parentId).toBe(phaseNode.nodeId);
  });

  it("carries mapped native usage onto the terminal SubAgentNode", () => {
    const sink = new ArrayWorkflowEventSink();
    emitNativeWorkflowTree(spec, sink, progress, "2.1.200");
    const agents = sink.getEvents().filter(isSubAgentNode) as SubAgentNode[];
    const terminal0 = agents.find((a) => a.nodeId === "agent:run-native-1:0:0" && a.terminalKind)!;
    expect(terminal0.usage.inputTokens).toBe(100);
    expect(terminal0.usage.estimated).toBe(false);
    expect(terminal0.model).toBe("claude-opus");
    expect(terminal0.outputRef).toBe("ref-0");

    // The agent with no reported usage is zeroed + estimated:true (never exact-0).
    const terminal1 = agents.find((a) => a.nodeId === "agent:run-native-1:0:1" && a.terminalKind)!;
    expect(terminal1.usage.estimated).toBe(true);
  });

  it("produces strictly monotonic seq numbers", () => {
    const sink = new ArrayWorkflowEventSink();
    emitNativeWorkflowTree(spec, sink, progress, "2.1.200");
    const seqs = sink.getEvents().map((e) => e.seq);
    for (let i = 1; i < seqs.length; i++) {
      expect(seqs[i]).toBeGreaterThan(seqs[i - 1]);
    }
  });

  it("marks the run failed when any agent failed", () => {
    const sink = new ArrayWorkflowEventSink();
    const failing: NativeProgressEvent[] = [
      { kind: "phase-start", phaseIndex: 0 },
      { kind: "agent-start", phaseIndex: 0, agentIndex: 0 },
      { kind: "agent-end", phaseIndex: 0, agentIndex: 0, failed: true },
      { kind: "phase-end", phaseIndex: 0, failed: true },
    ];
    const status = emitNativeWorkflowTree(spec, sink, failing, "2.1.200");
    expect(status).toBe("failed");
    const runs = sink.getEvents().filter(isWorkflowRun) as WorkflowRun[];
    expect(runs[runs.length - 1].status).toBe("failed");
  });
});
