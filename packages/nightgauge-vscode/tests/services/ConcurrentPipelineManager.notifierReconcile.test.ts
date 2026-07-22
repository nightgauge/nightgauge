/**
 * #3873 Case 2: the standalone failure notifier
 * (ConcurrentPipelineManager.haltQueueOnSlotFailure) must reconcile against
 * BOTH the forge AND the failed stage's exit-record before paging. The original
 * #3835/#3840 guard only checked issue `state === "CLOSED"`, so:
 *
 *   - a pr-create that exited non-zero but whose exit-record says success:true
 *     (the exact incident — acmeapp-flutter #35 paged "failed at pr-create"
 *     while PR #67 was OPEN+MERGEABLE), and
 *   - an issue still OPEN whose branch PR was already open/merged
 *
 * both slipped through and paged the operator on completed work.
 *
 * This pins the broadened `shouldSuppressFailurePage`:
 *   - exit-record success:true  → no page
 *   - branch PR OPEN on forge   → no page
 *   - genuine failure (open, no PR, success:false) → STILL pages (neg control)
 *   - forge query throws         → STILL pages (fail-closed)
 */

import { describe, it, expect, vi, beforeEach } from "vitest";

// ── Mutable forge / exit-record fixtures the mocks read ──────────────────────
let mockGhIssueState = "OPEN";
let mockGhPrListJson = "[]"; // gh pr list --json state output
let mockExitRecordLines: string[] = []; // JSONL lines returned for ANY exit-records day file
// Optional per-day-file override keyed by the YYYY-MM-DD stamp embedded in the
// path — lets a test put different records in today's vs yesterday's file so the
// cross-day ordering is actually exercised. When a path's stamp isn't in the map
// the read fails (ENOENT), as a missing day file would.
let mockExitRecordByDay: Record<string, string[]> | null = null;
let mockReadFileThrows = false; // force fs.readFile to reject (fail-closed test)
let mockGhThrows = false; // force gh calls to reject (fail-closed test)

vi.mock("node:fs/promises", async (importActual) => {
  const actual = await importActual<typeof import("node:fs/promises")>();
  return {
    ...actual,
    default: actual,
    readFile: vi.fn(async (p: string, _enc?: unknown) => {
      if (mockReadFileThrows) throw new Error("ENOENT");
      const ps = String(p);
      if (ps.includes("exit-records")) {
        if (mockExitRecordByDay) {
          const m = ps.match(/(\d{4}-\d{2}-\d{2})\.jsonl$/);
          const day = m?.[1] ?? "";
          if (day in mockExitRecordByDay) return mockExitRecordByDay[day].join("\n");
          throw Object.assign(new Error("ENOENT"), { code: "ENOENT" });
        }
        return mockExitRecordLines.join("\n");
      }
      throw Object.assign(new Error("ENOENT"), { code: "ENOENT" });
    }),
  };
});

// UTC day stamps matching the production helper (toISOString → UTC).
function utcDayStamp(offsetDays = 0): string {
  return new Date(Date.now() + offsetDays * 24 * 60 * 60 * 1000).toISOString().slice(0, 10);
}

vi.mock("child_process", async (importActual) => {
  const actual = await importActual<typeof import("child_process")>();
  return {
    ...actual,
    exec: (_cmd: string, optsOrCb: unknown, maybeCb?: unknown) => {
      const cb = (typeof optsOrCb === "function" ? optsOrCb : maybeCb) as (
        err: Error | null,
        out: { stdout: string; stderr: string }
      ) => void;
      cb(null, { stdout: "", stderr: "" });
    },
    execFile: (file: string, args: string[], optsOrCb: unknown, maybeCb?: unknown) => {
      const cb = (typeof optsOrCb === "function" ? optsOrCb : maybeCb) as (
        err: Error | null,
        out: { stdout: string; stderr: string }
      ) => void;
      if (mockGhThrows) {
        cb(new Error("gh: API rate limit exceeded"), { stdout: "", stderr: "" });
        return;
      }
      if (file === "gh" && args.includes("issue") && args.includes("view")) {
        cb(null, { stdout: JSON.stringify({ state: mockGhIssueState }), stderr: "" });
      } else if (file === "gh" && args.includes("pr") && args.includes("list")) {
        cb(null, { stdout: mockGhPrListJson, stderr: "" });
      } else {
        cb(null, { stdout: "", stderr: "" });
      }
    },
  };
});

vi.mock("vscode", () => ({
  EventEmitter: class {
    private listeners: Array<(...args: any[]) => void> = [];
    event = (listener: (...args: any[]) => void) => {
      this.listeners.push(listener);
      return { dispose: () => {} };
    };
    fire = (data: any) => this.listeners.forEach((l) => l(data));
    dispose = vi.fn();
  },
  workspace: { workspaceFolders: [{ uri: { fsPath: "/test-repo" } }] },
  window: {
    showErrorMessage: vi.fn().mockResolvedValue(undefined),
    showWarningMessage: vi.fn().mockResolvedValue(undefined),
    showInformationMessage: vi.fn().mockResolvedValue(undefined),
  },
  commands: { executeCommand: vi.fn().mockResolvedValue(undefined) },
  env: { openExternal: vi.fn().mockResolvedValue(true) },
  Uri: { parse: vi.fn((s: string) => ({ toString: () => s })) },
}));

vi.mock("../../src/utils/WorktreeManager", () => ({
  WorktreeManager: vi.fn(function () {
    return {
      create: vi.fn().mockImplementation((issueNumber: number, branchName: string) =>
        Promise.resolve({
          path: `/test-repo/.worktrees/issue-${issueNumber}`,
          branch: branchName,
          issueNumber,
          exists: true,
        })
      ),
      cleanup: vi.fn().mockResolvedValue(undefined),
      cleanupOrphans: vi.fn().mockResolvedValue(0),
      cleanupAll: vi.fn().mockResolvedValue(undefined),
      listActive: vi.fn().mockResolvedValue([]),
      getRepoRoot: vi.fn().mockReturnValue("/test-repo"),
      getWorktreePath: vi
        .fn()
        .mockImplementation((n: number) => `/test-repo/.worktrees/issue-${n}`),
    };
  }),
}));

vi.mock("../../src/utils/incrediConfig", () => ({
  getConcurrentPipelineConfig: vi
    .fn()
    .mockReturnValue({ maxConcurrent: 2, worktreeBase: ".worktrees" }),
}));

const mockAutonomousPause = vi.fn().mockResolvedValue(undefined);
const mockAutonomousStatus = vi.fn();

vi.mock("../../src/services/IpcClient", () => ({
  IpcClient: {
    getInstance: () => ({
      autonomousStatus: mockAutonomousStatus,
      autonomousPause: mockAutonomousPause,
    }),
  },
}));

import { ConcurrentPipelineManager } from "../../src/services/ConcurrentPipelineManager";

function makeQueueItem(issueNumber: number) {
  return {
    issueNumber,
    title: `Issue #${issueNumber}`,
    position: 1,
    status: "pending",
    addedAt: new Date().toISOString(),
    repoName: "nightgauge/acmeapp-flutter",
  };
}

function createControllableFactory() {
  const resolvers = new Map<number, (result: any) => void>();
  const factory = vi.fn().mockImplementation((_workDir: string, issueNumber: number) => {
    const promise = new Promise((resolve) => resolvers.set(issueNumber, resolve));
    return {
      orchestrator: {
        setWorktreeOverride: vi.fn(),
        setRepoOverride: vi.fn(),
        setUnattended: vi.fn(),
        runPipeline: vi.fn().mockReturnValue(promise),
        stop: vi.fn(),
        dispose: vi.fn(),
      },
      stateService: {
        onStateChanged: vi.fn().mockReturnValue({ dispose: vi.fn() }),
        onPhaseStart: vi.fn().mockReturnValue({ dispose: vi.fn() }),
        onPhaseComplete: vi.fn().mockReturnValue({ dispose: vi.fn() }),
        onUnifiedTokenUsage: vi.fn().mockReturnValue({ dispose: vi.fn() }),
        getState: vi.fn().mockResolvedValue(null),
        initEmpty: vi.fn(),
        setMeta: vi.fn(),
        dispose: vi.fn(),
      },
    };
  });
  return {
    factory,
    failIssue: (issueNumber: number, failedStage: string, errMessage: string) =>
      resolvers.get(issueNumber)?.({
        success: false,
        completedStages: ["issue-pickup"],
        skippedStages: [],
        deferredStages: [],
        failedStage,
        error: new Error(errMessage),
        totalDurationMs: 10000,
      }),
  };
}

function makeManager(issueNumber = 35) {
  const queueClear = vi.fn().mockResolvedValue(undefined);
  const queueService = {
    dequeueIndependent: vi
      .fn()
      .mockResolvedValueOnce([makeQueueItem(issueNumber)])
      .mockResolvedValue([]),
    updateActiveSlots: vi.fn().mockResolvedValue(undefined),
    drainBlockedSuccessors: vi.fn().mockResolvedValue([]),
    enqueue: vi.fn().mockResolvedValue(null),
    clear: queueClear,
    getQueue: vi.fn().mockResolvedValue({
      items: [makeQueueItem(9999)],
      status: "idle",
    }),
  };
  const workspaceManager = {
    findRepositoryByGitHub: vi.fn().mockReturnValue({ path: "/test-repo" }),
  };
  const controllable = createControllableFactory();
  const manager = new ConcurrentPipelineManager(
    "/test-repo",
    queueService as any,
    controllable.factory,
    { info: vi.fn(), warn: vi.fn(), error: vi.fn(), debug: vi.fn(), getChannel: vi.fn() } as any,
    { maxConcurrent: 1, worktreeBase: ".worktrees" },
    workspaceManager as any
  );
  return { manager, controllable, queueClear };
}

describe("ConcurrentPipelineManager — notifier reconciles forge + exit-record (#3873 Case 2)", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mockAutonomousStatus.mockResolvedValue({ status: "running" });
    mockGhIssueState = "OPEN";
    mockGhPrListJson = "[]";
    mockExitRecordLines = [];
    mockExitRecordByDay = null;
    mockReadFileThrows = false;
    mockGhThrows = false;
  });

  it("does NOT page when the failed stage's exit-record says success:true (the #35 incident shape)", async () => {
    // pr-create exited non-zero but its exit-record is success:true; issue OPEN.
    mockGhIssueState = "OPEN";
    mockGhPrListJson = "[]";
    mockExitRecordLines = [
      JSON.stringify({ ts: "2099-01-01T00:00:00Z", issue: 35, stage: "pr-create", success: true }),
    ];

    const { manager, controllable, queueClear } = makeManager(35);
    await manager.fillSlots();
    controllable.failIssue(35, "pr-create", "API Error: 429");
    await manager.settleForTest(35);

    expect(mockAutonomousPause).not.toHaveBeenCalled();
    expect(queueClear).not.toHaveBeenCalled();
  });

  it("does NOT page when the branch PR is OPEN on the forge (issue still open)", async () => {
    mockGhIssueState = "OPEN";
    mockGhPrListJson = JSON.stringify([{ state: "OPEN" }]);
    mockExitRecordLines = []; // no success record — branch PR is the only signal

    const { manager, controllable, queueClear } = makeManager(35);
    await manager.fillSlots();
    controllable.failIssue(35, "pr-create", "");
    await manager.settleForTest(35);

    expect(mockAutonomousPause).not.toHaveBeenCalled();
    expect(queueClear).not.toHaveBeenCalled();
  });

  it("STILL pages on a genuine failure (issue open, no PR, exit-record success:false)", async () => {
    mockGhIssueState = "OPEN";
    mockGhPrListJson = "[]";
    mockExitRecordLines = [
      JSON.stringify({
        ts: "2099-01-01T00:00:00Z",
        issue: 35,
        stage: "feature-validate",
        success: false,
      }),
    ];

    const { manager, controllable } = makeManager(35);
    await manager.fillSlots();
    controllable.failIssue(
      35,
      "feature-validate",
      "Schema validation failed: missing required field 'plan'"
    );
    await manager.settleForTest(35);

    expect(mockAutonomousPause).toHaveBeenCalledTimes(1);
    const [, triggeredBy] = mockAutonomousPause.mock.calls[0];
    expect(triggeredBy).toBe("haltQueueOnSlotFailure");
  });

  it("STILL pages when the exit-record matches a DIFFERENT stage (latest record for the failed stage is false)", async () => {
    // An earlier stage succeeded, but the FAILED stage's own record is false —
    // must page (do not over-suppress on an unrelated stage's success).
    mockGhIssueState = "OPEN";
    mockGhPrListJson = "[]";
    mockExitRecordLines = [
      JSON.stringify({
        ts: "2099-01-01T00:00:00Z",
        issue: 35,
        stage: "feature-dev",
        success: true,
      }),
      JSON.stringify({
        ts: "2099-01-01T00:01:00Z",
        issue: 35,
        stage: "feature-validate",
        success: false,
      }),
    ];

    const { manager, controllable } = makeManager(35);
    await manager.fillSlots();
    controllable.failIssue(35, "feature-validate", "subagent crashed");
    await manager.settleForTest(35);

    expect(mockAutonomousPause).toHaveBeenCalledTimes(1);
  });

  it("uses the LATEST record for the failed stage (append-only newest-last)", async () => {
    // Two records for the same {issue, stage}: an earlier false then a later
    // true (a retry that succeeded). The latest (true) must win → no page.
    mockGhIssueState = "OPEN";
    mockGhPrListJson = "[]";
    mockExitRecordLines = [
      JSON.stringify({ ts: "2099-01-01T00:00:00Z", issue: 35, stage: "pr-create", success: false }),
      JSON.stringify({ ts: "2099-01-01T00:05:00Z", issue: 35, stage: "pr-create", success: true }),
    ];

    const { manager, controllable, queueClear } = makeManager(35);
    await manager.fillSlots();
    controllable.failIssue(35, "pr-create", "phantom failure after retry");
    await manager.settleForTest(35);

    expect(mockAutonomousPause).not.toHaveBeenCalled();
    expect(queueClear).not.toHaveBeenCalled();
  });

  it("STILL pages when all forge queries throw (fail-closed)", async () => {
    mockGhThrows = true;
    mockReadFileThrows = true; // no exit-record either
    mockGhIssueState = "OPEN";

    const { manager, controllable } = makeManager(35);
    await manager.fillSlots();
    controllable.failIssue(35, "pr-create", "API Error: 500");
    await manager.settleForTest(35);

    expect(mockAutonomousPause).toHaveBeenCalledTimes(1);
    const [, triggeredBy] = mockAutonomousPause.mock.calls[0];
    expect(triggeredBy).toBe("haltQueueOnSlotFailure");
  });

  it("cross-day: today's success record wins over yesterday's failure record (no page)", async () => {
    // Yesterday: failed first attempt. Today: successful retry. The reader must
    // process yesterday THEN today so the latest (today, success:true) wins.
    mockGhIssueState = "OPEN";
    mockGhPrListJson = "[]";
    mockExitRecordByDay = {
      [utcDayStamp(-1)]: [
        JSON.stringify({ ts: "yesterday", issue: 35, stage: "pr-create", success: false }),
      ],
      [utcDayStamp(0)]: [
        JSON.stringify({ ts: "today", issue: 35, stage: "pr-create", success: true }),
      ],
    };

    const { manager, controllable, queueClear } = makeManager(35);
    await manager.fillSlots();
    controllable.failIssue(35, "pr-create", "phantom failure after cross-midnight retry");
    await manager.settleForTest(35);

    expect(mockAutonomousPause).not.toHaveBeenCalled();
    expect(queueClear).not.toHaveBeenCalled();
  });

  it("suppresses via the forge even when the exit-record read throws (positive forge signal short-circuits IO error)", async () => {
    // exit-record IO fails, but the branch PR is MERGED on the forge — the OR
    // gate must still suppress (a single positive check is enough; an IO error
    // on one leg does not force a page).
    mockReadFileThrows = true;
    mockGhIssueState = "OPEN";
    mockGhPrListJson = JSON.stringify([{ state: "MERGED" }]);

    const { manager, controllable, queueClear } = makeManager(35);
    await manager.fillSlots();
    controllable.failIssue(35, "pr-create", "API Error: 429");
    await manager.settleForTest(35);

    expect(mockAutonomousPause).not.toHaveBeenCalled();
    expect(queueClear).not.toHaveBeenCalled();
  });
});
