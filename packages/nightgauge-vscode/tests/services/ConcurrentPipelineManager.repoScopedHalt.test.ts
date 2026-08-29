/**
 * #1148 — one issue's failure must not pause autonomous work across the whole
 * workspace.
 *
 * `haltQueueOnSlotFailure` used to answer a terminal stage failure by clearing
 * the ENTIRE queue and calling `autonomousPause()`, which stops dispatch for
 * every repository in a multi-repo workspace. The evidence is one issue in one
 * repo; the blast radius was everything.
 *
 * Two changes are pinned here:
 *
 *   1. The halt is scoped to the repository that produced the failure —
 *      `autonomousPauseRepo`, and a queue drain restricted to that repo's
 *      pending items.
 *   2. A `blocked` terminal (#1142/#1147) does not halt at all. That run
 *      diagnosed itself, wrote a durable finding, posted a comment and raised
 *      a card; freezing the queue adds no decision a human can act on.
 *
 * And one thing is pinned as UNCHANGED: the environmental / overload / network
 * skip set (#3444, #4002), which is a separate and already-correct decision.
 * Those cases must reach NEITHER pause verb — asserting only on the old
 * fleet-wide `autonomousPause` would let this change silently widen the halt
 * to the repo verb without a single test going red.
 */

import { describe, it, expect, vi, beforeEach } from "vitest";

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
    showErrorMessage: (...args: any[]) => mockShowErrorMessage(...args),
    showWarningMessage: (...args: any[]) => mockShowWarningMessage(...args),
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

vi.mock("../../src/utils/nightgaugeConfig", () => ({
  getConcurrentPipelineConfig: vi
    .fn()
    .mockReturnValue({ maxConcurrent: 2, worktreeBase: ".worktrees" }),
}));

vi.mock("../../src/utils/failureComment", () => ({
  postFailureComment: vi.fn().mockResolvedValue(undefined),
}));

const mockAutonomousPause = vi.fn().mockResolvedValue(undefined);
const mockAutonomousPauseRepo = vi.fn().mockResolvedValue(undefined);
const mockAutonomousStatus = vi.fn();
const mockShowWarningMessage = vi.fn().mockResolvedValue(undefined);
const mockShowErrorMessage = vi.fn().mockResolvedValue(undefined);

const gitComposeBranchName = vi.fn(async (issueNumber: number) => ({
  name: `fix/${issueNumber}-work`,
}));

vi.mock("../../src/services/IpcClient", () => ({
  IpcClient: {
    getInstance: () => ({
      gitComposeBranchName,
      autonomousStatus: mockAutonomousStatus,
      autonomousPause: mockAutonomousPause,
      autonomousPauseRepo: mockAutonomousPauseRepo,
    }),
  },
}));

import { ConcurrentPipelineManager } from "../../src/services/ConcurrentPipelineManager";

const FAILING_REPO = "acme/web";
const OTHER_REPO = "acme/api";
const FAILING_ISSUE = 101;

function makeQueueItem(issueNumber: number, repoName: string, status = "pending") {
  return {
    issueNumber,
    title: `Issue #${issueNumber}`,
    position: 1,
    status,
    addedAt: new Date().toISOString(),
    repoName,
  };
}

function createControllableFactory() {
  const resolvers = new Map<number, (result: any) => void>();
  const factory = vi.fn().mockImplementation((_workDir: string, issueNumber: number) => {
    const promise = new Promise((resolve) => resolvers.set(issueNumber, resolve));
    return {
      orchestrator: {
        setWorktreeOverride: vi.fn(),
        setRunRepoRoot: vi.fn(),
        setRepoOverride: vi.fn(),
        setUnattended: vi.fn(),
        resolveRunRepoSlug: vi.fn().mockResolvedValue(FAILING_REPO),
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
        beginRun: vi.fn(),
        endRun: vi.fn(),
        getRunId: vi.fn().mockReturnValue(null),
        initEmpty: vi.fn(),
        setMeta: vi.fn(),
        dispose: vi.fn(),
      },
    };
  });
  return {
    factory,
    /** Terminal failure with an arbitrary error message. */
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
    /**
     * The #1147 shape: `blocked` with a durable out-of-scope finding already
     * recorded. `outOfScopeFinding` is the typed flag, not a prefix on
     * `blocker` — the OTHER producer of `blocked` (#190's pr-merge dead end) is
     * a real repo-config fault and must keep halting.
     */
    blockIssue: (issueNumber: number, outOfScopeFinding: boolean) =>
      resolvers.get(issueNumber)?.({
        success: false,
        completedStages: ["issue-pickup", "feature-planning", "feature-dev"],
        skippedStages: [],
        deferredStages: [],
        failedStage: "feature-validate",
        blocked: {
          blocker: outOfScopeFinding
            ? "out-of-scope: needs #99 landed first"
            : "repo-config: required-check-config-mismatch:Sentry Smoke",
          outOfScopeFinding: outOfScopeFinding || undefined,
        },
        error: new Error("BLOCKED"),
        totalDurationMs: 10000,
      }),
  };
}

/**
 * One slot for FAILING_ISSUE in `acme/web`, with pending queue items in BOTH
 * repositories so a repo-scoped drain is distinguishable from a queue-wide
 * `clear()`.
 */
function makeManager() {
  const queueClear = vi.fn().mockResolvedValue(undefined);
  const queueRemove = vi.fn().mockResolvedValue(true);
  const queueService = {
    dequeueIndependent: vi
      .fn()
      .mockResolvedValueOnce([makeQueueItem(FAILING_ISSUE, FAILING_REPO)])
      .mockResolvedValue([]),
    updateActiveSlots: vi.fn().mockResolvedValue(undefined),
    drainBlockedSuccessors: vi.fn().mockResolvedValue([]),
    enqueue: vi.fn().mockResolvedValue(null),
    clear: queueClear,
    remove: queueRemove,
    getQueue: vi.fn().mockResolvedValue({
      items: [
        makeQueueItem(201, FAILING_REPO),
        makeQueueItem(202, FAILING_REPO),
        makeQueueItem(301, OTHER_REPO),
        makeQueueItem(302, OTHER_REPO),
      ],
      status: "idle",
    }),
  };
  const controllable = createControllableFactory();
  const manager = new ConcurrentPipelineManager(
    "/test-repo",
    queueService as any,
    controllable.factory,
    { info: vi.fn(), warn: vi.fn(), error: vi.fn(), debug: vi.fn(), getChannel: vi.fn() } as any,
    { maxConcurrent: 1, worktreeBase: ".worktrees" }
  );
  return { manager, controllable, queueService, queueClear, queueRemove };
}

describe("ConcurrentPipelineManager — repo-scoped halt (#1148)", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mockAutonomousStatus.mockResolvedValue({ status: "running" });
  });

  // ── AC1: the halt stops one repository, not the workspace ───────────

  it("halts only the failing repository — never the fleet", async () => {
    const { manager, controllable } = makeManager();
    await manager.fillSlots();
    controllable.failIssue(FAILING_ISSUE, "feature-validate", "assertion failed in web/src/foo.ts");
    await manager.settleForTest(FAILING_ISSUE);

    expect(mockAutonomousPauseRepo).toHaveBeenCalledTimes(1);
    expect(mockAutonomousPauseRepo).toHaveBeenCalledWith(
      FAILING_REPO,
      expect.stringContaining(`issue #${FAILING_ISSUE} failed at feature-validate`),
      "haltQueueOnSlotFailure",
      FAILING_ISSUE,
      "feature-validate",
      expect.anything(),
      expect.anything()
    );
    // The fleet-wide verb is the thing being removed from this path.
    expect(mockAutonomousPause).not.toHaveBeenCalled();
  });

  it("leaves another repository's queued work alone", async () => {
    const { manager, controllable, queueClear, queueRemove } = makeManager();
    await manager.fillSlots();
    controllable.failIssue(FAILING_ISSUE, "feature-validate", "assertion failed");
    await manager.settleForTest(FAILING_ISSUE);

    // clear() is a queue-wide truncation — using it would delete the other
    // repository's pending work, which this halt has no evidence about.
    expect(queueClear).not.toHaveBeenCalled();

    const removed = queueRemove.mock.calls.map((c) => c[0]);
    expect(removed.sort()).toEqual([201, 202]);
    expect(removed).not.toContain(301);
    expect(removed).not.toContain(302);
  });

  it("does not abort a slot that is still running in another repository", async () => {
    const { manager, controllable } = makeManager();
    await manager.fillSlots();

    // A second, live dispatch in the OTHER repo. The halt suppresses future
    // fills; it has never aborted running work, and scoping it must not
    // change that.
    const other = await (manager as any).startSlot({
      issueNumber: 999,
      title: "other repo work",
      position: 2,
      status: "pending",
      addedAt: new Date().toISOString(),
      repoName: OTHER_REPO,
    });
    expect(other).toBe("started");
    const liveBefore = manager.getActiveSlots().length;

    controllable.failIssue(FAILING_ISSUE, "feature-validate", "assertion failed");
    await manager.settleForTest(FAILING_ISSUE);

    const stillLive = manager.getActiveSlots();
    expect(stillLive.map((s) => s.issueNumber)).toContain(999);
    expect(stillLive.length).toBe(liveBefore - 1); // only the failed slot left
  });

  // ── AC3: the gate still exists, it is only narrower ─────────────────

  it("still halts on a genuine defect and still asks for an explicit Resume", async () => {
    const { manager, controllable } = makeManager();
    await manager.fillSlots();
    controllable.failIssue(FAILING_ISSUE, "feature-validate", "assertion failed");
    await manager.settleForTest(FAILING_ISSUE);

    expect(mockAutonomousPauseRepo).toHaveBeenCalledTimes(1);
    // A modal, not a toast: a genuine defect is still interrupt-worthy, and
    // the operator is still told a human action is required to continue.
    const [title, options, ...rest] = mockShowErrorMessage.mock.calls[0];
    expect(title).toContain(FAILING_REPO);
    expect(options).toMatchObject({ modal: true });
    expect(options.detail).toContain(FAILING_REPO);
    expect(options.detail).toMatch(/Resume/);
    expect(rest.length).toBeGreaterThan(0);
  });

  // ── AC2: a blocked terminal halts nothing ───────────────────────────

  it("does not halt at all for a blocked terminal with a durable finding", async () => {
    const { manager, controllable, queueClear, queueRemove } = makeManager();
    await manager.fillSlots();
    controllable.blockIssue(FAILING_ISSUE, true);
    await manager.settleForTest(FAILING_ISSUE);

    expect(mockAutonomousPauseRepo).not.toHaveBeenCalled();
    expect(mockAutonomousPause).not.toHaveBeenCalled();
    expect(queueClear).not.toHaveBeenCalled();
    expect(queueRemove).not.toHaveBeenCalled();
    expect(mockShowErrorMessage).not.toHaveBeenCalled();
  });

  it("still halts for the OTHER producer of `blocked` — a repo-config dead end", async () => {
    const { manager, controllable } = makeManager();
    await manager.fillSlots();
    controllable.blockIssue(FAILING_ISSUE, false);
    await manager.settleForTest(FAILING_ISSUE);

    // #190's pr-merge blocked terminal (branch protection / required-check
    // mismatch) is a real repo-config fault a human must clear. Keying the
    // skip on a substring of `blocker` instead of the typed flag would make
    // these two indistinguishable.
    expect(mockAutonomousPauseRepo).toHaveBeenCalledTimes(1);
  });

  // ── AC4: the environmental skip set is UNCHANGED ────────────────────

  describe("environmental / overload / network skips are unchanged", () => {
    const SKIP_CASES: Array<[string, string]> = [
      [
        "rate_limit_quota_exhausted",
        "[stall-killed] feature-dev terminated.\nUpstream signal: [rate-limit-quota-exhausted] resetsAt=1715399460",
      ],
      ["stream_idle_timeout", "API Error: Stream idle timeout - partial response received"],
      [
        "network_unavailable",
        "network unavailable: extended GitHub connectivity loss (12 consecutive failures over 8m)",
      ],
      ["api_overloaded", "API Error: 529 Overloaded. This is a server-side issue; please retry."],
      ["api_connection_lost", "API Error: The socket connection was closed unexpectedly"],
      [
        "github_network_outage",
        "[pipeline-start-failure] github-network-outage: api.github.com unreachable",
      ],
      ["bare session/usage limit", "Claude AI usage limit reached"],
      ["stall_kill", "[stall-killed] feature-dev exceeded idle threshold"],
    ];

    it.each(SKIP_CASES)("%s reaches neither pause verb", async (_kind, message) => {
      const { manager, controllable, queueClear, queueRemove } = makeManager();
      await manager.fillSlots();
      controllable.failIssue(FAILING_ISSUE, "feature-dev", message);
      await manager.settleForTest(FAILING_ISSUE);

      expect(mockAutonomousPause).not.toHaveBeenCalled();
      expect(mockAutonomousPauseRepo).not.toHaveBeenCalled();
      expect(queueClear).not.toHaveBeenCalled();
      expect(queueRemove).not.toHaveBeenCalled();
    });
  });
});
