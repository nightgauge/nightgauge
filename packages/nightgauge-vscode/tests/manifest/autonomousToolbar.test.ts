/**
 * Tests for the pipeline-view toolbar manifest entries (#3309).
 *
 * Pins the visibility matrix that drives the autonomous control buttons:
 *
 *   stopped/complete/budget_exhausted/crashed → Run + Pickup Issue
 *   running                                    → Pause + Stop
 *   paused/safety_tripped                      → Resume + Stop
 *
 * Without these assertions a future refactor of `when` clauses could silently
 * regress to the old "no Resume button" state that #3309 fixed.
 */

import { describe, it, expect } from "vitest";
import { MANIFEST_CONTRIBUTES } from "../../src/manifest";

interface MenuEntry {
  command: string;
  when?: string;
  group?: string;
}

const viewTitleMenu = MANIFEST_CONTRIBUTES.menus["view/title"] as MenuEntry[];

function findEntry(command: string): MenuEntry {
  const entry = viewTitleMenu.find(
    (m) => m.command === command && m.when?.includes("nightgauge\\.pipeline")
  );
  if (!entry) throw new Error(`view/title entry for ${command} not found`);
  return entry;
}

describe("Pipeline-view autonomous toolbar (#3309)", () => {
  it("Run button is hidden when autonomous is resumable (must not compete with Resume)", () => {
    const run = findEntry("nightgauge.autonomousRun");
    expect(run.when).toContain("!nightgauge.autonomousResumable");
    expect(run.when).toContain("!nightgauge.autonomousRunning");
    expect(run.when).toContain("!nightgauge.pipelineRunning");
  });

  it("Resume button is visible whenever autonomous is resumable (paused/safety_tripped)", () => {
    const resume = findEntry("nightgauge.autonomousResume");
    expect(resume.when).toContain("nightgauge.autonomousResumable");
    expect(resume.when).not.toContain("!nightgauge.autonomousResumable");
  });

  it("Pause button is visible only while autonomous is actively running", () => {
    const pause = findEntry("nightgauge.autonomousPause");
    expect(pause.when).toContain("nightgauge.autonomousRunning");
    expect(pause.when).not.toContain("autonomousResumable");
  });

  it("Stop button stays visible across both running and resumable states", () => {
    const stop = findEntry("nightgauge.autonomousStop");
    expect(stop.when).toContain("nightgauge.autonomousRunning");
    expect(stop.when).toContain("nightgauge.autonomousResumable");
    // Disjunction (OR), not conjunction.
    expect(stop.when).toContain("||");
  });

  it("Run and Resume share the same toolbar slot (mutually exclusive)", () => {
    const run = findEntry("nightgauge.autonomousRun");
    const resume = findEntry("nightgauge.autonomousResume");
    expect(run.group).toBe("navigation@0");
    expect(resume.group).toBe("navigation@0");
  });

  it("Pause and Stop share the same toolbar slot (related controls)", () => {
    const pause = findEntry("nightgauge.autonomousPause");
    const stop = findEntry("nightgauge.autonomousStop");
    expect(pause.group).toBe("navigation@1");
    expect(stop.group).toBe("navigation@1");
  });

  it("Pickup Issue button hides when autonomous is resumable (avoid distracting recovery flow)", () => {
    const pickup = findEntry("nightgauge.pickupIssue");
    expect(pickup.when).toContain("!nightgauge.autonomousResumable");
  });

  it("All four autonomous control commands are registered with intuitive icons", () => {
    const commands = MANIFEST_CONTRIBUTES.commands as Array<{
      command: string;
      icon?: string;
      title: string;
    }>;
    const named = (cmd: string) => commands.find((c) => c.command === cmd);

    const run = named("nightgauge.autonomousRun");
    const pause = named("nightgauge.autonomousPause");
    const resume = named("nightgauge.autonomousResume");
    const stop = named("nightgauge.autonomousStop");

    expect(run?.icon).toBeTruthy();
    expect(pause?.icon).toBe("$(debug-pause)");
    expect(resume?.icon).toBe("$(debug-continue)");
    expect(stop?.icon).toBe("$(debug-stop)");

    // Titles use the "Autonomous: <Verb>" pattern so the command palette
    // groups them alphabetically and the tooltip is self-descriptive.
    expect(run?.title).toMatch(/^Autonomous: /);
    expect(pause?.title).toMatch(/^Autonomous: /);
    expect(resume?.title).toMatch(/^Autonomous: /);
    expect(stop?.title).toMatch(/^Autonomous: /);
  });
});
