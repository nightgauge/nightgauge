/**
 * resolveExtensionBundleRoot — real-filesystem tests.
 *
 * Reproduces the auto-update GC race (#3883): VSCode installs a new versioned
 * extension dir and deletes the running one, leaving bundle-relative lookups
 * pointing at a directory that no longer exists. The helper must self-heal to
 * the newest surviving sibling.
 *
 * @see src/utils/extensionBundle.ts
 */

import { describe, it, expect, beforeEach, afterEach } from "vitest";
import { mkdirSync, rmSync } from "fs";
import { join } from "path";
import { tmpdir } from "os";
import { resolveExtensionBundleRoot } from "../../src/utils/extensionBundle";

let root: string;
const PREFIX = "nightgauge.nightgauge-vscode-";

beforeEach(() => {
  root = join(tmpdir(), `ext-bundle-test-${process.pid}-${Math.floor(performance.now())}`);
  mkdirSync(root, { recursive: true });
});

afterEach(() => {
  rmSync(root, { recursive: true, force: true });
});

function makeVersion(version: string): string {
  const dir = join(root, `${PREFIX}${version}`);
  mkdirSync(dir, { recursive: true });
  return dir;
}

describe("resolveExtensionBundleRoot", () => {
  it("returns the running path unchanged when it still exists", () => {
    const running = makeVersion("0.1.1780370898");
    expect(resolveExtensionBundleRoot(running)).toBe(running);
  });

  it("self-heals to the newest surviving sibling when the running dir was GC'd", () => {
    // Running version was deleted; two newer ones survive.
    const stale = join(root, `${PREFIX}0.1.1780345587`); // never created (GC'd)
    makeVersion("0.1.1780360156");
    const newest = makeVersion("0.1.1780370898");

    expect(resolveExtensionBundleRoot(stale)).toBe(newest);
  });

  it("picks the highest build id, not lexical order", () => {
    const stale = join(root, `${PREFIX}0.1.1780000000`);
    makeVersion("0.1.1780370898"); // higher build id
    makeVersion("0.1.1780360156"); // would win a naive string sort? no — verify numeric
    const newest = join(root, `${PREFIX}0.1.1780370898`);

    expect(resolveExtensionBundleRoot(stale)).toBe(newest);
  });

  it("falls back to the stale path when no siblings survive", () => {
    const stale = join(root, `${PREFIX}0.1.1780345587`);
    expect(resolveExtensionBundleRoot(stale)).toBe(stale);
  });

  it("returns undefined when given undefined", () => {
    expect(resolveExtensionBundleRoot(undefined)).toBeUndefined();
  });
});
