/**
 * Tests for Source-to-Test Dependency Graph
 *
 * Uses real temp directories with fixture TypeScript files (no fs mocking).
 * @see Issue #1970 - Source-to-test dependency graph
 */

import { mkdtemp, mkdir, writeFile, rm } from "fs/promises";
import { tmpdir } from "os";
import { join } from "path";
import { describe, it, expect, beforeEach, afterEach } from "vitest";

import {
  extractImports,
  buildSourceToTestGraph,
  getAffectedTests,
  serializeGraph,
  deserializeGraph,
  saveGraph,
  loadGraph,
} from "../../src/analysis/SourceToTestGraph.js";
import { DependencyGraphSchema } from "../../src/analysis/graph-types.js";
import type { DependencyGraph, BuildOptions } from "../../src/analysis/graph-types.js";

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

let tempDir: string;

async function createFixture(relativePath: string, content: string): Promise<void> {
  const fullPath = join(tempDir, relativePath);
  await mkdir(join(fullPath, ".."), { recursive: true });
  await writeFile(fullPath, content, "utf-8");
}

function buildOpts(overrides: Partial<BuildOptions> = {}): BuildOptions {
  return {
    projectRoot: tempDir,
    packages: ["pkg"],
    ...overrides,
  };
}

// ---------------------------------------------------------------------------
// extractImports
// ---------------------------------------------------------------------------

describe("extractImports", () => {
  it("extracts static imports", () => {
    const content = `
      import { foo } from './foo';
      import bar from './bar';
    `;
    const result = extractImports(content);
    expect(result).toContain("./foo");
    expect(result).toContain("./bar");
  });

  it("extracts re-exports", () => {
    const content = `
      export { foo } from './foo';
      export * from './bar';
    `;
    const result = extractImports(content);
    expect(result).toContain("./foo");
    expect(result).toContain("./bar");
  });

  it("extracts dynamic imports", () => {
    const content = `
      const mod = await import('./dynamic');
    `;
    const result = extractImports(content);
    expect(result).toContain("./dynamic");
  });

  it("extracts require calls", () => {
    const content = `
      const x = require('./cjs');
    `;
    const result = extractImports(content);
    expect(result).toContain("./cjs");
  });

  it("ignores node_modules imports", () => {
    const content = `
      import { z } from 'zod';
      import path from 'path';
      import { foo } from './local';
    `;
    const result = extractImports(content);
    expect(result).toEqual(["./local"]);
  });

  it("deduplicates identical import paths", () => {
    const content = `
      import { a } from './same';
      import { b } from './same';
    `;
    const result = extractImports(content);
    expect(result).toEqual(["./same"]);
  });

  it("handles imports with .js extension", () => {
    const content = `
      import { foo } from './foo.js';
    `;
    const result = extractImports(content);
    expect(result).toContain("./foo.js");
  });
});

// ---------------------------------------------------------------------------
// buildSourceToTestGraph — file-system integration tests
// ---------------------------------------------------------------------------

describe("buildSourceToTestGraph", () => {
  beforeEach(async () => {
    tempDir = await mkdtemp(join(tmpdir(), "stg-test-"));
  });

  afterEach(async () => {
    await rm(tempDir, { recursive: true, force: true });
  });

  it("maps test file to directly imported source file", async () => {
    await createFixture(
      "pkg/src/utils.ts",
      "export const add = (a: number, b: number) => a + b;\n"
    );
    await createFixture(
      "pkg/tests/utils.test.ts",
      `import { add } from '../src/utils.js';\n` +
        `describe('add', () => { it('works', () => { expect(add(1,2)).toBe(3); }); });\n`
    );

    const graph = await buildSourceToTestGraph(buildOpts());

    expect(graph.sourceToTests["pkg/src/utils.ts"]).toContain("pkg/tests/utils.test.ts");
    expect(graph.testToSources["pkg/tests/utils.test.ts"]).toContain("pkg/src/utils.ts");
  });

  it("maps test file to transitively imported source files (A→B→C)", async () => {
    await createFixture("pkg/src/c.ts", "export const c = 1;\n");
    await createFixture("pkg/src/b.ts", `import { c } from './c.js';\nexport const b = c + 1;\n`);
    await createFixture("pkg/src/a.ts", `import { b } from './b.js';\nexport const a = b + 1;\n`);
    await createFixture(
      "pkg/tests/a.test.ts",
      `import { a } from '../src/a.js';\ndescribe('a', () => { it('works', () => {}); });\n`
    );

    const graph = await buildSourceToTestGraph(buildOpts());

    // Test for A should transitively cover B and C
    const sources = graph.testToSources["pkg/tests/a.test.ts"];
    expect(sources).toContain("pkg/src/a.ts");
    expect(sources).toContain("pkg/src/b.ts");
    expect(sources).toContain("pkg/src/c.ts");

    // Inversely, C should map to the test for A
    expect(graph.sourceToTests["pkg/src/c.ts"]).toContain("pkg/tests/a.test.ts");
  });

  it("inverts correctly: source file maps to all covering test files", async () => {
    await createFixture("pkg/src/shared.ts", "export const shared = 1;\n");
    await createFixture(
      "pkg/tests/test1.test.ts",
      `import { shared } from '../src/shared.js';\ndescribe('t1', () => { it('', () => {}); });\n`
    );
    await createFixture(
      "pkg/tests/test2.test.ts",
      `import { shared } from '../src/shared.js';\ndescribe('t2', () => { it('', () => {}); });\n`
    );

    const graph = await buildSourceToTestGraph(buildOpts());

    const testsCovering = graph.sourceToTests["pkg/src/shared.ts"];
    expect(testsCovering).toContain("pkg/tests/test1.test.ts");
    expect(testsCovering).toContain("pkg/tests/test2.test.ts");
    expect(testsCovering).toHaveLength(2);
  });

  it("handles packages with no test files gracefully", async () => {
    await createFixture("pkg/src/lonely.ts", "export const lonely = 1;\n");

    const graph = await buildSourceToTestGraph(buildOpts());

    expect(graph.sourceToTests["pkg/src/lonely.ts"]).toEqual([]);
    expect(Object.keys(graph.testToSources)).toHaveLength(0);
  });

  it("handles cycles without infinite loop", async () => {
    await createFixture(
      "pkg/src/cycleA.ts",
      `import { b } from './cycleB.js';\nexport const a = 1;\n`
    );
    await createFixture(
      "pkg/src/cycleB.ts",
      `import { a } from './cycleA.js';\nexport const b = 2;\n`
    );
    await createFixture(
      "pkg/tests/cycle.test.ts",
      `import { a } from '../src/cycleA.js';\ndescribe('cycle', () => { it('works', () => {}); });\n`
    );

    const graph = await buildSourceToTestGraph(buildOpts());

    const sources = graph.testToSources["pkg/tests/cycle.test.ts"];
    expect(sources).toContain("pkg/src/cycleA.ts");
    expect(sources).toContain("pkg/src/cycleB.ts");
  });

  it("resolves .ts extension correctly", async () => {
    await createFixture("pkg/src/helper.ts", "export const helper = true;\n");
    await createFixture(
      "pkg/tests/helper.test.ts",
      `import { helper } from '../src/helper';\ndescribe('h', () => { it('', () => {}); });\n`
    );

    const graph = await buildSourceToTestGraph(buildOpts());

    expect(graph.sourceToTests["pkg/src/helper.ts"]).toContain("pkg/tests/helper.test.ts");
  });

  it("resolves index.ts barrels", async () => {
    await createFixture("pkg/src/utils/index.ts", "export const util = 1;\n");
    await createFixture(
      "pkg/tests/barrel.test.ts",
      `import { util } from '../src/utils';\ndescribe('barrel', () => { it('', () => {}); });\n`
    );

    const graph = await buildSourceToTestGraph(buildOpts());

    expect(graph.sourceToTests["pkg/src/utils/index.ts"]).toContain("pkg/tests/barrel.test.ts");
  });

  it("includes correct metadata in graph", async () => {
    await createFixture("pkg/src/meta.ts", "export const meta = 1;\n");

    const graph = await buildSourceToTestGraph(buildOpts());

    expect(graph.version).toBe("1.0");
    expect(graph.projectRoot).toBe(tempDir);
    expect(graph.packages).toEqual(["pkg"]);
    expect(graph.generatedAt).toBeTruthy();
    // Validate via Zod schema
    expect(() => DependencyGraphSchema.parse(graph)).not.toThrow();
  });

  it("completes within 30s benchmark for actual SDK package", async () => {
    // Integration benchmark: run against the actual SDK package
    const sdkRoot = join(__dirname, "..", "..");
    const projectRoot = join(sdkRoot, "..", "..");

    const start = Date.now();
    const graph = await buildSourceToTestGraph({
      projectRoot,
      packages: ["packages/nightgauge-sdk"],
    });
    const elapsed = Date.now() - start;

    expect(elapsed).toBeLessThan(30_000);
    expect(Object.keys(graph.importGraph).length).toBeGreaterThan(0);
    expect(Object.keys(graph.sourceToTests).length).toBeGreaterThan(0);
  });
});

// ---------------------------------------------------------------------------
// getAffectedTests
// ---------------------------------------------------------------------------

describe("getAffectedTests", () => {
  const graph: DependencyGraph = {
    version: "1.0",
    generatedAt: new Date().toISOString(),
    projectRoot: "/project",
    packages: ["pkg"],
    sourceToTests: {
      "pkg/src/a.ts": ["pkg/tests/a.test.ts", "pkg/tests/ab.test.ts"],
      "pkg/src/b.ts": ["pkg/tests/ab.test.ts"],
      "pkg/src/c.ts": [],
    },
    testToSources: {
      "pkg/tests/a.test.ts": ["pkg/src/a.ts"],
      "pkg/tests/ab.test.ts": ["pkg/src/a.ts", "pkg/src/b.ts"],
    },
    importGraph: {
      "pkg/tests/a.test.ts": ["pkg/src/a.ts"],
      "pkg/tests/ab.test.ts": ["pkg/src/a.ts"],
      "pkg/src/a.ts": ["pkg/src/b.ts"],
      "pkg/src/b.ts": [],
      "pkg/src/c.ts": [],
    },
  };

  it("returns test files for directly changed source file", () => {
    const result = getAffectedTests(["pkg/src/a.ts"], graph);
    expect(result.affectedTests).toContain("pkg/tests/a.test.ts");
    expect(result.affectedTests).toContain("pkg/tests/ab.test.ts");
  });

  it("returns test files for transitively changed source file", () => {
    const result = getAffectedTests(["pkg/src/b.ts"], graph);
    expect(result.affectedTests).toContain("pkg/tests/ab.test.ts");
  });

  it("includes test file itself when a test file is changed", () => {
    const result = getAffectedTests(["pkg/tests/a.test.ts"], graph);
    expect(result.affectedTests).toContain("pkg/tests/a.test.ts");
    expect(result.directMatches).toContain("pkg/tests/a.test.ts");
  });

  it("returns empty array for unknown file", () => {
    const result = getAffectedTests(["pkg/src/unknown.ts"], graph);
    expect(result.affectedTests).toEqual([]);
  });

  it("deduplicates results", () => {
    // a.ts and b.ts both map to ab.test.ts — should only appear once
    const result = getAffectedTests(["pkg/src/a.ts", "pkg/src/b.ts"], graph);
    const abCount = result.affectedTests.filter((f) => f === "pkg/tests/ab.test.ts").length;
    expect(abCount).toBe(1);
  });

  it("classifies direct vs transitive matches", () => {
    // ab.test.ts directly imports a.ts; b.ts is reached transitively via a.ts→b.ts
    const result = getAffectedTests(["pkg/src/b.ts"], graph);
    // ab.test.ts does NOT directly import b.ts (it imports a.ts which imports b.ts)
    expect(result.transitiveMatches).toContain("pkg/tests/ab.test.ts");
  });
});

// ---------------------------------------------------------------------------
// Serialization
// ---------------------------------------------------------------------------

describe("serialization", () => {
  const graph: DependencyGraph = {
    version: "1.0",
    generatedAt: "2026-03-13T00:00:00.000Z",
    projectRoot: "/project",
    packages: ["pkg"],
    sourceToTests: { "src/a.ts": ["tests/a.test.ts"] },
    testToSources: { "tests/a.test.ts": ["src/a.ts"] },
    importGraph: { "tests/a.test.ts": ["src/a.ts"], "src/a.ts": [] },
  };

  it("round-trips through serializeGraph/deserializeGraph", () => {
    const json = serializeGraph(graph);
    const restored = deserializeGraph(json);
    expect(restored).toEqual(graph);
  });

  it("rejects invalid JSON schema via Zod", () => {
    const invalid = JSON.stringify({ version: "2.0", bad: true });
    expect(() => deserializeGraph(invalid)).toThrow();
  });
});

// ---------------------------------------------------------------------------
// File-based save/load
// ---------------------------------------------------------------------------

describe("saveGraph / loadGraph", () => {
  let saveTempDir: string;

  beforeEach(async () => {
    saveTempDir = await mkdtemp(join(tmpdir(), "stg-save-"));
  });

  afterEach(async () => {
    await rm(saveTempDir, { recursive: true, force: true });
  });

  it("saves and loads graph from disk", async () => {
    const graph: DependencyGraph = {
      version: "1.0",
      generatedAt: "2026-03-13T00:00:00.000Z",
      projectRoot: "/project",
      packages: ["pkg"],
      sourceToTests: { "src/a.ts": ["tests/a.test.ts"] },
      testToSources: { "tests/a.test.ts": ["src/a.ts"] },
      importGraph: { "tests/a.test.ts": ["src/a.ts"], "src/a.ts": [] },
    };

    const filePath = join(saveTempDir, "graph.json");
    await saveGraph(graph, filePath);
    const loaded = await loadGraph(filePath);

    expect(loaded).toEqual(graph);
  });
});

// ---------------------------------------------------------------------------
// Coverage enrichment
// ---------------------------------------------------------------------------

describe("coverage enrichment", () => {
  beforeEach(async () => {
    tempDir = await mkdtemp(join(tmpdir(), "stg-cov-"));
  });

  afterEach(async () => {
    await rm(tempDir, { recursive: true, force: true });
  });

  it("adds coverage-only files when no static mapping exists", async () => {
    await createFixture("pkg/src/static.ts", "export const s = 1;\n");
    await createFixture(
      "pkg/tests/static.test.ts",
      `import { s } from '../src/static.js';\ndescribe('s', () => { it('', () => {}); });\n`
    );

    // Create a coverage file that includes a file not in the import graph
    const coveragePath = join(tempDir, "coverage-final.json");
    const coverageData = {
      [join(tempDir, "pkg/src/dynamic-only.ts")]: {
        s: { "0": 1 },
        b: {},
        f: {},
      },
      [join(tempDir, "pkg/src/static.ts")]: {
        s: { "0": 5 },
        b: {},
        f: {},
      },
    };
    await writeFile(coveragePath, JSON.stringify(coverageData), "utf-8");

    const graph = await buildSourceToTestGraph(buildOpts({ coveragePath }));

    // dynamic-only.ts should appear in sourceToTests with empty array
    expect(graph.sourceToTests["pkg/src/dynamic-only.ts"]).toEqual([]);
    // static.ts should keep its static mapping
    expect(graph.sourceToTests["pkg/src/static.ts"]).toContain("pkg/tests/static.test.ts");
  });

  it("does not overwrite existing static mappings", async () => {
    await createFixture("pkg/src/existing.ts", "export const e = 1;\n");
    await createFixture(
      "pkg/tests/existing.test.ts",
      `import { e } from '../src/existing.js';\ndescribe('e', () => { it('', () => {}); });\n`
    );

    const coveragePath = join(tempDir, "coverage-final.json");
    const coverageData = {
      [join(tempDir, "pkg/src/existing.ts")]: { s: { "0": 1 }, b: {}, f: {} },
    };
    await writeFile(coveragePath, JSON.stringify(coverageData), "utf-8");

    const graph = await buildSourceToTestGraph(buildOpts({ coveragePath }));

    // Static mapping should be preserved — not replaced with empty array
    expect(graph.sourceToTests["pkg/src/existing.ts"]).toContain("pkg/tests/existing.test.ts");
  });

  it("gracefully skips when coverage file does not exist", async () => {
    await createFixture("pkg/src/safe.ts", "export const safe = 1;\n");

    const graph = await buildSourceToTestGraph(
      buildOpts({ coveragePath: "/nonexistent/coverage-final.json" })
    );

    // Should still produce a valid graph
    expect(graph.version).toBe("1.0");
    expect(graph.sourceToTests["pkg/src/safe.ts"]).toEqual([]);
  });
});
