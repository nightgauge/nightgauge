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
 * running `extensionPath` when it is still present, otherwise the sibling that
 * VS Code records in `extensions.json`. When that record is absent, ambiguous,
 * or unusable, it falls back to the first sibling in glob order, matching the
 * Go and shell resolvers. This lets the pipeline keep resolving the binary and
 * skills across an update without a window reload.
 */

import { existsSync, readFileSync, readdirSync } from "fs";
import { basename, dirname, join } from "path";

const EXT_DIR_PREFIX = "nightgauge.nightgauge-vscode-";
const RECORDED_LOCATION_PATTERN =
  String.raw`"relativeLocation"[ \t\f\v\r]*:[ \t\f\v\r]*"(` +
  EXT_DIR_PREFIX.replace(/[.*+?^${}()|[\]\\]/g, "\\$&") +
  String.raw`[^"\n]*)"`;

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

  // The running directory was removed (auto-update GC). Enumerate surviving
  // sibling bundles in the same glob order used by Go's filepath.Glob.
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

  surviving.sort(compareBundlePaths);

  const recordedDir = readRecordedBundleDir(extensionsDir);
  if (recordedDir) {
    const recorded = surviving.find((candidate) => basename(candidate) === recordedDir);
    if (recorded) return recorded;
  }

  // Zero or multiple records, a missing index, or a recorded directory that
  // no longer exists all restore the pre-record first-glob-match fallback.
  return surviving[0];
}

function compareBundlePaths(a: string, b: string): number {
  const aName = basename(a);
  const bName = basename(b);
  return aName < bName ? -1 : aName > bName ? 1 : 0;
}

/**
 * Apply the same byte-oriented extraction contract as guard.sh and Go:
 * exactly one plain `relativeLocation` value is authoritative. This is a text
 * scan rather than JSON decoding so transient/truncated index files produce
 * the same answer in all three implementations.
 */
function readRecordedBundleDir(extensionsDir: string): string | undefined {
  let raw: string;
  try {
    // latin1 preserves every input byte one-to-one, matching LC_ALL=C grep and
    // Go's regexp over []byte even when the surrounding index is not UTF-8.
    raw = readFileSync(join(extensionsDir, "extensions.json")).toString("latin1");
  } catch {
    return undefined;
  }

  const pattern = new RegExp(RECORDED_LOCATION_PATTERN, "g");
  const first = pattern.exec(raw);
  if (!first || pattern.exec(raw)) return undefined;

  const recordedDir = first[1];
  return isPlainDirName(recordedDir) ? recordedDir : undefined;
}

function isPlainDirName(name: string): boolean {
  return (
    name !== "" && name !== "." && name !== ".." && !name.includes("..") && !/[/\\]/.test(name)
  );
}
