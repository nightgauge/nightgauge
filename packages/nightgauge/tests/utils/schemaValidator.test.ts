import { describe, it, expect } from "vitest";
import { SchemaValidator } from "../../src/utils/schemaValidator";

describe("SchemaValidator", () => {
  describe("validateEnum", () => {
    const validModels = ["sonnet", "opus", "haiku"] as const;

    it("returns value when valid", () => {
      expect(SchemaValidator.validateEnum("sonnet", validModels)).toBe("sonnet");
    });

    it("returns null for invalid value", () => {
      expect(SchemaValidator.validateEnum("invalid", validModels)).toBeNull();
    });

    it("returns null for empty string", () => {
      expect(SchemaValidator.validateEnum("", validModels)).toBeNull();
    });
  });

  describe("coerceNumber", () => {
    it("parses valid integer", () => {
      expect(SchemaValidator.coerceNumber("42", { integer: true })).toBe(42);
    });

    it("parses valid float", () => {
      expect(SchemaValidator.coerceNumber("3.14")).toBe(3.14);
    });

    it("returns null for non-numeric string", () => {
      expect(SchemaValidator.coerceNumber("abc")).toBeNull();
    });

    it("returns null when below min", () => {
      expect(SchemaValidator.coerceNumber("-1", { min: 0 })).toBeNull();
    });

    it("returns null when above max", () => {
      expect(SchemaValidator.coerceNumber("100", { max: 50 })).toBeNull();
    });

    it("respects min boundary (inclusive)", () => {
      expect(SchemaValidator.coerceNumber("0", { min: 0 })).toBe(0);
    });

    it("respects max boundary (inclusive)", () => {
      expect(SchemaValidator.coerceNumber("50", { max: 50 })).toBe(50);
    });

    it("parses without options", () => {
      expect(SchemaValidator.coerceNumber("123")).toBe(123);
    });
  });

  describe("coerceBoolean", () => {
    it("coerces 'true' to true", () => {
      expect(SchemaValidator.coerceBoolean("true")).toBe(true);
    });

    it("coerces 'false' to false", () => {
      expect(SchemaValidator.coerceBoolean("false")).toBe(false);
    });

    it("returns null for other values", () => {
      expect(SchemaValidator.coerceBoolean("yes")).toBeNull();
      expect(SchemaValidator.coerceBoolean("1")).toBeNull();
      expect(SchemaValidator.coerceBoolean("")).toBeNull();
    });
  });

  describe("stripQuotes", () => {
    it("strips single quotes", () => {
      expect(SchemaValidator.stripQuotes("'interactive'")).toBe("interactive");
    });

    it("strips double quotes", () => {
      expect(SchemaValidator.stripQuotes('"headless"')).toBe("headless");
    });

    it("returns unquoted values as-is", () => {
      expect(SchemaValidator.stripQuotes("plain")).toBe("plain");
    });

    it("handles whitespace around quotes", () => {
      expect(SchemaValidator.stripQuotes("  'spaced'  ")).toBe("spaced");
    });

    it("does not strip mismatched quotes", () => {
      expect(SchemaValidator.stripQuotes("'mismatched\"")).toBe("'mismatched\"");
    });
  });
});
