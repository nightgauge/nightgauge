/**
 * Post-Merge Survival Outcome Model Schemas
 *
 * Mirrors internal/intelligence/survival/record.go (#4151, spike #4134):
 * field names match the Go struct's JSON tags exactly (snake_case) so a
 * survival record round-trips unchanged between the Go-written
 * `.nightgauge/pipeline/survival-records.jsonl` journal and TS readers.
 *
 * This schema module is capture/detection-agnostic — it only describes the
 * record shape. Calibration (#4152/#4153) lives in OutcomeRecorder.ts and
 * SurvivalCalibrationSchema below, which augments the complexity model's
 * prediction_accuracy section.
 *
 * @see docs/spikes/4134-post-merge-survival-outcome-model.md
 */

import { z } from "zod";

/**
 * Survival Verdict
 *
 * The lifecycle state of a survival record. Only `reverted`, `broke`,
 * `survived`, and `unobserved` are terminal; `pending` carries no signal.
 */
export const SurvivalVerdictSchema = z.enum([
  "pending",
  "survived",
  "reverted",
  "broke",
  "unobserved",
]);
export type SurvivalVerdict = z.infer<typeof SurvivalVerdictSchema>;

/**
 * Survival Record
 *
 * A single post-merge survival observation, keyed by the merge commit SHA
 * (the stable join key from #4133, independent of branch deletion / PR
 * renumbering).
 */
export const SurvivalRecordSchema = z.object({
  /** Discriminator — always "survival" */
  kind: z.string(),
  /** Join key (#4133) */
  merge_commit_sha: z.string().min(1),
  issue_number: z.number().int().positive(),
  pr_number: z.number().int().positive(),
  /** "owner/name" */
  repo: z.string(),
  /** Base branch the merge landed on (default "main") */
  base_ref: z.string(),
  /** ISO-8601 (#4133) */
  merged_at: z.string(),
  verdict: SurvivalVerdictSchema,
  /** When finalized (RFC3339) */
  observed_at: z.string().optional(),
  /** One of the reverts-commit / ancestry-ci-failure / window-elapsed-* evidence strings */
  evidence: z.string().optional(),
});
export type SurvivalRecord = z.infer<typeof SurvivalRecordSchema>;

/**
 * Survival Calibration
 *
 * Bias-safe calibration state derived from finalized survival verdicts
 * (#4152 penalize-reverts, #4153 weak-reward-survived; spike #4134 §1.2).
 * Mirrors internal/github/outcome_survival.go's survivalCalibration struct
 * field-for-field (snake_case to match the YAML the Go binary writes to the
 * same complexity-model.yaml).
 *
 * `confidence` starts at a neutral prior (0.5) rather than a ceiling — see
 * outcome_survival.go's doc comment for why a maxed-out starting point would
 * make the weak reward permanently unobservable.
 */
export const SurvivalCalibrationSchema = z.object({
  confidence: z.number().min(0).max(1),
  /** Cumulative finalized reverted+broke observations */
  negative_observations: z.number().int().nonnegative().default(0),
  /** Cumulative finalized survived observations */
  positive_observations: z.number().int().nonnegative().default(0),
  /** Times the penalty actually fired (post-gate) */
  penalties_applied: z.number().int().nonnegative().default(0),
  /** Times the weak reward actually fired (post-gate) */
  rewards_applied: z.number().int().nonnegative().default(0),
  /** Dedup ledger of merge_commit_sha already folded into calibration, bounded */
  processed_shas: z.array(z.string()).default([]),
});
export type SurvivalCalibration = z.infer<typeof SurvivalCalibrationSchema>;
