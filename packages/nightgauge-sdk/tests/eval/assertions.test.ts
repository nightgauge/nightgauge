/**
 * Tests for the cross-model skill eval assertion engine (Issue #3814).
 * Mock-mode only — no live model calls.
 */

import { describe, it, expect } from "vitest";
import { evaluateAssertions, __testing } from "../../src/eval/assertions.js";
import type { EvalAssertion } from "../../src/eval/schemas.js";

describe("evaluateAssertions — contains", () => {
  it("passes when the substring is present", () => {
    const r = evaluateAssertions({ text: "hello world" }, [{ type: "contains", value: "world" }]);
    expect(r.passed).toBe(true);
    expect(r.failures).toHaveLength(0);
  });

  it("fails when the substring is absent and records evidence", () => {
    const r = evaluateAssertions({ text: "hello" }, [{ type: "contains", value: "world" }]);
    expect(r.passed).toBe(false);
    expect(r.failures[0].type).toBe("contains");
    expect(r.failures[0].expected).toBe("world");
  });

  it("respects ignore_case", () => {
    const r = evaluateAssertions({ text: "HELLO" }, [
      { type: "contains", value: "hello", ignore_case: true },
    ]);
    expect(r.passed).toBe(true);
  });
});

describe("evaluateAssertions — not_contains", () => {
  it("passes when the forbidden string is absent", () => {
    const r = evaluateAssertions({ text: "safe output" }, [
      { type: "not_contains", value: "--body-file" },
    ]);
    expect(r.passed).toBe(true);
  });

  it("fails when the forbidden string is present", () => {
    const r = evaluateAssertions({ text: "uses --body-file flag" }, [
      { type: "not_contains", value: "--body-file" },
    ]);
    expect(r.passed).toBe(false);
    expect(r.failures[0].type).toBe("not_contains");
  });
});

describe("evaluateAssertions — matches_regex", () => {
  it("passes when the pattern matches", () => {
    const r = evaluateAssertions({ text: "feat/3814-foo" }, [
      { type: "matches_regex", pattern: "(feat|fix|docs)/" },
    ]);
    expect(r.passed).toBe(true);
  });

  it("honors flags", () => {
    const r = evaluateAssertions({ text: "RALPH LOOP" }, [
      { type: "matches_regex", pattern: "ralph", flags: "i" },
    ]);
    expect(r.passed).toBe(true);
  });

  it("fails (not throws) on an invalid regex", () => {
    const r = evaluateAssertions({ text: "x" }, [{ type: "matches_regex", pattern: "(" }]);
    expect(r.passed).toBe(false);
    expect(r.failures[0].reason).toContain("invalid regex");
  });
});

describe("evaluateAssertions — json_path_exists", () => {
  it("finds a nested path in fenced JSON", () => {
    const text = '```json\n{ "complexity_assessment": { "computed_score": 5 } }\n```';
    const r = evaluateAssertions({ text }, [
      { type: "json_path_exists", path: "complexity_assessment.computed_score" },
    ]);
    expect(r.passed).toBe(true);
  });

  it("supports array indices", () => {
    const r = evaluateAssertions({ text: '{"focus_acs":[1,2]}' }, [
      { type: "json_path_exists", path: "focus_acs[1]" },
    ]);
    expect(r.passed).toBe(true);
  });

  it("fails when no JSON is present", () => {
    const r = evaluateAssertions({ text: "plain prose" }, [
      { type: "json_path_exists", path: "a.b" },
    ]);
    expect(r.passed).toBe(false);
    expect(r.failures[0].reason).toContain("no parseable JSON");
  });

  it("fails when the path is missing", () => {
    const r = evaluateAssertions({ text: '{"a":{"b":1}}' }, [
      { type: "json_path_exists", path: "a.c" },
    ]);
    expect(r.passed).toBe(false);
  });

  it("treats a null leaf as present", () => {
    const r = evaluateAssertions({ text: '{"a":null}' }, [{ type: "json_path_exists", path: "a" }]);
    expect(r.passed).toBe(true);
  });
});

describe("evaluateAssertions — exit_code", () => {
  it("passes when the exit code matches", () => {
    const r = evaluateAssertions({ text: "", exit_code: 0 }, [{ type: "exit_code", value: 0 }]);
    expect(r.passed).toBe(true);
  });

  it("fails when the exit code differs", () => {
    const r = evaluateAssertions({ text: "", exit_code: 1 }, [{ type: "exit_code", value: 0 }]);
    expect(r.passed).toBe(false);
  });
});

describe("evaluateAssertions — aggregation", () => {
  it("requires ALL assertions to pass and collects every failure", () => {
    const assertions: EvalAssertion[] = [
      { type: "contains", value: "yes" },
      { type: "not_contains", value: "no" },
      { type: "contains", value: "missing" },
    ];
    const r = evaluateAssertions({ text: "yes and no" }, assertions);
    expect(r.passed).toBe(false);
    // Two failures: "no" is present (not_contains) and "missing" is absent (contains).
    expect(r.failures).toHaveLength(2);
  });
});

describe("extractJson (internal)", () => {
  it("ignores braces inside string literals", () => {
    const parsed = __testing.extractJson('prefix {"a":"}{ not json"} suffix') as { a: string };
    expect(parsed.a).toBe("}{ not json");
  });

  it("returns undefined when no balanced JSON exists", () => {
    expect(__testing.extractJson("{ unbalanced")).toBeUndefined();
  });
});
