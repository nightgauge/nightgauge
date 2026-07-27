/**
 * progressMonitor.phaseMarkerFeed.test.ts
 *
 * Issue #161, second defect: phase markers are a PRODUCTIVE signal, but only
 * the assistant-text branch of skillRunner's stream handler recorded one.
 *
 * Every skill emits its markers with `printf '<!-- phase:start ... -->'`, and
 * skillRunner's own comment names the tool_result branch as "the SOLE
 * detection channel for printf-emitted markers." That branch updated the phase
 * tracker, the trace recorder, and the UI callback — but never fed the runaway
 * monitor. The productive window therefore advanced on a marker stream that
 * does not exist in practice.
 *
 * The cost, from `bowlsheet-infra#186`'s exit record: a `feature-validate`
 * stage that had walked to phase 18 of 23 was recorded with 4 productive
 * signals over 40 minutes and killed for "no productive progress for 250s"
 * while running its commit-and-push phase.
 *
 * The wiring itself lives inside a subprocess-spawning function that cannot be
 * exercised cheaply, so these tests pin (a) the behavioural contract that a
 * printf-shaped marker advances the window, and (b) the wiring itself, read
 * from source — the same approach `IpcContract.coverage.test.ts` uses for the
 * IPC surface.
 */

import { readFileSync } from "node:fs";
import { join } from "node:path";
import { describe, it, expect, vi, afterEach } from "vitest";
import { parsePhaseMarkers } from "@nightgauge/sdk";
import { ProgressMonitor, type ProgressMonitorConfig } from "../../src/utils/progressMonitor";

/** The literal tool_result payload a skill's printf produces. */
const PRINTF_MARKER_RESULT =
  '<!-- phase:start name="commit-and-push" index=18 total=23 stage="feature-validate" -->\n';

function makeConfig(overrides: Partial<ProgressMonitorConfig> = {}): ProgressMonitorConfig {
  return {
    enabled: true,
    noProgressWindowMs: 120_000,
    minCostToActivateUsd: 0.5,
    catastrophicLimitUsd: 200,
    observeOnly: false,
    churnToolThreshold: 40,
    catastrophicKill: false,
    ...overrides,
  };
}

afterEach(() => {
  vi.useRealTimers();
});

describe("printf-emitted phase markers advance the productive window (#161)", () => {
  it("parses the marker out of a tool_result payload", () => {
    const markers = parsePhaseMarkers(PRINTF_MARKER_RESULT);
    expect(markers).toHaveLength(1);
    expect(markers[0].name).toBe("commit-and-push");
    expect(markers[0].index).toBe(18);
  });

  it("keeps a marker-emitting stage alive past the no-progress window", () => {
    vi.useFakeTimers();
    const monitor = new ProgressMonitor(makeConfig());

    // 100s in: nothing productive yet, still inside the window.
    vi.advanceTimersByTime(100_000);
    expect(monitor.check(5).shouldKill).toBe(false);

    // The stage prints its next phase marker — real forward motion.
    for (const _marker of parsePhaseMarkers(PRINTF_MARKER_RESULT)) {
      monitor.recordSignal("phase_marker");
    }

    // 100s later the window would have elapsed had the marker been dropped;
    // because it was recorded, the productive clock restarted.
    vi.advanceTimersByTime(100_000);
    expect(monitor.check(5).shouldKill).toBe(false);
    expect(monitor.hasObservedProductiveProgress).toBe(true);
  });

  it("would have been killed without the marker — the pre-fix behaviour", () => {
    // The counterfactual, so the test above is not vacuously true.
    vi.useFakeTimers();
    const monitor = new ProgressMonitor(makeConfig());
    vi.advanceTimersByTime(200_000);
    expect(monitor.check(5).shouldKill).toBe(true);
  });
});

describe("skillRunner wiring: the tool_result branch feeds the monitor (#161)", () => {
  it("records a phase_marker signal on the printf detection channel", () => {
    const source = readFileSync(
      join(__dirname, "..", "..", "src", "utils", "skillRunner.ts"),
      "utf8"
    );

    // Locate the tool_result phase-marker branch and assert the monitor is fed
    // inside it. Before #161 this branch updated phaseInference, traceRecorder
    // and the UI callback but never the monitor, so the fix is invisible to
    // any test that only checks that SOME branch records the signal.
    const branchStart = source.indexOf('parsed?.type === "user" && parsed.toolResult?.content');
    expect(branchStart, "tool_result phase-marker branch not found").toBeGreaterThan(-1);

    const branch = source.slice(branchStart, branchStart + 1600);
    expect(
      branch.includes('progressMonitor.recordSignal("phase_marker")'),
      "the printf/tool_result phase-marker channel must feed the runaway monitor"
    ).toBe(true);
  });
});
