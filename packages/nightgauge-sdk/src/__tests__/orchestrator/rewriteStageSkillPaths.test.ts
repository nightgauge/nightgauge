/**
 * Trailing-separator handling in `rewriteStageSkillPaths`.
 *
 * The skill directory came from caller-supplied configuration and was trimmed
 * with an end-anchored `/[/\\]+$/`, which CodeQL flagged as quadratic
 * (`js/polynomial-redos`, alert #46). These tests pin the trimming behaviour and
 * its linear scaling.
 */

import path from "node:path";
import { describe, expect, it } from "vitest";
import { rewriteStageSkillPaths } from "../../orchestrator/StageExecutor.js";

const CONTENT = "Read `skills/nightgauge-feature-planning/_includes/plan.md` now.";

describe("rewriteStageSkillPaths — skill directory normalisation", () => {
  it("resolves the same target with or without a trailing separator", () => {
    const base = "/extension/dist/skills/nightgauge-feature-planning";
    const expected = rewriteStageSkillPaths(CONTENT, "feature-planning", base);

    for (const suffix of ["/", "//", "\\", "///\\\\"]) {
      expect(rewriteStageSkillPaths(CONTENT, "feature-planning", base + suffix)).toBe(expected);
    }
    expect(expected).toContain(`${base}${path.sep}_includes/plan.md`);
  });

  it("leaves an interior separator run alone", () => {
    const rewritten = rewriteStageSkillPaths(
      CONTENT,
      "feature-planning",
      "/extension//dist/skills/nightgauge-feature-planning"
    );
    expect(rewritten).toContain("/extension//dist/skills/nightgauge-feature-planning");
  });

  it("trims a long separator run in linear time", () => {
    const base = "/extension/dist/skills/nightgauge-feature-planning";
    // The fastest of several samples: one wall-clock sample absorbs any
    // preemption or GC pause, which under a loaded machine alone could exceed
    // the ratio below. The minimum measures the code, not the scheduler.
    const time = (separators: number) => {
      const directory = base + "/".repeat(separators);
      let best = Infinity;
      for (let i = 0; i < 5; i++) {
        const started = performance.now();
        rewriteStageSkillPaths(CONTENT, "feature-planning", directory);
        best = Math.min(best, performance.now() - started);
      }
      return best;
    };

    time(10_000); // warm up the JIT before measuring
    const small = time(20_000);
    const large = time(80_000);

    expect(large).toBeLessThan(Math.max(small, 1) * 8);
  });
});
