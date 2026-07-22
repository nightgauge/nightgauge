#!/usr/bin/env tsx
/**
 * Generates the package.json "contributes" section from src/manifest/index.ts.
 *
 * Usage: npx tsx scripts/generate-package-contributions.ts [--check]
 *
 * --check  Dry-run: validate only, fail with exit code 1 if output differs.
 *          Used by CI to detect out-of-sync manifests.
 */

import { readFileSync, writeFileSync, realpathSync } from "node:fs";
import { execSync } from "node:child_process";
import { resolve, dirname } from "node:path";
import { fileURLToPath } from "node:url";
import { MANIFEST_CONTRIBUTES } from "../src/manifest/index.js";
import type {
  CommandContribution,
  ViewContribution,
  ManifestContributes,
} from "../src/manifest/types.js";

const __filename = fileURLToPath(import.meta.url);
const __dirname = dirname(__filename);
const PKG_DIR = resolve(__dirname, "..");
const PKG_PATH = resolve(PKG_DIR, "package.json");

// --- Validation functions (exported for testing) ---

export function detectDuplicateCommandIds(commands: CommandContribution[]): string[] {
  const seen = new Map<string, number>();
  const duplicates: string[] = [];
  for (const cmd of commands) {
    const count = (seen.get(cmd.command) ?? 0) + 1;
    seen.set(cmd.command, count);
    if (count === 2) duplicates.push(cmd.command);
  }
  return duplicates;
}

export function detectDuplicateViewIds(views: Record<string, ViewContribution[]>): string[] {
  const seen = new Map<string, number>();
  const duplicates: string[] = [];
  for (const viewList of Object.values(views)) {
    for (const view of viewList) {
      const count = (seen.get(view.id) ?? 0) + 1;
      seen.set(view.id, count);
      if (count === 2) duplicates.push(view.id);
    }
  }
  return duplicates;
}

export function collectAllWhenClauses(manifest: ManifestContributes): string[] {
  const clauses: string[] = [];

  for (const viewList of Object.values(manifest.views)) {
    for (const view of viewList) {
      if (view.when) clauses.push(view.when);
    }
  }
  for (const vw of manifest.viewsWelcome) {
    if (vw.when) clauses.push(vw.when);
  }
  for (const menuItems of Object.values(manifest.menus)) {
    for (const item of menuItems) {
      if (item.when) clauses.push(item.when);
    }
  }
  for (const kb of manifest.keybindings) {
    if (kb.when) clauses.push(kb.when);
  }

  return clauses;
}

/**
 * Extract context keys from file content using regex patterns.
 * Exported for unit testing.
 */
export function extractSetContextKeysFromContent(content: string): string[] {
  const patterns = [
    // Direct: setContext('key' or setContext("key"
    /setContext\s*\(\s*['"]([^'"]+)['"]/g,
    // executeCommand: 'setContext', 'key' (same or next line)
    /setContext['"]\s*,\s*['"]([^'"]+)['"]/g,
    // Catch-all: any 'nightgauge.*' string literal in files that use
    // setContext. Covers indirect patterns where keys are stored in maps or
    // variables before being passed to executeCommand. False positives (e.g.
    // command IDs) are harmless — they won't appear in when clauses.
    /['"]nightgauge\.[a-zA-Z0-9_.]+['"]/g,
  ];

  const keys = new Set<string>();
  for (const pattern of patterns) {
    pattern.lastIndex = 0;
    let m: RegExpExecArray | null;
    while ((m = pattern.exec(content)) !== null) {
      // For the catch-all pattern (group 0 only), strip surrounding quotes
      const key = m[1] ?? m[0].slice(1, -1);
      keys.add(key);
    }
  }
  return [...keys].sort();
}

/**
 * Find all files under srcDir that contain setContext, read their full
 * contents, and extract context key strings. Handles both direct calls
 * and indirect patterns (keys in maps/variables).
 */
export function extractSetContextKeys(srcDir: string): string[] {
  let fileList: string;
  try {
    fileList = execSync('grep -r -l "setContext" --include="*.ts" .', {
      cwd: srcDir,
      encoding: "utf8",
    });
  } catch {
    return [];
  }

  const allKeys = new Set<string>();
  for (const file of fileList.trim().split("\n").filter(Boolean)) {
    const filePath = resolve(srcDir, file);
    const content = readFileSync(filePath, "utf8");
    for (const key of extractSetContextKeysFromContent(content)) {
      allKeys.add(key);
    }
  }
  return [...allKeys].sort();
}

export function extractWhenClauseTokens(clauses: string[]): string[] {
  const tokenRegex = /nightgauge\.[a-zA-Z0-9_.]+/g;
  const tokens = new Set<string>();
  for (const clause of clauses) {
    let m: RegExpExecArray | null;
    while ((m = tokenRegex.exec(clause)) !== null) {
      tokens.add(m[0]);
    }
  }
  return [...tokens].sort();
}

export function detectOrphanedWhenClauses(
  manifest: ManifestContributes,
  knownKeys: string[]
): string[] {
  const clauses = collectAllWhenClauses(manifest);
  const tokens = extractWhenClauseTokens(clauses);
  const knownSet = new Set(knownKeys);

  // Also consider well-known VSCode-provided context keys and view IDs
  // View IDs used as "view == <id>" are not context keys set via setContext
  const viewIds = new Set<string>();
  for (const viewList of Object.values(manifest.views)) {
    for (const view of viewList) {
      viewIds.add(view.id);
    }
  }

  return tokens.filter((token) => !knownSet.has(token) && !viewIds.has(token));
}

// --- Main script ---

function main(): void {
  const checkMode = process.argv.includes("--check");

  // 1. Validate: duplicate command IDs
  const dupCommands = detectDuplicateCommandIds(MANIFEST_CONTRIBUTES.commands);
  if (dupCommands.length > 0) {
    console.error(`ERROR: Duplicate command IDs found: ${dupCommands.join(", ")}`);
    process.exit(1);
  }

  // 2. Validate: duplicate view IDs
  const dupViews = detectDuplicateViewIds(MANIFEST_CONTRIBUTES.views);
  if (dupViews.length > 0) {
    console.error(`ERROR: Duplicate view IDs found: ${dupViews.join(", ")}`);
    process.exit(1);
  }

  // 3. Validate: orphaned when clauses (warning only)
  // Reads full file contents of files that use setContext to catch both
  // direct calls and indirect patterns (keys in maps/variables).
  const srcDir = resolve(PKG_DIR, "src");
  const knownKeys = extractSetContextKeys(srcDir);
  const orphaned = detectOrphanedWhenClauses(MANIFEST_CONTRIBUTES, knownKeys);
  if (orphaned.length > 0) {
    console.warn(`[WARNING] Potentially orphaned when clause context keys: ${orphaned.join(", ")}`);
  }

  // 4. Read package.json, replace contributes (preserve configuration)
  const pkgText = readFileSync(PKG_PATH, "utf8");
  const pkg = JSON.parse(pkgText);

  // Build the contributes object: manifest data + existing configuration
  const newContributes: Record<string, unknown> = {
    viewsContainers: MANIFEST_CONTRIBUTES.viewsContainers,
    views: MANIFEST_CONTRIBUTES.views,
    viewsWelcome: MANIFEST_CONTRIBUTES.viewsWelcome,
    commands: MANIFEST_CONTRIBUTES.commands,
    menus: MANIFEST_CONTRIBUTES.menus,
    keybindings: MANIFEST_CONTRIBUTES.keybindings,
  };

  // Preserve configuration — it is NOT managed by the manifest
  if (pkg.contributes?.configuration) {
    newContributes.configuration = pkg.contributes.configuration;
  }

  pkg.contributes = newContributes;

  const output = JSON.stringify(pkg, null, 2) + "\n";

  if (checkMode) {
    if (output !== pkgText) {
      console.error("ERROR: Generated contributions are out of sync with package.json.");
      console.error("Run 'npm run generate:contributions' and commit the result.");
      process.exit(1);
    }
    console.log("OK: package.json contributions are in sync with manifest.");
    return;
  }

  // 5. Write updated package.json
  writeFileSync(PKG_PATH, output, "utf8");
  console.log("Generated contributions written to package.json.");
  console.log(
    `  ${MANIFEST_CONTRIBUTES.commands.length} commands, ` +
      `${Object.values(MANIFEST_CONTRIBUTES.views).flat().length} views, ` +
      `${MANIFEST_CONTRIBUTES.viewsWelcome.length} viewsWelcome, ` +
      `${Object.values(MANIFEST_CONTRIBUTES.menus).reduce((a, b) => a + b.length, 0)} menu items, ` +
      `${MANIFEST_CONTRIBUTES.keybindings.length} keybindings`
  );
}

/**
 * True only when this module is the process entry point — i.e. it was invoked
 * directly (`tsx scripts/generate-package-contributions.ts [--check]`) rather
 * than imported. Compares real (symlink-resolved) paths so relative argv and
 * symlinked checkouts still match; any resolution error is treated as "not
 * direct" (fail-safe to NOT writing).
 *
 * Exported for unit testing: a bare top-level `main()` previously rewrote the
 * real package.json on *import*, so when parallel vitest workers imported this
 * module's helpers it truncated package.json mid-read in a sibling suite →
 * "File is empty" JSON crashes that were misdiagnosed as runner contention.
 */
export function isDirectInvocation(entry: string | undefined, selfPath: string): boolean {
  if (!entry) return false;
  try {
    return realpathSync(entry) === realpathSync(selfPath);
  } catch {
    return false;
  }
}

// Run main() ONLY on direct invocation, never on import (tests import helpers).
if (isDirectInvocation(process.argv[1], __filename)) {
  main();
}
