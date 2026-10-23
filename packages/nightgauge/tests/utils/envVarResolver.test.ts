import { describe, it, expect, beforeEach, afterEach } from "vitest";
import { EnvVarResolver } from "../../src/utils/envVarResolver";

describe("EnvVarResolver", () => {
  const originalEnv = process.env;

  beforeEach(() => {
    process.env = { ...originalEnv };
  });

  afterEach(() => {
    process.env = originalEnv;
  });

  describe("get", () => {
    it("returns env var value when set", () => {
      process.env.TEST_VAR = "test-value";
      expect(EnvVarResolver.get("TEST_VAR")).toBe("test-value");
    });

    it("returns null when env var is not set", () => {
      delete process.env.TEST_VAR;
      expect(EnvVarResolver.get("TEST_VAR")).toBeNull();
    });

    it("returns null when env var is empty string", () => {
      process.env.TEST_VAR = "";
      expect(EnvVarResolver.get("TEST_VAR")).toBeNull();
    });
  });

  describe("expandPlaceholder", () => {
    it("expands env:VAR_NAME placeholders", () => {
      process.env.MY_PAT = "ghp_abc123";
      expect(EnvVarResolver.expandPlaceholder("env:MY_PAT")).toBe("ghp_abc123");
    });

    it("returns null when env var is not set", () => {
      delete process.env.MISSING_VAR;
      expect(EnvVarResolver.expandPlaceholder("env:MISSING_VAR")).toBeNull();
    });

    it("returns raw value when not a placeholder", () => {
      expect(EnvVarResolver.expandPlaceholder("ghp_abc123")).toBe("ghp_abc123");
    });

    it("returns null for empty var name after env:", () => {
      expect(EnvVarResolver.expandPlaceholder("env:")).toBeNull();
    });

    it("trims whitespace from var name", () => {
      process.env.SPACED_VAR = "value";
      expect(EnvVarResolver.expandPlaceholder("env: SPACED_VAR ")).toBe("value");
    });
  });

  describe("isPlaceholder", () => {
    it("returns true for env: prefix", () => {
      expect(EnvVarResolver.isPlaceholder("env:MY_VAR")).toBe(true);
    });

    it("returns false for non-placeholder values", () => {
      expect(EnvVarResolver.isPlaceholder("ghp_abc123")).toBe(false);
    });

    it("returns true for env: with empty var name", () => {
      expect(EnvVarResolver.isPlaceholder("env:")).toBe(true);
    });
  });
});
