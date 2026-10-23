/**
 * Tests for autonomousContextKeys (#3309) — verifies the helper sets the two
 * VSCode `setContext` keys that drive the pipeline-view toolbar so that the
 * Resume button is always visible when the user can recover.
 */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const executeCommand = vi.fn();

vi.mock("vscode", () => ({
  commands: {
    executeCommand: (...args: unknown[]) => executeCommand(...args),
  },
}));

import { setAutonomousContextKeys, _classifyForTests } from "../../src/utils/autonomousContextKeys";

beforeEach(() => {
  executeCommand.mockReset();
});

afterEach(() => {
  vi.clearAllMocks();
});

function getContextCalls(): Record<string, boolean> {
  const calls: Record<string, boolean> = {};
  for (const call of executeCommand.mock.calls) {
    if (call[0] === "setContext" && typeof call[1] === "string" && typeof call[2] === "boolean") {
      calls[call[1]] = call[2];
    }
  }
  return calls;
}

describe("setAutonomousContextKeys — toolbar visibility matrix (#3309)", () => {
  it("running → autonomousRunning=true, autonomousResumable=false (Pause + Stop)", () => {
    setAutonomousContextKeys("running");
    const ctx = getContextCalls();
    expect(ctx["nightgauge.autonomousRunning"]).toBe(true);
    expect(ctx["nightgauge.autonomousResumable"]).toBe(false);
  });

  it("paused → autonomousRunning=false, autonomousResumable=true (Resume + Stop)", () => {
    setAutonomousContextKeys("paused");
    const ctx = getContextCalls();
    expect(ctx["nightgauge.autonomousRunning"]).toBe(false);
    expect(ctx["nightgauge.autonomousResumable"]).toBe(true);
  });

  it("safety_tripped → autonomousResumable=true so Resume button is visible", () => {
    // The original bug: safety trip left users with no visible button. The
    // command-palette Resume entry exists but is not discoverable. Surfacing
    // a Resume button is the whole point of #3309.
    setAutonomousContextKeys("safety_tripped");
    const ctx = getContextCalls();
    expect(ctx["nightgauge.autonomousRunning"]).toBe(false);
    expect(ctx["nightgauge.autonomousResumable"]).toBe(true);
  });

  it("stopped → both flags false (Run button visible, Pickup Issue visible)", () => {
    setAutonomousContextKeys("stopped");
    const ctx = getContextCalls();
    expect(ctx["nightgauge.autonomousRunning"]).toBe(false);
    expect(ctx["nightgauge.autonomousResumable"]).toBe(false);
  });

  it("complete → both flags false (cold-start state)", () => {
    setAutonomousContextKeys("complete");
    const ctx = getContextCalls();
    expect(ctx["nightgauge.autonomousRunning"]).toBe(false);
    expect(ctx["nightgauge.autonomousResumable"]).toBe(false);
  });

  it("budget_exhausted → both flags false (manual restart required)", () => {
    setAutonomousContextKeys("budget_exhausted");
    const ctx = getContextCalls();
    expect(ctx["nightgauge.autonomousRunning"]).toBe(false);
    expect(ctx["nightgauge.autonomousResumable"]).toBe(false);
  });

  it("crashed → both flags false (manual Run after recovery)", () => {
    setAutonomousContextKeys("crashed");
    const ctx = getContextCalls();
    expect(ctx["nightgauge.autonomousRunning"]).toBe(false);
    expect(ctx["nightgauge.autonomousResumable"]).toBe(false);
  });

  it("init → both flags false (transitional state)", () => {
    setAutonomousContextKeys("init");
    const ctx = getContextCalls();
    expect(ctx["nightgauge.autonomousRunning"]).toBe(false);
    expect(ctx["nightgauge.autonomousResumable"]).toBe(false);
  });

  it("running and resumable are mutually exclusive across every status", () => {
    // Invariant: Run/Resume share toolbar slot @0, so they MUST be mutually
    // exclusive. If both ever became true at the same time the user would
    // see two competing primary buttons.
    const statuses = [
      "running",
      "paused",
      "safety_tripped",
      "stopped",
      "complete",
      "budget_exhausted",
      "crashed",
      "init",
      "cancelled",
      "unknown_future_status", // must default safely
    ];
    for (const status of statuses) {
      const c = _classifyForTests(status);
      expect(c.running && c.resumable, `status=${status}`).toBe(false);
    }
  });

  it("unknown statuses fall through to both-false (Run button visible)", () => {
    setAutonomousContextKeys("some-status-from-the-future");
    const ctx = getContextCalls();
    expect(ctx["nightgauge.autonomousRunning"]).toBe(false);
    expect(ctx["nightgauge.autonomousResumable"]).toBe(false);
  });
});
