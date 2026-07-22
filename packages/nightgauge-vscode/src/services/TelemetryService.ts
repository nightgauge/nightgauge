/**
 * TelemetryService — queues and submits pipeline telemetry via Go IPC.
 *
 * Consent UX (modal prompt, settings panel, VSCode-config writes) lives in
 * {@link TelemetryConsentService}. This service is responsible only for:
 * - Honoring the consent gate (`consentService.isEnabled()`).
 * - Per-stream gating via `consentService.isStreamEnabled(stream)` BEFORE
 *   queueing (ADR-005).
 * - Redaction-and-submit loop in {@link flushQueue} (#3326).
 * - Recording `lastUploadAt` after a successful flush.
 *
 * @see Issue #3327 — consent split / per-stream gating / lastUploadAt
 * @see Issue #3326 — redaction gate
 * @see Issue #1480 — initial submission path
 */

import * as vscode from "vscode";
import type { IpcClient } from "./IpcClient.js";
import type { AnalyticsEvent } from "../platform/types.js";
import type { ConfigBridge } from "./ConfigBridge.js";
import type { Logger } from "../utils/logger.js";
import { RedactionService } from "./RedactionService.js";
import { TelemetryConsentService } from "./TelemetryConsentService.js";
import type { TelemetryStream } from "./telemetry/types.js";
import {
  buildPipelineExecutionEvent,
  type PipelineExecutionInput,
} from "../utils/telemetryEventBuilder.js";

/**
 * A queued telemetry event awaiting submission.
 */
export interface TelemetryQueueEntry {
  event: AnalyticsEvent;
  stream: TelemetryStream;
  enqueuedAt: Date;
}

export class TelemetryService {
  // Singleton state for data submission
  private static submissionInstance: TelemetryService | null = null;

  // Instance state — primarily for tests; bootstrap drives the singleton.
  private readonly logger: Logger | null;

  // Submission state
  private readonly queue: TelemetryQueueEntry[] = [];
  private readonly maxQueueSize = 100;
  private readonly ipcClient: IpcClient | null;
  // Retained for API compatibility with consumers that still pass a bridge —
  // the consent gate is now the only source of truth for `isTelemetryEnabled`.
  private readonly configBridge: ConfigBridge;
  private readonly consentService: TelemetryConsentService | null;

  /**
   * Redactor applied to every event before IPC submission. Defense-in-depth
   * gate for #3326 — `flushQueue()` is the single call site that reaches the
   * platform analytics API, and `redactor.redact()` is invoked there
   * unconditionally with no per-event opt-out.
   */
  private readonly redactor: RedactionService;

  constructor(
    configBridge: ConfigBridge,
    consentService: TelemetryConsentService | null,
    logger?: Logger | null,
    ipcClient?: IpcClient | null,
    redactor?: RedactionService
  ) {
    this.configBridge = configBridge;
    this.consentService = consentService ?? null;
    this.logger = logger ?? null;
    this.ipcClient = ipcClient ?? null;
    this.redactor = redactor ?? new RedactionService();
  }

  /**
   * Initialize the singleton for data submission. Returns null when
   * `ipcClient` is null (platform disabled).
   */
  static initialize(
    ipcClient: IpcClient | null,
    configBridge: ConfigBridge,
    consentService: TelemetryConsentService | null,
    logger?: Logger | null
  ): TelemetryService | null {
    if (!ipcClient) {
      return null;
    }
    if (!TelemetryService.submissionInstance) {
      TelemetryService.submissionInstance = new TelemetryService(
        configBridge,
        consentService,
        logger,
        ipcClient
      );
    }
    return TelemetryService.submissionInstance;
  }

  static getInstance(): TelemetryService | null {
    return TelemetryService.submissionInstance;
  }

  static resetInstance(): void {
    TelemetryService.submissionInstance?.dispose();
    TelemetryService.submissionInstance = null;
  }

  /** Returns true if telemetry should be sent at all. */
  isTelemetryEnabled(): boolean {
    if (!vscode.env.isTelemetryEnabled) return false;
    return this.consentService?.isEnabled() ?? false;
  }

  /** Returns true if the stream is enabled (and master is enabled). */
  isStreamEnabled(stream: TelemetryStream): boolean {
    if (!this.isTelemetryEnabled()) return false;
    return this.consentService?.isStreamEnabled(stream) ?? false;
  }

  /**
   * Record a pipeline execution event. Per-stream gate runs BEFORE queue
   * insertion so disabled-stream events are dropped immediately (ADR-005).
   */
  async recordPipelineExecution(data: PipelineExecutionInput): Promise<void> {
    if (!this.ipcClient) return;
    if (!this.isStreamEnabled("pipeline-run")) return;

    const event = buildPipelineExecutionEvent(data);
    this.enqueue(event, "pipeline-run");
    await this.flushQueue();
  }

  /**
   * Flush all queued events to the platform via IPC. Each event is redacted
   * exactly once before `platformSubmitAnalytics`. After at least one event
   * is flushed, the consent service records `Date.now()` as the last upload.
   *
   * @see Issue #3326
   */
  async flushQueue(): Promise<void> {
    if (this.queue.length === 0 || !this.ipcClient) {
      return;
    }

    const entries = this.queue.splice(0);
    let recordsRedacted = 0;
    let totalFieldsRemoved = 0;
    let submitted = 0;

    for (const entry of entries) {
      const { event, fieldsRemoved, fieldsRedacted } = this.redactor.redact(entry.event);
      if (fieldsRemoved > 0 || fieldsRedacted > 0) {
        recordsRedacted++;
        totalFieldsRemoved += fieldsRemoved;
      }
      try {
        await this.ipcClient.platformSubmitAnalytics(
          event.eventType,
          event.payload,
          event.timestamp
        );
        submitted++;
      } catch (err) {
        this.logger?.warn("Analytics submission failed (fire-and-forget)", {
          event: event.eventType,
          error: String(err),
        });
      }
    }

    if (recordsRedacted > 0) {
      this.logger?.info("Telemetry redaction summary", {
        records_redacted: recordsRedacted,
        fields_removed: totalFieldsRemoved,
        records_in_flush: entries.length,
      });
    }

    if (submitted > 0 && this.consentService) {
      void this.consentService.recordUploadAt(Date.now());
    }
  }

  /** Returns the current queue size. */
  getQueueSize(): number {
    return this.queue.length;
  }

  dispose(): void {
    this.queue.length = 0;
  }

  /** Add an event to the FIFO queue. Drops oldest when at capacity. */
  private enqueue(event: AnalyticsEvent, stream: TelemetryStream = "pipeline-run"): void {
    if (this.queue.length >= this.maxQueueSize) {
      this.queue.shift();
      console.warn("[TelemetryService] Queue at capacity — dropped oldest event");
    }
    this.queue.push({ event, stream, enqueuedAt: new Date() });
  }
}
