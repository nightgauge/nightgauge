/**
 * marketplaceTrustSurface.test.ts
 *
 * Guards the two listing-and-packaging defects that an automated Marketplace
 * trust review can act on without ever telling you which one it acted on.
 *
 * Context. The `nightgauge` Visual Studio Marketplace publisher was blocked on
 * 2026-09-16 with no provision cited and no reinstatement. A full audit of the
 * published v0.4.1 found a genuine violation (a webview loading unversioned
 * script from jsDelivr, fixed in #1834 and guarded by
 * `webviewNoRemoteCode.test.ts`) alongside a set of items that are each
 * legitimate but that together read like a staged payload: an unsigned native
 * binary, shipped shell scripts, 74 process-spawn sites, environment
 * credential reads, machine identification, and default-on egress to a
 * non-Microsoft domain.
 *
 * Two of those items are free to remove and are checked here. Neither is worth
 * what it costs to defend:
 *
 *   1. A third-party trademark used as a listing KEYWORD. Naming a tool you
 *      integrate with in prose is nominative use and fine; keywords are the
 *      search-placement vector, which is what the policy against using others'
 *      marks in metadata is aimed at.
 *   2. A `curl … | bash` string anywhere in shipped source. It was only ever a
 *      help message, but a static scanner cannot know that, and a trust review
 *      is the wrong venue in which to explain it.
 */

import { describe, it, expect } from "vitest";
import { readFileSync, readdirSync, statSync } from "node:fs";
import { join } from "node:path";

const PKG_ROOT = join(__dirname, "..");
const SRC = join(PKG_ROOT, "src");

function walk(dir: string): string[] {
  return readdirSync(dir).flatMap((name) => {
    const full = join(dir, name);
    return statSync(full).isDirectory() ? walk(full) : full.endsWith(".ts") ? [full] : [];
  });
}

const tsFiles = walk(SRC);

/**
 * Marks owned by others that must never appear in `keywords`. Deliberately not
 * a general prose denylist: the description says "queue GitHub issues" and the
 * README names every supported adapter, both of which are ordinary nominative
 * use and must keep working.
 */
const FOREIGN_MARKS = [
  "claude",
  "anthropic",
  "copilot",
  "openai",
  "gpt",
  "chatgpt",
  "gemini",
  "codex",
  "grok",
  "microsoft",
  "visual studio",
  "vscode",
  "vs code",
  "github",
  "cursor",
];

describe("listing metadata carries no third-party trademark", () => {
  const manifest = JSON.parse(readFileSync(join(PKG_ROOT, "package.json"), "utf8")) as {
    keywords?: string[];
  };

  it("declares keywords at all (guard is not vacuous)", () => {
    expect(Array.isArray(manifest.keywords)).toBe(true);
    expect(manifest.keywords!.length).toBeGreaterThan(0);
  });

  it("uses no foreign mark as a keyword", () => {
    const offenders = (manifest.keywords ?? []).filter((kw) =>
      FOREIGN_MARKS.some((mark) => kw.toLowerCase().includes(mark))
    );
    expect(offenders).toEqual([]);
  });
});

describe("shipped source contains no pipe-to-shell install command", () => {
  it("finds source files to scan (guard is not vacuous)", () => {
    expect(tsFiles.length).toBeGreaterThan(0);
  });

  it.each(tsFiles)("%s has no curl/wget/iwr piped into a shell", (file) => {
    const body = readFileSync(file, "utf8");
    // Match the shapes a scanner matches: a fetch of a URL piped into an
    // interpreter. Tolerates flag ordering and both quote styles.
    const patterns = [
      /curl[^\n`"']*https?:\/\/[^\n`"']*\|\s*(ba)?sh\b/i,
      /wget[^\n`"']*https?:\/\/[^\n`"']*\|\s*(ba)?sh\b/i,
      /\b(iwr|invoke-webrequest)[^\n`"']*\|\s*(iex|invoke-expression)\b/i,
    ];
    const hits = patterns.filter((re) => re.test(body)).map((re) => String(re));
    expect(hits).toEqual([]);
  });
});
