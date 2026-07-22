/**
 * Tests for provider-aware system steering selection.
 *
 * @see Issue #4028 - Provider-aware system steering
 */

import { describe, it, expect } from "vitest";
import { systemPromptPresetForAdapter } from "../providerSteering.js";

describe("systemPromptPresetForAdapter (#4028)", () => {
  const CLAUDE_PRESET = { type: "preset", preset: "claude_code" };

  it("returns the claude_code preset for Claude adapters", () => {
    expect(systemPromptPresetForAdapter("claude-sdk")).toEqual(CLAUDE_PRESET);
    expect(systemPromptPresetForAdapter("claude-headless")).toEqual(CLAUDE_PRESET);
  });

  it("returns the claude_code preset for the default adapter-less path", () => {
    // The orchestrator's default queryFn is the Claude SDK; preserve that behavior.
    expect(systemPromptPresetForAdapter(undefined)).toEqual(CLAUDE_PRESET);
  });

  it("returns undefined for every non-Claude adapter (no Claude-only preset leak)", () => {
    for (const adapter of ["codex", "gemini", "gemini-sdk", "lm-studio", "ollama", "copilot"]) {
      expect(systemPromptPresetForAdapter(adapter)).toBeUndefined();
    }
  });
});
