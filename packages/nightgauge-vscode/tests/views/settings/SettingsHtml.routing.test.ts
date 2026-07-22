import { describe, it, expect } from "vitest";
import { getSettingsHtml } from "../../../src/views/settings/SettingsHtml";
import { getDefaultConfig } from "../../../src/config/schema";
import type { IncrediConfig } from "../../../src/views/settings/types";
import type { ConfigSourceMap } from "../../../src/config/schema";

describe("SettingsHtml routing section", () => {
  it("renders routing section controls", () => {
    const config = getDefaultConfig() as IncrediConfig;
    const html = getSettingsHtml({ cspSource: "test-csp" } as any, config);

    expect(html).toContain('id="section-routing"');
    expect(html).toContain('data-path="model_routing.mode"');
    expect(html).toContain('data-path="model_routing.complexity_thresholds.haiku_max"');
    expect(html).toContain('data-path="model_routing.complexity_thresholds.sonnet_max"');
    expect(html).toContain('data-path="model_routing.confidence_threshold"');
    expect(html).toContain('data-path="model_routing.auto_tune"');
    expect(html).toContain("Routing Mode");
    expect(html).toContain("Haiku Max Complexity");
    expect(html).toContain("Sonnet Max Complexity");
    expect(html).toContain("Confidence Threshold");
    expect(html).toContain("Auto-Tune Thresholds");
    expect(html).toContain('<option value="manual"');
    expect(html).toContain('<option value="automatic"');
    expect(html).toContain('<option value="hybrid"');
  });

  it("renders correct default values", () => {
    const config = {} as IncrediConfig;
    const html = getSettingsHtml({ cspSource: "test-csp" } as any, config);

    // mode defaults to automatic (selected)
    expect(html).toContain('<option value="automatic" selected>');

    // haiku_max defaults to 3
    expect(html).toMatch(/id="model_routing\.complexity_thresholds\.haiku_max"[^>]*value="3"/);

    // sonnet_max defaults to 6
    expect(html).toMatch(/id="model_routing\.complexity_thresholds\.sonnet_max"[^>]*value="6"/);

    // confidence_threshold defaults to 0.7 with step=0.05
    expect(html).toMatch(/id="model_routing\.confidence_threshold"[^>]*value="0\.7"/);
    expect(html).toMatch(/id="model_routing\.confidence_threshold"[^>]*step="0\.05"/);

    // auto_tune defaults to false (not checked)
    const autoTuneMatch = html.match(/id="model_routing\.auto_tune"[^>]*/);
    expect(autoTuneMatch).toBeTruthy();
    expect(autoTuneMatch![0]).not.toContain("checked");
  });

  it("renders custom config values", () => {
    const config = getDefaultConfig() as IncrediConfig;
    config.model_routing = {
      mode: "hybrid",
      complexity_thresholds: {
        haiku_max: 2,
        sonnet_max: 8,
      },
      confidence_threshold: 0.9,
      auto_tune: true,
    };
    const html = getSettingsHtml({ cspSource: "test-csp" } as any, config);

    // mode is hybrid (selected)
    expect(html).toContain('<option value="hybrid" selected>');
    expect(html).not.toMatch(/<option value="manual" selected>/);

    // haiku_max is 2
    expect(html).toMatch(/id="model_routing\.complexity_thresholds\.haiku_max"[^>]*value="2"/);

    // sonnet_max is 8
    expect(html).toMatch(/id="model_routing\.complexity_thresholds\.sonnet_max"[^>]*value="8"/);

    // confidence_threshold is 0.9
    expect(html).toMatch(/id="model_routing\.confidence_threshold"[^>]*value="0\.9"/);

    // auto_tune is true (checked)
    expect(html).toMatch(/id="model_routing\.auto_tune"[^>]*checked/);
  });

  it("renders tier badges when showBadges is true", () => {
    const config = getDefaultConfig() as IncrediConfig;
    const sources: ConfigSourceMap = {
      "model_routing.mode": "project",
      "model_routing.auto_tune": "local",
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

    expect(html).toContain('id="section-routing"');
    expect(html).toContain('data-path="model_routing.mode"');
    // Badges appear because we're in merged view with sources
    expect(html).toContain("setting-modified");
  });
});
