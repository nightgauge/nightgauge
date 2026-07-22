import { describe, it, expect, vi, beforeEach } from "vitest";
import { SelectiveTestRunner } from "../../../tools/selective-test-runner/SelectiveTestRunner.js";
import type { DependencyGraph } from "../../../analysis/graph-types.js";
import type { ImpactAnalysisResult } from "../../../analysis/change-impact-types.js";

// Mock dependencies
vi.mock("fs", () => ({
  existsSync: vi.fn().mockReturnValue(true),
}));

vi.mock("fs/promises", () => ({
  readdir: vi.fn(),
}));

vi.mock("../../../analysis/SourceToTestGraph.js", () => ({
  buildSourceToTestGraph: vi.fn(),
  loadGraph: vi.fn(),
}));

vi.mock("../../../analysis/ChangeImpactAnalyzer.js", () => ({
  analyzeImpact: vi.fn(),
}));

import { existsSync } from "fs";
import { buildSourceToTestGraph, loadGraph } from "../../../analysis/SourceToTestGraph.js";
import { analyzeImpact } from "../../../analysis/ChangeImpactAnalyzer.js";

const mockExistsSync = vi.mocked(existsSync);
const mockBuildGraph = vi.mocked(buildSourceToTestGraph);
const mockLoadGraph = vi.mocked(loadGraph);
const mockAnalyzeImpact = vi.mocked(analyzeImpact);

// ── Test Data ──────────────────────────────────────────────────────

function makeGraph(overrides: Partial<DependencyGraph> = {}): DependencyGraph {
  return {
    version: "1.0",
    generatedAt: new Date().toISOString(),
    projectRoot: "/project",
    packages: ["."],
    sourceToTests: {
      "src/foo.ts": ["tests/foo.test.ts"],
      "src/bar.ts": ["tests/bar.test.ts"],
    },
    testToSources: {
      "tests/foo.test.ts": ["src/foo.ts"],
      "tests/bar.test.ts": ["src/bar.ts"],
    },
    importGraph: {
      "tests/foo.test.ts": ["src/foo.ts"],
      "tests/bar.test.ts": ["src/bar.ts"],
      "src/foo.ts": [],
      "src/bar.ts": [],
    },
    ...overrides,
  };
}

function makeImpactResult(overrides: Partial<ImpactAnalysisResult> = {}): ImpactAnalysisResult {
  return {
    impactLevel: "isolated",
    affectedSources: [],
    affectedTests: [
      {
        testFile: "tests/foo.test.ts",
        confidence: "high",
        matchType: "direct",
        reason: "Direct import",
      },
    ],
    changedFiles: [],
    summary: {
      totalAffectedTests: 1,
      highConfidence: 1,
      mediumConfidence: 0,
      lowConfidence: 0,
      impactLevelReason: "test",
    },
    ...overrides,
  };
}

// ── Tests ──────────────────────────────────────────────────────────

describe("SelectiveTestRunner", () => {
  beforeEach(() => {
    vi.resetAllMocks();
    mockExistsSync.mockReturnValue(true);
    mockBuildGraph.mockResolvedValue(makeGraph());
    mockAnalyzeImpact.mockReturnValue(makeImpactResult());
  });

  describe("mode=never", () => {
    it("returns full suite without building graph", async () => {
      const runner = new SelectiveTestRunner({
        mode: "never",
        projectRoot: "/project",
      });

      const result = await runner.selectTests(["src/foo.ts"]);

      expect(result.mode).toBe("full");
      expect(result.reason).toBe('mode set to "never"');
      expect(result.vitestArgs).toEqual([]);
      expect(mockBuildGraph).not.toHaveBeenCalled();
      expect(mockAnalyzeImpact).not.toHaveBeenCalled();
    });
  });

  describe("empty changed files", () => {
    it("returns full suite", async () => {
      const runner = new SelectiveTestRunner({
        mode: "auto",
        projectRoot: "/project",
      });

      const result = await runner.selectTests([]);

      expect(result.mode).toBe("full");
      expect(result.reason).toBe("no changed files provided");
    });
  });

  describe("mode=auto", () => {
    it("returns selective result for isolated impact", async () => {
      const runner = new SelectiveTestRunner({
        mode: "auto",
        projectRoot: "/project",
      });

      const result = await runner.selectTests(["src/foo.ts"]);

      expect(result.mode).toBe("selective");
      expect(result.testFiles).toEqual(["tests/foo.test.ts"]);
      expect(result.vitestArgs).toEqual(["tests/foo.test.ts"]);
      expect(result.selectedTests).toBe(1);
      expect(result.impactLevel).toBe("isolated");
    });

    it("returns full suite for infrastructure impact", async () => {
      mockAnalyzeImpact.mockReturnValue(makeImpactResult({ impactLevel: "infrastructure" }));

      const runner = new SelectiveTestRunner({
        mode: "auto",
        projectRoot: "/project",
      });

      const result = await runner.selectTests(["package.json"]);

      expect(result.mode).toBe("full");
      expect(result.reason).toBe("infrastructure change detected");
    });

    it("returns full suite for cross-cutting impact", async () => {
      mockAnalyzeImpact.mockReturnValue(makeImpactResult({ impactLevel: "cross-cutting" }));

      const runner = new SelectiveTestRunner({
        mode: "auto",
        projectRoot: "/project",
      });

      const result = await runner.selectTests(["src/foo.ts", "src/bar.ts"]);

      expect(result.mode).toBe("full");
      expect(result.reason).toBe("cross-cutting change detected");
    });
  });

  describe("mode=always", () => {
    it("returns selective even for cross-cutting impact", async () => {
      mockAnalyzeImpact.mockReturnValue(makeImpactResult({ impactLevel: "cross-cutting" }));

      const runner = new SelectiveTestRunner({
        mode: "always",
        projectRoot: "/project",
      });

      const result = await runner.selectTests(["src/foo.ts"]);

      expect(result.mode).toBe("selective");
      expect(result.testFiles).toEqual(["tests/foo.test.ts"]);
    });

    it("returns full suite for infrastructure even in always mode", async () => {
      mockAnalyzeImpact.mockReturnValue(makeImpactResult({ impactLevel: "infrastructure" }));

      const runner = new SelectiveTestRunner({
        mode: "always",
        projectRoot: "/project",
      });

      const result = await runner.selectTests(["vitest.config.ts"]);

      expect(result.mode).toBe("full");
      expect(result.reason).toBe("infrastructure change detected");
    });
  });

  describe("graph loading", () => {
    it("loads cached graph when graphCachePath exists", async () => {
      const cachedGraph = makeGraph();
      mockLoadGraph.mockResolvedValue(cachedGraph);

      const runner = new SelectiveTestRunner({
        mode: "auto",
        projectRoot: "/project",
        graphCachePath: "/project/.nightgauge/test-graph.json",
      });

      await runner.selectTests(["src/foo.ts"]);

      expect(mockLoadGraph).toHaveBeenCalledWith("/project/.nightgauge/test-graph.json");
      expect(mockBuildGraph).not.toHaveBeenCalled();
    });

    it("builds graph when no cache exists", async () => {
      mockExistsSync.mockImplementation((path) => {
        // Cache file does not exist, but test files do
        return !String(path).includes("test-graph.json");
      });

      const runner = new SelectiveTestRunner({
        mode: "auto",
        projectRoot: "/project",
        graphCachePath: "/project/.nightgauge/test-graph.json",
      });

      await runner.selectTests(["src/foo.ts"]);

      expect(mockLoadGraph).not.toHaveBeenCalled();
      expect(mockBuildGraph).toHaveBeenCalled();
    });

    it("returns full suite when graph build fails", async () => {
      mockBuildGraph.mockRejectedValue(new Error("No source files found"));

      const runner = new SelectiveTestRunner({
        mode: "auto",
        projectRoot: "/project",
      });

      const result = await runner.selectTests(["src/foo.ts"]);

      expect(result.mode).toBe("full");
      expect(result.reason).toContain("graph unavailable");
      expect(result.reason).toContain("No source files found");
    });
  });

  describe("no affected tests", () => {
    it("returns full suite when no tests are identified", async () => {
      mockAnalyzeImpact.mockReturnValue(makeImpactResult({ affectedTests: [] }));

      const runner = new SelectiveTestRunner({
        mode: "auto",
        projectRoot: "/project",
      });

      const result = await runner.selectTests(["src/untracked.ts"]);

      expect(result.mode).toBe("full");
      expect(result.reason).toBe("no affected tests identified");
    });

    it("returns full suite when affected test files do not exist", async () => {
      mockExistsSync.mockImplementation((path) => {
        // Packages dir exists, but test files do not
        return String(path).includes("packages");
      });

      const runner = new SelectiveTestRunner({
        mode: "auto",
        projectRoot: "/project",
      });

      const result = await runner.selectTests(["src/foo.ts"]);

      expect(result.mode).toBe("full");
      expect(result.reason).toBe("no affected tests identified");
    });
  });

  describe("result shape", () => {
    it("includes totalTests and skippedTests counts", async () => {
      const runner = new SelectiveTestRunner({
        mode: "auto",
        projectRoot: "/project",
      });

      const result = await runner.selectTests(["src/foo.ts"]);

      expect(result.totalTests).toBe(2); // graph has 2 test files
      expect(result.selectedTests).toBe(1);
      expect(result.skippedTests).toBe(1);
    });

    it("full suite result has null counts", async () => {
      const runner = new SelectiveTestRunner({
        mode: "never",
        projectRoot: "/project",
      });

      const result = await runner.selectTests(["src/foo.ts"]);

      expect(result.totalTests).toBeNull();
      expect(result.skippedTests).toBeNull();
      expect(result.selectedTests).toBe(0);
    });
  });
});
