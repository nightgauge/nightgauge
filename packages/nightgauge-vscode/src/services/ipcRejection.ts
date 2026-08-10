/**
 * ipcRejection — one classifier for every rejected `pipeline.*` call.
 *
 * ADR-017 Decision 3 names the defect this closes: `PipelineStateService`'s
 * stage-transition calls sit behind BARE, NON-LOGGING `catch {}` blocks that
 * fabricate local state and fire `_onStateChanged`, so the UI shows a healthy
 * run while the server refused everything. Twelve such catches exist on the
 * extension's notify surface (seven stage transitions, `notifyComplete`, two
 * `setPaused`, two phase transitions). Every one of them delegates here.
 *
 * THE AUDIENCE IS THE OPERATOR READING A LOG, NOT A RECOVERY ROUTINE. No
 * design property in ADR-017 may depend on the TypeScript side observing a
 * rejection — Go logs, counts and traces every refusal itself. There is
 * deliberately no re-handshake, no retry and no `reHandshakeRequired` flag:
 * inventing a recipient for a rejection is how the permanent lockout was
 * designed in the first place (#307 round 3). This module only makes the
 * refusal VISIBLE, at a severity that matches what it means.
 *
 * THE `run_*` ARMS ARE LIVE (ADR-017 step 4). The server keys on the run
 * identity and refuses: `run_id_required` / `run_id_invalid` for a missing or
 * malformed id, `run_closed` once a run's own terminal claim has latched,
 * `run_not_found` and `run_wrong_owner` on the administrative and terminal
 * classes. `transport` is now what it says on the tin — the socket, the daemon,
 * a timeout — and is no longer the only reachable classification. Severity is
 * chosen accordingly: a closed run's late chatter is expected noise (debug),
 * an ownership or identity refusal is a real defect somewhere (warn).
 *
 * @see docs/decisions/017-runtime-identity-keying.md — Decision 3
 * @see internal/ipc/server.go — `sendError` writes the frame this parses
 */

/**
 * Minimal logger surface. `PipelineStateService` is constructed without a
 * `Logger` (it is a relay, not a service with injected deps), so the default
 * is the console the file already uses; tests inject a spy.
 */
export interface RejectionLogger {
  debug(message: string, meta?: Record<string, unknown>): void;
  info(message: string, meta?: Record<string, unknown>): void;
  warn(message: string, meta?: Record<string, unknown>): void;
}

const consoleLogger: RejectionLogger = {
  debug: (m, meta) => console.debug(m, meta ?? {}),
  info: (m, meta) => console.info(m, meta ?? {}),
  warn: (m, meta) => console.warn(m, meta ?? {}),
};

/**
 * How a rejection was classified. Exported so tests assert the decision
 * rather than the log string.
 */
export type IpcRejectionClass =
  /** The run's own terminal claim already closed it — expected, not a fault. */
  | "terminal-refusal"
  /** The id names a run this peer does not own, or no run at all. */
  | "ownership"
  /** The call carried no identity, or one the server could not parse. */
  | "identity"
  /** Anything else: the socket, the daemon, a timeout, a malformed frame. */
  | "transport";

export interface IpcRejectionContext {
  /** The `pipeline.*` method whose promise rejected. */
  method: string;
  /** The stage the call described, when it had one. */
  stage?: string;
  /** The identity the call carried; `null` when the emitter held none. */
  runId: string | null;
  /** Whatever the promise rejected with. */
  err: unknown;
  logger?: RejectionLogger;
}

export interface IpcRejectionRecord {
  classification: IpcRejectionClass;
  /** Machine-readable `run_*` code when the server named one. */
  code: string | null;
  /** Numeric JSON-RPC code from the error frame, when the message carried one. */
  rpcCode: number | null;
  message: string;
}

/**
 * `IpcClientBase` surfaces a JSON-RPC error frame as
 * `new Error("IPC error <code>: <message>")` (see its response handler), so
 * the numeric code is recoverable from the message and nothing else needs to
 * change on the client to read it. Parsed, never reconstructed: the tests
 * feed real frames through the client's own parser rather than hand-authoring
 * the shape under test (#166).
 */
const RPC_ERROR_PREFIX = /^IPC error (-?\d+): (.*)$/s;

/**
 * The machine-readable refusal codes ADR-017 Decision 3 defines. Matched as a
 * whole token anywhere in the server's message rather than at a fixed
 * position: the code LEADS the message today (`runIdentityError.Error`), but
 * the transport wraps the handler error into `RPCError.Message`, and a
 * classifier that depended on the offset would break the first time a wrapper
 * was added.
 */
const RUN_CODE = /\brun_(closed|not_found|wrong_owner|id_required|id_invalid)\b/;

function classify(code: string | null): IpcRejectionClass {
  switch (code) {
    case "run_closed":
      return "terminal-refusal";
    case "run_not_found":
    case "run_wrong_owner":
      return "ownership";
    case "run_id_required":
    case "run_id_invalid":
      return "identity";
    default:
      return "transport";
  }
}

/**
 * Log one rejected `pipeline.*` call at a severity that matches what the
 * refusal means, and return what was decided.
 *
 * Never throws and never rethrows: every call site is a fire-and-forget
 * telemetry path, and C9 forbids a refused IPC call from killing a run in
 * flight. The run continues on its local cache exactly as it did when these
 * catches were empty — the only thing that changes is that the refusal is
 * no longer invisible.
 */
export function handleIpcRejection(context: IpcRejectionContext): IpcRejectionRecord {
  const log = context.logger ?? consoleLogger;
  const raw =
    context.err instanceof Error ? context.err.message : String(context.err ?? "unknown error");
  const framed = RPC_ERROR_PREFIX.exec(raw);
  const rpcCode = framed ? Number(framed[1]) : null;
  const message = framed ? framed[2] : raw;
  const code = RUN_CODE.exec(message)?.[0] ?? null;
  const classification = classify(code);

  const meta: Record<string, unknown> = {
    method: context.method,
    stage: context.stage,
    runId: context.runId,
    code,
    rpcCode,
    error: message,
  };

  switch (classification) {
    case "terminal-refusal":
      // The run's own notifyComplete already booked this run. Its content is
      // recorded; a later call arriving against the closed id is expected
      // noise, not a fault — debug so a wedged run cannot flood warn.
      log.debug(`IPC refused ${context.method}: run already closed`, meta);
      break;
    case "ownership":
      log.warn(`IPC refused ${context.method}: run not owned by this peer`, meta);
      break;
    case "identity":
      log.warn(`IPC refused ${context.method}: run identity missing or malformed`, meta);
      break;
    case "transport":
      // Not a refusal at all — the daemon is unreachable, restarting, or the
      // request timed out. The local-state fallback at the call site is what
      // keeps the UI alive; this line is what stops it being silent.
      log.warn(`IPC call ${context.method} failed — state not sent to Go`, meta);
      break;
  }

  return { classification, code, rpcCode, message };
}
