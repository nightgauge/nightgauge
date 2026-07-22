/**
 * monitoringResolver.costCapStageTimeCap.test.ts
 *
 * Tests for the per-stage time-cap knob (Issue #3229).
 *
 * `getStageTimeCapMs` is the time-based fallback used when a provider's
 * `cost_cap_provider_scale` fires the `0` sentinel (lm-studio, ollama).
 * Computing per-stage defaults from `p95(elapsed) × 1.5` over historical
 * data is intentionally out-of-scope for #3229 (per AC #4) — these
 * tests pin only the wired-knob behavior:
 *   - default 0 (uncapped) until operator opts in
 *   - env-var precedence
 *   - file-config precedence via line-by-line YAML scanner
 *   - returns ms (input is seconds)
 */

import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import * as fs from "node:fs";
import * as os from "node:os";
import * as path from "node:path";

vi.mock("vscode", () => ({
  workspace: {
    workspaceFolders: undefined,
  },
}));

import {
  DEFAULT_STAGE_TIME_CAPS,
  getStageTimeCapMs,
} from "../../../src/utils/resolvers/monitoringResolver";

const TIME_CAP_ENV_KEYS = [
  "NIGHTGAUGE_PIPELINE_STAGE_TIME_CAP_FEATURE_DEV",
  "NIGHTGAUGE_PIPELINE_STAGE_TIME_CAP_PR_CREATE",
  "NIGHTGAUGE_PIPELINE_STAGE_TIME_CAP_FEATURE_PLANNING",
];

beforeEach(() => {
  for (const k of TIME_CAP_ENV_KEYS) delete process.env[k];
});

afterEach(() => {
  for (const k of TIME_CAP_ENV_KEYS) delete process.env[k];
});

describe("DEFAULT_STAGE_TIME_CAPS table", () => {
  it("is empty by default — defaults computed by separate audit (AC #4)", () => {
    // Until p95(elapsed) × 1.5 is computed by a follow-up audit, the
    // table is intentionally empty so lm-studio / ollama only get a
    // time cap when the operator opts in via config / env.
    expect(Object.keys(DEFAULT_STAGE_TIME_CAPS).length).toBe(0);
  });
});

describe("getStageTimeCapMs — defaults", () => {
  it("returns 0 (uncapped) when no env / config is set", () => {
    expect(getStageTimeCapMs("feature-dev")).toBe(0);
    expect(getStageTimeCapMs("pr-create")).toBe(0);
    expect(getStageTimeCapMs("issue-pickup")).toBe(0);
  });
});

describe("getStageTimeCapMs — env-var overrides", () => {
  it("NIGHTGAUGE_PIPELINE_STAGE_TIME_CAP_FEATURE_DEV=900 returns 900_000 ms", () => {
    process.env.NIGHTGAUGE_PIPELINE_STAGE_TIME_CAP_FEATURE_DEV = "900";
    expect(getStageTimeCapMs("feature-dev")).toBe(900_000);
  });

  it("hyphenated stages map correctly: PR_CREATE → pr-create", () => {
    process.env.NIGHTGAUGE_PIPELINE_STAGE_TIME_CAP_PR_CREATE = "300";
    expect(getStageTimeCapMs("pr-create")).toBe(300_000);
  });

  it("ignores non-numeric env values", () => {
    process.env.NIGHTGAUGE_PIPELINE_STAGE_TIME_CAP_FEATURE_DEV = "junk";
    expect(getStageTimeCapMs("feature-dev")).toBe(0);
  });

  it("ignores negative env values (only 0 and positives are valid)", () => {
    process.env.NIGHTGAUGE_PIPELINE_STAGE_TIME_CAP_FEATURE_DEV = "-500";
    expect(getStageTimeCapMs("feature-dev")).toBe(0);
  });

  it("env override for one stage does not leak to other stages", () => {
    process.env.NIGHTGAUGE_PIPELINE_STAGE_TIME_CAP_FEATURE_DEV = "1800";
    expect(getStageTimeCapMs("feature-dev")).toBe(1_800_000);
    expect(getStageTimeCapMs("pr-create")).toBe(0);
  });
});

describe("getStageTimeCapMs — file-config overrides", () => {
  let tmpDir: string;

  beforeEach(() => {
    tmpDir = fs.mkdtempSync(path.join(os.tmpdir(), "ib-3229-time-"));
    fs.mkdirSync(path.join(tmpDir, ".nightgauge"), { recursive: true });
  });

  afterEach(() => {
    fs.rmSync(tmpDir, { recursive: true, force: true });
  });

  it("pipeline.stage_time_caps.feature-dev: 1800 returns 1_800_000 ms", () => {
    const cfg = `pipeline:
  stage_time_caps:
    feature-dev: 1800
    pr-create: 600
`;
    fs.writeFileSync(path.join(tmpDir, ".nightgauge", "config.yaml"), cfg);
    expect(getStageTimeCapMs("feature-dev", tmpDir)).toBe(1_800_000);
    expect(getStageTimeCapMs("pr-create", tmpDir)).toBe(600_000);
  });

  it("env override beats config file", () => {
    const cfg = `pipeline:
  stage_time_caps:
    feature-dev: 1800
`;
    fs.writeFileSync(path.join(tmpDir, ".nightgauge", "config.yaml"), cfg);
    process.env.NIGHTGAUGE_PIPELINE_STAGE_TIME_CAP_FEATURE_DEV = "300";
    expect(getStageTimeCapMs("feature-dev", tmpDir)).toBe(300_000);
  });

  it("does not bleed into adjacent pipeline subsections", () => {
    const cfg = `pipeline:
  stage_time_caps:
    feature-dev: 1800
  stage_hard_caps:
    feature-dev: 600
`;
    fs.writeFileSync(path.join(tmpDir, ".nightgauge", "config.yaml"), cfg);
    // The time-cap parser should stop at `stage_hard_caps:` and only
    // surface its own block's value.
    expect(getStageTimeCapMs("feature-dev", tmpDir)).toBe(1_800_000);
  });

  it("returns 0 for stages not listed in the config block", () => {
    const cfg = `pipeline:
  stage_time_caps:
    feature-dev: 1800
`;
    fs.writeFileSync(path.join(tmpDir, ".nightgauge", "config.yaml"), cfg);
    expect(getStageTimeCapMs("pr-create", tmpDir)).toBe(0);
  });
});
