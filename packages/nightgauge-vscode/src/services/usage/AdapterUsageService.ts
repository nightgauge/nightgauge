/**
 * AdapterUsageService — the single source every usage surface reads
 * (Issue #658).
 *
 * Resolves a `UsageProvider` for the adapter currently configured for the
 * pipeline, caches the snapshot it produces, refreshes it on the configured
 * interval, and fires a change event when the derived usage actually changed.
 * The status-bar meter (#659) and the usage panel (#661) both read this and
 * nothing else, so neither can grow a second derivation path.
 *
 * @see docs/decisions/018-adapter-usage-quota-model.md
 * @see Issue #658 - Provider-neutral adapter usage model
 */

import * as vscode from "vscode";
import type { ExecutionAdapter } from "../../config/schema";
import { getLimitsSettings } from "../../config/limitsSettings";
import { getGlobalAdapterWithSource } from "../../utils/resolvers/adapterResolver";
import { LocalTelemetryUsageProvider, type UsageSessionClock } from "./LocalTelemetryUsageProvider";
import { unknownUsageSnapshot, type UsageProvider, type UsageSnapshot } from "./types";

/**
 * Ordered set of providers. `resolve` returns the first one that claims the
 * adapter, so registration order is precedence: a future per-adapter provider
 * registered ahead of `LocalTelemetryUsageProvider` wins for the adapters it
 * supports and leaves the rest to local telemetry.
 */
export class UsageProviderRegistry {
  private readonly providers: UsageProvider[] = [];

  register(provider: UsageProvider): this {
    this.providers.push(provider);
    return this;
  }

  /** The provider that claims `adapter`, or undefined when none does. */
  resolve(adapter: ExecutionAdapter): UsageProvider | undefined {
    return this.providers.find((provider) => provider.supports(adapter));
  }
}

export interface AdapterUsageServiceOptions {
  /**
   * Resolves the adapter currently configured for the pipeline. Defaults to
   * the global adapter resolver, which honours the env var, ConfigBridge, and
   * the workspace config files in that order.
   */
  resolveAdapter: () => ExecutionAdapter;
}

/**
 * True when two snapshots describe the same usage. `capturedAt` is
 * deliberately excluded — it changes on every refresh, and firing a change
 * event for it would make the event useless to a consumer that re-renders on
 * it.
 */
export function usageSnapshotsEquivalent(a: UsageSnapshot | null, b: UsageSnapshot): boolean {
  if (a === null) {
    return false;
  }
  if (a.adapter !== b.adapter || a.plan.kind !== b.plan.kind) {
    return false;
  }
  if (a.windows.length !== b.windows.length) {
    return false;
  }
  return a.windows.every((windowA, index) => {
    const windowB = b.windows[index];
    return (
      windowA.id === windowB.id &&
      windowA.label === windowB.label &&
      windowA.scope === windowB.scope &&
      windowA.modelFamily === windowB.modelFamily &&
      windowA.used === windowB.used &&
      windowA.limit === windowB.limit &&
      windowA.unit === windowB.unit &&
      (windowA.resetsAt?.getTime() ?? null) === (windowB.resetsAt?.getTime() ?? null) &&
      windowA.confidence === windowB.confidence
    );
  });
}

export class AdapterUsageService implements vscode.Disposable {
  private readonly changeEmitter = new vscode.EventEmitter<UsageSnapshot>();
  /** Fires whenever a refresh produced usage different from the cached one. */
  readonly onDidChangeUsage = this.changeEmitter.event;

  private cached: UsageSnapshot | null = null;
  private refreshTimer: ReturnType<typeof setInterval> | null = null;
  private inFlight: Promise<UsageSnapshot> | null = null;

  constructor(
    private readonly registry: UsageProviderRegistry,
    private readonly options: AdapterUsageServiceOptions
  ) {}

  /**
   * Build the default wiring: local-telemetry usage for the workspace, keyed
   * to the adapter the pipeline is configured to use.
   */
  static forWorkspace(workspaceRoot: string, sessionClock: UsageSessionClock): AdapterUsageService {
    const registry = new UsageProviderRegistry().register(
      LocalTelemetryUsageProvider.forWorkspace(workspaceRoot, sessionClock)
    );
    return new AdapterUsageService(registry, {
      resolveAdapter: () => getGlobalAdapterWithSource(workspaceRoot).adapter,
    });
  }

  /**
   * Derive an initial snapshot and start refreshing on the configured
   * interval. Call once after construction.
   */
  initialize(): void {
    void this.refresh();
    this.refreshTimer = setInterval(() => void this.refresh(), this.refreshIntervalMs());
  }

  /**
   * How often the snapshot is re-derived, in ms.
   *
   * Matches the cadence the existing usage surface already polls on
   * (`ui.limits.polling_interval_seconds`, default 300s) rather than
   * introducing a second usage-refresh number for a user to reconcile.
   */
  refreshIntervalMs(): number {
    return getLimitsSettings().pollingIntervalSeconds * 1000;
  }

  /** The last derived snapshot, or null before the first derivation. */
  getCachedSnapshot(): UsageSnapshot | null {
    return this.cached;
  }

  /** True when there is no snapshot, or the cached one has outlived a refresh interval. */
  isStale(now: Date = new Date()): boolean {
    if (this.cached === null) {
      return true;
    }
    return now.getTime() - this.cached.capturedAt.getTime() >= this.refreshIntervalMs();
  }

  /**
   * The current snapshot: the cached one while it is fresh, otherwise a newly
   * derived one. Consumers that render on demand call this; consumers that
   * react call `onDidChangeUsage`.
   */
  async getSnapshot(): Promise<UsageSnapshot> {
    if (this.cached !== null && !this.isStale()) {
      return this.cached;
    }
    return this.refresh();
  }

  /**
   * Re-derive the snapshot now, firing `onDidChangeUsage` when the result
   * differs from the cached one.
   *
   * Concurrent calls share one derivation — the timer and an on-demand read
   * landing together should not read the history directory twice.
   */
  refresh(): Promise<UsageSnapshot> {
    if (this.inFlight !== null) {
      return this.inFlight;
    }
    const derivation = this.derive()
      .then((snapshot) => {
        const changed = !usageSnapshotsEquivalent(this.cached, snapshot);
        this.cached = snapshot;
        if (changed) {
          this.changeEmitter.fire(snapshot);
        }
        return snapshot;
      })
      .finally(() => {
        this.inFlight = null;
      });
    this.inFlight = derivation;
    return derivation;
  }

  /**
   * Ask the resolved provider for a snapshot, falling back to the unknown
   * snapshot whenever nothing can answer.
   *
   * Three routes end in "unknown", and all three are the same answer to the
   * user: no provider claims the adapter, the provider claims it but has no
   * data, or the derivation threw. A failed read is not evidence of zero
   * usage, so it never becomes a zeroed window.
   */
  private async derive(): Promise<UsageSnapshot> {
    const adapter = this.options.resolveAdapter();
    const provider = this.registry.resolve(adapter);
    if (provider === undefined) {
      return unknownUsageSnapshot(adapter, new Date());
    }
    try {
      const snapshot = await provider.getSnapshot(adapter);
      return snapshot ?? unknownUsageSnapshot(adapter, new Date());
    } catch (error) {
      console.warn(`[Nightgauge] usage provider ${provider.id} failed for ${adapter}:`, error);
      return unknownUsageSnapshot(adapter, new Date());
    }
  }

  dispose(): void {
    if (this.refreshTimer !== null) {
      clearInterval(this.refreshTimer);
      this.refreshTimer = null;
    }
    this.changeEmitter.dispose();
  }
}
