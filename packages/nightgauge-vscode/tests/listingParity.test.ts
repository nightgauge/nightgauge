/**
 * listingParity.test.ts
 *
 * The extension's README is the listing page on every registry: Open VSX and
 * the Marketplace render the README packaged in the VSIX, and nothing else.
 * It is a second copy of the pitch the repository README makes, and the two
 * drifted: the repository README gained its hero demo in #2152 and the
 * listing kept showing static screenshots for three releases (#2303).
 *
 * Two facts are asserted:
 *
 *   1. Whatever demo the repository README leads with, the listing shows too.
 *   2. Every image in the listing is an absolute https URL. A registry page
 *      has no repository beside it, so a relative path is a broken image.
 */

import { describe, it, expect } from "vitest";
import { readFileSync } from "node:fs";
import { join } from "node:path";

const PKG_ROOT = join(__dirname, "..");
const REPO_ROOT = join(PKG_ROOT, "..", "..");
const RAW_BASE = "https://raw.githubusercontent.com/nightgauge/nightgauge/main/";

const rootReadme = readFileSync(join(REPO_ROOT, "README.md"), "utf8");
const listing = readFileSync(join(PKG_ROOT, "README.md"), "utf8");

/** Image sources in a README: markdown `![alt](src)` and HTML `<img src>`. */
function imageSources(markdown: string): string[] {
  const md = [...markdown.matchAll(/!\[[^\]]*\]\(\s*([^)\s]+)/g)].map((m) => m[1]);
  const html = [...markdown.matchAll(/<img\b[^>]*\bsrc="([^"]+)"/g)].map((m) => m[1]);
  return [...md, ...html];
}

describe("registry listing parity with the repository README (#2303)", () => {
  it("the repository README leads with a demo", () => {
    // If the hero demo is renamed or removed this guard would pass vacuously.
    expect(
      imageSources(rootReadme).filter((src) => /(^|\/)demo\.[a-z0-9]+$/.test(src))
    ).not.toEqual([]);
  });

  it("shows every demo the repository README shows", () => {
    const demos = imageSources(rootReadme).filter((src) => /(^|\/)demo\.[a-z0-9]+$/.test(src));
    const shown = imageSources(listing);
    for (const demo of demos) {
      const absolute = /^https?:\/\//.test(demo) ? demo : RAW_BASE + demo.replace(/^\.?\//, "");
      expect(shown, `the listing README must show ${absolute}`).toContain(absolute);
    }
  });

  it("references every image by absolute https URL", () => {
    const relative = imageSources(listing).filter((src) => !src.startsWith("https://"));
    expect(relative).toEqual([]);
  });
});
