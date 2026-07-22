/**
 * GitHubAuthService — Authentication via VSCode's built-in GitHub auth provider.
 *
 * Acquires a GitHub session via vscode.authentication.getSession, then exchanges
 * the GitHub access token for Nightgauge platform tokens via Go IPC
 * (`platform.authGithub`). Stores the resulting tokens via TokenStorage so that
 * TokenRefreshManager picks them up without modification.
 *
 * @see Issue #1467 - Add GitHub Sign-in as alternative auth path
 * @see Issue #1452 - Epic: Platform API integration
 */

import * as vscode from "vscode";
import { TokenStorage } from "../platform/TokenStorage";
import type { IpcClient } from "./IpcClient";
import type { Logger } from "../utils/logger";

/** GitHub OAuth scopes requested for identity confirmation. */
const GITHUB_SCOPES = ["user:email"];

/**
 * Discriminator values for the sign-in quick pick options.
 * Using constants instead of substring matching for robust label comparison.
 */
export const AUTH_METHOD = {
  GITHUB: "github",
  DEVICE_FLOW: "device-flow",
} as const;
export type AuthMethod = (typeof AUTH_METHOD)[keyof typeof AUTH_METHOD];

export class GitHubAuthService implements vscode.Disposable {
  private readonly _onSignedIn = new vscode.EventEmitter<void>();
  readonly onSignedIn = this._onSignedIn.event;

  private readonly _onSignedOut = new vscode.EventEmitter<void>();
  readonly onSignedOut = this._onSignedOut.event;

  constructor(
    private readonly ipcClient: IpcClient,
    private readonly logger: Logger
  ) {}

  /**
   * Authenticate using VSCode's built-in GitHub auth provider.
   *
   * Returns `true` on success, `false` if the user cancelled or a
   * platform API error was shown to the user. Throws on unexpected errors.
   */
  async signInWithGitHub(): Promise<boolean> {
    this.logger.info("GitHub Sign-in: requesting GitHub session");

    let session: vscode.AuthenticationSession;
    try {
      session = await vscode.authentication.getSession("github", GITHUB_SCOPES, {
        createIfNone: true,
      });
    } catch {
      // User cancelled the GitHub login dialog — treat as a silent no-op
      this.logger.info("GitHub Sign-in: user cancelled");
      return false;
    }

    this.logger.info("GitHub Sign-in: session acquired, exchanging token");

    try {
      const tokenResponse = await this.ipcClient.platformAuthGithub(session.accessToken);
      await this.storeTokens(tokenResponse);
      this._onSignedIn.fire();
      this.logger.info("GitHub Sign-in: signed in successfully");
      vscode.window.showInformationMessage("Nightgauge: Signed in via GitHub!");
      return true;
    } catch (err) {
      const message = err instanceof Error ? err.message : String(err);
      vscode.window.showErrorMessage(`Nightgauge: GitHub sign-in failed — ${message}`);
      this.logger.error("GitHub Sign-in: platform token exchange failed", err as Error);
      return false;
    }
  }

  /** Returns `true` if a valid access token is currently stored. */
  async isSignedIn(): Promise<boolean> {
    const token = await this.getAccessToken();
    return token !== null;
  }

  /** Returns the stored access token, or `null` if not signed in. */
  async getAccessToken(): Promise<string | null> {
    const tokenStorage = TokenStorage.getInstance();
    if (!tokenStorage) return null;
    return tokenStorage.retrieve("accessToken");
  }

  dispose(): void {
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
}
