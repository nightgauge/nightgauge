/**
 * webviewNoRemoteCode.test.ts
 *
 * Marketplace compliance guard: no webview may load or connect to executable
 * code from a remote origin.
 *
 * Both the Visual Studio Marketplace Publisher Agreement and the Open VSX
 * publishing terms require that an extension execute only the code contained
 * in its published package. A webview `<script src="https://cdn...">` breaks
 * that even when the Content-Security-Policy names the origin, because the
 * bytes that run on the user's machine are fetched after review and can change
 * without a republish. An unversioned CDN specifier (`npm/marked` rather than
 * `npm/marked@18.0.11`) makes that worse by resolving to "latest" on every
 * load.
 *
 * This shipped once, in OutputWindowHtml.ts, and is believed to be a cause of
 * the publisher block on the `nightgauge` Marketplace publisher. Browser
 * builds now live in dist/vendor/ and load through `webview.asWebviewUri`.
 * This test fails the build if a remote origin reappears in any webview HTML.
 */

import { describe, it, expect } from "vitest";
import { readFileSync, readdirSync, statSync } from "node:fs";
import { join } from "node:path";

const SRC = join(__dirname, "..", "..", "src");

function walk(dir: string): string[] {
  return readdirSync(dir).flatMap((name) => {
    const full = join(dir, name);
    return statSync(full).isDirectory() ? walk(full) : full.endsWith(".ts") ? [full] : [];
  });
}

/** Every source file that builds webview markup. */
const htmlSources = walk(SRC).filter((f) => {
  const body = readFileSync(f, "utf8");
  return body.includes("Content-Security-Policy") || /<script\s+src=/.test(body);
});

describe("webview HTML never references remote code", () => {
  it("finds the webview HTML builders (guard is not vacuous)", () => {
    expect(htmlSources.length).toBeGreaterThan(0);
  });

  it.each(htmlSources)("%s has no remote <script src>", (file) => {
    const matches = readFileSync(file, "utf8").match(/<script\s+src=["']https?:\/\/[^"']+/g);
    expect(matches ?? []).toEqual([]);
  });

  it.each(htmlSources)("%s declares no remote origin in its CSP", (file) => {
    const body = readFileSync(file, "utf8");
    // Pull each directive that can authorize code execution or exfiltration
    // and assert it names no http(s) origin.
    const directives = body.match(/(?:script-src|connect-src|worker-src)[^;"'`]*/g) ?? [];
    const offenders = directives.filter((d) => /https?:\/\//.test(d));
    expect(offenders).toEqual([]);
  });
});
