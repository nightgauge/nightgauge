/**
 * Eval-advice consumption in resolveModel (#581, spike #568 §4.2): opt-in via
 * `model_routing.use_eval_recommendations` (default OFF), applied on the
 * selector branch only, only from advisable entries, only for a job class the
 * issue directly names, and only INSIDE the stage's clamps.
 *
 * Go twin: internal/orchestrator/dispatch_routing_advice_test.go.
 */

import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import { mkdtempSync, mkdirSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import type { PipelineStage } from "@nightgauge/sdk";
import { ROUTING_ADVICE_RELATIVE_PATH } from "@nightgauge/sdk";

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

import { resolveModel } from "../../src/utils/skillRunner";

const STAGE = "feature-dev" as PipelineStage;
const METADATA = { labels: ["size:M", "type:bug"], title: "fix the widget" };

const ADVICE = {
  schema_version: 1,
  generated_at: "2026-08-16T00:00:00Z",
  min_samples: 5,
  quality_floor: 70,
  min_honest_schema_version: 3,
  entries: [
    {
      job_class: "bugfix",
      model_id: "claude-opus-5",
      effort: "high",
      thinking: "on",
      backoff: "exact",
      samples: 9,
      pass_rate: 1,
      mean_quality: 95,
      mean_cost_usd: 0.01,
      quality_per_dollar: 9500,
      advisable: true,
    },
  ],
};

let root: string;
const originalEnv = process.env;

beforeEach(() => {
  process.env = { ...originalEnv };
  delete process.env.NIGHTGAUGE_PERFORMANCE_MODE;
  delete process.env.NIGHTGAUGE_MODEL_ROUTING_MODE;
  delete process.env.NIGHTGAUGE_MODEL_ROUTING_USE_EVAL_RECOMMENDATIONS;
  delete process.env.NIGHTGAUGE_PIPELINE_STAGE_MODEL_FEATURE_DEV;
  root = mkdtempSync(join(tmpdir(), "ng-eval-advice-"));
  const advicePath = join(root, ROUTING_ADVICE_RELATIVE_PATH);
  mkdirSync(dirname(advicePath), { recursive: true });
  writeFileSync(advicePath, JSON.stringify(ADVICE));
});

afterEach(() => {
  process.env = originalEnv;
  rmSync(root, { recursive: true, force: true });
});

describe("resolveModel — eval-advice consumption (#581)", () => {
  it("ignores the advice file by default (conservative rollout: key off = today's behavior)", () => {
    const decision = resolveModel(STAGE, root, METADATA);
    expect(decision.model).toBe("sonnet"); // the selector's own M-complexity pick
    expect(decision.evalAdvisory).toBeUndefined();
  });

  it("re-picks within the envelope when enabled and evidence is advisable", () => {
    process.env.NIGHTGAUGE_MODEL_ROUTING_USE_EVAL_RECOMMENDATIONS = "true";
    const decision = resolveModel(STAGE, root, METADATA);
    expect(decision.model).toBe("opus"); // claude-opus-5 → opus band, inside elevated
    expect(decision.source).toBe("auto"); // the selector chain still decided
    expect(decision.evalAdvisory).toMatchObject({
      modelId: "claude-opus-5",
      band: "opus",
      jobClass: "bugfix",
      backoff: "exact",
    });
    expect(decision.effort).toBe("high"); // the advised envelope's effort
  });

  it("never escapes the mode envelope — efficiency caps at sonnet, so opus advice is inert", () => {
    process.env.NIGHTGAUGE_MODEL_ROUTING_USE_EVAL_RECOMMENDATIONS = "true";
    process.env.NIGHTGAUGE_PERFORMANCE_MODE = "efficiency";
    const decision = resolveModel(STAGE, root, METADATA);
    expect(decision.model).toBe("sonnet");
    expect(decision.evalAdvisory).toBeUndefined();
  });

  // Cross-language pair with internal/intelligence/routing/advice_test.go's
  // TestJobClassForLabels whitespace cases (#637). Go matched on
  // strings.TrimSpace(label) from the start; this path did not, so a label of
  // " type:bug" attributed as bugfix there and as nothing here — the same
  // issue routed two different ways depending on which path resolved it.
  //
  // Asserted through resolveModel rather than jobClassForIssue directly
  // because the divergence only costs anything where attribution is consumed:
  // an unattributed issue silently loses its advisory and falls back to the
  // axis query, which looks like a normal conservative no-op rather than a
  // bug. METADATA's own "type:bug" is the untrimmed control — this case must
  // reach the identical decision.
  it("attributes a job class from a label padded with surrounding whitespace", () => {
    process.env.NIGHTGAUGE_MODEL_ROUTING_USE_EVAL_RECOMMENDATIONS = "true";
    const decision = resolveModel(STAGE, root, {
      labels: ["size:M", "  type:bug  "],
      title: "fix the widget",
    });
    expect(decision.evalAdvisory).toMatchObject({
      modelId: "claude-opus-5",
      band: "opus",
      jobClass: "bugfix",
      backoff: "exact",
    });
    // Same decision as the untrimmed label, field for field — whitespace is
    // not allowed to change routing in any way, not merely to leave the
    // advisory populated.
    expect(decision).toEqual(resolveModel(STAGE, root, METADATA));
  });

  it("stays inert for an issue whose labels name no eval job class — no invented mapping", () => {
    process.env.NIGHTGAUGE_MODEL_ROUTING_USE_EVAL_RECOMMENDATIONS = "true";
    const decision = resolveModel(STAGE, root, {
      labels: ["size:M", "type:feature"],
      title: "add the widget",
    });
    expect(decision.model).toBe("sonnet");
    expect(decision.evalAdvisory).toBeUndefined();
  });

  it("stays inert when the advice file is missing — fail-open to declared routing", () => {
    process.env.NIGHTGAUGE_MODEL_ROUTING_USE_EVAL_RECOMMENDATIONS = "true";
    rmSync(join(root, ROUTING_ADVICE_RELATIVE_PATH));
    const decision = resolveModel(STAGE, root, METADATA);
    expect(decision.model).toBe("sonnet");
    expect(decision.evalAdvisory).toBeUndefined();
  });

  it("must not alter routing on below-sample-floor evidence — advisable:false through resolveModel", () => {
    // The sparse-evidence guarantee asserted on the CONSUMPTION path, not
    // just via pickAdvice's own unit test (Go twin:
    // TestAdviseBandIgnoresSparseEntries): a file whose only entry for the
    // issue's job class sits below the sample floor (advisable: false, per
    // spike §4.3 emitted rather than omitted) must leave the selector's own
    // pick standing even with the key ON.
    process.env.NIGHTGAUGE_MODEL_ROUTING_USE_EVAL_RECOMMENDATIONS = "true";
    const sparse = {
      ...ADVICE,
      entries: [
        {
          ...ADVICE.entries[0],
          samples: 2, // below min_samples: 5
          advisable: false,
        },
      ],
    };
    writeFileSync(join(root, ROUTING_ADVICE_RELATIVE_PATH), JSON.stringify(sparse));
    const decision = resolveModel(STAGE, root, METADATA);
    expect(decision.model).toBe("sonnet"); // the selector's own M-complexity pick
    expect(decision.evalAdvisory).toBeUndefined();
  });
});
