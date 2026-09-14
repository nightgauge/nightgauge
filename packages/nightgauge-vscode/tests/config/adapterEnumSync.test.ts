/**
 * Guard against adapter-enum drift between the single source of truth
 * (AdapterEnumSchema) and runtime parsing. Pipeline UI surfaces intentionally
 * expose only the subset with agentic tool loops.
 *
 * The `nightgauge.core.adapter` VS Code setting was a third drift surface until
 * it was deleted as unread (#968) — it reached no code, so guarding its enum
 * guarded nothing. The remaining surfaces are the settings webview matrix and
 * the runtime resolver, both of which are live.
 *
 * @see Issue #4030 - settings adapter-enum drift
 * @see Issue #1623 - collapsed four hand-spelled adapter unions into one
 */

import { describe, it, expect } from "vitest";
import * as fs from "node:fs";
import * as path from "node:path";
import { fileURLToPath } from "node:url";
import { AdapterEnumSchema, DefaultModelSchema } from "../../src/config/schema";
import { STAGE_ADAPTER_OPTIONS, STAGE_MODEL_OPTIONS } from "../../src/views/settings/SettingsHtml";
import { VALID_ADAPTERS } from "../../src/utils/resolvers/modelResolver";

const CANONICAL = [...AdapterEnumSchema.options].sort();
const AGENTIC_PIPELINE_ADAPTERS = [
  "claude",
  "codex",
  "copilot",
  "gemini",
  "grok",
  "opencode",
].sort();

const SRC_ROOT = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "../../src");

/**
 * Files that used to hand-spell their own copy of the adapter union
 * (`"claude" | "codex" | "gemini" | ...`) instead of deriving it from
 * `AdapterEnumSchema`. A literal union of adapter-id string constants in any
 * of these files means a fifth copy has crept back in, silently able to drift
 * from the canonical enum the way coreSettings.ts, schema.ts and
 * modelResolver.ts each independently did before #1623.
 */
const NO_HAND_SPELLED_UNION_FILES = [
  "config/coreSettings.ts",
  "config/schema.ts",
  "utils/resolvers/modelResolver.ts",
];

// Matches a quoted-string union with at least two members, e.g.
// `"claude" | "codex" | "gemini"` — the shape of a hand-spelled adapter enum.
// Requires "claude" AND "codex" to appear as members so unrelated two-member
// strings unions elsewhere in these files don't false-positive.
const HAND_SPELLED_UNION_RE = /"claude"\s*\|\s*"[a-z-]+"(?:\s*\|\s*"[a-z-]+")+/;

describe("adapter enum sync (#4030)", () => {
  it("the per-stage matrix dropdown exposes only agentic adapters", () => {
    const matrixAdapters = STAGE_ADAPTER_OPTIONS.map((o) => o.value)
      .filter((v) => v !== "")
      .sort();
    expect(matrixAdapters).toEqual(AGENTIC_PIPELINE_ADAPTERS);
  });

  it("runtime VALID_ADAPTERS == AdapterEnumSchema (no silent-drop drift, #4030)", () => {
    // The schema comment warns that a drift here silently drops a user's adapter
    // selection to the default. ollama was the historical gap; opencode (#1623)
    // is the current one being guarded.
    expect([...VALID_ADAPTERS].sort()).toEqual(CANONICAL);
    expect(VALID_ADAPTERS).toContain("ollama");
    expect(VALID_ADAPTERS).toContain("opencode");
  });

  it("no hand-spelled adapter union literal remains in the collapsed files (#1623)", () => {
    // Before #1623, coreSettings.ts, schema.ts (ExecutionAdapterSchema) and
    // modelResolver.ts each spelled their own `"claude" | "codex" | ...`
    // union independently of AdapterEnumSchema. All three now derive
    // `ExecutionAdapter` via `z.infer<typeof AdapterEnumSchema>` instead.
    // This scan fails if a literal union creeps back into any of them.
    for (const relativePath of NO_HAND_SPELLED_UNION_FILES) {
      const contents = fs.readFileSync(path.join(SRC_ROOT, relativePath), "utf-8");
      expect(
        HAND_SPELLED_UNION_RE.test(contents),
        `${relativePath} appears to contain a hand-spelled adapter union literal`
      ).toBe(false);
    }
  });

  it("per-stage model dropdown tiers == DefaultModelSchema (no silent-drop drift)", () => {
    // A tier offered in the UI but missing from the resolver's allow-list/regex
    // would silently no-op (the `fable` gap). Guard tier parity both ways.
    const uiTiers = STAGE_MODEL_OPTIONS.map((o) => o.value)
      .filter((v) => v !== "")
      .sort();
    expect(uiTiers).toEqual([...DefaultModelSchema.options].sort());
  });

  it("Codex is selectable in the canonical enum and the per-stage matrix", () => {
    expect(AdapterEnumSchema.options).toContain("codex");
    expect(STAGE_ADAPTER_OPTIONS.some((o) => o.value === "codex")).toBe(true);
  });
});
