/**
 * TS-side cache-pricing oracle (#391), mirroring the Go oracle in
 * `internal/intelligence/tokens/economics_cache_oracle_test.go` against the
 * SAME captured Claude CLI traffic.
 *
 * ── WHY THIS EXISTS ON THE TS SIDE TOO ──
 *
 * The Go oracle proves `tokens.CalculateCost` reproduces the vendor bill. It
 * says nothing about the extension, and the extension is the layer that
 * actually decides most bills: its number ships over IPC as
 * `stageResult.costUsd` and WINS over Go's whenever it is non-zero
 * (`scheduler.go` calls `CalculateCost` only as the `== 0` fallback). Before
 * #391 the extension priced from its own `providerPricing.ts` table, so the Go
 * oracle could be green while the booked number was wrong — which it was: that
 * table's Claude keys had rotted to the `claude-opus-4-8` era and the models
 * the pipeline routes today had no entry at all.
 *
 * ── THE ORACLE RULE (#166) ──
 *
 * The expectation is the vendor's own `total_cost_usd`, read out of a REAL
 * captured transcript. It is never hand-authored. Hand-authoring is how
 * #166/#300 stayed green against a fiction: the formula and the test agree
 * with each other and disagree with the bill. The fixtures are READ from
 * `internal/execution/testdata` — the same files the Go oracle reads, whose
 * capture provenance is documented in that directory's README and whose
 * replacement by a synthesized equivalent it forbids.
 *
 * @see Issue #391 — the registry is the only pricing authority
 * @see Issue #358 — split 5m/1h cache-write rates
 * @see internal/intelligence/tokens/economics_cache_oracle_test.go
 * @see internal/execution/testdata/README.md
 */

import { describe, it, expect } from "vitest";
import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { computeCostUsd } from "@nightgauge/sdk";
import { computeStageCost } from "../../src/utils/computeStageCost";

/** The Go oracle's `fixtureDir`, reached from this package's tests directory. */
const FIXTURE_DIR = resolve(__dirname, "../../../../internal/execution/testdata");

/**
 * Match window, mirroring the Go oracle's `costTolerance`: a billionth of a
 * dollar is far below any rounding the vendor applies, so a passing assertion
 * means the formula reproduces the bill exactly rather than approximately.
 */
const COST_TOLERANCE = 1e-9;

/** The subset of a Claude CLI stream-json line this oracle reads. Field names are the CLI's. */
interface StreamEnvelope {
  type?: string;
  total_cost_usd?: number;
  usage?: {
    input_tokens?: number;
    output_tokens?: number;
    cache_read_input_tokens?: number;
    cache_creation?: {
      ephemeral_5m_input_tokens?: number;
      ephemeral_1h_input_tokens?: number;
    };
  };
  modelUsage?: Record<
    string,
    {
      inputTokens: number;
      outputTokens: number;
      cacheReadInputTokens: number;
      cacheCreationInputTokens: number;
    }
  >;
  message?: {
    id?: string;
    usage?: {
      input_tokens?: number;
      cache_read_input_tokens?: number;
      cache_creation?: {
        ephemeral_5m_input_tokens?: number;
        ephemeral_1h_input_tokens?: number;
      };
    };
  };
}

function readFixture(name: string): StreamEnvelope[] {
  const raw = readFileSync(resolve(FIXTURE_DIR, name), "utf-8");
  const out = raw
    .split("\n")
    .filter((l) => l.trim().length > 0)
    .map((l) => JSON.parse(l) as StreamEnvelope);
  expect(
    out.length,
    `fixture ${name} parsed to zero events — the oracle would pass vacuously`
  ).toBeGreaterThan(0);
  return out;
}

/**
 * The single model id an envelope's `modelUsage` names. The oracle must price
 * the model the vendor actually billed, not one we assume.
 */
function soleModel(ev: StreamEnvelope): string {
  const ids = Object.keys(ev.modelUsage ?? {});
  expect(ids, "expected exactly one model in modelUsage").toHaveLength(1);
  return ids[0];
}

describe("cache-pricing oracle (real captured Claude CLI traffic)", () => {
  it("reproduces the vendor total_cost_usd on the primary capture", () => {
    const events = readFixture("claude_stream_real_capture.jsonl");

    let checked = 0;
    for (const ev of events) {
      if (ev.type !== "result" || ev.total_cost_usd === undefined) continue;
      const model = soleModel(ev);
      const u = ev.usage ?? {};
      const got = computeCostUsd(model, {
        input: u.input_tokens ?? 0,
        output: u.output_tokens ?? 0,
        cacheRead: u.cache_read_input_tokens ?? 0,
        cacheCreation5m: u.cache_creation?.ephemeral_5m_input_tokens ?? 0,
        cacheCreation1h: u.cache_creation?.ephemeral_1h_input_tokens ?? 0,
      });
      expect(Math.abs(got - ev.total_cost_usd)).toBeLessThanOrEqual(COST_TOLERANCE);
      checked++;
    }
    expect(checked, "no result envelope carrying total_cost_usd — the oracle checked nothing").toBe(
      1
    );
  });

  it("reproduces the vendor total_cost_usd on the subagent capture (both tiers bill-proven)", () => {
    // This fixture's envelopes are not individually self-describing: its README
    // records that `usage` is a per-envelope DELTA excluding subagent turns
    // while `total_cost_usd` is session-cumulative. The final envelope's
    // `modelUsage` is the only complete count in the file, and the per-tier
    // cache-creation split lives only on the assistant turns — so sum the
    // deduped turns for the split and cross-check that sum against modelUsage
    // before pricing anything. Unlike the primary capture (100% 1h-tier), this
    // one carries a genuine split (7890 5m / 5460 1h), so reproducing its bill
    // to delta-zero proves BOTH rates: a wrong 5m rate cannot hide behind a
    // correct 1h rate here.
    const events = readFixture("claude_stream_subagent_multi_result.jsonl");

    const seen = new Set<string>();
    let turnInput = 0;
    let turnCacheRead = 0;
    let turnCache5m = 0;
    let turnCache1h = 0;
    for (const ev of events) {
      const id = ev.message?.id;
      // The CLI repeats a turn's usage once per content block — dedupe on message.id.
      if (ev.type !== "assistant" || !id || seen.has(id)) continue;
      seen.add(id);
      const u = ev.message?.usage ?? {};
      turnInput += u.input_tokens ?? 0;
      turnCacheRead += u.cache_read_input_tokens ?? 0;
      turnCache5m += u.cache_creation?.ephemeral_5m_input_tokens ?? 0;
      turnCache1h += u.cache_creation?.ephemeral_1h_input_tokens ?? 0;
    }
    expect(
      seen.size,
      "no assistant turns found — the tier split would be vacuously zero"
    ).toBeGreaterThan(0);

    const finals = events.filter((e) => e.type === "result" && e.total_cost_usd !== undefined);
    expect(
      finals.length,
      "expected the multi-result capture to carry >= 2 result envelopes"
    ).toBeGreaterThanOrEqual(2);
    // total_cost_usd is session-cumulative, so it may never decrease.
    for (let i = 1; i < finals.length; i++) {
      expect(finals[i].total_cost_usd!).toBeGreaterThanOrEqual(finals[i - 1].total_cost_usd!);
    }

    const last = finals[finals.length - 1];
    const model = soleModel(last);
    const mu = last.modelUsage![model];

    // Cross-check the parsed split against the vendor's own cumulative totals
    // before trusting it. A mismatch means the fixture was recaptured with a
    // different shape, not that the formula is wrong.
    expect(turnCache5m + turnCache1h).toBe(mu.cacheCreationInputTokens);
    expect(turnInput).toBe(mu.inputTokens);
    expect(turnCacheRead).toBe(mu.cacheReadInputTokens);
    // Guard the premise of the "both tiers" claim: neither tier may be empty.
    expect(turnCache5m).toBeGreaterThan(0);
    expect(turnCache1h).toBeGreaterThan(0);

    const got = computeCostUsd(model, {
      input: mu.inputTokens,
      output: mu.outputTokens,
      cacheRead: mu.cacheReadInputTokens,
      cacheCreation5m: turnCache5m,
      cacheCreation1h: turnCache1h,
    });
    expect(Math.abs(got - last.total_cost_usd!)).toBeLessThanOrEqual(COST_TOLERANCE);
  });

  it("computeStageCost agrees with the vendor bill to its documented 6-decimal rounding", () => {
    // The oracle above pins the SDK formula at 1e-9. This pins the extension's
    // resolver — the thing that actually ships the number over IPC — to the same
    // bill, allowing only the 6-decimal rounding `computeStageCost` documents.
    const [result] = readFixture("claude_stream_real_capture.jsonl").filter(
      (e) => e.type === "result" && e.total_cost_usd !== undefined
    );
    const model = soleModel(result);
    const u = result.usage!;

    const got = computeStageCost("claude", model, {
      input: u.input_tokens ?? 0,
      output: u.output_tokens ?? 0,
      cache_read: u.cache_read_input_tokens ?? 0,
      cache_creation_5m: u.cache_creation?.ephemeral_5m_input_tokens ?? 0,
      cache_creation_1h: u.cache_creation?.ephemeral_1h_input_tokens ?? 0,
    });

    expect(got.source).toBe("computed");
    // 5e-7 is exactly half of the 1e-6 quantum round6() applies — anything
    // larger would be a real pricing error, not rounding.
    expect(Math.abs(got.cost_usd - result.total_cost_usd!)).toBeLessThanOrEqual(5e-7);
  });

  it("reads the 1h slot when it is populated: booking 1h writes as 5m under-prices the pool by 37.5%", () => {
    // The 37.5% case, stated against real captured counts rather than a
    // hypothetical. The primary capture's cache creation is 100% 1h-tier
    // (3308 tokens), which is precisely the traffic shape the deleted
    // extension table could not express — it carried ONE cache-write rate,
    // the 5m column.
    //
    // The assertion is deliberately phrased as "the 1h slot rates are READ
    // when cacheCreation1h is populated": today every extension caller books
    // unsplit counts into the 5m slot per the #358 floor convention, so this
    // proves the 1h path is live and correct the moment #390 supplies the
    // split — not that any caller exercises it yet.
    const [result] = readFixture("claude_stream_real_capture.jsonl").filter(
      (e) => e.type === "result" && e.total_cost_usd !== undefined
    );
    const model = soleModel(result);
    const u = result.usage!;
    const cache1h = u.cache_creation?.ephemeral_1h_input_tokens ?? 0;
    expect(cache1h, "fixture premise: this capture's cache creation is 1h-tier").toBeGreaterThan(0);
    expect(u.cache_creation?.ephemeral_5m_input_tokens ?? 0).toBe(0);

    const base = {
      input: u.input_tokens ?? 0,
      output: u.output_tokens ?? 0,
      cacheRead: u.cache_read_input_tokens ?? 0,
    };

    // Correct: the writes go in the slot the vendor billed them in.
    const correct = computeCostUsd(model, { ...base, cacheCreation1h: cache1h });
    // The #358 floor: the same writes booked as 5m, which is what an unsplit
    // caller produces today.
    const floor = computeCostUsd(model, { ...base, cacheCreation5m: cache1h });

    // Only the cache-creation pool differs; everything else is identical.
    const correctPool = correct - computeCostUsd(model, base);
    const floorPool = floor - computeCostUsd(model, base);

    expect(correct).toBeCloseTo(result.total_cost_usd!, 9);
    expect(floor).toBeLessThan(correct);
    // 1.25x vs 2.0x base input => the floor is exactly 62.5% of the true pool
    // cost, i.e. under-priced by 37.5%.
    expect(floorPool / correctPool).toBeCloseTo(0.625, 12);
    expect(1 - floorPool / correctPool).toBeCloseTo(0.375, 12);
  });
});
