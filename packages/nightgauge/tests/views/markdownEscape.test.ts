import { describe, it, expect } from "vitest";
import {
  escapeMarkdown,
  codeSpan,
  branchMarkdown,
  safeHttpsHref,
} from "../../src/views/markdownEscape";
import { UNDETERMINED_BRANCH_LABEL } from "../../src/views/dashboard/DashboardComponents";

describe("escapeMarkdown (#3369)", () => {
  it("backslash-escapes link, image, HTML and emphasis syntax", () => {
    expect(escapeMarkdown("[x](command:foo)")).toBe("\\[x\\](command:foo)");
    expect(escapeMarkdown("<img src=x>")).toBe("\\<img src=x\\>");
    expect(escapeMarkdown("![a](b)")).toBe("\\!\\[a\\](b)");
    expect(escapeMarkdown("*a* _b_ ~c~ `d` |e| &amp;")).toBe(
      "\\*a\\* \\_b\\_ \\~c\\~ \\`d\\` \\|e\\| \\&amp;"
    );
    expect(escapeMarkdown("a\\b")).toBe("a\\\\b");
  });

  it("replaces control and format characters with spaces", () => {
    expect(escapeMarkdown("a\nb\r\tc")).toBe("a b  c");
    expect(escapeMarkdown("x‮y​z")).toBe("x y z");
  });

  it("leaves plain text and the empty string unchanged", () => {
    expect(escapeMarkdown("Add feature 42")).toBe("Add feature 42");
    expect(escapeMarkdown("")).toBe("");
  });
});

describe("codeSpan (#3369)", () => {
  it("wraps plain text in a single-backtick fence", () => {
    expect(codeSpan("feat/1-x")).toBe("`feat/1-x`");
  });

  it("uses a fence longer than any inner backtick run", () => {
    expect(codeSpan("a``b")).toBe("```a``b```");
  });

  it("pads content that starts or ends with a backtick or space", () => {
    expect(codeSpan("`x")).toBe("`` `x ``");
    expect(codeSpan("x ")).toBe("` x  `");
  });

  it("replaces control characters and renders empty as empty", () => {
    expect(codeSpan("a\nb")).toBe("`a b`");
    expect(codeSpan("")).toBe("");
  });
});

describe("codeSpan — Go attention.CodeSpan parity", () => {
  it("matches the Go table's edge cases", () => {
    expect(codeSpan("a`")).toBe("`` a` ``");
    expect(codeSpan(" a")).toBe("`  a `");
    expect(codeSpan("a``b`c")).toBe("```a``b`c```");
    expect(codeSpan("a‮b")).toBe("`a b`");
    expect(codeSpan("/var/cd_ef/*x*\\[y]")).toBe("`/var/cd_ef/*x*\\[y]`");
  });
});

describe("branchMarkdown", () => {
  it("code-spans a real branch and labels an undetermined one", () => {
    expect(branchMarkdown("feat/1-x")).toBe("`feat/1-x`");
    expect(branchMarkdown("")).toBe(UNDETERMINED_BRANCH_LABEL);
    expect(branchMarkdown(undefined)).toBe(UNDETERMINED_BRANCH_LABEL);
  });
});

describe("safeHttpsHref", () => {
  it("keeps https URLs", () => {
    expect(safeHttpsHref("https://github.com/o/r/issues/1")).toBe(
      "https://github.com/o/r/issues/1"
    );
  });

  it("rejects non-https, unparseable and empty URLs", () => {
    for (const u of [
      "command:workbench.action.terminal.new",
      "javascript:alert(1)",
      "http://example.com",
      "not a url",
      "",
      undefined,
    ]) {
      expect(safeHttpsHref(u)).toBeUndefined();
    }
  });

  it("percent-encodes parentheses so the link destination cannot close early", () => {
    const href = safeHttpsHref("https://x.test/a)[b](command:foo)");
    expect(href).toBeDefined();
    expect(href).not.toMatch(/[()]/);
  });
});
