/**
 * fixtureContract.test.ts — the fixtures cannot disagree with the boundary.
 *
 * A hand-written fixture that disagrees with the API is how a renderer test
 * passes while the product is broken. Epic #741's tabs rendered perfectly
 * against fixtures that no longer described anything the platform sent.
 *
 * The recording procedure in PROVENANCE.md is the primary defence, but a
 * recording is a point-in-time act and the boundary keeps moving. This test is
 * the standing one: for every fixture the manifest binds to a Go struct, it
 * reads that struct's `json:"…"` tags out of the actual source the daemon
 * serialises from and asserts the fixture's key set matches —
 *
 *   - a key the boundary does not emit  → the fixture invented a field;
 *   - a non-`omitempty` tag the fixture lacks → the boundary emits a field the
 *     fixture (and therefore every assertion built on it) has never seen.
 *
 * Renaming a Go json tag turns this red. That is the point: without it, the
 * rename lands, the daemon starts sending a key nothing reads, and the arrival
 * tests keep passing against the old shape.
 *
 * @see Issue #746 — data-arrival test tier
 */

import { describe, it, expect } from "vitest";
import * as fs from "node:fs";
import * as path from "node:path";
import {
  ARRIVAL_FIXTURE_ROOT,
  readManifest,
  readFixtureRaw,
  type FixtureManifestEntry,
} from "./fixtures";

const REPO_ROOT = path.resolve(__dirname, "../../../..");

// ---------------------------------------------------------------------------
// A very small Go struct reader
// ---------------------------------------------------------------------------

interface GoField {
  jsonKey: string;
  omitempty: boolean;
}

/**
 * Extract the depth-1 json tags of `typeName` from a Go source file.
 *
 * Deliberately shallow: it tracks brace depth so an anonymous nested struct's
 * fields are skipped rather than silently flattened into the parent's key set
 * (CostAnalyticsResult.breakdown is the case that matters). Anything it cannot
 * find is an error, never an empty pass — a guard that quietly matches nothing
 * is the #539/#549 failure mode.
 */
function readGoStructFields(goFile: string, typeName: string): GoField[] {
  const full = path.join(REPO_ROOT, goFile);
  if (!fs.existsSync(full)) {
    throw new Error(`manifest references a Go file that does not exist: ${goFile}`);
  }
  const src = fs.readFileSync(full, "utf-8");
  const lines = src.split("\n");

  const openIdx = lines.findIndex((l) =>
    new RegExp(`^type\\s+${typeName}\\s+struct\\s*\\{`).test(l)
  );
  if (openIdx === -1) {
    throw new Error(
      `type ${typeName} not found in ${goFile} — the manifest's contract binding is stale.`
    );
  }

  const fields: GoField[] = [];
  let depth = 1;
  for (let i = openIdx + 1; i < lines.length && depth > 0; i++) {
    const line = lines[i];
    const opens = (line.match(/\{/g) ?? []).length;
    const closes = (line.match(/\}/g) ?? []).length;

    const depthAfter = depth + opens - closes;

    // A depth-1 field is one whose line sits at depth 1 either side of its own
    // braces. A multi-line anonymous struct carries its json tag on the line
    // that CLOSES it (`} \`json:"breakdown"\``, depth 2 → 1), so testing the
    // depth before the line alone would miss it and the parent would silently
    // lose a key it really does emit.
    if (Math.min(depth, depthAfter) === 1) {
      const tag = line.match(/`[^`]*json:"([^"]+)"/);
      if (tag) {
        const parts = tag[1].split(",");
        const key = parts[0];
        if (key && key !== "-") {
          fields.push({ jsonKey: key, omitempty: parts.includes("omitempty") });
        }
      }
    }
    depth = depthAfter;
  }

  if (fields.length === 0) {
    throw new Error(`type ${typeName} in ${goFile} yielded no json tags — the extractor is wrong.`);
  }
  return fields;
}

// ---------------------------------------------------------------------------
// Walking a fixture to the nodes a check applies to
// ---------------------------------------------------------------------------

/**
 * Resolve `at` (e.g. "", "[]", "entries/[]", "dimensions/[]/findings/[]") to
 * every object it selects. An empty selection is reported by the caller, not
 * swallowed — a path that matches nothing would otherwise assert nothing.
 */
function selectNodes(root: unknown, at: string): unknown[] {
  const segments = at.split("/").filter((s) => s.length > 0);
  let current: unknown[] = [root];
  for (const segment of segments) {
    const next: unknown[] = [];
    for (const node of current) {
      if (segment === "[]") {
        if (Array.isArray(node)) next.push(...node);
      } else if (node && typeof node === "object") {
        const child = (node as Record<string, unknown>)[segment];
        if (child !== undefined) next.push(child);
      }
    }
    current = next;
  }
  return current.filter((n) => n !== null && typeof n === "object" && !Array.isArray(n));
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

function listFixtureFiles(): string[] {
  const out: string[] = [];
  const walk = (dir: string, prefix: string) => {
    for (const entry of fs.readdirSync(dir, { withFileTypes: true })) {
      if (entry.isDirectory()) {
        walk(path.join(dir, entry.name), `${prefix}${entry.name}/`);
      } else if (entry.name.endsWith(".json") && `${prefix}${entry.name}` !== "manifest.json") {
        out.push(`${prefix}${entry.name}`);
      }
    }
  };
  walk(ARRIVAL_FIXTURE_ROOT, "");
  return out.sort();
}

describe("arrival fixture store (#746)", () => {
  const manifest = readManifest();

  it("every fixture file is declared in manifest.json, and vice versa", () => {
    const onDisk = listFixtureFiles();
    const declared = manifest.fixtures.map((f) => f.path).sort();
    // An undeclared fixture has no recorded provenance and no contract binding
    // — exactly the hand-written payload this tier exists to rule out.
    expect(onDisk).toEqual(declared);
  });

  it("every fixture parses and is non-empty", () => {
    for (const entry of manifest.fixtures) {
      const value = readFixtureRaw(entry.path);
      expect(value, `${entry.path} parsed to null`).not.toBeNull();
      if (Array.isArray(value)) {
        expect(value.length, `${entry.path} is an empty array`).toBeGreaterThan(0);
      } else {
        expect(
          Object.keys(value as Record<string, unknown>).length,
          `${entry.path} is an empty object`
        ).toBeGreaterThan(0);
      }
    }
  });

  it("a fixture without a Go contract says why", () => {
    for (const entry of manifest.fixtures) {
      if (entry.contract === null) {
        expect(entry.note, `${entry.path} has no contract and no note explaining it`).toBeTruthy();
      }
    }
  });

  const contracted: FixtureManifestEntry[] = manifest.fixtures.filter((f) => f.contract !== null);

  it.each(contracted.map((f) => [f.path, f] as const))(
    "%s matches the Go struct that serialises it",
    (_label, entry) => {
      const fixture = readFixtureRaw(entry.path);
      const contract = entry.contract!;

      for (const check of contract.checks) {
        const fields = readGoStructFields(contract.goFile, check.type);
        const allowed = new Set(fields.map((f) => f.jsonKey));
        const required = fields.filter((f) => !f.omitempty).map((f) => f.jsonKey);

        const nodes = selectNodes(fixture, check.at);
        expect(
          nodes.length,
          `${entry.path}: path "${check.at}" selected no objects — the check asserts nothing`
        ).toBeGreaterThan(0);

        for (const node of nodes) {
          const keys = Object.keys(node as Record<string, unknown>);

          const unknownKeys = keys.filter((k) => !allowed.has(k));
          expect(
            unknownKeys,
            `${entry.path} at "${check.at}": key(s) ${JSON.stringify(unknownKeys)} are not emitted ` +
              `by ${check.type} in ${contract.goFile}. Re-record the fixture (PROVENANCE.md) ` +
              `or fix the manifest binding.`
          ).toEqual([]);

          const missing = required.filter((k) => !keys.includes(k));
          expect(
            missing,
            `${entry.path} at "${check.at}": ${check.type} always emits ${JSON.stringify(missing)} ` +
              `(no omitempty), but the fixture omits it — the fixture describes a response the ` +
              `boundary never sends.`
          ).toEqual([]);
        }
      }
    }
  );
});
