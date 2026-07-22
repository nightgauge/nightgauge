/**
 * RejectCommandHandler.test.ts
 *
 * Unit tests for RejectCommandHandler — gate rejection forwarding via runId.
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

import { RejectCommandHandler } from "../../src/services/RejectCommandHandler";
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

function makeConcurrentManager(rejectResult = true) {
  return {
    rejectByRunId: vi.fn().mockReturnValue(rejectResult),
  };
}

function makeRejectCmd(runId = "run-abc", commandId = "cmd-1"): ReceivedCommand {
  return {
    id: commandId,
    type: "reject",
    payload: { runId },
    createdAt: new Date().toISOString(),
  };
}

// ── Tests ─────────────────────────────────────────────────────────────────────

describe("RejectCommandHandler", () => {
  let concurrentManager: ReturnType<typeof makeConcurrentManager>;
  let logger: ReturnType<typeof makeLogger>;
  let handler: RejectCommandHandler;

  beforeEach(() => {
    concurrentManager = makeConcurrentManager();
    logger = makeLogger();
    handler = new RejectCommandHandler(concurrentManager as never, logger as never);
  });

  it("ignores non-reject command types", () => {
    const cmd: ReceivedCommand = { id: "c", type: "heartbeat", payload: {}, createdAt: "" };
    handler.handle(cmd);
    expect(concurrentManager.rejectByRunId).not.toHaveBeenCalled();
  });

  it("happy path: calls rejectByRunId with correct runId and logs info", async () => {
    const cmd = makeRejectCmd("run-xyz", "cmd-2");
    handler.handle(cmd);
    await vi.waitFor(() => expect(concurrentManager.rejectByRunId).toHaveBeenCalledTimes(1));
    expect(concurrentManager.rejectByRunId).toHaveBeenCalledWith("run-xyz");
    expect(logger.info).toHaveBeenCalledWith(
      expect.stringContaining("gate rejected"),
      expect.objectContaining({ runId: "run-xyz", commandId: "cmd-2" })
    );
  });

  it("logs warn and skips rejectByRunId when runId is missing", async () => {
    const cmd: ReceivedCommand = {
      id: "cmd-3",
      type: "reject",
      payload: {},
      createdAt: "",
    };
    handler.handle(cmd);
    await vi.waitFor(() => expect(logger.warn).toHaveBeenCalledTimes(1));
    expect(logger.warn).toHaveBeenCalledWith(
      expect.stringContaining("missing runId"),
      expect.objectContaining({ commandId: "cmd-3" })
    );
    expect(concurrentManager.rejectByRunId).not.toHaveBeenCalled();
  });

  it("logs warn when rejectByRunId returns false (no pipeline waiting at gate)", async () => {
    concurrentManager = makeConcurrentManager(false);
    handler = new RejectCommandHandler(concurrentManager as never, logger as never);

    const cmd = makeRejectCmd("run-gone");
    handler.handle(cmd);
    await vi.waitFor(() => expect(logger.warn).toHaveBeenCalledTimes(1));
    expect(logger.warn).toHaveBeenCalledWith(
      expect.stringContaining("no active pipeline waiting at gate"),
      expect.objectContaining({ runId: "run-gone" })
    );
    expect(logger.info).not.toHaveBeenCalled();
  });

  it("handle() is synchronous and fire-and-forget does not throw", () => {
    const cmd = makeRejectCmd();
    expect(() => handler.handle(cmd)).not.toThrow();
  });
});
