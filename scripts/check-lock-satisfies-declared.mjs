#!/usr/bin/env node
// Declared-versus-locked gate (#3268).
//
// Fails when a manifest's declared semver range is not satisfied by the
// version package-lock.json installs for it. A root `overrides` entry once
// pinned @anthropic-ai/sdk to 0.x while the SDK workspace declared ^1.x, so
// eight Dependabot bumps changed the manifest only and merged green.
//
// Manifests are read from disk, never from the lock's copy (which can go
// stale). Resolution follows Node: <ws>/node_modules/<name>, then the root
// node_modules/<name>.
//
// Exit 0: every range satisfied. Exit 1: a mismatch. Exit 2 (HARNESS ERROR):
// the lock or a manifest could not be read.
//
// Usage: node scripts/check-lock-satisfies-declared.mjs [repo-root]
import { readFileSync, existsSync } from "node:fs";
import { join, resolve } from "node:path";
import { createRequire } from "node:module";

const root = resolve(process.argv[2] ?? process.cwd());
const require = createRequire(import.meta.url);

function harness(msg) {
  console.error(`HARNESS ERROR: ${msg}`);
  process.exit(2);
}

let semver;
try {
  semver = require("semver");
} catch (err) {
  harness(`semver module unavailable (${err.message}); run npm ci`);
}

function readJSON(path) {
  try {
    return JSON.parse(readFileSync(path, "utf8"));
  } catch (err) {
    harness(`cannot read ${path}: ${err.message}`);
  }
}

const lock = readJSON(join(root, "package-lock.json"));
const pkgs = lock.packages;
if (!pkgs || typeof pkgs !== "object") harness("package-lock.json has no packages map");

const rootManifest = readJSON(join(root, "package.json"));
const workspaceDirs = [""];
for (const key of Object.keys(pkgs)) {
  if (key && !key.includes("node_modules/") && !key.startsWith("node_modules")) {
    if (existsSync(join(root, key, "package.json"))) workspaceDirs.push(key);
  }
}

const SECTIONS = ["dependencies", "devDependencies", "optionalDependencies"];
const failures = [];
let checked = 0;

for (const dir of workspaceDirs) {
  const manifest = dir === "" ? rootManifest : readJSON(join(root, dir, "package.json"));
  for (const section of SECTIONS) {
    for (const [name, spec] of Object.entries(manifest[section] ?? {})) {
      if (typeof spec !== "string" || spec.trim() === "*" || spec.trim() === "") continue;
      if (!semver.validRange(spec)) continue; // file:, workspace:, git, URLs, npm: aliases
      const candidates = dir === "" ? [] : [`${dir}/node_modules/${name}`];
      candidates.push(`node_modules/${name}`);
      const key = candidates.find((k) => pkgs[k]);
      if (!key) {
        if (section === "optionalDependencies") continue;
        failures.push(`${dir || "(root)"}: ${name} ${spec} is not in package-lock.json`);
        continue;
      }
      const node = pkgs[key];
      if (node.link) continue; // a workspace link, not a registry install
      checked++;
      if (!node.version || !semver.satisfies(node.version, spec, { includePrerelease: true })) {
        failures.push(
          `${dir || "(root)"}: ${name} declares ${spec} but the lock installs ${node.version} (${key})`
        );
      }
    }
  }
}

if (failures.length) {
  console.error("Declared ranges not satisfied by package-lock.json:");
  for (const f of failures) console.error(`  ${f}`);
  console.error("Look for a root `overrides` entry capping the package, then run npm install.");
  process.exit(1);
}
console.log(`OK: ${checked} declared ranges satisfied by package-lock.json`);
