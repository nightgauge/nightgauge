/**
 * webviewsSuiteCallSitePinned.test.ts
 *
 * #1327 follow-up (adversarial review) — the fix for #1327 lives entirely in
 * `tests/vscode-host/fixture.ts`'s `waitForRenderThenDispose`, but the only
 * behaviour that actually protects CI is whether the host suite's per-panel
 * case *calls* it. `waitForRenderThenDispose.test.ts` exercises the helper
 * in isolation and would stay green even if the call site reverted to the
 * pre-fix shape — an unguarded `waitFor(...)` followed by a bare
 * `match.panel.dispose()` — because nothing else reads
 * `webviews.suite.ts`'s source and that tier only runs inside a downloaded
 * VSCode extension host (`npm run test:host`), which the fast pre-merge
 * gates never provision.
 *
 * This is a structural assertion against the source, the same shape as
 * `tests/bootstrap/manifestWatcherUnconditional.test.ts`: the claim under
 * test is "the render probe is wired through the helper", which a call-site
 * revert falsifies directly.
 */

import { describe, it, expect } from "vitest";
import { readFileSync } from "node:fs";
import path from "node:path";

const SUITE_PATH = path.resolve(__dirname, "../vscode-host/suites/webviews.suite.ts");
const source = readFileSync(SUITE_PATH, "utf-8");

describe("#1327 webviews.suite.ts per-panel render probe stays wired to the fix", () => {
  it("imports waitForRenderThenDispose from the fixture", () => {
    expect(source).toMatch(
      /import\s*\{[^}]*\bwaitForRenderThenDispose\b[^}]*\}\s*from\s*"..\/fixture(\.js)?"/
    );
  });

  it("routes the render probe through waitForRenderThenDispose, passing panel disposal as its callback", () => {
    const callIdx = source.indexOf("waitForRenderThenDispose(");
    expect(callIdx, "no call to waitForRenderThenDispose found").toBeGreaterThan(-1);

    // The call's closing paren is the next top-level ")" — walk parens from
    // the call's own "(" to find it, so the window below is exactly this
    // call's argument list and not whatever follows it.
    const openIdx = source.indexOf("(", callIdx);
    let depth = 0;
    let closeIdx = -1;
    for (let i = openIdx; i < source.length; i++) {
      if (source[i] === "(") depth++;
      else if (source[i] === ")") {
        depth--;
        if (depth === 0) {
          closeIdx = i;
          break;
        }
      }
    }
    expect(closeIdx, "unbalanced parens after waitForRenderThenDispose(").toBeGreaterThan(-1);

    const args = source.slice(openIdx + 1, closeIdx);
    expect(args, "match.panel.dispose() is not passed as the helper's dispose callback").toContain(
      "match.panel.dispose()"
    );
  });

  it("never disposes the panel as a bare statement outside the helper (the pre-#1327 shape)", () => {
    // Pre-fix: `match.panel.dispose();` sat on its own line, right after the
    // awaited (unguarded) render probe. Post-fix it appears only as
    // `() => match.panel.dispose()` — no trailing semicolon, and it is an
    // argument to waitForRenderThenDispose rather than a statement — so this
    // pattern is unique to the reverted, buggy shape.
    const bareDispose = /^\s*match\.panel\.dispose\(\);\s*$/m;
    expect(
      bareDispose.test(source),
      "match.panel.dispose() is called as a bare statement again — a hung render " +
        "will skip disposal and cascade into a second, unrelated-looking failure (#1327)"
    ).toBe(false);
  });
});
