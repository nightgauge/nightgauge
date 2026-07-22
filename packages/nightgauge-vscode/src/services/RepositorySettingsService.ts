/**
 * RepositorySettingsService — Detects and manages repository auto-merge setting.
 *
 * Wraps the `nightgauge repo settings` and `nightgauge repo disable-auto-merge`
 * Go binary commands with an in-memory cache to avoid repeated API calls.
 *
 * The pipeline's pr-merge stage requires exclusive control over PR merging.
 * When `allow_auto_merge` is enabled on the repository, PRs merge automatically
 * once CI passes, bypassing the pipeline's watch/resolve loop and recovery
 * mechanisms. This service detects that condition and provides a one-click fix.
 *
 * @see Issue #2720 — Detect and disable repo auto-merge
 * @see docs/GIT_WORKFLOW.md — Auto-Merge and Pipeline Control section
 */

import { execFile } from "child_process";
import { promisify } from "util";
import * as vscode from "vscode";
import type { Logger } from "../utils/logger";
import { BinaryResolver } from "./BinaryResolver";

const execFileAsync = promisify(execFile);

/** Cache TTL: 1 hour in milliseconds. Repo settings rarely change. */
const CACHE_TTL_MS = 60 * 60 * 1000;

interface CacheEntry {
  allowAutoMerge: boolean;
  fetchedAt: number;
}

/**
 * RepositorySettingsService detects and manages the `allow_auto_merge` setting
 * on GitHub repositories. Results are cached for CACHE_TTL_MS to avoid
 * repeated API calls during a VSCode session.
 */
export class RepositorySettingsService {
  private readonly cache = new Map<string, CacheEntry>();
  private binaryPath: string | null = null;

  /** Fired when auto-merge is detected on the active repository. */
  private readonly _onAutoMergeDetected = new vscode.EventEmitter<{
    owner: string;
    repo: string;
  }>();
  readonly onAutoMergeDetected = this._onAutoMergeDetected.event;

  constructor(
    private readonly logger: Logger,
    private readonly workspaceRoot: string
  ) {}

  /**
   * Checks whether the repository has auto-merge enabled.
   * Results are cached for 1 hour. Returns false on any error (fail-safe).
   */
  async detectAutoMerge(owner: string, repo: string): Promise<boolean> {
    const cacheKey = `${owner}/${repo}`;
    const cached = this.cache.get(cacheKey);
    if (cached && Date.now() - cached.fetchedAt < CACHE_TTL_MS) {
      return cached.allowAutoMerge;
    }

    try {
      const binary = await this.resolveBinary();
      if (!binary) {
        this.logger.debug(
          "[RepositorySettingsService] Go binary not found — skipping auto-merge detection"
        );
        return false;
      }

      const { stdout } = await execFileAsync(
        binary,
        ["repo", "settings", "--owner", owner, "--repo", repo, "--json"],
        { cwd: this.workspaceRoot }
      );

      const result = JSON.parse(stdout.trim()) as { allow_auto_merge: boolean };
      const allowAutoMerge = result.allow_auto_merge === true;

      this.cache.set(cacheKey, { allowAutoMerge, fetchedAt: Date.now() });

      if (allowAutoMerge) {
        this.logger.warn(
          `[RepositorySettingsService] auto-merge is enabled on ${owner}/${repo} — pipeline control is at risk`
        );
        this._onAutoMergeDetected.fire({ owner, repo });
      }

      return allowAutoMerge;
    } catch (error) {
      // Fail-safe: treat errors as "not auto-merge" so detection never blocks the pipeline.
      this.logger.debug(
        `[RepositorySettingsService] auto-merge detection failed (treating as false): ${
          error instanceof Error ? error.message : String(error)
        }`
      );
      return false;
    }
  }

  /**
   * Disables the allow_auto_merge setting on the repository via the Go binary.
   * Invalidates the cache after success so next detection call fetches fresh state.
   */
  async disableAutoMerge(owner: string, repo: string): Promise<void> {
    const binary = await this.resolveBinary();
    if (!binary) {
      throw new Error("nightgauge Go binary not found. Cannot disable auto-merge.");
    }

    const { stderr } = await execFileAsync(
      binary,
      ["repo", "disable-auto-merge", "--owner", owner, "--repo", repo, "--force"],
      { cwd: this.workspaceRoot }
    );

    if (stderr) {
      this.logger.debug(`[RepositorySettingsService] disable-auto-merge stderr: ${stderr}`);
    }

    // Invalidate cache so next detectAutoMerge call fetches the updated state.
    this.invalidateCache(owner, repo);
  }

  /**
   * Invalidates the cache entry for the given owner/repo pair.
   * Call after a successful disable to force re-detection.
   */
  invalidateCache(owner: string, repo: string): void {
    this.cache.delete(`${owner}/${repo}`);
  }

  /** Clears all cached entries. */
  clearCache(): void {
    this.cache.clear();
  }

  private async resolveBinary(): Promise<string | null> {
    if (this.binaryPath !== null) {
      return this.binaryPath;
    }

    const resolver = BinaryResolver.fromVSCode();
    const resolved = await resolver.resolve();
    this.binaryPath = resolved;
    return resolved;
  }

  dispose(): void {
    this._onAutoMergeDetected.dispose();
  }
}
