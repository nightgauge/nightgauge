#!/usr/bin/env node
// Generate THIRD_PARTY_NOTICES from the production dependency closure of the
// two shipped packages (the SDK npm tarball and the VS Code extension VSIX).
//
// The extension bundles its runtime deps into dist/extension.cjs via esbuild,
// and the SDK declares its runtime deps for the consumer to install — either
// way the shipped artifact carries third-party code, so we must attribute it.
//
// This walks `dependencies` (never `devDependencies`) transitively, resolves
// each package in the hoisted workspace node_modules, and concatenates each
// dependency's own LICENSE text. Output is written to the repo root (for the
// GoReleaser archive) and into each shipped package dir (for the VSIX and the
// npm tarball).
//
// No third-party tooling — deterministic Node stdlib only, so it runs anywhere
// `npm ci` has populated node_modules.

import fs from "node:fs";
import path from "node:path";
import crypto from "node:crypto";
import { fileURLToPath } from "node:url";

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const REPO_ROOT = path.resolve(__dirname, "..");
const ROOT_NODE_MODULES = path.join(REPO_ROOT, "node_modules");

// Packages whose runtime (production) dependencies are actually shipped.
const SHIPPED_PACKAGES = [
  path.join(REPO_ROOT, "packages", "nightgauge-sdk"),
  path.join(REPO_ROOT, "packages", "nightgauge-vscode"),
];

// Where to write the generated notice.
const OUTPUT_TARGETS = [
  path.join(REPO_ROOT, "THIRD_PARTY_NOTICES"),
  path.join(REPO_ROOT, "packages", "nightgauge-sdk", "THIRD_PARTY_NOTICES"),
  path.join(REPO_ROOT, "packages", "nightgauge-vscode", "THIRD_PARTY_NOTICES"),
];

const LICENSE_FILENAMES = [
  "LICENSE",
  "LICENSE.md",
  "LICENSE.txt",
  "LICENCE",
  "LICENCE.md",
  "LICENCE.txt",
  "COPYING",
  "COPYING.md",
  "UNLICENSE",
];

function readJson(file) {
  try {
    return JSON.parse(fs.readFileSync(file, "utf8"));
  } catch {
    return null;
  }
}

// Resolve a dependency directory by walking up from `fromDir` through
// node_modules, mirroring Node's resolution, and always falling back to the
// hoisted root node_modules where npm workspaces place most packages.
function resolvePackageDir(name, fromDir) {
  let dir = fromDir;
  // Walk up the tree checking each node_modules for `name`.
  while (true) {
    const candidate = path.join(dir, "node_modules", name);
    if (fs.existsSync(path.join(candidate, "package.json"))) return candidate;
    const parent = path.dirname(dir);
    if (parent === dir) break;
    dir = parent;
  }
  const rootCandidate = path.join(ROOT_NODE_MODULES, name);
  if (fs.existsSync(path.join(rootCandidate, "package.json"))) return rootCandidate;
  return null;
}

function findLicenseText(pkgDir) {
  let entries;
  try {
    entries = fs.readdirSync(pkgDir);
  } catch {
    return null;
  }
  // Exact known names first, then any file starting with LICEN/COPYING.
  for (const wanted of LICENSE_FILENAMES) {
    const hit = entries.find((e) => e.toLowerCase() === wanted.toLowerCase());
    if (hit) {
      try {
        return fs.readFileSync(path.join(pkgDir, hit), "utf8").trim();
      } catch {
        /* ignore */
      }
    }
  }
  const fuzzy = entries.find((e) => /^(licen|copying|unlicense)/i.test(e));
  if (fuzzy) {
    try {
      return fs.readFileSync(path.join(pkgDir, fuzzy), "utf8").trim();
    } catch {
      /* ignore */
    }
  }
  return null;
}

function licenseId(pkg) {
  if (typeof pkg.license === "string") return pkg.license;
  if (pkg.license && typeof pkg.license === "object" && pkg.license.type) return pkg.license.type;
  if (Array.isArray(pkg.licenses)) return pkg.licenses.map((l) => l.type || l).join(" OR ");
  return "UNKNOWN";
}

// BFS across the production dependency graph.
const collected = new Map(); // name -> { version, licenseId, licenseText }
const queue = [];

for (const pkgDir of SHIPPED_PACKAGES) {
  const pkg = readJson(path.join(pkgDir, "package.json"));
  if (!pkg) continue;
  for (const dep of Object.keys(pkg.dependencies || {})) {
    queue.push({ name: dep, from: pkgDir });
  }
}

const visited = new Set();
while (queue.length) {
  const { name, from } = queue.shift();
  // Skip our own workspace packages — their deps are seeded directly above and
  // their source is first-party (covered by the repo LICENSE/NOTICE).
  if (name.startsWith("@nightgauge/") || name === "nightgauge-vscode") {
    const dir = resolvePackageDir(name, from);
    if (dir) {
      const pkg = readJson(path.join(dir, "package.json"));
      for (const dep of Object.keys(pkg?.dependencies || {})) queue.push({ name: dep, from: dir });
    }
    continue;
  }
  const dir = resolvePackageDir(name, from);
  if (!dir) continue;
  const pkg = readJson(path.join(dir, "package.json"));
  if (!pkg) continue;
  const key = `${pkg.name}@${pkg.version}`;
  if (visited.has(key)) continue;
  visited.add(key);

  collected.set(pkg.name, {
    version: pkg.version,
    licenseId: licenseId(pkg),
    licenseText: findLicenseText(dir),
    homepage: pkg.homepage || (pkg.repository && (pkg.repository.url || pkg.repository)) || "",
  });

  for (const dep of Object.keys(pkg.dependencies || {})) {
    queue.push({ name: dep, from: dir });
  }
}

const names = [...collected.keys()].sort((a, b) => a.localeCompare(b));
const lockfileSha256 = crypto
  .createHash("sha256")
  .update(fs.readFileSync(path.join(REPO_ROOT, "package-lock.json")))
  .digest("hex");

const header = `THIRD-PARTY SOFTWARE NOTICES AND INFORMATION

Nightgauge redistributes, or depends at runtime on, the third-party open-source
components listed below. Each component remains under its own license; the full
license text follows each entry where the component ships one. This file is
generated by scripts/generate-third-party-notices.mjs — do not edit by hand.

Dependency lock SHA-256: ${lockfileSha256}
Components: ${names.length}

================================================================================
`;

const blocks = names.map((name) => {
  const info = collected.get(name);
  const lines = [`${name}@${info.version}`, `License: ${info.licenseId}`];
  if (info.homepage) lines.push(`Homepage: ${String(info.homepage).replace(/^git\+/, "")}`);
  lines.push("");
  if (info.licenseText) {
    lines.push(info.licenseText);
  } else {
    lines.push(`(No bundled license file found; declared license: ${info.licenseId})`);
  }
  lines.push("");
  lines.push("================================================================================");
  return lines.join("\n");
});

const output = header + "\n" + blocks.join("\n\n") + "\n";

if (names.length === 0) {
  console.error(
    "ERROR: generate-third-party-notices produced zero components — is node_modules installed (npm ci)?"
  );
  process.exit(1);
}

for (const target of OUTPUT_TARGETS) {
  fs.mkdirSync(path.dirname(target), { recursive: true });
  fs.writeFileSync(target, output, "utf8");
}

console.log(
  `Generated THIRD_PARTY_NOTICES (${names.length} components) → ${OUTPUT_TARGETS.map((t) => path.relative(REPO_ROOT, t)).join(", ")}`
);
