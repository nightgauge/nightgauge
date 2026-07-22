import { describe, it, expect } from "vitest";
import {
  sanitizeToolCallArgs,
  MAX_ARG_VALUE_LENGTH,
  MAX_SERIALIZED_BYTES,
} from "../../src/utils/toolCallSanitizer";

/**
 * Pins the size contract on in-memory tool call args. The extension host
 * holds `accumulatedToolCalls` for the entire pipeline run; without these
 * caps a single `Write({content: "<100KB>"})` × N calls saturates RAM.
 */
describe("sanitizeToolCallArgs", () => {
  it("returns undefined for undefined input", () => {
    expect(sanitizeToolCallArgs(undefined)).toBeUndefined();
  });

  it("preserves small primitive args unchanged", () => {
    expect(sanitizeToolCallArgs({ file_path: "/a", limit: 10 })).toEqual({
      file_path: "/a",
      limit: 10,
    });
  });

  it("truncates long string values with an ellipsis", () => {
    const long = "x".repeat(MAX_ARG_VALUE_LENGTH + 500);
    const result = sanitizeToolCallArgs({ content: long });
    expect(typeof result?.content).toBe("string");
    expect((result!.content as string).length).toBe(MAX_ARG_VALUE_LENGTH + 1); // +1 for the ellipsis
    expect((result!.content as string).endsWith("…")).toBe(true);
  });

  it("redacts sensitive keys by name", () => {
    expect(
      sanitizeToolCallArgs({
        api_key: "secret123",
        password: "pw",
        auth_token: "abc",
        file_path: "/safe",
      })
    ).toEqual({
      api_key: "[REDACTED]",
      password: "[REDACTED]",
      auth_token: "[REDACTED]",
      file_path: "/safe",
    });
  });

  it("recurses into nested objects and still truncates strings at depth", () => {
    const long = "y".repeat(500);
    const result = sanitizeToolCallArgs({ outer: { inner: long } });
    const outer = result?.outer as Record<string, unknown>;
    expect(typeof outer.inner).toBe("string");
    expect((outer.inner as string).length).toBe(MAX_ARG_VALUE_LENGTH + 1);
  });

  it("caps arrays at 10 items and keeps a tail-count summary", () => {
    const many = Array.from({ length: 25 }, (_, i) => `item-${i}`);
    const result = sanitizeToolCallArgs({ items: many });
    const items = result?.items as unknown[];
    expect(items.length).toBe(11); // 10 sampled + 1 summary
    expect(items[10]).toBe("…+15 more");
  });

  it("stops recursion past the depth limit", () => {
    // Build a 10-level nested object — exceeds MAX_DEPTH (6)
    let nested: unknown = "bottom";
    for (let i = 0; i < 10; i++) nested = { level: nested };
    const result = sanitizeToolCallArgs({ root: nested });
    // Walk down until we find DEPTH_LIMIT marker
    let cursor = result?.root as unknown;
    let found = false;
    for (let i = 0; i < 15; i++) {
      if (cursor === "[DEPTH_LIMIT]") {
        found = true;
        break;
      }
      if (cursor && typeof cursor === "object" && "level" in (cursor as object)) {
        cursor = (cursor as { level: unknown }).level;
      } else {
        break;
      }
    }
    expect(found).toBe(true);
  });

  it("replaces args with a size summary when serialized size blows the budget", () => {
    // Build a structure that survives recursive truncation but is still big:
    // many nested keys, each with a ~200-char string — the shape overhead alone
    // pushes total JSON past MAX_SERIALIZED_BYTES.
    const big: Record<string, unknown> = {};
    // Use "field_" prefix — "key_" would match the SENSITIVE_KEYS_PATTERN
    // and every value would be replaced with "[REDACTED]", defeating the test.
    for (let i = 0; i < 50; i++) {
      big[`field_${i}`] = "x".repeat(MAX_ARG_VALUE_LENGTH);
    }
    const result = sanitizeToolCallArgs(big);
    expect(result?._truncated).toBe(true);
    expect(Array.isArray(result?.keys)).toBe(true);
    expect((result!.keys as string[]).length).toBe(50);
    expect(typeof result?.approx_bytes).toBe("number");
    expect((result!.approx_bytes as number) > MAX_SERIALIZED_BYTES).toBe(true);
  });

  it("serialized output stays under MAX_SERIALIZED_BYTES when inputs fit", () => {
    const result = sanitizeToolCallArgs({
      file_path: "/a/b.ts",
      old_string: "abc",
      new_string: "def",
    });
    expect(JSON.stringify(result).length).toBeLessThan(MAX_SERIALIZED_BYTES);
  });
});
