/**
 * Tests for concurrent pipeline configuration
 *
 * Verifies:
 * - max_concurrent schema validation (1-8 range)
 * - worktree_base schema validation
 * - Default values in DEFAULT_CONFIG
 * - getConcurrentPipelineConfig utility
 *
 * @see Issue #1621 - Git worktree-based concurrent pipeline execution
 */

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";

// Mock vscode
vi.mock("vscode", () => ({
  workspace: {
    workspaceFolders: [{ uri: { fsPath: "/test" } }],
  },
}));

import { PipelineConfigSchema, DEFAULT_CONFIG } from "../../src/config/schema";
import {
  parseConcurrencyWorkspaceMax,
  parseMaxConcurrentBlocks,
} from "../../src/utils/resolvers/otherResolver";

describe("Concurrent pipeline config schema", () => {
  describe("max_concurrent", () => {
    it("accepts valid values (1-8)", () => {
      for (const val of [1, 2, 4, 8]) {
        const result = PipelineConfigSchema.safeParse({ max_concurrent: val });
        expect(result.success).toBe(true);
      }
    });

    it("rejects values below 1", () => {
      const result = PipelineConfigSchema.safeParse({ max_concurrent: 0 });
      expect(result.success).toBe(false);
    });

    it("rejects values above 8", () => {
      const result = PipelineConfigSchema.safeParse({ max_concurrent: 9 });
      expect(result.success).toBe(false);
    });

    it("rejects non-integer values", () => {
      const result = PipelineConfigSchema.safeParse({ max_concurrent: 2.5 });
      expect(result.success).toBe(false);
    });

    it("is optional", () => {
      const result = PipelineConfigSchema.safeParse({});
      expect(result.success).toBe(true);
    });
  });

  describe("worktree_base", () => {
    it("accepts string values", () => {
      const result = PipelineConfigSchema.safeParse({
        worktree_base: ".worktrees",
      });
      expect(result.success).toBe(true);
    });

    it("accepts custom paths", () => {
      const result = PipelineConfigSchema.safeParse({
        worktree_base: "custom/trees",
      });
      expect(result.success).toBe(true);
    });

    it("is optional", () => {
      const result = PipelineConfigSchema.safeParse({});
      expect(result.success).toBe(true);
    });
  });

  describe("DEFAULT_CONFIG", () => {
    it("has max_concurrent defaulting to 1", () => {
      expect(DEFAULT_CONFIG.pipeline?.max_concurrent).toBe(1);
    });

    it("has worktree_base defaulting to .worktrees", () => {
      expect(DEFAULT_CONFIG.pipeline?.worktree_base).toBe(".worktrees");
    });
  });
});

describe("getConcurrentPipelineConfig", () => {
  const originalEnv = process.env;

  beforeEach(() => {
    process.env = { ...originalEnv };
  });

  afterEach(() => {
    process.env = originalEnv;
  });

  it("returns defaults when no config or env", async () => {
    // Lazy import to get the fresh module
    const { getConcurrentPipelineConfig } = await import("../../src/utils/incrediConfig");
    const config = getConcurrentPipelineConfig("/nonexistent");
    expect(config.maxConcurrent).toBe(3);
    expect(config.worktreeBase).toBe(".worktrees");
  });

  it("respects NIGHTGAUGE_PIPELINE_MAX_CONCURRENT env var", async () => {
    process.env.NIGHTGAUGE_PIPELINE_MAX_CONCURRENT = "5";
    const { getConcurrentPipelineConfig } = await import("../../src/utils/incrediConfig");
    const config = getConcurrentPipelineConfig("/nonexistent");
    expect(config.maxConcurrent).toBe(5);
  });

  it("ignores invalid env var values", async () => {
    process.env.NIGHTGAUGE_PIPELINE_MAX_CONCURRENT = "99";
    const { getConcurrentPipelineConfig } = await import("../../src/utils/incrediConfig");
    const config = getConcurrentPipelineConfig("/nonexistent");
    expect(config.maxConcurrent).toBe(3); // Falls back to default
  });
});

describe("parseConcurrencyWorkspaceMax — de-mock: real YAML string through parser", () => {
  // Drive the parser that feeds getConcurrentPipelineConfig directly from
  // YAML string fixtures, bypassing the file-read layer.

  it("returns workspace_max from a concurrency block", () => {
    const yaml = `
concurrency:
  workspace_max: 5
  per_repo_max: 2
`;
    expect(parseConcurrencyWorkspaceMax(yaml, 1, 10)).toBe(5);
  });

  it("returns undefined when concurrency block is absent", () => {
    const yaml = `
pipeline:
  ci_timeout: 120
`;
    expect(parseConcurrencyWorkspaceMax(yaml, 1, 10)).toBeUndefined();
  });

  it("returns undefined when workspace_max is out of range", () => {
    const yaml = `
concurrency:
  workspace_max: 99
`;
    expect(parseConcurrencyWorkspaceMax(yaml, 1, 10)).toBeUndefined();
  });

  it("stops scanning at the next top-level key after concurrency", () => {
    const yaml = `
concurrency:
  per_repo_max: 1
pipeline:
  workspace_max: 7
`;
    // workspace_max under `pipeline:` must NOT be read as concurrency.workspace_max
    expect(parseConcurrencyWorkspaceMax(yaml, 1, 10)).toBeUndefined();
  });
});
