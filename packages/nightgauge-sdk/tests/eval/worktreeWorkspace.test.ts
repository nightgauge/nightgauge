/**
 * Tests for the real workspace provider (Issue #4174). The shell/git boundary is
 * mocked (injected ExecFn); filesystem work uses real temp dirs.
 */

import { describe, it, expect, afterEach } from "vitest";
import { mkdtemp, realpath, rm, stat } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import {
  WorktreeWorkspaceProvider,
  type ExecFn,
  type ExecResult,
} from "../../src/eval/worktreeWorkspace.js";
import type { EvalMatrixCell, EvalTask } from "../../src/eval/modelEvalSchemas.js";

const CELL: EvalMatrixCell = {
  model_id: "claude-opus-4-8",
  effort: "high",
  reasoning: "none",
  prompt_variant: "baseline",
};

function task(kind: EvalTask["fixture"]["kind"], ref: string): EvalTask {
  return {
    id: "t",
    title: "t",
    job_class: "backend-logic",
    target_stages: ["feature-dev"],
    difficulty: "easy",
    instruction: "x",
    fixture: { kind, ref },
    checks: [],
    rubric: { criteria: [{ dimension: "correctness", weight: 1, guidance: "?" }] },
  };
}

function recordingExec(code = 0): { exec: ExecFn; calls: Array<{ cmd: string; args: string[] }> } {
  const calls: Array<{ cmd: string; args: string[] }> = [];
  const exec: ExecFn = async (cmd, args): Promise<ExecResult> => {
    calls.push({ cmd, args });
    return { code, stdout: "", stderr: code === 0 ? "" : "boom" };
  };
  return { exec, calls };
}

let roots: string[] = [];
async function tmpRoot(): Promise<string> {
  const r = await mkdtemp(join(tmpdir(), "eval-ws-"));
  roots.push(r);
  return r;
}
afterEach(async () => {
  await Promise.all(roots.map((r) => rm(r, { recursive: true, force: true }).catch(() => {})));
  roots = [];
});

describe("WorktreeWorkspaceProvider", () => {
  it("scaffold-script: makes a fresh dir and runs the setup script, then disposes", async () => {
    const root = await tmpRoot();
    const { exec, calls } = recordingExec();
    const provider = new WorktreeWorkspaceProvider({
      repoRoot: root,
      workspacesDir: join(root, "ws"),
      exec,
    });
    const ws = await provider.acquire(task("scaffold-script", "evals/fixtures/t/setup.sh"), CELL);

    expect((await stat(ws.dir)).isDirectory()).toBe(true);
    expect(calls[0].cmd).toBe("bash");
    expect(calls[0].args[0]).toContain("evals/fixtures/t/setup.sh");
    // Fixture is git-isolated: its own `git init` runs after seeding so a model's
    // git commands stay contained to the throwaway repo, never the host repo.
    const gitInit = calls.find((c) => c.cmd === "git" && c.args[0] === "init");
    expect(gitInit).toBeTruthy();
    expect(calls.some((c) => c.cmd === "git" && c.args.includes("commit"))).toBe(true);

    await ws.dispose();
    await expect(stat(ws.dir)).rejects.toBeTruthy(); // removed
  });

  it("defaults the workspace location OUTSIDE the host repo (under os.tmpdir())", async () => {
    const root = await tmpRoot();
    const { exec } = recordingExec();
    // No workspacesDir override → must not land inside repoRoot.
    const provider = new WorktreeWorkspaceProvider({ repoRoot: root, exec });
    const ws = await provider.acquire(task("scaffold-script", "s.sh"), CELL);
    try {
      expect(ws.dir.startsWith(root)).toBe(false);
      expect(ws.dir.startsWith(tmpdir())).toBe(true);
      expect(ws.dir).toContain("nightgauge-eval-workspaces");
    } finally {
      await ws.dispose();
    }
  });

  it("does NOT git-init a base-commit worktree (already isolated by git worktree)", async () => {
    const root = await tmpRoot();
    const { exec, calls } = recordingExec();
    const provider = new WorktreeWorkspaceProvider({
      repoRoot: root,
      workspacesDir: join(root, "ws"),
      exec,
    });
    const ws = await provider.acquire(task("base-commit", "abc1234"), CELL);
    expect(calls.some((c) => c.cmd === "git" && c.args[0] === "init")).toBe(false);
    await ws.dispose();
  });

  it("base-commit: adds a detached worktree and removes it on dispose", async () => {
    const root = await tmpRoot();
    const { exec, calls } = recordingExec();
    const provider = new WorktreeWorkspaceProvider({
      repoRoot: root,
      workspacesDir: join(root, "ws"),
      exec,
    });
    const ws = await provider.acquire(task("base-commit", "abc1234"), CELL);

    expect(calls[0]).toMatchObject({ cmd: "git" });
    expect(calls[0].args.slice(0, 3)).toEqual(["worktree", "add", "--detach"]);
    expect(calls[0].args[4]).toBe("abc1234");

    await ws.dispose();
    const removeCall = calls.find((c) => c.args.includes("remove"));
    expect(removeCall?.args).toEqual(expect.arrayContaining(["worktree", "remove", "--force"]));
  });

  it("throws when the seed command fails", async () => {
    const root = await tmpRoot();
    const { exec } = recordingExec(1);
    const provider = new WorktreeWorkspaceProvider({
      repoRoot: root,
      workspacesDir: join(root, "ws"),
      exec,
    });
    await expect(provider.acquire(task("scaffold-script", "s.sh"), CELL)).rejects.toThrow(/failed/);
  });

  it("gives concurrent cells distinct directories", async () => {
    const root = await tmpRoot();
    const { exec } = recordingExec();
    const provider = new WorktreeWorkspaceProvider({
      repoRoot: root,
      workspacesDir: join(root, "ws"),
      exec,
    });
    const a = await provider.acquire(task("scaffold-script", "s.sh"), CELL);
    const b = await provider.acquire(task("scaffold-script", "s.sh"), CELL);
    expect(a.dir).not.toBe(b.dir);
    await a.dispose();
    await b.dispose();
  });

  it("workspace identity includes the prompt variant so per-variant cells never collide (#72)", async () => {
    const root = await tmpRoot();
    const { exec } = recordingExec();
    const provider = new WorktreeWorkspaceProvider({
      repoRoot: root,
      workspacesDir: join(root, "ws"),
      exec,
    });
    const ws = await provider.acquire(task("scaffold-script", "s.sh"), {
      ...CELL,
      prompt_variant: "concise-preamble",
    });
    expect(ws.dir).toContain("concise-preamble");
    await ws.dispose();
  });

  it("base-commit with fixture.repo: clones once into a cache and adds the worktree there", async () => {
    const root = await tmpRoot();
    const calls: Array<{ cmd: string; args: string[]; cwd?: string }> = [];
    const exec: ExecFn = async (cmd, args, opts) => {
      calls.push({ cmd, args, cwd: opts.cwd });
      // No cache yet, and the fresh clone lacks the pinned commit.
      const fail = args[0] === "rev-parse" || args[0] === "cat-file";
      return { code: fail ? 1 : 0, stdout: "", stderr: "" };
    };
    const provider = new WorktreeWorkspaceProvider({
      repoRoot: root,
      workspacesDir: join(root, "ws"),
      exec,
    });
    const sha = "d".repeat(40);
    const t = {
      ...task("base-commit", sha),
      fixture: { kind: "base-commit" as const, ref: sha, repo: "acme/fixture" },
    };
    const [a, b] = await Promise.all([provider.acquire(t, CELL), provider.acquire(t, CELL)]);
    const cache = join(root, "ws", "_repos", "acme__fixture");

    const clones = calls.filter((c) => c.args[0] === "clone");
    expect(clones).toHaveLength(1);
    expect(clones[0].args).toEqual([
      "clone",
      "--no-checkout",
      "https://github.com/acme/fixture.git",
      cache,
    ]);
    expect(calls.filter((c) => c.args[0] === "fetch")).toEqual([
      { cmd: "git", args: ["fetch", "origin", sha], cwd: cache },
    ]);
    const adds = calls.filter((c) => c.args[0] === "worktree" && c.args[1] === "add");
    expect(adds.map((c) => c.cwd)).toEqual([cache, cache]);
    expect(adds[0].args).toEqual(["worktree", "add", "--detach", a.dir, sha]);

    await b.dispose();
    const remove = calls.find((c) => c.args[1] === "remove");
    expect(remove?.cwd).toBe(cache);
    await a.dispose();
  });

  it("base-commit without fixture.repo still uses the host repo", async () => {
    const root = await tmpRoot();
    const { exec, calls } = recordingExec();
    const provider = new WorktreeWorkspaceProvider({
      repoRoot: root,
      workspacesDir: join(root, "ws"),
      exec,
    });
    await provider.acquire(task("base-commit", "e".repeat(40)), CELL);
    expect(calls.some((c) => c.args[0] === "clone")).toBe(false);
  });
});

describe("WorktreeWorkspaceProvider isolation: clone (#2118)", () => {
  let root: string;
  afterEach(async () => {
    if (root) await rm(root, { recursive: true, force: true });
  });

  async function git(args: string[], cwd: string): Promise<ExecResult> {
    const { defaultExec } = await import("../../src/eval/worktreeWorkspace.js");
    return defaultExec("git", args, { cwd });
  }

  /** A bare source repo with one commit on main; returns [bareDir, sha]. */
  async function seed(): Promise<[string, string]> {
    root = await mkdtemp(join(tmpdir(), "ng-clone-"));
    const work = join(root, "work");
    const bare = join(root, "src.git");
    const id = ["-c", "user.email=t@t", "-c", "user.name=t", "-c", "commit.gpgsign=false"];
    await git(["init", "-q", "-b", "main", work], root);
    await git([...id, "commit", "-q", "--allow-empty", "-m", "seed"], work);
    await git(["clone", "-q", "--bare", work, bare], root);
    const sha = (await git(["rev-parse", "HEAD"], work)).stdout.trim();
    return [bare, sha];
  }

  it("gives each cell its own clone, branch and git dir, refuses pushes to main, and disposes", async () => {
    const [bare, sha] = await seed();
    const provider = new WorktreeWorkspaceProvider({
      repoRoot: bare,
      workspacesDir: join(root, "cells"),
      isolation: "clone",
      runId: "r1",
    });
    const t = task("base-commit", sha);
    const a = await provider.acquire(t, { ...CELL, effort: "low" });
    const b = await provider.acquire(t, { ...CELL, effort: "high" });
    expect(a.dir).not.toBe(b.dir);
    const branchA = (await git(["branch", "--show-current"], a.dir)).stdout.trim();
    const branchB = (await git(["branch", "--show-current"], b.dir)).stdout.trim();
    expect([branchA, branchB]).toEqual(["eval/sweep/r1/low", "eval/sweep/r1/high"]);
    const gitDirA = (await git(["rev-parse", "--absolute-git-dir"], a.dir)).stdout.trim();
    const gitDirB = (await git(["rev-parse", "--absolute-git-dir"], b.dir)).stdout.trim();
    expect(gitDirA).not.toBe(gitDirB);
    expect(gitDirA.startsWith(await realpath(a.dir))).toBe(true);

    await git(
      [
        "-c",
        "user.email=t@t",
        "-c",
        "user.name=t",
        "-c",
        "commit.gpgsign=false",
        "commit",
        "-q",
        "--allow-empty",
        "-m",
        "cell work",
      ],
      a.dir
    );
    const push = await git(["push", "origin", "HEAD:refs/heads/main"], a.dir);
    expect(push.code).not.toBe(0);
    expect(push.stderr).toContain("refusing to push to refs/heads/main");
    // The cell's own branch may be pushed.
    expect((await git(["push", "origin"], a.dir)).code).toBe(0);

    await a.dispose();
    await b.dispose();
    await expect(stat(a.dir)).rejects.toThrow();
    await expect(stat(b.dir)).rejects.toThrow();
  });
});
