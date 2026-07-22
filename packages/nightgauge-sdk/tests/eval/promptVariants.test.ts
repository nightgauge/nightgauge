/**
 * Tests for the prompt-variant axis primitives (#72): variant schema
 * validation, overlay application, cell-name resolution, and file loading.
 */

import { describe, it, expect } from "vitest";
import { mkdtemp, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import {
  PromptVariantSchema,
  applyPromptVariant,
  loadPromptVariants,
  resolveVariant,
  type PromptVariant,
} from "../../src/eval/promptVariants.js";

const PREAMBLE: PromptVariant = {
  name: "concise-preamble",
  description: "adds a brevity preamble",
  prepend: "Be concise. Prefer the smallest correct change.",
};

describe("PromptVariantSchema", () => {
  it("accepts a named overlay with at least one op", () => {
    expect(PromptVariantSchema.parse(PREAMBLE)).toEqual(PREAMBLE);
    expect(
      PromptVariantSchema.parse({
        name: "swap-wording",
        replacements: [{ find: "must", replace_with: "should" }],
      }).replacements
    ).toHaveLength(1);
  });

  it("rejects the reserved baseline name", () => {
    expect(() => PromptVariantSchema.parse({ name: "baseline", prepend: "x" })).toThrow(/reserved/);
  });

  it("rejects a variant with no overlay ops", () => {
    expect(() => PromptVariantSchema.parse({ name: "empty-variant" })).toThrow(
      /at least one overlay op/
    );
  });

  it("rejects non-kebab-case names", () => {
    expect(() => PromptVariantSchema.parse({ name: "Not Kebab", prepend: "x" })).toThrow(
      /kebab-case/
    );
  });
});

describe("applyPromptVariant", () => {
  const instruction = "Implement the cache. The cache must evict LRU entries.";

  it("returns the instruction untouched with no variant (baseline)", () => {
    expect(applyPromptVariant(instruction, undefined)).toBe(instruction);
  });

  it("prepends and appends around the instruction", () => {
    const out = applyPromptVariant(instruction, {
      name: "wrapped",
      prepend: "PREFIX",
      append: "SUFFIX",
    });
    expect(out.startsWith("PREFIX\n\n")).toBe(true);
    expect(out.endsWith("\n\nSUFFIX")).toBe(true);
    expect(out).toContain(instruction);
  });

  it("applies replacements (all occurrences, in order)", () => {
    const out = applyPromptVariant("a cache a cache", {
      name: "swap",
      replacements: [{ find: "cache", replace_with: "store" }],
    });
    expect(out).toBe("a store a store");
  });

  it("throws when a replacement target is absent — a silent no-op would fake a measurement", () => {
    expect(() =>
      applyPromptVariant(instruction, {
        name: "swap",
        replacements: [{ find: "NOT-PRESENT", replace_with: "x" }],
      })
    ).toThrow(/replacement target not found/);
  });
});

describe("resolveVariant", () => {
  const loaded = new Map([[PREAMBLE.name, PREAMBLE]]);

  it("maps baseline to undefined (no overlay) even with no map", () => {
    expect(resolveVariant(undefined, "baseline")).toBeUndefined();
    expect(resolveVariant(loaded, "baseline")).toBeUndefined();
  });

  it("resolves a loaded variant by name", () => {
    expect(resolveVariant(loaded, PREAMBLE.name)).toBe(PREAMBLE);
  });

  it("throws for an unloaded name — harness config error, not a model signal", () => {
    expect(() => resolveVariant(loaded, "unknown-variant")).toThrow(/not loaded/);
  });
});

describe("loadPromptVariants", () => {
  it("loads <name>.json files and skips the implicit baseline", async () => {
    const dir = await mkdtemp(join(tmpdir(), "variants-"));
    await writeFile(join(dir, "concise-preamble.json"), JSON.stringify(PREAMBLE));

    const loaded = await loadPromptVariants(dir, ["baseline", "concise-preamble"]);
    expect(loaded.size).toBe(1);
    expect(loaded.get("concise-preamble")).toEqual(PREAMBLE);
  });

  it("throws when the file's name field does not match its filename", async () => {
    const dir = await mkdtemp(join(tmpdir(), "variants-"));
    await writeFile(join(dir, "wrong-name.json"), JSON.stringify(PREAMBLE));

    await expect(loadPromptVariants(dir, ["wrong-name"])).rejects.toThrow(
      /must match its filename/
    );
  });

  it("throws for a missing variant file", async () => {
    const dir = await mkdtemp(join(tmpdir(), "variants-"));
    await expect(loadPromptVariants(dir, ["absent"])).rejects.toThrow(/cannot read/);
  });
});
