/**
 * PlatformSseClient — shared fetch-based SSE transport with resilience.
 *
 * Handles connection, exponential backoff with jitter, Last-Event-ID persistence,
 * 401 token refresh (one attempt per connect), and 429 connection-limit backoff.
 * Both EventStreamService and ProjectEventSubscriber delegate stream I/O here.
 *
 * Not a singleton — each subscriber owns its own instance.
 *
 * @see Issue #3711 — Shared Resilient PlatformSseClient
 * @see AgentCommandStreamService — source of the canonical backoff constants
 */

import * as vscode from "vscode";
import type { Logger } from "../utils/logger";

const BACKOFF_DELAYS_MS = [1_000, 2_000, 4_000, 8_000, 16_000, 30_000] as const;
const JITTER_RATIO = 0.2;

export type SseStreamStatus = "connected" | "reconnecting" | "disconnected";

export interface PlatformSseClientOptions {
  context: vscode.ExtensionContext;
  logger: Logger;
  /** globalState key for Last-Event-ID cursor persistence */
  lastEventIdKey: string;
  /** Called for each dispatched SSE event data line (after keepalive filtering) */
  onEvent: (data: string, eventId: string | null) => void;
  /** Called on stream status transitions */
  onStatusChanged: (status: SseStreamStatus, label: string) => void;
  /**
   * Called on 401. Should force-refresh the token and return the new access
   * token, or null if refresh is not possible.
   * Called at most once per connect attempt.
   */
  onAuthRequired: () => Promise<string | null>;
  /**
   * Called when onAuthRequired returns null and there is no refresh token,
   * indicating the user needs to sign in. Optional — fires at most once
   * (after the first unrecoverable 401), then SSE disconnects.
   * Callers should invoke the platform sign-in command to start device flow.
   */
  onSignInRequired?: () => void;
}

class SseAuthError extends Error {}
class SseConnectionLimitError extends Error {}

export class PlatformSseClient implements vscode.Disposable {
  private _abortController: AbortController | null = null;
  private _reconnectTimer: ReturnType<typeof setTimeout> | null = null;
  private _connected = false;
  private _retryIndex = 0;
  private _consecutiveAuthErrors = 0;
  private _currentUrl: string | null = null;
  private _currentToken: string | null = null;
  private _disposed = false;

  constructor(private readonly _opts: PlatformSseClientOptions) {}

  connect(url: string, token: string): void {
    if (this._disposed || this._connected || this._abortController) return;

    this._currentUrl = url;
    this._currentToken = token;

    const lastEventId = this._opts.context.globalState.get<string>(this._opts.lastEventIdKey);
    const controller = new AbortController();
    this._abortController = controller;
    const { signal } = controller;

    void this._runStream(url, token, lastEventId ?? undefined, signal)
      .then(() => {
        if (signal.aborted) return;
        this._opts.logger.warn("PlatformSseClient: stream closed by server — scheduling reconnect");
        this._scheduleReconnect();
      })
      .catch((err) => {
        if (signal.aborted) return;
        if (err instanceof SseAuthError) {
          void this._handleAuthError();
        } else if (err instanceof SseConnectionLimitError) {
          this._handleConnectionLimit();
        } else {
          this._opts.logger.warn("PlatformSseClient: stream error", {
            error: err instanceof Error ? err.message : String(err),
          });
          this._scheduleReconnect();
        }
      });
  }

  reconnect(url: string, token: string): void {
    if (this._reconnectTimer !== null) {
      clearTimeout(this._reconnectTimer);
      this._reconnectTimer = null;
    }
    this._abortController?.abort();
    this._abortController = null;
    this._connected = false;
    this._retryIndex = 0;
    this.connect(url, token);
  }

  disconnect(): void {
    if (this._reconnectTimer !== null) {
      clearTimeout(this._reconnectTimer);
      this._reconnectTimer = null;
    }
    this._abortController?.abort();
    this._abortController = null;
    this._connected = false;
    this._currentUrl = null;
    this._currentToken = null;
  }

  isConnected(): boolean {
    return this._connected;
  }

  dispose(): void {
    this._disposed = true;
    this.disconnect();
  }

  private async _runStream(
    url: string,
    token: string,
    lastEventId: string | undefined,
    signal: AbortSignal
  ): Promise<void> {
    const headers: Record<string, string> = {
      Accept: "text/event-stream",
      Authorization: `Bearer ${token}`,
      "Cache-Control": "no-cache",
    };
    if (lastEventId) {
      headers["Last-Event-ID"] = lastEventId;
    }

    const response = await fetch(url, { headers, signal });

    if (response.status === 401 || response.status === 403) {
      throw new SseAuthError(`SSE auth failed: ${response.status} ${response.statusText}`);
    }

    if (response.status === 429) {
      // Check for SSE_CONNECTION_LIMIT_EXCEEDED in the body or headers
      let isConnectionLimit: boolean;
      try {
        const bodyText = await response
          .clone()
          .text()
          .catch(() => "");
        isConnectionLimit =
          bodyText.includes("SSE_CONNECTION_LIMIT_EXCEEDED") ||
          (response.headers.get("x-error-code") ?? "").includes("SSE_CONNECTION_LIMIT_EXCEEDED");
      } catch {
        // body read failed — treat as connection limit
        isConnectionLimit = true;
      }
      if (isConnectionLimit) {
        throw new SseConnectionLimitError("SSE connection limit exceeded");
      }
      throw new Error(`SSE connect failed: ${response.status} ${response.statusText}`);
    }

    if (!response.ok || !response.body) {
      throw new Error(`SSE connect failed: ${response.status} ${response.statusText}`);
    }

    const contentType = response.headers.get("content-type") ?? "";
    if (!contentType.includes("text/event-stream")) {
      throw new Error(`SSE connect failed: unexpected content-type ${contentType}`);
    }

    this._connected = true;
    this._retryIndex = 0;
    this._consecutiveAuthErrors = 0;
    this._opts.logger.info("PlatformSseClient: stream connected", { url });
    this._opts.onStatusChanged("connected", "● live");

    const reader = response.body.getReader();
    const decoder = new TextDecoder();
    let buffer = "";

    try {
      while (!signal.aborted) {
        const { done, value } = await reader.read();
        if (done) break;

        buffer += decoder.decode(value, { stream: true });
        const lines = buffer.split("\n");
        buffer = lines.pop() ?? "";

        let eventId: string | null = null;
        let eventData: string | null = null;

        for (const line of lines) {
          if (line.startsWith("id:")) {
            eventId = line.slice(3).trim();
          } else if (line.startsWith("data:")) {
            eventData = (eventData !== null ? eventData + "\n" : "") + line.slice(5).trim();
          } else if (line === "" && eventData !== null) {
            if (eventId) {
              await this._opts.context.globalState.update(this._opts.lastEventIdKey, eventId);
            }
            this._opts.onEvent(eventData, eventId);
            eventId = null;
            eventData = null;
          }
        }
      }
    } finally {
      reader.releaseLock();
      this._connected = false;
    }
  }

  private async _handleAuthError(): Promise<void> {
    this._consecutiveAuthErrors++;
    this._abortController = null;

    if (this._consecutiveAuthErrors >= 2) {
      this._opts.logger.warn(
        "PlatformSseClient: two consecutive 401s — disconnecting (no further retry)"
      );
      this._opts.onStatusChanged("disconnected", "✕ auth error");
      this._connected = false;
      return;
    }

    this._opts.logger.warn("PlatformSseClient: 401 received — requesting token refresh");

    const newToken = await this._opts.onAuthRequired();

    if (!newToken) {
      this._opts.logger.warn("PlatformSseClient: token refresh returned null — disconnecting");
      this._opts.onStatusChanged("disconnected", "✕ auth error");
      this._connected = false;
      // No refresh token available — prompt the user to sign in (#3723)
      this._opts.onSignInRequired?.();
      return;
    }

    if (this._disposed || !this._currentUrl) return;

    this._currentToken = newToken;
    this.connect(this._currentUrl, newToken);
  }

  private _handleConnectionLimit(): void {
    this._connected = false;
    this._abortController = null;
    this._opts.logger.warn("PlatformSseClient: SSE connection limit exceeded — backing off");
    this._opts.onStatusChanged("reconnecting", "↻ connection limit — backing off");
    this._scheduleReconnect();
  }

  private _scheduleReconnect(): void {
    this._connected = false;

    const idx = Math.min(this._retryIndex, BACKOFF_DELAYS_MS.length - 1);
    const base = BACKOFF_DELAYS_MS[idx];
    const jitter = base * JITTER_RATIO * (Math.random() * 2 - 1);
    const delay = Math.max(0, Math.round(base + jitter));

    this._retryIndex++;
    this._opts.logger.warn(
      `PlatformSseClient: scheduling reconnect in ${delay}ms (attempt ${this._retryIndex})`
    );
    this._opts.onStatusChanged("reconnecting", "↻ reconnecting");

    this._reconnectTimer = setTimeout(() => {
      this._reconnectTimer = null;
      this._abortController = null;
      if (!this._disposed && this._currentUrl && this._currentToken) {
        this.connect(this._currentUrl, this._currentToken);
      }
    }, delay);
  }
}
