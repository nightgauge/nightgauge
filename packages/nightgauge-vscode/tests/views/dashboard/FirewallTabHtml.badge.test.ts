/**
 * FirewallTabHtml.badge.test.ts
 *
 * Regression coverage for #986: `getFirewallSectionHtml` (and the mode-badge
 * renderer it calls internally) must never throw when handed a `mode` that
 * isn't one of `warn | block | disabled` — including `undefined`, which is
 * exactly what an under-specified fixture (or a stale IPC payload) produces.
 * Anything outside the known set renders the "Unknown" badge instead of
 * crashing the whole dashboard render (this is also correct product
 * behaviour for a daemon that hasn't reported a mode we recognize).
 */

import { describe, it, expect } from "vitest";
import { getFirewallSectionHtml } from "../../../src/views/dashboard/tabs/FirewallTabHtml";
import type {
  SanitizationEvent,
  FirewallFilterState,
  FirewallAggregates,
  FirewallTimeSeriesPoint,
} from "../../../src/views/dashboard/FirewallTypes";

const events: SanitizationEvent[] = [];
const filters: FirewallFilterState = {
  timeRange: "all",
  eventTypes: ["blocked", "warned", "bypassed"],
  categories: [],
  searchText: "",
};
const aggregates: FirewallAggregates = {
  totalBlocked: 0,
  totalWarned: 0,
  totalBypassed: 0,
  mostCommonCategory: null,
  mostRecentEvent: null,
  categoryBreakdown: {
    destructive: 0,
    exfiltration: 0,
    privilege_escalation: 0,
    prompt_injection: 0,
    path_traversal: 0,
    allowlist: 0,
    unknown: 0,
  },
  toolBreakdown: {},
};
const timeSeriesData: FirewallTimeSeriesPoint[] = [];

function badgeHtmlFor(mode: unknown): string {
  return getFirewallSectionHtml(
    events,
    filters,
    aggregates,
    timeSeriesData,
    "test-nonce",
    mode as never
  );
}

describe("getFirewallSectionHtml mode badge (#986)", () => {
  it("does not throw and renders the Unknown badge when mode is undefined", () => {
    let html = "";
    expect(() => {
      html = badgeHtmlFor(undefined);
    }).not.toThrow();
    expect(html).toContain("firewall-mode-badge firewall-mode-unknown");
    expect(html).toContain("Firewall: Unknown");
  });

  it("does not throw and renders the Unknown badge for a legacy/bogus mode string", () => {
    let html = "";
    expect(() => {
      html = badgeHtmlFor("legacy-mode-string");
    }).not.toThrow();
    expect(html).toContain("firewall-mode-badge firewall-mode-unknown");
    expect(html).toContain("Firewall: Unknown");
  });

  it("still renders the real badge for a known mode", () => {
    const html = badgeHtmlFor("block");
    expect(html).toContain("firewall-mode-badge firewall-mode-block");
    expect(html).toContain("Firewall: Block");
  });

  it("renders the Unknown badge for explicit null (config authority unreachable)", () => {
    const html = badgeHtmlFor(null);
    expect(html).toContain("firewall-mode-badge firewall-mode-unknown");
    expect(html).toContain("Firewall: Unknown");
  });
});
