/**
 * Platform emit for model-eval runs (Issue #4167 — historical storage).
 *
 * Pushes a completed `EvalRun` to the platform's ingest endpoint
 * (`POST /v1/analytics/evals`) so runs are stored account-scoped and become
 * queryable/trendable on the dashboard — the whole point being to compare how
 * models progress over time as the registry changes.
 *
 * The local `EvalRun` shape IS the wire `EvalRunRecord` (same fields, same
 * `cells` name, cost in **dollars** and quality as **0–100 floats** — the
 * micro-dollar / centi-point integer encoding is storage-only, applied
 * server-side). So there is no mapping: the run is sent as-is.
 *
 * Auth is a bearer **license key** (the ingest route uses `pipelineAuth`, which
 * accepts license keys — unlike the JWT-only read endpoints). The token is never
 * logged or included in error messages.
 *
 * The HTTP boundary (`fetch`) is injected so this is unit-testable without a
 * network. Emit is fire-and-forget from the caller's perspective: a failure must
 * never crash a run or lose the local JSONL.
 *
 * @see docs/MODEL_EVALUATION.md — platform emit + dashboard
 * @see acme-platform POST /v1/analytics/evals (EvalRunRecordSchema)
 */

import type { EvalRun } from "./modelEvalSchemas.js";

/** Default production platform base URL (mirrors the Go client default). */
export const DEFAULT_PLATFORM_URL = "https://api.nightgauge.dev";

/** The ingest path appended to the resolved base URL. */
export const EVAL_INGEST_PATH = "/v1/analytics/evals";

/** Resolved credentials + endpoint for an emit. */
export interface EvalEmitConfig {
  /** Base URL, no trailing slash. */
  baseUrl: string;
  /** Bearer token (license key or API key). Never logged. */
  token: string;
}

/** The credential sources, in precedence order, for {@link resolveEvalEmitConfig}. */
export interface EvalEmitConfigSources {
  /** Process env (default `process.env`). */
  env?: NodeJS.ProcessEnv;
  /** Parsed `~/.nightgauge/config.yaml` `platform:` section, if any. */
  fileConfig?: { api_url?: string; license_key?: string } | null;
  /** Explicit overrides (e.g. a `--platform-url` flag). Win over everything. */
  overrides?: { baseUrl?: string; token?: string };
}

/**
 * Resolve `{ baseUrl, token }` from overrides → env → machine config → default.
 * Token precedence: override → `NIGHTGAUGE_API_KEY` → `NIGHTGAUGE_LICENSE_KEY`
 * → file `license_key`. Returns `{ error }` (not a throw) when no token is found,
 * so the CLI can print guidance and continue without emitting.
 */
export function resolveEvalEmitConfig(
  sources: EvalEmitConfigSources = {}
): EvalEmitConfig | { error: string } {
  const env = sources.env ?? process.env;
  const file = sources.fileConfig ?? null;
  const overrides = sources.overrides ?? {};

  const token =
    overrides.token ||
    env.NIGHTGAUGE_API_KEY ||
    env.NIGHTGAUGE_LICENSE_KEY ||
    file?.license_key ||
    "";

  if (!token) {
    return {
      error:
        "no platform credential found — set NIGHTGAUGE_LICENSE_KEY (or " +
        "NIGHTGAUGE_API_KEY), or add platform.license_key to ~/.nightgauge/config.yaml.",
    };
  }

  // Never ship a bearer credential over an unvalidated / cleartext transport: a
  // tampered `api_url`, a typo'd `--platform-url`, or a plain http:// host would
  // otherwise leak the license key. Require https except for explicit loopback.
  const rawBase = (
    overrides.baseUrl ||
    env.NIGHTGAUGE_PLATFORM_URL ||
    file?.api_url ||
    DEFAULT_PLATFORM_URL
  ).trim();
  let parsed: URL;
  try {
    parsed = new URL(rawBase);
  } catch {
    return { error: `invalid platform URL: ${rawBase}` };
  }
  const loopback =
    parsed.hostname === "localhost" ||
    parsed.hostname === "127.0.0.1" ||
    parsed.hostname === "::1" ||
    parsed.hostname === "[::1]";
  const httpsOrLoopbackHttp =
    parsed.protocol === "https:" || (parsed.protocol === "http:" && loopback);
  if (!httpsOrLoopbackHttp) {
    return {
      error:
        `refusing to send credentials to ${parsed.protocol}//${parsed.host} — ` +
        "the platform URL must be https:// (http:// is allowed only for localhost).",
    };
  }

  return { baseUrl: rawBase.replace(/\/+$/, ""), token };
}

/** One rejected run in the ingest envelope. */
export interface IngestRejection {
  index: number;
  reason: string;
}

/** The `POST /v1/analytics/evals` success envelope (HTTP 202). */
export interface EvalIngestResult {
  accepted: number;
  rejected: IngestRejection[];
}

/** Injected fetch (defaults to global `fetch`); typed minimally for testability. */
export type FetchLike = (
  url: string,
  init: {
    method: string;
    headers: Record<string, string>;
    body: string;
    signal?: AbortSignal;
  }
) => Promise<{
  status: number;
  ok: boolean;
  text: () => Promise<string>;
}>;

export interface EmitEvalRunOptions {
  fetchImpl?: FetchLike;
  /** Request timeout (default 15s). */
  timeoutMs?: number;
}

/** Thrown when an emit fails. Carries the HTTP status/code; never the token. */
export class EvalEmitError extends Error {
  constructor(
    message: string,
    readonly status?: number,
    readonly code?: string
  ) {
    super(message);
    this.name = "EvalEmitError";
  }
}

const DEFAULT_EMIT_TIMEOUT_MS = 15_000;

/**
 * POST one `EvalRun` to the platform ingest endpoint. Returns the `{ accepted,
 * rejected }` envelope on 202. Throws `EvalEmitError` on any non-2xx or transport
 * failure — with the server's error code/message surfaced but the token redacted.
 *
 * Note: a 202 with `accepted: 0` and a non-empty `rejected` means the run was
 * received but failed the server's strict schema validation — the caller should
 * surface those reasons (they are not thrown).
 */
export async function emitEvalRun(
  run: EvalRun,
  config: EvalEmitConfig,
  options: EmitEvalRunOptions = {}
): Promise<EvalIngestResult> {
  const fetchImpl = options.fetchImpl ?? (globalThis.fetch as unknown as FetchLike);
  if (!fetchImpl) {
    throw new EvalEmitError("no fetch implementation available (Node >= 18 or inject fetchImpl)");
  }
  const url = config.baseUrl + EVAL_INGEST_PATH;
  const timeoutMs = options.timeoutMs ?? DEFAULT_EMIT_TIMEOUT_MS;
  const controller = new AbortController();
  const timer = setTimeout(() => controller.abort(), timeoutMs);

  // The timer must stay armed until the RESPONSE BODY is fully read — fetch
  // resolves on headers, so a stalled body would otherwise never time out and the
  // caller (a CLI awaiting emit) could hang. One try/finally covers both.
  try {
    let res;
    try {
      res = await fetchImpl(url, {
        method: "POST",
        headers: {
          Authorization: `Bearer ${config.token}`,
          "Content-Type": "application/json",
          Accept: "application/json",
        },
        body: JSON.stringify(run),
        signal: controller.signal,
      });
    } catch (err) {
      // Transport/timeout — surface without the token.
      const detail = err instanceof Error ? err.message : String(err);
      throw new EvalEmitError(`eval emit request failed: ${redact(detail, config.token)}`);
    }

    const raw = await res.text().catch(() => "");
    if (controller.signal.aborted) {
      throw new EvalEmitError(`eval emit timed out after ${timeoutMs}ms`);
    }
    if (!res.ok) {
      const parsed = safeJson(raw) as { error?: { code?: string; message?: string } } | null;
      const code = parsed?.error?.code;
      const message = parsed?.error?.message ?? raw.slice(0, 200) ?? "";
      throw new EvalEmitError(
        `eval emit failed (HTTP ${res.status}${code ? ` ${code}` : ""}): ${redact(message, config.token)}`,
        res.status,
        code
      );
    }

    const body = safeJson(raw) as Partial<EvalIngestResult> | null;
    return {
      accepted: typeof body?.accepted === "number" ? body.accepted : 0,
      rejected: Array.isArray(body?.rejected) ? body.rejected : [],
    };
  } finally {
    clearTimeout(timer);
  }
}

function safeJson(text: string): unknown {
  try {
    return JSON.parse(text);
  } catch {
    return null;
  }
}

/** Defensive: strip the token from any string before it reaches a log/error. */
function redact(text: string, token: string): string {
  if (!token) return text;
  return text.split(token).join("<redacted>");
}
