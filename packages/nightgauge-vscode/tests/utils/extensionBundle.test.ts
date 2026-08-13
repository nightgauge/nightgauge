/**
 * resolveExtensionBundleRoot — real-filesystem tests.
 *
 * Reproduces the auto-update GC race (#3883): VSCode installs a new versioned
 * extension dir and deletes the running one, leaving bundle-relative lookups
 * pointing at a directory that no longer exists. The helper must self-heal to
 * the sibling VS Code records as installed.
 *
 * @see src/utils/extensionBundle.ts
 */

import { describe, it, expect, beforeEach, afterEach } from "vitest";
import { mkdirSync, readFileSync, rmSync, writeFileSync } from "fs";
import { basename, join } from "path";
import { tmpdir } from "os";
import { fileURLToPath } from "url";
import { resolveExtensionBundleRoot } from "../../src/utils/extensionBundle";

let root: string;
const PREFIX = "nightgauge.nightgauge-vscode-";
const FIXTURE_DIR = fileURLToPath(
  new URL("../../../../internal/doctor/testdata/vscode-bundles/", import.meta.url)
);

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

function writeExtensionsIndex(content: string): void {
  writeFileSync(join(root, "extensions.json"), content);
}

function recordedIndex(...relativeLocations: string[]): string {
  return JSON.stringify(relativeLocations.map((relativeLocation) => ({ relativeLocation })));
}

describe("resolveExtensionBundleRoot", () => {
  it("returns the running path unchanged when it still exists", () => {
    const running = makeVersion("0.1.1780370898");
    expect(resolveExtensionBundleRoot(running)).toBe(running);
  });

  it("self-heals to the recorded surviving sibling when the running dir was GC'd", () => {
    // Running version was deleted; two newer ones survive.
    const stale = join(root, `${PREFIX}0.1.1780345587`); // never created (GC'd)
    makeVersion("0.1.1780360156");
    const newest = makeVersion("0.1.1780370898");
    writeExtensionsIndex(recordedIndex(basename(newest)));

    expect(resolveExtensionBundleRoot(stale)).toBe(newest);
  });

  it("falls back to the first glob-order sibling instead of ranking versions", () => {
    const stale = join(root, `${PREFIX}0.1.1780000000`);
    makeVersion("0.1.1780370898");
    const first = makeVersion("0.1.1780360156");

    expect(resolveExtensionBundleRoot(stale)).toBe(first);
  });

  it("selects the recorded RC bundle instead of a numerically ranked dev bundle", () => {
    const stale = join(root, `${PREFIX}0.1.1780000000`);
    makeVersion("0.1.1785982325");
    const recorded = makeVersion("0.2.0-rc.23-darwin-arm64");
    writeExtensionsIndex(readFileSync(join(FIXTURE_DIR, "extensions-index-large.json"), "utf8"));

    expect(resolveExtensionBundleRoot(stale)).toBe(recorded);
  });

  it("honors a recorded downgrade instead of version ordering", () => {
    const stale = join(root, `${PREFIX}0.1.1780000000`);
    makeVersion("0.1.0-darwin-arm64");
    const recorded = makeVersion("0.2.0-darwin-arm64");
    makeVersion("0.3.0-darwin-arm64");
    writeExtensionsIndex(recordedIndex(basename(recorded)));

    expect(resolveExtensionBundleRoot(stale)).toBe(recorded);
  });

  it("shares the captured fixture selection with Go ResolveBinary", () => {
    // Go's TestScanVSCodeBundles_CapturedRealLayout consumes these same two
    // files. A changed expected record therefore fails both implementations.
    const layout = JSON.parse(readFileSync(join(FIXTURE_DIR, "bundle-layout.json"), "utf8")) as {
      recorded_relative_location: string;
      bundles: Array<{ dir: string }>;
    };
    for (const bundle of layout.bundles) {
      makeVersion(basename(bundle.dir).slice(PREFIX.length));
    }
    writeExtensionsIndex(readFileSync(join(FIXTURE_DIR, "extensions-index.json"), "utf8"));
    const stale = join(root, `${PREFIX}0.0.0-stale`);

    expect(resolveExtensionBundleRoot(stale)).toBe(join(root, layout.recorded_relative_location));
  });

  it.each([
    ["no record", "[]"],
    [
      "multiple records",
      recordedIndex(`${PREFIX}0.2.0-darwin-arm64`, `${PREFIX}0.3.0-darwin-arm64`),
    ],
  ])("falls back to the first glob-order sibling with %s", (_name, index) => {
    const stale = join(root, `${PREFIX}0.0.0-stale`);
    const first = makeVersion("0.1.0-darwin-arm64");
    makeVersion("0.3.0-darwin-arm64");
    writeExtensionsIndex(index);

    expect(resolveExtensionBundleRoot(stale)).toBe(first);
  });

  it("falls back when the recorded bundle directory no longer exists", () => {
    const stale = join(root, `${PREFIX}0.0.0-stale`);
    const first = makeVersion("0.1.0-darwin-arm64");
    makeVersion("0.2.0-darwin-arm64");
    writeExtensionsIndex(recordedIndex(`${PREFIX}9.9.9-darwin-arm64`));

    expect(resolveExtensionBundleRoot(stale)).toBe(first);
  });

  it("falls back to the stale path when no siblings survive", () => {
    const stale = join(root, `${PREFIX}0.1.1780345587`);
    expect(resolveExtensionBundleRoot(stale)).toBe(stale);
  });

  it("returns undefined when given undefined", () => {
    expect(resolveExtensionBundleRoot(undefined)).toBeUndefined();
  });
});
