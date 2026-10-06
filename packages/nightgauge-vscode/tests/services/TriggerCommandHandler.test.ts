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
import type { ConcurrentPipelineManager } from "../../src/services/ConcurrentPipelineManager";
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

/** Every placement the manager can report for a trigger's run. */
type Placement = Awaited<ReturnType<ConcurrentPipelineManager["placeRemoteRun"]>>;

function makeConcurrentManager(isRunning = false) {
  return {
    isRunning: vi.fn().mockReturnValue(isRunning),
    fillSlots: vi.fn().mockResolvedValue(1),
    remoteTriggerConflict: vi.fn().mockResolvedValue(null),
    // As the manager places a run when nothing of the issue is under way
    // here (#2344): it queues it through the handler's enqueue.
    placeRemoteRun: vi.fn(
      async (_run: unknown, enqueue: () => Promise<boolean>): Promise<Placement> =>
        (await enqueue()) ? "queued" : "not-queued"
    ),
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

    // The window holds the run from the ack on (#2340): the manager places
    // it, and the enqueue runs inside that placement (#2344); the queued item
    // carries the run id the slot adopts.
    expect(concurrentManager.placeRemoteRun).toHaveBeenCalledWith(
      { remoteRunId: "run-abc", issueNumber: 10, repo: "nightgauge/nightgauge" },
      expect.any(Function)
    );
    const ackOrder = ipcClient.agentAcknowledgeCommand.mock.invocationCallOrder[0];
    const placeOrder = concurrentManager.placeRemoteRun.mock.invocationCallOrder[0];
    const enqOrder = queueService.enqueue.mock.invocationCallOrder[0];
    expect(ackOrder).toBeLessThan(placeOrder);
    expect(placeOrder).toBeLessThan(enqOrder);
    expect(concurrentManager.remoteTriggerConflict).toHaveBeenCalledWith(
      10,
      "nightgauge/nightgauge",
      false
    );

    expect(logger.info).toHaveBeenCalledWith(
      expect.stringContaining("ack succeeded"),
      expect.objectContaining({ issueNumber: 10 })
    );
  });

  it("a private trigger queues and places a private run (#2400)", async () => {
    const cmd = makeTriggerCmd(10);
    (cmd.payload as Record<string, unknown>).visibility = "private";
    handler.handle(cmd);

    await vi.waitFor(() => expect(queueService.enqueue).toHaveBeenCalledTimes(1));
    expect(queueService.enqueue).toHaveBeenCalledWith(
      10,
      "Real issue title",
      ["type:feature"],
      undefined,
      {
        repoOverride: { owner: "nightgauge", repo: "nightgauge" },
        remoteRunId: "run-abc",
        visibility: "private",
      }
    );
    expect(concurrentManager.placeRemoteRun).toHaveBeenCalledWith(
      {
        remoteRunId: "run-abc",
        issueNumber: 10,
        repo: "nightgauge/nightgauge",
        visibility: "private",
      },
      expect.any(Function)
    );
  });

  it("a trigger naming team, or anything else, queues a team run (#2400)", async () => {
    for (const visibility of ["team", "PRIVATE"]) {
      queueService = makeQueueService();
      concurrentManager = makeConcurrentManager();
      ipcClient = makeIpcClient();
      handler = build();
      const cmd = makeTriggerCmd(10);
      (cmd.payload as Record<string, unknown>).visibility = visibility;
      handler.handle(cmd);
      await vi.waitFor(() => expect(queueService.enqueue).toHaveBeenCalledTimes(1));
      expect(queueService.enqueue.mock.calls[0][4]).not.toHaveProperty("visibility");
    }
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

  it("does not fill slots when enqueue is refused", async () => {
    queueService.enqueue.mockResolvedValue(null); // e.g. stop-in-progress guard
    const cmd = makeTriggerCmd(42);
    handler.handle(cmd);
    await vi.waitFor(() =>
      expect(logger.error).toHaveBeenCalledWith(
        expect.stringContaining("enqueue refused"),
        expect.any(Object)
      )
    );
    expect(await concurrentManager.placeRemoteRun.mock.results[0].value).toBe("not-queued");
    expect(concurrentManager.fillSlots).not.toHaveBeenCalled();
  });

  it("does not fill slots when enqueue throws", async () => {
    queueService.enqueue.mockRejectedValue(new Error("ipc down"));
    const cmd = makeTriggerCmd(42);
    handler.handle(cmd);
    await vi.waitFor(() =>
      expect(logger.error).toHaveBeenCalledWith(
        expect.stringContaining("enqueue failed"),
        expect.any(Object)
      )
    );
    expect(concurrentManager.fillSlots).not.toHaveBeenCalled();
  });

  // #2344: an issue queued or on its way to a slot here for another platform
  // run cannot serve this one; the requester learns why before any ack.
  it("refuses a trigger for an issue queued here for another run, before the ack", async () => {
    concurrentManager.remoteTriggerConflict.mockResolvedValue("busy");
    handler.handle(makeTriggerCmd(42));

    await vi.waitFor(() => expect(ipcClient.agentAcknowledgeCommand).toHaveBeenCalledTimes(1));
    expect(ipcClient.agentAcknowledgeCommand).toHaveBeenCalledWith(
      "agent-1",
      "cmd-1",
      "rejected",
      "already-queued: the issue is already queued on this agent for another run"
    );
    await new Promise((r) => setTimeout(r, 10));
    expect(concurrentManager.placeRemoteRun).not.toHaveBeenCalled();
    expect(queueService.enqueue).not.toHaveBeenCalled();
  });

  it("leaves a trigger alone when the issue's slot opened while it was fetched", async () => {
    concurrentManager.remoteTriggerConflict.mockResolvedValue("running");
    handler.handle(makeTriggerCmd(42));

    await vi.waitFor(() =>
      expect(logger.warn).toHaveBeenCalledWith(
        expect.stringContaining("concurrent trigger rejected"),
        expect.any(Object)
      )
    );
    expect(ipcClient.agentAcknowledgeCommand).not.toHaveBeenCalled();
    expect(concurrentManager.placeRemoteRun).not.toHaveBeenCalled();
  });

  it("starts nothing more when the issue's dispatch already under way serves the run", async () => {
    concurrentManager.placeRemoteRun.mockResolvedValue("attached");
    handler.handle(makeTriggerCmd(42));

    await vi.waitFor(() =>
      expect(logger.info).toHaveBeenCalledWith(
        expect.stringContaining("dispatch already under way here serves the run"),
        expect.any(Object)
      )
    );
    expect(queueService.enqueue).not.toHaveBeenCalled();
    expect(concurrentManager.fillSlots).not.toHaveBeenCalled();
  });

  // An epic runs as its sub-issues, each a run of its own: the epic's
  // platform run has no queued item to ride, so it is not placed (#2344).
  it("queues an epic's sub-issues and fills slots without placing its run", async () => {
    ipcClient.issueView.mockResolvedValue({ number: 42, title: "An epic", labels: ["type:epic"] });
    handler.handle(makeTriggerCmd(42));

    await vi.waitFor(() => expect(concurrentManager.fillSlots).toHaveBeenCalledTimes(1));
    expect(queueService.enqueue).toHaveBeenCalledWith(42, "An epic", ["type:epic"], undefined, {
      repoOverride: { owner: "nightgauge", repo: "nightgauge" },
      remoteRunId: "run-abc",
    });
    expect(concurrentManager.placeRemoteRun).not.toHaveBeenCalled();
  });

  it.each(["running", "busy"] as const)(
    "reports a run acked but not served when the placement finds the issue %s",
    async (placement) => {
      concurrentManager.placeRemoteRun.mockResolvedValue(placement);
      handler.handle(makeTriggerCmd(42));

      await vi.waitFor(() =>
        expect(logger.error).toHaveBeenCalledWith(
          expect.stringContaining("acked but is not served"),
          expect.objectContaining({ placement, runId: "run-abc" })
        )
      );
      expect(concurrentManager.fillSlots).not.toHaveBeenCalled();
    }
  );

  // #2357: the platform cancelled the run while it waited for the queue turn;
  // this window applied the cancel, and nothing is queued or started.
  it("starts nothing when the platform cancelled the run while it was placed", async () => {
    concurrentManager.placeRemoteRun.mockResolvedValue("cancelled");
    handler.handle(makeTriggerCmd(42));

    await vi.waitFor(() =>
      expect(logger.info).toHaveBeenCalledWith(
        expect.stringContaining("cancelled the run before it was queued"),
        expect.objectContaining({ runId: "run-abc" })
      )
    );
    expect(concurrentManager.fillSlots).not.toHaveBeenCalled();
    expect(logger.error).not.toHaveBeenCalled();
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

describe("TriggerCommandHandler — remote run request (#1656)", () => {
  const MODEL = "lmstudio/qwen/qwen3.8-27b";
  let ipcClient: ReturnType<typeof makeIpcClient> & { queueValidatePin: ReturnType<typeof vi.fn> };
  let concurrentManager: ReturnType<typeof makeConcurrentManager>;
  let queueService: ReturnType<typeof makeQueueService>;
  let logger: ReturnType<typeof makeLogger>;
  let handler: TriggerCommandHandler;

  function pinnedCmd(fields: Record<string, unknown>): ReceivedCommand {
    const cmd = makeTriggerCmd(42);
    return { ...cmd, payload: { ...(cmd.payload as object), ...fields } };
  }

  beforeEach(() => {
    ipcClient = {
      ...makeIpcClient(),
      queueValidatePin: vi.fn().mockResolvedValue({ ok: true }),
    };
    concurrentManager = makeConcurrentManager();
    queueService = makeQueueService();
    logger = makeLogger();
    handler = new TriggerCommandHandler(
      ipcClient as never,
      concurrentManager as never,
      queueService as never,
      logger as never
    );
    handler.setAgentId("agent-1");
  });

  it("validates a requested pair before the ack, then enqueues it with the pair", async () => {
    handler.handle(pinnedCmd({ adapter: "opencode", model: MODEL }));

    await vi.waitFor(() => expect(queueService.enqueue).toHaveBeenCalledTimes(1));
    expect(ipcClient.queueValidatePin).toHaveBeenCalledWith(
      "opencode",
      MODEL,
      "nightgauge",
      "nightgauge",
      42
    );
    expect(ipcClient.agentAcknowledgeCommand).toHaveBeenCalledWith("agent-1", "cmd-1");
    const validateOrder = ipcClient.queueValidatePin.mock.invocationCallOrder[0];
    const ackOrder = ipcClient.agentAcknowledgeCommand.mock.invocationCallOrder[0];
    expect(validateOrder).toBeLessThan(ackOrder);
    expect(queueService.enqueue.mock.calls[0][4]).toEqual({
      repoOverride: { owner: "nightgauge", repo: "nightgauge" },
      remoteRunId: "run-abc",
      requestedAdapter: "opencode",
      requestedModel: MODEL,
    });
  });

  // The operator queued the issue, or began its dispatch, after Go checked
  // the pair: the trigger is refused just before the ack, as Go refuses it,
  // and is never served on the operator's adapter and model.
  it("refuses a pinned trigger whose issue the operator queued here since Go's check, before the ack", async () => {
    concurrentManager.remoteTriggerConflict.mockResolvedValue("pinned");
    handler.handle(pinnedCmd({ adapter: "opencode", model: MODEL }));

    await vi.waitFor(() => expect(ipcClient.agentAcknowledgeCommand).toHaveBeenCalledTimes(1));
    expect(concurrentManager.remoteTriggerConflict).toHaveBeenCalledWith(
      42,
      "nightgauge/nightgauge",
      true
    );
    expect(ipcClient.agentAcknowledgeCommand).toHaveBeenCalledWith(
      "agent-1",
      "cmd-1",
      "rejected",
      "already-queued: the issue is already queued on this agent, so the requested adapter and model cannot apply"
    );
    await new Promise((r) => setTimeout(r, 10));
    expect(concurrentManager.placeRemoteRun).not.toHaveBeenCalled();
    expect(queueService.enqueue).not.toHaveBeenCalled();
  });

  it("places a pinned run as pinned, and reports it not served when the operator's dispatch began meanwhile", async () => {
    concurrentManager.placeRemoteRun.mockResolvedValue("pinned");
    handler.handle(pinnedCmd({ adapter: "opencode", model: MODEL }));

    await vi.waitFor(() =>
      expect(logger.error).toHaveBeenCalledWith(
        expect.stringContaining("acked but is not served"),
        expect.objectContaining({ runId: "run-abc" })
      )
    );
    expect(concurrentManager.placeRemoteRun.mock.calls[0][0]).toEqual({
      remoteRunId: "run-abc",
      issueNumber: 42,
      repo: "nightgauge/nightgauge",
      pinned: true,
    });
    expect(concurrentManager.fillSlots).not.toHaveBeenCalled();
  });

  it("acks a refused pair as rejected with Go's public category, and never enqueues", async () => {
    const reason = "model-not-in-catalog";
    ipcClient.queueValidatePin.mockResolvedValue({ ok: false, reason });
    handler.handle(pinnedCmd({ adapter: "opencode", model: "lmstudio/qwen/other" }));

    await vi.waitFor(() => expect(ipcClient.agentAcknowledgeCommand).toHaveBeenCalledTimes(1));
    expect(ipcClient.agentAcknowledgeCommand).toHaveBeenCalledWith(
      "agent-1",
      "cmd-1",
      "rejected",
      reason
    );
    await new Promise((r) => setTimeout(r, 10));
    expect(queueService.enqueue).not.toHaveBeenCalled();
    expect(concurrentManager.placeRemoteRun).not.toHaveBeenCalled();
    expect(concurrentManager.fillSlots).not.toHaveBeenCalled();
  });

  it("refuses a non-string field without asking Go, and never enqueues", async () => {
    handler.handle(pinnedCmd({ adapter: ["opencode"], model: MODEL }));

    await vi.waitFor(() => expect(ipcClient.agentAcknowledgeCommand).toHaveBeenCalledTimes(1));
    expect(ipcClient.agentAcknowledgeCommand.mock.calls[0][2]).toBe("rejected");
    expect(ipcClient.queueValidatePin).not.toHaveBeenCalled();
    expect(queueService.enqueue).not.toHaveBeenCalled();
  });

  it("an already-queued issue is acked rejected before any accepted ack (#1656)", async () => {
    ipcClient.queueValidatePin.mockResolvedValue({ ok: false, reason: "already-queued" });
    handler.handle(pinnedCmd({ adapter: "opencode", model: MODEL }));

    await vi.waitFor(() => expect(ipcClient.agentAcknowledgeCommand).toHaveBeenCalledTimes(1));
    expect(ipcClient.agentAcknowledgeCommand).toHaveBeenCalledWith(
      "agent-1",
      "cmd-1",
      "rejected",
      "already-queued"
    );
    expect(queueService.enqueue).not.toHaveBeenCalled();
  });

  it("refuses a pin on an epic, which enqueue would otherwise drop", async () => {
    ipcClient.issueView.mockResolvedValue({ number: 42, title: "Epic", labels: ["type:epic"] });
    handler.handle(pinnedCmd({ adapter: "opencode", model: MODEL }));

    await vi.waitFor(() => expect(ipcClient.agentAcknowledgeCommand).toHaveBeenCalledTimes(1));
    expect(ipcClient.agentAcknowledgeCommand.mock.calls[0][2]).toBe("rejected");
    expect(ipcClient.agentAcknowledgeCommand.mock.calls[0][3]).toBe("epic-not-pinnable");
    expect(queueService.enqueue).not.toHaveBeenCalled();
  });

  it("acks rejected with a fixed category when Go cannot be asked, keeping the error local", async () => {
    ipcClient.queueValidatePin.mockRejectedValue(new Error("IPC down at /home/someone/.sock"));
    handler.handle(pinnedCmd({ adapter: "opencode", model: MODEL }));

    await vi.waitFor(() => expect(ipcClient.agentAcknowledgeCommand).toHaveBeenCalledTimes(1));
    expect(ipcClient.agentAcknowledgeCommand.mock.calls[0][3]).toBe("validation-unavailable");
    expect(logger.warn).toHaveBeenCalledWith(
      expect.stringContaining("refused"),
      expect.objectContaining({ localReason: "IPC down at /home/someone/.sock" })
    );
    expect(queueService.enqueue).not.toHaveBeenCalled();
  });

  it("a payload without the fields never asks Go and enqueues exactly as before", async () => {
    handler.handle(makeTriggerCmd(42));

    await vi.waitFor(() => expect(queueService.enqueue).toHaveBeenCalledTimes(1));
    expect(ipcClient.queueValidatePin).not.toHaveBeenCalled();
    expect(ipcClient.agentAcknowledgeCommand).toHaveBeenCalledWith("agent-1", "cmd-1");
    expect(Object.keys(queueService.enqueue.mock.calls[0][4]).sort()).toEqual([
      "remoteRunId",
      "repoOverride",
    ]);
  });
});
