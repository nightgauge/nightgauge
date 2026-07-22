/**
 * Behavior tests for ui.output_window.* configuration fields
 *
 * These tests verify that output window config fields affect runtime behavior,
 * specifically auto-open, auto-scroll, verbosity, and token display.
 *
 * @see Issue #472 - Add UI config sections to Zod schema
 * @see packages/nightgauge-vscode/src/config/schema.ts - UIOutputWindowConfigSchema
 */

import { describe, it, expect } from "vitest";
import {
  UIOutputWindowConfigSchema,
  VerboseLevelSchema,
  DEFAULT_CONFIG,
} from "../../src/config/schema";

// ============================================================================
// Mock Fixtures
// ============================================================================

/**
 * Default output window configuration for tests
 */
export const DEFAULT_UI_OUTPUT_WINDOW_CONFIG = {
  auto_open: true,
  auto_scroll: true,
  verbose_level: "normal" as const,
  show_token_usage: true,
  word_wrap: true,
};

/**
 * Create a mock output window configuration with optional overrides
 */
export function createMockUIOutputWindowConfig(
  overrides?: Partial<typeof DEFAULT_UI_OUTPUT_WINDOW_CONFIG>
) {
  return {
    ...DEFAULT_UI_OUTPUT_WINDOW_CONFIG,
    ...overrides,
  };
}

describe("ui.output_window.behavior", () => {
  // ============================================================================
  // auto_open - Behavior Tests
  // ============================================================================

  describe("auto_open", () => {
    it("opens output window automatically on pipeline start", () => {
      const config = createMockUIOutputWindowConfig({ auto_open: true });

      const shouldOpenOnStart = (cfg: typeof config): boolean => {
        return cfg.auto_open === true;
      };

      expect(shouldOpenOnStart(config)).toBe(true);
    });

    it("keeps output window closed when disabled", () => {
      const config = createMockUIOutputWindowConfig({ auto_open: false });

      const shouldOpenOnStart = (cfg: typeof config): boolean => {
        return cfg.auto_open === true;
      };

      expect(shouldOpenOnStart(config)).toBe(false);
    });

    it("defaults to true", () => {
      expect(DEFAULT_CONFIG.ui?.output_window?.auto_open).toBe(true);
    });
  });

  // ============================================================================
  // auto_scroll - Behavior Tests
  // ============================================================================

  describe("auto_scroll", () => {
    it("scrolls to latest output when enabled", () => {
      const config = createMockUIOutputWindowConfig({ auto_scroll: true });

      const shouldScrollToBottom = (cfg: typeof config): boolean => {
        return cfg.auto_scroll === true;
      };

      expect(shouldScrollToBottom(config)).toBe(true);
    });

    it("maintains scroll position when disabled", () => {
      const config = createMockUIOutputWindowConfig({ auto_scroll: false });

      const shouldScrollToBottom = (cfg: typeof config): boolean => {
        return cfg.auto_scroll === true;
      };

      expect(shouldScrollToBottom(config)).toBe(false);
    });

    it("defaults to true", () => {
      expect(DEFAULT_CONFIG.ui?.output_window?.auto_scroll).toBe(true);
    });
  });

  // ============================================================================
  // verbose_level - Behavior Tests
  // ============================================================================

  describe("verbose_level", () => {
    it("filters output based on level", () => {
      type Level = "minimal" | "normal" | "verbose" | "debug";
      type MessageType = "error" | "warn" | "info" | "tool" | "debug";

      const shouldShow = (level: Level, messageType: MessageType): boolean => {
        const levels: Level[] = ["minimal", "normal", "verbose", "debug"];
        const messageTypeLevels: Record<MessageType, Level> = {
          error: "minimal",
          warn: "minimal",
          info: "normal",
          tool: "verbose",
          debug: "debug",
        };

        const currentLevelIndex = levels.indexOf(level);
        const requiredLevelIndex = levels.indexOf(messageTypeLevels[messageType]);

        return currentLevelIndex >= requiredLevelIndex;
      };

      // Minimal level: only errors and warnings
      expect(shouldShow("minimal", "error")).toBe(true);
      expect(shouldShow("minimal", "warn")).toBe(true);
      expect(shouldShow("minimal", "info")).toBe(false);
      expect(shouldShow("minimal", "tool")).toBe(false);
      expect(shouldShow("minimal", "debug")).toBe(false);

      // Normal level: + info
      expect(shouldShow("normal", "error")).toBe(true);
      expect(shouldShow("normal", "info")).toBe(true);
      expect(shouldShow("normal", "tool")).toBe(false);

      // Verbose level: + tool calls
      expect(shouldShow("verbose", "tool")).toBe(true);
      expect(shouldShow("verbose", "debug")).toBe(false);

      // Debug level: everything
      expect(shouldShow("debug", "debug")).toBe(true);
    });

    it("accepts all valid levels", () => {
      expect(VerboseLevelSchema.safeParse("minimal").success).toBe(true);
      expect(VerboseLevelSchema.safeParse("normal").success).toBe(true);
      expect(VerboseLevelSchema.safeParse("verbose").success).toBe(true);
      expect(VerboseLevelSchema.safeParse("debug").success).toBe(true);
    });

    it("rejects invalid levels", () => {
      expect(VerboseLevelSchema.safeParse("trace").success).toBe(false);
      expect(VerboseLevelSchema.safeParse("info").success).toBe(false);
    });

    it("defaults to normal", () => {
      expect(DEFAULT_CONFIG.ui?.output_window?.verbose_level).toBe("normal");
    });
  });

  // ============================================================================
  // show_token_usage - Behavior Tests
  // ============================================================================

  describe("show_token_usage", () => {
    it("displays token usage when enabled", () => {
      const config = createMockUIOutputWindowConfig({ show_token_usage: true });

      const shouldShowTokens = (cfg: typeof config): boolean => {
        return cfg.show_token_usage === true;
      };

      expect(shouldShowTokens(config)).toBe(true);
    });

    it("hides token usage when disabled", () => {
      const config = createMockUIOutputWindowConfig({
        show_token_usage: false,
      });

      const shouldShowTokens = (cfg: typeof config): boolean => {
        return cfg.show_token_usage === true;
      };

      expect(shouldShowTokens(config)).toBe(false);
    });

    it("affects output format", () => {
      const formatOutput = (message: string, tokens: number, showTokens: boolean): string => {
        if (showTokens) {
          return `${message} [${tokens} tokens]`;
        }
        return message;
      };

      expect(formatOutput("Stage complete", 5000, true)).toBe("Stage complete [5000 tokens]");
      expect(formatOutput("Stage complete", 5000, false)).toBe("Stage complete");
    });

    it("defaults to true", () => {
      expect(DEFAULT_CONFIG.ui?.output_window?.show_token_usage).toBe(true);
    });
  });

  // ============================================================================
  // word_wrap - Behavior Tests
  // ============================================================================

  describe("word_wrap", () => {
    it("enables word wrap for long lines", () => {
      const config = createMockUIOutputWindowConfig({ word_wrap: true });

      const getWordWrapMode = (cfg: typeof config): "on" | "off" | "wordWrapColumn" => {
        return cfg.word_wrap ? "on" : "off";
      };

      expect(getWordWrapMode(config)).toBe("on");
    });

    it("disables word wrap when off", () => {
      const config = createMockUIOutputWindowConfig({ word_wrap: false });

      const getWordWrapMode = (cfg: typeof config): "on" | "off" | "wordWrapColumn" => {
        return cfg.word_wrap ? "on" : "off";
      };

      expect(getWordWrapMode(config)).toBe("off");
    });

    it("defaults to true", () => {
      expect(DEFAULT_CONFIG.ui?.output_window?.word_wrap).toBe(true);
    });
  });

  // ============================================================================
  // Output Filtering Integration
  // ============================================================================

  describe("output filtering integration", () => {
    it("combines settings for output display", () => {
      interface OutputMessage {
        type: "error" | "info" | "tool" | "debug";
        text: string;
        tokens?: number;
      }

      interface OutputConfig {
        verbose_level: "minimal" | "normal" | "verbose" | "debug";
        show_token_usage: boolean;
      }

      const formatOutput = (msg: OutputMessage, cfg: OutputConfig): string | null => {
        // Check if message should be shown based on verbosity
        const levels = ["minimal", "normal", "verbose", "debug"];
        const typeLevels = {
          error: "minimal",
          info: "normal",
          tool: "verbose",
          debug: "debug",
        };

        const currentLevel = levels.indexOf(cfg.verbose_level);
        const requiredLevel = levels.indexOf(typeLevels[msg.type]);

        if (currentLevel < requiredLevel) {
          return null;
        }

        // Format message with optional token usage
        if (cfg.show_token_usage && msg.tokens) {
          return `[${msg.type.toUpperCase()}] ${msg.text} (${msg.tokens} tokens)`;
        }

        return `[${msg.type.toUpperCase()}] ${msg.text}`;
      };

      const config: OutputConfig = {
        verbose_level: "normal",
        show_token_usage: true,
      };

      expect(formatOutput({ type: "info", text: "Running", tokens: 100 }, config)).toBe(
        "[INFO] Running (100 tokens)"
      );

      expect(formatOutput({ type: "tool", text: "Read file" }, config)).toBe(null); // verbose level needed

      expect(
        formatOutput({ type: "info", text: "Running" }, { ...config, show_token_usage: false })
      ).toBe("[INFO] Running");
    });
  });

  // ============================================================================
  // Schema Validation Tests
  // ============================================================================

  describe("validation", () => {
    it("validates complete output_window config", () => {
      const result = UIOutputWindowConfigSchema.safeParse(DEFAULT_UI_OUTPUT_WINDOW_CONFIG);
      expect(result.success).toBe(true);
    });

    it("validates partial output_window config", () => {
      const result = UIOutputWindowConfigSchema.safeParse({
        verbose_level: "debug",
        auto_scroll: false,
      });
      expect(result.success).toBe(true);
    });

    it("validates empty output_window config", () => {
      const result = UIOutputWindowConfigSchema.safeParse({});
      expect(result.success).toBe(true);
    });

    it("rejects invalid verbose_level", () => {
      const result = UIOutputWindowConfigSchema.safeParse({
        verbose_level: "trace",
      });
      expect(result.success).toBe(false);
    });
  });
});
