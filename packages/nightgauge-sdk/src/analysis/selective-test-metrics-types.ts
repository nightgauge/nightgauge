/**
 * Types and Zod schemas for selective test metrics recording and analysis.
 *
 * Three core record types:
 * - `SelectiveTestMetricRecord` — per-PR savings metrics written after each
 *   selective test run.
 * - `GraphGapRecord` — escaped defect record written when a post-merge CI
 *   failure was not in the selected test set.
 * - `SelectiveTestEffectivenessResult` — rolling-window analysis result
 *   produced by `SelectiveTestEffectivenessAnalyzer`.
 *
 * @see Issue #1975 - Validation & Cost Tracking
 */

import { z } from "zod";
import type { ImpactLevel } from "./change-impact-types.js";

// ---------------------------------------------------------------------------
// SelectiveTestMetricRecord
// ---------------------------------------------------------------------------

export const SelectiveTestMetricRecordSchema = z.object({
  schema_version: z.literal("1.0"),
  record_type: z.literal("selective_run"),
  issue_number: z.number().int().positive(),
  pr_number: z.number().int().positive().optional(),
  branch: z.string().min(1),
  run_at: z.string().datetime(),
  impact_level: z.enum(["isolated", "cross-cutting", "infrastructure"]),
  total_tests: z.number().int().min(0).nullable(),
  selected_tests: z.number().int().min(0),
  skipped_tests: z.number().int().min(0).nullable(),
  /** Snapshot of selected test files for escaped defect correlation */
  selected_test_files: z.array(z.string()),
  estimated_tokens_saved: z.number().min(0),
  estimated_time_saved_ms: z.number().min(0),
  estimated_cost_saved_usd: z.number().min(0),
  /** Estimated full-suite cost derived from token count × rate */
  full_suite_cost_usd: z.number().min(0),
  /** Actual cost of the validate stage when selective testing was used */
  selective_cost_usd: z.number().min(0),
});

export type SelectiveTestMetricRecord = z.infer<typeof SelectiveTestMetricRecordSchema> & {
  impact_level: ImpactLevel;
};

// ---------------------------------------------------------------------------
// GraphGapRecord
// ---------------------------------------------------------------------------

export const GraphGapRecordSchema = z.object({
  schema_version: z.literal("1.0"),
  record_type: z.literal("graph_gap"),
  issue_number: z.number().int().positive(),
  pr_number: z.number().int().positive(),
  detected_at: z.string().datetime(),
  failing_ci_job: z.string().min(1),
  failing_test_file: z.string().min(1),
  /** Always false by definition — escaped means it was NOT in the selected set */
  was_in_selected_set: z.literal(false),
  /** Human-readable description, e.g. "src/foo.ts → bar.test.ts edge missing" */
  gap_description: z.string().min(1),
});

export type GraphGapRecord = z.infer<typeof GraphGapRecordSchema>;

// ---------------------------------------------------------------------------
// SelectiveTestEffectivenessResult
// ---------------------------------------------------------------------------

export interface SelectiveTestEffectivenessResult {
  /** Rolling window length in days */
  period_days: number;
  /** Total PRs analyzed (including non-selective) in the window */
  total_prs_analyzed: number;
  /** PRs that used selective testing in the window */
  total_prs_selective: number;
  /** Fraction of analyzed PRs that used selective testing (0.0–1.0) */
  selective_adoption_rate: number;
  /** Total tests skipped across all selective PRs in the window */
  total_tests_skipped: number;
  /** Average skip rate across selective runs (0.0–1.0) */
  avg_skip_rate: number;
  /** Total estimated cost saved (USD) across all selective PRs */
  total_cost_saved_usd: number;
  /** Total estimated time saved (ms) across all selective PRs */
  total_time_saved_ms: number;
  /** Number of escaped defects detected in the window */
  escaped_defects: number;
  /** escaped_defects / total_prs_selective (0.0 when no selective PRs) */
  escaped_defect_rate: number;
  /** true when escaped_defect_rate > threshold */
  threshold_exceeded: boolean;
  /** Configured alert threshold (default 0.02) */
  threshold: number;
  /** Human-readable recommendations */
  recommendations: string[];
}
