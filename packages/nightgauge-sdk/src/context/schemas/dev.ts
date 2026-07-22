import { z } from "zod";
import { PipelineFeedbackSchema } from "./feedback.js";
import { flexEnum, optionalString } from "./helpers.js";

/**
 * Schema for dev-{N}.json context files
 *
 * Created by: /nightgauge-feature-dev
 * Read by: /nightgauge-pr-create, /nightgauge-feature-validate
 *
 * Schema versions:
 * - 1.0: Initial schema
 * - 1.1: Added build_verification, extended tests_status and quality_checks (issue #867)
 * - 1.2: Added optional feedback field for backward pipeline signals (issue #1341)
 * - 1.3: Added retry_count and retry_reasons for feedback consumption on re-run (issue #1347)
 * - 1.4: commit_sha now always null — commit deferred to feature-validate (issue #1608)
 * - 1.5: Added optional knowledge_path field (Issue #1679)
 * - 1.6: Added optional cross_repo_knowledge field (Issue #1700)
 * - 1.7: Added e2e_framework and e2e_tests_generated to tests_status (Issue #9)
 * - 1.8: Added optional architectural_constraints field (Issue #3594)
 *
 * @see docs/CONTEXT_ARCHITECTURE.md for field documentation
 */
export const DevContextSchema = z
  .object({
    schema_version: z.string().regex(/^\d+\.\d+$/),
    issue_number: z.number().int().positive(),
    commit_sha: optionalString(),
    files_changed: z
      .object({
        created: z.array(z.string()),
        modified: z.array(z.string()),
        deleted: z.array(z.string()),
      })
      .nullish(),
    build_verification: z
      .object({
        ran: z.boolean().nullish(),
        status: flexEnum(["passed", "failed", "skipped"] as const).nullish(),
        commands_run: z.array(z.string()).nullish(),
        timestamp: z.string().datetime().nullish(),
      })
      .nullish(),
    tests_status: z
      .object({
        passed: z.number().int().min(0).nullish(),
        failed: z.number().int().min(0).nullish(),
        coverage: z.number().min(0).max(100).nullish(),
        test_command: optionalString(),
        includes_integration: z.boolean().nullish(),
        includes_e2e: z.boolean().nullish(),
        test_files_run: z.number().int().min(0).nullish(),
        /** E2E framework detected and used during feature-dev (v1.7+) */
        e2e_framework: z.string().nullish(),
        /** Whether E2E test suggestions were generated for UI changes (v1.7+) */
        e2e_tests_generated: z.boolean().nullish(),
      })
      .nullish(),
    quality_checks: z
      .object({
        code_standards: flexEnum(["passed", "failed", "skipped"] as const).nullish(),
        security_review: flexEnum(["passed", "failed", "skipped"] as const).nullish(),
        type_check: flexEnum(["passed", "failed", "skipped"] as const).nullish(),
        dead_code_scan: flexEnum(["passed", "failed", "not_run", "skipped"] as const).nullish(),
      })
      .nullish(),
    created_at: z.string().datetime().nullish(),
    /** Backward pipeline signals emitted during feature-dev (v1.2+) */
    feedback: PipelineFeedbackSchema.nullish(),
    /** Number of times dev has been retried for this issue — 0 for first run (v1.3+) */
    retry_count: z.number().int().min(0).nullish(),
    /** Evidence strings from validate feedback that triggered the retry (v1.3+) */
    retry_reasons: z.array(z.string()).nullish(),
    /** Path to the knowledge directory for this issue (v1.5+).
     * Relative to workspace root: .nightgauge/knowledge/features/{N}-slug/
     */
    knowledge_path: z.string().nullish(),
    /**
     * Cross-repo knowledge entries from planning context, threaded to dev
     * for implementation context (v1.6+).
     *
     * Copied from planning-{N}.json. Read-only — never written to sibling repos.
     * Empty or absent when planning found no sibling knowledge.
     */
    cross_repo_knowledge: z
      .array(
        z.object({
          /** Repository name from workspace config */
          repo: z.string().min(1),
          /** Workspace-relative path to the sibling repo's knowledge directory */
          path: z.string().min(1),
          /** Markdown filenames from the sibling repo's knowledge directory */
          entries: z.array(z.string()),
        })
      )
      .nullish(),
    /**
     * ADRs recalled from the knowledge base that constrain implementation for
     * the files being modified. Same RecallHit shape as recalled_decisions in
     * planning context. Null when recall was skipped or returned no hits above
     * dev_threshold. Written by Phase 1.6 of feature-dev. (v1.8+)
     */
    architectural_constraints: z
      .array(
        z
          .object({
            rank: z.number().int().positive(),
            score: z.number(),
            path: z.string(),
            kind: z.string(),
            issue_number: z.number().int().optional(),
            tags: z.array(z.string()).optional(),
            snippet: z.string(),
            graduated: z.boolean().optional(),
          })
          .passthrough()
      )
      .nullish(),
  })
  // AI agents may include extra fields. passthrough() prevents
  // unknown properties from causing validation failures.
  .passthrough();

export type DevContext = z.infer<typeof DevContextSchema>;
