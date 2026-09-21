import { describe, it, expect } from "vitest";
import { createPhaseInference } from "../events/phaseInference";

/**
 * #1924. These mirror internal/execution/phase_gapfill_test.go case for case.
 * phaseInference.ts and phase_inference.go are a declared parity pair, and the
 * dual-path drift class this repo has already paid for twice (#1247, #1398)
 * starts exactly here — one path taught a behaviour the other was not.
 */
describe("phase gap-fill", () => {
  it("reports the phases an advance jumped over", () => {
    const inf = createPhaseInference("feature-dev");
    expect(inf.start()).not.toBeNull();

    const toOne = inf.observeToolUse("Read", { file_path: "PLAN.md" });
    expect(toOne?.marker.index).toBe(1);
    expect(toOne?.passed).toEqual([]);

    const toEight = inf.observeToolUse("Write", { file_path: "src/app.ts" });
    expect(toEight?.marker.index).toBe(8);
    expect(toEight?.passed.map((p) => p.index)).toEqual([2, 3, 4, 5, 6, 7]);
    for (const p of toEight!.passed) {
      expect(p.name).not.toBe("");
      expect(p.stage).toBe("feature-dev");
    }
  });

  it("reports the gap a real marker revealed", () => {
    const inf = createPhaseInference("feature-planning");
    inf.start();
    expect(inf.observeRealMarker(6).map((p) => p.index)).toEqual([1, 2, 3, 4, 5]);
    // A marker that does not advance reveals nothing.
    expect(inf.observeRealMarker(6)).toEqual([]);
    expect(inf.observeRealMarker(3)).toEqual([]);
  });

  it("gap-fills for a stage with a phase table and no inference rules", () => {
    // Ordering is structural, so it does not depend on the rule table having
    // been taught this stage.
    const inf = createPhaseInference("feature-validate");
    expect(inf.enabled).toBe(true);
    expect(inf.observeRealMarker(4).map((p) => p.index)).toEqual([0, 1, 2, 3]);
  });
});
