/**
 * Zod schema for Skill Effectiveness records
 *
 * Defines the JSONL record format for per-skill-change effectiveness
 * data written by PostPipelineAnalyzer after each pipeline run.
 * Used to surface skill edit ROI in future pipeline-health dimensions.
 *
 * File path: .nightgauge/health/skill-effectiveness.jsonl
 *
 * @see Issue #1414 - Skill effectiveness tracking
 */

import { z } from "zod";

export const SkillEffectivenessRecordSchema = z.object({
  schema_version: z.literal("1"),
  skill_file: z.string(),
  stage: z.string(),
  commit_hash: z.string(),
  /** ISO 8601 */
  changed_at: z.string(),
  before_sample_count: z.number().int().min(0),
  before_success_rate: z.number().min(0).max(1),
  after_sample_count: z.number().int().min(0),
  after_success_rate: z.number().min(0).max(1),
  delta: z.number(),
  classification: z.enum(["effective", "regression", "neutral", "insufficient_data"]),
  confidence: z.enum(["insufficient_data", "low", "moderate"]),
  /** ISO 8601 */
  analyzed_at: z.string(),
});

export type SkillEffectivenessRecord = z.infer<typeof SkillEffectivenessRecordSchema>;
