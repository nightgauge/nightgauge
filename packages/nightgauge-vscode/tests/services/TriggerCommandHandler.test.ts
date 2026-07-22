/**
 * TriggerCommandHandler.test.ts
 *
 * Unit tests for TriggerCommandHandler — title fetch, ack dispatch, concurrent
 * guard, enqueue (with repoOverride), pipeline start, error paths, the
 * agentId setter, and (workspaceManager-aware) pre-ack repo resolution.
 *
 * @see Issue #3551 — Handle trigger command ack and start pipeline
 * @see Issue #4118 — Trigger acked but never ran because the issue was never enqueued
 * @see Issue #4117 — Resolve the target repo against the open workspace before
 *   ack/enqueue in multi-root .code-workspace setups
 */

import { describe, it, expect, vi, beforeEach } from "vitest";

vi.mock("vscode", () => ({
  window: {
    createOutputChannel: vi.fn(() => ({
      appendLine: vi.fn(),
      show: vi.fn(),
      clear: vi.fn(),
      dispose: vi.fn(),
    })),
  },
}));

import { TriggerCommandHandler } from "../../src/services/TriggerCommandHandler";
import type { ReceivedCommand } from "../../src/services/AgentCommandStreamService";

// ── Minimal mock builders ─────────────────────────────────────────────────────

function makeLogger() {
  return {
    info: vi.fn(),
    warn: vi.fn(),
    error: vi.fn(),
    debug: vi.fn(),
  };
}

function makeIpcClient(runId = "run-abc") {
  return {
    agentAcknowledgeCommand: vi.fn().mockResolvedValue({ runId }),
    issueView: vi
      .fn()
      .mockResolvedValue({ number: 42, title: "Real issue title", labels: ["type:feature"] }),
  };
}

function makeConcurrentManager(isRunning = false) {
  return {
    isRunning: vi.fn().mockReturnValue(isRunning),
    fillSlots: vi.fn().mockResolvedValue(1),
    setPendingRunId: vi.fn(),
    clearPendingRunId: vi.fn(),
  };
}

function makeQueueService() {
  return {
    enqueue: vi.fn().mockResolvedValue({ issueNumber: 42, position: 0 }),
  };
}

function makeTriggerCmd(issueNumber = 42, commandId = "cmd-1"): ReceivedCommand {
  return {
    id: commandId,
    type: "trigger",
    // The platform publishes SEPARATE owner + repo (not a combined slug).
    payload: { owner: "nightgauge", repo: "nightgauge", issueNumber },
    createdAt: new Date().toISOString(),
  };
}

/** Mock WorkspaceManager — only findRepositoryByGitHub is used by the handler. */
function makeWorkspaceManager(found: unknown = { path: "/test-repo" }) {
  return {
    findRepositoryByGitHub: vi.fn().mockReturnValue(found),
  };
}

// ── Tests ─────────────────────────────────────────────────────────────────────

describe("TriggerCommandHandler", () => {
  let ipcClient: ReturnType<typeof makeIpcClient>;
  let concurrentManager: ReturnType<typeof makeConcurrentManager>;
  let queueService: ReturnType<typeof makeQueueService>;
  let logger: ReturnType<typeof makeLogger>;
  let handler: TriggerCommandHandler;

  function build(): TriggerCommandHandler {
    const h = new TriggerCommandHandler(
      ipcClient as never,
      concurrentManager as never,
      queueService as never,
      logger as never
    );
    h.setAgentId("agent-1");
    return h;
  }

  beforeEach(() => {
    ipcClient = makeIpcClient();
    concurrentManager = makeConcurrentManager();
    queueService = makeQueueService();
    logger = makeLogger();
    handler = build();
  });

  it("ignores non-trigger commands", () => {
    const cmd: ReceivedCommand = { id: "c", type: "heartbeat", payload: {}, createdAt: "" };
    handler.handle(cmd);
    expect(ipcClient.agentAcknowledgeCommand).not.toHaveBeenCalled();
    expect(queueService.enqueue).not.toHaveBeenCalled();
    expect(concurrentManager.fillSlots).not.toHaveBeenCalled();
  });

  it("happy path: fetches title, acks, enqueues with repoOverride, then fills slots", async () => {
    const cmd = makeTriggerCmd(10);
    handler.handle(cmd);

    await vi.waitFor(() => expect(ipcClient.agentAcknowledgeCommand).toHaveBeenCalledTimes(1));
    expect(ipcClient.issueView).toHaveBeenCalledWith("nightgauge", "nightgauge", 10);
    expect(ipcClient.agentAcknowledgeCommand).toHaveBeenCalledWith("agent-1", "cmd-1");

    await vi.waitFor(() => expect(queueService.enqueue).toHaveBeenCalledTimes(1));
    expect(queueService.enqueue).toHaveBeenCalledWith(
      10,
      "Real issue title",
      ["type:feature"],
      undefined,
      {
        repoOverride: { owner: "nightgauge", repo: "nightgauge" },
        remoteRunId: "run-abc",
      }
    );

    await vi.waitFor(() => expect(concurrentManager.fillSlots).toHaveBeenCalledTimes(1));

    // Pending runId must be set BEFORE enqueue so the slot adopts it on open.
    expect(concurrentManager.setPendingRunId).toHaveBeenCalledWith(10, "run-abc");
    const setOrder = concurrentManager.setPendingRunId.mock.invocationCallOrder[0];
    const enqOrder = queueService.enqueue.mock.invocationCallOrder[0];
    expect(setOrder).toBeLessThan(enqOrder);

    expect(logger.info).toHaveBeenCalledWith(
      expect.stringContaining("ack succeeded"),
      expect.objectContaining({ issueNumber: 10 })
    );
  });

  it("uses a placeholder title when issueView fails but still enqueues", async () => {
    ipcClient.issueView.mockRejectedValue(new Error("gh rate limited"));
    const cmd = makeTriggerCmd(77);
    handler.handle(cmd);

    await vi.waitFor(() => expect(queueService.enqueue).toHaveBeenCalledTimes(1));
    expect(queueService.enqueue).toHaveBeenCalledWith(77, "Issue #77", [], undefined, {
      repoOverride: { owner: "nightgauge", repo: "nightgauge" },
      remoteRunId: "run-abc",
    });
    await vi.waitFor(() => expect(concurrentManager.fillSlots).toHaveBeenCalledTimes(1));
    expect(logger.warn).toHaveBeenCalledWith(
      expect.stringContaining("issueView failed"),
      expect.any(Object)
    );
  });

  it("rejects concurrent trigger without calling ack or enqueue", async () => {
    concurrentManager = makeConcurrentManager(true); // issue already running
    handler = build();

    const cmd = makeTriggerCmd(42);
    handler.handle(cmd);
    await vi.waitFor(() =>
      expect(logger.warn).toHaveBeenCalledWith(
        expect.stringContaining("concurrent trigger rejected"),
        expect.any(Object)
      )
    );
    expect(ipcClient.agentAcknowledgeCommand).not.toHaveBeenCalled();
    expect(queueService.enqueue).not.toHaveBeenCalled();
    expect(concurrentManager.fillSlots).not.toHaveBeenCalled();
  });

  it("does not enqueue or start pipeline when ack fails", async () => {
    ipcClient.agentAcknowledgeCommand.mockRejectedValue(new Error("network error"));
    const cmd = makeTriggerCmd(42);
    handler.handle(cmd);
    await vi.waitFor(() =>
      expect(logger.error).toHaveBeenCalledWith(
        expect.stringContaining("ack failed"),
        expect.any(Object)
      )
    );
    expect(queueService.enqueue).not.toHaveBeenCalled();
    expect(concurrentManager.fillSlots).not.toHaveBeenCalled();
  });

  it("clears the pending runId and does not fill slots when enqueue is refused", async () => {
    queueService.enqueue.mockResolvedValue(null); // e.g. stop-in-progress guard
    const cmd = makeTriggerCmd(42);
    handler.handle(cmd);
    await vi.waitFor(() =>
      expect(logger.error).toHaveBeenCalledWith(
        expect.stringContaining("enqueue refused"),
        expect.any(Object)
      )
    );
    expect(concurrentManager.clearPendingRunId).toHaveBeenCalledWith(42);
    expect(concurrentManager.fillSlots).not.toHaveBeenCalled();
  });

  it("clears the pending runId when enqueue throws", async () => {
    queueService.enqueue.mockRejectedValue(new Error("ipc down"));
    const cmd = makeTriggerCmd(42);
    handler.handle(cmd);
    await vi.waitFor(() =>
      expect(logger.error).toHaveBeenCalledWith(
        expect.stringContaining("enqueue failed"),
        expect.any(Object)
      )
    );
    expect(concurrentManager.clearPendingRunId).toHaveBeenCalledWith(42);
    expect(concurrentManager.fillSlots).not.toHaveBeenCalled();
  });

  it("logs error when pipeline start throws", async () => {
    concurrentManager.fillSlots.mockRejectedValue(new Error("slot error"));
    const cmd = makeTriggerCmd(42);
    handler.handle(cmd);
    await vi.waitFor(() =>
      expect(logger.error).toHaveBeenCalledWith(
        expect.stringContaining("pipeline start failed"),
        expect.any(Object)
      )
    );
    // Enqueue still happened — the failure is only in fillSlots.
    expect(queueService.enqueue).toHaveBeenCalledTimes(1);
  });

  it("drops trigger when agentId is not set", async () => {
    handler = new TriggerCommandHandler(
      ipcClient as never,
      concurrentManager as never,
      queueService as never,
      logger as never
    );
    // setAgentId NOT called

    const cmd = makeTriggerCmd(42);
    handler.handle(cmd);
    await vi.waitFor(() =>
      expect(logger.warn).toHaveBeenCalledWith(
        expect.stringContaining("agentId not set"),
        expect.any(Object)
      )
    );
    expect(ipcClient.agentAcknowledgeCommand).not.toHaveBeenCalled();
    expect(queueService.enqueue).not.toHaveBeenCalled();
  });

  it("drops trigger when payload has no issueNumber", async () => {
    const cmd: ReceivedCommand = {
      id: "c",
      type: "trigger",
      payload: { owner: "nightgauge", repo: "nightgauge" }, // missing issueNumber
      createdAt: "",
    };
    handler.handle(cmd);
    await vi.waitFor(() =>
      expect(logger.warn).toHaveBeenCalledWith(
        expect.stringContaining("need owner, repo, issueNumber"),
        expect.any(Object)
      )
    );
    expect(ipcClient.agentAcknowledgeCommand).not.toHaveBeenCalled();
    expect(queueService.enqueue).not.toHaveBeenCalled();
  });

  it("drops trigger when payload has no owner/repo", async () => {
    const cmd: ReceivedCommand = {
      id: "c",
      type: "trigger",
      payload: { issueNumber: 42 }, // missing owner + repo
      createdAt: "",
    };
    handler.handle(cmd);
    await vi.waitFor(() =>
      expect(logger.warn).toHaveBeenCalledWith(
        expect.stringContaining("need owner, repo, issueNumber"),
        expect.any(Object)
      )
    );
    expect(ipcClient.agentAcknowledgeCommand).not.toHaveBeenCalled();
    expect(queueService.enqueue).not.toHaveBeenCalled();
  });

  it("setAgentId updates the agentId used in ack calls", async () => {
    handler.setAgentId("new-agent-id");
    const cmd = makeTriggerCmd(5);
    handler.handle(cmd);
    await vi.waitFor(() => expect(ipcClient.agentAcknowledgeCommand).toHaveBeenCalledTimes(1));
    expect(ipcClient.agentAcknowledgeCommand).toHaveBeenCalledWith(
      "new-agent-id",
      expect.any(String)
    );
  });
});

// ── Workspace-aware repo resolution (#4117) ─────────────────────────────────
//
// TriggerCommandHandler optionally accepts a WorkspaceManager (5th ctor arg).
// When provided, a trigger's {owner, repo} is resolved against the open
// workspace via findRepositoryByGitHub BEFORE ack/enqueue, so a repo that
// isn't open in a multi-root .code-workspace fails fast instead of acking a
// command ConcurrentPipelineManager will silently drop later. When omitted
// (undefined), behavior is unchanged from pre-#4117 — resolution is deferred
// entirely to ConcurrentPipelineManager at dispatch time.

describe("TriggerCommandHandler — workspace-aware repo resolution (#4117)", () => {
  let ipcClient: ReturnType<typeof makeIpcClient>;
  let concurrentManager: ReturnType<typeof makeConcurrentManager>;
  let queueService: ReturnType<typeof makeQueueService>;
  let logger: ReturnType<typeof makeLogger>;

  beforeEach(() => {
    ipcClient = makeIpcClient();
    concurrentManager = makeConcurrentManager();
    queueService = makeQueueService();
    logger = makeLogger();
  });

  function build(workspaceManager?: ReturnType<typeof makeWorkspaceManager>) {
    const h = new TriggerCommandHandler(
      ipcClient as never,
      concurrentManager as never,
      queueService as never,
      logger as never,
      workspaceManager as never
    );
    h.setAgentId("agent-1");
    return h;
  }

  it("proceeds with ack + enqueue when the target repo resolves in the workspace", async () => {
    const workspaceManager = makeWorkspaceManager({ path: "/workspace/nightgauge" });
    const handler = build(workspaceManager);

    const cmd = makeTriggerCmd(42);
    handler.handle(cmd);

    await vi.waitFor(() => expect(ipcClient.agentAcknowledgeCommand).toHaveBeenCalledTimes(1));
    expect(workspaceManager.findRepositoryByGitHub).toHaveBeenCalledWith("nightgauge/nightgauge");
    await vi.waitFor(() => expect(queueService.enqueue).toHaveBeenCalledTimes(1));
    await vi.waitFor(() => expect(concurrentManager.fillSlots).toHaveBeenCalledTimes(1));
  });

  it("drops the trigger gracefully — no ack, no enqueue, no throw — when the repo isn't open in this workspace", async () => {
    // Multi-root .code-workspace where the platform-triggered {owner, repo}
    // doesn't match any open folder — the edge case #4117 calls out.
    // NOTE: pass `null`, not `undefined` — an explicit `undefined` argument
    // would trigger makeWorkspaceManager's default parameter (found repo).
    const workspaceManager = makeWorkspaceManager(null);
    const handler = build(workspaceManager);

    const cmd = makeTriggerCmd(42);
    expect(() => handler.handle(cmd)).not.toThrow();

    await vi.waitFor(() =>
      expect(logger.warn).toHaveBeenCalledWith(
        expect.stringContaining("no matching repo open in this workspace"),
        expect.objectContaining({
          owner: "nightgauge",
          repo: "nightgauge",
          issueNumber: 42,
        })
      )
    );
    expect(workspaceManager.findRepositoryByGitHub).toHaveBeenCalledWith("nightgauge/nightgauge");
    expect(ipcClient.agentAcknowledgeCommand).not.toHaveBeenCalled();
    expect(queueService.enqueue).not.toHaveBeenCalled();
    expect(concurrentManager.fillSlots).not.toHaveBeenCalled();
  });

  it("skips the resolution check entirely when no workspaceManager is provided (pre-#4117 behavior)", async () => {
    const handler = build(undefined);

    const cmd = makeTriggerCmd(42);
    handler.handle(cmd);

    // No workspaceManager to consult — falls straight through to ack/enqueue,
    // same as every other test in this file that omits the 5th ctor arg.
    await vi.waitFor(() => expect(ipcClient.agentAcknowledgeCommand).toHaveBeenCalledTimes(1));
    await vi.waitFor(() => expect(queueService.enqueue).toHaveBeenCalledTimes(1));
  });
});
