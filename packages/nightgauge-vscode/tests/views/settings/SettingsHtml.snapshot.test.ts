/**
 * SettingsHtml.snapshot.test.ts
 *
 * HTML snapshot regression tests for getSettingsHtml().
 * Captures structural HTML output to catch silent regressions
 * in the settings panel template generator.
 *
 * @see Issue #1242 - Add HTML snapshot regression tests for *Html.ts
 */

import { describe, it, expect } from "vitest";
import { getSettingsHtml } from "../../../src/views/settings/SettingsHtml";
import { getDefaultConfig } from "../../../src/config/schema";
import type { IncrediConfig } from "../../../src/views/settings/types";

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

const mockWebview = { cspSource: "test-csp" } as any;

function normalize(html: string): string {
  return html
    .replace(/nonce-[A-Za-z0-9]{32}/g, "nonce-NONCE")
    .replace(/nonce="[A-Za-z0-9]{32}"/g, 'nonce="NONCE"');
}

// ---------------------------------------------------------------------------
// Snapshot tests
// ---------------------------------------------------------------------------

describe("getSettingsHtml snapshots (Issue #1242)", () => {
  it("default config render", () => {
    const config = getDefaultConfig() as IncrediConfig;
    const html = getSettingsHtml(mockWebview, config);
    expect(normalize(html)).toMatchSnapshot();
  });

  it("locked section — pipeline running", () => {
    const config = getDefaultConfig() as IncrediConfig;
    const lockedSections = new Set(["pipeline", "routing"]);
    const html = getSettingsHtml(mockWebview, config, lockedSections);
    expect(normalize(html)).toMatchSnapshot();
  });

  it("global tier view", () => {
    const config = getDefaultConfig() as IncrediConfig;
    const html = getSettingsHtml(
      mockWebview,
      config,
      new Set(),
      {},
      {
        currentTier: "global",
        defaultEditTier: "project",
        hasGlobalConfig: true,
        hasLocalConfig: false,
        hasProjectConfig: true,
        activeEnvVars: [],
      }
    );
    expect(normalize(html)).toMatchSnapshot();
  });

  it("project tier view", () => {
    const config = getDefaultConfig() as IncrediConfig;
    const html = getSettingsHtml(
      mockWebview,
      config,
      new Set(),
      {},
      {
        currentTier: "project",
        defaultEditTier: "project",
        hasGlobalConfig: false,
        hasLocalConfig: false,
        hasProjectConfig: true,
        activeEnvVars: [],
      }
    );
    expect(normalize(html)).toMatchSnapshot();
  });

  it("local tier view", () => {
    const config = getDefaultConfig() as IncrediConfig;
    const html = getSettingsHtml(
      mockWebview,
      config,
      new Set(),
      {},
      {
        currentTier: "local",
        defaultEditTier: "local",
        hasGlobalConfig: false,
        hasLocalConfig: true,
        hasProjectConfig: false,
        activeEnvVars: ["NIGHTGAUGE_PIPELINE_AUTO_FIX"],
      }
    );
    expect(normalize(html)).toMatchSnapshot();
  });
});
