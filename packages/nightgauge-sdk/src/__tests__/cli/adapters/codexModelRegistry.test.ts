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
  isServedCodexModelMeta,
  isDeprecatedCodexModel,
  isResearchPreviewCodexModel,
  listCodexModels,
  filterCodexModelIds,
  resolveCodexModelAlias,
  type CodexModelMeta,
} from "../../../cli/adapters/codexModelRegistry.js";

describe("codexModelRegistry", () => {
  describe("CODEX_TIER_MODEL_MAP", () => {
    it("maps tiers to the verified current model ids", () => {
      expect(CODEX_TIER_MODEL_MAP).toEqual({
        haiku: "gpt-5.6-luna",
        sonnet: "gpt-5.6-terra",
        opus: "gpt-5.6-sol",
        fable: "gpt-5.6-sol",
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
      expect(resolveCodexModelAlias("haiku")).toBe("gpt-5.6-luna");
      expect(resolveCodexModelAlias("sonnet")).toBe("gpt-5.6-terra");
      expect(resolveCodexModelAlias("opus")).toBe("gpt-5.6-sol");
      expect(resolveCodexModelAlias("fable")).toBe("gpt-5.6-sol");
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
      expect(resolveCodexModelAlias("  opus  ")).toBe("gpt-5.6-sol");
    });

    it("maps Claude escalation ids by prefix, mirroring the Go adapter (#4021)", () => {
      // Must match resolveCodexModel in internal/execution/adapters/codex.go.
      expect(resolveCodexModelAlias("claude-haiku-4-5")).toBe("gpt-5.6-luna");
      expect(resolveCodexModelAlias("claude-sonnet-4-6")).toBe("gpt-5.6-terra");
      expect(resolveCodexModelAlias("claude-opus-4-8")).toBe("gpt-5.6-sol");
      // Prefix match is intentional (escalation ids are internally generated).
      expect(resolveCodexModelAlias("claude-sonnet-4-6-bad")).toBe("gpt-5.6-terra");
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

    it("every current openai entry is served over the codex transport today (#600 baseline)", () => {
      // No openai entry currently declares transports.<codex transport>.served:
      // false, so isValidCodexModel's existing behavior above is entirely
      // driven by known-id membership today. This pins that baseline so a
      // future entry that flips servedOverTransport to false is a deliberate,
      // reviewed change rather than a silent behavior shift.
      for (const [id, meta] of Object.entries(CODEX_MODELS)) {
        expect(meta.servedOverTransport, `${id} servedOverTransport`).toBe(true);
      }
    });
  });

  // Served-filtering (#600): no current openai registry entry has
  // transports.<codex transport>.served: false, so the served:false branch is
  // exercised here against SYNTHETIC CodexModelMeta data — mirrors the
  // hand-constructed-fixture pattern registry_axes_test.go's
  // TestValidateTransportsGraduated uses on the Go side.
  describe("served-transport filtering (#600)", () => {
    it("isServedCodexModelMeta is false only when servedOverTransport is explicitly false", () => {
      expect(isServedCodexModelMeta(undefined)).toBe(false);
      expect(isServedCodexModelMeta({ servedOverTransport: true })).toBe(true);
      expect(isServedCodexModelMeta({ servedOverTransport: false })).toBe(false);
      // A deprecated-but-served entry (the real gpt-5.3-codex shape) still
      // reads as served — deprecation and transport reachability are
      // independent axes, exactly like #579's grok-build-0.1 precedent.
      expect(isServedCodexModelMeta({ deprecated: true, servedOverTransport: true })).toBe(true);
    });

    it("filterCodexModelIds excludes an unserved entry by default and includes it with includeUnserved", () => {
      const synthetic: Record<string, CodexModelMeta> = {
        "gpt-served": { recommended: true, servedOverTransport: true },
        "gpt-unserved": { servedOverTransport: false },
      };
      expect(filterCodexModelIds(synthetic)).toEqual(["gpt-served"]);
      expect(filterCodexModelIds(synthetic)).not.toContain("gpt-unserved");

      const widened = filterCodexModelIds(synthetic, { includeUnserved: true });
      expect(widened).toContain("gpt-served");
      expect(widened).toContain("gpt-unserved");
    });

    it("filterCodexModelIds composes served-filtering with deprecated/research-preview filtering independently", () => {
      const synthetic: Record<string, CodexModelMeta> = {
        "gpt-clean": { servedOverTransport: true },
        "gpt-deprecated-served": { deprecated: true, servedOverTransport: true },
        "gpt-preview-unserved": { researchPreview: true, servedOverTransport: false },
      };
      expect(filterCodexModelIds(synthetic)).toEqual(["gpt-clean"]);
      expect(
        filterCodexModelIds(synthetic, { includeDeprecated: true, includeUnserved: true })
      ).toEqual(expect.arrayContaining(["gpt-clean", "gpt-deprecated-served"]));
      expect(
        filterCodexModelIds(synthetic, { includeDeprecated: true, includeUnserved: true })
      ).not.toContain("gpt-preview-unserved");
      expect(
        filterCodexModelIds(synthetic, {
          includeDeprecated: true,
          includeResearchPreview: true,
          includeUnserved: true,
        })
      ).toEqual(
        expect.arrayContaining(["gpt-clean", "gpt-deprecated-served", "gpt-preview-unserved"])
      );
    });
  });

  describe("listCodexModels", () => {
    it("excludes deprecated and research-preview by default, recommended first", () => {
      const list = listCodexModels();
      expect(list[0]).toBe("gpt-5.6-sol");
      expect(list).toContain("gpt-5.6-terra");
      expect(list).toContain("gpt-5.6-luna");
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

    it("defaults to the live registry (delegates to filterCodexModelIds(CODEX_MODELS, opts))", () => {
      expect(listCodexModels()).toEqual(filterCodexModelIds(CODEX_MODELS));
      expect(listCodexModels({ includeUnserved: true })).toEqual(
        filterCodexModelIds(CODEX_MODELS, { includeUnserved: true })
      );
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
