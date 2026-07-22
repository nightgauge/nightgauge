/**
 * Adapter-parameterized spawn profiles for the live cell executor (Issue #107).
 *
 * `LiveCellExecutor` historically spoke only the Claude CLI's flag shape
 * (`--print --output-format json --model <id> --dangerously-skip-permissions`)
 * and parsed only Claude's single-JSON-object result — so no non-Claude adapter
 * could run a live eval cell (the pre-registered executor limitation in
 * docs/spikes/77-...). A profile captures the three things that genuinely differ
 * per adapter, behind one interface, so the executor stays provider-neutral and
 * adding an adapter is one data entry, not an executor change:
 *
 *   1. the CLI command + flag shape for a headless, permissionless run;
 *   2. how the reasoning-budget axis is expressed — Claude's extended-thinking
 *      keyword ladder lives in the PROMPT, OpenAI's `model_reasoning_effort`
 *      lives in a CLI FLAG — so a profile can inject reasoning into either place;
 *   3. how the CLI's stdout is normalized to {usage, durationMs, isError}.
 *
 * Only the two adapters with a healthy local CLI today are wired — `claude`
 * (anthropic) and `codex` (openai). The **measurement** half of #107 (rerun the
 * preamble A/B on codex) is a separate follow-up that consumes this spawn.
 * Adding gemini/copilot/etc. is a new profile entry here, not an executor edit.
 *
 * @see docs/spikes/77-measure-the-behavioral-preamble-hypothesis-on-the-eval-axis.md
 * @see packages/nightgauge-sdk/src/cli/adapterQuery.ts — summarizeCodexJsonOutput (reused)
 */

import type { Provider, ReasoningLevel, TokenUsage } from "./modelEvalSchemas.js";
import { providerForAdapter } from "./modelRegistry.js";
import { summarizeCodexJsonOutput } from "../cli/adapterQuery.js";

/** Token usage reported by the Claude CLI `result` object. */
export interface ClaudeJsonUsage {
  input_tokens?: number;
  output_tokens?: number;
  cache_read_input_tokens?: number;
  cache_creation_input_tokens?: number;
}

/** The single JSON object emitted by `claude --print --output-format json`. */
export interface ClaudeJsonResult {
  type?: string;
  subtype?: string;
  is_error?: boolean;
  duration_ms?: number;
  result?: string;
  total_cost_usd?: number;
  usage?: ClaudeJsonUsage;
}

/**
 * Normalized telemetry from one spawned adapter invocation — the common shape
 * every profile's {@link EvalAdapterProfile.parseResult} produces from its own
 * CLI dialect, so the executor never branches on adapter.
 *
 * `durationMs` is the CLI-reported wall time when the adapter emits one (Claude
 * does; Codex's JSONL does not, so it reports 0 and the executor falls back to
 * its own measured elapsed time). `isError` is the adapter's own failure signal,
 * used only for the no-deterministic-checks fallback verdict.
 */
export interface SpawnTelemetry {
  usage: TokenUsage;
  durationMs: number;
  isError: boolean;
}

/**
 * Per-adapter spawn profile: the sole place adapter-specific CLI shape,
 * reasoning wiring, and output parsing live. The executor holds one of these and
 * is otherwise adapter-agnostic.
 */
export interface EvalAdapterProfile {
  /** Canonical adapter key for logs/errors, e.g. `claude` or `codex`. */
  readonly adapter: string;
  /** Registry provider this profile serves (drives model resolution + pricing). */
  readonly provider: Provider;
  /** CLI command invoked when neither an override nor the env var is set. */
  readonly defaultCommand: string;
  /** Env var overriding the CLI command (e.g. `NIGHTGAUGE_CODEX_CLI_COMMAND`). */
  readonly commandEnvVar: string;

  /**
   * Resolve the CLI command: explicit `override` wins (the executor's `command`
   * option), then the profile's env var, then the built-in default.
   */
  resolveCommand(override?: string): string;

  /**
   * CLI args for a headless, permissionless run of `model`. Adapters that carry
   * the reasoning budget in a FLAG (Codex's `-c model_reasoning_effort=...`)
   * inject it here; adapters that carry it in the PROMPT (Claude) ignore
   * `reasoning` and return an empty {@link reasoningPromptDirective}.
   */
  buildArgs(model: string, reasoning: ReasoningLevel): string[];

  /**
   * Prompt suffix requesting the reasoning budget, for adapters that steer
   * reasoning through the prompt (Claude). Returns `""` for adapters that use a
   * CLI flag instead (Codex) — the two are mutually exclusive per adapter so the
   * budget is expressed exactly once.
   */
  reasoningPromptDirective(reasoning: ReasoningLevel): string;

  /** Normalize the CLI's stdout to {@link SpawnTelemetry}, or `null` if unparseable. */
  parseResult(stdout: string): SpawnTelemetry | null;
}

// ---------------------------------------------------------------------------
// Claude (anthropic) profile — the original hardcoded behavior, extracted.
// ---------------------------------------------------------------------------

/**
 * Extract the Claude CLI result object. `--output-format json` emits exactly one
 * JSON object, but we scan from the last line as a defence against any leading
 * diagnostics on stdout.
 */
export function parseClaudeResult(stdout: string): ClaudeJsonResult | null {
  const trimmed = stdout.trim();
  if (!trimmed) return null;
  const candidates = [trimmed, ...trimmed.split(/\r?\n/).reverse()];
  for (const candidate of candidates) {
    const s = candidate.trim();
    if (!s.startsWith("{")) continue;
    try {
      const obj = JSON.parse(s) as unknown;
      if (obj && typeof obj === "object") return obj as ClaudeJsonResult;
    } catch {
      // Try the next candidate.
    }
  }
  return null;
}

/**
 * Map the reasoning axis onto Claude Code's extended-thinking keyword ladder.
 * `none` requests no extended reasoning; the keywords escalate the budget.
 */
function claudeThinkingDirective(reasoning: ReasoningLevel): string {
  switch (reasoning) {
    case "high":
      return "\n\nUltrathink: reason carefully about correctness and edge cases before writing code.";
    case "medium":
      return "\n\nThink hard about the edge cases before implementing.";
    case "low":
      return "\n\nThink about your approach before implementing.";
    default:
      return "";
  }
}

export const claudeEvalProfile: EvalAdapterProfile = {
  adapter: "claude",
  provider: "anthropic",
  defaultCommand: "claude",
  commandEnvVar: "NIGHTGAUGE_CLAUDE_CLI_COMMAND",

  resolveCommand(override) {
    return override ?? process.env[this.commandEnvVar] ?? this.defaultCommand;
  },

  buildArgs(model) {
    return [
      "--print",
      "--output-format",
      "json",
      "--model",
      model,
      // The workspace is a disposable, isolated dir — allow edits/bash without prompts.
      "--dangerously-skip-permissions",
    ];
  },

  reasoningPromptDirective(reasoning) {
    return claudeThinkingDirective(reasoning);
  },

  parseResult(stdout) {
    const parsed = parseClaudeResult(stdout);
    if (!parsed) return null;
    return {
      usage: {
        input: parsed.usage?.input_tokens ?? 0,
        output: parsed.usage?.output_tokens ?? 0,
        cache_read: parsed.usage?.cache_read_input_tokens ?? 0,
        cache_creation: parsed.usage?.cache_creation_input_tokens ?? 0,
      },
      durationMs: parsed.duration_ms ?? 0,
      isError: parsed.is_error === true,
    };
  },
};

// ---------------------------------------------------------------------------
// Codex (openai) profile — the non-Claude leg unblocked by #107.
// ---------------------------------------------------------------------------

/**
 * Map the reasoning axis onto Codex's `model_reasoning_effort` config knob.
 * `none` omits the override entirely (Codex applies its own default); the other
 * levels map 1:1 to Codex's low/medium/high reasoning-effort bands. Returned as
 * a `-c key=value` config-override pair (Codex's documented flag for overriding
 * a single config value on a non-interactive `exec` run).
 */
function codexReasoningArgs(reasoning: ReasoningLevel): string[] {
  if (reasoning === "none") return [];
  return ["-c", `model_reasoning_effort=${reasoning}`];
}

export const codexEvalProfile: EvalAdapterProfile = {
  adapter: "codex",
  provider: "openai",
  defaultCommand: "codex",
  commandEnvVar: "NIGHTGAUGE_CODEX_CLI_COMMAND",

  resolveCommand(override) {
    return override ?? process.env[this.commandEnvVar] ?? this.defaultCommand;
  },

  buildArgs(model, reasoning) {
    return [
      // `--dangerously-bypass-approvals-and-sandbox` disables BOTH the filesystem
      // sandbox and approval prompts — the documented mode for ephemeral, fully
      // isolated environments (the eval worktree is exactly that). `--json` emits
      // the JSONL event stream we parse below. Mirrors the pipeline's CodexAdapter.
      "exec",
      "--dangerously-bypass-approvals-and-sandbox",
      "--json",
      "--model",
      model,
      ...codexReasoningArgs(reasoning),
    ];
  },

  reasoningPromptDirective() {
    // Codex steers reasoning through a CLI flag (see buildArgs), not the prompt.
    return "";
  },

  parseResult(stdout) {
    // Empty stdout means Codex produced nothing to parse — mirror Claude's
    // "unparseable" signal so the executor throws rather than record a phantom
    // zero-token run. Non-empty output is summarized by the single-source-of-truth
    // Codex JSONL parser (shared with the pipeline); an early exit with no
    // `turn.completed` usage payload yields zero tokens (honest — Codex reported none).
    if (!stdout.trim()) return null;
    const summary = summarizeCodexJsonOutput(stdout);
    return {
      usage: {
        input: summary.usage?.input_tokens ?? 0,
        output: summary.usage?.output_tokens ?? 0,
        cache_read: summary.usage?.cache_read_input_tokens ?? 0,
        cache_creation: summary.usage?.cache_creation_input_tokens ?? 0,
      },
      // Codex's JSONL carries no wall-clock duration; the executor substitutes its
      // own measured elapsed time when a profile reports 0.
      durationMs: 0,
      isError: summary.hasExplicitFailure,
    };
  },
};

// ---------------------------------------------------------------------------
// Resolution
// ---------------------------------------------------------------------------

/** Provider → wired spawn profile. Adding a provider is one entry here. */
const PROFILES_BY_PROVIDER: Partial<Record<Provider, EvalAdapterProfile>> = {
  anthropic: claudeEvalProfile,
  openai: codexEvalProfile,
};

/**
 * The spawn profile for a registry `provider`. Throws for a provider whose live
 * CLI is not yet wired (gemini/copilot/ollama/lm-studio/other) — an honest,
 * actionable error instead of silently spawning the wrong CLI.
 */
export function resolveEvalAdapterProfile(provider: Provider): EvalAdapterProfile {
  const profile = PROFILES_BY_PROVIDER[provider];
  if (!profile) {
    const wired = Object.values(PROFILES_BY_PROVIDER)
      .map((p) => p.adapter)
      .join(", ");
    throw new Error(
      `live eval spawn is not implemented for provider '${provider}'. ` +
        `Wired adapters: ${wired}. Add an EvalAdapterProfile in evalAdapters.ts to enable it.`
    );
  }
  return profile;
}

/**
 * The spawn profile for an execution-adapter name (any layer's vocabulary:
 * `claude`, `claude-headless`, `codex`, …), resolved through the registry's
 * adapter→provider mapping. Used when a caller pins the adapter explicitly rather
 * than deriving it from the model's provider.
 */
export function resolveEvalAdapterProfileForAdapter(adapter: string): EvalAdapterProfile {
  return resolveEvalAdapterProfile(providerForAdapter(adapter));
}
