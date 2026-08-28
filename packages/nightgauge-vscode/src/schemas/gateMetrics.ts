/**
 * Zod schema for Quality Gate Metric records
 *
 * Defines the JSONL record format for per-gate telemetry written by
 * feature-validate during each validation run. Used to compute gate
 * hit-rates and surface ROI in PostPipelineAnalyzer and /pipeline-health.
 *
 * File path: .nightgauge/health/gate-metrics.jsonl
 *
 * @see Issue #1412 - Quality gate hit-rate metrics
 */

import { z } from "zod";

export const GateMetricRecordSchema = z.object({
  schema_version: z.literal("1"),
  timestamp: z.string(), // ISO 8601
  issue_number: z.number().int().positive(),
  /**
   * Quality gate that produced this record. The deterministic build/lint/test
   * gates emit a `"catch"` result on a defect; `"judges"` is the adversarial
   * anti-hallucination judge gate (#3918) whose verdict is folded in as a
   * `"fail"` result so the Go FeatureValidateGate.Verify() loop (which trips on
   * `result != "pass"`) consumes it with zero new Go scaffolding.
   *
   * NOT AN ENUM, deliberately (#1084). It was one, listing six names, while the
   * shipped feature-validate skill emitted `adversarial-review` and `verify-ui`
   * — neither on the list. The Go writer validates no name at all, so those
   * records were written happily and then dropped by `safeParse` on read, in
   * silence. The reader's only observable failure was a shorter list, which the
   * renderer turned into `Gates: N/A` — indistinguishable from "no gates ran".
   *
   * The producer here is a MARKDOWN SKILL, not a typed call site: nothing makes
   * a new gate name in a skill fail a build, so a closed enum in the reader can
   * only ever discover the mismatch at runtime, after the record is lost. A
   * non-empty string keeps the record and lets the tally name gates this schema
   * has never heard of, which is the honest model of that boundary.
   */
  gate_name: z.string().min(1),
  /**
   * - "pass"  — gate ran and found no defects (every gate).
   * - "catch" — a deterministic gate found defects (build/lint/test).
   * - "fail"  — an adversarial judge rejected a "done" claim (`judges` gate).
   *
   * The Go gate treats anything other than "pass" as a quality-gate failure, so
   * both "catch" and "fail" trip it.
   */
  result: z.enum(["pass", "catch", "fail"]),
  /** Issue type extracted from planning context (feature | bug | refactor | docs) */
  issue_type: z.string().nullish(),
  /** Complexity label from planning context (XS | S | M | L | XL) */
  complexity_label: z.string().nullish(),
  /** Gate execution duration in milliseconds */
  duration_ms: z.number().int().min(0).nullish(),
  /** First line of error output; populated only when result="catch" */
  error_summary: z.string().nullish(),
});

export type GateMetricRecord = z.infer<typeof GateMetricRecordSchema>;
