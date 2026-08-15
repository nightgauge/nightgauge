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
 *   2. how the effort and reasoning axes reach the CLI (#571) — REAL knobs
 *      only, the same ones the production pipeline emits. Claude: `--effort`
 *      (the flag skillRunner pushes, verified on CLI 2.1.233) plus the
 *      thinking env parameters (`CLAUDE_CODE_DISABLE_THINKING`,
 *      `MAX_THINKING_TOKENS`). Codex: `-c model_reasoning_effort=<effort>`
 *      (the CodexAdapter translation). An axis value an adapter has no knob
 *      for throws {@link UnsupportedCellError} — the cell fails as
 *      unsupported instead of running mislabeled;
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

import {
  EFFORT_LEVELS,
  type EffortLevel,
  type Provider,
  type ReasoningLevel,
  type TokenUsage,
} from "./modelEvalSchemas.js";
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
 * Everything one adapter invocation needs beyond the prompt: the argv and the
 * spawn-env OVERLAY (merged over `process.env`). Env is how the Claude CLI
 * takes its thinking parameters, so it is a first-class part of the plan —
 * not an executor-side afterthought.
 */
export interface EvalSpawnPlan {
  args: string[];
  env: Record<string, string>;
}

/**
 * A cell axis value the adapter has no real knob for (#571). The executor
 * lets this propagate, so the runner records the cell as an error with this
 * message — the cell FAILS AS UNSUPPORTED instead of running with a label
 * the spawn never honored.
 */
export class UnsupportedCellError extends Error {
  constructor(message: string) {
    super(message);
    this.name = "UnsupportedCellError";
  }
}

/**
 * Per-adapter spawn profile: the sole place adapter-specific CLI shape,
 * effort/reasoning wiring, and output parsing live. The executor holds one of
 * these and is otherwise adapter-agnostic.
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
   * CLI args + spawn-env overlay for a headless, permissionless run of `model`
   * at the cell's `effort` and `reasoning` (#571). Both axes are expressed
   * through the adapter's REAL knobs — never prompt keywords. Throws
   * {@link UnsupportedCellError} when the adapter has no knob for a value; it
   * never silently drops an axis, because a dropped axis is a mislabeled
   * measurement. Per-model level validity (`supported_efforts`, the
   * thinking-disable interlock) is the runner's registry interlock, upstream
   * of this call.
   */
  buildSpawnPlan(model: string, effort: EffortLevel, reasoning: ReasoningLevel): EvalSpawnPlan;

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
 * The Claude Code escape hatch that turns extended thinking OFF — the same env
 * var the pipeline documents as the operator workaround (skillRunner,
 * internal/execution/adapters/claude.go, internal/preflight/thinking_effort.go).
 * `reasoning: "none"` is expressed through it, and the registry's
 * thinking-disable interlock (ThinkingDisableConflict) guards the combinations
 * a model rejects.
 */
export const CLAUDE_DISABLE_THINKING_ENV = "CLAUDE_CODE_DISABLE_THINKING";

/**
 * The Claude Code thinking-budget parameter (a real CLI env knob, present in
 * the installed CLI). Replaces the old prompt-keyword ladder ("Think" /
 * "Think hard" / "Ultrathink") with the budgets those keywords historically
 * bought, so the reasoning axis is applied for real instead of hinted at.
 */
export const CLAUDE_MAX_THINKING_TOKENS_ENV = "MAX_THINKING_TOKENS";

/**
 * Thinking-token budgets per reasoning level for {@link CLAUDE_MAX_THINKING_TOKENS_ENV}.
 * The values are the documented budgets of the keyword ladder this replaces:
 * think ≈ 4k, think hard ≈ 10k, ultrathink ≈ 32k.
 */
export const CLAUDE_THINKING_BUDGETS: Record<Exclude<ReasoningLevel, "none">, number> = {
  low: 4_000,
  medium: 10_000,
  high: 31_999,
};

export const claudeEvalProfile: EvalAdapterProfile = {
  adapter: "claude",
  provider: "anthropic",
  defaultCommand: "claude",
  commandEnvVar: "NIGHTGAUGE_CLAUDE_CLI_COMMAND",

  resolveCommand(override) {
    return override ?? process.env[this.commandEnvVar] ?? this.defaultCommand;
  },

  buildSpawnPlan(model, effort, reasoning) {
    // Effort rides the CLI's own `--effort` flag — the exact knob the
    // production pipeline emits (skillRunner `args.push("--effort", ...)`),
    // verified on the installed CLI (2.1.233: low, medium, high, xhigh, max).
    // Every EFFORT_LEVELS value is on the flag, so there is no unsupported
    // effort VALUE for this adapter; per-model level membership is the
    // runner's registry interlock.
    const args = [
      "--print",
      "--output-format",
      "json",
      "--model",
      model,
      // The workspace is a disposable, isolated dir — allow edits/bash without prompts.
      "--dangerously-skip-permissions",
      "--effort",
      effort,
    ];
    // Reasoning rides the CLI's real thinking parameters (#571), not prompt
    // keywords: `none` disables thinking via the pipeline's documented escape
    // hatch; the levels set an explicit thinking budget.
    const env: Record<string, string> =
      reasoning === "none"
        ? { [CLAUDE_DISABLE_THINKING_ENV]: "1" }
        : { [CLAUDE_MAX_THINKING_TOKENS_ENV]: String(CLAUDE_THINKING_BUDGETS[reasoning]) };
    return { args, env };
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
 * Effort values the Codex CLI's `model_reasoning_effort` config accepts —
 * mirrors the production CodexAdapter's `CODEX_REASONING_EFFORTS` translation
 * table. Every {@link EFFORT_LEVELS} value is currently in it; the guard exists
 * so a future ladder extension fails as unsupported instead of shipping an
 * unknown value to the CLI.
 */
const CODEX_EFFORT_VALUES: ReadonlySet<string> = new Set(["none", ...EFFORT_LEVELS]);

export const codexEvalProfile: EvalAdapterProfile = {
  adapter: "codex",
  provider: "openai",
  defaultCommand: "codex",
  commandEnvVar: "NIGHTGAUGE_CODEX_CLI_COMMAND",

  resolveCommand(override) {
    return override ?? process.env[this.commandEnvVar] ?? this.defaultCommand;
  },

  buildSpawnPlan(model, effort, reasoning) {
    // Codex has exactly ONE budget knob — `model_reasoning_effort` — and the
    // production pipeline drives it from the EFFORT axis (CodexAdapter's
    // NIGHTGAUGE_CODEX_REASONING_EFFORT translation). There is no separate
    // thinking parameter, so a cell requesting an independent reasoning
    // budget cannot be honored: running it anyway would label one knob with
    // two axes. `none` means "no separate thinking request" and is the only
    // reasoning value a codex cell supports.
    if (reasoning !== "none") {
      throw new UnsupportedCellError(
        `codex has no thinking knob separate from 'model_reasoning_effort', which the ` +
          `effort axis drives — reasoning '${reasoning}' cannot be applied for model ` +
          `'${model}'. Use reasoning 'none' for codex cells and vary the effort axis instead.`
      );
    }
    if (!CODEX_EFFORT_VALUES.has(effort)) {
      throw new UnsupportedCellError(
        `codex 'model_reasoning_effort' has no '${effort}' value ` +
          `(accepted: ${[...CODEX_EFFORT_VALUES].join(", ")}) — refusing to run mislabeled.`
      );
    }
    return {
      args: [
        // `--dangerously-bypass-approvals-and-sandbox` disables BOTH the filesystem
        // sandbox and approval prompts — the documented mode for ephemeral, fully
        // isolated environments (the eval worktree is exactly that). `--json` emits
        // the JSONL event stream we parse below. Mirrors the pipeline's CodexAdapter.
        "exec",
        "--dangerously-bypass-approvals-and-sandbox",
        "--json",
        "--model",
        model,
        "-c",
        `model_reasoning_effort=${effort}`,
      ],
      env: {},
    };
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
