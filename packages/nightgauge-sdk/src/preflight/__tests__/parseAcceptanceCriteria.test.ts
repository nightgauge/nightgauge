import { readFileSync } from "node:fs";
import { join } from "node:path";
import { describe, expect, it } from "vitest";
import { parseAcceptanceCriteria } from "../parseAcceptanceCriteria.js";

describe("parseAcceptanceCriteria", () => {
  it("keeps an over-long criterion, capped at 4096 code points like Go (#2906)", () => {
    const long = "é".repeat(4106);
    const got = parseAcceptanceCriteria(
      `## Verification\n\n- ${long}\n- [ ] ${"ü".repeat(4106)}\n`
    );
    expect(got).toHaveLength(2);
    for (const c of got) expect(Array.from(c.text)).toHaveLength(4096);
  });

  it("returns [] for empty input", () => {
    expect(parseAcceptanceCriteria("")).toEqual([]);
  });

  it("returns [] for non-string input", () => {
    expect(parseAcceptanceCriteria(undefined as unknown as string)).toEqual([]);
  });

  it("returns [] for body with no checkboxes", () => {
    expect(parseAcceptanceCriteria("Just a paragraph.\nNo lists here.")).toEqual([]);
  });

  it("extracts a single checkbox", () => {
    const acs = parseAcceptanceCriteria("- [ ] Add a new feature");
    expect(acs).toEqual([{ index: 0, text: "Add a new feature", checkbox_state: "unchecked" }]);
  });

  it("extracts multiple checkboxes preserving order and state", () => {
    const body = `## Acceptance Criteria

- [ ] First requirement
- [x] Second requirement done
- [X] Third also done
- [ ] Fourth pending`;
    const acs = parseAcceptanceCriteria(body);
    expect(acs).toHaveLength(4);
    expect(acs.map((a) => a.text)).toEqual([
      "First requirement",
      "Second requirement done",
      "Third also done",
      "Fourth pending",
    ]);
    expect(acs.map((a) => a.checkbox_state)).toEqual([
      "unchecked",
      "checked",
      "checked",
      "unchecked",
    ]);
    expect(acs.map((a) => a.index)).toEqual([0, 1, 2, 3]);
  });

  it("accepts both `-` and `*` bullet markers", () => {
    const acs = parseAcceptanceCriteria("- [ ] dash\n* [x] star");
    expect(acs.map((a) => a.text)).toEqual(["dash", "star"]);
  });

  it("accepts indented (nested) checkboxes", () => {
    const body = "- [ ] Top\n  - [x] Nested";
    const acs = parseAcceptanceCriteria(body);
    expect(acs).toHaveLength(2);
    expect(acs[1]).toMatchObject({ text: "Nested", checkbox_state: "checked" });
  });

  it("ignores entries with empty text", () => {
    const body = "- [ ]    \n- [ ] real one";
    const acs = parseAcceptanceCriteria(body);
    expect(acs).toHaveLength(1);
    expect(acs[0].text).toBe("real one");
  });

  it("trims surrounding whitespace from text", () => {
    const acs = parseAcceptanceCriteria("- [ ]   has spaces   ");
    expect(acs[0].text).toBe("has spaces");
  });

  it("extracts numbered items inside an Acceptance Criteria section", () => {
    const body = "## Acceptance Criteria\n1. dot\n2) paren\n3. **AC3:** bold\n";
    expect(parseAcceptanceCriteria(body).map((a) => a.text)).toEqual([
      "dot",
      "paren",
      "**AC3:** bold",
    ]);
  });

  it("keeps checkbox and numbered items in document order", () => {
    const body = "## Acceptance Criteria\n- [ ] box\n1. num\n* [x] star\n";
    const acs = parseAcceptanceCriteria(body);
    expect(acs.map((a) => [a.index, a.text, a.checkbox_state])).toEqual([
      [0, "box", "unchecked"],
      [1, "num", "unchecked"],
      [2, "star", "checked"],
    ]);
  });

  it("ignores numbered items outside an Acceptance Criteria section", () => {
    const body = "## Steps\n1. run\n## Acceptance Criteria\n1. in\n## Notes\n1. out\n";
    expect(parseAcceptanceCriteria(body).map((a) => a.text)).toEqual(["in"]);
  });

  it("does not let an inline `<details>` mention swallow later criteria", () => {
    const body =
      "- [ ] keeps the `<details>` exclusion\n- [ ] later\n\n<details>\n- [ ] archived\n</details>\n";
    expect(parseAcceptanceCriteria(body).map((a) => a.text)).toEqual([
      "keeps the `<details>` exclusion",
      "later",
    ]);
  });

  it.each([
    [
      "details line inside backtick fence",
      "## Acceptance Criteria\n- [ ] first\n```html\n<details>\n```\n- [ ] after fence\n</details>\n- [ ] last\n",
      ["first", "after fence", "last"],
    ],
    ["details line inside tilde fence", "~~~\n<details>\n~~~\n- [ ] kept\n</details>\n", ["kept"]],
    [
      "archive after fence still dropped",
      "```\nx\n```\n<details>\n- [ ] archived\n</details>\n- [ ] live\n",
      ["live"],
    ],
    [
      "longer backtick fence holds a shorter one (#2727)",
      "## Acceptance Criteria\n- [ ] one\n````md\n```\n- [ ] phantom\n````\n- [ ] two\n- [ ] three\n",
      ["one", "two", "three"],
    ],
    [
      "tilde fence holds a backtick line (#2727)",
      "## Acceptance Criteria\n- [ ] one\n~~~\n```\n- [ ] phantom\n~~~\n- [ ] two\n- [ ] three\n",
      ["one", "two", "three"],
    ],
    [
      "closer with trailing text does not close (#2727)",
      "## Acceptance Criteria\n```\n```x\n- [ ] phantom\n```\n- [ ] real\n",
      ["real"],
    ],
    [
      "longer closer closes (#2727)",
      "## Acceptance Criteria\n```\n- [ ] phantom\n````\n- [ ] real\n",
      ["real"],
    ],
    [
      "archive after nested-fence block still dropped (#2727)",
      "````\n```\n````\n<details>\n- [ ] archived\n</details>\n- [ ] live\n",
      ["live"],
    ],
    ["unterminated details kept", "<details>\n- [ ] kept\n", ["kept"]],
  ])("mirrors Go fence-aware <details> handling: %s", (_name, body, want) => {
    expect(parseAcceptanceCriteria(body).map((a) => a.text)).toEqual(want);
  });

  it("parses #2672's body into six whole criteria (#2679)", () => {
    const body = readFileSync(join(__dirname, "fixtures", "issue-2672-body.md"), "utf8");
    const got = parseAcceptanceCriteria(body);
    expect(got).toHaveLength(6);
    expect(got[1].text.endsWith("including the `<details>` exclusion and de-duplication.")).toBe(
      true
    );
  });

  it("skips fenced code blocks inside an Acceptance Criteria section", () => {
    const body =
      "## Acceptance Criteria\n1. first:\n   ```sh\n   # run it\n   1. output line\n   - [ ] example box\n   ```\n2. second\n~~~\n1. phantom\n~~~\n";
    expect(parseAcceptanceCriteria(body).map((a) => a.text)).toEqual(["first:", "second"]);
  });

  it("does not count a nested numbered sub-step as a criterion", () => {
    const body = "## Acceptance Criteria\n1. Do X:\n    1. sub-step\n2. Do Y\n";
    expect(parseAcceptanceCriteria(body).map((a) => a.text)).toEqual(["Do X:", "Do Y"]);
  });

  // Same cases as TestParseACs_Forms: the sub-step sits at the parent's content column (#2684).
  it.each([
    [
      "3-space sub-step at the content column",
      "## Acceptance Criteria\n1. Do X:\n   1. sub\n   2. sub two\n2. Do Y\n",
      ["Do X:", "Do Y"],
    ],
    ["sub-step after a blank line", "## Acceptance Criteria\n1. Do X:\n\n   1. sub\n", ["Do X:"]],
    [
      "whitespace-only item opens no column",
      "## Acceptance Criteria\n1.  \n   1. real\n",
      ["real"],
    ],
    ["sibling items", "## Acceptance Criteria\n1. a\n2. b\n", ["a", "b"]],
    [
      "4-column content keeps a 3-space item a sibling",
      "## Acceptance Criteria\n10. x\n   1. y\n",
      ["x", "y"],
    ],
    [
      "2-space item below the column is a sibling",
      "## Acceptance Criteria\n1. a\n  2. b\n",
      ["a", "b"],
    ],
    ["first item at 3 spaces counts", "## Acceptance Criteria\n   1. lone\n", ["lone"]],
    [
      "unindented line ends the item",
      "## Acceptance Criteria\n1. a\nNotes:\n   1. b\n",
      ["a", "b"],
    ],
    ["heading ends the item", "## Acceptance Criteria\n1. a\n### More\n   1. b\n", ["a", "b"]],
  ])("numbered sub-steps: %s", (_name, body, want) => {
    expect(parseAcceptanceCriteria(body).map((a) => a.text)).toEqual(want);
  });

  it("drops repeated criteria", () => {
    const acs = parseAcceptanceCriteria("- [ ] same  text\n- [x] same text\n");
    expect(acs).toHaveLength(1);
  });

  it("skips <details> archives, nested or unterminated, and repeated criteria (#2655)", () => {
    const body = [
      "- [ ] One",
      "- [x] Two",
      "<details><summary>Original</summary>",
      "- [ ] Archived",
      "<details open>",
      "- [ ] Inner",
      "</details>",
      "- [ ] After inner",
      "</DETAILS>",
      "- [ ]  One",
      "- [ ] Three",
    ].join("\n");
    const acs = parseAcceptanceCriteria(body);
    expect(acs.map((a) => [a.index, a.text])).toEqual([
      [0, "One"],
      [1, "Two"],
      [2, "Three"],
    ]);
  });

  it("matches Go's TestParseACs_ParityFixture", () => {
    const body =
      "## Acceptance Criteria\n1. first\n   ```sh\n   # comment\n   1. output\n   ```\n    1. sub-step\n<details>\n- [ ] outer\n<details>\n- [ ] inner\n</details>\n- [ ] after inner\n</details>\n2. second\n- [ ]  first\n## Verification\n- third that\n  wraps\n## Acceptance Criteria\n- [ ] box covers both cases:\n  - case a\n  - case b\n- [ ] last\n";
    expect(parseAcceptanceCriteria(body).map((a) => [a.index, a.text])).toEqual([
      [0, "first"],
      [1, "second"],
      [2, "third that wraps"],
      [3, "box covers both cases: case a case b"],
      [4, "last"],
    ]);
  });

  // #2906: mirrors the Go TestParseACs_Forms cases for wider section names,
  // plain bullets and wrapped continuation lines.
  it.each([
    [
      "checkbox sub-bullets 2 spaces join (#2972)",
      "## Acceptance Criteria\n- [ ] one\n- [ ] two\n- [ ] `scripts/test-check-md-links.sh` covers both cases:\n  - case a\n  - case b\n- [ ] four\n",
      ["one", "two", "`scripts/test-check-md-links.sh` covers both cases: case a case b", "four"],
    ],
    [
      "checkbox sub-bullets 4 spaces join (#2972)",
      "## Acceptance Criteria\n- [ ] one\n- [ ] two\n- [ ] `scripts/test-check-md-links.sh` covers both cases:\n    - case a\n    - case b\n- [ ] four\n",
      ["one", "two", "`scripts/test-check-md-links.sh` covers both cases: case a case b", "four"],
    ],
    [
      "checkbox numbered sub-line joins",
      "## Acceptance Criteria\n- [ ] parent\n  1. step\n2. next\n",
      ["parent step", "next"],
    ],
    [
      "checkbox sub-bullet after blank joins",
      "## Acceptance Criteria\n- [ ] parent\n\n  - more\n",
      ["parent more"],
    ],
    [
      "checkbox sub-bullet outside section joins",
      "## Notes\n- [ ] parent\n  - more\n- plain\n",
      ["parent more"],
    ],
    [
      "indented checkbox stays own criterion",
      "## Acceptance Criteria\n- [ ] parent\n  - [ ] child\n    - deep\n",
      ["parent", "child deep"],
    ],
  ])("checkbox sub-bullets: %s", (_name, body, want) => {
    expect(parseAcceptanceCriteria(body).map((a) => a.text)).toEqual(want);
  });

  it.each([
    ["verification numbered", "## Verification\n\n1. one\n2. two\n", ["one", "two"]],
    [
      "done when bulleted",
      "## Done when\n- first\n* second\n+ third\n",
      ["first", "second", "third"],
    ],
    ["acceptance colon numbered", "### Acceptance:\n1. x\n2) y\n", ["x", "y"]],
    ["definition of done", "## Definition of Done\n- shipped\n", ["shipped"]],
    ["bullets in acceptance criteria", "## Acceptance Criteria\n- a\n  - sub\n- b\n", ["a", "b"]],
    ["post-merge verification notes", "## Post-merge verification notes\n1. no\n- no\n", []],
    ["bullets outside section", "## Summary\n- not\n- criteria\n", []],
    [
      "wrapped line joins",
      "## Verification\n1. starts here and\n   ends here.\n2. next\n",
      ["starts here and ends here.", "next"],
    ],
    ["paragraph after blank line", "## Verification\n1. a\n\n   later para\n2. b\n", ["a", "b"]],
    [
      "sub-step continuation",
      "## Acceptance Criteria\n1. Do X:\n   1. sub that\n      wraps\n2. Do Y\n",
      ["Do X:", "Do Y"],
    ],
    ["deep marker", "## Acceptance Criteria\n1. Do X:\n    1. sub\n2. Do Y\n", ["Do X:", "Do Y"]],
    ["unindented line", "## Verification\n1. a\nNotes here\n", ["a"]],
    ["empty checkbox is not a bullet", "## Acceptance Criteria\n- [x] done\n- [ ]\n", ["done"]],
  ])("%s (#2906)", (_name, body, want) => {
    expect(parseAcceptanceCriteria(body).map((a) => a.text)).toEqual(want);
  });

  it("parses the G1 fixture's Verification section into three criteria (#2906)", () => {
    const body = readFileSync(
      join(__dirname, "..", "..", "..", "..", "..", "tests", "clean-install", "issue.md"),
      "utf8"
    );
    const got = parseAcceptanceCriteria(body);
    expect(got).toHaveLength(3);
    expect(got[0].text.endsWith("the `TypeError` and the `RangeError`.")).toBe(true);
    expect(got[1].text.startsWith("`npm test` passes")).toBe(true);
    expect(got[2].text.startsWith("`README.md` lists")).toBe(true);
  });

  it("stays linear on long whitespace runs (CodeQL js/polynomial-redos)", () => {
    const run = "\t".repeat(50_000);
    const body = [
      `## Verification${run}:${run}`,
      `* [ ]${run}x${run}`,
      `1.${run}y${run}`,
      `-${run}z${run}`,
      `- [ ]${run}`,
    ].join("\n");
    const start = Date.now();
    const got = parseAcceptanceCriteria(body).map((a) => a.text);
    expect(Date.now() - start).toBeLessThan(1000);
    expect(got).toEqual(["x", "y", "z"]);
  });
});
