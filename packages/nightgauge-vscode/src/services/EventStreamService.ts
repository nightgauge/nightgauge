/**
 * EventStreamService — fetch-based SSE client for real-time audit + workflow events.
 *
 * Delegates all stream I/O (connection, backoff, 401 refresh, Last-Event-ID) to
 * PlatformSseClient. This class owns event parsing and status emitters.
 *
 * Workflow events are the canonical `schemaVersion-4` {@link WorkflowEvent} node
 * tree (run / phase / agent / judge) emitted by the SDK EventBus and re-served
 * over SSE. The previous hand-rolled `PipelineEvent` discriminated union + string
 * matching (#3714) is GONE: every workflow payload is validated by one
 * `parseWorkflowEvent` Zod call and forwarded VERBATIM — `nodeId` / `parentId` /
 * `seq` / `ts` intact — to the sidebar workflow tree (#3919).
 *
 * @see Issue #3321 — Wire up SSE consumer for real-time audit/pipeline events
 * @see Issue #3711 — Shared Resilient PlatformSseClient
 * @see Issue #3919 — live workflow node-tree sidebar; reverses the #3714 mirror
 * @see ProjectEventSubscriber.ts — sibling subscriber using the same transport
 */

import * as vscode from "vscode";
import type { WorkflowEvent } from "@nightgauge/sdk";
import type { Logger } from "../utils/logger";
import type { AuditLogEntry } from "../views/dashboard/DashboardState";
import type { TokenRefreshManager } from "../platform/TokenRefreshManager";
import { parseWorkflowEvent } from "../schemas/workflowEvent";
import { PlatformSseClient } from "./PlatformSseClient";
import type { SseStreamStatus } from "./PlatformSseClient";

const LAST_EVENT_ID_KEY = "nightgauge.eventStream.lastEventId";

export type StreamStatus = SseStreamStatus;

export interface StreamStatusEvent {
  status: StreamStatus;
  label: string;
}

export interface EventStreamServiceOptions {
  context: vscode.ExtensionContext;
  logger: Logger;
  tokenRefreshManager: TokenRefreshManager;
  /** Called when SSE gets a 401 and token refresh fails — triggers sign-in flow (#3723). */
  onSignInRequired?: () => void;
}

export class EventStreamService implements vscode.Disposable {
  private static instance: EventStreamService | null = null;

  private readonly _sseClient: PlatformSseClient;

  private readonly _onAuditLiveEvent = new vscode.EventEmitter<AuditLogEntry>();
  readonly onAuditLiveEvent = this._onAuditLiveEvent.event;

  private readonly _onStreamStatusChanged = new vscode.EventEmitter<StreamStatusEvent>();
  readonly onStreamStatusChanged = this._onStreamStatusChanged.event;

  /**
   * Fires for every canonical {@link WorkflowEvent} node emission received over
   * SSE — the run / phase / agent / judge tree, forwarded verbatim (with its
   * `seq`). Subscribers fold the stream into the live workflow sidebar tree.
   */
  private readonly _onWorkflowEvent = new vscode.EventEmitter<WorkflowEvent>();
  readonly onWorkflowEvent = this._onWorkflowEvent.event;

  private constructor(opts: EventStreamServiceOptions) {
    this._sseClient = new PlatformSseClient({
      context: opts.context,
      logger: opts.logger,
      lastEventIdKey: LAST_EVENT_ID_KEY,
      onEvent: (data) => this._dispatchEvent(data),
      onStatusChanged: (status, label) => this._onStreamStatusChanged.fire({ status, label }),
      onAuthRequired: () => opts.tokenRefreshManager.forceRefresh(),
      onSignInRequired: opts.onSignInRequired,
    });
  }

  static getInstance(opts?: EventStreamServiceOptions): EventStreamService {
    if (!EventStreamService.instance) {
      if (!opts) throw new Error("EventStreamService requires options on first call");
      EventStreamService.instance = new EventStreamService(opts);
    }
    return EventStreamService.instance;
  }

  /**
   * Non-throwing accessor for the existing singleton (#3925). Returns `null`
   * when the service has not been initialized yet, so read-only call sites
   * (e.g. the `reconnectEventStreams` command) can `?.`-chain instead of
   * throwing a swallowed "requires options on first call" error.
   */
  static getInstanceOrNull(): EventStreamService | null {
    return EventStreamService.instance;
  }

  static resetInstance(): void {
    EventStreamService.instance?.dispose();
    EventStreamService.instance = null;
  }

  isConnected(): boolean {
    return this._sseClient.isConnected();
  }

  connect(baseUrl: string, token: string): void {
    this._sseClient.connect(`${baseUrl}/v1/events/stream`, token);
  }

  reconnect(baseUrl: string, token: string): void {
    this._sseClient.reconnect(`${baseUrl}/v1/events/stream`, token);
  }

  disconnect(): void {
    this._sseClient.disconnect();
  }

  dispose(): void {
    this._sseClient.dispose();
    this._onAuditLiveEvent.dispose();
    this._onStreamStatusChanged.dispose();
    this._onWorkflowEvent.dispose();
  }

  private _dispatchEvent(data: string): void {
    if (data.startsWith(":")) return; // keepalive comment

    let payload: { type?: string } & Record<string, unknown>;
    try {
      payload = JSON.parse(data) as { type?: string } & Record<string, unknown>;
    } catch {
      return;
    }

    const eventType = payload.type ?? "";

    if (eventType.startsWith("audit_") || eventType === "audit") {
      const entry = parseAuditEntry(payload);
      if (entry) {
        this._onAuditLiveEvent.fire(entry);
      }
      return;
    }

    // Workflow node-tree emission — one Zod parse, no string matching. The SSE
    // envelope may nest the node under `data`; unwrap before parsing.
    const candidate =
      typeof payload["data"] === "object" && payload["data"] !== null ? payload["data"] : payload;
    const node = parseWorkflowEvent(candidate);
    if (node) {
      this._onWorkflowEvent.fire(node);
    }
  }
}

function parseAuditEntry(payload: Record<string, unknown>): AuditLogEntry | null {
  const data =
    typeof payload["data"] === "object" && payload["data"] !== null
      ? (payload["data"] as Record<string, unknown>)
      : payload;

  const id = String(data["id"] ?? data["eventId"] ?? "");
  if (!id) return null;

  return {
    id,
    timestamp: String(data["timestamp"] ?? data["createdAt"] ?? new Date().toISOString()),
    userId: String(data["userId"] ?? data["accountId"] ?? ""),
    userEmail: typeof data["userEmail"] === "string" ? data["userEmail"] : undefined,
    action: String(data["action"] ?? ""),
    resourceType: typeof data["resourceType"] === "string" ? data["resourceType"] : undefined,
    resourceId: typeof data["resourceId"] === "string" ? data["resourceId"] : undefined,
    status: parseStatus(data["status"]),
    metadata:
      typeof data["metadata"] === "object" && data["metadata"] !== null
        ? (data["metadata"] as Record<string, unknown>)
        : undefined,
    costUsd: typeof data["costUsd"] === "number" ? data["costUsd"] : undefined,
  };
}

function parseStatus(v: unknown): "success" | "failure" | "pending" {
  if (v === "success" || v === "failure" || v === "pending") return v;
  return "success";
}
