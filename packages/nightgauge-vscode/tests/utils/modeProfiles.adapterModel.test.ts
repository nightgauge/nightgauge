/**
 * modeProfiles.adapterModel.test.ts (Issue #3214, #56)
 *
 * Asserts the `getModeStageAdapterModel` translator across every
 * (mode, stage, adapter) cell — including the lm-studio / ollama mismatch
 * path. Tier→model translation resolves through the SDK's provider-aware
 * model registry (`resolveModelForAdapter`), which replaced the hand-synced
 * `ADAPTER_MODEL_TABLES` (#56).
 */
import { describe, it, expect } from "vitest";
import { getModeStageAdapterModel } from "../../src/utils/modeProfiles";
import type { ExecutionAdapter } from "../../src/utils/resolvers/modelResolver";
import type { PipelineStage } from "@nightgauge/sdk";
import { resolveModelForAdapter } from "@nightgauge/sdk";

const STAGES: PipelineStage[] = [
  "issue-pickup",
  "feature-planning",
  "feature-dev",
  "feature-validate",
  "pr-create",
  "pr-merge",
];

const MAPPED_ADAPTERS: Exclude<ExecutionAdapter, "claude" | "lm-studio" | "ollama">[] = [
  "codex",
  "gemini",
  "gemini-sdk",
  "copilot",
];

const UNMAPPED_ADAPTERS: Extract<ExecutionAdapter, "lm-studio" | "ollama">[] = [
  "lm-studio",
  "ollama",
];

describe("registry tier bands per adapter (Issue #3214, #56)", () => {
  it("each mapped adapter covers haiku, sonnet, opus, fable", () => {
    for (const adapter of MAPPED_ADAPTERS) {
      for (const tier of ["haiku", "sonnet", "opus", "fable"] as const) {
        expect(resolveModelForAdapter(adapter, tier)?.id, `${adapter}.${tier}`).toBeTypeOf(
          "string"
        );
      }
    }
  });

  it("lm-studio and ollama have no tier bands — configured local model serves every tier", () => {
    for (const adapter of UNMAPPED_ADAPTERS) {
      for (const tier of ["haiku", "sonnet", "opus", "fable"] as const) {
        expect(resolveModelForAdapter(adapter, tier), `${adapter}.${tier}`).toBeUndefined();
      }
    }
  });

  it("snapshots concrete model ids for codex/gemini/copilot (regression guard)", () => {
    const snapshot = (adapter: ExecutionAdapter) =>
      Object.fromEntries(
        (["haiku", "sonnet", "opus", "fable"] as const).map((tier) => [
          tier,
          resolveModelForAdapter(adapter, tier)?.id,
        ])
      );
    expect(snapshot("codex")).toEqual({
      haiku: "gpt-5.6-luna",
      sonnet: "gpt-5.6-terra",
      opus: "gpt-5.6-sol",
      fable: "gpt-5.6-sol",
    });
    expect(snapshot("gemini")).toEqual({
      haiku: "gemini-2.5-flash",
      sonnet: "gemini-2.5-flash",
      opus: "gemini-2.5-pro",
      fable: "gemini-2.5-pro",
    });
    expect(snapshot("gemini-sdk")).toEqual({
      haiku: "gemini-2.5-flash",
      sonnet: "gemini-2.5-flash",
      opus: "gemini-2.5-pro",
      fable: "gemini-2.5-pro",
    });
    expect(snapshot("copilot")).toEqual({
      haiku: "gpt-4o-mini",
      sonnet: "gpt-4o",
      opus: "claude-sonnet-4.5",
      fable: "claude-sonnet-4.5",
    });
  });
});

describe("getModeStageAdapterModel (Issue #3214, #19)", () => {
  it("returns undefined for the claude adapter on every (mode, stage)", () => {
    for (const stage of STAGES) {
      expect(getModeStageAdapterModel("maximum", stage, "claude")).toBeUndefined();
      expect(getModeStageAdapterModel("elevated", stage, "claude")).toBeUndefined();
    }
  });

  // Issue #19: envelope modes have no per-stage pins, so this pin-translator
  // returns undefined for them. Their resolved tier is translated downstream
  // from modelDecision.model instead. Only Maximum (still pinned) maps here.
  it("returns undefined for every envelope mode (efficiency/elevated/frontier)", () => {
    for (const mode of ["efficiency", "elevated", "frontier"] as const) {
      for (const stage of STAGES) {
        for (const adapter of [...MAPPED_ADAPTERS, ...UNMAPPED_ADAPTERS]) {
          expect(
            getModeStageAdapterModel(mode, stage, adapter),
            `${adapter}/${stage}/${mode}`
          ).toBeUndefined();
        }
      }
    }
  });

  it("maximum maps every stage to the opus-tier adapter id (codex/gemini/copilot)", () => {
    expect(getModeStageAdapterModel("maximum", "feature-dev", "codex")).toEqual({
      model: "gpt-5.6-sol",
      mismatch: false,
    });
    expect(getModeStageAdapterModel("maximum", "feature-dev", "gemini")).toEqual({
      model: "gemini-2.5-pro",
      mismatch: false,
    });
    expect(getModeStageAdapterModel("maximum", "feature-dev", "gemini-sdk")).toEqual({
      model: "gemini-2.5-pro",
      mismatch: false,
    });
    expect(getModeStageAdapterModel("maximum", "feature-dev", "copilot")).toEqual({
      model: "claude-sonnet-4.5",
      mismatch: false,
    });
  });

  it("maximum maps every stage to a real (non-alias) id for mapped adapters", () => {
    for (const adapter of MAPPED_ADAPTERS) {
      for (const stage of STAGES) {
        const result = getModeStageAdapterModel("maximum", stage, adapter);
        expect(result, `${adapter}/${stage}`).toBeDefined();
        expect(result!.mismatch, `${adapter}/${stage}`).toBe(false);
        expect(["haiku", "sonnet", "opus", "fable"]).not.toContain(result!.model);
      }
    }
  });

  it("returns mismatch:true with the opus alias for lm-studio/ollama under maximum", () => {
    expect(getModeStageAdapterModel("maximum", "feature-dev", "lm-studio")).toEqual({
      model: "opus",
      mismatch: true,
    });
    expect(getModeStageAdapterModel("maximum", "pr-create", "ollama")).toEqual({
      model: "opus",
      mismatch: true,
    });
  });
});
