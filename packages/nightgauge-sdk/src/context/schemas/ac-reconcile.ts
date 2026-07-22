import { z } from "zod";

/**
 * Schema for `.nightgauge/pipeline/ac-reconcile-{N}.json`.
 *
 * Output of the deterministic AC reconciliation pre-flight (Issue #3003).
 * Same schema is embedded under `planning.ac_reconcile` (PlanningContextSchema 1.6+).
 */
export const ACReconcileContextSchema = z.object({
  schema_version: z.literal("1.0"),
  issue_number: z.number().int().positive(),
  /** Output of `git rev-parse main` at evaluation time. */
  main_sha: z.string().min(1),
  evaluated_at: z.string().datetime(),
  acceptance_criteria: z.array(
    z.object({
      index: z.number().int().min(0),
      text: z.string(),
      checkbox_state: z.enum(["checked", "unchecked"]),
      /** null when no rule matched (classification will be `undetectable`). */
      rule_applied: z.string().nullable(),
      classification: z.enum(["satisfied", "partial", "unsatisfied", "undetectable"]),
      reason: z.string(),
      evidence: z.array(z.string()).default([]),
    })
  ),
  aggregate_status: z.enum([
    "all-satisfied",
    "mostly-satisfied",
    "partial",
    "unsatisfied",
    "undetectable",
    "no-acs-detected",
  ]),
  suggested_route: z.object({
    approach: z.enum(["verify-and-close", "narrow-scope", "standard"]),
    /** 0-based indices into acceptance_criteria the planner should focus on. */
    focus_acs: z.array(z.number().int().min(0)).default([]),
    rationale: z.string(),
  }),
});

export type ACReconcileContext = z.infer<typeof ACReconcileContextSchema>;
