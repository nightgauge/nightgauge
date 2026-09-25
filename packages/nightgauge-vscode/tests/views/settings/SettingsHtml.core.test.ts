import { describe, it, expect } from "vitest";
import { getSettingsHtml } from "../../../src/views/settings/SettingsHtml";
import { getDefaultConfig } from "../../../src/config/schema";
import type { NightgaugeConfig } from "../../../src/views/settings/types";

describe("SettingsHtml core section", () => {
  it("renders execution adapter control in Nightgauge Settings view", () => {
    const config = getDefaultConfig() as NightgaugeConfig;
    const html = getSettingsHtml(
      { cspSource: "test-csp" } as any,
      config,
      new Set(),
      {},
      undefined,
      {
        codexModels: ["gpt-5.5", "gpt-5.4", "gpt-5.4-mini"],
      }
    );

    expect(html).toContain('id="section-core"');
    expect(html).toContain('data-path="ui.core.adapter"');
    expect(html).toContain("Execution Adapter");
    expect(html).toContain('<option value="codex"');
    expect(html).toContain('<option value="gemini"');
    expect(html).toContain('<option value="gemini-sdk"');
    // #2128: the removed lm-studio and ollama adapters are no longer offered.
    expect(html).not.toContain('<option value="lm-studio"');
    expect(html).not.toContain('<option value="ollama"');
    expect(html).not.toContain("lm_studio.");
    expect(html).toContain('<option value="copilot"');
    expect(html).toContain('data-path="ui.core.auth_provider"');
    expect(html).toContain('data-path="ui.core.default_model"');
    expect(html).toContain('data-path="ui.core.codex.model"');
    expect(html).toContain('data-path="ui.core.codex.reasoning_effort"');
    expect(html).toContain('<option value="max"');
    expect(html).toContain('<option value="gpt-5.5"');
    expect(html).toContain('<option value="gpt-5.4"');
    expect(html).toContain('<option value="gpt-5.4-mini"');
    expect(html).toContain("Refresh Models");
    expect(html).toContain('data-path="ui.core.codex.cli_command"');
    expect(html).toContain('data-path="ui.core.codex.cli_args"');
    expect(html).toContain('data-path="ui.core.codex.resume_enabled"');
  });

  it("shows codex selected with codex-specific controls and hides Claude-only fields", () => {
    const config = getDefaultConfig() as NightgaugeConfig;
    config.ui = {
      ...config.ui,
      core: {
        ...config.ui?.core,
        adapter: "codex",
      },
    };

    const html = getSettingsHtml({ cspSource: "test-csp" } as any, config);

    expect(html).toContain('<option value="codex" selected>');
    expect(html).toContain('id="core-claude-settings" style="display:none;"');
    expect(html).toContain('id="core-codex-settings"');
    expect(html).toContain("Codex runs through your local");
    expect(html).toContain('data-path="ui.core.codex.model"');
    expect(html).toContain('data-path="ui.core.codex.cli_command"');
    expect(html).toContain('data-path="ui.core.codex.cli_args"');
    expect(html).toContain('data-path="ui.core.codex.resume_enabled"');
    expect(html).toContain('id="core-non-claude-note" class="section-note" style="display:none;"');
  });

  it("renders an unset project adapter as Use Global with the inherited adapter", () => {
    const tierState = {
      currentTier: "project" as const,
      defaultEditTier: "local" as const,
      hasGlobalConfig: true,
      hasLocalConfig: false,
      hasProjectConfig: true,
      activeEnvVars: [],
    };
    const html = getSettingsHtml({ cspSource: "test-csp" } as any, {}, new Set(), {}, tierState, {
      currentTier: "project",
      adapterConfiguredInTier: false,
      inheritedGlobalAdapter: "codex",
      effectiveAdapter: "codex",
    });

    expect(html).toContain('<option value="" selected>Use Global (codex)</option>');
    expect(html).toContain('data-inherited-value="codex"');
    expect(html).toContain("Reset Project");
    expect(html).not.toContain("Reset all settings to defaults");
  });

  it("labels merged reset as Local and leaves other tiers untouched by contract", () => {
    const html = getSettingsHtml({ cspSource: "test-csp" } as any, getDefaultConfig());
    expect(html).toContain("Reset Local");
    expect(html).toContain('title="Reset Local settings only"');
  });
});
