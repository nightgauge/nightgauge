/**
 * ipcNotifyParams — TypeScript mirrors of the Go `pipeline.*` notify params.
 *
 * Every raw `ipc.call("pipeline.…", { … })` literal in the extension is typed
 * with one of these. The field names are EXACTLY the JSON tags on the Go
 * structs in `internal/ipc/protocol.go`
 * (`PipelineNotifyStageTransitionParams`, `PipelineNotifyStageProgressParams`,
 * `PipelineNotifyCompleteParams`, `PipelineNotifyPhaseTransitionParams`,
 * `PipelineSetPausedParams`) — that file is the source of truth and this one
 * is a mirror, not a second authority.
 *
 * WHY THIS EXISTS (ADR-017 step 3, #370). The identity these params carry is
 * a bare string in an object literal; a typo in `runId` is not a compile
 * error against `call(method: string, params?: unknown)`, and the server
 * ACCEPTS AND IGNORES unknown keys today. Step 4 flips the verbs to refuse a
 * call with no identity, at which point a misspelled key is a silently
 * refused run-progress call — F16's shape. Typing the literals here is the
 * compile-time guard that makes that key name checkable before the flip.
 *
 * @see docs/decisions/017-runtime-identity-keying.md — Decisions 1, 3, 10
 * @see internal/ipc/protocol.go — the authoritative param shapes
 */

/**
 * Canonical run-identity shape — lowercase UUIDv7.
 *
 * SOURCE OF TRUTH: `internal/runstate/identity.go:23` (`IdentityPattern`).
 * The literal is duplicated here because the extension cannot import Go; it
 * must stay character-identical to that constant, because an id this side
 * accepts and the Go side rejects becomes a `run_id_invalid` refusal at
 * step 4 — a run that mints a locally-valid id the server will not key on.
 */
export const RUN_IDENTITY_PATTERN =
  /^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/;

/**
 * True when `value` is a canonical lowercase UUIDv7 run identity.
 *
 * Twin of `runstate.IsIdentity` (internal/runstate/identity.go). Called
 * BEFORE a value is installed on a service or put on the wire: the value ends
 * up as a Go map key and a `runtime-{issue}-{runId}.json` filename component,
 * so a string containing "/" or ".." is an arbitrary-path write on a socket
 * ADR-015 documents as unauthenticated.
 */
export function isRunIdentity(value: unknown): value is string {
  return typeof value === "string" && RUN_IDENTITY_PATTERN.test(value);
}

/** Mirrors `PipelineNotifyStageTransitionParams` (internal/ipc/protocol.go). */
export interface NotifyStageTransitionParams {
  repo: string;
  issueNumber: number;
  stage: string;
  /** "initialized" | "running" | "model-resolved" | "complete" | "failed" | "skipped" | "deferred" */
  status: string;
  title?: string;
  branch?: string;
  baseBranch?: string;
  error?: string;
  inputTokens?: number;
  outputTokens?: number;
  cacheReadTokens?: number;
  costUsd?: number;
  model?: string;
  adapter?: string;
  /**
   * Advisory OS pid of the child executing this stage (ADR-017 §7.2).
   *
   * Present on exactly ONE `running` transition per stage attempt, and `0` on
   * that stage's terminal transition so a finished child can never vouch for
   * the run. OMITTED — not zero — on `initialized` / `model-resolved` /
   * `skipped` / `deferred`, which describe no child at all.
   */
  stagePid?: number;
  /** The run this transition belongs to. Accepted and ignored until step 4. */
  runId: string;
}

/** Mirrors `PipelineNotifyStageProgressParams` (internal/ipc/protocol.go). */
export interface NotifyStageProgressParams {
  repo: string;
  issueNumber: number;
  stage: string;
  inputTokens?: number;
  outputTokens?: number;
  cacheReadTokens?: number;
  costUsd?: number;
  model?: string;
  /** The run this progress estimate belongs to. Accepted and ignored until step 4. */
  runId: string;
}

/** Mirrors `PipelineNotifyCompleteParams` (internal/ipc/protocol.go). */
export interface NotifyCompleteParams {
  repo: string;
  issueNumber: number;
  success: boolean;
  totalDurationMs: number;
  stagesRun: string[];
  prMerged?: boolean;
  deferred?: boolean;
  stageExecutionPaths?: Record<string, string>;
  stagePuntReasons?: Record<string, string>;
  /** The run being claimed terminal. Accepted and ignored until step 4. */
  runId: string;
}

/** Mirrors `PipelineNotifyPhaseTransitionParams` (internal/ipc/protocol.go). */
export interface NotifyPhaseTransitionParams {
  repo: string;
  issueNumber: number;
  stage: string;
  name: string;
  index: number;
  total: number;
  /** "start" | "complete" */
  eventType: string;
  /** The run this phase belongs to. Accepted and ignored until step 4. */
  runId: string;
}

/**
 * Mirrors `PipelineSetPausedParams` (internal/ipc/protocol.go).
 *
 * `repo` + `runId` are non-optional HERE even though the Go tags carry
 * `omitempty`: `setPaused` is the verb ADR-017 Decision 10 calls "three
 * defects in one call" precisely because it used to arrive with neither, and
 * an emitter that cannot name the run it is pausing must not send at all.
 */
export interface SetPausedParams {
  issueNumber: number;
  paused: boolean;
  repo: string;
  runId: string;
}
