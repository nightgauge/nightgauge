/**
 * RunState — durable pipeline lifecycle record
 *
 * Single source of truth for the pipeline lifecycle. Persisted to
 * `.nightgauge/pipeline/run-state.json` per repo. Mirrored field-for-field
 * by `internal/runstate.RunState` in Go.
 *
 * Schema version history:
 * - 1.0: Initial schema (Issue #3238)
 *
 * @see docs/PIPELINE_STATE_SCHEMA.md for the full schema catalog and
 *      first-run-after-upgrade migration semantics.
 */
import { z } from "zod";
import { flexEnum } from "./helpers.js";

/**
 * Lifecycle states. Transition rules enforced by RunStateManager:
 *   running   → paused | completed | aborted
 *   paused    → running | discarded
 *   aborted   → discarded
 *   completed → (terminal — only archive)
 *   discarded → (terminal — already archived)
 */
export const RunStateLifecycleSchema = flexEnum([
  "running",
  "paused",
  "completed",
  "discarded",
  "aborted",
] as const);
export type RunStateLifecycle = z.infer<typeof RunStateLifecycleSchema>;

/**
 * Stage names — same set used in the rest of the pipeline. The literal list
 * mirrors PipelineStage in the orchestrator.
 */
export const RunStageSchema = z.enum([
  "issue-pickup",
  "feature-planning",
  "feature-dev",
  "feature-validate",
  "pr-create",
  "pr-merge",
]);
export type RunStage = z.infer<typeof RunStageSchema>;

/**
 * Per-attempt metadata used to detect partial-stage state on resume.
 */
export const RunAttemptSchema = z.object({
  run_id: z.string().uuid(),
  attempt_number: z.number().int().min(1),
  started_at: z.string().datetime(),
  ended_at: z.string().datetime().nullish(),
  /** PID of the writer process (used for liveness check on concurrent-run detection). */
  pid: z.number().int().nonnegative().nullish(),
  /** Stable host identifier (machine UUID where available). Pairs with PID for
   *  cross-host concurrent-run detection on shared filesystems. */
  host_id: z.string().nullish(),
  /** Last stage marker emitted by the writer — best-effort, may be stale. */
  last_stage: RunStageSchema.nullish(),
});
export type RunAttempt = z.infer<typeof RunAttemptSchema>;

/**
 * Top-level RunState envelope. One file per repo:
 * `.nightgauge/pipeline/run-state.json`.
 */
export const RunStateSchema = z
  .object({
    schema_version: z.string().regex(/^\d+\.\d+$/),
    issue_number: z.number().int().nonnegative(),
    state: RunStateLifecycleSchema,
    /** UUID v7 generated on first transition to `running`. Stable across
     *  pause/resume; new run = new run_id. */
    run_id: z.string().uuid(),
    attempt_number: z.number().int().min(1),
    /** Stages whose context-file rename has completed. Pure log of progress;
     *  resume sets currentStage to the first stage NOT in this set. */
    completed_stages: z.array(RunStageSchema),
    /** Where to resume from. When state === "paused", this is the stage the
     *  next run should start. When state === "running", this is the stage
     *  currently executing. */
    resume_from_stage: RunStageSchema.nullish(),
    /** Absolute path to the orchestrator-managed worktree (when used). Stop
     *  preserves the worktree; discard removes it. */
    worktree_path: z.string().nullish(),
    /** Feature branch attached to the run. */
    branch: z.string().min(1),
    /** ISO-8601 timestamps. */
    created_at: z.string().datetime(),
    updated_at: z.string().datetime(),
    /** Free-form reason — populated for `paused`, `aborted`, `discarded`. */
    reason: z.string().nullish(),
    /** Whether the user can recover from the current state without manual
     *  intervention. For `aborted` this distinguishes a transient failure
     *  (recoverable) from a structural mismatch (not recoverable, e.g.
     *  pre-Gap-1 first-run-after-upgrade per ADR-002). */
    recoverable: z.boolean().nullish(),
    /** Suggested recovery actions — surfaces directly into the IPC channel
     *  so the recovery UX (Gap 2) can render quick-picks without mapping. */
    recovery_actions: z.array(z.string()).nullish(),
    /** Per-attempt metadata. Most-recent attempt is the last entry. */
    attempts: z.array(RunAttemptSchema),
  })
  .passthrough();
export type RunState = z.infer<typeof RunStateSchema>;

/**
 * Helper — produce a fresh RunState for a brand-new run.
 */
export function newRunState(args: {
  issue_number: number;
  branch: string;
  run_id: string;
  pid?: number;
  host_id?: string;
  worktree_path?: string;
}): RunState {
  const now = new Date().toISOString();
  return {
    schema_version: "1.0",
    issue_number: args.issue_number,
    state: "running",
    run_id: args.run_id,
    attempt_number: 1,
    completed_stages: [],
    resume_from_stage: "issue-pickup",
    worktree_path: args.worktree_path ?? null,
    branch: args.branch,
    created_at: now,
    updated_at: now,
    reason: null,
    recoverable: null,
    recovery_actions: null,
    attempts: [
      {
        run_id: args.run_id,
        attempt_number: 1,
        started_at: now,
        ended_at: null,
        pid: args.pid ?? null,
        host_id: args.host_id ?? null,
        last_stage: "issue-pickup",
      },
    ],
  };
}
