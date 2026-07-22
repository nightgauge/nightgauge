/**
 * HeadlessOrchestrator.validateCommitContract.test.ts
 *
 * Deterministic backstop for the Issue #1608 commit contract (production
 * autonomous-run post-mortem): feature-validate owns commit-and-push of validated code,
 * but that phase is an LLM step and was observed skipped — the branch reached
 * pr-create with zero commits ahead of base and the validated implementation
 * was destroyed with the worktree.
 *
 * enforceValidateCommitContract(issueNumber):
 *   - dev context claims no files            → null (pass through)
 *   - branch already ahead of origin/<base>  → null (contract satisfied)
 *   - 0 ahead + source changes on disk       → remediate: commit tree, push
 *   - 0 ahead + clean tree                   → Error (work lost — fail here)
 *
 * Uses REAL git repos in temp dirs (bare origin + clone) so the rev-list /
 * status / commit / push behavior is the genuine article, not a mock.
 */
import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import { execFileSync } from "child_process";
import * as fs from "fs";
import * as os from "os";
import * as path from "path";
import { HeadlessOrchestrator } from "../../src/services/HeadlessOrchestrator";
import type { Logger } from "../../src/utils/logger";

// Mock skillRunner so importing the orchestrator doesn't pull the real CLI.
vi.mock("../../src/utils/skillRunner", () => ({
  hasActiveProcess: vi.fn().mockReturnValue(false),
  killAllActiveProcesses: vi.fn(),
  getActiveInteractiveProcess: vi.fn().mockReturnValue(null),
  runStageSkillHeadless: vi.fn(),
  getNextStage: vi.fn(),
  getStageLabel: vi.fn((stage: string) => stage),
  resolveModel: vi.fn().mockReturnValue({ model: "sonnet", source: "default" }),
}));

function makeLogger(): Logger {
  return {
    info: vi.fn(),
    warn: vi.fn(),
    error: vi.fn(),
    debug: vi.fn(),
  } as unknown as Logger;
}

function git(cwd: string, ...args: string[]): string {
  return execFileSync("git", args, { cwd, encoding: "utf-8" });
}

interface Fixture {
  root: string;
  originDir: string;
  workDir: string;
}

/** bare origin + clone with one commit on main + feature branch checked out */
function makeGitFixture(): Fixture {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), "validate-contract-"));
  const originDir = path.join(root, "origin.git");
  const workDir = path.join(root, "work");
  fs.mkdirSync(originDir);
  git(originDir, "init", "--bare", "--initial-branch=main");
  git(root, "clone", originDir, workDir);
  git(workDir, "config", "user.email", "test@nightgauge.dev");
  git(workDir, "config", "user.name", "Test");
  git(workDir, "checkout", "-b", "main");
  fs.writeFileSync(path.join(workDir, "base.txt"), "base\n");
  git(workDir, "add", "-A");
  git(workDir, "commit", "-m", "base commit");
  git(workDir, "push", "-u", "origin", "main");
  git(workDir, "checkout", "-b", "feat/252-test-branch");
  return { root, originDir, workDir };
}

function writeContexts(
  workDir: string,
  issueNumber: number,
  filesCreated: string[],
  filesModified: string[] = []
): void {
  const dir = path.join(workDir, ".nightgauge", "pipeline");
  fs.mkdirSync(dir, { recursive: true });
  fs.writeFileSync(
    path.join(dir, `dev-${issueNumber}.json`),
    JSON.stringify({
      issue_number: issueNumber,
      stage: "feature-dev",
      files_created: filesCreated,
      files_modified: filesModified,
    })
  );
  fs.writeFileSync(
    path.join(dir, `issue-${issueNumber}.json`),
    JSON.stringify({ issue_number: issueNumber, base_branch: "main", title: "Test issue" })
  );
}

function makeOrch(workDir: string) {
  const orch = new HeadlessOrchestrator(null as never, makeLogger(), {
    contextFileWaitMs: 0,
  } as never);
  vi.spyOn(
    orch as never as { getWorkingDirectory: () => string },
    "getWorkingDirectory"
  ).mockReturnValue(workDir);
  return orch as unknown as {
    enforceValidateCommitContract: (issueNumber: number) => Promise<Error | null>;
  };
}

describe("HeadlessOrchestrator.enforceValidateCommitContract (#1608 backstop)", () => {
  let fx: Fixture;

  beforeEach(() => {
    vi.clearAllMocks();
    fx = makeGitFixture();
  });

  afterEach(() => {
    fs.rmSync(fx.root, { recursive: true, force: true });
  });

  it("remediates: commits the validated tree and pushes when validate skipped its commit", async () => {
    writeContexts(fx.workDir, 252, ["lib/new_widget.dart"], ["base.txt"]);
    // Simulate feature-dev's uncommitted implementation (untracked + modified).
    fs.mkdirSync(path.join(fx.workDir, "lib"), { recursive: true });
    fs.writeFileSync(path.join(fx.workDir, "lib", "new_widget.dart"), "class NewWidget {}\n");
    fs.writeFileSync(path.join(fx.workDir, "base.txt"), "base modified by dev\n");

    const orch = makeOrch(fx.workDir);
    const result = await orch.enforceValidateCommitContract(252);
    expect(result).toBeNull();

    // The validated tree is now committed ahead of base...
    const ahead = git(fx.workDir, "rev-list", "--count", "origin/main..HEAD").trim();
    expect(Number(ahead)).toBe(1);
    // ...source files are clean (only the excluded .nightgauge remains untracked)...
    const status = git(fx.workDir, "status", "--porcelain")
      .split("\n")
      .filter((l) => l.trim() && !l.includes(".nightgauge"));
    expect(status).toEqual([]);
    // ...and the branch was pushed to origin.
    const remoteBranches = git(fx.originDir, "branch", "--list");
    expect(remoteBranches).toContain("feat/252-test-branch");
    // Commit message marks the deterministic backstop.
    const msg = git(fx.workDir, "log", "-1", "--format=%s");
    expect(msg).toContain("deterministic backstop");
    expect(msg).toContain("#252");
  });

  it("fails precisely when the implementation is gone (0 ahead, clean tree, files claimed)", async () => {
    writeContexts(fx.workDir, 252, ["lib/new_widget.dart"]);
    // Clean tree, no commits ahead — the #252 total-loss scenario.
    const orch = makeOrch(fx.workDir);
    const result = await orch.enforceValidateCommitContract(252);
    expect(result).toBeInstanceOf(Error);
    expect(result?.message).toContain("commit contract");
    expect(result?.message).toContain("lib/new_widget.dart");
  });

  it("passes through when the branch is already ahead of base (contract satisfied)", async () => {
    writeContexts(fx.workDir, 252, ["lib/new_widget.dart"]);
    fs.mkdirSync(path.join(fx.workDir, "lib"), { recursive: true });
    fs.writeFileSync(path.join(fx.workDir, "lib", "new_widget.dart"), "class NewWidget {}\n");
    git(fx.workDir, "add", "-A");
    git(fx.workDir, "commit", "-m", "feat(#252): implementation");

    const orch = makeOrch(fx.workDir);
    const result = await orch.enforceValidateCommitContract(252);
    expect(result).toBeNull();
    // No extra commit was created.
    const count = git(fx.workDir, "rev-list", "--count", "origin/main..HEAD").trim();
    expect(Number(count)).toBe(1);
  });

  it("passes through when dev context claims no files (already-resolved flows)", async () => {
    writeContexts(fx.workDir, 252, [], []);
    // Even with a dirty tree, no claimed files → not our contract to enforce.
    fs.writeFileSync(path.join(fx.workDir, "scratch.txt"), "unrelated\n");

    const orch = makeOrch(fx.workDir);
    const result = await orch.enforceValidateCommitContract(252);
    expect(result).toBeNull();
    const ahead = git(fx.workDir, "rev-list", "--count", "origin/main..HEAD").trim();
    expect(Number(ahead)).toBe(0);
  });

  it("fails open when the dev context file is missing", async () => {
    // No contexts written at all.
    const orch = makeOrch(fx.workDir);
    const result = await orch.enforceValidateCommitContract(252);
    expect(result).toBeNull();
  });
});
