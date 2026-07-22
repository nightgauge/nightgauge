/**
 * Structured error contract for the pipeline state machine.
 *
 * The recovery UX (Gap 2 / separate issue) consumes these errors to render
 * dialogs and quick-picks. The contract is `recoverable: boolean` plus
 * `recovery_actions: string[]` — both are surfaced to the UX without
 * additional translation.
 *
 * @see ADR-005 in .nightgauge/knowledge/features/3238-graceful-pipeline-stop-with-durable/decisions.md
 */

/**
 * Base class for all pipeline-state errors. Subclasses set `recoverable` and
 * `recovery_actions` so callers can branch without `instanceof` chains.
 */
export class PipelineStateError extends Error {
  constructor(
    message: string,
    public readonly code: string,
    public readonly recoverable: boolean,
    public readonly recovery_actions: string[]
  ) {
    super(message);
    this.name = "PipelineStateError";
  }
}

/**
 * Raised when a pipeline JSON file does not match its Zod schema OR when its
 * `schema_version` does not satisfy the same-major / equal-or-older minor
 * tolerance rule.
 *
 * Recoverable when the file can be discarded and rewritten by re-running the
 * stage that produced it; non-recoverable for a major-version mismatch
 * (which requires a manual migration).
 */
export class ContextSchemaError extends PipelineStateError {
  constructor(
    public readonly filename: string,
    public readonly detail: string,
    recoverable: boolean = true,
    recovery_actions: string[] = ["Restart from earlier stage", "Discard run"]
  ) {
    super(
      `Context schema error in ${filename}: ${detail}`,
      "CONTEXT_SCHEMA_ERROR",
      recoverable,
      recovery_actions
    );
    this.name = "ContextSchemaError";
  }
}

/**
 * Raised on resume when `run-state.json` lists a worktree path that no longer
 * exists on disk. The user typically deleted the worktree manually; we can't
 * silently recreate it because uncommitted work may have been lost.
 */
export class WorktreeMissing extends PipelineStateError {
  constructor(
    public readonly worktree_path: string,
    public readonly branch: string
  ) {
    super(`Worktree missing: ${worktree_path} (branch: ${branch})`, "WORKTREE_MISSING", true, [
      "Re-create worktree from branch HEAD",
      "Restart from stage",
    ]);
    this.name = "WorktreeMissing";
  }
}

/**
 * Raised when a second pipeline run is attempted against an issue that
 * already has a `running` run-state.json with a live PID. The autonomous
 * orchestrator never bypasses this; users may pass `--force-concurrent`.
 */
export class ConcurrentRunRefused extends PipelineStateError {
  constructor(
    public readonly issue_number: number,
    public readonly holder_pid: number | null,
    public readonly host_id: string | null
  ) {
    super(
      `Concurrent run refused for issue #${issue_number} (holder pid=${holder_pid ?? "unknown"}, host=${host_id ?? "unknown"})`,
      "CONCURRENT_RUN_REFUSED",
      true,
      ["Wait for the running pipeline to finish", "Pass --force-concurrent to override"]
    );
    this.name = "ConcurrentRunRefused";
  }
}

/**
 * Raised when a context file's `schema_version` major bump does not match
 * the reader's expected major. The migration doc identifies the path forward.
 */
export class SchemaVersionMismatch extends PipelineStateError {
  constructor(
    public readonly filename: string,
    public readonly file_version: string,
    public readonly reader_major: number
  ) {
    super(
      `Schema version mismatch in ${filename}: file is ${file_version}, reader expects major ${reader_major}.x — see docs/PIPELINE_STATE_SCHEMA.md for migration steps`,
      "SCHEMA_VERSION_MISMATCH",
      false,
      ["Read docs/PIPELINE_STATE_SCHEMA.md", "Discard run"]
    );
    this.name = "SchemaVersionMismatch";
  }
}

/**
 * Raised when a stage cannot start because its required input context file
 * (the previous stage's handoff JSON) is missing on disk. Drives the
 * Recovery Dialog (Issue #3239): the producing stage is named so the user
 * can choose to run it now, restart from earlier, or discard the run.
 */
export class MissingInputFile extends PipelineStateError {
  constructor(
    public readonly filename: string,
    public readonly triggeringStage: string,
    public readonly producingStage: string | null
  ) {
    const producerHint = producingStage
      ? `${producingStage} must complete and write this file before ${triggeringStage} can proceed.`
      : `No producing stage is registered for this file — manual recovery required.`;
    super(
      `Cannot start ${triggeringStage}: required input file ${filename} is missing. ${producerHint}`,
      "MISSING_INPUT_FILE",
      true,
      ["Run producing stage", "Resume from paused stage", "Restart from beginning", "Discard run"]
    );
    this.name = "MissingInputFile";
  }
}

/**
 * Raised when run-state.json is expected but absent — typically a clobbered
 * worktree or first-run after manual cleanup. Recoverable via restart.
 */
export class RunStateMissing extends PipelineStateError {
  constructor(public readonly issue_number: number) {
    super(
      `No run-state.json found for issue #${issue_number} — pipeline lifecycle cannot be resumed.`,
      "RUN_STATE_MISSING",
      true,
      ["Restart from beginning", "Discard run"]
    );
    this.name = "RunStateMissing";
  }
}

/**
 * Discriminator for the on-disk run-state lifecycle as exposed to the
 * Recovery Dialog. `none` indicates run-state.json is absent.
 */
export type RecoveryRunState = "running" | "paused" | "aborted" | "none";

/**
 * Discriminator for the structured error kinds the Recovery Dialog
 * understands. Mirrors the `code` field of each PipelineStateError subclass
 * that drives a recoverable failure; types not in this union (e.g.
 * `SCHEMA_VERSION_MISMATCH` major bumps) require docs-led migration rather
 * than dialog-driven recovery.
 */
export type RecoveryErrorKind =
  | "MISSING_INPUT_FILE"
  | "CONTEXT_SCHEMA_ERROR"
  | "WORKTREE_MISSING"
  | "RUN_STATE_MISSING"
  | "SCHEMA_VERSION_MISMATCH";

/**
 * Recovery actions presentable in the Recovery Dialog. The orchestrator
 * computes the subset valid for the current on-disk state; the dialog
 * renders the resulting array verbatim.
 *
 * - `resume-from-paused-stage` — re-enter the paused stage with intact context.
 * - `run-producing-stage` — invoke the missing context's producer, then
 *   continue downstream.
 * - `restart-from-beginning` — archive existing state under
 *   `history/<runId>/` and start a fresh run.
 * - `discard-run` — destructive: delete branch + worktree + context files.
 * - `open-run-state-directory` — reveal the on-disk pipeline directory.
 * - `cancel` — close the dialog without acting.
 */
export type RecoveryAction =
  | "resume-from-paused-stage"
  | "run-producing-stage"
  | "restart-from-beginning"
  | "discard-run"
  | "open-run-state-directory"
  | "cancel";

/**
 * Payload emitted by `OrchestratorEventDispatcher.onRecoveryRequired`.
 * Computed deterministically from disk state by
 * `HeadlessOrchestrator.computeRecoveryRequired`; consumed by the Recovery
 * Dialog and the telemetry layer.
 */
export interface RecoveryRequiredPayload {
  issueNumber: number;
  triggeringStage: string;
  producingStage?: string | null;
  errorKind: RecoveryErrorKind;
  errorDetail: string;
  runState: RecoveryRunState;
  availableActions: RecoveryAction[];
}

/**
 * Compare two semver-ish "major.minor" strings. Returns true when `file` is
 * acceptable to a reader expecting `expected` (same major, file minor ≤
 * expected minor — readers tolerate older minors but not future ones to
 * avoid silently dropping fields the writer added).
 */
export function isSchemaCompatible(file: string, expected: string): boolean {
  const [fileMajor, fileMinor] = file.split(".").map((n) => parseInt(n, 10));
  const [expMajor, expMinor] = expected.split(".").map((n) => parseInt(n, 10));
  if (Number.isNaN(fileMajor) || Number.isNaN(expMajor)) return false;
  if (fileMajor !== expMajor) return false;
  if (Number.isNaN(fileMinor) || Number.isNaN(expMinor)) return true;
  return fileMinor <= expMinor;
}
