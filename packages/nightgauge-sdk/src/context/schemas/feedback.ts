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
 * - 1.2: Added NOT_PIPELINE_ACTIONABLE signal (issue #1241)
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
// NOT_PIPELINE_ACTIONABLE (issue #1241) is the declaration that an issue's
// deliverable is not something ANY agent lap can produce — counsel sign-off, a
// credential only an operator holds, a physical or legal act, a decision
// reserved to a human. It is categorically different from every other type
// here, all of which describe a run that went wrong: this one describes a run
// that should never have been dispatched. The pipeline is not failing; the work
// is simply not pipeline work.
//
// It is deliberately NOT a variant of ACCEPTANCE_CRITERIA_AMBIGUOUS. Ambiguity
// is answerable — a human settles the criterion and the SAME issue becomes
// implementable. This is not: no answer makes the deliverable agent-producible,
// so the remedy is to take the issue out of the dispatch pool permanently
// (`owner-action`) rather than to ask a question and wait.
//
// `backtrack_target_stage` is null on this type by definition, and
// readFeedbackSignals admits it on that basis — see TERMINAL_BLOCKING_SIGNAL_TYPES
// in HeadlessOrchestrator.
export const PipelineFeedbackSignalTypeSchema = z.enum([
  "PLAN_REVISION_NEEDED",
  "SCOPE_DISCOVERED",
  "COMPLEXITY_UNDERESTIMATED",
  "MODEL_ESCALATION_NEEDED",
  "ACCEPTANCE_CRITERIA_AMBIGUOUS",
  "CONFLICT_RESOLUTION_NEEDED",
  "OPERATOR_STEER",
  "NOT_PIPELINE_ACTIONABLE",
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
// resolve in-place (and by the Go branch-out-of-date recovery action, which
// mirrors it). It captures the conflicting files and BOTH sides of each conflict
// BEFORE `git rebase --abort` discards the conflict state. The recovery loop
// pairs it with a CONFLICT_RESOLUTION_NEEDED feedback signal so feature-dev is
// re-dispatched on the SAME branch to resolve the conflict, rather than the
// whole branch being deleted for a fresh-branch restart.
// ============================================================================

// ----------------------------------------------------------------------------
// ours / theirs are the CONSUMER's vocabulary, not git's (#301)
//
//   ours   = the PR branch's own work
//   theirs = the base the PR is being landed onto
//
// Git's index stage names are relative to what is CHECKED OUT, and a rebase
// checks out the upstream and replays your commits onto it — so under a rebase
// git calls the base "ours" and your work "theirs", the exact inverse of a
// merge. Every writer of this document MUST translate; passing git's naming
// through handed feature-dev the base branch under the field it is told is its
// own feature work, and its resolution then inverted both sides.
//
//   operation | ours (PR branch work) | theirs (base)  | detected from
//   ----------|-----------------------|----------------|------------------------
//   rebase    | index stage 3         | index stage 2  | rebase-merge/ or
//             |                       |                | rebase-apply/ exists
//   merge     | index stage 2         | index stage 3  | MERGE_HEAD exists
//
// `conflict_operation` on the context records which mapping was applied.
// ----------------------------------------------------------------------------

// Index modes that carry inlinable blob content. 160000 (a submodule pointer,
// "gitlink") does not: its object id is a COMMIT in the submodule's own store,
// so ours/theirs stay empty and ours_commit/theirs_commit carry the ids.
const BLOB_MODES = ["100644", "100755", "120000"] as const;

// A single conflicting file with both sides of the conflict.
export const ConflictFileSchema = z.object({
  /** Repo-relative path of the conflicting file. */
  path: z.string().min(1),
  /** "ours" side blob — the PR branch's own work. Empty for a gitlink or an absent side. */
  ours: z.string(),
  /** "theirs" side blob — the base being landed onto. Empty for a gitlink or an absent side. */
  theirs: z.string(),
  /**
   * Whether the index carried an "ours" stage at all. false is a real conflict
   * shape (modify/delete, delete/delete), NOT a failed read — which is why an
   * empty `ours` is only ambiguous when this flag is missing.
   */
  ours_present: z.boolean().optional(),
  /** Whether the index carried a "theirs" stage at all. */
  theirs_present: z.boolean().optional(),
  /** Index mode of the "ours" stage ("" when absent). Anything outside BLOB_MODES is metadata-only. */
  ours_mode: z.string().optional(),
  /** Index mode of the "theirs" stage ("" when absent). */
  theirs_mode: z.string().optional(),
  /** Submodule commit id on the "ours" side. Present only when ours_mode is 160000. */
  ours_commit: z.string().optional(),
  /** Submodule commit id on the "theirs" side. Present only when theirs_mode is 160000. */
  theirs_commit: z.string().optional(),
  /**
   * Why THIS path could not be recorded (unreadable blob, over the size cap,
   * content that cannot round-trip through JSON). Set by the shell writer,
   * which raises the document-level capture_failed marker alongside it. It must
   * be DECLARED here or zod strips it and the consumer sees a failed entry with
   * its diagnosis deleted.
   */
  capture_error: z.string().optional(),
});
export type ConflictFile = z.infer<typeof ConflictFileSchema>;

/**
 * True when neither side of the entry has content and nothing in the entry
 * explains it — the shape a writer produces when its blob reads failed and it
 * substituted "". Consumers must refuse such an entry rather than resolve
 * against it. Mirrors conflictContextEntry.unexplainedEmpty in
 * internal/orchestrator/recovery/conflict_recovery_loop.go.
 *
 * A MODE-ONLY conflict is explained and not flagged: an empty placeholder
 * (`.gitkeep`, `__init__.py`, `py.typed`) added on both sides with different
 * exec bits stages as `100644 e69de29 2` / `100755 e69de29 3`, so both sides are
 * present, both are genuinely empty, and the differing modes ARE the conflict.
 * Content-identical sides with the SAME mode are never an unmerged path, so a
 * same-mode all-empty entry stays flagged.
 *
 * This predicate does NOT look at capture_error, exactly as its Go counterpart
 * does not: an entry naming one is refused on that ground independently,
 * whatever this returns. Check both.
 */
export function isUnrecordedConflictFile(f: ConflictFile): boolean {
  if (f.ours !== "" || f.theirs !== "") return false;
  const nonBlob = (m?: string) =>
    m !== undefined && m !== "" && !(BLOB_MODES as readonly string[]).includes(m);
  if (nonBlob(f.ours_mode) || nonBlob(f.theirs_mode)) return false;
  if (f.ours_mode && f.theirs_mode && f.ours_mode !== f.theirs_mode) return false;
  if (f.ours_present === false || f.theirs_present === false) return false;
  return true;
}

// Standalone conflict-context-{N}.json schema. Extra fields are tolerated so a
// future capture can add hunk context without a schema bump.
export const ConflictContextSchema = z
  .object({
    schema_version: z.string().regex(/^\d+\.\d+$/),
    issue_number: z.number().int().positive(),
    pr_number: z.number().int().nonnegative(),
    branch: z.string().min(1),
    base_ref: z.string().min(1),
    /** Which stage→side mapping the writer applied. See the table above. */
    conflict_operation: z.enum(["rebase", "merge"]).optional(),
    /**
     * The writer's own admission that it could not record this conflict
     * faithfully. A consumer must escalate rather than resolve. Only the shell
     * capture in skills/nightgauge-pr-merge sets it; the Go capture writes no
     * document at all when it fails.
     */
    capture_failed: z.boolean().optional(),
    /**
     * The document-level reason behind capture_failed (the per-path reasons are
     * on each entry). Declared rather than left to .passthrough() so the field
     * is typed and cannot be dropped by a future tightening of the container.
     */
    capture_error: z.string().optional(),
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
