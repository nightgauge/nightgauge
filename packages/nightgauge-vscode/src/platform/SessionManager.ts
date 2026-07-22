/**
 * SessionManager — Centralized auth session state machine.
 *
 * Owns the canonical auth state machine:
 *   unauthenticated → authenticating → authenticated → error
 *
 * Subscribes to events from OAuthDeviceFlowService, GitHubAuthService,
 * TokenRefreshManager, and TokenStorage to derive and expose the current
 * session state. Emits `onSessionChanged` on every state transition.
 *
 * Session restoration on activation: call `restore()` after construction
 * to read persisted tokens from TokenStorage and set the initial state.
 *
 * @see Issue #1468 - Build Session State Manager with Auth State Machine
 * @see Issue #1452 - Epic: Platform API integration
 */

import * as vscode from "vscode";
import type { Logger } from "../utils/logger";
import type { ITokenStorage } from "./TokenStorage";
import type { TokenRefreshManager } from "./TokenRefreshManager";
import type { OAuthDeviceFlowService } from "../services/OAuthDeviceFlowService";
import type { GitHubAuthService } from "../services/GitHubAuthService";
import type { TeamRole } from "./types";
import type { ConfigBridge } from "../services/ConfigBridge";

/** The canonical auth state machine states. */
export type SessionState =
  | "unauthenticated" // No tokens, not attempting auth
  | "authenticating" // Device flow or GitHub sign-in in progress
  | "authenticated" // Valid access token present
  | "error"; // Auth attempt failed (token refresh failure, etc.)

/** Session data snapshot — null fields mean the value is unknown/unavailable. */
export interface SessionData {
  accessToken: string | null;
  expiresAt: string | null; // ISO 8601 string from TokenStorage
  userEmail: string | null; // User email from platform profile, if stored
  userTier: string | null; // Subscription tier, e.g. 'community' | 'pro' | 'team' | 'enterprise'
  userRole: TeamRole | null; // Team role, null for non-team or unauthenticated users
}

/** Payload emitted by onSessionChanged. */
export interface SessionStateEvent {
  previous: SessionState;
  current: SessionState;
  data: SessionData;
  reason: string;
}

export class SessionManager implements vscode.Disposable {
  private _state: SessionState = "unauthenticated";
  private _disposed = false;
  private readonly _disposables: vscode.Disposable[] = [];

  private readonly _onSessionChanged = new vscode.EventEmitter<SessionStateEvent>();
  readonly onSessionChanged = this._onSessionChanged.event;

  constructor(
    private readonly tokenStorage: ITokenStorage,
    private readonly tokenRefreshManager: TokenRefreshManager,
    private readonly oauthDeviceFlowService: OAuthDeviceFlowService,
    private readonly gitHubAuthService: GitHubAuthService,
    private readonly logger: Logger,
    configBridge?: ConfigBridge
  ) {
    // Transition to unauthenticated on platform host change (#3723)
    if (configBridge) {
      this._disposables.push(
        configBridge.onPlatformHostChanged(() => {
          this._transition("unauthenticated", "platform host changed");
        })
      );
    }

    // Subscribe to sign-in/sign-out events from both auth methods
    this._disposables.push(
      oauthDeviceFlowService.onSignedIn(() =>
        this._transition("authenticated", "device-flow sign-in succeeded")
      ),
      oauthDeviceFlowService.onSignedOut(() =>
        this._transition("unauthenticated", "device-flow sign-out")
      ),
      gitHubAuthService.onSignedIn(() =>
        this._transition("authenticated", "github sign-in succeeded")
      ),
      gitHubAuthService.onSignedOut(() => this._transition("unauthenticated", "github sign-out")),
      // Refresh success means session is still authenticated (fire event for data update)
      tokenRefreshManager.onRefreshSucceeded(() =>
        this._transition("authenticated", "token refresh succeeded")
      ),
      // Refresh failure means session is lost
      tokenRefreshManager.onRefreshFailed(() => this._transition("error", "token refresh failed")),
      // Token cleared externally (e.g., user manually signed out) → unauthenticated
      tokenStorage.onTokenChanged((evt) => {
        if (evt.action === "cleared") {
          this._transition("unauthenticated", "tokens cleared");
        }
      })
    );
  }

  get state(): SessionState {
    return this._state;
  }

  /** Returns true if the user has a valid session (access token present). */
  async isAuthenticated(): Promise<boolean> {
    const token = await this.tokenStorage.retrieve("accessToken");
    return token !== null;
  }

  /**
   * The current device-flow access token (JWT), or null when not signed in.
   * Used by flows that must present the user's JWT to a JWT-only platform
   * endpoint (e.g. Start Free Trial). Callers should refresh-and-retry on a 401
   * since the stored token may have expired.
   */
  async getAccessToken(): Promise<string | null> {
    return this.tokenStorage.retrieve("accessToken");
  }

  /**
   * Restore session state on extension activation.
   * Reads TokenStorage to determine if persisted tokens exist.
   * Call once from bootstrap after all services are wired.
   */
  async restore(): Promise<void> {
    if (this._disposed) return;
    const accessToken = await this.tokenStorage.retrieve("accessToken");
    if (accessToken !== null) {
      this._transition("authenticated", "session restored from storage");
    } else {
      this.logger.debug("[SessionManager] No persisted tokens — starting unauthenticated");
    }
  }

  dispose(): void {
    this._disposed = true;
    this._onSessionChanged.dispose();
    for (const d of this._disposables) d.dispose();
  }

  // ---------------------------------------------------------------------------
  // Private
  // ---------------------------------------------------------------------------

  private async _readSessionData(): Promise<SessionData> {
    const [accessToken, expiresAt, userEmail, userTier, rawRole] = await Promise.all([
      this.tokenStorage.retrieve("accessToken"),
      this.tokenStorage.retrieve("expiresAt"),
      this.tokenStorage.retrieve("userEmail"),
      this.tokenStorage.retrieve("userTier"),
      this.tokenStorage.retrieve("userRole"),
    ]);
    const userRole = (["owner", "admin", "developer", "viewer"] as const).includes(
      rawRole as TeamRole
    )
      ? (rawRole as TeamRole)
      : null;
    return { accessToken, expiresAt, userEmail, userTier, userRole };
  }

  private _transition(next: SessionState, reason: string): void {
    if (this._disposed) return;
    const previous = this._state;
    this._state = next;
    // Downgrade same-state transitions (e.g. token refresh) to debug to reduce log noise
    const logFn = previous === next ? "debug" : "info";
    this.logger[logFn](`[SessionManager] ${previous} → ${next} (${reason})`);
    // Read session data async and fire event (fire-and-forget)
    void this._readSessionData().then((data) => {
      try {
        this._onSessionChanged.fire({ previous, current: next, data, reason });
      } catch {
        // Event emission is fire-and-forget — errors must not propagate
      }
    });
  }
}
