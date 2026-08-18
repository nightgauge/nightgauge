/**
 * Tests for the Claude subscription-window usage path (Issue #709).
 *
 * What is pinned here is the contract the status bar and the usage panel read:
 * which `rateLimitType` bucket becomes which `UsageWindowScope`, that a
 * reading survives a process restart so the meter can sample at rest, that a
 * cached reading is downgraded to `estimated` and carries its as-of, that a
 * reading whose own window has already refilled is dropped rather than served,
 * and that a plan with no observed event says nothing at all so the
 * pay-per-token path keeps answering.
 *
 * These are behaviours, not shapes: each one corresponds to a way the meter
 * could tell an operator something untrue.
 */

import { describe, it, expect, beforeEach, afterEach } from "vitest";
import * as fs from "node:fs/promises";
import * as os from "node:os";
import * as path from "node:path";

import {
  ClaudeRateLimitStore,
  readingHasExpired,
  type RateLimitReading,
} from "../../../src/services/usage/ClaudeRateLimitStore";
import {
  ClaudeRateLimitUsageProvider,
  readingToWindow,
} from "../../../src/services/usage/ClaudeRateLimitUsageProvider";
import type { RateLimitEventData } from "../../../src/utils/tokenParser";

/**
 * Anchored to the real clock, not a literal date.
 *
 * `ClaudeRateLimitUsageProvider.getSnapshot` reads `new Date()` internally to
 * decide which windows have refilled, so a hard-coded "now" would make every
 * expiry assertion depend on when the suite happens to run. Every figure below
 * is an offset from this instant, which keeps the behaviour deterministic
 * without faking timers around `fs/promises`.
 */
const NOW = new Date();
/** Two hours after NOW, in the unix **seconds** the wire format uses. */
const RESETS_LATER = Math.floor(NOW.getTime() / 1000) + 2 * 60 * 60;
/** One hour before NOW — a window that has already refilled. */
const RESETS_EARLIER = Math.floor(NOW.getTime() / 1000) - 60 * 60;

let workspace: string;

beforeEach(async () => {
  workspace = await fs.mkdtemp(path.join(os.tmpdir(), "ng-claude-usage-"));
});

afterEach(async () => {
  await fs.rm(workspace, { recursive: true, force: true });
});

function event(overrides: Partial<RateLimitEventData> = {}): RateLimitEventData {
  return {
    resetsAt: RESETS_LATER,
    rateLimitType: "five_hour",
    utilization: 44,
    status: "allowed",
    isUsingOverage: false,
    ...overrides,
  };
}

function reading(overrides: Partial<RateLimitReading> = {}): RateLimitReading {
  return {
    rateLimitType: "five_hour",
    utilization: 44,
    resetsAt: RESETS_LATER,
    status: "allowed",
    observedAt: new Date(NOW.getTime() - 25 * 60 * 1000),
    live: false,
    ...overrides,
  };
}

describe("readingToWindow — the rateLimitType → scope mapping", () => {
  it.each([
    ["five_hour", "rolling", "Session (5h)"],
    ["seven_day", "weekly", "This week"],
    ["daily", "daily", "Today"],
  ])("maps %s onto scope %s", (bucket, scope, label) => {
    const window = readingToWindow("claude-rate-limit", reading({ rateLimitType: bucket }));

    expect(window).not.toBeNull();
    expect(window!.scope).toBe(scope);
    expect(window!.label).toBe(label);
    expect(window!.id).toBe(`claude-rate-limit:${scope}`);
  });

  it("renders utilization as a vendor-reported percentage against 100", () => {
    const window = readingToWindow("claude-rate-limit", reading({ utilization: 44 }))!;

    expect(window.used).toBe(44);
    expect(window.limit).toBe(100);
    expect(window.unit).toBe("percent");
    // resetsAt is unix SECONDS on the wire and milliseconds in the model. A
    // factor-of-1000 slip here reads as "resets in 56 years".
    expect(window.resetsAt?.toISOString()).toBe(new Date(RESETS_LATER * 1000).toISOString());
  });

  it("produces no window for a bucket name it cannot honestly name", () => {
    // An unrecognised bucket has no scope, and guessing one would mislabel the
    // period the percentage describes. The wire format is unofficial and has
    // changed shape before, so this is a live risk, not a hypothetical.
    expect(
      readingToWindow("claude-rate-limit", reading({ rateLimitType: "monthly_v2" }))
    ).toBeNull();
  });

  it("never populates modelFamily — the signal carries none", () => {
    expect(readingToWindow("claude-rate-limit", reading())!.modelFamily).toBeUndefined();
  });

  it("carries no reset time when the event carried none", () => {
    expect(readingToWindow("claude-rate-limit", reading({ resetsAt: 0 }))!.resetsAt).toBeNull();
  });
});

describe("confidence and the as-of stamp", () => {
  it("reports measured, with no as-of needed, for a same-run reading", () => {
    const window = readingToWindow("claude-rate-limit", reading({ live: true }))!;

    expect(window.confidence).toBe("measured");
  });

  it("downgrades a cached reading to estimated and surfaces when it was observed", () => {
    const observedAt = new Date(NOW.getTime() - 25 * 60 * 1000);
    const window = readingToWindow("claude-rate-limit", reading({ live: false, observedAt }))!;

    expect(window.confidence).toBe("estimated");
    expect(window.observedAt).toEqual(observedAt);
  });
});

describe("readingHasExpired — a refilled window is known-wrong, not merely stale", () => {
  it("is expired once its own resetsAt has passed", () => {
    expect(readingHasExpired(reading({ resetsAt: RESETS_EARLIER }), NOW)).toBe(true);
  });

  it("is not expired while its window is still running", () => {
    expect(readingHasExpired(reading({ resetsAt: RESETS_LATER }), NOW)).toBe(false);
  });

  it("cannot expire when the event carried no reset time", () => {
    // There is no clock to expire against. The reading stays readable and
    // relies on confidence/observedAt to state its age.
    expect(readingHasExpired(reading({ resetsAt: 0 }), NOW)).toBe(false);
  });
});

describe("ClaudeRateLimitStore — persistence across process restarts", () => {
  it("persists an observed event and reads it back in a fresh store", async () => {
    const writer = new ClaudeRateLimitStore(workspace);
    await writer.record(event({ utilization: 61 }), NOW);

    // A second store is a stand-in for the next VS Code window: the whole
    // point of the file is that the meter can answer between runs.
    const reader = new ClaudeRateLimitStore(workspace);
    await reader.load();
    const readings = reader.readings(NOW);

    expect(readings).toHaveLength(1);
    expect(readings[0].utilization).toBe(61);
    expect(readings[0].observedAt).toEqual(NOW);
    // Restored, therefore not from the run that is streaming now.
    expect(readings[0].live).toBe(false);
  });

  it("writes the file where the gitignore rule covers it", async () => {
    const store = new ClaudeRateLimitStore(workspace);
    await store.record(event(), NOW);

    // Pinned literally, because the `/usage/` rule in the generated
    // .nightgauge/.gitignore is what keeps this per-machine cache out of git.
    // Moving the file without moving the rule surfaces it as an untracked
    // change in every user's repository.
    expect(store.filePath).toBe(path.join(workspace, ".nightgauge/usage/claude-rate-limits.json"));
    await expect(fs.access(store.filePath)).resolves.toBeUndefined();
  });

  it("keeps only the newest reading per bucket, and one per bucket", async () => {
    const store = new ClaudeRateLimitStore(workspace);
    store.record(event({ utilization: 10 }), NOW);
    store.record(event({ utilization: 55 }), NOW);
    await store.record(event({ rateLimitType: "seven_day", utilization: 12 }), NOW);

    const readings = store
      .readings(NOW)
      .sort((a, b) => a.rateLimitType.localeCompare(b.rateLimitType));
    expect(readings.map((r) => [r.rateLimitType, r.utilization])).toEqual([
      ["five_hour", 55],
      ["seven_day", 12],
    ]);
  });

  it("ignores an event whose bucket the parser could not name", async () => {
    // tokenParser defaults a missing rateLimitType to "unknown". That names no
    // window, and must not become one.
    const store = new ClaudeRateLimitStore(workspace);
    store.record(event({ rateLimitType: "unknown" }), NOW);

    expect(store.readings(NOW)).toEqual([]);
  });

  it("settle() turns every live reading into a cached one", async () => {
    const store = new ClaudeRateLimitStore(workspace);
    await store.record(event(), NOW);
    expect(store.readings(NOW)[0].live).toBe(true);

    store.settle();

    expect(store.readings(NOW)[0].live).toBe(false);
  });

  it("does not let lazy hydration demote a reading recorded before it", async () => {
    // Hydration is lazy — the first getSnapshot triggers it — so a run that
    // streamed an event before anything read the meter would have its live
    // reading overwritten by the `live: false` copy the write path had just
    // persisted, silently reporting a same-run figure as a cached one.
    const store = new ClaudeRateLimitStore(workspace);
    await store.record(event({ utilization: 77 }), NOW);

    await store.load();

    expect(store.readings(NOW)[0].live).toBe(true);
    expect(store.readings(NOW)[0].utilization).toBe(77);
  });

  it("treats an unreadable or malformed file as no readings, never as a crash", async () => {
    const store = new ClaudeRateLimitStore(workspace);
    await fs.mkdir(path.dirname(store.filePath), { recursive: true });
    await fs.writeFile(store.filePath, "{ not json", "utf8");

    await store.load();

    expect(store.readings(NOW)).toEqual([]);
  });

  it("discards a persisted entry whose fields do not hold up", async () => {
    const store = new ClaudeRateLimitStore(workspace);
    await fs.mkdir(path.dirname(store.filePath), { recursive: true });
    await fs.writeFile(
      store.filePath,
      JSON.stringify({
        version: 1,
        buckets: {
          five_hour: {
            rateLimitType: "five_hour",
            utilization: "lots",
            resetsAt: RESETS_LATER,
            status: "allowed",
            observedAt: NOW.toISOString(),
          },
        },
      }),
      "utf8"
    );

    await store.load();

    // Coercing "lots" to a number would put a fabricated percentage on screen.
    expect(store.readings(NOW)).toEqual([]);
  });
});

describe("ClaudeRateLimitUsageProvider", () => {
  it("claims claude and nothing else", () => {
    const provider = new ClaudeRateLimitUsageProvider(new ClaudeRateLimitStore(workspace));

    expect(provider.supports("claude")).toBe(true);
    expect(provider.supports("codex")).toBe(false);
    expect(provider.supports("copilot")).toBe(false);
  });

  it("says nothing until a rate_limit_event has been observed", async () => {
    // The API-key (pay-per-token) path on the same adapter id never emits the
    // envelope. Returning null is what lets the dollar windows answer for it —
    // the plan kind follows the observed signal, not the adapter name.
    const provider = new ClaudeRateLimitUsageProvider(new ClaudeRateLimitStore(workspace));

    expect(await provider.getSnapshot("claude")).toBeNull();
  });

  it("reports a subscription-window plan once an event has been observed", async () => {
    const store = new ClaudeRateLimitStore(workspace);
    await store.record(event({ utilization: 44 }), NOW);

    const snapshot = (await new ClaudeRateLimitUsageProvider(store).getSnapshot("claude"))!;

    expect(snapshot.plan.kind).toBe("subscription-window");
    expect(snapshot.adapter).toBe("claude");
    expect(snapshot.windows).toHaveLength(1);
    expect(snapshot.windows[0].used).toBe(44);
    expect(snapshot.windows[0].confidence).toBe("measured");
  });

  it("serves the persisted reading at rest, downgraded and stamped", async () => {
    const observedAt = new Date(NOW.getTime() - 25 * 60 * 1000);
    const store = new ClaudeRateLimitStore(workspace);
    await store.record(event({ utilization: 44 }), observedAt);
    // The run that reported it has ended — PipelineBridge's `finally`.
    store.settle();

    const snapshot = (await new ClaudeRateLimitUsageProvider(store).getSnapshot("claude"))!;

    expect(snapshot.windows[0].used).toBe(44);
    expect(snapshot.windows[0].confidence).toBe("estimated");
    expect(snapshot.windows[0].observedAt).toEqual(observedAt);
  });

  it("drops a bucket whose window has already refilled", async () => {
    const store = new ClaudeRateLimitStore(workspace);
    store.record(event({ rateLimitType: "five_hour", resetsAt: RESETS_EARLIER }), NOW);
    await store.record(
      event({ rateLimitType: "seven_day", utilization: 12, resetsAt: RESETS_LATER }),
      NOW
    );

    const snapshot = (await new ClaudeRateLimitUsageProvider(store).getSnapshot("claude"))!;

    // The five-hour bucket refilled an hour ago, so its 44% is known-wrong
    // rather than stale. Nightgauge cannot know the post-reset figure (the
    // user may have spent it outside nightgauge), so it reports nothing for it.
    expect(snapshot.windows.map((w) => w.scope)).toEqual(["weekly"]);
  });

  it("returns null — handing the adapter back — once every bucket has refilled", async () => {
    const store = new ClaudeRateLimitStore(workspace);
    await store.record(event({ resetsAt: RESETS_EARLIER }), NOW);

    expect(await new ClaudeRateLimitUsageProvider(store).getSnapshot("claude")).toBeNull();
  });

  it("orders windows shortest first, so the meter opens on the five-hour one", async () => {
    const store = new ClaudeRateLimitStore(workspace);
    store.record(event({ rateLimitType: "seven_day", utilization: 12 }), NOW);
    store.record(event({ rateLimitType: "daily", utilization: 30 }), NOW);
    await store.record(event({ rateLimitType: "five_hour", utilization: 44 }), NOW);

    const snapshot = (await new ClaudeRateLimitUsageProvider(store).getSnapshot("claude"))!;

    expect(snapshot.windows.map((w) => w.scope)).toEqual(["rolling", "daily", "weekly"]);
  });
});
