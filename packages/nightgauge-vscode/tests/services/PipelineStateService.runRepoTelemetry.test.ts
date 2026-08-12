/**
 * Tests that PipelineStateService threads the run's repo into every stage
 * transition and signals pipeline completion — the run-creation context the Go
 * IPC layer needs to materialise/finalise the platform's live pipeline_runs row.
 *
 * Regression guard for the "No pipeline runs yet" bug: extension/Headless
 * orchestrator runs sent stage transitions with an empty repo, so the platform
 * never materialised a run. beginRun must propagate the repo to the IPC payload.
 */

import { describe, it, expect, vi, beforeEach } from "vitest";
import { uuidV7 } from "@nightgauge/sdk";

// Capture every ipc.call(method, params) — resolving so the service takes the
// real IPC path (not the local-fallback catch branch).
const ipcCalls: Array<{ method: string; params: Record<string, unknown> }> = [];

vi.mock("../../src/services/IpcClient", () => ({
  IpcClient: {
    getInstance: () => ({
      on: vi.fn(() => ({ dispose: vi.fn() })),
      call: vi.fn((method: string, params: Record<string, unknown>) => {
        ipcCalls.push({ method, params });
        return Promise.resolve({ status: "ok" });
      }),
    }),
  },
}));

vi.mock("vscode", () => ({
  EventEmitter: class {
    private _handlers: Array<(v: unknown) => void> = [];
    event = (cb: (v: unknown) => void) => {
      this._handlers.push(cb);
      return { dispose: () => {} };
    };
    fire(value: unknown) {
      for (const h of this._handlers) h(value);
    }
    dispose() {}
  },
  Disposable: class {
    dispose() {}
  },
  window: {
    createOutputChannel: vi.fn(() => ({
      appendLine: vi.fn(),
      show: vi.fn(),
      clear: vi.fn(),
      dispose: vi.fn(),
    })),
  },
}));

async function makeService(issueNumber: number, repo = "nightgauge/acmeapp") {
  const { PipelineStateService } = await import("../../src/services/PipelineStateService");
  PipelineStateService.resetInstance();
  const svc = PipelineStateService.createForWorktree("/tmp/repo", issueNumber);
  // ADR-017 step 3 (#370): `beginRun` replaced `setRunRepo`. The repo is now
  // an attribute installed WITH the identity, never on its own — a repo with
  // no run to attach it to is what let `setPaused` mint an unattributed stub.
  svc.beginRun(uuidV7(), repo, issueNumber);
  return svc;
}

function callsTo(method: string) {
  return ipcCalls.filter((c) => c.method === method);
}

describe("PipelineStateService — run repo + completion telemetry", () => {
  beforeEach(() => {
    ipcCalls.length = 0;
  });

  it("threads the installed run repo into stage transitions (start/complete/fail)", async () => {
    const svc = await makeService(153);

    await svc.startStage("issue-pickup");
    await svc.completeStage("issue-pickup");
    await svc.failStage("feature-dev", "boom");

    const transitions = callsTo("pipeline.notifyStageTransition");
    expect(transitions.length).toBe(3);
    for (const t of transitions) {
      expect(t.params.repo).toBe("nightgauge/acmeapp");
      expect(t.params.issueNumber).toBe(153);
    }
    expect(transitions[0].params.status).toBe("running");
    expect(transitions[1].params.status).toBe("complete");
    expect(transitions[2].params.status).toBe("failed");
  });

  it("completeStage forwards the served model + adapter attribution (#268)", async () => {
    const svc = await makeService(268);

    await svc.completeStage("feature-dev", { model: "claude-opus-4-8", adapter: "claude" });

    const complete = callsTo("pipeline.notifyStageTransition").find(
      (c) => c.params.status === "complete"
    );
    expect(complete).toBeDefined();
    expect(complete?.params.model).toBe("claude-opus-4-8");
    expect(complete?.params.adapter).toBe("claude");
  });

  it("completeStage forwards the cache-write TTL split (#390)", async () => {
    const svc = await makeService(390);
    svc.initEmpty();
    await svc.updateTokens({
      stage: "feature-dev",
      inputTokens: 18,
      outputTokens: 236,
      cacheReadTokens: 29622,
      cacheCreationTokens: 3308,
      cacheCreation5mTokens: 0,
      cacheCreation1hTokens: 3308,
      costUsd: 0.01,
    });

    await svc.completeStage("feature-dev");

    const complete = callsTo("pipeline.notifyStageTransition").find(
      (c) => c.params.status === "complete"
    );
    expect(complete?.params).toMatchObject({
      cacheCreationTokens: 3308,
      cacheCreation5mTokens: 0,
      cacheCreation1hTokens: 3308,
    });
  });

  it("sends only the unbooked retry delta after a failed attempt (#390)", async () => {
    const svc = await makeService(390);
    svc.initEmpty();
    await svc.updateTokens({
      stage: "feature-dev",
      inputTokens: 10,
      outputTokens: 20,
      cacheCreationTokens: 100,
      cacheCreation1hTokens: 100,
      costUsd: 0.01,
    });
    await svc.failStage("feature-dev", "retryable");

    await svc.updateTokens({
      stage: "feature-dev",
      inputTokens: 5,
      outputTokens: 8,
      cacheCreationTokens: 50,
      cacheCreation1hTokens: 50,
      costUsd: 0.005,
    });
    await svc.completeStage("feature-dev");

    const terminals = callsTo("pipeline.notifyStageTransition").filter(
      (c) => c.params.status === "failed" || c.params.status === "complete"
    );
    expect(terminals).toHaveLength(2);
    expect(terminals[0].params).toMatchObject({
      inputTokens: 10,
      outputTokens: 20,
      cacheCreationTokens: 100,
      cacheCreation1hTokens: 100,
    });
    expect(terminals[1].params).toMatchObject({
      inputTokens: 5,
      outputTokens: 8,
      cacheCreationTokens: 50,
      cacheCreation1hTokens: 50,
    });
  });

  it("completeStage omits model/adapter keys when no attribution is passed (#268)", async () => {
    const svc = await makeService(268);

    await svc.completeStage("feature-dev");

    const complete = callsTo("pipeline.notifyStageTransition").find(
      (c) => c.params.status === "complete"
    );
    expect(complete).toBeDefined();
    // Absent attribution must not put empty model/adapter keys on the wire —
    // the Go recorders treat absence as "unknown", never a defaulted value.
    expect("model" in (complete?.params ?? {})).toBe(false);
    expect("adapter" in (complete?.params ?? {})).toBe(false);
  });

  it("initializePipeline carries the run repo (not an empty string)", async () => {
    const svc = await makeService(42, "nightgauge/nightgauge");

    await svc.initializePipeline(42, "Title", "feat/42");

    const init = callsTo("pipeline.notifyStageTransition").find(
      (c) => c.params.status === "initialized"
    );
    expect(init).toBeDefined();
    expect(init?.params.repo).toBe("nightgauge/nightgauge");
  });

  it("notifyPipelineComplete sends the terminal pipeline_done signal", async () => {
    const svc = await makeService(153);

    await svc.notifyPipelineComplete({
      success: true,
      totalDurationMs: 99000,
      stagesRun: ["issue-pickup", "feature-dev", "pr-merge"],
    });

    const done = callsTo("pipeline.notifyComplete");
    expect(done.length).toBe(1);
    expect(done[0].params).toMatchObject({
      repo: "nightgauge/acmeapp",
      issueNumber: 153,
      success: true,
      totalDurationMs: 99000,
      stagesRun: ["issue-pickup", "feature-dev", "pr-merge"],
    });
  });

  it("sends an empty repo when the dispatch could not resolve one", async () => {
    // A dispatch with no resolvable owner/name is a real state, not an
    // error: the manager logs it and the run is reported unattributed. What
    // must NOT happen is a fabricated repo — the platform would materialise
    // the run row against the wrong repository.
    const svc = await makeService(7, "");
    await svc.startStage("issue-pickup");
    const t = callsTo("pipeline.notifyStageTransition")[0];
    expect(t.params.repo).toBe("");
  });
});
