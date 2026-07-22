/**
 * OAuthDeviceFlowService — Orchestrates the OAuth Device Flow (RFC 8628).
 *
 * Manages the full lifecycle: request device code → display to user →
 * poll for token → store tokens → emit events.
 *
 * @see Issue #1464 - Implement OAuth Device Flow login command and UI
 * @see Issue #1452 - Epic: Platform API integration
 */

import * as vscode from "vscode";
import { TokenStorage } from "../platform/TokenStorage";
import type { IpcClient } from "./IpcClient";
import type { Logger } from "../utils/logger";

export type DeviceFlowState = "idle" | "polling" | "signed-in" | "cancelled" | "error";

/** Maximum poll interval cap in seconds (RFC 8628 recommendation). */
const MAX_POLL_INTERVAL_S = 30;

export class OAuthDeviceFlowService implements vscode.Disposable {
  private _state: DeviceFlowState = "idle";
  private _pollTimer: ReturnType<typeof setTimeout> | null = null;
  private _cancelled = false;

  private readonly _onSignedIn = new vscode.EventEmitter<void>();
  readonly onSignedIn = this._onSignedIn.event;

  private readonly _onSignedOut = new vscode.EventEmitter<void>();
  readonly onSignedOut = this._onSignedOut.event;

  constructor(
    private readonly ipcClient: IpcClient,
    private readonly logger: Logger
  ) {}

  get state(): DeviceFlowState {
    return this._state;
  }

  /** Returns true if a valid access token exists in secret storage. */
  async isSignedIn(): Promise<boolean> {
    const token = await this.getAccessToken();
    return token !== null;
  }

  /** Returns the stored access token, or null. Used by AuthProvider. */
  async getAccessToken(): Promise<string | null> {
    const tokenStorage = TokenStorage.getInstance();
    if (!tokenStorage) return null;
    return tokenStorage.retrieve("accessToken");
  }

  /**
   * Start the OAuth Device Flow.
   * 1. POST /v1/auth/device/code
   * 2. Show device code notification
   * 3. Poll /v1/auth/device/token until granted, expired, or cancelled
   */
  async startDeviceFlow(): Promise<void> {
    // Re-entrancy guard
    if (this._state === "polling") {
      vscode.window.showInformationMessage("Nightgauge: Sign-in already in progress.");
      return;
    }

    this.logger.info("Starting OAuth Device Flow");
    this._cancelled = false;

    // Step 1: Request device code via IPC
    const deviceCodeResponse = await this.ipcClient.platformAuthDeviceCode();
    const {
      device_code: deviceCode,
      user_code: userCode,
      verification_uri: verificationUri,
      expires_in: expiresIn,
      interval,
    } = deviceCodeResponse;

    this.logger.info("Device code received", {
      userCode,
      verificationUri,
      expiresIn,
    });

    // Step 2: Show notification with actions
    const copyAction = "Copy Code";
    const openAction = "Open Browser";
    vscode.window
      .showInformationMessage(
        `Nightgauge: Visit ${verificationUri} and enter code: ${userCode}`,
        copyAction,
        openAction
      )
      .then(async (action) => {
        if (action === copyAction) {
          await vscode.env.clipboard.writeText(userCode);
          vscode.window.showInformationMessage("Nightgauge: Code copied to clipboard.");
        } else if (action === openAction) {
          await vscode.env.openExternal(vscode.Uri.parse(verificationUri));
        }
      });

    // Step 3: Poll for token
    this._state = "polling";
    const deadline = Date.now() + expiresIn * 1000;
    let currentInterval = interval;

    while (!this._cancelled && Date.now() < deadline) {
      await this.delay(currentInterval * 1000);
      if (this._cancelled) break;

      try {
        const result = (await this.ipcClient.platformAuthDeviceToken(deviceCode)) as Record<
          string,
          unknown
        >;

        if ("access_token" in result) {
          // Success — store tokens
          await this.storeTokens(
            result as {
              access_token: string;
              refresh_token: string;
              expires_in: number;
            }
          );
          this._state = "signed-in";
          this._onSignedIn.fire();
          this.logger.info("OAuth Device Flow completed — signed in");
          vscode.window.showInformationMessage("Nightgauge: Signed in successfully!");
          return;
        }

        // Pending response — check status for slow_down
        if (result.status === "slow_down") {
          currentInterval = Math.min(currentInterval + 5, MAX_POLL_INTERVAL_S);
          this.logger.info("Poll slow_down — increasing interval", {
            currentInterval,
          });
        }
        // authorization_pending: continue with same interval
      } catch (err) {
        const msg = err instanceof Error ? err.message : String(err);
        this._state = "error";
        this.logger.error("OAuth Device Flow error", err as Error);
        vscode.window.showErrorMessage(`Nightgauge: Sign-in failed — ${msg}`);
        return;
      }
    }

    // Reached here: either cancelled or timed out
    if (this._cancelled) {
      this._state = "cancelled";
      this.logger.info("OAuth Device Flow cancelled by user");
    } else {
      this._state = "error";
      this.logger.warn("OAuth Device Flow timed out");
      vscode.window.showWarningMessage(
        "Nightgauge: Sign-in timed out. The device code expired. Please try again."
      );
    }
  }

  /** Clear all stored platform auth state. */
  async signOut(): Promise<void> {
    const tokenStorage = TokenStorage.getInstance();
    if (tokenStorage) {
      await tokenStorage.clear();
    }

    this.cancelPolling();
    this._state = "idle";
    this._onSignedOut.fire();
    this.logger.info("Signed out — auth state cleared");
  }

  /** Cancel active polling loop. */
  cancelPolling(): void {
    this._cancelled = true;
    if (this._pollTimer) {
      clearTimeout(this._pollTimer);
      this._pollTimer = null;
    }
  }

  dispose(): void {
    this.cancelPolling();
    this._onSignedIn.dispose();
    this._onSignedOut.dispose();
  }

  // ---------------------------------------------------------------------------
  // Private helpers
  // ---------------------------------------------------------------------------

  private async storeTokens(tokenResponse: {
    access_token: string;
    refresh_token: string;
    expires_in: number;
  }): Promise<void> {
    const tokenStorage = TokenStorage.getInstance();
    if (!tokenStorage) {
      this.logger.error("TokenStorage not available — cannot store tokens");
      return;
    }

    await tokenStorage.store("accessToken", tokenResponse.access_token);
    await tokenStorage.store("refreshToken", tokenResponse.refresh_token);

    const expiresAt = new Date(Date.now() + tokenResponse.expires_in * 1000).toISOString();
    await tokenStorage.store("expiresAt", expiresAt);
  }

  private delay(ms: number): Promise<void> {
    return new Promise((resolve) => {
      this._pollTimer = setTimeout(resolve, ms);
    });
  }
}
