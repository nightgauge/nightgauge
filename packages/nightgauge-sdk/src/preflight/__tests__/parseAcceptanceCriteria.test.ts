import { readFileSync } from "node:fs";
import { join } from "node:path";
import { describe, expect, it } from "vitest";
import { parseAcceptanceCriteria } from "../parseAcceptanceCriteria.js";

describe("parseAcceptanceCriteria", () => {
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
      "## Acceptance Criteria\n1. first\n   ```sh\n   # comment\n   1. output\n   ```\n    1. sub-step\n<details>\n- [ ] outer\n<details>\n- [ ] inner\n</details>\n- [ ] after inner\n</details>\n2. second\n- [ ]  first\n";
    expect(parseAcceptanceCriteria(body).map((a) => [a.index, a.text])).toEqual([
      [0, "first"],
      [1, "second"],
    ]);
  });
});
