import { z } from "zod";
import { flexEnum } from "./helpers.js";

/**
 * KnowledgeTypeSchema — enum of knowledge entry types.
 *
 * Values:
 * - decision: An architectural decision record (ADR)
 * - prd: A product requirements document
 * - conversation: A recorded design conversation
 * - adr: Formal ADR (more structured than decision)
 * - reference: External reference or link
 * - note: Freeform technical note
 *
 * Uses flexEnum for hyphen-normalization defense against AI agent output
 * variations (e.g., "decision" and "decision" both accepted).
 */
export const KnowledgeTypeSchema = flexEnum([
  "decision",
  "prd",
  "conversation",
  "adr",
  "reference",
  "note",
] as const);

export type KnowledgeType = z.infer<typeof KnowledgeTypeSchema>;

/**
 * KnowledgeEntrySchema — metadata for a single knowledge base entry.
 *
 * Represents the structured metadata about a knowledge artifact
 * (PRD.md, decisions.md, or other future knowledge files). This schema
 * validates the metadata object, not the raw Markdown file content.
 *
 * Knowledge files do not use YAML frontmatter — this schema validates
 * in-memory metadata objects consistent with how KnowledgeService returns
 * a ScaffoldResult with metadata fields.
 *
 * Schema version: 1.0
 *
 * @see docs/KNOWLEDGE_BASE.md for knowledge directory structure
 */
export const KnowledgeEntrySchema = z
  .object({
    /** Human-readable title of the knowledge entry */
    title: z.string().min(1),
    /** Type of knowledge entry */
    type: KnowledgeTypeSchema,
    /** ISO 8601 datetime when entry was created */
    created: z.string().datetime(),
    /** ISO 8601 datetime of last update. Equal to created if not yet updated. */
    updated: z.string().datetime(),
    /** Optional topic tags for discovery */
    tags: z.array(z.string()).optional(),
    /** GitHub issue numbers this entry relates to */
    related_issues: z.array(z.number().int().positive()).optional(),
    /** Source file paths related to this entry */
    related_files: z.array(z.string()).optional(),
    /** Repository slugs this workspace-level entry is scoped to */
    repos: z.array(z.string()).optional(),
    /** Related issue/PR references, e.g. ['#2090', '#2091'] */
    related: z.array(z.string()).optional(),
    /** Lifecycle status of this knowledge entry */
    status: z.enum(["draft", "stable", "superseded"]).optional(),
    /** Issue number that supersedes this entry (when status=superseded) */
    superseded_by: z.string().optional(),
  })
  .passthrough();

export type KnowledgeEntry = z.infer<typeof KnowledgeEntrySchema>;

/**
 * KnowledgeIndexSchema — validates the auto-generated README index structure.
 *
 * The index groups knowledge entries by category (epics, features) and
 * provides a directory listing for the knowledge base. The categories field
 * uses z.record for forward compatibility with future categories beyond
 * epics/ and features/ (e.g., glossary/, architecture/).
 *
 * Schema version: 1.0
 *
 * @see docs/KNOWLEDGE_BASE.md for knowledge directory structure
 */
export const KnowledgeIndexSchema = z
  .object({
    /** Total number of knowledge entries in the index */
    total_entries: z.number().int().min(0),
    /** ISO 8601 datetime when the index was last generated */
    generated_at: z.string().datetime(),
    /** Entries grouped by category directory */
    categories: z.record(
      z.string(),
      z.array(
        z
          .object({
            /** Issue number for this knowledge entry */
            issue_number: z.number().int().positive(),
            /** Slug portion of the directory name */
            slug: z.string().min(1),
            /** Full directory path relative to workspace root */
            path: z.string().min(1),
            /** Files present in this knowledge directory */
            files: z.array(z.string()),
            /** Optional entry metadata (present when index includes full metadata) */
            entry: KnowledgeEntrySchema.optional(),
          })
          .passthrough()
      )
    ),
  })
  .passthrough();

export type KnowledgeIndex = z.infer<typeof KnowledgeIndexSchema>;

/**
 * RepoTopicTypeSchema — enum of repo-topic knowledge category types.
 *
 * These correspond to flat-file categories under .nightgauge/knowledge/
 * that hold repo-wide reference material (not per-issue entries).
 *
 * - architecture: Cross-issue architectural principles and patterns
 * - glossary: One-file-per-term domain vocabulary definitions
 * - runbook: Operational procedures for recurring tasks
 * - post-mortem: Incident write-ups and retrospective analyses
 */
export const RepoTopicTypeSchema = z.enum(["architecture", "glossary", "runbook", "post-mortem"]);

export type RepoTopicType = z.infer<typeof RepoTopicTypeSchema>;
