/**
 * phaseInference.test.ts (Issue #3760)
 *
 * Verifies deterministic phase inference from observable tool activity for the
 * feature-dev stage, which does not reliably emit its own phase markers.
 * Covers: stage-start emission, tool-driven advancement, monotonicity (never
 * regresses), real-marker precedence, path disambiguation (source edit vs
 * dev-context write), and the no-op behaviour for stages without rules.
 */
import { describe, it, expect } from "vitest";
import { createPhaseInference } from "../events/phaseInference.js";
import { PHASE_REGISTRY } from "../events/phaseRegistry.js";

const DEV_TOTAL = PHASE_REGISTRY["feature-dev"].length; // 18

describe("createPhaseInference — feature-dev", () => {
  it("is enabled and emits validate-environment on start", () => {
    const inf = createPhaseInference("feature-dev");
    expect(inf.enabled).toBe(true);
    const m = inf.start();
    expect(m).toEqual({
      name: "validate-environment",
      index: 0,
      total: DEV_TOTAL,
      stage: "feature-dev",
    });
  });

  it("advances to read-planning-context on the first context read", () => {
    const inf = createPhaseInference("feature-dev");
    inf.start();
    const m = inf.observeToolUse("Read", { file_path: "PLAN.md" });
    expect(m?.name).toBe("read-planning-context");
    expect(m?.index).toBe(1);
  });

  it("advances to implementation on the first source edit", () => {
    const inf = createPhaseInference("feature-dev");
    inf.start();
    const m = inf.observeToolUse("Write", { file_path: "src/foo.ts" });
    expect(m?.name).toBe("implementation");
    expect(m?.index).toBe(8);
  });

  it("does NOT treat a .nightgauge bookkeeping write as implementation", () => {
    const inf = createPhaseInference("feature-dev");
    inf.start();
    const m = inf.observeToolUse("Write", {
      file_path: ".nightgauge/pipeline/dev-3760.json",
    });
    // dev-context path → write-dev-context (14), not implementation (8)
    expect(m?.index).toBe(14);
    expect(m?.name).toBe("write-dev-context");
  });

  it("advances to testing when a test/build command runs", () => {
    const inf = createPhaseInference("feature-dev");
    inf.start();
    inf.observeToolUse("Write", { file_path: "src/foo.ts" }); // implementation
    const m = inf.observeToolUse("Bash", {
      command: "npx -w nightgauge-vscode vitest run",
    });
    expect(m?.index).toBe(9);
    expect(m?.name).toBe("testing");
  });

  it("advances to sync-project-status on a move-status command", () => {
    const inf = createPhaseInference("feature-dev");
    inf.start();
    const m = inf.observeToolUse("Bash", { command: "nightgauge project move-status 3760" });
    expect(m?.index).toBe(15);
    expect(m?.name).toBe("sync-project-status");
  });

  it("is monotonic — a late context read does not regress the phase", () => {
    const inf = createPhaseInference("feature-dev");
    inf.start();
    inf.observeToolUse("Write", { file_path: "src/foo.ts" }); // → implementation (8)
    const regress = inf.observeToolUse("Read", { file_path: "src/other.ts" });
    expect(regress).toBeNull(); // index 1 < cursor 8, no emission
  });

  it("emits each advancement only once", () => {
    const inf = createPhaseInference("feature-dev");
    inf.start();
    const first = inf.observeToolUse("Edit", { file_path: "src/a.ts" });
    expect(first?.index).toBe(8);
    const second = inf.observeToolUse("Edit", { file_path: "src/b.ts" });
    expect(second).toBeNull(); // already at implementation
  });

  it("real markers take precedence and prevent inferred regressions", () => {
    const inf = createPhaseInference("feature-dev");
    inf.start();
    // Skill emitted a genuine marker for quality-review (index 11).
    inf.observeRealMarker(11);
    // A subsequent source edit (would infer index 8) must not regress.
    const m = inf.observeToolUse("Write", { file_path: "src/foo.ts" });
    expect(m).toBeNull();
    // But a later test run (index 9) is also behind 11 → still null.
    const t = inf.observeToolUse("Bash", { command: "go test ./..." });
    expect(t).toBeNull();
  });

  it("ignores unrelated bash commands", () => {
    const inf = createPhaseInference("feature-dev");
    inf.start();
    inf.observeToolUse("Write", { file_path: "src/foo.ts" });
    const m = inf.observeToolUse("Bash", { command: "git status" });
    expect(m).toBeNull();
  });
});

const PLANNING_TOTAL = PHASE_REGISTRY["feature-planning"].length; // 14

describe("createPhaseInference — feature-planning (#3771)", () => {
  it("is enabled and emits feedback-context-check on start", () => {
    const inf = createPhaseInference("feature-planning");
    expect(inf.enabled).toBe(true);
    expect(inf.start()).toEqual({
      name: "feedback-context-check",
      index: 0,
      total: PLANNING_TOTAL,
      stage: "feature-planning",
    });
  });

  it("advances to documentation-analysis on a doc/source read", () => {
    const inf = createPhaseInference("feature-planning");
    inf.start();
    const m = inf.observeToolUse("Grep", { pattern: "foo", path: "docs/" });
    expect(m?.index).toBe(6);
    expect(m?.name).toBe("documentation-analysis");
  });

  it("advances to produce-plan when the plan file is written", () => {
    const inf = createPhaseInference("feature-planning");
    inf.start();
    const m = inf.observeToolUse("Write", {
      file_path: ".nightgauge/plans/6-flutter-ia-nav.md",
    });
    expect(m?.index).toBe(9);
    expect(m?.name).toBe("produce-plan");
  });

  it("advances to write-planning-context on the planning handoff write", () => {
    const inf = createPhaseInference("feature-planning");
    inf.start();
    inf.observeToolUse("Write", { file_path: ".nightgauge/plans/6-x.md" }); // produce-plan
    const m = inf.observeToolUse("Write", {
      file_path: ".nightgauge/pipeline/planning-6.json",
    });
    expect(m?.index).toBe(10);
    expect(m?.name).toBe("write-planning-context");
  });

  it("does NOT treat the plan-file write as the context handoff", () => {
    const inf = createPhaseInference("feature-planning");
    inf.start();
    // plan markdown matches produce-plan (9), not write-planning-context (10)
    const m = inf.observeToolUse("Edit", {
      file_path: ".nightgauge/plans/6-flutter-ia-nav.md",
    });
    expect(m?.index).toBe(9);
  });

  it("is monotonic — a late read does not regress past produce-plan", () => {
    const inf = createPhaseInference("feature-planning");
    inf.start();
    inf.observeToolUse("Write", { file_path: ".nightgauge/plans/6-x.md" }); // → 9
    const regress = inf.observeToolUse("Read", { file_path: "docs/ARCHITECTURE.md" });
    expect(regress).toBeNull(); // index 6 < cursor 9
  });

  it("real markers take precedence over inferred planning phases", () => {
    const inf = createPhaseInference("feature-planning");
    inf.start();
    inf.observeRealMarker(10); // skill emitted write-planning-context
    const m = inf.observeToolUse("Read", { file_path: "docs/x.md" }); // would infer 6
    expect(m).toBeNull();
  });
});

describe("createPhaseInference — stages without rules", () => {
  it("is a no-op for feature-validate (it self-reports reliably)", () => {
    const inf = createPhaseInference("feature-validate");
    expect(inf.enabled).toBe(false);
    expect(inf.start()).toBeNull();
    expect(inf.observeToolUse("Write", { file_path: "src/foo.ts" })).toBeNull();
    expect(() => inf.observeRealMarker(3)).not.toThrow();
  });

  it("is a no-op for an unknown stage", () => {
    const inf = createPhaseInference("not-a-stage");
    expect(inf.enabled).toBe(false);
    expect(inf.start()).toBeNull();
  });
});
