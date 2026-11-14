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
  /**
   * The provider that served the stage's model, for a multi-provider adapter
   * (ADR-022 § 2), and the declared endpoint id that served it. Sourced from
   * `model_selection.model_provider` / `model_selection.endpoint` as
   * recorded, never derived from the model string, and mirroring the Go
   * mapper's `ModelProvider`/`Endpoint` (internal/platform/
   * execution_history_mapper.go). The platform's strict stage schema
   * declares both optional and nullable: absent or label-invalid values omit
   * the key, so a stage of any other adapter is sent unchanged.
   */
  modelProvider?: string;
  endpoint?: string;
  /**
   * executionPath/modelEffort/modelReasoning mirror V2StageDetail's fields of
   * the same name (internal/state/history.go) verbatim — read directly from
   * `detail`, with NO fallback to `model_selection`/`tokens.adapter` (unlike
   * `model` above). Absent provenance serializes as `null`, never borrows
   * another field's value (#217).
   */
  executionPath: string | null;
  modelEffort: string | null;
  modelReasoning: string | null;
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
  /**
   * "private" for a run its creator chose to keep private (#2400), from the
   * JSONL record's `visibility`. Omitted for team: the platform declares it
   * optional, reads absent as team, and a private record raises the run and
   * nothing lowers it.
   */
  visibility?: "private";
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

/** Truncated string, or null for a non-string/empty value — matches nonEmptyTruncatedPtr (Go). */
function asNonEmptyString(v: unknown, max = 100): string | null {
  return typeof v === "string" && v !== "" ? v.slice(0, max) : null;
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

/**
 * The platform's guard for a stage's modelProvider and endpoint labels, the
 * grammar of an OpenCode provider key — identical to the Go mapper's
 * `telemetryLabelPattern` / `telemetryLabelMax`.
 */
const TELEMETRY_LABEL_PATTERN = /^[a-z0-9][a-z0-9-]*$/u;
const TELEMETRY_LABEL_MAX = 64;

/**
 * Returns the label when the platform's guard accepts it, else undefined. The
 * stage schema is strict, so a label it rejects would drop the whole run's
 * upload. Mirrors the Go mapper's `telemetryLabel`.
 */
function telemetryLabel(v: unknown): string | undefined {
  return typeof v === "string" && v.length <= TELEMETRY_LABEL_MAX && TELEMETRY_LABEL_PATTERN.test(v)
    ? v
    : undefined;
}

/** cost_source values that mark a priced figure (Go state.CostSource*). */
const STAMPED_COST_SOURCES: readonly string[] = ["computed", "native", "deterministic"];

/**
 * Whether a stage's cost is a priced figure: priced from a rate card,
 * reported by the CLI, or the exact zero of a stage that dispatched no model,
 * and not unstamped. A record that names no source is not one: absence means
 * source-unknown, never priced. Mirrors the Go mapper's `stampedStageCost`.
 */
function stampedStageCost(tokens: Record<string, unknown>): boolean {
  if (tokens["cost_unstamped"] === true) return false;
  return (
    typeof tokens["cost_source"] === "string" &&
    STAMPED_COST_SOURCES.includes(tokens["cost_source"])
  );
}

/**
 * Whether every stage cost of a run is a priced figure, so a zero run total
 * is a real zero rather than an unknown. Mirrors the Go mapper's
 * `runCostStamped`.
 */
function runCostStamped(runTokens: Record<string, unknown>): boolean {
  if (runTokens["cost_unstamped"] === true) return false;
  const perStage = asRecord(runTokens["per_stage"]);
  if (!perStage) return false;
  const entries = Object.values(perStage);
  return entries.length > 0 && entries.every((t) => stampedStageCost(asRecord(t) ?? {}));
}

/**
 * The run's totalCostUsd, by the Go mapper's switch: the run-level estimate
 * when non-zero; else the sum of the stage costs when positive; else 0 when
 * every stage is a stamped zero (a run that used only local models); else
 * null — an unknown cost is never sent as 0 (ADR-022 § 3, #1630).
 */
function runTotalCostUsd(
  runTokens: Record<string, unknown>,
  summedStageCostUsd: number
): number | null {
  const estimate = asFiniteNumber(runTokens["estimated_cost_usd"]);
  if (estimate != null && estimate !== 0) return estimate;
  if (summedStageCostUsd > 0) return summedStageCostUsd;
  if (runCostStamped(runTokens)) return 0;
  return null;
}

function mapStages(
  stagesRaw: unknown,
  perStageTokensRaw: unknown
): { stages: V4StageMetric[]; summedCostUsd: number } {
  const stages = asRecord(stagesRaw);
  if (!stages) return { stages: [], summedCostUsd: 0 };
  const perStage = asRecord(perStageTokensRaw) ?? {};

  const out: V4StageMetric[] = [];
  let summedCostUsd = 0;
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

    // A stamped zero, such as a local model's, is sent as 0: null tells the
    // platform the cost is unknown (#1630, ADR-022 § 3). tokens
    // ["cost_unstamped"] and ["cost_source"] are READ for that decision but
    // never forwarded: the platform's `.strict()` V4 Zod schema has no field
    // for them and rejects the WHOLE record on any unknown key, which would
    // reintroduce the "0 runs" failure mode this file's header documents.
    const rawCost = asFiniteNumber(tokens["cost_usd"]);
    let costUsd: number | null = null;
    if ((rawCost != null && rawCost !== 0) || stampedStageCost(tokens)) {
      costUsd = rawCost ?? 0;
      summedCostUsd += costUsd;
    }

    // model_selection's model_provider and endpoint are sent as recorded,
    // label-guarded (mirrors the Go mapper). Its dispatch-envelope fields —
    // adapter, served_model, effort, thinking, mode (#580) — are not:
    // V4StageMetric has no properties for them, so they cannot leak into the
    // strict V4 schema until a coordinated platform schema change adds them.
    const modelProvider = telemetryLabel(modelSelection?.["model_provider"]);
    const endpoint = telemetryLabel(modelSelection?.["endpoint"]);
    out.push({
      stageId: stageName.slice(0, 100),
      stageName: stageName.slice(0, 100),
      attempt: 1,
      model: model ? model.slice(0, 100) : null,
      durationMs: asFiniteNumber(detail["duration_ms"]),
      inputTokens,
      outputTokens,
      totalTokens: inputTokens + outputTokens,
      costUsd,
      // 'failed'/'error' are the only non-success terminal states the producer
      // writes; 'complete' and 'skipped' both count as success.
      success: status !== "failed" && status !== "error",
      // Read directly from detail — no fallback to model_selection/tokens.adapter (#217).
      executionPath: asNonEmptyString(detail["execution_path"]),
      modelEffort: asNonEmptyString(detail["model_effort"]),
      modelReasoning: asNonEmptyString(detail["model_reasoning"]),
      ...(modelProvider !== undefined ? { modelProvider } : {}),
      ...(endpoint !== undefined ? { endpoint } : {}),
    });
  }
  return { stages: out, summedCostUsd };
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
  const { stages, summedCostUsd } = mapStages(r["stages"], tokens["per_stage"]);
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
      ...(r["visibility"] === "private" ? { visibility: "private" as const } : {}),
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
      totalCostUsd: runTotalCostUsd(tokens, summedCostUsd),
      stages,
      agents: [],
      routingPath: toRoutingPath(routing["path"]),
    },
  };
}
