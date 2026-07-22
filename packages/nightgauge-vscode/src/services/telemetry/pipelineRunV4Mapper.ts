/**
 * pipelineRunV4Mapper — transforms the on-disk snake_case history record
 * (ExecutionHistoryRunRecord V2 / V3, written by the Go history producer) into
 * the camelCase `ExecutionHistoryRunRecordV4` wire shape that
 * `POST /v1/telemetry/pipeline-run` requires.
 *
 * WHY THIS EXISTS — the dashboard "0 runs" bug:
 *   The platform's telemetry endpoint validates against a `.strict()` V4 Zod
 *   schema (camelCase, schemaVersion=4, a REQUIRED `repo`, no unknown keys).
 *   The producer writes V2 (snake_case, schema_version="2", many extra keys).
 *   The uploader used to POST the raw V2 lines verbatim, so every record was
 *   rejected — and because the endpoint answers `202 {accepted, rejected}`, the
 *   uploader advanced its watermark anyway and the data was silently lost. This
 *   mapper produces a schema-valid V4 record so records actually persist.
 *
 * The authoritative wire contract lives in the platform repo:
 *   acme-platform/packages/shared-types/src/telemetry.ts
 *   (ExecutionHistoryRunRecordV4Schema). Keep this mapper aligned with it; the
 *   platform's post-deploy telemetry canary + integration test fail the deploy
 *   if producer and consumer drift, so a mismatch surfaces in CI, not in prod.
 *
 * Records that cannot be mapped to a valid V4 record (e.g. pre-`repo` history
 * lines, non-run records, unparseable timestamps) return `{ ok: false }` with a
 * reason — the caller treats those as permanently skippable so they neither
 * upload nor block the watermark.
 *
 * `pipelineRunId` threading: the V3 JSONL
 * record already carries the run's own UUID as `run_id` (written by
 * internal/state/history.go). This mapper forwards it onto the wire record
 * as `pipelineRunId` — UUID-guarded via {@link validTelemetryRunID}, mirroring
 * the Go authoritative push's identical guard
 * (internal/platform/execution_history_mapper.go) — so the uploader's
 * best-effort batch upsert lands on the SAME `pipeline_runs` row the
 * authoritative Go notify-path push created, instead of the platform minting
 * a second row under a derived `account:issue:startedAt` id.
 */

/** Telemetry schema version literal — must match TELEMETRY_SCHEMA_VERSION_V4. */
export const TELEMETRY_SCHEMA_VERSION_V4 = 4 as const;

/** Allowed pipeline outcome states (matches TELEMETRY_OUTCOMES). */
const V4_OUTCOMES = ["complete", "failed", "cancelled"] as const;
type V4Outcome = (typeof V4_OUTCOMES)[number];

/** Allowed t-shirt size labels (matches TELEMETRY_SIZES). */
const V4_SIZES = ["XS", "S", "M", "L", "XL"] as const;
type V4Size = (typeof V4_SIZES)[number];

/** Allowed Fibonacci-style complexity scores (matches TELEMETRY_COMPLEXITY_SCORES). */
const V4_COMPLEXITY_SCORES = [1, 2, 3, 5, 8] as const;
type V4Complexity = (typeof V4_COMPLEXITY_SCORES)[number];

/** Per-run agent / stage caps (match TELEMETRY_AGENTS_MAX / TELEMETRY_STAGES_MAX). */
const V4_STAGES_MAX = 32;

/** Per-stage telemetry (matches StageMetricSchema). */
export interface V4StageMetric {
  stageId: string;
  stageName: string;
  attempt: number;
  model: string | null;
  durationMs: number | null;
  inputTokens: number;
  outputTokens: number;
  totalTokens: number;
  costUsd: number | null;
  success: boolean;
}

/** One completed pipeline run (matches ExecutionHistoryRunRecordV4). */
export interface ExecutionHistoryRunRecordV4 {
  schemaVersion: typeof TELEMETRY_SCHEMA_VERSION_V4;
  issueNumber: number;
  repo: string;
  /**
   * The run's own UUID (V3 JSONL `run_id`), when the source record carries a
   * well-formed UUID. Threading it onto the wire lets the platform's
   * terminal upsert land on the SAME `pipeline_runs` row the authoritative
   * Go notify-path push created, instead of minting a duplicate row under a
   * derived `account:issue:startedAt` id.
   * The platform schema declares this field `z.string().uuid().optional()`
   * — unlike every other field here, it is NOT `.nullable()` — so it is the
   * one field genuinely optional on the TS side: an invalid/missing run_id
   * resolves to `undefined` and JSON.stringify omits the key entirely
   * (schema rejection avoided) rather than sending `null`.
   */
  pipelineRunId?: string;
  startedAt: string;
  completedAt: string | null;
  outcome: V4Outcome;
  terminalFailureKind: string | null;
  predictedSize: V4Size | null;
  actualSize: V4Size | null;
  predictedModel: string | null;
  actualModel: string | null;
  complexityScore: V4Complexity | null;
  retries: number;
  durationMs: number | null;
  totalCostUsd: number | null;
  stages: V4StageMetric[];
  agents: never[];
  routingPath: string[] | null;
}

export type MapResult =
  { ok: true; record: ExecutionHistoryRunRecordV4 } | { ok: false; reason: string };

// ─── Helpers ─────────────────────────────────────────────────────────────────

function asRecord(v: unknown): Record<string, unknown> | null {
  return typeof v === "object" && v !== null && !Array.isArray(v)
    ? (v as Record<string, unknown>)
    : null;
}

function asFiniteNumber(v: unknown): number | null {
  return typeof v === "number" && Number.isFinite(v) ? v : null;
}

function asNonNegInt(v: unknown): number {
  const n = asFiniteNumber(v);
  return n != null && n >= 0 ? Math.floor(n) : 0;
}

/**
 * Normalises a timestamp to an RFC 3339 / ISO 8601 UTC string ending in `Z`.
 * The producer writes local-offset timestamps (e.g. `…-06:00`) but the V4
 * schema's `z.string().datetime()` rejects offsets — so every timestamp must be
 * converted to UTC `Z`. Returns null when the value is absent or unparseable.
 */
function toUtcIso(v: unknown): string | null {
  if (typeof v !== "string" || v.trim() === "") return null;
  const ms = Date.parse(v);
  if (Number.isNaN(ms)) return null;
  return new Date(ms).toISOString();
}

/**
 * Well-formed-UUID guard for `pipelineRunId`, mirroring the Go authoritative
 * push's `validTelemetryRunID` (internal/platform/execution_history_mapper.go)
 * so both paths apply the identical validation before the field reaches the
 * wire. Returns the id unchanged when it matches, else `undefined` — a
 * malformed value would fail the platform's `z.string().uuid()` validation
 * and reject the WHOLE record under `.strict()`, so this must gate rather
 * than pass through (#302).
 */
const TELEMETRY_RUN_ID_PATTERN =
  /^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$/u;

function validTelemetryRunID(v: unknown): string | undefined {
  return typeof v === "string" && TELEMETRY_RUN_ID_PATTERN.test(v) ? v : undefined;
}

function asOutcome(v: unknown): V4Outcome | null {
  return typeof v === "string" && (V4_OUTCOMES as readonly string[]).includes(v)
    ? (v as V4Outcome)
    : null;
}

function asSize(v: unknown): V4Size | null {
  return typeof v === "string" && (V4_SIZES as readonly string[]).includes(v)
    ? (v as V4Size)
    : null;
}

function asComplexity(v: unknown): V4Complexity | null {
  const n = asFiniteNumber(v);
  return n != null && (V4_COMPLEXITY_SCORES as readonly number[]).includes(n)
    ? (n as V4Complexity)
    : null;
}

/**
 * Splits the V2 `routing.path` string (e.g. "issue-pickup,feature-planning" or
 * "issue-pickup > feature-dev") into the V4 `routingPath` string[]. Bounds each
 * entry to 50 chars and the array to 20 entries to satisfy the schema. Returns
 * null for an empty / placeholder path.
 */
function toRoutingPath(v: unknown): string[] | null {
  if (typeof v !== "string") return null;
  const parts = v
    .split(/[,>\s]+/u)
    .map((s) => s.trim())
    .filter((s) => s.length > 0 && s !== "standard")
    .slice(0, 20)
    .map((s) => s.slice(0, 50));
  return parts.length > 0 ? parts : null;
}

function mapStages(stagesRaw: unknown, perStageTokensRaw: unknown): V4StageMetric[] {
  const stages = asRecord(stagesRaw);
  if (!stages) return [];
  const perStage = asRecord(perStageTokensRaw) ?? {};

  const out: V4StageMetric[] = [];
  for (const [stageName, detailRaw] of Object.entries(stages)) {
    if (out.length >= V4_STAGES_MAX) break;
    const detail = asRecord(detailRaw) ?? {};
    const tokens = asRecord(perStage[stageName]) ?? {};

    const modelSelection = asRecord(detail["model_selection"]);
    const model =
      (typeof modelSelection?.["model"] === "string"
        ? (modelSelection["model"] as string)
        : null) ?? (typeof tokens["adapter"] === "string" ? (tokens["adapter"] as string) : null);

    const status = typeof detail["status"] === "string" ? detail["status"] : "";
    const inputTokens = asNonNegInt(tokens["input"]);
    const outputTokens = asNonNegInt(tokens["output"]);

    out.push({
      stageId: stageName.slice(0, 100),
      stageName: stageName.slice(0, 100),
      attempt: 1,
      model: model ? model.slice(0, 100) : null,
      durationMs: asFiniteNumber(detail["duration_ms"]),
      inputTokens,
      outputTokens,
      totalTokens: inputTokens + outputTokens,
      costUsd: asFiniteNumber(tokens["cost_usd"]),
      // 'failed'/'error' are the only non-success terminal states the producer
      // writes; 'complete' and 'skipped' both count as success.
      success: status !== "failed" && status !== "error",
    });
  }
  return out;
}

// ─── Mapper ──────────────────────────────────────────────────────────────────

/**
 * Maps one parsed history JSONL record to a V4 telemetry record, or returns a
 * skip reason when the record cannot produce a schema-valid V4 record.
 */
export function mapHistoryRecordToV4(raw: unknown): MapResult {
  const r = asRecord(raw);
  if (!r) return { ok: false, reason: "not an object" };

  // Only pipeline-run records map to pipeline-run telemetry.
  if (r["record_type"] !== undefined && r["record_type"] !== "run") {
    return { ok: false, reason: `record_type=${String(r["record_type"])}` };
  }

  const issueNumber = asFiniteNumber(r["issue_number"]);
  if (issueNumber == null || issueNumber <= 0) {
    return { ok: false, reason: "missing/invalid issue_number" };
  }

  // repo is REQUIRED by V4 and must be "owner/name" (no whitespace). Pre-`repo`
  // history lines omit it — skip them rather than guess a repo.
  const repo = r["repo"];
  if (typeof repo !== "string" || !/^[^\s]+\/[^\s]+$/u.test(repo)) {
    return { ok: false, reason: "missing/invalid repo (pre-repo record?)" };
  }

  const startedAt = toUtcIso(r["started_at"]);
  if (startedAt == null) {
    return { ok: false, reason: "missing/unparseable started_at" };
  }

  const outcome = asOutcome(r["outcome"]);
  if (outcome == null) {
    return { ok: false, reason: `unmappable outcome=${String(r["outcome"])}` };
  }

  const tokens = asRecord(r["tokens"]) ?? {};
  const routing = asRecord(r["routing"]) ?? {};
  const terminalFailureKind =
    typeof r["terminal_failure_kind"] === "string"
      ? (r["terminal_failure_kind"] as string).slice(0, 100)
      : null;

  return {
    ok: true,
    record: {
      schemaVersion: TELEMETRY_SCHEMA_VERSION_V4,
      issueNumber: Math.floor(issueNumber),
      repo,
      pipelineRunId: validTelemetryRunID(r["run_id"]),
      startedAt,
      completedAt: toUtcIso(r["completed_at"]),
      outcome,
      terminalFailureKind,
      predictedSize: null,
      actualSize: asSize(r["size"]),
      predictedModel: null,
      actualModel: null,
      complexityScore: asComplexity(routing["complexity_score"]),
      retries: 0,
      durationMs: asFiniteNumber(r["total_duration_ms"]),
      totalCostUsd: asFiniteNumber(tokens["estimated_cost_usd"]),
      stages: mapStages(r["stages"], tokens["per_stage"]),
      agents: [],
      routingPath: toRoutingPath(routing["path"]),
    },
  };
}
