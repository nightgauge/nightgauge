/**
 * concurrency-schema-key-guard.test.ts
 *
 * Schema-key-existence guard for the unified concurrency model (#3781/#3789).
 *
 * These tests fail immediately if:
 *   1. A key referenced by a concurrency resolver is absent from
 *      ConcurrencyConfigSchema (catches silent mock-drift regressions).
 *   2. A deprecated pre-#3781 key is accidentally re-added to the schema.
 *
 * Consumer audit (as of #3789 — verified by reading source, not tests):
 *   - RepositoriesTreeProvider: reads via createSequentialRepoConfigService
 *     (readSequentialRepo / readMaxConcurrentRepo) — no raw key access.
 *   - ConcurrentPipelineManager: reads via getConcurrentPipelineConfig /
 *     parseConcurrencyWorkspaceMax — uses "workspace_max" line scanner.
 *   - cmd/nightgauge/main.go: uses ResolveConcurrency(cfg) — no raw key.
 *   - SettingsHtml.ts: renders resolved values from bridge, not raw keys.
 *
 * All consumers go through resolver functions, so any key rename in the schema
 * is caught here before it can silently pass mocked tests.
 */

import { describe, it, expect } from "vitest";
import { z } from "zod";

vi.mock("vscode", () => ({}));

import { ConcurrencyConfigSchema } from "../../src/config/schema";

describe("ConcurrencyConfigSchema shape guard", () => {
  it("ConcurrencyConfigSchema is a ZodObject", () => {
    expect(ConcurrencyConfigSchema).toBeInstanceOf(z.ZodObject);
  });

  it("all keys referenced by concurrency resolvers exist in the schema", () => {
    // Keys read by sequentialRepoConfig.ts and parseConcurrencyWorkspaceMax
    const resolverKeys = ["workspace_max", "per_repo_max", "repository_overrides"];
    const schemaKeys = new Set(Object.keys(ConcurrencyConfigSchema.shape));
    for (const key of resolverKeys) {
      expect(
        schemaKeys.has(key),
        `resolver key "${key}" missing from ConcurrencyConfigSchema`
      ).toBe(true);
    }
  });

  it("deprecated pre-#3781 key paths are absent from ConcurrencyConfigSchema", () => {
    // These keys were removed in #3781. If any reappears, mock tests will start
    // validating against a schema that callers never read.
    const removedKeys = [
      "max_concurrent", // was pipeline.max_concurrent / autonomous.max_concurrent
      "sequential", // was autonomous.repositories.<repo>.sequential
    ];
    const schemaKeys = new Set(Object.keys(ConcurrencyConfigSchema.shape));
    for (const key of removedKeys) {
      expect(
        schemaKeys.has(key),
        `deprecated key "${key}" must NOT be in ConcurrencyConfigSchema (removed in #3781)`
      ).toBe(false);
    }
  });

  it("schema has exactly the expected set of keys (no undocumented additions)", () => {
    const expectedKeys = new Set(["workspace_max", "per_repo_max", "repository_overrides"]);
    const actualKeys = new Set(Object.keys(ConcurrencyConfigSchema.shape));
    for (const key of actualKeys) {
      expect(
        expectedKeys.has(key),
        `unexpected key "${key}" found in ConcurrencyConfigSchema — update this test if intentional`
      ).toBe(true);
    }
    for (const key of expectedKeys) {
      expect(actualKeys.has(key), `expected key "${key}" missing from schema`).toBe(true);
    }
  });
});
