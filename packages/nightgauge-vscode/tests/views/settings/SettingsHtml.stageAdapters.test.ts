import { describe, it, expect } from "vitest";
import { getSettingsHtml } from "../../../src/views/settings/SettingsHtml";
import { getDefaultConfig } from "../../../src/config/schema";
import type { IncrediConfig } from "../../../src/views/settings/types";

const STAGES = [
  "issue-pickup",
  "feature-planning",
  "feature-dev",
  "feature-validate",
  "pr-create",
  "pr-merge",
];

const ADAPTER_VALUES = [
  "claude",
  "codex",
  "gemini",
  "gemini-sdk",
  "lm-studio",
  "ollama",
  "copilot",
];

describe("SettingsHtml per-stage adapter matrix (Issue #3225)", () => {
  it("renders all six stage rows with adapter selects bound to pipeline.stage_adapters.<stage>", () => {
    const config = getDefaultConfig() as IncrediConfig;
    const html = getSettingsHtml({ cspSource: "test-csp" } as any, config);

    expect(html).toContain('id="stage-adapter-matrix"');
    expect(html).toContain("Per-Stage Adapter");

    for (const stage of STAGES) {
      expect(html).toContain(`data-path="pipeline.stage_adapters.${stage}"`);
      expect(html).toContain(`<select id="pipeline.stage_adapters.${stage}"`);
    }
  });

  it("includes the global-default option plus the seven adapter ids in each stage select", () => {
    const config = getDefaultConfig() as IncrediConfig;
    const html = getSettingsHtml({ cspSource: "test-csp" } as any, config);

    expect(html).toContain("(Use global default)");

    for (const adapter of ADAPTER_VALUES) {
      expect(html).toContain(`<option value="${adapter}"`);
    }
  });

  it("defaults every stage row to the empty (global default) option when no overrides are set", () => {
    const config = getDefaultConfig() as IncrediConfig;
    const html = getSettingsHtml({ cspSource: "test-csp" } as any, config);

    for (const stage of STAGES) {
      const selectMatch = html.match(
        new RegExp(`<select id="pipeline\\.stage_adapters\\.${stage}"[^>]*>([\\s\\S]*?)</select>`)
      );
      expect(selectMatch).toBeTruthy();
      expect(selectMatch![1]).toContain('<option value="" selected>');
    }
  });

  it("selects the configured override for a stage and leaves siblings on the empty option", () => {
    const config = getDefaultConfig() as IncrediConfig;
    config.pipeline = {
      ...(config.pipeline ?? {}),
      stage_adapters: { "feature-dev": "codex" },
    };
    const html = getSettingsHtml({ cspSource: "test-csp" } as any, config);

    const featureDevMatch = html.match(
      /<select id="pipeline\.stage_adapters\.feature-dev"[^>]*>([\s\S]*?)<\/select>/
    );
    expect(featureDevMatch).toBeTruthy();
    expect(featureDevMatch![1]).toContain('<option value="codex" selected>');

    for (const stage of STAGES) {
      if (stage === "feature-dev") continue;
      const m = html.match(
        new RegExp(`<select id="pipeline\\.stage_adapters\\.${stage}"[^>]*>([\\s\\S]*?)</select>`)
      );
      expect(m).toBeTruthy();
      expect(m![1]).toContain('<option value="" selected>');
    }
  });

  it("renders per-row auth indicator with data-status='unknown' and reset buttons", () => {
    const config = getDefaultConfig() as IncrediConfig;
    config.pipeline = {
      ...(config.pipeline ?? {}),
      stage_adapters: { "feature-dev": "codex" },
    };
    const html = getSettingsHtml(
      { cspSource: "test-csp" } as any,
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

    for (const stage of STAGES) {
      expect(html).toContain(
        `<span class="auth-indicator"\n                data-stage="${stage}"`.replace(
          /\n\s+/g,
          "\n                "
        )
      );
    }

    expect(html).toMatch(/<span class="auth-indicator"[^>]*data-status="unknown"/);

    for (const stage of STAGES) {
      expect(html).toContain(
        `class="btn reset-stage-adapter-btn"\n                  data-path="pipeline.stage_adapters.${stage}"`.replace(
          /\n\s+/g,
          "\n                  "
        )
      );
    }

    const featureDevResetMatch = html.match(
      /class="btn reset-stage-adapter-btn"[^>]*data-stage="feature-dev"[^>]*>/
    );
    expect(featureDevResetMatch).toBeTruthy();
    expect(featureDevResetMatch![0]).not.toContain("disabled");

    const issuePickupResetMatch = html.match(
      /class="btn reset-stage-adapter-btn"[^>]*data-stage="issue-pickup"[^>]*>/
    );
    expect(issuePickupResetMatch).toBeTruthy();
    expect(issuePickupResetMatch![0]).toContain("disabled");
  });

  it("places the matrix immediately after the global ui.core.adapter row", () => {
    const config = getDefaultConfig() as IncrediConfig;
    const html = getSettingsHtml({ cspSource: "test-csp" } as any, config);

    const globalAdapterIdx = html.indexOf('id="ui.core.adapter"');
    const matrixIdx = html.indexOf('id="stage-adapter-matrix"');
    const claudeSettingsIdx = html.indexOf('id="core-claude-settings"');

    expect(globalAdapterIdx).toBeGreaterThan(0);
    expect(matrixIdx).toBeGreaterThan(globalAdapterIdx);
    expect(claudeSettingsIdx).toBeGreaterThan(matrixIdx);
  });

  it("renders the mode-aware preview when stageAdapterPreview is supplied", () => {
    const config = getDefaultConfig() as IncrediConfig;
    const html = getSettingsHtml(
      { cspSource: "test-csp" } as any,
      config,
      new Set(),
      {},
      undefined,
      {
        performanceMode: "elevated",
        stageAdapterPreview: STAGES.map((stage) => ({
          stage,
          adapter: "claude",
          source: "default",
          model: "(adapter default)",
        })),
      }
    );

    expect(html).toContain("Mode-aware Resolution Preview");
    expect(html).toContain("(elevated)");
    expect(html).toContain('class="stage-adapter-preview-table"');
    for (const stage of STAGES) {
      expect(html).toContain(`<tr data-stage="${stage}">`);
    }
  });

  it("hides the preview when stageAdapterPreview is empty/undefined", () => {
    const config = getDefaultConfig() as IncrediConfig;
    const html = getSettingsHtml({ cspSource: "test-csp" } as any, config);

    expect(html).not.toContain("Mode-aware Resolution Preview");
    expect(html).not.toContain('class="stage-adapter-preview-table"');
  });

  it("flags model mismatches in the preview table", () => {
    const config = getDefaultConfig() as IncrediConfig;
    const html = getSettingsHtml(
      { cspSource: "test-csp" } as any,
      config,
      new Set(),
      {},
      undefined,
      {
        performanceMode: "maximum",
        stageAdapterPreview: [
          {
            stage: "feature-dev",
            adapter: "lm-studio",
            source: "stage-config",
            model: "opus",
            modelMismatch: true,
          },
        ],
      }
    );

    expect(html).toContain('class="preview-mismatch"');
    expect(html).toContain("(mismatch)");
  });
});

const MODEL_TIERS = ["haiku", "sonnet", "opus", "fable"];

describe("SettingsHtml per-stage model selector (Issue #4030)", () => {
  it("renders a model select bound to pipeline.stage_models.<stage> for every stage", () => {
    const config = getDefaultConfig() as IncrediConfig;
    const html = getSettingsHtml({ cspSource: "test-csp" } as any, config);

    for (const stage of STAGES) {
      expect(html).toContain(`data-path="pipeline.stage_models.${stage}"`);
      expect(html).toContain(`<select id="pipeline.stage_models.${stage}"`);
    }
  });

  it("offers the canonical model tiers plus a global-default option", () => {
    const config = getDefaultConfig() as IncrediConfig;
    const html = getSettingsHtml({ cspSource: "test-csp" } as any, config);

    const m = html.match(
      /<select id="pipeline\.stage_models\.feature-dev"[^>]*>([\s\S]*?)<\/select>/
    );
    expect(m).toBeTruthy();
    expect(m![1]).toContain('<option value="" selected>');
    for (const tier of MODEL_TIERS) {
      expect(m![1]).toContain(`<option value="${tier}"`);
    }
  });

  it("selects the configured per-stage model and leaves siblings on global default", () => {
    const config = getDefaultConfig() as IncrediConfig;
    config.pipeline = {
      ...(config.pipeline ?? {}),
      stage_models: { "feature-dev": "opus" },
    };
    const html = getSettingsHtml({ cspSource: "test-csp" } as any, config);

    const fd = html.match(
      /<select id="pipeline\.stage_models\.feature-dev"[^>]*>([\s\S]*?)<\/select>/
    );
    expect(fd![1]).toContain('<option value="opus" selected>');

    const pickup = html.match(
      /<select id="pipeline\.stage_models\.issue-pickup"[^>]*>([\s\S]*?)<\/select>/
    );
    expect(pickup![1]).toContain('<option value="" selected>');
  });
});
