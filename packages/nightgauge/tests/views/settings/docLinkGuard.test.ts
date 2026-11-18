/**
 * docLinkGuard.test.ts
 *
 * Settings documentation links open only files under the workspace docs/.
 */

import { describe, it, expect, beforeAll, afterAll } from "vitest";
import { mkdtempSync, mkdirSync, rmSync, symlinkSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { isDocLinkPath, resolveDocLink } from "../../../src/views/settings/docLinkGuard";

describe("docLinkGuard", () => {
  let root: string;
  let outside: string;

  beforeAll(() => {
    root = mkdtempSync(join(tmpdir(), "doclink-"));
    outside = mkdtempSync(join(tmpdir(), "doclink-out-"));
    mkdirSync(join(root, "docs"));
    writeFileSync(join(root, "docs", "CONFIGURATION.md"), "# c\n");
    writeFileSync(join(outside, "secret.md"), "x\n");
    symlinkSync(join(outside, "secret.md"), join(root, "docs", "link.md"));
  });

  afterAll(() => {
    rmSync(root, { recursive: true, force: true });
    rmSync(outside, { recursive: true, force: true });
  });

  it("resolves a docs link and keeps its fragment", () => {
    expect(resolveDocLink(root, "docs/CONFIGURATION.md#pipeline")).toEqual({
      file: join(root, "docs", "CONFIGURATION.md"),
      fragment: "pipeline",
    });
  });

  it.each([
    "../../etc/hosts",
    "docs/../../etc/hosts",
    "docs/../package.json",
    "/etc/hosts",
    "C:/Windows/win.ini",
    "docs\\..\\x",
    "README.md",
    "docs/",
    "",
  ])("refuses %j", (link) => {
    expect(isDocLinkPath(link)).toBe(false);
    expect(resolveDocLink(root, link)).toBeUndefined();
  });

  it("refuses a symlink under docs/ that leads outside it", () => {
    expect(resolveDocLink(root, "docs/link.md")).toBeUndefined();
  });
});
