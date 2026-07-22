/**
 * Tests for the real workspace provider (Issue #4174). The shell/git boundary is
 * mocked (injected ExecFn); filesystem work uses real temp dirs.
 */

import { describe, it, expect, afterEach } from "vitest";
import { mkdtemp, rm, stat } from "node:fs/promises";
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
});
