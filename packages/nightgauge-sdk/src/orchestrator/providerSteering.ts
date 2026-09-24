/**
 * providerSteering — declares HOW each adapter receives system-level steering,
 * so the shared StageExecutor path carries no Claude-only assumptions.
 *
 * Each provider receives baseline guidance differently:
 *  - Claude (`claude-sdk` / `claude-headless`, and the default adapter-less
 *    path): the `claude_code` SDK system-prompt preset.
 *  - Codex: AGENTS.md — see {@link CodexContextGenerator}.
 *  - Gemini (`gemini` / `gemini-sdk`): GEMINI.md — see {@link GeminiContextGenerator}.
 *  - lm-studio / ollama / copilot / opencode: no system-prompt preset; their
 *    CLIs ignore it and receive guidance via the prompt or a file. `opencode`
 *    never gets the `claude_code` preset even when its model is Anthropic's
 *    (ADR-022) — the OpenCode CLI does not consume that preset, and steering
 *    here is keyed by the HOST ADAPTER, never by the resolved provider; its
 *    guidance is the per-run config's `instructions` #1626 provisions (the
 *    repository's steering files and the run's own steering.md), not a file
 *    written into the worktree.
 *
 * @see Issue #4028 - Provider-aware system steering
 * @see Issue #1622 - opencode is model-aware for provider resolution
 *      elsewhere (cost, eval, calibration) but NOT here: this function takes
 *      no model, by design, so it cannot key on one.
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
 * GEMINI.md) or the prompt instead. `opencode` is always `undefined` here,
 * regardless of which provider its model resolves to (ADR-022): this function
 * is keyed on the host adapter, never the model-derived provider, and takes no
 * model parameter for that reason.
 */
export function systemPromptPresetForAdapter(
  adapter?: string
): { type: string; preset?: string } | undefined {
  if (adapter === undefined || CLAUDE_PRESET_ADAPTERS.has(adapter)) {
    return { type: "preset", preset: "claude_code" };
  }
  return undefined;
}
