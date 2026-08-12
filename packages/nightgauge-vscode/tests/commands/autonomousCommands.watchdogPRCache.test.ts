/**
 * Tests for the stall watchdog's cached PR↔issue mapping (Issue #483).
 *
 * Before this fix, every watchdog tick called `getPRForIssue()` — a
 * `gh pr list --search` shell-out against GitHub's separate, much tighter
 * search-bucket quota — plus `ipc.prView()`, for EVERY in-progress issue,
 * every tick, uncached (verified against the pre-fix implementation: two
 * consecutive ticks with unchanged state made 2 mapping calls + 2 PR-detail
 * calls EACH tick, i.e. the count kept growing instead of holding steady).
 *
 * This test pins the exact GitHub-client call counts the fixed watchdog
 * makes across two consecutive ticks with unchanged board state, using the
 * REAL prDetection.ts caching logic (only the underlying `gh` shell-out is
 * mocked, at the `child_process.exec` boundary, so the batching/TTL code
 * under test actually runs):
 *
 *   - Tick 1 (cold cache): exactly ONE `gh pr list` shell-out for the one
 *     enabled repo — no `--search` — even though two issues need a lookup,
 *     plus one `ipc.prView()` per in-progress issue.
 *   - Tick 2 (2 minutes later, board state unchanged): ZERO further shell
 *     calls and ZERO further `ipc.prView()` calls — the cached mapping is
 *     still within its TTL.
 *   - Once the TTL has elapsed, the next tick refreshes both.
 *
 * @see src/commands/autonomousCommands.ts
 * @see src/utils/prDetection.ts
 * @see Issue #483
 */

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { exec } from "child_process";
import {
  disposeAutonomousOutputChannel,
  resetWatchdogStateForTest,
  _runStallWatchdogTickForTest,
} from "../../src/commands/autonomousCommands";
import { IpcClient } from "../../src/services/IpcClient";
import { _resetOpenPRsCacheForTests } from "../../src/utils/prDetection";
import type { Logger } from "../../src/utils/logger";

// ---------------------------------------------------------------------------
// Mocks
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

// Only the actual network-touching primitive is mocked — prDetection.ts's
// batching/TTL logic (getOpenPRsForRepo, findPRForIssueInList) runs for real
// so the test exercises the real caching behavior, not a stand-in for it.
vi.mock("child_process", async () => {
  const actual = await vi.importActual<typeof import("child_process")>("child_process");
  return {
    ...actual,
    exec: vi.fn((_cmd: string, optsOrCb: unknown, cb?: unknown) => {
      const callback = (typeof optsOrCb === "function" ? optsOrCb : cb) as (
        err: unknown,
        result: { stdout: string; stderr: string }
      ) => void;
      const prs = [
        {
          number: 701,
          url: "https://github.com/nightgauge/nightgauge/pull/701",
          title: "fix: issue 201",
          body: "Closes #201",
        },
        {
          number: 702,
          url: "https://github.com/nightgauge/nightgauge/pull/702",
          title: "fix: issue 202",
          body: "Closes #202",
        },
      ];
      callback(null, { stdout: JSON.stringify(prs), stderr: "" });
    }),
  };
});

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
    createTerminal: vi.fn(() => ({ show: vi.fn(), sendText: vi.fn() })),
  },
  workspace: {
    workspaceFolders: [{ uri: { fsPath: "/tmp/nightgauge" }, name: "nightgauge", index: 0 }],
  },
  ThemeColor: class ThemeColor {
    constructor(public id: string) {}
  },
  StatusBarAlignment: { Left: 1, Right: 2 },
}));

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

const createMockLogger = (): Logger =>
  ({
    info: vi.fn(),
    warn: vi.fn(),
    error: vi.fn(),
    debug: vi.fn(),
  }) as unknown as Logger;

/** Two in-progress, non-PR, non-epic board items — unchanged across ticks. */
const boardItems = [
  {
    id: "i1",
    number: 201,
    title: "Issue 201",
    state: "OPEN",
    status: "In Progress",
    priority: "",
    size: "",
    labels: [],
    assignees: [],
    repo: "nightgauge",
    url: "https://github.com/nightgauge/nightgauge/issues/201",
    updatedAt: new Date().toISOString(),
    isPR: false,
    isEpic: false,
  },
  {
    id: "i2",
    number: 202,
    title: "Issue 202",
    state: "OPEN",
    status: "In Progress",
    priority: "",
    size: "",
    labels: [],
    assignees: [],
    repo: "nightgauge",
    url: "https://github.com/nightgauge/nightgauge/issues/202",
    updatedAt: new Date().toISOString(),
    isPR: false,
    isEpic: false,
  },
];

describe("stall watchdog PR↔issue mapping cache (#483)", () => {
  let mockLogger: Logger;
  let mockIpc: any;

  beforeEach(() => {
    vi.clearAllMocks();
    vi.useFakeTimers();
    vi.setSystemTime(new Date("2026-08-11T12:00:00.000Z"));
    disposeAutonomousOutputChannel();
    resetWatchdogStateForTest();
    _resetOpenPRsCacheForTests();

    mockLogger = createMockLogger();

    mockIpc = {
      configGetProjectConfig: vi.fn(async () => ({ projectNumber: 1, ownerType: undefined })),
      boardList: vi.fn(async () => boardItems),
      prView: vi.fn(async () => ({
        state: "OPEN",
        checkStatus: "PENDING", // deliberately not SUCCESS/MERGEABLE — never "stalled"
        mergeable: "UNKNOWN",
      })),
      autonomousStatus: vi.fn(async () => ({ status: "stopped" })),
    };
    (IpcClient.getInstance as any).mockReturnValue(mockIpc);
  });

  afterEach(() => {
    vi.clearAllTimers();
    vi.useRealTimers();
    resetWatchdogStateForTest();
    _resetOpenPRsCacheForTests();
  });

  /** Count only the `gh pr list` invocations among all `exec` calls. */
  function prListShellCalls(): unknown[][] {
    return (exec as any).mock.calls.filter((c: unknown[]) => String(c[0]).startsWith("gh pr list"));
  }

  it("tick 1 (cold cache): one non-search gh pr list shell-out per repo, one PR-detail call per issue", async () => {
    await _runStallWatchdogTickForTest(mockLogger);

    expect(mockIpc.boardList).toHaveBeenCalledTimes(1);
    // ≤ 1 call per enabled repo — here exactly 1 — even though 2 issues
    // needed a lookup (shared via getOpenPRsForRepo's own TTL cache).
    expect(prListShellCalls()).toHaveLength(1);
    // Never the old per-issue `--search` shell-out (search-bucket quota).
    expect(String(prListShellCalls()[0][0])).not.toContain("--search");
    // One PR-detail call per issue whose mapping was (newly) resolved.
    expect(mockIpc.prView).toHaveBeenCalledTimes(2);
  });

  it("tick 2 with unchanged state: zero additional PR-list or PR-detail calls", async () => {
    // Tick 1 — cold cache.
    await _runStallWatchdogTickForTest(mockLogger);
    expect(prListShellCalls()).toHaveLength(1);
    expect(mockIpc.prView).toHaveBeenCalledTimes(2);

    // Tick 2 — 2 minutes later (the real watchdog cadence), board state
    // unchanged. The PR↔issue mapping is still within its TTL.
    vi.setSystemTime(new Date("2026-08-11T12:02:00.000Z"));
    await _runStallWatchdogTickForTest(mockLogger);

    // boardList still runs every tick (EMPTY_BOARD_SKIP_MS behavior
    // preserved — only skips repeated calls when the board was EMPTY).
    expect(mockIpc.boardList).toHaveBeenCalledTimes(2);
    // Zero search-bucket calls and zero PR-detail calls on the second tick:
    // the cached mapping from tick 1 is still fresh.
    expect(prListShellCalls()).toHaveLength(1);
    expect(mockIpc.prView).toHaveBeenCalledTimes(2);
  });

  it("refreshes the mapping (and PR detail) once the cache TTL has expired", async () => {
    await _runStallWatchdogTickForTest(mockLogger);
    expect(prListShellCalls()).toHaveLength(1);
    expect(mockIpc.prView).toHaveBeenCalledTimes(2);

    // 6 minutes later — past PR_MAPPING_CACHE_TTL_MS (5 min).
    vi.setSystemTime(new Date("2026-08-11T12:06:00.000Z"));
    await _runStallWatchdogTickForTest(mockLogger);

    expect(prListShellCalls()).toHaveLength(2);
    expect(mockIpc.prView).toHaveBeenCalledTimes(4);
  });
});
