/**
 * Tests for PR detection helpers
 */

import { describe, it, expect } from "vitest";
import { parsePRFromGHCLI, hasInReviewLabel } from "../../src/utils/prDetection";

describe("prDetection", () => {
  describe("parsePRFromGHCLI", () => {
    it("parses valid gh pr list output", () => {
      const output = JSON.stringify([
        {
          number: 123,
          url: "https://github.com/org/repo/pull/123",
          title: "Fix: authentication bug",
        },
      ]);

      const result = parsePRFromGHCLI(output);

      expect(result).toEqual({
        number: 123,
        url: "https://github.com/org/repo/pull/123",
        title: "Fix: authentication bug",
      });
    });

    it("returns null for empty array", () => {
      const output = JSON.stringify([]);

      const result = parsePRFromGHCLI(output);

      expect(result).toBeNull();
    });

    it("returns first PR when multiple exist", () => {
      const output = JSON.stringify([
        {
          number: 123,
          url: "https://github.com/org/repo/pull/123",
          title: "First PR",
        },
        {
          number: 124,
          url: "https://github.com/org/repo/pull/124",
          title: "Second PR",
        },
      ]);

      const result = parsePRFromGHCLI(output);

      expect(result?.number).toBe(123);
      expect(result?.title).toBe("First PR");
    });

    it("handles malformed JSON", () => {
      const output = "not valid json";

      const result = parsePRFromGHCLI(output);

      expect(result).toBeNull();
    });

    it("handles missing required fields", () => {
      const output = JSON.stringify([
        {
          // Missing number and url
          title: "PR without required fields",
        },
      ]);

      const result = parsePRFromGHCLI(output);

      expect(result).toBeNull();
    });

    it("handles PR without title", () => {
      const output = JSON.stringify([
        {
          number: 123,
          url: "https://github.com/org/repo/pull/123",
          // title is optional
        },
      ]);

      const result = parsePRFromGHCLI(output);

      expect(result).toEqual({
        number: 123,
        url: "https://github.com/org/repo/pull/123",
        title: undefined,
      });
    });

    it("handles invalid types", () => {
      const output = JSON.stringify([
        {
          number: "123", // Should be number
          url: "https://github.com/org/repo/pull/123",
        },
      ]);

      const result = parsePRFromGHCLI(output);

      expect(result).toBeNull();
    });
  });

  describe("hasInReviewLabel", () => {
    it("detects status:in-review label", () => {
      const labels = ["type:bug", "status:in-review", "priority:high"];

      const result = hasInReviewLabel(labels);

      expect(result).toBe(true);
    });

    it("is case insensitive", () => {
      const labels = ["STATUS:IN-REVIEW"];

      const result = hasInReviewLabel(labels);

      expect(result).toBe(true);
    });

    it("returns false for other status labels", () => {
      const labels = ["status:ready", "status:in-progress"];

      const result = hasInReviewLabel(labels);

      expect(result).toBe(false);
    });

    it("returns false for empty array", () => {
      const labels: string[] = [];

      const result = hasInReviewLabel(labels);

      expect(result).toBe(false);
    });

    it("returns false when no status labels", () => {
      const labels = ["type:bug", "priority:high"];

      const result = hasInReviewLabel(labels);

      expect(result).toBe(false);
    });

    it("handles partial matches", () => {
      // Should only match exact status:in-review, not other variations
      const labels = ["in-review-mode"]; // Not a status label

      const result = hasInReviewLabel(labels);

      // This will be true because it contains 'status:in-review' substring
      // which is acceptable for this heuristic
      expect(result).toBe(false);
    });
  });
});
