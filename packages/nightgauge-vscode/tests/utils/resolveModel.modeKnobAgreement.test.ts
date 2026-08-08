/**
 * resolveModel.modeKnobAgreement.test.ts (#340)
 *
 * The TypeScript half of the mode × knob agreement matrix. Its Go twin is
 * internal/orchestrator/dispatch_routing_mode_test.go
 * (TestDispatchModelModeKnobMatrix), which asserts the SAME cells against
 * `resolveDispatchModel`. Each resolver owns one dispatch path, so a cell where
 * they disagree is the Dual-Path Drift class #340 exists to remove.
 *
 * Nothing here is mocked except the config FILE and `vscode`: the real
 * `getPerformanceMode`, `getStageEnvModel`, `getStageModel` and
 * `getModelRoutingMode` run, because the precedence between them is exactly
 * what the matrix is about. `AutoModelSelector` is out of the picture (no issue
 * metadata is passed) so the un-overridden step lands on the configured default
 * — the TS counterpart of the Go path's routed tier, fed the same value.
 */
import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import type { PipelineStage } from "@nightgauge/sdk";

vi.mock("vscode", () => ({
  workspace: { workspaceFolders: [{ uri: { fsPath: "/test/workspace" } }] },
  window: {
    createOutputChannel: vi.fn(() => ({
      appendLine: vi.fn(),
      show: vi.fn(),
      clear: vi.fn(),
      dispose: vi.fn(),
    })),
    showWarningMessage: vi.fn(),
  },
  extensions: { getExtension: vi.fn(() => null) },
}));

let configText = "";

vi.mock("../../src/utils/configPathResolver", () => ({
  resolveConfigPathSync: vi.fn(() => ({
    path: "/test/workspace/.nightgauge/config.yaml",
    isLegacy: false,
    exists: true,
  })),
  logDeprecationWarning: vi.fn(),
}));

vi.mock("../../src/utils/mergedConfigReader", () => ({
  readEffectiveConfigTextSync: vi.fn(() => configText),
}));

import { resolveModel } from "../../src/utils/skillRunner";

/** The tier both resolvers are handed for the un-overridden step. */
const ROUTED_TIER = "opus";

const STAGE = "feature-dev" as PipelineStage;

const MANUAL_HAIKU =
  "model_routing:\n  mode: manual\npipeline:\n  stage_models:\n    feature-dev: haiku\n";
const MANUAL_OPUS =
  "model_routing:\n  mode: manual\npipeline:\n  stage_models:\n    feature-dev: opus\n";
const AUTOMATIC = "model_routing:\n  mode: automatic\n";

const originalEnv = process.env;

beforeEach(() => {
  process.env = { ...originalEnv };
  delete process.env.NIGHTGAUGE_PERFORMANCE_MODE;
  delete process.env.NIGHTGAUGE_PIPELINE_STAGE_MODEL_FEATURE_DEV;
  delete process.env.NIGHTGAUGE_MODEL_ROUTING_MODE;
  process.env.NIGHTGAUGE_UI_CORE_DEFAULT_MODEL = ROUTED_TIER;
  configText = AUTOMATIC;
});

afterEach(() => {
  process.env = originalEnv;
});

interface Cell {
  name: string;
  mode?: string;
  config?: string;
  env?: string;
  want: string;
  why: string;
}

const MATRIX: Cell[] = [
  {
    name: "unset + automatic: the routed tier",
    want: "opus",
    why: "elevated is the open [haiku, opus] envelope — nothing to clamp",
  },
  {
    name: "unset + manual stage_models",
    config: MANUAL_HAIKU,
    want: "haiku",
    why: "explicit per-stage routing beats the router",
  },
  {
    name: "unset + env override",
    env: "haiku",
    want: "haiku",
    why: "the env override is the operator's escape hatch",
  },
  {
    name: "efficiency + automatic: capped, not pinned",
    mode: "efficiency",
    want: "sonnet",
    why: "MODE_PROFILES.efficiency.stages is {} — the [haiku, sonnet] envelope clamps instead",
  },
  {
    name: "efficiency + manual stage_models above the ceiling",
    mode: "efficiency",
    config: MANUAL_OPUS,
    want: "opus",
    why: "Step 1 returns the explicit model unclamped — it overrides the mode for that stage",
  },
  {
    name: "efficiency + env override above the ceiling",
    mode: "efficiency",
    env: "opus",
    want: "opus",
    why: "the env override wins in every mode, and is not clamped",
  },
  {
    name: "frontier + automatic: the routed tier, not Fable",
    mode: "frontier",
    want: "opus",
    why: "frontier widens the ceiling; only AutoModelSelector's L/XL reasoning rule reaches Fable",
  },
  {
    name: "frontier + manual stage_models",
    mode: "frontier",
    config: MANUAL_HAIKU,
    want: "haiku",
    why: "MODE_PROFILES.frontier.stages is {} — there is no pin to preempt Step 1",
  },
  {
    name: "frontier + env override",
    mode: "frontier",
    env: "haiku",
    want: "haiku",
    why: "docs/CONFIGURATION.md promises the env var wins in every mode",
  },
  {
    name: "maximum + automatic: pinned to opus",
    mode: "maximum",
    want: "opus",
    why: "maximum is the ONE mode that still pins, on every stage",
  },
  {
    name: "maximum + manual stage_models loses to the pin",
    mode: "maximum",
    config: MANUAL_HAIKU,
    want: "opus",
    why: "Step 0 returns the pin before Step 1 reads the config",
  },
  {
    name: "maximum + env override beats the pin",
    mode: "maximum",
    env: "haiku",
    want: "haiku",
    why: "the per-stage env override is resolved ahead of the pin, on both resolvers",
  },
];

describe("resolveModel × resolveDispatchModel — mode × knob agreement (#340)", () => {
  for (const cell of MATRIX) {
    it(cell.name, () => {
      if (cell.mode) process.env.NIGHTGAUGE_PERFORMANCE_MODE = cell.mode;
      if (cell.env) process.env.NIGHTGAUGE_PIPELINE_STAGE_MODEL_FEATURE_DEV = cell.env;
      if (cell.config) configText = cell.config;

      expect(resolveModel(STAGE, "/test/workspace").model, cell.why).toBe(cell.want);
    });
  }

  it("ignores a per-stage env value that is not a registry band", () => {
    // getStageModel's `validModels` guard, and its Go pair `validStageModel`.
    // A value one resolver drops and the other dispatches is the drift #340
    // removed.
    process.env.NIGHTGAUGE_PIPELINE_STAGE_MODEL_FEATURE_DEV = "gpt-5.6-sol";
    expect(resolveModel(STAGE, "/test/workspace").model).toBe(ROUTED_TIER);
  });

  it("attributes an env-sourced model to `env`, not `config`", () => {
    process.env.NIGHTGAUGE_PERFORMANCE_MODE = "maximum";
    process.env.NIGHTGAUGE_PIPELINE_STAGE_MODEL_FEATURE_DEV = "haiku";
    const decision = resolveModel(STAGE, "/test/workspace");
    expect(decision.source).toBe("env");
    // The mode still governs the effort — the override named a model.
    expect(decision.effort).toBe("high");
  });
});
