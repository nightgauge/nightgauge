/**
 * Tests for testFileMapper - source-to-test file mapping utility
 *
 * @see Issue #1046 - Optimize feature-validate stage cost
 */

import { describe, it, expect, vi, beforeEach } from "vitest";
import { mapSourceToTestFiles, isCrossCuttingChange } from "../testFileMapper";

// Mock fs.existsSync to control which test files "exist"
vi.mock("fs", () => ({
  existsSync: vi.fn((path: string) => {
    // Track which paths are checked and return true for configured ones
    return mockExistingFiles.some((f) => path.endsWith(f));
  }),
}));

let mockExistingFiles: string[] = [];

beforeEach(() => {
  mockExistingFiles = [];
});

describe("mapSourceToTestFiles", () => {
  const projectRoot = "/project";

  it("maps a single source file to tests/foo/bar.test.ts", () => {
    mockExistingFiles = ["tests/foo/bar.test.ts"];
    const result = mapSourceToTestFiles(["src/foo/bar.ts"], projectRoot);
    expect(result).toEqual(["tests/foo/bar.test.ts"]);
  });

  it("maps a source file to __tests__ sibling pattern", () => {
    mockExistingFiles = ["src/foo/__tests__/bar.test.ts"];
    const result = mapSourceToTestFiles(["src/foo/bar.ts"], projectRoot);
    expect(result).toEqual(["src/foo/__tests__/bar.test.ts"]);
  });

  it("maps a source file to src/__tests__/foo/bar.test.ts", () => {
    mockExistingFiles = ["src/__tests__/foo/bar.test.ts"];
    const result = mapSourceToTestFiles(["src/foo/bar.ts"], projectRoot);
    expect(result).toEqual(["src/__tests__/foo/bar.test.ts"]);
  });

  it("returns multiple matching test files for one source", () => {
    mockExistingFiles = ["tests/foo/bar.test.ts", "src/foo/__tests__/bar.test.ts"];
    const result = mapSourceToTestFiles(["src/foo/bar.ts"], projectRoot);
    expect(result).toHaveLength(2);
    expect(result).toContain("tests/foo/bar.test.ts");
    expect(result).toContain("src/foo/__tests__/bar.test.ts");
  });

  it("maps multiple source files correctly", () => {
    mockExistingFiles = ["tests/a/one.test.ts", "tests/b/two.test.ts"];
    const result = mapSourceToTestFiles(["src/a/one.ts", "src/b/two.ts"], projectRoot);
    expect(result).toHaveLength(2);
    expect(result).toContain("tests/a/one.test.ts");
    expect(result).toContain("tests/b/two.test.ts");
  });

  it("excludes non-existent test files", () => {
    mockExistingFiles = []; // no test files exist
    const result = mapSourceToTestFiles(["src/foo/bar.ts"], projectRoot);
    expect(result).toEqual([]);
  });

  it("passes through test files directly", () => {
    mockExistingFiles = ["tests/foo/bar.test.ts"];
    const result = mapSourceToTestFiles(["tests/foo/bar.test.ts"], projectRoot);
    expect(result).toEqual(["tests/foo/bar.test.ts"]);
  });

  it("deduplicates when same test file matched by multiple sources", () => {
    mockExistingFiles = ["tests/foo/bar.test.ts"];
    const result = mapSourceToTestFiles(["src/foo/bar.ts", "tests/foo/bar.test.ts"], projectRoot);
    expect(result).toEqual(["tests/foo/bar.test.ts"]);
  });

  it("handles src/ root-level files", () => {
    mockExistingFiles = ["tests/utils.test.ts"];
    const result = mapSourceToTestFiles(["src/utils.ts"], projectRoot);
    expect(result).toEqual(["tests/utils.test.ts"]);
  });

  it("skips non-source files (e.g., .json, .md)", () => {
    mockExistingFiles = [];
    const result = mapSourceToTestFiles(["src/config.json", "README.md"], projectRoot);
    expect(result).toEqual([]);
  });

  it("handles empty input", () => {
    const result = mapSourceToTestFiles([], projectRoot);
    expect(result).toEqual([]);
  });

  it("handles .tsx files mapping to .test.tsx", () => {
    mockExistingFiles = ["tests/components/Button.test.tsx"];
    const result = mapSourceToTestFiles(["src/components/Button.tsx"], projectRoot);
    expect(result).toEqual(["tests/components/Button.test.tsx"]);
  });

  it("handles mixed test and source files", () => {
    mockExistingFiles = ["tests/foo/bar.test.ts", "src/baz/__tests__/qux.test.ts"];
    const result = mapSourceToTestFiles(
      ["src/foo/bar.ts", "src/baz/qux.ts", "tests/foo/bar.test.ts"],
      projectRoot
    );
    expect(result).toHaveLength(2);
    expect(result).toContain("tests/foo/bar.test.ts");
    expect(result).toContain("src/baz/__tests__/qux.test.ts");
  });
});

describe("isCrossCuttingChange", () => {
  it("returns false for small, localized changes", () => {
    const files = ["src/foo/a.ts", "src/foo/b.ts"];
    expect(isCrossCuttingChange(files)).toBe(false);
  });

  it("returns true when file count exceeds threshold", () => {
    const files = Array.from({ length: 11 }, (_, i) => `src/foo/file${i}.ts`);
    expect(isCrossCuttingChange(files)).toBe(true);
  });

  it("returns true at threshold + 1", () => {
    const files = Array.from({ length: 11 }, (_, i) => `src/foo/file${i}.ts`);
    expect(isCrossCuttingChange(files, 10)).toBe(true);
  });

  it("returns false at exactly threshold count", () => {
    const files = Array.from({ length: 10 }, (_, i) => `src/foo/file${i}.ts`);
    expect(isCrossCuttingChange(files, 10)).toBe(false);
  });

  it("returns true when files span more than 3 top-level directories", () => {
    const files = ["src/a/file.ts", "packages/b/file.ts", "tests/c/file.ts", "scripts/d/file.ts"];
    expect(isCrossCuttingChange(files)).toBe(true);
  });

  it("returns false when files span exactly 3 directories", () => {
    const files = ["src/a/file.ts", "packages/b/file.ts", "tests/c/file.ts"];
    expect(isCrossCuttingChange(files)).toBe(false);
  });

  it("allows custom threshold", () => {
    const files = Array.from({ length: 6 }, (_, i) => `src/foo/file${i}.ts`);
    expect(isCrossCuttingChange(files, 5)).toBe(true);
    expect(isCrossCuttingChange(files, 6)).toBe(false);
  });

  it("handles empty input", () => {
    expect(isCrossCuttingChange([])).toBe(false);
  });

  it("handles files without directory", () => {
    // Single file at root level — only 0 top-level dirs counted (no slash)
    const files = ["file.ts"];
    expect(isCrossCuttingChange(files)).toBe(false);
  });
});
