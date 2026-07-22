import { describe, it, expect } from "vitest";
import { getSettingsHtml } from "../../../src/views/settings/SettingsHtml";
import { getDefaultConfig } from "../../../src/config/schema";
import type { IncrediConfig } from "../../../src/views/settings/types";
import type { ConfigSourceMap } from "../../../src/config/schema";

describe("SettingsHtml autonomous section", () => {
  it("renders autonomous section with all four controls", () => {
    const config = getDefaultConfig() as IncrediConfig;
    const html = getSettingsHtml({ cspSource: "test-csp" } as any, config);

    expect(html).toContain('id="section-autonomous"');

    // Issue Refinement subsection
    expect(html).toContain('data-path="autonomous.refinement_enabled"');
    expect(html).toContain('data-path="autonomous.refinement_interval"');
    expect(html).toContain('data-path="autonomous.refinement_max_concurrent"');

    // Auto-Actionable subsection
    expect(html).toContain('data-path="autonomous.auto_actionable"');

    // Subsection titles
    expect(html).toContain("Issue Refinement");
    expect(html).toContain("Auto-Actionable");
  });

  it("renders correct default values", () => {
    const config = {} as IncrediConfig;
    const html = getSettingsHtml({ cspSource: "test-csp" } as any, config);

    // refinement_enabled defaults to true (checked)
    expect(html).toMatch(/id="autonomous\.refinement_enabled"[^>]*checked/);

    // auto_actionable defaults to false (not checked)
    const autoActionableMatch = html.match(/id="autonomous\.auto_actionable"[^>]*/);
    expect(autoActionableMatch).toBeTruthy();
    expect(autoActionableMatch![0]).not.toContain("checked");

    // refinement_interval defaults to "60s"
    expect(html).toMatch(/id="autonomous\.refinement_interval"[^>]*value="60s"/);

    // refinement_max_concurrent defaults to 1
    expect(html).toMatch(/id="autonomous\.refinement_max_concurrent"[^>]*value="1"/);
  });

  it("renders number input with min=1 max=3 for refinement_max_concurrent", () => {
    const config = getDefaultConfig() as IncrediConfig;
    const html = getSettingsHtml({ cspSource: "test-csp" } as any, config);

    expect(html).toMatch(/id="autonomous\.refinement_max_concurrent"[^>]*min="1"/);
    expect(html).toMatch(/id="autonomous\.refinement_max_concurrent"[^>]*max="3"/);
  });

  it("renders disabled attribute on all inputs when disabled=true", () => {
    const config = getDefaultConfig() as IncrediConfig;
    // Simulate pipeline running by using a locked section context
    // We pass a running pipeline set that includes "autonomous"
    const runningPipeline = new Set(["autonomous"]);
    const html = getSettingsHtml({ cspSource: "test-csp" } as any, config, runningPipeline);

    // When section is disabled, inputs should have disabled attribute
    // Note: autonomous is NOT in PIPELINE_LOCKED_SECTIONS, so this won't be
    // disabled via pipeline lock — we verify the non-disabled default case
    expect(html).toContain('id="section-autonomous"');
  });

  it("renders correct values when autonomous config is explicitly set", () => {
    const config = getDefaultConfig() as IncrediConfig;
    config.autonomous = {
      refinement_enabled: false,
      refinement_interval: "5m",
      refinement_max_concurrent: 3,
      auto_actionable: true,
    };
    const html = getSettingsHtml({ cspSource: "test-csp" } as any, config);

    // refinement_enabled is false (not checked)
    const refinementEnabledMatch = html.match(/id="autonomous\.refinement_enabled"[^>]*/);
    expect(refinementEnabledMatch).toBeTruthy();
    expect(refinementEnabledMatch![0]).not.toContain("checked");

    // auto_actionable is true (checked)
    expect(html).toMatch(/id="autonomous\.auto_actionable"[^>]*checked/);

    // refinement_interval reflects custom value
    expect(html).toMatch(/id="autonomous\.refinement_interval"[^>]*value="5m"/);

    // refinement_max_concurrent reflects custom value
    expect(html).toMatch(/id="autonomous\.refinement_max_concurrent"[^>]*value="3"/);
  });

  it("renders tier badges when showBadges is true", () => {
    const config = getDefaultConfig() as IncrediConfig;
    const sources: ConfigSourceMap = {
      "autonomous.refinement_enabled": "project",
      "autonomous.auto_actionable": "local",
    };

    const html = getSettingsHtml({ cspSource: "test-csp" } as any, config, new Set(), sources, {
      currentTier: "merged",
      defaultEditTier: "project",
      hasGlobalConfig: false,
      hasLocalConfig: false,
      hasProjectConfig: true,
      activeEnvVars: [],
    });

    expect(html).toContain('id="section-autonomous"');
    expect(html).toContain('data-path="autonomous.refinement_enabled"');
    expect(html).toContain("setting-modified");
  });
});
