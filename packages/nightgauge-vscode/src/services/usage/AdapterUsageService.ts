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
import { probeClaudeFeedHealth } from "./claudeFeedHealth";
import { readClaudePlanDeclaration, type ClaudePlanDeclaration } from "./claudePlanDeclaration";
import type { ClaudeFeedHealth } from "./claudeStatusLineSetup";
import { ClaudeRateLimitUsageProvider } from "./ClaudeRateLimitUsageProvider";
import type { ClaudeRateLimitStore } from "./ClaudeRateLimitStore";
import { unknownUsageSnapshot, type UsageProvider, type UsageSnapshot } from "./types";

/**
 * Ordered set of providers. `resolveAll` returns every provider that claims
 * the adapter, in registration order, so registration order is precedence: a
 * per-adapter provider registered ahead of `LocalTelemetryUsageProvider`
 * answers first for the adapters it supports and falls back to local
 * telemetry when it has nothing to say.
 */
export class UsageProviderRegistry {
  private readonly providers: UsageProvider[] = [];

  register(provider: UsageProvider): this {
    this.providers.push(provider);
    return this;
  }

  /**
   * Every provider that claims `adapter`, highest precedence first. Empty
   * when none does.
   *
   * A list rather than a single winner because `supports` answers "could I
   * ever describe this adapter", while only `getSnapshot` knows whether it
   * has anything today (see `UsageProvider`'s doc comment). One provider
   * claiming an adapter must not silence the others (Issue #709).
   */
  resolveAll(adapter: ExecutionAdapter): UsageProvider[] {
    return this.providers.filter((provider) => provider.supports(adapter));
  }
}

export interface AdapterUsageServiceOptions {
  /**
   * Resolves the adapter currently configured for the pipeline. Defaults to
   * the global adapter resolver, which honours the env var, ConfigBridge, and
   * the workspace config files in that order.
   */
  resolveAdapter: () => ExecutionAdapter;
  /**
   * Reports the Claude usage feed's health (#810).
   *
   * Required to be explicit, with NO default: defaulting to the real probe made
   * every service constructed in a test read the developer's own
   * ~/.claude/settings.json on each refresh. Hidden filesystem IO behind a
   * default is worth avoiding on its own, and here it also broke a fake-timer
   * test whose refresh could no longer resolve inside an advanced tick.
   *
   * Omitted, snapshots carry no health — which every surface already renders as
   * "not known yet".
   */
  probeClaudeFeedHealth?: () => Promise<ClaudeFeedHealth>;
  /**
   * Reads the operator's declared Claude plan (#808). Explicit for the same
   * reason as the probe above: no hidden host access behind a default.
   */
  readClaudePlanDeclaration?: () => ClaudePlanDeclaration;
}

/**
 * True when two snapshots describe the same usage. `capturedAt` is
 * deliberately excluded — it changes on every refresh, and firing a change
 * event for it would make the event useless to a consumer that re-renders on
 * it.
 *
 * A window's `observedAt` is deliberately **included**: unlike `capturedAt`
 * it moves only when the provider actually saw a new reading, and the as-of
 * a surface prints is part of what the operator reads (Issue #709). A
 * re-observed identical percentage is genuinely newer information.
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
      (windowA.observedAt?.getTime() ?? null) === (windowB.observedAt?.getTime() ?? null) &&
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
   * Build the default wiring, keyed to the adapter the pipeline is configured
   * to use.
   *
   * Registration order is precedence. The Claude subscription provider goes
   * first because a vendor-reported window allowance answers the operator's
   * question better than a locally-derived dollar estimate ever can — but it
   * only answers when a `rate_limit_event` has actually been observed, so
   * local telemetry still covers the API-key path and every other adapter
   * (Issue #709).
   *
   * `claudeRateLimitStore` is null when the caller has no place to persist
   * readings; the subscription provider is then simply not registered, which
   * is the pre-#709 behaviour.
   */
  static forWorkspace(
    workspaceRoot: string,
    sessionClock: UsageSessionClock,
    claudeRateLimitStore: ClaudeRateLimitStore | null = null
  ): AdapterUsageService {
    const registry = new UsageProviderRegistry();
    if (claudeRateLimitStore !== null) {
      registry.register(
        new ClaudeRateLimitUsageProvider(claudeRateLimitStore, readClaudePlanDeclaration)
      );
    }
    registry.register(LocalTelemetryUsageProvider.forWorkspace(workspaceRoot, sessionClock));
    return new AdapterUsageService(registry, {
      resolveAdapter: () => getGlobalAdapterWithSource(workspaceRoot).adapter,
      // Same store the readings are written to, so "when did this last work"
      // and "what does it say" can never disagree (#810).
      readClaudePlanDeclaration,
      probeClaudeFeedHealth: () =>
        probeClaudeFeedHealth(claudeRateLimitStore === null ? {} : { store: claudeRateLimitStore }),
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
   * Ask each provider that claims the adapter, in precedence order, for a
   * snapshot — falling back to the unknown snapshot only once none can
   * answer.
   *
   * Three routes end in "unknown", and all three are the same answer to the
   * user: no provider claims the adapter, every provider that claims it has
   * no data, or every derivation threw. A failed read is not evidence of zero
   * usage, so it never becomes a zeroed window.
   *
   * The fall-through matters because a provider's `supports` predicate is
   * about the adapter, not about whether it has observed anything yet
   * (Issue #709). `ClaudeRateLimitUsageProvider` claims `claude` on both the
   * subscription and API-key paths, and returns `null` on the latter — which
   * has to reach `LocalTelemetryUsageProvider`'s dollar windows rather than
   * dead-ending in "unknown". A provider that throws is skipped for the same
   * reason: one broken source must not take the whole meter down.
   */
  private async derive(): Promise<UsageSnapshot> {
    const adapter = this.options.resolveAdapter();
    for (const provider of this.registry.resolveAll(adapter)) {
      try {
        const snapshot = await provider.getSnapshot(adapter);
        if (snapshot !== null) {
          return this.withClaudeFeedHealth(snapshot);
        }
      } catch (error) {
        console.warn(`[Nightgauge] usage provider ${provider.id} failed for ${adapter}:`, error);
      }
    }
    return this.withClaudeFeedHealth(unknownUsageSnapshot(adapter, new Date()));
  }

  /**
   * Attach the Claude usage feed's health to a snapshot (#810).
   *
   * Decorating here rather than inside each provider is deliberate: it is one
   * place, so every consumer of a `claude` snapshot sees the SAME verdict.
   * They used to infer the feed's state independently from `plan.kind`, which
   * is how the Dashboard panel and the status-bar tooltip could offer to enable
   * a feed the enable command called already enabled.
   *
   * Non-blocking: the probe reads two small files, and a failure leaves the
   * field undefined rather than taking the meter down with it.
   */
  private async withClaudeFeedHealth(snapshot: UsageSnapshot): Promise<UsageSnapshot> {
    if (snapshot.adapter !== "claude") {
      return snapshot;
    }
    const declared = this.options.readClaudePlanDeclaration?.() ?? "not-declared";
    const decorated = { ...snapshot, claudePlanDeclared: declared !== "not-declared" };

    const probe = this.options.probeClaudeFeedHealth;
    if (probe === undefined) {
      return decorated;
    }
    try {
      return { ...decorated, claudeFeedHealth: await probe() };
    } catch {
      return decorated;
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
