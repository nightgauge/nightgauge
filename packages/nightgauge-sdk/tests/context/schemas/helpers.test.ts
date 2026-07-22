import { describe, it, expect } from "vitest";
import { flexEnum } from "../../../src/context/schemas/helpers.js";

describe("flexEnum", () => {
  const schema = flexEnum(["passed", "failed", "skipped"] as const);

  describe("exact values", () => {
    it("accepts canonical enum values", () => {
      expect(schema.parse("passed")).toBe("passed");
      expect(schema.parse("failed")).toBe("failed");
      expect(schema.parse("skipped")).toBe("skipped");
    });
  });

  describe("hyphen-to-underscore normalization", () => {
    const withUnderscore = flexEnum(["passed", "failed", "not_run"] as const);

    it("normalizes hyphens to underscores", () => {
      expect(withUnderscore.parse("not-run")).toBe("not_run");
    });

    it("accepts the canonical underscore form", () => {
      expect(withUnderscore.parse("not_run")).toBe("not_run");
    });
  });

  describe("agent truncation aliases", () => {
    it('normalizes "pass" to "passed"', () => {
      expect(schema.parse("pass")).toBe("passed");
    });

    it('normalizes "fail" to "failed"', () => {
      expect(schema.parse("fail")).toBe("failed");
    });

    it('normalizes "skip" to "skipped"', () => {
      expect(schema.parse("skip")).toBe("skipped");
    });
  });

  describe("alias only applies when target is in enum", () => {
    const noSkipped = flexEnum(["passed", "failed", "not_run"] as const);

    it('does not alias "skip" when "skipped" is not in the enum', () => {
      expect(() => noSkipped.parse("skip")).toThrow();
    });
  });

  describe("invalid values", () => {
    it("rejects unknown values", () => {
      expect(() => schema.parse("unknown")).toThrow();
    });

    it("rejects non-string values", () => {
      expect(() => schema.parse(123)).toThrow();
    });
  });
});
