/**
 * Provider pricing tables — (adapter, model) → cost map.
 *
 * Code-resident pricing data used by the cost-computation path to fall back
 * to a published-rate-card cost when an adapter does not surface a native
 * `total_cost_usd` (only Claude does today). Pricing is hand-curated and
 * revalidated quarterly; the unit tests warn at 90 days and fail at 180
 * days since `last_verified` to keep the table honest.
 *
 * Lookup contract:
 *   - `getProviderPricing(adapter, model)` returns a `PricingEntry` or
 *     `null`. `null` is the safe "unknown cost — do not bill" sentinel.
 *   - `lm-studio` and `ollama` always return a synthetic `tier: 'local'`
 *     zero entry (any model string resolves) — never `null`.
 *
 * Wiring into the cost computation path is intentionally NOT in this PR
 * (that work is C2 under epic #3213). Until then, this module is dead
 * code on disk by design.
 *
 * @see Issue #3227 — Provider pricing tables: (adapter, model) cost map
 * @see Epic #3213 — Per-stage cost accuracy across adapters
 */

import type { ExecutionAdapter } from "../config/schema";

/**
 * One row of the pricing table. Rates are USD per 1,000,000 tokens to
 * mirror published vendor rate cards; callers convert to per-token cost.
 *
 * `last_verified` and `source_url` are real fields (not just comments) so
 * the stale-pricing test can iterate the table deterministically without
 * parsing source comments. A redundant comment above each entry is kept
 * for human review (see ADR-001 in knowledge/decisions.md).
 */
export interface PricingEntry {
  input_per_mtok: number;
  output_per_mtok: number;
  cache_read_per_mtok?: number;
  cache_write_per_mtok?: number;
  tier?: "paid" | "local";
  last_verified: string; // 'YYYY-MM-DD'
  source_url: string;
  notes?: string;
}

/**
 * Module-level constant for the synthetic local-tier entry's verification
 * date. Local pricing is structurally zero and does not need refreshing,
 * but a real date keeps the stale-pricing test loop uniform.
 */
const LOCAL_TIER_VERIFIED_DATE = "2026-05-07";

/**
 * Static pricing table keyed by `(adapter, model)`. `Partial<Record<...>>`
 * because `lm-studio` and `ollama` are handled by the wildcard branch in
 * `getProviderPricing` and have no per-model entries.
 *
 * Comment style above each entry duplicates `last_verified` and source URL
 * for human review. The fields below are the source of truth at runtime.
 */
const PRICING_TABLE: Partial<Record<ExecutionAdapter, Record<string, PricingEntry>>> = {
  claude: {
    // Premium frontier tier — ~2× Opus. Never auto-routed; reached only via the
    // `frontier` performance mode or an explicit per-run model override.
    // Source: https://platform.claude.com/docs/en/about-claude/pricing
    // last_verified: 2026-05-28
    "claude-fable-5": {
      input_per_mtok: 10.0,
      output_per_mtok: 50.0,
      cache_read_per_mtok: 1.0,
      cache_write_per_mtok: 12.5,
      tier: "paid",
      last_verified: "2026-05-28",
      source_url: "https://platform.claude.com/docs/en/about-claude/pricing",
    },
    // Source: https://platform.claude.com/docs/en/about-claude/pricing
    // last_verified: 2026-05-28
    "claude-opus-4-8": {
      input_per_mtok: 5.0,
      output_per_mtok: 25.0,
      cache_read_per_mtok: 0.5,
      cache_write_per_mtok: 6.25,
      tier: "paid",
      last_verified: "2026-05-28",
      source_url: "https://platform.claude.com/docs/en/about-claude/pricing",
    },
    // Source: https://platform.claude.com/docs/en/about-claude/pricing
    // last_verified: 2026-05-28
    "claude-opus-4-7": {
      input_per_mtok: 5.0,
      output_per_mtok: 25.0,
      cache_read_per_mtok: 0.5,
      cache_write_per_mtok: 6.25,
      tier: "paid",
      last_verified: "2026-05-28",
      source_url: "https://platform.claude.com/docs/en/about-claude/pricing",
    },
    // Source: https://www.anthropic.com/pricing
    // last_verified: 2026-05-07
    "claude-sonnet-4-6": {
      input_per_mtok: 3.0,
      output_per_mtok: 15.0,
      cache_read_per_mtok: 0.3,
      cache_write_per_mtok: 3.75,
      tier: "paid",
      last_verified: "2026-05-07",
      source_url: "https://www.anthropic.com/pricing",
    },
    // Source: https://www.anthropic.com/pricing
    // last_verified: 2026-05-07
    "claude-haiku-4-5": {
      input_per_mtok: 1.0,
      output_per_mtok: 5.0,
      cache_read_per_mtok: 0.1,
      cache_write_per_mtok: 1.25,
      tier: "paid",
      last_verified: "2026-05-07",
      source_url: "https://www.anthropic.com/pricing",
    },
  },
  codex: {
    // Source: https://developers.openai.com/api/docs/models
    // last_verified: 2026-07-23
    "gpt-5.6-sol": {
      input_per_mtok: 5.0,
      output_per_mtok: 30.0,
      tier: "paid",
      last_verified: "2026-07-23",
      source_url: "https://developers.openai.com/api/docs/models/gpt-5.6-sol",
    },
    "gpt-5.6-terra": {
      input_per_mtok: 2.5,
      output_per_mtok: 15.0,
      tier: "paid",
      last_verified: "2026-07-23",
      source_url: "https://developers.openai.com/api/docs/models/gpt-5.6-terra",
    },
    "gpt-5.6-luna": {
      input_per_mtok: 1.0,
      output_per_mtok: 6.0,
      tier: "paid",
      last_verified: "2026-07-23",
      source_url: "https://developers.openai.com/api/docs/models/gpt-5.6-luna",
    },
    // Source: https://openai.com/api/pricing/
    // last_verified: 2026-05-07
    "gpt-5.5": {
      input_per_mtok: 1.25,
      output_per_mtok: 10.0,
      tier: "paid",
      last_verified: "2026-05-07",
      source_url: "https://openai.com/api/pricing/",
    },
    // Source: https://openai.com/api/pricing/
    // last_verified: 2026-05-07
    "gpt-5.4": {
      input_per_mtok: 1.0,
      output_per_mtok: 8.0,
      tier: "paid",
      last_verified: "2026-05-07",
      source_url: "https://openai.com/api/pricing/",
    },
    // Lightweight Codex mini tier (the `haiku` alias in CODEX_TIER_MODEL_MAP).
    // Source: https://openai.com/api/pricing/
    // last_verified: 2026-06-16
    "gpt-5.4-mini": {
      input_per_mtok: 0.25,
      output_per_mtok: 2.0,
      tier: "paid",
      last_verified: "2026-06-16",
      source_url: "https://openai.com/api/pricing/",
    },
    // Research-preview, text-only model (ChatGPT Pro–bundled). OpenAI publishes
    // NO per-token rate for spark, so this deliberately MIRRORS the gpt-5.4-mini
    // tier as the closest proxy — a documented decision, not a pending
    // verification (#1116). Confirmed the *current* Codex research preview (not
    // deprecated/superseded — GPT-5.3-Codex and GPT-5.2 are the deprecated ones)
    // via OpenAI's Codex model docs on 2026-06-18. Never auto-routed (absent
    // from CODEX_TIER_MODEL_MAP); reached only via an explicit model override.
    // last_verified: 2026-06-18
    "gpt-5.3-codex-spark": {
      input_per_mtok: 0.25,
      output_per_mtok: 2.0,
      tier: "paid",
      last_verified: "2026-06-18",
      source_url: "https://developers.openai.com/codex/models",
      notes:
        "Proxy rate: mirrors gpt-5.4-mini. OpenAI discloses no per-token rate for this Pro-bundled research preview (#1116).",
    },
  },
  gemini: {
    // Source: https://ai.google.dev/pricing
    // last_verified: 2026-05-07
    // Tiered pricing: base rate up to 200k input tokens; premium tier above
    // that is captured in `notes` rather than as a structural field (ADR-004).
    "gemini-2.5-pro": {
      input_per_mtok: 1.25,
      output_per_mtok: 10.0,
      tier: "paid",
      last_verified: "2026-05-07",
      source_url: "https://ai.google.dev/pricing",
      notes:
        "Long-context premium tier (>200k input tokens): input_per_mtok=2.50, output_per_mtok=15.00",
    },
    // Source: https://ai.google.dev/pricing
    // last_verified: 2026-05-07
    "gemini-2.5-flash": {
      input_per_mtok: 0.3,
      output_per_mtok: 2.5,
      tier: "paid",
      last_verified: "2026-05-07",
      source_url: "https://ai.google.dev/pricing",
    },
    // Source: https://ai.google.dev/pricing
    // last_verified: 2026-05-07
    "gemini-2.0-flash": {
      input_per_mtok: 0.1,
      output_per_mtok: 0.4,
      tier: "paid",
      last_verified: "2026-05-07",
      source_url: "https://ai.google.dev/pricing",
    },
  },
  // Duplicated map (ADR-003): gemini-sdk consumes the same model strings
  // today but bills via Vertex AI which may diverge in the future. Keeping
  // the tables independent avoids a brittle indirection.
  "gemini-sdk": {
    // Source: https://ai.google.dev/pricing
    // last_verified: 2026-05-07
    "gemini-2.5-pro": {
      input_per_mtok: 1.25,
      output_per_mtok: 10.0,
      tier: "paid",
      last_verified: "2026-05-07",
      source_url: "https://ai.google.dev/pricing",
      notes:
        "Long-context premium tier (>200k input tokens): input_per_mtok=2.50, output_per_mtok=15.00",
    },
    // Source: https://ai.google.dev/pricing
    // last_verified: 2026-05-07
    "gemini-2.5-flash": {
      input_per_mtok: 0.3,
      output_per_mtok: 2.5,
      tier: "paid",
      last_verified: "2026-05-07",
      source_url: "https://ai.google.dev/pricing",
    },
    // Source: https://ai.google.dev/pricing
    // last_verified: 2026-05-07
    "gemini-2.0-flash": {
      input_per_mtok: 0.1,
      output_per_mtok: 0.4,
      tier: "paid",
      last_verified: "2026-05-07",
      source_url: "https://ai.google.dev/pricing",
    },
  },
  copilot: {
    // Source: https://docs.github.com/en/copilot/about-github-copilot/plans-for-github-copilot
    // last_verified: 2026-05-07
    // Copilot bills flat-per-request in the user's tier (subscription).
    // Per-token rates are zero by design; see ADR-005.
    "gpt-4o": {
      input_per_mtok: 0,
      output_per_mtok: 0,
      tier: "paid",
      last_verified: "2026-05-07",
      source_url:
        "https://docs.github.com/en/copilot/about-github-copilot/plans-for-github-copilot",
      notes: "Copilot bills flat-per-request in user tier",
    },
    // Source: https://docs.github.com/en/copilot/about-github-copilot/plans-for-github-copilot
    // last_verified: 2026-05-07
    "gpt-4o-mini": {
      input_per_mtok: 0,
      output_per_mtok: 0,
      tier: "paid",
      last_verified: "2026-05-07",
      source_url:
        "https://docs.github.com/en/copilot/about-github-copilot/plans-for-github-copilot",
      notes: "Copilot bills flat-per-request in user tier",
    },
    // Source: https://docs.github.com/en/copilot/about-github-copilot/plans-for-github-copilot
    // last_verified: 2026-05-07
    "claude-sonnet-4.5": {
      input_per_mtok: 0,
      output_per_mtok: 0,
      tier: "paid",
      last_verified: "2026-05-07",
      source_url:
        "https://docs.github.com/en/copilot/about-github-copilot/plans-for-github-copilot",
      notes: "Copilot bills flat-per-request in user tier",
    },
  },
};

/**
 * Look up pricing for `(adapter, model)`.
 *
 * Returns `null` when the combination is unknown — callers MUST treat
 * `null` as "do not bill" rather than as zero-cost. For `lm-studio` and
 * `ollama`, returns a synthetic `tier: 'local'` zero entry regardless of
 * the model string (see ADR-002).
 */
export function getProviderPricing(adapter: ExecutionAdapter, model: string): PricingEntry | null {
  if (adapter === "lm-studio" || adapter === "ollama") {
    return {
      input_per_mtok: 0,
      output_per_mtok: 0,
      tier: "local",
      last_verified: LOCAL_TIER_VERIFIED_DATE,
      source_url: "n/a (local inference)",
    };
  }
  const adapterTable = PRICING_TABLE[adapter];
  if (!adapterTable) return null;
  return adapterTable[model] ?? null;
}

/**
 * Internal-but-exported view of the pricing table for the stale-pricing
 * unit test. Named explicitly so consumers do not mistake it for the
 * public API — read-only iteration only.
 */
export const PRICING_TABLE_FOR_TESTS = PRICING_TABLE;
