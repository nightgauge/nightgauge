/**
 * The managed Codex steering block must never reach a commit (issue 1675).
 *
 * The leak these tests reproduce: CodexContextGenerator writes the block into
 * AGENTS.md before a Codex stage, the stage's agent commits while it is present
 * (`git add -A && git commit`), and cleanup used to strip only the working
 * tree — so HEAD, and every push of it, kept the block.
 */

import { describe, it, expect, beforeEach, afterEach } from "vitest";
import { execFileSync } from "node:child_process";
import * as fs from "node:fs";
import * as os from "node:os";
import * as path from "node:path";
import { CodexContextGenerator } from "../CodexContextGenerator.js";
import { CODEX_MANAGED_BEGIN, CODEX_MANAGED_END } from "../steeringSources.js";
import {
  containsManagedSteering,
  guardUpstreamHead,
  repairCommittedSteering,
  repairCommittedSteeringSync,
  sanitizeStagedAgentsMd,
  sanitizeStagedAgentsMdSync,
  STEERING_REPAIR_COMMIT_MESSAGE,
  stripManagedSteering,
} from "../steeringGuard.js";

const USER = "# Contract\n\nUser rules.\n";
const BLOCK = `${CODEX_MANAGED_BEGIN}\nsteering\n${CODEX_MANAGED_END}\n`;

function git(cwd: string, ...args: string[]): string {
  return execFileSync("git", args, {
    cwd,
    encoding: "utf-8",
    stdio: ["pipe", "pipe", "pipe"],
    env: { ...process.env, GIT_CONFIG_GLOBAL: os.devNull, GIT_CONFIG_NOSYSTEM: "1" },
  });
}

function tryShow(cwd: string, ref: string): string | null {
  try {
    return git(cwd, "show", ref);
  } catch {
    return null;
  }
}

describe("steeringGuard (issue 1675)", () => {
  let dir: string;

  beforeEach(() => {
    dir = fs.mkdtempSync(path.join(os.tmpdir(), "steering-guard-"));
    git(dir, "init", "-q", "-b", "main");
    git(dir, "config", "user.email", "test@example.com");
    git(dir, "config", "user.name", "Test");
    git(dir, "config", "commit.gpgsign", "false");
    fs.writeFileSync(path.join(dir, "AGENTS.md"), USER);
    git(dir, "add", "-A");
    git(dir, "commit", "-qm", "contract");
  });

  afterEach(() => {
    fs.rmSync(dir, { recursive: true, force: true });
  });

  it("cleanup repairs a block the Codex stage's agent committed", async () => {
    const gen = new CodexContextGenerator();
    await gen.generate({
      projectRoot: dir,
      stage: "feature-dev",
      issueNumber: 1,
      adapter: "codex",
    });
    // The stage's agent: stage everything and commit while the block is present.
    fs.writeFileSync(path.join(dir, "work.txt"), "work\n");
    git(dir, "add", "-A");
    git(dir, "commit", "-qm", "feat: work");
    expect(containsManagedSteering(git(dir, "show", "HEAD:AGENTS.md"))).toBe(true);

    const res = await gen.cleanup(dir);

    expect(res?.repaired).toBe(true);
    expect(git(dir, "show", "HEAD:AGENTS.md")).toBe(USER);
    expect(git(dir, "show", "HEAD:work.txt")).toBe("work\n");
    expect(git(dir, "log", "-1", "--format=%s").trim()).toBe(STEERING_REPAIR_COMMIT_MESSAGE);
    expect(fs.readFileSync(path.join(dir, "AGENTS.md"), "utf-8")).toBe(USER);
    expect(git(dir, "status", "--porcelain").trim()).toBe("");
  });

  it("cleanupSync repairs too, and a second cleanup is a no-op", () => {
    const gen = new CodexContextGenerator();
    gen.generateSync({ projectRoot: dir, stage: "feature-dev", issueNumber: 1, adapter: "codex" });
    git(dir, "commit", "-qam", "feat: leaked");
    expect(gen.cleanupSync(dir)?.repaired).toBe(true);
    expect(containsManagedSteering(git(dir, "show", "HEAD:AGENTS.md"))).toBe(false);
    expect(new CodexContextGenerator().cleanupSync(dir)?.repaired).toBe(false);
  });

  it("repair pushes when the leaked commit was already the upstream tip", async () => {
    const remote = fs.mkdtempSync(path.join(os.tmpdir(), "steering-remote-"));
    try {
      git(remote, "init", "-q", "--bare", "-b", "main");
      git(dir, "remote", "add", "origin", remote);
      git(dir, "checkout", "-qb", "feat/x");
      fs.writeFileSync(path.join(dir, "AGENTS.md"), USER + "\n" + BLOCK);
      git(dir, "commit", "-qam", "feat: leaked");
      git(dir, "push", "-qu", "origin", "feat/x");

      const res = await repairCommittedSteering(dir, { push: true });

      expect(res.repaired && res.pushed).toBe(true);
      expect(git(remote, "rev-parse", "feat/x").trim()).toBe(res.newHead);
    } finally {
      fs.rmSync(remote, { recursive: true, force: true });
    }
  });

  it("repair publishes an earlier local repair whose upstream tip is still leaked", async () => {
    const remote = fs.mkdtempSync(path.join(os.tmpdir(), "steering-remote-"));
    try {
      git(remote, "init", "-q", "--bare", "-b", "main");
      git(dir, "remote", "add", "origin", remote);
      git(dir, "checkout", "-qb", "feat/x");
      fs.writeFileSync(path.join(dir, "AGENTS.md"), USER + "\n" + BLOCK);
      git(dir, "commit", "-qam", "feat: leaked");
      git(dir, "push", "-qu", "origin", "feat/x");
      expect(repairCommittedSteeringSync(dir).repaired).toBe(true); // local only

      const res = await repairCommittedSteering(dir, { push: true });

      expect(res.repaired).toBe(false);
      expect(res.pushed).toBe(true);
      expect(containsManagedSteering(git(remote, "show", "feat/x:AGENTS.md"))).toBe(false);
    } finally {
      fs.rmSync(remote, { recursive: true, force: true });
    }
  });

  it("guardUpstreamHead reports a leaked published head, repairs and pushes it", async () => {
    const remote = fs.mkdtempSync(path.join(os.tmpdir(), "steering-remote-"));
    try {
      git(remote, "init", "-q", "--bare", "-b", "main");
      git(dir, "remote", "add", "origin", remote);
      git(dir, "checkout", "-qb", "feat/x");
      fs.writeFileSync(path.join(dir, "AGENTS.md"), USER + "\n" + BLOCK);
      git(dir, "commit", "-qam", "feat: leaked");
      git(dir, "push", "-qu", "origin", "feat/x");

      const v = await guardUpstreamHead(dir);

      expect(v.inspected).toBe(true);
      expect(v.leaked).toEqual(["AGENTS.md"]);
      expect(v.pushed).toBe(true);
      expect(git(remote, "show", "feat/x:AGENTS.md")).toBe(USER);

      const again = await guardUpstreamHead(dir);
      expect(again.leaked).toEqual([]);
    } finally {
      fs.rmSync(remote, { recursive: true, force: true });
    }
  });

  it("sanitize rewrites only the staged AGENTS.md (sync and async agree)", async () => {
    const live = USER + "Another rule.\n\n" + BLOCK;
    for (const sanitize of [
      async (d: string) => sanitizeStagedAgentsMdSync(d),
      (d: string) => sanitizeStagedAgentsMd(d),
    ]) {
      fs.writeFileSync(path.join(dir, "AGENTS.md"), live);
      git(dir, "add", "-A");
      expect(await sanitize(dir)).toBe(true);
      expect(git(dir, "show", ":AGENTS.md")).toBe(USER + "Another rule.\n");
      expect(fs.readFileSync(path.join(dir, "AGENTS.md"), "utf-8")).toBe(live);
      git(dir, "reset", "-q");
    }
  });

  it("sanitize unstages an AGENTS.md that is nothing but generated steering", async () => {
    git(dir, "rm", "-q", "AGENTS.md");
    git(dir, "commit", "-qm", "no contract");
    fs.writeFileSync(path.join(dir, "AGENTS.md"), BLOCK);
    git(dir, "add", "-A");
    expect(await sanitizeStagedAgentsMd(dir)).toBe(true);
    expect(tryShow(dir, ":AGENTS.md")).toBeNull();
  });

  it("sanitize is a no-op outside a repository", async () => {
    const plain = fs.mkdtempSync(path.join(os.tmpdir(), "not-a-repo-"));
    try {
      expect(sanitizeStagedAgentsMdSync(plain)).toBe(false);
      expect(await sanitizeStagedAgentsMd(plain)).toBe(false);
    } finally {
      fs.rmSync(plain, { recursive: true, force: true });
    }
  });

  it("stripManagedSteering drops orphaned markers", () => {
    const out = stripManagedSteering(`${USER}${CODEX_MANAGED_BEGIN}\n`);
    expect(containsManagedSteering(out)).toBe(false);
    expect(out).toContain("User rules.");
  });
});
