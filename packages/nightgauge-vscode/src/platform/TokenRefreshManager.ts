/**
 * TokenRefreshManager — Centralized, resilient OAuth token refresh.
 *
 * This is the SINGLE owner of refresh-token spending in the extension. Refresh
 * tokens are single-use and rotated by the platform (each successful refresh
 * revokes the old token), so two concurrent requests that both read the same
 * stored refresh token will race: the first rotates it, the second sends a
 * now-revoked token and gets a spurious 401. Every refresh — proactive
 * (scheduled before expiry) and on-demand (SSE / heartbeat / registration 401
 * recovery) — therefore funnels through {@link forceRefresh}/{@link _doRefresh}
 * and the shared {@link _sharedRefresh} in-flight guard so a token is never
 * spent twice concurrently.
 *
 * Failure handling distinguishes two cases (#3751):
 *   - **Auth-fatal** (platform returned 401/403 — the refresh token is genuinely
 *     invalid or expired): sign out once and prompt re-authentication.
 *   - **Transient** (network blip, platform 5xx, Go backend restart, host swap
 *     mid-flight): retry with exponential backoff and NEVER sign out. A valid
 *     30-day refresh token must survive an arbitrarily long platform outage or
 *     a laptop wake-from-sleep where the network isn't up yet.
 *
 * @see Issue #1466 - Implement token refresh lifecycle and automatic renewal
 * @see Issue #3751 - Resilient refresh: classify failures, dedup, stop spurious sign-outs
 */

import * as vscode from "vscode";
import type { Logger } from "../utils/logger";
import type { ITokenStorage } from "./TokenStorage";
import type { IpcClient } from "../services/IpcClient";
import type { OfflineManager } from "./OfflineManager";
import type { ConfigBridge } from "../services/ConfigBridge";

/**
 * Narrow contract for on-demand token refresh, deduplicated across all callers.
 * Implemented by {@link TokenRefreshManager}; consumed by heartbeat and agent
 * registration so their 401 recovery shares the same single-use-token guard
 * instead of calling the refresh endpoint directly (#3751).
 */
export interface IOnDemandTokenRefresher {
  /** Refresh the access token. Returns the new token, or null on any failure. */
  forceRefresh(): Promise<string | null>;
}

/** Outcome of a single refresh attempt. Drives caller retry / sign-out policy. */
type RefreshResult =
  | { ok: true; accessToken: string }
  | { ok: false; kind: "auth" | "transient" | "no-token"; error: Error };

/** Proactive refresh fires at most this long before the access token expires. */
const MAX_PROACTIVE_ADVANCE_MS = 5 * 60 * 1000;
/** First transient-retry delay; doubles each attempt up to the cap. */
const RETRY_BASE_MS = 5_000;
/** Ceiling for transient-retry backoff — retries continue at this cadence. */
const RETRY_MAX_MS = 5 * 60 * 1000;

/**
 * Classify a refresh failure. The Go IPC layer formats platform rejections as
 * "authRefresh: unexpected status <code>" (server.go) and the platform returns
 * 401 for an invalid refresh token and 403 for an expired one
 * (acme-platform routes/auth/token.ts). Everything else — network
 * errors, 5xx, "Go backend not connected/exited" — is transient and retryable.
 */
function classifyRefreshError(error: Error): "auth" | "transient" {
  const msg = error.message;
  if (/\b40[13]\b/.test(msg) || /unauthorized|forbidden/i.test(msg)) {
    return "auth";
  }
  return "transient";
}

export class TokenRefreshManager implements vscode.Disposable, IOnDemandTokenRefresher {
  private _timer: ReturnType<typeof setTimeout> | null = null;
  private _paused = false;
  private _disposed = false;
  /** Shared in-flight refresh, so concurrent callers join instead of racing the single-use token. */
  private _refreshInFlight: Promise<RefreshResult> | null = null;
  /** Consecutive transient-failure count, for exponential backoff. Reset on success / fresh cycle. */
  private _retryCount = 0;
  /** Set once terminal sign-out has run, so concurrent paths don't double-prompt. Cleared on re-login. */
  private _terminated = false;
  private readonly _disposables: vscode.Disposable[] = [];
  private _currentHostKey: string;

  private readonly _onRefreshSucceeded = new vscode.EventEmitter<void>();
  readonly onRefreshSucceeded = this._onRefreshSucceeded.event;

  private readonly _onRefreshFailed = new vscode.EventEmitter<Error>();
  readonly onRefreshFailed = this._onRefreshFailed.event;

  constructor(
    private readonly tokenStorage: ITokenStorage,
    private readonly ipcClient: IpcClient,
    private readonly offlineManager: OfflineManager,
    private readonly onSignOut: () => Promise<void>,
    private readonly logger: Logger,
    getHostKey?: () => string,
    configBridge?: ConfigBridge
  ) {
    this._currentHostKey = getHostKey?.() ?? "production";

    // Cancel scheduled/in-flight refresh on platform host change (#3723)
    if (configBridge) {
      this._disposables.push(
        configBridge.onPlatformHostChanged((evt) => {
          this._currentHostKey = evt.newHost;
          this._clearTimer();
          // Null out the in-flight promise so its result is discarded.
          // The host snapshot guard in _performRefresh provides a second line
          // of defense against token pollution if the promise resolves late.
          this._refreshInFlight = null;
          this._retryCount = 0;
          this.logger.info("[TokenRefreshManager] Host changed — refresh scheduler reset");
        })
      );
    }

    // Subscribe to offline state changes
    this._disposables.push(
      offlineManager.onStateChanged((evt) => {
        if (evt.current === "online" && this._paused) {
          this._paused = false;
          this.logger.info("[TokenRefreshManager] Platform online — resuming refresh scheduler");
          void this._scheduleNext();
        } else if (evt.current !== "online" && !this._paused) {
          this._paused = true;
          this.logger.info("[TokenRefreshManager] Platform offline — pausing refresh scheduler");
          this._clearTimer();
        }
      })
    );

    // Subscribe to token changes to reschedule when new tokens are stored
    this._disposables.push(
      tokenStorage.onTokenChanged((evt) => {
        if (evt.key === "accessToken" && evt.action === "stored") {
          // New token stored (by device flow or prior refresh) — a fresh
          // session episode begins, so re-arm the terminal guard and reschedule.
          this._terminated = false;
          this._retryCount = 0;
          void this._scheduleNext();
        }
        if (evt.action === "cleared") {
          // Sign-out — cancel any pending refresh
          this._clearTimer();
        }
      })
    );
  }

  /** Start the refresh scheduler. Call once after construction. */
  async start(): Promise<void> {
    // Check initial offline state
    if (this.offlineManager.state !== "online") {
      this._paused = true;
      this.logger.info("[TokenRefreshManager] Started in paused state (platform not online)");
      return;
    }
    await this._scheduleNext();
  }

  /**
   * On-demand token refresh for 401 recovery (SSE, heartbeat, registration).
   * Joins any in-flight refresh rather than spending the single-use token
   * again. Returns the new access token on success, or null on any failure.
   * Only an auth-fatal failure triggers sign-out; transient failures leave the
   * session intact so the caller can retry its own request.
   */
  async forceRefresh(): Promise<string | null> {
    if (this._disposed) return null;

    const result = await this._sharedRefresh();
    if (result.ok) {
      return result.accessToken;
    }
    if (result.kind === "auth") {
      await this._handleTerminalAuthFailure(result.error);
    }
    return null;
  }

  dispose(): void {
    this._disposed = true;
    this._clearTimer();
    this._onRefreshSucceeded.dispose();
    this._onRefreshFailed.dispose();
    for (const d of this._disposables) d.dispose();
  }

  // ---------------------------------------------------------------------------
  // Private
  // ---------------------------------------------------------------------------

  /** Calculate delay (ms) until refresh should fire. Returns null if no token or already expired. */
  private async _computeDelay(): Promise<number | null> {
    const expiresAtStr = await this.tokenStorage.retrieve("expiresAt");
    if (!expiresAtStr) return null;

    const expiresAt = new Date(expiresAtStr).getTime();
    const now = Date.now();
    const msUntilExpiry = expiresAt - now;

    if (msUntilExpiry <= 0) {
      // Already expired — refresh immediately
      return 0;
    }

    // Proactive: 5 min before expiry or 10% of lifetime, whichever is sooner
    const tenPercent = msUntilExpiry * 0.1;
    const advance = Math.min(MAX_PROACTIVE_ADVANCE_MS, tenPercent);

    const delay = msUntilExpiry - advance;
    return Math.max(0, delay);
  }

  private _clearTimer(): void {
    if (this._timer !== null) {
      clearTimeout(this._timer);
      this._timer = null;
    }
  }

  private async _scheduleNext(): Promise<void> {
    if (this._disposed || this._paused) return;
    this._clearTimer();
    // A fresh proactive cycle — discard any prior transient-retry backoff state.
    this._retryCount = 0;

    const delay = await this._computeDelay();
    if (delay === null) {
      this.logger.debug("[TokenRefreshManager] No expiresAt — scheduler idle");
      return;
    }

    this.logger.debug(`[TokenRefreshManager] Scheduling refresh in ${Math.round(delay / 1000)}s`);
    this._timer = setTimeout(() => {
      void this._doRefresh();
    }, delay);
  }

  /**
   * Deduplicated refresh. If a refresh is already in flight, all callers await
   * the same promise and share its result — the single-use refresh token is
   * spent exactly once (#3751).
   */
  private _sharedRefresh(): Promise<RefreshResult> {
    if (this._refreshInFlight) {
      return this._refreshInFlight;
    }
    this._refreshInFlight = this._performRefresh().finally(() => {
      this._refreshInFlight = null;
    });
    return this._refreshInFlight;
  }

  /**
   * Perform one refresh: read the refresh token, call the platform, persist the
   * rotated tokens, and fire onRefreshSucceeded. Returns a classified result;
   * callers decide retry vs sign-out. Never throws.
   */
  private async _performRefresh(): Promise<RefreshResult> {
    // Snapshot host key before any await so a host change during the call is
    // detectable and its result discarded (prevents token namespace pollution, #3723).
    const hostAtRefreshStart = this._currentHostKey;

    const refreshToken = await this.tokenStorage.retrieve("refreshToken");
    if (!refreshToken) {
      this.logger.warn("[TokenRefreshManager] No refresh token — cannot refresh");
      return { ok: false, kind: "no-token", error: new Error("no refresh token") };
    }

    try {
      const response = await this.ipcClient.platformAuthRefresh(refreshToken);

      // Host changed mid-refresh — discard result and treat as transient so the
      // scheduler retries against the new host rather than signing out (#3723).
      if (this._currentHostKey !== hostAtRefreshStart) {
        this.logger.warn("[TokenRefreshManager] Host changed during refresh — discarding result");
        return { ok: false, kind: "transient", error: new Error("host changed during refresh") };
      }

      // Store expiresAt first so _scheduleNext reads the new expiry when
      // onTokenChanged fires for accessToken (ordering matters for scheduling).
      const expiresAt = new Date(Date.now() + response.expires_in * 1000).toISOString();
      await this.tokenStorage.store("expiresAt", expiresAt);
      await this.tokenStorage.store("refreshToken", response.refresh_token);
      // Storing accessToken last — fires onTokenChanged → _scheduleNext.
      await this.tokenStorage.store("accessToken", response.access_token);

      this.logger.debug("[TokenRefreshManager] Token refreshed successfully");
      this._onRefreshSucceeded.fire();
      return { ok: true, accessToken: response.access_token };
    } catch (err) {
      const error = err instanceof Error ? err : new Error(String(err));
      const kind = classifyRefreshError(error);
      this.logger.warn(`[TokenRefreshManager] Refresh failed (${kind})`, { error });
      return { ok: false, kind, error };
    }
  }

  /** Scheduled (proactive) refresh. Retries transient failures; signs out only on auth-fatal. */
  private async _doRefresh(): Promise<void> {
    this._timer = null;
    if (this._disposed || this._paused) return;

    const result = await this._sharedRefresh();
    if (this._disposed) return;

    if (result.ok) {
      this._retryCount = 0;
      // _scheduleNext is driven by onTokenChanged (accessToken stored).
      return;
    }

    if (result.kind === "no-token") {
      // No local refresh token to spend — stay idle rather than spam sign-out.
      // SessionManager/sign-in flow owns recovery from this state.
      return;
    }

    if (result.kind === "transient") {
      // Never destroy a valid refresh token over a transient outage — back off
      // and keep retrying. Offline transitions pause/resume this independently.
      this._retryCount++;
      const delay = Math.min(RETRY_MAX_MS, RETRY_BASE_MS * 2 ** (this._retryCount - 1));
      this.logger.warn(
        `[TokenRefreshManager] Transient refresh failure — retry #${this._retryCount} in ${Math.round(
          delay / 1000
        )}s`,
        { error: result.error }
      );
      this._timer = setTimeout(() => {
        void this._doRefresh();
      }, delay);
      return;
    }

    // Auth-fatal: the refresh token is genuinely invalid/expired.
    await this._handleTerminalAuthFailure(result.error);
  }

  /**
   * Terminal handling for an auth-fatal refresh failure: fire onRefreshFailed,
   * prompt re-authentication, and sign out. Idempotent — concurrent refresh
   * paths that both observe the failure only sign out (and prompt) once.
   */
  private async _handleTerminalAuthFailure(error: Error): Promise<void> {
    if (this._terminated || this._disposed) return;
    this._terminated = true;
    this._retryCount = 0;
    this._clearTimer();

    this.logger.error("[TokenRefreshManager] Refresh rejected (auth) — signing out", { error });
    this._onRefreshFailed.fire(error);
    vscode.window.showWarningMessage("Nightgauge: Your session has expired. Please sign in again.");
    await this.onSignOut();
  }
}
