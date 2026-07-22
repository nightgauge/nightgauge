/**
 * ProjectIterationService - GitHub Project Iteration (Sprint) Management
 *
 * TypeScript replacement for sync-project-iteration.sh shell script.
 * Provides proper date handling with date-fns for reliable iteration resolution.
 *
 * @see Issue #132 - Rewrite sync-project-iteration.sh in TypeScript
 * @see Issue #433 - config.yaml (formerly nightgauge.yaml)
 * @see docs/ARCHITECTURE.md - Deterministic vs Probabilistic Architecture
 */

import { exec } from "child_process";
import { promisify } from "util";
import * as fs from "fs";
import * as path from "path";
import { parseISO, addDays, isWithinInterval, isAfter, startOfDay } from "date-fns";
import type {
  IterationTarget,
  Iteration,
  SyncResult,
  IterationConfig,
  GraphQLIterationField,
  GraphQLProjectField,
  GraphQLProjectItem,
  GraphQLPageInfo,
} from "./types/iteration";
import { resolveConfigPath, logDeprecationWarning } from "../utils/configPathResolver";
import { getGitHubUser } from "../utils/incrediConfig";
import { execFile } from "child_process";

const execAsync = promisify(exec);
const execFileAsync = promisify(execFile);

/** Default iteration field name */
const DEFAULT_ITERATION_FIELD_NAME = "Sprint";

/**
 * ProjectIterationService - Singleton service for iteration management
 *
 * Replaces sync-project-iteration.sh with proper TypeScript implementation
 * using date-fns for reliable date calculations.
 *
 * @example
 * ```typescript
 * const service = ProjectIterationService.getInstance('/path/to/workspace');
 *
 * // Assign current iteration
 * const result = await service.syncIteration(90, '@current');
 *
 * // Assign next iteration
 * const result = await service.syncIteration(90, '@next');
 *
 * // Clear iteration
 * const result = await service.syncIteration(90, 'none');
 *
 * // Get all iterations
 * const iterations = await service.getIterations();
 * ```
 */
export class ProjectIterationService {
  private static instance: ProjectIterationService | null = null;

  private workspaceRoot: string;
  private configCache: IterationConfig | null = null;
  private ownerCache: string | null = null;
  private repoNameCache: string | null = null;
  private projectGlobalIdCache: Map<number, string> = new Map();

  private ghTokenCache: string | null | undefined = undefined; // undefined = not yet resolved

  private constructor(workspaceRoot: string) {
    this.workspaceRoot = workspaceRoot;
  }

  /**
   * Resolve per-repo GH_TOKEN for gh CLI calls. Uses github_user from
   * .nightgauge/config.yaml when available (multi-identity support).
   */
  private async resolveGhEnv(): Promise<Record<string, string>> {
    if (this.ghTokenCache === undefined) {
      const githubUser = getGitHubUser(this.workspaceRoot);
      if (githubUser) {
        try {
          const { stdout } = await execFileAsync("gh", ["auth", "token", "--user", githubUser], {
            timeout: 5000,
          });
          this.ghTokenCache = stdout.trim() || null;
        } catch {
          this.ghTokenCache = null;
        }
      } else {
        this.ghTokenCache = null;
      }
    }
    if (this.ghTokenCache) {
      return { ...process.env, GH_TOKEN: this.ghTokenCache } as Record<string, string>;
    }
    return process.env as Record<string, string>;
  }

  /**
   * Run a gh CLI command with per-repo auth token injected.
   */
  private async ghExec(
    command: string,
    opts?: { cwd?: string; timeout?: number }
  ): Promise<{ stdout: string; stderr: string }> {
    if (this.ghTokenCache === undefined) {
      await this.resolveGhEnv();
    }
    const execOpts = this.ghTokenCache
      ? { ...opts, env: { ...process.env, GH_TOKEN: this.ghTokenCache } }
      : opts;
    const result = await execAsync(command, execOpts);
    return {
      stdout: String(result.stdout),
      stderr: String(result.stderr),
    };
  }

  /**
   * Get the singleton instance
   */
  static getInstance(workspaceRoot: string): ProjectIterationService {
    if (!ProjectIterationService.instance) {
      ProjectIterationService.instance = new ProjectIterationService(workspaceRoot);
    }
    return ProjectIterationService.instance;
  }

  /**
   * Reset the singleton (for testing)
   */
  static resetInstance(): void {
    ProjectIterationService.instance = null;
  }

  /**
   * Sync iteration for an issue
   *
   * @param issueNumber - GitHub issue number
   * @param target - Iteration target (@current, @next, none, or specific ID)
   * @returns SyncResult indicating success, skip, or error
   */
  async syncIteration(
    issueNumber: number,
    target: IterationTarget = "@current"
  ): Promise<SyncResult> {
    // Load configuration
    const config = await this.loadConfig();
    if (!config) {
      return {
        skipped: true,
        reason: "No project configured in .nightgauge/config.yaml",
      };
    }

    if (!config.sprintEnabled) {
      return {
        skipped: true,
        reason: "Sprint feature not enabled in .nightgauge/config.yaml",
      };
    }

    // Get repository info
    const owner = await this.getRepoOwner();
    const repoName = await this.getRepoName();

    if (!owner || !repoName) {
      return {
        skipped: true,
        reason: "Could not determine repository owner/name",
      };
    }

    // Get iteration field ID
    const fieldId = await this.getIterationFieldId(config.projectNumber, owner, config.fieldName);

    if (!fieldId) {
      return {
        skipped: true,
        reason: `No iteration field '${config.fieldName}' in project #${config.projectNumber}`,
      };
    }

    // Find project item for the issue
    const itemId = await this.findProjectItem(issueNumber, config.projectNumber, owner, repoName);

    if (!itemId) {
      return {
        skipped: true,
        reason: `Issue #${issueNumber} not in project #${config.projectNumber}`,
      };
    }

    // Handle "none" to clear iteration
    if (target === "none") {
      const cleared = await this.clearIteration(itemId, fieldId, config.projectNumber);
      if (cleared) {
        return {
          success: true,
          issue: issueNumber,
          project: config.projectNumber,
          item_id: itemId,
          iteration: null,
          action: "cleared",
        };
      }
      return {
        skipped: true,
        reason: "Failed to clear iteration",
      };
    }

    // Resolve iteration ID
    const iterationId = await this.resolveIterationId(
      config.projectNumber,
      owner,
      config.fieldName,
      target
    );

    if (!iterationId) {
      return {
        skipped: true,
        reason: `No ${target} iteration found`,
      };
    }

    // Get iteration title for the result
    const iterationTitle = await this.getIterationTitle(
      config.projectNumber,
      owner,
      config.fieldName,
      iterationId
    );

    // Set iteration on project item
    const success = await this.setIteration(itemId, fieldId, iterationId, config.projectNumber);

    if (success) {
      return {
        success: true,
        issue: issueNumber,
        project: config.projectNumber,
        item_id: itemId,
        iteration: {
          id: iterationId,
          title: iterationTitle,
        },
        action: "assigned",
      };
    }

    return {
      skipped: true,
      reason: "Failed to set iteration",
    };
  }

  /**
   * Get all iterations for a project
   *
   * @param fieldName - Optional field name override
   * @returns Array of iterations or empty array on error
   */
  async getIterations(fieldName?: string): Promise<Iteration[]> {
    const config = await this.loadConfig();
    if (!config || !config.sprintEnabled) {
      return [];
    }

    const owner = await this.getRepoOwner();
    if (!owner) {
      return [];
    }

    const effectiveFieldName = fieldName ?? config.fieldName;
    const projectGlobalId = await this.getProjectGlobalId(config.projectNumber, owner);

    if (!projectGlobalId) {
      return [];
    }

    return await this.fetchIterations(projectGlobalId, effectiveFieldName);
  }

  // =========================================================================
  // Configuration Loading
  // =========================================================================

  /**
   * Load configuration from nightgauge config file
   */
  private async loadConfig(): Promise<IterationConfig | null> {
    if (this.configCache) {
      return this.configCache;
    }

    // Resolve config path with fallback to legacy
    const pathResult = await resolveConfigPath(this.workspaceRoot);

    if (!pathResult.exists) {
      return null;
    }

    // Log deprecation warning if using legacy path
    if (pathResult.isLegacy) {
      logDeprecationWarning(pathResult.path);
    }

    try {
      const content = await fs.promises.readFile(pathResult.path, "utf-8");

      // Simple YAML parsing for our needs
      const projectNumberMatch = content.match(/^\s*number:\s*(\d+)/m);
      const sprintEnabledMatch = content.match(/^\s*enabled:\s*(true|false)/m);
      const fieldNameMatch = content.match(/^\s*field_name:\s*["']?([^"'\n]+)["']?/m);

      if (!projectNumberMatch) {
        return null;
      }

      this.configCache = {
        projectNumber: parseInt(projectNumberMatch[1], 10),
        sprintEnabled: sprintEnabledMatch?.[1] === "true",
        fieldName: fieldNameMatch?.[1]?.trim() ?? DEFAULT_ITERATION_FIELD_NAME,
      };

      return this.configCache;
    } catch {
      return null;
    }
  }

  /**
   * Get repository owner via gh CLI
   */
  private async getRepoOwner(): Promise<string | null> {
    if (this.ownerCache) {
      return this.ownerCache;
    }

    try {
      const result = await this.ghExec("gh repo view --json owner -q .owner.login", {
        cwd: this.workspaceRoot,
      });
      this.ownerCache = result.stdout.trim() || null;
      return this.ownerCache;
    } catch {
      return null;
    }
  }

  /**
   * Get repository name via gh CLI
   */
  private async getRepoName(): Promise<string | null> {
    if (this.repoNameCache) {
      return this.repoNameCache;
    }

    try {
      const result = await this.ghExec("gh repo view --json name -q .name", {
        cwd: this.workspaceRoot,
      });
      this.repoNameCache = result.stdout.trim() || null;
      return this.repoNameCache;
    } catch {
      return null;
    }
  }

  // =========================================================================
  // Project/Field Resolution
  // =========================================================================

  /**
   * Get project global ID for GraphQL queries
   */
  private async getProjectGlobalId(projectNumber: number, owner: string): Promise<string | null> {
    const cacheKey = projectNumber;
    if (this.projectGlobalIdCache.has(cacheKey)) {
      return this.projectGlobalIdCache.get(cacheKey)!;
    }

    try {
      const result = await this.ghExec(`gh project list --owner "${owner}" --format json`, {
        cwd: this.workspaceRoot,
      });

      const data = JSON.parse(result.stdout);
      const project = data.projects?.find(
        (p: { number: number; id: string }) => p.number === projectNumber
      );

      if (project?.id) {
        this.projectGlobalIdCache.set(cacheKey, project.id);
        return project.id;
      }

      return null;
    } catch {
      return null;
    }
  }

  /**
   * Get iteration field ID from project
   */
  private async getIterationFieldId(
    projectNumber: number,
    owner: string,
    fieldName: string
  ): Promise<string | null> {
    try {
      const result = await this.ghExec(
        `gh project field-list ${projectNumber} --owner "${owner}" --format json`,
        { cwd: this.workspaceRoot }
      );

      const data = JSON.parse(result.stdout);
      const field = data.fields?.find(
        (f: GraphQLProjectField) => f.name === fieldName && f.type === "ITERATION"
      );

      return field?.id ?? null;
    } catch {
      return null;
    }
  }

  // =========================================================================
  // Iteration Resolution
  // =========================================================================

  /**
   * Resolve iteration target to actual iteration ID
   *
   * Uses date-fns for reliable date calculations.
   */
  private async resolveIterationId(
    projectNumber: number,
    owner: string,
    fieldName: string,
    target: IterationTarget
  ): Promise<string | null> {
    // If specific ID, return as-is
    if (target !== "@current" && target !== "@next" && target !== "none") {
      return target;
    }

    const projectGlobalId = await this.getProjectGlobalId(projectNumber, owner);
    if (!projectGlobalId) {
      return null;
    }

    const iterations = await this.fetchIterations(projectGlobalId, fieldName);
    if (iterations.length === 0) {
      return null;
    }

    const today = startOfDay(new Date());

    if (target === "@current") {
      return this.findCurrentIteration(iterations, today);
    }

    if (target === "@next") {
      return this.findNextIteration(iterations, today);
    }

    return null;
  }

  /**
   * Find iteration containing today's date
   *
   * An iteration is current if: startDate <= today < startDate + duration
   */
  private findCurrentIteration(iterations: Iteration[], today: Date): string | null {
    for (const iteration of iterations) {
      if (this.isCurrentIteration(iteration, today)) {
        return iteration.id;
      }
    }
    return null;
  }

  /**
   * Find first iteration starting after today
   */
  private findNextIteration(iterations: Iteration[], today: Date): string | null {
    const future = iterations
      .filter((iteration) => this.isNextIteration(iteration, today))
      .sort((a, b) => a.startDate.localeCompare(b.startDate));

    return future[0]?.id ?? null;
  }

  /**
   * Check if iteration contains today's date
   *
   * Uses date-fns for reliable date interval checking.
   * For a 14-day iteration starting Feb 3, days 1-14 are:
   * Feb 3, 4, 5, ..., 16 (inclusive on both ends)
   */
  isCurrentIteration(iteration: Iteration, today: Date): boolean {
    const start = startOfDay(parseISO(iteration.startDate));
    // Last day is start + (duration - 1) days
    // For 14-day iteration starting Feb 3: Feb 3 + 13 = Feb 16
    const lastDay = addDays(start, iteration.duration - 1);
    const todayNormalized = startOfDay(today);

    // Check if today is >= start AND <= lastDay
    return (
      (todayNormalized >= start || todayNormalized.getTime() === start.getTime()) &&
      (todayNormalized <= lastDay || todayNormalized.getTime() === lastDay.getTime())
    );
  }

  /**
   * Check if iteration starts after today
   */
  isNextIteration(iteration: Iteration, today: Date): boolean {
    const start = startOfDay(parseISO(iteration.startDate));
    const todayNormalized = startOfDay(today);
    return isAfter(start, todayNormalized);
  }

  /**
   * Fetch iterations from GitHub GraphQL API
   */
  private async fetchIterations(projectGlobalId: string, fieldName: string): Promise<Iteration[]> {
    const query = `
      query($projectId: ID!, $fieldName: String!) {
        node(id: $projectId) {
          ... on ProjectV2 {
            field(name: $fieldName) {
              ... on ProjectV2IterationField {
                id
                configuration {
                  iterations {
                    id
                    title
                    startDate
                    duration
                  }
                }
              }
            }
          }
        }
      }
    `;

    try {
      const escapedQuery = query.replace(/\n/g, " ").replace(/'/g, "\\'");
      const result = await this.ghExec(
        `gh api graphql -f query='${escapedQuery}' -f projectId='${projectGlobalId}' -f fieldName='${fieldName}'`,
        { cwd: this.workspaceRoot }
      );

      const data = JSON.parse(result.stdout);
      const field = data.data?.node?.field as GraphQLIterationField | undefined;

      return field?.configuration?.iterations ?? [];
    } catch {
      return [];
    }
  }

  /**
   * Get iteration title by ID
   */
  private async getIterationTitle(
    projectNumber: number,
    owner: string,
    fieldName: string,
    iterationId: string
  ): Promise<string> {
    const projectGlobalId = await this.getProjectGlobalId(projectNumber, owner);
    if (!projectGlobalId) {
      return "Unknown";
    }

    const iterations = await this.fetchIterations(projectGlobalId, fieldName);
    const iteration = iterations.find((i) => i.id === iterationId);

    return iteration?.title ?? "Unknown";
  }

  // =========================================================================
  // Project Item Resolution
  // =========================================================================

  /**
   * Find project item ID for an issue with pagination
   */
  private async findProjectItem(
    issueNumber: number,
    projectNumber: number,
    owner: string,
    repoName: string
  ): Promise<string | null> {
    const projectGlobalId = await this.getProjectGlobalId(projectNumber, owner);
    if (!projectGlobalId) {
      return null;
    }

    const fullRepo = `${owner}/${repoName}`;
    let cursor: string | null = null;

    do {
      const result = await this.fetchProjectItemsPage(projectGlobalId, cursor);
      if (!result) {
        break;
      }

      // Search for our issue in this page
      for (const item of result.items) {
        if (
          item.content?.number === issueNumber &&
          item.content?.repository?.nameWithOwner === fullRepo
        ) {
          return item.id;
        }
      }

      cursor = result.pageInfo.hasNextPage ? result.pageInfo.endCursor : null;
    } while (cursor !== null);

    return null;
  }

  /**
   * Fetch a page of project items
   */
  private async fetchProjectItemsPage(
    projectGlobalId: string,
    cursor: string | null
  ): Promise<{
    items: GraphQLProjectItem[];
    pageInfo: GraphQLPageInfo;
  } | null> {
    const query = `
      query($projectId: ID!, $cursor: String) {
        node(id: $projectId) {
          ... on ProjectV2 {
            items(first: 100, after: $cursor) {
              pageInfo {
                hasNextPage
                endCursor
              }
              nodes {
                id
                content {
                  ... on Issue {
                    number
                    repository {
                      nameWithOwner
                    }
                  }
                }
              }
            }
          }
        }
      }
    `;

    try {
      const escapedQuery = query.replace(/\n/g, " ").replace(/'/g, "\\'");
      const cursorArg = cursor ? `-f cursor='${cursor}'` : "";

      const result = await this.ghExec(
        `gh api graphql -f query='${escapedQuery}' -f projectId='${projectGlobalId}' ${cursorArg}`,
        { cwd: this.workspaceRoot }
      );

      const data = JSON.parse(result.stdout);
      const items = data.data?.node?.items;

      if (!items) {
        return null;
      }

      return {
        items: items.nodes as GraphQLProjectItem[],
        pageInfo: items.pageInfo as GraphQLPageInfo,
      };
    } catch {
      return null;
    }
  }

  // =========================================================================
  // Iteration Assignment
  // =========================================================================

  /**
   * Set iteration on a project item
   */
  private async setIteration(
    itemId: string,
    fieldId: string,
    iterationId: string,
    projectNumber: number
  ): Promise<boolean> {
    const projectGlobalId = await this.getProjectGlobalId(
      projectNumber,
      (await this.getRepoOwner()) ?? ""
    );

    if (!projectGlobalId) {
      return false;
    }

    try {
      await this.ghExec(
        `gh project item-edit --id "${itemId}" --project-id "${projectGlobalId}" --field-id "${fieldId}" --iteration-id "${iterationId}"`,
        { cwd: this.workspaceRoot }
      );
      return true;
    } catch {
      return false;
    }
  }

  /**
   * Clear iteration on a project item
   */
  private async clearIteration(
    itemId: string,
    fieldId: string,
    projectNumber: number
  ): Promise<boolean> {
    const projectGlobalId = await this.getProjectGlobalId(
      projectNumber,
      (await this.getRepoOwner()) ?? ""
    );

    if (!projectGlobalId) {
      return false;
    }

    try {
      await this.ghExec(
        `gh project item-edit --id "${itemId}" --project-id "${projectGlobalId}" --field-id "${fieldId}" --clear`,
        { cwd: this.workspaceRoot }
      );
      return true;
    } catch {
      return false;
    }
  }

  /**
   * Clear all caches (for testing or when config changes)
   */
  clearCaches(): void {
    this.configCache = null;
    this.ownerCache = null;
    this.repoNameCache = null;
    this.projectGlobalIdCache.clear();
  }
}
