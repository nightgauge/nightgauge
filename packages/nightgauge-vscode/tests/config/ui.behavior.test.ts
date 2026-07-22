/**
 * Behavior tests for ui.* configuration fields
 *
 * These tests verify that UI config fields validate correctly and have proper
 * default values. UI settings control dashboard, notifications, output window,
 * sidebar, and other VSCode-specific behaviors.
 *
 * @see Issue #472 - Add UI config sections to Zod schema
 * @see packages/nightgauge-vscode/src/config/schema.ts - UIConfigSchema
 */

import { describe, it, expect } from "vitest";
import {
  UIConfigSchema,
  UICoreConfigSchema,
  UIDashboardConfigSchema,
  UIOutputWindowConfigSchema,
  UINotificationsConfigSchema,
  UIReadyItemsConfigSchema,
  UISidebarConfigSchema,
  UIPipelineUIConfigSchema,
  UIProjectBoardConfigSchema,
  UIWarningsConfigSchema,
  UIPluginsConfigSchema,
  mergeWithDefaults,
  DEFAULT_CONFIG,
} from "../../src/config/schema";

describe("ui.behavior", () => {
  // ============================================================================
  // Schema Validation Tests
  // ============================================================================

  describe("validation", () => {
    it("validates complete UI config", () => {
      const fullConfig = {
        core: {
          adapter: "claude",
          auth_provider: "max",
          default_model: "sonnet",
          context_path: ".nightgauge/pipeline",
          plans_path: ".nightgauge/plans",
        },
        dashboard: {
          time_savings: {
            issue_pickup: 5,
            feature_planning: 30,
            feature_dev: 120,
            pr_create: 10,
            pr_merge: 5,
          },
        },
        output_window: {
          auto_open: true,
          auto_scroll: true,
          verbose_level: "normal",
          show_token_usage: true,
          word_wrap: true,
        },
        notifications: {
          enabled: true,
          sounds: {
            enabled: true,
            alert: "Glass",
            success: "Hero",
            error: "Basso",
            volume: 0.5,
          },
          banner_enabled: true,
          dock_bounce_enabled: true,
          respect_do_not_disturb: true,
        },
        ready_items: {
          auto_refresh: false,
          refresh_interval: 300,
          sort_by: "smart",
          sort_direction: "asc",
          filters: {
            priority: "all",
            size: "all",
            component: "all",
          },
          search_text: "",
          show_dependencies: true,
        },
        sidebar: {
          hide_empty_sections: false,
        },
        pipeline: {
          auto_continue: true,
          auto_continue_delay: 1000,
        },
        project_board: {
          group_by_epic: true,
          default_epic_collapsed: false,
        },
        warnings: {
          enabled: true,
          warn_on_in_progress: true,
          warn_on_in_review: true,
        },
        plugins: {
          auto_prompt: true,
          marketplace_url: "https://github.com/nightgauge/nightgauge.git",
        },
      };

      const result = UIConfigSchema.safeParse(fullConfig);
      expect(result.success).toBe(true);
    });

    it("validates partial UI config", () => {
      const partialConfig = {
        core: { auth_provider: "bedrock" },
        output_window: { verbose_level: "debug" },
      };
      const result = UIConfigSchema.safeParse(partialConfig);
      expect(result.success).toBe(true);
    });

    it("validates empty UI config", () => {
      const result = UIConfigSchema.safeParse({});
      expect(result.success).toBe(true);
    });

    it("rejects invalid auth_provider", () => {
      const result = UICoreConfigSchema.safeParse({
        auth_provider: "invalid",
      });
      expect(result.success).toBe(false);
    });

    it("rejects invalid verbose_level", () => {
      const result = UIOutputWindowConfigSchema.safeParse({
        verbose_level: "invalid",
      });
      expect(result.success).toBe(false);
    });

    it("rejects invalid sort_by", () => {
      const result = UIReadyItemsConfigSchema.safeParse({
        sort_by: "invalid",
      });
      expect(result.success).toBe(false);
    });
  });

  // ============================================================================
  // Default Values Tests
  // ============================================================================

  describe("default values", () => {
    it("DEFAULT_CONFIG.ui has correct defaults", () => {
      expect(DEFAULT_CONFIG.ui?.core?.auth_provider).toBe("max");
      expect(DEFAULT_CONFIG.ui?.core?.adapter).toBe("claude");
      expect(DEFAULT_CONFIG.ui?.core?.default_model).toBe("sonnet");
      expect(DEFAULT_CONFIG.ui?.core?.context_path).toBe(".nightgauge/pipeline");
      expect(DEFAULT_CONFIG.ui?.core?.plans_path).toBe(".nightgauge/plans");

      expect(DEFAULT_CONFIG.ui?.dashboard?.time_savings?.issue_pickup).toBe(5);
      expect(DEFAULT_CONFIG.ui?.dashboard?.time_savings?.feature_planning).toBe(30);
      expect(DEFAULT_CONFIG.ui?.dashboard?.time_savings?.feature_dev).toBe(120);
      expect(DEFAULT_CONFIG.ui?.dashboard?.time_savings?.pr_create).toBe(10);
      expect(DEFAULT_CONFIG.ui?.dashboard?.time_savings?.pr_merge).toBe(5);

      expect(DEFAULT_CONFIG.ui?.output_window?.auto_open).toBe(true);
      expect(DEFAULT_CONFIG.ui?.output_window?.auto_scroll).toBe(true);
      expect(DEFAULT_CONFIG.ui?.output_window?.verbose_level).toBe("normal");
      expect(DEFAULT_CONFIG.ui?.output_window?.show_token_usage).toBe(true);
      expect(DEFAULT_CONFIG.ui?.output_window?.word_wrap).toBe(true);

      expect(DEFAULT_CONFIG.ui?.notifications?.enabled).toBe(true);
      expect(DEFAULT_CONFIG.ui?.notifications?.sounds?.enabled).toBe(true);
      expect(DEFAULT_CONFIG.ui?.notifications?.sounds?.alert).toBe("Glass");
      expect(DEFAULT_CONFIG.ui?.notifications?.sounds?.success).toBe("Hero");
      expect(DEFAULT_CONFIG.ui?.notifications?.sounds?.error).toBe("Basso");
      expect(DEFAULT_CONFIG.ui?.notifications?.sounds?.volume).toBe(0.5);
      expect(DEFAULT_CONFIG.ui?.notifications?.banner_enabled).toBe(true);
      expect(DEFAULT_CONFIG.ui?.notifications?.dock_bounce_enabled).toBe(true);
      expect(DEFAULT_CONFIG.ui?.notifications?.respect_do_not_disturb).toBe(true);

      expect(DEFAULT_CONFIG.ui?.ready_items?.auto_refresh).toBe(false);
      expect(DEFAULT_CONFIG.ui?.ready_items?.refresh_interval).toBe(600);
      expect(DEFAULT_CONFIG.ui?.ready_items?.sort_by).toBe("board");
      expect(DEFAULT_CONFIG.ui?.ready_items?.sort_direction).toBe("asc");
      expect(DEFAULT_CONFIG.ui?.ready_items?.filters?.priority).toBe("all");
      expect(DEFAULT_CONFIG.ui?.ready_items?.filters?.size).toBe("all");
      expect(DEFAULT_CONFIG.ui?.ready_items?.filters?.component).toBe("all");
      expect(DEFAULT_CONFIG.ui?.ready_items?.show_dependencies).toBe(true);

      expect(DEFAULT_CONFIG.ui?.sidebar?.hide_empty_sections).toBe(false);

      expect(DEFAULT_CONFIG.ui?.pipeline?.auto_continue).toBe(true);
      expect(DEFAULT_CONFIG.ui?.pipeline?.auto_continue_delay).toBe(500);

      expect(DEFAULT_CONFIG.ui?.project_board?.group_by_epic).toBe(true);
      expect(DEFAULT_CONFIG.ui?.project_board?.default_epic_collapsed).toBe(true);

      expect(DEFAULT_CONFIG.ui?.warnings?.enabled).toBe(true);
      expect(DEFAULT_CONFIG.ui?.warnings?.warn_on_in_progress).toBe(true);
      expect(DEFAULT_CONFIG.ui?.warnings?.warn_on_in_review).toBe(true);

      expect(DEFAULT_CONFIG.ui?.plugins?.auto_prompt).toBe(true);
      expect(DEFAULT_CONFIG.ui?.plugins?.marketplace_url).toBe(
        "https://github.com/nightgauge/nightgauge.git"
      );
    });

    it("mergeWithDefaults preserves user UI values", () => {
      const config = mergeWithDefaults({
        ui: {
          core: { auth_provider: "bedrock" },
          output_window: { verbose_level: "debug" },
        },
      });

      expect(config.ui?.core?.auth_provider).toBe("bedrock");
      expect(config.ui?.output_window?.verbose_level).toBe("debug");
    });

    it("missing ui section uses defaults", () => {
      const config = mergeWithDefaults({});

      expect(config.ui?.core?.auth_provider).toBe("max");
      expect(config.ui?.notifications?.sounds?.alert).toBe("Glass");
    });
  });

  // ============================================================================
  // Sub-Schema Validation Tests
  // ============================================================================

  describe("sub-schema validation", () => {
    it("validates core config", () => {
      const result = UICoreConfigSchema.safeParse({
        auth_provider: "vertex",
        default_model: "opus",
      });
      expect(result.success).toBe(true);
    });

    it("validates dashboard config", () => {
      const result = UIDashboardConfigSchema.safeParse({
        time_savings: {
          issue_pickup: 10,
          feature_dev: 240,
        },
      });
      expect(result.success).toBe(true);
    });

    it("validates output_window config", () => {
      const result = UIOutputWindowConfigSchema.safeParse({
        verbose_level: "verbose",
        auto_scroll: false,
      });
      expect(result.success).toBe(true);
    });

    it("validates notifications config", () => {
      const result = UINotificationsConfigSchema.safeParse({
        enabled: false,
        sounds: { alert: "Ping", volume: 0.75 },
      });
      expect(result.success).toBe(true);
    });

    it("validates ready_items config", () => {
      const result = UIReadyItemsConfigSchema.safeParse({
        sort_by: "priority",
        sort_direction: "desc",
        filters: { priority: "P0" },
      });
      expect(result.success).toBe(true);
    });

    it("validates sidebar config", () => {
      const result = UISidebarConfigSchema.safeParse({
        hide_empty_sections: true,
      });
      expect(result.success).toBe(true);
    });

    it("validates pipeline UI config", () => {
      const result = UIPipelineUIConfigSchema.safeParse({
        auto_continue: false,
        auto_continue_delay: 5000,
      });
      expect(result.success).toBe(true);
    });

    it("validates project_board config", () => {
      const result = UIProjectBoardConfigSchema.safeParse({
        group_by_epic: false,
        default_epic_collapsed: true,
      });
      expect(result.success).toBe(true);
    });

    it("validates warnings config", () => {
      const result = UIWarningsConfigSchema.safeParse({
        enabled: true,
        warn_on_in_progress: false,
      });
      expect(result.success).toBe(true);
    });

    it("validates plugins config", () => {
      const result = UIPluginsConfigSchema.safeParse({
        auto_prompt: false,
        marketplace_url: "https://example.com/plugins.git",
      });
      expect(result.success).toBe(true);
    });
  });
});
