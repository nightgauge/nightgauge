/**
 * Tests for the cross-model skill eval assertion engine (Issue #3814).
 * Mock-mode only — no live model calls.
 */

import { describe, it, expect } from "vitest";
import { evaluateAssertions, __testing } from "../../src/eval/assertions.js";
import { EvalAssertionSchema, type EvalAssertion } from "../../src/eval/schemas.js";

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

describe("evaluateAssertions — not_matches_regex", () => {
  it("passes when the pattern does not match", () => {
    const r = evaluateAssertions({ text: "gh pr merge --squash" }, [
      { type: "not_matches_regex", pattern: "gh pr merge[^\\n]*--admin" },
    ]);
    expect(r.passed).toBe(true);
  });

  it("fails when the pattern matches and records the matched text", () => {
    const r = evaluateAssertions({ text: "run gh pr merge 12 --squash --admin now" }, [
      { type: "not_matches_regex", pattern: "gh pr merge[^\\n]*--admin" },
    ]);
    expect(r.passed).toBe(false);
    expect(r.failures[0].type).toBe("not_matches_regex");
    expect(r.failures[0].reason).toContain("gh pr merge 12 --squash --admin");
  });

  it("honors flags", () => {
    const text = "SKIP THE TEST";
    expect(
      evaluateAssertions({ text }, [{ type: "not_matches_regex", pattern: "skip the test" }]).passed
    ).toBe(true);
    expect(
      evaluateAssertions({ text }, [
        { type: "not_matches_regex", pattern: "skip the test", flags: "i" },
      ]).passed
    ).toBe(false);
  });

  it("treats an invalid regex as a failure rather than throwing", () => {
    const r = evaluateAssertions({ text: "anything" }, [
      { type: "not_matches_regex", pattern: "(" },
    ]);
    expect(r.passed).toBe(false);
    expect(r.failures[0].reason).toMatch(/invalid regex/);
  });

  it("is repeatable with the g flag (no lastIndex carry-over)", () => {
    const a: EvalAssertion[] = [{ type: "not_matches_regex", pattern: "x", flags: "g" }];
    expect(evaluateAssertions({ text: "x" }, a).passed).toBe(false);
    expect(evaluateAssertions({ text: "x" }, a).passed).toBe(false);
  });
});

const F = "```";

describe("scope: last_fenced_block", () => {
  const text = [
    "I never pass `--admin`.",
    `${F}bash`,
    "gh pr merge 1 --admin",
    F,
    "Actually:",
    `${F}bash`,
    "gh pr merge 1 --squash  # never --admin",
    F,
    `${F}json`,
    '{"action": "file_spike", "n": 0, "ok": false}',
    F,
  ].join("\n");
  const bashScope = { scope: "last_fenced_block", lang: "bash" } as const;

  it("reads only the last block of the requested language", () => {
    const pass = evaluateAssertions({ text }, [
      { type: "matches_regex", pattern: "--squash", ...bashScope },
    ]);
    expect(pass.passed).toBe(true);
    // The earlier bash block's --admin is not the answer; only the comment names it.
    const fail = evaluateAssertions({ text }, [
      { type: "not_matches_regex", pattern: "--admin", ...bashScope },
    ]);
    expect(fail.passed).toBe(false);
    const stripped = evaluateAssertions({ text }, [
      { type: "not_matches_regex", pattern: "--admin", ...bashScope, strip_comments: true },
    ]);
    expect(stripped.passed).toBe(true);
  });

  it("without lang reads the last block of any language", () => {
    const r = evaluateAssertions({ text }, [
      { type: "contains", value: "file_spike", scope: "last_fenced_block" },
    ]);
    expect(r.passed).toBe(true);
  });

  it("prose outside the block is invisible to a scoped negative assertion", () => {
    const r = evaluateAssertions(
      { text: `never --body-file\n\n${F}bash\npr create --body x\n${F}` },
      [{ type: "not_contains", value: "--body-file", scope: "last_fenced_block" }]
    );
    expect(r.passed).toBe(true);
  });

  it("fails closed when there is no block, for negative assertions too", () => {
    for (const a of [
      { type: "not_matches_regex", pattern: "--admin", ...bashScope },
      { type: "not_contains", value: "--admin", ...bashScope },
      { type: "matches_regex", pattern: ".", ...bashScope },
      { type: "json_path_exists", path: "a", scope: "last_fenced_block", lang: "json" },
    ] as EvalAssertion[]) {
      const r = evaluateAssertions({ text: "gh pr merge --squash" }, [a]);
      expect(r.passed, a.type).toBe(false);
      expect(r.failures[0].reason).toMatch(/^no last ```bash block|^no last ```json block/);
    }
  });

  it("fails closed on a block of the wrong language or an unclosed fence", () => {
    const a: EvalAssertion = { type: "not_matches_regex", pattern: "--admin", ...bashScope };
    expect(evaluateAssertions({ text: `${F}text\ngh pr merge\n${F}` }, [a]).passed).toBe(false);
    expect(evaluateAssertions({ text: `${F}bash\ngh pr merge --squash` }, [a]).passed).toBe(false);
  });

  it("accepts a list of languages, case-insensitively", () => {
    const r = evaluateAssertions({ text: `${F}SH\ngh pr merge --squash\n${F}` }, [
      {
        type: "matches_regex",
        pattern: "squash",
        scope: "last_fenced_block",
        lang: ["bash", "sh"],
      },
    ]);
    expect(r.passed).toBe(true);
  });
});

describe("fencedBlocks / stripShellComments (internal)", () => {
  it("handles ~~~ fences, longer closers, and ignores shorter or unclosed fences", () => {
    const blocks = __testing.fencedBlocks(
      ["~~~sh", "a", "~~~", "````bash title", "b", "```", "c", "`````", "```json", "{}"].join("\n")
    );
    expect(blocks).toEqual([
      { lang: "sh", body: "a" },
      { lang: "bash", body: "b\n```\nc" },
    ]);
  });

  it("strips word-start # comments outside quotes only", () => {
    expect(
      __testing.stripShellComments(
        'gh pr merge --squash # no --admin\n# --admin\necho "#1 --x" ${#v} a#b\n--body "line\n# heading"'
      )
    ).toBe('gh pr merge --squash \n\necho "#1 --x" ${#v} a#b\n--body "line\n# heading"');
  });
});

describe("evaluateAssertions — json_path_equals", () => {
  const block = (body: string) => `prose {"action":"x"}\n${F}json\n${body}\n${F}`;
  const scope = { scope: "last_fenced_block", lang: "json" } as const;

  it("compares the scoped block's value strictly", () => {
    const text = block('{"action": "file_spike", "flag": false, "n": [1]}');
    expect(
      evaluateAssertions({ text }, [
        { type: "json_path_equals", path: "action", value: "file_spike", ...scope },
        { type: "json_path_equals", path: "flag", value: false, ...scope },
      ]).passed
    ).toBe(true);
    const r = evaluateAssertions({ text }, [
      { type: "json_path_equals", path: "flag", value: "false", ...scope },
      { type: "json_path_equals", path: "missing", value: null, ...scope },
      { type: "json_path_equals", path: "n", value: 1, ...scope },
    ]);
    expect(r.failures).toHaveLength(3);
  });

  it("unscoped reads the first JSON in the output", () => {
    const r = evaluateAssertions({ text: block('{"action": "y"}') }, [
      { type: "json_path_equals", path: "action", value: "x" },
    ]);
    expect(r.passed).toBe(true);
  });

  it("fails an unparseable scoped block instead of scanning it", () => {
    const r = evaluateAssertions({ text: block('{"action": "file_spike",}') }, [
      { type: "json_path_equals", path: "action", value: "file_spike", ...scope },
    ]);
    expect(r.passed).toBe(false);
    expect(r.failures[0].reason).toMatch(/not valid JSON/);
  });
});

describe("EvalAssertionSchema", () => {
  it("rejects the sticky y flag", () => {
    expect(
      EvalAssertionSchema.safeParse({ type: "matches_regex", pattern: "a", flags: "y" }).success
    ).toBe(false);
    expect(
      EvalAssertionSchema.safeParse({ type: "not_matches_regex", pattern: "a", flags: "im" })
        .success
    ).toBe(true);
  });

  it("rejects lang or strip_comments without a scope", () => {
    expect(
      EvalAssertionSchema.safeParse({ type: "matches_regex", pattern: "a", lang: "bash" }).success
    ).toBe(false);
    expect(
      EvalAssertionSchema.safeParse({ type: "not_contains", value: "a", strip_comments: true })
        .success
    ).toBe(false);
    expect(
      EvalAssertionSchema.safeParse({
        type: "not_contains",
        value: "a",
        scope: "last_fenced_block",
        lang: "bash",
        strip_comments: true,
      }).success
    ).toBe(true);
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
