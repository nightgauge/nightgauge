/**
 * PlatformCredentialBridge — keeps the Go daemon's platform credential equal to
 * the extension's current session.
 *
 * The extension signs a user in and keeps the access token in SecretStorage,
 * but every platform-backed dashboard tab except Audit fetches its data through
 * the Go `serve` process — which was spawned with a *license key* in its
 * environment and nothing else. A license key identifies an account, not a
 * user, so the user-scoped routes (`/v1/analytics/health`,
 * `/v1/analytics/trends`, `/v1/analytics/cost`, `/v1/audit/reports`) answered
 * 401 for a signed-in user and the Health, Trends, Cost and Compliance tabs
 * failed by construction (#742).
 *
 * The fix is a push, not another spawn-time environment variable. Access tokens
 * expire and {@link TokenRefreshManager} rotates them for the life of the
 * session; a credential frozen at spawn works for exactly one token lifetime
 * and then regresses silently, which is the failure mode this class exists to
 * prevent. So the bridge follows the token itself:
 *
 * - **sign-in / refresh** — TokenStorage fires `accessToken`/`stored`, and the
 *   fresh token is pushed to the daemon;
 * - **sign-out** — TokenStorage fires `cleared` (or `accessToken`/`deleted`),
 *   and an empty token is pushed, which drops the daemon back to its
 *   license-key fallback exactly as a never-signed-in headless run;
 * - **daemon (re)start** — the owner calls {@link sync}, because a restarted
 *   process has forgotten everything it was told.
 *
 * Every trigger means the same thing — "the daemon's credential may be stale,
 * reconcile it against storage" — so they collapse into a single dirty flag
 * driven by one pump with at most one push in flight. That matters because the
 * Go server dispatches each IPC request in its own goroutine: two overlapping
 * pushes could be applied in either order, and the loser would leave the daemon
 * holding a revoked token. One in flight, always re-read from storage, means
 * the last write is always the current credential.
 *
 * @see Issue #742 - The Go backend never receives the signed-in session token
 */

import type * as vscode from "vscode";
import type { ITokenStorage } from "./TokenStorage";

/**
 * The narrow slice of the IPC client this bridge needs: one call that hands the
 * daemon a credential. An empty string clears it.
 */
export interface SessionTokenTransport {
  setSessionToken(token: string): Promise<void>;
}

export class PlatformCredentialBridge implements vscode.Disposable {
  private _disposed = false;
  private readonly _subscription: vscode.Disposable;
  /** Set by every trigger; cleared as the pump picks the work up. */
  private _stale = false;
  /** The running pump, so {@link sync} can await the reconciliation it asked for. */
  private _pump: Promise<void> = Promise.resolve();
  private _pumping = false;

  constructor(
    private readonly transport: SessionTokenTransport,
    private readonly tokenStorage: ITokenStorage,
    private readonly log: (message: string) => void = () => {}
  ) {
    this._subscription = tokenStorage.onTokenChanged((evt) => {
      // 'cleared' is the bulk sign-out; otherwise only the access token matters.
      if (evt.action !== "cleared" && evt.key !== "accessToken") return;
      this._markStale();
    });
  }

  /**
   * Reconcile the daemon's credential with the extension's current session and
   * wait for it to land. Call this after the daemon starts or restarts — its
   * in-memory credential does not survive the process.
   */
  async sync(): Promise<void> {
    this._markStale();
    await this._pump;
  }

  dispose(): void {
    this._disposed = true;
    this._subscription.dispose();
  }

  // ---------------------------------------------------------------------------
  // Private
  // ---------------------------------------------------------------------------

  private _markStale(): void {
    if (this._disposed) return;
    this._stale = true;
    if (this._pumping) return; // The running pump will pick this up.
    this._pumping = true;
    this._pump = this._drain().finally(() => {
      this._pumping = false;
    });
  }

  private async _drain(): Promise<void> {
    while (this._stale && !this._disposed) {
      this._stale = false;
      let token: string | null;
      try {
        token = await this.tokenStorage.retrieve("accessToken");
      } catch (err) {
        this.log(`[PlatformCredentialBridge] Could not read the access token: ${errText(err)}`);
        return;
      }
      await this._push(token ?? "");
    }
  }

  private async _push(token: string): Promise<void> {
    if (this._disposed) return;
    try {
      await this.transport.setSessionToken(token);
      this.log(
        token
          ? "[PlatformCredentialBridge] Session token pushed to the Go backend"
          : "[PlatformCredentialBridge] Session token cleared in the Go backend"
      );
    } catch (err) {
      // A community build has no platform client at all and answers "platform
      // client not configured"; a daemon that just died answers nothing. Neither
      // is worth surfacing to the user — the next start() re-syncs.
      this.log(`[PlatformCredentialBridge] Credential push failed: ${errText(err)}`);
    }
  }
}

function errText(err: unknown): string {
  return err instanceof Error ? err.message : String(err);
}
