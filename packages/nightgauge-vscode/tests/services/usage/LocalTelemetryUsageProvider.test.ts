/**
 * Tests for LocalTelemetryUsageProvider (Issue #658).
 *
 * The provider turns raw history records into dollar windows, so the things
 * worth pinning are the ones a reader cannot verify by eye: which stages get
 * attributed to the adapter, where each window's boundary falls, what the
 * configured budget maps onto, and how a stage whose cost could not be priced
 * degrades the window's confidence.
 *
 * System time is frozen at 2026-08-17 10:00 local so the calendar boundaries
 * (start of day, start of month, next month) are exact rather than
 * approximately right.
 */

import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import {
  LocalTelemetryUsageProvider,
  LOCAL_TELEMETRY_METERED_ADAPTERS,
  stageCostConfidence,
  type UsageHistorySource,
  type UsageSessionClock,
} from "../../../src/services/usage/LocalTelemetryUsageProvider";
import type { ExecutionAdapter } from "../../../src/config/schema";
import {
  ExecutionHistoryRunRecordV2Schema,
  type ExecutionHistoryRecord,
  type HistoryStageTokenUsage,
} from "../../../src/schemas/executionHistory";
import { setMockUIConfig, resetMockConfigBridge } from "../../setup";

/** 2026-08-17 10:00 local — a Monday mid-month, mid-day. */
const NOW = new Date(2026, 7, 17, 10, 0, 0);

type StageUsageFixture = Partial<HistoryStageTokenUsage> & { cost_usd: number };

/** A schema-valid v2 run record carrying the given per-stage token entries. */
function runRecord(
  startedAt: Date,
  perStage: Record<string, StageUsageFixture>
): ExecutionHistoryRecord {
  const tokens: Record<string, unknown> = {};
  for (const [stage, usage] of Object.entries(perStage)) {
    tokens[stage] = { input: 0, output: 0, cache_read: 0, cache_creation: 0, ...usage };
  }
  return ExecutionHistoryRunRecordV2Schema.parse({
    schema_version: "2",
    record_type: "run",
    issue_number: 658,
    title: "fixture",
    branch: "feat/fixture",
    base_branch: "main",
    execution_mode: "automatic",
    started_at: startedAt.toISOString(),
    completed_at: new Date(startedAt.getTime() + 60_000).toISOString(),
    total_duration_ms: 60_000,
    outcome: "complete",
    stages: {},
    tokens: {
      total_input: 0,
      total_output: 0,
      total_cache_read: 0,
      total_cache_creation: 0,
      estimated_cost_usd: 0,
      per_stage: tokens,
    },
    files: { read_count: 0, written_count: 0 },
    routing: { complexity_score: 1, path: "standard", skip_stages: [] },
    recorded_at: startedAt.toISOString(),
  });
}

/** Records the range asked for and replays a fixed corpus. */
class FakeHistorySource implements UsageHistorySource {
  calls: Array<{ startDate: Date; endDate: Date }> = [];

  constructor(private readonly records: ExecutionHistoryRecord[]) {}

  async readDateRange(startDate: Date, endDate: Date): Promise<ExecutionHistoryRecord[]> {
    this.calls.push({ startDate, endDate });
    return this.records;
  }
}

const sessionClock = (start: Date): UsageSessionClock => ({ getSessionStartTime: () => start });

/** Session started at 09:00 today — inside the day, well inside the month. */
const SESSION_START = new Date(2026, 7, 17, 9, 0, 0);

function provider(records: ExecutionHistoryRecord[], sessionStart = SESSION_START) {
  const source = new FakeHistorySource(records);
  return {
    source,
    provider: new LocalTelemetryUsageProvider(source, sessionClock(sessionStart)),
  };
}

beforeEach(() => {
  resetMockConfigBridge();
  vi.useFakeTimers();
  vi.setSystemTime(NOW);
});

afterEach(() => {
  vi.useRealTimers();
  resetMockConfigBridge();
});

describe("supports()", () => {
  it.each(LOCAL_TELEMETRY_METERED_ADAPTERS)("claims the dollar-metered adapter %s", (adapter) => {
    expect(provider([]).provider.supports(adapter)).toBe(true);
  });

  // Local inference has no dollar meter and a flat seat is metered in
  // requests, so a dollar window for either would be an invented number.
  it.each<ExecutionAdapter>(["lm-studio", "ollama", "copilot"])(
    "declines %s, which is not metered in dollars",
    (adapter) => {
      expect(provider([]).provider.supports(adapter)).toBe(false);
    }
  );

  it("returns no snapshot for an adapter it does not claim", async () => {
    const { provider: p, source } = provider([
      runRecord(NOW, { "feature-dev": { cost_usd: 3, adapter: "ollama" } }),
    ]);

    await expect(p.getSnapshot("ollama")).resolves.toBeNull();
    // It must not even read history for an adapter it cannot describe.
    expect(source.calls).toHaveLength(0);
  });
});

describe("pay-per-token derivation", () => {
  it("buckets attributed cost into session, daily and monthly windows", async () => {
    const { provider: p } = provider([
      // Last month — outside every window, but inside the read horizon only
      // by padding; must not be counted.
      runRecord(new Date(2026, 6, 30, 12, 0), {
        "feature-dev": { cost_usd: 100, adapter: "claude" },
      }),
      // Earlier this month.
      runRecord(new Date(2026, 7, 3, 12, 0), {
        "feature-dev": { cost_usd: 7, adapter: "claude" },
      }),
      // Earlier today, before the session started.
      runRecord(new Date(2026, 7, 17, 2, 0), {
        "feature-dev": { cost_usd: 2, adapter: "claude" },
      }),
      // Inside the session.
      runRecord(new Date(2026, 7, 17, 9, 30), {
        "feature-dev": { cost_usd: 0.5, adapter: "claude" },
        "feature-validate": { cost_usd: 0.25, adapter: "claude" },
      }),
    ]);

    const snapshot = await p.getSnapshot("claude");

    expect(snapshot).not.toBeNull();
    expect(snapshot!.adapter).toBe("claude");
    expect(snapshot!.plan.kind).toBe("pay-per-token");
    expect(snapshot!.capturedAt).toEqual(NOW);
    expect(snapshot!.windows.map((w) => [w.id, w.scope, w.unit, w.used])).toEqual([
      ["local-telemetry:session", "session", "usd", 0.75],
      ["local-telemetry:daily", "daily", "usd", 2.75],
      ["local-telemetry:monthly", "monthly", "usd", 9.75],
    ]);
  });

  it("resets daily at the next local midnight and monthly on the 1st; a session never resets", async () => {
    const { provider: p } = provider([
      runRecord(NOW, { "feature-dev": { cost_usd: 1, adapter: "claude" } }),
    ]);

    const snapshot = await p.getSnapshot("claude");

    expect(snapshot!.windows.map((w) => w.resetsAt)).toEqual([
      null,
      new Date(2026, 7, 18, 0, 0, 0),
      new Date(2026, 8, 1, 0, 0, 0),
    ]);
  });

  it("reads back to the earliest window boundary with a day of padding", async () => {
    const { provider: p, source } = provider([
      runRecord(NOW, { "feature-dev": { cost_usd: 1, adapter: "claude" } }),
    ]);

    await p.getSnapshot("claude");

    // Month start (Aug 1 00:00 local) is the earliest boundary; padding puts
    // the read one day before it and one day after now.
    expect(source.calls).toHaveLength(1);
    expect(source.calls[0].startDate).toEqual(new Date(2026, 6, 31, 0, 0, 0));
    expect(source.calls[0].endDate.getTime()).toBe(NOW.getTime() + 24 * 60 * 60 * 1000);
  });

  it("reads back past the month when the session started before it", async () => {
    const sessionStart = new Date(2026, 6, 20, 8, 0, 0);
    const { provider: p, source } = provider(
      [runRecord(NOW, { "feature-dev": { cost_usd: 1, adapter: "claude" } })],
      sessionStart
    );

    await p.getSnapshot("claude");

    expect(source.calls[0].startDate).toEqual(new Date(2026, 6, 19, 8, 0, 0));
  });
});

describe("adapter attribution", () => {
  it("counts only stages whose adapter field names the adapter", async () => {
    const { provider: p } = provider([
      runRecord(new Date(2026, 7, 17, 9, 30), {
        "feature-dev": { cost_usd: 1, adapter: "claude" },
        "feature-validate": { cost_usd: 8, adapter: "codex" },
        // Pre-#3224 record: adapter-unknown. Crediting it to claude would be
        // a guess, so it contributes nothing.
        "pr-create": { cost_usd: 4 },
      }),
    ]);

    const snapshot = await p.getSnapshot("claude");

    expect(snapshot!.windows.map((w) => w.used)).toEqual([1, 1, 1]);
  });

  it("returns no snapshot when nothing in the horizon is attributed to the adapter", async () => {
    const { provider: p } = provider([
      runRecord(new Date(2026, 7, 17, 9, 30), {
        "feature-dev": { cost_usd: 8, adapter: "codex" },
        "pr-create": { cost_usd: 4 },
      }),
    ]);

    // Not a $0 snapshot: a fresh install and a quiet month are indistinguishable
    // from here, and a zeroed bar would claim to know which.
    await expect(p.getSnapshot("claude")).resolves.toBeNull();
  });

  it("reports a measured zero for a window the adapter simply did not run in", async () => {
    const { provider: p } = provider([
      // Earlier this month, but not today and not in this session.
      runRecord(new Date(2026, 7, 4, 9, 0), {
        "feature-dev": { cost_usd: 6, adapter: "claude" },
      }),
    ]);

    const snapshot = await p.getSnapshot("claude");

    expect(snapshot!.windows.map((w) => [w.used, w.confidence])).toEqual([
      [0, "measured"],
      [0, "measured"],
      [6, "measured"],
    ]);
  });

  it("drops records whose started_at cannot be parsed rather than zeroing them into a window", async () => {
    const unparseable = () => {
      const record = runRecord(NOW, { "feature-dev": { cost_usd: 5, adapter: "claude" } });
      (record as { started_at: string }).started_at = "not-a-date";
      return record;
    };
    const good = runRecord(NOW, { "feature-dev": { cost_usd: 2, adapter: "claude" } });

    const mixed = await provider([unparseable(), good]).provider.getSnapshot("claude");
    expect(mixed!.windows[2].used).toBe(2);

    // A record we cannot place on the calendar is not evidence of $0 spend.
    // Counting it as an event would produce three all-zero windows, which is
    // the zeroed bar #658 forbids — the honest answer is no snapshot.
    const onlyBad = await provider([unparseable()]).provider.getSnapshot("claude");
    expect(onlyBad).toBeNull();
  });
});

describe("budget-to-limit mapping", () => {
  it("maps the configured monthly budget onto the monthly window's limit", async () => {
    setMockUIConfig({ limits: { monthly_budget_usd: 50 } });
    const { provider: p } = provider([
      runRecord(new Date(2026, 7, 5, 9, 0), {
        "feature-dev": { cost_usd: 12.5, adapter: "claude" },
      }),
    ]);

    const snapshot = await p.getSnapshot("claude");

    expect(snapshot!.windows.map((w) => w.limit)).toEqual([null, null, 50]);
    expect(snapshot!.windows[2].used).toBe(12.5);
  });

  it("leaves the limit null and still reports absolute usage when no budget is set", async () => {
    setMockUIConfig({ limits: { monthly_budget_usd: 0 } });
    const { provider: p } = provider([
      runRecord(new Date(2026, 7, 5, 9, 0), {
        "feature-dev": { cost_usd: 12.5, adapter: "claude" },
      }),
    ]);

    const snapshot = await p.getSnapshot("claude");

    // 0 means "not configured", not "a ceiling of zero" — a limit of 0 would
    // render as permanently over budget.
    expect(snapshot!.windows[2].limit).toBeNull();
    expect(snapshot!.windows[2].used).toBe(12.5);
  });
});

describe("confidence", () => {
  it("maps a stage's pricing provenance onto its confidence", () => {
    const base = { input: 0, output: 0, cache_read: 0, cache_creation: 0 };
    expect(stageCostConfidence({ ...base, cost_usd: 1, cost_source: "native" })).toBe("measured");
    expect(stageCostConfidence({ ...base, cost_usd: 1, cost_source: "computed" })).toBe(
      "estimated"
    );
    expect(stageCostConfidence({ ...base, cost_usd: 0, cost_source: "unknown" })).toBe("unknown");
    // The Go writer's authoritative "this zero is a placeholder" flag wins
    // over any cost_source label.
    expect(
      stageCostConfidence({ ...base, cost_usd: 0, cost_source: "native", cost_unstamped: true })
    ).toBe("unknown");
    // Absent on records whose writer omits the field; the reader has already
    // backfilled "native" for every priced stage, so what is left is a real zero.
    expect(stageCostConfidence({ ...base, cost_usd: 0 })).toBe("measured");
  });

  it("degrades a window to its weakest contributing stage", async () => {
    const { provider: p } = provider([
      // Session + day + month: one rate-card-priced stage alongside a native one.
      runRecord(new Date(2026, 7, 17, 9, 30), {
        "feature-dev": { cost_usd: 1, adapter: "claude", cost_source: "native" },
        "feature-validate": { cost_usd: 2, adapter: "claude", cost_source: "computed" },
      }),
      // Month only: an unpriceable stage, which taints the monthly window.
      runRecord(new Date(2026, 7, 2, 9, 30), {
        "feature-dev": { cost_usd: 0, adapter: "claude", cost_unstamped: true },
      }),
    ]);

    const snapshot = await p.getSnapshot("claude");

    expect(snapshot!.windows.map((w) => w.confidence)).toEqual([
      "estimated",
      "estimated",
      "unknown",
    ]);
  });
});
