/**
 * Tests for QueryTypes — extension-side query type definitions and conversion functions
 *
 * Covers:
 * - BUILTIN_QUERIES constant
 * - DEFAULT_QUERY_CONFIG constant
 * - toQueryableIssue() conversion
 * - toQueryableIssues() batch conversion
 */

import { describe, it, expect, vi } from "vitest";

// ---------------------------------------------------------------------------
// Mocks
// ---------------------------------------------------------------------------

vi.mock("vscode", () => ({}));

vi.mock("@nightgauge/sdk", () => ({}));

vi.mock("../../src/services/ProjectBoardService", () => ({}));

// ---------------------------------------------------------------------------
// Import after mocks
// ---------------------------------------------------------------------------

import {
  BUILTIN_QUERIES,
  DEFAULT_QUERY_CONFIG,
  toQueryableIssue,
  toQueryableIssues,
} from "../../src/types/QueryTypes";
import type { ReadyIssue } from "../../src/services/ProjectBoardService";

// ---------------------------------------------------------------------------
// ReadyIssue factory
// ---------------------------------------------------------------------------

function makeReadyIssue(overrides: Partial<ReadyIssue> = {}): ReadyIssue {
  return {
    number: 1,
    title: "Test issue",
    labels: ["type:bug"],
    priority: "P0" as any,
    size: "M" as any,
    url: "https://github.com/test/repo/issues/1",
    status: "ready",
    ...overrides,
  } as ReadyIssue;
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

describe("BUILTIN_QUERIES", () => {
  it("is a non-empty array", () => {
    expect(Array.isArray(BUILTIN_QUERIES)).toBe(true);
    expect(BUILTIN_QUERIES.length).toBeGreaterThan(0);
  });

  it("every entry has name, query, and isBuiltIn=true", () => {
    for (const q of BUILTIN_QUERIES) {
      expect(typeof q.name).toBe("string");
      expect(q.name.length).toBeGreaterThan(0);
      expect(typeof q.query).toBe("string");
      expect(q.query.length).toBeGreaterThan(0);
      expect(q.isBuiltIn).toBe(true);
    }
  });

  it("includes expected built-in queries", () => {
    const names = BUILTIN_QUERIES.map((q) => q.name);
    expect(names).toContain("High Priority Ready");
    expect(names).toContain("Small Tasks");
    expect(names).toContain("Recently Updated");
    expect(names).toContain("My Issues");
    expect(names).toContain("Bugs");
  });

  it("has exactly 5 built-in queries", () => {
    expect(BUILTIN_QUERIES).toHaveLength(5);
  });
});

describe("DEFAULT_QUERY_CONFIG", () => {
  it("has expected default values", () => {
    expect(DEFAULT_QUERY_CONFIG.maxHistoryEntries).toBe(20);
    expect(DEFAULT_QUERY_CONFIG.showBuiltInQueries).toBe(true);
    expect(DEFAULT_QUERY_CONFIG.defaultFormat).toBe("tree");
  });
});

describe("toQueryableIssue()", () => {
  it("converts a ReadyIssue to QueryableIssue", () => {
    const ready = makeReadyIssue({
      number: 42,
      title: "Fix login",
      labels: ["type:bug", "critical"],
      priority: "P0" as any,
      size: "M" as any,
      status: "ready",
      url: "https://github.com/test/repo/issues/42",
    });

    const result = toQueryableIssue(ready);

    expect(result.number).toBe(42);
    expect(result.title).toBe("Fix login");
    expect(result.labels).toEqual(["type:bug", "critical"]);
    expect(result.priority).toBe("P0");
    expect(result.size).toBe("M");
    expect(result.status).toBe("ready");
    expect(result.url).toContain("42");
  });

  it("sets assignee, updatedAt, and createdAt to undefined", () => {
    const ready = makeReadyIssue();
    const result = toQueryableIssue(ready);

    expect(result.assignee).toBeUndefined();
    expect(result.updatedAt).toBeUndefined();
    expect(result.createdAt).toBeUndefined();
  });

  it("preserves null priority and size", () => {
    const ready = makeReadyIssue({ priority: null as any, size: null as any });
    const result = toQueryableIssue(ready);
    expect(result.priority).toBeNull();
    expect(result.size).toBeNull();
  });
});

describe("toQueryableIssues()", () => {
  it("converts an array of ReadyIssues", () => {
    const issues = [
      makeReadyIssue({ number: 1, title: "Issue 1" }),
      makeReadyIssue({ number: 2, title: "Issue 2" }),
      makeReadyIssue({ number: 3, title: "Issue 3" }),
    ];

    const result = toQueryableIssues(issues);

    expect(result).toHaveLength(3);
    expect(result[0].number).toBe(1);
    expect(result[1].number).toBe(2);
    expect(result[2].number).toBe(3);
  });

  it("returns empty array for empty input", () => {
    expect(toQueryableIssues([])).toEqual([]);
  });
});
