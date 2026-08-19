/**
 * What leaves the machine (Issue #736).
 *
 * These cases are the enforcement of a privacy promise, not a formatting
 * check. Each one pins a claim made to the operator in the setting's own
 * documentation: off means off, minimal means no money, and an unanswered
 * consent prompt is not consent.
 */

import { describe, expect, it } from "vitest";
import {
  buildUsageReport,
  resolveUsageReportingLevel,
  type UsageReportingLevel,
} from "../../../src/services/usage/usageReporting";
import type { UsageSnapshot, UsageWindow } from "../../../src/services/usage/types";

const CAPTURED = new Date("2026-08-19T15:00:00.000Z");

function percentWindow(overrides: Partial<UsageWindow> = {}): UsageWindow {
  return {
    id: "claude-rate-limit:rolling",
    label: "Session (5h)",
    scope: "rolling",
    used: 44,
    limit: 100,
    unit: "percent",
    resetsAt: new Date("2026-08-19T17:14:00.000Z"),
    confidence: "estimated",
    observedAt: new Date("2026-08-19T14:41:00.000Z"),
    ...overrides,
  };
}

function dollarWindow(overrides: Partial<UsageWindow> = {}): UsageWindow {
  return {
    id: "local-telemetry:monthly",
    label: "This month",
    scope: "monthly",
    used: 178.61,
    limit: null,
    unit: "usd",
    resetsAt: null,
    confidence: "measured",
    ...overrides,
  };
}

function snapshot(windows: UsageWindow[], kind: UsageSnapshot["plan"]["kind"]): UsageSnapshot {
  return { adapter: "claude", plan: { kind }, capturedAt: CAPTURED, windows };
}

describe("buildUsageReport — the tier decides what leaves", () => {
  it("sends nothing at all when reporting is off", () => {
    const report = buildUsageReport(
      snapshot([percentWindow(), dollarWindow()], "subscription-window"),
      "off"
    );

    expect(report).toBeNull();
  });

  it("withholds every monetary window at the minimal tier", () => {
    const report = buildUsageReport(
      snapshot([percentWindow(), dollarWindow()], "subscription-window"),
      "minimal"
    );

    expect(report?.windows).toHaveLength(1);
    expect(report?.windows[0].unit).toBe("percent");
    // The promise is about money, so assert on the whole serialised payload:
    // a dollar figure must not appear anywhere in it, under any key.
    expect(JSON.stringify(report)).not.toContain("178.61");
    expect(JSON.stringify(report)).not.toContain("usd");
  });

  it("sends every window at the full tier", () => {
    const report = buildUsageReport(
      snapshot([percentWindow(), dollarWindow()], "subscription-window"),
      "full"
    );

    expect(report?.windows.map((w) => w.unit)).toEqual(["percent", "usd"]);
  });

  it("stamps the tier that produced the payload", () => {
    expect(buildUsageReport(snapshot([], "unknown"), "minimal")?.level).toBe("minimal");
    expect(buildUsageReport(snapshot([], "unknown"), "full")?.level).toBe("full");
  });

  // A dashboard that cannot tell "no dollar spend" from "dollar spend
  // withheld" would state something false, which is the same failure ADR 018
  // forbids locally.
  it("reports an empty snapshot rather than staying silent", () => {
    const report = buildUsageReport(snapshot([], "unknown"), "minimal");

    expect(report).not.toBeNull();
    expect(report?.plan).toBe("unknown");
    expect(report?.windows).toEqual([]);
  });

  it("reports a subscription snapshot whose windows are all withheld as empty, not as unknown", () => {
    const report = buildUsageReport(snapshot([dollarWindow()], "pay-per-token"), "minimal");

    // The plan is still what the agent observed. Rewriting it to `unknown`
    // because this tier withheld the windows would attribute the operator's
    // privacy choice to the provider.
    expect(report?.plan).toBe("pay-per-token");
    expect(report?.windows).toEqual([]);
  });

  it("serialises dates as ISO strings and absent as null", () => {
    const report = buildUsageReport(
      snapshot([percentWindow({ resetsAt: null, observedAt: undefined })], "subscription-window"),
      "minimal"
    );

    expect(report?.captured_at).toBe("2026-08-19T15:00:00.000Z");
    expect(report?.windows[0].resets_at).toBeNull();
    // Absent locally means "observed as the snapshot was derived". Copying
    // `captured_at` here would claim an as-of the provider never stated.
    expect(report?.windows[0].observed_at).toBeNull();
  });

  it("carries the vendor's own as-of when there is one", () => {
    const report = buildUsageReport(snapshot([percentWindow()], "subscription-window"), "minimal");

    expect(report?.windows[0].observed_at).toBe("2026-08-19T14:41:00.000Z");
    expect(report?.windows[0].confidence).toBe("estimated");
  });

  it("trims to the server's window cap rather than being rejected wholesale", () => {
    const many = Array.from({ length: 25 }, (_, i) =>
      percentWindow({ id: `claude-rate-limit:w${i}` })
    );

    const report = buildUsageReport(snapshot(many, "subscription-window"), "full");

    expect(report?.windows).toHaveLength(20);
  });

  it("preserves a null limit rather than substituting a ceiling", () => {
    const report = buildUsageReport(snapshot([dollarWindow()], "pay-per-token"), "full");

    expect(report?.windows[0].limit).toBeNull();
  });
});

describe("resolveUsageReportingLevel — consent is authoritative", () => {
  const tiers: UsageReportingLevel[] = ["off", "minimal", "full"];

  it("forces off when telemetry is declined, whatever the tier says", () => {
    for (const tier of tiers) {
      expect(resolveUsageReportingLevel(tier, false)).toBe("off");
    }
  });

  // null is the one-time consent prompt still unanswered. An unanswered
  // question must never be read as a yes.
  it("forces off while the consent prompt is unanswered", () => {
    for (const tier of tiers) {
      expect(resolveUsageReportingLevel(tier, null)).toBe("off");
      expect(resolveUsageReportingLevel(tier, undefined)).toBe("off");
    }
  });

  it("honours the configured tier once telemetry is enabled", () => {
    expect(resolveUsageReportingLevel("minimal", true)).toBe("minimal");
    expect(resolveUsageReportingLevel("full", true)).toBe("full");
    expect(resolveUsageReportingLevel("off", true)).toBe("off");
  });

  it("defaults to off when no tier is configured", () => {
    expect(resolveUsageReportingLevel(undefined, true)).toBe("off");
  });
});
