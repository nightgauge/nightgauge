/**
 * UsagePanelHtml.test.ts
 *
 * Issue #661 — what the usage & quota panel actually renders. The two rules
 * that are correctness rather than styling get their own cases: a window with
 * no ceiling gets no bar, and a window whose usage is a floor never gets a
 * proportional one.
 *
 * @see docs/decisions/018-adapter-usage-quota-model.md
 */

import { describe, it, expect } from "vitest";
import type { PipelineRunSummary } from "../../../src/views/dashboard/DashboardState";
import type { UsageSnapshot, UsageWindow } from "../../../src/services/usage/types";
import { unknownUsageSnapshot } from "../../../src/services/usage/types";
import { buildUsagePanelState } from "../../../src/views/dashboard/usagePanel";
import { getUsagePanelSectionHtml } from "../../../src/views/dashboard/tabs/UsagePanelHtml";

const NOW = new Date("2026-08-18T12:00:00.000Z");
const DAY_MS = 24 * 60 * 60 * 1000;

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
  return { adapter: "claude", plan: { kind: "pay-per-token" }, capturedAt: NOW, windows };
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
      costUsd: 2,
      durationMs: 1000,
      stageCount: 1,
    },
    ...overrides,
  };
}

function render(snapshot: UsageSnapshot | null, history: PipelineRunSummary[] = []): string {
  return getUsagePanelSectionHtml(buildUsagePanelState(snapshot, history, NOW), NOW);
}

/**
 * The markup for one window row, isolated from its neighbours by splitting on
 * the row delimiter — so a "this row has no bar" assertion cannot be satisfied
 * (or defeated) by an adjacent row's markup.
 */
function rowFor(html: string, windowId: string): string {
  const chunks = html.split('<div class="usage-window-row"');
  const row = chunks.find((chunk) => chunk.startsWith(` data-window-id="${windowId}"`));
  expect(row, `no window row rendered for "${windowId}"`).toBeDefined();
  return row as string;
}

describe("getUsagePanelSectionHtml — empty states (Issue #661)", () => {
  it("renders nothing when there is no usage service at all", () => {
    expect(render(null)).toBe("");
  });

  it("names the adapter in the unknown-plan empty state, and says unknown not zero", () => {
    const html = getUsagePanelSectionHtml(
      buildUsagePanelState(unknownUsageSnapshot("ollama", NOW), [makeRun()], NOW),
      NOW
    );

    // Assert against the explanatory sentence itself, not the whole document:
    // the panel's capture footnote also names the adapter, so a page-wide
    // `toContain` would stay green if the explanation stopped naming it.
    const explanation = html.split('<p class="usage-value">')[1].split("</p>")[0];
    expect(explanation).toContain("ollama");
    expect(explanation).toContain("unknown");
    expect(explanation).toContain("not zero");
    // Nothing to fill: no bar of any kind, empty or otherwise.
    expect(html).not.toContain("usage-progress-bar");
  });
});

describe("getUsagePanelSectionHtml — every window (Issue #661)", () => {
  const threeWindows = () =>
    makeSnapshot([
      makeWindow({
        id: "s",
        scope: "session",
        label: "This session",
        used: 3,
        limit: null,
        resetsAt: null,
      }),
      makeWindow({ id: "d", scope: "daily", label: "Today", used: 7, limit: null }),
      makeWindow({ id: "m", scope: "monthly", label: "This month", used: 50, limit: 200 }),
    ]);

  it("renders a row per window with its absolute figures", () => {
    const html = render(threeWindows());

    expect(html).toContain('data-window-id="s"');
    expect(html).toContain('data-window-id="d"');
    expect(html).toContain('data-window-id="m"');
    expect(html).toContain("This session");
    expect(html).toContain("$50.00 of $200.00 (25%)");
  });

  it("gives a window with a real ceiling a proportional bar", () => {
    const row = rowFor(render(threeWindows()), "m");
    expect(row).toContain("usage-progress-track");
    expect(row).toContain('style="width: 25.0%"');
  });

  it("gives a window with no ceiling no bar at all — never an empty one", () => {
    const row = rowFor(render(threeWindows()), "d");
    expect(row).toContain("no limit configured");
    expect(row).not.toContain("usage-progress-track");
  });

  it("shows each window's reset time, and says so when there is none", () => {
    const html = render(threeWindows());
    expect(rowFor(html, "s")).toContain("No scheduled reset");
    expect(rowFor(html, "m")).toContain("Resets");
  });

  it("labels a floor as a floor and draws an indeterminate bar, not an empty one", () => {
    const html = render(
      makeSnapshot([makeWindow({ id: "m", used: 0, limit: 100, confidence: "unknown" })])
    );
    const row = rowFor(html, "m");

    expect(row).toContain("Unknown");
    expect(row).toContain("at least");
    expect(row).toContain("usage-bar-unknown");
    // The forbidden rendering: a zero-width proportional fill.
    expect(row).not.toContain('style="width: 0.0%"');
  });

  it("marks a window over its warning and critical thresholds", () => {
    expect(rowFor(render(makeSnapshot([makeWindow({ id: "m", used: 85 })])), "m")).toContain(
      "usage-bar-warning"
    );
    expect(rowFor(render(makeSnapshot([makeWindow({ id: "m", used: 95 })])), "m")).toContain(
      "usage-bar-critical"
    );
  });
});

describe("getUsagePanelSectionHtml — per-model-family breakdown (Issue #661)", () => {
  it("omits the section entirely when the snapshot carries no family windows", () => {
    expect(render(makeSnapshot([makeWindow()]))).not.toContain("Per-model breakdown");
  });

  it("renders a group per family when it does", () => {
    const html = render(
      makeSnapshot([
        makeWindow({ id: "overall" }),
        makeWindow({ id: "opus:weekly", label: "This week", modelFamily: "opus", used: 90 }),
        makeWindow({ id: "sonnet:weekly", label: "This week", modelFamily: "sonnet", used: 10 }),
      ])
    );

    expect(html).toContain("Per-model breakdown");
    expect(html).toContain("opus");
    expect(html).toContain("sonnet");
    expect(html).toContain('data-window-id="opus:weekly"');
  });
});

describe("getUsagePanelSectionHtml — burn rate and history strip (Issue #661)", () => {
  const twoRuns = () => [
    makeRun({ issueNumber: 1, startedAt: new Date(NOW.getTime() - 2 * DAY_MS) }),
    makeRun({ issueNumber: 2, startedAt: new Date(NOW.getTime() - 1 * DAY_MS) }),
  ];

  it("states the rate and the projected exhaustion of the active window", () => {
    const html = render(makeSnapshot([makeWindow({ used: 20, limit: 100 })]), twoRuns());

    expect(html).toContain("Burn rate");
    expect(html).toContain("/hour");
    expect(html).toContain("reaches its");
    expect(html).toContain("2 runs in the last 7 days");
  });

  it("says why there is no rate instead of showing a zero", () => {
    const html = render(makeSnapshot([makeWindow()]), []);
    expect(html).toContain("Not enough recent history");
    expect(html).not.toContain("/hour");
  });

  it("lists the recent runs with spend and tokens, and totals them", () => {
    const html = render(makeSnapshot([makeWindow()]), twoRuns());

    expect(html).toContain("Recent runs");
    expect(html).toContain("#1 A run");
    expect(html).toContain("#2 A run");
    // Two runs at $2, 1000 tokens each.
    expect(html).toContain("$4.00");
    expect(html).toContain("2.0K");
  });

  it("says so when there are no runs yet", () => {
    expect(render(makeSnapshot([makeWindow()]), [])).toContain("No pipeline runs recorded yet.");
  });

  it("names the workspace-wide attribution the burn rate actually has", () => {
    const html = render(makeSnapshot([makeWindow()]), twoRuns());
    expect(html).toContain("no adapter attribution");
  });
});

describe("getUsagePanelSectionHtml — a subscription-window plan (Issue #709)", () => {
  /** What the Claude provider hands the panel on a Max plan. */
  function maxSnapshot(overrides: Partial<UsageWindow> = {}): UsageSnapshot {
    return {
      adapter: "claude",
      plan: { kind: "subscription-window" },
      capturedAt: NOW,
      windows: [
        makeWindow({
          id: "claude-rate-limit:rolling",
          label: "Session (5h)",
          scope: "rolling",
          used: 44,
          limit: 100,
          unit: "percent",
          resetsAt: new Date(NOW.getTime() + 2 * 60 * 60 * 1000),
          confidence: "measured",
          ...overrides,
        }),
      ],
    };
  }

  it("states what is left rather than printing the same percentage twice", () => {
    const row = rowFor(render(maxSnapshot()), "claude-rate-limit:rolling");

    expect(row).toContain("44% used");
    expect(row).toContain("56% remaining");
    // "44% of 100% (44%)" is the shape a plain used/limit line would produce.
    expect(row).not.toContain("of 100%");
  });

  it("shows when a cached reading was actually reported", () => {
    const observedAt = new Date(NOW.getTime() - 25 * 60 * 1000);
    const row = rowFor(
      render(maxSnapshot({ confidence: "estimated", observedAt })),
      "claude-rate-limit:rolling"
    );

    expect(row).toContain("Reported as of");
    expect(row).toContain(observedAt.toLocaleString());
  });

  it("adds no as-of to a live reading", () => {
    const row = rowFor(
      render(maxSnapshot({ confidence: "measured", observedAt: NOW })),
      "claude-rate-limit:rolling"
    );

    expect(row).not.toContain("Reported as of");
  });

  it("declines to project a dollar burn rate against a percentage window", () => {
    // Run history is denominated in dollars. Projecting it against a
    // vendor-reported percentage would be arithmetic across two different
    // things, so the panel says why instead of showing a number.
    const html = render(maxSnapshot(), [makeRun(), makeRun({ issueNumber: 2 })]);

    expect(html).toContain(
      "Burn rate is measured in dollars from run history, which cannot describe"
    );
    expect(html).not.toContain("/hour");
  });
});
