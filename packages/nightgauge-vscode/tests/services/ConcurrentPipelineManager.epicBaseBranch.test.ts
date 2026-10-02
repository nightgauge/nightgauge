/**
 * Which epic branch a concurrent slot's worktree is based on (#2377).
 *
 * An epic branch, `epic/<N>-<slug>`, names issue #N of the repository it is
 * pushed to. The slot manager used to look for `epic/<epicNumber>-*` in the
 * sub-issue's own checkout whatever repository the epic lived in, so a
 * sub-issue of `org/repo-a#20` that lives in `org/repo-b` was based on
 * `org/repo-b#20`'s branch. The Go queue now records the epic's repository
 * (`epicRepo`) and the manager uses the epic branch only when that repository
 * is the sub-issue's own.
 */

import { describe, it, expect, vi, beforeEach } from "vitest";

vi.mock("vscode", () => ({
  EventEmitter: class {
    private listeners: Array<(...args: any[]) => void> = [];
    event = (listener: (...args: any[]) => void) => {
      this.listeners.push(listener);
      return {
        dispose: () => {
          this.listeners = this.listeners.filter((l) => l !== listener);
        },
      };
    };
    fire = (data: any) => {
      this.listeners.forEach((l) => l(data));
    };
    dispose = vi.fn();
  },
  workspace: {
    workspaceFolders: [{ uri: { fsPath: "/workspace/repo-a" } }],
  },
  window: {
    showErrorMessage: vi.fn().mockResolvedValue(undefined),
    showWarningMessage: vi.fn().mockResolvedValue(undefined),
  },
  commands: {
    executeCommand: vi.fn().mockResolvedValue(undefined),
  },
}));

const { createOptions, lsRemote } = vi.hoisted(() => ({
  /** The options each issue's worktree was created with. */
  createOptions: new Map<number, { baseBranch?: string }>(),
  /** Every `git ls-remote` the manager ran, with the checkout it ran in. */
  lsRemote: [] as Array<{ cmd: string; cwd: string }>,
}));

vi.mock("../../src/utils/WorktreeManager", () => ({
  WorktreeManager: vi.fn(function (this: unknown, repoRoot: string) {
    return {
      create: vi
        .fn()
        .mockImplementation(
          async (issueNumber: number, branchName: string, opts: { baseBranch?: string }) => {
            createOptions.set(issueNumber, opts);
            return {
              path: `${repoRoot}/.worktrees/issue-${issueNumber}`,
              branch: branchName,
              issueNumber,
              exists: true,
            };
          }
        ),
      cleanup: vi.fn().mockResolvedValue(undefined),
      cleanupOrphans: vi.fn().mockResolvedValue(0),
      cleanupAll: vi.fn().mockResolvedValue(undefined),
      listActive: vi.fn().mockResolvedValue([]),
      getRepoRoot: vi.fn().mockReturnValue(repoRoot),
      getWorktreePath: vi
        .fn()
        .mockImplementation((n: number) => `${repoRoot}/.worktrees/issue-${n}`),
    };
  }),
}));

// Both checkouts hold an epic/20-* branch: repo-a's is its epic #20's, and
// repo-b's belongs to repo-b's own, unrelated #20.
vi.mock("child_process", async (importActual) => {
  const actual = await importActual<typeof import("child_process")>();
  return {
    ...actual,
    exec: (cmd: string, optsOrCb: unknown, maybeCb?: unknown) => {
      const cb = (typeof optsOrCb === "function" ? optsOrCb : maybeCb) as (
        err: Error | null,
        out: { stdout: string; stderr: string }
      ) => void;
      const cwd =
        (typeof optsOrCb === "object" && optsOrCb !== null
          ? (optsOrCb as { cwd?: string }).cwd
          : undefined) ?? "";
      if (cmd.includes("ls-remote")) {
        lsRemote.push({ cmd, cwd });
        const owner = cwd.endsWith("repo-b") ? "repo-b" : "repo-a";
        const stdout = cmd.includes('"epic/20-*"') ? `epic/20-${owner}-epic\n` : "";
        cb(null, { stdout, stderr: "" });
        return;
      }
      cb(null, { stdout: "", stderr: "" });
    },
  };
});

vi.mock("../../src/utils/nightgaugeConfig", () => ({
  getConcurrentPipelineConfig: vi.fn().mockReturnValue({
    maxConcurrent: 3,
  }),
}));

vi.mock("../../src/services/IpcClient", () => ({
  IpcClient: {
    getInstance: () => ({
      gitComposeBranchName: vi.fn(async (issueNumber: number) => ({
        name: `feat/${issueNumber}-sub-issue`,
      })),
    }),
  },
}));

import { ConcurrentPipelineManager } from "../../src/services/ConcurrentPipelineManager";
import { fakeCloneLayout } from "../helpers/cloneLayout";
import * as path from "node:path";

beforeEach(() => {
  createOptions.clear();
  lsRemote.length = 0;
  fakeCloneLayout("/workspace/repo-a");
  fakeCloneLayout("/workspace/repo-b");
  for (const [root, n] of [
    ["/workspace/repo-a", 22],
    ["/workspace/repo-a", 23],
    ["/workspace/repo-b", 21],
  ] as const) {
    fakeCloneLayout(`${root}/.worktrees/issue-${n}`, path.join(root, ".git"));
  }
});

function makeItem(issueNumber: number, repoName: string, epicNumber: number, epicRepo?: string) {
  return {
    issueNumber,
    title: `Issue #${issueNumber}`,
    position: 1,
    status: "pending",
    addedAt: new Date().toISOString(),
    repoName,
    epicNumber,
    ...(epicRepo ? { epicRepo } : {}),
  };
}

function createPendingFactory() {
  return vi.fn().mockImplementation(() => ({
    orchestrator: {
      setRepoOverride: vi.fn(),
      setRunRepoRoot: vi.fn(),
      setUnattended: vi.fn(),
      resolveRunRepoSlug: vi.fn().mockResolvedValue("org/repo-a"),
      runPipeline: vi.fn().mockReturnValue(new Promise(() => {})),
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
      initializePipeline: vi.fn().mockResolvedValue(undefined),
      setMeta: vi.fn(),
      dispose: vi.fn(),
    },
  }));
}

describe("ConcurrentPipelineManager — epic base branch of a slot (#2377)", () => {
  it("bases a sub-issue on the epic branch only in the epic's own repository", async () => {
    const workspaceManager = {
      findRepositoryByGitHub: vi.fn(
        (slug: string) =>
          ({
            "org/repo-a": { path: "/workspace/repo-a" },
            "org/repo-b": { path: "/workspace/repo-b" },
          })[slug]
      ),
    };
    const queue = {
      dequeueIndependent: vi
        .fn()
        .mockResolvedValueOnce([
          // A sub-issue of org/repo-a#20 that lives in org/repo-b.
          makeItem(21, "org/repo-b", 20, "org/repo-a"),
          // A sub-issue of org/repo-a#20 in the epic's own repository, with
          // the repository spelled in another case.
          makeItem(22, "org/repo-a", 20, "Org/Repo-A"),
          // No epic repository recorded: the item's own, as before.
          makeItem(23, "org/repo-a", 20),
        ])
        .mockResolvedValue([]),
      updateActiveSlots: vi.fn().mockResolvedValue(undefined),
      enqueue: vi.fn().mockResolvedValue({ issueNumber: 0, position: 0 }),
      // Read for the epic progress telemetry a sub-issue's slot reports.
      getQueue: vi.fn().mockResolvedValue(null),
    };
    const logger = {
      info: vi.fn(),
      warn: vi.fn(),
      error: vi.fn(),
      debug: vi.fn(),
      getChannel: vi.fn(),
    };

    const manager = new ConcurrentPipelineManager(
      "/workspace/repo-a",
      queue as any,
      createPendingFactory(),
      logger as any,
      { maxConcurrent: 3 },
      workspaceManager as any
    );

    expect(await manager.fillSlots()).toBe(3);

    // repo-b's epic/20-* is repo-b's own #20's branch: never looked up, never
    // used as the base.
    expect(lsRemote.filter((c) => c.cwd === "/workspace/repo-b")).toEqual([]);
    expect(createOptions.get(21)?.baseBranch).toBeUndefined();

    expect(createOptions.get(22)?.baseBranch).toBe("epic/20-repo-a-epic");
    expect(createOptions.get(23)?.baseBranch).toBe("epic/20-repo-a-epic");
  });
});
