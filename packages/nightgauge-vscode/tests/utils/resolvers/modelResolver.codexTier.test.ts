import { describe, it, expect } from "vitest";
import { resolveCodexPipelineModel } from "../../../src/utils/resolvers/modelResolver";

/**
 * Real (un-mocked) coverage of resolveCodexPipelineModel's tier→model
 * resolution. Regression guard for #4018/#4019: the `fable` tier previously
 * bypassed the canonical map (a type-guard omitted it) and leaked the literal
 * string "fable" to the Codex CLI as an invalid model id.
 */
describe("resolveCodexPipelineModel — Codex tier resolution (#4018/#4019)", () => {
  it("maps haiku/opus/fable tiers to concrete current Codex models", () => {
    expect(resolveCodexPipelineModel("haiku")).toBe("gpt-5.4-mini");
    expect(resolveCodexPipelineModel("opus")).toBe("gpt-5.5");
    // fable is the regression case — must resolve, not leak "fable".
    expect(resolveCodexPipelineModel("fable")).toBe("gpt-5.5");
  });

  it("never returns a raw tier-alias string to the CLI", () => {
    for (const tier of ["haiku", "opus", "fable"]) {
      expect(resolveCodexPipelineModel(tier)).not.toBe(tier);
    }
  });

  it("passes an exact Codex model id through unchanged", () => {
    expect(resolveCodexPipelineModel("gpt-5.4-mini")).toBe("gpt-5.4-mini");
    expect(resolveCodexPipelineModel("gpt-5.5")).toBe("gpt-5.5");
  });
});
