/**
 * extensionBundle — resilient resolution of the extension's on-disk bundle root.
 *
 * VSCode auto-updates an extension by installing a new versioned directory
 * (e.g. `nightgauge.nightgauge-vscode-0.1.1780370898`) and deleting
 * the previous one. A long-lived extension host keeps running against its
 * original `extensionPath`, but once that directory is garbage-collected every
 * bundle-relative lookup — the Go binary (`dist/bin/...`) and the bundled
 * pipeline skills (`dist/skills/...`) — fails its `existsSync` check. That
 * surfaces as "nightgauge binary not found" and "SKILL.md not found for
 * stage: ..." with no obvious cause, and (because the failure lands mid-run)
 * halts the autonomous queue. See #3883.
 *
 * `resolveExtensionBundleRoot` returns a bundle root that actually exists: the
 * running `extensionPath` when it is still present, otherwise the newest
 * sibling `nightgauge.nightgauge-vscode-*` directory that survives —
 * i.e. the version the auto-update just installed. This lets the pipeline keep
 * resolving the binary and skills across an update without a window reload.
 */

import { existsSync, readdirSync } from "fs";
import { basename, dirname, join } from "path";

const EXT_DIR_PREFIX = "nightgauge.nightgauge-vscode-";

/**
 * Resolve a bundle root that exists on disk, self-healing past a
 * garbage-collected extension directory.
 *
 * @param extensionPath the running extension's `extensionPath` (may be stale)
 * @returns a directory that exists, or `undefined` when none can be found
 */
export function resolveExtensionBundleRoot(extensionPath: string | undefined): string | undefined {
  if (!extensionPath) return undefined;
  if (existsSync(extensionPath)) return extensionPath;

  // The running directory was removed (auto-update GC). Look for the newest
  // installed sibling version that still exists.
  const extensionsDir = dirname(extensionPath);
  let surviving: string[];
  try {
    surviving = readdirSync(extensionsDir)
      .filter((name) => name.startsWith(EXT_DIR_PREFIX))
      .map((name) => join(extensionsDir, name))
      .filter((candidate) => candidate !== extensionPath && existsSync(candidate));
  } catch {
    // Cannot enumerate (permissions, missing dir) — fall back to the stale path
    // so callers behave exactly as before this helper existed.
    return extensionPath;
  }

  if (surviving.length === 0) return extensionPath;

  surviving.sort((a, b) => buildIdOf(b) - buildIdOf(a));
  return surviving[0];
}

/**
 * Extract the monotonic build id from a versioned extension directory name.
 * `nightgauge.nightgauge-vscode-0.1.1780370898` -> 1780370898.
 * Returns 0 when the suffix is not numeric so malformed names sort last.
 */
function buildIdOf(dirPath: string): number {
  const suffix = basename(dirPath).slice(EXT_DIR_PREFIX.length);
  const last = suffix.split(".").pop() ?? "0";
  const parsed = Number(last);
  return Number.isFinite(parsed) ? parsed : 0;
}
