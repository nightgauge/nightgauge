import { z } from "zod";
import { flexEnum, optionalString } from "./helpers.js";

/**
 * Change type for routing decisions
 *
 * Uses flexEnum for coercion of common AI agent deviations:
 * - "code_change", "code_modification" → "code"
 * - "documentation", "doc" → "docs"
 * - "configuration", "conf" → "config"
 *
 * @see packages/nightgauge-sdk/src/context/schemas/helpers.ts — AGENT_ALIASES
 */
export const ChangeTypeSchema = flexEnum(["docs", "config", "code"] as const);
export type ChangeType = z.infer<typeof ChangeTypeSchema>;

/**
 * Size labels from GitHub issue
 */
export const SizeLabelSchema = z.enum(["XS", "S", "M", "L", "XL"]).nullable();
export type SizeLabel = z.infer<typeof SizeLabelSchema>;

/**
 * Routing path options
 *
 * Uses flexEnum for coercion of common AI agent deviations:
 * - "trivial_route", "quick", "simple" → "trivial"
 * - "extensive_route", "complex", "deep" → "extensive"
 *
 * @see packages/nightgauge-sdk/src/context/schemas/helpers.ts — AGENT_ALIASES
 */
export const RoutingPathSchema = flexEnum(["trivial", "standard", "extensive"] as const);
export type RoutingPath = z.infer<typeof RoutingPathSchema>;

/**
 * Skippable pipeline stages
 *
 * Note: 'issue-pickup' and 'feature-dev' are never skippable:
 * - issue-pickup creates the context file needed by all other stages
 * - feature-dev is where the actual work happens
 *
 * @see Issue #268 - Task-Type Routing (expanded from 2 to 4 stages)
 * @see Issue #418 - Pipeline routing completion
 */
export const SkippableStageSchema = z.enum([
  "feature-planning",
  "feature-validate",
  "pr-create",
  "pr-merge",
]);
export type SkippableStage = z.infer<typeof SkippableStageSchema>;

/**
 * Routing information for complexity-based stage routing
 *
 * @see Issue #216 - Complexity-Based Stage Routing
 * @see docs/CONTEXT_ARCHITECTURE.md for field documentation
 */
export const RoutingSchema = z.object({
  /** Detected change type */
  change_type: ChangeTypeSchema,
  /**
   * Computed complexity score (Fibonacci: 1/2/3/5/8).
   *
   * Clamped to the Zod max of 8. The Go complexity estimator uses a 1-10 scale;
   * values above 8 are coerced to 3 (M size) as a safe default.
   * The .catch(3) fallback also handles missing or non-numeric values.
   */
  complexity_score: z.number().int().min(1).max(8).catch(3),
  /** Suggested routing path */
  suggested_route: RoutingPathSchema,
  /** Stages to skip based on routing */
  skip_stages: z.array(SkippableStageSchema),
  /**
   * Human-readable rationale for routing decision.
   * Defaults to empty string if agent omits this field.
   */
  rationale: z.string().catch(""),
  /** Estimated time in minutes based on route (defaults to 15 if omitted by AI) */
  estimated_time_minutes: z.number().int().min(0).catch(15),
  /**
   * True when label-based risk classification forced the full pipeline +
   * extensive route regardless of complexity_score.
   *
   * @see Issue #4093 - Risk dimension forces the extensive route
   */
  risk_high: z.boolean().catch(false),
  /**
   * Label slugs that triggered the high-risk classification. Consumed by the
   * per-repo discipline score (#4100); empty when risk_high is false.
   *
   * @see Issue #4093 - Risk dimension forces the extensive route
   */
  risk_reasons: z.array(z.string()).catch([]),
});
export type Routing = z.infer<typeof RoutingSchema>;

/**
 * Sub-issue progress tracking
 *
 * @see Issue #38 - GitHub Sub-Issues Integration
 */
export const SubIssueProgressSchema = z.object({
  /** Number of open sub-issues */
  open: z.number().int().min(0),
  /** Number of closed sub-issues */
  closed: z.number().int().min(0),
  /** Total number of sub-issues */
  total: z.number().int().min(0),
});
export type SubIssueProgress = z.infer<typeof SubIssueProgressSchema>;

/**
 * Schema for issue-{N}.json context files
 *
 * Schema version history:
 * - 1.0: Initial schema
 * - 1.1: Added routing field for complexity-based stage routing (Issue #216)
 * - 1.3: Added sub-issue fields for GitHub native sub-issues integration (Issue #38)
 * - 1.4: Added 'spike' type for research/investigation tasks (Issue #168)
 * - 1.5: Added optional knowledge_path field (Issue #1679)
 * - 1.6: Added routing.risk_high / routing.risk_reasons (Issue #4093)
 *
 * Created by: /nightgauge-issue-pickup
 * Read by: /nightgauge-feature-planning
 *
 * @see docs/CONTEXT_ARCHITECTURE.md for field documentation
 */
export const IssueContextSchema = z
  .object({
    schema_version: z.string().regex(/^\d+\.\d+$/),
    // Agents occasionally omit issue_number — coerce string→number, default 0
    issue_number: z
      .preprocess(
        (val) => (typeof val === "string" ? Number(val) : val),
        z.number().int().nonnegative()
      )
      .catch(0),
    title: z.string().catch("Untitled"),
    type: z
      .preprocess(
        (val) => {
          if (typeof val !== "string") return val;
          // Normalize common agent deviations
          const lower = val.toLowerCase().trim();
          const aliases: Record<string, string> = {
            bugfix: "bug",
            fix: "bug",
            hotfix: "bug",
            documentation: "docs",
            doc: "docs",
            research: "spike",
            investigation: "spike",
            maintenance: "chore",
            cleanup: "chore",
            enhancement: "feature",
          };
          return aliases[lower] ?? lower;
        },
        z.enum(["feature", "bug", "docs", "refactor", "spike", "chore"])
      )
      .catch("feature"),
    // Agents sometimes write branch as {name: "feat/..."} instead of a string
    branch: z
      .preprocess((val) => {
        if (val && typeof val === "object" && "name" in (val as Record<string, unknown>)) {
          return (val as Record<string, unknown>).name;
        }
        return val;
      }, z.string().min(1))
      .catch("main"),
    base_branch: z.string().min(1).catch("main"),
    requirements: z.preprocess(
      // AI agents occasionally omit the requirements object entirely.
      // Default to empty object so sub-fields resolve to undefined
      // (valid — they are all nullish).
      (val) => (val === undefined || val === null ? {} : val),
      z.object({
        summary: optionalString(),
        user_story: z.string().nullish(),
        acceptance_criteria: z.array(z.string()).nullish(),
        // Agents write technical_notes as a string, object, or array of strings.
        // Coerce all forms into string[] | null.
        technical_notes: z.preprocess((val) => {
          if (typeof val === "string") return [val];
          if (val && typeof val === "object" && !Array.isArray(val)) {
            return Object.values(val).filter((v) => typeof v === "string");
          }
          return val;
        }, z.array(z.string()).nullish()),
      })
    ),
    labels: z
      .preprocess(
        (val) => {
          // AI sometimes writes labels as an object instead of an array,
          // or omits it entirely. Coerce to array.
          if (val === undefined || val === null) return [];
          if (val && typeof val === "object" && !Array.isArray(val)) {
            const obj = val as Record<string, unknown>;
            // If it has a 'name' key, it's a single label object — wrap it
            if ("name" in obj) return [obj];
            // Otherwise extract values (e.g., { "0": "label1", "1": "label2" })
            return Object.values(obj);
          }
          return val;
        },
        z.array(z.union([z.string(), z.object({ name: z.string() }).passthrough()]))
      )
      .catch([]),
    milestone: z
      .union([
        z.string(),
        z.object({
          number: z.number().int().positive(),
          title: z.string(),
          due_on: z.string().datetime().nullish(),
        }),
      ])
      .nullish(),
    parent_issue: z.number().int().positive().nullish(),
    /** Child issue numbers from GitHub native sub-issues API (v1.3+) */
    child_issues: z.array(z.number().int().positive()).nullish(),
    /** Progress statistics for child issues (v1.3+) */
    sub_issue_progress: SubIssueProgressSchema.nullish(),
    /** Parent issue number from GitHub native sub-issues API (v1.3+) */
    native_parent: z.number().int().positive().nullish(),
    /** Routing information for complexity-based stage routing (v1.1+) */
    routing: RoutingSchema.nullish(),
    /**
     * Risk-tiering facts emitted by feature-planning for the architecture
     * approval gate (#4135). `null` until feature-planning runs; the approval
     * gate treats absence as no-trigger (never over-fires).
     */
    dependency_analysis: z
      .object({
        /** Count of dependency MAJOR-version bumps the change introduces. */
        major_bumps_count: z.number().int().nonnegative(),
        /** True when the change touches production-affecting surfaces. */
        production_area: z.boolean(),
      })
      .passthrough()
      .nullish(),
    /** Path to the knowledge directory for this issue, relative to workspace root (v1.5+).
     * Format: .nightgauge/knowledge/features/{N}-slug/
     */
    knowledge_path: z.string().nullish(),
    created_at: z.string().datetime().nullish(),
    created_by: z.string().nullish(),
  })
  // AI agents may include extra fields (e.g., _deterministic, repository,
  // dependencies). passthrough() prevents unknown properties from
  // causing validation failures.
  .passthrough();

export type IssueContext = z.infer<typeof IssueContextSchema>;
