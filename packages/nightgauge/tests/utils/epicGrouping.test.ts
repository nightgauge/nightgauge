import { describe, it, expect } from "vitest";
import { parseEpicReference } from "../../src/services/ProjectBoardService";

describe("parseEpicReference", () => {
  describe("hash pattern", () => {
    it('should parse "Part of #123" pattern', () => {
      const body = "This issue is Part of #123";
      expect(parseEpicReference(body)).toBe(123);
    });

    it('should parse "part of #456" pattern (lowercase)', () => {
      const body = "This issue is part of #456";
      expect(parseEpicReference(body)).toBe(456);
    });

    it('should parse "PART OF #789" pattern (uppercase)', () => {
      const body = "This issue is PART OF #789";
      expect(parseEpicReference(body)).toBe(789);
    });

    it('should handle "Part of" at start of body', () => {
      const body = "Part of #100\n\nDetails here...";
      expect(parseEpicReference(body)).toBe(100);
    });

    it('should handle "Part of" at end of body', () => {
      const body = "Some description\n\nPart of #200";
      expect(parseEpicReference(body)).toBe(200);
    });

    it("should handle extra whitespace", () => {
      const body = "Part   of   #300";
      expect(parseEpicReference(body)).toBe(300);
    });

    it("should return first match when multiple patterns exist", () => {
      const body = "Part of #100\n\nAlso Part of #200";
      expect(parseEpicReference(body)).toBe(100);
    });
  });

  describe("URL pattern", () => {
    it("should parse full GitHub URL", () => {
      const body = "Part of https://github.com/nightgauge/nightgauge/issues/123";
      expect(parseEpicReference(body)).toBe(123);
    });

    it("should parse GitHub URL with organization and repo", () => {
      const body = "Part of https://github.com/my-org/my-repo/issues/456";
      expect(parseEpicReference(body)).toBe(456);
    });

    it("should prefer hash pattern over URL pattern", () => {
      const body = "Part of #100\n\nAlso part of https://github.com/org/repo/issues/200";
      expect(parseEpicReference(body)).toBe(100);
    });
  });

  describe('words between "of" and "#" (regression: issue #414-418)', () => {
    it('should parse "Part of Epic #287" pattern', () => {
      const body = "Part of Epic #287";
      expect(parseEpicReference(body)).toBe(287);
    });

    it('should parse "Part of epic #287" pattern (lowercase)', () => {
      const body = "Part of epic #287";
      expect(parseEpicReference(body)).toBe(287);
    });

    it('should parse "Part of Issue #100" pattern', () => {
      const body = "Part of Issue #100";
      expect(parseEpicReference(body)).toBe(100);
    });

    it('should parse "Part of Epic #287" in Related Issues section', () => {
      const body = `## Related Issues

Part of Epic #287 (Pipeline Reliability & Control)

## Technical Notes

Some implementation details.`;
      expect(parseEpicReference(body)).toBe(287);
    });

    it('should parse "Part of Epic #317" in real issue body', () => {
      const body = `## Summary

Fix documentation inconsistencies across skills.

## Related Issues

Part of #317 (Documentation Overhaul)`;
      expect(parseEpicReference(body)).toBe(317);
    });
  });

  describe("edge cases", () => {
    it("should return undefined for undefined body", () => {
      expect(parseEpicReference(undefined)).toBeUndefined();
    });

    it("should return undefined for empty body", () => {
      expect(parseEpicReference("")).toBeUndefined();
    });

    it("should return undefined for body without pattern", () => {
      const body = "This is just a regular issue description";
      expect(parseEpicReference(body)).toBeUndefined();
    });

    it("should not match incomplete patterns", () => {
      const body = "Part of the problem";
      expect(parseEpicReference(body)).toBeUndefined();
    });

    it('should not match "Part of" without issue number', () => {
      const body = "Part of some other issue";
      expect(parseEpicReference(body)).toBeUndefined();
    });

    it("should not match standalone issue numbers", () => {
      const body = "Related to #123";
      expect(parseEpicReference(body)).toBeUndefined();
    });

    it('should not match "Part of" with invalid URL', () => {
      const body = "Part of https://example.com/issues/123";
      expect(parseEpicReference(body)).toBeUndefined();
    });

    it('should not match "Part of" followed by a sentence without #', () => {
      const body = "Part of the larger authentication epic";
      expect(parseEpicReference(body)).toBeUndefined();
    });
  });

  describe("real-world examples", () => {
    it("should parse typical issue body with acceptance criteria", () => {
      const body = `## Description

Implement the login form component.

Part of #50

## Acceptance Criteria

- [ ] Form validates email format
- [ ] Password field has show/hide toggle
- [ ] Submit button disabled until form is valid`;

      expect(parseEpicReference(body)).toBe(50);
    });

    it('should parse issue body with "Part of" in metadata section', () => {
      const body = `## Summary

Add user profile settings.

## Details

This implements the profile page for the user dashboard.

---

**Epic:** Part of #75
**Size:** M`;

      expect(parseEpicReference(body)).toBe(75);
    });

    it("should parse issue body with GitHub link in body", () => {
      const body = `## Task

Update the authentication flow.

See epic: Part of https://github.com/nightgauge/nightgauge/issues/42

## Implementation Notes

Follow existing patterns.`;

      expect(parseEpicReference(body)).toBe(42);
    });
  });
});
