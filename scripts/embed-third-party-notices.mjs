#!/usr/bin/env node
// Prepend the generated THIRD_PARTY_NOTICES into the extension bundle as a
// preserved license banner. esbuild inlines every runtime dependency into
// dist/extension.cjs, but a package's license lives in its LICENSE file (not an
// `@license` source banner), so esbuild's own legal-comments pass never carries
// the actual license *text* into the bundle. Embedding the notices here makes
// the shipped bundle self-attributing: the MIT/BSD/ISC/etc. texts of everything
// it contains travel with it.
//
// Idempotent and cwd-independent. Runs after `build:bundle` in the extension's
// build chain; regenerates notices first if a standalone build skipped the root.

import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { execFileSync } from "node:child_process";

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const REPO_ROOT = path.resolve(__dirname, "..");
const PKG_DIR = path.join(REPO_ROOT, "packages", "nightgauge-vscode");
const NOTICES = path.join(PKG_DIR, "THIRD_PARTY_NOTICES");
const BUNDLE = path.join(PKG_DIR, "dist", "extension.cjs");
const MARKER = "NIGHTGAUGE:THIRD_PARTY_NOTICES";

if (!fs.existsSync(NOTICES)) {
  execFileSync(process.execPath, [path.join(__dirname, "generate-third-party-notices.mjs")], {
    stdio: "inherit",
  });
}

if (!fs.existsSync(BUNDLE)) {
  console.error(`ERROR: ${BUNDLE} not found — run build:bundle before embedding notices.`);
  process.exit(1);
}

const bundle = fs.readFileSync(BUNDLE, "utf8");
if (bundle.includes(MARKER)) {
  console.log("THIRD_PARTY_NOTICES already embedded in extension.cjs — skipping.");
  process.exit(0);
}

const notices = fs.readFileSync(NOTICES, "utf8");
// Neutralize any `*/` in a license body so the block comment cannot close early.
const safe = notices.replace(/\*\//g, "*\\/");
const banner = `/*! ${MARKER}\n${safe}\n*/\n`;
fs.writeFileSync(BUNDLE, banner + bundle, "utf8");

const grants = (banner.match(/permission is hereby granted/gi) || []).length;
console.log(
  `Embedded THIRD_PARTY_NOTICES into extension.cjs (${grants} MIT-style grant clauses carried into the bundle).`
);
