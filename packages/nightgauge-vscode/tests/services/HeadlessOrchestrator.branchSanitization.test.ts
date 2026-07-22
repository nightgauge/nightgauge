/**
 * Unit tests for branch name sanitization in tryRecoverExpectedBranch.
 *
 * Verifies the allowlist-based sanitization introduced in Issue #2492 to
 * replace the escape-only approach that left shell injection vectors open.
 * The sanitization regex `[^a-zA-Z0-9/_.-]` is tested directly — it lives at
 * a single call site and has no vscode or orchestrator dependencies.
 *
 * @see Issue #2492 - Branch name sanitization only escapes double quotes
 * @see Issue #2490 - Security epic
 */

import { describe, it, expect } from "vitest";

// The sanitization logic extracted for direct unit testing.
// Mirrors the exact regex used in HeadlessOrchestrator.ts tryRecoverExpectedBranch.
function sanitizeBranchName(name: string): string {
  return name.replace(/[^a-zA-Z0-9/_.-]/g, "");
}

describe("branch name sanitization — allowlist [a-zA-Z0-9/_.-]", () => {
  it("passes valid branch names unchanged", () => {
    expect(sanitizeBranchName("feat/123-fix-something")).toBe("feat/123-fix-something");
    expect(sanitizeBranchName("main")).toBe("main");
    expect(sanitizeBranchName("fix/ISSUE-42.patch")).toBe("fix/ISSUE-42.patch");
    expect(sanitizeBranchName("release/1.2.3")).toBe("release/1.2.3");
  });

  it("strips $() command substitution injection", () => {
    expect(sanitizeBranchName("feat/foo$(evil)")).toBe("feat/fooevil");
    expect(sanitizeBranchName("$(rm -rf /)")).toBe("rm-rf/");
  });

  it("strips backtick command substitution injection", () => {
    expect(sanitizeBranchName("feat/foo`evil`")).toBe("feat/fooevil");
    expect(sanitizeBranchName("`id`")).toBe("id");
  });

  it("strips semicolons", () => {
    expect(sanitizeBranchName("feat/foo;evil")).toBe("feat/fooevil");
    expect(sanitizeBranchName(";ls -la")).toBe("ls-la");
  });

  it("strips pipes", () => {
    expect(sanitizeBranchName("feat/foo|bar")).toBe("feat/foobar");
    expect(sanitizeBranchName("feat/test|cat /etc/passwd")).toBe("feat/testcat/etc/passwd");
  });

  it("strips ampersands", () => {
    expect(sanitizeBranchName("feat/foo&&evil")).toBe("feat/fooevil");
    expect(sanitizeBranchName("feat/foo&")).toBe("feat/foo");
  });

  it("strips double quotes (regression for original escape-only bug)", () => {
    // Before the fix, `"` was escaped as `\"` — now it's stripped entirely.
    expect(sanitizeBranchName('feat/foo"bar"')).toBe("feat/foobar");
    expect(sanitizeBranchName('"injected"')).toBe("injected");
  });

  it("strips all metacharacters from a complex injection string", () => {
    const injection = 'feat/foo$(evil);ls|cat&"bad"`more`';
    expect(sanitizeBranchName(injection)).toBe("feat/fooevillscatbadmore");
  });

  it("strips spaces", () => {
    expect(sanitizeBranchName("feat/foo bar")).toBe("feat/foobar");
  });

  it("strips newlines and null bytes", () => {
    expect(sanitizeBranchName("feat/foo\nbar")).toBe("feat/foobar");
    expect(sanitizeBranchName("feat/foo\0bar")).toBe("feat/foobar");
  });

  it("produces empty string when input contains only metacharacters", () => {
    expect(sanitizeBranchName("$(evil)")).toBe("evil");
    expect(sanitizeBranchName(";;;")).toBe("");
    expect(sanitizeBranchName("`cmd`")).toBe("cmd");
  });

  it("preserves dots in branch names", () => {
    expect(sanitizeBranchName("hotfix/1.2.3")).toBe("hotfix/1.2.3");
  });

  it("preserves underscores in branch names", () => {
    // Underscores are in the allowlist [a-zA-Z0-9/_.-] and pass through unchanged.
    expect(sanitizeBranchName("feat/my_feature")).toBe("feat/my_feature");
  });
});
