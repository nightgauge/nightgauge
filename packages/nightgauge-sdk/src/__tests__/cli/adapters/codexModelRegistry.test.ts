/**
 * Canonical Codex model registry tests (#4018).
 *
 * Guards the single source of truth against drift: tier map correctness, no
 * deprecated ids leaking through alias resolution, and catalog filtering.
 */

import { describe, it, expect } from "vitest";
import {
  CODEX_MODELS,
  CODEX_TIER_MODEL_MAP,
  CODEX_RECOMMENDED_DEFAULT_MODEL,
  CODEX_DEFAULT_BASE_MODEL,
  isValidCodexModel,
  isDeprecatedCodexModel,
  isResearchPreviewCodexModel,
  listCodexModels,
  resolveCodexModelAlias,
} from "../../../cli/adapters/codexModelRegistry.js";

describe("codexModelRegistry", () => {
  describe("CODEX_TIER_MODEL_MAP", () => {
    it("maps tiers to the verified current model ids", () => {
      expect(CODEX_TIER_MODEL_MAP).toEqual({
        haiku: "gpt-5.4-mini",
        sonnet: "gpt-5.4",
        opus: "gpt-5.5",
        fable: "gpt-5.5",
      });
    });

    it("never maps a tier to a deprecated or unknown model", () => {
      for (const id of Object.values(CODEX_TIER_MODEL_MAP)) {
        expect(isValidCodexModel(id)).toBe(true);
        expect(isDeprecatedCodexModel(id)).toBe(false);
      }
    });
  });

  describe("resolveCodexModelAlias", () => {
    it("resolves each tier alias to the mapped id", () => {
      expect(resolveCodexModelAlias("haiku")).toBe("gpt-5.4-mini");
      expect(resolveCodexModelAlias("sonnet")).toBe("gpt-5.4");
      expect(resolveCodexModelAlias("opus")).toBe("gpt-5.5");
      expect(resolveCodexModelAlias("fable")).toBe("gpt-5.5");
    });

    it("returns undefined for undefined input", () => {
      expect(resolveCodexModelAlias(undefined)).toBeUndefined();
    });

    it("passes through exact and unknown model ids unchanged", () => {
      expect(resolveCodexModelAlias("gpt-5.4")).toBe("gpt-5.4");
      expect(resolveCodexModelAlias("gpt-5.4-mini")).toBe("gpt-5.4-mini");
      expect(resolveCodexModelAlias("some-future-model")).toBe("some-future-model");
    });

    it("trims whitespace before resolving a tier", () => {
      expect(resolveCodexModelAlias("  opus  ")).toBe("gpt-5.5");
    });

    it("maps Claude escalation ids by prefix, mirroring the Go adapter (#4021)", () => {
      // Must match resolveCodexModel in internal/execution/adapters/codex.go.
      expect(resolveCodexModelAlias("claude-haiku-4-5")).toBe("gpt-5.4-mini");
      expect(resolveCodexModelAlias("claude-sonnet-4-6")).toBe("gpt-5.4");
      expect(resolveCodexModelAlias("claude-opus-4-8")).toBe("gpt-5.5");
      // Prefix match is intentional (escalation ids are internally generated).
      expect(resolveCodexModelAlias("claude-sonnet-4-6-bad")).toBe("gpt-5.4");
    });

    it("never returns a deprecated id for a tier alias (regression: opus→gpt-5.3-codex)", () => {
      for (const tier of ["haiku", "sonnet", "opus", "fable"]) {
        const resolved = resolveCodexModelAlias(tier);
        expect(resolved).toBeDefined();
        expect(isDeprecatedCodexModel(resolved as string)).toBe(false);
        expect(resolved).not.toBe("gpt-5.3-codex");
        expect(resolved).not.toBe("gpt-5.1-codex-mini");
      }
    });

    it("remaps a known-deprecated id to its current replacement", () => {
      expect(resolveCodexModelAlias("gpt-5.3-codex")).toBe("gpt-5.5");
      expect(resolveCodexModelAlias("gpt-5.2")).toBe("gpt-5.4");
      expect(resolveCodexModelAlias("gpt-5.1-codex-mini")).toBe("gpt-5.4-mini");
    });
  });

  describe("isResearchPreviewCodexModel", () => {
    it("is true only for research-preview ids", () => {
      expect(isResearchPreviewCodexModel("gpt-5.3-codex-spark")).toBe(true);
      expect(isResearchPreviewCodexModel("gpt-5.5")).toBe(false);
      expect(isResearchPreviewCodexModel("gpt-5.4")).toBe(false);
      expect(isResearchPreviewCodexModel("unknown")).toBe(false);
    });
  });

  describe("isValidCodexModel", () => {
    it("is true for current and known (deprecated/preview) ids", () => {
      expect(isValidCodexModel("gpt-5.5")).toBe(true);
      expect(isValidCodexModel("gpt-5.4")).toBe(true);
      expect(isValidCodexModel("gpt-5.4-mini")).toBe(true);
      expect(isValidCodexModel("gpt-5.3-codex-spark")).toBe(true);
      expect(isValidCodexModel("gpt-5.3-codex")).toBe(true);
    });

    it("is false for unknown ids", () => {
      expect(isValidCodexModel("gpt-5")).toBe(false);
      expect(isValidCodexModel("codex-mini")).toBe(false);
      expect(isValidCodexModel("o4-mini")).toBe(false);
      expect(isValidCodexModel("")).toBe(false);
    });
  });

  describe("listCodexModels", () => {
    it("excludes deprecated and research-preview by default, recommended first", () => {
      const list = listCodexModels();
      expect(list[0]).toBe("gpt-5.5");
      expect(list).toContain("gpt-5.4");
      expect(list).toContain("gpt-5.4-mini");
      expect(list).not.toContain("gpt-5.3-codex");
      expect(list).not.toContain("gpt-5.1-codex-mini");
      expect(list).not.toContain("gpt-5.3-codex-spark");
    });

    it("includes deprecated/preview ids when requested", () => {
      const all = listCodexModels({
        includeDeprecated: true,
        includeResearchPreview: true,
      });
      expect(all).toContain("gpt-5.3-codex");
      expect(all).toContain("gpt-5.3-codex-spark");
    });
  });

  describe("deprecation metadata", () => {
    it("every deprecated model has a valid, non-deprecated replacement", () => {
      for (const [id, meta] of Object.entries(CODEX_MODELS)) {
        if (meta.deprecated) {
          expect(meta.replacement, `${id} needs a replacement`).toBeTruthy();
          expect(isValidCodexModel(meta.replacement as string)).toBe(true);
          expect(isDeprecatedCodexModel(meta.replacement as string)).toBe(false);
        }
      }
    });

    it("default model constants reference valid, non-deprecated models", () => {
      for (const id of [CODEX_RECOMMENDED_DEFAULT_MODEL, CODEX_DEFAULT_BASE_MODEL]) {
        expect(isValidCodexModel(id)).toBe(true);
        expect(isDeprecatedCodexModel(id)).toBe(false);
      }
    });
  });
});
