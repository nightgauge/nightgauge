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
});
