/**
 * What leaves the machine (Issues #736, #738).
 *
 * These cases are the enforcement of a privacy promise, not a formatting
 * check. Each one pins a claim made to the operator in the setting's own
 * documentation: off means off, minimal means no money, an explicit refusal
 * survives a change of default, and the editor's kill switch outranks every
 * Nightgauge setting.
 */

import { beforeEach, describe, expect, it, vi } from "vitest";
import {
  buildUsageReport,
  getUsageReportingLevel,
  resolveUsageReportingLevel,
  DEFAULT_USAGE_REPORTING_LEVEL,
  type UsageReportingLevel,
} from "../../../src/services/usage/usageReporting";
import { ConfigBridge } from "../../../src/services/ConfigBridge";
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

describe("resolveUsageReportingLevel — opt-out, with two vetoes (#738)", () => {
  const tiers: UsageReportingLevel[] = ["off", "minimal", "full"];

  // The resolved extension consent is not ours to reinterpret. No merged-config
  // value — not an explicit YAML consent, not an explicit tier — may send over
  // the top of an operator who refused at the extension or editor level.
  it("forces off when extension consent is denied, whatever the config says", () => {
    for (const tier of tiers) {
      expect(resolveUsageReportingLevel(tier, true, false)).toBe("off");
      expect(resolveUsageReportingLevel(tier, undefined, false)).toBe("off");
    }
  });

  // Changing a default is not the same act as overriding an answer.
  it("forces off when telemetry is explicitly declined, whatever the tier says", () => {
    for (const tier of tiers) {
      expect(resolveUsageReportingLevel(tier, false, true)).toBe("off");
    }
  });

  // undefined = never configured; null = the legacy prompt-pending state.
  // Neither is a refusal, and under an opt-out default neither blocks.
  it("reports when consent was never configured", () => {
    expect(resolveUsageReportingLevel(undefined, undefined, true)).toBe("full");
    expect(resolveUsageReportingLevel(undefined, null, true)).toBe("full");
    expect(resolveUsageReportingLevel("minimal", null, true)).toBe("minimal");
  });

  it("honours the configured tier", () => {
    expect(resolveUsageReportingLevel("minimal", true, true)).toBe("minimal");
    expect(resolveUsageReportingLevel("full", true, true)).toBe("full");
    expect(resolveUsageReportingLevel("off", true, true)).toBe("off");
  });

  it("defaults to full when no tier is configured", () => {
    expect(resolveUsageReportingLevel(undefined, true, true)).toBe("full");
    expect(DEFAULT_USAGE_REPORTING_LEVEL).toBe("full");
  });

  // An explicit `off` tier is still a refusal of *this* stream even though
  // product telemetry is on — the two switches stay independent.
  it("keeps the tier independent of the consent flag", () => {
    expect(resolveUsageReportingLevel("off", true, true)).toBe("off");
  });
});

describe("getUsageReportingLevel — consent is supplied, the tier is read (#738)", () => {
  const platform = { telemetry: { usage_reporting: "full" as const, enabled: true } };

  beforeEach(() => {
    vi.mocked(ConfigBridge.getInstance).mockReturnValue({
      isInitialized: () => true,
      getPlatform: () => platform,
    } as unknown as ReturnType<typeof ConfigBridge.getInstance>);
  });

  it("reports the configured tier when consent is granted", () => {
    expect(getUsageReportingLevel(true)).toBe("full");
  });

  // The caller's boolean is TelemetryConsentService.isEnabled(), which already
  // covers VSCode's telemetry.telemetryLevel kill switch and the
  // nightgauge.telemetry.enabled setting. Either one refusing lands here as
  // false, and nothing in the merged config may override it.
  it("sends nothing when consent is denied, even with an explicit full tier", () => {
    expect(getUsageReportingLevel(false)).toBe("off");
  });

  // The YAML store is a separate key from the extension setting, and a refusal
  // in it is equally binding.
  it("sends nothing when the YAML consent is explicitly false", () => {
    vi.mocked(ConfigBridge.getInstance).mockReturnValue({
      isInitialized: () => true,
      getPlatform: () => ({ telemetry: { usage_reporting: "full", enabled: false } }),
    } as unknown as ReturnType<typeof ConfigBridge.getInstance>);
    expect(getUsageReportingLevel(true)).toBe("off");
  });

  // The one absence that still fails closed: an unread config may contain an
  // explicit `false` we have no way to see yet.
  it("fails closed while the config bridge is uninitialised", () => {
    vi.mocked(ConfigBridge.getInstance).mockReturnValue({
      isInitialized: () => false,
      getPlatform: () => undefined,
    } as unknown as ReturnType<typeof ConfigBridge.getInstance>);
    expect(getUsageReportingLevel(true)).toBe("off");
  });

  it("defaults to full when the platform block is absent entirely", () => {
    vi.mocked(ConfigBridge.getInstance).mockReturnValue({
      isInitialized: () => true,
      getPlatform: () => undefined,
    } as unknown as ReturnType<typeof ConfigBridge.getInstance>);
    expect(getUsageReportingLevel(true)).toBe("full");
  });
});
