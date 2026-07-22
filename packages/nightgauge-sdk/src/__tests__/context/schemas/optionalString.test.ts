import { describe, expect, it } from "vitest";
import { z } from "zod";
import { optionalString } from "../../../context/schemas/helpers.js";
import { DevContextSchema } from "../../../context/schemas/index.js";

// optionalString helper — direct schema tests

describe("optionalString — empty/whitespace normalization", () => {
  const schema = z.object({ field: optionalString() });

  it("accepts null unchanged", () => {
    const result = schema.safeParse({ field: null });
    expect(result.success).toBe(true);
    if (result.success) expect(result.data.field).toBeNull();
  });

  it("accepts undefined (missing field)", () => {
    const result = schema.safeParse({});
    expect(result.success).toBe(true);
    if (result.success) expect(result.data.field).toBeUndefined();
  });

  it("accepts a non-empty string unchanged", () => {
    const result = schema.safeParse({ field: "value" });
    expect(result.success).toBe(true);
    if (result.success) expect(result.data.field).toBe("value");
  });

  it("normalizes empty string to null", () => {
    const result = schema.safeParse({ field: "" });
    expect(result.success).toBe(true);
    if (result.success) expect(result.data.field).toBeNull();
  });

  it("normalizes whitespace-only string to null", () => {
    const result = schema.safeParse({ field: "   " });
    expect(result.success).toBe(true);
    if (result.success) expect(result.data.field).toBeNull();
  });

  it("normalizes tab/newline-only string to null", () => {
    const result = schema.safeParse({ field: "\t\n  " });
    expect(result.success).toBe(true);
    if (result.success) expect(result.data.field).toBeNull();
  });
});

// DevContextSchema — the exact failure mode from the bug report

describe("DevContextSchema — tests_status.test_command empty-string regression (Issue #2719)", () => {
  const minimalDev = {
    schema_version: "1.4",
    issue_number: 42,
  };

  it("accepts empty-string test_command (previously warned)", () => {
    const input = {
      ...minimalDev,
      tests_status: { test_command: "" },
    };
    const result = DevContextSchema.safeParse(input);
    expect(result.success).toBe(true);
    if (result.success) {
      expect(result.data.tests_status?.test_command).toBeNull();
    }
  });

  it("accepts null test_command (backward compat)", () => {
    const input = {
      ...minimalDev,
      tests_status: { test_command: null },
    };
    const result = DevContextSchema.safeParse(input);
    expect(result.success).toBe(true);
    if (result.success) {
      expect(result.data.tests_status?.test_command).toBeNull();
    }
  });

  it("accepts real test_command string (backward compat)", () => {
    const input = {
      ...minimalDev,
      tests_status: { test_command: "npm test" },
    };
    const result = DevContextSchema.safeParse(input);
    expect(result.success).toBe(true);
    if (result.success) {
      expect(result.data.tests_status?.test_command).toBe("npm test");
    }
  });

  it("accepts empty-string commit_sha (same pattern)", () => {
    const input = { ...minimalDev, commit_sha: "" };
    const result = DevContextSchema.safeParse(input);
    expect(result.success).toBe(true);
    if (result.success) {
      expect(result.data.commit_sha).toBeNull();
    }
  });
});
