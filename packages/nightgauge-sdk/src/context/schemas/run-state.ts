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
import { RUN_IDENTITY_PATTERN } from "../runIdentity.js";

/**
 * `run_id` as it is validated on READ-BACK — the same shape the minter emits
 * and the same shape Go keys runs on (#468).
 *
 * DERIVED from the single TypeScript authority, never transcribed: the
 * character sequence lives once, in `context/runIdentity.ts`, pinned
 * byte-for-byte to Go's `IdentityPattern` by
 * `internal/runstate/identity_crosslang_test.go`. A pasted copy here would
 * satisfy that pin (it reads only the authority's file) and drift on its own
 * schedule, which is the exact failure #424 removed four instances of.
 *
 * WHY NOT `z.string().uuid()` — which is what this used to be. Zod's UUID check
 * accepts every RFC 9562 version and is case-insensitive, so it admitted v4 ids
 * and uppercase hex: precisely the set Go's authority refuses with
 * `ErrNoRunIdentity`. That made this file the one place where an id the rest of
 * the system cannot use could enter from disk and flow onward into IPC params
 * and `runtime-{issue}-{runId}.json` filename components the Go scanner will
 * not parse — the F16 `run_id_invalid` family, where every progress call for
 * that run is silently discarded.
 *
 * REFUSAL IS STRICT, with no lenient-with-telemetry branch: that would be a
 * compatibility shim for on-disk files no customer has, which `AGENTS.md`
 * § Agent Operating Rules rules out. `RunStateManager.read()` turns the refusal
 * into a `ContextSchemaError` naming the file, and the message below names the
 * id, so the resume path fails loudly instead of silently dropping a run or
 * rewriting the id underneath the operator.
 */
const RunIdSchema = z.string().superRefine((value, ctx) => {
  if (RUN_IDENTITY_PATTERN.test(value)) return;
  ctx.addIssue({
    code: "custom",
    message:
      `run_id ${JSON.stringify(value)} is not a canonical run identity ` +
      `(lowercase UUIDv7). Go refuses this id as run_id_invalid, so the run ` +
      `cannot be keyed, resumed, or reported on. This file was written by an ` +
      `older or foreign writer — delete it and start the run again.`,
  });
});

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
  run_id: RunIdSchema,
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
     *  pause/resume; new run = new run_id. Validated against the single
     *  run-identity authority — see {@link RunIdSchema}. */
    run_id: RunIdSchema,
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
