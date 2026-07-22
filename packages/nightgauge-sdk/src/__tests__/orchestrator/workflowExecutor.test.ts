/**
 * Tests for the WorkflowExecutor engine (#3908).
 *
 * Drives the executor with FAKE seams (a fake adapter, an in-memory JournalFs, a
 * fake clock, a fake quota provider) — no real CLIs, no real disk — and proves:
 *  (a) backend resolution: native offload only when capability + runWorkflow +
 *      prefer-offload + preflight all hold; otherwise graceful downgrade to the
 *      portable floor,
 *  (b) budget enforcement: the run stops spawning once the aggregated cost would
 *      exceed the cap and emits a deterministic budget-exceeded terminal,
 *  (c) durable journal: one record per emission is appended, and a kill-and-resume
 *      replays the tree and re-dispatches ONLY pending nodes,
 *  (d) sandboxed outputRef replay: a completed node's handle is carried opaquely
 *      and an injected path-traversal handle is rejected (never trusted),
 *  (e) quota gating: a large fan-out into an exhausted quota is deferred (#3909),
 *  (f) the absolute ceiling + config caps clamp the spec,
 *  (g) per-node usage is recorded on the TokenTracker, and
 *  (h) node-level liveness keeps a fresh fan-out from being SIGTERM'd.
 */

import { describe, it, expect } from "vitest";
import {
  WorkflowExecutor,
  OrchestrationDisabledError,
  ABSOLUTE_CEILING,
  MAX_OUTPUT_REF_BYTES,
  resolveBackend,
  clampSpecCeiling,
  sanitizeOutputRef,
  replayJournal,
  isRunLive,
  fitsUnderAbsoluteCeiling,
  type JournalFs,
  type Clock,
  type VersionPreflight,
  type WorkflowExecutorDeps,
} from "../../orchestrator/WorkflowExecutor.js";
import {
  WORKFLOW_SCHEMA_VERSION,
  zeroUsage,
  isSubAgentNode,
  CLAUDE_CEILING,
  FANOUT_CEILING,
  type WorkflowSpec,
  type WorkflowAgentUsage,
  type WorkflowExecutorBindings,
  type WorkflowEventSink,
  type WorkflowEvent,
  type WorkflowQuotaState,
  type ResolvedOrchestrationConfig,
  type OrchestrationStage,
} from "../../cli/workflow/index.js";
import type { ICliAdapter, IncrediAdapter } from "../../cli/adapters/ICliAdapter.js";
import { TokenTracker } from "../../tracking/TokenTracker.js";

// ---------------------------------------------------------------------------
// Fakes
// ---------------------------------------------------------------------------

const usage = (over: Partial<WorkflowAgentUsage> = {}): WorkflowAgentUsage => ({
  ...zeroUsage(true),
  inputTokens: 100,
  outputTokens: 50,
  costUsd: 0.01,
  ...over,
});

/** An in-memory append-only filesystem standing in for the journal on disk. */
class FakeFs implements JournalFs {
  files = new Map<string, string>();
  dirs = new Set<string>();

  async mkdir(dir: string): Promise<void> {
    this.dirs.add(dir);
  }
  async appendFile(file: string, data: string): Promise<void> {
    this.files.set(file, (this.files.get(file) ?? "") + data);
  }
  async readFile(file: string): Promise<string | null> {
    return this.files.get(file) ?? null;
  }
}

/** A clock returning a fixed (or scripted) ms + ISO. */
function fakeClock(ms = 1_000_000): Clock {
  return { now: () => ms, iso: () => new Date(ms).toISOString() };
}

/** A collecting sink for assertions. */
class CollectSink implements WorkflowEventSink {
  events: WorkflowEvent[] = [];
  emit(e: WorkflowEvent): void {
    this.events.push(e);
  }
}

const RESOLVED_OFF: ResolvedOrchestrationConfig = {
  disabled: true,
  prefer_native_offload: {},
  max_usd: 0,
  max_agents: 0,
  max_concurrency: 0,
};

const RESOLVED_ON: ResolvedOrchestrationConfig = {
  ...RESOLVED_OFF,
  disabled: false,
};

/** A minimal fake adapter; capability + runWorkflow presence are configurable. */
function fakeAdapter(opts: {
  capability: "native-workflow" | "sdk-fanout";
  withRunWorkflow?: boolean;
  onRunWorkflow?: ICliAdapter["runWorkflow"];
}): ICliAdapter {
  const adapter: ICliAdapter = {
    name: "claude-sdk" as IncrediAdapter,
    displayName: "Fake",
    cliCommand: "fake",
    agentic: true,
    async validateAuth() {
      return "passed";
    },
    async createQueryFunction() {
      return (async function* () {})() as never;
    },
    getDefaultArgs() {
      return [];
    },
    getOrchestrationCapability() {
      return opts.capability;
    },
    requiresDirectApiKey() {
      return false;
    },
  };
  if (opts.withRunWorkflow) {
    adapter.runWorkflow = opts.onRunWorkflow ?? (async () => {});
  }
  return adapter;
}

/** A passing bindings impl: every agent succeeds with `usage()`. */
function passingBindings(over?: Partial<WorkflowExecutorBindings>): WorkflowExecutorBindings {
  return {
    async runAgent() {
      return { usage: usage(), terminalKind: "success" as const };
    },
    async runJudge() {
      return { verdict: "pass" as const, usage: usage() };
    },
    ...over,
  };
}

/** A 1-phase spec with N agents (+ optional judges). */
function makeSpec(agents: number, over: Partial<WorkflowSpec> = {}, judges = 0): WorkflowSpec {
  return {
    schemaVersion: WORKFLOW_SCHEMA_VERSION,
    runId: "r1",
    issueNumber: 7,
    stage: "feature-dev",
    phases: [
      {
        name: "find",
        agents: Array.from({ length: agents }, (_, i) => ({
          agentId: `a${i}`,
          prompt: `p${i}`,
          provider: "codex",
        })),
        judges: Array.from({ length: judges }, (_, i) => ({ judgeId: `j${i}`, prompt: `j${i}` })),
      },
    ],
    ceiling: FANOUT_CEILING,
    ...over,
  };
}

function baseDeps(over: Partial<WorkflowExecutorDeps>): WorkflowExecutorDeps {
  return {
    adapter: fakeAdapter({ capability: "sdk-fanout" }),
    config: RESOLVED_ON,
    bindings: passingBindings(),
    fs: new FakeFs(),
    clock: fakeClock(),
    ...over,
  };
}

// ---------------------------------------------------------------------------
// Pure helpers
// ---------------------------------------------------------------------------

describe("WorkflowExecutor — pure helpers", () => {
  it("resolveBackend: native only when ALL four conditions hold", () => {
    expect(
      resolveBackend({
        capability: "native-workflow",
        hasRunWorkflow: true,
        prefersNativeOffload: true,
        preflightPassed: true,
      })
    ).toBe("native-offload");

    // any single missing condition → floor
    for (const miss of ["cap", "fn", "prefer", "preflight"] as const) {
      expect(
        resolveBackend({
          capability: miss === "cap" ? "sdk-fanout" : "native-workflow",
          hasRunWorkflow: miss !== "fn",
          prefersNativeOffload: miss !== "prefer",
          preflightPassed: miss !== "preflight",
        })
      ).toBe("sdk-fanout");
    }
  });

  it("clampSpecCeiling: clamps to the absolute ceiling and lowers with config caps", () => {
    const huge = makeSpec(1, { ceiling: { maxConcurrent: 9999, maxTotal: 9999 } });
    const clamped = clampSpecCeiling(huge, { max_agents: 0, max_concurrency: 0 });
    expect(clamped.ceiling).toEqual(ABSOLUTE_CEILING);

    // config caps only LOWER, never raise.
    const lowered = clampSpecCeiling(huge, { max_agents: 10, max_concurrency: 3 });
    expect(lowered.ceiling).toEqual({ maxConcurrent: 3, maxTotal: 10 });

    const raised = clampSpecCeiling(makeSpec(1, { ceiling: { maxConcurrent: 2, maxTotal: 4 } }), {
      max_agents: 5000,
      max_concurrency: 5000,
    });
    // never above the spec's own (already-small) ceiling
    expect(raised.ceiling).toEqual({ maxConcurrent: 2, maxTotal: 4 });
    // input spec is never mutated
    expect(huge.ceiling).toEqual({ maxConcurrent: 9999, maxTotal: 9999 });
  });

  it("fitsUnderAbsoluteCeiling: reports whether a planned fan-out fits the clamped ceiling", () => {
    const fit = fitsUnderAbsoluteCeiling(makeSpec(5, { ceiling: CLAUDE_CEILING }), {
      max_agents: 0,
      max_concurrency: 0,
    });
    expect(fit.fits).toBe(true);
    expect(fit.planned).toBe(5);

    const over = fitsUnderAbsoluteCeiling(makeSpec(20, { ceiling: CLAUDE_CEILING }), {
      max_agents: 10,
      max_concurrency: 0,
    });
    expect(over.fits).toBe(false);
    expect(over.clampedMaxTotal).toBe(10);
  });
});

// ---------------------------------------------------------------------------
// sanitizeOutputRef — the F-series security guarantee
// ---------------------------------------------------------------------------

describe("WorkflowExecutor — sanitizeOutputRef (untrusted handle)", () => {
  it("accepts a bounded opaque handle", () => {
    expect(sanitizeOutputRef("ref:abc123-DEF")).toBe("ref:abc123-DEF");
    expect(sanitizeOutputRef("a".repeat(100))).toBe("a".repeat(100));
  });

  it("rejects path-traversal, absolute, home, and UNC/drive-letter paths", () => {
    expect(sanitizeOutputRef("../etc/passwd")).toBeUndefined();
    expect(sanitizeOutputRef("/etc/passwd")).toBeUndefined();
    expect(sanitizeOutputRef("~/secrets")).toBeUndefined();
    expect(sanitizeOutputRef("a/../../b")).toBeUndefined();
    expect(sanitizeOutputRef("C:\\Windows\\system32")).toBeUndefined();
    expect(sanitizeOutputRef("\\\\host\\share")).toBeUndefined();
  });

  it("rejects whitespace, newlines, control chars, empty, oversize, and non-strings", () => {
    expect(sanitizeOutputRef("has space")).toBeUndefined();
    expect(sanitizeOutputRef("line1\nline2")).toBeUndefined();
    expect(sanitizeOutputRef("nul byte")).toBeUndefined();
    expect(sanitizeOutputRef("")).toBeUndefined();
    expect(sanitizeOutputRef("x".repeat(MAX_OUTPUT_REF_BYTES + 1))).toBeUndefined();
    expect(sanitizeOutputRef(42 as unknown)).toBeUndefined();
    expect(sanitizeOutputRef(undefined)).toBeUndefined();
    expect(sanitizeOutputRef({ evil: true } as unknown)).toBeUndefined();
  });
});

// ---------------------------------------------------------------------------
// Backend resolution end-to-end
// ---------------------------------------------------------------------------

describe("WorkflowExecutor — backend resolution", () => {
  it("refuses to run when orchestration is disabled", async () => {
    const exec = new WorkflowExecutor(baseDeps({ config: RESOLVED_OFF }));
    await expect(exec.execute(makeSpec(1), new CollectSink())).rejects.toBeInstanceOf(
      OrchestrationDisabledError
    );
  });

  it("downgrades to the floor when the adapter is not native-workflow", async () => {
    const exec = new WorkflowExecutor(
      baseDeps({ adapter: fakeAdapter({ capability: "sdk-fanout", withRunWorkflow: true }) })
    );
    const res = await exec.execute(makeSpec(2), new CollectSink());
    expect(res.backend).toBe("sdk-fanout");
    expect(res.summary.agentCount).toBe(2);
  });

  it("downgrades to the floor when prefer-offload is on but the preflight FAILS", async () => {
    const config: ResolvedOrchestrationConfig = {
      ...RESOLVED_ON,
      prefer_native_offload: { "feature-dev": true } as Partial<
        Record<OrchestrationStage, boolean>
      >,
    };
    const denyPreflight: VersionPreflight = () => false;
    let ranWorkflow = false;
    const exec = new WorkflowExecutor(
      baseDeps({
        config,
        adapter: fakeAdapter({
          capability: "native-workflow",
          withRunWorkflow: true,
          onRunWorkflow: async () => {
            ranWorkflow = true;
          },
        }),
        versionPreflight: denyPreflight,
      })
    );
    const res = await exec.execute(makeSpec(2), new CollectSink());
    expect(res.backend).toBe("sdk-fanout");
    expect(ranWorkflow).toBe(false);
  });

  it("offloads to the native backend when capability + runWorkflow + prefer + preflight all hold", async () => {
    const config: ResolvedOrchestrationConfig = {
      ...RESOLVED_ON,
      prefer_native_offload: { "feature-dev": true } as Partial<
        Record<OrchestrationStage, boolean>
      >,
    };
    // The native adapter emits the SAME canonical tree the floor would.
    const onRunWorkflow: ICliAdapter["runWorkflow"] = async (spec, sink) => {
      const runNodeId = `run:${spec.runId}`;
      sink.emit({
        schemaVersion: WORKFLOW_SCHEMA_VERSION,
        kind: "run",
        nodeId: runNodeId,
        parentId: null,
        seq: 0,
        ts: new Date().toISOString(),
        status: "running",
        runId: spec.runId,
        backend: "native-workflow",
        startedAt: new Date().toISOString(),
      });
      sink.emit({
        schemaVersion: WORKFLOW_SCHEMA_VERSION,
        kind: "agent",
        nodeId: `agent:${spec.runId}:0:0`,
        parentId: runNodeId,
        seq: 1,
        ts: new Date().toISOString(),
        status: "succeeded",
        agentId: "a0",
        provider: "claude",
        usage: usage(),
        terminalKind: "success",
      });
      sink.emit({
        schemaVersion: WORKFLOW_SCHEMA_VERSION,
        kind: "run",
        nodeId: runNodeId,
        parentId: null,
        seq: 2,
        ts: new Date().toISOString(),
        status: "succeeded",
        runId: spec.runId,
        backend: "native-workflow",
        startedAt: new Date().toISOString(),
        finishedAt: new Date().toISOString(),
      });
    };
    const sink = new CollectSink();
    const exec = new WorkflowExecutor(
      baseDeps({
        config,
        adapter: fakeAdapter({
          capability: "native-workflow",
          withRunWorkflow: true,
          onRunWorkflow,
        }),
        versionPreflight: () => true,
      })
    );
    const res = await exec.execute(makeSpec(1), sink);
    expect(res.backend).toBe("native-offload");
    expect(res.summary.agentCount).toBe(1);
    expect(res.summary.agentsSucceeded).toBe(1);
    // The native emissions were journaled identically (run + agent + run).
    const journal = (exec as unknown as { fs: FakeFs }).fs;
    const raw = await journal.readFile(exec.journalPathFor("r1"));
    expect(raw).not.toBeNull();
    const { latestByNode } = replayJournal(raw!);
    expect(latestByNode.size).toBe(2); // run + agent
  });

  it("a spec.preferNativeOffload=true overrides config and offloads (preflight passing)", async () => {
    let ran = false;
    const exec = new WorkflowExecutor(
      baseDeps({
        adapter: fakeAdapter({
          capability: "native-workflow",
          withRunWorkflow: true,
          onRunWorkflow: async (spec, sink) => {
            ran = true;
            sink.emit({
              schemaVersion: WORKFLOW_SCHEMA_VERSION,
              kind: "run",
              nodeId: `run:${spec.runId}`,
              parentId: null,
              seq: 0,
              ts: new Date().toISOString(),
              status: "succeeded",
              runId: spec.runId,
              backend: "native-workflow",
              startedAt: new Date().toISOString(),
              finishedAt: new Date().toISOString(),
            });
          },
        }),
        versionPreflight: () => true,
      })
    );
    const res = await exec.execute(makeSpec(1, { preferNativeOffload: true }), new CollectSink());
    expect(res.backend).toBe("native-offload");
    expect(ran).toBe(true);
  });
});

// ---------------------------------------------------------------------------
// Budget enforcement
// ---------------------------------------------------------------------------

describe("WorkflowExecutor — budget enforcement", () => {
  it("stops spawning and emits budget-exceeded terminals once the cap is reached", async () => {
    // Each agent costs 0.01; budget 0.025 → first 2 succeed (0.02), the 3rd's
    // cost would push to 0.03 > 0.025 → budget-exceeded; subsequent ones don't run.
    const sink = new CollectSink();
    const exec = new WorkflowExecutor(
      baseDeps({
        // sequential (maxConcurrent 1) makes the accrual order deterministic
        bindings: passingBindings(),
      })
    );
    const spec = makeSpec(5, {
      budgetUsd: 0.025,
      ceiling: { maxConcurrent: 1, maxTotal: 32 },
    });
    const res = await exec.execute(spec, sink);
    expect(res.budgetStopped).toBe(true);

    const terminals = sink.events.filter((e) => isSubAgentNode(e) && e.status !== "running");
    const succeeded = terminals.filter((e) => e.status === "succeeded");
    const overBudget = terminals.filter(
      (e) => isSubAgentNode(e) && e.terminalKind === "budget-exceeded"
    );
    expect(succeeded.length).toBe(2);
    expect(overBudget.length).toBeGreaterThanOrEqual(1);
    // Total cost never exceeds the cap.
    expect(res.totalCostUsd).toBeLessThanOrEqual(0.025);
  });

  it("config max_usd applies when the spec sets no budget", async () => {
    const sink = new CollectSink();
    const config: ResolvedOrchestrationConfig = { ...RESOLVED_ON, max_usd: 0.015 };
    const exec = new WorkflowExecutor(baseDeps({ config }));
    const spec = makeSpec(4, { ceiling: { maxConcurrent: 1, maxTotal: 32 } });
    const res = await exec.execute(spec, sink);
    expect(res.budgetStopped).toBe(true);
    expect(res.totalCostUsd).toBeLessThanOrEqual(0.015);
  });

  it("no budget cap → every agent runs", async () => {
    const exec = new WorkflowExecutor(baseDeps({}));
    const res = await exec.execute(
      makeSpec(6, { ceiling: { maxConcurrent: 3, maxTotal: 32 } }),
      new CollectSink()
    );
    expect(res.budgetStopped).toBe(false);
    expect(res.summary.agentsSucceeded).toBe(6);
  });
});

// ---------------------------------------------------------------------------
// Durable journal + resume
// ---------------------------------------------------------------------------

describe("WorkflowExecutor — durable journal + resume", () => {
  it("writes one journal record per emission", async () => {
    const fs = new FakeFs();
    const sink = new CollectSink();
    const exec = new WorkflowExecutor(baseDeps({ fs }));
    await exec.execute(makeSpec(2), sink);
    const raw = await fs.readFile(exec.journalPathFor("r1"));
    const lines = raw!.trim().split("\n");
    // One journal line per emitted event.
    expect(lines.length).toBe(sink.events.length);
    for (const line of lines) {
      const rec = JSON.parse(line);
      expect(rec.event.schemaVersion).toBe(WORKFLOW_SCHEMA_VERSION);
      expect(typeof rec.heartbeatMs).toBe("number");
    }
  });

  it("resume re-dispatches ONLY pending nodes; completed agents replay and DO NOT re-run", async () => {
    // Simulate a crash: the first run completes agent a0 but the process is
    // "killed" before a1 ran. We hand-craft a journal with a0 terminal and a1
    // never emitted, then resume.
    const fs = new FakeFs();
    const journalPath = ".nightgauge/pipeline/workflow-r1.jsonl";
    const hb = 5_000;
    const rec = (event: WorkflowEvent): string => JSON.stringify({ event, heartbeatMs: hb }) + "\n";
    const ts = new Date().toISOString();
    const lines =
      rec({
        schemaVersion: WORKFLOW_SCHEMA_VERSION,
        kind: "run",
        nodeId: "run:r1",
        parentId: null,
        seq: 0,
        ts,
        status: "running",
        runId: "r1",
        backend: "sdk-fanout",
        startedAt: ts,
      }) +
      rec({
        schemaVersion: WORKFLOW_SCHEMA_VERSION,
        kind: "agent",
        nodeId: "agent:r1:0:0",
        parentId: "run:r1",
        seq: 1,
        ts,
        status: "succeeded",
        agentId: "a0",
        provider: "codex",
        usage: usage({ costUsd: 0.5 }),
        terminalKind: "success",
        outputRef: "ref:done-a0",
      });
    fs.files.set(journalPath, lines);

    // On resume, runAgent should be called ONLY for a1, never a0.
    const ran: string[] = [];
    const bindings: WorkflowExecutorBindings = {
      async runAgent(a) {
        ran.push(a.agentId);
        return { usage: usage(), terminalKind: "success" as const, outputRef: "ref:fresh" };
      },
      async runJudge() {
        return { verdict: "pass" as const, usage: usage() };
      },
    };

    const sink = new CollectSink();
    const exec = new WorkflowExecutor(baseDeps({ fs, bindings }));
    const res = await exec.resume("r1", makeSpec(2), sink);

    // a0 replayed (not re-run); a1 freshly dispatched.
    expect(ran).toEqual(["a1"]);
    // The resumed agent a0 carries its replayed (sanitized) outputRef.
    const a0Terminal = sink.events.find(
      (e) => isSubAgentNode(e) && e.agentId === "a0" && e.status === "succeeded"
    );
    expect(a0Terminal && isSubAgentNode(a0Terminal) && a0Terminal.outputRef).toBe("ref:done-a0");
    expect(res.summary.agentsSucceeded).toBeGreaterThanOrEqual(1);
  });

  it("resume rejects a poisoned (path-traversal) outputRef — never trusts it", async () => {
    const fs = new FakeFs();
    const journalPath = ".nightgauge/pipeline/workflow-r1.jsonl";
    const ts = new Date().toISOString();
    const poisoned =
      JSON.stringify({
        event: {
          schemaVersion: WORKFLOW_SCHEMA_VERSION,
          kind: "agent",
          nodeId: "agent:r1:0:0",
          parentId: "run:r1",
          seq: 1,
          ts,
          status: "succeeded",
          agentId: "a0",
          provider: "codex",
          usage: usage(),
          terminalKind: "success",
          outputRef: "../../etc/passwd",
        },
        heartbeatMs: 1,
      }) + "\n";
    fs.files.set(journalPath, poisoned);

    const bindings: WorkflowExecutorBindings = {
      async runAgent() {
        // a1 (the pending one) runs; a0 is replayed by the engine.
        return { usage: usage(), terminalKind: "success" as const };
      },
      async runJudge() {
        return { verdict: "pass" as const, usage: usage() };
      },
    };

    const sink = new CollectSink();
    const exec = new WorkflowExecutor(baseDeps({ fs, bindings }));
    await exec.resume("r1", makeSpec(2), sink);

    // The replayed a0 terminal must carry NO outputRef (the traversal handle was
    // rejected), proving the engine never trusted the poisoned value.
    const a0 = sink.events.find(
      (e) => isSubAgentNode(e) && e.agentId === "a0" && e.status === "succeeded"
    );
    expect(a0 && isSubAgentNode(a0) ? a0.outputRef : undefined).toBeUndefined();
  });

  it("resume throws when no journal exists for the run", async () => {
    const exec = new WorkflowExecutor(baseDeps({ fs: new FakeFs() }));
    await expect(exec.resume("missing", makeSpec(1), new CollectSink())).rejects.toThrow(
      /no journal to resume/i
    );
  });
});

// ---------------------------------------------------------------------------
// Quota gating (#3909 passthrough)
// ---------------------------------------------------------------------------

describe("WorkflowExecutor — quota gating", () => {
  const exhausted = (over: Partial<WorkflowQuotaState> = {}): WorkflowQuotaState => ({
    remaining: 0,
    limit: 5000,
    resetsAt: 0,
    exhausted: true,
    ...over,
  });

  it("defers a LARGE fan-out into an exhausted quota (no agents spawned)", async () => {
    let agentRuns = 0;
    const bindings: WorkflowExecutorBindings = {
      async runAgent() {
        agentRuns++;
        return { usage: usage(), terminalKind: "success" as const };
      },
      async runJudge() {
        return { verdict: "pass" as const, usage: usage() };
      },
    };
    // 16 agents == DEFAULT_LARGE_FANOUT_THRESHOLD → gated; exhausted → deferred.
    const spec = makeSpec(16, { ceiling: CLAUDE_CEILING });
    const exec = new WorkflowExecutor(
      baseDeps({
        bindings,
        quotaProvider: async () => exhausted({ cooldownUntil: "2099-01-01T00:00:00Z" }),
      })
    );
    const res = await exec.execute(spec, new CollectSink());
    expect(res.quotaDeferred).toBe(true);
    expect(res.summary.status).toBe("skipped");
    expect(agentRuns).toBe(0);
  });

  it("proceeds for a SMALL fan-out even when exhausted (not gated)", async () => {
    const spec = makeSpec(2, { ceiling: FANOUT_CEILING });
    const exec = new WorkflowExecutor(baseDeps({ quotaProvider: async () => exhausted() }));
    const res = await exec.execute(spec, new CollectSink());
    expect(res.quotaDeferred).toBe(false);
    expect(res.summary.agentsSucceeded).toBe(2);
  });

  it("fails OPEN when the quota provider throws (transient bridge outage)", async () => {
    const spec = makeSpec(16, { ceiling: CLAUDE_CEILING });
    const exec = new WorkflowExecutor(
      baseDeps({
        quotaProvider: async () => {
          throw new Error("ipc down");
        },
      })
    );
    const res = await exec.execute(spec, new CollectSink());
    expect(res.quotaDeferred).toBe(false);
    expect(res.summary.agentsSucceeded).toBe(16);
  });
});

// ---------------------------------------------------------------------------
// TokenTracker roll-up + liveness
// ---------------------------------------------------------------------------

describe("WorkflowExecutor — token roll-up + liveness", () => {
  it("records per-node usage on the TokenTracker", async () => {
    const tracker = new TokenTracker();
    const exec = new WorkflowExecutor(baseDeps({ tokenTracker: tracker }));
    await exec.execute(
      makeSpec(3, { ceiling: { maxConcurrent: 3, maxTotal: 32 } }, 1),
      new CollectSink()
    );
    const nodes = tracker.getAllWorkflowNodeUsage();
    // 3 agents + 1 judge each recorded once on terminal.
    expect(nodes.length).toBe(4);
    expect(tracker.getWorkflowCostUsd()).toBeCloseTo(0.04, 5);
    for (const n of nodes) {
      expect(n.stage).toBe("feature-dev");
      expect(n.costUsd).toBeGreaterThan(0);
    }
  });

  it("isRunLive: a fresh heartbeat with running nodes is LIVE (must not be SIGTERM'd)", () => {
    const ts = new Date().toISOString();
    const raw =
      JSON.stringify({
        event: {
          schemaVersion: WORKFLOW_SCHEMA_VERSION,
          kind: "agent",
          nodeId: "agent:r1:0:0",
          parentId: "run:r1",
          seq: 1,
          ts,
          status: "running",
          agentId: "a0",
          provider: "codex",
          usage: zeroUsage(),
        },
        heartbeatMs: 10_000,
      }) + "\n";

    // now=10_500, staleAfter=1_000 → age 500ms, one running node → LIVE.
    expect(isRunLive(raw, 10_500, 1_000)).toMatchObject({ live: true, runningNodeCount: 1 });
    // now far ahead → stale → NOT live even though a node is "running".
    expect(isRunLive(raw, 100_000, 1_000).live).toBe(false);
  });

  it("isRunLive: a completed run (no running nodes) is NOT live", () => {
    const ts = new Date().toISOString();
    const raw =
      JSON.stringify({
        event: {
          schemaVersion: WORKFLOW_SCHEMA_VERSION,
          kind: "agent",
          nodeId: "agent:r1:0:0",
          parentId: "run:r1",
          seq: 2,
          ts,
          status: "succeeded",
          agentId: "a0",
          provider: "codex",
          usage: usage(),
          terminalKind: "success",
        },
        heartbeatMs: 10_000,
      }) + "\n";
    expect(isRunLive(raw, 10_100, 1_000).live).toBe(false);
  });
});

// ---------------------------------------------------------------------------
// Absolute ceiling
// ---------------------------------------------------------------------------

describe("WorkflowExecutor — absolute ceiling", () => {
  it("rejects a spec whose planned fan-out exceeds the config-tightened ceiling", async () => {
    const config: ResolvedOrchestrationConfig = { ...RESOLVED_ON, max_agents: 3 };
    const exec = new WorkflowExecutor(baseDeps({ config }));
    // 5 agents but max_agents 3 → clamped maxTotal 3 → planned 5 > 3 → rejected.
    await expect(
      exec.execute(makeSpec(5, { ceiling: CLAUDE_CEILING }), new CollectSink())
    ).rejects.toThrow(/invalid WorkflowSpec|exceeds ceiling/i);
  });
});
