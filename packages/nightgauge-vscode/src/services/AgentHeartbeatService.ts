import * as vscode from "vscode";
import type { ITokenStorage } from "../platform/TokenStorage";
import type { Logger } from "../utils/logger";
import type { IOnDemandTokenRefresher } from "../platform/TokenRefreshManager";
import type { ReportedUsage } from "./usage/usageReporting";

/**
 * Supplies the adapter usage report to attach to a beat, or `null` to send
 * none (Issue #736).
 *
 * A callback rather than a service reference so this class keeps knowing
 * nothing about the usage model: it transports whatever it is handed, and
 * every decision about what may leave the machine stays in
 * `usageReporting.ts`. Returning `null` — which is the default configuration —
 * leaves the heartbeat exactly as it was before reporting existed.
 */
export type UsageReportProvider = () => Promise<ReportedUsage | null>;

const HEARTBEAT_INTERVAL_MS = 30_000;
const RETRY_DELAY_MS = 5_000;
const MAX_CONSECUTIVE_FAILURES = 3;

function sleep(ms: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

export class AgentHeartbeatService implements vscode.Disposable {
  private timer: ReturnType<typeof setInterval> | null = null;
  private consecutiveFailures = 0;
  private agentId: string | null = null;

  constructor(
    private readonly getPlatformUrl: () => string,
    private readonly tokenStorage: ITokenStorage,
    private readonly logger: Logger,
    // Centralized refresh (#3751) — see AgentRegistrationService for rationale.
    private readonly tokenRefresher?: IOnDemandTokenRefresher,
    /**
     * Optional. Absent, or returning null, means the bodiless PUT — the only
     * behaviour that existed before Issue #736 and still the default.
     */
    private readonly getUsageReport?: UsageReportProvider
  ) {}

  /** Call once agentId is available from registration. No-op if already started. */
  start(agentId: string): void {
    if (this.timer !== null || !agentId) return;
    this.agentId = agentId;
    this.timer = setInterval(() => {
      void this.sendHeartbeat();
    }, HEARTBEAT_INTERVAL_MS);
  }

  dispose(): void {
    if (this.timer !== null) {
      clearInterval(this.timer);
      this.timer = null;
    }
    this.agentId = null;
  }

  private async sendHeartbeat(): Promise<void> {
    if (!this.agentId) return;
    const success = await this.attemptHeartbeat();
    if (!success) {
      await sleep(RETRY_DELAY_MS);
      const retrySuccess = await this.attemptHeartbeat();
      if (!retrySuccess) {
        this.consecutiveFailures++;
        if (this.consecutiveFailures >= MAX_CONSECUTIVE_FAILURES) {
          this.logger.warn(
            `AgentHeartbeatService: ${this.consecutiveFailures} consecutive heartbeat failures for agent ${this.agentId}`
          );
        }
        return;
      }
    }
    this.consecutiveFailures = 0;
  }

  private async attemptHeartbeat(): Promise<boolean> {
    try {
      let token = await this.tokenStorage.retrieve("accessToken");
      if (!token || !this.agentId) return false;

      let response = await this.putHeartbeat(token);

      // On 401/403, refresh once and retry (mirrors registration fix #3697).
      if ((response.status === 401 || response.status === 403) && this.tokenRefresher) {
        const refreshed = await this.refreshAccessToken();
        if (refreshed) {
          response = await this.putHeartbeat(refreshed);
        }
      }

      return response.ok;
    } catch {
      return false;
    }
  }

  private async putHeartbeat(token: string): Promise<Response> {
    return fetch(`${this.getPlatformUrl()}/v1/agents/${this.agentId!}/heartbeat`, {
      method: "PUT",
      headers: {
        Authorization: `Bearer ${token}`,
        "Content-Type": "application/json",
      },
      ...(await this.usageBody()),
    });
  }

  /**
   * The request body for this beat: `{ body }` when a report is due, and an
   * empty object — no `body` key at all — otherwise.
   *
   * A usage report must never cost the operator agent presence, so a provider
   * that throws is swallowed: the beat proceeds bodiless and the dashboard
   * keeps whatever it was last told. Losing one sample is a far better trade
   * than flipping a machine offline over telemetry.
   */
  private async usageBody(): Promise<{ body?: string }> {
    if (!this.getUsageReport) {
      return {};
    }
    try {
      const usage = await this.getUsageReport();
      return usage === null ? {} : { body: JSON.stringify({ usage }) };
    } catch (error) {
      this.logger.warn(`AgentHeartbeatService: usage report skipped — ${String(error)}`);
      return {};
    }
  }

  private refreshAccessToken(): Promise<string | null> {
    return this.tokenRefresher?.forceRefresh() ?? Promise.resolve(null);
  }
}
