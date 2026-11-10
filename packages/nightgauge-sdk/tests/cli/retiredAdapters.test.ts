import { describe, it, expect } from "vitest";
import { isRetiredAdapter, retiredAdapterMessage } from "../../src/cli/adapters/retiredAdapters.js";

describe("retiredAdapters (#2128)", () => {
  describe("isRetiredAdapter", () => {
    it("returns true for all retired adapter identifiers", () => {
      const retired = ["lm-studio", "lm_studio", "lmstudio", "ollama"];
      for (const name of retired) {
        expect(isRetiredAdapter(name)).toBe(true);
      }
    });

    it("handles case-insensitivity and surrounding whitespace", () => {
      expect(isRetiredAdapter("  OLLAMA  ")).toBe(true);
      expect(isRetiredAdapter("Lm-Studio")).toBe(true);
      expect(isRetiredAdapter(" LM_STUDIO ")).toBe(true);
      expect(isRetiredAdapter("LMSTUDIO")).toBe(true);
    });

    it("returns false for active adapters", () => {
      const active = [
        "claude-headless",
        "claude-sdk",
        "codex",
        "gemini",
        "gemini-sdk",
        "grok",
        "copilot",
        "opencode",
        "openai-compatible",
      ];
      for (const name of active) {
        expect(isRetiredAdapter(name)).toBe(false);
      }
    });

    it("returns false for non-string, empty, or undefined inputs", () => {
      expect(isRetiredAdapter(undefined)).toBe(false);
      expect(isRetiredAdapter(null)).toBe(false);
      expect(isRetiredAdapter("")).toBe(false);
      expect(isRetiredAdapter("   ")).toBe(false);
    });
  });

  describe("retiredAdapterMessage", () => {
    it("returns undefined for non-retired adapter names", () => {
      expect(retiredAdapterMessage("claude", "config.adapter")).toBeUndefined();
      expect(retiredAdapterMessage(undefined, "NIGHTGAUGE_ADAPTER")).toBeUndefined();
      expect(retiredAdapterMessage(null, "--adapter")).toBeUndefined();
      expect(retiredAdapterMessage("", "env")).toBeUndefined();
    });

    it("returns an actionable migration message naming the setting and removed adapter", () => {
      const msg = retiredAdapterMessage("ollama", "config.adapter");
      expect(msg).toBeDefined();
      expect(msg).toContain('config.adapter names adapter "ollama"');
      expect(msg).toContain("which was removed (#2128)");
      expect(msg).toContain("opencode");
      expect(msg).toContain("openai-compatible");
    });

    it("includes migration guidance for lm-studio", () => {
      const msg = retiredAdapterMessage("lm-studio", "NIGHTGAUGE_ADAPTER");
      expect(msg).toBeDefined();
      expect(msg).toContain('NIGHTGAUGE_ADAPTER names adapter "lm-studio"');
      expect(msg).toContain("LM Studio, Ollama, llama.cpp, vLLM");
    });
  });
});
