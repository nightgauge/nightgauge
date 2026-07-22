/**
 * TelemetryUploaderService — Uploads pipeline-run, health-snapshot, and
 * recommendation-outcome JSONL streams to the platform telemetry endpoints.
 *
 * Streams:
 *   - `pipeline-run`: daily JSONL files in `.nightgauge/pipeline/history/`
 *                     → POST /v1/telemetry/pipeline-run
 *   - `health`:       single file `.nightgauge/pipeline/health-history.jsonl`
 *                     → POST /v1/telemetry/health-snapshot
 *   - `recommendation`: single file `.nightgauge/pipeline/recommendation-history.jsonl`
 *                       → POST /v1/telemetry/recommendation-outcome
 *                       (only finalized records where `metric_after` is populated)
 *   - `trace`:        per-run JSONL files in `.nightgauge/pipeline/trace/`
 *                     → POST /v1/telemetry/pipeline-trace
 *                     (lifecycle decision trace, ADR 013 / Issue #180; events
 *                     upload verbatim — the (run_id, producer, seq) key makes
 *                     re-upload idempotent server-side)
 *
 * Maintains a per-file watermark at `.nightgauge/pipeline/.upload-watermarks.json`.
 * POSTs batches respecting per-stream batch size limits. Respects HTTP 429 with
 * exponential backoff. Per-stream consent gating via TelemetryConsentService.
 *
 * Follows the direct HTTP pattern established by AuditLogService — does NOT
 * go through Go IPC.
 *
 * @see Issue #3315 — Build TelemetryUploaderService for pipeline-run history JSONL
 * @see Issue #3316 — Extend Telemetry Uploader for Health-Snapshot + Recommendation Streams
 * @see Issue #3312 — Parent epic (platform endpoint implementation)
 */

import * as vscode from "vscode";
import * as path from "node:path";
import type { TelemetryConsentService } from "./TelemetryConsentService";
import type { TelemetryStream } from "./telemetry/types";
import type { Logger } from "../utils/logger";
import {
  mapHistoryRecordToV4,
  type ExecutionHistoryRunRecordV4,
} from "./telemetry/pipelineRunV4Mapper";

// ─── Constants ───────────────────────────────────────────────────────────────

const UPLOAD_INTERVAL_MS = 15 * 60 * 1000; // 15 minutes (idle cadence)
/**
 * While one or more runs are active the uploader switches to a short cadence so
 * newly-produced trace events reach the platform within ~10s (#234 / ADR 014
 * §D), instead of waiting out the 15-minute idle timer. Restored to
 * `UPLOAD_INTERVAL_MS` once every run completes.
 */
export const ACTIVE_RUN_UPLOAD_INTERVAL_MS = 10_000; // 10 seconds
/**
 * Minimum wall-clock gap between the START of two consecutive upload cycles.
 * This is the hard backpressure bound: bursts of progress events coalesce into
 * one upload rather than one-per-event, so the uploader can never exceed a
 * bounded request rate no matter how fast events arrive (#234 / ADR 014 §D).
 */
export const MIN_UPLOAD_GAP_MS = 5_000; // 5 seconds
/**
 * Number of progress events (per active run) that accrue before an eager flush
 * is scheduled. Sparser progress is still picked up by the short-interval timer
 * ({@link ACTIVE_RUN_UPLOAD_INTERVAL_MS}); either trigger is coalesced and rate
 * bounded by {@link MIN_UPLOAD_GAP_MS}.
 */
export const ACTIVE_RUN_FLUSH_EVENT_COUNT = 25;
const MAX_BATCH_SIZE_PIPELINE = 100;
const MAX_BATCH_SIZE_HEALTH = 500;
const MAX_BATCH_SIZE_RECOMMENDATION = 200;
/** ADR 013 §D fixes the trace upload batch ceiling at 500 events. */
const MAX_BATCH_SIZE_TRACE = 500;
/** Exported so tests can reference the boundary without duplicating the magic number. */
export const MAX_FILE_BYTES = 10 * 1024 * 1024; // 10 MB
/** Exponential backoff delays for HTTP retries — 6 total attempts, max 60s wait. */
const RETRY_DELAYS_MS = [1000, 2000, 4000, 8000, 16000, 60000];
/**
 * Max consecutive cycles a file may stay blocked on server-rejected records
 * before the uploader gives up and advances past them (logging loudly). Without
 * this cap a single permanently-rejected record (poison message) would block
 * every later record in the file forever. With it, rejections are retried (so
 * a transient/deploy-in-progress skew self-heals) but can never wedge the
 * stream. The platform's post-deploy telemetry canary is the systematic guard
 * against rejections; this is the local safety valve.
 */
const MAX_REJECTION_RETRY_CYCLES = 5;
const WATERMARK_FILENAME = ".upload-watermarks.json";
const HISTORY_SUBDIR = path.join(".nightgauge", "pipeline", "history");
const TRACE_SUBDIR = path.join(".nightgauge", "pipeline", "trace");
const WATERMARK_SUBDIR = path.join(".nightgauge", "pipeline");

// ─── Types ───────────────────────────────────────────────────────────────────

/** Tracks how many lines from each JSONL file have been uploaded. */
interface WatermarkStore {
  [filename: string]: number;
}

interface StreamConfig {
  stream: TelemetryStream;
  /** Relative path from incrediRoot to the JSONL file. */
  filePath: string;
  /** Absolute path segment appended to platformUrl (e.g. "/v1/telemetry/health-snapshot"). */
  endpoint: string;
  maxBatchSize: number;
  /** Optional record-level filter applied before batching. */
  filterRecord?: (record: Record<string, unknown>) => boolean;
  /**
   * Watermark-store key override. Defaults to the file's basename — the
   * per-run trace stream passes a "trace/"-prefixed key so a run-id filename
   * can never collide with the daily history / consolidated keys.
   */
  watermarkKey?: string;
}

interface StreamSummary {
  stream: TelemetryStream;
  uploaded: number;
  skipped: boolean;
  /**
   * True when a batch upload failed (endpoint down / non-2xx after retries).
   * The trace stream's per-file loop bails on the first failed file so an
   * unreachable endpoint costs one retry cycle, not one per trace file.
   */
  failed?: boolean;
}

/**
 * Wire body for every telemetry POST: a BARE JSON array of records. The
 * platform's canonical telemetry routes parse `record | record[]`
 * (`Array.isArray(body) ? body : [body]`) and strict-reject anything else —
 * the previous `{records: [...]}` envelope was wrapped as a single "record",
 * failed Zod validation, and every upload was silently rejected inside a 202
 * response (#261). `unknown[]` so both the consolidated streams
 * (Record<string, unknown>[]) and the pipeline-run stream
 * (ExecutionHistoryRunRecordV4[]) share the shape.
 */
type TelemetryBatch = unknown[];

// ─── Stream configs ───────────────────────────────────────────────────────────

const HEALTH_STREAM_CONFIG: StreamConfig = {
  stream: "health",
  filePath: path.join(".nightgauge", "pipeline", "health-history.jsonl"),
  endpoint: "/v1/telemetry/health-snapshot",
  maxBatchSize: MAX_BATCH_SIZE_HEALTH,
};

const RECOMMENDATION_STREAM_CONFIG: StreamConfig = {
  stream: "recommendation",
  filePath: path.join(".nightgauge", "pipeline", "recommendation-history.jsonl"),
  endpoint: "/v1/telemetry/recommendation-outcome",
  maxBatchSize: MAX_BATCH_SIZE_RECOMMENDATION,
  filterRecord: (r) => r["metric_after"] != null,
};

const CONSOLIDATED_STREAM_CONFIGS: readonly StreamConfig[] = [
  HEALTH_STREAM_CONFIG,
  RECOMMENDATION_STREAM_CONFIG,
];

// ─── Helpers ─────────────────────────────────────────────────────────────────

function sleep(ms: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

async function loadWatermarks(uri: vscode.Uri, logger?: Logger | null): Promise<WatermarkStore> {
  let bytes: Uint8Array;
  try {
    bytes = await vscode.workspace.fs.readFile(uri);
  } catch {
    // File not found on first run — start with empty watermarks.
    return {};
  }
  try {
    return JSON.parse(Buffer.from(bytes).toString("utf8")) as WatermarkStore;
  } catch {
    // Corrupt file — reset to empty so records are re-uploaded rather than skipped.
    logger?.warn("TelemetryUploaderService: watermark file is corrupt, resetting", {
      path: uri.fsPath,
    });
    return {};
  }
}

async function saveWatermarks(uri: vscode.Uri, store: WatermarkStore): Promise<void> {
  const tempUri = vscode.Uri.file(uri.fsPath + ".tmp");
  const bytes = Buffer.from(JSON.stringify(store, null, 2), "utf8");
  await vscode.workspace.fs.writeFile(tempUri, bytes);
  await vscode.workspace.fs.rename(tempUri, uri, { overwrite: true });
}

/**
 * POST a batch to the platform with exponential backoff on HTTP 429.
 * Returns the final Response (caller checks ok). Throws after all retries
 * exhausted.
 */
async function uploadWithRetry(
  url: string,
  headers: Record<string, string>,
  body: string,
  logger: Logger | null
): Promise<Response> {
  let lastError: Error | null = null;
  const totalAttempts = RETRY_DELAYS_MS.length + 1;

  for (let attempt = 0; attempt < totalAttempts; attempt++) {
    let response: Response;
    try {
      response = await fetch(url, { method: "POST", headers, body });
    } catch (err) {
      lastError = err instanceof Error ? err : new Error(String(err));
      logger?.warn(`TelemetryUploaderService: attempt ${attempt + 1} network error`, {
        error: lastError.message,
      });
      if (attempt < RETRY_DELAYS_MS.length) {
        await sleep(RETRY_DELAYS_MS[attempt]);
      }
      continue;
    }

    if (response.status === 429) {
      const delayMs = RETRY_DELAYS_MS[attempt] ?? 60000;
      logger?.warn(`TelemetryUploaderService: attempt ${attempt + 1} got 429, retrying`, {
        delayMs,
      });
      lastError = new Error(`HTTP 429 after attempt ${attempt + 1}`);
      if (attempt < RETRY_DELAYS_MS.length) {
        await sleep(delayMs);
      }
      continue;
    }

    return response;
  }

  throw lastError ?? new Error("TelemetryUploaderService: upload failed after max retries");
}

// ─── Service ─────────────────────────────────────────────────────────────────

export class TelemetryUploaderService implements vscode.Disposable {
  private timer: ReturnType<typeof setInterval> | null = null;
  private uploading = false;
  /**
   * Per-file count of consecutive upload cycles blocked on server-rejected
   * records. In-memory (process-scoped): a restart resets it, which simply
   * retries — acceptable. Capped by MAX_REJECTION_RETRY_CYCLES.
   */
  private readonly rejectionRetryCycles = new Map<string, number>();

  // ─── Active-run cadence state (#234 / ADR 014) ───────────────────────────
  /**
   * Number of pipeline runs currently in flight. Driven by the pipeline
   * lifecycle wiring (onRunStarted/onRunCompleted) as the single source of
   * truth. 0 → idle 15-minute cadence; >0 → short {@link
   * ACTIVE_RUN_UPLOAD_INTERVAL_MS} cadence so trace uploads keep pace with the
   * run. Correct under concurrent slots (only returns to idle when the last
   * run completes).
   */
  private activeRunCount = 0;
  /**
   * Handle for a coalesced near-term flush scheduled by {@link
   * scheduleActiveFlush}. Non-null means a flush is already pending, so further
   * progress events coalesce into it rather than scheduling their own.
   */
  private pendingFlushTimer: ReturnType<typeof setTimeout> | null = null;
  /** Epoch ms when the most recent upload cycle STARTED — the rate-bound anchor. */
  private lastUploadStartedAt = 0;
  /** Progress events observed since the last scheduled flush (event-count threshold). */
  private progressSinceFlush = 0;

  constructor(
    private readonly getLicenseKey: () => string | null | undefined,
    private readonly consentService: TelemetryConsentService,
    private readonly getPlatformUrl: () => string,
    private readonly incrediRoot: string,
    private readonly logger: Logger | null,
    // Fallback when no license key is configured: the OAuth access token (JWT).
    // The platform's telemetry ingest accepts either credential. Without this,
    // device-flow / community accounts can never upload telemetry, so the
    // dashboard's Analytics view stays empty.
    private readonly getAccessToken?: () => Promise<string | null>,
    /**
     * Returns every repo root in the workspace (primary + target repos),
     * mirroring `WorkspaceManager.getAllRepositories().map(r => r.path)` — the
     * existing multi-repo resolution mechanism (docs/MULTI_REPO_WORKSPACE.md).
     * The Go binary's interactive-run writer lands per-run history/trace
     * JSONL under the run's TARGET repo root (`repoRoot(p.Repo)`,
     * `internal/ipc/server.go`), which in a multi-repo workspace is not
     * always `incrediRoot` (the primary workspace root) — so the
     * pipeline-run and trace streams scan every root this returns, not just
     * `incrediRoot` (#247). Optional and defaults to `[incrediRoot]` alone
     * for single-repo workspaces or callers that predate #247. The
     * consolidated health/recommendation streams are whole-workspace
     * concepts written only under `incrediRoot` and are unaffected.
     */
    private readonly getWorkspaceRoots?: () => string[]
  ) {}

  /**
   * Resolve every repo root to scan for the pipeline-run and trace streams.
   * `incrediRoot` is always included (defensive — some workspace-root
   * resolutions may not enumerate the primary root explicitly); duplicates
   * are deduped via `Set`.
   */
  private resolveHistoryScanRoots(): string[] {
    const extraRoots = this.getWorkspaceRoots?.() ?? [];
    return Array.from(new Set([this.incrediRoot, ...extraRoots]));
  }

  /** Start the 15-minute periodic timer and run an immediate upload cycle. */
  initialize(): void {
    void this.runUploadCycle();
    this.setUploadCadence(UPLOAD_INTERVAL_MS);
  }

  /**
   * Redundant completion nudge from the Go pipeline history-writer IPC handler.
   * The active-run counter and cadence are owned by onRunStarted/onRunCompleted
   * (the single source of truth), so this ONLY triggers an idempotent flush —
   * it must NOT touch {@link activeRunCount}, or completion would be
   * double-counted against the lifecycle events wired in bootstrap/services.ts.
   * Re-entrancy-guarded inside runUploadCycle.
   */
  onPipelineCompleted(): void {
    void this.runUploadCycle();
  }

  /**
   * A run has started. Increments the active-run count; the first active run
   * switches the periodic timer to the short cadence and kicks off an immediate
   * (rate-bounded) flush so early trace events don't wait out the idle timer.
   */
  onRunStarted(): void {
    this.activeRunCount++;
    if (this.activeRunCount === 1) {
      this.setUploadCadence(ACTIVE_RUN_UPLOAD_INTERVAL_MS);
    }
    this.scheduleActiveFlush();
  }

  /**
   * A run made progress (stage transition, phase, decision). Once a burst of
   * {@link ACTIVE_RUN_FLUSH_EVENT_COUNT} events has accrued, schedule an eager
   * (coalesced, rate-bounded) flush; sparser progress is caught by the
   * short-interval timer. Cheap and safe to call on every progress signal.
   */
  onRunProgress(): void {
    this.progressSinceFlush++;
    if (this.progressSinceFlush >= ACTIVE_RUN_FLUSH_EVENT_COUNT) {
      this.scheduleActiveFlush();
    }
  }

  /**
   * A run completed. Decrements the active-run count (floored at 0 so a
   * completion without a matching start can never drive it negative); the last
   * run restores the idle cadence. Always runs one final flush so the run's
   * trailing trace events upload promptly.
   */
  onRunCompleted(): void {
    if (this.activeRunCount > 0) {
      this.activeRunCount--;
    }
    if (this.activeRunCount === 0) {
      this.setUploadCadence(UPLOAD_INTERVAL_MS);
    }
    void this.runUploadCycle();
  }

  /**
   * Swap the periodic upload timer to a new interval. Clears any existing timer
   * first so exactly one interval runs at a time (idle ⇄ active cadence).
   */
  private setUploadCadence(intervalMs: number): void {
    if (this.timer !== null) {
      clearInterval(this.timer);
    }
    this.timer = setInterval(() => void this.runUploadCycle(), intervalMs);
  }

  /**
   * Coalescing core of the continuous-upload path. If a flush is already
   * pending, the caller coalesces into it (bounded request rate). Otherwise
   * schedule one no sooner than {@link MIN_UPLOAD_GAP_MS} after the previous
   * cycle STARTED — bursts collapse to one upload, and the rate can never
   * exceed one cycle per gap. The pending handle is held until the cycle
   * finishes so events arriving mid-upload coalesce rather than stacking.
   */
  private scheduleActiveFlush(): void {
    if (this.pendingFlushTimer !== null) {
      return; // coalesce — a flush is already pending
    }
    const elapsed = Date.now() - this.lastUploadStartedAt;
    const delay = Math.max(0, MIN_UPLOAD_GAP_MS - elapsed);
    this.pendingFlushTimer = setTimeout(() => {
      void (async () => {
        try {
          await this.runUploadCycle();
        } finally {
          this.pendingFlushTimer = null;
        }
      })();
    }, delay);
  }

  dispose(): void {
    if (this.timer !== null) {
      clearInterval(this.timer);
      this.timer = null;
    }
    if (this.pendingFlushTimer !== null) {
      clearTimeout(this.pendingFlushTimer);
      this.pendingFlushTimer = null;
    }
  }

  // ─── Upload cycle ───────────────────────────────────────────────────────

  async runUploadCycle(): Promise<void> {
    if (this.uploading) {
      return;
    }

    // Anchor the rate bound at the moment this cycle STARTS (not when it ends),
    // and reset the progress-event accumulator — the pending flush this cycle
    // satisfies has now been consumed (#234 / ADR 014 §D).
    this.lastUploadStartedAt = Date.now();
    this.progressSinceFlush = 0;

    if (!this.consentService.isEnabled()) {
      this.logger?.info("TelemetryUploaderService: skipping — consent not granted");
      return;
    }

    this.uploading = true;
    // Prefer a license key; fall back to the OAuth access token (JWT) so
    // device-flow / community accounts can still upload. The platform's
    // telemetry ingest accepts either credential as a Bearer token.
    let token: string | null = this.getLicenseKey() ?? null;
    if (!token && this.getAccessToken) {
      token = await this.getAccessToken();
    }

    if (!token) {
      this.logger?.info("TelemetryUploaderService: skipping — no license key or access token");
      this.uploading = false;
      return;
    }
    try {
      await this.uploadAll(token);
    } catch (err) {
      const msg = err instanceof Error ? err.message : String(err);
      this.logger?.error("TelemetryUploaderService: upload cycle failed", { error: msg });
    } finally {
      this.uploading = false;
    }
  }

  private async uploadAll(token: string): Promise<void> {
    const summary: StreamSummary[] = [];

    // Stream 1: pipeline-run (daily-rotating directory scan)
    if (this.consentService.isStreamEnabled("pipeline-run")) {
      const result = await this.uploadPipelineRunStream(token);
      summary.push(result);
    }

    // Streams 2 & 3: single consolidated files
    for (const cfg of CONSOLIDATED_STREAM_CONFIGS) {
      if (this.consentService.isStreamEnabled(cfg.stream)) {
        const result = await this.uploadConsolidatedStream(token, cfg);
        summary.push(result);
      }
    }

    // Stream 4: lifecycle trace (per-run directory scan, ADR 013 / #180)
    if (this.consentService.isStreamEnabled("trace")) {
      const result = await this.uploadTraceStream(token);
      summary.push(result);
    }

    // Single output-channel summary line per cycle
    const totalUploaded = summary.reduce((n, s) => n + s.uploaded, 0);
    if (totalUploaded > 0 || summary.some((s) => s.skipped)) {
      this.logger?.info("TelemetryUploaderService: cycle complete", {
        streams: summary.map((s) => `${s.stream}:+${s.uploaded}`).join(", "),
        total: totalUploaded,
      });
    }
  }

  /**
   * Upload the pipeline-run stream across every workspace repo root
   * (#247) — the Go binary's history writer lands interactive-run records
   * under the run's TARGET repo root, not necessarily `incrediRoot`, so
   * scanning only the primary root silently missed target-repo runs in a
   * multi-repo workspace. Stops scanning further roots once a root reports
   * an aborted upload (endpoint down/unreachable) — every other root would
   * fail identically against the same endpoint this cycle.
   */
  private async uploadPipelineRunStream(token: string): Promise<StreamSummary> {
    let totalUploaded = 0;
    let anyScanned = false;

    for (const root of this.resolveHistoryScanRoots()) {
      const result = await this.uploadPipelineRunStreamForRoot(token, root);
      totalUploaded += result.uploaded;
      if (!result.skipped) {
        anyScanned = true;
      }
      if (result.aborted) {
        break;
      }
    }

    return { stream: "pipeline-run", uploaded: totalUploaded, skipped: !anyScanned };
  }

  /**
   * Scan a single repo root's `.nightgauge/pipeline/history/` directory and
   * upload any unwatermarked lines. Extracted from the (formerly
   * single-root) `uploadPipelineRunStream` so it can be run once per
   * workspace repo root (#247).
   */
  private async uploadPipelineRunStreamForRoot(
    token: string,
    root: string
  ): Promise<{ uploaded: number; skipped: boolean; aborted: boolean }> {
    const historyDirUri = vscode.Uri.file(path.join(root, HISTORY_SUBDIR));
    const watermarkUri = vscode.Uri.file(path.join(root, WATERMARK_SUBDIR, WATERMARK_FILENAME));

    // Enumerate JSONL files
    let entries: [string, vscode.FileType][];
    try {
      entries = await vscode.workspace.fs.readDirectory(historyDirUri);
    } catch {
      // History directory may not exist yet (e.g. root has never run a pipeline)
      return { uploaded: 0, skipped: true, aborted: false };
    }

    const jsonlFiles = entries
      .filter(([name, type]) => name.endsWith(".jsonl") && type === vscode.FileType.File)
      .map(([name]) => name);

    if (jsonlFiles.length === 0) {
      return { uploaded: 0, skipped: false, aborted: false };
    }

    const watermarks = await loadWatermarks(watermarkUri, this.logger);
    let watermarksDirty = false;
    let totalUploaded = 0;
    let rootAborted = false;

    for (const filename of jsonlFiles) {
      // History filenames are date-stamped (e.g. `2026-05-10.jsonl`), so two
      // repo roots can easily produce identically-named files on the same
      // day. Qualify the poison-message retry counter by root so a stuck
      // file in one repo can never bleed its cycle count into another.
      const retryKey = `${root}::${filename}`;
      const fileUri = vscode.Uri.file(path.join(root, HISTORY_SUBDIR, filename));

      // Size guard — skip files larger than 10 MB
      try {
        const stat = await vscode.workspace.fs.stat(fileUri);
        if (stat.size > MAX_FILE_BYTES) {
          this.logger?.warn("TelemetryUploaderService: skipping oversized file", {
            filename,
            sizeBytes: stat.size,
            limitBytes: MAX_FILE_BYTES,
          });
          continue;
        }
      } catch {
        continue;
      }

      // Read file content
      let content: string;
      try {
        const bytes = await vscode.workspace.fs.readFile(fileUri);
        content = Buffer.from(bytes).toString("utf8");
      } catch {
        continue;
      }

      const allLines = content.split("\n").filter((line) => line.trim().length > 0);

      const uploadedCount = watermarks[filename] ?? 0;
      const newLines = allLines.slice(uploadedCount);

      if (newLines.length === 0) {
        continue;
      }

      // Map each new line to the platform's strict V4 wire shape. Lines that
      // cannot map (malformed JSON, pre-`repo` records, non-run records) are
      // permanently skippable — they neither upload nor block the watermark.
      const toSend: { lineIdx: number; v4: ExecutionHistoryRunRecordV4 }[] = [];
      const doneLines = new Set<number>(); // skipped OR accepted = consumed
      for (let i = 0; i < newLines.length; i++) {
        let parsed: unknown;
        try {
          parsed = JSON.parse(newLines[i]!);
        } catch {
          doneLines.add(i);
          this.logger?.warn("TelemetryUploaderService: skipping malformed JSONL line", {
            filename,
          });
          continue;
        }
        const mapped = mapHistoryRecordToV4(parsed);
        if (mapped.ok) {
          toSend.push({ lineIdx: i, v4: mapped.record });
        } else {
          doneLines.add(i);
          this.logger?.warn("TelemetryUploaderService: skipping unmappable run record", {
            filename,
            reason: mapped.reason,
          });
        }
      }

      // Upload mappable records in batches; track which source lines the
      // platform accepted vs rejected (rejected indices are batch-relative).
      let uploadAborted = false;
      let hadRejections = false;
      let batchStart = 0;
      while (batchStart < toSend.length) {
        const batch = toSend.slice(batchStart, batchStart + MAX_BATCH_SIZE_PIPELINE);
        const batchBody: TelemetryBatch = batch.map((e) => e.v4);
        const url = `${this.getPlatformUrl()}/v1/telemetry/pipeline-run`;
        const headers: Record<string, string> = {
          Authorization: `Bearer ${token}`,
          "Content-Type": "application/json",
        };

        let response: Response;
        try {
          response = await uploadWithRetry(url, headers, JSON.stringify(batchBody), this.logger);
        } catch (err) {
          const msg = err instanceof Error ? err.message : String(err);
          this.logger?.error("TelemetryUploaderService: batch upload failed after retries", {
            filename,
            batchStart,
            batchSize: batch.length,
            error: msg,
          });
          uploadAborted = true;
          break;
        }

        if (!response.ok) {
          // 404 = endpoint not deployed yet (info, low noise); anything else is
          // a real error. Either way abort this file, leaving these lines behind
          // the watermark so they retry once the endpoint is healthy.
          const logFn = response.status === 404 ? "info" : "error";
          this.logger?.[logFn]("TelemetryUploaderService: batch upload got non-2xx", {
            filename,
            status: response.status,
            batchStart,
          });
          uploadAborted = true;
          break;
        }

        // 202 Accepted carries a per-record {accepted, rejected} body — a 202 is
        // NOT proof of success. Parse it so rejected records are surfaced loudly
        // and are NOT silently consumed by the watermark (the original bug).
        let acceptedInBatch = batch.length;
        const rejectedBatchIdx = new Set<number>();
        try {
          const body = (await response.json()) as {
            accepted?: number;
            rejected?: { index: number; reason?: string }[];
          };
          const rejected = body?.rejected ?? [];
          for (const r of rejected) rejectedBatchIdx.add(r.index);
          acceptedInBatch =
            typeof body?.accepted === "number" ? body.accepted : batch.length - rejected.length;
          if (rejected.length > 0) {
            hadRejections = true;
            this.logger?.error(
              "TelemetryUploaderService: platform REJECTED pipeline-run records — likely a producer/consumer schema or status-vocab skew; see acme-platform docs/runbooks/operations.md → Diagnosing telemetry not reaching the dashboard",
              {
                filename,
                rejected: rejected.length,
                accepted: acceptedInBatch,
                sampleReasons: rejected.slice(0, 3).map((r) => r.reason ?? "unknown"),
              }
            );
          }
        } catch {
          // Unparseable 202 body: the server returned success, so treat the
          // batch as accepted rather than re-sending. Logged for visibility.
          this.logger?.warn(
            "TelemetryUploaderService: 202 with unparseable body — assuming batch accepted",
            { filename, batchSize: batch.length }
          );
        }

        for (let j = 0; j < batch.length; j++) {
          if (!rejectedBatchIdx.has(j)) doneLines.add(batch[j]!.lineIdx);
        }
        totalUploaded += acceptedInBatch;
        if (rejectedBatchIdx.size === 0) {
          this.logger?.info("TelemetryUploaderService: batch uploaded", {
            filename,
            count: acceptedInBatch,
          });
        }
        batchStart += batch.length;
      }

      // Advance the watermark over the longest contiguous prefix of new lines
      // that are all consumed (skipped or accepted). A rejected or not-yet-sent
      // line stops the advance so it retries on the next cycle.
      let advanceBy = 0;
      while (advanceBy < newLines.length && doneLines.has(advanceBy)) advanceBy++;

      if (hadRejections && advanceBy < newLines.length) {
        // Blocked on rejected records. Retry next cycle — unless we've been
        // stuck on this file too long (poison message), in which case advance
        // past the new range to unblock the stream (records dropped, but each
        // was already logged at error above).
        const cycles = (this.rejectionRetryCycles.get(retryKey) ?? 0) + 1;
        if (cycles >= MAX_REJECTION_RETRY_CYCLES) {
          this.logger?.error(
            "TelemetryUploaderService: giving up on rejected records after repeated cycles — advancing past them to unblock the file (records dropped; see prior rejection errors)",
            { filename, root, cycles }
          );
          watermarks[filename] = allLines.length;
          watermarksDirty = true;
          this.rejectionRetryCycles.delete(retryKey);
        } else {
          this.rejectionRetryCycles.set(retryKey, cycles);
          if (advanceBy > 0) {
            watermarks[filename] = uploadedCount + advanceBy;
            watermarksDirty = true;
          }
        }
      } else {
        // No rejection blocking progress — advance over the consumed prefix and
        // clear any stuck counter (advanceBy === newLines.length on a clean run).
        if (advanceBy > 0) {
          watermarks[filename] = uploadedCount + advanceBy;
          watermarksDirty = true;
        }
        if (advanceBy === newLines.length) this.rejectionRetryCycles.delete(retryKey);
      }

      if (uploadAborted) {
        // Endpoint down/unreachable — persist progress so far and stop; the
        // remaining files would fail the same way.
        rootAborted = true;
        if (watermarksDirty) {
          try {
            await saveWatermarks(watermarkUri, watermarks);
            watermarksDirty = false;
          } catch (err) {
            const msg = err instanceof Error ? err.message : String(err);
            this.logger?.error("TelemetryUploaderService: failed to save watermarks", {
              error: msg,
            });
          }
        }
        break;
      }
    }

    // Persist final watermark state
    if (watermarksDirty) {
      try {
        await saveWatermarks(watermarkUri, watermarks);
      } catch (err) {
        const msg = err instanceof Error ? err.message : String(err);
        this.logger?.error("TelemetryUploaderService: failed to save watermarks", { error: msg });
      }
    }

    return { uploaded: totalUploaded, skipped: false, aborted: rootAborted };
  }

  /**
   * Upload the lifecycle trace stream (ADR 013 / Issue #180): every per-run
   * JSONL under `.nightgauge/pipeline/trace/` uploads verbatim through the
   * consolidated-stream driver (per-file watermark keyed by the run-id
   * filename; server-side (run_id, producer, seq) idempotency makes any
   * re-send harmless). Scans every workspace repo root (#247) — trace files
   * land under the run's target repo root just like pipeline-run history, so
   * a multi-repo workspace can have trace files outside `incrediRoot`. Bails
   * on the first failed file — an unreachable endpoint should cost one retry
   * cycle, not one per trace file (and not one per root).
   */
  private async uploadTraceStream(token: string): Promise<StreamSummary> {
    let totalUploaded = 0;

    for (const root of this.resolveHistoryScanRoots()) {
      const traceDirUri = vscode.Uri.file(path.join(root, TRACE_SUBDIR));

      let entries: [string, vscode.FileType][];
      try {
        entries = await vscode.workspace.fs.readDirectory(traceDirUri);
      } catch {
        // Trace directory may not exist yet in this root (no traced runs there).
        continue;
      }

      const jsonlFiles = entries
        .filter(([name, type]) => name.endsWith(".jsonl") && type === vscode.FileType.File)
        .map(([name]) => name);

      for (const filename of jsonlFiles) {
        const result = await this.uploadConsolidatedStream(
          token,
          {
            stream: "trace",
            filePath: path.join(TRACE_SUBDIR, filename),
            endpoint: "/v1/telemetry/pipeline-trace",
            maxBatchSize: MAX_BATCH_SIZE_TRACE,
            // Root-qualified so a run-id filename (already unique) can never
            // collide with the same run-id in another repo root's watermark
            // file — though since each root has its own watermark file this
            // is belt-and-suspenders, it keeps the key scheme consistent.
            watermarkKey: `trace/${filename}`,
          },
          root
        );
        totalUploaded += result.uploaded;
        if (result.failed) {
          return { stream: "trace", uploaded: totalUploaded, skipped: false, failed: true };
        }
      }
    }

    return { stream: "trace", uploaded: totalUploaded, skipped: false };
  }

  private async uploadConsolidatedStream(
    token: string,
    cfg: StreamConfig,
    /**
     * Repo root the stream's file lives under. Defaults to `incrediRoot` —
     * the health and recommendation streams are whole-workspace concepts
     * written only there. The trace stream (#247) passes the per-run
     * target-repo root explicitly since trace files can live in any
     * workspace repo, not just the primary one.
     */
    root: string = this.incrediRoot
  ): Promise<StreamSummary> {
    const watermarkUri = vscode.Uri.file(path.join(root, WATERMARK_SUBDIR, WATERMARK_FILENAME));
    const fileUri = vscode.Uri.file(path.join(root, cfg.filePath));
    const watermarkKey = cfg.watermarkKey ?? path.basename(cfg.filePath);

    // Size guard
    try {
      const stat = await vscode.workspace.fs.stat(fileUri);
      if (stat.size > MAX_FILE_BYTES) {
        this.logger?.warn("TelemetryUploaderService: skipping oversized file", {
          stream: cfg.stream,
          filename: watermarkKey,
          sizeBytes: stat.size,
          limitBytes: MAX_FILE_BYTES,
        });
        return { stream: cfg.stream, uploaded: 0, skipped: true };
      }
    } catch {
      // File doesn't exist yet
      return { stream: cfg.stream, uploaded: 0, skipped: true };
    }

    // Read file content
    let content: string;
    try {
      const bytes = await vscode.workspace.fs.readFile(fileUri);
      content = Buffer.from(bytes).toString("utf8");
    } catch {
      return { stream: cfg.stream, uploaded: 0, skipped: true };
    }

    const allLines = content.split("\n").filter((line) => line.trim().length > 0);
    const watermarks = await loadWatermarks(watermarkUri, this.logger);
    const uploadedCount = watermarks[watermarkKey] ?? 0;
    const newLines = allLines.slice(uploadedCount);

    if (newLines.length === 0) {
      return { stream: cfg.stream, uploaded: 0, skipped: false };
    }

    // Parse new lines — skip malformed JSON
    const allRecords: Record<string, unknown>[] = [];
    for (const line of newLines) {
      try {
        allRecords.push(JSON.parse(line) as Record<string, unknown>);
      } catch {
        this.logger?.warn("TelemetryUploaderService: skipping malformed JSONL line", {
          stream: cfg.stream,
        });
      }
    }

    if (allRecords.length === 0) {
      // All malformed — advance watermark past unreadable lines
      watermarks[watermarkKey] = allLines.length;
      try {
        await saveWatermarks(watermarkUri, watermarks);
      } catch {
        // Non-fatal
      }
      return { stream: cfg.stream, uploaded: 0, skipped: false };
    }

    // Apply optional record filter (e.g. recommendation: only metric_after populated)
    const records = cfg.filterRecord ? allRecords.filter(cfg.filterRecord) : allRecords;

    if (records.length === 0) {
      // All records filtered out — watermark not advanced; they'll be re-evaluated next cycle
      this.logger?.info("TelemetryUploaderService: all new records filtered, skipping", {
        stream: cfg.stream,
        totalNew: allRecords.length,
      });
      return { stream: cfg.stream, uploaded: 0, skipped: false };
    }

    // Upload in batches
    let batchStart = 0;
    let uploadFailed = false;
    let watermarksDirty = false;
    let totalUploaded = 0;

    while (batchStart < records.length) {
      const batch = records.slice(batchStart, batchStart + cfg.maxBatchSize);
      const batchBody: TelemetryBatch = batch;
      const url = `${this.getPlatformUrl()}${cfg.endpoint}`;
      const headers: Record<string, string> = {
        Authorization: `Bearer ${token}`,
        "Content-Type": "application/json",
      };

      let response: Response;
      try {
        response = await uploadWithRetry(url, headers, JSON.stringify(batchBody), this.logger);
      } catch (err) {
        const msg = err instanceof Error ? err.message : String(err);
        this.logger?.error("TelemetryUploaderService: batch upload failed after retries", {
          stream: cfg.stream,
          batchStart,
          batchSize: batch.length,
          error: msg,
        });
        uploadFailed = true;
        break;
      }

      if (!response.ok) {
        // 404 means the platform endpoint isn't deployed yet (see epic #3312).
        // Log at info to avoid noise until the endpoint is live.
        const logFn = response.status === 404 ? "info" : "warn";
        this.logger?.[logFn]("TelemetryUploaderService: batch upload got non-2xx", {
          stream: cfg.stream,
          status: response.status,
          batchStart,
        });
        uploadFailed = true;
        break;
      }

      // Advance watermark per successful batch
      watermarks[watermarkKey] = uploadedCount + batchStart + batch.length;
      watermarksDirty = true;
      totalUploaded += batch.length;

      this.logger?.info("TelemetryUploaderService: batch uploaded", {
        stream: cfg.stream,
        count: batch.length,
        watermark: watermarks[watermarkKey],
      });

      batchStart += batch.length;
    }

    if (watermarksDirty || uploadFailed) {
      try {
        await saveWatermarks(watermarkUri, watermarks);
      } catch (err) {
        const msg = err instanceof Error ? err.message : String(err);
        this.logger?.error("TelemetryUploaderService: failed to save watermarks", { error: msg });
      }
    }

    return { stream: cfg.stream, uploaded: totalUploaded, skipped: false, failed: uploadFailed };
  }
}
