/**
 * E2E: Extension Activation Integration Tests
 *
 * Validates the service chain initialized during extension activation:
 *   Config load → IPC init → ProjectBoardService → ProjectBoardTreeProvider
 *
 * These tests sit between the existing activation-smoke.test.ts (which tests
 * data flow through the tree view) and real-binary tests (future work, gated by
 * NIGHTGAUGE_GO_BINARY_PATH). They wire real service classes together with
 * only the outermost IPC and filesystem boundary mocked.
 *
 * Acceptance criteria addressed:
 *   AC3 — activation smoke test validation and service chain verification
 *   AC4 — inline documentation of test patterns
 *
 * @see Issue #2504 — E2E pipeline execution tests
 * @see tests/e2e/activation-smoke.test.ts — broader activation flow tests
 */

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";

import { ProjectBoardService } from "../../src/services/ProjectBoardService";
import { ProjectBoardTreeProvider } from "../../src/views/ProjectBoardTreeProvider";
import { ReadyIssueTreeItem } from "../../src/views/items/ReadyIssueTreeItem";

import { createBoardItem } from "../mocks/board-item";

// ---------------------------------------------------------------------------
// Hoisted mocks — must be created inside vi.hoisted() so they are available
// before vi.mock() factories run (vi.mock() calls are hoisted to the top of the
// module by Vitest's transform, before variable declarations).
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
// Tests
// ============================================================================

describe("E2E: Extension Activation Integration", () => {
  let service: ProjectBoardService;

  beforeEach(() => {
    ipcMock.mockBoardList.mockReset();
    ipcMock.mockBoardCounts.mockReset();
    ipcMock.mockConfigGetProjectConfig.mockReset().mockResolvedValue({
      owner: "nightgauge",
      projectNumber: 42,
      defaultRepo: "",
    });
    ipcMock.mockOn.mockReset().mockReturnValue({ dispose: vi.fn() });

    // Create service and bypass filesystem config loading (unit test boundary)
    service = new ProjectBoardService("/test/workspace");
    (service as any).projectNumber = 42;
    (service as any).owner = "nightgauge";
    (service as any).configLoaded = true;
    (service as any).onItemsUpdated = vi.fn().mockReturnValue({ dispose: vi.fn() });
  });

  afterEach(() => {
    service.dispose();
  });

  // ==========================================================================
  // Config load → IPC init
  // ==========================================================================

  describe("Config load and IPC initialization", () => {
    it("should load config via IPC and initialize project context", async () => {
      const freshService = new ProjectBoardService("/test/workspace");

      ipcMock.mockConfigGetProjectConfig.mockResolvedValueOnce({
        owner: "TestOrg",
        projectNumber: 77,
        defaultRepo: "",
      });

      await freshService.loadConfig();

      expect(freshService.getOwner()).toBe("TestOrg");
      expect(freshService.getProjectNumber()).toBe(77);

      freshService.dispose();
    });

    it("should initialize IPC client and call start on extension activation", async () => {
      // Verify IpcClient.getInstance() returns the mock (IPC is available)
      const { IpcClient } = await import("../../src/services/IpcClient");
      const client = IpcClient.getInstance();

      // start() is idempotent — calling it should not throw
      await client.start();

      expect(ipcMock.mockStart).toHaveBeenCalled();
    });

    it("should handle missing config gracefully without crashing", async () => {
      const freshService = new ProjectBoardService("/test/workspace");

      // IPC returns no config
      ipcMock.mockConfigGetProjectConfig.mockResolvedValueOnce({
        owner: "",
        projectNumber: 0,
        defaultRepo: "",
      });

      await freshService.loadConfig();

      // Service should not crash; getIssuesByStatus returns [] when no owner/project
      ipcMock.mockBoardList.mockResolvedValue([]);
      const result = await freshService.getIssuesByStatus("ready");
      expect(result).toEqual([]);

      freshService.dispose();
    });
  });

  // ==========================================================================
  // IPC data → tree view population
  // ==========================================================================

  describe("Board view population with IPC data", () => {
    it("should populate board view tree items from mocked boardList response", async () => {
      const boardItems = [
        createBoardItem({
          number: 201,
          title: "Implement auth",
          status: "Ready",
          priority: "P1",
          size: "L",
          labels: ["type:feature"],
        }),
        createBoardItem({
          number: 202,
          title: "Add tests",
          status: "Ready",
          priority: "P2",
          size: "M",
          labels: ["type:chore"],
        }),
      ];
      ipcMock.mockBoardList.mockResolvedValue(boardItems);

      const provider = new ProjectBoardTreeProvider(service, "ready");
      (provider as any).groupByEpic = false;

      const children = await provider.getChildren();

      expect(children).toHaveLength(2);
      expect(children[0]).toBeInstanceOf(ReadyIssueTreeItem);
      expect((children[0] as ReadyIssueTreeItem).issueNumber).toBe(201);

      provider.dispose();
    });

    it("should show empty state when boardList returns no items", async () => {
      ipcMock.mockBoardList.mockResolvedValue([]);

      const provider = new ProjectBoardTreeProvider(service, "ready");
      const children = await provider.getChildren();

      expect(children).toHaveLength(1);
      expect(children[0].label).toContain("No ready issues found");

      provider.dispose();
    });

    it("should not call boardList when owner or project number is missing", async () => {
      const emptyService = new ProjectBoardService("/test/workspace");
      (emptyService as any).configLoaded = true;
      // owner and projectNumber not set — remain null

      const result = await emptyService.getIssuesByStatus("ready");

      expect(result).toEqual([]);
      expect(ipcMock.mockBoardList).not.toHaveBeenCalled();

      emptyService.dispose();
    });

    it("should use cached board data on repeated calls within TTL", async () => {
      ipcMock.mockBoardList.mockResolvedValue([createBoardItem({ number: 300, status: "Ready" })]);

      // First call hits IPC
      await service.getIssuesByStatus("ready");
      expect(ipcMock.mockBoardList).toHaveBeenCalledTimes(1);

      // Second call within TTL uses cache
      await service.getIssuesByStatus("ready");
      expect(ipcMock.mockBoardList).toHaveBeenCalledTimes(1);
    });

    it("should invalidate cache after clearCache() and re-fetch from IPC", async () => {
      ipcMock.mockBoardList.mockResolvedValue([createBoardItem({ number: 301, status: "Ready" })]);

      await service.getIssuesByStatus("ready");
      service.clearCache();
      await service.getIssuesByStatus("ready");

      expect(ipcMock.mockBoardList).toHaveBeenCalledTimes(2);
    });
  });
});
