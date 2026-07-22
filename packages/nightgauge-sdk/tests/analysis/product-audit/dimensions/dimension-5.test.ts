/**
 * Unit tests for Dimension 5: Test Coverage
 *
 * Tests lcov.info parsing, coverage threshold detection,
 * missing test file detection, and repo-level graceful degradation.
 */

import { describe, it, expect, vi, beforeEach } from "vitest";
import { parseLcov } from "../../../../src/analysis/product-audit/utils/coverage-parser.js";

// ── parseLcov tests ───────────────────────────────────────────────────────────

describe("parseLcov", () => {
  it("parses a minimal lcov.info with one file", () => {
    const content = `
SF:src/foo.ts
LF:10
LH:8
BRF:4
BRH:3
end_of_record
`.trim();

    const result = parseLcov(content);
    expect(result.files).toHaveLength(1);
    const file = result.files[0];
    expect(file.path).toBe("src/foo.ts");
    expect(file.linesFound).toBe(10);
    expect(file.linesHit).toBe(8);
    expect(file.linePercent).toBeCloseTo(80);
    expect(file.branchPercent).toBeCloseTo(75);
  });

  it("returns 0% coverage for a file with linesHit=0", () => {
    const content = `
SF:src/uncovered.ts
LF:20
LH:0
end_of_record
`.trim();

    const result = parseLcov(content);
    expect(result.files[0].linePercent).toBe(0);
  });

  it("returns null linePercent when linesFound=0", () => {
    const content = `
SF:src/empty.ts
LF:0
LH:0
end_of_record
`.trim();

    const result = parseLcov(content);
    expect(result.files[0].linePercent).toBeNull();
  });

  it("parses multiple files and computes overall coverage", () => {
    const content = `
SF:src/a.ts
LF:10
LH:10
end_of_record
SF:src/b.ts
LF:10
LH:0
end_of_record
`.trim();

    const result = parseLcov(content);
    expect(result.files).toHaveLength(2);
    // 10 + 0 hit out of 20 found = 50%
    expect(result.overallLinePercent).toBeCloseTo(50);
  });

  it("handles missing SF line gracefully with a warning", () => {
    const content = `
LF:10
LH:5
end_of_record
`.trim();

    const result = parseLcov(content);
    expect(result.files).toHaveLength(0);
    expect(result.warnings).toHaveLength(1);
    expect(result.warnings[0]).toContain("SF:");
  });

  it("handles empty input", () => {
    const result = parseLcov("");
    expect(result.files).toHaveLength(0);
    expect(result.overallLinePercent).toBeNull();
    expect(result.overallBranchPercent).toBeNull();
  });

  it("handles malformed numeric fields gracefully", () => {
    const content = `
SF:src/broken.ts
LF:not-a-number
LH:also-bad
end_of_record
`.trim();

    const result = parseLcov(content);
    expect(result.files).toHaveLength(1);
    // NaN → 0 after parseInt check
    expect(result.files[0].linesFound).toBe(0);
    expect(result.files[0].linePercent).toBeNull();
  });

  it("parses branch coverage separately from line coverage", () => {
    const content = `
SF:src/branchy.ts
LF:5
LH:5
BRF:10
BRH:6
end_of_record
`.trim();

    const result = parseLcov(content);
    const file = result.files[0];
    expect(file.linePercent).toBe(100);
    expect(file.branchPercent).toBeCloseTo(60);
  });

  it("computes null overallBranchPercent when no branch data exists", () => {
    const content = `
SF:src/foo.ts
LF:5
LH:5
end_of_record
`.trim();

    const result = parseLcov(content);
    expect(result.overallBranchPercent).toBeNull();
  });

  it("handles multiple end_of_record sections correctly", () => {
    const lines = Array.from(
      { length: 5 },
      (_, i) => `SF:src/file${i}.ts\nLF:100\nLH:${i * 20}\nend_of_record`
    ).join("\n");

    const result = parseLcov(lines);
    expect(result.files).toHaveLength(5);
  });
});

// ── Dimension5 integration tests (using mocked fs) ───────────────────────────

import { runDimension5 } from "../../../../src/analysis/product-audit/dimensions/dimension-5-coverage.js";
import * as fs from "fs";
import * as path from "path";
import * as childProcess from "child_process";

vi.mock("fs");
vi.mock("child_process");

const mockFs = vi.mocked(fs);
const mockExecSync = vi.mocked(childProcess.execSync);

describe("runDimension5", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    // Default: repo exists
    mockFs.existsSync.mockReturnValue(true);
    mockFs.readdirSync.mockReturnValue([] as unknown as ReturnType<typeof fs.readdirSync>);
  });

  it("returns no findings when no lcov.info found in quick mode", async () => {
    // No lcov.info files
    mockFs.existsSync.mockImplementation((p: fs.PathLike) => {
      const s = p.toString();
      return s === "/repo"; // only the repo root exists
    });

    const result = await runDimension5(
      [{ name: "nightgauge", root: "/repo" }],
      true // quick mode — no command execution
    );

    expect(result.findings).toHaveLength(0);
    expect(result.repos_scanned).toContain("nightgauge");
    expect(result.warnings.some((w) => w.includes("No lcov.info available"))).toBe(true);
    expect(mockExecSync).not.toHaveBeenCalled();
  });

  it("marks repo as missing when root does not exist", async () => {
    mockFs.existsSync.mockReturnValue(false);

    const result = await runDimension5([{ name: "missing-repo", root: "/does/not/exist" }], true);

    expect(result.repos_missing).toContain("missing-repo");
    expect(result.repos_scanned).toHaveLength(0);
    expect(result.findings).toHaveLength(0);
  });

  it("generates critical finding for 0% coverage file", async () => {
    const lcovContent = `
SF:src/uncovered.ts
LF:50
LH:0
end_of_record
`.trim();

    mockFs.existsSync.mockImplementation((p: fs.PathLike) => {
      const s = p.toString();
      if (s === "/repo") return true;
      if (s === "/repo/coverage/lcov.info") return true;
      return false;
    });
    mockFs.readFileSync.mockImplementation((p: fs.PathLike) => {
      if (p.toString().includes("lcov")) return lcovContent;
      throw new Error("not found");
    });

    const result = await runDimension5([{ name: "nightgauge", root: "/repo" }], true);

    expect(result.findings).toHaveLength(1);
    const finding = result.findings[0];
    expect(finding.severity).toBe("critical");
    expect(finding.coverage_percent).toBe(0);
    expect(finding.category).toBe("CRITICAL_PATH_UNCOVERED");
    expect(finding.id).toMatch(/^coverage-001-/);
  });

  it("generates medium finding for 40% coverage file", async () => {
    const lcovContent = `
SF:src/partial.ts
LF:100
LH:40
end_of_record
`.trim();

    mockFs.existsSync.mockImplementation((p: fs.PathLike) => {
      const s = p.toString();
      return s === "/repo" || s === "/repo/coverage/lcov.info";
    });
    mockFs.readFileSync.mockImplementation(() => lcovContent);

    const result = await runDimension5([{ name: "nightgauge", root: "/repo" }], true);

    expect(result.findings).toHaveLength(1);
    expect(result.findings[0].severity).toBe("medium");
    expect(result.findings[0].coverage_percent).toBeCloseTo(40);
  });

  it("does not generate findings for files at or above 80% threshold", async () => {
    const lcovContent = `
SF:src/well-tested.ts
LF:100
LH:85
end_of_record
`.trim();

    mockFs.existsSync.mockImplementation((p: fs.PathLike) => {
      const s = p.toString();
      return s === "/repo" || s === "/repo/coverage/lcov.info";
    });
    mockFs.readFileSync.mockImplementation(() => lcovContent);

    const result = await runDimension5([{ name: "nightgauge", root: "/repo" }], true);

    expect(result.findings).toHaveLength(0);
  });

  it("sorts findings by severity (critical first)", async () => {
    const lcovContent = `
SF:src/partial.ts
LF:100
LH:60
end_of_record
SF:src/uncovered.ts
LF:50
LH:0
end_of_record
`.trim();

    mockFs.existsSync.mockImplementation((p: fs.PathLike) => {
      const s = p.toString();
      return s === "/repo" || s === "/repo/coverage/lcov.info";
    });
    mockFs.readFileSync.mockImplementation(() => lcovContent);

    const result = await runDimension5([{ name: "nightgauge", root: "/repo" }], true);

    expect(result.findings.length).toBeGreaterThanOrEqual(2);
    expect(result.findings[0].severity).toBe("critical");
  });

  it("records overall coverage percentage per repo", async () => {
    const lcovContent = `
SF:src/foo.ts
LF:100
LH:75
end_of_record
`.trim();

    mockFs.existsSync.mockImplementation((p: fs.PathLike) => {
      const s = p.toString();
      return s === "/repo" || s === "/repo/coverage/lcov.info";
    });
    mockFs.readFileSync.mockImplementation(() => lcovContent);

    const result = await runDimension5([{ name: "nightgauge", root: "/repo" }], true);

    expect(result.overall_coverage["nightgauge"]).toBeCloseTo(75);
  });
});
