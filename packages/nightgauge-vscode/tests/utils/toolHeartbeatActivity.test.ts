/**
 * #1083 — a stage was killed for inactivity while a tool call was in flight.
 *
 * The adapter emits a heartbeat every ~30s for a running tool call. Nothing
 * consumed it: `tool_progress` was absent from the parser's type union, so
 * `parseStreamJsonLine` returned null and neither ProgressMonitor clock ever
 * saw it. `msSinceLastActivity` advances only on a NOVEL tool signature, and a
 * stage waiting inside ONE long call produces none — so its activity clock went
 * cold precisely because it was waiting correctly rather than churning.
 *
 * Observed: killed at 3001s, 171 seconds after the stage's suite reported 1436
 * tests passing, `idle_ms_at_exit: 355`.
 */
import { describe, it, expect } from "vitest";
import { parseStreamJsonLine } from "../../src/utils/tokenParser";
import { ProgressMonitor } from "../../src/utils/progressMonitor";

const heartbeat = (id: string, elapsed: number, name = "TaskOutput") =>
  JSON.stringify({
    type: "tool_progress",
    tool_use_id: id,
    tool_name: name,
    elapsed_time_seconds: elapsed,
    heartbeat: true,
  });

describe("#1083 — the parser surfaces tool-call heartbeats", () => {
  it("parses a heartbeat instead of dropping it", () => {
    const parsed = parseStreamJsonLine(heartbeat("toolu_013g", 240));
    expect(parsed).not.toBeNull();
    expect(parsed!.type).toBe("tool_progress");
    expect(parsed!.toolProgress).toEqual({
      toolUseId: "toolu_013g",
      toolName: "TaskOutput",
      elapsedSeconds: 240,
    });
  });

  // An unattributable heartbeat must not be able to defer a kill: without an
  // id there is no call whose elapsed time could ever cross the ceiling.
  it("drops a heartbeat with no tool_use_id", () => {
    expect(
      parseStreamJsonLine(JSON.stringify({ type: "tool_progress", elapsed_time_seconds: 30 }))
    ).toBeNull();
  });
});

describe("#1083 — a heartbeat is activity, never progress", () => {
  function monitor(): ProgressMonitor {
    return new ProgressMonitor({ noProgressWindowMs: 60_000, activityWindowMs: 60_000 } as never);
  }

  it("resets the activity clock", async () => {
    const m = monitor();
    await new Promise((r) => setTimeout(r, 25));
    const before = m.msSinceLastActivity;
    m.recordSignal("tool_heartbeat");
    expect(m.msSinceLastActivity).toBeLessThan(before);
  });

  // The whole point: repeated heartbeats for ONE waiting call must each count.
  // `distinct_tool` dedups on signature and would swallow every one after the
  // first — which is the hole the stage fell through.
  it("is not deduplicated the way a distinct tool signature is", async () => {
    const m = monitor();
    for (let i = 0; i < 5; i++) {
      m.recordSignal("tool_heartbeat");
    }
    await new Promise((r) => setTimeout(r, 20));
    m.recordSignal("tool_heartbeat");
    expect(m.msSinceLastActivity).toBeLessThan(20);
  });

  it("never advances the productive-progress window", async () => {
    const m = monitor();
    await new Promise((r) => setTimeout(r, 25));
    const productiveBefore = m.msSinceLastProductiveProgress;
    m.recordSignal("tool_heartbeat");
    // Liveness gates the kill; it must never satisfy it.
    expect(m.msSinceLastProductiveProgress).toBeGreaterThanOrEqual(productiveBefore);
  });

  it("a real productive signal still advances both clocks", () => {
    const m = monitor();
    m.recordSignal("commit");
    expect(m.msSinceLastProductiveProgress).toBeLessThan(20);
    expect(m.msSinceLastActivity).toBeLessThan(20);
  });
});
