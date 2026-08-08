/**
 * terminalKind.predicateFields.test.ts
 *
 * The PREDICATE FIELD SURFACE — SDK half of a two-language pin (#306).
 *
 * The other guards on this module bound what it may COMPARE an error text
 * against: every string literal in terminalKind.ts is on an exact allowlist, and
 * every import is too. Neither says which model-registry FIELDS the one named
 * predicate may READ, and a widening there needs no literal and no new import:
 *
 *   if (m.provider && t.includes(m.provider.toLowerCase())) return true;
 *
 * is one line, and it makes all six clauses gated on `@mentions_registry_model`
 * fire for any text that merely names a vendor — `Anthropic API: this model is
 * not available on your current plan` becomes `model_unavailable`. Done
 * symmetrically in both languages it left the Go suite, the drift check, this
 * suite and the extension suite green, with `table.json`, the generated module
 * and `stress-golden.json` all byte-identical.
 *
 * So the read set is pinned behaviourally, from the SAME fixture Go uses
 * (internal/terminalkind/testdata/predicate-registry-poison.json): a synthetic
 * registry of one model whose every non-read field carries a unique sentinel.
 * The registry module is mocked with it, so the assertions run through the real
 * `classifyTerminalKind` rather than around it.
 *
 * The Go twin is internal/terminalkind/predicates_test.go. Both read this file;
 * neither can be widened without the other going red.
 */

import { describe, it, expect, vi } from "vitest";
import { readFileSync } from "node:fs";
import path from "node:path";

const REPO_ROOT = path.resolve(__dirname, "../../../../..");
const POISON_PATH = path.join(
  REPO_ROOT,
  "internal/terminalkind/testdata/predicate-registry-poison.json"
);
const REAL_REGISTRY_PATH = path.join(
  REPO_ROOT,
  "packages/nightgauge-sdk/src/eval/model-registry.json"
);

interface PoisonFixture {
  predicate: string;
  reads: string[];
  models: Record<string, unknown>[];
}

const poison: PoisonFixture = JSON.parse(readFileSync(POISON_PATH, "utf-8"));

// Hoisted above the imports by vitest, so the factory re-reads the fixture
// rather than closing over anything.
vi.mock("../../../src/eval/modelRegistry.js", async () => {
  const { readFileSync: read } = await import("node:fs");
  const p = await import("node:path");
  const file = p.resolve(
    __dirname,
    "../../../../../internal/terminalkind/testdata/predicate-registry-poison.json"
  );
  return { MODEL_REGISTRY: JSON.parse(read(file, "utf-8")).models };
});

const { classifyTerminalKind } = await import("../../../src/analysis/health/terminalKind.js");

/** Every key path in a decoded JSON value; array elements append `[]`. */
function keyPaths(value: unknown, prefix: string, out: Set<string>): void {
  if (Array.isArray(value)) {
    for (const item of value) keyPaths(item, prefix + "[]", out);
    return;
  }
  if (value && typeof value === "object") {
    for (const [k, v] of Object.entries(value as Record<string, unknown>)) {
      const p = prefix ? `${prefix}.${k}` : k;
      out.add(p);
      keyPaths(v, p, out);
    }
  }
}

/** Every string leaf, keyed by the path it was found at. */
function stringLeaves(value: unknown, prefix: string, out: Map<string, string>): void {
  if (typeof value === "string") {
    out.set(prefix, value);
    return;
  }
  if (Array.isArray(value)) {
    value.forEach((item, i) => stringLeaves(item, `${prefix}[${i}]`, out));
    return;
  }
  if (value && typeof value === "object") {
    for (const [k, v] of Object.entries(value as Record<string, unknown>)) {
      stringLeaves(v, prefix ? `${prefix}.${k}` : k, out);
    }
  }
}

/** The first path segment — the granularity `reads` is declared at. */
function topLevelOf(p: string): string {
  const i = p.search(/[.[]/);
  return i < 0 ? p : p.slice(0, i);
}

/**
 * A text the `usage limit AND @mentions_registry_model` clause claims if and
 * only if the predicate fires for `name`. Driving the assertion through the
 * classifier means it covers the predicate as the table actually uses it.
 */
function claimsAsModelUnavailable(name: string): boolean {
  return classifyTerminalKind(`usage limit reached for ${name}`) === "model_unavailable";
}

describe("the predicate's registry field surface", () => {
  const reads = new Set(poison.reads);
  const model = poison.models[0];

  it("declares every field the real registry has", () => {
    // A registry field this fixture has never heard of is a field the predicate
    // could start reading with nothing able to notice.
    const real = new Set<string>();
    const realFile: { models: unknown[] } = JSON.parse(readFileSync(REAL_REGISTRY_PATH, "utf-8"));
    for (const m of realFile.models) keyPaths(m, "", real);
    const declared = new Set<string>();
    for (const m of poison.models) keyPaths(m, "", declared);
    expect(
      [...real].filter((p) => !declared.has(p)).sort(),
      "the model registry has field(s) the poison fixture does not declare. Add them to " +
        "internal/terminalkind/testdata/predicate-registry-poison.json with a unique sentinel " +
        "value (or to `reads` if the predicate is meant to read them)."
    ).toEqual([]);
  });

  it("fires for no field outside the declared read set", () => {
    const leaves = new Map<string, string>();
    stringLeaves(model, "", leaves);

    const sentinels = new Map<string, string>();
    for (const [p, value] of leaves) {
      if (reads.has(topLevelOf(p))) continue;
      expect(value, `field ${p} carries an empty value; an empty sentinel pins nothing`).not.toBe(
        ""
      );
      expect(
        sentinels.has(value),
        `fields ${sentinels.get(value)} and ${p} share the sentinel ${JSON.stringify(value)} — ` +
          `a hit could not be attributed to one field`
      ).toBe(false);
      sentinels.set(value, p);
    }
    expect(
      sentinels.size,
      "too few sentinels derived from the fixture — the walk is broken and this guard now " +
        "checks almost nothing"
    ).toBeGreaterThanOrEqual(8);

    const fired = [...sentinels]
      .filter(([value]) => claimsAsModelUnavailable(value))
      .map(([value, p]) => `${p} (${JSON.stringify(value)})`);
    expect(
      fired.sort(),
      `the predicate reads registry field(s) that are not in its declared read set ` +
        `[${poison.reads.join(", ")}]. Every clause gated on @${poison.predicate} now claims any ` +
        `text containing that field's real value — for \`provider\` that is every message naming ` +
        `a vendor — with no string literal, no golden movement and no corpus row able to see it.`
    ).toEqual([]);
  });

  it("fires for every field in the declared read set", () => {
    // The exact-set's other direction: dropping a read is a live NARROWING of
    // every clause gated on the predicate, and would otherwise be silent here.
    const leaves = new Map<string, string>();
    stringLeaves(model, "", leaves);
    const fired = new Set<string>();
    for (const [p, value] of leaves) {
      const top = topLevelOf(p);
      if (!reads.has(top) || value === "") continue;
      if (claimsAsModelUnavailable(value)) fired.add(top);
    }
    expect(
      poison.reads.filter((r) => !fired.has(r)).sort(),
      "the predicate does NOT fire on the value of a declared read field. Either the read was " +
        "dropped, or the fixture no longer carries a value for it."
    ).toEqual([]);
  });
});
