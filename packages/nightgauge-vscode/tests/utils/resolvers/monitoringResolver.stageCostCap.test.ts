/**
 * monitoringResolver.stageCostCap.test.ts
 *
 * Tests for `getStageCostCapUsd` and `DEFAULT_STAGE_COST_CAPS` (Issue #3002).
 *
 * Three-source resolver: env var > config YAML > default constant.
 *  - Returns the configured USD cap for the stage.
 *  - Returns 0 when no cap is set ("uncapped").
 *  - Falls through to default when env/config values are missing or malformed.
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
  DEFAULT_STAGE_COST_CAPS,
  getStageCostCapUsd,
} from "../../../src/utils/resolvers/monitoringResolver";

describe("DEFAULT_STAGE_COST_CAPS (Issues #3002, #3208)", () => {
  // All defaults below are p95 × 2 (rounded to the nearest dollar) over the
  // last 90 days of `complete | cancelled` runs (Issue #3208 calibration,
  // 2026-05-06). See the comment block on `DEFAULT_STAGE_COST_CAPS` for the
  // full distribution and `scripts/audit-stage-cost-distribution.ts` for the
  // re-runnable audit.
  it("caps feature-dev at $23.00 (p95 $11.25 × 2, n=848)", () => {
    expect(DEFAULT_STAGE_COST_CAPS["feature-dev"]).toBe(23.0);
  });

  it("caps feature-planning at $6.00 (p95 $2.97 × 2, n=733)", () => {
    expect(DEFAULT_STAGE_COST_CAPS["feature-planning"]).toBe(6.0);
  });

  it("caps feature-validate at $7.00 (p95 $3.72 × 2, n=755)", () => {
    expect(DEFAULT_STAGE_COST_CAPS["feature-validate"]).toBe(7.0);
  });

  it("caps pr-create at $3.00 (p95 $1.56 × 2, n=828)", () => {
    expect(DEFAULT_STAGE_COST_CAPS["pr-create"]).toBe(3.0);
  });

  it("caps pr-merge at $4.00 (p95 $2.25 × 2, n=841)", () => {
    expect(DEFAULT_STAGE_COST_CAPS["pr-merge"]).toBe(4.0);
  });

  it("caps issue-pickup at $1.00 (p95 $0.68 × 2, n=561)", () => {
    expect(DEFAULT_STAGE_COST_CAPS["issue-pickup"]).toBe(1.0);
  });

  it("does not cap pipeline-start (no productive cost recorded)", () => {
    expect(DEFAULT_STAGE_COST_CAPS["pipeline-start"]).toBeUndefined();
  });
});

describe("getStageCostCapUsd — defaults", () => {
  const ENV_KEY = "NIGHTGAUGE_PIPELINE_STAGE_COST_CAP_FEATURE_DEV";
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

  it("returns the default for feature-dev when no env or config override", () => {
    expect(getStageCostCapUsd("feature-dev")).toBe(23.0);
  });

  it("returns the default for each pipeline stage that has one", () => {
    // After Issue #3208, every productive stage ships a default — see
    // DEFAULT_STAGE_COST_CAPS test above for the per-stage justification.
    expect(getStageCostCapUsd("issue-pickup")).toBe(1.0);
    expect(getStageCostCapUsd("feature-planning")).toBe(6.0);
    expect(getStageCostCapUsd("feature-validate")).toBe(7.0);
    expect(getStageCostCapUsd("pr-create")).toBe(3.0);
    expect(getStageCostCapUsd("pr-merge")).toBe(4.0);
  });

  it("returns 0 (uncapped) for stages with no default", () => {
    // pipeline-start is bookkeeping only and has no productive cost.
    expect(getStageCostCapUsd("pipeline-start")).toBe(0);
    expect(getStageCostCapUsd("nonexistent-stage")).toBe(0);
  });
});

describe("getStageCostCapUsd — environment variable override", () => {
  const ENV_KEY = "NIGHTGAUGE_PIPELINE_STAGE_COST_CAP_FEATURE_DEV";
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
  });

  it("honors env var override for feature-dev", () => {
    process.env[ENV_KEY] = "10.50";
    expect(getStageCostCapUsd("feature-dev")).toBe(10.5);
  });

  it("honors env var of 0 (explicit disable)", () => {
    process.env[ENV_KEY] = "0";
    expect(getStageCostCapUsd("feature-dev")).toBe(0);
  });

  it("ignores invalid env var values and falls back to default", () => {
    process.env[ENV_KEY] = "not-a-number";
    expect(getStageCostCapUsd("feature-dev")).toBe(23.0);
  });

  it("ignores negative env values and falls back to default", () => {
    process.env[ENV_KEY] = "-1.0";
    expect(getStageCostCapUsd("feature-dev")).toBe(23.0);
  });

  it("env var override works for stages with no default", () => {
    const k = "NIGHTGAUGE_PIPELINE_STAGE_COST_CAP_PR_CREATE";
    const originalK = process.env[k];
    try {
      process.env[k] = "0.50";
      expect(getStageCostCapUsd("pr-create")).toBe(0.5);
    } finally {
      if (originalK === undefined) delete process.env[k];
      else process.env[k] = originalK;
    }
  });

  it("converts kebab-case stage names to underscored uppercase env keys", () => {
    // 'feature-validate' -> NIGHTGAUGE_PIPELINE_STAGE_COST_CAP_FEATURE_VALIDATE
    const k = "NIGHTGAUGE_PIPELINE_STAGE_COST_CAP_FEATURE_VALIDATE";
    const originalK = process.env[k];
    try {
      process.env[k] = "2.50";
      expect(getStageCostCapUsd("feature-validate")).toBe(2.5);
    } finally {
      if (originalK === undefined) delete process.env[k];
      else process.env[k] = originalK;
    }
  });
});

describe("getStageCostCapUsd — config file", () => {
  let tmpRoot: string;

  beforeEach(() => {
    tmpRoot = fs.mkdtempSync(path.join(os.tmpdir(), "stagecostcap-"));
    fs.mkdirSync(path.join(tmpRoot, ".nightgauge"), { recursive: true });
    delete process.env.NIGHTGAUGE_PIPELINE_STAGE_COST_CAP_FEATURE_DEV;
    delete process.env.NIGHTGAUGE_PIPELINE_STAGE_COST_CAP_PR_CREATE;
  });

  afterEach(() => {
    fs.rmSync(tmpRoot, { recursive: true, force: true });
  });

  it("reads cap from .nightgauge/config.yaml", () => {
    const yaml = `pipeline:
  stage_cost_caps:
    feature-dev: 7.50
    pr-create: 1.00
`;
    fs.writeFileSync(path.join(tmpRoot, ".nightgauge", "config.yaml"), yaml);

    expect(getStageCostCapUsd("feature-dev", tmpRoot)).toBe(7.5);
    expect(getStageCostCapUsd("pr-create", tmpRoot)).toBe(1.0);
  });

  it("falls back to default when stage missing from config block", () => {
    const yaml = `pipeline:
  stage_cost_caps:
    pr-create: 1.00
`;
    fs.writeFileSync(path.join(tmpRoot, ".nightgauge", "config.yaml"), yaml);

    // pr-create override applied; feature-dev/pr-merge fall through to defaults
    expect(getStageCostCapUsd("pr-create", tmpRoot)).toBe(1.0);
    expect(getStageCostCapUsd("feature-dev", tmpRoot)).toBe(23.0);
    expect(getStageCostCapUsd("pr-merge", tmpRoot)).toBe(4.0);
    // pipeline-start has no default and no config entry → uncapped
    expect(getStageCostCapUsd("pipeline-start", tmpRoot)).toBe(0);
  });

  it("env var beats config file", () => {
    const yaml = `pipeline:
  stage_cost_caps:
    feature-dev: 7.50
`;
    fs.writeFileSync(path.join(tmpRoot, ".nightgauge", "config.yaml"), yaml);

    process.env.NIGHTGAUGE_PIPELINE_STAGE_COST_CAP_FEATURE_DEV = "12.00";
    try {
      expect(getStageCostCapUsd("feature-dev", tmpRoot)).toBe(12.0);
    } finally {
      delete process.env.NIGHTGAUGE_PIPELINE_STAGE_COST_CAP_FEATURE_DEV;
    }
  });

  it("explicit 0 in config disables the cap", () => {
    const yaml = `pipeline:
  stage_cost_caps:
    feature-dev: 0
`;
    fs.writeFileSync(path.join(tmpRoot, ".nightgauge", "config.yaml"), yaml);

    expect(getStageCostCapUsd("feature-dev", tmpRoot)).toBe(0);
  });

  it("ignores non-stage_cost_caps subsections", () => {
    // Make sure we don't accidentally match keys from neighboring subsections
    // (e.g. stage_hard_caps which has the same shape).
    const yaml = `pipeline:
  stage_hard_caps:
    feature-dev: 600
  stall_kill_multiplier: 8
`;
    fs.writeFileSync(path.join(tmpRoot, ".nightgauge", "config.yaml"), yaml);

    // No stage_cost_caps key — should fall back to the defaults
    expect(getStageCostCapUsd("feature-dev", tmpRoot)).toBe(23.0);
    expect(getStageCostCapUsd("pr-create", tmpRoot)).toBe(3.0);
    expect(getStageCostCapUsd("pipeline-start", tmpRoot)).toBe(0);
  });

  it("returns the default when config file is missing", () => {
    expect(getStageCostCapUsd("feature-dev", tmpRoot)).toBe(23.0);
    expect(getStageCostCapUsd("pr-create", tmpRoot)).toBe(3.0);
    expect(getStageCostCapUsd("pipeline-start", tmpRoot)).toBe(0);
  });
});
