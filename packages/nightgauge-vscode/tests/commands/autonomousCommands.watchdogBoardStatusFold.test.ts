/**
 * End-to-end pin for the stall watchdog on a NIGHTGAUGE-PROVISIONED board
 * (Issue #623).
 *
 * `BoardItem.status` is the raw single-select option label: Go's
 * `gh.BoardService.ListItems` copies the option name verbatim into
 * `BoardItem.Status`, and it crosses IPC unchanged. The column nightgauge's
 * own provisioner creates is spelled **"In progress"**
 * (`DefaultFieldSchema` in internal/github/project.go, mirrored by
 * `state.StatusInProgress`), while a hand-made board commonly spells it
 * "In Progress".
 *
 * The pre-fix guard in `detectAutonomousStall` compared
 * `input.boardStatus !== "In Progress"` exactly, so on every provisioned
 * board it returned early and the watchdog never fired — no error, no log,
 * no card. Every existing watchdog test fixture happens to use the hand-made
 * spelling, which is precisely why the defect survived: the unit tests and
 * the #483 cache tests all pinned the one casing that worked.
 *
 * This file drives the REAL watchdog tick with the provisioned spelling. It
 * is load-bearing: against the pre-fix source both tests fail — no terminal
 * is created, no warning is shown, and no PR-detail call is even attempted
 * past the guard.
 *
 * @see src/utils/autonomousStallDetector.ts
 * @see src/utils/projectFieldMapping.ts — boardStatusEquals
 * @see internal/state/board_state.go — BoardStatus.EqualFold (#413)
 * @see Issue #623
 */

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import * as vscode from "vscode";
import {
  disposeAutonomousOutputChannel,
  resetWatchdogStateForTest,
  _runStallWatchdogTickForTest,
} from "../../src/commands/autonomousCommands";
import { IpcClient } from "../../src/services/IpcClient";
import { clearOpenPRsCache } from "../../src/utils/prDetection";
import type { Logger } from "../../src/utils/logger";

// ---------------------------------------------------------------------------
// Mocks — same boundaries as autonomousCommands.watchdogPRCache.test.ts, so
// prDetection.ts's batching/TTL logic and the whole watchdog tick run for
// real. Only the network-touching primitives are stubbed.
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
          title: "fix: issue 623001",
          body: "Closes #623001",
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

/**
 * One stalled, non-PR, non-epic board item. `status` is supplied per test so
 * the ONLY difference between the provisioned-board and hand-made-board runs
 * is the column's capitalization.
 */
const stalledBoardItem = (status: string) => ({
  id: "i1",
  number: 623001,
  title: "Issue 623001",
  state: "OPEN",
  status,
  priority: "",
  size: "",
  labels: [],
  assignees: [],
  repo: "nightgauge",
  url: "https://github.com/nightgauge/nightgauge/issues/623001",
  // 3h before the fake "now" — far past the 5-minute threshold forced below.
  updatedAt: new Date("2026-08-11T09:00:00.000Z").toISOString(),
  isPR: false,
  isEpic: false,
});

describe("stall watchdog fires on a provisioned board's 'In progress' column (#623)", () => {
  let mockLogger: Logger;

  /** Build the IPC mock for a board whose In-progress column is spelled `status`. */
  function ipcForBoardSpelling(status: string) {
    return {
      configGetProjectConfig: vi.fn(async () => ({ projectNumber: 1, ownerType: undefined })),
      boardList: vi.fn(async () => [stalledBoardItem(status)]),
      prView: vi.fn(async () => ({
        state: "OPEN",
        checkStatus: "SUCCESS",
        mergeable: "MERGEABLE",
      })),
      autonomousStatus: vi.fn(async () => ({ status: "stopped" })),
    };
  }

  beforeEach(() => {
    vi.clearAllMocks();
    vi.useFakeTimers();
    vi.setSystemTime(new Date("2026-08-11T12:00:00.000Z"));
    disposeAutonomousOutputChannel();
    resetWatchdogStateForTest();
    clearOpenPRsCache();

    // Force auto-redispatch and a low threshold so the single stale item trips
    // the detector deterministically, independent of config.yaml contents.
    process.env.NIGHTGAUGE_AUTONOMOUS_STALL_ESCALATION_ENABLED = "true";
    process.env.NIGHTGAUGE_AUTONOMOUS_STALL_DETECTION_MINUTES = "5";
    process.env.NIGHTGAUGE_AUTONOMOUS_AUTO_REDISPATCH_STALLED = "true";

    mockLogger = createMockLogger();
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

  it("detects the stall and re-runs pr-merge when the column is spelled 'In progress'", async () => {
    // "In progress" is what `nightgauge project provision` writes. Pre-fix,
    // this tick did nothing at all.
    const mockIpc = ipcForBoardSpelling("In progress");
    (IpcClient.getInstance as any).mockReturnValue(mockIpc);

    await _runStallWatchdogTickForTest(mockLogger);

    // The recovery branch actually ran: a terminal was opened for the
    // pr-merge re-run and the operator was warned.
    expect(vscode.window.createTerminal).toHaveBeenCalledTimes(1);
    expect(vscode.window.showWarningMessage).toHaveBeenCalledTimes(1);

    // ...and the stall was reported with the recovery command.
    const logged = mockOutputChannel.appendLine.mock.calls.map((c) => String(c[0])).join("\n");
    expect(logged).toContain("Stalled issue detected: nightgauge/nightgauge#623001");
    expect(logged).toContain("nightgauge pr merge 701");
  });

  it("reaches the SAME outcome for 'In progress' and 'In Progress'", async () => {
    // Both spellings name one column. Capitalization is board-provenance
    // trivia; it must not change what the watchdog does.
    const observe = async (status: string) => {
      resetWatchdogStateForTest();
      clearOpenPRsCache();
      vi.clearAllMocks();
      const mockIpc = ipcForBoardSpelling(status);
      (IpcClient.getInstance as any).mockReturnValue(mockIpc);

      await _runStallWatchdogTickForTest(mockLogger);

      return {
        terminals: (vscode.window.createTerminal as any).mock.calls.length,
        warnings: (vscode.window.showWarningMessage as any).mock.calls.length,
        prViews: mockIpc.prView.mock.calls.length,
        reported: mockOutputChannel.appendLine.mock.calls
          .map((c) => String(c[0]))
          .filter((line) => line.includes("Stalled issue detected"))
          .map((line) => line.replace(/^\[[^\]]+\]\s*/, "")),
      };
    };

    const provisioned = await observe("In progress");
    const handMade = await observe("In Progress");

    expect(provisioned).toEqual(handMade);
    expect(provisioned.terminals).toBe(1);
    expect(provisioned.reported).toHaveLength(1);
  });
});
