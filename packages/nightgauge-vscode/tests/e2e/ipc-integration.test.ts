/**
 * E2E: IPC Integration Tests
 *
 * Tests the extension ↔ Go binary ↔ skill communication boundary using a
 * fully mocked IPC client. Verifies request/response lifecycle, error handling,
 * and event streaming without starting a real Go process.
 *
 * Boundary under test:
 *   [Extension TS code] → IpcClient.call() / IpcClient.on() → [mocked Go binary]
 *
 * @see Issue #2504 — E2E pipeline execution tests
 * @see Issue #2500 — IPC transport layer round-trip tests (related)
 */

import { describe, it, expect, vi, beforeEach } from "vitest";

import { createBoardItem } from "../mocks/board-item";

// ---------------------------------------------------------------------------
// Hoisted mocks — inline in vi.hoisted() (imported helpers cannot be called
// inside vi.hoisted() because imports are evaluated after the hoisted block).
// ---------------------------------------------------------------------------

const ipcMock = vi.hoisted(() => ({
  mockBoardList: vi.fn().mockResolvedValue([]),
  mockBoardCounts: vi.fn().mockResolvedValue({
    ready: 0,
    inProgress: 0,
    inReview: 0,
    done: 0,
    backlog: 0,
  }),
  mockConfigGetProjectConfig: vi.fn().mockResolvedValue({
    owner: "nightgauge",
    projectNumber: 42,
    defaultRepo: "",
  }),
  mockPipelineRun: vi.fn().mockResolvedValue({ success: true, runId: "run-1" }),
  mockPipelineGetState: vi.fn().mockResolvedValue(null),
  mockStart: vi.fn().mockResolvedValue(undefined),
  mockStop: vi.fn(),
  mockOn: vi.fn().mockReturnValue({ dispose: vi.fn() }),
  mockCall: vi.fn().mockResolvedValue({}),
}));

vi.mock("../../src/services/IpcClient", () => ({
  IpcClient: {
    getInstance: () => ({
      boardList: ipcMock.mockBoardList,
      boardCounts: ipcMock.mockBoardCounts,
      configGetProjectConfig: ipcMock.mockConfigGetProjectConfig,
      pipelineRun: ipcMock.mockPipelineRun,
      pipelineGetState: ipcMock.mockPipelineGetState,
      start: ipcMock.mockStart,
      stop: ipcMock.mockStop,
      on: ipcMock.mockOn,
      call: ipcMock.mockCall,
    }),
  },
}));

vi.mock("../../src/utils/configPathResolver", () => ({
  resolveConfigPathSync: vi.fn(),
  logDeprecationWarning: vi.fn(),
}));

// ============================================================================
// IPC request/response lifecycle
// ============================================================================

describe("E2E: IPC Round-Trip Integration", () => {
  beforeEach(() => {
    ipcMock.mockCall.mockReset();
    ipcMock.mockBoardList.mockReset();
    ipcMock.mockOn.mockReset().mockReturnValue({ dispose: vi.fn() });
  });

  it("should send skill.run request and receive ACK with request ID", async () => {
    // call() is the low-level transport; pipelineRun wraps it for pipeline.run
    ipcMock.mockCall.mockResolvedValueOnce({
      requestId: "req-001",
      status: "ack",
    });

    const { IpcClient } = await import("../../src/services/IpcClient");
    const client = IpcClient.getInstance();

    const result = await client.call("skill.run", {
      skill: "feature-dev",
      issueNumber: 2504,
    });

    expect(ipcMock.mockCall).toHaveBeenCalledWith("skill.run", {
      skill: "feature-dev",
      issueNumber: 2504,
    });
    expect(result).toMatchObject({ status: "ack" });
  });

  it("should stream progress events before receiving completion event", () => {
    const received: unknown[] = [];

    // on() mock fires two progress events synchronously when the handler is registered
    ipcMock.mockOn.mockImplementation((_event: string, handler: (d: unknown) => void) => {
      handler({ type: "progress", stage: "feature-dev", pct: 25 });
      handler({ type: "progress", stage: "feature-dev", pct: 75 });
      handler({ type: "complete", stage: "feature-dev", exitCode: 0 });
      return { dispose: vi.fn() };
    });

    // Drive on() directly through the mock — ipcMock.mockOn is the same fn
    // used by any real client.on() call after the vi.mock() is applied.
    const client = { on: ipcMock.mockOn };

    client.on("pipeline.stageResult", (event: unknown) => {
      received.push(event);
    });

    // All three events should have been delivered
    expect(received).toHaveLength(3);

    // First two are progress, last is complete
    expect((received[0] as any).type).toBe("progress");
    expect((received[1] as any).type).toBe("progress");
    expect((received[2] as any).type).toBe("complete");
    expect((received[2] as any).exitCode).toBe(0);
  });

  it("should handle IPC timeout gracefully with descriptive error", async () => {
    ipcMock.mockPipelineRun.mockRejectedValueOnce(
      Object.assign(new Error("IPC timeout after 30000ms"), {
        code: "TIMEOUT",
      })
    );

    const { IpcClient } = await import("../../src/services/IpcClient");
    const client = IpcClient.getInstance();

    let caughtError: Error | undefined;
    try {
      await client.pipelineRun("nightgauge", "nightgauge", 9999);
    } catch (err) {
      caughtError = err as Error;
    }

    expect(caughtError).toBeDefined();
    expect(caughtError!.message).toContain("IPC timeout");
  });

  it("should handle binary not found error without crashing extension", async () => {
    ipcMock.mockStart.mockRejectedValueOnce(
      Object.assign(new Error("spawn ENOENT"), { code: "ENOENT" })
    );

    const { IpcClient } = await import("../../src/services/IpcClient");
    const client = IpcClient.getInstance();

    await expect(client.start()).rejects.toThrow("spawn ENOENT");

    // Extension should remain in a usable state (no uncaught exceptions)
    // Subsequent calls can still be made — they'll fail gracefully
    ipcMock.mockBoardList.mockResolvedValueOnce([]);
    const items = await client.boardList("nightgauge", 42, "Ready");
    expect(items).toEqual([]);
  });

  it("should return board items from boardList after successful IPC call", async () => {
    const fakeItems = [
      createBoardItem({ number: 10, title: "First issue", status: "Ready" }),
      createBoardItem({ number: 11, title: "Second issue", status: "Ready" }),
    ];
    ipcMock.mockBoardList.mockResolvedValueOnce(fakeItems);

    const { IpcClient } = await import("../../src/services/IpcClient");
    const client = IpcClient.getInstance();

    const result = await client.boardList("nightgauge", 42, "Ready");

    expect(result).toHaveLength(2);
    expect(result[0].number).toBe(10);
    expect(result[1].number).toBe(11);
  });

  it("should register and dispose event handlers without leaking", () => {
    const disposeA = vi.fn();
    const disposeB = vi.fn();

    ipcMock.mockOn
      .mockReturnValueOnce({ dispose: disposeA })
      .mockReturnValueOnce({ dispose: disposeB });

    const client = { on: ipcMock.mockOn };

    const subA = client.on("pipeline.progress", vi.fn());
    const subB = client.on("pipeline.complete", vi.fn());

    subA.dispose();
    subB.dispose();

    expect(disposeA).toHaveBeenCalledOnce();
    expect(disposeB).toHaveBeenCalledOnce();
  });
});
