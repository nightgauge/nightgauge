/**
 * Jira Cloud REST API v3 — TypeScript type definitions.
 *
 * These types represent the raw shapes returned by the Jira Cloud REST API.
 * They are NOT the WorkItem contract — mapper.ts converts these to WorkItem.
 *
 * Coverage: Jira Cloud REST API v3 (https://developer.atlassian.com/cloud/jira/rest/v3/)
 * Scope: Issue, IssueType, Priority, Status, IssueLink, User, Project
 *
 * NOTE: This is a spike (issue #2572). Types cover the fields needed for the
 * WorkItemProvider contract. Production implementation (#2568) should expand
 * coverage to include Agile boards, sprints, attachments, and comments.
 *
 * @see mapper.ts — converts JiraIssue to WorkItem
 * @see JiraWorkItemProvider.ts — IWorkItemProvider implementation
 * @see Issue #2572 — Jira adapter spike
 * @see Issue #2568 — Production Jira adapter (follow-up)
 */

// ---------------------------------------------------------------------------
// Pagination
// ---------------------------------------------------------------------------

/**
 * Jira uses offset-based pagination (not cursor-based like GitHub GraphQL).
 * The client must loop until `startAt + issues.length >= total`.
 *
 * @see https://developer.atlassian.com/cloud/jira/rest/v3/api-group-issue-search/#api-rest-api-3-search-get
 */
export interface JiraSearchResult {
  /** Offset of the first returned result (matches requested startAt) */
  startAt: number;
  /** Maximum results requested (may be less than returned) */
  maxResults: number;
  /** Total matching issues (may change between pages if issues are added/removed) */
  total: number;
  /** The issues on this page */
  issues: JiraIssue[];
}

// ---------------------------------------------------------------------------
// Issue
// ---------------------------------------------------------------------------

/**
 * A Jira issue as returned by the Search API (GET /rest/api/3/search)
 * and the Issue API (GET /rest/api/3/issue/{issueIdOrKey}).
 */
export interface JiraIssue {
  /** Jira internal ID (numeric string, e.g. "10042") */
  id: string;
  /** Alphanumeric issue key (e.g. "PROJ-123") — primary user-facing identifier */
  key: string;
  /** URL to view this issue in the browser */
  self: string;
  /** All issue field values */
  fields: JiraIssueFields;
}

/**
 * The `fields` object on a JiraIssue.
 *
 * Custom fields appear as `customfield_NNNNN`. The specific IDs are
 * instance-specific (no standard across Jira installations). Users must
 * configure the field IDs in `.nightgauge/config.yaml`.
 *
 * @see https://developer.atlassian.com/cloud/jira/rest/v3/api-group-issue-fields/
 */
export interface JiraIssueFields {
  /** Issue title / headline */
  summary: string;

  /** Current workflow status */
  status: JiraStatus;

  /** Issue type (e.g. "Story", "Bug", "Epic", "Task") */
  issuetype: JiraIssueType;

  /** Standard priority field (Low, Medium, High, Highest) */
  priority: JiraPriority | null;

  /** Reporter who created the issue */
  reporter: JiraUser | null;

  /** User the issue is currently assigned to */
  assignee: JiraUser | null;

  /** Plaintext labels (e.g. ["backend", "spike"]) */
  labels: string[];

  /**
   * Issue link relationships (blocking, blocked-by, clones, relates-to, etc.)
   * Used to derive WorkItem.blockedBy and WorkItem.blocks.
   *
   * @see https://developer.atlassian.com/cloud/jira/rest/v3/api-group-issue-links/
   */
  issuelinks: JiraIssueLink[];

  /**
   * Parent issue — set on sub-tasks and next-gen child issues.
   * In classic Jira: sub-tasks only. In next-gen (team-managed): all child issues.
   * NOT the same as the Epic Link (which is a custom field in classic Jira).
   */
  parent?: JiraIssueRef;

  /**
   * Child issues for next-gen (team-managed) projects.
   * In classic Jira, use JQL `parent = PROJ-123` to fetch children.
   */
  subtasks?: JiraIssueRef[];

  /**
   * Issue description in Atlassian Document Format (ADF).
   * Complex nested structure — not used by the WorkItemProvider contract.
   * Use issue.fields.description?.content for text extraction if needed.
   */
  description?: JiraAdfDocument | null;

  /** Created timestamp (ISO 8601) */
  created: string;

  /** Last updated timestamp (ISO 8601) */
  updated: string;

  /** Resolution date (ISO 8601) if resolved, null otherwise */
  resolutiondate: string | null;

  // -------------------------------------------------------------------------
  // Custom fields — instance-specific IDs
  // -------------------------------------------------------------------------

  /**
   * Story points / size estimate.
   * Classic Jira: customfield_10016 (common default, but varies).
   * Next-gen Jira: customfield_10016 (story_points field).
   * Users must configure the correct field ID in `.nightgauge/config.yaml`.
   */
  [customFieldKey: `customfield_${string}`]: unknown;
}

// ---------------------------------------------------------------------------
// Status
// ---------------------------------------------------------------------------

/**
 * Jira workflow status.
 *
 * Status names are fully customizable per project workflow. The
 * WorkItemProvider maps them to standard board columns via the user-configured
 * `status_mapping` in `.nightgauge/config.yaml`.
 *
 * @see JiraAdapterOptions.statusMapping
 */
export interface JiraStatus {
  /** Jira internal status ID (numeric string) */
  id: string;
  /** User-visible status name (e.g. "To Do", "In Progress", "Done") */
  name: string;
  /** Status category (todo, in_progress, done) */
  statusCategory: JiraStatusCategory;
  /** REST API URL for this status */
  self: string;
}

export interface JiraStatusCategory {
  /** Category ID */
  id: number;
  /** Category key: "new" | "indeterminate" | "done" */
  key: "new" | "indeterminate" | "done";
  /** Category name (e.g. "To Do", "In Progress", "Done") */
  name: string;
  /** Color name for UI rendering */
  colorName: string;
}

// ---------------------------------------------------------------------------
// Issue Type
// ---------------------------------------------------------------------------

/**
 * Jira issue type.
 *
 * Common types: Story, Bug, Task, Sub-task, Epic.
 * Epic detection: `issuetype.name === "Epic"` (or `issuetype.hierarchyLevel === 1`).
 *
 * @see JiraIssueFields.issuetype
 */
export interface JiraIssueType {
  /** Jira internal issue type ID */
  id: string;
  /** User-visible type name (e.g. "Epic", "Story", "Bug") */
  name: string;
  /** Whether this is a sub-task type */
  subtask: boolean;
  /** Hierarchy level: 0=sub-task, 1=story, 2=epic (Jira Software) */
  hierarchyLevel?: number;
  /** Icon URL for the issue type */
  iconUrl?: string;
}

// ---------------------------------------------------------------------------
// Priority
// ---------------------------------------------------------------------------

/**
 * Jira standard priority.
 *
 * Default Jira priorities (from highest to lowest):
 *   Highest → maps to P0
 *   High    → maps to P1
 *   Medium  → maps to P2
 *   Low     → maps to P3
 *   Lowest  → maps to P3 (no P4 in WorkItem)
 *
 * Custom priorities are possible — mapper falls back to null on unknown names.
 *
 * @see mapper.ts mapPriority()
 */
export interface JiraPriority {
  /** Jira internal priority ID */
  id: string;
  /** Priority name (e.g. "High", "Medium", "Low") */
  name: string;
  /** Icon URL */
  iconUrl?: string;
}

// ---------------------------------------------------------------------------
// User
// ---------------------------------------------------------------------------

/**
 * Jira user (reporter or assignee).
 */
export interface JiraUser {
  /** Jira account ID (stable unique identifier) */
  accountId: string;
  /** Display name (e.g. "Jane Smith") */
  displayName: string;
  /** Email address (may be omitted for privacy) */
  emailAddress?: string;
  /** Whether the account is active */
  active: boolean;
}

// ---------------------------------------------------------------------------
// Issue Links (Blocking Relationships)
// ---------------------------------------------------------------------------

/**
 * A directional relationship between two Jira issues.
 *
 * The `type` field identifies the relationship kind. To find blocking
 * relationships, filter for types where `type.name === "Blocks"`.
 *
 * The direction matters:
 * - `outwardIssue` is present when this issue is the BLOCKER
 *   (e.g. "PROJ-5 blocks PROJ-6" → outwardIssue=PROJ-6)
 * - `inwardIssue` is present when this issue is BLOCKED
 *   (e.g. "PROJ-5 is blocked by PROJ-4" → inwardIssue=PROJ-4)
 *
 * @see mapper.ts mapIssueLinks()
 * @see https://developer.atlassian.com/cloud/jira/rest/v3/api-group-issue-links/
 */
export interface JiraIssueLink {
  /** Jira internal link ID */
  id: string;
  /** The relationship type */
  type: JiraIssueLinkType;
  /** The issue this link points TO (when this issue is the blocker) */
  outwardIssue?: JiraIssueRef;
  /** The issue this link comes FROM (when this issue is blocked) */
  inwardIssue?: JiraIssueRef;
}

/**
 * The relationship type for a JiraIssueLink.
 *
 * Common built-in types:
 *   name="Blocks"    inward="is blocked by"  outward="blocks"
 *   name="Cloners"   inward="is cloned by"   outward="clones"
 *   name="Duplicate" inward="is duplicated by" outward="duplicates"
 *   name="Relates"   inward="is related to"  outward="relates to"
 */
export interface JiraIssueLinkType {
  /** Jira internal link type ID */
  id: string;
  /** Type name (e.g. "Blocks") */
  name: string;
  /** Inward description (e.g. "is blocked by") */
  inward: string;
  /** Outward description (e.g. "blocks") */
  outward: string;
}

/**
 * A lightweight issue reference used in links and parent/subtask fields.
 */
export interface JiraIssueRef {
  /** Jira internal ID */
  id: string;
  /** Alphanumeric issue key (e.g. "PROJ-45") */
  key: string;
  /** REST API URL for this issue */
  self: string;
  /** Partial fields — not all fields are populated in a ref */
  fields?: {
    summary?: string;
    status?: JiraStatus;
    issuetype?: JiraIssueType;
    priority?: JiraPriority | null;
  };
}

// ---------------------------------------------------------------------------
// Atlassian Document Format (ADF)
// ---------------------------------------------------------------------------

/**
 * Atlassian Document Format root node.
 *
 * Used for issue descriptions and comments. Not used by the WorkItem contract
 * directly — included for completeness and future text extraction.
 *
 * @see https://developer.atlassian.com/cloud/jira/platform/apis/document/structure/
 */
export interface JiraAdfDocument {
  version: number;
  type: "doc";
  content: JiraAdfNode[];
}

export interface JiraAdfNode {
  type: string;
  content?: JiraAdfNode[];
  text?: string;
  attrs?: Record<string, unknown>;
  marks?: Array<{ type: string; attrs?: Record<string, unknown> }>;
}

// ---------------------------------------------------------------------------
// Configuration types for the Jira adapter
// ---------------------------------------------------------------------------

/**
 * Field ID configuration for instance-specific Jira custom fields.
 *
 * Custom field IDs look like `customfield_10016`. They vary between Jira
 * instances. Users discover these by:
 * 1. GET /rest/api/3/field (list all fields, find by name)
 * 2. Jira Admin → Issues → Custom Fields
 * 3. Browser DevTools on an issue page (inspect API responses)
 */
export interface JiraFieldIds {
  /**
   * Story points / size estimate field.
   * Common default: "customfield_10016" (Jira Software next-gen)
   * Classic projects often differ.
   */
  size?: string;

  /**
   * Epic Link field (classic Jira only).
   * Next-gen Jira uses fields.parent instead.
   * Common default: "customfield_10014"
   */
  epicLink?: string;

  /**
   * Sprint field (for future sprint-aware features).
   * Common default: "customfield_10020"
   */
  sprint?: string;
}

/**
 * User-configured mapping from Jira status names to canonical board columns.
 *
 * Keys: Jira status names (exact match, case-sensitive)
 * Values: Canonical board column names used by the WorkItem contract
 *         ("Backlog" | "Ready" | "In Progress" | "In Review" | "Done")
 *
 * @example
 * ```yaml
 * status_mapping:
 *   "To Do": "Backlog"
 *   "Selected for Development": "Ready"
 *   "In Progress": "In Progress"
 *   "In Review": "In Review"
 *   "Done": "Done"
 * ```
 */
export type JiraStatusMapping = Record<string, string>;

/**
 * Full configuration options for JiraWorkItemProvider.
 *
 * These are read from `.nightgauge/config.yaml` under
 * `work_item_source.provider_options` when `mode: "jira"`.
 *
 * @see .nightgauge/docs/jira-config-schema.md
 * @see .nightgauge/examples/jira-config.yaml
 */
export interface JiraAdapterOptions {
  /** Atlassian Cloud base URL (e.g. "https://mycompany.atlassian.net") */
  url: string;

  /** Jira project key to query (e.g. "PROJ") */
  projectKey: string;

  /**
   * Name of the environment variable holding the Jira API token.
   * The token itself is never stored in config — only the env var name.
   * @example "NIGHTGAUGE_JIRA_TOKEN"
   */
  apiTokenEnv: string;

  /**
   * Email address associated with the Jira API token.
   * Required for Basic auth (email:token base64 encoding).
   */
  email: string;

  /** Instance-specific custom field IDs */
  fieldIds?: JiraFieldIds;

  /**
   * Mapping from Jira status names to canonical board columns.
   * Any Jira status not in this map is excluded from board views.
   */
  statusMapping?: JiraStatusMapping;

  /**
   * Cache TTL in minutes (default: 5).
   * Lower values increase API usage; higher values reduce freshness.
   */
  cacheTtlMinutes?: number;

  /**
   * Maximum issues to fetch per page (default: 100, max: 100).
   * Jira Cloud hard-caps at 100 per request.
   */
  pageSize?: number;
}

// ---------------------------------------------------------------------------
// Adapter internal types
// ---------------------------------------------------------------------------

/**
 * Entry in the status-filtered cache.
 * Mirrors the cache entry pattern used by ProjectBoardService.
 */
export interface JiraCacheEntry<T> {
  items: T;
  timestamp: number;
}
