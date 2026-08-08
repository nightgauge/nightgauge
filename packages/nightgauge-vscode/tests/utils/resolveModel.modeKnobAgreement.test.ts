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

import { resolveModel, resolveStageEffort } from "../../src/utils/skillRunner";

/** The tier both resolvers are handed for the un-overridden step. */
const ROUTED_TIER = "opus";

const STAGE = "feature-dev" as PipelineStage;

const MANUAL_HAIKU =
  "model_routing:\n  mode: manual\npipeline:\n  stage_models:\n    feature-dev: haiku\n";
const MANUAL_OPUS =
  "model_routing:\n  mode: manual\npipeline:\n  stage_models:\n    feature-dev: opus\n";
const AUTOMATIC = "model_routing:\n  mode: automatic\n";

/** `model_routing.mode: manual` + an explicit model + a floor above it. */
const MANUAL_HAIKU_FLOOR_OPUS =
  "model_routing:\n  mode: manual\n  minimum_model:\n    feature-dev: opus\n" +
  "pipeline:\n  stage_models:\n    feature-dev: haiku\n";
/** The same, with the floor above the operator's own (stronger) model. */
const MANUAL_OPUS_FLOOR_FABLE =
  "model_routing:\n  mode: manual\n  minimum_model:\n    feature-dev: fable\n" +
  "pipeline:\n  stage_models:\n    feature-dev: opus\n";

const originalEnv = process.env;

beforeEach(() => {
  process.env = { ...originalEnv };
  delete process.env.NIGHTGAUGE_PERFORMANCE_MODE;
  delete process.env.NIGHTGAUGE_PIPELINE_STAGE_MODEL_FEATURE_DEV;
  delete process.env.NIGHTGAUGE_PIPELINE_STAGE_EFFORT_FEATURE_DEV;
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
  // The minimum_model floor × manual mode (#340). Before this, Step 1 returned
  // the explicit model and never consulted the floor, while Go's
  // resolveDispatchModel applied it — so `minimum_model.feature-dev: fable`
  // dispatched Fable on an autonomous run and Sonnet on an extension-driven
  // one, from one config file. `manual` is not an exotic cell: every
  // recommended profile in docs/CONFIGURATION.md sets it, and there
  // getStageModel answers for EVERY stage out of DEFAULT_STAGE_MODELS.
  {
    name: "unset + manual + minimum_model above the explicit model",
    config: MANUAL_HAIKU_FLOOR_OPUS,
    want: "opus",
    why: "a floor binds an explicit per-stage model too — one config cannot mean two things",
  },
  {
    name: "efficiency + manual + minimum_model above the mode ceiling",
    mode: "efficiency",
    config: MANUAL_HAIKU_FLOOR_OPUS,
    want: "sonnet",
    why: "the raise the floor produced lands inside the mode ceiling — a cost-capping mode caps",
  },
  {
    name: "efficiency + manual + a floor above the operator's stronger model",
    mode: "efficiency",
    config: MANUAL_OPUS_FLOOR_FABLE,
    want: "opus",
    why: "the ceiling discards the raise, never the operator's own model — a floor must not downgrade",
  },
  {
    name: "maximum + env override + a floor below the mode's envelope floor",
    mode: "maximum",
    env: "haiku",
    config: "model_routing:\n  minimum_model:\n    feature-dev: sonnet\n",
    want: "sonnet",
    why: "bounding a raise uses the CEILING only — re-applying maximum's [opus, opus] floor would turn a floor into an upgrade",
  },
  {
    name: "frontier + manual + minimum_model fable on a reasoning stage",
    mode: "frontier",
    config:
      "model_routing:\n  mode: manual\n  minimum_model:\n    feature-dev: fable\n" +
      "pipeline:\n  stage_models:\n    feature-dev: haiku\n",
    want: "fable",
    why: "feature-planning/feature-dev are the stages the frontier ceiling is offered to",
  },
];

/**
 * `feature-validate` under `frontier` — the one stage MODE_PROFILES.frontier
 * caps at Opus, and the one the codebase records as having "empirically failed
 * validation in dogfooding" on Fable. The floor arrives after the selector, so
 * clamping it against the raw `fable` ceiling re-creates exactly the behavior
 * #19 deleted.
 */
const VALIDATE_STAGE = "feature-validate" as PipelineStage;

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

  it("caps a fable floor at opus on feature-validate under frontier", () => {
    process.env.NIGHTGAUGE_PERFORMANCE_MODE = "frontier";
    configText =
      "model_routing:\n  mode: manual\n  minimum_model:\n    feature-validate: fable\n" +
      "pipeline:\n  stage_models:\n    feature-validate: sonnet\n";
    expect(
      resolveModel(VALIDATE_STAGE, "/test/workspace").model,
      "MODE_PROFILES.frontier caps this stage at Opus — a floor is not an explicit per-stage model"
    ).toBe("opus");
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

/**
 * Steps 1.6 and 1.7 are the two branches that used to return their model RAW
 * (#340 round 3). Every other branch runs it through `withFloor` — the
 * `model_routing.minimum_model` raise, landed inside the stage's routed-tier
 * ceiling — so a policy override or an A/B treatment was the one door out of
 * the envelope that every other door closed. The `frontier` +
 * `feature-validate` cell is the specific escape MODE_PROFILES.frontier
 * documents as deleted: "feature-validate never exceeds Opus", the behavior
 * that "empirically failed validation in dogfooding".
 *
 * These are ALSO cross-path cells: Go's `resolveDispatchModel` applies the
 * floor and the ceiling to every base it produces, so an unclamped TS branch
 * means one config file dispatches two different tiers.
 */
describe("resolveModel Steps 1.6/1.7 stay inside the envelope (#340)", () => {
  const POLICY_HAIKU_FLOOR_OPUS =
    "model_routing:\n  mode: automatic\n  minimum_model:\n    feature-dev: opus\n" +
    "  stage_overrides:\n    feature-dev: haiku\n";
  const POLICY_OPUS =
    "model_routing:\n  mode: automatic\n  stage_overrides:\n    feature-dev: opus\n";
  const POLICY_VALIDATE_FABLE =
    "model_routing:\n  mode: automatic\n  stage_overrides:\n    feature-validate: fable\n";
  /** split_percent 100 → every issue lands in the treatment arm. */
  const EXPERIMENT_OPUS =
    "model_routing:\n  mode: automatic\n  experiment:\n    name: tier-probe\n    active: true\n" +
    "    split_percent: 100\n    control:\n      model: haiku\n    treatment:\n      model: opus\n";

  it("Step 1.6: the minimum_model floor binds an adaptive-policy override", () => {
    configText = POLICY_HAIKU_FLOOR_OPUS;
    expect(
      resolveModel(STAGE, "/test/workspace").model,
      "Go applies the floor to every base it produces — an unfloored policy override is one config, two tiers"
    ).toBe("opus");
  });

  it("Step 1.6: the mode ceiling binds an adaptive-policy override", () => {
    process.env.NIGHTGAUGE_PERFORMANCE_MODE = "efficiency";
    configText = POLICY_OPUS;
    expect(
      resolveModel(STAGE, "/test/workspace").model,
      "a cost-capping mode that a policy override can raise out of caps nothing"
    ).toBe("sonnet");
  });

  it("Step 1.6: frontier's fable ceiling still stops at Opus on feature-validate", () => {
    process.env.NIGHTGAUGE_PERFORMANCE_MODE = "frontier";
    configText = POLICY_VALIDATE_FABLE;
    expect(
      resolveModel(VALIDATE_STAGE, "/test/workspace").model,
      "MODE_PROFILES.frontier: plumbing stays Haiku and feature-validate never exceeds Opus"
    ).toBe("opus");
  });

  it("Step 1.7: an A/B treatment is clamped like every other pipeline choice", () => {
    process.env.NIGHTGAUGE_PERFORMANCE_MODE = "efficiency";
    configText = EXPERIMENT_OPUS;
    const decision = resolveModel(STAGE, "/test/workspace", undefined, 7);
    expect(decision.source, "the assignment still happened — only its tier is bounded").toBe(
      "experiment"
    );
    expect(
      decision.model,
      "an experiment must not be the one mechanism that escapes the envelope"
    ).toBe("sonnet");
  });
});

/**
 * `ui.core.default_model` (Step 3) read from the FILE, not the env var (#340).
 *
 * `getDefaultModel`'s env branch accepts all four registry bands and its file
 * branch accepted three: a regex written before Fable existed. Go's
 * `workspaceDefaultModel` accepts four from both, so `ui.core.default_model:
 * fable` dispatched Fable autonomously and Sonnet (the Step 4 hardcoded
 * fallback) from the extension — from one config file, with no log line on
 * either side. The Go twin is TestDispatchModelResolvesTheWorkspaceDefault.
 */
describe("resolveModel Step 3 reads the same band set Go does (#340)", () => {
  beforeEach(() => {
    // This suite is about the FILE half — the env var is the half that already
    // agreed.
    delete process.env.NIGHTGAUGE_UI_CORE_DEFAULT_MODEL;
  });

  it("accepts `fable` from ui.core.default_model", () => {
    process.env.NIGHTGAUGE_PERFORMANCE_MODE = "frontier";
    configText = "ui:\n  core:\n    default_model: fable\n";
    expect(
      resolveModel(STAGE, "/test/workspace").model,
      "fable is a registry band; DefaultModelSchema permits it and Go dispatches it"
    ).toBe("fable");
  });

  it("clamps a `fable` default into the mode envelope, like every other Step 3 value", () => {
    configText = "ui:\n  core:\n    default_model: fable\n";
    expect(
      resolveModel(STAGE, "/test/workspace").model,
      "elevated tops out at opus — accepting the band does not exempt it from the ceiling"
    ).toBe("opus");
  });
});

/**
 * `resolveStageEffort` is the effort half of the IPC path: with the model
 * pre-decided on the wire, `runStageSkillHeadless` skips `resolveModel`
 * entirely and asks this instead. One config must therefore yield one thinking
 * budget on both paths (#340).
 */
describe("resolveStageEffort mirrors resolveModel Step 0's env suppression (#340)", () => {
  it("an explicit per-stage model override suppresses the mode's effort pin", () => {
    process.env.NIGHTGAUGE_PERFORMANCE_MODE = "maximum";
    process.env.NIGHTGAUGE_PIPELINE_STAGE_MODEL_FEATURE_DEV = "opus";
    process.env.NIGHTGAUGE_PIPELINE_STAGE_EFFORT_FEATURE_DEV = "xhigh";

    expect(
      resolveModel(STAGE, "/test/workspace").effort,
      "Step 0 is suppressed by the env override, so the configured effort is only floored"
    ).toBe("xhigh");
    expect(
      resolveStageEffort(STAGE, "/test/workspace"),
      "the IPC path must not spawn a different thinking budget than the orchestrated one"
    ).toBe("xhigh");
  });

  it("keeps the mode's pin when no per-stage model override is set", () => {
    process.env.NIGHTGAUGE_PERFORMANCE_MODE = "maximum";
    process.env.NIGHTGAUGE_PIPELINE_STAGE_EFFORT_FEATURE_DEV = "xhigh";

    expect(resolveModel(STAGE, "/test/workspace").effort).toBe("high");
    expect(resolveStageEffort(STAGE, "/test/workspace")).toBe("high");
  });

  it("control: efficiency caps both paths at medium", () => {
    process.env.NIGHTGAUGE_PERFORMANCE_MODE = "efficiency";
    process.env.NIGHTGAUGE_PIPELINE_STAGE_EFFORT_FEATURE_DEV = "xhigh";

    expect(resolveModel(STAGE, "/test/workspace").effort).toBe("medium");
    expect(resolveStageEffort(STAGE, "/test/workspace")).toBe("medium");
  });
});
