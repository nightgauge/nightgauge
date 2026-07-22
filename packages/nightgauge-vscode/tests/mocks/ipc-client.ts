/**
 * IPC client mock setup for E2E tests.
 *
 * Provides a hoisted mock factory and a typed handle for controlling responses
 * in individual tests. Import `setupIpcClientMock` in a `vi.mock` call, then
 * use the returned `IpcMockHandle` to drive return values.
 *
 * Pattern mirrors activation-smoke.test.ts — vi.hoisted() ensures mock refs
 * are available before module-level vi.mock() factories execute.
 *
 * Usage:
 *   const { mockBoardList, mockPipelineRun, ... } = vi.hoisted(() => setupIpcClientMock());
 *   vi.mock("../../src/services/IpcClient", () => makeIpcClientModule(mockBoardList, ...));
 */

import { vi } from "vitest";
import type { BoardItem } from "../../src/services/IpcClient";

/** All mock functions exposed by the IPC client mock. */
export interface IpcMockHandle {
  mockBoardList: ReturnType<typeof vi.fn>;
  mockBoardCounts: ReturnType<typeof vi.fn>;
  mockConfigGetProjectConfig: ReturnType<typeof vi.fn>;
  mockPipelineRun: ReturnType<typeof vi.fn>;
  mockPipelineGetState: ReturnType<typeof vi.fn>;
  mockStart: ReturnType<typeof vi.fn>;
  mockStop: ReturnType<typeof vi.fn>;
  mockOn: ReturnType<typeof vi.fn>;
  mockCall: ReturnType<typeof vi.fn>;
}

/**
 * Create all mock functions for the IPC client.
 * Call this inside vi.hoisted() to ensure refs are set before imports resolve.
 */
export function setupIpcClientMock(): IpcMockHandle {
  return {
    mockBoardList: vi.fn<() => Promise<BoardItem[]>>().mockResolvedValue([]),
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
  };
}

/**
 * Build the vi.mock factory body for "../../src/services/IpcClient".
 * Call makeIpcClientMockModule(handle) inside vi.mock().
 */
export function makeIpcClientMockModule(handle: IpcMockHandle) {
  return {
    IpcClient: {
      getInstance: () => ({
        boardList: handle.mockBoardList,
        boardCounts: handle.mockBoardCounts,
        configGetProjectConfig: handle.mockConfigGetProjectConfig,
        pipelineRun: handle.mockPipelineRun,
        pipelineGetState: handle.mockPipelineGetState,
        start: handle.mockStart,
        stop: handle.mockStop,
        on: handle.mockOn,
        call: handle.mockCall,
      }),
    },
  };
}
