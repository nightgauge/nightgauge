/**
 * #3819: the Pipeline Slots card TOKENS headline must show fresh model I/O
 * (inputTokens + outputTokens) and EXCLUDE cache-read tokens, which the cost
 * path already prices ~10–20× cheaper. Cache reads are surfaced as a dimmed
 * secondary "cached" annotation so the cache benefit stays visible without
 * inflating the headline.
 */
import { describe, it, expect } from "vitest";
import { getPipelineSlotsHtml } from "../../../../src/views/dashboard/tabs/OverviewTabHtml";
import type {
  PipelineSlotsViewData,
  SlotCardData,
} from "../../../../src/views/dashboard/SlotCardTypes";

function makeSlot(overrides: Partial<SlotCardData> = {}): SlotCardData {
  return {
    slotIndex: 0,
    issueNumber: 3819,
    title: "fix slot tokens",
    status: "running",
    stages: [],
    completedStageCount: 1,
    totalStageCount: 6,
    inputTokens: 1000,
    outputTokens: 500,
    cacheReadTokens: 0,
    costUsd: 0.01,
    ...overrides,
  };
}

function makeView(slot: SlotCardData): PipelineSlotsViewData {
  return {
    maxConcurrent: 1,
    queueStatus: "processing",
    slots: [slot],
    queued: [],
  };
}

describe("Pipeline Slots — TOKENS headline excludes cache reads (#3819)", () => {
  it("headline = inputTokens + outputTokens, excluding cacheReadTokens", () => {
    // 1000 + 500 = 1.5K. If cache reads (200000) were folded in it would read 201.5K.
    const html = getPipelineSlotsHtml(
      makeView(makeSlot({ inputTokens: 1000, outputTokens: 500, cacheReadTokens: 200000 }))
    );
    // The Tokens metric value sits directly above its "Tokens" label.
    expect(html).toMatch(
      /<div class="slot-card-metric-value">1\.5K<\/div>\s*<div class="slot-card-metric-label">Tokens<\/div>/
    );
    expect(html).not.toContain("201.5K");
  });

  it("renders the cached annotation when cacheReadTokens > 0", () => {
    const html = getPipelineSlotsHtml(
      makeView(makeSlot({ inputTokens: 1000, outputTokens: 500, cacheReadTokens: 200000 }))
    );
    expect(html).toContain(`<div class="slot-card-metric-sub">200.0K cached</div>`);
  });

  it("omits the cached annotation when cacheReadTokens === 0", () => {
    const html = getPipelineSlotsHtml(
      makeView(makeSlot({ inputTokens: 1000, outputTokens: 500, cacheReadTokens: 0 }))
    );
    expect(html).not.toContain("cached");
    expect(html).not.toContain("slot-card-metric-sub");
  });
});
