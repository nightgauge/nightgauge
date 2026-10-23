/**
 * monitoringResolver.stageHardCap.test.ts
 *
 * Regression tests for the pr-create hard cap removal (Issue #2982).
 *
 * Before #2982, DEFAULT_STAGE_HARD_CAPS["pr-create"] = 300 capped pr-create at
 * 5 minutes regardless of the calibrated stall-kill threshold. This negated
 * #2973's fix: opus/supercharge pr-create runs on large PRs were still
 * force-killed at 5 min even though pr-create legitimately has silent windows
 * during `gh pr create` / `gh pr comment` / label edits.
 *
 * #2982 removed the default entry. The calibrated stall-kill (~35 min) is now
 * the sole authority for pr-create. Users can still override via env var or
 * `pipeline.stage_hard_caps.<stage>` in config.yaml.
 */

import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";

vi.mock("vscode", () => ({
  workspace: {
    workspaceFolders: undefined,
  },
}));

import {
  DEFAULT_STAGE_HARD_CAPS,
  getStageHardCapMs,
} from "../../../src/utils/resolvers/monitoringResolver";

describe("DEFAULT_STAGE_HARD_CAPS (Issue #2982)", () => {
  it("does not set a default hard cap for pr-create", () => {
    // Before #2982 this was 300 (5 minutes), which killed opus/supercharge
    // pr-create runs on large PRs even though the run was healthy.
    expect(DEFAULT_STAGE_HARD_CAPS["pr-create"]).toBeUndefined();
  });

  it("only sets the progress-gated feature-dev backstop (#3851)", () => {
    // #2982 emptied this map; #3851 re-added a single GENEROUS, progress-gated
    // feature-dev backstop (90 min). It is NOT a blunt elapsed kill — skillRunner
    // only acts on it when there is ALSO no productive progress in the window.
    expect(Object.keys(DEFAULT_STAGE_HARD_CAPS)).toEqual(["feature-dev"]);
    expect(DEFAULT_STAGE_HARD_CAPS["feature-dev"]).toBe(5400);
  });
});

describe("getStageHardCapMs", () => {
  const ENV_KEY = "NIGHTGAUGE_PIPELINE_STAGE_HARD_CAP_PR_CREATE";
  const originalEnv = process.env[ENV_KEY];

  beforeEach(() => {
    vi.clearAllMocks();
    delete process.env[ENV_KEY];
  });

  afterEach(() => {
    if (originalEnv === undefined) {
      delete process.env[ENV_KEY];
    } else {
      process.env[ENV_KEY] = originalEnv;
    }
    vi.resetAllMocks();
  });

  it("returns 0 for pr-create when no env var and no config override", () => {
    // With #2982's removal of the default, the function must return 0
    // (no hard cap) when nothing is configured. skillRunner.ts interprets
    // 0 as "no hard cap applied" and falls back to calibrated stall-kill.
    expect(getStageHardCapMs("pr-create")).toBe(0);
  });

  it("returns 0 for stages with no default, config, or env override", () => {
    expect(getStageHardCapMs("pr-merge")).toBe(0);
    expect(getStageHardCapMs("issue-pickup")).toBe(0);
  });

  it("returns the 90-min progress-gated backstop for feature-dev (#3851)", () => {
    // 5400s default → 5_400_000 ms. skillRunner gates this on no productive
    // progress so a healthy long run is never blunt-killed.
    expect(getStageHardCapMs("feature-dev")).toBe(5_400_000);
  });

  it("still honors env var override for pr-create", () => {
    process.env[ENV_KEY] = "600"; // 10 min in seconds
    expect(getStageHardCapMs("pr-create")).toBe(600_000);
  });

  it("honors env var override of 0 (explicit disable)", () => {
    process.env[ENV_KEY] = "0";
    expect(getStageHardCapMs("pr-create")).toBe(0);
  });

  it("ignores invalid env var values and falls back to default (0)", () => {
    process.env[ENV_KEY] = "not-a-number";
    expect(getStageHardCapMs("pr-create")).toBe(0);
  });

  it("env var remains the fast override path after #2982 default removal", () => {
    // When DEFAULT_STAGE_HARD_CAPS["pr-create"] was 300, env vars could lower
    // or raise it. With the default removed, env vars remain the fast path for
    // users who want an explicit cap without editing config.yaml.
    process.env[ENV_KEY] = "1800"; // 30 min
    expect(getStageHardCapMs("pr-create")).toBe(1800_000);
  });
});
