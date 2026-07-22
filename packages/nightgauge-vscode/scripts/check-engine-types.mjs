#!/usr/bin/env node
// Guard the invariant `vsce package` enforces at packaging time, but which no
// build/test step exercises: the `@types/vscode` version must NOT exceed
// `engines.vscode`. If it does, the extension type-checks against an API
// surface newer than the minimum VSCode it claims to support — code compiles,
// then crashes at runtime on that minimum VSCode. `vsce package` refuses it
// with "greater than engines.vscode".
//
// This regressed via a Dependabot bump (#165) that raised `@types/vscode`
// 1.85 → 1.125 without touching `engines.vscode` (1.85). ci-local.sh and the
// `vscode` CI job never package, so it only surfaced in dev-install.sh. This
// script closes that gap — run it in CI so the mismatch fails loudly and
// locally instead of at install time.
//
// Zero dependencies (no semver): VSCode ships on the 1.x line, so a
// major/minor/patch tuple compare is exact and cannot break on a missing dep.

import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { dirname, join } from "node:path";

const pkgPath = join(dirname(fileURLToPath(import.meta.url)), "..", "package.json");
const pkg = JSON.parse(readFileSync(pkgPath, "utf8"));

const engineRange = pkg.engines?.vscode;
const typesRange = pkg.devDependencies?.["@types/vscode"] ?? pkg.dependencies?.["@types/vscode"];

if (!engineRange) {
  console.error("ERROR: engines.vscode is not declared in package.json.");
  process.exit(1);
}
if (!typesRange) {
  console.error("ERROR: @types/vscode is not declared in package.json.");
  process.exit(1);
}

// vsce treats `*`/`latest` engines as "any version" — nothing to compare.
if (engineRange.trim() === "*" || engineRange.trim() === "latest") {
  console.log(
    `✓ engines.vscode is "${engineRange}" (unbounded) — no @types/vscode ceiling to check.`
  );
  process.exit(0);
}

// Coerce a range like "^1.85.0", "~1.85", ">=1.85.0" to a [major, minor, patch]
// tuple, ignoring the range operator (vsce compares the concrete floor).
function coerce(range, label) {
  const m = /(\d+)\.(\d+)(?:\.(\d+))?/.exec(range);
  if (!m) {
    console.error(`ERROR: could not parse a version out of ${label} "${range}".`);
    process.exit(1);
  }
  return [Number(m[1]), Number(m[2]), Number(m[3] ?? 0)];
}

const engine = coerce(engineRange, "engines.vscode");
const types = coerce(typesRange, "@types/vscode");

// types > engine (lexicographic on the tuple) is the failure vsce rejects.
const cmp = types[0] - engine[0] || types[1] - engine[1] || types[2] - engine[2];

if (cmp > 0) {
  console.error(
    `ERROR: @types/vscode ${typesRange} is greater than engines.vscode ${engineRange}.`
  );
  console.error("       vsce package will reject this. Either lower @types/vscode to match");
  console.error("       the engine floor, or deliberately raise engines.vscode (a product");
  console.error("       decision — it drops support for older VSCode versions).");
  process.exit(1);
}

console.log(`✓ @types/vscode ${typesRange} <= engines.vscode ${engineRange}.`);
