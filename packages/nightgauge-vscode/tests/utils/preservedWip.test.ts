/**
 * preservedWip.test.ts
 *
 * Issue #1105: `refs/nightgauge/wip/` had one writer and zero readers. A run
 * killed on 2026-08-28 preserved 13 paths; the branch and worktree were then
 * cleaned up; the next day the same issue was re-dispatched and started from
 * scratch, never mentioning the preserved commit to anyone.
 *
 * The fixture here is the REAL writer — `preserveWorkInProgress` — rather than
 * a hand-built ref. A reader tested against a hand-built anchor only proves the
 * test agrees with itself, and the shape the writer actually produces (its
 * `Refs:` line, its trailer, its sanitized ref name) is exactly what this
 * reader has to survive.
 */

import { describe, it, expect, beforeEach, afterEach } from "vitest";
import { execFileSync } from "child_process";
import * as fs from "fs";
import * as os from "os";
import * as path from "path";
import { preserveWorkInProgress } from "../../src/utils/preserveWorkInProgress";
import { describePreservedWip, listPreservedWip } from "../../src/utils/preservedWip";

let repo: string;

function git(args: string[], cwd = repo): string {
  return execFileSync("git", args, { cwd, encoding: "utf-8" });
}

function initRepo(branch: string): void {
  git(["init", "--quiet"], repo);
  git(["symbolic-ref", "HEAD", "refs/heads/main"]);
  git(["config", "user.email", "pipeline@nightgauge.test"]);
  git(["config", "user.name", "Nightgauge Test"]);
  git(["config", "commit.gpgsign", "false"]);
  fs.writeFileSync(path.join(repo, "README.md"), "base\n");
  git(["add", "-A"]);
  git(["commit", "--quiet", "-m", "base"]);
  git(["checkout", "--quiet", "-b", branch]);
}

/** A guard kill on a dirty tree — the only thing that writes a WIP anchor. */
async function killWithUncommittedWork(issueNumber: number, stage: string): Promise<void> {
  fs.mkdirSync(path.join(repo, "lib"), { recursive: true });
  fs.writeFileSync(path.join(repo, "lib", "auth.dart"), "guest\n");
  fs.writeFileSync(path.join(repo, "README.md"), "base\nchanged\n");
  const result = await preserveWorkInProgress({
    cwd: repo,
    stage,
    issueNumber,
    killReason: "stall-kill",
  });
  expect(result.outcome).toBe("committed");
  expect(result.preservedRef).toBeTruthy();
}

beforeEach(() => {
  repo = fs.mkdtempSync(path.join(os.tmpdir(), "ng-wip-read-"));
});

afterEach(() => {
  fs.rmSync(repo, { recursive: true, force: true });
});

describe("listPreservedWip", () => {
  it("finds the anchor the kill path wrote, with its issue, stage and path count", async () => {
    initRepo("feat/338-guest-auth");
    await killWithUncommittedWork(338, "feature-validate");

    const refs = await listPreservedWip(repo);

    expect(refs).toHaveLength(1);
    expect(refs[0].ref).toContain("refs/nightgauge/wip/");
    expect(refs[0].commit).toMatch(/^[0-9a-f]{40}$/);
    expect(refs[0].issueNumber).toBe(338);
    expect(refs[0].stage).toBe("feature-validate");
    expect(refs[0].filesChanged).toBe(2);
  });

  it("still finds the work after the branch is deleted, which is the case that matters", async () => {
    initRepo("feat/338-guest-auth");
    await killWithUncommittedWork(338, "feature-dev");
    // Exactly what re-dispatch does before rebuilding the worktree.
    git(["checkout", "--quiet", "main"]);
    git(["branch", "-D", "feat/338-guest-auth"]);

    const refs = await listPreservedWip(repo);

    expect(refs).toHaveLength(1);
    expect(refs[0].issueNumber).toBe(338);
  });

  it("narrows to one issue so a dispatch only sees its own preserved work", async () => {
    initRepo("feat/338-guest-auth");
    await killWithUncommittedWork(338, "feature-dev");
    git(["checkout", "--quiet", "main"]);
    git(["checkout", "--quiet", "-b", "feat/912-other"]);
    await killWithUncommittedWork(912, "feature-dev");

    expect(await listPreservedWip(repo, 338)).toHaveLength(1);
    expect((await listPreservedWip(repo, 338))[0].issueNumber).toBe(338);
    expect(await listPreservedWip(repo)).toHaveLength(2);
  });

  it("returns nothing, and does not throw, for a repo with no preserved work", async () => {
    initRepo("feat/1-nothing");
    await expect(listPreservedWip(repo)).resolves.toEqual([]);
  });

  it("returns nothing, and does not throw, outside a git repository", async () => {
    const notARepo = fs.mkdtempSync(path.join(os.tmpdir(), "ng-not-a-repo-"));
    try {
      await expect(listPreservedWip(notARepo)).resolves.toEqual([]);
    } finally {
      fs.rmSync(notARepo, { recursive: true, force: true });
    }
  });
});

describe("describePreservedWip", () => {
  it("names the issue, the commit and the way to get the work back", async () => {
    initRepo("feat/338-guest-auth");
    await killWithUncommittedWork(338, "feature-validate");
    const refs = await listPreservedWip(repo, 338);

    const message = describePreservedWip(338, refs);

    expect(message).toContain("#338");
    expect(message).toContain(refs[0].commit.slice(0, 8));
    expect(message).toContain("2 path(s)");
    expect(message).toContain("feature-validate");
    // A notice that assumes the reader already knows the namespace exists is
    // the bug, not the fix.
    expect(message).toContain("nightgauge wip list --issue 338");
    expect(message).toContain("git checkout -b salvage-338");
  });
});
