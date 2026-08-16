/**
 * WorktreeManager - Git worktree lifecycle management for concurrent pipelines
 *
 * Creates, lists, and cleans up git worktrees used for parallel pipeline
 * execution. Each concurrent pipeline runs in its own worktree directory,
 * providing full filesystem isolation while sharing the .git object store.
 *
 * @see Issue #1621 - Git worktree-based concurrent pipeline execution
 */

import * as path from "node:path";
import { exec, execFile } from "node:child_process";
import { promisify } from "node:util";
import * as fs from "node:fs/promises";
import { assertValidBranchName } from "./BranchNameValidator";

const execAsync = promisify(exec);
// #2884: avoid sync subprocess — blocks the VSCode extension host event loop.
const execFileAsync = promisify(execFile);

/**
 * Information about an active worktree
 */
export interface WorktreeInfo {
  /** Absolute path to worktree directory */
  path: string;
  /** Git branch name checked out in this worktree */
  branch: string;
  /** Issue number this worktree is associated with */
  issueNumber: number;
  /** Whether the worktree directory exists on disk */
  exists: boolean;
}

/**
 * Options for worktree creation
 */
export interface WorktreeCreateOptions {
  /** Run npm install after creating the worktree (default: true) */
  npmInstall?: boolean;
  /** Timeout for npm install in ms (default: 300000 / 5 minutes) */
  npmInstallTimeout?: number;
  /** Base branch to create worktree from (default: 'main') */
  baseBranch?: string;
  /**
   * Delete the remote tracking branch before creating the worktree.
   * Use for conflict-restart: clears the old conflicting remote branch so
   * GitHub auto-closes the stale PR, and the fresh push won't be rejected
   * as a non-fast-forward update.
   * Default: false (do not touch remote branches on normal retries).
   */
  deleteRemoteBranch?: boolean;
}

const DEFAULT_NPM_INSTALL_TIMEOUT = 300_000; // 5 minutes

export class WorktreeManager {
  private repoRoot: string;
  private worktreeBase: string;

  constructor(repoRoot: string, worktreeBase: string = ".worktrees") {
    this.repoRoot = repoRoot;
    this.worktreeBase = worktreeBase;
  }

  /**
   * Get the repository root path
   */
  getRepoRoot(): string {
    return this.repoRoot;
  }

  /**
   * Get the absolute path where a worktree for an issue would live
   */
  getWorktreePath(issueNumber: number): string {
    return path.join(this.repoRoot, this.worktreeBase, `issue-${issueNumber}`);
  }

  /**
   * Create an isolated worktree for an issue
   *
   * Creates a new git worktree with a new branch based off the base branch.
   * Optionally runs npm install to populate node_modules.
   *
   * @param issueNumber - The issue number to create a worktree for
   * @param branchName - The git branch name to create
   * @param options - Creation options
   * @returns Information about the created worktree
   * @throws Error if worktree creation fails
   */
  async create(
    issueNumber: number,
    branchName: string,
    options?: WorktreeCreateOptions
  ): Promise<WorktreeInfo> {
    const worktreePath = this.getWorktreePath(issueNumber);
    const baseBranch = options?.baseBranch ?? "main";
    const shouldInstall = options?.npmInstall !== false;
    const installTimeout = options?.npmInstallTimeout ?? DEFAULT_NPM_INSTALL_TIMEOUT;

    // Validate branch names before use in any shell or git command
    assertValidBranchName(branchName, "branchName");
    assertValidBranchName(baseBranch, "baseBranch");

    // A pipeline worktree must never be created on the default branch (#332).
    // `.worktrees/issue-696` ended up holding `main`, and a worktree holding
    // `main` breaks the operator's own primary clone outright:
    //
    //   $ git checkout main
    //   fatal: 'main' is already used by worktree at '.../.worktrees/issue-696'
    //
    // It also arms the stale-cleanup path below, which runs
    // `git branch -D <branchName>` unconditionally — with branchName === main
    // that deletes the trunk. Refusing here is the only place both failures
    // are still cheap; by the time `worktree sweep` sees it the damage is done
    // and reclamation is all that is left.
    if (branchName === baseBranch || branchName === "main" || branchName === "master") {
      throw new Error(
        `Refusing to create a pipeline worktree for issue #${issueNumber} on the default branch ` +
          `("${branchName}"): a worktree holding the default branch blocks checkout in the primary ` +
          `clone and would make the stale-worktree cleanup delete the branch.`
      );
    }

    // Ensure the base directory exists
    const baseDir = path.join(this.repoRoot, this.worktreeBase);
    await fs.mkdir(baseDir, { recursive: true });

    // Add .worktrees to .gitignore if not already there
    await this.ensureGitignore();

    // Fetch latest from remote to ensure base branch is up to date
    try {
      await execAsync("git fetch origin", {
        cwd: this.repoRoot,
        timeout: 30_000,
      });
    } catch {
      // Non-fatal — may be offline
    }

    // Re-dispatch reuse decision (#135): a conflict-restart is an explicit
    // discard signal and always takes the destructive path below. Otherwise,
    // if the target branch already has commits ahead of origin/<baseBranch>,
    // reuse it instead of destroying committed work on every retry.
    if (!options?.deleteRemoteBranch) {
      // #283 defect 3 (work destruction): commits alone cannot decide reuse.
      // feature-dev leaves its implementation UNCOMMITTED by contract
      // (feature-validate commits), so after a failure-revert the preserved
      // worktree is dirty with zero unique commits — and the rebuild below
      // (`worktree remove --force` + `branch -D` + fresh add) deleted a
      // completed, paid-for implementation on re-dispatch. A worktree that is
      // registered to this branch AND carries uncommitted work is real work:
      // reuse it in place.
      if (
        (await this.isWorktreeRegisteredToBranch(worktreePath, branchName)) &&
        (await this.worktreeHasUncommittedWork(worktreePath))
      ) {
        return {
          path: worktreePath,
          branch: branchName,
          issueNumber,
          exists: true,
        };
      }
      const hasUniqueCommits = await this.branchHasUniqueCommits(branchName, baseBranch);
      if (hasUniqueCommits) {
        if (await this.isWorktreeRegisteredToBranch(worktreePath, branchName)) {
          // Worktree already checked out to this branch — nothing to do.
          return {
            path: worktreePath,
            branch: branchName,
            issueNumber,
            exists: true,
          };
        }

        // Branch has unique work but no worktree registered to it (e.g. the
        // directory was removed externally) — resume from the branch tip
        // instead of rebuilding from origin/<baseBranch>.
        await fs.rm(worktreePath, { recursive: true, force: true }).catch(() => {});
        await execAsync(`git worktree prune`, {
          cwd: this.repoRoot,
          timeout: 5_000,
        }).catch(() => {
          // Non-fatal
        });
        await execFileAsync("git", ["worktree", "add", worktreePath, branchName], {
          cwd: this.repoRoot,
          timeout: 30_000,
        });

        return this.finishCreate(worktreePath, branchName, issueNumber, baseBranch, {
          shouldInstall,
          installTimeout,
        });
      }
    }

    // Clean up stale worktree/branch from a previous failed run.
    // Without this, `git worktree add -b` fails with "branch already exists"
    // or "path already exists" on retry after a crash.
    try {
      await execAsync(`git worktree remove "${worktreePath}" --force`, {
        cwd: this.repoRoot,
        timeout: 10_000,
      });
    } catch {
      // Worktree not registered — try removing leftover directory
      await fs.rm(worktreePath, { recursive: true, force: true }).catch(() => {});
    }
    try {
      await execAsync(`git worktree prune`, {
        cwd: this.repoRoot,
        timeout: 5_000,
      });
    } catch {
      // Non-fatal
    }
    try {
      await execFileAsync("git", ["branch", "-D", branchName], {
        cwd: this.repoRoot,
        timeout: 5_000,
      });
    } catch {
      // Branch doesn't exist — expected on first run
    }

    // Conflict-restart: force-delete the remote branch so GitHub auto-closes
    // the stale conflicting PR, and the fresh push won't be rejected as
    // non-fast-forward. Only done when explicitly requested — never on
    // normal retries where the user may have work on the remote branch.
    if (options?.deleteRemoteBranch) {
      try {
        await execFileAsync("git", ["push", "origin", "--delete", branchName], {
          cwd: this.repoRoot,
          timeout: 15_000,
        });
      } catch {
        // Remote branch may not exist — non-fatal
      }
    }

    // Create the worktree with a new branch
    await execFileAsync(
      "git",
      ["worktree", "add", worktreePath, "-b", branchName, `origin/${baseBranch}`],
      { cwd: this.repoRoot, timeout: 30_000 }
    );

    return this.finishCreate(worktreePath, branchName, issueNumber, baseBranch, {
      shouldInstall,
      installTimeout,
    });
  }

  /**
   * Shared post-creation steps (gitignore/config propagation, epic-branch
   * merge, npm install, Flutter codegen) that run identically regardless of
   * whether `create()` took the reuse or rebuild path — they only depend on
   * `worktreePath` existing and being checked out.
   */
  private async finishCreate(
    worktreePath: string,
    branchName: string,
    issueNumber: number,
    baseBranch: string,
    opts: { shouldInstall: boolean; installTimeout: number }
  ): Promise<WorktreeInfo> {
    const { shouldInstall, installTimeout } = opts;

    // Propagate the gitignored local config tier into the worktree. Tracked
    // files (including .nightgauge/config.yaml) arrive via the checkout,
    // but config.local.yaml is gitignored, so a fresh worktree would silently
    // drop the operator's local overrides — the Go binary's gates
    // (approval-gate, etc.) run with --workdir <worktree> and merge
    // machine → project → local from THAT directory. Mirrors the Go-path
    // copyWorktreeConfig (internal/execution/worktree.go). The tracked
    // config.yaml is deliberately NOT copied: the worktree's checkout of
    // origin/<base> is fresher than a possibly-stale parent checkout.
    try {
      const localConfigSrc = path.join(this.repoRoot, ".nightgauge", "config.local.yaml");
      const localConfigDst = path.join(worktreePath, ".nightgauge", "config.local.yaml");
      const localConfigData = await fs.readFile(localConfigSrc, "utf-8");
      await fs.mkdir(path.dirname(localConfigDst), { recursive: true });
      await fs.writeFile(localConfigDst, localConfigData, "utf-8");
    } catch {
      // No local config — nothing to propagate.
    }

    // For epic branches, merge main to keep the worktree up to date.
    // Without this, sub-issues build on a stale epic branch that may be
    // missing fixes merged directly to main, causing merge conflicts at PR time.
    if (baseBranch !== "main" && baseBranch.startsWith("epic/")) {
      try {
        await execAsync("git merge origin/main --no-edit", {
          cwd: worktreePath,
          timeout: 60_000,
        });
      } catch {
        // Merge conflict — non-fatal. The worktree still has the epic branch
        // content. Log via stderr which gets captured by the extension.

        console.warn(
          `[WorktreeManager] Failed to merge main into ${baseBranch} for issue #${issueNumber}. ` +
            "The worktree will use the epic branch as-is. Manual merge may be needed."
        );
      }
    }

    // Run npm install if requested AND the project has a package.json.
    // Non-Node.js projects (e.g. Flutter/Dart) won't have one.
    const hasPackageJson = await fs
      .access(path.join(worktreePath, "package.json"))
      .then(() => true)
      .catch(() => false);
    if (shouldInstall && hasPackageJson) {
      try {
        await execAsync("npm install --prefer-offline", {
          cwd: worktreePath,
          timeout: installTimeout,
        });
      } catch (error) {
        // Non-fatal: npm install may fail due to engine mismatch (e.g. VSCode's
        // bundled Node vs project's required version). The pipeline agent runs
        // with the user's full shell (nvm) and can install deps itself.

        console.warn(
          `[WorktreeManager] npm install failed in worktree for issue #${issueNumber} (non-fatal): ${
            error instanceof Error ? error.message : "Unknown error"
          }`
        );
      }
    }

    // Run Flutter codegen if this is a Flutter project that uses .g.dart parts.
    // Without this, sub-issue worktrees ship whatever .g.dart files were
    // committed — often stale relative to the current schema — and tests
    // written by feature-dev fail with "Undefined class" errors for generated
    // companions (Drift tables, Riverpod providers, etc.).
    if (shouldInstall) {
      await this.runFlutterCodegen(worktreePath, issueNumber, installTimeout);
    }

    return {
      path: worktreePath,
      branch: branchName,
      issueNumber,
      exists: true,
    };
  }

  /**
   * Check whether worktreePath has modified, staged, or untracked tracked
   * changes. Mirrors the Go side's `hasUncommittedChanges`
   * (internal/execution/worktree_sweep.go) so both languages preserve a
   * worktree with real work in progress rather than force-removing it.
   */
  private async hasUncommittedChanges(worktreePath: string): Promise<boolean> {
    try {
      const { stdout } = await execAsync("git status --porcelain", {
        cwd: worktreePath,
        timeout: 10_000,
      });
      return stdout.trim().length > 0;
    } catch {
      // Can't tell — treat as dirty so we never destroy something we
      // couldn't inspect.
      return true;
    }
  }

  /**
   * Content-diff merge check mirroring the Go side's `mergedIntoBase`
   * (internal/execution/worktree_sweep.go). A full-tree two-dot
   * `git diff --stat baseRef..branch` false-negatives after update-branch
   * + squash (#583). Same algorithm as branch-merged-check.sh:
   * ancestor fast-path, three-dot file list, path-restricted tip-vs-tip
   * via execFile (argv-safe). Empty file list + not ancestor is not merged.
   */
  private async isMergedIntoBase(
    branch: string,
    baseRef: string
  ): Promise<{ merged: boolean; hasOwnCommits: boolean }> {
    try {
      const { stdout: countOut } = await execFileAsync(
        "git",
        ["rev-list", "--count", `${baseRef}..${branch}`],
        { cwd: this.repoRoot, timeout: 10_000, encoding: "utf-8" }
      );
      const hasOwnCommits = String(countOut).trim() !== "0";

      try {
        await execFileAsync("git", ["merge-base", "--is-ancestor", branch, baseRef], {
          cwd: this.repoRoot,
          timeout: 10_000,
        });
        return { merged: true, hasOwnCommits };
      } catch {
        // Exit 1 = not an ancestor (squash-merged tips). Other failures
        // fall through; later git calls will reject and we fail closed.
      }

      const { stdout: nameOut } = await execFileAsync(
        "git",
        ["diff", "--name-only", "-z", `${baseRef}...${branch}`],
        { cwd: this.repoRoot, timeout: 10_000, encoding: "utf-8" }
      );
      const files = String(nameOut)
        .split("\0")
        .filter((p) => p.length > 0);
      if (files.length === 0) {
        return { merged: false, hasOwnCommits };
      }

      const { stdout: diffOut } = await execFileAsync(
        "git",
        ["diff", "--stat", baseRef, branch, "--", ...files],
        { cwd: this.repoRoot, timeout: 10_000, encoding: "utf-8" }
      );
      return { merged: String(diffOut).trim() === "", hasOwnCommits };
    } catch {
      // Can't tell — treat as not-merged so we never delete an unmerged branch.
      return { merged: false, hasOwnCommits: false };
    }
  }

  /**
   * Resolve the ref branches are compared against, preferring the remote
   * tracking ref (mirrors the Go side's `resolveBaseRef`).
   */
  private async resolveBaseRef(defaultBranch: string): Promise<string> {
    try {
      await execFileAsync(
        "git",
        ["rev-parse", "--verify", "--quiet", `refs/remotes/origin/${defaultBranch}`],
        {
          cwd: this.repoRoot,
          timeout: 5_000,
        }
      );
      return `origin/${defaultBranch}`;
    } catch {
      return defaultBranch;
    }
  }

  /**
   * Detect the repo's default branch (mirrors the Go side's
   * `detectDefaultBranch`): origin/HEAD first, then whichever of main/master
   * exists locally or on origin.
   */
  private async detectDefaultBranch(): Promise<string> {
    try {
      const { stdout } = await execFileAsync(
        "git",
        ["symbolic-ref", "--short", "refs/remotes/origin/HEAD"],
        { cwd: this.repoRoot, timeout: 5_000, encoding: "utf-8" }
      );
      const name = stdout.trim().replace(/^origin\//, "");
      if (name) return name;
    } catch {
      // fall through to main/master probing
    }
    for (const candidate of ["main", "master"]) {
      try {
        await execFileAsync(
          "git",
          ["rev-parse", "--verify", "--quiet", `refs/heads/${candidate}`],
          {
            cwd: this.repoRoot,
            timeout: 5_000,
          }
        );
        return candidate;
      } catch {
        // try next candidate
      }
    }
    return "main";
  }

  /**
   * Remove a worktree and optionally delete the branch
   *
   * A worktree with uncommitted tracked changes is preserved (logged, not
   * removed). When deleteBranch is requested, the branch is only deleted
   * after a content-diff check confirms it is already merged into the
   * default branch — an unmerged branch is never deleted (#106).
   *
   * @param issueNumber - The issue number whose worktree to remove
   * @param deleteBranch - Whether to also delete the local branch (default: false)
   */
  async cleanup(issueNumber: number, deleteBranch?: boolean): Promise<void> {
    const worktreePath = this.getWorktreePath(issueNumber);

    if (await this.hasUncommittedChanges(worktreePath)) {
      console.warn(
        `[WorktreeManager] preserving worktree for issue #${issueNumber} — uncommitted tracked changes`
      );
      return;
    }

    // Tear down the per-issue docker compose stack BEFORE removing the
    // worktree. Soft-fail by design — when docker is missing or the daemon
    // is down, log a warning and continue. See Issue #3050.
    await this.teardownComposeStack(issueNumber);

    // Get branch name before removing worktree
    let branchName: string | undefined;
    if (deleteBranch) {
      try {
        const { stdout } = await execFileAsync(
          "git",
          ["-C", worktreePath, "branch", "--show-current"],
          { timeout: 5_000, encoding: "utf-8" }
        );
        branchName = stdout.trim();
      } catch {
        // Non-fatal — branch may already be deleted
      }
    }

    // Remove the worktree (--force handles dirty working directories)
    try {
      await execAsync(`git worktree remove "${worktreePath}" --force`, {
        cwd: this.repoRoot,
        timeout: 15_000,
      });
    } catch (error) {
      // Best-effort, but never silent. A swallowed failure here is exactly how
      // leaked worktrees stayed invisible until someone counted them days
      // later (Issue #110) — the reconcile sweep reclaims them eventually, but
      // the operator should be able to see the leak happen.
      console.warn(
        `[WorktreeManager] git worktree remove failed for issue #${issueNumber} at ${worktreePath} — falling back to manual removal: ${
          error instanceof Error ? error.message : String(error)
        }`
      );
      // If git worktree remove fails, try manual cleanup
      try {
        await fs.rm(worktreePath, { recursive: true, force: true });
        await execAsync("git worktree prune", {
          cwd: this.repoRoot,
          timeout: 10_000,
        });
      } catch (fallbackError) {
        console.warn(
          `[WorktreeManager] manual removal of ${worktreePath} also failed — worktree LEAKED: ${
            fallbackError instanceof Error ? fallbackError.message : String(fallbackError)
          }`
        );
      }
    }

    // Delete the branch if requested and worktree was cleaned up — but only
    // once a content-diff check confirms it is actually merged (#106). Never
    // delete on an inconclusive check.
    if (deleteBranch && branchName) {
      const defaultBranch = await this.detectDefaultBranch();
      try {
        await execFileAsync("git", ["fetch", "origin", defaultBranch], {
          cwd: this.repoRoot,
          timeout: 30_000,
        });
      } catch (error) {
        console.warn(
          `[WorktreeManager] failed to fetch origin/${defaultBranch} before merge check — classifying against local ref: ${
            error instanceof Error ? error.message : String(error)
          }`
        );
      }
      const baseRef = await this.resolveBaseRef(defaultBranch);
      const { merged, hasOwnCommits } = await this.isMergedIntoBase(branchName, baseRef);
      if (!merged || !hasOwnCommits) {
        console.warn(
          `[WorktreeManager] not deleting branch ${branchName} for issue #${issueNumber} — unmerged content vs ${baseRef}`
        );
      } else {
        try {
          await execFileAsync("git", ["branch", "-D", branchName], {
            cwd: this.repoRoot,
            timeout: 5_000,
          });
        } catch {
          // Non-fatal — branch may not exist or be checked out elsewhere
        }
      }
    }
  }

  /**
   * List all active worktrees managed by this instance
   *
   * Filters `git worktree list` output to only show worktrees in the
   * configured worktree base directory.
   *
   * @returns Array of active worktree info
   */
  async listActive(): Promise<WorktreeInfo[]> {
    try {
      const { stdout } = await execAsync("git worktree list --porcelain", {
        cwd: this.repoRoot,
        timeout: 10_000,
      });

      const worktrees: WorktreeInfo[] = [];
      const baseDir = path.join(this.repoRoot, this.worktreeBase);
      const entries = stdout.split("\n\n").filter(Boolean);

      for (const entry of entries) {
        const lines = entry.split("\n");
        const worktreeLine = lines.find((l) => l.startsWith("worktree "));
        const branchLine = lines.find((l) => l.startsWith("branch "));

        if (!worktreeLine) continue;
        const worktreePath = worktreeLine.replace("worktree ", "");

        // Only include worktrees in our managed directory
        if (!worktreePath.startsWith(baseDir)) continue;

        const branch = branchLine ? branchLine.replace("branch refs/heads/", "") : "unknown";

        // Extract issue number from directory name
        const dirName = path.basename(worktreePath);
        const match = dirName.match(/^issue-(\d+)$/);
        if (!match) continue;

        const issueNumber = parseInt(match[1], 10);
        let exists = false;
        try {
          await fs.access(worktreePath);
          exists = true;
        } catch {
          exists = false;
        }

        worktrees.push({
          path: worktreePath,
          branch,
          issueNumber,
          exists,
        });
      }

      return worktrees;
    } catch {
      return [];
    }
  }

  /**
   * Check whether `branchName` exists locally and has commits ahead of
   * `origin/<baseBranch>` — i.e. real work that a destructive rebuild would
   * lose. Requires a prior `git fetch origin` (already run earlier in
   * `create()`) for `origin/<baseBranch>` to be current.
   */
  /**
   * True when the worktree directory carries uncommitted work — modified
   * tracked files or unignored untracked files (`git status --porcelain`).
   * The check that keeps a re-dispatch from deleting a completed,
   * not-yet-committed implementation (#283): feature-dev's deliverable is
   * uncommitted by contract, so this — not commit count — is the signal that
   * the worktree holds real work. Errors (missing dir, not a worktree) fall
   * back to false, which routes to the existing rebuild path.
   */
  private async worktreeHasUncommittedWork(worktreePath: string): Promise<boolean> {
    try {
      const { stdout } = await execFileAsync("git", ["-C", worktreePath, "status", "--porcelain"], {
        timeout: 10_000,
        encoding: "utf-8",
      });
      return stdout.trim().length > 0;
    } catch {
      return false;
    }
  }

  private async branchHasUniqueCommits(branchName: string, baseBranch: string): Promise<boolean> {
    try {
      await execFileAsync("git", ["rev-parse", "--verify", "--quiet", `refs/heads/${branchName}`], {
        cwd: this.repoRoot,
        timeout: 5_000,
      });
    } catch {
      // Branch doesn't exist locally — nothing to reuse.
      return false;
    }

    try {
      const { stdout } = await execFileAsync(
        "git",
        ["rev-list", "--count", `origin/${baseBranch}..${branchName}`],
        { cwd: this.repoRoot, timeout: 5_000, encoding: "utf-8" }
      );
      return parseInt(stdout.trim(), 10) > 0;
    } catch {
      // Can't determine ahead-count (e.g. origin/<baseBranch> missing) —
      // fall back to the safe, existing destructive path.
      return false;
    }
  }

  /**
   * Check whether the worktree directory is already registered by git and
   * checked out to `branchName` — mirrors the Go path's `os.Stat`
   * short-circuit (`internal/execution/worktree.go`) for the TS pipeline.
   */
  private async isWorktreeRegisteredToBranch(
    worktreePath: string,
    branchName: string
  ): Promise<boolean> {
    const active = await this.listActive();
    return active.some((w) => w.path === worktreePath && w.branch === branchName && w.exists);
  }

  /**
   * Check if a worktree exists for a given issue
   */
  async exists(issueNumber: number): Promise<boolean> {
    const worktreePath = this.getWorktreePath(issueNumber);
    try {
      await fs.access(worktreePath);
      return true;
    } catch {
      return false;
    }
  }

  /**
   * Detect and clean up orphaned worktrees
   *
   * Orphaned worktrees are those that exist on disk but whose git metadata
   * is missing (e.g., from a crashed extension). Also prunes git's internal
   * worktree list of stale entries.
   *
   * @returns Number of orphans cleaned up
   */
  async cleanupOrphans(): Promise<number> {
    let cleaned = 0;

    // First, prune git's internal list
    try {
      await execAsync("git worktree prune", {
        cwd: this.repoRoot,
        timeout: 10_000,
      });
    } catch {
      // Non-fatal
    }

    // Check for directories in worktree base that aren't tracked by git
    const baseDir = path.join(this.repoRoot, this.worktreeBase);
    try {
      const entries = await fs.readdir(baseDir, { withFileTypes: true });
      const activeWorktrees = await this.listActive();
      const activePaths = new Set(activeWorktrees.map((w) => w.path));

      for (const entry of entries) {
        if (!entry.isDirectory()) continue;
        const fullPath = path.join(baseDir, entry.name);

        // If this directory isn't tracked by git worktree, it's orphaned
        if (!activePaths.has(fullPath)) {
          try {
            await fs.rm(fullPath, { recursive: true, force: true });
            cleaned++;
          } catch {
            // Best effort
          }
        }
      }
    } catch {
      // Base directory may not exist yet — that's fine
    }

    return cleaned;
  }

  /**
   * Remove all managed worktrees
   */
  async cleanupAll(): Promise<void> {
    const active = await this.listActive();
    for (const worktree of active) {
      await this.cleanup(worktree.issueNumber, true);
    }
    await this.cleanupOrphans();
  }

  /**
   * Tear down the per-issue docker compose stack (project name `issue-NNN`)
   * before the worktree is removed. Soft-fail: docker may not be installed
   * on this host, the daemon may be down, or the project may not exist —
   * none of those should block worktree removal.
   *
   * Mirrors the Go `internal/dockercompose.TeardownProject` behaviour. See
   * Issue #3050.
   */
  private async teardownComposeStack(issueNumber: number): Promise<void> {
    const projectName = `issue-${issueNumber}`;

    // `docker version` is the cheapest check that proves both the CLI is
    // on PATH and the daemon is reachable.
    try {
      await execFileAsync("docker", ["version", "--format", "{{.Server.Version}}"], {
        timeout: 10_000,
      });
    } catch {
      // Docker missing or daemon down — nothing to tear down.
      return;
    }

    try {
      await execFileAsync(
        "docker",
        ["compose", "-p", projectName, "down", "-v", "--remove-orphans"],
        { cwd: this.repoRoot, timeout: 30_000 }
      );
    } catch (error) {
      console.warn(
        `[WorktreeManager] docker compose teardown for ${projectName} failed (continuing): ${
          error instanceof Error ? error.message : String(error)
        }`
      );
    }

    // Remove project-tagged images (best-effort). Filter strictly to the
    // `issue-NNN-` prefix so we never touch unrelated images.
    try {
      const { stdout } = await execFileAsync("docker", ["images", "--format", "{{.Repository}}"], {
        timeout: 10_000,
      });
      const images = stdout
        .split("\n")
        .map((s) => s.trim())
        .filter((s) => s !== "" && s !== "<none>" && s.startsWith(`${projectName}-`));
      const seen = new Set<string>();
      for (const img of images) {
        if (seen.has(img)) continue;
        seen.add(img);
        await execFileAsync("docker", ["rmi", "-f", img], { timeout: 10_000 }).catch(
          () => undefined
        );
      }
    } catch {
      // docker images failing is non-fatal — continue.
    }
  }

  /**
   * Run Flutter code generation in the worktree, if applicable.
   *
   * Non-fatal by design: if `flutter` isn't installed, if the project isn't
   * a Flutter project, or if codegen fails, logs a warning and continues.
   * The worktree may still be usable (non-Flutter, or Flutter without
   * generated files).
   *
   * Detection:
   * - Flutter project: `pubspec.yaml` at worktree root
   * - Codegen needed: any `.dart` file under `lib/` or `test/` contains a
   *   `part '*.g.dart';` directive
   *
   * When `.fvmrc` or `.fvm/` is present, prefers `fvm flutter` over bare
   * `flutter` so per-project Flutter versions (FVM) are honored.
   */
  private async runFlutterCodegen(
    worktreePath: string,
    issueNumber: number,
    timeout: number
  ): Promise<void> {
    // 1. Detect Flutter project
    const hasPubspec = await fs
      .access(path.join(worktreePath, "pubspec.yaml"))
      .then(() => true)
      .catch(() => false);
    if (!hasPubspec) return;

    // 2. Detect whether codegen is needed (any `.dart` file has `part '*.g.dart';`)
    let needsCodegen: boolean;
    try {
      const { stdout } = await execAsync(
        `grep -r --include="*.dart" -l "part '.*\\.g\\.dart'" lib test 2>/dev/null | head -1`,
        { cwd: worktreePath, timeout: 10_000 }
      );
      needsCodegen = stdout.trim().length > 0;
    } catch {
      // grep returns non-zero when no matches — treat as "no codegen needed"
      needsCodegen = false;
    }
    if (!needsCodegen) return;

    // 3. Prefer fvm flutter when .fvmrc or .fvm/ is present
    const [hasFvmrc, hasFvmDir] = await Promise.all([
      fs
        .access(path.join(worktreePath, ".fvmrc"))
        .then(() => true)
        .catch(() => false),
      fs
        .access(path.join(worktreePath, ".fvm"))
        .then(() => true)
        .catch(() => false),
    ]);
    const flutterCmd = hasFvmrc || hasFvmDir ? "fvm flutter" : "flutter";

    // 4. pub get then build_runner
    try {
      await execAsync(`${flutterCmd} pub get`, {
        cwd: worktreePath,
        timeout,
      });
      await execAsync(`${flutterCmd} pub run build_runner build --delete-conflicting-outputs`, {
        cwd: worktreePath,
        timeout,
      });
    } catch (error) {
      // Non-fatal: flutter may not be installed, project may not need codegen,
      // or build_runner may be absent. The pipeline agent runs in the user's
      // full shell and can install Flutter deps itself if required.

      console.warn(
        `[WorktreeManager] Flutter codegen failed in worktree for issue #${issueNumber} (non-fatal): ${
          error instanceof Error ? error.message : "Unknown error"
        }`
      );
    }
  }

  /**
   * Ensure .worktrees is in .gitignore
   */
  private async ensureGitignore(): Promise<void> {
    const gitignorePath = path.join(this.repoRoot, ".gitignore");
    const pattern = this.worktreeBase;

    try {
      const content = await fs.readFile(gitignorePath, "utf-8");
      if (content.includes(pattern)) return;

      // Append to .gitignore
      const newContent = content.endsWith("\n")
        ? `${content}${pattern}\n`
        : `${content}\n${pattern}\n`;
      await fs.writeFile(gitignorePath, newContent, "utf-8");
    } catch {
      // .gitignore may not exist — create it
      try {
        await fs.writeFile(gitignorePath, `${pattern}\n`, "utf-8");
      } catch {
        // Non-fatal
      }
    }
  }
}
