/**
 * providerSteering — declares HOW each adapter receives system-level steering,
 * so the shared StageExecutor path carries no Claude-only assumptions.
 *
 * Each provider receives baseline guidance differently:
 *  - Claude (`claude-sdk` / `claude-headless`, and the default adapter-less
 *    path): the `claude_code` SDK system-prompt preset.
 *  - Codex: AGENTS.md — see {@link CodexContextGenerator}.
 *  - Gemini (`gemini` / `gemini-sdk`): GEMINI.md — see {@link GeminiContextGenerator}.
 *  - lm-studio / ollama / copilot: no system-prompt preset; their CLIs ignore it
 *    and receive guidance via the prompt.
 *
 * @see Issue #4028 - Provider-aware system steering
 */

/** Adapters that understand the `claude_code` SDK system-prompt preset. */
const CLAUDE_PRESET_ADAPTERS = new Set(["claude-sdk", "claude-headless"]);

/**
 * Resolve the system-prompt directive for an adapter's query options.
 *
 * Returns the `claude_code` preset only for Claude adapters (and the default,
 * adapter-less Claude path); returns `undefined` for every other provider so the
 * shared StageExecutor path never emits a Claude-only preset to a CLI that would
 * ignore it. Non-Claude providers get their steering from files (AGENTS.md /
 * GEMINI.md) or the prompt instead.
 */
export function systemPromptPresetForAdapter(
  adapter?: string
): { type: string; preset?: string } | undefined {
  if (adapter === undefined || CLAUDE_PRESET_ADAPTERS.has(adapter)) {
    return { type: "preset", preset: "claude_code" };
  }
  return undefined;
}
