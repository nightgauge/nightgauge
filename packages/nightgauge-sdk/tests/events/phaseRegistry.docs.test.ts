/**
 * The docs table must match the phase registry (#1121).
 *
 * `docs/ARCHITECTURE.md` carries a table of every stage's phases. Nothing
 * checked it, so it drifted silently and badly: it claimed "All 64 phases
 * across 6 stages" while the registry held 97, and every one of the six counts
 * was wrong (issue-pickup 9 vs 14, feature-planning 7 vs 14, feature-dev 14 vs
 * 18, feature-validate 14 vs 23, pr-create 5 vs 14, pr-merge 10 vs 14). It also
 * listed phases that do not exist (`parallel-quality-checks`) and omitted ones
 * that do (`freshness-check`, `verify-ui-gate`, `ci-parity-check`, …).
 *
 * That is not a cosmetic problem: a maintainer reading the table to reason
 * about a run's progress gets a denominator the product never uses. The
 * registry is the single source of truth — the GUI's "9/23" comes from it —
 * so this test pins the doc to it.
 */

import { describe, it, expect } from "vitest";
import { readFileSync } from "node:fs";
import { join } from "node:path";
import { PHASE_REGISTRY } from "../../src/events/phaseRegistry";

const DOC = join(__dirname, "../../../../docs/ARCHITECTURE.md");

/** Parse the `| \`stage\` | N | \`a\`, \`b\`, … |` rows out of the doc table. */
function parseDocTable(md: string): Record<string, { count: number; phases: string[] }> {
  const out: Record<string, { count: number; phases: string[] }> = {};
  const row = /^\|\s*`([a-z-]+)`\s*\|\s*(\d+)\s*\|\s*(.+?)\s*\|$/gm;
  let m: RegExpExecArray | null;
  while ((m = row.exec(md))) {
    const phases = [...m[3].matchAll(/`([^`]+)`/g)].map((p) => p[1]);
    out[m[1]] = { count: Number(m[2]), phases };
  }
  return out;
}

describe("ARCHITECTURE.md phase table (#1121)", () => {
  const md = readFileSync(DOC, "utf8");
  const table = parseDocTable(md);
  const stages = Object.keys(PHASE_REGISTRY);

  it("documents every stage in the registry, and no others", () => {
    expect(Object.keys(table).sort()).toEqual(stages.sort());
  });

  it.each(stages)("%s: names and order match the registry exactly", (stage) => {
    const expected = [...PHASE_REGISTRY[stage as keyof typeof PHASE_REGISTRY]]
      .sort((a, b) => a.index - b.index)
      .map((p) => p.name);

    expect(table[stage], `no row for ${stage}`).toBeDefined();
    expect(table[stage].phases).toEqual(expected);
    expect(table[stage].count).toBe(expected.length);
  });

  it("states the correct grand total", () => {
    const total = stages.reduce(
      (n, s) => n + PHASE_REGISTRY[s as keyof typeof PHASE_REGISTRY].length,
      0
    );
    // The old table said 64 while the registry held 97.
    expect(md).toContain(`All ${total} phases across ${stages.length} stages`);
  });
});
