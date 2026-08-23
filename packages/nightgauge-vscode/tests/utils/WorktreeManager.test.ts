/**
 * Tests for WorktreeManager
 *
 * Verifies git worktree lifecycle management:
 * - Worktree path generation
 * - Create / cleanup operations (mocked git commands)
 * - List active worktrees parsing
 * - Orphan cleanup detection
 * - .gitignore management
 *
 * @see Issue #1621 - Git worktree-based concurrent pipeline execution
 */

import { describe, it, expect, vi, beforeEach } from "vitest";

// Use vi.hoisted() to avoid hoisting issues with vi.mock
const { execAsyncMock, execFileAsyncMock, fsMock } = vi.hoisted(() => ({
  execAsyncMock: vi.fn(),
  execFileAsyncMock: vi.fn(),
  fsMock: {
    mkdir: vi.fn().mockResolvedValue(undefined),
    access: vi.fn().mockResolvedValue(undefined),
    rm: vi.fn().mockResolvedValue(undefined),
    readFile: vi.fn().mockResolvedValue(""),
    writeFile: vi.fn().mockResolvedValue(undefined),
    readdir: vi.fn().mockResolvedValue([]),
    cp: vi.fn().mockResolvedValue(undefined),
  },
}));

const execFileSyncMock = vi.hoisted(() => vi.fn().mockReturnValue(Buffer.from("")));

// Mock node:child_process — provides exec/execFile (with promisify.custom)
// plus the legacy execFileSync export still consumed by test fixtures.
// #2884: WorktreeManager now uses promisify(execFile) for git operations.
vi.mock("node:child_process", () => {
  const kCustom = Symbol.for("nodejs.util.promisify.custom");

  const execMock = vi.fn();
  (execMock as any)[kCustom] = execAsyncMock;

  const execFileMock = vi.fn();
  (execFileMock as any)[kCustom] = execFileAsyncMock;

  return { exec: execMock, execFile: execFileMock, execFileSync: execFileSyncMock };
});

// Mock node:fs/promises
vi.mock("node:fs/promises", () => fsMock);

import { WorktreeManager } from "../../src/utils/WorktreeManager";

describe("WorktreeManager", () => {
  let manager: WorktreeManager;
  const repoRoot = "/repo";

  beforeEach(() => {
    vi.clearAllMocks();
    manager = new WorktreeManager(repoRoot, ".worktrees");
    execAsyncMock.mockResolvedValue({ stdout: "", stderr: "" });
    execFileAsyncMock.mockResolvedValue({ stdout: "", stderr: "" });
    execFileSyncMock.mockReturnValue(Buffer.from(""));
  });

  describe("getWorktreePath", () => {
    it("returns correct path for issue number", () => {
      expect(manager.getWorktreePath(42)).toBe("/repo/.worktrees/issue-42");
    });

    it("works with custom worktree base", () => {
      const custom = new WorktreeManager(repoRoot, "custom-trees");
      expect(custom.getWorktreePath(100)).toBe("/repo/custom-trees/issue-100");
    });
  });

  describe("create", () => {
    it("creates worktree with correct git command", async () => {
      const result = await manager.create(42, "feat/42-dark-mode");

      // Should fetch via execAsync
      expect(execAsyncMock).toHaveBeenCalledWith(
        "git fetch origin",
        expect.objectContaining({ cwd: repoRoot })
      );
      // Worktree add uses execFile with array args (no shell). #2884 made
      // this async, so the mock is execFileAsyncMock now.
      expect(execFileAsyncMock).toHaveBeenCalledWith(
        "git",
        ["worktree", "add", "/repo/.worktrees/issue-42", "-b", "feat/42-dark-mode", "origin/main"],
        expect.objectContaining({ cwd: repoRoot })
      );
      // npm install via execAsync
      expect(execAsyncMock).toHaveBeenCalledWith(
        "npm install --prefer-offline",
        expect.objectContaining({ cwd: "/repo/.worktrees/issue-42" })
      );

      expect(result.path).toBe("/repo/.worktrees/issue-42");
      expect(result.branch).toBe("feat/42-dark-mode");
      expect(result.issueNumber).toBe(42);
      expect(result.exists).toBe(true);
    });

    // #332. `.worktrees/issue-696` ended up holding `main`, which makes
    // `git checkout main` fail in the operator's own primary clone and which
    // `worktree sweep` protected as `protected-branch` forever, so it could
    // never self-heal. The stale-cleanup path below also runs an unconditional
    // `git branch -D <branchName>` — with branchName === "main" that deletes
    // the trunk.
    it.each(["main", "master"])(
      "refuses to create a pipeline worktree on the default branch (%s)",
      async (branch) => {
        await expect(manager.create(696, branch)).rejects.toThrow(/default branch/i);

        // The side effect is the point: an error-only assertion would pass
        // against a guard placed AFTER the destructive cleanup.
        expect(
          execFileAsyncMock.mock.calls.some(
            ([, args]: [string, string[]]) =>
              Array.isArray(args) && args[0] === "branch" && args.includes(branch)
          )
        ).toBe(false);
        expect(
          execFileAsyncMock.mock.calls.some(
            ([, args]: [string, string[]]) => Array.isArray(args) && args[0] === "worktree"
          )
        ).toBe(false);
      }
    );

    it("refuses a branch equal to a non-default baseBranch", async () => {
      await expect(manager.create(696, "develop", { baseBranch: "develop" })).rejects.toThrow(
        /default branch/i
      );
    });

    it("uses execFile array args for branch deletion (no shell interpolation)", async () => {
      await manager.create(42, "feat/42-dark-mode", { npmInstall: false });

      expect(execFileAsyncMock).toHaveBeenCalledWith(
        "git",
        ["branch", "-D", "feat/42-dark-mode"],
        expect.objectContaining({ cwd: repoRoot })
      );
    });

    it("uses execFile array args for remote branch deletion", async () => {
      await manager.create(42, "feat/42-test", {
        npmInstall: false,
        deleteRemoteBranch: true,
      });

      expect(execFileAsyncMock).toHaveBeenCalledWith(
        "git",
        ["push", "origin", "--delete", "feat/42-test"],
        expect.objectContaining({ cwd: repoRoot })
      );
    });

    it("rejects branch names with shell metacharacters", async () => {
      await expect(manager.create(42, "feat/$(whoami)")).rejects.toThrow(/invalid branch name/i);
    });

    it("rejects branch names with backticks", async () => {
      await expect(manager.create(42, "feat/`id`")).rejects.toThrow(/invalid branch name/i);
    });

    it("rejects branch names with semicolons", async () => {
      await expect(manager.create(42, "feat/;rm -rf /")).rejects.toThrow(/invalid branch name/i);
    });

    it("rejects baseBranch with shell metacharacters", async () => {
      await expect(
        manager.create(42, "feat/42-test", { baseBranch: "main;curl evil.com" })
      ).rejects.toThrow(/invalid branch name/i);
    });

    it("allows normal branch names without throwing", async () => {
      await expect(
        manager.create(42, "feat/42-test", { npmInstall: false })
      ).resolves.toBeDefined();
    });

    it("skips npm install when npmInstall is false", async () => {
      await manager.create(42, "feat/42-test", { npmInstall: false });

      const npmCalls = execAsyncMock.mock.calls.filter(
        ([cmd]: unknown[]) => typeof cmd === "string" && cmd.includes("npm install")
      );
      expect(npmCalls).toHaveLength(0);
    });

    it("continues worktree creation when npm install fails (non-fatal)", async () => {
      const warnSpy = vi.spyOn(console, "warn").mockImplementation(() => {});
      execAsyncMock.mockImplementation((cmd: string) => {
        if (cmd.includes("npm install")) {
          return Promise.reject(new Error("npm ERR! EBADENGINE"));
        }
        return Promise.resolve({ stdout: "", stderr: "" });
      });

      const result = await manager.create(42, "feat/42-test");

      expect(result.path).toBe("/repo/.worktrees/issue-42");
      expect(warnSpy).toHaveBeenCalledWith(expect.stringContaining("npm install failed"));
      warnSpy.mockRestore();
    });

    it("ensures .gitignore includes worktree base", async () => {
      fsMock.readFile.mockResolvedValue("node_modules\n");

      await manager.create(42, "feat/42-test", { npmInstall: false });

      expect(fsMock.writeFile).toHaveBeenCalledWith(
        "/repo/.gitignore",
        expect.stringContaining(".worktrees"),
        "utf-8"
      );
    });

    it("does not duplicate .gitignore entry", async () => {
      fsMock.readFile.mockImplementation((p: string) => {
        if (String(p).endsWith(".gitignore")) {
          return Promise.resolve("node_modules\n.worktrees\n");
        }
        // No config.local.yaml in this repo.
        return Promise.reject(Object.assign(new Error("ENOENT"), { code: "ENOENT" }));
      });

      await manager.create(42, "feat/42-test", { npmInstall: false });

      // writeFile should not be called since .worktrees is already in .gitignore
      expect(fsMock.writeFile).not.toHaveBeenCalled();
    });

    it("copies config.local.yaml into the worktree when present (tier parity)", async () => {
      const localBody = "pipeline:\n  architecture_approval:\n    enabled: false\n";
      fsMock.readFile.mockImplementation((p: string) => {
        if (String(p) === "/repo/.nightgauge/config.local.yaml") {
          return Promise.resolve(localBody);
        }
        if (String(p).endsWith(".gitignore")) {
          return Promise.resolve(".worktrees\n");
        }
        return Promise.reject(Object.assign(new Error("ENOENT"), { code: "ENOENT" }));
      });

      await manager.create(42, "feat/42-test", { npmInstall: false });

      // The gitignored local tier must reach the worktree — the Go gates run
      // with --workdir <worktree> and merge project+local from there.
      expect(fsMock.writeFile).toHaveBeenCalledWith(
        "/repo/.worktrees/issue-42/.nightgauge/config.local.yaml",
        localBody,
        "utf-8"
      );
    });

    it("skips the local-config copy when the repo has none", async () => {
      fsMock.readFile.mockImplementation((p: string) => {
        if (String(p).endsWith(".gitignore")) {
          return Promise.resolve(".worktrees\n");
        }
        return Promise.reject(Object.assign(new Error("ENOENT"), { code: "ENOENT" }));
      });

      await manager.create(42, "feat/42-test", { npmInstall: false });

      const localWrites = fsMock.writeFile.mock.calls.filter(([p]: [string]) =>
        String(p).endsWith("config.local.yaml")
      );
      expect(localWrites).toHaveLength(0);
    });
  });

  describe("create — re-dispatch reuse (#135)", () => {
    function mockUniqueCommits(count: number, branchExists = true) {
      execFileAsyncMock.mockImplementation((cmd: string, args: string[]) => {
        if (args[0] === "rev-parse" && args[1] === "--verify") {
          return branchExists
            ? Promise.resolve({ stdout: "abc123\n", stderr: "" })
            : Promise.reject(new Error("unknown revision"));
        }
        if (args[0] === "rev-list" && args[1] === "--count") {
          return Promise.resolve({ stdout: `${count}\n`, stderr: "" });
        }
        return Promise.resolve({ stdout: "", stderr: "" });
      });
    }

    it("reuses the worktree in place when it is already registered to the branch", async () => {
      mockUniqueCommits(3);
      execAsyncMock.mockImplementation((cmd: string) => {
        if (cmd === "git worktree list --porcelain") {
          return Promise.resolve({
            stdout: [
              "worktree /repo/.worktrees/issue-42",
              "HEAD def456",
              "branch refs/heads/feat/42-test",
              "",
            ].join("\n"),
            stderr: "",
          });
        }
        return Promise.resolve({ stdout: "", stderr: "" });
      });

      const result = await manager.create(42, "feat/42-test", { npmInstall: false });

      expect(result).toEqual({
        path: "/repo/.worktrees/issue-42",
        branch: "feat/42-test",
        issueNumber: 42,
        exists: true,
      });
      expect(execFileAsyncMock).not.toHaveBeenCalledWith(
        "git",
        ["branch", "-D", "feat/42-test"],
        expect.anything()
      );
      expect(
        execAsyncMock.mock.calls.some(
          ([cmd]: unknown[]) => typeof cmd === "string" && cmd.includes("worktree remove")
        )
      ).toBe(false);
      expect(execFileAsyncMock).not.toHaveBeenCalledWith(
        "git",
        expect.arrayContaining(["-b"]),
        expect.anything()
      );
    });

    it("resumes from the branch tip when the branch has unique commits but no registered worktree", async () => {
      mockUniqueCommits(2);
      execAsyncMock.mockResolvedValue({ stdout: "", stderr: "" }); // listActive: no worktrees registered

      await manager.create(42, "feat/42-test", { npmInstall: false });

      expect(execFileAsyncMock).toHaveBeenCalledWith(
        "git",
        ["worktree", "add", "/repo/.worktrees/issue-42", "feat/42-test"],
        expect.objectContaining({ cwd: repoRoot })
      );
      expect(execFileAsyncMock).not.toHaveBeenCalledWith(
        "git",
        ["branch", "-D", "feat/42-test"],
        expect.anything()
      );
    });

    it("reuses a registered worktree carrying UNCOMMITTED work even with zero unique commits (#283)", async () => {
      // feature-dev leaves its implementation uncommitted by contract
      // (feature-validate commits), so after a failure-revert the preserved
      // worktree is dirty with zero commits ahead — the commits-only
      // predicate routed exactly this shape into the destructive rebuild,
      // deleting a completed implementation on re-dispatch.
      execFileAsyncMock.mockImplementation((cmd: string, args: string[]) => {
        if (args[0] === "-C" && args[2] === "status" && args[3] === "--porcelain") {
          return Promise.resolve({ stdout: " M src/impl.ts\n?? tests/impl.test.ts\n", stderr: "" });
        }
        if (args[0] === "rev-parse" && args[1] === "--verify") {
          return Promise.resolve({ stdout: "abc123\n", stderr: "" });
        }
        if (args[0] === "rev-list" && args[1] === "--count") {
          return Promise.resolve({ stdout: "0\n", stderr: "" });
        }
        return Promise.resolve({ stdout: "", stderr: "" });
      });
      execAsyncMock.mockImplementation((cmd: string) => {
        if (cmd === "git worktree list --porcelain") {
          return Promise.resolve({
            stdout: [
              "worktree /repo/.worktrees/issue-42",
              "HEAD def456",
              "branch refs/heads/feat/42-test",
              "",
            ].join("\n"),
            stderr: "",
          });
        }
        return Promise.resolve({ stdout: "", stderr: "" });
      });

      const result = await manager.create(42, "feat/42-test", { npmInstall: false });

      expect(result).toEqual({
        path: "/repo/.worktrees/issue-42",
        branch: "feat/42-test",
        issueNumber: 42,
        exists: true,
      });
      expect(execFileAsyncMock).not.toHaveBeenCalledWith(
        "git",
        ["branch", "-D", "feat/42-test"],
        expect.anything()
      );
      expect(
        execAsyncMock.mock.calls.some(
          ([cmd]: unknown[]) => typeof cmd === "string" && cmd.includes("worktree remove")
        )
      ).toBe(false);
    });

    it("still rebuilds a registered but CLEAN worktree with zero unique commits (#283 negative)", async () => {
      execFileAsyncMock.mockImplementation((cmd: string, args: string[]) => {
        if (args[0] === "-C" && args[2] === "status" && args[3] === "--porcelain") {
          return Promise.resolve({ stdout: "", stderr: "" }); // clean
        }
        if (args[0] === "rev-parse" && args[1] === "--verify") {
          return Promise.resolve({ stdout: "abc123\n", stderr: "" });
        }
        if (args[0] === "rev-list" && args[1] === "--count") {
          return Promise.resolve({ stdout: "0\n", stderr: "" });
        }
        return Promise.resolve({ stdout: "", stderr: "" });
      });
      execAsyncMock.mockImplementation((cmd: string) => {
        if (cmd === "git worktree list --porcelain") {
          return Promise.resolve({
            stdout: [
              "worktree /repo/.worktrees/issue-42",
              "HEAD def456",
              "branch refs/heads/feat/42-test",
              "",
            ].join("\n"),
            stderr: "",
          });
        }
        return Promise.resolve({ stdout: "", stderr: "" });
      });

      await manager.create(42, "feat/42-test", { npmInstall: false });

      expect(execFileAsyncMock).toHaveBeenCalledWith(
        "git",
        ["worktree", "add", "/repo/.worktrees/issue-42", "-b", "feat/42-test", "origin/main"],
        expect.anything()
      );
    });

    it("follows the destructive stale-worktree path when the branch has zero unique commits", async () => {
      mockUniqueCommits(0);

      await manager.create(42, "feat/42-test", { npmInstall: false });

      expect(execFileAsyncMock).toHaveBeenCalledWith(
        "git",
        ["branch", "-D", "feat/42-test"],
        expect.objectContaining({ cwd: repoRoot })
      );
      expect(execFileAsyncMock).toHaveBeenCalledWith(
        "git",
        ["worktree", "add", "/repo/.worktrees/issue-42", "-b", "feat/42-test", "origin/main"],
        expect.objectContaining({ cwd: repoRoot })
      );
    });

    it("follows the destructive path unchanged when the branch does not exist at all", async () => {
      mockUniqueCommits(0, /* branchExists */ false);

      await manager.create(42, "feat/42-test", { npmInstall: false });

      expect(execFileAsyncMock).toHaveBeenCalledWith(
        "git",
        ["worktree", "add", "/repo/.worktrees/issue-42", "-b", "feat/42-test", "origin/main"],
        expect.objectContaining({ cwd: repoRoot })
      );
    });

    it("conflict-restart (deleteRemoteBranch) always takes the destructive path, even with unique commits", async () => {
      mockUniqueCommits(5);

      await manager.create(42, "feat/42-test", {
        npmInstall: false,
        deleteRemoteBranch: true,
      });

      expect(execFileAsyncMock).toHaveBeenCalledWith(
        "git",
        ["push", "origin", "--delete", "feat/42-test"],
        expect.objectContaining({ cwd: repoRoot })
      );
      expect(execFileAsyncMock).toHaveBeenCalledWith(
        "git",
        ["branch", "-D", "feat/42-test"],
        expect.objectContaining({ cwd: repoRoot })
      );
      expect(execFileAsyncMock).toHaveBeenCalledWith(
        "git",
        ["worktree", "add", "/repo/.worktrees/issue-42", "-b", "feat/42-test", "origin/main"],
        expect.objectContaining({ cwd: repoRoot })
      );
    });
  });

  describe("cleanup", () => {
    it("removes worktree with force flag", async () => {
      await manager.cleanup(42);

      expect(execAsyncMock).toHaveBeenCalledWith(
        'git worktree remove "/repo/.worktrees/issue-42" --force',
        expect.objectContaining({ cwd: repoRoot })
      );
    });

    it("falls back to manual removal if git worktree remove fails", async () => {
      execAsyncMock.mockImplementation((cmd: string) => {
        if (cmd.includes("worktree remove")) {
          return Promise.reject(new Error("not a valid worktree"));
        }
        return Promise.resolve({ stdout: "", stderr: "" });
      });

      await manager.cleanup(42);

      expect(fsMock.rm).toHaveBeenCalledWith(
        "/repo/.worktrees/issue-42",
        expect.objectContaining({ recursive: true, force: true })
      );
      expect(execAsyncMock).toHaveBeenCalledWith(
        "git worktree prune",
        expect.objectContaining({ cwd: repoRoot })
      );
    });

    // Issue #110 — a swallowed removal failure is how leaked worktrees stayed
    // invisible until someone counted them days later. The fallback may still
    // succeed, but the failure must always be observable.
    it("warns when git worktree remove fails (#110)", async () => {
      const warn = vi.spyOn(console, "warn").mockImplementation(() => {});
      execAsyncMock.mockImplementation((cmd: string) => {
        if (cmd.includes("worktree remove")) {
          return Promise.reject(new Error("not a valid worktree"));
        }
        return Promise.resolve({ stdout: "", stderr: "" });
      });

      await manager.cleanup(42);

      expect(warn).toHaveBeenCalledWith(
        expect.stringContaining("git worktree remove failed for issue #42")
      );
      warn.mockRestore();
    });

    it("warns that the worktree LEAKED when manual removal also fails (#110)", async () => {
      const warn = vi.spyOn(console, "warn").mockImplementation(() => {});
      execAsyncMock.mockImplementation((cmd: string) => {
        if (cmd.includes("worktree remove")) {
          return Promise.reject(new Error("not a valid worktree"));
        }
        return Promise.resolve({ stdout: "", stderr: "" });
      });
      fsMock.rm.mockRejectedValueOnce(new Error("EPERM"));

      await manager.cleanup(42);

      expect(warn).toHaveBeenCalledWith(expect.stringContaining("worktree LEAKED"));
      warn.mockRestore();
    });

    it("deletes branch when deleteBranch is true and content-diff confirms merged", async () => {
      execFileAsyncMock.mockImplementation((_cmd: string, args: string[]) => {
        if (args.includes("--show-current")) {
          return Promise.resolve({ stdout: "feat/42-test\n", stderr: "" });
        }
        return Promise.resolve({ stdout: "", stderr: "" });
      });

      await manager.cleanup(42, true);

      expect(execFileAsyncMock).toHaveBeenCalledWith(
        "git",
        ["branch", "-D", "feat/42-test"],
        expect.objectContaining({ cwd: repoRoot })
      );
    });

    // #106 — a worktree with real work in progress must never be
    // force-removed, regardless of terminal outcome.
    it("preserves the worktree and skips removal when it has uncommitted changes", async () => {
      execAsyncMock.mockImplementation((cmd: string) => {
        if (cmd.includes("git status --porcelain")) {
          return Promise.resolve({ stdout: " M some-file.ts\n", stderr: "" });
        }
        return Promise.resolve({ stdout: "", stderr: "" });
      });
      const warn = vi.spyOn(console, "warn").mockImplementation(() => {});

      await manager.cleanup(42);

      expect(warn).toHaveBeenCalledWith(
        expect.stringContaining("preserving worktree for issue #42")
      );
      expect(execAsyncMock).not.toHaveBeenCalledWith(
        expect.stringContaining("worktree remove"),
        expect.anything()
      );
      warn.mockRestore();
    });

    // #106 — an unmerged branch must never be deleted, even when
    // deleteBranch is requested.
    it("does not delete the branch when the content-diff check reports unmerged content", async () => {
      execFileAsyncMock.mockImplementation((_cmd: string, args: string[]) => {
        if (args.includes("--show-current")) {
          return Promise.resolve({ stdout: "feat/42-test\n", stderr: "" });
        }
        if (args[0] === "rev-list" && args[1] === "--count") {
          return Promise.resolve({ stdout: "3\n", stderr: "" });
        }
        if (args[0] === "merge-base" && args[1] === "--is-ancestor") {
          return Promise.reject(Object.assign(new Error("exit 1"), { code: 1 }));
        }
        if (args[0] === "diff" && args[1] === "--name-only") {
          return Promise.resolve({ stdout: "src/impl.ts\0", stderr: "" });
        }
        if (args[0] === "diff" && args[1] === "--stat") {
          return Promise.resolve({ stdout: "1 file changed\n", stderr: "" });
        }
        return Promise.resolve({ stdout: "", stderr: "" });
      });
      const warn = vi.spyOn(console, "warn").mockImplementation(() => {});

      await manager.cleanup(42, true);

      expect(warn).toHaveBeenCalledWith(
        expect.stringContaining("not deleting branch feat/42-test")
      );
      expect(execFileAsyncMock).not.toHaveBeenCalledWith(
        "git",
        ["branch", "-D", "feat/42-test"],
        expect.anything()
      );
      warn.mockRestore();
    });

    it("treats path-restricted empty stat as merged and never uses two-dot diff --stat", async () => {
      execFileAsyncMock.mockImplementation((_cmd: string, args: string[]) => {
        if (args.includes("--show-current")) {
          return Promise.resolve({ stdout: "feat/42-test\n", stderr: "" });
        }
        if (args[0] === "rev-list" && args[1] === "--count") {
          return Promise.resolve({ stdout: "2\n", stderr: "" });
        }
        if (args[0] === "merge-base" && args[1] === "--is-ancestor") {
          return Promise.reject(Object.assign(new Error("exit 1"), { code: 1 }));
        }
        if (args[0] === "diff" && args[1] === "--name-only") {
          return Promise.resolve({ stdout: "feature.txt\0", stderr: "" });
        }
        if (args[0] === "diff" && args[1] === "--stat") {
          return Promise.resolve({ stdout: "", stderr: "" });
        }
        return Promise.resolve({ stdout: "", stderr: "" });
      });

      await manager.cleanup(42, true);

      expect(execFileAsyncMock).toHaveBeenCalledWith(
        "git",
        ["diff", "--stat", "origin/main", "feat/42-test", "--", "feature.txt"],
        expect.objectContaining({ cwd: repoRoot })
      );
      expect(
        execFileAsyncMock.mock.calls.some(
          ([, args]: [string, string[]]) =>
            Array.isArray(args) &&
            args[0] === "diff" &&
            args[1] === "--stat" &&
            typeof args[2] === "string" &&
            args[2].includes("..")
        )
      ).toBe(false);
      expect(execFileAsyncMock).toHaveBeenCalledWith(
        "git",
        ["branch", "-D", "feat/42-test"],
        expect.objectContaining({ cwd: repoRoot })
      );
    });

    it("does not treat an empty file list as merged when the tip is not an ancestor", async () => {
      execFileAsyncMock.mockImplementation((_cmd: string, args: string[]) => {
        if (args.includes("--show-current")) {
          return Promise.resolve({ stdout: "feat/42-test\n", stderr: "" });
        }
        if (args[0] === "rev-list" && args[1] === "--count") {
          return Promise.resolve({ stdout: "1\n", stderr: "" });
        }
        if (args[0] === "merge-base" && args[1] === "--is-ancestor") {
          return Promise.reject(Object.assign(new Error("exit 1"), { code: 1 }));
        }
        if (args[0] === "diff" && args[1] === "--name-only") {
          return Promise.resolve({ stdout: "", stderr: "" });
        }
        return Promise.resolve({ stdout: "", stderr: "" });
      });
      const warn = vi.spyOn(console, "warn").mockImplementation(() => {});

      await manager.cleanup(42, true);

      expect(warn).toHaveBeenCalledWith(
        expect.stringContaining("not deleting branch feat/42-test")
      );
      expect(execFileAsyncMock).not.toHaveBeenCalledWith(
        "git",
        ["branch", "-D", "feat/42-test"],
        expect.anything()
      );
      warn.mockRestore();
    });

    // Issue #3050 — worktree teardown must remove the per-issue docker
    // compose stack before deleting the worktree directory. The order
    // matters because compose project metadata is stored on the docker
    // daemon, not in the worktree, but tearing the stack down after the
    // worktree is gone leaves containers/volumes/networks orphaned with no
    // worktree to look up the project name from.
    it("tears down docker compose stack before removing worktree (#3050)", async () => {
      const callOrder: string[] = [];
      execFileAsyncMock.mockImplementation((cmd: string, args: string[]) => {
        if (cmd === "docker" && args[0] === "version") {
          callOrder.push("docker:version");
          return Promise.resolve({ stdout: "24.0.0\n", stderr: "" });
        }
        if (cmd === "docker" && args[0] === "compose" && args.includes("down")) {
          callOrder.push(`docker:compose:down:${args[2]}`);
          return Promise.resolve({ stdout: "", stderr: "" });
        }
        if (cmd === "docker" && args[0] === "images") {
          callOrder.push("docker:images");
          return Promise.resolve({
            stdout: "issue-42-api\nissue-42-worker\nunrelated\n",
            stderr: "",
          });
        }
        if (cmd === "docker" && args[0] === "rmi") {
          callOrder.push(`docker:rmi:${args[2]}`);
          return Promise.resolve({ stdout: "", stderr: "" });
        }
        return Promise.resolve({ stdout: "", stderr: "" });
      });
      execAsyncMock.mockImplementation((cmd: string) => {
        if (cmd.includes("worktree remove")) {
          callOrder.push("git:worktree:remove");
        }
        return Promise.resolve({ stdout: "", stderr: "" });
      });

      await manager.cleanup(42);

      expect(callOrder).toEqual([
        "docker:version",
        "docker:compose:down:issue-42",
        "docker:images",
        "docker:rmi:issue-42-api",
        "docker:rmi:issue-42-worker",
        "git:worktree:remove",
      ]);
    });

    it("continues with worktree removal when docker is unavailable (#3050)", async () => {
      execFileAsyncMock.mockImplementation((cmd: string, args: string[]) => {
        if (cmd === "docker" && args[0] === "version") {
          return Promise.reject(new Error("docker: command not found"));
        }
        return Promise.resolve({ stdout: "", stderr: "" });
      });

      await manager.cleanup(42);

      expect(execAsyncMock).toHaveBeenCalledWith(
        'git worktree remove "/repo/.worktrees/issue-42" --force',
        expect.objectContaining({ cwd: repoRoot })
      );
    });

    it("continues with worktree removal when compose teardown fails (#3050)", async () => {
      const warnSpy = vi.spyOn(console, "warn").mockImplementation(() => {});
      execFileAsyncMock.mockImplementation((cmd: string, args: string[]) => {
        if (cmd === "docker" && args[0] === "version") {
          return Promise.resolve({ stdout: "24.0.0\n", stderr: "" });
        }
        if (cmd === "docker" && args[0] === "compose" && args.includes("down")) {
          return Promise.reject(new Error("Cannot connect to the Docker daemon"));
        }
        if (cmd === "docker" && args[0] === "images") {
          return Promise.resolve({ stdout: "", stderr: "" });
        }
        return Promise.resolve({ stdout: "", stderr: "" });
      });

      await manager.cleanup(42);

      expect(execAsyncMock).toHaveBeenCalledWith(
        'git worktree remove "/repo/.worktrees/issue-42" --force',
        expect.objectContaining({ cwd: repoRoot })
      );
      expect(warnSpy).toHaveBeenCalledWith(expect.stringContaining("docker compose teardown"));
      warnSpy.mockRestore();
    });

    it("filters images strictly to issue-NNN- prefix (#3050)", async () => {
      const rmiCalls: string[] = [];
      execFileAsyncMock.mockImplementation((cmd: string, args: string[]) => {
        if (cmd === "docker" && args[0] === "version") {
          return Promise.resolve({ stdout: "24.0.0\n", stderr: "" });
        }
        if (cmd === "docker" && args[0] === "compose") {
          return Promise.resolve({ stdout: "", stderr: "" });
        }
        if (cmd === "docker" && args[0] === "images") {
          return Promise.resolve({
            stdout: "issue-7-api\nissue-7-worker\nissue-77-api\nunrelated\n",
            stderr: "",
          });
        }
        if (cmd === "docker" && args[0] === "rmi") {
          rmiCalls.push(args[2]);
          return Promise.resolve({ stdout: "", stderr: "" });
        }
        return Promise.resolve({ stdout: "", stderr: "" });
      });

      await manager.cleanup(7);

      // Must NOT remove issue-77-api (different issue) or unrelated images.
      expect(rmiCalls).toEqual(["issue-7-api", "issue-7-worker"]);
    });
  });

  describe("listActive", () => {
    it("parses git worktree list porcelain output", async () => {
      execAsyncMock.mockResolvedValue({
        stdout: [
          "worktree /repo",
          "HEAD abc123",
          "branch refs/heads/main",
          "",
          "worktree /repo/.worktrees/issue-42",
          "HEAD def456",
          "branch refs/heads/feat/42-dark-mode",
          "",
          "worktree /repo/.worktrees/issue-99",
          "HEAD ghi789",
          "branch refs/heads/feat/99-fix-bug",
          "",
        ].join("\n"),
        stderr: "",
      });

      const active = await manager.listActive();

      expect(active).toHaveLength(2);
      expect(active[0].issueNumber).toBe(42);
      expect(active[0].branch).toBe("feat/42-dark-mode");
      expect(active[0].path).toBe("/repo/.worktrees/issue-42");
      expect(active[1].issueNumber).toBe(99);
      expect(active[1].branch).toBe("feat/99-fix-bug");
    });

    it("filters out worktrees not in managed directory", async () => {
      execAsyncMock.mockResolvedValue({
        stdout: [
          "worktree /repo",
          "HEAD abc123",
          "branch refs/heads/main",
          "",
          "worktree /other/path/issue-42",
          "HEAD def456",
          "branch refs/heads/feat/42-test",
          "",
        ].join("\n"),
        stderr: "",
      });

      const active = await manager.listActive();
      expect(active).toHaveLength(0);
    });

    it("returns empty array on error", async () => {
      execAsyncMock.mockRejectedValue(new Error("git not found"));
      const active = await manager.listActive();
      expect(active).toEqual([]);
    });
  });

  describe("exists", () => {
    it("returns true when worktree directory exists", async () => {
      fsMock.access.mockResolvedValue(undefined);
      expect(await manager.exists(42)).toBe(true);
    });

    it("returns false when worktree directory does not exist", async () => {
      fsMock.access.mockRejectedValue(new Error("ENOENT"));
      expect(await manager.exists(42)).toBe(false);
    });
  });

  describe("cleanupOrphans", () => {
    it("removes directories not tracked by git worktree", async () => {
      execAsyncMock.mockResolvedValue({ stdout: "", stderr: "" });

      fsMock.readdir.mockResolvedValue([
        { name: "issue-42", isDirectory: () => true },
        { name: "issue-99", isDirectory: () => true },
      ]);

      // listActive returns empty (no tracked worktrees)
      const cleaned = await manager.cleanupOrphans();

      expect(fsMock.rm).toHaveBeenCalledTimes(2);
      expect(cleaned).toBe(2);
    });
  });

  describe("packaged SDK CLI", () => {
    beforeEach(() => {
      // Restore fsMock.access so hasPackageJson resolves true
      fsMock.access.mockResolvedValue(undefined);
    });

    it("does not build Nightgauge source artifacts inside a consumer worktree", async () => {
      await manager.create(42, "feat/42-consumer-test");
      expect(fsMock.cp).not.toHaveBeenCalled();
      expect(
        execAsyncMock.mock.calls.some(
          ([cmd]: unknown[]) => typeof cmd === "string" && cmd.includes("@nightgauge/sdk")
        )
      ).toBe(false);
    });
  });
});
