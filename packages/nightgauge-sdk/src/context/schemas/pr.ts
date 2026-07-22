import { z } from "zod";
import { flexEnum } from "./helpers.js";

/**
 * Schema for pr-{N}.json context files
 *
 * Schema version history:
 * - 1.0: Initial schema
 * - 1.1: Added optional knowledge_path field (Issue #1679)
 * - 1.2: Added optional retrospective_feedback field (Issue #14)
 * - 1.3: Added scope_drift_check to preflight_results (Issue #3040)
 * Note: schema_version literal remains "1.0" — all additions are backwards-compatible optional fields.
 *
 * Created by: /nightgauge-pr-create
 * Read by: /nightgauge-pr-merge
 *
 * @see docs/CONTEXT_ARCHITECTURE.md for field documentation
 */
export const PRContextSchema = z
  .object({
    schema_version: z.literal("1.0"),
    issue_number: z.number().int().positive(),
    pr_number: z.number().int().positive(),
    pr_url: z.string().url(),
    title: z.string().min(1),
    base_branch: z.string().min(1),
    status: z.enum(["open", "draft", "closed", "merged"]),
    reviewers: z.array(z.string()),
    preflight_results: z
      .object({
        json_validation: flexEnum(["passed", "failed", "skipped"] as const).nullish(),
        yaml_validation: flexEnum(["passed", "failed", "skipped"] as const).nullish(),
        version_consistency: flexEnum(["passed", "failed", "skipped"] as const).nullish(),
        security_scan: flexEnum(["passed", "failed", "skipped"] as const).nullish(),
        coverage_check: flexEnum(["passed", "failed", "skipped"] as const).nullish(),
        /** Scope-drift gate result for type:docs / type:chore issues (v1.3+, Issue #3040). */
        scope_drift_check: flexEnum(["passed", "failed", "skipped"] as const).nullish(),
      })
      .nullish(),
    created_at: z.string().datetime().nullish(),
    /** Path to the knowledge directory for this issue (v1.1+).
     * Relative to workspace root: .nightgauge/knowledge/features/{N}-slug/
     */
    knowledge_path: z.string().nullish(),
    /** Retrospective feedback captured after merge (v1.2+, Issue #14).
     * Optional — only present when user provided feedback in VSCode interactive mode.
     */
    retrospective_feedback: z
      .object({
        what_went_well: z.array(z.string()).optional(),
        what_could_improve: z.array(z.string()).optional(),
        captured_at: z.string().datetime(),
        execution_mode: z.enum(["interactive", "headless", "unknown"]),
      })
      .nullish(),
  })
  // AI agents may include extra fields. passthrough() prevents
  // unknown properties from causing validation failures.
  .passthrough();

export type PRContext = z.infer<typeof PRContextSchema>;

/** Retrospective feedback captured after a PR merge (v1.2+, Issue #14). */
export type RetrospectiveFeedback = NonNullable<PRContext["retrospective_feedback"]>;
