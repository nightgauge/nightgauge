/**
 * PlatformStatusBarItem — VSCode status bar item showing platform connection and auth state.
 *
 * Displays the current platform connection state (Connected / Degraded / Offline /
 * Disabled) combined with auth state (signed in/out, user email, tier) in the
 * status bar and updates in real-time.
 *
 * After Issue #2091, connection state tracking is no longer driven by
 * PlatformApiClient events. The status bar starts as 'connected' when the
 * platform is enabled and relies on session state for user-facing updates.
 * Rate limit retry events are still sourced from PlatformApiClient (OAuth).
 *
 * @see Issue #1461 - Add platform connection status indicator to status bar
 * @see Issue #1469 - Add auth status indicators to sidebar and status bar
 * @see Issue #2091 - Remove PlatformApiClient HTTP code and consolidate types
 */

import * as vscode from "vscode";
import type { SessionManager, SessionState, SessionData } from "./SessionManager";
import type { TrialStateStore, TrialStatus } from "./TrialState";
import type { PlatformConfig } from "../config/schema";

/** Operational connection state (formerly from PlatformApiClient). */
type ConnectionState = "connected" | "disconnected" | "degraded";

/** Minimal interface for connection state eventing (decoupled from PlatformApiClient). */
interface ConnectionStateEmitter {
  getConnectionState(): ConnectionState;
  onConnectionStateChanged: vscode.Event<ConnectionState>;
  onRateLimitRetry: vscode.Event<{ retryInSeconds: number; attempt: number }>;
}

/**
 * The four display states for the platform status bar item.
 * 'disabled' is shown when platform.enabled = false in config.
 */
export type PlatformDisplayState = "connected" | "degraded" | "offline" | "disabled";

/** Visual configuration per display state. */
interface StateDisplay {
  icon: string;
  label: string;
  backgroundColor: vscode.ThemeColor | undefined;
}

const STATE_DISPLAY: Record<PlatformDisplayState, StateDisplay> = {
  connected: {
    icon: "$(check)",
    label: "Platform: Connected",
    backgroundColor: undefined,
  },
  degraded: {
    icon: "$(warning)",
    label: "Platform: Degraded",
    backgroundColor: new vscode.ThemeColor("statusBarItem.warningBackground"),
  },
  offline: {
    icon: "$(error)",
    label: "Platform: Offline",
    backgroundColor: new vscode.ThemeColor("statusBarItem.errorBackground"),
  },
  disabled: {
    icon: "$(circle-slash)",
    label: "Platform: Disabled",
    backgroundColor: undefined,
  },
};

/**
 * Formats the auth-aware status bar label.
 *
 * When offline/degraded/disabled, connection state takes precedence.
 * When connected, augments the label with auth state:
 *   - connected + unauthenticated → "$(sign-in) Sign In"
 *   - connected + authenticated, no email → "$(account) Signed In"
 *   - connected + authenticated, email known → "$(account) user@email"
 *   - connected + authenticated, email+tier → "$(account) user@email (Pro)"
 */
function buildLabel(
  displayState: PlatformDisplayState,
  sessionState: SessionState | null,
  sessionData: SessionData | null,
  trial: TrialStatus | null = null
): string {
  const base = STATE_DISPLAY[displayState];

  if (displayState !== "connected" || sessionState === null) {
    return base.label;
  }

  if (sessionState === "authenticated") {
    if (sessionData?.userEmail) {
      // A live trial takes precedence over the plain tier label so the countdown
      // is always visible; an expired trial nudges toward upgrading.
      let suffix = sessionData.userTier ? ` (${capitalize(sessionData.userTier)})` : "";
      if (trial?.expired) {
        suffix = " (Trial expired)";
      } else if (trial?.active) {
        suffix = ` (Trial · ${trial.daysRemaining}d left)`;
      }
      return `$(account) ${sessionData.userEmail}${suffix}`;
    }
    return "$(account) Signed In";
  }

  if (sessionState === "authenticating") {
    return "$(sync~spin) Signing in…";
  }

  // unauthenticated or error — show sign-in prompt
  return "$(sign-in) Sign In";
}

function capitalize(s: string): string {
  return s.charAt(0).toUpperCase() + s.slice(1);
}

/**
 * PlatformStatusBarItem manages the platform connection + auth indicator in the VSCode status bar.
 *
 * When platform.enabled = false in config, shows "Platform: Disabled" and does not
 * subscribe to any events. Otherwise, starts as 'connected' and subscribes to
 * SessionManager.onSessionChanged for auth state updates and PlatformApiClient
 * onRateLimitRetry for rate limit display.
 *
 * Clicking the item runs `nightgauge.showPlatformStatus` which shows either:
 *   - Auth quick pick (when authenticated): Sign Out, Switch Account, View Account
 *   - Connection details info message (when not authenticated)
 *
 * @example
 * ```typescript
 * const statusItem = new PlatformStatusBarItem(client, platformConfig, sessionManager);
 * context.subscriptions.push(statusItem);
 * ```
 */
export class PlatformStatusBarItem implements vscode.Disposable {
  readonly item: vscode.StatusBarItem;

  private displayState: PlatformDisplayState;
  private sessionState: SessionState | null = null;
  private sessionData: SessionData | null = null;
  private readonly apiUrl: string;
  private readonly disposables: vscode.Disposable[] = [];
  private rateLimitOverrideLabel: string | null = null;
  private rateLimitOverrideTimer: ReturnType<typeof setTimeout> | null = null;

  constructor(
    private readonly client: ConnectionStateEmitter | null,
    private readonly config: PlatformConfig | undefined,
    sessionManager?: SessionManager | null,
    private readonly trialStore?: TrialStateStore | null
  ) {
    this.item = vscode.window.createStatusBarItem(
      vscode.StatusBarAlignment.Left,
      97 // To the right of usageItem (98) in the status bar
    );
    this.item.command = "nightgauge.showPlatformStatus";

    this.apiUrl = config?.api_url ?? "https://api.nightgauge.dev";

    const platformEnabled = config?.enabled ?? false;

    if (!platformEnabled) {
      this.displayState = "disabled";
      this.render();
    } else {
      // Start with actual connection state if available, otherwise assume connected
      if (client) {
        const initial = client.getConnectionState();
        this.displayState =
          initial === "disconnected"
            ? "offline"
            : initial === "degraded"
              ? "degraded"
              : "connected";
      } else {
        this.displayState = "connected";
      }
      this.render();

      // Subscribe to connection state changes from OfflineManager adapter
      if (client) {
        const connSub = client.onConnectionStateChanged((state) => {
          this.displayState =
            state === "disconnected" ? "offline" : state === "degraded" ? "degraded" : "connected";
          this.render();
        });
        this.disposables.push(connSub);

        const rateLimitSub = client.onRateLimitRetry(({ retryInSeconds }) => {
          this.rateLimitOverrideLabel = `$(sync~spin) Platform: Rate limited, retrying in ${retryInSeconds}s…`;
          // Auto-clear after delay + small buffer so label doesn't linger
          if (this.rateLimitOverrideTimer !== null) {
            clearTimeout(this.rateLimitOverrideTimer);
          }
          this.rateLimitOverrideTimer = setTimeout(
            () => {
              this.rateLimitOverrideLabel = null;
              this.rateLimitOverrideTimer = null;
              this.render();
            },
            (retryInSeconds + 1) * 1_000
          );
          this.render();
        });
        this.disposables.push(rateLimitSub);
      }

      if (sessionManager) {
        const sessionSubscription = sessionManager.onSessionChanged((evt) => {
          this.sessionState = evt.current;
          this.sessionData = evt.data;
          this.render();
        });
        this.disposables.push(sessionSubscription);
      }

      // Keep the trial countdown honest across a long-running session. The day
      // count only changes daily, so an hourly re-render is plenty; unref so it
      // never holds the process open.
      if (this.trialStore) {
        const timer = setInterval(() => this.render(), 60 * 60 * 1000);
        (timer as { unref?: () => void }).unref?.();
        this.disposables.push({ dispose: () => clearInterval(timer) });
      }
    }

    this.item.show();
  }

  /**
   * Get the current display state (for testing).
   */
  getDisplayState(): PlatformDisplayState {
    return this.displayState;
  }

  /**
   * Get the current session state (for testing).
   */
  getSessionState(): SessionState | null {
    return this.sessionState;
  }

  /**
   * Get the connection details string shown when user clicks the status bar item.
   */
  getConnectionDetails(): string {
    const stateLabel = STATE_DISPLAY[this.displayState].label;

    return [stateLabel, `URL: ${this.apiUrl}`].join("\n");
  }

  /**
   * Returns true if the user is currently authenticated.
   */
  isAuthenticated(): boolean {
    return this.sessionState === "authenticated";
  }

  dispose(): void {
    if (this.rateLimitOverrideTimer !== null) {
      clearTimeout(this.rateLimitOverrideTimer);
    }
    for (const d of this.disposables) {
      d.dispose();
    }
    this.item.dispose();
  }

  private render(): void {
    const trial = this.trialStore?.status() ?? null;
    this.item.text =
      this.rateLimitOverrideLabel ??
      buildLabel(this.displayState, this.sessionState, this.sessionData, trial);
    this.item.backgroundColor = STATE_DISPLAY[this.displayState].backgroundColor;

    // When unauthenticated and connected, click should sign in directly
    if (
      this.displayState === "connected" &&
      this.sessionState !== "authenticated" &&
      this.sessionState !== "authenticating"
    ) {
      this.item.command = "nightgauge.signIn";
      this.item.tooltip = "Click to sign in to Nightgauge";
    } else {
      this.item.command = "nightgauge.showPlatformStatus";
      // Build tooltip
      const details = this.getConnectionDetails();
      const authLine =
        this.sessionState === "authenticated"
          ? `${this.sessionData?.userEmail ?? "Signed in"}${this.sessionData?.userTier ? ` · ${capitalize(this.sessionData.userTier)}` : ""}`
          : this.sessionState
            ? `Auth: ${this.sessionState}`
            : "Auth: Unknown";

      const trialLine = trial?.active
        ? `Free Pro trial · ${trial.daysRemaining} day${trial.daysRemaining === 1 ? "" : "s"} left · ${trial.record.runAllowance} runs`
        : trial?.expired
          ? "Free trial expired — upgrade to keep Pro"
          : null;

      this.item.tooltip = [details, authLine, trialLine, "Click for account options"]
        .filter(Boolean)
        .join("\n");
    }
  }
}
