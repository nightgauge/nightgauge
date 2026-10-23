/**
 * Tests for zodErrorFormatter utility
 *
 * @see Issue #2552 - Pipeline context schema self-correction
 */

import { describe, it, expect } from "vitest";
import { z } from "zod";
import { formatZodErrors, formatZodErrorsForPrompt } from "../../src/utils/zodErrorFormatter";

// Helper: produce a ZodError by parsing invalid data against a schema
function getZodError(schema: z.ZodSchema, data: unknown): z.ZodError {
  const result = schema.safeParse(data);
  if (result.success) throw new Error("Expected validation to fail");
  return result.error;
}

describe("zodErrorFormatter", () => {
  describe("formatZodErrors", () => {
    it("returns structured errors for a single missing field", () => {
      const schema = z.object({ name: z.string() });
      const error = getZodError(schema, {});

      const result = formatZodErrors(error);
      expect(result).toHaveLength(1);
      expect(result[0].path).toBe("name");
      expect(result[0].severity).toBe("error");
      expect(result[0].message).toContain("string");
      expect(result[0].suggestion).toBeDefined();
    });

    it("returns structured errors for multiple fields", () => {
      const schema = z.object({
        name: z.string(),
        age: z.number(),
        active: z.boolean(),
      });
      const error = getZodError(schema, {});

      const result = formatZodErrors(error);
      expect(result).toHaveLength(3);
      expect(result.map((r) => r.path)).toEqual(expect.arrayContaining(["name", "age", "active"]));
    });

    it("formats nested field paths correctly", () => {
      const schema = z.object({
        files_changed: z.object({
          created: z.array(z.string()),
        }),
      });
      const error = getZodError(schema, { files_changed: { created: "not-an-array" } });

      const result = formatZodErrors(error);
      expect(result).toHaveLength(1);
      expect(result[0].path).toBe("files_changed.created");
    });

    it("handles type mismatch errors", () => {
      const schema = z.object({ count: z.number() });
      const error = getZodError(schema, { count: "not-a-number" });

      const result = formatZodErrors(error);
      expect(result).toHaveLength(1);
      expect(result[0].message).toContain("number");
      // Zod v4 may not expose input on nested issues — accept either the
      // actual received type or "undefined" as the fallback.
      expect(result[0].message).toMatch(/string|undefined/);
    });

    it("handles enum validation errors", () => {
      const schema = z.object({
        status: z.enum(["passed", "failed", "skipped"]),
      });
      const error = getZodError(schema, { status: "invalid" });

      const result = formatZodErrors(error);
      expect(result).toHaveLength(1);
      // Zod v4 may not expose input on nested issues — accept "Invalid value"
      // prefix with either the actual input or "undefined" fallback.
      expect(result[0].message).toMatch(/Invalid value|invalid/i);
      expect(result[0].message).toMatch(/passed|failed|skipped/);
      expect(result[0].suggestion).toContain('"passed"');
    });

    it("handles missing-field errors (undefined received)", () => {
      const schema = z.object({ required_field: z.string() });
      const error = getZodError(schema, {});

      const result = formatZodErrors(error);
      expect(result).toHaveLength(1);
      expect(result[0].suggestion).toContain("Add this required field");
    });

    it("handles empty error list gracefully", () => {
      // Construct an empty ZodError manually
      const emptyError = new z.ZodError([]);
      const result = formatZodErrors(emptyError);
      expect(result).toHaveLength(0);
    });
  });

  describe("formatZodErrorsForPrompt", () => {
    it("formats a single error into prompt text", () => {
      const schema = z.object({ name: z.string() });
      const error = getZodError(schema, {});

      const text = formatZodErrorsForPrompt(error);
      expect(text).toContain("schema validation errors");
      expect(text).toContain("name:");
      expect(text).toContain("Ensure your JSON output");
    });

    it("formats multiple errors into prompt text", () => {
      const schema = z.object({
        name: z.string(),
        count: z.number(),
      });
      const error = getZodError(schema, {});

      const text = formatZodErrorsForPrompt(error);
      expect(text).toContain("name:");
      expect(text).toContain("count:");
    });

    it("includes suggestions when available", () => {
      const schema = z.object({
        status: z.enum(["passed", "failed"]),
      });
      const error = getZodError(schema, { status: "invalid" });

      const text = formatZodErrorsForPrompt(error);
      expect(text).toContain("→");
      expect(text).toMatch(/"passed"|"failed"/);
    });

    it("handles empty error list", () => {
      const emptyError = new z.ZodError([]);
      const text = formatZodErrorsForPrompt(emptyError);
      expect(text).toContain("No schema violations");
    });

    it("includes 'Do NOT change any other behavior' instruction", () => {
      const schema = z.object({ name: z.string() });
      const error = getZodError(schema, {});

      const text = formatZodErrorsForPrompt(error);
      expect(text).toContain("Do NOT change any other behavior");
    });

    it("formats nested paths in prompt text", () => {
      const schema = z.object({
        tests_status: z.object({
          passed: z.number(),
          failed: z.number(),
        }),
      });
      const error = getZodError(schema, {
        tests_status: { passed: "wrong", failed: "wrong" },
      });

      const text = formatZodErrorsForPrompt(error);
      expect(text).toContain("tests_status.passed:");
      expect(text).toContain("tests_status.failed:");
    });
  });
});
