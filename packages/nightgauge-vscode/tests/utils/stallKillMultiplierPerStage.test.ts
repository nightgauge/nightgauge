/**
 * Tests for the per-stage stall_kill_multiplier resolution introduced in
 * #3020. Original feature-validate kill window was 40 min (300s × 8) — the
 * incident burned $18.96 spinning before the kill fired. Per-stage default
 * of 4 brings feature-validate down to 20 min while leaving other stages
 * untouched.
 */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import * as fs from "node:fs";

vi.mock("vscode", () => ({
  workspace: {
    workspaceFolders: [{ uri: { fsPath: "/test/workspace" } }],
  },
}));

vi.mock("node:fs", () => ({
  existsSync: vi.fn(),
  readFileSync: vi.fn(),
}));

vi.mock("../../src/utils/configPathResolver", () => ({
  resolveConfigPathSync: vi.fn(),
  logDeprecationWarning: vi.fn(),
}));

import { resolveConfigPathSync } from "../../src/utils/configPathResolver";
import { getStallKillMultiplier } from "../../src/utils/resolvers/monitoringResolver";

beforeEach(() => {
  vi.clearAllMocks();
  delete process.env.NIGHTGAUGE_PIPELINE_STALL_KILL_MULTIPLIER;
  delete process.env.NIGHTGAUGE_PIPELINE_STALL_KILL_MULTIPLIER_FEATURE_VALIDATE;
});

afterEach(() => {
  delete process.env.NIGHTGAUGE_PIPELINE_STALL_KILL_MULTIPLIER;
  delete process.env.NIGHTGAUGE_PIPELINE_STALL_KILL_MULTIPLIER_FEATURE_VALIDATE;
});

describe("getStallKillMultiplier — per-stage resolution (#3020)", () => {
  it("global default is 8 when no stage is provided", () => {
    vi.mocked(resolveConfigPathSync).mockReturnValue({
      path: "/test/workspace/.nightgauge/config.yaml",
      exists: false,
      isLegacy: false,
    });
    expect(getStallKillMultiplier("/test/workspace")).toBe(8);
  });

  it("feature-validate default is 4 (per-stage override)", () => {
    vi.mocked(resolveConfigPathSync).mockReturnValue({
      path: "/test/workspace/.nightgauge/config.yaml",
      exists: false,
      isLegacy: false,
    });
    expect(getStallKillMultiplier("/test/workspace", "feature-validate")).toBe(4);
  });

  it("pr-create default is 4 (per-stage override — follow-up to #291)", () => {
    vi.mocked(resolveConfigPathSync).mockReturnValue({
      path: "/test/workspace/.nightgauge/config.yaml",
      exists: false,
      isLegacy: false,
    });
    expect(getStallKillMultiplier("/test/workspace", "pr-create")).toBe(4);
  });

  it("non-overridden stages still use the global default of 8", () => {
    vi.mocked(resolveConfigPathSync).mockReturnValue({
      path: "/test/workspace/.nightgauge/config.yaml",
      exists: false,
      isLegacy: false,
    });
    expect(getStallKillMultiplier("/test/workspace", "feature-dev")).toBe(8);
    expect(getStallKillMultiplier("/test/workspace", "pr-merge")).toBe(8);
    expect(getStallKillMultiplier("/test/workspace", "feature-planning")).toBe(8);
    expect(getStallKillMultiplier("/test/workspace", "issue-pickup")).toBe(8);
  });

  it("per-stage env var beats per-stage default and global env", () => {
    process.env.NIGHTGAUGE_PIPELINE_STALL_KILL_MULTIPLIER = "10";
    process.env.NIGHTGAUGE_PIPELINE_STALL_KILL_MULTIPLIER_FEATURE_VALIDATE = "2";
    vi.mocked(resolveConfigPathSync).mockReturnValue({
      path: "/test/workspace/.nightgauge/config.yaml",
      exists: false,
      isLegacy: false,
    });
    expect(getStallKillMultiplier("/test/workspace", "feature-validate")).toBe(2);
    expect(getStallKillMultiplier("/test/workspace", "feature-dev")).toBe(10);
  });

  it("per-stage YAML override wins over per-stage default and global YAML", () => {
    vi.mocked(resolveConfigPathSync).mockReturnValue({
      path: "/test/workspace/.nightgauge/config.yaml",
      exists: true,
      isLegacy: false,
    });
    vi.mocked(fs.readFileSync).mockReturnValue(`
pipeline:
  stall_kill_multiplier: 6
  stall_kill_multipliers:
    feature-validate: 2
`);
    expect(getStallKillMultiplier("/test/workspace", "feature-validate")).toBe(2);
    expect(getStallKillMultiplier("/test/workspace", "feature-dev")).toBe(6);
  });

  it("global YAML overrides per-stage default for unlisted stages", () => {
    vi.mocked(resolveConfigPathSync).mockReturnValue({
      path: "/test/workspace/.nightgauge/config.yaml",
      exists: true,
      isLegacy: false,
    });
    vi.mocked(fs.readFileSync).mockReturnValue(`
pipeline:
  stall_kill_multiplier: 6
`);
    // feature-validate has a per-stage default but no per-stage YAML override —
    // global YAML wins (6) over per-stage default (4).
    expect(getStallKillMultiplier("/test/workspace", "feature-validate")).toBe(6);
  });

  it("global env var still wins over per-stage YAML and per-stage default (when no per-stage env)", () => {
    process.env.NIGHTGAUGE_PIPELINE_STALL_KILL_MULTIPLIER = "10";
    vi.mocked(resolveConfigPathSync).mockReturnValue({
      path: "/test/workspace/.nightgauge/config.yaml",
      exists: true,
      isLegacy: false,
    });
    vi.mocked(fs.readFileSync).mockReturnValue(`
pipeline:
  stall_kill_multipliers:
    feature-validate: 2
`);
    // No per-stage env, so global env (10) trumps both YAML and per-stage default.
    expect(getStallKillMultiplier("/test/workspace", "feature-validate")).toBe(10);
  });
});

// #2378: the global multiplier is pipeline.stall_kill_multiplier itself. The
// same key nested under performance_mode.overrides.maximum or the legacy
// supercharge block, as CONFIGURATION.md's example once suggested, used to be
// read as the global value for every stage in every mode.
describe("getStallKillMultiplier — only pipeline's own key is global (#2378)", () => {
  function config(yaml: string): void {
    vi.mocked(resolveConfigPathSync).mockReturnValue({
      path: "/test/workspace/.nightgauge/config.yaml",
      exists: true,
      isLegacy: false,
    });
    vi.mocked(fs.readFileSync).mockReturnValue(yaml);
  }

  it.each([
    [
      "performance_mode.overrides.maximum",
      `
pipeline:
  performance_mode:
    overrides:
      maximum:
        stall_kill_multiplier: 10
`,
    ],
    [
      "supercharge",
      `
pipeline:
  supercharge:
    stall_kill_multiplier: 10
`,
    ],
  ])("ignores a stall_kill_multiplier nested under %s", (_where, yaml) => {
    config(yaml);
    expect(getStallKillMultiplier("/test/workspace", "feature-dev")).toBe(8);
    expect(getStallKillMultiplier("/test/workspace", "feature-validate")).toBe(4);
  });

  it("still reads pipeline.stall_kill_multiplier beside a nested one, at any indentation", () => {
    config(`
pipeline:
    supercharge:
        stall_kill_multiplier: 10
    stall_kill_multiplier: 6
`);
    expect(getStallKillMultiplier("/test/workspace", "feature-dev")).toBe(6);
  });
});
