/**
 * Regression test for #193: the rendered "Cache" field's hit rate must never
 * exceed 100%.
 *
 * `total_input` is COMBINED (raw input + total_cache_read) by convention —
 * see the invariant comment on PipelineStateTokens.total_input in
 * PipelineStateService.ts. DiscordService divides
 * `total_cache_read / total_input` to compute the hit percentage; if a writer
 * ever aliases `total_input` to the non-cached input accumulator instead of
 * the combined total, the denominator shrinks below the numerator and the
 * rendered rate exceeds 100%.
 */

import { describe, it, expect } from "vitest";
import { DiscordService } from "../../src/services/DiscordService";

function buildFieldsFor(totalInput: number, totalCacheRead: number) {
  const service = new DiscordService({} as never, {} as never, {} as never);

  const run = {
    issueNumber: 1,
    issueTitle: "Test",
    branch: "feat/test",
    repoName: "repo",
    webhookId: "1",
    webhookToken: "token",
    messageId: "1",
    startTime: Date.now(),
    costUsd: 0,
    stageStartTimes: new Map<string, number>(),
    isFinal: true,
    finalPatchRetries: 0,
  };

  const state = {
    issue_number: 1,
    title: "Test",
    branch: "feat/test",
    outcome_type: "productive",
    tokens: {
      total_input: totalInput,
      total_cache_read: totalCacheRead,
    },
  };

  // buildFields is private — invoked directly to test the field-rendering
  // logic without wiring up the full webhook/state-service plumbing.
  return (
    service as unknown as {
      buildFields: (r: unknown, s: unknown) => Array<{ name: string; value: string }>;
    }
  ).buildFields(run, state);
}

describe("DiscordService — cache hit rate rendering (#193)", () => {
  it("renders a hit rate within [0, 1] when total_input is the combined total", () => {
    // Realistic combined case: 40000 cache reads out of 90000 combined input.
    const fields = buildFieldsFor(90000, 40000);
    const cacheField = fields.find((f) => f.name === "📦 Cache");
    expect(cacheField).toBeTruthy();

    const match = cacheField!.value.match(/^(\d+)% hit rate$/);
    expect(match).toBeTruthy();
    const pct = Number(match![1]);
    expect(pct).toBeGreaterThanOrEqual(0);
    expect(pct).toBeLessThanOrEqual(100);
  });

  it("does not exceed 100% even when cache reads approach the combined total", () => {
    const fields = buildFieldsFor(11000500, 11000000);
    const cacheField = fields.find((f) => f.name === "📦 Cache");
    const pct = Number(cacheField!.value.match(/^(\d+)% hit rate$/)![1]);
    expect(pct).toBeLessThanOrEqual(100);
  });

  it("caps at exactly 100% when cache reads equal the entire combined total", () => {
    const fields = buildFieldsFor(40000, 40000);
    const cacheField = fields.find((f) => f.name === "📦 Cache");
    const pct = Number(cacheField!.value.match(/^(\d+)% hit rate$/)![1]);
    expect(pct).toBe(100);
  });
});
