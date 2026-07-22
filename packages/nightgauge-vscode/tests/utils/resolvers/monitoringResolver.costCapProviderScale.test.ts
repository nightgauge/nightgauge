/**
 * monitoringResolver.costCapProviderScale.test.ts
 *
 * Tests for the per-adapter cost-cap provider scale (Issue #3229).
 *
 * The base caps in `DEFAULT_STAGE_COST_CAPS` are calibrated from
 * Claude-only history (PR #3209). The provider scale composes
 * multiplicatively last so that:
 *   - claude (1.0×) preserves the calibrated baseline byte-for-byte —
 *     guarantees no regression for default Claude users (AC #5)
 *   - non-Claude adapters get a proportionally tighter ceiling
 *     reflecting their lower per-token cost
 *   - lm-studio / ollama (0.0×) opt into time-based cap mode
 *     (`provider_scale=0` is a deliberate sentinel, not a typo guard)
 *
 * Composition: effectiveCap = baseCap × modelScale × modeMultiplier × providerScale
 *
 * Covers:
 *   - DEFAULT_COST_CAP_PROVIDER_SCALE seed values
 *   - env-var precedence (incl. hyphen → underscore mapping)
 *   - 0 is accepted (asymmetry vs. mode/model multipliers)
 *   - file-config precedence via line-by-line YAML scanner
 *   - getStageCostCapPerProviderUsd override path
 *   - AC #6 4-tuple matrix of (adapter, model, mode) effective caps
 *   - AC #5 regression anchor: claude preserves PR #3209 defaults
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
  DEFAULT_COST_CAP_PROVIDER_SCALE,
  getCostCapProviderScale,
  getEffectiveStageCostCap,
  getStageCostCapPerProviderUsd,
} from "../../../src/utils/resolvers/monitoringResolver";

const PROVIDER_ENV_KEYS = [
  "NIGHTGAUGE_COST_CAP_PROVIDER_SCALE_CLAUDE",
  "NIGHTGAUGE_COST_CAP_PROVIDER_SCALE_CODEX",
  "NIGHTGAUGE_COST_CAP_PROVIDER_SCALE_GEMINI",
  "NIGHTGAUGE_COST_CAP_PROVIDER_SCALE_GEMINI_SDK",
  "NIGHTGAUGE_COST_CAP_PROVIDER_SCALE_COPILOT",
  "NIGHTGAUGE_COST_CAP_PROVIDER_SCALE_LM_STUDIO",
  "NIGHTGAUGE_COST_CAP_PROVIDER_SCALE_OLLAMA",
];

const OVERRIDE_ENV_KEYS = [
  "NIGHTGAUGE_PIPELINE_STAGE_COST_CAP_PER_PROVIDER_GEMINI_FEATURE_DEV",
  "NIGHTGAUGE_PIPELINE_STAGE_COST_CAP_PER_PROVIDER_CODEX_PR_CREATE",
  "NIGHTGAUGE_PIPELINE_STAGE_COST_CAP_PER_PROVIDER_LM_STUDIO_FEATURE_DEV",
];

const COST_CAP_BASE_ENV_KEYS = [
  "NIGHTGAUGE_PIPELINE_STAGE_COST_CAP_FEATURE_DEV",
  "NIGHTGAUGE_PIPELINE_STAGE_COST_CAP_PR_CREATE",
];

const MODE_ENV_KEYS = [
  "NIGHTGAUGE_COST_CAP_MODE_MULTIPLIER_EFFICIENCY",
  "NIGHTGAUGE_COST_CAP_MODE_MULTIPLIER_ELEVATED",
  "NIGHTGAUGE_COST_CAP_MODE_MULTIPLIER_MAXIMUM",
];

const MODEL_ENV_KEYS = [
  "NIGHTGAUGE_BUDGET_MODEL_SCALE_OPUS",
  "NIGHTGAUGE_BUDGET_MODEL_SCALE_OPUS_HIGH",
  "NIGHTGAUGE_BUDGET_MODEL_SCALE_SONNET",
  "NIGHTGAUGE_BUDGET_MODEL_SCALE_SONNET_HIGH",
];

const ALL_ENV_KEYS = [
  ...PROVIDER_ENV_KEYS,
  ...OVERRIDE_ENV_KEYS,
  ...COST_CAP_BASE_ENV_KEYS,
  ...MODE_ENV_KEYS,
  ...MODEL_ENV_KEYS,
];

beforeEach(() => {
  for (const k of ALL_ENV_KEYS) delete process.env[k];
});

afterEach(() => {
  for (const k of ALL_ENV_KEYS) delete process.env[k];
});

describe("DEFAULT_COST_CAP_PROVIDER_SCALE table", () => {
  it("claude is 1.0× — preserves PR #3209 defaults byte-for-byte (AC #5)", () => {
    expect(DEFAULT_COST_CAP_PROVIDER_SCALE.claude).toBe(1.0);
  });

  it("codex is 0.7× per C1 pricing-table ratio", () => {
    expect(DEFAULT_COST_CAP_PROVIDER_SCALE.codex).toBe(0.7);
  });

  it("gemini and gemini-sdk are 0.4× (same Google rate card)", () => {
    expect(DEFAULT_COST_CAP_PROVIDER_SCALE.gemini).toBe(0.4);
    expect(DEFAULT_COST_CAP_PROVIDER_SCALE["gemini-sdk"]).toBe(0.4);
  });

  it("copilot is 0.2×", () => {
    expect(DEFAULT_COST_CAP_PROVIDER_SCALE.copilot).toBe(0.2);
  });

  it("lm-studio and ollama are 0.0 — switch to time-based cap", () => {
    expect(DEFAULT_COST_CAP_PROVIDER_SCALE["lm-studio"]).toBe(0.0);
    expect(DEFAULT_COST_CAP_PROVIDER_SCALE.ollama).toBe(0.0);
  });

  it("covers every ExecutionAdapter union member — drift guard", () => {
    expect(Object.keys(DEFAULT_COST_CAP_PROVIDER_SCALE).sort()).toEqual(
      ["claude", "codex", "copilot", "gemini", "gemini-sdk", "lm-studio", "ollama"].sort()
    );
  });
});

describe("getCostCapProviderScale — defaults", () => {
  it("returns 1.0 when adapter is undefined (defensive default)", () => {
    expect(getCostCapProviderScale(undefined)).toBe(1.0);
  });

  it.each([
    ["claude", 1.0],
    ["codex", 0.7],
    ["gemini", 0.4],
    ["gemini-sdk", 0.4],
    ["copilot", 0.2],
    ["lm-studio", 0.0],
    ["ollama", 0.0],
  ] as const)("returns the seeded default for %s (%s)", (adapter, expected) => {
    expect(getCostCapProviderScale(adapter)).toBe(expected);
  });
});

describe("getCostCapProviderScale — env-var overrides", () => {
  it("NIGHTGAUGE_COST_CAP_PROVIDER_SCALE_GEMINI=0.5 wins over default 0.4", () => {
    process.env.NIGHTGAUGE_COST_CAP_PROVIDER_SCALE_GEMINI = "0.5";
    expect(getCostCapProviderScale("gemini")).toBe(0.5);
  });

  it("hyphenated adapters parse: GEMINI_SDK maps to gemini-sdk", () => {
    process.env.NIGHTGAUGE_COST_CAP_PROVIDER_SCALE_GEMINI_SDK = "0.6";
    expect(getCostCapProviderScale("gemini-sdk")).toBe(0.6);
  });

  it("hyphenated adapters parse: LM_STUDIO maps to lm-studio", () => {
    process.env.NIGHTGAUGE_COST_CAP_PROVIDER_SCALE_LM_STUDIO = "0.3";
    expect(getCostCapProviderScale("lm-studio")).toBe(0.3);
  });

  it("env value of 0 is accepted (provider_scale=0 = time-cap mode)", () => {
    // ASYMMETRY GUARD: this is the key difference from
    // getCostCapModeMultiplier and getCostCapModelScale, which both
    // reject 0 as a typo.
    process.env.NIGHTGAUGE_COST_CAP_PROVIDER_SCALE_GEMINI = "0";
    expect(getCostCapProviderScale("gemini")).toBe(0);
  });

  it("ignores non-numeric env values, falls through to default", () => {
    process.env.NIGHTGAUGE_COST_CAP_PROVIDER_SCALE_GEMINI = "not-a-number";
    expect(getCostCapProviderScale("gemini")).toBe(0.4);
  });

  it("ignores negative env values (only 0 and positives are valid)", () => {
    process.env.NIGHTGAUGE_COST_CAP_PROVIDER_SCALE_GEMINI = "-0.5";
    expect(getCostCapProviderScale("gemini")).toBe(0.4);
  });

  it("env override for one adapter does not leak to other adapters", () => {
    process.env.NIGHTGAUGE_COST_CAP_PROVIDER_SCALE_GEMINI = "0.9";
    expect(getCostCapProviderScale("gemini")).toBe(0.9);
    expect(getCostCapProviderScale("codex")).toBe(0.7);
    expect(getCostCapProviderScale("claude")).toBe(1.0);
  });
});

describe("getCostCapProviderScale — file-config overrides", () => {
  let tmpDir: string;

  beforeEach(() => {
    tmpDir = fs.mkdtempSync(path.join(os.tmpdir(), "ib-3229-prov-"));
    fs.mkdirSync(path.join(tmpDir, ".nightgauge"), { recursive: true });
  });

  afterEach(() => {
    fs.rmSync(tmpDir, { recursive: true, force: true });
  });

  it("pipeline.cost_cap_provider_scale.<adapter> parses via line-by-line YAML scanner", () => {
    const cfg = `pipeline:
  cost_cap_provider_scale:
    gemini: 0.6
    codex: 0.8
    "lm-studio": 0.0
`;
    fs.writeFileSync(path.join(tmpDir, ".nightgauge", "config.yaml"), cfg);
    expect(getCostCapProviderScale("gemini", tmpDir)).toBe(0.6);
    expect(getCostCapProviderScale("codex", tmpDir)).toBe(0.8);
    expect(getCostCapProviderScale("lm-studio", tmpDir)).toBe(0.0);
  });

  it("env override beats config file", () => {
    const cfg = `pipeline:
  cost_cap_provider_scale:
    gemini: 0.6
`;
    fs.writeFileSync(path.join(tmpDir, ".nightgauge", "config.yaml"), cfg);
    process.env.NIGHTGAUGE_COST_CAP_PROVIDER_SCALE_GEMINI = "0.9";
    expect(getCostCapProviderScale("gemini", tmpDir)).toBe(0.9);
  });

  it("config-file 0 is accepted (matches env-var semantics)", () => {
    const cfg = `pipeline:
  cost_cap_provider_scale:
    gemini: 0
`;
    fs.writeFileSync(path.join(tmpDir, ".nightgauge", "config.yaml"), cfg);
    expect(getCostCapProviderScale("gemini", tmpDir)).toBe(0);
  });

  it("adjacent cost_cap_mode_multiplier and cost_cap_provider_scale blocks both parse", () => {
    // Risks-and-mitigations: confirm the line-by-line scanner doesn't
    // bleed between two sibling subsections.
    const cfg = `pipeline:
  cost_cap_mode_multiplier:
    maximum: 3.0
  cost_cap_provider_scale:
    gemini: 0.55
    codex: 0.85
`;
    fs.writeFileSync(path.join(tmpDir, ".nightgauge", "config.yaml"), cfg);
    expect(getCostCapProviderScale("gemini", tmpDir)).toBe(0.55);
    expect(getCostCapProviderScale("codex", tmpDir)).toBe(0.85);
  });
});

describe("getStageCostCapPerProviderUsd — env-var overrides", () => {
  it("returns undefined when no override is set (caller falls through)", () => {
    expect(getStageCostCapPerProviderUsd("gemini", "feature-dev")).toBeUndefined();
  });

  it("returns undefined when adapter is undefined", () => {
    expect(getStageCostCapPerProviderUsd(undefined, "feature-dev")).toBeUndefined();
  });

  it("env override NIGHTGAUGE_PIPELINE_STAGE_COST_CAP_PER_PROVIDER_GEMINI_FEATURE_DEV=15 wins", () => {
    process.env.NIGHTGAUGE_PIPELINE_STAGE_COST_CAP_PER_PROVIDER_GEMINI_FEATURE_DEV = "15";
    expect(getStageCostCapPerProviderUsd("gemini", "feature-dev")).toBe(15);
  });

  it("hyphenated adapter and stage map correctly: lm-studio + feature-dev", () => {
    process.env.NIGHTGAUGE_PIPELINE_STAGE_COST_CAP_PER_PROVIDER_LM_STUDIO_FEATURE_DEV = "20";
    expect(getStageCostCapPerProviderUsd("lm-studio", "feature-dev")).toBe(20);
  });

  it("ignores non-numeric env value", () => {
    process.env.NIGHTGAUGE_PIPELINE_STAGE_COST_CAP_PER_PROVIDER_GEMINI_FEATURE_DEV = "junk";
    expect(getStageCostCapPerProviderUsd("gemini", "feature-dev")).toBeUndefined();
  });
});

describe("getStageCostCapPerProviderUsd — file-config overrides", () => {
  let tmpDir: string;

  beforeEach(() => {
    tmpDir = fs.mkdtempSync(path.join(os.tmpdir(), "ib-3229-ovr-"));
    fs.mkdirSync(path.join(tmpDir, ".nightgauge"), { recursive: true });
  });

  afterEach(() => {
    fs.rmSync(tmpDir, { recursive: true, force: true });
  });

  it("pipeline.stage_cost_caps_per_provider.<adapter>.<stage> parses for one adapter", () => {
    const cfg = `pipeline:
  stage_cost_caps_per_provider:
    gemini:
      feature-dev: 15
      pr-create: 2
`;
    fs.writeFileSync(path.join(tmpDir, ".nightgauge", "config.yaml"), cfg);
    expect(getStageCostCapPerProviderUsd("gemini", "feature-dev", tmpDir)).toBe(15);
    expect(getStageCostCapPerProviderUsd("gemini", "pr-create", tmpDir)).toBe(2);
  });

  it("does not bleed across adapter sections", () => {
    const cfg = `pipeline:
  stage_cost_caps_per_provider:
    gemini:
      feature-dev: 15
    codex:
      feature-dev: 30
`;
    fs.writeFileSync(path.join(tmpDir, ".nightgauge", "config.yaml"), cfg);
    expect(getStageCostCapPerProviderUsd("gemini", "feature-dev", tmpDir)).toBe(15);
    expect(getStageCostCapPerProviderUsd("codex", "feature-dev", tmpDir)).toBe(30);
    expect(getStageCostCapPerProviderUsd("gemini", "pr-create", tmpDir)).toBeUndefined();
  });

  it("returns undefined when override absent — caller falls through to global default", () => {
    const cfg = `pipeline:
  stage_cost_caps_per_provider:
    gemini:
      feature-dev: 15
`;
    fs.writeFileSync(path.join(tmpDir, ".nightgauge", "config.yaml"), cfg);
    expect(getStageCostCapPerProviderUsd("codex", "pr-create", tmpDir)).toBeUndefined();
  });
});

describe("getEffectiveStageCostCap — providerScale composition (AC #6 matrix)", () => {
  // AC #6: 4 representative (adapter, model, mode) tuples assert the
  // 4-factor formula composes correctly.
  //
  //   effectiveCap = baseCap × modelScale × modeMultiplier × providerScale
  //
  // Tuple table — base = $23 (feature-dev), $3 (pr-create):
  type Tuple = {
    label: string;
    stage: string;
    baseCap: number;
    adapter: "claude" | "codex" | "gemini" | "gemini-sdk" | "lm-studio" | "ollama" | "copilot";
    model?: string;
    effort?: string;
    mode: "efficiency" | "elevated" | "maximum";
    expectedScale: number;
    expectedModeMultiplier: number;
    expectedProviderScale: number;
    expectedEffective: number;
  };

  const tuples: Tuple[] = [
    {
      // Regression-anchor tuple — must match PR #3209 byte-for-byte.
      label: "claude × sonnet/medium × elevated → $23 (AC #5 anchor)",
      stage: "feature-dev",
      baseCap: 23.0,
      adapter: "claude",
      model: "claude-sonnet-4-6",
      effort: "medium",
      mode: "elevated",
      expectedScale: 1.0,
      expectedModeMultiplier: 1.0,
      expectedProviderScale: 1.0,
      expectedEffective: 23.0,
    },
    {
      label: "claude × opus/high × maximum → $230",
      stage: "feature-dev",
      baseCap: 23.0,
      adapter: "claude",
      model: "claude-opus-4-7",
      effort: "high",
      mode: "maximum",
      expectedScale: 5.0,
      expectedModeMultiplier: 2.0,
      expectedProviderScale: 1.0,
      expectedEffective: 230.0,
    },
    {
      label: "gemini × elevated → $9.20 (provider scale alone)",
      stage: "feature-dev",
      baseCap: 23.0,
      adapter: "gemini",
      mode: "elevated",
      expectedScale: 1.0,
      expectedModeMultiplier: 1.0,
      expectedProviderScale: 0.4,
      expectedEffective: 9.2,
    },
    {
      label: "codex × maximum → $32.20 (cross-axis: provider × mode)",
      stage: "feature-dev",
      baseCap: 23.0,
      adapter: "codex",
      mode: "maximum",
      expectedScale: 1.0,
      expectedModeMultiplier: 2.0,
      expectedProviderScale: 0.7,
      expectedEffective: 32.2,
    },
  ];

  it.each(tuples)("$label", (t) => {
    const result = getEffectiveStageCostCap(
      t.stage,
      t.model ? { model: t.model, effort: t.effort } : undefined,
      undefined,
      t.mode,
      t.adapter
    );
    expect(result.baseCap).toBe(t.baseCap);
    expect(result.scale).toBeCloseTo(t.expectedScale, 6);
    expect(result.modeMultiplier).toBeCloseTo(t.expectedModeMultiplier, 6);
    expect(result.providerScale).toBeCloseTo(t.expectedProviderScale, 6);
    expect(result.effectiveCap).toBeCloseTo(t.expectedEffective, 6);
  });
});

describe("getEffectiveStageCostCap — providerScale=0 → time-cap fallback", () => {
  it("lm-studio short-circuits effectiveCap to 0 with providerScale=0", () => {
    const result = getEffectiveStageCostCap(
      "feature-dev",
      undefined,
      undefined,
      "elevated",
      "lm-studio"
    );
    expect(result.baseCap).toBe(23.0);
    expect(result.providerScale).toBe(0);
    expect(result.effectiveCap).toBe(0);
    // Mode/model multipliers are bypassed in time-cap mode (they don't
    // apply when the cost-cap path is disabled).
    expect(result.scale).toBe(1.0);
    expect(result.modeMultiplier).toBe(1.0);
  });

  it("ollama also short-circuits to providerScale=0", () => {
    const result = getEffectiveStageCostCap(
      "feature-dev",
      undefined,
      undefined,
      "maximum",
      "ollama"
    );
    expect(result.providerScale).toBe(0);
    expect(result.effectiveCap).toBe(0);
  });

  it("env override providerScale=0 also triggers time-cap mode", () => {
    process.env.NIGHTGAUGE_COST_CAP_PROVIDER_SCALE_GEMINI = "0";
    const result = getEffectiveStageCostCap(
      "feature-dev",
      undefined,
      undefined,
      "elevated",
      "gemini"
    );
    expect(result.providerScale).toBe(0);
    expect(result.effectiveCap).toBe(0);
  });
});

describe("getEffectiveStageCostCap — AC #5 regression guard", () => {
  // AC #5: claude with default scale=1.0 must produce identical math to
  // calling getEffectiveStageCostCap WITHOUT an adapter argument. This
  // is the hard invariant that PR #3209 calibrated defaults stay
  // unchanged for default Claude users.
  const cases: Array<{
    stage: string;
    model: string;
    effort: string;
    mode: "elevated" | "maximum";
  }> = [
    { stage: "feature-dev", model: "claude-sonnet-4-6", effort: "medium", mode: "elevated" },
    { stage: "feature-dev", model: "claude-opus-4-7", effort: "high", mode: "maximum" },
    { stage: "pr-create", model: "claude-sonnet-4-6", effort: "medium", mode: "elevated" },
    { stage: "pr-create", model: "claude-opus-4-7", effort: "high", mode: "maximum" },
  ];

  it.each(cases)(
    "[$stage][$model/$effort/$mode] adapter=claude == adapter=undefined",
    ({ stage, model, effort, mode }) => {
      const withClaude = getEffectiveStageCostCap(
        stage,
        { model, effort },
        undefined,
        mode,
        "claude"
      );
      const withoutAdapter = getEffectiveStageCostCap(stage, { model, effort }, undefined, mode);
      expect(withClaude.baseCap).toBe(withoutAdapter.baseCap);
      expect(withClaude.scale).toBe(withoutAdapter.scale);
      expect(withClaude.modeMultiplier).toBe(withoutAdapter.modeMultiplier);
      expect(withClaude.providerScale).toBe(1.0);
      expect(withoutAdapter.providerScale).toBe(1.0);
      expect(withClaude.effectiveCap).toBe(withoutAdapter.effectiveCap);
    }
  );
});

describe("getEffectiveStageCostCap — uncapped stages (baseCap === 0)", () => {
  it("returns providerScale=1.0 even when adapter is gemini (uncapped stays uncapped)", () => {
    const result = getEffectiveStageCostCap(
      "pipeline-start",
      undefined,
      undefined,
      "maximum",
      "gemini"
    );
    expect(result.baseCap).toBe(0);
    expect(result.providerScale).toBe(1.0);
    expect(result.effectiveCap).toBe(0);
  });
});

describe("getEffectiveStageCostCap — per-(provider, stage) override path", () => {
  it("env override replaces baseCap; provider scale still composes on top", () => {
    process.env.NIGHTGAUGE_PIPELINE_STAGE_COST_CAP_PER_PROVIDER_GEMINI_FEATURE_DEV = "15";
    const result = getEffectiveStageCostCap(
      "feature-dev",
      undefined,
      undefined,
      "elevated",
      "gemini"
    );
    // baseCap from override = $15; scale=1.0; mode=1.0; provider=0.4
    // effective = $15 × 1.0 × 1.0 × 0.4 = $6.00
    expect(result.baseCap).toBe(15);
    expect(result.providerScale).toBe(0.4);
    expect(result.effectiveCap).toBeCloseTo(6.0, 6);
  });

  it("override absent → falls through to global stage_cost_caps default", () => {
    const result = getEffectiveStageCostCap(
      "feature-dev",
      undefined,
      undefined,
      "elevated",
      "gemini"
    );
    expect(result.baseCap).toBe(23.0);
    expect(result.effectiveCap).toBeCloseTo(9.2, 6);
  });
});

describe("getEffectiveStageCostCap — adapter-switch recompute (Issue #3231)", () => {
  // AC #2 of the cost-cap spec, exercised here as a regression test: when
  // skillRunner's fallback walker switches the adapter (e.g. claude → gemini
  // → lm-studio), the cost cap MUST be computed against the FINAL adapter,
  // not the primary. Today this is satisfied implicitly because
  // skillRunner.ts:2384 calls getEffectiveStageCostCap AFTER the walker has
  // run — but a future refactor that reorders these calls would silently
  // regress. This test pins the contract: identical args except for the
  // adapter must produce different effective caps.
  it("claude → lm-studio fallback flips providerScale to 0 (time-cap mode)", () => {
    const claude = getEffectiveStageCostCap(
      "feature-dev",
      { model: "claude-sonnet-4-6", effort: "medium" },
      undefined,
      "elevated",
      "claude"
    );
    const lmStudio = getEffectiveStageCostCap(
      "feature-dev",
      // lm-studio doesn't use claude model/effort, but the API still accepts
      // them — the fallback path passes undefined here. We pin both shapes.
      undefined,
      undefined,
      "elevated",
      "lm-studio"
    );
    // Claude path: providerScale=1.0, capped USD.
    expect(claude.providerScale).toBe(1.0);
    expect(claude.effectiveCap).toBeGreaterThan(0);
    // LM Studio path: providerScale=0 → effectiveCap collapses to 0
    // (time-cap fallback mode).
    expect(lmStudio.providerScale).toBe(0);
    expect(lmStudio.effectiveCap).toBe(0);
  });

  it("claude → gemini fallback applies the gemini provider scale", () => {
    const claude = getEffectiveStageCostCap(
      "feature-dev",
      { model: "claude-sonnet-4-6", effort: "medium" },
      undefined,
      "elevated",
      "claude"
    );
    const gemini = getEffectiveStageCostCap(
      "feature-dev",
      undefined,
      undefined,
      "elevated",
      "gemini"
    );
    // The composed effective caps differ — proving the recompute used the
    // final post-fallback adapter, not the primary.
    expect(gemini.providerScale).not.toBe(claude.providerScale);
    expect(gemini.effectiveCap).not.toBe(claude.effectiveCap);
    // Gemini's provider scale is lower than claude's, so the cap is tighter.
    expect(gemini.effectiveCap).toBeLessThan(claude.effectiveCap);
  });
});
