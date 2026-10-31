/**
 * stableMarketplace - a marketplace directory that survives extension upgrades
 *
 * The extension directory carries its version in the name, so an upgrade removes
 * it. A marketplace registered from `dist/` then fails to load and every
 * Nightgauge skill and hook disappears. This copies the bundled marketplace to
 * globalStorage, which an upgrade keeps, and that copy is what gets registered.
 *
 * @see Issue #2719
 */

import * as fs from "fs";
import * as path from "path";
import type * as vscode from "vscode";

const STABLE_DIR = "claude-marketplace";
const VERSION_STAMP = ".nightgauge-version";
const BUNDLED_ENTRIES = [".claude-plugin", "claude-plugins"];

type MarketplaceContext = Pick<vscode.ExtensionContext, "extensionPath" | "globalStorageUri"> & {
  extension: { packageJSON: { version: string } };
};

export interface StableMarketplace {
  /** Directory holding `.claude-plugin/marketplace.json`. */
  path: string;
  /** True when this call replaced the copy, so a registered source has changed. */
  recopied: boolean;
}

function readStamp(dir: string): string | undefined {
  try {
    return fs.readFileSync(path.join(dir, VERSION_STAMP), "utf8").trim();
  } catch {
    return undefined;
  }
}

/** Replace `target` with `staged`. A concurrent window may land the same version first. */
function swapIn(staged: string, target: string, version: string): boolean {
  const displaced = `${staged}.old`;
  if (fs.existsSync(target)) {
    fs.renameSync(target, displaced);
  }
  try {
    fs.renameSync(staged, target);
    return true;
  } catch (error) {
    if (readStamp(target) === version) {
      return false;
    }
    if (fs.existsSync(displaced) && !fs.existsSync(target)) {
      fs.renameSync(displaced, target);
    }
    throw error;
  } finally {
    fs.rmSync(displaced, { recursive: true, force: true });
  }
}

/**
 * Keep the stable copy at this extension's version.
 *
 * The copy is staged beside the target and renamed into place, with the stamp
 * written last, so a crash never leaves a partial source that looks current.
 * Returns undefined when the VSIX has no bundle or the copy failed; callers then
 * use the configured marketplace URL.
 */
export function ensureStableMarketplace(
  context: MarketplaceContext,
  warn: (message: string) => void = () => undefined
): StableMarketplace | undefined {
  let staged: string | undefined;
  try {
    const dist = path.join(context.extensionPath, "dist");
    if (!fs.existsSync(path.join(dist, ".claude-plugin", "marketplace.json"))) {
      return undefined;
    }

    const root = context.globalStorageUri.fsPath;
    const target = path.join(root, STABLE_DIR);
    const version = context.extension.packageJSON.version;
    if (readStamp(target) === version) {
      return { path: target, recopied: false };
    }

    fs.mkdirSync(root, { recursive: true });
    staged = fs.mkdtempSync(path.join(root, `${STABLE_DIR}.tmp-`));
    for (const entry of BUNDLED_ENTRIES) {
      // verbatimSymlinks: a resolved link would point back into the extension
      // directory, which is the dependency this copy exists to remove.
      fs.cpSync(path.join(dist, entry), path.join(staged, entry), {
        recursive: true,
        verbatimSymlinks: true,
      });
    }
    fs.writeFileSync(path.join(staged, VERSION_STAMP), `${version}\n`);

    return { path: target, recopied: swapIn(staged, target, version) };
  } catch (error) {
    warn(
      `Could not copy the bundled marketplace to global storage: ${
        error instanceof Error ? error.message : String(error)
      }`
    );
    return undefined;
  } finally {
    if (staged) {
      fs.rmSync(staged, { recursive: true, force: true });
    }
  }
}

/** True when `source` sits under the extensions directory, which upgrades delete. */
export function isVersionKeyedSource(source: string, extensionPath: string): boolean {
  const relative = path.relative(path.dirname(extensionPath), source);
  return relative !== "" && !relative.startsWith("..") && !path.isAbsolute(relative);
}
