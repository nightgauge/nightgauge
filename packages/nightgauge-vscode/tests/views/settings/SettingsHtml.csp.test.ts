/**
 * CSP conformance for the settings webview.
 *
 * The webview's Content-Security-Policy is `script-src 'nonce-<nonce>'`. A
 * <script> emitted without that nonce is blocked outright by the browser and
 * never executes — silently, with only a console entry nobody reads.
 *
 * That is exactly how the "Open Notifier Settings" button and the forge
 * instance buttons came to do nothing when clicked: their listeners lived in
 * section-level inline <script> blocks that carried no nonce, so the listeners
 * were never attached. Both buttons looked live and were inert.
 */

import { describe, it, expect } from "vitest";
import { getSettingsHtml } from "../../../src/views/settings/SettingsHtml";
import { getDefaultConfig } from "../../../src/config/schema";
import type { NightgaugeConfig } from "../../../src/views/settings/types";

const mockWebview = { cspSource: "test-csp" } as any;

/** Render with the real nonce the template generated, so the assertions
 *  compare against what a browser would actually enforce. */
function render(): { html: string; nonce: string } {
  const html = getSettingsHtml(mockWebview, getDefaultConfig() as NightgaugeConfig);
  const m = html.match(/nonce-([A-Za-z0-9]{8,})/);
  if (!m) throw new Error("no CSP nonce found in the rendered document");
  return { html, nonce: m[1] };
}

describe("settings webview CSP", () => {
  it("emits no <script> without the CSP nonce (#1115)", () => {
    const { html, nonce } = render();

    // Every script tag must carry the nonce, or the browser drops it.
    const scripts = html.match(/<script[^>]*>/g) ?? [];
    expect(scripts.length).toBeGreaterThan(0);

    const unnonced = scripts.filter((tag) => !tag.includes(`nonce="${nonce}"`));
    expect(
      unnonced,
      `these <script> tags carry no nonce and are blocked by script-src 'nonce-…', ` +
        `so any listener they attach never exists:\n${unnonced.join("\n")}`
    ).toEqual([]);
  });

  it("attaches the notifier and forge button listeners inside a nonced script", () => {
    const { html, nonce } = render();

    // The buttons themselves must still be rendered...
    expect(html).toContain('id="notifications-open-panel-btn"');

    // ...and their listeners must be reachable, i.e. inside a nonced <script>.
    const re = new RegExp(`<script nonce="${nonce}">([\\s\\S]*?)</script>`, "g");
    const nonced = [...html.matchAll(re)].map((m) => m[1]).join("\n");

    expect(nonced).toContain("notifications-open-panel-btn");
    expect(nonced).toContain("open-notifier-settings");
    expect(nonced).toContain("forge-add-btn");
    expect(nonced).toContain("forge-action");
  });
});
