/**
 * usagePanel.test.ts
 *
 * Issue #661 — panel state derivation per plan kind, the unknown /
 * no-provider empty state, and burn-rate computation.
 *
 * @see docs/decisions/018-adapter-usage-quota-model.md
 */

import { describe, it, expect } from "vitest";
import type { PipelineRunSummary } from "../../../src/views/dashboard/DashboardState";
import type { UsageSnapshot, UsageWindow } from "../../../src/services/usage/types";
import { unknownUsageSnapshot } from "../../../src/services/usage/types";
import {
  BURN_RATE_LOOKBACK_DAYS,
  RECENT_RUN_STRIP_SIZE,
  buildUsagePanelState,
  computeUsageBurnRate,
  groupByModelFamily,
  selectActiveWindow,
  toWindowView,
} from "../../../src/views/dashboard/usagePanel";

const NOW = new Date("2026-08-18T12:00:00.000Z");
const HOUR_MS = 60 * 60 * 1000;
const DAY_MS = 24 * HOUR_MS;

function makeWindow(overrides: Partial<UsageWindow> = {}): UsageWindow {
  return {
    id: "local-telemetry:monthly",
    label: "This month",
    scope: "monthly",
    used: 20,
    limit: 100,
    unit: "usd",
    resetsAt: new Date("2026-09-01T00:00:00.000Z"),
    confidence: "measured",
    ...overrides,
  };
}

function makeSnapshot(windows: UsageWindow[]): UsageSnapshot {
  return {
    adapter: "claude",
    plan: { kind: "pay-per-token" },
    capturedAt: NOW,
    windows,
  };
}

function makeRun(overrides: Partial<PipelineRunSummary> = {}): PipelineRunSummary {
  return {
    issueNumber: 1,
    title: "A run",
    branch: "feat/x",
    startedAt: new Date(NOW.getTime() - DAY_MS),
    status: "complete",
    stages: [],
    toolCalls: [],
    usage: {
      inputTokens: 100,
      outputTokens: 200,
      cacheReadTokens: 300,
      cacheCreationTokens: 400,
      costUsd: 1,
      durationMs: 1000,
      stageCount: 1,
    },
    ...overrides,
  };
}

describe("buildUsagePanelState — plan kinds (Issue #661)", () => {
  it("returns null when there is no snapshot at all — no service, nothing to name", () => {
    expect(buildUsagePanelState(null, [makeRun()], NOW)).toBeNull();
  });

  it("renders a pay-per-token snapshot's every window, quota figures untouched", () => {
    const snapshot = makeSnapshot([
      makeWindow({ id: "s", scope: "session", label: "This session", limit: null, used: 3 }),
      makeWindow({ id: "d", scope: "daily", label: "Today", limit: null, used: 7 }),
      makeWindow({ id: "m", scope: "monthly", label: "This month", limit: 200, used: 50 }),
    ]);

    const state = buildUsagePanelState(snapshot, [], NOW)!;

    expect(state.planKind).toBe("pay-per-token");
    expect(state.adapter).toBe("claude");
    expect(state.capturedAt).toBe(NOW);
    expect(state.windows.map((w) => w.id)).toEqual(["s", "d", "m"]);
    expect(state.windows.map((w) => w.used)).toEqual([3, 7, 50]);
    expect(state.windows.map((w) => w.limit)).toEqual([null, null, 200]);
  });

  it("renders the unknown plan as an empty state that still names the adapter", () => {
    const state = buildUsagePanelState(unknownUsageSnapshot("ollama", NOW), [makeRun()], NOW)!;

    expect(state.planKind).toBe("unknown");
    expect(state.adapter).toBe("ollama");
    expect(state.windows).toEqual([]);
    expect(state.familyGroups).toEqual([]);
    // An adapter-blind run strip under "we cannot describe this adapter"
    // would read as that adapter's spend.
    expect(state.recentRuns).toEqual([]);
    expect(state.burnRate).toBeNull();
    expect(state.burnRateUnavailableReason).toBe("no-window");
  });
});

describe("toWindowView — bar, percentage, confidence (Issue #661)", () => {
  it("computes a fill percentage only when a real ceiling exists", () => {
    const view = toWindowView(makeWindow({ used: 25, limit: 200 }));
    expect(view.pct).toBeCloseTo(12.5);
    expect(view.barPct).toBeCloseTo(12.5);
    expect(view.severity).toBe("ok");
  });

  it("reports no percentage and no bar when the limit is null", () => {
    const view = toWindowView(makeWindow({ used: 4.12, limit: null }));
    expect(view.pct).toBeNull();
    expect(view.barPct).toBeNull();
    expect(view.severity).toBe("unmeasured");
  });

  it("clamps only the bar width past the ceiling — the percentage stays honest", () => {
    const view = toWindowView(makeWindow({ used: 250, limit: 100 }));
    expect(view.pct).toBeCloseTo(250);
    expect(view.barPct).toBe(100);
    expect(view.severity).toBe("critical");
  });

  it("crosses to warning at 80% and critical at 90%", () => {
    expect(toWindowView(makeWindow({ used: 79, limit: 100 })).severity).toBe("ok");
    expect(toWindowView(makeWindow({ used: 80, limit: 100 })).severity).toBe("warning");
    expect(toWindowView(makeWindow({ used: 90, limit: 100 })).severity).toBe("critical");
  });

  it("labels an unknown-confidence window unknown, whatever its fill would be", () => {
    const view = toWindowView(makeWindow({ used: 10, limit: 100, confidence: "unknown" }));
    expect(view.severity).toBe("unknown");
    expect(view.usedIsFloor).toBe(true);
    // The floor is still carried — it is a floor, not an absence.
    expect(view.used).toBe(10);
  });

  it("keeps unknown confidence at zero usage from reading as a measured zero", () => {
    const view = toWindowView(makeWindow({ used: 0, limit: 100, confidence: "unknown" }));
    // Not "ok at 0%" — the renderer keys off this to draw an indeterminate
    // bar rather than one that is pixel-identical to "you have spent nothing".
    expect(view.severity).toBe("unknown");
    expect(view.usedIsFloor).toBe(true);
    expect(view.barPct).toBe(0);
  });

  it("treats an estimated window as a real measurement for severity", () => {
    const view = toWindowView(makeWindow({ used: 10, limit: 100, confidence: "estimated" }));
    expect(view.severity).toBe("ok");
    expect(view.usedIsFloor).toBe(false);
  });
});

describe("groupByModelFamily (Issue #661)", () => {
  it("returns nothing when no window carries a family — the section is omitted", () => {
    expect(groupByModelFamily([makeWindow(), makeWindow({ id: "b" })])).toEqual([]);
  });

  it("groups family windows by family, in first-appearance order", () => {
    const groups = groupByModelFamily([
      makeWindow({ id: "opus:session", modelFamily: "opus" }),
      makeWindow({ id: "sonnet:session", modelFamily: "sonnet" }),
      makeWindow({ id: "opus:weekly", modelFamily: "opus" }),
      makeWindow({ id: "overall" }),
    ]);

    expect(groups.map((g) => g.modelFamily)).toEqual(["opus", "sonnet"]);
    expect(groups[0].windows.map((w) => w.id)).toEqual(["opus:session", "opus:weekly"]);
    expect(groups[1].windows.map((w) => w.id)).toEqual(["sonnet:session"]);
  });

  it("keeps family windows out of the overall window list", () => {
    const snapshot = makeSnapshot([
      makeWindow({ id: "overall" }),
      makeWindow({ id: "opus", modelFamily: "opus" }),
    ]);
    const state = buildUsagePanelState(snapshot, [], NOW)!;

    expect(state.windows.map((w) => w.id)).toEqual(["overall"]);
    expect(state.familyGroups.map((g) => g.modelFamily)).toEqual(["opus"]);
  });
});

describe("selectActiveWindow (Issue #661)", () => {
  it("prefers the first window that has a real ceiling", () => {
    const active = selectActiveWindow([
      makeWindow({ id: "session", limit: null }),
      makeWindow({ id: "monthly", limit: 100 }),
    ]);
    expect(active?.id).toBe("monthly");
  });

  it("falls back to the first window when nothing has a ceiling", () => {
    const active = selectActiveWindow([
      makeWindow({ id: "session", limit: null }),
      makeWindow({ id: "daily", limit: null }),
    ]);
    expect(active?.id).toBe("session");
  });

  it("never picks a per-family window while an overall window exists", () => {
    const active = selectActiveWindow([
      makeWindow({ id: "overall", limit: null }),
      makeWindow({ id: "opus", limit: 100, modelFamily: "opus" }),
    ]);
    expect(active?.id).toBe("overall");
  });

  it("returns null for an empty window list", () => {
    expect(selectActiveWindow([])).toBeNull();
  });
});

describe("computeUsageBurnRate (Issue #661)", () => {
  /** Two runs, 24h apart, $2 then $4 — $4 of spend across one day. */
  const twoDayRuns = (): PipelineRunSummary[] => [
    makeRun({
      issueNumber: 1,
      startedAt: new Date(NOW.getTime() - 2 * DAY_MS),
      usage: { ...makeRun().usage, costUsd: 2 },
    }),
    makeRun({
      issueNumber: 2,
      startedAt: new Date(NOW.getTime() - 1 * DAY_MS),
      usage: { ...makeRun().usage, costUsd: 4 },
    }),
  ];

  it("derives $/hour and $/day from the elapsed time between history samples", () => {
    const rate = computeUsageBurnRate(makeWindow({ used: 20, limit: 100 }), twoDayRuns(), NOW)!;

    expect(rate.sampleCount).toBe(2);
    expect(rate.usdPerDay).toBeCloseTo(4);
    expect(rate.usdPerHour).toBeCloseTo(4 / 24);
    expect(rate.lookbackDays).toBe(BURN_RATE_LOOKBACK_DAYS);
  });

  it("projects exhaustion of the remaining ceiling at that rate", () => {
    const rate = computeUsageBurnRate(makeWindow({ used: 20, limit: 100 }), twoDayRuns(), NOW)!;

    // $80 remaining at $4/day = 20 days out.
    expect(rate.hoursToExhaustion).toBeCloseTo(20 * 24);
    expect(rate.projectedExhaustionAt!.getTime()).toBeCloseTo(NOW.getTime() + 20 * DAY_MS, -3);
    expect(rate.alreadyExhausted).toBe(false);
  });

  it("reports an already-exceeded ceiling instead of a projection", () => {
    const rate = computeUsageBurnRate(makeWindow({ used: 120, limit: 100 }), twoDayRuns(), NOW)!;

    expect(rate.alreadyExhausted).toBe(true);
    expect(rate.projectedExhaustionAt).toBeNull();
    expect(rate.hoursToExhaustion).toBeNull();
  });

  it("states a rate but no exhaustion when the window has no ceiling", () => {
    const rate = computeUsageBurnRate(makeWindow({ used: 20, limit: null }), twoDayRuns(), NOW)!;

    expect(rate.usdPerDay).toBeCloseTo(4);
    expect(rate.limitUsd).toBeNull();
    expect(rate.projectedExhaustionAt).toBeNull();
  });

  it("ignores runs older than the lookback", () => {
    const runs = [
      ...twoDayRuns(),
      makeRun({
        issueNumber: 99,
        startedAt: new Date(NOW.getTime() - (BURN_RATE_LOOKBACK_DAYS + 1) * DAY_MS),
        usage: { ...makeRun().usage, costUsd: 500 },
      }),
    ];
    const rate = computeUsageBurnRate(makeWindow({ used: 20, limit: 100 }), runs, NOW)!;

    expect(rate.sampleCount).toBe(2);
    expect(rate.usdPerDay).toBeCloseTo(4);
  });

  it("says nothing at all with fewer than two runs in the lookback", () => {
    expect(computeUsageBurnRate(makeWindow(), [makeRun()], NOW)).toBeNull();
    expect(computeUsageBurnRate(makeWindow(), [], NOW)).toBeNull();
  });

  it("refuses to project a non-dollar window from a dollar history", () => {
    const percentWindow = makeWindow({ used: 40, limit: 100, unit: "percent" });
    expect(computeUsageBurnRate(percentWindow, twoDayRuns(), NOW)).toBeNull();
  });

  it("marks the projection optimistic when the window's usage is a floor", () => {
    const rate = computeUsageBurnRate(
      makeWindow({ used: 20, limit: 100, confidence: "unknown" }),
      twoDayRuns(),
      NOW
    )!;
    expect(rate.usedIsFloor).toBe(true);
  });

  it("surfaces the reason a rate is missing on the panel state", () => {
    const noHistory = buildUsagePanelState(makeSnapshot([makeWindow()]), [], NOW)!;
    expect(noHistory.burnRate).toBeNull();
    expect(noHistory.burnRateUnavailableReason).toBe("insufficient-history");

    const percentOnly = buildUsagePanelState(
      makeSnapshot([makeWindow({ unit: "percent" })]),
      twoDayRuns(),
      NOW
    )!;
    expect(percentOnly.burnRate).toBeNull();
    expect(percentOnly.burnRateUnavailableReason).toBe("non-dollar-window");

    const ok = buildUsagePanelState(makeSnapshot([makeWindow()]), twoDayRuns(), NOW)!;
    expect(ok.burnRate).not.toBeNull();
    expect(ok.burnRateUnavailableReason).toBeNull();
  });
});

describe("recent-history strip (Issue #661)", () => {
  it("shows the newest N runs with their spend and total tokens", () => {
    const runs = Array.from({ length: RECENT_RUN_STRIP_SIZE + 5 }, (_, i) =>
      makeRun({
        issueNumber: i,
        startedAt: new Date(NOW.getTime() - i * HOUR_MS),
        usage: { ...makeRun().usage, costUsd: 2 },
      })
    );

    const state = buildUsagePanelState(makeSnapshot([makeWindow()]), runs, NOW)!;

    expect(state.recentRuns).toHaveLength(RECENT_RUN_STRIP_SIZE);
    // Newest first, regardless of the order history arrived in.
    expect(state.recentRuns.map((r) => r.issueNumber)).toEqual([0, 1, 2, 3, 4, 5, 6, 7, 8, 9]);
    expect(state.recentTotals.runCount).toBe(RECENT_RUN_STRIP_SIZE);
    expect(state.recentTotals.costUsd).toBeCloseTo(2 * RECENT_RUN_STRIP_SIZE);
    // 100 + 200 + 300 + 400 tokens per run, every class counted.
    expect(state.recentTotals.tokens).toBe(1000 * RECENT_RUN_STRIP_SIZE);
  });

  it("sorts an out-of-order history newest-first rather than trusting the caller", () => {
    const older = makeRun({ issueNumber: 1, startedAt: new Date(NOW.getTime() - 5 * HOUR_MS) });
    const newer = makeRun({ issueNumber: 2, startedAt: new Date(NOW.getTime() - 1 * HOUR_MS) });

    const state = buildUsagePanelState(makeSnapshot([makeWindow()]), [older, newer], NOW)!;

    expect(state.recentRuns.map((r) => r.issueNumber)).toEqual([2, 1]);
  });

  it("is empty, with zero totals, on a workspace with no runs", () => {
    const state = buildUsagePanelState(makeSnapshot([makeWindow()]), [], NOW)!;
    expect(state.recentRuns).toEqual([]);
    expect(state.recentTotals).toEqual({ costUsd: 0, tokens: 0, runCount: 0 });
  });
});
