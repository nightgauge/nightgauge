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
import * as vscode from "vscode";
import {
  disposeAutonomousOutputChannel,
  resetWatchdogStateForTest,
  _runStallWatchdogTickForTest,
  _stopWatchdogForTest,
} from "../../src/commands/autonomousCommands";
import { IpcClient } from "../../src/services/IpcClient";
import { clearOpenPRsCache } from "../../src/utils/prDetection";
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
    clearOpenPRsCache();

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
    clearOpenPRsCache();
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

  // Additional pin (review-round fixup): exact `<` vs `<=` behavior at the
  // TTL boundary itself, not past it. A `<=` mutant would treat this tick as
  // still-fresh and make zero further calls.
  it("treats the cache as expired exactly AT the TTL boundary (not just past it)", async () => {
    await _runStallWatchdogTickForTest(mockLogger);
    expect(prListShellCalls()).toHaveLength(1);
    expect(mockIpc.prView).toHaveBeenCalledTimes(2);

    // Exactly PR_MAPPING_CACHE_TTL_MS (5 min) later — the boundary itself.
    vi.setSystemTime(new Date("2026-08-11T12:05:00.000Z"));
    await _runStallWatchdogTickForTest(mockLogger);

    expect(prListShellCalls()).toHaveLength(2);
    expect(mockIpc.prView).toHaveBeenCalledTimes(4);
  });

  // #483 must-fix — a failed gh pr list must never be negative-cached. Before
  // this fix, tick 1's failure with no prior snapshot got mapped to
  // `{ prInfo: null, pr: null }` and cached for the full TTL, blinding the
  // watchdog to a real stall for up to 5 minutes after the failure cleared.
  it("a failed gh pr list is not negative-cached — tick 2 retries instead of serving a poisoned 'no PR' result", async () => {
    // Tick 1: BOTH issues' getOpenPRsForRepo() calls must fail. A single
    // failure only fails the FIRST issue's attempt — since a failed fetch
    // deliberately does NOT populate the repo-level cache (that's the fix
    // under test), the second issue's cache lookup is an independent miss
    // and retries too, so two `gh pr list` shell-outs happen this tick and
    // both must fail to exercise "the whole tick's fetch failed".
    const failOnce = () =>
      (exec as any).mockImplementationOnce((_cmd: string, optsOrCb: unknown, cb?: unknown) => {
        const callback = (typeof optsOrCb === "function" ? optsOrCb : cb) as (
          err: unknown,
          result: { stdout: string; stderr: string }
        ) => void;
        callback(new Error("gh: command timed out"), { stdout: "", stderr: "" });
      });
    failOnce();
    failOnce();

    await _runStallWatchdogTickForTest(mockLogger);

    expect(prListShellCalls()).toHaveLength(2);
    // Both issues bailed on the null result WITHOUT calling prView or
    // caching a "no PR" mapping.
    expect(mockIpc.prView).toHaveBeenCalledTimes(0);

    // Tick 2 — 2 minutes later, well inside the TTL that would have kept a
    // poisoned cache entry alive under the pre-fix behavior. The default
    // mock implementation (successful) is used since tick 1 consumed both
    // queued `mockImplementationOnce` failures. Only ONE more shell-out is
    // needed this tick: the first issue's fetch populates the repo-level
    // cache, so the second issue's lookup is served from it within the
    // same tick.
    vi.setSystemTime(new Date("2026-08-11T12:02:00.000Z"));
    await _runStallWatchdogTickForTest(mockLogger);

    // A fresh gh pr list call AND a prView per issue — proving tick 1's
    // failure left no cached "no PR" entries behind for either issue.
    expect(prListShellCalls()).toHaveLength(3);
    expect(mockIpc.prView).toHaveBeenCalledTimes(2);
  });
});

// ---------------------------------------------------------------------------
// #483 should-fix coverage — the stall/alert/invalidation branch and the
// watchdogPRCache prune loop had zero test coverage: the shared fixture
// above deliberately pins prView to checkStatus "PENDING" so
// detectAutonomousStall never returns stalled and autonomousCommands.ts's
// alert/invalidation code (lines ~797-849) never executes. These describe
// blocks drive that branch for real. Kills mutants T10 (delete the
// invalidation calls) and T11 (delete the prune loop).
// ---------------------------------------------------------------------------

describe("stall detection -> invalidation (Issue #483 should-fix, kills mutant T10)", () => {
  let mockLogger: Logger;
  let mockIpc: any;

  /** One in-progress issue, updated long enough ago to exceed the (env-forced) 5-minute stall threshold. */
  const staleBoardItem = {
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
    updatedAt: new Date("2026-08-11T09:00:00.000Z").toISOString(), // 3h before fake "now"
    isPR: false,
    isEpic: false,
  };

  beforeEach(() => {
    vi.clearAllMocks();
    vi.useFakeTimers();
    vi.setSystemTime(new Date("2026-08-11T12:00:00.000Z"));
    disposeAutonomousOutputChannel();
    resetWatchdogStateForTest();
    clearOpenPRsCache();

    // Force autoRedispatchStalled=true and a low threshold so the single
    // stale board item above trips detectAutonomousStall deterministically,
    // without depending on config.yaml contents.
    process.env.NIGHTGAUGE_AUTONOMOUS_STALL_ESCALATION_ENABLED = "true";
    process.env.NIGHTGAUGE_AUTONOMOUS_STALL_DETECTION_MINUTES = "5";
    process.env.NIGHTGAUGE_AUTONOMOUS_AUTO_REDISPATCH_STALLED = "true";

    mockLogger = createMockLogger();
    mockIpc = {
      configGetProjectConfig: vi.fn(async () => ({ projectNumber: 1, ownerType: undefined })),
      boardList: vi.fn(async () => [staleBoardItem]),
      prView: vi.fn(async () => ({
        state: "OPEN",
        checkStatus: "SUCCESS",
        mergeable: "MERGEABLE",
      })),
      autonomousStatus: vi.fn(async () => ({ status: "stopped" })),
    };
    (IpcClient.getInstance as any).mockReturnValue(mockIpc);
  });

  afterEach(() => {
    vi.clearAllTimers();
    vi.useRealTimers();
    resetWatchdogStateForTest();
    clearOpenPRsCache();
    delete process.env.NIGHTGAUGE_AUTONOMOUS_STALL_ESCALATION_ENABLED;
    delete process.env.NIGHTGAUGE_AUTONOMOUS_STALL_DETECTION_MINUTES;
    delete process.env.NIGHTGAUGE_AUTONOMOUS_AUTO_REDISPATCH_STALLED;
  });

  function prListShellCalls(): unknown[][] {
    return (exec as any).mock.calls.filter((c: unknown[]) => String(c[0]).startsWith("gh pr list"));
  }

  it("auto-redispatches a stalled issue, then re-fetches on the NEXT tick despite being inside the TTL", async () => {
    await _runStallWatchdogTickForTest(mockLogger);

    // Confirms the stall/alert/rerun branch actually executed.
    expect(vscode.window.createTerminal).toHaveBeenCalledTimes(1);
    expect(prListShellCalls()).toHaveLength(1);
    expect(mockIpc.prView).toHaveBeenCalledTimes(1);

    // Tick 2 — 2 minutes later, well inside PR_MAPPING_CACHE_TTL_MS (5 min).
    // Without invalidateOpenPRsCache()/watchdogPRCache.delete() on the
    // redispatch path (mutant T10), this tick would serve the cached
    // (now-stale) mapping and make ZERO further calls.
    vi.setSystemTime(new Date("2026-08-11T12:02:00.000Z"));
    await _runStallWatchdogTickForTest(mockLogger);

    expect(prListShellCalls()).toHaveLength(2);
    expect(mockIpc.prView).toHaveBeenCalledTimes(2);
  });
});

describe("watchdogPRCache prune loop (Issue #483 should-fix, kills mutant T11)", () => {
  let mockLogger: Logger;
  let mockIpc: any;

  beforeEach(() => {
    vi.clearAllMocks();
    vi.useFakeTimers();
    vi.setSystemTime(new Date("2026-08-11T12:00:00.000Z"));
    disposeAutonomousOutputChannel();
    resetWatchdogStateForTest();
    clearOpenPRsCache();

    mockLogger = createMockLogger();
    mockIpc = {
      configGetProjectConfig: vi.fn(async () => ({ projectNumber: 1, ownerType: undefined })),
      boardList: vi.fn(async () => boardItems),
      prView: vi.fn(async () => ({
        state: "OPEN",
        checkStatus: "PENDING",
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
    clearOpenPRsCache();
  });

  it("prunes entries for issues no longer seen In Progress, so a later reappearance costs a fresh prView", async () => {
    // Tick 1: both issues 201 + 202 in progress.
    await _runStallWatchdogTickForTest(mockLogger);
    expect(mockIpc.prView).toHaveBeenCalledTimes(2);

    // Tick 2 — 1 minute later, inside the TTL — boardList now reports ONLY
    // issue 201 (202 left "In Progress", e.g. merged/closed elsewhere).
    vi.setSystemTime(new Date("2026-08-11T12:01:00.000Z"));
    mockIpc.boardList = vi.fn(async () => [boardItems[0]]);
    await _runStallWatchdogTickForTest(mockLogger);
    // 201 still cached (inside TTL) — zero new prView calls this tick; 202's
    // entry is pruned at the end of this tick since it wasn't seen.
    expect(mockIpc.prView).toHaveBeenCalledTimes(2);

    // Tick 3 — 2 minutes later still (well inside the 5-min TTL from tick
    // 1's original fetch) — 202 reappears.
    vi.setSystemTime(new Date("2026-08-11T12:03:00.000Z"));
    mockIpc.boardList = vi.fn(async () => boardItems);
    await _runStallWatchdogTickForTest(mockLogger);

    // 202's entry was pruned on tick 2, so despite still being inside the
    // TTL window since it was ORIGINALLY cached, it costs a fresh prView.
    // Without the prune loop (mutant T11), this stays at 2 — proving the
    // loop actually ran.
    expect(mockIpc.prView).toHaveBeenCalledTimes(3);
  });
});

describe("stop -> restart cache clearing (Issue #483 should-fix)", () => {
  let mockLogger: Logger;
  let mockIpc: any;

  beforeEach(() => {
    vi.clearAllMocks();
    vi.useFakeTimers();
    vi.setSystemTime(new Date("2026-08-11T12:00:00.000Z"));
    disposeAutonomousOutputChannel();
    resetWatchdogStateForTest();
    clearOpenPRsCache();

    mockLogger = createMockLogger();
    mockIpc = {
      configGetProjectConfig: vi.fn(async () => ({ projectNumber: 1, ownerType: undefined })),
      boardList: vi.fn(async () => boardItems),
      prView: vi.fn(async () => ({
        state: "OPEN",
        checkStatus: "PENDING",
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
    clearOpenPRsCache();
  });

  function prListShellCalls(): unknown[][] {
    return (exec as any).mock.calls.filter((c: unknown[]) => String(c[0]).startsWith("gh pr list"));
  }

  it("clears the repo-level open-PR snapshot on stop, so a restart within the TTL still issues a fresh gh pr list", async () => {
    await _runStallWatchdogTickForTest(mockLogger);
    expect(prListShellCalls()).toHaveLength(1);
    expect(mockIpc.prView).toHaveBeenCalledTimes(2);

    _stopWatchdogForTest();

    // Still well inside PR_MAPPING_CACHE_TTL_MS (5 min) — the only thing
    // that should force a refetch here is the stop-time cache clear, not
    // TTL expiry.
    vi.setSystemTime(new Date("2026-08-11T12:01:00.000Z"));
    await _runStallWatchdogTickForTest(mockLogger);

    expect(prListShellCalls()).toHaveLength(2);
    expect(mockIpc.prView).toHaveBeenCalledTimes(4);
  });
});
