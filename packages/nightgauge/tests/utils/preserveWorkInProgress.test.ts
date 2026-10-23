/**
 * preserveWorkInProgress.test.ts
 *
 * Issue #128: a guard kill (progress-runaway / idle-stall / hard-cap / quota /
 * abort) that lands on a dirty worktree must never discard the work.
 *
 * `feature-dev` never commits (#1608), so between its first edit and
 * `feature-validate` Phase 5 the entire deliverable exists only as uncommitted
 * changes. In the observed incident a complete implementation (a 36-line test
 * plus a ~194-line harness addition, $5.65 of tokens) was terminated in its
 * verification phase and had to be salvaged by hand.
 *
 * These tests run against REAL git repositories in a temp directory — the
 * behaviour under test is entirely "what does git end up containing", and a
 * mocked git would prove nothing about that.
 */

import { describe, it, expect, beforeEach, afterEach } from "vitest";
import { execFileSync } from "child_process";
import * as fs from "fs";
import * as os from "os";
import * as path from "path";
import {
  preserveWorkInProgress,
  shouldPreserveWorkOnExit,
  WIP_COMMIT_TRAILER,
  WIP_REF_NAMESPACE,
} from "../../src/utils/preserveWorkInProgress";

let repo: string;

function git(args: string[], cwd = repo): string {
  return execFileSync("git", args, { cwd, encoding: "utf-8" });
}

/** A repo with one commit on `main` and the stage branch checked out. */
function initRepo(branch = "fix/128-runaway"): void {
  git(["init", "--quiet"], repo);
  git(["symbolic-ref", "HEAD", "refs/heads/main"]);
  git(["config", "user.email", "pipeline@nightgauge.test"]);
  git(["config", "user.name", "Nightgauge Test"]);
  git(["config", "commit.gpgsign", "false"]);
  fs.writeFileSync(path.join(repo, "README.md"), "base\n");
  git(["add", "-A"]);
  git(["commit", "--quiet", "-m", "base"]);
  if (branch !== "main") {
    git(["checkout", "--quiet", "-b", branch]);
  }
}

/** The deliverable the incident produced: one new file, one modified file. */
function writeStageWork(): void {
  fs.mkdirSync(path.join(repo, "test"), { recursive: true });
  fs.writeFileSync(path.join(repo, "test", "harness_entrypoint_test.dart"), "// 36 lines\n");
  fs.writeFileSync(path.join(repo, "README.md"), "base\nharness entrypoint\n");
}

const killOpts = {
  stage: "feature-dev",
  issueNumber: 128,
  killReason: "runaway-progress",
};

beforeEach(() => {
  repo = fs.mkdtempSync(path.join(os.tmpdir(), "ng-wip-"));
});

afterEach(() => {
  fs.rmSync(repo, { recursive: true, force: true });
});

describe("preserveWorkInProgress (#128)", () => {
  it("commits a dirty worktree to the stage branch instead of stranding it", async () => {
    initRepo();
    writeStageWork();

    const result = await preserveWorkInProgress({ cwd: repo, ...killOpts });

    expect(result.outcome).toBe("committed");
    expect(result.branch).toBe("fix/128-runaway");
    expect(result.filesChanged).toBe(2);
    expect(result.commitSha).toMatch(/^[0-9a-f]{40}$/);

    // The tree is clean and the work is IN the commit — not merely staged.
    expect(git(["status", "--porcelain"]).trim()).toBe("");
    expect(git(["rev-list", "--count", "main..HEAD"]).trim()).toBe("1");
    const files = git(["show", "--name-only", "--format=", "HEAD"]).trim().split("\n").sort();
    expect(files).toEqual(["README.md", "test/harness_entrypoint_test.dart"]);
    expect(git(["show", "HEAD:test/harness_entrypoint_test.dart"])).toBe("// 36 lines\n");
  });

  it("preserves an untracked-only deliverable (new files, nothing modified)", async () => {
    initRepo();
    fs.mkdirSync(path.join(repo, "test"), { recursive: true });
    fs.writeFileSync(path.join(repo, "test", "new_test.dart"), "// new\n");

    const result = await preserveWorkInProgress({ cwd: repo, ...killOpts });

    expect(result.outcome).toBe("committed");
    expect(git(["show", "--name-only", "--format=", "HEAD"]).trim()).toBe("test/new_test.dart");
  });

  it("anchors the commit outside refs/heads so re-dispatch cannot orphan it", async () => {
    initRepo();
    writeStageWork();

    const result = await preserveWorkInProgress({ cwd: repo, ...killOpts });
    expect(result.preservedRef).toContain(WIP_REF_NAMESPACE);
    expect(git(["rev-parse", result.preservedRef!]).trim()).toBe(result.commitSha);

    // Simulate what the next dispatch does (WorktreeManager.create):
    // force-remove the worktree and delete the branch, re-creating it from
    // origin/<base>. A branch-only commit would become unreachable here.
    git(["checkout", "--quiet", "main"]);
    git(["branch", "-D", "fix/128-runaway"]);

    expect(git(["rev-parse", result.preservedRef!]).trim()).toBe(result.commitSha);
    expect(git(["cat-file", "-t", result.commitSha!]).trim()).toBe("commit");
    // ...and the salvage is a one-liner for an operator or a later stage.
    expect(git(["show", `${result.preservedRef!}:test/harness_entrypoint_test.dart`])).toBe(
      "// 36 lines\n"
    );
  });

  it("records why the commit exists so a retro can read it back", async () => {
    initRepo();
    writeStageWork();

    await preserveWorkInProgress({ cwd: repo, ...killOpts });

    const message = git(["log", "-1", "--format=%B"]);
    expect(message).toMatch(/^wip\(feature-dev\): preserve uncommitted work/);
    expect(message).toContain("runaway-progress");
    expect(message).toContain("Refs: #128");
    expect(message).toContain(`${WIP_COMMIT_TRAILER}: feature-dev`);
  });

  it("cannot be blocked by a failing pre-commit hook", async () => {
    initRepo();
    writeStageWork();
    const hook = path.join(repo, ".git", "hooks", "pre-commit");
    fs.writeFileSync(hook, "#!/bin/sh\necho 'lint failed' >&2\nexit 1\n");
    fs.chmodSync(hook, 0o755);

    const result = await preserveWorkInProgress({ cwd: repo, ...killOpts });

    expect(result.outcome).toBe("committed");
    expect(git(["status", "--porcelain"]).trim()).toBe("");
  });

  it("is a no-op on a clean worktree", async () => {
    initRepo();

    const result = await preserveWorkInProgress({ cwd: repo, ...killOpts });

    expect(result.outcome).toBe("clean");
    expect(git(["rev-list", "--count", "HEAD"]).trim()).toBe("1"); // no extra commit
  });

  it("refuses to write a WIP commit to main, leaving the work in place", async () => {
    initRepo("main");
    writeStageWork();

    const result = await preserveWorkInProgress({ cwd: repo, ...killOpts });

    expect(result.outcome).toBe("protected-branch");
    expect(result.filesChanged).toBe(2);
    expect(git(["rev-list", "--count", "HEAD"]).trim()).toBe("1");
    // The work is untouched — refusing to commit must never mean discarding.
    expect(git(["status", "--porcelain"]).trim()).not.toBe("");
    expect(fs.existsSync(path.join(repo, "test", "harness_entrypoint_test.dart"))).toBe(true);
  });

  it("refuses on a detached HEAD, leaving the work in place", async () => {
    initRepo();
    git(["checkout", "--quiet", "--detach", "HEAD"]);
    writeStageWork();

    const result = await preserveWorkInProgress({ cwd: repo, ...killOpts });

    expect(result.outcome).toBe("detached-head");
    expect(git(["status", "--porcelain"]).trim()).not.toBe("");
  });

  it("reports a git failure without throwing, leaving the work in place", async () => {
    initRepo();
    writeStageWork();
    // A stale index.lock is the realistic kill-path failure: the agent was
    // mid-`git` when it was reaped.
    fs.writeFileSync(path.join(repo, ".git", "index.lock"), "");

    const result = await preserveWorkInProgress({ cwd: repo, ...killOpts });

    expect(result.outcome).toBe("failed");
    expect(result.detail).toMatch(/left in the worktree/);
    fs.rmSync(path.join(repo, ".git", "index.lock"));
    expect(git(["status", "--porcelain"]).trim()).not.toBe("");
  });

  it("reports a non-repository directory without throwing", async () => {
    const result = await preserveWorkInProgress({ cwd: repo, ...killOpts });
    expect(result.outcome).toBe("not-a-repo");

    const missing = await preserveWorkInProgress({ cwd: "", ...killOpts });
    expect(missing.outcome).toBe("not-a-repo");
  });
});

/**
 * The trigger condition the stage close handler evaluates. Every kill path
 * (runaway / idle-stall / hard-cap / quota fast-fail / autonomous abort)
 * funnels through that one handler, so this predicate is the whole wiring.
 */
describe("shouldPreserveWorkOnExit (#128)", () => {
  it("preserves after a progress-runaway kill", () => {
    expect(
      shouldPreserveWorkOnExit({ success: false, stallKilled: false, costCapExceeded: true })
    ).toBe(true);
  });

  it("preserves after a stall / hard-cap / quota / abort kill", () => {
    expect(
      shouldPreserveWorkOnExit({ success: false, stallKilled: true, costCapExceeded: false })
    ).toBe(true);
  });

  it("does not touch the tree when the stage succeeded", () => {
    expect(
      shouldPreserveWorkOnExit({ success: true, stallKilled: false, costCapExceeded: false })
    ).toBe(false);
    // A kill that raced a clean exit still counts as a success — the stage
    // wrote its own commit and context.
    expect(
      shouldPreserveWorkOnExit({ success: true, stallKilled: true, costCapExceeded: false })
    ).toBe(false);
  });

  it("does not touch the tree when the stage failed on its own", () => {
    // e.g. feature-validate exiting non-zero: its contract is to leave the
    // failed tree in place for triage.
    expect(
      shouldPreserveWorkOnExit({ success: false, stallKilled: false, costCapExceeded: false })
    ).toBe(false);
  });
});
