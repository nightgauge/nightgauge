import { describe, it, expect } from "vitest";
import { getKnowledgeValueDashboardHtml } from "../../../src/views/dashboard/KnowledgeValueDashboardHtml";
import type { KnowledgeValueState } from "../../../src/views/dashboard/KnowledgeValueDashboardTypes";
import type { KnowledgeMetricsResult } from "../../../src/services/IpcClientBase";

function makeResult(overrides: Partial<KnowledgeMetricsResult> = {}): KnowledgeMetricsResult {
  return {
    window_days: 7,
    stale_days: 30,
    status: "enabled",
    generated_at: "2026-05-16T12:00:00Z",
    hit_rate: 0.6,
    totals: {
      writes: 5,
      reads: 12,
      recalls: 10,
      recall_hits: 6,
      graduations: 2,
      scaffolds: 1,
      prunes: 0,
      indexes: 0,
      validates: 0,
      stats: 0,
      events_in_range: 36,
    },
    per_stage: [
      { stage: "feature-dev", reads: 7, writes: 3, recalls: 4, recall_hits: 2 },
      { stage: "feature-planning", reads: 5, writes: 2, recalls: 6, recall_hits: 4 },
    ],
    top_recalled: [
      { path: "k/a.md", hits: 5 },
      { path: "k/b.md", hits: 3 },
    ],
    stale_entries: [
      { path: "old.md", last_touched_at: "2026-04-01T00:00:00Z", days_since_touch: 45 },
    ],
    graduation_history: [
      {
        timestamp: "2026-05-15T10:00:00Z",
        issue_number: 101,
        path: "k/decisions.md",
        mode: "auto",
      },
    ],
    ...overrides,
  };
}

function makeState(current: KnowledgeMetricsResult | null): KnowledgeValueState {
  return {
    windowDays: 7,
    current,
    prior: null,
    delta: null,
    loadedAt: 0,
    loading: false,
    error: null,
  };
}

describe("KnowledgeValueDashboardHtml", () => {
  it("renders all five header cards when status is enabled", () => {
    const html = getKnowledgeValueDashboardHtml(makeState(makeResult()));
    expect(html).toContain("Writes");
    expect(html).toContain("Reads");
    expect(html).toContain("Recalls");
    expect(html).toContain("Hits");
    expect(html).toContain("Graduations");
  });

  it("renders the hit-rate gauge with the value rounded to a percent", () => {
    const html = getKnowledgeValueDashboardHtml(makeState(makeResult({ hit_rate: 0.6 })));
    expect(html).toMatch(/60%/);
    // green band for >50%
    expect(html).toContain("#22c55e");
  });

  it("renders a yellow band for 20-50%", () => {
    const html = getKnowledgeValueDashboardHtml(makeState(makeResult({ hit_rate: 0.3 })));
    expect(html).toContain("#eab308");
  });

  it("renders a red band for <20%", () => {
    const html = getKnowledgeValueDashboardHtml(makeState(makeResult({ hit_rate: 0.1 })));
    expect(html).toContain("#ef4444");
  });

  it("renders an em-dash for hit rate when recalls is 0", () => {
    const result = makeResult({
      hit_rate: undefined,
      totals: { ...makeResult().totals, recalls: 0 },
    });
    const html = getKnowledgeValueDashboardHtml(makeState(result));
    expect(html).toContain("—");
  });

  it("renders the per-stage SVG bar chart", () => {
    const html = getKnowledgeValueDashboardHtml(makeState(makeResult()));
    expect(html).toContain("<svg");
    expect(html).toContain("feature-dev");
    expect(html).toContain("feature-planning");
  });

  it("renders the top-recalled table rows", () => {
    const html = getKnowledgeValueDashboardHtml(makeState(makeResult()));
    expect(html).toContain("k/a.md");
    expect(html).toContain("k/b.md");
  });

  it("renders the stale entries table with day age", () => {
    const html = getKnowledgeValueDashboardHtml(makeState(makeResult()));
    expect(html).toContain("old.md");
    expect(html).toContain("45d");
  });

  it("renders the disabled empty-state when status is disabled", () => {
    const html = getKnowledgeValueDashboardHtml(makeState(makeResult({ status: "disabled" })));
    expect(html).toContain("Knowledge telemetry is disabled");
    expect(html).toContain("knowledge:");
    expect(html).toContain("enabled: true");
  });

  it("renders the empty-state when status is empty", () => {
    const empty = makeResult({
      status: "empty",
      totals: {
        writes: 0,
        reads: 0,
        recalls: 0,
        recall_hits: 0,
        graduations: 0,
        scaffolds: 0,
        prunes: 0,
        indexes: 0,
        validates: 0,
        stats: 0,
        events_in_range: 0,
      },
    });
    const html = getKnowledgeValueDashboardHtml(makeState(empty));
    expect(html).toContain("No knowledge activity yet");
  });

  it("renders an error block when state.error is populated", () => {
    const state = makeState(null);
    state.error = "Aggregation failed";
    const html = getKnowledgeValueDashboardHtml(state);
    expect(html).toContain("Could not load metrics");
    expect(html).toContain("Aggregation failed");
  });

  it("includes a CSP meta tag with the nonce", () => {
    const html = getKnowledgeValueDashboardHtml(makeState(makeResult()));
    expect(html).toMatch(/Content-Security-Policy.*nonce-[A-Za-z0-9]+/);
  });

  it("marks the active window-selector button", () => {
    const state = makeState(makeResult());
    state.windowDays = 30;
    const html = getKnowledgeValueDashboardHtml(state);
    expect(html).toContain(`kv-window-btn kv-window-btn--active" onclick="setWindow(30)"`);
  });

  it("escapes HTML in path fields", () => {
    const result = makeResult({
      top_recalled: [{ path: "<script>alert(1)</script>.md", hits: 1 }],
    });
    const html = getKnowledgeValueDashboardHtml(makeState(result));
    expect(html).not.toContain("<script>alert(1)</script>.md");
    expect(html).toContain("&lt;script&gt;alert(1)&lt;/script&gt;.md");
  });
});
