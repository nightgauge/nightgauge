/**
 * autonomousCommands.ts — active pipeline run keeps its monitoring alive
 * regardless of visibility (#484 AC3).
 *
 * The autonomous stall watchdog is the one GitHub-touching timer in this
 * file, and it is deliberately NOT gated by the shared PollingVisibilityGate
 * that AttentionSweepService, ProjectBoardTreeProvider, and
 * RepositoriesTreeProvider now consult for their idle-state convenience
 * polling — see the doc comment above `scheduleNextWatchdog` in
 * autonomousCommands.ts for why. This suite pins that invariant directly:
 * with every view hidden and the window unfocused well past the focus-grace
 * window, the watchdog's `boardList()` calls continue on schedule —
 * identically to when the gate is wide open.
 */

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import {
  registerAutonomousCommands,
  disposeAutonomousOutputChannel,
  resetWatchdogStateForTest,
} from "../../src/commands/autonomousCommands";
import { IpcClient } from "../../src/services/IpcClient";
import {
  PollingVisibilityGate,
  WINDOW_FOCUS_GRACE_MS,
} from "../../src/services/AttentionSweepService";
import type { AutonomousStatusResult } from "../../src/services/IpcClientBase";
import type { Logger } from "../../src/utils/logger";
import type { StatusBarManager } from "../../src/utils/statusBar";

// ---------------------------------------------------------------------------
// Mocks (mirrors tests/commands/autonomousCommands.test.ts's own fixture)
// ---------------------------------------------------------------------------

vi.mock("../../src/services/IpcClient", () => ({
  IpcClient: {
    getInstance: vi.fn(),
  },
  IpcClientBase: {
    activeCallSource: undefined,
  },
}));

vi.mock("../../src/utils/configPathResolver", () => ({
  getRepoIdentity: vi.fn(async () => ({
    owner: "nightgauge",
    repo: "nightgauge",
  })),
}));

const mockOutputChannel = {
  appendLine: vi.fn(),
  clear: vi.fn(),
  show: vi.fn(),
  dispose: vi.fn(),
};

vi.mock("vscode", () => ({
  commands: {
    registerCommand: vi.fn(() => ({ dispose: vi.fn() })),
    executeCommand: vi.fn(),
  },
  window: {
    showWarningMessage: vi.fn(),
    showInformationMessage: vi.fn(),
    showErrorMessage: vi.fn(),
    createOutputChannel: vi.fn(() => mockOutputChannel),
    createStatusBarItem: vi.fn(() => ({
      text: "",
      tooltip: "",
      backgroundColor: undefined,
      command: "",
      show: vi.fn(),
      hide: vi.fn(),
      dispose: vi.fn(),
    })),
  },
  workspace: {
    workspaceFolders: [{ uri: { fsPath: "/test/workspace" }, name: "nightgauge", index: 0 }],
  },
  ThemeColor: class ThemeColor {
    constructor(public id: string) {}
  },
  StatusBarAlignment: { Left: 1, Right: 2 },
}));

const createMockLogger = (): Logger =>
  ({
    info: vi.fn(),
    warn: vi.fn(),
    error: vi.fn(),
    debug: vi.fn(),
  }) as unknown as Logger;

const createMockStatusBar = (): StatusBarManager =>
  ({
    showAutonomousRunning: vi.fn(),
    showAutonomousPaused: vi.fn(),
    showAutonomousComplete: vi.fn(),
    showAutonomousDisconnected: vi.fn(),
    showAutonomousCooldown: vi.fn(),
  }) as unknown as StatusBarManager;

function createMockStatus(overrides?: Partial<AutonomousStatusResult>): AutonomousStatusResult {
  return {
    status: "running",
    startedAt: new Date().toISOString(),
    lastScanAt: new Date().toISOString(),
    running: [],
    completed: [],
    failed: [],
    remaining: 1,
    tokensSpent: 0,
    tokensCeiling: 0,
    cyclesRun: 0,
    ...overrides,
  };
}

/**
 * One "In Progress" board item marked as an epic, so the watchdog's inner
 * per-item loop `continue`s immediately (`if (item.isPR || item.isEpic)
 * continue;`) without touching PR-lookup / `gh` shell-outs — that machinery
 * is #483's concern, not this suite's. `boardList` itself still runs every
 * tick because the raw item count is non-zero (the empty-board skip cache
 * only engages on a truly empty board).
 */
const boardItems = [
  {
    id: "i1",
    number: 501,
    title: "Epic placeholder",
    state: "OPEN",
    status: "In Progress",
    priority: "",
    size: "",
    labels: [],
    assignees: [],
    repo: "nightgauge",
    url: "https://github.com/nightgauge/nightgauge/issues/501",
    updatedAt: new Date().toISOString(),
    isPR: false,
    isEpic: true,
  },
];

const WATCHDOG_BASE_INTERVAL_MS = 2 * 60_000;

describe("autonomous stall watchdog ignores the visibility gate (#484 AC3)", () => {
  let mockLogger: Logger;
  let mockStatusBar: StatusBarManager;
  let mockIpc: any;

  beforeEach(() => {
    vi.clearAllMocks();
    vi.useFakeTimers();
    disposeAutonomousOutputChannel();
    resetWatchdogStateForTest();
    PollingVisibilityGate.resetForTests();

    mockLogger = createMockLogger();
    mockStatusBar = createMockStatusBar();
    mockIpc = {
      on: vi.fn(() => ({ dispose: vi.fn() })),
      onDidChangeStatus: vi.fn(() => ({ dispose: vi.fn() })),
      autonomousStatus: vi.fn(() => Promise.resolve(createMockStatus())),
      configGetProjectConfig: vi.fn(async () => ({ projectNumber: 1, ownerType: undefined })),
      boardList: vi.fn(async () => boardItems),
    };
    (IpcClient.getInstance as any).mockReturnValue(mockIpc);
  });

  afterEach(() => {
    resetWatchdogStateForTest();
    PollingVisibilityGate.resetForTests();
    vi.useRealTimers();
  });

  function getStatusChangedHandler(): (data: unknown) => void {
    const calls = (mockIpc.on as any).mock.calls;
    const match = calls.find((c: any[]) => c[0] === "autonomous.statusChanged");
    if (!match) throw new Error("autonomous.statusChanged not subscribed");
    return match[1];
  }

  it("keeps calling boardList() on schedule while every view is hidden and the window has been unfocused past the focus-grace window", async () => {
    // Close the shared idle-state polling gate completely — no view visible,
    // window unfocused, and well past WINDOW_FOCUS_GRACE_MS.
    PollingVisibilityGate.instance.setWindowFocused(false);
    vi.advanceTimersByTime(WINDOW_FOCUS_GRACE_MS + 1);
    expect(PollingVisibilityGate.instance.isWindowActive()).toBe(false);

    registerAutonomousCommands(mockLogger, mockStatusBar, null);
    const handler = getStatusChangedHandler();
    handler({ status: "running", runningCount: 0, remaining: 1 });

    // The immediate tick fired by startAutonomousStallWatchdog().
    await vi.advanceTimersByTimeAsync(0);
    expect(mockIpc.boardList).toHaveBeenCalledTimes(1);

    // Three more scheduled ticks at the base watchdog cadence — none skipped.
    await vi.advanceTimersByTimeAsync(WATCHDOG_BASE_INTERVAL_MS);
    await vi.advanceTimersByTimeAsync(WATCHDOG_BASE_INTERVAL_MS);
    await vi.advanceTimersByTimeAsync(WATCHDOG_BASE_INTERVAL_MS);

    expect(mockIpc.boardList).toHaveBeenCalledTimes(4);
    // The gate genuinely never reopened during this — not an accidental regain.
    expect(PollingVisibilityGate.instance.isWindowActive()).toBe(false);
  });

  it("makes the identical number of boardList() calls whether the gate is open or closed — the watchdog never consults it", async () => {
    registerAutonomousCommands(mockLogger, mockStatusBar, null);
    const handler = getStatusChangedHandler();

    async function runFourTicks(): Promise<number> {
      resetWatchdogStateForTest();
      mockIpc.boardList.mockClear();
      handler({ status: "running", runningCount: 0, remaining: 1 });
      await vi.advanceTimersByTimeAsync(0);
      await vi.advanceTimersByTimeAsync(WATCHDOG_BASE_INTERVAL_MS);
      await vi.advanceTimersByTimeAsync(WATCHDOG_BASE_INTERVAL_MS);
      await vi.advanceTimersByTimeAsync(WATCHDOG_BASE_INTERVAL_MS);
      return mockIpc.boardList.mock.calls.length;
    }

    PollingVisibilityGate.instance.setViewVisible("repositoriesView", true);
    const callsWhileVisible = await runFourTicks();

    PollingVisibilityGate.instance.setViewVisible("repositoriesView", false);
    PollingVisibilityGate.instance.setWindowFocused(false);
    vi.advanceTimersByTime(WINDOW_FOCUS_GRACE_MS + 1);
    expect(PollingVisibilityGate.instance.isWindowActive()).toBe(false);
    const callsWhileHidden = await runFourTicks();

    expect(callsWhileHidden).toBe(callsWhileVisible);
    expect(callsWhileVisible).toBe(4);
  });
});
