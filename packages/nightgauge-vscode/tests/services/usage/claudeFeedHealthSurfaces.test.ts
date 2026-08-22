/**
 * One verdict, three surfaces (#810 AC 3).
 *
 * The Dashboard Usage panel, the status-bar tooltip and the enable command each
 * inferred the feed's state independently. The panel and the tooltip read
 * `plan.kind`; the command read `readStatusLineState().wired`. Those two
 * questions disagree exactly when the feed is wired but dead — which is the
 * state the maintainer's machine was in — so the panel offered to enable a feed
 * the command called already enabled, in the same session.
 *
 * Health now rides on the snapshot, so the disagreement is not merely fixed but
 * unavailable.
 */

import { describe, expect, it } from "vitest";
import * as fs from "node:fs/promises";
import * as os from "node:os";
import * as path from "node:path";
import { ClaudeRateLimitStore } from "../../../src/services/usage/ClaudeRateLimitStore";
import { buildUsagePanelState } from "../../../src/views/dashboard/usagePanel";
import { getUsagePanelSectionHtml } from "../../../src/views/dashboard/tabs/UsagePanelHtml";
import type { ClaudeFeedHealth } from "../../../src/services/usage/claudeStatusLineSetup";
import type { UsageSnapshot } from "../../../src/services/usage/types";

const CAPTURED = new Date("2026-08-22T12:00:00.000Z");

function snapshot(health: ClaudeFeedHealth | undefined): UsageSnapshot {
  return {
    adapter: "claude",
    // The state the footer is in when the feed is dead: dollar windows, because
    // ClaudeRateLimitUsageProvider has nothing to report.
    plan: { kind: "pay-per-token" },
    capturedAt: CAPTURED,
    windows: [
      {
        id: "local:monthly",
        label: "This month",
        kind: "calendar",
        used: 12,
        limit: 100,
        unit: "usd",
        confidence: "estimated",
        observedAt: CAPTURED,
        resetsAt: null,
      },
    ],
    claudeFeedHealth: health,
  } as UsageSnapshot;
}

const BROKEN: ClaudeFeedHealth = {
  state: "broken",
  reason: "binary-missing",
  binary: "/gone/nightgauge",
  lastObservedAt: new Date("2026-08-20T04:34:11.326Z"),
};

function panelHtmlFor(health: ClaudeFeedHealth | undefined): string {
  const state = buildUsagePanelState(snapshot(health), []);
  expect(state).not.toBeNull();
  return getUsagePanelSectionHtml(state!);
}

describe("the Usage panel's feed affordance follows the shared verdict", () => {
  it("offers to enable only when the feed is genuinely not wired", () => {
    const html = panelHtmlFor({
      state: "not-wired",
      reason: "not-wired",
      binary: null,
      lastObservedAt: null,
    });
    expect(html).toContain("Show my 5-hour and weekly limits");
    expect(html).not.toContain("Check the Claude usage feed");
  });

  it("stops offering to enable a feed that is wired but broken", () => {
    const html = panelHtmlFor(BROKEN);
    // The contradiction, gone: this is the state in which the command said
    // "already enabled".
    expect(html).not.toContain("Show my 5-hour and weekly limits");
    expect(html).toContain("Check the Claude usage feed");
  });

  it("says the feed is enabled but not reporting, rather than implying it is off", () => {
    expect(panelHtmlFor(BROKEN)).toContain("enabled but is not reporting");
  });

  it("falls back to the enable prompt when health is not yet known", () => {
    // First render, before the probe resolves. The pre-#810 copy is the honest
    // default: we have no verdict, and an Enable prompt is recoverable while a
    // wrong "it's broken" is alarming.
    const html = panelHtmlFor(undefined);
    expect(html).toContain("Show my 5-hour and weekly limits");
  });

  it("carries the verdict's state, and nothing more, into the panel state", () => {
    const state = buildUsagePanelState(snapshot(BROKEN), []);
    expect(state!.claudeFeedHealth).toBe("broken");
  });
});

describe("ClaudeRateLimitStore.lastObservedAt", () => {
  async function store(): Promise<ClaudeRateLimitStore> {
    return new ClaudeRateLimitStore(await fs.mkdtemp(path.join(os.tmpdir(), "ng-810-obs-")));
  }

  it("is null before anything is recorded", async () => {
    const s = await store();
    await s.load();
    expect(s.lastObservedAt()).toBeNull();
  });

  it("reports the newest observation across buckets", async () => {
    const s = await store();
    const older = new Date("2026-08-20T04:34:11.326Z");
    const newer = new Date("2026-08-21T09:00:00.000Z");
    await s.record(
      {
        rateLimitType: "five_hour",
        utilization: 0.4,
        // resetsAt is UNIX SECONDS on the wire, not a Date.
        resetsAt: Math.floor(Date.now() / 1000) + 3600,
      } as never,
      older
    );
    await s.record(
      {
        rateLimitType: "seven_day",
        utilization: 0.2,
        // resetsAt is UNIX SECONDS on the wire, not a Date.
        resetsAt: Math.floor(Date.now() / 1000) + 3600,
      } as never,
      newer
    );

    expect(s.lastObservedAt()?.toISOString()).toBe(newer.toISOString());
  });

  it("still reports a timestamp for a reading whose window has already refilled", async () => {
    // This is the whole point. `readings()` drops expired readings because
    // their NUMBERS are known-wrong; their TIMESTAMPS are exactly the "when did
    // this last work" signal that separates a dead feed from a quiet one.
    const s = await store();
    const observed = new Date("2026-08-20T04:34:11.326Z");
    await s.record(
      {
        rateLimitType: "five_hour",
        utilization: 0.4,
        // resetsAt is UNIX SECONDS on the wire, not a Date.
        resetsAt: Math.floor(Date.now() / 1000) - 60,
      } as never,
      observed
    );

    expect(s.readings()).toHaveLength(0);
    expect(s.lastObservedAt()?.toISOString()).toBe(observed.toISOString());
  });
});
