import { z } from "zod";
import { normalizeDiscoveredPattern } from "./helpers";

/**
 * Schema for pattern mining results
 *
 * Produced by: /nightgauge-pattern-mining (utility skill)
 * Consumed by: /nightgauge-feature-planning (merged into planning context)
 * Read by: /nightgauge-feature-dev (via planning-{N}.json)
 *
 * @see docs/PATTERN_MINING.md for methodology
 * @see docs/CONTEXT_ARCHITECTURE.md for integration into planning-{N}.json
 */

/**
 * Pattern type classification.
 * - naming_convention: File/variable/function naming patterns
 * - structural: Directory organization and file placement patterns
 * - implementation_interface: Method signatures, return types, class patterns
 * - idiom: Recurring code idioms (builder pattern, factory, etc.)
 */
export const PatternTypeSchema = z.enum([
  "naming_convention",
  "structural",
  "implementation_interface",
  "idiom",
]);

export type PatternType = z.infer<typeof PatternTypeSchema>;

/**
 * A single discovered pattern with evidence from the codebase.
 *
 * The schema is intentionally permissive. LLM subagents have consistently
 * produced entries shaped like `{name, location, description}` rather than
 * the original canonical shape, generating large volumes of non-fatal
 * validation warnings in the pipeline output (see #2616, PR #2702).
 *
 * Strategy:
 *   1. `normalizeDiscoveredPattern` (preprocess) maps known LLM field-name
 *      variants to the canonical keys.
 *   2. Only `pattern` (the human-readable description) remains required —
 *      it's the one field that makes an entry useful. Everything else is
 *      optional with sensible defaults so partial output validates cleanly.
 *   3. The inner object is `.passthrough()` so unknown fields round-trip
 *      for downstream consumers that need them.
 *
 * Nothing in the codebase currently reads these fields by name — the
 * `patterns_found[]` array is passed through as informational context for
 * later prompts. If that changes, tighten the individual fields at the
 * consumer boundary, not here.
 */
export const DiscoveredPatternSchema = z.preprocess(
  normalizeDiscoveredPattern,
  z
    .object({
      /** Classification of the pattern */
      pattern_type: z.string().min(1).optional(),
      /** Sub-category within the pattern type */
      category: z.string().min(1).optional(),
      /** Human-readable description of the pattern (the one required field) */
      pattern: z.string().min(1),
      /** File paths where this pattern was observed */
      evidence: z.array(z.string().min(1)).optional(),
      /** Number of occurrences found across the codebase */
      frequency: z.number().int().min(1).optional(),
      /** File path + line range references for example implementations */
      example_implementations: z.array(z.string().min(1)).optional(),
    })
    .passthrough()
);

export type DiscoveredPattern = z.infer<typeof DiscoveredPatternSchema>;

/** A similar issue detected via pattern overlap */
export const SimilarIssueSchema = z.object({
  /** GitHub issue number */
  issue_number: z.number().int().positive(),
  /** Issue title */
  title: z.string().min(1),
  /** Relevance score based on pattern overlap (0.0 - 1.0) */
  relevance_score: z.number().min(0).max(1),
  /** Pattern categories that overlap with the current issue */
  pattern_overlap: z.array(z.string().min(1)),
  /** Path to the similar issue's plan file (if exists) */
  plan_file: z.string().nullish(),
});

export type SimilarIssue = z.infer<typeof SimilarIssueSchema>;

/** Counts of patterns found per classification */
export const PatternClassificationsSchema = z.object({
  naming_conventions: z.number().int().min(0),
  structural_patterns: z.number().int().min(0),
  interface_patterns: z.number().int().min(0),
  idioms: z.number().int().min(0),
});

export type PatternClassifications = z.infer<typeof PatternClassificationsSchema>;

/** Full output of the pattern mining skill */
export const PatternMiningResultSchema = z
  .object({
    /** Discovered codebase patterns with evidence */
    patterns_found: z.array(DiscoveredPatternSchema),
    /** Issues with similar pattern overlaps */
    similar_issues: z.array(SimilarIssueSchema),
    /** Summary counts by pattern classification */
    pattern_classifications: PatternClassificationsSchema,
    /** Search queries used during mining */
    search_queries_used: z.array(z.string().min(1)),
    /** Ratio of codebase covered by pattern evidence (0.0 - 1.0) */
    coverage_ratio: z.number().min(0).max(1),
    /** Estimated token cost of the pattern mining operation */
    token_cost_estimate: z.number().int().min(0),
    /** Actionable recommendations based on discovered patterns */
    recommendations: z.array(z.string().min(1)),
  })
  .passthrough();

export type PatternMiningResult = z.infer<typeof PatternMiningResultSchema>;

/** @deprecated Use PatternMiningResultSchema instead */
export const CompassResultSchema = PatternMiningResultSchema;
/** @deprecated Use PatternMiningResult instead */
export type CompassResult = PatternMiningResult;
