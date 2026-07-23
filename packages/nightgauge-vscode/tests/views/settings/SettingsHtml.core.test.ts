import { describe, it, expect } from "vitest";
import { getSettingsHtml } from "../../../src/views/settings/SettingsHtml";
import { getDefaultConfig } from "../../../src/config/schema";
import type { IncrediConfig } from "../../../src/views/settings/types";

describe("SettingsHtml core section", () => {
  it("renders execution adapter control in Nightgauge Settings view", () => {
    const config = getDefaultConfig() as IncrediConfig;
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
    expect(html).toContain('<option value="lm-studio"');
    expect(html).toContain('<option value="ollama"');
    expect(html).toContain('<option value="copilot"');
    expect(html).toContain("Refresh Models");
    expect(html).toContain("Use Max");
    expect(html).toContain('data-path="lm_studio.context_length"');
    expect(html).toContain('data-path="ui.core.auth_provider"');
    expect(html).toContain('data-path="ui.core.default_model"');
    expect(html).toContain('data-path="ui.core.codex.model"');
    expect(html).toContain('<option value="gpt-5.5"');
    expect(html).toContain('<option value="gpt-5.4"');
    expect(html).toContain('<option value="gpt-5.4-mini"');
    expect(html).toContain("Refresh Models");
    expect(html).toContain('data-path="ui.core.codex.cli_command"');
    expect(html).toContain('data-path="ui.core.codex.cli_args"');
    expect(html).toContain('data-path="ui.core.codex.resume_enabled"');
  });

  it("renders LM Studio models into the dropdown", () => {
    const config = getDefaultConfig() as IncrediConfig;
    config.ui = {
      ...config.ui,
      core: {
        ...config.ui?.core,
        adapter: "lm-studio",
      },
    };
    config.lm_studio = {
      ...config.lm_studio,
      model: "openai/gpt-oss-20b",
    };

    const html = getSettingsHtml(
      { cspSource: "test-csp" } as any,
      config,
      new Set(),
      {},
      undefined,
      {
        lmStudioModels: [
          {
            id: "openai/gpt-oss-20b",
            loaded: true,
            maxContextLength: 65536,
            currentContextLength: 32768,
          },
          { id: "qwen2.5-coder-7b", loaded: false },
        ],
      }
    );

    expect(html).toContain('<select id="lm_studio.model"');
    expect(html).toContain(
      '<option value="openai/gpt-oss-20b" selected data-max-context-length="65536" data-current-context-length="32768">openai/gpt-oss-20b (loaded)</option>'
    );
    expect(html).toContain('<option value="qwen2.5-coder-7b" >qwen2.5-coder-7b</option>');
    expect(html).toContain('data-max-context-length="65536"');
    expect(html).toContain('data-current-context-length="32768"');
  });

  it("shows codex selected with codex-specific controls and hides Claude-only fields", () => {
    const config = getDefaultConfig() as IncrediConfig;
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
    const html = getSettingsHtml(
      { cspSource: "test-csp" } as any,
      {},
      new Set(),
      {},
      tierState,
      {
        currentTier: "project",
        adapterConfiguredInTier: false,
        inheritedGlobalAdapter: "codex",
        effectiveAdapter: "codex",
      }
    );

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
