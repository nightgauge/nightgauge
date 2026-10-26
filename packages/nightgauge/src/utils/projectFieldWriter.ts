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

import { exec, execFile } from "child_process";
import { promisify } from "util";
import * as fs from "fs";
import * as path from "path";
import * as yaml from "yaml";
import { resolveConfigPath } from "./configPathResolver";
import { IpcClient } from "../services/IpcClient";
import type { Logger } from "./logger";

const execAsync = promisify(exec);
const execFileAsync = promisify(execFile);

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

/** A project board and its Status/Priority/Size fields, resolved from the board itself */
interface ProjectFieldConfig {
  owner: string;
  repo: string;
  projectId: string;
  fields: {
    status: { id: string; options: Record<string, string> };
    priority: { id: string; options: Record<string, string> } | null;
    size: { id: string; options: Record<string, string> } | null;
  };
}

/** Outcome of resolving a repository's board: a config, or why there is none */
type BoardResolution = { config: ProjectFieldConfig } | { error: string };

/** Board identity as printed by `nightgauge project resolve --repo <slug> --json` */
interface ResolvedProject {
  id: string;
  owner: string;
  number: number;
}

/** Injectable process boundary for `nightgauge project resolve` (tests replace it) */
export interface BoardResolverDeps {
  resolveBinary: () => Promise<string | null>;
  exec: (binary: string, args: string[], cwd: string) => Promise<{ stdout: string }>;
}

const defaultBoardResolverDeps: BoardResolverDeps = {
  resolveBinary: async () => {
    const { BinaryResolver } = await import("../services/BinaryResolver");
    return BinaryResolver.fromVSCode().resolve();
  },
  exec: (binary, args, cwd) => execFileAsync(binary, args, { cwd, timeout: 60000 }),
};

let boardResolverDeps: BoardResolverDeps = defaultBoardResolverDeps;

// ============================================================================
// Board Cache (a board's field and option IDs don't change between calls)
// ============================================================================

/** Resolved boards keyed by `${cwd}|${owner/repo}`. Failures are never cached. */
const boardCache = new Map<string, ProjectFieldConfig>();

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
 * Clear the cached boards. Call when workspace changes.
 */
export function clearConfigCache(): void {
  boardCache.clear();
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
// Board Resolution (project by repo, fields by project)
// ============================================================================

/**
 * The workspace's own `owner/repo`, read from `.nightgauge/config.yaml`.
 * Used only when the caller names no repository.
 */
async function readWorkspaceRepo(cwd: string): Promise<string | null> {
  try {
    const configResult = await resolveConfigPath(cwd);
    if (!configResult.exists) return null;
    const config = yaml.parse(await fs.promises.readFile(configResult.path, "utf-8"));
    const owner = config?.project?.owner || config?.owner;
    const repo = config?.project?.repo || config?.repo;
    return owner && repo ? `${owner}/${repo}` : null;
  } catch {
    return null;
  }
}

/** Map a ProjectV2 single-select field's option names to their IDs. */
function optionMap(field: { id: string; options?: { id: string; name: string }[] }): {
  id: string;
  options: Record<string, string>;
} {
  const options: Record<string, string> = {};
  for (const opt of field.options ?? []) {
    if (opt?.name && opt?.id) options[opt.name] = opt.id;
  }
  return { id: field.id, options };
}

/**
 * Resolve the board for a repository, the same way the Go scheduler does:
 * the project comes from `nightgauge project resolve --repo <slug>`, and the
 * Status/Priority/Size field and option IDs come from one `ProjectV2.fields`
 * query against that project. Nothing is read from hand-copied
 * `project.fields` config (#1501). Successful resolutions are cached per
 * workspace and repository, so a second transition issues no discovery calls.
 */
async function resolveBoard(cwd: string, repoNameWithOwner?: string): Promise<BoardResolution> {
  const slug =
    repoNameWithOwner && /^[^/\s]+\/[^/\s]+$/.test(repoNameWithOwner)
      ? repoNameWithOwner
      : await readWorkspaceRepo(cwd);
  if (!slug) {
    return {
      error: "Board fields unresolvable: no repository named and none in .nightgauge/config.yaml",
    };
  }
  const cacheKey = `${cwd}|${slug}`;
  const cached = boardCache.get(cacheKey);
  if (cached) return { config: cached };

  const fail = (cause: string): BoardResolution => ({
    error: `Board fields unresolvable for ${slug}: ${cause}`,
  });

  const binary = await boardResolverDeps.resolveBinary();
  if (!binary) return fail("nightgauge binary not found");

  let project: ResolvedProject;
  try {
    const { stdout } = await boardResolverDeps.exec(
      binary,
      ["project", "resolve", "--repo", slug, "--json"],
      cwd
    );
    project = JSON.parse(stdout) as ResolvedProject;
  } catch (err) {
    const e = err as { stderr?: unknown; message?: unknown };
    return fail(String(e?.stderr || e?.message || err).trim());
  }
  if (!project?.id) return fail("no project is mapped to this repository");

  let nodes: { id?: string; name?: string; options?: { id: string; name: string }[] }[];
  try {
    const result = await executeGraphQL(
      `query($projectId: ID!) {
        node(id: $projectId) {
          ... on ProjectV2 {
            fields(first: 50) {
              nodes { ... on ProjectV2SingleSelectField { id name options { id name } } }
            }
          }
        }
      }`,
      { projectId: project.id },
      cwd
    );
    // eslint-disable-next-line @typescript-eslint/no-explicit-any
    nodes = (result as any)?.data?.node?.fields?.nodes ?? [];
  } catch (err) {
    return fail(err instanceof Error ? err.message : String(err));
  }

  const byName = (name: string) => nodes.find((n) => n?.name === name && n?.id);
  const status = byName("Status");
  if (!status?.id) return fail(`project ${project.owner}/${project.number} has no Status field`);
  const priority = byName("Priority");
  const size = byName("Size");

  const [owner, repo] = slug.split("/", 2);
  const config: ProjectFieldConfig = {
    owner,
    repo,
    projectId: project.id,
    fields: {
      status: optionMap({ id: status.id, options: status.options }),
      priority: priority?.id ? optionMap({ id: priority.id, options: priority.options }) : null,
      size: size?.id ? optionMap({ id: size.id, options: size.options }) : null,
    },
  };
  boardCache.set(cacheKey, config);
  return { config };
}

// ============================================================================
// GraphQL Execution (same pattern as githubStatusSync.ts)
// ============================================================================

/**
 * Execute a GraphQL query/mutation.
 *
 * Prefers the running daemon's `github.graphqlRaw` IPC method: that client is
 * the one the API ledger instruments and the rate-limit gate throttles, so a
 * board write made this way is visible spend instead of invisible spend
 * (#1913). The `gh api graphql` subprocess remains the fallback for the case
 * this utility runs with no daemon connected — it is a synchronous helper
 * other code calls directly, and making it require `serve` would trade a
 * measurement gap for an outage.
 */
async function executeGraphQL(
  query: string,
  variables: Record<string, unknown>,
  cwd: string
): Promise<Record<string, unknown>> {
  const ipc = IpcClient.getInstance();
  if (ipc.isConnected) {
    try {
      return (await ipc.githubGraphqlRaw(query, variables)) as Record<string, unknown>;
    } catch {
      // Fall through to the subprocess: a refused or failed IPC call must not
      // turn a board write into a hard failure.
    }
  }
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

  const board = await resolveBoard(cwd, repoNameWithOwner);
  if ("error" in board) {
    logger.warn(board.error, { cwd, issueNumber });
    return null;
  }
  const { config } = board;

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

  const board = await resolveBoard(cwd, repoNameWithOwner);
  if ("error" in board) {
    logger.warn(board.error, { cwd, issueNumber });
    return null;
  }
  const { config } = board;
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
 * the Status single-select field. The board is resolved from the
 * repository, not from config (#1501).
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

  const board = await resolveBoard(cwd, repoNameWithOwner);
  if ("error" in board) {
    return { success: false, error: board.error };
  }
  const { config } = board;

  const optionId = config.fields.status.options[statusValue];
  if (!optionId) {
    return {
      success: false,
      error: `Status option "${statusValue}" not found on the Status field of the board for ${config.owner}`,
    };
  }

  try {
    // Find or add the issue to the project (cached on hit)
    let itemId = await findProjectItem(config, issueNumber, cwd, repoNameWithOwner);

    if (!itemId) {
      // Issue not on project — add it first
      const issueNodeId = await getContentNodeId(config, issueNumber, cwd, repoNameWithOwner);
      if (!issueNodeId) {
        const { owner, repo } = resolveOwnerRepo(config, repoNameWithOwner);
        return { success: false, error: `Issue #${issueNumber} not found in ${owner}/${repo}` };
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
        return {
          success: false,
          error: `Failed to add issue #${issueNumber} to project ${config.owner}`,
        };
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
        project: config.owner,
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
      project: config.owner,
    });
  } catch (error) {
    const msg = error instanceof Error ? error.message : String(error);
    logger.warn("Failed to update project board status", {
      issueNumber,
      status: statusValue,
      project: config.owner,
      error: msg,
    });
    return { success: false, error: `Failed to update status in project ${config.owner}: ${msg}` };
  }

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

// ============================================================================
// Exported for testing
// ============================================================================

export const _testing = {
  resolveBoard,
  setBoardResolverDeps: (deps: BoardResolverDeps | null): void => {
    boardResolverDeps = deps ?? defaultBoardResolverDeps;
  },
  findProjectItem,
  getContentNodeId,
  executeGraphQL,
  clearConfigCache,
};
