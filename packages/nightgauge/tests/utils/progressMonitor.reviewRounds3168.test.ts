/**
 * progressMonitor.reviewRounds3168.test.ts
 *
 * Issue #3168: feature-validate's Step 2.7.3 loop (review prepare → reviewer →
 * record → fix → prepare) emitted no productive signal. Fixes re-edit existing
 * paths (churn), nothing is committed, and the loop sits inside one phase, so
 * a three-round review crossed `churnToolThreshold` and was killed as #3811
 * churn. Each round's prepare/run block now re-prints the phase marker.
 */

import { readFileSync } from "node:fs";
import { join } from "node:path";
import { describe, it, expect, vi, afterEach } from "vitest";
import { parsePhaseMarkers, PHASE_REGISTRY } from "@nightgauge/sdk";
import { ProgressMonitor, type ProgressMonitorConfig } from "../../src/utils/progressMonitor";

const SKILL_DIR = join(__dirname, "..", "..", "..", "..", "skills", "nightgauge-feature-validate");
const INCLUDE = readFileSync(join(SKILL_DIR, "_includes", "ci-and-knowledge.md"), "utf8");

/** The bash fence that contains `needle`. */
function blockContaining(needle: string): string {
  const at = INCLUDE.indexOf(needle);
  expect(at, `${needle} not found in ci-and-knowledge.md`).toBeGreaterThan(-1);
  const start = INCLUDE.lastIndexOf("```bash", at);
  const end = INCLUDE.indexOf("```", at);
  return INCLUDE.slice(start, end);
}

const PREPARE_BLOCK = blockContaining('"$BINARY" review prepare');
const RUN_BLOCK = blockContaining('"$BINARY" review run');

function makeConfig(): ProgressMonitorConfig {
  return {
    enabled: true,
    noProgressWindowMs: 120_000,
    minCostToActivateUsd: 0.5,
    catastrophicLimitUsd: 200,
    observeOnly: false,
    churnToolThreshold: 40,
    catastrophicKill: false,
    externalProgressCeilingMs: 1_200_000,
  };
}

const EDITED = ["a.go", "b.go", "c.ts"];
const COST = 5;

/** Three review rounds over ~7 minutes; returns whether any check killed. */
function runReview(withMarkers: boolean): { killed: boolean; ceiling?: string } {
  vi.useFakeTimers();
  const monitor = new ProgressMonitor(makeConfig());
  for (const p of EDITED) monitor.recordSignal("file_change", p);
  let tool = 0;
  for (let round = 0; round < 3; round++) {
    if (withMarkers) {
      for (const _m of parsePhaseMarkers(PREPARE_BLOCK)) monitor.recordSignal("phase_marker");
    }
    for (let i = 0; i < 17; i++) {
      vi.advanceTimersByTime(8_000);
      monitor.recordSignal("distinct_tool", `tool-${tool++}`);
      if (i % 4 === 0) monitor.recordSignal("file_change", EDITED[i % EDITED.length]);
      const r = monitor.check(COST);
      if (r.shouldKill) return { killed: true, ceiling: r.ceiling?.name };
    }
  }
  return { killed: false };
}

afterEach(() => {
  vi.useRealTimers();
});

describe("independent review rounds emit a productive signal (#3168)", () => {
  it("a three-round review with fixes is not killed", () => {
    expect(runReview(true).killed).toBe(false);
  });

  it("the same stream without round markers is killed as churn (#3811 holds)", () => {
    const r = runReview(false);
    expect(r.killed).toBe(true);
    expect(r.ceiling).toBe("progress-churn-tools");
  });

  it.each([
    ["review prepare", PREPARE_BLOCK],
    ["review run", RUN_BLOCK],
  ])("the %s block prints a registered feature-validate marker", (_label, block) => {
    const markers = parsePhaseMarkers(block);
    expect(markers).toHaveLength(1);
    const m = markers[0];
    expect(m.stage).toBe("feature-validate");
    expect(PHASE_REGISTRY["feature-validate"]).toContainEqual(
      expect.objectContaining({ name: m.name, index: m.index })
    );
  });

  it.each(["SKILL.md", join("_profiles", "compact.md")])(
    "%s routes Step 2.7.3 through the shared include",
    (file) => {
      const text = readFileSync(join(SKILL_DIR, file), "utf8");
      expect(text).toMatch(/_includes\/ci-and-knowledge\.md[^\n]*Step 2\.7\.3/);
    }
  );
});
