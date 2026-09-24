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

/**
 * How one heartbeat attempt ended. `rejected-after-downgrade` is a failure
 * whose one resend has already been spent on the `local` → `unknown`
 * downgrade, so the beat is not retried again.
 */
type AttemptOutcome = "ok" | "failed" | "rejected-after-downgrade";

/**
 * True for a 4xx that rejects the request itself. 401/403 are an expired
 * token (refreshed above) and 429 is rate limiting: neither says anything
 * about the body, so neither downgrades the plan.
 */
function isBodyRejection(status: number): boolean {
  return status >= 400 && status < 500 && status !== 401 && status !== 403 && status !== 429;
}

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
  /**
   * Pinned once the server has rejected a heartbeat carrying `plan: "local"`
   * (Issue #1665). An older self-hosted deployment predates that plan kind
   * and fails the whole body over it, so for the rest of this session every
   * `local` report is sent as `unknown` instead. Never reset: the server does
   * not learn the plan mid-session.
   */
  private localPlanDowngraded = false;

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
    const outcome = await this.attemptHeartbeat();
    if (outcome !== "ok") {
      let retrySuccess = false;
      // A beat whose downgrade resend was rejected has already had its one retry.
      if (outcome === "failed") {
        await sleep(RETRY_DELAY_MS);
        retrySuccess = (await this.attemptHeartbeat()) === "ok";
      }
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

  private async attemptHeartbeat(): Promise<AttemptOutcome> {
    try {
      let token = await this.tokenStorage.retrieve("accessToken");
      if (!token || !this.agentId) return "failed";

      const usage = await this.usageReport();
      let response = await this.putHeartbeat(token, usage);

      // On 401/403, refresh once and retry (mirrors registration fix #3697).
      if ((response.status === 401 || response.status === 403) && this.tokenRefresher) {
        const refreshed = await this.refreshAccessToken();
        if (refreshed) {
          token = refreshed;
          response = await this.putHeartbeat(token, usage);
        }
      }

      // Issue #1665: a server that predates `plan: "local"` rejects the whole
      // body over it. Resend this beat exactly once as `unknown` and pin that
      // for the session. Not a loop: once pinned, no report is `local` again,
      // so this branch cannot be taken twice.
      if (
        !response.ok &&
        isBodyRejection(response.status) &&
        usage?.plan === "local" &&
        !this.localPlanDowngraded
      ) {
        this.localPlanDowngraded = true;
        this.logger.warn(
          `AgentHeartbeatService: the server rejected usage plan "local" (HTTP ${response.status}); ` +
            'reporting plan "unknown" for the rest of this session'
        );
        response = await this.putHeartbeat(token, downgradeLocalPlan(usage));
        return response.ok ? "ok" : "rejected-after-downgrade";
      }

      return response.ok ? "ok" : "failed";
    } catch {
      return "failed";
    }
  }

  private async putHeartbeat(token: string, usage: ReportedUsage | null): Promise<Response> {
    return fetch(`${this.getPlatformUrl()}/v1/agents/${this.agentId!}/heartbeat`, {
      method: "PUT",
      headers: {
        Authorization: `Bearer ${token}`,
        "Content-Type": "application/json",
      },
      ...(usage === null ? {} : { body: JSON.stringify({ usage }) }),
    });
  }

  /**
   * The usage report for this beat, or `null` for a bodiless PUT — no `body`
   * key at all. Once the session is downgraded, a `local` report is sent as
   * `unknown`.
   *
   * A usage report must never cost the operator agent presence, so a provider
   * that throws is swallowed: the beat proceeds bodiless and the dashboard
   * keeps whatever it was last told. Losing one sample is a far better trade
   * than flipping a machine offline over telemetry.
   */
  private async usageReport(): Promise<ReportedUsage | null> {
    if (!this.getUsageReport) {
      return null;
    }
    try {
      const usage = await this.getUsageReport();
      if (usage !== null && usage.plan === "local" && this.localPlanDowngraded) {
        return downgradeLocalPlan(usage);
      }
      return usage;
    } catch (error) {
      this.logger.warn(`AgentHeartbeatService: usage report skipped — ${String(error)}`);
      return null;
    }
  }

  private refreshAccessToken(): Promise<string | null> {
    return this.tokenRefresher?.forceRefresh() ?? Promise.resolve(null);
  }
}

/**
 * A `local` report as a server that predates the plan kind accepts it:
 * `unknown`, with its windows dropped, because ADR-018 pairs `unknown` with
 * an empty window list and a server that cannot name the plan cannot label
 * its windows either.
 */
function downgradeLocalPlan(usage: ReportedUsage): ReportedUsage {
  return { ...usage, plan: "unknown", windows: [] };
}
