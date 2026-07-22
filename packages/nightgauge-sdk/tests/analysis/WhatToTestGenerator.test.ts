/**
 * Tests for WhatToTestGenerator
 *
 * Constructs ImpactAnalysisResult objects directly (no mocking) following the
 * Arrange/Act/Assert pattern with Vitest.
 *
 * @see Issue #1972 - "What to Test" PR Section Generator
 */

import { describe, it, expect } from "vitest";

import { generateWhatToTestSection } from "../../src/analysis/WhatToTestGenerator.js";
import type { ImpactAnalysisResult, AffectedTest } from "../../src/analysis/change-impact-types.js";

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

function makeResult(overrides: Partial<ImpactAnalysisResult>): ImpactAnalysisResult {
  const affectedTests = overrides.affectedTests ?? [];
  const high = affectedTests.filter((t) => t.confidence === "high").length;
  const medium = affectedTests.filter((t) => t.confidence === "medium").length;
  const low = affectedTests.filter((t) => t.confidence === "low").length;
  return {
    impactLevel: "isolated",
    affectedSources: [],
    affectedTests,
    changedFiles: [],
    summary: {
      totalAffectedTests: affectedTests.length,
      highConfidence: high,
      mediumConfidence: medium,
      lowConfidence: low,
      impactLevelReason: "Test",
    },
    ...overrides,
    // Re-apply summary when affectedTests was explicitly set
    ...(overrides.affectedTests
      ? {
          summary: {
            totalAffectedTests: affectedTests.length,
            highConfidence: high,
            mediumConfidence: medium,
            lowConfidence: low,
            impactLevelReason: "Test",
            ...(overrides.summary ?? {}),
          },
        }
      : {}),
  };
}

function makeTest(overrides: Partial<AffectedTest> = {}): AffectedTest {
  return {
    testFile: "tests/some.test.ts",
    confidence: "high",
    matchType: "direct",
    reason: "Direct import of changed file",
    ...overrides,
  };
}

// ---------------------------------------------------------------------------
// 1. Empty result
// ---------------------------------------------------------------------------

describe("generateWhatToTestSection — empty result", () => {
  it("returns generated:false when affectedTests is empty and not infrastructure", () => {
    const result = makeResult({ impactLevel: "isolated" });
    const section = generateWhatToTestSection(result);
    expect(section.generated).toBe(false);
  });

  it("returns empty markdown when affectedTests is empty and not infrastructure", () => {
    const result = makeResult({ impactLevel: "isolated" });
    const section = generateWhatToTestSection(result);
    expect(section.markdown).toBe("");
  });

  it("reports zero stats when affectedTests is empty", () => {
    const result = makeResult({ impactLevel: "isolated" });
    const section = generateWhatToTestSection(result);
    expect(section.stats.testsListed).toBe(0);
    expect(section.stats.testsOmitted).toBe(0);
  });
});

// ---------------------------------------------------------------------------
// 2. Infrastructure level
// ---------------------------------------------------------------------------

describe("generateWhatToTestSection — infrastructure level", () => {
  it("includes infrastructure warning in markdown", () => {
    const result = makeResult({ impactLevel: "infrastructure" });
    const section = generateWhatToTestSection(result);
    expect(section.markdown).toContain(
      "**Infrastructure change detected** — full regression recommended."
    );
  });

  it("sets generated:true for infrastructure even with no affected tests", () => {
    const result = makeResult({ impactLevel: "infrastructure" });
    const section = generateWhatToTestSection(result);
    expect(section.generated).toBe(true);
  });

  it('includes "No affected test files detected" note when tests array is empty', () => {
    const result = makeResult({ impactLevel: "infrastructure" });
    const section = generateWhatToTestSection(result);
    expect(section.markdown).toContain("No affected test files detected");
  });
});

// ---------------------------------------------------------------------------
// 3. High confidence tests
// ---------------------------------------------------------------------------

describe("generateWhatToTestSection — high confidence", () => {
  it("lists high-confidence tests under the High confidence header", () => {
    const result = makeResult({
      affectedTests: [
        makeTest({
          testFile: "tests/analysis/Foo.test.ts",
          confidence: "high",
        }),
      ],
    });
    const section = generateWhatToTestSection(result);
    expect(section.markdown).toContain("### High confidence (direct imports)");
    expect(section.markdown).toContain("tests/analysis/Foo.test.ts");
  });

  it("sets generated:true when high confidence tests exist", () => {
    const result = makeResult({
      affectedTests: [makeTest({ confidence: "high" })],
    });
    const section = generateWhatToTestSection(result);
    expect(section.generated).toBe(true);
  });

  it("includes the reason for each high-confidence test", () => {
    const result = makeResult({
      affectedTests: [
        makeTest({
          reason: "Direct import of changed file",
          confidence: "high",
        }),
      ],
    });
    const section = generateWhatToTestSection(result);
    expect(section.markdown).toContain("Direct import of changed file");
  });
});

// ---------------------------------------------------------------------------
// 4. Medium confidence tests
// ---------------------------------------------------------------------------

describe("generateWhatToTestSection — medium confidence", () => {
  it("lists medium-confidence tests under the Medium confidence header", () => {
    const result = makeResult({
      affectedTests: [
        makeTest({
          testFile: "tests/analysis/Bar.test.ts",
          confidence: "medium",
          matchType: "transitive",
          reason: "Transitive dependency on changed file",
        }),
      ],
    });
    const section = generateWhatToTestSection(result);
    expect(section.markdown).toContain("### Medium confidence (transitive)");
    expect(section.markdown).toContain("tests/analysis/Bar.test.ts");
  });

  it("does not include High confidence header when no high tests", () => {
    const result = makeResult({
      affectedTests: [makeTest({ confidence: "medium", matchType: "transitive" })],
    });
    const section = generateWhatToTestSection(result);
    expect(section.markdown).not.toContain("### High confidence");
  });
});

// ---------------------------------------------------------------------------
// 5. Low confidence excluded by default
// ---------------------------------------------------------------------------

describe("generateWhatToTestSection — low confidence excluded by default", () => {
  it("does not include Low confidence header when includeLowConfidence is not set", () => {
    const result = makeResult({
      affectedTests: [
        makeTest({
          testFile: "tests/analysis/Baz.test.ts",
          confidence: "low",
          matchType: "heuristic",
          reason: "Heuristic match for src/baz.ts",
        }),
      ],
    });
    const section = generateWhatToTestSection(result);
    expect(section.markdown).not.toContain("### Low confidence");
  });

  it("still sets generated:true when only low confidence tests exist", () => {
    const result = makeResult({
      affectedTests: [makeTest({ confidence: "low", matchType: "heuristic" })],
    });
    const section = generateWhatToTestSection(result);
    expect(section.generated).toBe(true);
  });

  it("counts low-confidence tests as omitted in stats", () => {
    const result = makeResult({
      affectedTests: [makeTest({ confidence: "low", matchType: "heuristic" })],
    });
    const section = generateWhatToTestSection(result);
    expect(section.stats.testsOmitted).toBe(1);
    expect(section.stats.testsListed).toBe(0);
  });
});

// ---------------------------------------------------------------------------
// 6. Low confidence included when opt-in
// ---------------------------------------------------------------------------

describe("generateWhatToTestSection — low confidence included", () => {
  it("includes Low confidence header when includeLowConfidence:true", () => {
    const result = makeResult({
      affectedTests: [
        makeTest({
          testFile: "tests/analysis/Baz.test.ts",
          confidence: "low",
          matchType: "heuristic",
          reason: "Heuristic match for src/baz.ts",
        }),
      ],
    });
    const section = generateWhatToTestSection(result, {
      includeLowConfidence: true,
    });
    expect(section.markdown).toContain("### Low confidence (heuristic)");
    expect(section.markdown).toContain("tests/analysis/Baz.test.ts");
  });

  it("lists low-confidence tests in stats.testsListed", () => {
    const result = makeResult({
      affectedTests: [makeTest({ confidence: "low", matchType: "heuristic" })],
    });
    const section = generateWhatToTestSection(result, {
      includeLowConfidence: true,
    });
    expect(section.stats.testsListed).toBe(1);
    expect(section.stats.testsOmitted).toBe(0);
  });
});

// ---------------------------------------------------------------------------
// 7. Truncation
// ---------------------------------------------------------------------------

describe("generateWhatToTestSection — truncation", () => {
  it('shows "... N more" when high-confidence tests exceed maxHighConfidence', () => {
    const result = makeResult({
      affectedTests: [
        makeTest({ testFile: "tests/a.test.ts", confidence: "high" }),
        makeTest({ testFile: "tests/b.test.ts", confidence: "high" }),
        makeTest({ testFile: "tests/c.test.ts", confidence: "high" }),
      ],
    });
    const section = generateWhatToTestSection(result, { maxHighConfidence: 2 });
    expect(section.markdown).toContain("... 1 more");
    expect(section.stats.testsListed).toBe(2);
    expect(section.stats.testsOmitted).toBe(1);
  });

  it('shows "... N more" when medium-confidence tests exceed maxMediumConfidence', () => {
    const result = makeResult({
      affectedTests: [
        makeTest({
          testFile: "tests/a.test.ts",
          confidence: "medium",
          matchType: "transitive",
        }),
        makeTest({
          testFile: "tests/b.test.ts",
          confidence: "medium",
          matchType: "transitive",
        }),
        makeTest({
          testFile: "tests/c.test.ts",
          confidence: "medium",
          matchType: "transitive",
        }),
      ],
    });
    const section = generateWhatToTestSection(result, {
      maxMediumConfidence: 2,
    });
    expect(section.markdown).toContain("... 1 more");
  });

  it("lists all tests when count is within limits", () => {
    const result = makeResult({
      affectedTests: [
        makeTest({ testFile: "tests/a.test.ts", confidence: "high" }),
        makeTest({ testFile: "tests/b.test.ts", confidence: "high" }),
      ],
    });
    const section = generateWhatToTestSection(result, {
      maxHighConfidence: 10,
    });
    expect(section.markdown).not.toContain("more");
    expect(section.stats.testsListed).toBe(2);
    expect(section.stats.testsOmitted).toBe(0);
  });
});

// ---------------------------------------------------------------------------
// 8. Mixed confidence levels — correct order
// ---------------------------------------------------------------------------

describe("generateWhatToTestSection — mixed confidence order", () => {
  it("renders High, then Medium, then Low sections in correct order", () => {
    const result = makeResult({
      affectedTests: [
        makeTest({ testFile: "tests/high.test.ts", confidence: "high" }),
        makeTest({
          testFile: "tests/medium.test.ts",
          confidence: "medium",
          matchType: "transitive",
        }),
        makeTest({
          testFile: "tests/low.test.ts",
          confidence: "low",
          matchType: "heuristic",
        }),
      ],
    });
    const section = generateWhatToTestSection(result, {
      includeLowConfidence: true,
    });

    const highIdx = section.markdown.indexOf("### High confidence");
    const mediumIdx = section.markdown.indexOf("### Medium confidence");
    const lowIdx = section.markdown.indexOf("### Low confidence");

    expect(highIdx).toBeGreaterThan(-1);
    expect(mediumIdx).toBeGreaterThan(highIdx);
    expect(lowIdx).toBeGreaterThan(mediumIdx);
  });

  it("reports correct testsListed count for mixed levels", () => {
    const result = makeResult({
      affectedTests: [
        makeTest({ confidence: "high" }),
        makeTest({ confidence: "medium", matchType: "transitive" }),
        makeTest({ confidence: "low", matchType: "heuristic" }),
      ],
    });
    const section = generateWhatToTestSection(result, {
      includeLowConfidence: true,
    });
    expect(section.stats.testsListed).toBe(3);
  });
});

// ---------------------------------------------------------------------------
// 9. Path relativization
// ---------------------------------------------------------------------------

describe("generateWhatToTestSection — path relativization", () => {
  it("strips the projectRoot prefix from test file paths", () => {
    const projectRoot = "/workspace/myproject";
    const result = makeResult({
      affectedTests: [
        makeTest({
          testFile: "/workspace/myproject/tests/analysis/Foo.test.ts",
          confidence: "high",
        }),
      ],
    });
    const section = generateWhatToTestSection(result, { projectRoot });
    expect(section.markdown).toContain("`tests/analysis/Foo.test.ts`");
    expect(section.markdown).not.toContain("/workspace/myproject/tests");
  });

  it("leaves paths unchanged when they do not start with projectRoot", () => {
    const projectRoot = "/workspace/myproject";
    const result = makeResult({
      affectedTests: [
        makeTest({
          testFile: "tests/analysis/Foo.test.ts",
          confidence: "high",
        }),
      ],
    });
    const section = generateWhatToTestSection(result, { projectRoot });
    expect(section.markdown).toContain("`tests/analysis/Foo.test.ts`");
  });

  it("handles projectRoot with trailing slash", () => {
    const projectRoot = "/workspace/myproject/";
    const result = makeResult({
      affectedTests: [
        makeTest({
          testFile: "/workspace/myproject/tests/Foo.test.ts",
          confidence: "high",
        }),
      ],
    });
    const section = generateWhatToTestSection(result, { projectRoot });
    expect(section.markdown).toContain("`tests/Foo.test.ts`");
  });
});

// ---------------------------------------------------------------------------
// 10. generated:false when affectedTests is empty and not infrastructure
// ---------------------------------------------------------------------------

describe("generateWhatToTestSection — generated:false", () => {
  it("returns generated:false for isolated impact with no tests", () => {
    const result = makeResult({ impactLevel: "isolated" });
    const section = generateWhatToTestSection(result);
    expect(section.generated).toBe(false);
  });

  it("returns generated:false for cross-cutting impact with no tests", () => {
    const result = makeResult({ impactLevel: "cross-cutting" });
    const section = generateWhatToTestSection(result);
    expect(section.generated).toBe(false);
  });

  it("returns generated:true for infrastructure impact with no tests", () => {
    const result = makeResult({ impactLevel: "infrastructure" });
    const section = generateWhatToTestSection(result);
    expect(section.generated).toBe(true);
  });

  it("includes impact level in stats even when generated is false", () => {
    const result = makeResult({ impactLevel: "isolated" });
    const section = generateWhatToTestSection(result);
    expect(section.stats.impactLevel).toBe("isolated");
  });
});
