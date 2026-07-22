/**
 * Unit tests for RegressionTriggerEvaluator.
 *
 * Tests each of the 7 trigger types (dependency-change, build-config,
 * shared-types, test-infrastructure, ci-config, manual-override,
 * low-confidence) with positive and negative cases.
 *
 * @see Issue #1974 — Full Regression Trigger Rules
 */

import { describe, it, expect } from "vitest";
import {
  evaluateRegressionTriggers,
  evaluateLowConfidenceTrigger,
  matchesPattern,
} from "../../analysis/RegressionTriggerEvaluator.js";
import type { DiffEntry, ImpactAnalysisResult } from "../../analysis/change-impact-types.js";
import { ChangeImpactAnalyzerConfigSchema } from "../../analysis/change-impact-types.js";

// ── Factories ──────────────────────────────────────────────────────

function makeEntry(path: string, status: DiffEntry["status"] = "modified"): DiffEntry {
  return { path, status };
}

function makeConfig(overrides: Record<string, unknown> = {}) {
  return ChangeImpactAnalyzerConfigSchema.parse(overrides);
}

function makeResult(
  affectedTests: ImpactAnalysisResult["affectedTests"] = []
): Pick<ImpactAnalysisResult, "affectedTests" | "summary"> {
  return {
    affectedTests,
    summary: {
      totalAffectedTests: affectedTests.length,
      highConfidence: affectedTests.filter((t) => t.confidence === "high").length,
      mediumConfidence: affectedTests.filter((t) => t.confidence === "medium").length,
      lowConfidence: affectedTests.filter((t) => t.confidence === "low").length,
      impactLevelReason: "",
    },
  };
}

function makeTest(
  testFile: string,
  confidence: "high" | "medium" | "low"
): ImpactAnalysisResult["affectedTests"][number] {
  return {
    testFile,
    confidence,
    matchType:
      confidence === "low" ? "heuristic" : confidence === "medium" ? "transitive" : "direct",
    reason: `${confidence} confidence match`,
  };
}

// ── matchesPattern ─────────────────────────────────────────────────

describe("matchesPattern()", () => {
  it("matches exact basename", () => {
    expect(matchesPattern("path/to/package.json", ["package.json"])).toBe(true);
  });

  it("does not match different basename", () => {
    expect(matchesPattern("src/package-utils.ts", ["package.json"])).toBe(false);
  });

  it("matches wildcard basename (tsconfig*)", () => {
    expect(matchesPattern("tsconfig.json", ["tsconfig*"])).toBe(true);
    expect(matchesPattern("tsconfig.build.json", ["tsconfig*"])).toBe(true);
    expect(matchesPattern("src/other.ts", ["tsconfig*"])).toBe(false);
  });

  it("matches directory prefix pattern (.github/**)", () => {
    expect(matchesPattern(".github/workflows/ci.yml", [".github/**"])).toBe(true);
    expect(matchesPattern("src/.github/file.ts", [".github/**"])).toBe(false);
  });

  it("matches **/segment/** pattern", () => {
    expect(matchesPattern("src/__mocks__/vscode.ts", ["**/__mocks__/**"])).toBe(true);
    expect(matchesPattern("__mocks__/vscode.ts", ["**/__mocks__/**"])).toBe(true);
    expect(matchesPattern("src/mocks/vscode.ts", ["**/__mocks__/**"])).toBe(false);
  });

  it("matches **/*.d.ts pattern", () => {
    expect(matchesPattern("src/types/index.d.ts", ["**/*.d.ts"])).toBe(true);
    expect(matchesPattern("src/types/index.ts", ["**/*.d.ts"])).toBe(false);
  });
});

// ── evaluateRegressionTriggers — no trigger ────────────────────────

describe("evaluateRegressionTriggers() — no trigger", () => {
  it("returns not triggered for empty changed files", () => {
    const cfg = makeConfig();
    const result = evaluateRegressionTriggers([], cfg);
    expect(result.triggered).toBe(false);
  });

  it("returns not triggered for ordinary source files", () => {
    const cfg = makeConfig();
    const result = evaluateRegressionTriggers(
      [makeEntry("src/services/UserService.ts"), makeEntry("src/utils/helpers.ts")],
      cfg
    );
    expect(result.triggered).toBe(false);
  });
});

// ── trigger: dependency-change ─────────────────────────────────────

describe("evaluateRegressionTriggers() — dependency-change", () => {
  it("triggers on package.json", () => {
    const cfg = makeConfig();
    const result = evaluateRegressionTriggers([makeEntry("package.json")], cfg);
    expect(result.triggered).toBe(true);
    if (result.triggered) {
      expect(result.type).toBe("dependency-change");
      expect(result.matchedFile).toBe("package.json");
      expect(result.reason).toContain("package.json");
    }
  });

  it("triggers on package.json in subdirectory", () => {
    const cfg = makeConfig();
    const result = evaluateRegressionTriggers([makeEntry("packages/my-lib/package.json")], cfg);
    expect(result.triggered).toBe(true);
    if (result.triggered) expect(result.type).toBe("dependency-change");
  });

  it("triggers on go.mod", () => {
    const cfg = makeConfig();
    const result = evaluateRegressionTriggers([makeEntry("go.mod")], cfg);
    expect(result.triggered).toBe(true);
    if (result.triggered) expect(result.type).toBe("dependency-change");
  });

  it("triggers on go.sum", () => {
    const cfg = makeConfig();
    const result = evaluateRegressionTriggers([makeEntry("go.sum")], cfg);
    expect(result.triggered).toBe(true);
    if (result.triggered) expect(result.type).toBe("dependency-change");
  });

  it("triggers on yarn.lock", () => {
    const cfg = makeConfig();
    const result = evaluateRegressionTriggers([makeEntry("yarn.lock")], cfg);
    expect(result.triggered).toBe(true);
    if (result.triggered) expect(result.type).toBe("dependency-change");
  });

  it("triggers on pnpm-lock.yaml", () => {
    const cfg = makeConfig();
    const result = evaluateRegressionTriggers([makeEntry("pnpm-lock.yaml")], cfg);
    expect(result.triggered).toBe(true);
    if (result.triggered) expect(result.type).toBe("dependency-change");
  });

  it("does not trigger on src/foo.ts", () => {
    const cfg = makeConfig();
    const result = evaluateRegressionTriggers([makeEntry("src/foo.ts")], cfg);
    expect(result.triggered).toBe(false);
  });

  it("does not trigger when dependency-change is disabled", () => {
    const cfg = makeConfig({
      regressionTriggers: { dependencyChange: { enabled: false } },
    });
    const result = evaluateRegressionTriggers([makeEntry("package.json")], cfg);
    expect(result.triggered).toBe(false);
  });

  it("triggers on custom additionalPatterns", () => {
    const cfg = makeConfig({
      regressionTriggers: {
        dependencyChange: { additionalPatterns: ["custom-deps.yaml"] },
      },
    });
    const result = evaluateRegressionTriggers([makeEntry("infra/custom-deps.yaml")], cfg);
    expect(result.triggered).toBe(true);
    if (result.triggered) expect(result.type).toBe("dependency-change");
  });
});

// ── trigger: build-config ──────────────────────────────────────────

describe("evaluateRegressionTriggers() — build-config", () => {
  it("triggers on tsconfig.json", () => {
    const cfg = makeConfig();
    const result = evaluateRegressionTriggers([makeEntry("tsconfig.json")], cfg);
    expect(result.triggered).toBe(true);
    if (result.triggered) expect(result.type).toBe("build-config");
  });

  it("triggers on tsconfig.build.json", () => {
    const cfg = makeConfig();
    const result = evaluateRegressionTriggers([makeEntry("tsconfig.build.json")], cfg);
    expect(result.triggered).toBe(true);
    if (result.triggered) expect(result.type).toBe("build-config");
  });

  it("triggers on vitest.config.ts", () => {
    const cfg = makeConfig();
    const result = evaluateRegressionTriggers([makeEntry("vitest.config.ts")], cfg);
    expect(result.triggered).toBe(true);
    if (result.triggered) expect(result.type).toBe("build-config");
  });

  it("triggers on webpack.config.js", () => {
    const cfg = makeConfig();
    const result = evaluateRegressionTriggers([makeEntry("webpack.config.js")], cfg);
    expect(result.triggered).toBe(true);
    if (result.triggered) expect(result.type).toBe("build-config");
  });

  it("does not trigger on src/config.ts", () => {
    const cfg = makeConfig();
    const result = evaluateRegressionTriggers([makeEntry("src/config.ts")], cfg);
    expect(result.triggered).toBe(false);
  });

  it("does not trigger when build-config is disabled", () => {
    const cfg = makeConfig({
      regressionTriggers: { buildConfig: { enabled: false } },
    });
    const result = evaluateRegressionTriggers([makeEntry("tsconfig.json")], cfg);
    expect(result.triggered).toBe(false);
  });
});

// ── trigger: shared-types ──────────────────────────────────────────

describe("evaluateRegressionTriggers() — shared-types", () => {
  it("triggers on shared-types/index.ts", () => {
    const cfg = makeConfig();
    const result = evaluateRegressionTriggers([makeEntry("shared-types/index.ts")], cfg);
    expect(result.triggered).toBe(true);
    if (result.triggered) expect(result.type).toBe("shared-types");
  });

  it("triggers on packages/shared-types/Foo.d.ts", () => {
    const cfg = makeConfig();
    const result = evaluateRegressionTriggers([makeEntry("packages/shared-types/Foo.d.ts")], cfg);
    expect(result.triggered).toBe(true);
    if (result.triggered) expect(result.type).toBe("shared-types");
  });

  it("triggers on any .d.ts file", () => {
    const cfg = makeConfig();
    const result = evaluateRegressionTriggers([makeEntry("src/types/generated.d.ts")], cfg);
    expect(result.triggered).toBe(true);
    if (result.triggered) expect(result.type).toBe("shared-types");
  });

  it("does not trigger on src/types/helpers.ts", () => {
    const cfg = makeConfig();
    const result = evaluateRegressionTriggers([makeEntry("src/types/helpers.ts")], cfg);
    expect(result.triggered).toBe(false);
  });

  it("does not trigger when shared-types is disabled", () => {
    const cfg = makeConfig({
      regressionTriggers: { sharedTypes: { enabled: false } },
    });
    const result = evaluateRegressionTriggers([makeEntry("shared-types/index.ts")], cfg);
    expect(result.triggered).toBe(false);
  });
});

// ── trigger: test-infrastructure ──────────────────────────────────

describe("evaluateRegressionTriggers() — test-infrastructure", () => {
  it("triggers on __mocks__/vscode.ts", () => {
    const cfg = makeConfig();
    const result = evaluateRegressionTriggers([makeEntry("__mocks__/vscode.ts")], cfg);
    expect(result.triggered).toBe(true);
    if (result.triggered) expect(result.type).toBe("test-infrastructure");
  });

  it("triggers on test-utils/helpers.ts", () => {
    const cfg = makeConfig();
    const result = evaluateRegressionTriggers([makeEntry("src/test-utils/helpers.ts")], cfg);
    expect(result.triggered).toBe(true);
    if (result.triggered) expect(result.type).toBe("test-infrastructure");
  });

  it("triggers on vitest.setup.ts", () => {
    const cfg = makeConfig();
    const result = evaluateRegressionTriggers([makeEntry("vitest.setup.ts")], cfg);
    expect(result.triggered).toBe(true);
    if (result.triggered) expect(result.type).toBe("test-infrastructure");
  });

  it("triggers on setup.ts", () => {
    const cfg = makeConfig();
    const result = evaluateRegressionTriggers([makeEntry("setup.ts")], cfg);
    expect(result.triggered).toBe(true);
    if (result.triggered) expect(result.type).toBe("test-infrastructure");
  });

  it("triggers on test fixture file", () => {
    const cfg = makeConfig();
    const result = evaluateRegressionTriggers([makeEntry("src/fixtures/user.fixture.ts")], cfg);
    expect(result.triggered).toBe(true);
    if (result.triggered) expect(result.type).toBe("test-infrastructure");
  });

  it("does not trigger on regular test file", () => {
    // Regular test files are not test infrastructure
    const cfg = makeConfig();
    const result = evaluateRegressionTriggers(
      [makeEntry("src/__tests__/UserService.test.ts")],
      cfg
    );
    expect(result.triggered).toBe(false);
  });

  it("does not trigger when test-infrastructure is disabled", () => {
    const cfg = makeConfig({
      regressionTriggers: { testInfrastructure: { enabled: false } },
    });
    const result = evaluateRegressionTriggers([makeEntry("__mocks__/vscode.ts")], cfg);
    expect(result.triggered).toBe(false);
  });
});

// ── trigger: ci-config ─────────────────────────────────────────────

describe("evaluateRegressionTriggers() — ci-config", () => {
  it("triggers on .github/workflows/ci.yml", () => {
    const cfg = makeConfig();
    const result = evaluateRegressionTriggers([makeEntry(".github/workflows/ci.yml")], cfg);
    expect(result.triggered).toBe(true);
    if (result.triggered) {
      expect(result.type).toBe("ci-config");
      expect(result.reason).toContain(".github/workflows/ci.yml");
    }
  });

  it("triggers on .gitlab-ci.yml", () => {
    const cfg = makeConfig();
    const result = evaluateRegressionTriggers([makeEntry(".gitlab-ci.yml")], cfg);
    expect(result.triggered).toBe(true);
    if (result.triggered) expect(result.type).toBe("ci-config");
  });

  it("does not trigger on src/ci.ts", () => {
    const cfg = makeConfig();
    const result = evaluateRegressionTriggers([makeEntry("src/ci.ts")], cfg);
    expect(result.triggered).toBe(false);
  });

  it("does not trigger when ci-config is disabled", () => {
    const cfg = makeConfig({
      regressionTriggers: { ciConfig: { enabled: false } },
    });
    const result = evaluateRegressionTriggers([makeEntry(".github/workflows/ci.yml")], cfg);
    expect(result.triggered).toBe(false);
  });

  it("triggers on custom additionalPatterns", () => {
    const cfg = makeConfig({
      regressionTriggers: {
        ciConfig: { additionalPatterns: ["custom-infra/**"] },
      },
    });
    const result = evaluateRegressionTriggers([makeEntry("custom-infra/config.ts")], cfg);
    expect(result.triggered).toBe(true);
    if (result.triggered) expect(result.type).toBe("ci-config");
  });
});

// ── trigger: manual-override ───────────────────────────────────────

describe("evaluateRegressionTriggers() — manual-override", () => {
  it("triggers when forceFullRegression is true", () => {
    const cfg = makeConfig({ forceFullRegression: true });
    const result = evaluateRegressionTriggers([makeEntry("src/ordinary.ts")], cfg);
    expect(result.triggered).toBe(true);
    if (result.triggered) {
      expect(result.type).toBe("manual-override");
      expect(result.reason).toContain("Manual override");
    }
  });

  it("does not trigger when forceFullRegression is false", () => {
    const cfg = makeConfig({ forceFullRegression: false });
    const result = evaluateRegressionTriggers([makeEntry("src/ordinary.ts")], cfg);
    expect(result.triggered).toBe(false);
  });

  it("manual-override takes priority over file triggers", () => {
    // Even with package.json, manual-override fires first
    const cfg = makeConfig({ forceFullRegression: true });
    const result = evaluateRegressionTriggers([makeEntry("package.json")], cfg);
    expect(result.triggered).toBe(true);
    if (result.triggered) expect(result.type).toBe("manual-override");
  });

  it("does not trigger when manualOverride.enabled is false", () => {
    const cfg = makeConfig({
      forceFullRegression: true,
      regressionTriggers: { manualOverride: { enabled: false } },
    });
    const result = evaluateRegressionTriggers([makeEntry("src/ordinary.ts")], cfg);
    expect(result.triggered).toBe(false);
  });
});

// ── trigger ordering (first-match semantics) ───────────────────────

describe("evaluateRegressionTriggers() — trigger ordering", () => {
  it("returns first matching trigger when multiple would fire", () => {
    const cfg = makeConfig();
    // package.json (dependency-change) AND tsconfig.json (build-config)
    const result = evaluateRegressionTriggers(
      [makeEntry("package.json"), makeEntry("tsconfig.json")],
      cfg
    );
    expect(result.triggered).toBe(true);
    // dependency-change comes first in the table
    if (result.triggered) expect(result.type).toBe("dependency-change");
  });

  it("falls through to second trigger when first is disabled", () => {
    const cfg = makeConfig({
      regressionTriggers: { dependencyChange: { enabled: false } },
    });
    const result = evaluateRegressionTriggers(
      [makeEntry("package.json"), makeEntry("tsconfig.json")],
      cfg
    );
    expect(result.triggered).toBe(true);
    // dependency-change disabled → build-config fires
    if (result.triggered) expect(result.type).toBe("build-config");
  });
});

// ── trigger: low-confidence ────────────────────────────────────────

describe("evaluateLowConfidenceTrigger()", () => {
  it("triggers when all tests are low confidence (default threshold 1.0)", () => {
    const cfg = makeConfig();
    const result = evaluateLowConfidenceTrigger(
      makeResult([
        makeTest("a.test.ts", "low"),
        makeTest("b.test.ts", "low"),
        makeTest("c.test.ts", "low"),
      ]),
      cfg
    );
    expect(result.triggered).toBe(true);
    if (result.triggered) {
      expect(result.type).toBe("low-confidence");
      expect(result.reason).toContain("3/3");
    }
  });

  it("does not trigger when some tests are high confidence (default threshold 1.0)", () => {
    const cfg = makeConfig();
    const result = evaluateLowConfidenceTrigger(
      makeResult([makeTest("a.test.ts", "high"), makeTest("b.test.ts", "low")]),
      cfg
    );
    expect(result.triggered).toBe(false);
  });

  it("does not trigger when no tests were found", () => {
    const cfg = makeConfig();
    const result = evaluateLowConfidenceTrigger(makeResult([]), cfg);
    expect(result.triggered).toBe(false);
  });

  it("triggers at custom threshold", () => {
    // threshold 0.5 — triggers when ≥50% are low confidence
    const cfg = makeConfig({
      regressionTriggers: { lowConfidence: { threshold: 0.5 } },
    });
    const result = evaluateLowConfidenceTrigger(
      makeResult([
        makeTest("a.test.ts", "high"),
        makeTest("b.test.ts", "low"),
        makeTest("c.test.ts", "low"),
      ]),
      cfg
    );
    // 2/3 ≈ 0.67 >= 0.5 → should trigger
    expect(result.triggered).toBe(true);
    if (result.triggered) expect(result.type).toBe("low-confidence");
  });

  it("does not trigger at custom threshold when below it", () => {
    // threshold 0.8 — 2/3 ≈ 0.67 < 0.8 → no trigger
    const cfg = makeConfig({
      regressionTriggers: { lowConfidence: { threshold: 0.8 } },
    });
    const result = evaluateLowConfidenceTrigger(
      makeResult([
        makeTest("a.test.ts", "high"),
        makeTest("b.test.ts", "low"),
        makeTest("c.test.ts", "low"),
      ]),
      cfg
    );
    expect(result.triggered).toBe(false);
  });

  it("does not trigger when lowConfidence trigger is disabled", () => {
    const cfg = makeConfig({
      regressionTriggers: { lowConfidence: { enabled: false } },
    });
    const result = evaluateLowConfidenceTrigger(
      makeResult([makeTest("a.test.ts", "low"), makeTest("b.test.ts", "low")]),
      cfg
    );
    expect(result.triggered).toBe(false);
  });
});
