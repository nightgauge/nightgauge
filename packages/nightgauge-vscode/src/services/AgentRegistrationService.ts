import * as vscode from "vscode";
import type { ITokenStorage } from "../platform/TokenStorage";
import type { Logger } from "../utils/logger";
import type { IOnDemandTokenRefresher } from "../platform/TokenRefreshManager";

export interface WorkspaceRegisterMetadata {
  slug: string;
  display_name: string;
}

export interface AgentRegistrationPayload {
  agent_version: string;
  capabilities: string[];
  repos: Array<{ owner: string; repo: string }>;
  machine_id: string;
  vscode_version: string;
  workspace?: WorkspaceRegisterMetadata;
}

export class AgentRegistrationService implements vscode.Disposable {
  /**
   * Human-readable detail for the most recent registration failure, or
   * undefined after a success. `register()` collapses every failure mode to a
   * `null` return, which historically surfaced to the operator as the opaque
   * "Registration returned no agentId" tree item — indistinguishable between an
   * expired token, a 5xx, a network outage, or a malformed body. The workspace
   * sync UI reads this so the sticky "Workspace sync failed" item names the
   * REAL cause (#360). Includes the HTTP status and a truncated response body
   * where available. Non-sensitive: it is the platform's own error text.
   */
  private _lastFailureDetail: string | undefined;

  constructor(
    private readonly getPlatformUrl: () => string,
    private readonly tokenStorage: ITokenStorage,
    private readonly logger: Logger,
    // Refresh is centralized in TokenRefreshManager so registration's 401
    // recovery shares the single-use-token dedup guard (#3751). Optional so the
    // service degrades gracefully when the platform layer is disabled.
    private readonly tokenRefresher?: IOnDemandTokenRefresher
  ) {}

  dispose(): void {
    // No persistent resources
  }

  /** Detail of the last registration failure; undefined after a success. */
  getLastFailureDetail(): string | undefined {
    return this._lastFailureDetail;
  }

  /**
   * POST /v1/agents/register. Returns agentId on success, null on failure.
   * Non-2xx responses are logged but never thrown. On any failure,
   * `getLastFailureDetail()` carries the concrete reason.
   * 401/403 returns null; caller must clear stored agentId and skip heartbeat.
   */
  async register(payload: AgentRegistrationPayload): Promise<string | null> {
    let token = await this.tokenStorage.retrieve("accessToken");
    if (!token) {
      this.failWith("no access token — sign in to sync the workspace");
      return null;
    }
    try {
      let response = await this.postRegister(token, payload);

      // The stored access token may be expired — e.g. VS Code was closed past
      // the 15-minute token lifetime, so registration (which fires on the
      // session-restore transition) can race ahead of the proactive refresh.
      // Refresh once and retry before surfacing a failure, instead of leaving
      // the user stuck on "Workspace sync failed" until a manual re-login.
      if ((response.status === 401 || response.status === 403) && this.tokenRefresher) {
        const refreshed = await this.refreshAccessToken();
        if (refreshed) {
          token = refreshed;
          response = await this.postRegister(token, payload);
        }
      }

      if (response.status === 401 || response.status === 403) {
        this.failWith(
          `authentication failed (HTTP ${response.status})${await this.bodySnippet(response)}`
        );
        return null;
      }
      if (!response.ok) {
        this.failWith(
          `platform returned HTTP ${response.status}${await this.bodySnippet(response)}`
        );
        return null;
      }
      const body = (await response.json().catch(() => ({}))) as Record<string, unknown>;
      const agentId = typeof body["agentId"] === "string" ? body["agentId"] : null;
      if (!agentId) {
        this.failWith("platform response did not include an agentId (HTTP 200)");
        return null;
      }
      this._lastFailureDetail = undefined;
      this.logger.info("AgentRegistrationService: registered", { agentId });
      return agentId;
    } catch (err) {
      this.failWith(`network error: ${err instanceof Error ? err.message : String(err)}`);
      return null;
    }
  }

  /** Record and log a failure detail so the UI and log line agree. */
  private failWith(detail: string): void {
    this._lastFailureDetail = detail;
    this.logger.warn(`AgentRegistrationService: registration failed — ${detail}`);
  }

  /**
   * Best-effort short excerpt of an error response body for diagnostics.
   * Truncated so a large HTML error page can't flood the tooltip/log. Never
   * throws — a body that can't be read yields an empty string.
   */
  private async bodySnippet(response: Response): Promise<string> {
    try {
      const text = (await response.text()).trim();
      if (!text) return "";
      const truncated = text.length > 200 ? `${text.slice(0, 200)}…` : text;
      return `: ${truncated}`;
    } catch {
      return "";
    }
  }

  private postRegister(token: string, payload: AgentRegistrationPayload): Promise<Response> {
    return fetch(`${this.getPlatformUrl()}/v1/agents/register`, {
      method: "POST",
      headers: {
        Authorization: `Bearer ${token}`,
        "Content-Type": "application/json",
      },
      body: JSON.stringify(payload),
    });
  }

  private refreshAccessToken(): Promise<string | null> {
    return this.tokenRefresher?.forceRefresh() ?? Promise.resolve(null);
  }

  /**
   * DELETE /v1/agents/{agentId}. Fire-and-forget — caller should not await.
   * Notifies the platform the agent is going offline so it can re-queue pending
   * commands immediately rather than waiting for heartbeat TTL expiry.
   */
  async deregister(agentId: string): Promise<void> {
    const token = await this.tokenStorage.retrieve("accessToken");
    if (!token) {
      this.logger.warn("AgentRegistrationService: no accessToken, skipping deregister");
      return;
    }
    try {
      const response = await fetch(`${this.getPlatformUrl()}/v1/agents/${agentId}`, {
        method: "DELETE",
        headers: {
          Authorization: `Bearer ${token}`,
          "Content-Type": "application/json",
        },
      });
      if (!response.ok) {
        this.logger.warn(`AgentRegistrationService: deregister failed ${response.status}`);
      } else {
        this.logger.info("AgentRegistrationService: deregistered", { agentId });
      }
    } catch (err) {
      this.logger.warn("AgentRegistrationService: deregister network error", {
        error: err instanceof Error ? err.message : String(err),
      });
    }
  }
}
