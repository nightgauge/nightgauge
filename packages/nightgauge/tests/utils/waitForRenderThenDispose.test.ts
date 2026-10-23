/**
 * #1327 — the VSCode host smoke tier's per-panel render probe.
 *
 * `tests/vscode-host/suites/webviews.suite.ts` waits for a panel's
 * `webview.html` to become non-empty, then disposes the panel. On main's own
 * post-merge run (0415c27c, run 33688162874), Dashboard's first render
 * crossed the uniform 5s `RENDER_BUDGET_MS` under hosted-runner load; the
 * `waitFor(...)` call threw, `match.panel.dispose()` — placed *after* the
 * await, guarded by nothing — never ran, and the one flake reported as two
 * failures: the render timeout, then "no panel from this suite is left
 * open" as an unrelated-looking follow-on.
 *
 * `waitForRenderThenDispose` (tests/vscode-host/fixture.ts) is the fix: the
 * elapsed render time is logged unconditionally, in a grep-able
 * `render-ms <name> <ms>` line, and disposal happens in a `finally` so a
 * timeout cannot skip it.
 *
 * This suite cannot exercise the real webview suite directly — that tier
 * only runs inside a downloaded VSCode extension host (`npm run test:host`),
 * which this environment cannot provision in a few minutes. It instead
 * unit-tests the extracted helper with the same shape of failure: a probe
 * that never resolves, under a tiny injected budget, so the timeout is
 * deterministic instead of CI-load-dependent.
 *
 * The real call site (`webviews.suite.ts`) never passes a `log` argument, so
 * the two cases above — both of which inject a spy — never exercise the
 * arm that actually runs in CI: the `console.log` default. The third case
 * below calls the helper the same way the suite does (four arguments) and
 * spies on `console.log` itself, so the shipped configuration, not just the
 * extracted unit, is under test.
 */
import { describe, expect, it, vi } from "vitest";
import { waitForRenderThenDispose } from "../vscode-host/fixture.js";

describe("waitForRenderThenDispose", () => {
  it("logs the elapsed render time and disposes when the probe succeeds", async () => {
    const dispose = vi.fn();
    const log = vi.fn();

    const html = await waitForRenderThenDispose(
      "Getting Started",
      () => "<html>ok</html>",
      1_000,
      dispose,
      log
    );

    expect(html).toBe("<html>ok</html>");
    expect(dispose).toHaveBeenCalledTimes(1);
    expect(log).toHaveBeenCalledTimes(1);
    expect(log.mock.calls[0][0]).toMatch(/^render-ms Getting Started \d+$/);
  });

  it("still logs and disposes when the probe never resolves before the budget (#1327 AC1 + AC3)", async () => {
    const dispose = vi.fn();
    const log = vi.fn();

    // A panel that never renders: webview.html stays empty forever, exactly
    // the shape of a render that has genuinely hung, not merely a slow one.
    const neverRenders = () => undefined;

    await expect(
      waitForRenderThenDispose("Dashboard", neverRenders, 10, dispose, log)
    ).rejects.toThrow(/Timed out after 10ms waiting for Dashboard to render a non-empty body/);

    // AC3: a timed-out render must still dispose its panel, so the "no
    // panel left open" assertion cannot cascade from an unrelated timeout.
    expect(dispose).toHaveBeenCalledTimes(1);
    // AC1: the elapsed render time is printed for every case, pass or fail,
    // in a form the orchestrator can grep out of raw CI output.
    expect(log).toHaveBeenCalledTimes(1);
    expect(log.mock.calls[0][0]).toMatch(/^render-ms Dashboard \d+$/);
  });

  it("logs through console.log by default when called with no log argument, as the suite does", async () => {
    const dispose = vi.fn();
    const consoleLog = vi.spyOn(console, "log").mockImplementation(() => undefined);

    try {
      const html = await waitForRenderThenDispose(
        "Getting Started",
        () => "<html>ok</html>",
        1_000,
        dispose
      );

      expect(html).toBe("<html>ok</html>");
      expect(dispose).toHaveBeenCalledTimes(1);
      expect(consoleLog).toHaveBeenCalledTimes(1);
      expect(consoleLog.mock.calls[0][0]).toMatch(/^render-ms Getting Started \d+$/);
    } finally {
      consoleLog.mockRestore();
    }
  });
});
