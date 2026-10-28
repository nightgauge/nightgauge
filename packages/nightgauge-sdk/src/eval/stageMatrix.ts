/**
 * Cross-provider adapter × stage verification matrix (Issue #2).
 *
 * Drives one representative issue through the six pipeline stages once per
 * execution adapter (`nightgauge run <issue> --adapter <name>`) and records one
 * evidence row per adapter × stage. Every side effect — the pipeline run, the
 * run-record read, the adapter CLI probe — is injected, so this module is
 * offline-testable; `scripts/evaluate-adapter-matrix.ts` supplies the live
 * dependencies behind `NIGHTGAUGE_SKILL_EVAL_LIVE=1`.
 *
 * The agentic gate is consulted, never reimplemented: an adapter
 * {@link isAgenticAdapter} rejects (Ollama, LM Studio — providers, not
 * pipeline adapters) yields six `skipped: agentic-gate` rows before any probe
 * or spawn. The Go `AgenticNames()` gate stays the runtime authority.
 *
 * @see docs/SKILL_EVALUATION.md#cross-provider-adapter--stage-matrix
 * @see docs/ADAPTER_MATRIX.md
 */

import { z } from "zod";
import { isAgenticAdapter } from "../cli/adapters/AdapterRegistry.js";
import { maybeResolveEvalAdapterProfile } from "./evalAdapters.js";
import { providerForAdapter } from "./modelRegistry.js";
import type { PipelineRunRecord, PipelineRunResult } from "./pipelineCellExecutor.js";
import { PIPELINE_SKILLS } from "./schemas.js";

export const StageMatrixSkipReasonSchema = z.enum([
  "adapter-unavailable",
  "agentic-gate",
  "routed",
]);
export type StageMatrixSkipReason = z.infer<typeof StageMatrixSkipReasonSchema>;

export const StageMatrixRowSchema = z.object({
  adapter: z.string(),
  stage: z.string(),
  status: z.enum(["pass", "fail", "skipped"]),
  skip_reason: StageMatrixSkipReasonSchema.optional(),
  reason: z.string().optional(),
  model_identity: z.string().nullable(),
  tokens_available: z.boolean(),
  supported_controls: z.object({
    model: z.boolean(),
    effort: z.boolean(),
    reasoning: z.boolean(),
  }),
  evidence: z.object({
    run_id: z.string().optional(),
    branch: z.string().optional(),
    duration_ms: z.number().optional(),
    input_tokens: z.number().optional(),
    output_tokens: z.number().optional(),
  }),
  recorded_at: z.string(),
});
export type StageMatrixRow = z.infer<typeof StageMatrixRowSchema>;

/** Result of probing for an adapter's CLI; never reads or echoes credentials. */
export interface AdapterProbeResult {
  available: boolean;
  reason?: string;
}

export type AdapterProbe = (adapter: string) => Promise<AdapterProbeResult>;

/** Runs the whole pipeline for `issueNumber` with `adapter` pinned. */
export type StageMatrixRunFn = (args: {
  adapter: string;
  issueNumber: number;
  repo: string;
  timeoutMs: number;
}) => Promise<PipelineRunResult>;

/** Reads the run record the adapter's run left behind, or undefined. */
export type StageMatrixRecordReader = (
  adapter: string,
  issueNumber: number
) => Promise<(PipelineRunRecord & { run_id?: string; duration_ms?: number }) | undefined>;

export interface StageMatrixOptions {
  adapters: readonly string[];
  issueNumber: number;
  repo: string;
  timeoutMs: number;
  run: StageMatrixRunFn;
  readRecord: StageMatrixRecordReader;
  probe: AdapterProbe;
  /** Clock for `recorded_at` (injectable for tests). */
  now?: () => Date;
}

const SECRET_PATTERNS: readonly RegExp[] = [
  /\bsk-[A-Za-z0-9_-]{8,}/g,
  /\bgh[pousr]_[A-Za-z0-9]{8,}/g,
  /\bgithub_pat_[A-Za-z0-9_]{8,}/g,
  /\bxox[abprs]-[A-Za-z0-9-]{8,}/g,
  /\bAIza[A-Za-z0-9_-]{20,}/g,
  /\b[Bb]earer\s+[A-Za-z0-9._~+/=-]{8,}/g,
];

/** Replace credential-shaped substrings with `[REDACTED]`. */
export function redactSecrets(text: string): string {
  return SECRET_PATTERNS.reduce((s, re) => s.replace(re, "[REDACTED]"), text);
}

/** Which axes the adapter's eval profile can actually drive. */
export function supportedControlsFor(adapter: string): StageMatrixRow["supported_controls"] {
  const profile = maybeResolveEvalAdapterProfile(providerForAdapter(adapter));
  if (!profile) return { model: false, effort: false, reasoning: false };
  let effort = true;
  try {
    profile.buildSpawnPlan("probe-model", "medium", "none");
  } catch {
    effort = false;
  }
  return { model: true, effort, reasoning: profile.hasSeparateThinkingKnob };
}

/** What to do with an adapter before any pipeline run. */
export type AdapterCellPlan =
  | { runnable: true; adapter: string }
  | { runnable: false; adapter: string; rows: StageMatrixRow[] };

function baseRow(adapter: string, stage: string, recordedAt: string): StageMatrixRow {
  return {
    adapter,
    stage,
    status: "skipped",
    model_identity: null,
    tokens_available: false,
    supported_controls: supportedControlsFor(adapter),
    evidence: {},
    recorded_at: recordedAt,
  };
}

function rowsForAll(
  adapter: string,
  recordedAt: string,
  patch: Partial<StageMatrixRow>
): StageMatrixRow[] {
  return PIPELINE_SKILLS.map((stage) => ({ ...baseRow(adapter, stage, recordedAt), ...patch }));
}

/** Gate, then probe: six skipped rows, or a runnable plan. */
export async function planAdapterCells(
  adapter: string,
  probe: AdapterProbe,
  recordedAt: string
): Promise<AdapterCellPlan> {
  if (!isAgenticAdapter(adapter)) {
    return {
      runnable: false,
      adapter,
      rows: rowsForAll(adapter, recordedAt, {
        skip_reason: "agentic-gate",
        reason: `'${adapter}' is not an agentic pipeline adapter`,
      }),
    };
  }
  const probed = await probe(adapter);
  if (!probed.available) {
    return {
      runnable: false,
      adapter,
      rows: rowsForAll(adapter, recordedAt, {
        skip_reason: "adapter-unavailable",
        reason: redactSecrets(probed.reason ?? `'${adapter}' CLI not found`),
      }),
    };
  }
  return { runnable: true, adapter };
}

/** One row per pipeline stage from a completed run's history record. */
export function rowsFromRecord(
  adapter: string,
  record: NonNullable<Awaited<ReturnType<StageMatrixRecordReader>>>,
  recordedAt: string
): StageMatrixRow[] {
  const perStage = (record.tokens?.per_stage ?? {}) as Record<
    string,
    { input?: number; output?: number; cost_usd?: number }
  >;
  return PIPELINE_SKILLS.map((stage) => {
    const row = baseRow(adapter, stage, recordedAt);
    row.evidence = {
      ...(record.run_id ? { run_id: record.run_id } : {}),
      ...(record.branch ? { branch: redactSecrets(record.branch) } : {}),
      ...(record.duration_ms !== undefined ? { duration_ms: record.duration_ms } : {}),
    };
    const st = record.stages?.[stage];
    if (!st) return { ...row, status: "fail", reason: "stage absent from run record" };
    const tok = perStage[stage];
    const input = tok?.input ?? 0;
    const output = tok?.output ?? 0;
    row.model_identity = st.model_selection?.model ? redactSecrets(st.model_selection.model) : null;
    row.tokens_available = input + output > 0;
    if (row.tokens_available)
      row.evidence = { ...row.evidence, input_tokens: input, output_tokens: output };
    if (st.status === "complete") return { ...row, status: "pass" };
    if (st.status === "skipped") return { ...row, status: "skipped", skip_reason: "routed" };
    return {
      ...row,
      status: "fail",
      reason: redactSecrets(`stage status: ${st.status ?? "unknown"}`),
    };
  });
}

function tail(s: string): string {
  const lines = s.trim().split("\n");
  return (lines[lines.length - 1] ?? "").slice(0, 300);
}

/** Run the matrix. Never throws for a cell: failures become `fail` rows. */
export async function runStageMatrix(opts: StageMatrixOptions): Promise<StageMatrixRow[]> {
  const now = opts.now ?? (() => new Date());
  const rows: StageMatrixRow[] = [];
  for (const adapter of opts.adapters) {
    const recordedAt = now().toISOString();
    const plan = await planAdapterCells(adapter, opts.probe, recordedAt);
    if (!plan.runnable) {
      rows.push(...plan.rows);
      continue;
    }
    let failure: string | undefined;
    try {
      const result = await opts.run({
        adapter,
        issueNumber: opts.issueNumber,
        repo: opts.repo,
        timeoutMs: opts.timeoutMs,
      });
      if (result.timedOut) failure = `pipeline run timed out after ${opts.timeoutMs}ms`;
      else if (result.code !== 0) {
        failure = `pipeline run exited ${result.code}: ${tail(result.stderr || result.stdout)}`;
      }
    } catch (err) {
      failure = `pipeline run threw: ${err instanceof Error ? err.message : String(err)}`;
    }
    let record: Awaited<ReturnType<StageMatrixRecordReader>>;
    try {
      record = await opts.readRecord(adapter, opts.issueNumber);
    } catch {
      record = undefined;
    }
    if (record) {
      const fromRecord = rowsFromRecord(adapter, record, recordedAt);
      // A failed run still reports the stages it reached; unreached ones carry the run's reason.
      rows.push(
        ...fromRecord.map((r) =>
          failure && r.status === "fail" ? { ...r, reason: redactSecrets(failure) } : r
        )
      );
    } else {
      rows.push(
        ...rowsForAll(adapter, recordedAt, {
          status: "fail",
          reason: redactSecrets(failure ?? "no run record found for the issue"),
        })
      );
    }
  }
  return rows;
}

/** One JSON object per line; every row re-validated against the schema. */
export function toJsonl(rows: readonly StageMatrixRow[]): string {
  return rows.map((r) => JSON.stringify(StageMatrixRowSchema.parse(r))).join("\n") + "\n";
}

/** Adapter rows × stage columns, one status cell each. */
export function renderMarkdownTable(rows: readonly StageMatrixRow[]): string {
  const adapters = [...new Set(rows.map((r) => r.adapter))];
  const cell = (r: StageMatrixRow | undefined): string => {
    if (!r) return "—";
    if (r.status === "skipped") return `skipped: ${r.skip_reason ?? "unknown"}`;
    return r.model_identity ? `${r.status} (${r.model_identity})` : r.status;
  };
  const lines = [
    `| Adapter | ${PIPELINE_SKILLS.join(" | ")} |`,
    `| --- | ${PIPELINE_SKILLS.map(() => "---").join(" | ")} |`,
  ];
  for (const a of adapters) {
    const cells = PIPELINE_SKILLS.map((s) =>
      cell(rows.find((r) => r.adapter === a && r.stage === s))
    );
    lines.push(`| ${a} | ${cells.join(" | ")} |`);
  }
  return lines.join("\n") + "\n";
}
