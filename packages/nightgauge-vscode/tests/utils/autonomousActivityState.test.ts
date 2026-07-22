/**
 * AutonomousActivityState — the in-process gate that keeps the tree providers
 * from polling GitHub in the background when autonomous mode is off (#360).
 */
import { describe, it, expect, beforeEach, afterEach } from "vitest";
import { AutonomousActivityState } from "../../src/utils/autonomousActivityState";

// AutonomousActivityState is deliberately self-contained (its own listener set,
// no vscode.EventEmitter), so these tests need no vscode mock — its onDidChange
// delivers for real.

describe("AutonomousActivityState (#360)", () => {
  beforeEach(() => AutonomousActivityState.resetForTests());
  afterEach(() => AutonomousActivityState.resetForTests());

  it("starts inactive so an idle workspace makes no background traffic", () => {
    expect(AutonomousActivityState.instance.isActive()).toBe(false);
  });

  it("is active only for the 'running' dispatch status", () => {
    const s = AutonomousActivityState.instance;
    s.setStatus("running");
    expect(s.isActive()).toBe(true);

    for (const status of ["paused", "safety_tripped", "stopped", "complete", "crashed", "init"]) {
      s.setStatus(status);
      expect(s.isActive()).toBe(false);
    }
  });

  it("fires onDidChange only when the active-state actually flips", () => {
    const s = AutonomousActivityState.instance;
    const seen: boolean[] = [];
    s.onDidChange((v) => seen.push(v));

    s.setStatus("running"); // false -> true  (fire)
    s.setStatus("running"); // true  -> true  (no fire)
    s.setStatus("paused"); //  true  -> false (fire)
    s.setStatus("stopped"); // false -> false (no fire)

    expect(seen).toEqual([true, false]);
  });
});
