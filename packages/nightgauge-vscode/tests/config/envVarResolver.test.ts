/**
 * Tests for Environment Variable Resolver
 *
 * Tests the env var resolution module including:
 * - Path to env var name transformation
 * - Type coercion (boolean, number, array, string)
 * - Schema introspection for type detection
 * - Resolution of NIGHTGAUGE_* variables
 *
 * @see Issue #436 - Config Merge Engine with 6-Tier Precedence Chain
 * @see packages/nightgauge-vscode/src/config/envVarResolver.ts
 */

import { describe, it, expect, beforeEach, afterEach } from "vitest";
import {
  configPathToEnvVar,
  envVarToConfigPath,
  getSchemaType,
  parseEnvValue,
  parseBooleanEnv,
  parseNumberEnv,
  parseArrayEnv,
  parseObjectEnv,
  setNestedValue,
  resolveEnvVars,
  getEnvConfigValue,
  hasEnvOverride,
  getEnvVarName,
  getAllKnownEnvVars,
  ENV_VAR_PREFIX,
} from "../../src/config/envVarResolver";

describe("envVarResolver", () => {
  // Store original env vars
  const originalEnv = { ...process.env };

  beforeEach(() => {
    // Clear all NIGHTGAUGE_ env vars
    Object.keys(process.env).forEach((key) => {
      if (key.startsWith("NIGHTGAUGE_")) {
        delete process.env[key];
      }
    });
  });

  afterEach(() => {
    // Restore original environment
    process.env = { ...originalEnv };
  });

  // ============================================================================
  // Path to Env Var Transformation
  // ============================================================================

  describe("configPathToEnvVar", () => {
    it("transforms simple path", () => {
      expect(configPathToEnvVar("pr.merge_strategy")).toBe("NIGHTGAUGE_PR_MERGE_STRATEGY");
    });

    it("transforms nested path", () => {
      expect(configPathToEnvVar("pipeline.retry.max_auto_attempts")).toBe(
        "NIGHTGAUGE_PIPELINE_RETRY_MAX_AUTO_ATTEMPTS"
      );
    });

    it("transforms deeply nested path", () => {
      expect(configPathToEnvVar("pipeline.retry.max_auto_attempts")).toBe(
        "NIGHTGAUGE_PIPELINE_RETRY_MAX_AUTO_ATTEMPTS"
      );
    });

    it("handles single segment path", () => {
      expect(configPathToEnvVar("project")).toBe("NIGHTGAUGE_PROJECT");
    });

    it("handles camelCase in path", () => {
      expect(configPathToEnvVar("humanInTheLoop.autoAccept")).toBe(
        "NIGHTGAUGE_HUMAN_IN_THE_LOOP_AUTO_ACCEPT"
      );
    });
  });

  describe("envVarToConfigPath", () => {
    it("transforms env var to path", () => {
      expect(envVarToConfigPath("NIGHTGAUGE_PR_MERGE_STRATEGY")).toBe("pr.merge.strategy");
    });

    it("returns null for non-NIGHTGAUGE_ vars", () => {
      expect(envVarToConfigPath("OTHER_VAR")).toBeNull();
    });

    it("handles deeply nested env vars", () => {
      expect(envVarToConfigPath("NIGHTGAUGE_PIPELINE_RETRY_MAX_AUTO_ATTEMPTS")).toBe(
        "pipeline.retry.max.auto.attempts"
      );
    });
  });

  // ============================================================================
  // Schema Type Introspection
  // ============================================================================

  describe("getSchemaType", () => {
    it("returns boolean for boolean fields", () => {
      expect(getSchemaType("pr.delete_branch")).toBe("boolean");
      expect(getSchemaType("pipeline.auto_fix")).toBe("boolean");
      expect(getSchemaType("issue.auto_assign")).toBe("boolean");
    });

    it("returns number for numeric fields", () => {
      expect(getSchemaType("project.number")).toBe("number");
      expect(getSchemaType("pipeline.ci_timeout")).toBe("number");
      expect(getSchemaType("validation.max_files_changed")).toBe("number");
    });

    it("returns string for string fields", () => {
      expect(getSchemaType("pr.merge_strategy")).toBe("string");
      expect(getSchemaType("branch.base")).toBe("string");
      expect(getSchemaType("commands.test")).toBe("string");
    });

    it("returns array for array fields", () => {
      expect(getSchemaType("pr.reviewers")).toBe("array");
      expect(getSchemaType("branch.protected")).toBe("array");
      expect(getSchemaType("issue.default_labels")).toBe("array");
    });

    it("returns null for unknown paths", () => {
      expect(getSchemaType("unknown.path")).toBeNull();
      expect(getSchemaType("pr.nonexistent")).toBeNull();
    });

    it("handles nested paths correctly", () => {
      expect(getSchemaType("pipeline.retry.max_auto_attempts")).toBe("number");
      expect(getSchemaType("pipeline.skip.tests")).toBe("boolean");
    });
  });

  // ============================================================================
  // Boolean Parsing
  // ============================================================================

  describe("parseBooleanEnv", () => {
    it("parses true values", () => {
      expect(parseBooleanEnv("true")).toBe(true);
      expect(parseBooleanEnv("TRUE")).toBe(true);
      expect(parseBooleanEnv("True")).toBe(true);
      expect(parseBooleanEnv("yes")).toBe(true);
      expect(parseBooleanEnv("YES")).toBe(true);
      expect(parseBooleanEnv("1")).toBe(true);
      expect(parseBooleanEnv("on")).toBe(true);
      expect(parseBooleanEnv("ON")).toBe(true);
    });

    it("parses false values", () => {
      expect(parseBooleanEnv("false")).toBe(false);
      expect(parseBooleanEnv("FALSE")).toBe(false);
      expect(parseBooleanEnv("False")).toBe(false);
      expect(parseBooleanEnv("no")).toBe(false);
      expect(parseBooleanEnv("NO")).toBe(false);
      expect(parseBooleanEnv("0")).toBe(false);
      expect(parseBooleanEnv("off")).toBe(false);
      expect(parseBooleanEnv("OFF")).toBe(false);
    });

    it("returns undefined for invalid values", () => {
      expect(parseBooleanEnv("invalid")).toBeUndefined();
      expect(parseBooleanEnv("maybe")).toBeUndefined();
      expect(parseBooleanEnv("2")).toBeUndefined();
    });

    it("handles whitespace", () => {
      expect(parseBooleanEnv("  true  ")).toBe(true);
      expect(parseBooleanEnv("  false  ")).toBe(false);
    });
  });

  // ============================================================================
  // Number Parsing
  // ============================================================================

  describe("parseNumberEnv", () => {
    it("parses integers", () => {
      expect(parseNumberEnv("42")).toBe(42);
      expect(parseNumberEnv("0")).toBe(0);
      expect(parseNumberEnv("-1")).toBe(-1);
    });

    it("parses floats", () => {
      expect(parseNumberEnv("3.14")).toBe(3.14);
      expect(parseNumberEnv("0.5")).toBe(0.5);
    });

    it("returns undefined for invalid values", () => {
      expect(parseNumberEnv("abc")).toBeUndefined();
      expect(parseNumberEnv("12abc")).toBeUndefined();
      expect(parseNumberEnv("")).toBeUndefined();
    });

    it("handles whitespace", () => {
      expect(parseNumberEnv("  42  ")).toBe(42);
    });
  });

  // ============================================================================
  // Array Parsing
  // ============================================================================

  describe("parseArrayEnv", () => {
    it("parses comma-separated values", () => {
      expect(parseArrayEnv("a,b,c")).toEqual(["a", "b", "c"]);
      expect(parseArrayEnv("alice,bob")).toEqual(["alice", "bob"]);
    });

    it("trims whitespace from elements", () => {
      expect(parseArrayEnv("a, b, c")).toEqual(["a", "b", "c"]);
      expect(parseArrayEnv("  a  ,  b  ")).toEqual(["a", "b"]);
    });

    it("parses JSON arrays", () => {
      expect(parseArrayEnv('["a","b","c"]')).toEqual(["a", "b", "c"]);
      expect(parseArrayEnv("[1, 2, 3]")).toEqual(["1", "2", "3"]);
    });

    it("returns empty array for empty string", () => {
      expect(parseArrayEnv("")).toEqual([]);
    });

    it("falls back to comma-separated for invalid JSON", () => {
      expect(parseArrayEnv("[invalid")).toEqual(["[invalid"]);
    });

    it("handles single element", () => {
      expect(parseArrayEnv("single")).toEqual(["single"]);
    });
  });

  // ============================================================================
  // Object Parsing
  // ============================================================================

  describe("parseObjectEnv", () => {
    it("parses JSON objects", () => {
      expect(parseObjectEnv('{"a":1}')).toEqual({ a: 1 });
      expect(parseObjectEnv('{"key":"value"}')).toEqual({ key: "value" });
    });

    it("returns undefined for non-object values", () => {
      expect(parseObjectEnv("not-json")).toBeUndefined();
      expect(parseObjectEnv("[1,2,3]")).toBeUndefined();
    });

    it("returns undefined for invalid JSON", () => {
      expect(parseObjectEnv("{invalid}")).toBeUndefined();
    });
  });

  // ============================================================================
  // parseEnvValue (Type-Aware Parsing)
  // ============================================================================

  describe("parseEnvValue", () => {
    it("parses boolean type", () => {
      expect(parseEnvValue("true", "boolean")).toBe(true);
      expect(parseEnvValue("false", "boolean")).toBe(false);
    });

    it("parses number type", () => {
      expect(parseEnvValue("42", "number")).toBe(42);
      expect(parseEnvValue("3.14", "number")).toBe(3.14);
    });

    it("parses array type", () => {
      expect(parseEnvValue("a,b,c", "array")).toEqual(["a", "b", "c"]);
    });

    it("parses string type (passthrough)", () => {
      expect(parseEnvValue("hello", "string")).toBe("hello");
    });

    it("defaults to string for null type", () => {
      expect(parseEnvValue("hello", null)).toBe("hello");
    });

    it("returns undefined for empty value", () => {
      expect(parseEnvValue("", "string")).toBeUndefined();
    });
  });

  // ============================================================================
  // setNestedValue
  // ============================================================================

  describe("setNestedValue", () => {
    it("rejects prototype-polluting paths", () => {
      expect(() => setNestedValue({}, "__proto__.polluted", true)).toThrow(
        "Unsafe configuration path"
      );
      expect(({} as Record<string, unknown>)["polluted"]).toBeUndefined();
    });

    it("sets simple path", () => {
      const obj: Record<string, unknown> = {};
      setNestedValue(obj, "key", "value");
      expect(obj).toEqual({ key: "value" });
    });

    it("sets nested path", () => {
      const obj: Record<string, unknown> = {};
      setNestedValue(obj, "pr.merge_strategy", "squash");
      expect(obj).toEqual({ pr: { merge_strategy: "squash" } });
    });

    it("sets deeply nested path", () => {
      const obj: Record<string, unknown> = {};
      setNestedValue(obj, "pipeline.retry.max_auto_attempts", 5);
      expect(obj).toEqual({ pipeline: { retry: { max_auto_attempts: 5 } } });
    });

    it("preserves existing values", () => {
      const obj: Record<string, unknown> = { pr: { delete_branch: true } };
      setNestedValue(obj, "pr.merge_strategy", "squash");
      expect(obj).toEqual({
        pr: { delete_branch: true, merge_strategy: "squash" },
      });
    });

    it("overwrites existing path", () => {
      const obj: Record<string, unknown> = { pr: { merge_strategy: "merge" } };
      setNestedValue(obj, "pr.merge_strategy", "squash");
      expect(obj).toEqual({ pr: { merge_strategy: "squash" } });
    });
  });

  // ============================================================================
  // resolveEnvVars
  // ============================================================================

  describe("resolveEnvVars", () => {
    it("returns empty config when no NIGHTGAUGE_ vars", () => {
      const result = resolveEnvVars();

      expect(result.config).toEqual({});
      expect(result.appliedVars).toHaveLength(0);
      expect(result.errors).toHaveLength(0);
    });

    it("resolves boolean env var", () => {
      process.env.NIGHTGAUGE_PR_DELETE_BRANCH = "true";

      const result = resolveEnvVars();

      expect(result.config.pr?.delete_branch).toBe(true);
      expect(result.appliedVars).toContain("NIGHTGAUGE_PR_DELETE_BRANCH");
    });

    it("resolves number env var", () => {
      process.env.NIGHTGAUGE_PIPELINE_CI_TIMEOUT = "600";

      const result = resolveEnvVars();

      expect(result.config.pipeline?.ci_timeout).toBe(600);
      expect(result.appliedVars).toContain("NIGHTGAUGE_PIPELINE_CI_TIMEOUT");
    });

    it("resolves string env var", () => {
      process.env.NIGHTGAUGE_PR_MERGE_STRATEGY = "rebase";

      const result = resolveEnvVars();

      expect(result.config.pr?.merge_strategy).toBe("rebase");
      expect(result.appliedVars).toContain("NIGHTGAUGE_PR_MERGE_STRATEGY");
    });

    it("resolves array env var (comma-separated)", () => {
      process.env.NIGHTGAUGE_PR_REVIEWERS = "alice,bob,charlie";

      const result = resolveEnvVars();

      expect(result.config.pr?.reviewers).toEqual(["alice", "bob", "charlie"]);
    });

    it("resolves array env var (JSON)", () => {
      process.env.NIGHTGAUGE_PR_REVIEWERS = '["alice", "bob"]';

      const result = resolveEnvVars();

      expect(result.config.pr?.reviewers).toEqual(["alice", "bob"]);
    });

    it("resolves multiple env vars", () => {
      process.env.NIGHTGAUGE_PR_DELETE_BRANCH = "true";
      process.env.NIGHTGAUGE_PIPELINE_AUTO_FIX = "false";
      process.env.NIGHTGAUGE_PIPELINE_CI_TIMEOUT = "600";

      const result = resolveEnvVars();

      expect(result.config.pr?.delete_branch).toBe(true);
      expect(result.config.pipeline?.auto_fix).toBe(false);
      expect(result.config.pipeline?.ci_timeout).toBe(600);
      expect(result.appliedVars).toHaveLength(3);
    });

    it("ignores unknown env vars", () => {
      process.env.NIGHTGAUGE_UNKNOWN_VAR = "value";

      const result = resolveEnvVars();

      expect(result.appliedVars).not.toContain("NIGHTGAUGE_UNKNOWN_VAR");
      expect(result.errors).toHaveLength(0);
    });

    it("ignores empty values", () => {
      process.env.NIGHTGAUGE_PR_DELETE_BRANCH = "";

      const result = resolveEnvVars();

      expect(result.config.pr?.delete_branch).toBeUndefined();
      expect(result.appliedVars).toHaveLength(0);
    });

    it("accepts custom env object", () => {
      const customEnv = {
        NIGHTGAUGE_PR_DELETE_BRANCH: "true",
        NIGHTGAUGE_PIPELINE_AUTO_FIX: "false",
      };

      const result = resolveEnvVars(customEnv);

      expect(result.config.pr?.delete_branch).toBe(true);
      expect(result.config.pipeline?.auto_fix).toBe(false);
    });

    it("reports errors for invalid values", () => {
      process.env.NIGHTGAUGE_PIPELINE_CI_TIMEOUT = "not-a-number";

      const result = resolveEnvVars();

      expect(result.errors.length).toBeGreaterThan(0);
      expect(result.errors[0].envVar).toBe("NIGHTGAUGE_PIPELINE_CI_TIMEOUT");
    });

    it("resolves nested paths correctly", () => {
      process.env.NIGHTGAUGE_PIPELINE_RETRY_MAX_AUTO_ATTEMPTS = "5";

      const result = resolveEnvVars();

      expect(result.config.pipeline?.retry?.max_auto_attempts).toBe(5);
    });
  });

  // ============================================================================
  // Convenience Functions
  // ============================================================================

  describe("getEnvConfigValue", () => {
    it("returns parsed value for existing env var", () => {
      process.env.NIGHTGAUGE_PR_DELETE_BRANCH = "true";

      expect(getEnvConfigValue("pr.delete_branch")).toBe(true);
    });

    it("returns undefined for missing env var", () => {
      expect(getEnvConfigValue("pr.delete_branch")).toBeUndefined();
    });

    it("returns undefined for empty env var", () => {
      process.env.NIGHTGAUGE_PR_DELETE_BRANCH = "";

      expect(getEnvConfigValue("pr.delete_branch")).toBeUndefined();
    });
  });

  describe("hasEnvOverride", () => {
    it("returns true for existing non-empty env var", () => {
      process.env.NIGHTGAUGE_PR_DELETE_BRANCH = "true";

      expect(hasEnvOverride("pr.delete_branch")).toBe(true);
    });

    it("returns false for missing env var", () => {
      expect(hasEnvOverride("pr.delete_branch")).toBe(false);
    });

    it("returns false for empty env var", () => {
      process.env.NIGHTGAUGE_PR_DELETE_BRANCH = "";

      expect(hasEnvOverride("pr.delete_branch")).toBe(false);
    });
  });

  describe("getEnvVarName", () => {
    it("returns the env var name for a config path", () => {
      expect(getEnvVarName("pr.merge_strategy")).toBe("NIGHTGAUGE_PR_MERGE_STRATEGY");
    });
  });

  describe("getAllKnownEnvVars", () => {
    it("returns a sorted list of known env vars", () => {
      const vars = getAllKnownEnvVars();

      expect(vars.length).toBeGreaterThan(0);
      expect(vars).toContain("NIGHTGAUGE_PR_DELETE_BRANCH");
      expect(vars).toContain("NIGHTGAUGE_PIPELINE_AUTO_FIX");
      // Check sorted
      expect(vars).toEqual([...vars].sort());
    });
  });

  // 'ENV_VAR_PREFIX is NIGHTGAUGE_' test removed (Issue #1826):
  // Tautological — a constant checking its own literal value tests nothing.
});
