/**
 * Real workspace provider for the model-eval runner (Issue #4174).
 *
 * Materializes a task's fixture into an isolated directory per cell, so every
 * model starts from the identical seed state, then tears it down. Implements the
 * S4 `WorkspaceProvider` boundary. Three fixture kinds (per EvalFixtureRef):
 *   - scaffold-script : fresh temp dir + run the task's setup.sh
 *   - base-commit     : `git worktree add --detach <dir> <sha>` (reuses the
 *                       pipeline's worktree isolation)
 *   - snapshot-dir    : copy a pre-built tree
 *
 * **Isolation from the host repo (critical for live mode).** Scaffold/snapshot
 * workspaces default to a directory under `os.tmpdir()`, *outside* the host repo,
 * and are given their own throwaway git repo (`git init`). Without this, a live
 * model told to "fix and commit" runs `git` against the nearest `.git` — the
 * host repo — and can stage/commit/checkout the user's working tree. Placing the
 * fixture outside the repo removes that vector; the per-fixture `git init`
 * additionally contains any git command the model runs and gives it a clean,
 * committable baseline. (`base-commit` already isolates via `git worktree`.)
 *
 * The shell/git boundary is an injected `ExecFn` so the provider is unit-testable
 * without a real repo; the default binds to node:child_process.
 *
 * @see docs/decisions/011-model-eval-system.md
 */

import { mkdir, rm, cp } from "node:fs/promises";
import { tmpdir } from "node:os";
import { resolve, join } from "node:path";
import type { EvalMatrixCell, EvalTask } from "./modelEvalSchemas.js";
import type { EvalWorkspace, WorkspaceProvider } from "./modelEvalRunner.js";

export interface ExecResult {
  code: number;
  stdout: string;
  stderr: string;
}

/** Runs a command; injected so the provider is testable without a shell. */
export type ExecFn = (cmd: string, args: string[], opts: { cwd?: string }) => Promise<ExecResult>;

export interface WorktreeWorkspaceOptions {
  /** Canonical repo root fixtures/worktrees are resolved against. */
  repoRoot: string;
  /**
   * Where per-cell isolated dirs are created. Defaults to a directory under
   * `os.tmpdir()` — deliberately *outside* the host repo so a live model's git
   * commands cannot reach the user's working tree. An absolute override is used
   * as-is; a relative one is resolved against `repoRoot`.
   */
  workspacesDir?: string;
  /** Shell/git boundary (default: node:child_process). */
  exec?: ExecFn;
}

/**
 * WorkspaceProvider that gives each cell a fresh isolated directory seeded to the
 * task's fixture, and disposes it afterward. Directory names are unique per
 * (task, cell) plus a monotonically increasing counter so concurrent cells never
 * collide.
 */
export class WorktreeWorkspaceProvider implements WorkspaceProvider {
  private readonly repoRoot: string;
  private readonly workspacesDir: string;
  private readonly exec: ExecFn;
  private counter = 0;

  constructor(options: WorktreeWorkspaceOptions) {
    this.repoRoot = options.repoRoot;
    // Default OUTSIDE the host repo (see class doc). `resolve` below keeps an
    // absolute override as-is and resolves a relative one against repoRoot.
    this.workspacesDir = options.workspacesDir ?? join(tmpdir(), "nightgauge-eval-workspaces");
    this.exec = options.exec ?? defaultExec;
  }

  async acquire(task: EvalTask, cell: EvalMatrixCell): Promise<EvalWorkspace> {
    // prompt_variant is part of the identity (#72) so per-variant cells never
    // share a workspace even at equal counter values across providers.
    const id = `${task.id}-${cell.model_id}-${cell.effort}-${cell.reasoning}-${cell.prompt_variant}-${this.counter++}`;
    const dir = resolve(this.repoRoot, this.workspacesDir, sanitize(id));
    const { kind, ref } = task.fixture;

    if (kind === "base-commit") {
      // Reuse git-worktree isolation: a detached worktree at the seed commit.
      await this.run("git", ["worktree", "add", "--detach", dir, ref], this.repoRoot);
      return {
        dir,
        dispose: async () => {
          await this.run("git", ["worktree", "remove", "--force", dir], this.repoRoot).catch(
            () => {}
          );
        },
      };
    }

    // scaffold-script and snapshot-dir both start from a fresh empty dir.
    await mkdir(dir, { recursive: true });
    if (kind === "scaffold-script") {
      const script = resolve(this.repoRoot, ref);
      await this.run("bash", [script], dir);
    } else {
      // snapshot-dir: copy the pre-built tree into the workspace.
      await cp(resolve(this.repoRoot, ref), dir, { recursive: true });
    }
    // Contain any git the model runs to this throwaway repo (see class doc).
    await this.initFixtureGit(dir);
    return {
      dir,
      dispose: async () => {
        await rm(dir, { recursive: true, force: true }).catch(() => {});
      },
    };
  }

  /**
   * Give the fixture its own git repo with a baseline commit. `git init` is the
   * isolation guarantee (the model's `git` never escapes this dir); the baseline
   * commit is a best-effort convenience so tasks that "commit the fix" start from
   * a clean tree. All best-effort: a missing `git` leaves the dir untracked, and
   * since it lives outside the host repo there is nothing to corrupt either way.
   */
  private async initFixtureGit(dir: string): Promise<void> {
    const init = await this.exec("git", ["init", "-q"], { cwd: dir });
    if (init.code !== 0) return;
    const identity = [
      "-c",
      "user.email=eval@nightgauge.local",
      "-c",
      "user.name=Nightgauge Eval",
      "-c",
      "commit.gpgsign=false",
    ];
    await this.exec("git", ["add", "-A"], { cwd: dir });
    await this.exec("git", [...identity, "commit", "-q", "-m", "eval fixture baseline"], {
      cwd: dir,
    });
  }

  private async run(cmd: string, args: string[], cwd: string): Promise<void> {
    const r = await this.exec(cmd, args, { cwd });
    if (r.code !== 0) {
      throw new Error(
        `${cmd} ${args.join(" ")} failed (exit ${r.code}): ${r.stderr.trim() || r.stdout.trim()}`
      );
    }
  }
}

/** Replace path-unsafe characters so ids form valid directory names. */
function sanitize(id: string): string {
  return id.replace(/[^A-Za-z0-9._-]/g, "_");
}

/** Default exec bound to node:child_process (spawn, buffered). */
export const defaultExec: ExecFn = (cmd, args, opts) =>
  import("node:child_process").then(
    ({ spawn }) =>
      new Promise<ExecResult>((resolvePromise) => {
        const child = spawn(cmd, args, { cwd: opts.cwd });
        let stdout = "";
        let stderr = "";
        child.stdout?.on("data", (d) => (stdout += d.toString()));
        child.stderr?.on("data", (d) => (stderr += d.toString()));
        child.on("error", (err) =>
          resolvePromise({ code: 1, stdout, stderr: stderr + String(err) })
        );
        child.on("close", (code) => resolvePromise({ code: code ?? 1, stdout, stderr }));
      })
  );
