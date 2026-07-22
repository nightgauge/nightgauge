/**
 * ApproveCommandHandler.test.ts
 *
 * Unit tests for ApproveCommandHandler — gate approval forwarding via runId.
 *
 * @see Issue #3553 — Handle approve/reject command — forward gate approval to waiting pipeline
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

import { ApproveCommandHandler } from "../../src/services/ApproveCommandHandler";
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

function makeConcurrentManager(approveResult = true) {
  return {
    approveByRunId: vi.fn().mockReturnValue(approveResult),
  };
}

function makeApproveCmd(runId = "run-abc", commandId = "cmd-1"): ReceivedCommand {
  return {
    id: commandId,
    type: "approve",
    payload: { runId },
    createdAt: new Date().toISOString(),
  };
}

// ── Tests ─────────────────────────────────────────────────────────────────────

describe("ApproveCommandHandler", () => {
  let concurrentManager: ReturnType<typeof makeConcurrentManager>;
  let logger: ReturnType<typeof makeLogger>;
  let handler: ApproveCommandHandler;

  beforeEach(() => {
    concurrentManager = makeConcurrentManager();
    logger = makeLogger();
    handler = new ApproveCommandHandler(concurrentManager as never, logger as never);
  });

  it("ignores non-approve command types", () => {
    const cmd: ReceivedCommand = { id: "c", type: "heartbeat", payload: {}, createdAt: "" };
    handler.handle(cmd);
    expect(concurrentManager.approveByRunId).not.toHaveBeenCalled();
  });

  it("happy path: calls approveByRunId with correct runId and logs info", async () => {
    const cmd = makeApproveCmd("run-xyz", "cmd-2");
    handler.handle(cmd);
    await vi.waitFor(() => expect(concurrentManager.approveByRunId).toHaveBeenCalledTimes(1));
    expect(concurrentManager.approveByRunId).toHaveBeenCalledWith("run-xyz");
    expect(logger.info).toHaveBeenCalledWith(
      expect.stringContaining("gate approved"),
      expect.objectContaining({ runId: "run-xyz", commandId: "cmd-2" })
    );
  });

  it("logs warn and skips approveByRunId when runId is missing", async () => {
    const cmd: ReceivedCommand = {
      id: "cmd-3",
      type: "approve",
      payload: {},
      createdAt: "",
    };
    handler.handle(cmd);
    await vi.waitFor(() => expect(logger.warn).toHaveBeenCalledTimes(1));
    expect(logger.warn).toHaveBeenCalledWith(
      expect.stringContaining("missing runId"),
      expect.objectContaining({ commandId: "cmd-3" })
    );
    expect(concurrentManager.approveByRunId).not.toHaveBeenCalled();
  });

  it("logs warn when approveByRunId returns false (no pipeline waiting at gate)", async () => {
    concurrentManager = makeConcurrentManager(false);
    handler = new ApproveCommandHandler(concurrentManager as never, logger as never);

    const cmd = makeApproveCmd("run-gone");
    handler.handle(cmd);
    await vi.waitFor(() => expect(logger.warn).toHaveBeenCalledTimes(1));
    expect(logger.warn).toHaveBeenCalledWith(
      expect.stringContaining("no active pipeline waiting at gate"),
      expect.objectContaining({ runId: "run-gone" })
    );
    expect(logger.info).not.toHaveBeenCalled();
  });

  it("handle() is synchronous and fire-and-forget does not throw", () => {
    const cmd = makeApproveCmd();
    expect(() => handler.handle(cmd)).not.toThrow();
  });
});
