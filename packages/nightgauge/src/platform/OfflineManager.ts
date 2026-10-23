/**
 * OfflineManager — Periodic health check and connection state machine.
 *
 * Monitors platform API reachability via `GET /v1/health` (or an injected
 * `IHealthChecker`). Maintains a three-state connection model:
 *
 *   online → degraded → offline
 *
 * Hysteresis: 1 failure to enter degraded, `failureThreshold` consecutive
 * failures to enter offline, and 1 success to recover to online.
 *
 * Consumers register fallback strategies for named operations (e.g.
 * 'skillResolve') and subscribe to `onStateChanged` events to adapt their
 * behavior when the platform is unreachable.
 *
 * @see Issue #1459 - Offline detection and degraded mode fallback framework
 * @see Issue #1452 - Epic: Platform API integration
 */

import * as vscode from "vscode";
import type {
  ConnectionState,
  ConnectionStateEvent,
  FallbackStrategy,
  IHealthChecker,
  OfflineManagerConfig,
} from "./types";

const DEFAULTS: Required<
  Pick<OfflineManagerConfig, "intervalMs" | "timeoutMs" | "failureThreshold">
> = {
  intervalMs: 60_000,
  timeoutMs: 10_000,
  failureThreshold: 3,
};

export class OfflineManager implements vscode.Disposable {
  // State
  private _state: ConnectionState = "offline";
  private _consecutiveFailures = 0;
  private _timer: ReturnType<typeof setInterval> | null = null;

  // Config
  private readonly _config: Required<OfflineManagerConfig>;

  // EventEmitter for state change notifications
  private readonly _onStateChanged = new vscode.EventEmitter<ConnectionStateEvent>();
  readonly onStateChanged = this._onStateChanged.event;

  // Fallback registry
  private readonly _registry = new Map<string, FallbackStrategy>();

  // Optional injected health checker (for PlatformApiClient integration later)
  private readonly _checker: IHealthChecker | null;

  constructor(
    config: Partial<OfflineManagerConfig> & { getBaseUrl: () => string },
    checker?: IHealthChecker,
    private readonly _logger?: {
      debug: (m: string) => void;
      warn: (m: string) => void;
    }
  ) {
    this._config = {
      getBaseUrl: config.getBaseUrl,
      intervalMs: config.intervalMs ?? DEFAULTS.intervalMs,
      timeoutMs: config.timeoutMs ?? DEFAULTS.timeoutMs,
      failureThreshold: config.failureThreshold ?? DEFAULTS.failureThreshold,
    };
    this._checker = checker ?? null;
  }

  // --- Public API ---

  get state(): ConnectionState {
    return this._state;
  }

  /**
   * Register a fallback strategy for a named operation.
   * Called by consumers: offlineManager.register('skillResolve', localSkillLoader)
   */
  register<T>(operationName: string, strategy: FallbackStrategy<T>): void {
    this._registry.set(operationName, strategy as FallbackStrategy);
  }

  /** Retrieve a registered fallback or undefined if not found. */
  getStrategy<T>(operationName: string): FallbackStrategy<T> | undefined {
    return this._registry.get(operationName) as FallbackStrategy<T> | undefined;
  }

  /**
   * Start the periodic health check timer.
   * Safe to call multiple times — no-op if already running.
   */
  start(): void {
    if (this._timer !== null) return;
    // Run immediately, then on interval
    void this._tick();
    this._timer = setInterval(() => void this._tick(), this._config.intervalMs);
  }

  /**
   * Stop the periodic health check timer.
   * Does NOT clear state — the current connection state is preserved.
   */
  stop(): void {
    if (this._timer !== null) {
      clearInterval(this._timer);
      this._timer = null;
    }
  }

  dispose(): void {
    this.stop();
    this._onStateChanged.dispose();
    this._registry.clear();
  }

  // --- State machine (private) ---

  private async _tick(): Promise<void> {
    const reachable = await this._performCheck();
    this._handleResult(reachable);
  }

  private async _performCheck(): Promise<boolean> {
    if (this._checker) {
      try {
        const result = await this._checker.checkHealth();
        return result.reachable;
      } catch {
        return false;
      }
    }
    return this._fetchHealth();
  }

  private async _fetchHealth(): Promise<boolean> {
    const url = `${this._config.getBaseUrl()}/v1/health`;
    const controller = new AbortController();
    const timeout = setTimeout(() => controller.abort(), this._config.timeoutMs);
    try {
      const res = await fetch(url, { signal: controller.signal });
      return res.ok;
    } catch {
      return false;
    } finally {
      clearTimeout(timeout);
    }
  }

  /**
   * State machine transitions.
   *
   * Online → Degraded: 1st failure (starts counting)
   * Degraded → Offline: failureThreshold consecutive failures
   * Degraded → Online: 1 success (recovers immediately)
   * Offline → Online: 1 success (via degraded intermediate)
   *
   * Hysteresis: failureThreshold failures to go offline, 1 success to come back.
   */
  private _handleResult(reachable: boolean): void {
    if (reachable) {
      this._consecutiveFailures = 0;
      if (this._state !== "online") {
        if (this._state === "offline") {
          this._transition("degraded", "1 success from offline — beginning recovery");
        }
        this._transition("online", "1 success — connection restored");
      }
      return;
    }

    // Not reachable
    this._consecutiveFailures++;
    if (this._state === "online") {
      this._transition("degraded", "1 failure from online");
    } else if (
      this._state === "degraded" &&
      this._consecutiveFailures >= this._config.failureThreshold
    ) {
      this._transition("offline", `${this._consecutiveFailures} consecutive failures`);
    }
    // If already offline, stay offline (no state change)
  }

  private _transition(next: ConnectionState, reason: string): void {
    const prev = this._state;
    if (prev === next) return;
    this._state = next;
    this._logger?.debug(`[OfflineManager] ${prev} → ${next}: ${reason}`);
    this._onStateChanged.fire({
      previous: prev,
      current: next,
      at: new Date().toISOString(),
      reason,
    });
  }
}
