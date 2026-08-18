/**
 * Tests for AdapterUsageService and UsageProviderRegistry (Issue #658).
 *
 * The service is the single source #659 and #661 read, so what is pinned here
 * is the contract those consumers depend on: which provider answers for the
 * configured adapter, that "we cannot say" always arrives as the unknown
 * snapshot rather than a zeroed one, when the cache is served versus
 * re-derived, and that the change event fires on change and not on the clock.
 *
 * The suite-wide vscode mock stubs EventEmitter with `vi.fn()`s that never
 * dispatch, so this file supplies a real one — otherwise every event
 * assertion would be testing the stub.
 */

import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import * as fs from "node:fs/promises";
import * as os from "node:os";
import * as path from "node:path";

vi.mock("vscode", () => ({
  EventEmitter: class {
    private handlers: Array<(value: unknown) => void> = [];
    event = (cb: (value: unknown) => void) => {
      this.handlers.push(cb);
      return { dispose: () => {} };
    };
    fire(value: unknown) {
      for (const handler of this.handlers) handler(value);
    }
    dispose() {
      this.handlers = [];
    }
  },
  Disposable: class {
    dispose() {}
  },
  workspace: {
    workspaceFolders: undefined,
    getConfiguration: vi.fn(() => ({ get: vi.fn() })),
  },
  window: {
    createOutputChannel: vi.fn(() => ({ appendLine: vi.fn(), dispose: vi.fn() })),
  },
}));

import {
  AdapterUsageService,
  UsageProviderRegistry,
  usageSnapshotsEquivalent,
} from "../../../src/services/usage/AdapterUsageService";
import { LocalTelemetryUsageProvider } from "../../../src/services/usage/LocalTelemetryUsageProvider";
import type { UsageProvider, UsageSnapshot } from "../../../src/services/usage/types";
import type { ExecutionAdapter } from "../../../src/config/schema";
import { setMockUIConfig, resetMockConfigBridge } from "../../setup";

const NOW = new Date(2026, 7, 17, 10, 0, 0);

function snapshotFor(adapter: ExecutionAdapter, usedUsd: number): UsageSnapshot {
  return {
    adapter,
    plan: { kind: "pay-per-token" },
    capturedAt: new Date(),
    windows: [
      {
        id: "fake:monthly",
        label: "This month",
        scope: "monthly",
        used: usedUsd,
        limit: null,
        unit: "usd",
        resetsAt: null,
        confidence: "measured",
      },
    ],
  };
}

/** A provider whose claimed adapters and answer are set per test. */
class FakeProvider implements UsageProvider {
  calls: ExecutionAdapter[] = [];
  result: UsageSnapshot | null = null;
  error: Error | null = null;

  constructor(
    readonly id: string,
    private readonly claimed: readonly ExecutionAdapter[]
  ) {}

  supports(adapter: ExecutionAdapter): boolean {
    return this.claimed.includes(adapter);
  }

  async getSnapshot(adapter: ExecutionAdapter): Promise<UsageSnapshot | null> {
    this.calls.push(adapter);
    if (this.error) throw this.error;
    return this.result;
  }
}

function serviceWith(provider: UsageProvider, adapter: ExecutionAdapter): AdapterUsageService {
  const registry = new UsageProviderRegistry().register(provider);
  return new AdapterUsageService(registry, { resolveAdapter: () => adapter });
}

beforeEach(() => {
  resetMockConfigBridge();
  vi.useFakeTimers();
  vi.setSystemTime(NOW);
});

afterEach(() => {
  vi.useRealTimers();
  resetMockConfigBridge();
  vi.restoreAllMocks();
});

describe("UsageProviderRegistry", () => {
  it("returns every provider that claims the adapter, in registration order", () => {
    const specific = new FakeProvider("specific", ["claude"]);
    const general = new FakeProvider("general", ["claude", "codex"]);
    const registry = new UsageProviderRegistry().register(specific).register(general);

    // Registration order is precedence, and a claim by the first provider must
    // not hide the second: the first may have nothing to say today (#709).
    expect(registry.resolveAll("claude").map((p) => p.id)).toEqual(["specific", "general"]);
    expect(registry.resolveAll("codex").map((p) => p.id)).toEqual(["general"]);
    expect(registry.resolveAll("ollama")).toEqual([]);
  });
});

describe("unknown fallback", () => {
  it("reports plan.kind unknown with no windows when no provider claims the adapter", async () => {
    // The real provider, the real reason: ollama has no dollar meter.
    const local = new LocalTelemetryUsageProvider(
      { readDateRange: async () => [] },
      { getSessionStartTime: () => NOW }
    );
    const service = serviceWith(local, "ollama");

    const snapshot = await service.getSnapshot();

    expect(snapshot).toEqual({
      adapter: "ollama",
      plan: { kind: "unknown" },
      capturedAt: NOW,
      windows: [],
    });
  });

  it("reports unknown when the provider claims the adapter but has nothing to say", async () => {
    const provider = new FakeProvider("fake", ["claude"]);
    provider.result = null;
    const service = serviceWith(provider, "claude");

    const snapshot = await service.getSnapshot();

    expect(snapshot.plan.kind).toBe("unknown");
    expect(snapshot.windows).toEqual([]);
  });

  it("reports unknown — not zero — when the provider throws", async () => {
    const warn = vi.spyOn(console, "warn").mockImplementation(() => {});
    const provider = new FakeProvider("fake", ["claude"]);
    provider.error = new Error("history read failed");
    const service = serviceWith(provider, "claude");

    const snapshot = await service.getSnapshot();

    expect(snapshot.plan.kind).toBe("unknown");
    expect(snapshot.windows).toEqual([]);
    expect(warn).toHaveBeenCalled();
  });
});

describe("provider fall-through (#709)", () => {
  function serviceWithAll(
    providers: readonly UsageProvider[],
    adapter: ExecutionAdapter
  ): AdapterUsageService {
    const registry = new UsageProviderRegistry();
    for (const provider of providers) registry.register(provider);
    return new AdapterUsageService(registry, { resolveAdapter: () => adapter });
  }

  it("asks the next claiming provider when the first has nothing to say", async () => {
    // The production shape: the Claude subscription provider claims `claude`
    // whether or not a rate_limit_event has been observed, so an API-key user
    // — who never emits one — has to reach the dollar windows behind it.
    const subscription = new FakeProvider("claude-rate-limit", ["claude"]);
    subscription.result = null;
    const local = new FakeProvider("local-telemetry", ["claude"]);
    local.result = snapshotFor("claude", 4.12);

    const snapshot = await serviceWithAll([subscription, local], "claude").getSnapshot();

    expect(subscription.calls).toEqual(["claude"]);
    expect(snapshot.plan.kind).toBe("pay-per-token");
    expect(snapshot.windows[0].used).toBe(4.12);
  });

  it("stops at the first provider that answers", async () => {
    const subscription = new FakeProvider("claude-rate-limit", ["claude"]);
    subscription.result = { ...snapshotFor("claude", 0), plan: { kind: "subscription-window" } };
    const local = new FakeProvider("local-telemetry", ["claude"]);
    local.result = snapshotFor("claude", 4.12);

    const snapshot = await serviceWithAll([subscription, local], "claude").getSnapshot();

    expect(snapshot.plan.kind).toBe("subscription-window");
    // Precedence is only real if the loser is never consulted — otherwise the
    // dollar figure would be derived (and its history read) on every refresh.
    expect(local.calls).toEqual([]);
  });

  it("falls through a provider that throws rather than taking the meter down", async () => {
    const warn = vi.spyOn(console, "warn").mockImplementation(() => {});
    const subscription = new FakeProvider("claude-rate-limit", ["claude"]);
    subscription.error = new Error("unreadable store");
    const local = new FakeProvider("local-telemetry", ["claude"]);
    local.result = snapshotFor("claude", 4.12);

    const snapshot = await serviceWithAll([subscription, local], "claude").getSnapshot();

    expect(snapshot.plan.kind).toBe("pay-per-token");
    expect(warn).toHaveBeenCalled();
  });
});

describe("derivation and caching", () => {
  it("serves the resolved provider's snapshot for the configured adapter", async () => {
    const provider = new FakeProvider("fake", ["codex"]);
    provider.result = snapshotFor("codex", 4.5);
    const service = serviceWith(provider, "codex");

    const snapshot = await service.getSnapshot();

    expect(provider.calls).toEqual(["codex"]);
    expect(snapshot.adapter).toBe("codex");
    expect(snapshot.windows[0].used).toBe(4.5);
    expect(service.getCachedSnapshot()).toBe(snapshot);
  });

  it("has no cached snapshot before the first derivation", () => {
    const provider = new FakeProvider("fake", ["claude"]);
    expect(serviceWith(provider, "claude").getCachedSnapshot()).toBeNull();
  });

  it("serves the cache while fresh and re-derives once the interval has passed", async () => {
    setMockUIConfig({ limits: { polling_interval_seconds: 60 } });
    const provider = new FakeProvider("fake", ["claude"]);
    provider.result = snapshotFor("claude", 1);
    const service = serviceWith(provider, "claude");

    await service.getSnapshot();
    expect(service.isStale()).toBe(false);

    vi.setSystemTime(new Date(NOW.getTime() + 59_000));
    await service.getSnapshot();
    expect(provider.calls).toHaveLength(1);
    expect(service.isStale()).toBe(false);

    vi.setSystemTime(new Date(NOW.getTime() + 60_000));
    expect(service.isStale()).toBe(true);
    await service.getSnapshot();
    expect(provider.calls).toHaveLength(2);
  });

  it("reports a missing snapshot as stale", () => {
    const service = serviceWith(new FakeProvider("fake", ["claude"]), "claude");
    expect(service.isStale()).toBe(true);
  });

  it("shares one derivation between concurrent refreshes", async () => {
    const provider = new FakeProvider("fake", ["claude"]);
    provider.result = snapshotFor("claude", 1);
    const service = serviceWith(provider, "claude");

    const [a, b] = await Promise.all([service.refresh(), service.refresh()]);

    expect(provider.calls).toHaveLength(1);
    expect(a).toBe(b);
  });
});

describe("refresh loop", () => {
  it("derives immediately on initialize and again every interval until disposed", async () => {
    setMockUIConfig({ limits: { polling_interval_seconds: 60 } });
    const provider = new FakeProvider("fake", ["claude"]);
    provider.result = snapshotFor("claude", 1);
    const service = serviceWith(provider, "claude");

    service.initialize();
    await vi.advanceTimersByTimeAsync(0);
    expect(provider.calls).toHaveLength(1);

    await vi.advanceTimersByTimeAsync(60_000);
    expect(provider.calls).toHaveLength(2);

    service.dispose();
    await vi.advanceTimersByTimeAsync(180_000);
    expect(provider.calls).toHaveLength(2);
  });
});

describe("change event", () => {
  it("fires when the derived usage changed and stays quiet when it did not", async () => {
    const provider = new FakeProvider("fake", ["claude"]);
    provider.result = snapshotFor("claude", 1);
    const service = serviceWith(provider, "claude");
    const seen: UsageSnapshot[] = [];
    service.onDidChangeUsage((snapshot) => seen.push(snapshot));

    await service.refresh();
    expect(seen).toHaveLength(1);

    // Same usage, later capture — a consumer re-rendering on this event must
    // not be woken by the clock alone.
    vi.setSystemTime(new Date(NOW.getTime() + 60_000));
    provider.result = snapshotFor("claude", 1);
    await service.refresh();
    expect(seen).toHaveLength(1);

    provider.result = snapshotFor("claude", 2);
    await service.refresh();
    expect(seen).toHaveLength(2);
    expect(seen[1].windows[0].used).toBe(2);
  });
});

describe("usageSnapshotsEquivalent", () => {
  it("ignores capturedAt and compares everything else", () => {
    const a = snapshotFor("claude", 1);
    const b = { ...snapshotFor("claude", 1), capturedAt: new Date(NOW.getTime() + 5_000) };

    expect(usageSnapshotsEquivalent(a, b)).toBe(true);
    expect(usageSnapshotsEquivalent(null, b)).toBe(false);
    expect(usageSnapshotsEquivalent({ ...a, adapter: "codex" }, b)).toBe(false);
    expect(usageSnapshotsEquivalent({ ...a, plan: { kind: "unknown" } }, b)).toBe(false);
    expect(usageSnapshotsEquivalent({ ...a, windows: [] }, b)).toBe(false);
    expect(usageSnapshotsEquivalent({ ...a, windows: [{ ...a.windows[0], limit: 50 }] }, b)).toBe(
      false
    );
    expect(
      usageSnapshotsEquivalent({ ...a, windows: [{ ...a.windows[0], confidence: "unknown" }] }, b)
    ).toBe(false);
    expect(
      usageSnapshotsEquivalent({ ...a, windows: [{ ...a.windows[0], resetsAt: NOW }] }, b)
    ).toBe(false);
  });
});

describe("forWorkspace — the default wiring #659 will call", () => {
  // Real timers and a real temp workspace: this is the one test that walks the
  // whole path #659 uses (adapter resolver → registry → local telemetry
  // provider → ExecutionHistoryReader → the workspace's JSONL files). Every
  // other test injects around one of those seams.
  it("derives usage from the workspace's own history directory", async () => {
    vi.useRealTimers();
    const previousAdapter = process.env.NIGHTGAUGE_UI_CORE_ADAPTER;
    process.env.NIGHTGAUGE_UI_CORE_ADAPTER = "claude";
    const workspaceRoot = await fs.mkdtemp(path.join(os.tmpdir(), "ng-usage-"));

    try {
      const now = new Date();
      const historyDir = path.join(workspaceRoot, ".nightgauge", "pipeline", "history");
      await fs.mkdir(historyDir, { recursive: true });
      const record = {
        schema_version: "2",
        record_type: "run",
        issue_number: 658,
        title: "fixture",
        branch: "feat/fixture",
        base_branch: "main",
        execution_mode: "automatic",
        started_at: now.toISOString(),
        completed_at: now.toISOString(),
        total_duration_ms: 1000,
        outcome: "complete",
        stages: {},
        tokens: {
          total_input: 10,
          total_output: 10,
          total_cache_read: 0,
          total_cache_creation: 0,
          estimated_cost_usd: 1.25,
          per_stage: {
            "feature-dev": {
              input: 10,
              output: 10,
              cache_read: 0,
              cache_creation: 0,
              cost_usd: 1.25,
              adapter: "claude",
            },
          },
        },
        files: { read_count: 0, written_count: 0 },
        routing: { complexity_score: 1, path: "standard", skip_stages: [] },
        recorded_at: now.toISOString(),
      };
      const filename = now.toISOString().split("T")[0] + ".jsonl";
      await fs.writeFile(path.join(historyDir, filename), JSON.stringify(record) + "\n");

      const service = AdapterUsageService.forWorkspace(workspaceRoot, {
        getSessionStartTime: () => new Date(now.getTime() - 60 * 60 * 1000),
      });

      const snapshot = await service.getSnapshot();
      service.dispose();

      expect(snapshot.adapter).toBe("claude");
      expect(snapshot.plan.kind).toBe("pay-per-token");
      expect(snapshot.windows.map((w) => [w.scope, w.used])).toEqual([
        ["session", 1.25],
        ["daily", 1.25],
        ["monthly", 1.25],
      ]);
    } finally {
      if (previousAdapter === undefined) {
        delete process.env.NIGHTGAUGE_UI_CORE_ADAPTER;
      } else {
        process.env.NIGHTGAUGE_UI_CORE_ADAPTER = previousAdapter;
      }
      await fs.rm(workspaceRoot, { recursive: true, force: true });
    }
  });
});
