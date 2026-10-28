/**
 * Issue-label model pins and caps in resolveModel (#8). The label rule is
 * driven by the SAME fixture as the Go suite (label_override_test.go), so a
 * divergence fails both; the resolver cases mirror
 * TestResolveDispatchModel_LabelPin (scheduler_dispatch_model_test.go).
 */

import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import { mkdtempSync, readFileSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
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

vi.mock("../../src/utils/configPathResolver", () => ({
  resolveConfigPathSync: vi.fn(() => ({
    path: "/test/workspace/.nightgauge/config.yaml",
    isLegacy: false,
    exists: false,
  })),
  logDeprecationWarning: vi.fn(),
}));

vi.mock("../../src/utils/mergedConfigReader", () => ({
  readEffectiveConfigTextSync: vi.fn(() => ""),
}));

import {
  applyModelLabelCap,
  applyModelLabelPin,
  parseModelLabels,
  resolveModel,
} from "../../src/utils/skillRunner";
import { resolveConfigPathSync } from "../../src/utils/configPathResolver";
import { readEffectiveConfigTextSync } from "../../src/utils/mergedConfigReader";
import type { DefaultModel } from "../../src/utils/resolvers/modelResolver";

interface FixtureCase {
  name: string;
  labels: string[];
  base: DefaultModel;
  want_pin: string;
  want_max: string;
  want_label: string;
  want_source: string;
  want_warnings: number;
  want_model: string;
}

const fixturePath = join(
  dirname(fileURLToPath(import.meta.url)),
  "../../../..",
  "internal/intelligence/routing/testdata/model-label-overrides-crosslang.json"
);
const FIXTURE = JSON.parse(readFileSync(fixturePath, "utf-8")) as { cases: FixtureCase[] };

describe("parseModelLabels — shared crosslang fixture (#8)", () => {
  it("has cases", () => {
    expect(FIXTURE.cases.length).toBeGreaterThan(0);
  });
  for (const c of FIXTURE.cases) {
    it(c.name, () => {
      const { override, warnings } = parseModelLabels(c.labels);
      expect(override.pin ?? "").toBe(c.want_pin);
      expect(override.max ?? "").toBe(c.want_max);
      expect(override.label).toBe(c.want_label);
      expect(override.source).toBe(c.want_source);
      expect(warnings).toHaveLength(c.want_warnings);
      expect(applyModelLabelCap(override, applyModelLabelPin(override, c.base))).toBe(c.want_model);
    });
  }
});

const STAGE = "feature-dev" as PipelineStage;
const meta = (...labels: string[]) => ({ labels: ["size:M", ...labels], title: "t" });

let root: string;
const originalEnv = process.env;

beforeEach(() => {
  process.env = { ...originalEnv };
  delete process.env.NIGHTGAUGE_PERFORMANCE_MODE;
  delete process.env.NIGHTGAUGE_MODEL_ROUTING_MODE;
  delete process.env.NIGHTGAUGE_PIPELINE_STAGE_MODEL_FEATURE_DEV;
  delete process.env.NIGHTGAUGE_MODEL_ROUTING_MIN_MODEL_FEATURE_DEV;
  root = mkdtempSync(join(tmpdir(), "ng-label-pin-"));
  vi.mocked(resolveConfigPathSync).mockReturnValue({
    path: join(root, ".nightgauge/config.yaml"),
    isLegacy: false,
    exists: false,
  } as ReturnType<typeof resolveConfigPathSync>);
  vi.mocked(readEffectiveConfigTextSync).mockReturnValue("");
});

afterEach(() => {
  process.env = originalEnv;
  rmSync(root, { recursive: true, force: true });
});

function withConfig(text: string): void {
  vi.mocked(resolveConfigPathSync).mockReturnValue({
    path: join(root, ".nightgauge/config.yaml"),
    isLegacy: false,
    exists: true,
  } as ReturnType<typeof resolveConfigPathSync>);
  vi.mocked(readEffectiveConfigTextSync).mockReturnValue(text);
}

describe("resolveModel — issue-label pins and caps (#8)", () => {
  it("no model label leaves the selection unchanged", () => {
    for (const mode of ["efficiency", "elevated", "frontier", "maximum"]) {
      process.env.NIGHTGAUGE_PERFORMANCE_MODE = mode;
      const plain = resolveModel(STAGE, root, meta());
      const labelled = resolveModel(STAGE, root, meta("type:bug", "model:unknown"));
      expect({ ...labelled, evalAdvisory: undefined }).toEqual({
        ...plain,
        evalAdvisory: undefined,
      });
    }
    delete process.env.NIGHTGAUGE_PERFORMANCE_MODE;
    expect(resolveModel(STAGE, root, meta()).model).toBe("opus");
  });

  it("a pin below the routed tier is honoured", () => {
    expect(resolveModel(STAGE, root, meta("model:pin-haiku")).model).toBe("haiku");
  });

  it("a pin above the mode ceiling is clamped", () => {
    process.env.NIGHTGAUGE_PERFORMANCE_MODE = "efficiency";
    expect(resolveModel(STAGE, root, meta("model:pin-opus")).model).toBe("sonnet");
  });

  it("a cap below the routed tier lowers it; above changes nothing", () => {
    expect(resolveModel(STAGE, root, meta("model:max-sonnet")).model).toBe("sonnet");
    expect(resolveModel(STAGE, root, meta("model:max-fable")).model).toBe("opus");
  });

  it("pin above cap: the cap wins", () => {
    expect(resolveModel(STAGE, root, meta("model:pin-opus", "model:max-haiku")).model).toBe(
      "haiku"
    );
  });

  it("explicit stage config beats the label", () => {
    withConfig(
      "model_routing:\n  mode: manual\npipeline:\n  stage_models:\n    feature-dev: sonnet\n"
    );
    expect(resolveModel(STAGE, root, meta("model:pin-haiku")).model).toBe("sonnet");
  });

  it("the env override beats the label", () => {
    process.env.NIGHTGAUGE_PIPELINE_STAGE_MODEL_FEATURE_DEV = "sonnet";
    expect(resolveModel(STAGE, root, meta("model:max-haiku")).model).toBe("sonnet");
  });

  it("the minimum_model floor beats a cap", () => {
    process.env.NIGHTGAUGE_MODEL_ROUTING_MIN_MODEL_FEATURE_DEV = "opus";
    expect(resolveModel(STAGE, root, meta("model:max-haiku")).model).toBe("opus");
  });

  it("pin-haiku under maximum dispatches haiku", () => {
    process.env.NIGHTGAUGE_PERFORMANCE_MODE = "maximum";
    expect(resolveModel(STAGE, root, meta()).model).toBe("opus");
    expect(resolveModel(STAGE, root, meta("model:pin-haiku")).model).toBe("haiku");
  });
});
