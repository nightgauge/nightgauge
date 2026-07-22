/**
 * EpicDashboard - Cross-repository epic progress tracking
 *
 * Provides epic progress aggregation across multiple repositories in a workspace.
 * Queries each repository's GitHub project for sub-issues and calculates
 * repository-grouped progress.
 *
 * @see Issue #330 - Epic Dashboard with Cross-Repo Progress
 * @see docs/MULTI_REPO_WORKSPACE.md for multi-repo workspace patterns
 */

import type { Repository } from "../../models/Repository";
import type { WorkspaceManager } from "../../services/WorkspaceManager";
import type { EpicEstimate, SubIssueEstimate } from "@nightgauge/sdk";
import { IpcClient } from "../../services/IpcClient";
import { getRepoIdentity } from "../../utils/configPathResolver";
import { resolveActiveRepository } from "../../utils/resolveActiveRepository";

/**
 * Progress data for a single repository within an epic
 */
export interface RepositoryProgress {
  /** Repository name */
  name: string;
  /** Repository path */
  path: string;
  /** Sub-issues in this repository */
  subIssues: SubIssueEstimate[];
  /** Total estimated minutes for this repo */
  totalMinutes: number;
  /** Remaining minutes for this repo (open issues only) */
  remainingMinutes: number;
  /** Completion percentage (0-100) */
  completionPercent: number;
  /** Count of closed issues */
  closedCount: number;
  /** Count of open issues */
  openCount: number;
  /** Query status for this repository */
  status: "success" | "error" | "no-data";
  /** Error message if status is 'error' */
  errorMessage?: string;
}

/**
 * Cross-repository epic progress data
 */
export interface CrossRepoEpicProgress {
  /** Epic issue number */
  epicNumber: number;
  /** Epic title */
  epicTitle: string;
  /** Progress breakdown by repository */
  repositories: RepositoryProgress[];
  /** Overall completion percentage across all repos */
  overallCompletionPercent: number;
  /** Total estimated minutes across all repos */
  totalMinutes: number;
  /** Remaining minutes across all repos */
  remainingMinutes: number;
  /** Integration buffer (15%) */
  integrationBufferMinutes: number;
  /** Confidence level based on historical data */
  confidence: "high" | "medium" | "low";
  /** Confidence explanation */
  confidenceDetail: string;
  /** Whether this is a cross-repo epic (multiple repos with issues) */
  isCrossRepo: boolean;
  /** When this data was fetched */
  fetchedAt: Date;
}

/**
 * Result of fetching epic numbers — distinguishes "no epics" from "gh CLI error" (Issue #639)
 */
export interface EpicFetchResult {
  epicNumbers: number[];
  error?: string;
}

/**
 * Cache entry for cross-repo epic progress
 */
interface CacheEntry {
  data: CrossRepoEpicProgress;
  fetchedAt: Date;
  ttl: number; // TTL in milliseconds
}

/**
 * Default cache TTL: 5 minutes
 */
const DEFAULT_CACHE_TTL_MS = 5 * 60 * 1000;

/**
 * EpicDashboard service for cross-repository epic progress tracking
 *
 * @example
 * ```typescript
 * const epicDashboard = new EpicDashboard(workspaceManager);
 *
 * // Get cross-repo progress for an epic
 * const progress = await epicDashboard.getCrossRepoProgress(322);
 *
 * // Check if it spans multiple repos
 * if (progress.isCrossRepo) {
 *   console.log(`Epic spans ${progress.repositories.length} repositories`);
 * }
 * ```
 */
export class EpicDashboard {
  private workspaceManager: WorkspaceManager;
  private cache: Map<number, CacheEntry> = new Map();
  private cacheTtl: number;

  constructor(workspaceManager: WorkspaceManager, cacheTtl?: number) {
    this.workspaceManager = workspaceManager;
    this.cacheTtl = cacheTtl ?? DEFAULT_CACHE_TTL_MS;
  }

  /**
   * Get cross-repository progress for an epic
   *
   * Queries all repositories in the workspace for sub-issues related to the epic.
   * Uses parallel queries with individual error handling for resilience.
   *
   * @param epicNumber - GitHub issue number of the epic
   * @param forceRefresh - Skip cache and query fresh data
   * @returns Cross-repo progress data
   */
  async getCrossRepoProgress(
    epicNumber: number,
    forceRefresh: boolean = false
  ): Promise<CrossRepoEpicProgress> {
    // Check cache first
    if (!forceRefresh) {
      const cached = this.getFromCache(epicNumber);
      if (cached) {
        return cached;
      }
    }

    // Fetch epic metadata from current repository
    const epicMeta = await this.fetchEpicMetadata(epicNumber);

    // Get all repositories in workspace
    const repositories = this.workspaceManager.getAllRepositories();

    // Query each repository in parallel
    const progressPromises = repositories.map((repo) =>
      this.queryRepositoryProgress(repo, epicNumber, epicMeta.subIssueNumbers)
    );

    const repoResults = await Promise.all(progressPromises);

    // Filter out repositories with no data
    const reposWithData = repoResults.filter(
      (r) => r.status !== "no-data" || r.subIssues.length > 0
    );

    // Calculate overall progress
    const totalMinutes = reposWithData.reduce((sum, r) => sum + r.totalMinutes, 0);
    const remainingMinutes = reposWithData.reduce((sum, r) => sum + r.remainingMinutes, 0);
    const integrationBuffer = Math.round(totalMinutes * 0.15);

    const completedMinutes = totalMinutes - remainingMinutes;
    const overallCompletionPercent =
      totalMinutes > 0 ? Math.round((completedMinutes / totalMinutes) * 100) : 0;

    // Determine confidence based on data quality
    const { confidence, confidenceDetail } = this.calculateConfidence(reposWithData);

    const result: CrossRepoEpicProgress = {
      epicNumber,
      epicTitle: epicMeta.title,
      repositories: reposWithData,
      overallCompletionPercent,
      totalMinutes: totalMinutes + integrationBuffer,
      remainingMinutes: remainingMinutes + Math.round(remainingMinutes * 0.15),
      integrationBufferMinutes: integrationBuffer,
      confidence,
      confidenceDetail,
      isCrossRepo: reposWithData.filter((r) => r.subIssues.length > 0).length > 1,
      fetchedAt: new Date(),
    };

    // Cache the result
    this.setCache(epicNumber, result);

    return result;
  }

  /**
   * Get all cross-repo epic progress for open epics
   *
   * Fetches progress for all epics with type:epic label in the current repository.
   *
   * @param forceRefresh - Skip cache for all epics
   * @returns Array of cross-repo progress data
   */
  async getAllCrossRepoProgress(forceRefresh: boolean = false): Promise<CrossRepoEpicProgress[]> {
    const currentRepo = resolveActiveRepository(this.workspaceManager);
    if (!currentRepo) {
      return [];
    }

    // Get all open epics from current repo (Issue #639: error differentiation)
    const fetchResult = await this.fetchOpenEpicNumbers(currentRepo.path);

    if (fetchResult.epicNumbers.length === 0) {
      return [];
    }

    // Query progress for each epic in parallel
    const progressPromises = fetchResult.epicNumbers.map((num) =>
      this.getCrossRepoProgress(num, forceRefresh).catch((error) => {
        console.warn(`Failed to get progress for epic #${num}:`, error);
        return null;
      })
    );

    const results = await Promise.all(progressPromises);

    // Filter out nulls (failed queries) and sort by remaining time
    return results
      .filter((r): r is CrossRepoEpicProgress => r !== null)
      .sort((a, b) => b.remainingMinutes - a.remainingMinutes);
  }

  /**
   * Invalidate cache for a specific epic
   */
  invalidateCache(epicNumber: number): void {
    this.cache.delete(epicNumber);
  }

  /**
   * Invalidate all cached data
   */
  invalidateAllCache(): void {
    this.cache.clear();
  }

  /**
   * Check if an epic has cross-repo sub-issues
   */
  async isCrossRepoEpic(epicNumber: number): Promise<boolean> {
    const progress = await this.getCrossRepoProgress(epicNumber);
    return progress.isCrossRepo;
  }

  // Private methods

  private getFromCache(epicNumber: number): CrossRepoEpicProgress | null {
    const entry = this.cache.get(epicNumber);
    if (!entry) {
      return null;
    }

    const now = Date.now();
    const age = now - entry.fetchedAt.getTime();

    if (age > entry.ttl) {
      this.cache.delete(epicNumber);
      return null;
    }

    return entry.data;
  }

  private setCache(epicNumber: number, data: CrossRepoEpicProgress): void {
    this.cache.set(epicNumber, {
      data,
      fetchedAt: new Date(),
      ttl: this.cacheTtl,
    });
  }

  private async fetchEpicMetadata(epicNumber: number): Promise<{
    title: string;
    subIssueNumbers: number[];
  }> {
    const currentRepo = resolveActiveRepository(this.workspaceManager);
    const cwd = currentRepo?.path ?? process.cwd();

    try {
      // Get owner/repo from config
      const identity = await getRepoIdentity(cwd);
      if (!identity) {
        throw new Error(`Could not determine repository identity for ${cwd}`);
      }

      // Query epic via IPC — returns IssueDetail with subIssues array
      const ipc = IpcClient.getInstance();
      const issue = await ipc.issueView(identity.owner, identity.repo, epicNumber);

      if (!issue) {
        throw new Error(`Epic #${epicNumber} not found`);
      }

      const subIssueNumbers = (issue.subIssues || [])
        .map((n) => n.number)
        .sort((a: number, b: number) => a - b);

      return {
        title: issue.title,
        subIssueNumbers,
      };
    } catch (error) {
      throw new Error(
        `Failed to fetch epic #${epicNumber}: ${error instanceof Error ? error.message : "Unknown error"}`,
        { cause: error }
      );
    }
  }

  private async queryRepositoryProgress(
    repo: Repository,
    epicNumber: number,
    subIssueNumbers: number[]
  ): Promise<RepositoryProgress> {
    const baseProgress: RepositoryProgress = {
      name: repo.name,
      path: repo.path,
      subIssues: [],
      totalMinutes: 0,
      remainingMinutes: 0,
      completionPercent: 0,
      closedCount: 0,
      openCount: 0,
      status: "no-data",
    };

    if (subIssueNumbers.length === 0) {
      return baseProgress;
    }

    try {
      // Query sub-issues that exist in this repository
      const subIssues = await this.querySubIssuesInRepo(repo.path, subIssueNumbers);

      if (subIssues.length === 0) {
        return baseProgress;
      }

      const totalMinutes = subIssues.reduce((sum, issue) => sum + issue.estimated_minutes, 0);
      const remainingMinutes = subIssues
        .filter((issue) => issue.status === "open")
        .reduce((sum, issue) => sum + issue.estimated_minutes, 0);

      const completedMinutes = totalMinutes - remainingMinutes;
      const completionPercent =
        totalMinutes > 0 ? Math.round((completedMinutes / totalMinutes) * 100) : 0;

      const closedCount = subIssues.filter((i) => i.status === "closed").length;
      const openCount = subIssues.filter((i) => i.status === "open").length;

      return {
        name: repo.name,
        path: repo.path,
        subIssues,
        totalMinutes,
        remainingMinutes,
        completionPercent,
        closedCount,
        openCount,
        status: "success",
      };
    } catch (error) {
      return {
        ...baseProgress,
        status: "error",
        errorMessage: error instanceof Error ? error.message : "Unknown error",
      };
    }
  }

  private async querySubIssuesInRepo(
    repoPath: string,
    issueNumbers: number[]
  ): Promise<SubIssueEstimate[]> {
    const identity = await getRepoIdentity(repoPath);
    if (!identity) {
      return [];
    }

    const ipc = IpcClient.getInstance();

    // Single batched IPC call instead of one issueView per number. The Go
    // side uses GetIssuesByNumbers, which sends one aliased GraphQL query.
    let issues: Awaited<ReturnType<typeof ipc.issueViewMany>>;
    try {
      issues = await ipc.issueViewMany(identity.owner, identity.repo, issueNumbers);
    } catch {
      // Whole-batch failure (auth, network, repo not found) — degrade
      // gracefully like the prior per-issue catch. Caller treats empty
      // as "no data" for this repo.
      return [];
    }

    return issues.map((issue) => {
      const sizeLabel = issue.labels
        ?.find((l: string) => l.startsWith("size:"))
        ?.replace("size:", "") as SubIssueEstimate["size"];

      return {
        number: issue.number,
        title: issue.title,
        size: sizeLabel || null,
        estimated_minutes: this.getEstimatedMinutes(sizeLabel),
        status: issue.state === "OPEN" ? "open" : "closed",
      };
    });
  }

  private getEstimatedMinutes(size: SubIssueEstimate["size"]): number {
    const defaults: Record<string, number> = {
      XS: 30,
      S: 120,
      M: 600,
      L: 1920,
      XL: 4800,
    };

    return size ? defaults[size] || 0 : 0;
  }

  private async fetchOpenEpicNumbers(repoPath: string): Promise<EpicFetchResult> {
    try {
      const identity = await getRepoIdentity(repoPath);
      if (!identity) {
        return {
          epicNumbers: [],
          error: `Could not determine repository identity for ${repoPath}`,
        };
      }

      const ipc = IpcClient.getInstance();
      const epics = await ipc.issueList(identity.owner, identity.repo, {
        labels: ["type:epic"],
      });
      return { epicNumbers: epics.map((e) => e.number) };
    } catch (error) {
      const errorMessage = error instanceof Error ? error.message : "Unknown error";
      console.warn("Failed to fetch open epic numbers:", errorMessage);
      return { epicNumbers: [], error: errorMessage };
    }
  }

  private calculateConfidence(repositories: RepositoryProgress[]): {
    confidence: "high" | "medium" | "low";
    confidenceDetail: string;
  } {
    const totalIssues = repositories.reduce((sum, r) => sum + r.subIssues.length, 0);
    const issuesWithSize = repositories.reduce(
      (sum, r) => sum + r.subIssues.filter((i) => i.size !== null).length,
      0
    );

    if (totalIssues === 0) {
      return {
        confidence: "low",
        confidenceDetail: "No sub-issues found",
      };
    }

    const sizeRatio = issuesWithSize / totalIssues;

    if (sizeRatio >= 0.9) {
      return {
        confidence: "high",
        confidenceDetail: `${issuesWithSize}/${totalIssues} issues have size labels`,
      };
    } else if (sizeRatio >= 0.6) {
      return {
        confidence: "medium",
        confidenceDetail: `${issuesWithSize}/${totalIssues} issues have size labels`,
      };
    } else {
      return {
        confidence: "low",
        confidenceDetail: `Only ${issuesWithSize}/${totalIssues} issues have size labels`,
      };
    }
  }
}
