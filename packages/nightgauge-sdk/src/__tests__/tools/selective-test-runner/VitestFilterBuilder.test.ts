import { describe, it, expect, vi, beforeEach } from "vitest";
import { buildVitestArgs } from "../../../tools/selective-test-runner/VitestFilterBuilder.js";
import type { ImpactAnalysisResult, AffectedTest } from "../../../analysis/change-impact-types.js";

// Mock fs.existsSync at the module level
vi.mock("fs", () => ({
  existsSync: vi.fn(),
}));

import { existsSync } from "fs";
const mockExistsSync = vi.mocked(existsSync);

// ── Test Data Factories ────────────────────────────────────────────

function makeTest(
  testFile: string,
  confidence: "high" | "medium" | "low" = "high",
  matchType: "direct" | "transitive" | "heuristic" = "direct"
): AffectedTest {
  return {
    testFile,
    confidence,
    matchType,
    reason: `Test match for ${testFile}`,
  };
}

function makeResult(
  affectedTests: AffectedTest[],
  impactLevel: "isolated" | "cross-cutting" | "infrastructure" = "isolated"
): ImpactAnalysisResult {
  return {
    impactLevel,
    affectedSources: [],
    affectedTests,
    changedFiles: [],
    summary: {
      totalAffectedTests: affectedTests.length,
      highConfidence: affectedTests.filter((t) => t.confidence === "high").length,
      mediumConfidence: affectedTests.filter((t) => t.confidence === "medium").length,
      lowConfidence: affectedTests.filter((t) => t.confidence === "low").length,
      impactLevelReason: "test",
    },
  };
}

// ── Tests ──────────────────────────────────────────────────────────

describe("VitestFilterBuilder", () => {
  beforeEach(() => {
    vi.resetAllMocks();
    // Default: all files exist
    mockExistsSync.mockReturnValue(true);
  });

  describe("buildVitestArgs", () => {
    it("returns file paths for existing affected tests", () => {
      const result = makeResult([
        makeTest("tests/foo.test.ts", "high"),
        makeTest("tests/bar.test.ts", "medium"),
      ]);

      const args = buildVitestArgs(result, {}, "/project");

      expect(args).toEqual(["tests/foo.test.ts", "tests/bar.test.ts"]);
    });

    it("filters out non-existent files", () => {
      mockExistsSync.mockImplementation((path) => {
        return String(path).includes("foo");
      });

      const result = makeResult([
        makeTest("tests/foo.test.ts", "high"),
        makeTest("tests/missing.test.ts", "high"),
      ]);

      const args = buildVitestArgs(result, {}, "/project");

      expect(args).toEqual(["tests/foo.test.ts"]);
    });

    it("deduplicates test file paths", () => {
      const result = makeResult([
        makeTest("tests/foo.test.ts", "high", "direct"),
        makeTest("tests/foo.test.ts", "medium", "transitive"),
      ]);

      const args = buildVitestArgs(result, {}, "/project");

      expect(args).toEqual(["tests/foo.test.ts"]);
    });

    it("returns empty array when all files are filtered out", () => {
      mockExistsSync.mockReturnValue(false);

      const result = makeResult([makeTest("tests/gone.test.ts", "high")]);

      const args = buildVitestArgs(result, {}, "/project");

      expect(args).toEqual([]);
    });

    it("returns empty array when no affected tests", () => {
      const result = makeResult([]);

      const args = buildVitestArgs(result, {}, "/project");

      expect(args).toEqual([]);
    });

    describe("minConfidence filtering", () => {
      const mixedConfidenceResult = makeResult([
        makeTest("tests/high.test.ts", "high"),
        makeTest("tests/medium.test.ts", "medium"),
        makeTest("tests/low.test.ts", "low"),
      ]);

      it('includes all confidence levels when minConfidence is "low"', () => {
        const args = buildVitestArgs(mixedConfidenceResult, { minConfidence: "low" }, "/project");

        expect(args).toHaveLength(3);
        expect(args).toContain("tests/low.test.ts");
      });

      it('excludes low confidence when minConfidence is "medium"', () => {
        const args = buildVitestArgs(
          mixedConfidenceResult,
          { minConfidence: "medium" },
          "/project"
        );

        expect(args).toHaveLength(2);
        expect(args).toContain("tests/high.test.ts");
        expect(args).toContain("tests/medium.test.ts");
        expect(args).not.toContain("tests/low.test.ts");
      });

      it('includes only high confidence when minConfidence is "high"', () => {
        const args = buildVitestArgs(mixedConfidenceResult, { minConfidence: "high" }, "/project");

        expect(args).toEqual(["tests/high.test.ts"]);
      });

      it('defaults to "low" when minConfidence is not specified', () => {
        const args = buildVitestArgs(mixedConfidenceResult, {}, "/project");

        expect(args).toHaveLength(3);
      });
    });
  });
});
