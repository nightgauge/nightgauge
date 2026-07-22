/**
 * ProjectEventSubscriber — fetch-based SSE client for project board event streaming.
 *
 * Delegates all stream I/O (connection, backoff, 401 refresh, Last-Event-ID) to
 * PlatformSseClient. This class owns event filtering, debounced rescan dispatch,
 * and disconnected-duration tracking.
 *
 * @see Issue #3025 — Event-Driven Dispatch Phase 2
 * @see Issue #3711 — Shared Resilient PlatformSseClient
 * @see docs/AUTONOMOUS_ORCHESTRATOR.md
 */

import * as vscode from "vscode";
import { IpcClient } from "./IpcClient";
import type { Logger } from "../utils/logger";
import { PlatformSseClient } from "./PlatformSseClient";
import type { SseStreamStatus } from "./PlatformSseClient";

const LAST_EVENT_ID_KEY = "nightgauge.projectStream.lastEventId";
const SSE_WIDENED_INTERVAL_MS = 5 * 60_000; // 5 min — used by autonomousCommands
const DISCONNECT_REVERT_THRESHOLD_MS = 2 * 60_000; // revert after 2 min disconnected

export { SSE_WIDENED_INTERVAL_MS, DISCONNECT_REVERT_THRESHOLD_MS };

interface ProjectEventPayload {
  repo?: string;
  type?: string;
  fromStatus?: string;
  toStatus?: string;
  issueNumber?: number;
  projectNumber?: number;
  action?: string;
  merged?: boolean;
  assignee?: { login?: string; email?: string };
  requested_reviewer?: { login?: string; email?: string };
  issue?: { number?: number; html_url?: string };
  pull_request?: { number?: number; html_url?: string };
}

export interface GitHubUserEvent {
  type: "issue.assigned" | "pull_request.review_requested";
  issueNumber: number;
  repo: string;
  targetLogin: string;
  targetEmail?: string;
  url?: string;
}

export interface ProjectEventSubscriberOptions {
  context: vscode.ExtensionContext;
  logger: Logger;
  enabledRepos?: string[];
  onAuthRequired?: () => Promise<string | null>;
  /** Called directly (short-debounced) when a status-change event arrives with toStatus in payload. */
  onStatusChanged?: (repoSlug: string, statuses: string[]) => void;
  /** Called when SSE gets a 401 and token refresh fails — triggers sign-in flow (#3723). */
  onSignInRequired?: () => void;
  /** Called when a GitHub event targets the authenticated user (issue.assigned, pr.review_requested). */
  onUserEvent?: (event: GitHubUserEvent) => void;
}

export class ProjectEventSubscriber {
  private static instance: ProjectEventSubscriber | null = null;

  private readonly logger: Logger;
  private readonly enabledRepos: string[];
  private readonly _onStatusChangedCb: ((repoSlug: string, statuses: string[]) => void) | undefined;
  private readonly _onUserEventCb: ((event: GitHubUserEvent) => void) | undefined;

  private readonly _onSseStatusChanged = new vscode.EventEmitter<{
    status: SseStreamStatus;
    label: string;
  }>();
  readonly onSseStatusChanged = this._onSseStatusChanged.event;

  private readonly _sseClient: PlatformSseClient;
  private _disconnectedAt: number | null = null;
  private _rescanDebounceTimer: ReturnType<typeof setTimeout> | null = null;
  /** Per-repo status-change debounce timers (500ms window for coalescing bursts). */
  private readonly _statusDebounceMap = new Map<
    string,
    { timer: ReturnType<typeof setTimeout>; statuses: Set<string> }
  >();

  private constructor(opts: ProjectEventSubscriberOptions) {
    this.logger = opts.logger;
    this.enabledRepos = opts.enabledRepos ?? [];
    this._onStatusChangedCb = opts.onStatusChanged;
    this._onUserEventCb = opts.onUserEvent;

    this._sseClient = new PlatformSseClient({
      context: opts.context,
      logger: opts.logger,
      lastEventIdKey: LAST_EVENT_ID_KEY,
      onEvent: (data) => this._handleEventData(data),
      onStatusChanged: (status, label) => {
        if (status === "disconnected" || status === "reconnecting") {
          this._disconnectedAt = Date.now();
        } else if (status === "connected") {
          this._disconnectedAt = null;
        }
        this._onSseStatusChanged.fire({ status, label });
      },
      onAuthRequired: opts.onAuthRequired ?? (() => Promise.resolve(null)),
      onSignInRequired: opts.onSignInRequired,
    });
  }

  static getInstance(opts?: ProjectEventSubscriberOptions): ProjectEventSubscriber {
    if (!ProjectEventSubscriber.instance) {
      if (!opts) throw new Error("ProjectEventSubscriber requires options on first call");
      ProjectEventSubscriber.instance = new ProjectEventSubscriber(opts);
    }
    return ProjectEventSubscriber.instance;
  }

  /**
   * Non-throwing accessor for the existing singleton (#3925). Returns `null`
   * when the subscriber has not been initialized yet — which happens whenever
   * `autonomous.event_stream_enabled` is false (the session handler that
   * constructs it with options never runs) or on a re-auth where the two
   * session handlers race. Read-only call sites that only want to act on an
   * already-running subscriber MUST use this instead of the bare
   * `getInstance()`, whose no-arg form throws and was being swallowed as a
   * noisy `requires options on first call` WARN.
   */
  static getInstanceOrNull(): ProjectEventSubscriber | null {
    return ProjectEventSubscriber.instance;
  }

  static resetInstance(): void {
    ProjectEventSubscriber.instance?.disconnect();
    ProjectEventSubscriber.instance = null;
  }

  isConnected(): boolean {
    return this._sseClient.isConnected();
  }

  getDisconnectedDurationMs(): number {
    if (this._sseClient.isConnected() || this._disconnectedAt === null) return 0;
    return Date.now() - this._disconnectedAt;
  }

  connect(platformBaseUrl: string, token: string): void {
    this._sseClient.connect(`${platformBaseUrl}/v1/events/project/stream`, token);
  }

  disconnect(): void {
    this._sseClient.disconnect();
    this._onSseStatusChanged.dispose();
    this._disconnectedAt = this._disconnectedAt ?? Date.now();
    for (const entry of this._statusDebounceMap.values()) {
      clearTimeout(entry.timer);
    }
    this._statusDebounceMap.clear();
  }

  private _handleEventData(data: string): void {
    if (data.startsWith(":")) return; // keepalive comment

    let payload: ProjectEventPayload;
    try {
      payload = JSON.parse(data) as ProjectEventPayload;
    } catch {
      return;
    }

    // Filter by enabled repos if configured
    if (this.enabledRepos.length > 0 && payload.repo) {
      const repoName = payload.repo.includes("/") ? payload.repo.split("/")[1] : payload.repo;
      const matches =
        this.enabledRepos.includes(payload.repo) || this.enabledRepos.includes(repoName ?? "");
      if (!matches) return;
    }

    // USER EVENT FAST PATH: route issue.assigned / pull_request.review_requested before status logic
    if (this._onUserEventCb) {
      const userEvent = ProjectEventSubscriber._parseUserEvent(payload);
      if (userEvent) {
        this._onUserEventCb(userEvent);
        // Do NOT return — also allow status invalidation path to run if applicable
      }
    }

    const isStatusChange =
      payload.type === "project.statusChanged" ||
      (payload.type === "projects_v2_item.edited" && payload.toStatus != null);

    if (isStatusChange && payload.toStatus && this._onStatusChangedCb) {
      // FAST PATH: direct cache invalidation with 500ms debounce per repo
      this._scheduleStatusInvalidation(payload.repo ?? "", payload.fromStatus, payload.toStatus);
      return;
    }

    // FAST PATH: issue/PR lifecycle events route to status invalidation
    if (payload.type && this._onStatusChangedCb) {
      const statuses = ProjectEventSubscriber._issueEventToStatuses(
        payload.type,
        payload.action,
        payload.merged
      );
      if (statuses !== null) {
        for (const status of statuses) {
          this._scheduleStatusInvalidation(payload.repo ?? "", undefined, status);
        }
        return;
      }
    }

    // SLOW PATH: full autonomousRescan (for non-status, non-issue events)
    this._scheduleAutonomousRescan(payload);
  }

  private static _issueEventToStatuses(
    type: string,
    action: string | undefined,
    merged: boolean | undefined
  ): string[] | null {
    const t = type.toLowerCase();
    const a = (action ?? "").toLowerCase();

    if (t === "issues.closed" || (t === "issues" && a === "closed")) {
      return ["ready", "done"];
    }
    if (t === "issues.reopened" || (t === "issues" && a === "reopened")) {
      return ["done", "ready"];
    }
    if (
      t === "issues.labeled" ||
      t === "issues.unlabeled" ||
      (t === "issues" && (a === "labeled" || a === "unlabeled"))
    ) {
      return ["ready"];
    }
    if (t === "pull_request.closed" || (t === "pull_request" && a === "closed")) {
      return merged ? ["in-review", "done"] : ["in-review", "ready"];
    }
    return null;
  }

  private _scheduleStatusInvalidation(
    repo: string,
    fromStatus: string | undefined,
    toStatus: string
  ): void {
    const repoSlug = repo.includes("/") ? (repo.split("/")[1] ?? repo) : repo;
    const key = repoSlug;

    let entry = this._statusDebounceMap.get(key);
    if (entry) {
      clearTimeout(entry.timer);
      entry.statuses.add(toStatus.toLowerCase());
      if (fromStatus) entry.statuses.add(fromStatus.toLowerCase());
    } else {
      entry = {
        timer: null as unknown as ReturnType<typeof setTimeout>,
        statuses: new Set([
          toStatus.toLowerCase(),
          ...(fromStatus ? [fromStatus.toLowerCase()] : []),
        ]),
      };
      this._statusDebounceMap.set(key, entry);
    }

    entry.timer = setTimeout(() => {
      const statuses = [...(this._statusDebounceMap.get(key)?.statuses ?? [])];
      this._statusDebounceMap.delete(key);
      this.logger.info("ProjectEventSubscriber: direct status invalidation", {
        repoSlug,
        statuses,
      });
      this._onStatusChangedCb!(repoSlug, statuses);
    }, 500);
  }

  static _parseUserEvent(payload: ProjectEventPayload): GitHubUserEvent | null {
    const t = (payload.type ?? "").toLowerCase();

    if (
      t === "issues.assigned" ||
      (t === "issues" && payload.action?.toLowerCase() === "assigned")
    ) {
      const login = payload.assignee?.login;
      if (!login) return null;
      return {
        type: "issue.assigned",
        issueNumber: payload.issue?.number ?? payload.issueNumber ?? 0,
        repo: payload.repo ?? "",
        targetLogin: login,
        targetEmail: payload.assignee?.email,
        url: payload.issue?.html_url,
      };
    }

    if (
      t === "pull_request.review_requested" ||
      (t === "pull_request" && payload.action?.toLowerCase() === "review_requested")
    ) {
      const login = payload.requested_reviewer?.login;
      if (!login) return null;
      return {
        type: "pull_request.review_requested",
        issueNumber: payload.pull_request?.number ?? payload.issueNumber ?? 0,
        repo: payload.repo ?? "",
        targetLogin: login,
        targetEmail: payload.requested_reviewer?.email,
        url: payload.pull_request?.html_url,
      };
    }

    return null;
  }

  private _scheduleAutonomousRescan(payload: ProjectEventPayload): void {
    this.logger.info("ProjectEventSubscriber: event received, debouncing rescan", {
      type: payload.type,
      repo: payload.repo,
    });

    if (this._rescanDebounceTimer !== null) {
      clearTimeout(this._rescanDebounceTimer);
    }
    this._rescanDebounceTimer = setTimeout(() => {
      this._rescanDebounceTimer = null;
      this.logger.info("ProjectEventSubscriber: triggering debounced rescan");
      IpcClient.getInstance()
        .autonomousRescan()
        .catch((err) => {
          this.logger.warn("ProjectEventSubscriber: autonomousRescan failed", {
            error: err instanceof Error ? err.message : String(err),
          });
        });
    }, 5_000);
  }
}
