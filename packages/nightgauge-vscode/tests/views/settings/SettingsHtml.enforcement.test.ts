import { describe, it, expect } from "vitest";
import { getSettingsHtml } from "../../../src/views/settings/SettingsHtml";
import { getDefaultConfig } from "../../../src/config/schema";
import type { IncrediConfig } from "../../../src/views/settings/types";
import type { ConfigSourceMap } from "../../../src/config/schema";

describe("SettingsHtml enforcement section", () => {
  it("renders enforcement section controls", () => {
    const config = getDefaultConfig() as IncrediConfig;
    const html = getSettingsHtml({ cspSource: "test-csp" } as any, config);

    expect(html).toContain('id="section-enforcement"');
    expect(html).toContain('data-path="enforcement.dependencies.enabled"');
    expect(html).toContain('data-path="enforcement.dependencies.mode"');
    expect(html).toContain('data-path="enforcement.dependencies.check_transitive"');
    expect(html).toContain("Enable Dependency Checking");
    expect(html).toContain("Enforcement Mode");
    expect(html).toContain("Check Transitive Dependencies");
    expect(html).toContain('<option value="warn"');
    expect(html).toContain('<option value="block"');
    expect(html).toContain('<option value="ignore"');
  });

  it("renders correct default values", () => {
    const config = {} as IncrediConfig;
    const html = getSettingsHtml({ cspSource: "test-csp" } as any, config);

    // enabled defaults to true (checked)
    expect(html).toContain('id="enforcement.dependencies.enabled"');
    expect(html).toMatch(/id="enforcement\.dependencies\.enabled"[^>]*checked/);

    // mode defaults to warn (selected)
    expect(html).toContain('<option value="warn" selected>');

    // check_transitive defaults to false (not checked)
    const checkTransitiveMatch = html.match(
      /id="enforcement\.dependencies\.check_transitive"[^>]*/
    );
    expect(checkTransitiveMatch).toBeTruthy();
    expect(checkTransitiveMatch![0]).not.toContain("checked");
  });

  it("renders custom config values", () => {
    const config = getDefaultConfig() as IncrediConfig;
    config.enforcement = {
      dependencies: {
        enabled: false,
        mode: "block",
        check_transitive: true,
      },
    };
    const html = getSettingsHtml({ cspSource: "test-csp" } as any, config);

    // enabled is false (not checked)
    const enabledMatch = html.match(/id="enforcement\.dependencies\.enabled"[^>]*/);
    expect(enabledMatch).toBeTruthy();
    expect(enabledMatch![0]).not.toContain("checked");

    // mode is block (selected)
    expect(html).toContain('<option value="block" selected>');
    expect(html).not.toMatch(/<option value="warn" selected>/);

    // check_transitive is true (checked)
    expect(html).toMatch(/id="enforcement\.dependencies\.check_transitive"[^>]*checked/);
  });

  it("renders tier badges when showBadges is true", () => {
    const config = getDefaultConfig() as IncrediConfig;
    const sources: ConfigSourceMap = {
      "enforcement.dependencies.enabled": "project",
      "enforcement.dependencies.mode": "local",
    };

    // merged view shows badges
    const html = getSettingsHtml({ cspSource: "test-csp" } as any, config, new Set(), sources, {
      currentTier: "merged",
      defaultEditTier: "project",
      hasGlobalConfig: false,
      hasLocalConfig: false,
      hasProjectConfig: true,
      activeEnvVars: [],
    });

    expect(html).toContain('id="section-enforcement"');
    expect(html).toContain('data-path="enforcement.dependencies.enabled"');
    // Badges appear because we're in merged view with sources
    expect(html).toContain("setting-modified");
  });
});
