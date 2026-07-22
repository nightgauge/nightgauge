import { z } from "zod";

/**
 * Schema for pipeline feedback signals and feedback-{N}.json context files.
 *
 * Feedback signals allow stage agents (feature-dev, feature-validate) to emit
 * structured backward signals to the orchestrator. The orchestrator can act on
 * blocking signals to trigger retries, backtrack to a prior stage, or escalate
 * the model.
 *
 * Schema versions:
 * - 1.0: Initial schema (issue #1341)
 * - 1.1: Added CONFLICT_RESOLUTION_NEEDED signal + ConflictContext (issue #4072)
 *
 * @see docs/CONTEXT_ARCHITECTURE.md — "Backward Edges & Feedback Signals" section
 * @see docs/FEEDBACK_LOOPS.md — CONFLICT_RESOLUTION_NEEDED + conflict-context-{N}.json
 */

// Signal type union — feedback signal types consumed by the rewind plumbing.
// CONFLICT_RESOLUTION_NEEDED (issue #4072) is emitted by pr-merge on an
// unresolvable rebase conflict and targets feature-dev: the dev stage checks
// out the EXISTING PR branch and resolves the conflict instead of the pipeline
// discarding the work via a blind fresh-branch restart.
// OPERATOR_STEER (ADR 015 §G) carries free-text operator steering typed on an
// Action Center DecisionRequest resolution. It is always `warning` severity
// with a null backtrack_target_stage, so it is pinned as next-stage CONTEXT and
// never triggers a rewind — the operator steer is background the next stage must
// honor, not a command. Provenance is marked in `evidence` ("operator-origin:
// action-center").
export const PipelineFeedbackSignalTypeSchema = z.enum([
  "PLAN_REVISION_NEEDED",
  "SCOPE_DISCOVERED",
  "COMPLEXITY_UNDERESTIMATED",
  "MODEL_ESCALATION_NEEDED",
  "ACCEPTANCE_CRITERIA_AMBIGUOUS",
  "CONFLICT_RESOLUTION_NEEDED",
  "OPERATOR_STEER",
]);
export type PipelineFeedbackSignalType = z.infer<typeof PipelineFeedbackSignalTypeSchema>;

// Stage identifiers matching the six pipeline stages
export const PipelineStageSchema = z.enum([
  "issue-pickup",
  "feature-planning",
  "feature-dev",
  "feature-validate",
  "pr-create",
  "pr-merge",
]);
export type PipelineStage = z.infer<typeof PipelineStageSchema>;

// Individual feedback signal
export const PipelineFeedbackSignalSchema = z.object({
  signal_type: PipelineFeedbackSignalTypeSchema,
  emitted_by_stage: PipelineStageSchema,
  /** Null for MODEL_ESCALATION_NEEDED (retries same stage rather than backtracking) */
  backtrack_target_stage: PipelineStageSchema.nullish(),
  rationale: z.string().min(1),
  evidence: z.array(z.string()),
  severity: z.enum(["warning", "blocking"]),
  timestamp: z.string().datetime().nullish(),
});
export type PipelineFeedbackSignal = z.infer<typeof PipelineFeedbackSignalSchema>;

// Array alias used as an optional field in DevContext / ValidateContext
export const PipelineFeedbackSchema = z.array(PipelineFeedbackSignalSchema);
export type PipelineFeedback = z.infer<typeof PipelineFeedbackSchema>;

// Standalone feedback-{N}.json schema (orchestrator-level cross-stage signals)
export const FeedbackContextSchema = z
  .object({
    schema_version: z.string().regex(/^\d+\.\d+$/),
    issue_number: z.number().int().positive(),
    signals: PipelineFeedbackSchema,
    created_at: z.string().datetime().nullish(),
  })
  .passthrough();
export type FeedbackContext = z.infer<typeof FeedbackContextSchema>;

// ============================================================================
// Conflict Context (Issue #4072)
//
// conflict-context-{N}.json is written by the pr-merge stage (merge.md Step
// 6.1.5) when a rebase hits a non-trivial conflict that the skill cannot
// resolve in-place. It captures the conflicting files and BOTH sides of each
// conflict (ours = the PR's feature work, theirs = the rebased base branch)
// BEFORE `git rebase --abort` discards the conflict state. The recovery loop
// pairs it with a CONFLICT_RESOLUTION_NEEDED feedback signal so feature-dev is
// re-dispatched on the SAME branch to resolve the conflict, rather than the
// whole branch being deleted for a fresh-branch restart.
// ============================================================================

// A single conflicting file with both sides of the conflict.
export const ConflictFileSchema = z.object({
  /** Repo-relative path of the conflicting file. */
  path: z.string().min(1),
  /** "ours" side blob — the PR branch's version (git show :2:<path>). */
  ours: z.string(),
  /** "theirs" side blob — the rebased base version (git show :3:<path>). */
  theirs: z.string(),
});
export type ConflictFile = z.infer<typeof ConflictFileSchema>;

// Standalone conflict-context-{N}.json schema. Extra fields are tolerated so a
// future capture can add hunk context without a schema bump.
export const ConflictContextSchema = z
  .object({
    schema_version: z.string().regex(/^\d+\.\d+$/),
    issue_number: z.number().int().positive(),
    pr_number: z.number().int().nonnegative(),
    branch: z.string().min(1),
    base_ref: z.string().min(1),
    conflicting_files: z.array(ConflictFileSchema).min(1),
    created_at: z.string().datetime().nullish(),
  })
  .passthrough();
export type ConflictContext = z.infer<typeof ConflictContextSchema>;

// ============================================================================
// Reviewer Feedback Signals (Issue #1409)
//
// Parsed from PR review comments after merge. Used by FeedbackLearningService
// to adjust complexity model pattern confidence based on human reviewer insights.
// ============================================================================

export const ReviewerSignalTypeSchema = z.enum([
  "SCOPE_UNDERESTIMATED",
  "APPROACH_MISMATCH",
  "VALIDATION_GAP",
  "COMPLEXITY_OVERESTIMATED",
  "ARCHITECTURE_DRIFT",
]);
export type ReviewerSignalType = z.infer<typeof ReviewerSignalTypeSchema>;

export const ReviewerSignalSchema = z.object({
  signal_type: ReviewerSignalTypeSchema,
  source_comment: z.string(),
  reviewer_login: z.string(),
  review_verdict: z.enum(["APPROVED", "CHANGES_REQUESTED", "COMMENTED"]),
  confidence: z.number().min(0).max(1),
  matched_keywords: z.array(z.string()),
});
export type ReviewerSignal = z.infer<typeof ReviewerSignalSchema>;
