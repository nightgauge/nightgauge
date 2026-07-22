/**
 * otherResolver.maxConcurrent.test.ts
 *
 * Tests for `getConcurrentPipelineConfig` and `parseMaxConcurrentBlocks`
 * (Issue #3195). Pin the unified-slot-ceiling resolution semantics:
 *   1. env var NIGHTGAUGE_PIPELINE_MAX_CONCURRENT
 *   2. pipeline.max_concurrent (source of truth)
 *   3. autonomous.max_concurrent (deprecated legacy fallback, logs once)
 *   4. default 3
 *
 * Also covers the regression — a config that previously diverged
 * (`pipeline: 3 / autonomous: 1`) must still resolve to the pipeline value
 * for both drag-to-pipeline and queue auto-start, so dragging two issues with
 * `pipeline.max_concurrent: 1` only fills one slot.
 */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import * as fs from "node:fs";
import * as os from "node:os";
import * as path from "node:path";

vi.mock("vscode", () => ({
  workspace: {
    workspaceFolders: undefined,
  },
}));

import {
  getConcurrentPipelineConfig,
  parseMaxConcurrentBlocks,
} from "../../../src/utils/resolvers/otherResolver";

function withTempConfig(yaml: string): string {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), "incredi-mc-"));
  fs.mkdirSync(path.join(dir, ".nightgauge"), { recursive: true });
  fs.writeFileSync(path.join(dir, ".nightgauge", "config.yaml"), yaml, "utf-8");
  return dir;
}

describe("parseMaxConcurrentBlocks", () => {
  it("extracts pipeline.max_concurrent and worktree_base", () => {
    const result = parseMaxConcurrentBlocks(
      `pipeline:\n  max_concurrent: 4\n  worktree_base: .wt\n`,
      1,
      10
    );
    expect(result.pipelineMaxConcurrent).toBe(4);
    expect(result.worktreeBase).toBe(".wt");
    expect(result.autonomousMaxConcurrent).toBeUndefined();
  });

  it("extracts autonomous.max_concurrent", () => {
    const result = parseMaxConcurrentBlocks(`autonomous:\n  max_concurrent: 2\n`, 1, 10);
    expect(result.autonomousMaxConcurrent).toBe(2);
    expect(result.pipelineMaxConcurrent).toBeUndefined();
  });

  it("captures both blocks independently", () => {
    const result = parseMaxConcurrentBlocks(
      `pipeline:\n  max_concurrent: 5\nautonomous:\n  max_concurrent: 1\n`,
      1,
      10
    );
    expect(result.pipelineMaxConcurrent).toBe(5);
    expect(result.autonomousMaxConcurrent).toBe(1);
  });

  it("ignores nested keys at depth > 2 spaces", () => {
    const result = parseMaxConcurrentBlocks(
      `pipeline:\n  context_schema_repair:\n    max_attempts: 1\n  max_concurrent: 6\n`,
      1,
      10
    );
    expect(result.pipelineMaxConcurrent).toBe(6);
  });

  it("rejects out-of-range values", () => {
    const result = parseMaxConcurrentBlocks(
      `pipeline:\n  max_concurrent: 99\nautonomous:\n  max_concurrent: 0\n`,
      1,
      10
    );
    expect(result.pipelineMaxConcurrent).toBeUndefined();
    expect(result.autonomousMaxConcurrent).toBeUndefined();
  });

  it("does not confuse refinement_max_concurrent with max_concurrent", () => {
    const result = parseMaxConcurrentBlocks(`autonomous:\n  refinement_max_concurrent: 2\n`, 1, 10);
    expect(result.autonomousMaxConcurrent).toBeUndefined();
  });
});

describe("getConcurrentPipelineConfig", () => {
  let warnSpy: ReturnType<typeof vi.spyOn>;

  beforeEach(() => {
    delete process.env.NIGHTGAUGE_PIPELINE_MAX_CONCURRENT;
    warnSpy = vi.spyOn(console, "warn").mockImplementation(() => {});
  });

  afterEach(() => {
    warnSpy.mockRestore();
  });

  it("returns the default when no config and no env", () => {
    const result = getConcurrentPipelineConfig("/nonexistent/path");
    expect(result.maxConcurrent).toBe(3);
  });

  it("reads concurrency.workspace_max when set (#3781)", () => {
    const dir = withTempConfig(`concurrency:\n  workspace_max: 4\n`);
    const result = getConcurrentPipelineConfig(dir);
    expect(result.maxConcurrent).toBe(4);
    expect(warnSpy).not.toHaveBeenCalled();
  });

  // Regression: workspace_max=1 means dragging two issues only fills one slot.
  it("returns 1 from concurrency.workspace_max so drag-to-pipeline only fills one slot", () => {
    const dir = withTempConfig(`concurrency:\n  workspace_max: 1\n`);
    const result = getConcurrentPipelineConfig(dir);
    expect(result.maxConcurrent).toBe(1);
  });

  it("env var overrides the config block", () => {
    process.env.NIGHTGAUGE_PIPELINE_MAX_CONCURRENT = "7";
    const dir = withTempConfig(`concurrency:\n  workspace_max: 1\n`);
    const result = getConcurrentPipelineConfig(dir);
    expect(result.maxConcurrent).toBe(7);
    expect(warnSpy).not.toHaveBeenCalled();
  });

  it("ignores invalid env values and uses concurrency.workspace_max", () => {
    process.env.NIGHTGAUGE_PIPELINE_MAX_CONCURRENT = "not-a-number";
    const dir = withTempConfig(`concurrency:\n  workspace_max: 4\n`);
    const result = getConcurrentPipelineConfig(dir);
    expect(result.maxConcurrent).toBe(4);
  });

  it("preserves worktree_base from pipeline block", () => {
    const dir = withTempConfig(`pipeline:\n  worktree_base: .custom-wt\n`);
    const result = getConcurrentPipelineConfig(dir);
    expect(result.worktreeBase).toBe(".custom-wt");
  });
});
