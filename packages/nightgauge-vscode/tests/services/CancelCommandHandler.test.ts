/**
 * CancelCommandHandler.test.ts
 *
 * Unit tests for CancelCommandHandler — cancel dispatch, runId lookup,
 * graceful stop, and no-op paths.
 *
 * Terminal platform telemetry (pipeline_done) is emitted by the normal
 * completion path (HeadlessOrchestrator.firePipelineComplete), NOT by this
 * handler, so these tests only assert the cancel mechanics.
 *
 * @see Issue #3552 — Handle cancel command gracefully
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

import { CancelCommandHandler } from "../../src/services/CancelCommandHandler";
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

function makeConcurrentManager(opts: { found?: boolean } = {}) {
  return {
    cancelByRunId: vi.fn().mockResolvedValue(opts.found ?? true),
  };
}

function makeCancelCmd(runId = "run-xyz", commandId = "cmd-1"): ReceivedCommand {
  return {
    id: commandId,
    type: "cancel",
    payload: { runId },
    createdAt: new Date().toISOString(),
  };
}

// ── Tests ─────────────────────────────────────────────────────────────────────

describe("CancelCommandHandler", () => {
  let concurrentManager: ReturnType<typeof makeConcurrentManager>;
  let logger: ReturnType<typeof makeLogger>;
  let handler: CancelCommandHandler;

  beforeEach(() => {
    concurrentManager = makeConcurrentManager();
    logger = makeLogger();
    handler = new CancelCommandHandler(concurrentManager as never, logger as never);
  });

  it("ignores non-cancel commands", () => {
    const cmd: ReceivedCommand = { id: "c", type: "trigger", payload: {}, createdAt: "" };
    handler.handle(cmd);
    expect(concurrentManager.cancelByRunId).not.toHaveBeenCalled();
  });

  it("happy path: calls cancelByRunId and logs cancellation", async () => {
    const cmd = makeCancelCmd("run-abc");
    handler.handle(cmd);
    await vi.waitFor(() => expect(concurrentManager.cancelByRunId).toHaveBeenCalledTimes(1));
    expect(concurrentManager.cancelByRunId).toHaveBeenCalledWith("run-abc");
    expect(logger.info).toHaveBeenCalledWith(
      expect.stringContaining("pipeline cancelled"),
      expect.objectContaining({ runId: "run-abc" })
    );
  });

  it("no-op when runId not found in active slots", async () => {
    concurrentManager = makeConcurrentManager({ found: false });
    handler = new CancelCommandHandler(concurrentManager as never, logger as never);

    const cmd = makeCancelCmd("run-missing");
    handler.handle(cmd);
    await vi.waitFor(() =>
      expect(logger.warn).toHaveBeenCalledWith(
        expect.stringContaining("no active pipeline for runId"),
        expect.objectContaining({ runId: "run-missing" })
      )
    );
  });

  it("no-op when runId is missing from payload", async () => {
    const cmd: ReceivedCommand = {
      id: "cmd-2",
      type: "cancel",
      payload: {},
      createdAt: "",
    };
    handler.handle(cmd);
    await vi.waitFor(() =>
      expect(logger.warn).toHaveBeenCalledWith(
        expect.stringContaining("missing runId in payload"),
        expect.any(Object)
      )
    );
    expect(concurrentManager.cancelByRunId).not.toHaveBeenCalled();
  });
});
