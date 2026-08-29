/**
 * Every stage skill's phase markers must match the registry (#1121).
 *
 * The registry is the denominator the product actually shows — the GUI's
 * "baseline-comparison 9/23" is read from it. The skills are what emit the
 * markers at run time. Nothing checked that the two agreed, and they drifted:
 * `feature-dev` declared `e2e-testing` at index 10 **twice**, in a "Step 4.5"
 * and again in a "Phase 4b", both reading the same include. A stage that
 * emitted both would double-record index 10 in `phaseHistory` — the same
 * double-count class already guarded against elsewhere (#217).
 *
 * This pins names, order, indices, totals and uniqueness in one place, so a
 * skill edit that adds or renames a phase without updating the registry fails
 * here rather than surfacing as a wrong number on someone's screen.
 */

import { describe, it, expect } from "vitest";
import { readFileSync, existsSync } from "node:fs";
import { join } from "node:path";
import { PHASE_REGISTRY } from "../../src/events/phaseRegistry";

const REPO_ROOT = join(__dirname, "../../../..");

interface Marker {
  name: string;
  index: number;
  total: number;
}

function markersFor(stage: string): Marker[] {
  const path = join(REPO_ROOT, "skills", `nightgauge-${stage}`, "SKILL.md");
  if (!existsSync(path)) return [];
  const md = readFileSync(path, "utf8");
  return [...md.matchAll(/phase:start name="([^"]+)" index=(\d+) total=(\d+)/g)].map((m) => ({
    name: m[1],
    index: Number(m[2]),
    total: Number(m[3]),
  }));
}

const stages = Object.keys(PHASE_REGISTRY).filter((s) =>
  existsSync(join(REPO_ROOT, "skills", `nightgauge-${s}`, "SKILL.md"))
);

describe("stage skills vs phase registry (#1121)", () => {
  it("finds a SKILL.md for every registry stage", () => {
    // A stage silently missing its skill would make every assertion below
    // vacuous, so assert the set rather than iterating an empty list.
    expect(stages.sort()).toEqual(Object.keys(PHASE_REGISTRY).sort());
  });

  it.each(stages)("%s: emits each phase exactly once, in registry order", (stage) => {
    const expected = [...PHASE_REGISTRY[stage as keyof typeof PHASE_REGISTRY]]
      .sort((a, b) => a.index - b.index)
      .map((p) => p.name);
    const markers = markersFor(stage);

    // Uniqueness first: a duplicate is what actually happened, and it makes
    // the order assertion below fail in a way that hides the cause.
    const dupes = markers.map((m) => m.index).filter((idx, i, all) => all.indexOf(idx) !== i);
    expect(dupes, `${stage} declares these phase indices more than once`).toEqual([]);

    expect(markers.map((m) => m.name)).toEqual(expected);
    expect(markers.map((m) => m.index)).toEqual(expected.map((_, i) => i));
  });

  it.each(stages)("%s: every marker reports the registry's total", (stage) => {
    const total = PHASE_REGISTRY[stage as keyof typeof PHASE_REGISTRY].length;
    const totals = [...new Set(markersFor(stage).map((m) => m.total))];
    expect(totals).toEqual([total]);
  });
});
