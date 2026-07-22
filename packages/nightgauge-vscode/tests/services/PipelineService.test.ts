/**
 * Tests for PipelineService — IPC-backed pipeline execution service.
 *
 * Covers:
 * - Initial state (isRunning, getCurrentIssueNumber)
 * - run() — delegates to ipc.pipelineRun, sets state, fires onPipelineStarted
 * - stop() — delegates to ipc.pipelineStop, resets state, fires onPipelineCompleted
 * - pause() / resume() — delegate to ipc; no-op when no current execution
 * - listExecutions() / queue methods — all delegate to ipc
 * - IPC event handling: stage.start, stage.complete, stage.failed, pipeline.complete, pipeline.error
 * - dispose()
 */

import { describe, it, expect, vi, beforeEach } from "vitest";

// ---------------------------------------------------------------------------
// IPC event handler capture
// ---------------------------------------------------------------------------

type EventHandler = (data: unknown) => void;
const ipcHandlers: Map<string, EventHandler> = new Map();

function fireIpcEvent(event: string, data: unknown): void {
  ipcHandlers.get(event)?.(data);
}

// ---------------------------------------------------------------------------
// Hoisted mocks
// ---------------------------------------------------------------------------

const {
  mockPipelineRun,
  mockPipelineStop,
  mockPipelinePause,
  mockPipelineResume,
  mockPipelineGetState,
  mockExecutionList,
  mockQueueAdd,
  mockQueueList,
  mockQueueRemove,
  mockQueueClear,
} = vi.hoisted(() => ({
  mockPipelineRun: vi.fn(),
  mockPipelineStop: vi.fn(),
  mockPipelinePause: vi.fn(),
  mockPipelineResume: vi.fn(),
  mockPipelineGetState: vi.fn(),
  mockExecutionList: vi.fn(),
  mockQueueAdd: vi.fn(),
  mockQueueList: vi.fn(),
  mockQueueRemove: vi.fn(),
  mockQueueClear: vi.fn(),
}));

vi.mock("../../src/services/IpcClient", () => ({
  IpcClient: {
    getInstance: () => ({
      on: vi.fn((event: string, handler: EventHandler) => {
        ipcHandlers.set(event, handler);
        return { dispose: vi.fn() };
      }),
      pipelineRun: mockPipelineRun,
      pipelineStop: mockPipelineStop,
      pipelinePause: mockPipelinePause,
      pipelineResume: mockPipelineResume,
      pipelineGetState: mockPipelineGetState,
      executionList: mockExecutionList,
      queueAdd: mockQueueAdd,
      queueList: mockQueueList,
      queueRemove: mockQueueRemove,
      queueClear: mockQueueClear,
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
}));

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

async function makeService() {
  const { PipelineService } = await import("../../src/services/PipelineService");
  ipcHandlers.clear();
  return new PipelineService();
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

describe("PipelineService", () => {
  beforeEach(() => {
    vi.resetModules();
    ipcHandlers.clear();
    mockPipelineRun.mockReset();
    mockPipelineStop.mockReset();
    mockPipelinePause.mockReset();
    mockPipelineResume.mockReset();
    mockPipelineGetState.mockReset();
    mockExecutionList.mockReset();
    mockQueueAdd.mockReset();
    mockQueueList.mockReset();
    mockQueueRemove.mockReset();
    mockQueueClear.mockReset();
  });

  // -------------------------------------------------------------------------
  // Initial state
  // -------------------------------------------------------------------------

  describe("initial state", () => {
    it("isRunning() returns false initially", async () => {
      const svc = await makeService();
      expect(svc.isRunning()).toBe(false);
    });

    it("getCurrentIssueNumber() returns null initially", async () => {
      const svc = await makeService();
      expect(svc.getCurrentIssueNumber()).toBeNull();
    });
  });

  // -------------------------------------------------------------------------
  // run()
  // -------------------------------------------------------------------------

  describe("run()", () => {
    it("calls ipc.pipelineRun with correct params", async () => {
      mockPipelineRun.mockResolvedValue({ executionId: "exec-1" });
      const svc = await makeService();

      await svc.run({
        owner: "nightgauge",
        repo: "nightgauge",
        issueNumber: 42,
        fromStage: "feature-dev",
        targetBranch: "main",
        model: "claude-opus-4-5",
        adapter: "cli",
      });

      expect(mockPipelineRun).toHaveBeenCalledWith("nightgauge", "nightgauge", 42, {
        fromStage: "feature-dev",
        targetBranch: "main",
        model: "claude-opus-4-5",
        adapter: "cli",
      });
    });

    it("sets running=true and currentIssueNumber after run()", async () => {
      mockPipelineRun.mockResolvedValue({ executionId: "exec-2" });
      const svc = await makeService();

      await svc.run({
        owner: "nightgauge",
        repo: "nightgauge",
        issueNumber: 99,
      });

      expect(svc.isRunning()).toBe(true);
      expect(svc.getCurrentIssueNumber()).toBe(99);
    });

    it("fires onPipelineStarted with issueNumber", async () => {
      mockPipelineRun.mockResolvedValue({ executionId: "exec-3" });
      const svc = await makeService();

      const started: Array<{ issueNumber: number }> = [];
      svc.onPipelineStarted((e: { issueNumber: number }) => started.push(e));

      await svc.run({
        owner: "nightgauge",
        repo: "nightgauge",
        issueNumber: 55,
      });

      expect(started).toHaveLength(1);
      expect(started[0].issueNumber).toBe(55);
    });

    it("returns the result from ipc.pipelineRun", async () => {
      const fakeResult = { executionId: "exec-result-123" };
      mockPipelineRun.mockResolvedValue(fakeResult);
      const svc = await makeService();

      const result = await svc.run({
        owner: "nightgauge",
        repo: "nightgauge",
        issueNumber: 10,
      });

      expect(result).toEqual(fakeResult);
    });
  });

  // -------------------------------------------------------------------------
  // stop()
  // -------------------------------------------------------------------------

  describe("stop()", () => {
    it("does nothing when there is no current execution", async () => {
      const svc = await makeService();
      await svc.stop();
      expect(mockPipelineStop).not.toHaveBeenCalled();
    });

    it("calls ipc.pipelineStop, resets state, and fires onPipelineCompleted(success=false)", async () => {
      mockPipelineRun.mockResolvedValue({ executionId: "exec-stop" });
      mockPipelineStop.mockResolvedValue(undefined);
      const svc = await makeService();

      await svc.run({
        owner: "nightgauge",
        repo: "nightgauge",
        issueNumber: 77,
      });

      const completed: Array<{ issueNumber: number; success: boolean }> = [];
      svc.onPipelineCompleted((e: { issueNumber: number; success: boolean }) => completed.push(e));

      await svc.stop();

      expect(mockPipelineStop).toHaveBeenCalledWith("exec-stop");
      expect(svc.isRunning()).toBe(false);
      expect(svc.getCurrentIssueNumber()).toBeNull();
      expect(completed).toHaveLength(1);
      expect(completed[0].success).toBe(false);
      expect(completed[0].issueNumber).toBe(77);
    });
  });

  // -------------------------------------------------------------------------
  // pause()
  // -------------------------------------------------------------------------

  describe("pause()", () => {
    it("does nothing when there is no current execution", async () => {
      const svc = await makeService();
      await svc.pause();
      expect(mockPipelinePause).not.toHaveBeenCalled();
    });

    it("calls ipc.pipelinePause with executionId", async () => {
      mockPipelineRun.mockResolvedValue({ executionId: "exec-pause" });
      mockPipelinePause.mockResolvedValue(undefined);
      const svc = await makeService();

      await svc.run({
        owner: "nightgauge",
        repo: "nightgauge",
        issueNumber: 11,
      });

      await svc.pause();

      expect(mockPipelinePause).toHaveBeenCalledWith("exec-pause");
    });
  });

  // -------------------------------------------------------------------------
  // resume()
  // -------------------------------------------------------------------------

  describe("resume()", () => {
    it("does nothing when there is no current execution", async () => {
      const svc = await makeService();
      await svc.resume();
      expect(mockPipelineResume).not.toHaveBeenCalled();
    });

    it("calls ipc.pipelineResume with executionId", async () => {
      mockPipelineRun.mockResolvedValue({ executionId: "exec-resume" });
      mockPipelineResume.mockResolvedValue(undefined);
      const svc = await makeService();

      await svc.run({
        owner: "nightgauge",
        repo: "nightgauge",
        issueNumber: 12,
      });

      await svc.resume();

      expect(mockPipelineResume).toHaveBeenCalledWith("exec-resume");
    });
  });

  // -------------------------------------------------------------------------
  // listExecutions()
  // -------------------------------------------------------------------------

  describe("listExecutions()", () => {
    it("delegates to ipc.executionList", async () => {
      const fakeList = [{ executionId: "e1", status: "running" }];
      mockExecutionList.mockResolvedValue(fakeList);
      const svc = await makeService();

      const result = await svc.listExecutions();

      expect(mockExecutionList).toHaveBeenCalled();
      expect(result).toEqual(fakeList);
    });
  });

  // -------------------------------------------------------------------------
  // Queue methods
  // -------------------------------------------------------------------------

  describe("queue methods", () => {
    it("queueAdd() delegates to ipc.queueAdd", async () => {
      mockQueueAdd.mockResolvedValue(undefined);
      const svc = await makeService();

      await svc.queueAdd("nightgauge", "nightgauge", 5, "Fix bug", ["bug"]);

      expect(mockQueueAdd).toHaveBeenCalledWith("nightgauge", "nightgauge", 5, "Fix bug", ["bug"]);
    });

    it("queueList() delegates to ipc.queueList", async () => {
      const fakeQueue = [{ issueNumber: 5 }];
      mockQueueList.mockResolvedValue(fakeQueue);
      const svc = await makeService();

      const result = await svc.queueList();

      expect(mockQueueList).toHaveBeenCalled();
      expect(result).toEqual(fakeQueue);
    });

    it("queueRemove() delegates to ipc.queueRemove", async () => {
      mockQueueRemove.mockResolvedValue(undefined);
      const svc = await makeService();

      await svc.queueRemove(7);

      expect(mockQueueRemove).toHaveBeenCalledWith(7);
    });

    it("queueClear() delegates to ipc.queueClear", async () => {
      mockQueueClear.mockResolvedValue(undefined);
      const svc = await makeService();

      await svc.queueClear();

      expect(mockQueueClear).toHaveBeenCalled();
    });
  });

  // -------------------------------------------------------------------------
  // IPC event handling
  // -------------------------------------------------------------------------

  describe("IPC event handling", () => {
    it("stage.start fires onStageChanged with status=running", async () => {
      const svc = await makeService();

      const events: Array<{
        issueNumber: number;
        stage: string;
        status: string;
      }> = [];
      svc.onStageChanged((e: { issueNumber: number; stage: string; status: string }) =>
        events.push(e)
      );

      fireIpcEvent("stage.start", {
        issueNumber: 20,
        stage: "feature-dev",
      });

      expect(events).toHaveLength(1);
      expect(events[0].issueNumber).toBe(20);
      expect(events[0].stage).toBe("feature-dev");
      expect(events[0].status).toBe("running");
    });

    it("stage.complete fires onStageChanged with status=complete", async () => {
      const svc = await makeService();

      const events: Array<{ stage: string; status: string }> = [];
      svc.onStageChanged((e: { stage: string; status: string }) => events.push(e));

      fireIpcEvent("stage.complete", {
        issueNumber: 21,
        stage: "feature-planning",
      });

      expect(events).toHaveLength(1);
      expect(events[0].stage).toBe("feature-planning");
      expect(events[0].status).toBe("complete");
    });

    it("stage.failed fires onStageChanged with status=failed and error", async () => {
      const svc = await makeService();

      const events: Array<{ stage: string; status: string; error?: string }> = [];
      svc.onStageChanged((e: { stage: string; status: string; error?: string }) => events.push(e));

      fireIpcEvent("stage.failed", {
        issueNumber: 22,
        stage: "feature-validate",
        error: "Tests failed",
      });

      expect(events).toHaveLength(1);
      expect(events[0].stage).toBe("feature-validate");
      expect(events[0].status).toBe("failed");
      expect(events[0].error).toBe("Tests failed");
    });

    it("pipeline.complete fires onPipelineCompleted and resets running state", async () => {
      mockPipelineRun.mockResolvedValue({ executionId: "exec-complete" });
      const svc = await makeService();

      await svc.run({
        owner: "nightgauge",
        repo: "nightgauge",
        issueNumber: 30,
      });

      expect(svc.isRunning()).toBe(true);

      const completed: Array<{ issueNumber: number; success: boolean }> = [];
      svc.onPipelineCompleted((e: { issueNumber: number; success: boolean }) => completed.push(e));

      fireIpcEvent("pipeline.complete", {
        issueNumber: 30,
        success: true,
      });

      expect(completed).toHaveLength(1);
      expect(completed[0].issueNumber).toBe(30);
      expect(completed[0].success).toBe(true);
      expect(svc.isRunning()).toBe(false);
      expect(svc.getCurrentIssueNumber()).toBeNull();
    });

    it("pipeline.error fires onPipelineCompleted(success=false) and resets state", async () => {
      mockPipelineRun.mockResolvedValue({ executionId: "exec-error" });
      const svc = await makeService();

      await svc.run({
        owner: "nightgauge",
        repo: "nightgauge",
        issueNumber: 31,
      });

      const completed: Array<{ issueNumber: number; success: boolean }> = [];
      svc.onPipelineCompleted((e: { issueNumber: number; success: boolean }) => completed.push(e));

      fireIpcEvent("pipeline.error", {
        issueNumber: 31,
        error: "Unexpected failure",
      });

      expect(completed).toHaveLength(1);
      expect(completed[0].issueNumber).toBe(31);
      expect(completed[0].success).toBe(false);
      expect(svc.isRunning()).toBe(false);
      expect(svc.getCurrentIssueNumber()).toBeNull();
    });
  });

  // -------------------------------------------------------------------------
  // dispose()
  // -------------------------------------------------------------------------

  describe("dispose()", () => {
    it("disposes all EventEmitters without throwing", async () => {
      const svc = await makeService();
      expect(() => svc.dispose()).not.toThrow();
    });
  });
});
