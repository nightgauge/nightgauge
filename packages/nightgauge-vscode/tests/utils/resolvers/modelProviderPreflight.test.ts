import { describe, it, expect } from "vitest";
import { validateModelForAdapter, AdapterError } from "@nightgauge/sdk";

/**
 * Extension-boundary guard for the provider-aware model preflight (#4021).
 *
 * skillRunner.ts validates each adapter's computed model env immediately before
 * spawn using `validateModelForAdapter` from the SDK barrel, and writes the
 * resolved concrete model back so no routing tier leaks to the CLI. This test
 * asserts that contract holds through the published SDK surface (the same
 * import the extension uses), without spinning up a real subprocess.
 */
describe("model↔provider preflight at the extension boundary (#4021)", () => {
  it("resolves Codex tiers — including the fable regression — to concrete ids", () => {
    expect(validateModelForAdapter("codex", "haiku").model).toBe("gpt-5.6-luna");
    expect(validateModelForAdapter("codex", "opus").model).toBe("gpt-5.6-sol");
    // fable previously leaked the literal string to the Codex CLI (#4018/#4019).
    expect(validateModelForAdapter("codex", "fable").model).toBe("gpt-5.6-sol");
  });

  it("never returns a raw tier keyword for a closed adapter (no --model leak)", () => {
    for (const tier of ["haiku", "sonnet", "opus", "fable"]) {
      expect(validateModelForAdapter("codex", tier).model).not.toBe(tier);
      expect(validateModelForAdapter("gemini", tier).model).not.toBe(tier);
    }
  });

  it("throws an AdapterError on an invalid codex/gemini model (fails before spawn)", () => {
    expect(() => validateModelForAdapter("codex", "gpt-5.4-typo")).toThrow(AdapterError);
    expect(() => validateModelForAdapter("gemini", "gemini-nope")).toThrow(AdapterError);
  });

  it("passes arbitrary local model ids through for open adapters", () => {
    expect(validateModelForAdapter("lm-studio", "qwen2.5-coder").model).toBe("qwen2.5-coder");
    expect(validateModelForAdapter("copilot", "gpt-4o").model).toBe("gpt-4o");
  });
});
