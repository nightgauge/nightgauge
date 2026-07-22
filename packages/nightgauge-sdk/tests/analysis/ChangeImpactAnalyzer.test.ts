/**
 * Tests for Change Impact Analyzer
 *
 * Uses manually constructed graphs for unit tests and real temp directories
 * for heuristic fallback tests (mapSourceToTestFiles checks existsSync).
 *
 * @see Issue #1971 - Change Impact Analyzer
 */

import { mkdtemp, mkdir, writeFile, rm } from "fs/promises";
import { tmpdir } from "os";
import { join } from "path";
import { describe, it, expect, beforeEach, afterEach } from "vitest";

import {
  parseDiff,
  analyzeImpact,
  analyzeImpactFromDiff,
} from "../../src/analysis/ChangeImpactAnalyzer.js";
import type { DiffEntry } from "../../src/analysis/change-impact-types.js";
import type { DependencyGraph } from "../../src/analysis/graph-types.js";

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

function makeGraph(
  overrides: Partial<DependencyGraph> & {
    sourceToTests: DependencyGraph["sourceToTests"];
    testToSources: DependencyGraph["testToSources"];
    importGraph: DependencyGraph["importGraph"];
  }
): DependencyGraph {
  return {
    version: "1.0",
    generatedAt: new Date().toISOString(),
    projectRoot: "/fake",
    packages: ["pkg"],
    ...overrides,
  };
}

// ---------------------------------------------------------------------------
// parseDiff
// ---------------------------------------------------------------------------

describe("parseDiff", () => {
  it("parses modified (M) entries", () => {
    const result = parseDiff("M\tsrc/foo.ts");
    expect(result).toEqual([{ status: "modified", path: "src/foo.ts" }]);
  });

  it("parses added (A) entries", () => {
    const result = parseDiff("A\tsrc/bar.ts");
    expect(result).toEqual([{ status: "added", path: "src/bar.ts" }]);
  });

  it("parses deleted (D) entries", () => {
    const result = parseDiff("D\tsrc/old.ts");
    expect(result).toEqual([{ status: "deleted", path: "src/old.ts" }]);
  });

  it("parses renamed (R100) entries with tab-separated paths", () => {
    const result = parseDiff("R100\tsrc/old-name.ts\tsrc/new-name.ts");
    expect(result).toEqual([
      {
        status: "renamed",
        path: "src/new-name.ts",
        oldPath: "src/old-name.ts",
      },
    ]);
  });

  it("parses renamed entries with space-separated paths", () => {
    const result = parseDiff("R100  src/old.ts  src/new.ts");
    expect(result).toEqual([{ status: "renamed", path: "src/new.ts", oldPath: "src/old.ts" }]);
  });

  it("skips blank lines", () => {
    const result = parseDiff("M\tsrc/a.ts\n\n\nA\tsrc/b.ts\n");
    expect(result).toHaveLength(2);
    expect(result[0].path).toBe("src/a.ts");
    expect(result[1].path).toBe("src/b.ts");
  });

  it("handles --name-only format (no status prefix)", () => {
    const result = parseDiff("src/foo.ts\nsrc/bar.ts");
    expect(result).toEqual([
      { status: "modified", path: "src/foo.ts" },
      { status: "modified", path: "src/bar.ts" },
    ]);
  });

  it("normalizes backslashes to forward slashes", () => {
    const result = parseDiff("M\tsrc\\utils\\helper.ts");
    expect(result[0].path).toBe("src/utils/helper.ts");
  });

  it("parses multiple entries", () => {
    const diff = ["M\tsrc/a.ts", "A\tsrc/b.ts", "D\tsrc/c.ts", "R100\tsrc/old.ts\tsrc/new.ts"].join(
      "\n"
    );
    const result = parseDiff(diff);
    expect(result).toHaveLength(4);
    expect(result[0]).toEqual({ status: "modified", path: "src/a.ts" });
    expect(result[1]).toEqual({ status: "added", path: "src/b.ts" });
    expect(result[2]).toEqual({ status: "deleted", path: "src/c.ts" });
    expect(result[3]).toEqual({
      status: "renamed",
      path: "src/new.ts",
      oldPath: "src/old.ts",
    });
  });
});

// ---------------------------------------------------------------------------
// analyzeImpact — isolated scenario
// ---------------------------------------------------------------------------

describe("analyzeImpact — isolated scenario", () => {
  const graph = makeGraph({
    sourceToTests: {
      "pkg/src/utils.ts": ["pkg/tests/utils.test.ts"],
    },
    testToSources: {
      "pkg/tests/utils.test.ts": ["pkg/src/utils.ts"],
    },
    importGraph: {
      "pkg/tests/utils.test.ts": ["pkg/src/utils.ts"],
      "pkg/src/utils.ts": [],
    },
  });

  it("classifies single-module change as isolated", () => {
    const changedFiles: DiffEntry[] = [{ status: "modified", path: "pkg/src/utils.ts" }];
    const result = analyzeImpact(changedFiles, graph);
    expect(result.impactLevel).toBe("isolated");
  });

  it("returns high confidence for directly imported test files", () => {
    const changedFiles: DiffEntry[] = [{ status: "modified", path: "pkg/src/utils.ts" }];
    const result = analyzeImpact(changedFiles, graph);
    expect(result.affectedTests).toHaveLength(1);
    expect(result.affectedTests[0].testFile).toBe("pkg/tests/utils.test.ts");
    expect(result.affectedTests[0].confidence).toBe("high");
    expect(result.affectedTests[0].matchType).toBe("direct");
  });

  it("populates summary counts correctly", () => {
    const changedFiles: DiffEntry[] = [{ status: "modified", path: "pkg/src/utils.ts" }];
    const result = analyzeImpact(changedFiles, graph);
    expect(result.summary.totalAffectedTests).toBe(1);
    expect(result.summary.highConfidence).toBe(1);
    expect(result.summary.mediumConfidence).toBe(0);
    expect(result.summary.lowConfidence).toBe(0);
  });

  it("marks source files with hasGraphEntry correctly", () => {
    const changedFiles: DiffEntry[] = [{ status: "modified", path: "pkg/src/utils.ts" }];
    const result = analyzeImpact(changedFiles, graph);
    expect(result.affectedSources).toHaveLength(1);
    expect(result.affectedSources[0].hasGraphEntry).toBe(true);
  });
});

// ---------------------------------------------------------------------------
// analyzeImpact — cross-cutting scenario
// ---------------------------------------------------------------------------

describe("analyzeImpact — cross-cutting scenario", () => {
  it("classifies change affecting many test files as cross-cutting (dep threshold)", () => {
    const graph = makeGraph({
      sourceToTests: {
        "pkg/src/shared.ts": ["pkg/tests/a.test.ts", "pkg/tests/b.test.ts", "pkg/tests/c.test.ts"],
      },
      testToSources: {
        "pkg/tests/a.test.ts": ["pkg/src/shared.ts"],
        "pkg/tests/b.test.ts": ["pkg/src/shared.ts"],
        "pkg/tests/c.test.ts": ["pkg/src/shared.ts"],
      },
      importGraph: {
        "pkg/tests/a.test.ts": ["pkg/src/shared.ts"],
        "pkg/tests/b.test.ts": ["pkg/src/shared.ts"],
        "pkg/tests/c.test.ts": ["pkg/src/shared.ts"],
        "pkg/src/shared.ts": [],
      },
    });

    const changedFiles: DiffEntry[] = [{ status: "modified", path: "pkg/src/shared.ts" }];
    const result = analyzeImpact(changedFiles, graph);
    expect(result.impactLevel).toBe("cross-cutting");
    expect(result.affectedTests).toHaveLength(3);
  });

  it("classifies change spanning multiple top-level dirs as cross-cutting (dir threshold)", () => {
    const graph = makeGraph({
      packages: ["pkg-a", "pkg-b"],
      sourceToTests: {
        "pkg-a/src/a.ts": ["pkg-a/tests/a.test.ts"],
        "pkg-b/src/b.ts": ["pkg-b/tests/b.test.ts"],
      },
      testToSources: {
        "pkg-a/tests/a.test.ts": ["pkg-a/src/a.ts"],
        "pkg-b/tests/b.test.ts": ["pkg-b/src/b.ts"],
      },
      importGraph: {
        "pkg-a/tests/a.test.ts": ["pkg-a/src/a.ts"],
        "pkg-b/tests/b.test.ts": ["pkg-b/src/b.ts"],
        "pkg-a/src/a.ts": [],
        "pkg-b/src/b.ts": [],
      },
    });

    const changedFiles: DiffEntry[] = [
      { status: "modified", path: "pkg-a/src/a.ts" },
      { status: "modified", path: "pkg-b/src/b.ts" },
    ];
    const result = analyzeImpact(changedFiles, graph);
    expect(result.impactLevel).toBe("cross-cutting");
  });

  it("respects custom cross-cutting thresholds", () => {
    const graph = makeGraph({
      sourceToTests: {
        "pkg/src/a.ts": ["pkg/tests/a.test.ts"],
      },
      testToSources: {
        "pkg/tests/a.test.ts": ["pkg/src/a.ts"],
      },
      importGraph: {
        "pkg/tests/a.test.ts": ["pkg/src/a.ts"],
        "pkg/src/a.ts": [],
      },
    });

    const changedFiles: DiffEntry[] = [{ status: "modified", path: "pkg/src/a.ts" }];
    // Lower dep threshold to 1 — even a single affected test triggers cross-cutting
    const result = analyzeImpact(changedFiles, graph, {
      crossCuttingDepThreshold: 1,
    });
    expect(result.impactLevel).toBe("cross-cutting");
  });
});

// ---------------------------------------------------------------------------
// analyzeImpact — infrastructure scenario
// ---------------------------------------------------------------------------

describe("analyzeImpact — infrastructure scenario", () => {
  const emptyGraph = makeGraph({
    sourceToTests: {},
    testToSources: {},
    importGraph: {},
  });

  it("classifies package.json change as infrastructure", () => {
    const changedFiles: DiffEntry[] = [{ status: "modified", path: "package.json" }];
    const result = analyzeImpact(changedFiles, emptyGraph);
    expect(result.impactLevel).toBe("infrastructure");
  });

  it("classifies tsconfig.json change as infrastructure", () => {
    const changedFiles: DiffEntry[] = [{ status: "modified", path: "tsconfig.json" }];
    const result = analyzeImpact(changedFiles, emptyGraph);
    expect(result.impactLevel).toBe("infrastructure");
  });

  it("classifies tsconfig.build.json change as infrastructure", () => {
    const changedFiles: DiffEntry[] = [{ status: "modified", path: "tsconfig.build.json" }];
    const result = analyzeImpact(changedFiles, emptyGraph);
    expect(result.impactLevel).toBe("infrastructure");
  });

  it("classifies .github/ change as infrastructure", () => {
    const changedFiles: DiffEntry[] = [{ status: "modified", path: ".github/workflows/ci.yml" }];
    const result = analyzeImpact(changedFiles, emptyGraph);
    expect(result.impactLevel).toBe("infrastructure");
  });

  it("classifies vitest.config.ts change as infrastructure", () => {
    const changedFiles: DiffEntry[] = [{ status: "modified", path: "vitest.config.ts" }];
    const result = analyzeImpact(changedFiles, emptyGraph);
    expect(result.impactLevel).toBe("infrastructure");
  });

  it("respects custom infrastructure patterns", () => {
    const changedFiles: DiffEntry[] = [{ status: "modified", path: "custom-build.config" }];
    const result = analyzeImpact(changedFiles, emptyGraph, {
      infrastructurePatterns: ["custom-build.config"],
    });
    expect(result.impactLevel).toBe("infrastructure");
  });

  it("infrastructure takes priority even when source files are also changed", () => {
    const graph = makeGraph({
      sourceToTests: {
        "pkg/src/utils.ts": ["pkg/tests/utils.test.ts"],
      },
      testToSources: {
        "pkg/tests/utils.test.ts": ["pkg/src/utils.ts"],
      },
      importGraph: {
        "pkg/tests/utils.test.ts": ["pkg/src/utils.ts"],
        "pkg/src/utils.ts": [],
      },
    });

    const changedFiles: DiffEntry[] = [
      { status: "modified", path: "package.json" },
      { status: "modified", path: "pkg/src/utils.ts" },
    ];
    const result = analyzeImpact(changedFiles, graph);
    expect(result.impactLevel).toBe("infrastructure");
    // Note: package.json triggers the pre-graph regression trigger (dependency-change),
    // which short-circuits before the graph walk. Affected tests are empty on the
    // short-circuit path — the regressionTrigger tells consumers to run the full suite.
    expect(result.summary.regressionTrigger?.type).toBe("dependency-change");
  });
});

// ---------------------------------------------------------------------------
// analyzeImpact — confidence scoring
// ---------------------------------------------------------------------------

describe("analyzeImpact — confidence scoring", () => {
  it("assigns medium confidence for transitive dependencies", () => {
    const graph = makeGraph({
      sourceToTests: {
        "pkg/src/a.ts": ["pkg/tests/a.test.ts"],
        "pkg/src/b.ts": ["pkg/tests/a.test.ts"],
      },
      testToSources: {
        "pkg/tests/a.test.ts": ["pkg/src/a.ts", "pkg/src/b.ts"],
      },
      importGraph: {
        "pkg/tests/a.test.ts": ["pkg/src/a.ts"], // direct import of a.ts only
        "pkg/src/a.ts": ["pkg/src/b.ts"], // a.ts imports b.ts
        "pkg/src/b.ts": [],
      },
    });

    // Change b.ts — test imports a.ts → a.ts imports b.ts (transitive)
    const changedFiles: DiffEntry[] = [{ status: "modified", path: "pkg/src/b.ts" }];
    const result = analyzeImpact(changedFiles, graph);
    expect(result.affectedTests).toHaveLength(1);
    expect(result.affectedTests[0].confidence).toBe("medium");
    expect(result.affectedTests[0].matchType).toBe("transitive");
  });

  it("prefers high confidence when test is both direct and transitive", () => {
    const graph = makeGraph({
      sourceToTests: {
        "pkg/src/a.ts": ["pkg/tests/a.test.ts"],
        "pkg/src/b.ts": ["pkg/tests/a.test.ts"],
      },
      testToSources: {
        "pkg/tests/a.test.ts": ["pkg/src/a.ts", "pkg/src/b.ts"],
      },
      importGraph: {
        "pkg/tests/a.test.ts": ["pkg/src/a.ts"],
        "pkg/src/a.ts": ["pkg/src/b.ts"],
        "pkg/src/b.ts": [],
      },
    });

    // Change both a.ts (direct) and b.ts (transitive) — should be high
    const changedFiles: DiffEntry[] = [
      { status: "modified", path: "pkg/src/a.ts" },
      { status: "modified", path: "pkg/src/b.ts" },
    ];
    const result = analyzeImpact(changedFiles, graph);
    const testEntry = result.affectedTests.find((t) => t.testFile === "pkg/tests/a.test.ts");
    expect(testEntry?.confidence).toBe("high");
  });
});

// ---------------------------------------------------------------------------
// analyzeImpact — edge cases
// ---------------------------------------------------------------------------

describe("analyzeImpact — edge cases", () => {
  it("returns tests for deleted files still in graph", () => {
    const graph = makeGraph({
      sourceToTests: {
        "pkg/src/old.ts": ["pkg/tests/old.test.ts"],
      },
      testToSources: {
        "pkg/tests/old.test.ts": ["pkg/src/old.ts"],
      },
      importGraph: {
        "pkg/tests/old.test.ts": ["pkg/src/old.ts"],
        "pkg/src/old.ts": [],
      },
    });

    const changedFiles: DiffEntry[] = [{ status: "deleted", path: "pkg/src/old.ts" }];
    const result = analyzeImpact(changedFiles, graph);
    expect(result.affectedTests).toHaveLength(1);
    expect(result.affectedTests[0].testFile).toBe("pkg/tests/old.test.ts");
    expect(result.affectedTests[0].confidence).toBe("high");
  });

  it("queries both old and new paths for renamed files", () => {
    const graph = makeGraph({
      sourceToTests: {
        "pkg/src/old-name.ts": ["pkg/tests/old-name.test.ts"],
      },
      testToSources: {
        "pkg/tests/old-name.test.ts": ["pkg/src/old-name.ts"],
      },
      importGraph: {
        "pkg/tests/old-name.test.ts": ["pkg/src/old-name.ts"],
        "pkg/src/old-name.ts": [],
      },
    });

    const changedFiles: DiffEntry[] = [
      {
        status: "renamed",
        path: "pkg/src/new-name.ts",
        oldPath: "pkg/src/old-name.ts",
      },
    ];
    const result = analyzeImpact(changedFiles, graph);

    // Should find tests via the old path
    expect(result.affectedTests).toHaveLength(1);
    expect(result.affectedTests[0].testFile).toBe("pkg/tests/old-name.test.ts");

    // affectedSources should include both old and new paths
    expect(result.affectedSources).toHaveLength(2);
    const newPathSource = result.affectedSources.find(
      (s) => s.sourceFile === "pkg/src/new-name.ts"
    );
    const oldPathSource = result.affectedSources.find(
      (s) => s.sourceFile === "pkg/src/old-name.ts"
    );
    expect(newPathSource?.hasGraphEntry).toBe(false);
    expect(oldPathSource?.hasGraphEntry).toBe(true);
  });

  it("includes changed test files with high confidence", () => {
    const graph = makeGraph({
      sourceToTests: {},
      testToSources: {
        "pkg/tests/utils.test.ts": ["pkg/src/utils.ts"],
      },
      importGraph: {
        "pkg/tests/utils.test.ts": ["pkg/src/utils.ts"],
      },
    });

    const changedFiles: DiffEntry[] = [{ status: "modified", path: "pkg/tests/utils.test.ts" }];
    const result = analyzeImpact(changedFiles, graph);
    expect(result.affectedTests).toHaveLength(1);
    expect(result.affectedTests[0].testFile).toBe("pkg/tests/utils.test.ts");
    expect(result.affectedTests[0].confidence).toBe("high");
    expect(result.affectedTests[0].matchType).toBe("direct");
  });

  it("handles empty changed files list", () => {
    const graph = makeGraph({
      sourceToTests: {},
      testToSources: {},
      importGraph: {},
    });

    const result = analyzeImpact([], graph);
    expect(result.impactLevel).toBe("isolated");
    expect(result.affectedTests).toHaveLength(0);
    expect(result.affectedSources).toHaveLength(0);
    expect(result.summary.impactLevelReason).toBe("No files changed");
  });

  it("returns empty affected tests for unknown file with no graph entry", () => {
    const graph = makeGraph({
      sourceToTests: {},
      testToSources: {},
      importGraph: {},
    });

    const changedFiles: DiffEntry[] = [{ status: "modified", path: "pkg/src/unknown.ts" }];
    const result = analyzeImpact(changedFiles, graph);
    // No graph entry + no heuristic match (projectRoot is /fake) → empty
    expect(result.affectedTests).toHaveLength(0);
    expect(result.affectedSources[0].hasGraphEntry).toBe(false);
  });
});

// ---------------------------------------------------------------------------
// analyzeImpact — heuristic fallback (requires temp dirs)
// ---------------------------------------------------------------------------

describe("analyzeImpact — heuristic fallback", () => {
  let tempDir: string;

  beforeEach(async () => {
    tempDir = await mkdtemp(join(tmpdir(), "cia-test-"));
  });

  afterEach(async () => {
    await rm(tempDir, { recursive: true, force: true });
  });

  it("falls back to heuristic for new files not in graph", async () => {
    // Create a test file that matches the naming convention
    const testPath = join(tempDir, "tests", "services", "auth.test.ts");
    await mkdir(join(testPath, ".."), { recursive: true });
    await writeFile(testPath, 'describe("auth", () => {});', "utf-8");

    const graph = makeGraph({
      projectRoot: tempDir,
      sourceToTests: {},
      testToSources: {},
      importGraph: {},
    });

    // New file — no graph entry → heuristic finds the test file
    const changedFiles: DiffEntry[] = [{ status: "added", path: "src/services/auth.ts" }];
    const result = analyzeImpact(changedFiles, graph);

    expect(result.affectedTests).toHaveLength(1);
    expect(result.affectedTests[0].testFile).toBe("tests/services/auth.test.ts");
    expect(result.affectedTests[0].confidence).toBe("low");
    expect(result.affectedTests[0].matchType).toBe("heuristic");
  });

  it("returns empty when heuristic finds no matching test files", async () => {
    // Temp dir exists but has no test files
    const graph = makeGraph({
      projectRoot: tempDir,
      sourceToTests: {},
      testToSources: {},
      importGraph: {},
    });

    const changedFiles: DiffEntry[] = [{ status: "added", path: "src/orphan.ts" }];
    const result = analyzeImpact(changedFiles, graph);
    expect(result.affectedTests).toHaveLength(0);
  });
});

// ---------------------------------------------------------------------------
// analyzeImpactFromDiff
// ---------------------------------------------------------------------------

describe("analyzeImpactFromDiff", () => {
  it("parses diff and analyzes impact in one call", () => {
    const graph = makeGraph({
      sourceToTests: {
        "pkg/src/utils.ts": ["pkg/tests/utils.test.ts"],
      },
      testToSources: {
        "pkg/tests/utils.test.ts": ["pkg/src/utils.ts"],
      },
      importGraph: {
        "pkg/tests/utils.test.ts": ["pkg/src/utils.ts"],
        "pkg/src/utils.ts": [],
      },
    });

    const diff = "M\tpkg/src/utils.ts\n";
    const result = analyzeImpactFromDiff(diff, graph);

    expect(result.impactLevel).toBe("isolated");
    expect(result.affectedTests).toHaveLength(1);
    expect(result.affectedTests[0].testFile).toBe("pkg/tests/utils.test.ts");
    expect(result.affectedTests[0].confidence).toBe("high");
    expect(result.changedFiles).toHaveLength(1);
    expect(result.changedFiles[0].status).toBe("modified");
  });

  it("handles multi-status diff with confidence scoring", () => {
    const graph = makeGraph({
      sourceToTests: {
        "pkg/src/a.ts": ["pkg/tests/a.test.ts"],
        "pkg/src/b.ts": ["pkg/tests/a.test.ts"],
      },
      testToSources: {
        "pkg/tests/a.test.ts": ["pkg/src/a.ts", "pkg/src/b.ts"],
      },
      importGraph: {
        "pkg/tests/a.test.ts": ["pkg/src/a.ts"],
        "pkg/src/a.ts": ["pkg/src/b.ts"],
        "pkg/src/b.ts": [],
      },
    });

    // Only change b.ts (transitive dep) — should produce medium confidence
    const diff = "M\tpkg/src/b.ts\n";
    const result = analyzeImpactFromDiff(diff, graph);

    expect(result.affectedTests).toHaveLength(1);
    expect(result.affectedTests[0].confidence).toBe("medium");
    expect(result.affectedTests[0].matchType).toBe("transitive");
  });
});
