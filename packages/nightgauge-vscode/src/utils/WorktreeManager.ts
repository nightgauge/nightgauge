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
   * Remove a worktree and optionally delete the branch
   *
   * @param issueNumber - The issue number whose worktree to remove
   * @param deleteBranch - Whether to also delete the local branch (default: false)
   */
  async cleanup(issueNumber: number, deleteBranch?: boolean): Promise<void> {
    const worktreePath = this.getWorktreePath(issueNumber);

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
    } catch {
      // If git worktree remove fails, try manual cleanup
      try {
        await fs.rm(worktreePath, { recursive: true, force: true });
        await execAsync("git worktree prune", {
          cwd: this.repoRoot,
          timeout: 10_000,
        });
      } catch {
        // Best effort cleanup
      }
    }

    // Delete the branch if requested and worktree was cleaned up
    if (deleteBranch && branchName) {
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
