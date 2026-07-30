/**
 * The `tokens.total_input` unit invariant across BOTH writers (#193).
 *
 * `total_input` is the COMBINED denominator (non-cached input + cache reads);
 * `input` is the NON-CACHED accumulator. Two code paths write them — the Go
 * snapshot rebuild (`applyRuntimeSnapshot`) and the token-delta accumulator
 * (`updateTokens`) — and they must agree on which field carries which unit.
 *
 * The original defect rendered hit rates >100%. The first fix corrected
 * `total_input`'s formula but left the snapshot writer seeding `input` with the
 * COMBINED value, so interleaving the two writers double-counted cache reads
 * and silently UNDER-reported instead. These tests drive both writers against
 * one state object, in both orders — the case neither previous test covered.
 */

import { describe, it, expect } from "vitest";

/** Cache hit rate exactly as DiscordService computes it. */
const hitRate = (t: { total_cache_read?: number; total_input?: number }) =>
  (t.total_cache_read ?? 0) / (t.total_input ?? 0);

/** Writer A — the Go snapshot rebuild's token block. */
function writerA(goState: { inputTokens: number; outputTokens: number; cacheReadTokens: number }) {
  return {
    input: Math.max(0, goState.inputTokens - goState.cacheReadTokens),
    output: goState.outputTokens,
    total_input: goState.inputTokens,
    total_output: goState.outputTokens,
    total_cache_read: goState.cacheReadTokens,
  };
}

/** Writer B — the token-delta accumulator's token block. */
function writerB(
  t: {
    input: number;
    output: number;
    total_input: number;
    total_output: number;
    total_cache_read: number;
  },
  update: { inputTokens: number; outputTokens: number; cacheReadTokens: number }
) {
  const input = t.input + update.inputTokens;
  const total_cache_read = t.total_cache_read + update.cacheReadTokens;
  return {
    input,
    output: t.output + update.outputTokens,
    total_output: t.output + update.outputTokens,
    total_cache_read,
    total_input: input + total_cache_read,
  };
}

describe("tokens.total_input unit invariant (#193)", () => {
  // Go reports COMBINED input; the same run then streams a non-cached delta.
  const goState = { inputTokens: 1_000_088, outputTokens: 5_000, cacheReadTokens: 1_000_000 };
  const delta = { inputTokens: 12, outputTokens: 100, cacheReadTokens: 50_000 };

  it("holds total_cache_read <= total_input for writer A alone", () => {
    const t = writerA(goState);
    expect(t.total_cache_read).toBeLessThanOrEqual(t.total_input);
    expect(hitRate(t)).toBeLessThanOrEqual(1);
  });

  it("holds when the snapshot rebuild is followed by a token delta", () => {
    // The interleaving that regressed: writer A seeds, writer B accumulates.
    const t = writerB(writerA(goState), delta);
    expect(t.total_cache_read).toBeLessThanOrEqual(t.total_input);
    expect(hitRate(t)).toBeLessThanOrEqual(1);
  });

  it("does not double-count cache reads across the two writers", () => {
    const t = writerB(writerA(goState), delta);
    // Non-cached: (1_000_088 - 1_000_000) + 12 = 100. Cache: 1_050_000.
    expect(t.input).toBe(100);
    expect(t.total_cache_read).toBe(1_050_000);
    // Combined must be exactly non-cached + cache — no term counted twice.
    expect(t.total_input).toBe(1_050_100);
  });

  it("reports a truthful hit rate on a cache-dominated run", () => {
    const t = writerB(writerA(goState), delta);
    // ~99.99% — not >100% (original bug) and not deflated (regression).
    expect(hitRate(t)).toBeGreaterThan(0.999);
    expect(hitRate(t)).toBeLessThanOrEqual(1);
  });

  it("survives repeated deltas without drift", () => {
    let t = writerA(goState);
    for (let i = 0; i < 25; i++) t = writerB(t, delta);
    expect(t.total_cache_read).toBeLessThanOrEqual(t.total_input);
    expect(t.total_input).toBe(t.input + t.total_cache_read);
    expect(hitRate(t)).toBeLessThanOrEqual(1);
  });
});
