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
    for (const adapter of [
      "codex",
      "gemini",
      "gemini-sdk",
      "lm-studio",
      "ollama",
      "copilot",
      "opencode",
    ]) {
      expect(systemPromptPresetForAdapter(adapter)).toBeUndefined();
    }
  });

  it("returns undefined for opencode regardless of which provider its model resolves to (#1622)", () => {
    // opencode never receives the claude_code preset, even when its model is
    // Anthropic's (ADR-022): steering here is keyed by the HOST ADAPTER, never
    // by the resolved provider — the OpenCode CLI does not consume this preset
    // and gets its guidance from the per-run config's `instructions` #1626
    // provisions instead.
    // This function takes no model parameter, so "regardless of model" holds
    // for every model an opencode run could dispatch to, cloud or local.
    expect(systemPromptPresetForAdapter("opencode")).toBeUndefined();
  });
});
