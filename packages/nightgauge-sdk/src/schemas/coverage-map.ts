import { z } from "zod";

/**
 * Schema for coverage-map-{N}.json pipeline artifacts
 *
 * Created by: feature-validate Phase 2.6 (knowledge coverage check)
 * Read by: knowledge render-pr-section --coverage-map
 *
 * Schema versions:
 * - 1.0: Initial schema (issue #3595)
 *
 * @see docs/CONTEXT_ARCHITECTURE.md for field documentation
 */

export const CriteriaCoverageSchema = z.object({
  text: z.string().min(1),
  evidence: z.array(z.string()),
  status: z.enum(["covered", "no_evidence"]),
});

export const ViolationSchema = z.object({
  constraint: z.string().min(1),
  violating_files: z.array(z.string()),
  severity: z.literal("warn"),
});

export const CoverageMapSchema = z
  .object({
    issue: z.number().int().positive(),
    criteria: z.array(CriteriaCoverageSchema),
    violations: z.array(ViolationSchema),
    created_at: z.string().datetime().nullish(),
  })
  // AI agents may include extra fields. passthrough() prevents
  // unknown properties from causing validation failures.
  .passthrough();

export type CriteriaCoverage = z.infer<typeof CriteriaCoverageSchema>;
export type Violation = z.infer<typeof ViolationSchema>;
export type CoverageMap = z.infer<typeof CoverageMapSchema>;
