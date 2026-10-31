/**
 * Unit tests for stableMarketplace.ts, on real temp directories: the defect is
 * a directory that disappears, so a mocked filesystem could not show it.
 *
 * @see Issue #2719
 */

import { describe, it, expect, beforeEach, afterEach } from "vitest";
import * as fs from "fs";
import * as os from "os";
import * as path from "path";
import {
  ensureStableMarketplace,
  isVersionKeyedSource,
} from "../../src/services/stableMarketplace";

function bundle(extensionPath: string, marker: string): void {
  const dist = path.join(extensionPath, "dist");
  const plugin = path.join(dist, "claude-plugins", "nightgauge");
  fs.mkdirSync(path.join(dist, ".claude-plugin"), { recursive: true });
  fs.mkdirSync(path.join(plugin, "skills"), { recursive: true });
  fs.writeFileSync(path.join(dist, ".claude-plugin", "marketplace.json"), `{"m":"${marker}"}`);
  fs.writeFileSync(path.join(plugin, "skills", "SKILL.md"), marker);
}

describe("stableMarketplace", () => {
  let tmp: string;
  let storage: string;

  const contextFor = (version: string, extensionPath = extDir(version)) => ({
    extensionPath,
    globalStorageUri: { fsPath: storage } as never,
    extension: { packageJSON: { version } },
  });
  const extDir = (version: string) => path.join(tmp, "extensions", `nightgauge-${version}`);
  const skill = (root: string) =>
    path.join(root, "claude-plugins", "nightgauge", "skills", "SKILL.md");

  beforeEach(() => {
    tmp = fs.mkdtempSync(path.join(os.tmpdir(), "ng-stable-marketplace-"));
    storage = path.join(tmp, "globalStorage");
  });

  afterEach(() => {
    fs.rmSync(tmp, { recursive: true, force: true });
  });

  it("still resolves after the previous extension directory is removed", () => {
    bundle(extDir("1.0.0"), "one");
    const first = ensureStableMarketplace(contextFor("1.0.0"));

    bundle(extDir("2.0.0"), "two");
    fs.rmSync(extDir("1.0.0"), { recursive: true });
    const second = ensureStableMarketplace(contextFor("2.0.0"));

    expect(second?.path).toBe(first?.path);
    expect(fs.existsSync(path.join(second!.path, ".claude-plugin", "marketplace.json"))).toBe(true);
    expect(fs.readFileSync(skill(second!.path), "utf8")).toBe("two");
  });

  it("resolves when the extension directory goes before any new version lands", () => {
    bundle(extDir("1.0.0"), "one");
    const stable = ensureStableMarketplace(contextFor("1.0.0"));

    fs.rmSync(extDir("1.0.0"), { recursive: true });

    expect(fs.readFileSync(skill(stable!.path), "utf8")).toBe("one");
  });

  it("does not recopy while the stamp matches", () => {
    bundle(extDir("1.0.0"), "one");
    const first = ensureStableMarketplace(contextFor("1.0.0"));
    fs.writeFileSync(skill(first!.path), "local");

    const second = ensureStableMarketplace(contextFor("1.0.0"));

    expect(first?.recopied).toBe(true);
    expect(second?.recopied).toBe(false);
    expect(fs.readFileSync(skill(second!.path), "utf8")).toBe("local");
  });

  it("recopies when the stamp differs and leaves no staging directory", () => {
    bundle(extDir("1.0.0"), "one");
    ensureStableMarketplace(contextFor("1.0.0"));
    bundle(extDir("2.0.0"), "two");

    const second = ensureStableMarketplace(contextFor("2.0.0"));

    expect(second?.recopied).toBe(true);
    expect(fs.readdirSync(storage)).toEqual(["claude-marketplace"]);
  });

  it("copies a relative symlink verbatim, so it does not reach back into dist", () => {
    bundle(extDir("1.0.0"), "one");
    const skills = path.join(extDir("1.0.0"), "dist", "claude-plugins", "nightgauge", "skills");
    fs.symlinkSync("SKILL.md", path.join(skills, "LINK.md"));

    const stable = ensureStableMarketplace(contextFor("1.0.0"));

    const link = path.join(path.dirname(skill(stable!.path)), "LINK.md");
    expect(fs.readlinkSync(link)).toBe("SKILL.md");
  });

  it("returns undefined when the VSIX has no bundle", () => {
    fs.mkdirSync(extDir("1.0.0"), { recursive: true });
    expect(ensureStableMarketplace(contextFor("1.0.0"))).toBeUndefined();
  });

  it("a failed copy warns, returns undefined and keeps the previous source", () => {
    bundle(extDir("1.0.0"), "one");
    const first = ensureStableMarketplace(contextFor("1.0.0"));
    bundle(extDir("2.0.0"), "two");
    fs.rmSync(path.join(extDir("2.0.0"), "dist", "claude-plugins"), { recursive: true });

    const warnings: string[] = [];
    const second = ensureStableMarketplace(contextFor("2.0.0"), (m) => warnings.push(m));

    expect(second).toBeUndefined();
    expect(warnings[0]).toContain("Could not copy");
    expect(fs.readFileSync(skill(first!.path), "utf8")).toBe("one");
    expect(fs.readdirSync(storage)).toEqual(["claude-marketplace"]);
  });

  describe("isVersionKeyedSource", () => {
    const extensionPath = "/home/u/.vscode/extensions/nightgauge.nightgauge-0.8.0";

    it("is true for this version's dist and for an older sibling", () => {
      expect(isVersionKeyedSource(`${extensionPath}/dist`, extensionPath)).toBe(true);
      expect(
        isVersionKeyedSource(
          "/home/u/.vscode/extensions/nightgauge.nightgauge-0.7.0/dist",
          extensionPath
        )
      ).toBe(true);
    });

    it("is false for global storage and a primary checkout", () => {
      expect(
        isVersionKeyedSource(
          "/home/u/.config/Code/globalStorage/x/claude-marketplace",
          extensionPath
        )
      ).toBe(false);
      expect(isVersionKeyedSource("/home/u/src/nightgauge", extensionPath)).toBe(false);
    });
  });
});
