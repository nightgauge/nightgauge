/**
 * Epic Context Schema — accumulates findings across sub-issues in an epic.
 *
 * When multiple sub-issues run through the pipeline, each starts cold.
 * This schema defines the epic-level context file that accumulates
 * codebase discoveries, decisions, and file references so that later
 * sub-issues benefit from earlier research.
 *
 * File location: .nightgauge/pipeline/epic-{number}-context.json
 *
 * @see docs/CONTEXT_ARCHITECTURE.md
 * @see Issue #2404
 */

import { z } from "zod";

export const SubIssueFindingsSchema = z.object({
  /** Files created or modified by this sub-issue */
  files_touched: z.array(z.string()).default([]),
  /** Key decisions made during this sub-issue's execution */
  decisions: z.array(z.string()).default([]),
  /** Codebase discoveries useful for sibling sub-issues */
  discoveries: z.array(z.string()).default([]),
  /** Architecture patterns identified */
  patterns: z.array(z.string()).default([]),
  /** Timestamp of when this was recorded */
  recorded_at: z.string(),
});

export const EpicContextSchema = z.object({
  /** Schema version for forward compatibility */
  schema_version: z.literal("1.0"),
  /** Epic issue number */
  epic_number: z.number(),
  /** When this context was last updated */
  last_updated: z.string(),
  /** Accumulated findings from each sub-issue */
  sub_issue_findings: z.record(z.string(), SubIssueFindingsSchema).default({}),
  /** Shared research that applies to all sub-issues */
  shared_research: z
    .object({
      /** Codebase structure notes */
      codebase_notes: z.array(z.string()).default([]),
      /** Architecture observations */
      architecture_notes: z.array(z.string()).default([]),
      /** Relevant file paths discovered */
      relevant_files: z.array(z.string()).default([]),
    })
    .default(() => ({
      codebase_notes: [],
      architecture_notes: [],
      relevant_files: [],
    })),
});

export type EpicContext = z.infer<typeof EpicContextSchema>;
export type SubIssueFindings = z.infer<typeof SubIssueFindingsSchema>;
