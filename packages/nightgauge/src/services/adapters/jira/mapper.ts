/**
 * Jira → WorkItem mapper (spike stub).
 *
 * Converts raw Jira Cloud REST API v3 responses to the WorkItem contract
 * defined by IWorkItemProvider. This is the only layer that knows about
 * Jira field shapes — JiraWorkItemProvider and views never touch JiraIssue
 * directly.
 *
 * SPIKE STATUS (issue #2572):
 * - Method signatures and field mapping logic are complete
 * - HTTP calls are NOT included — this file is pure data transformation
 * - Production implementation (#2568) should add error handling, logging,
 *   and integration tests against Jira Cloud fixtures
 *
 * Key mapping decisions (see .nightgauge/docs/jira-github-mapping-gaps.md):
 * 1. Issue number: Jira key suffix parsed as integer (PROJ-123 → 123).
 *    WorkItem.source.nativeId preserves the full key.
 * 2. Priority: name-based mapping (Highest→P0, High→P1, Medium→P2, Low/Lowest→P3)
 * 3. Size: read from user-configured customfield ID; null if not configured
 * 4. Status: user-configured statusMapping from Jira name to board column
 * 5. blockedBy/blocks: derived from issuelinks where type.name === "Blocks"
 * 6. epicRef: next-gen uses fields.parent; classic uses epicLink custom field
 * 7. isEpic: issuetype.name === "Epic" (case-insensitive)
 * 8. subIssueNumbers: NOT available on the issue response — requires a
 *    secondary JQL search (parent = <key>) in the provider
 *
 * @see types.ts — Jira API response type definitions
 * @see JiraWorkItemProvider.ts — calls this mapper
 * @see .nightgauge/docs/jira-github-mapping-gaps.md — gap analysis
 * @see Issue #2568 — production adapter (follow-up)
 */

import type { WorkItem, WorkItemSource } from "../../types/WorkItemProvider";
import type { Priority, Size, BlockingIssue } from "../../ProjectBoardService";
import type {
  JiraIssue,
  JiraIssueLink,
  JiraFieldIds,
  JiraStatusMapping,
  JiraIssueRef,
} from "./types";

// ---------------------------------------------------------------------------
// Primary mapping entry point
// ---------------------------------------------------------------------------

/**
 * Convert a JiraIssue to a WorkItem.
 *
 * This is the main entry point called by JiraWorkItemProvider after fetching
 * issues from the Jira Search API.
 *
 * @param issue - Raw Jira issue from REST API
 * @param options - Adapter configuration (field IDs, status mapping)
 * @returns WorkItem matching the IWorkItemProvider contract
 *
 * @example
 * ```typescript
 * const workItem = mapJiraIssueToWorkItem(jiraIssue, {
 *   baseUrl: "https://myco.atlassian.net",
 *   projectKey: "PROJ",
 *   fieldIds: { size: "customfield_10016", epicLink: "customfield_10014" },
 *   statusMapping: { "To Do": "Ready", "In Progress": "In Progress" },
 * });
 * ```
 */
export function mapJiraIssueToWorkItem(issue: JiraIssue, options: MapperOptions): WorkItem {
  const source: WorkItemSource = {
    provider: "jira",
    repository: options.projectKey,
    projectId: options.baseUrl,
  };

  // Derive numeric issue number from Jira key (e.g. "PROJ-123" → 123).
  // WorkItem.number is typed as number. The full Jira key is preserved in
  // source metadata for display and deep-link purposes.
  // GAP: Cross-project collision if two projects share the same numeric suffix.
  // Follow-up #2568: consider using Jira's internal integer ID instead.
  const number = parseIssueNumber(issue.key);

  const blockedBy = mapBlockedBy(issue.fields.issuelinks, options.baseUrl);
  const blocks = mapBlocks(issue.fields.issuelinks, options.baseUrl);

  const epicRef = resolveEpicRef(issue, options.fieldIds);
  const epicTitle = resolveEpicTitle(issue, options.fieldIds);

  const isEpic = issue.fields.issuetype.name.toLowerCase() === "epic";

  // subIssueNumbers cannot be populated here — requires a secondary JQL call.
  // JiraWorkItemProvider.getAllItems() must perform the parent= search and
  // populate this field in a post-processing pass.
  // See .nightgauge/docs/jira-github-mapping-gaps.md § epicRef row.
  const subIssueNumbers: number[] | undefined = isEpic ? [] : undefined;

  return {
    number,
    title: issue.fields.summary,
    labels: issue.fields.labels ?? [],
    priority: mapPriority(issue.fields.priority?.name ?? null),
    size: mapSize(issue, options.fieldIds?.size),
    url: buildIssueUrl(options.baseUrl, issue.key),
    status: mapStatus(issue.fields.status.name, options.statusMapping),
    epicRef,
    epicTitle,
    blockedBy: blockedBy.length > 0 ? blockedBy : undefined,
    blocks: blocks.length > 0 ? blocks : undefined,
    isEpic,
    subIssueNumbers,
    source: {
      ...source,
      // Store the full Jira key in an extended source field so callers can
      // reconstruct the original identifier when needed (e.g. deep links).
      // Cast is needed because WorkItemSource doesn't yet have nativeId.
      // Follow-up #2568: add nativeId to WorkItemSource interface.
      ...({ nativeId: issue.key } as unknown as object),
    },
  };
}

// ---------------------------------------------------------------------------
// Configuration interface for this mapper
// ---------------------------------------------------------------------------

/**
 * Options passed from JiraWorkItemProvider to the mapper on each call.
 * Derived from JiraAdapterOptions but limited to what the mapper needs.
 */
export interface MapperOptions {
  /** Atlassian Cloud base URL (e.g. "https://myco.atlassian.net") */
  baseUrl: string;
  /** Jira project key (e.g. "PROJ") */
  projectKey: string;
  /** Custom field IDs for instance-specific fields */
  fieldIds?: JiraFieldIds;
  /** User-configured mapping from Jira status names to board columns */
  statusMapping?: JiraStatusMapping;
}

// ---------------------------------------------------------------------------
// Field mappers
// ---------------------------------------------------------------------------

/**
 * Parse the numeric suffix from a Jira issue key.
 *
 * @example parseIssueNumber("PROJ-123") → 123
 * @example parseIssueNumber("ABC-1")    → 1
 *
 * Returns 0 if the key cannot be parsed (should not happen in practice).
 *
 * GAP: Loses project context. "PROJ-42" and "OTHER-42" both become 42.
 * Use WorkItem.source metadata to recover the full key for display/links.
 */
export function parseIssueNumber(key: string): number {
  const match = key.match(/-(\d+)$/);
  return match ? parseInt(match[1], 10) : 0;
}

/**
 * Map a Jira priority name to the WorkItem Priority type.
 *
 * Jira default priorities (highest → lowest):
 *   Highest → P0
 *   High    → P1
 *   Medium  → P2
 *   Low     → P3
 *   Lowest  → P3  (no P4 equivalent in WorkItem)
 *
 * Custom priority names fall back to null. Production implementation (#2568)
 * should let users configure custom priority mappings in `.nightgauge/config.yaml`.
 */
export function mapPriority(jiraPriorityName: string | null): Priority {
  if (!jiraPriorityName) return null;

  const normalized = jiraPriorityName.toLowerCase().trim();
  switch (normalized) {
    case "highest":
    case "blocker":
    case "critical":
      return "P0";
    case "high":
      return "P1";
    case "medium":
    case "normal":
      return "P2";
    case "low":
    case "lowest":
    case "trivial":
    case "minor":
      return "P3";
    default:
      // Unknown custom priority — return null rather than throwing.
      // Production (#2568): log a warning and surface in config validation.
      return null;
  }
}

/**
 * Read size / story points from the issue's custom field.
 *
 * Story point values in Jira are typically numbers (0.5, 1, 2, 3, 5, 8, 13).
 * We map numeric ranges to XS/S/M/L/XL. This mapping is intentionally coarse
 * and should be made user-configurable in #2568.
 *
 * If no size field is configured, or the field is null, returns null.
 *
 * @param issue - Raw Jira issue
 * @param sizeFieldId - Custom field ID (e.g. "customfield_10016"), or undefined
 */
export function mapSize(issue: JiraIssue, sizeFieldId: string | undefined): Size {
  if (!sizeFieldId) return null;

  // Custom fields are indexed by their ID on the fields object.
  const rawValue = (issue.fields as unknown as Record<string, unknown>)[sizeFieldId];
  if (rawValue === null || rawValue === undefined) return null;

  const points = typeof rawValue === "number" ? rawValue : parseFloat(String(rawValue));
  if (isNaN(points)) return null;

  // Fibonacci-adjacent mapping (common Jira story point scales):
  //   0.5–1  → XS
  //   2      → S
  //   3–5    → M
  //   8–13   → L
  //   20+    → XL
  if (points <= 1) return "XS";
  if (points <= 2) return "S";
  if (points <= 5) return "M";
  if (points <= 13) return "L";
  return "XL";
}

/**
 * Map a Jira status name to the canonical board column name.
 *
 * Uses the user-configured statusMapping from `.nightgauge/config.yaml`.
 * Returns undefined (item hidden from board views) if the status is not mapped.
 *
 * Production (#2568): surface unmapped statuses as a config validation warning.
 */
export function mapStatus(
  jiraStatusName: string,
  statusMapping: JiraStatusMapping | undefined
): string | undefined {
  if (!statusMapping) return undefined;
  return statusMapping[jiraStatusName];
}

/**
 * Derive WorkItem.blockedBy from Jira issuelinks.
 *
 * "This issue is blocked by X" → X is in blockedBy.
 *
 * Detection: Look for inwardIssue links where type.name === "Blocks"
 * (inward description is typically "is blocked by").
 *
 * GAP: The linked issue's state (OPEN/CLOSED) is not included on the issue
 * response. A secondary fetch or JQL expand is needed to determine if the
 * blocking issue is still open. For the spike, we treat all blocking links
 * as OPEN (conservative — better to show a false lock than miss a real one).
 *
 * @see .nightgauge/docs/jira-github-mapping-gaps.md § blockedBy/blocks
 */
export function mapBlockedBy(issuelinks: JiraIssueLink[], baseUrl: string = ""): BlockingIssue[] {
  return issuelinks
    .filter((link) => link.type.name.toLowerCase() === "blocks" && link.inwardIssue !== undefined)
    .map((link) => issueRefToBlockingIssue(link.inwardIssue!, "OPEN", baseUrl));
}

/**
 * Derive WorkItem.blocks from Jira issuelinks.
 *
 * "This issue blocks X" → X is in blocks.
 *
 * Detection: Look for outwardIssue links where type.name === "Blocks".
 *
 * Same GAP as mapBlockedBy — state is always set to "OPEN" in the spike.
 */
export function mapBlocks(issuelinks: JiraIssueLink[], baseUrl: string = ""): BlockingIssue[] {
  return issuelinks
    .filter((link) => link.type.name.toLowerCase() === "blocks" && link.outwardIssue !== undefined)
    .map((link) => issueRefToBlockingIssue(link.outwardIssue!, "OPEN", baseUrl));
}

/**
 * Convert a JiraIssueRef to a BlockingIssue.
 *
 * @param ref - Jira issue reference from the issuelinks field
 * @param state - Issue state ("OPEN" | "CLOSED") — cannot be determined from
 *                ref alone; caller must provide (or default to "OPEN" for spike)
 */
export function issueRefToBlockingIssue(
  ref: JiraIssueRef,
  state: "OPEN" | "CLOSED",
  baseUrl: string = ""
): BlockingIssue {
  return {
    number: parseIssueNumber(ref.key),
    title: ref.fields?.summary ?? ref.key,
    url: buildIssueUrl(baseUrl, ref.key),
    state,
  };
}

/**
 * Resolve the parent epic number for this issue.
 *
 * Two Jira project types handle epic links differently:
 * - Next-gen (team-managed): fields.parent contains the epic directly
 * - Classic (company-managed): uses a custom "Epic Link" field
 *
 * Returns undefined if no epic relationship is found.
 *
 * @param issue - Raw Jira issue
 * @param fieldIds - Custom field ID configuration
 *
 * @see .nightgauge/docs/jira-github-mapping-gaps.md § epicRef row
 */
export function resolveEpicRef(
  issue: JiraIssue,
  fieldIds: JiraFieldIds | undefined
): number | undefined {
  // Next-gen: parent field is set when the parent is an Epic
  if (issue.fields.parent && issue.fields.parent.fields?.issuetype?.name.toLowerCase() === "epic") {
    return parseIssueNumber(issue.fields.parent.key);
  }

  // Classic: Epic Link custom field (if configured)
  if (fieldIds?.epicLink) {
    const epicKey = (issue.fields as unknown as Record<string, unknown>)[fieldIds.epicLink];
    if (typeof epicKey === "string" && epicKey.length > 0) {
      return parseIssueNumber(epicKey);
    }
  }

  return undefined;
}

/**
 * Resolve the parent epic title for this issue.
 *
 * Follows the same detection logic as resolveEpicRef but returns the
 * summary string. Only available via fields.parent.fields.summary on
 * next-gen projects — classic projects only have the key in the epic link
 * custom field (summary requires a secondary fetch).
 *
 * Returns undefined if the title cannot be determined without a secondary call.
 */
export function resolveEpicTitle(
  issue: JiraIssue,
  _fieldIds: JiraFieldIds | undefined
): string | undefined {
  if (issue.fields.parent && issue.fields.parent.fields?.issuetype?.name.toLowerCase() === "epic") {
    return issue.fields.parent.fields?.summary;
  }
  // Classic Jira: epic title would require a secondary GET /issue/{epicKey} call.
  // Production (#2568): implement fetchEpicTitle() with caching.
  return undefined;
}

/**
 * Build the browser URL for a Jira issue.
 *
 * @param baseUrl - Atlassian Cloud base URL (e.g. "https://myco.atlassian.net")
 * @param issueKey - Jira issue key (e.g. "PROJ-123")
 * @returns Full browser URL
 *
 * @example buildIssueUrl("https://myco.atlassian.net", "PROJ-123")
 *          → "https://myco.atlassian.net/browse/PROJ-123"
 */
export function buildIssueUrl(baseUrl: string, issueKey: string): string {
  const cleanBase = baseUrl.replace(/\/$/, "");
  return `${cleanBase}/browse/${issueKey}`;
}
