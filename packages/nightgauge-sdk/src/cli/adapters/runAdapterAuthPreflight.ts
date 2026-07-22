/**
 * runAdapterAuthPreflight — pipeline-start auth probe runner.
 *
 * Takes a list of adapter names, dedupes them, probes each in parallel via
 * {@link validateAdapterAuth}, and returns an aggregate result. Any single
 * failure flips `result.ok` to false; failures carry an actionable
 * `suggestedFix` so the failure-comment surface stays useful even when
 * multiple adapters fail at once.
 *
 * @see Issue #3222 - validateAdapterAuth pre-flight checker per adapter
 */

import type { IncrediAdapter } from "./ICliAdapter.js";
import { type AdapterAuthResult, type ValidateAdapterAuthOptions } from "./validateAdapterAuth.js";
import { probeAdapterAuthCached, type AuthPreflightCacheOptions } from "./authPreflightCache.js";

export interface AdapterAuthFailure {
  adapter: IncrediAdapter;
  reason: string;
  suggestedFix: string;
  /**
   * True when the failure is a probe TIMEOUT (transient infra) rather than a
   * definitive negative ("not authenticated"). Callers use it to distinguish
   * "probe timed out" from "logged out" in the failure surface and to route the
   * run to the retryable-infra terminal kind. Issue #312.
   */
  timedOut: boolean;
}

export interface AdapterPreflightAggregateResult {
  ok: boolean;
  results: Partial<Record<IncrediAdapter, AdapterAuthResult>>;
  failures: AdapterAuthFailure[];
}

/**
 * Options for {@link runAdapterAuthPreflight}: the per-probe
 * {@link ValidateAdapterAuthOptions} plus the process-wide cache / single-flight
 * / timeout-retry controls from {@link AuthPreflightCacheOptions}.
 */
export interface RunAdapterAuthPreflightOptions
  extends ValidateAdapterAuthOptions, AuthPreflightCacheOptions {}

/**
 * Per-adapter login hint table. The SDK defines the single source of truth so
 * the failure-comment renderer in the VSCode layer does not need to duplicate
 * provider-specific install advice.
 */
const SUGGESTED_FIX: Record<IncrediAdapter, string> = {
  "claude-sdk": "Set ANTHROPIC_API_KEY in your environment, or run `claude auth login`.",
  "claude-headless": "Run `claude auth login` (install via `brew install claude` if missing).",
  codex: "Run `codex login` (install via `npm install -g @openai/codex` if missing).",
  gemini:
    "Set GEMINI_API_KEY/GOOGLE_API_KEY, or run `gcloud auth application-default login` for ADC.",
  "gemini-sdk": "Set GEMINI_API_KEY or GOOGLE_API_KEY in your environment.",
  "lm-studio": "Start LM Studio locally and load a model (default: http://localhost:1234).",
  ollama: "Start the Ollama server (`ollama serve`) and pull a model (`ollama pull llama3.2`).",
  copilot:
    "Set COPILOT_GITHUB_TOKEN, or run `gh auth login` then `gh extension install github/gh-copilot`.",
};

function suggestedFixFor(adapter: IncrediAdapter): string {
  return SUGGESTED_FIX[adapter] ?? `Re-check authentication for adapter '${adapter}'.`;
}

/**
 * Probe each distinct adapter once, in parallel, and aggregate the outcome.
 *
 * Each probe goes through {@link probeAdapterAuthCached}, so a concurrent burst
 * of runs for the same `(adapter, cwd)` shares ONE `claude auth status` spawn
 * (single-flight), a resolved result is reused for a short TTL, and a probe that
 * merely TIMES OUT retries once before failing — only a definitive negative
 * (logged out) fails without a retry (Issue #312). Pass `bypassCache: true` to
 * force a fresh probe (the Adapter Doctor does this).
 *
 * @param adapters - Adapter names. Duplicates are removed before probing.
 * @param opts - Per-probe options plus cache / retry controls.
 */
export async function runAdapterAuthPreflight(
  adapters: IncrediAdapter[],
  opts: RunAdapterAuthPreflightOptions = {}
): Promise<AdapterPreflightAggregateResult> {
  const distinct: IncrediAdapter[] = [];
  const seen = new Set<IncrediAdapter>();
  for (const a of adapters) {
    if (!seen.has(a)) {
      seen.add(a);
      distinct.push(a);
    }
  }

  if (distinct.length === 0) {
    return { ok: true, results: {}, failures: [] };
  }

  const probes = await Promise.all(
    distinct.map(async (adapter) => {
      const result = await probeAdapterAuthCached(adapter, opts, opts);
      return { adapter, result };
    })
  );

  const results: Partial<Record<IncrediAdapter, AdapterAuthResult>> = {};
  const failures: AdapterAuthFailure[] = [];
  for (const { adapter, result } of probes) {
    results[adapter] = result;
    if (!result.ok) {
      failures.push({
        adapter,
        reason: result.reason,
        suggestedFix: suggestedFixFor(adapter),
        timedOut: result.category === "TIMEOUT",
      });
    }
  }

  return {
    ok: failures.length === 0,
    results,
    failures,
  };
}
