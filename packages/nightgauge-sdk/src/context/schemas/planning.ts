import { z } from "zod";
import { DiscoveredPatternSchema } from "./pattern-mining";
import { ACReconcileContextSchema } from "./ac-reconcile.js";
import { flexEnum, normalizePatternClassifications, optionalString } from "./helpers.js";

/**
 * Schema for planning-{N}.json context files
 *
 * Schema version history:
 * - 1.0: Initial schema
 * - 1.1: Added decisions and docs_consulted fields
 * - 1.2: Added revision_count and revision_reasons fields
 * - 1.3: Added optional knowledge_path and knowledge_entries fields (Issue #1679)
 * - 1.4: Added optional cross_repo_knowledge field (Issue #1700)
 * - 1.5: Added optional pattern_mining_results field (Issue #20)
 * - 1.6: Added optional ac_reconcile field (Issue #3003)
 * - 1.7: Added optional recalled_decisions field (Issue #3593)
 * - 1.8: Added optional knowledge_read field (Issue #2964) — KB files read during planning
 *
 * Created by: /nightgauge-feature-planning
 * Read by: /nightgauge-feature-dev
 *
 * @see docs/CONTEXT_ARCHITECTURE.md for field documentation
 */
export const PlanningContextSchema = z
  .object({
    /** Current schema version is 1.8 (see history above). */
    schema_version: z.string().regex(/^\d+\.\d+$/),
    issue_number: z.number().int().positive(),
    plan_file: z.string().min(1),
    /**
     * Selected implementation approach.
     *
     * Special signal value: `"verify-and-close"` — indicates the issue is already
     * resolved and the pipeline should short-circuit remaining stages.
     * When used, `files_to_create` and `files_to_modify` MUST be empty arrays.
     * @see Issue #708
     */
    approach: z.string().min(1),
    /**
     * Files to create as part of the implementation.
     *
     * Nullish for spike tasks (research/investigation) that produce documentation
     * rather than code files. feature-dev treats null the same as [].
     * @see Issue #2616 — spike-specific planning schema variant
     */
    files_to_create: z.array(z.string()).nullish(),
    /**
     * Files to modify as part of the implementation.
     *
     * Nullish for spike tasks (research/investigation) that produce documentation
     * rather than code files. feature-dev treats null the same as [].
     * @see Issue #2616 — spike-specific planning schema variant
     */
    files_to_modify: z.array(z.string()).nullish(),
    patterns_applied: z.record(z.string(), z.string()).nullish(),
    dependencies: z
      .object({
        runtime: z.array(z.string()).nullish(),
        dev: z.array(z.string()).nullish(),
      })
      .nullish(),
    coverage_baseline: z
      .object({
        statements: z.number().min(0).max(100).nullish(),
        branches: z.number().min(0).max(100).nullish(),
        lines: z.number().min(0).max(100).nullish(),
      })
      .nullish(),
    complexity_assessment: z
      .object({
        /** Size label from issue (XS/S/M/L/XL) */
        size_label: z.string().nullish(),
        /** Issue type (feature/bug/docs/refactor/chore) */
        type_label: z.string().nullish(),
        /** Priority label (critical/high/medium/low) */
        priority_label: z.string().nullish(),
        /** Fibonacci complexity score (1/2/3/5/8) */
        computed_score: z.number().int().min(1).max(8).nullish(),
        /** Documentation scope to use */
        documentation_scope: flexEnum([
          "minimal",
          "targeted",
          "standard",
          "extended",
        ] as const).nullish(),
        /** Human-readable explanation of the decision */
        rationale: optionalString(),
        /** Estimated token savings vs standard scope (0 if standard/extended) */
        estimated_token_savings: z.number().int().min(0).nullish(),
      })
      .nullish(),
    /** Architectural decisions made during planning (v1.1+)
     * AI agents produce varying shapes:
     *   { topic, options, selection, rationale } — canonical object form
     *   { decision, rationale }                 — common AI shorthand
     *   "plain string description"              — simplest AI shorthand
     * Schema accepts all three via z.union + passthrough(). */
    decisions: z
      .array(
        z.union([
          z.string(),
          z
            .object({
              /** Decision topic (e.g., "Storage Backend") */
              topic: optionalString(),
              /** Options that were considered */
              options: z.array(z.string()).nullish(),
              /** Selected option */
              selection: optionalString(),
              /** Shorthand decision text (AI-generated alternative to topic) */
              decision: optionalString(),
              /** Reason for the selection */
              rationale: optionalString(),
            })
            .passthrough(),
        ])
      )
      .nullish(),
    /** Documentation discovery results (v1.1+) */
    docs_consulted: z
      .object({
        /** Discovery method used */
        discovery_method: z.enum(["keyword-matched", "scope-fallback", "extended-all"]),
        /** Keywords extracted from issue content */
        keywords_extracted: z.array(z.string()),
        /** Files successfully read */
        files_read: z.array(
          z.object({
            path: z.string(),
            /** e.g., "keyword match: security" or "essential doc" or "scope: standard" */
            reason: z.string(),
          })
        ),
        /** Files that were skipped */
        files_skipped: z
          .array(
            z.object({
              path: z.string(),
              /** e.g., "no keyword match" or "not in priority list" */
              reason: z.string(),
            })
          )
          .nullish(),
        /** Estimated tokens saved vs reading all docs */
        estimated_tokens_saved: z.number().int().min(0),
      })
      .nullish(),
    /**
     * Number of prior plan attempts for this issue.
     * 0 on the first run. Populated from feedback-{N}.json on revision runs.
     * Enables the dashboard to display "Plan Revision N/N" (v1.2+)
     */
    revision_count: z.number().int().min(0).nullish(),
    /**
     * Evidence strings collected from all feedback signals on revision runs.
     * Empty array on first run. Populated from feedback-{N}.json (v1.2+)
     */
    revision_reasons: z.array(z.string()).nullish(),
    /** Path to the knowledge directory for this issue (v1.3+).
     * Relative to workspace root: .nightgauge/knowledge/features/{N}-slug/
     */
    knowledge_path: z.string().nullish(),
    /**
     * List of markdown filenames (basenames only, not paths) present in the knowledge
     * directory when planning ran (v1.3+).
     *
     * Example: `["PRD.md", "decisions.md"]`
     *
     * The directory path is provided separately in `knowledge_path`. This field
     * is used by downstream stages to determine which knowledge files to read
     * without re-scanning the filesystem.
     */
    knowledge_entries: z.array(z.string()).nullish(),
    /**
     * Pattern mining results from pattern mining subagent (v1.5+).
     *
     * Populated by Phase 2.5 (Pattern Mining) when the pattern mining subagent
     * discovers existing codebase patterns relevant to the issue.
     * Null or absent when pattern mining was skipped or returned no results.
     *
     * @see docs/PATTERN_MINING.md for methodology
     * @see packages/nightgauge-sdk/src/context/schemas/pattern-mining.ts for schema
     */
    pattern_mining_results: z
      .object({
        /**
         * LLM agents may omit patterns_found entirely or return an empty
         * array. Entries follow the shared, alias-tolerant
         * `DiscoveredPatternSchema` — see `./pattern-mining.ts` for the
         * rationale behind the loose shape.
         */
        patterns_found: z.array(DiscoveredPatternSchema).nullish(),
        /** LLM agents often return bare strings (e.g., "#42") instead of full objects */
        similar_issues: z
          .array(
            z.union([
              z.string(),
              z
                .object({
                  issue_number: z.number().int().positive(),
                  title: z.string().min(1),
                  relevance_score: z.number().min(0).max(1),
                  pattern_overlap: z.array(z.string().min(1)),
                  plan_file: z.string().nullish(),
                })
                .passthrough(),
            ])
          )
          .nullish(),
        /** LLM agents may omit classification counts or use wrong key names */
        pattern_classifications: z
          .preprocess(
            normalizePatternClassifications,
            z
              .object({
                naming_conventions: z.number().int().min(0),
                structural_patterns: z.number().int().min(0),
                interface_patterns: z.number().int().min(0),
                idioms: z.number().int().min(0),
              })
              .passthrough()
          )
          .nullish(),
        recommendations: z.array(z.string().min(1)).nullish(),
      })
      .passthrough()
      .nullish(),
    /**
     * Cross-repository knowledge entries read during planning (v1.4+).
     *
     * Populated when a workspace config lists sibling repos with knowledge bases
     * relevant to this issue. Read-only — pipeline never writes to sibling repos.
     * Empty or absent when no workspace config is found or no sibling knowledge exists.
     */
    cross_repo_knowledge: z
      .array(
        z.object({
          /** Repository name from workspace config (e.g., "acme-platform") */
          repo: z.string().min(1),
          /** Absolute or workspace-relative path to the sibling repo's knowledge directory */
          path: z.string().min(1),
          /** Markdown filenames read from the sibling repo's knowledge directory */
          entries: z.array(z.string()),
        })
      )
      .nullish(),
    /**
     * Deterministic AC reconciliation report (v1.6+, Issue #3003).
     *
     * Populated when the feature-planning ac-reconcile sub-step ran. Null or
     * absent when the issue body had no checkboxes, or when the binary was
     * unavailable. Same shape as `.nightgauge/pipeline/ac-reconcile-{N}.json`.
     */
    ac_reconcile: ACReconcileContextSchema.nullish(),
    /**
     * Prior decisions retrieved from the knowledge base via recall before plan
     * generation (v1.7+, Issue #3593).
     *
     * Each entry is one RecallHit from `nightgauge knowledge recall --json`.
     * Null or absent when recall was skipped (knowledge disabled, no KB entries,
     * or recall errored).
     */
    recalled_decisions: z
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
    /**
     * KB files the feature-planning stage read during plan generation (v1.8+,
     * Issue #2964). Workspace-relative paths. VSCode's KnowledgeTreeProvider
     * highlights these entries under "Active Issue" so developers can see
     * which knowledge the agent already consumed.
     */
    knowledge_read: z.array(z.string()).nullish(),
    created_at: z.string().datetime(),
  })
  // AI agents may include extra fields not in the schema (title, type,
  // complexity, requirements, files_to_read, validation, stage, status).
  // passthrough() prevents those from causing validation failures.
  .passthrough();

export type PlanningContext = z.infer<typeof PlanningContextSchema>;
