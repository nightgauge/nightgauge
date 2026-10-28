/**
 * skillRunner.cacheAffinity.test.ts (Issue #55)
 *
 * Dual-path parity: runs the same fixture as Go's
 * TestApplyCacheAffinity_SharedFixture
 * (internal/orchestrator/testdata/cache_affinity_cases.json), so a case added
 * on one side runs on the other.
 */
import { readFileSync } from "fs";
import { resolve } from "path";
import { describe, it, expect, vi } from "vitest";

vi.mock("vscode", () => ({
  workspace: { workspaceFolders: [{ uri: { fsPath: "/test/workspace" } }] },
}));

import { applyCacheAffinity, type AffinityRates } from "../../src/utils/skillRunner";

interface Fixture {
  rates: Record<string, AffinityRates>;
  cases: Array<{
    name: string;
    input: {
      stage: string;
      candidate: string;
      previous: string;
      same_provider: boolean;
      prev_within_expiry: boolean;
      implicit_cache: boolean;
      warm_prefix_tokens: number;
    };
    want: { model: string; kept: boolean; penalty_usd: number; saving_usd: number; reason: string };
  }>;
}

const fixture: Fixture = JSON.parse(
  readFileSync(
    resolve(__dirname, "../../../../internal/orchestrator/testdata/cache_affinity_cases.json"),
    "utf8"
  )
);

describe("applyCacheAffinity (shared fixture with Go)", () => {
  it("has cases", () => {
    expect(fixture.cases.length).toBeGreaterThan(0);
  });
  for (const c of fixture.cases) {
    it(c.name, () => {
      const got = applyCacheAffinity({ ...c.input, rates: (m) => fixture.rates[m] });
      expect(got.model).toBe(c.want.model);
      expect(got.kept).toBe(c.want.kept);
      expect(got.reason).toBe(c.want.reason);
      expect(got.penalty_usd).toBeCloseTo(c.want.penalty_usd, 9);
      expect(got.saving_usd).toBeCloseTo(c.want.saving_usd, 9);
    });
  }
});
