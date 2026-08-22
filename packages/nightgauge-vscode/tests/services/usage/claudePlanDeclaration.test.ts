/**
 * Declaring a Claude plan (#808).
 *
 * Nightgauge can only OBSERVE a subscription allowance once a
 * `rate_limit_event` reading has arrived. Until then the footer falls through
 * to locally-derived dollar windows, so an operator on Max 20x is shown a
 * different BILLING MODEL than the one they are on.
 *
 * ADR 018 forbids INFERRING a plan from the adapter id. A declaration is not an
 * inference — but it must stay strictly on the "which windows exist" side of
 * the line and never supply the numbers in them.
 */

import { describe, expect, it } from "vitest";
import * as fs from "node:fs/promises";
import * as os from "node:os";
import * as path from "node:path";
import { ClaudeRateLimitStore } from "../../../src/services/usage/ClaudeRateLimitStore";
import { ClaudeRateLimitUsageProvider } from "../../../src/services/usage/ClaudeRateLimitUsageProvider";
import {
  declaredPlanWindows,
  isSubscriptionDeclaration,
  parseClaudePlanDeclaration,
  type ClaudePlanDeclaration,
} from "../../../src/services/usage/claudePlanDeclaration";

async function emptyStore(): Promise<ClaudeRateLimitStore> {
  return new ClaudeRateLimitStore(await fs.mkdtemp(path.join(os.tmpdir(), "ng-808-")));
}

function provider(store: ClaudeRateLimitStore, plan: ClaudePlanDeclaration) {
  return new ClaudeRateLimitUsageProvider(store, () => plan);
}

describe("parseClaudePlanDeclaration", () => {
  it("accepts every declared plan", () => {
    for (const plan of ["not-declared", "max-20x", "max-5x", "pro", "api"] as const) {
      expect(parseClaudePlanDeclaration(plan)).toBe(plan);
    }
  });

  it("degrades an unrecognised value to not-declared rather than guessing", () => {
    // A typo in a settings file must fall back to today's behaviour, never
    // assert a plan the operator did not choose.
    for (const raw of ["max20x", "Max 20x", "", null, undefined, 5, {}]) {
      expect(parseClaudePlanDeclaration(raw)).toBe("not-declared");
    }
  });

  it("treats exactly the refilling-allowance plans as subscriptions", () => {
    expect(isSubscriptionDeclaration("max-20x")).toBe(true);
    expect(isSubscriptionDeclaration("max-5x")).toBe(true);
    expect(isSubscriptionDeclaration("pro")).toBe(true);
    expect(isSubscriptionDeclaration("api")).toBe(false);
    expect(isSubscriptionDeclaration("not-declared")).toBe(false);
  });
});

describe("declaredPlanWindows", () => {
  it("names the two allowances a subscription has, and measures neither", () => {
    const windows = declaredPlanWindows("claude-rate-limit");
    expect(windows.map((w) => w.scope)).toEqual(["rolling", "weekly"]);
    for (const window of windows) {
      // The invariant this whole issue turns on: no fabricated utilization.
      expect(window.used).toBeNull();
      expect(window.confidence).toBe("unknown");
    }
  });
});

describe("ClaudeRateLimitUsageProvider with a declaration", () => {
  it("preserves today's behaviour exactly when nothing is declared", async () => {
    const snapshot = await provider(await emptyStore(), "not-declared").getSnapshot("claude");
    // null hands the adapter to LocalTelemetryUsageProvider's dollar windows.
    expect(snapshot).toBeNull();
  });

  it("keeps the dollar windows for a declared API/pay-per-token operator", async () => {
    // Not an oversight: dollar windows ARE the right answer for that operator.
    const snapshot = await provider(await emptyStore(), "api").getSnapshot("claude");
    expect(snapshot).toBeNull();
  });

  it.each(["max-20x", "max-5x", "pro"] as const)(
    "reports a subscription plan with unmeasured window shells for %s",
    async (plan) => {
      const snapshot = await provider(await emptyStore(), plan).getSnapshot("claude");

      expect(snapshot).not.toBeNull();
      expect(snapshot!.plan.kind).toBe("subscription-window");
      expect(snapshot!.windows.map((w) => w.scope)).toEqual(["rolling", "weekly"]);
      expect(snapshot!.windows.every((w) => w.used === null)).toBe(true);
    }
  );

  it("fabricates no utilization for a declared plan with no reading", async () => {
    const snapshot = await provider(await emptyStore(), "max-20x").getSnapshot("claude");

    for (const window of snapshot!.windows) {
      expect(window.used).toBeNull();
      // Not zero, and not a number of any kind — a zero would render as "0%
      // used", which is precisely the fabricated figure ADR 018 forbids.
      expect(window.used).not.toBe(0);
    }
  });

  it("lets an observed reading outrank the declaration for utilization", async () => {
    const store = await emptyStore();
    await store.record(
      {
        rateLimitType: "five_hour",
        utilization: 44,
        resetsAt: Math.floor(Date.now() / 1000) + 3600,
      } as never,
      new Date()
    );

    const snapshot = await provider(store, "max-20x").getSnapshot("claude");

    // The declaration decided the plan kind; the reading decided the number.
    expect(snapshot!.plan.kind).toBe("subscription-window");
    const rolling = snapshot!.windows.find((w) => w.scope === "rolling");
    expect(rolling!.used).toBe(44);
    expect(rolling!.confidence).not.toBe("unknown");
    // No shell was left beside the real reading.
    expect(snapshot!.windows.filter((w) => w.used === null)).toHaveLength(0);
  });

  it("still reports an observed reading when nothing is declared", async () => {
    const store = await emptyStore();
    await store.record(
      {
        rateLimitType: "five_hour",
        utilization: 12,
        resetsAt: Math.floor(Date.now() / 1000) + 3600,
      } as never,
      new Date()
    );

    const snapshot = await provider(store, "not-declared").getSnapshot("claude");

    expect(snapshot!.plan.kind).toBe("subscription-window");
    expect(snapshot!.windows[0].used).toBe(12);
  });

  it("claims nothing for a non-claude adapter, declared or not", async () => {
    expect(await provider(await emptyStore(), "max-20x").getSnapshot("codex")).toBeNull();
  });
});
