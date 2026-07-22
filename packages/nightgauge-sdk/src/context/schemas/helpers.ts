import { z } from "zod";

/**
 * Deterministic alias map for known AI agent output deviations.
 *
 * Agents frequently truncate past-tense enum values. Each entry maps a
 * known agent variant to its canonical schema value. New patterns should
 * be added explicitly — never use heuristic or fuzzy matching.
 *
 * When an alias fires at runtime, it is logged so we can track deviation
 * rates and improve SKILL.md vocabulary instructions.
 */
const AGENT_ALIASES: Record<string, string> = {
  pass: "passed",
  fail: "failed",
  skip: "skipped",
  // Routing change_type aliases — agents frequently use verbose or alternate forms
  code_change: "code",
  code_modification: "code",
  documentation: "docs",
  doc: "docs",
  configuration: "config",
  conf: "config",
  // Routing suggested_route aliases — agents use verbose or abbreviated forms
  trivial_route: "trivial",
  quick: "trivial",
  simple: "trivial",
  extensive_route: "extensive",
  complex: "extensive",
  deep: "extensive",
};

/**
 * Creates a Zod enum with deterministic normalization for common AI agent
 * output deviations.
 *
 * Defense-in-depth layer 2 (layer 1 is SKILL.md vocabulary instructions):
 * 1. Hyphen/underscore substitution: "not-run" → "not_run"
 * 2. Explicit alias map: "pass" → "passed", "fail" → "failed", etc.
 *
 * All normalizations are deterministic and auditable — no heuristic or
 * fuzzy matching. Unknown values pass through unchanged and are caught
 * by normal Zod validation.
 *
 * @example
 *   flexEnum(['passed', 'failed', 'not_run'])
 *   // Accepts: "passed", "failed", "not_run", "not-run", "pass", "fail"
 */
export function flexEnum<T extends readonly [string, ...string[]]>(
  values: T
): z.ZodType<T[number]> {
  return z.preprocess(
    (val) => {
      if (typeof val !== "string") return val;
      // Step 1: normalize hyphens to underscores
      let normalized = val.replace(/-/g, "_");
      // Step 2: exact match — return as-is
      if ((values as readonly string[]).includes(normalized)) return normalized;
      // Step 3: check deterministic alias map
      const alias = AGENT_ALIASES[normalized];
      if (alias && (values as readonly string[]).includes(alias)) {
        return alias;
      }
      return normalized;
    },
    z.enum([...values] as [...T])
  ) as z.ZodType<T[number]>;
}

/**
 * Field-name aliases observed in real pattern-mining subagent output.
 *
 * The subagent's natural instinct is to write `{name, location, description}`
 * rather than the schema's `{pattern_type, example_implementations, pattern}`.
 * Rather than fight this with increasingly-strict SKILL.md examples (which
 * drift — see #2616 and PR #2702, where the warnings recurred within hours),
 * we normalize at the schema boundary. The first key present wins; later
 * keys are ignored.
 *
 * See `DiscoveredPatternSchema` in `pattern-mining.ts` for the consumer.
 */
const DISCOVERED_PATTERN_ALIASES: Record<string, readonly string[]> = {
  pattern_type: ["pattern_type", "type", "name"],
  category: ["category", "subtype", "sub_type"],
  pattern: ["pattern", "description", "summary"],
  evidence: ["evidence", "evidence_files", "files"],
  frequency: ["frequency", "count", "occurrences"],
  example_implementations: ["example_implementations", "examples", "locations", "location"],
};

/**
 * Preprocessor for individual `patterns_found[]` entries — maps common LLM
 * field-name variants to the canonical schema shape and coerces a bare
 * string `location`/`evidence` into a one-element array.
 *
 * Untouched fields pass through unchanged so downstream `.optional()`
 * handling can accept genuinely-missing data without warnings.
 */
export function normalizeDiscoveredPattern(raw: unknown): unknown {
  if (!raw || typeof raw !== "object" || Array.isArray(raw)) return raw;
  const src = raw as Record<string, unknown>;
  const out: Record<string, unknown> = { ...src };
  for (const [canonical, aliases] of Object.entries(DISCOVERED_PATTERN_ALIASES)) {
    if (out[canonical] !== undefined) continue;
    for (const alias of aliases) {
      if (alias === canonical) continue;
      if (src[alias] !== undefined) {
        out[canonical] = src[alias];
        break;
      }
    }
  }
  if (typeof out.example_implementations === "string") {
    out.example_implementations = [out.example_implementations];
  }
  if (typeof out.evidence === "string") {
    out.evidence = [out.evidence];
  }
  return out;
}

/**
 * Canonical key aliases for `pattern_classifications` objects.
 *
 * LLM agents frequently use abbreviated or camelCase key names instead of the
 * canonical snake_case keys. This map normalizes all known variants to the
 * four canonical keys required by PlanningContextSchema.
 */
const PATTERN_CLASSIFICATION_ALIASES: Record<string, string> = {
  naming: "naming_conventions",
  name_conventions: "naming_conventions",
  namingConventions: "naming_conventions",
  structural: "structural_patterns",
  structure: "structural_patterns",
  structuralPatterns: "structural_patterns",
  interface: "interface_patterns",
  interfaces: "interface_patterns",
  interfacePatterns: "interface_patterns",
  idiom: "idioms",
  idiomatic: "idioms",
};

const PATTERN_CLASSIFICATION_CANONICAL = new Set([
  "naming_conventions",
  "structural_patterns",
  "interface_patterns",
  "idioms",
]);

/**
 * Preprocessor for `pattern_classifications` in PlanningContextSchema.
 *
 * Handles these agent output variants:
 * - Array of strings: `["naming_conventions", "structural"]` → zero-fill object
 * - Array of `key:count` strings: `["naming:2", "structural:3"]` → parsed object
 * - Wrong key names: `{naming: 2, structural: 3}` → mapped via alias table
 * - Correct object: passed through unchanged
 *
 * All normalizations are deterministic — no fuzzy matching.
 */
export function normalizePatternClassifications(raw: unknown): unknown {
  const zero = {
    naming_conventions: 0,
    structural_patterns: 0,
    interface_patterns: 0,
    idioms: 0,
  };

  if (Array.isArray(raw)) {
    const out = { ...zero };
    for (const item of raw) {
      if (typeof item !== "string") continue;
      // Handle "key:count" form
      const colonIdx = item.indexOf(":");
      if (colonIdx !== -1) {
        const key = item.slice(0, colonIdx).trim();
        const count = parseInt(item.slice(colonIdx + 1).trim(), 10);
        const canonical =
          PATTERN_CLASSIFICATION_ALIASES[key] ??
          (PATTERN_CLASSIFICATION_CANONICAL.has(key) ? key : null);
        if (canonical && !isNaN(count)) {
          (out as Record<string, number>)[canonical] = count;
        }
      } else {
        // Plain string — mark as 1 occurrence
        const canonical =
          PATTERN_CLASSIFICATION_ALIASES[item] ??
          (PATTERN_CLASSIFICATION_CANONICAL.has(item) ? item : null);
        if (canonical) {
          (out as Record<string, number>)[canonical] =
            ((out as Record<string, number>)[canonical] ?? 0) + 1;
        }
      }
    }
    return out;
  }

  if (raw && typeof raw === "object" && !Array.isArray(raw)) {
    const src = raw as Record<string, unknown>;

    // If all four canonical keys are already present, pass through unchanged
    const hasAllCanonical = [...PATTERN_CLASSIFICATION_CANONICAL].every((k) => k in src);
    if (hasAllCanonical) return raw;

    // Remap non-canonical keys via alias table
    const out: Record<string, unknown> = { ...zero };
    for (const [srcKey, val] of Object.entries(src)) {
      const canonical =
        PATTERN_CLASSIFICATION_ALIASES[srcKey] ??
        (PATTERN_CLASSIFICATION_CANONICAL.has(srcKey) ? srcKey : null);
      if (canonical) {
        out[canonical] = val;
      } else {
        out[srcKey] = val; // pass through unknown keys for .passthrough()
      }
    }
    return out;
  }

  return raw;
}

/**
 * Optional string field that treats empty/whitespace-only strings as null.
 *
 * LLM agents routinely emit "" as a semantic equivalent of "not applicable",
 * which is indistinguishable from null for an optional field. This preprocess
 * collapses "" and whitespace-only strings to null before validation, so
 * schemas can use a single `.nullish()` without also needing `.min(1)`.
 */
export const optionalString = (inner: z.ZodString = z.string()) =>
  z.preprocess((v) => {
    if (typeof v === "string" && v.trim() === "") return null;
    return v;
  }, inner.nullish());
