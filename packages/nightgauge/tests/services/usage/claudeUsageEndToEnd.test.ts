/**
 * End-to-end: a `rate_limit_event` on the wire becomes a percentage in the
 * status bar (Issue #709).
 *
 * Every unit in the chain is tested on its own elsewhere. What this file
 * pins is that they are actually joined up, using the **production** parser,
 * store, provider, registry and formatter rather than fakes — the failure mode
 * where each piece works and nothing routes between them is exactly what this
 * ticket was filed to fix (the signal has been parsed and discarded for a long
 * time).
 *
 * The one seam left out is `PipelineBridge`, which owns the two calls this
 * file makes by hand: `record()` inside its `onRateLimitEvent` callback and
 * `settle()` in the `finally` of `handleRunStage`. Standing a PipelineBridge up
 * needs an IPC client and a live VS Code host, so those two calls are asserted
 * by inspection there and simulated here.
 */

import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import * as fs from "node:fs/promises";
import * as os from "node:os";
import * as path from "node:path";

import { parseStreamJsonLine } from "../../../src/utils/tokenParser";
import { ClaudeRateLimitStore } from "../../../src/services/usage/ClaudeRateLimitStore";
import { ClaudeRateLimitUsageProvider } from "../../../src/services/usage/ClaudeRateLimitUsageProvider";
import { LocalTelemetryUsageProvider } from "../../../src/services/usage/LocalTelemetryUsageProvider";
import {
  AdapterUsageService,
  UsageProviderRegistry,
} from "../../../src/services/usage/AdapterUsageService";
import { formatUsageWindowText, renderUsageBar } from "../../../src/utils/statusBar";
import { resetMockConfigBridge } from "../../setup";

/** Fixed clock, so the rendered reset countdown is an exact string. */
const NOW = new Date(2026, 7, 18, 10, 0, 0);
/** 2h 14m after NOW, in the unix seconds the wire format carries. */
const RESETS_AT = Math.floor(NOW.getTime() / 1000) + 2 * 60 * 60 + 14 * 60;

/**
 * A real line off `claude -p --output-format stream-json`, in the nested
 * `rate_limit_info` shape the CLI emits today.
 */
const WIRE_LINE = JSON.stringify({
  type: "rate_limit_event",
  rate_limit_info: {
    resetsAt: RESETS_AT,
    rateLimitType: "five_hour",
    utilization: 44,
    status: "allowed",
    isUsingOverage: false,
  },
});

let workspace: string;

beforeEach(async () => {
  resetMockConfigBridge();
  workspace = await fs.mkdtemp(path.join(os.tmpdir(), "ng-claude-e2e-"));
  vi.useFakeTimers();
  vi.setSystemTime(NOW);
});

afterEach(async () => {
  vi.useRealTimers();
  resetMockConfigBridge();
  await fs.rm(workspace, { recursive: true, force: true });
});

/** The production registry composition: subscription first, dollars behind it. */
function serviceFor(store: ClaudeRateLimitStore): AdapterUsageService {
  const registry = new UsageProviderRegistry()
    .register(new ClaudeRateLimitUsageProvider(store))
    .register(
      new LocalTelemetryUsageProvider(
        { readDateRange: async () => [] },
        {
          getSessionStartTime: () => NOW,
        }
      )
    );
  return new AdapterUsageService(registry, { resolveAdapter: () => "claude" });
}

describe("rate_limit_event → status bar (Issue #709)", () => {
  it("carries a mid-run reading all the way to the rendered meter", async () => {
    // 1. The CLI emits the envelope; the parser nightgauge already had reads it.
    const parsed = parseStreamJsonLine(WIRE_LINE);
    expect(parsed?.type).toBe("rate_limit_event");

    // 2. PipelineBridge's onRateLimitEvent hands it to the store.
    const store = new ClaudeRateLimitStore(workspace);
    // Awaited so the queued write cannot outlive the test and race the
    // temp-directory teardown. `record` updates memory synchronously, so
    // this changes nothing about what the assertions below observe.
    await store.record(parsed!.rateLimitEvent!, NOW);

    // 3. The service resolves the subscription provider ahead of the dollar one.
    const snapshot = await serviceFor(store).getSnapshot();

    expect(snapshot.plan.kind).toBe("subscription-window");
    expect(snapshot.windows[0].unit).toBe("percent");
    expect(snapshot.windows[0].confidence).toBe("measured");

    // 4. The status bar renders it. This literal string is the acceptance
    //    criterion: a Max user sees utilization and a refill time, not "$4.12
    //    this session".
    expect(formatUsageWindowText(snapshot.adapter, snapshot.windows[0], NOW)).toBe(
      `$(flame) claude session (5h) ${renderUsageBar(44)} 44% · 56% left · resets 2h 14m`
    );
  });

  it("still answers at rest, from disk, after the run and the process are gone", async () => {
    const observedAt = new Date(NOW.getTime() - 25 * 60 * 1000);
    const writer = new ClaudeRateLimitStore(workspace);
    await writer.record(parseStreamJsonLine(WIRE_LINE)!.rateLimitEvent!, observedAt);
    // The stage ended — PipelineBridge's `finally`.
    writer.settle();

    // A different store instance stands in for the next VS Code window.
    const snapshot = await serviceFor(new ClaudeRateLimitStore(workspace)).getSnapshot();

    expect(snapshot.plan.kind).toBe("subscription-window");
    expect(snapshot.windows[0].confidence).toBe("estimated");
    expect(formatUsageWindowText(snapshot.adapter, snapshot.windows[0], NOW)).toBe(
      `$(flame) claude session (5h) ${renderUsageBar(44)} 44% · 56% left · resets 2h 14m · as of 09:35`
    );
  });

  it("falls back to the dollar meter when no event has ever been observed", async () => {
    // An API-key (pay-per-token) Claude user emits no rate_limit_event, so the
    // subscription provider must not swallow the adapter. With no history
    // either, local telemetry also declines and the honest answer is "unknown"
    // — never a fabricated 0%.
    const snapshot = await serviceFor(new ClaudeRateLimitStore(workspace)).getSnapshot();

    expect(snapshot.plan.kind).toBe("unknown");
    expect(snapshot.windows).toEqual([]);
  });

  it("stops reporting a window once it has refilled, rather than serving a known-wrong number", async () => {
    const store = new ClaudeRateLimitStore(workspace);
    await store.record(parseStreamJsonLine(WIRE_LINE)!.rateLimitEvent!, NOW);
    store.settle();

    // Jump past the bucket's own reset. The 44% described a window that has
    // since refilled; nightgauge cannot know the new figure, so it says nothing.
    vi.setSystemTime(new Date(RESETS_AT * 1000 + 60_000));
    const snapshot = await serviceFor(store).getSnapshot();

    expect(snapshot.plan.kind).toBe("unknown");
    expect(snapshot.windows).toEqual([]);
  });
});
