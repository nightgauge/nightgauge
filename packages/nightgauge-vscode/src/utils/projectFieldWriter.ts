/**
 * GitHub Project v2 Field Writer
 *
 * Centralized TypeScript utility for reading and writing GitHub Project v2
 * field values via GraphQL. Replaces shell script `gh project item-edit` calls
 * with native TypeScript GraphQL mutations.
 *
 * Used by HeadlessOrchestrator and other services to update project board
 * status directly, without going through label-based sync scripts.
 *
 * @see Issue #1713 - Add GraphQL project field write utility
 * @see Issue #1711 - Epic: Eliminate Label-Field Bidirectional Sync
 */

import { exec } from "child_process";
import { promisify } from "util";
import * as fs from "fs";
import * as path from "path";
import * as yaml from "yaml";
import { resolveConfigPath } from "./configPathResolver";
import type { Logger } from "./logger";

const execAsync = promisify(exec);

// ============================================================================
// Public Types
// ============================================================================

/** Valid status values on the project board */
export type ProjectStatusValue = "Backlog" | "Ready" | "In progress" | "In review" | "Done";

/** Result of a project field operation */
export interface ProjectFieldResult {
  success: boolean;
  error?: string;
}

// ============================================================================
// Internal Types
// ============================================================================

/** Cached project configuration loaded from local config files */
interface ProjectFieldConfig {
  owner: string;
  repo: string;
  projectId: string;
  fields: {
    status: { id: string; options: Record<string, string> };
    priority: { id: string; options: Record<string, string> };
    size: { id: string; options: Record<string, string> };
  };
}

/** A single project configuration for multi-project support */
interface MultiProjectConfig extends ProjectFieldConfig {
  name?: string;
  isDefault: boolean;
}

// ============================================================================
// Config Cache (field IDs don't change between calls)
// ============================================================================

let cachedConfigs: MultiProjectConfig[] | null = null;
let cachedConfigCwd: string | null = null;

/**
 * Project item ID cache — keyed by `${projectId}|${owner}/${repo}#${number}`.
 * Project items are immutable for the life of a board membership, so caching
 * is safe for the process lifetime. This avoids re-fetching the same
 * (issue → projectItem) mapping on every stage transition.
 *
 * @see Issue #2866 — GraphQL rate-limit exhaustion from per-transition lookups
 */
const projectItemIdCache = new Map<string, string>();

function projectItemCacheKey(
  projectId: string,
  owner: string,
  repo: string,
  number: number
): string {
  return `${projectId}|${owner}/${repo}#${number}`;
}

/**
 * Clear the cached configuration. Call when workspace changes.
 */
export function clearConfigCache(): void {
  cachedConfigs = null;
  cachedConfigCwd = null;
  projectItemIdCache.clear();
}

/**
 * Clear only the project item ID cache. Useful when a board operation makes
 * the cached mapping suspect (e.g. an item was removed from the board).
 */
export function clearProjectItemIdCache(): void {
  projectItemIdCache.clear();
}

// ============================================================================
// Config Loading (local files only — zero API calls)
// ============================================================================

/**
 * Map config.yaml option keys to title-case runtime keys.
 * Supports both kebab-case (in-progress) and snake_case (in_progress).
 */
const STATUS_KEY_MAP: Record<string, string> = {
  backlog: "Backlog",
  ready: "Ready",
  "in-progress": "In progress",
  in_progress: "In progress",
  "in-review": "In review",
  in_review: "In review",
  done: "Done",
};

const PRIORITY_KEY_MAP: Record<string, string> = {
  p0: "P0",
  p1: "P1",
  p2: "P2",
};

const SIZE_KEY_MAP: Record<string, string> = {
  xs: "XS",
  s: "S",
  m: "M",
  l: "L",
  xl: "XL",
};

/** Remap lowercase option keys to their title-case equivalents */
function remapOptions(
  options: Record<string, string>,
  keyMap: Record<string, string>
): Record<string, string> {
  const result: Record<string, string> = {};
  for (const [key, value] of Object.entries(options)) {
    result[keyMap[key] ?? key] = value;
  }
  return result;
}

/**
 * Load field mappings from `.nightgauge/config.yaml`.
 *
 * Supports three layouts for backward compatibility:
 * - `project.fields.status` (canonical nested, used by newer repos)
 * - `fields.status` (legacy top-level nested, used by older repos)
 * - flat: `project.{status,priority,size}_field_id` + `project.field_options`
 *   (used by configs generated in the flat format; valid per
 *   `config/schema.ts` `ProjectEntrySchema`)
 *
 * Option keys are remapped from lowercase (kebab or snake_case) to
 * title-case for the GraphQL API (e.g. "in-progress" → "In progress").
 *
 * Returns null if config.yaml is missing or lacks field data.
 */
async function loadFieldMappings(cwd: string): Promise<ProjectFieldConfig["fields"] | null> {
  try {
    const configResult = await resolveConfigPath(cwd);
    if (!configResult.exists) return null;

    const configRaw = await fs.promises.readFile(configResult.path, "utf-8");
    const config = yaml.parse(configRaw);
    const project = config?.project;

    // Nested layout (canonical): project.fields, or legacy top-level fields.
    const fields = project?.fields || config?.fields;
    if (fields?.status?.id && fields?.status?.options && fields?.priority?.id && fields?.size?.id) {
      return {
        status: {
          id: fields.status.id,
          options: remapOptions(fields.status.options, STATUS_KEY_MAP),
        },
        priority: {
          id: fields.priority.id,
          options: remapOptions(fields.priority.options ?? {}, PRIORITY_KEY_MAP),
        },
        size: {
          id: fields.size.id,
          options: remapOptions(fields.size.options ?? {}, SIZE_KEY_MAP),
        },
      };
    }

    // Flat layout: project.{status,priority,size}_field_id + project.field_options.
    if (project?.status_field_id && project?.priority_field_id && project?.size_field_id) {
      const opts = project.field_options ?? {};
      return {
        status: {
          id: project.status_field_id,
          options: remapOptions(opts.status ?? {}, STATUS_KEY_MAP),
        },
        priority: {
          id: project.priority_field_id,
          options: remapOptions(opts.priority ?? {}, PRIORITY_KEY_MAP),
        },
        size: { id: project.size_field_id, options: remapOptions(opts.size ?? {}, SIZE_KEY_MAP) },
      };
    }

    return null;
  } catch {
    return null;
  }
}

/**
 * Load project configurations from config.yaml.
 *
 * All configuration comes from a single source: `.nightgauge/config.yaml`.
 * Supports both single-project (`project:`) and multi-project (`projects:`)
 * formats. Field IDs, project ID, and owner are all read from config.yaml.
 *
 * Results are cached — field IDs don't change between calls.
 */
async function loadProjectConfigs(cwd: string): Promise<MultiProjectConfig[] | null> {
  // Return cached if same workspace
  if (cachedConfigs && cachedConfigCwd === cwd) {
    return cachedConfigs;
  }

  const fields = await loadFieldMappings(cwd);
  if (!fields) {
    return null;
  }

  const configResult = await resolveConfigPath(cwd);
  if (!configResult.exists) {
    return null;
  }

  try {
    const configRaw = await fs.promises.readFile(configResult.path, "utf-8");
    const config = yaml.parse(configRaw);

    const configs: MultiProjectConfig[] = [];

    // Top-level owner (flat format used by all repos)
    const topOwner = config?.owner;

    if (config?.projects && Array.isArray(config.projects)) {
      // Multi-project mode
      for (const proj of config.projects) {
        const owner = proj.owner || config?.project?.owner || topOwner;
        const repo = proj.repo || config?.project?.repo || config?.repo;
        const projectId = proj.id || config?.project?.id;

        if (!owner || !repo || !projectId) continue;

        configs.push({
          name: proj.name,
          owner: String(owner),
          repo: String(repo),
          projectId: String(projectId),
          fields,
          isDefault: proj.default === true,
        });
      }
    } else if (config?.project) {
      // Single-project mode
      const owner = config.project.owner || topOwner;
      const repo = config.project.repo || config?.repo;
      const projectId = config.project.id;

      if (owner && projectId) {
        configs.push({
          name: undefined,
          owner: String(owner),
          repo: repo ? String(repo) : "nightgauge",
          projectId: String(projectId),
          fields,
          isDefault: true,
        });
      }
    }

    if (configs.length === 0) {
      return null;
    }

    // Cache for subsequent calls
    cachedConfigs = configs;
    cachedConfigCwd = cwd;
    return configs;
  } catch {
    return null;
  }
}

// ============================================================================
// GraphQL Execution (same pattern as githubStatusSync.ts)
// ============================================================================

/**
 * Execute a GraphQL query/mutation via `gh api graphql --input -`.
 *
 * Passes the full request body as JSON via stdin to avoid shell escaping issues.
 */
async function executeGraphQL(
  query: string,
  variables: Record<string, unknown>,
  cwd: string
): Promise<Record<string, unknown>> {
  const body = JSON.stringify({ query, variables });
  const shellSafe = body.replace(/'/g, "'\\''");
  const { stdout } = await execAsync(`printf '%s' '${shellSafe}' | gh api graphql --input -`, {
    cwd,
  });
  return JSON.parse(stdout);
}

type RepositoryNumberNode = Record<string, unknown>;
type RepositoryNumberKind = "issue" | "pullRequest";
type ProjectItemFieldValueNode = {
  name?: string | null;
  field?: { name?: string | null } | null;
};
type ProjectItemNode = {
  id?: string;
  project?: { id?: string } | null;
  fieldValues?: { nodes?: ProjectItemFieldValueNode[] } | null;
};

/**
 * Resolve a GitHub number by trying the Issue path first, then PullRequest.
 *
 * GitHub returns a top-level GraphQL error when a query asks for
 * `pullRequest(number: N)` and `N` is only an issue (and vice versa). Keep the
 * lookups separate so issue-only pipeline stages do not spam the output log
 * with non-actionable warnings.
 */
async function queryRepositoryNumberNode(
  owner: string,
  repo: string,
  number: number,
  selectionSet: string,
  cwd: string
): Promise<{ kind: RepositoryNumberKind; node: RepositoryNumberNode } | null> {
  for (const kind of ["issue", "pullRequest"] as const) {
    const result = await executeGraphQL(
      `
        query($owner: String!, $repo: String!, $number: Int!) {
          repository(owner: $owner, name: $repo) {
            ${kind}(number: $number) {
              ${selectionSet}
            }
          }
        }
      `,
      { owner, repo, number },
      cwd
    );

    const repoNode = result?.data as { repository?: Record<string, unknown> } | undefined;
    const node = repoNode?.repository?.[kind];
    if (node && typeof node === "object") {
      return { kind, node: node as RepositoryNumberNode };
    }
  }

  return null;
}

// ============================================================================
// Project Item Resolution
// ============================================================================

/**
 * Resolve the (owner, repo) pair to use for an issue/PR lookup.
 *
 * `repoNameWithOwner` overrides the config default — required for cross-repo
 * pipelines where the issue lives in a different repo than the workspace's
 * primary one (e.g. an `acme-platform` issue resolved from the
 * `nightgauge` workspace). When the override is missing or malformed,
 * fall back to config so single-repo callers keep working unchanged.
 *
 * @see Issue #2867 — cross-repo issue lookups must not hardcode the home repo
 */
function resolveOwnerRepo(
  config: ProjectFieldConfig,
  repoNameWithOwner?: string
): { owner: string; repo: string } {
  if (repoNameWithOwner && repoNameWithOwner.includes("/")) {
    const [owner, repo] = repoNameWithOwner.split("/", 2);
    if (owner && repo) return { owner, repo };
  }
  return { owner: config.owner, repo: config.repo };
}

/**
 * Find the project item ID for a given issue/PR.
 *
 * Uses a single targeted GraphQL query against the issue's `projectItems`
 * connection (capped at 20 — issues belong to at most a handful of boards).
 * This replaces the old pagination-everything-on-the-board approach which
 * exhausted the GraphQL rate limit on boards with hundreds of items.
 *
 * Resolved IDs are cached per (project, owner/repo, number) for the process
 * lifetime; subsequent calls for the same issue cost zero API points.
 *
 * @see Issue #2866 — server-side filter / direct lookup, no full-board scans
 */
async function findProjectItem(
  config: ProjectFieldConfig,
  issueNumber: number,
  cwd: string,
  repoNameWithOwner?: string
): Promise<string | null> {
  const { owner, repo } = resolveOwnerRepo(config, repoNameWithOwner);

  const cacheKey = projectItemCacheKey(config.projectId, owner, repo, issueNumber);
  const cached = projectItemIdCache.get(cacheKey);
  if (cached) {
    return cached;
  }

  const resolved = await queryRepositoryNumberNode(
    owner,
    repo,
    issueNumber,
    "projectItems(first: 20) { nodes { id project { id } } }",
    cwd
  );
  const items =
    (resolved?.node.projectItems as { nodes?: ProjectItemNode[] } | undefined)?.nodes ?? [];

  for (const item of items) {
    if (item?.project?.id === config.projectId && item?.id) {
      projectItemIdCache.set(cacheKey, item.id);
      return item.id;
    }
  }

  return null;
}

/**
 * Get the content node ID for a given number — either an Issue or a PullRequest.
 *
 * GitHub numbers Issues and PullRequests from the same sequence, but they are
 * distinct GraphQL node types. Look up the Issue first, then PullRequest.
 * Both Issue and PullRequest implement
 * ProjectV2Owner, so the returned node ID is usable with addProjectV2ItemById
 * and updateProjectV2ItemFieldValue interchangeably.
 */
async function getContentNodeId(
  config: ProjectFieldConfig,
  issueNumber: number,
  cwd: string,
  repoNameWithOwner?: string
): Promise<string | null> {
  const { owner, repo } = resolveOwnerRepo(config, repoNameWithOwner);
  const resolved = await queryRepositoryNumberNode(owner, repo, issueNumber, "id", cwd);
  return typeof resolved?.node.id === "string" ? resolved.node.id : null;
}

// ============================================================================
// Public API
// ============================================================================

/**
 * Ensure an issue is on the project board. If not present, adds it.
 *
 * @param issueNumber - The GitHub issue number
 * @param cwd - The workspace root directory
 * @param logger - Logger instance for structured logging
 * @returns The project item ID, or null on failure
 */
export async function ensureIssueOnProject(
  issueNumber: number,
  cwd: string,
  logger: Logger,
  repoNameWithOwner?: string
): Promise<string | null> {
  if (!Number.isInteger(issueNumber) || issueNumber <= 0) {
    logger.error("Invalid issue number", { issueNumber } as unknown as Error);
    return null;
  }

  const configs = await loadProjectConfigs(cwd);
  if (!configs || configs.length === 0) {
    logger.warn("No project configuration found", { cwd });
    return null;
  }

  // Use default project (or first)
  const config = configs.find((c) => c.isDefault) || configs[0];

  // Check if already on the project (single targeted query, cached on hit)
  let itemId = await findProjectItem(config, issueNumber, cwd, repoNameWithOwner);
  if (itemId) {
    logger.debug("Issue already on project board", { issueNumber, itemId });
    return itemId;
  }

  // Get the issue's node ID
  const issueNodeId = await getContentNodeId(config, issueNumber, cwd, repoNameWithOwner);
  if (!issueNodeId) {
    logger.error("Could not find issue node ID", {
      issueNumber,
    } as unknown as Error);
    return null;
  }

  // Add to project
  const mutation = `
    mutation($projectId: ID!, $contentId: ID!) {
      addProjectV2ItemById(input: {projectId: $projectId, contentId: $contentId}) {
        item { id }
      }
    }
  `;

  try {
    const result = await executeGraphQL(
      mutation,
      { projectId: config.projectId, contentId: issueNodeId },
      cwd
    );
    // eslint-disable-next-line @typescript-eslint/no-explicit-any
    itemId = (result as any)?.data?.addProjectV2ItemById?.item?.id ?? null;

    if (itemId) {
      const { owner, repo } = resolveOwnerRepo(config, repoNameWithOwner);
      projectItemIdCache.set(
        projectItemCacheKey(config.projectId, owner, repo, issueNumber),
        itemId
      );
      logger.info("Added issue to project board", { issueNumber, itemId });
    } else {
      logger.warn("addProjectV2ItemById returned no item ID", { issueNumber });
    }

    return itemId;
  } catch (error) {
    logger.error(
      "Failed to add issue to project",
      error instanceof Error ? error : new Error(String(error))
    );
    return null;
  }
}

/**
 * Get the current Status field value for an issue on the project board.
 *
 * @param issueNumber - The GitHub issue number
 * @param cwd - The workspace root directory
 * @param logger - Logger instance for structured logging
 * @returns The status string (e.g. "Ready", "In progress") or null
 */
export async function getProjectItemStatus(
  issueNumber: number,
  cwd: string,
  logger: Logger,
  repoNameWithOwner?: string
): Promise<string | null> {
  if (!Number.isInteger(issueNumber) || issueNumber <= 0) {
    return null;
  }

  const configs = await loadProjectConfigs(cwd);
  if (!configs || configs.length === 0) {
    return null;
  }

  const config = configs.find((c) => c.isDefault) || configs[0];
  const { owner, repo } = resolveOwnerRepo(config, repoNameWithOwner);

  try {
    const resolved = await queryRepositoryNumberNode(
      owner,
      repo,
      issueNumber,
      `projectItems(first: 20) {
        nodes {
          id
          project { id }
          fieldValues(first: 20) {
            nodes {
              ... on ProjectV2ItemFieldSingleSelectValue {
                name
                field { ... on ProjectV2SingleSelectField { name } }
              }
            }
          }
        }
      }`,
      cwd
    );
    const items =
      (resolved?.node.projectItems as { nodes?: ProjectItemNode[] } | undefined)?.nodes ?? [];

    for (const item of items) {
      if (item?.project?.id !== config.projectId) continue;
      // Opportunistically warm the item-ID cache while we're here.
      if (item?.id) {
        projectItemIdCache.set(
          projectItemCacheKey(config.projectId, owner, repo, issueNumber),
          item.id
        );
      }
      for (const fv of item.fieldValues?.nodes ?? []) {
        if (fv?.field?.name === "Status") {
          return fv.name ?? null;
        }
      }
      return null; // Found item but no status value set
    }
  } catch (error) {
    logger.warn("Failed to read project item status", {
      issueNumber,
      error: error instanceof Error ? error.message : String(error),
    });
  }

  return null;
}

/**
 * Update the Status field on the project board for an issue.
 *
 * Finds the project item, then uses `updateProjectV2ItemFieldValue` to set
 * the Status single-select field. Supports multi-project configs.
 *
 * @param issueNumber - The GitHub issue number
 * @param statusValue - The status to set (e.g. "Ready", "In progress", "Done")
 * @param cwd - The workspace root directory
 * @param logger - Logger instance for structured logging
 * @returns Success status and optional error message
 */
export async function updateProjectItemStatus(
  issueNumber: number,
  statusValue: ProjectStatusValue,
  cwd: string,
  logger: Logger,
  repoNameWithOwner?: string
): Promise<ProjectFieldResult> {
  if (!Number.isInteger(issueNumber) || issueNumber <= 0) {
    return { success: false, error: `Invalid issue number: ${issueNumber}` };
  }

  const configs = await loadProjectConfigs(cwd);
  if (!configs || configs.length === 0) {
    return {
      success: false,
      error:
        "No project configuration found. Check .nightgauge/config.yaml has project.fields with status/priority/size field IDs",
    };
  }

  const errors: string[] = [];
  let anySuccess = false;

  for (const config of configs) {
    const optionId = config.fields.status.options[statusValue];
    if (!optionId) {
      errors.push(
        `Status option "${statusValue}" not found in field mappings for project ${config.name || config.owner}`
      );
      continue;
    }

    try {
      // Find or add the issue to the project (cached on hit)
      let itemId = await findProjectItem(config, issueNumber, cwd, repoNameWithOwner);

      if (!itemId) {
        // Issue not on project — add it first
        const issueNodeId = await getContentNodeId(config, issueNumber, cwd, repoNameWithOwner);
        if (!issueNodeId) {
          const { owner, repo } = resolveOwnerRepo(config, repoNameWithOwner);
          errors.push(`Issue #${issueNumber} not found in ${owner}/${repo}`);
          continue;
        }

        const addResult = await executeGraphQL(
          `mutation($projectId: ID!, $contentId: ID!) {
            addProjectV2ItemById(input: {projectId: $projectId, contentId: $contentId}) {
              item { id }
            }
          }`,
          { projectId: config.projectId, contentId: issueNodeId },
          cwd
        );
        // eslint-disable-next-line @typescript-eslint/no-explicit-any
        itemId = (addResult as any)?.data?.addProjectV2ItemById?.item?.id;

        if (!itemId) {
          errors.push(
            `Failed to add issue #${issueNumber} to project ${config.name || config.owner}`
          );
          continue;
        }
        // Cache the freshly-created item ID so subsequent stage transitions
        // skip even the lookup query.
        const { owner, repo } = resolveOwnerRepo(config, repoNameWithOwner);
        projectItemIdCache.set(
          projectItemCacheKey(config.projectId, owner, repo, issueNumber),
          itemId
        );
        logger.info("Added issue to project board before status update", {
          issueNumber,
          project: config.name || config.owner,
        });
      }

      // Update the status field
      const mutation = `
        mutation($projectId: ID!, $itemId: ID!, $fieldId: ID!, $optionId: String!) {
          updateProjectV2ItemFieldValue(input: {
            projectId: $projectId
            itemId: $itemId
            fieldId: $fieldId
            value: { singleSelectOptionId: $optionId }
          }) {
            projectV2Item { id }
          }
        }
      `;

      await executeGraphQL(
        mutation,
        {
          projectId: config.projectId,
          itemId,
          fieldId: config.fields.status.id,
          optionId,
        },
        cwd
      );

      logger.info("Updated project board status", {
        issueNumber,
        status: statusValue,
        project: config.name || config.owner,
      });
      anySuccess = true;
    } catch (error) {
      const msg = error instanceof Error ? error.message : String(error);
      errors.push(`Failed to update status in project ${config.name || config.owner}: ${msg}`);
      logger.warn("Failed to update project board status", {
        issueNumber,
        status: statusValue,
        project: config.name || config.owner,
        error: msg,
      });
    }
  }

  if (anySuccess) {
    // #3023 phase 1: a successful promote-to-Ready is the most common path
    // by which users tell autonomous "go work this now". Trigger an
    // immediate scheduler rescan so the user sees instant dispatch instead
    // of waiting for the next poll. Best-effort — IPC failure must not
    // shadow the (already successful) status update; the scheduler's
    // polling loop is the safety net.
    if (statusValue === "Ready") {
      try {
        // Lazy require to avoid pulling IPC into projectFieldWriter's hot
        // path for non-Ready transitions.
        const { IpcClient } = await import("../services/IpcClient");
        await IpcClient.getInstance().autonomousRescan();
        logger.debug("Triggered autonomous rescan after promote to Ready", {
          issueNumber,
        });
      } catch (rescanErr) {
        logger.debug("Autonomous rescan trigger failed (non-fatal)", {
          issueNumber,
          error: rescanErr instanceof Error ? rescanErr.message : String(rescanErr),
        });
      }
    }
    return { success: true };
  }

  return {
    success: false,
    error: errors.join("; "),
  };
}

// ============================================================================
// Exported for testing
// ============================================================================

export const _testing = {
  loadProjectConfigs,
  loadFieldMappings,
  findProjectItem,
  getContentNodeId,
  /** @deprecated Use getContentNodeId — kept as an alias for test compatibility. */
  getIssueNodeId: getContentNodeId,
  executeGraphQL,
  clearConfigCache,
};
