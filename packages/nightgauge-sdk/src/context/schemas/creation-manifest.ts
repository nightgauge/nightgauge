import { z } from "zod";
import { flexEnum, optionalString } from "./helpers.js";

/**
 * CreationManifestSchema — strict-mode contract for post-creation issue audit.
 *
 * Written by `nightgauge-issue-create` (and future epic decomposition
 * flows) immediately after every issue + relationship is created. Read by
 * `nightgauge-issue-audit --manifest <path>` as the authoritative
 * declaration of *what should exist*. Every assertion in the manifest must
 * hold; any drift is a CRITICAL audit finding.
 *
 * The schema is forward-only: it describes intent at creation time. It is
 * never updated retroactively. Manifests for historical issues are not
 * back-filled — `--epic` / `--issues` / `--all-recent` audit modes cover
 * pre-existing issues using inferential checks.
 *
 * Schema version: 1.0
 *
 * @see docs/ISSUE_AUDIT.md for finding taxonomy and severity rules
 * @see docs/CONTEXT_ARCHITECTURE.md for pipeline lifecycle
 */

/**
 * Issue type — mirrors the enum used by IssueContextSchema. Spike issues
 * carry an additional `spike_artifact` field below.
 */
export const ManifestIssueTypeSchema = flexEnum([
  "feature",
  "bug",
  "docs",
  "refactor",
  "spike",
  "chore",
  "epic",
] as const);
export type ManifestIssueType = z.infer<typeof ManifestIssueTypeSchema>;

/**
 * Priority — project board field, not a label.
 */
export const ManifestPrioritySchema = flexEnum(["P0", "P1", "P2", "P3"] as const);
export type ManifestPriority = z.infer<typeof ManifestPrioritySchema>;

/**
 * Size — project board field, not a label.
 */
export const ManifestSizeSchema = z.enum(["XS", "S", "M", "L", "XL"]);
export type ManifestSize = z.infer<typeof ManifestSizeSchema>;

/**
 * Initial status — `Backlog` for sub-issues that still need wiring,
 * `Ready` for issues that are immediately pickup-eligible.
 */
export const ManifestStatusSchema = flexEnum(["Backlog", "Ready", "In progress"] as const);
export type ManifestStatus = z.infer<typeof ManifestStatusSchema>;

/**
 * Cross-repo blocker reference. Same-repo blockers omit `repo` and use just
 * `number`. Cross-repo blockers carry `owner/repo` (since GitHub's native
 * `blockedBy` does not span repositories — Phase 7 of the audit checks the
 * body annotation instead).
 */
export const ManifestBlockerRefSchema = z.object({
  number: z.number().int().positive(),
  /** Optional `owner/repo` slug. Omit for same-repo blockers. */
  repo: optionalString(),
});
export type ManifestBlockerRef = z.infer<typeof ManifestBlockerRefSchema>;

/**
 * Spike artifact pre-declaration — required for `type: spike` entries per
 * docs/SPIKE_CONTRACT.md. Populated when issue-create authors a spike issue.
 */
export const ManifestSpikeArtifactSchema = z.object({
  /** Path to the spike artifact, e.g. `docs/spikes/3237-issue-audit.md` */
  path: z.string().min(1),
  /** Whether the artifact already exists at creation time (typically false) */
  exists: z.boolean().catch(false),
});
export type ManifestSpikeArtifact = z.infer<typeof ManifestSpikeArtifactSchema>;

/**
 * Per-issue manifest entry. One entry per issue created (epic + every
 * sub-issue). The audit walks every entry and asserts each declared
 * relationship and field still holds.
 */
export const CreationManifestEntrySchema = z
  .object({
    /** `owner/repo` slug for the issue's home repository */
    repo: z.string().min(1),
    /** GitHub issue number */
    number: z.number().int().positive(),
    /** Issue type at creation time */
    type: ManifestIssueTypeSchema,
    /** Project board priority (set as field, not label) */
    priority: ManifestPrioritySchema,
    /** Project board size (set as field, not label) */
    size: ManifestSizeSchema,
    /** Initial project board status */
    status: ManifestStatusSchema,
    /**
     * Parent epic reference — present for sub-issues only. Use the parent's
     * `owner/repo#number` slug for cross-repo parents; same-repo parents may
     * use `#number` shorthand.
     */
    parent_epic: optionalString(),
    /** Sub-issue numbers (epic only) */
    sub_issues: z.array(z.number().int().positive()).catch([]),
    /** `blockedBy` declarations the audit must verify */
    blocked_by: z.array(ManifestBlockerRefSchema).catch([]),
    /**
     * Required body section headings (e.g. `["Summary", "Acceptance Criteria"]`).
     * The audit asserts each heading exists and its content is non-empty.
     */
    body_sections: z.array(z.string().min(1)).catch([]),
    /** Component labels (`component:*`) attached at creation time */
    component_labels: z.array(z.string().min(1)).catch([]),
    /** Knowledge directory path (when `--with-knowledge` was used) */
    knowledge_path: optionalString(),
    /** Spike artifact pre-declaration (required when `type: spike`) */
    spike_artifact: ManifestSpikeArtifactSchema.nullish(),
  })
  .passthrough();
export type CreationManifestEntry = z.infer<typeof CreationManifestEntrySchema>;

/**
 * Top-level manifest. One file per creation flow, written to
 * `.nightgauge/pipeline/issue-create-manifest-<timestamp>.json`.
 */
export const CreationManifestSchema = z
  .object({
    /** Schema version — pinned to 1.0 for the initial release */
    schema_version: z.string().regex(/^\d+\.\d+$/),
    /** ISO 8601 timestamp at write time */
    created_at: z.string().datetime(),
    /**
     * Skill that wrote this manifest, e.g. `nightgauge-issue-create`.
     * Useful for future cross-flow auditing.
     */
    created_by_skill: z.string().min(1),
    /** Project number this creation flow targeted (when single-repo) */
    project_number: z.number().int().positive().nullish(),
    /** Per-issue declarations */
    entries: z.array(CreationManifestEntrySchema).min(1),
  })
  .passthrough();
export type CreationManifest = z.infer<typeof CreationManifestSchema>;
