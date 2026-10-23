/**
 * #2403 — the pipeline tree names a concurrent slot by repository and issue
 * number. Two repositories' issues with one number run in two slots, so they
 * are two tree items, and removing or updating one leaves the other.
 */

import { describe, it, expect, vi, beforeEach } from "vitest";
import { PipelineTreeProvider } from "../../src/views/PipelineTreeProvider";
import { ConcurrentSlotTreeItem } from "../../src/views/items/ConcurrentSlotTreeItem";

vi.mock("../../src/services/IpcClient", () => ({
  IpcClient: {
    getInstance: () => ({
      on: vi.fn(() => ({ dispose: vi.fn() })),
    }),
  },
}));

function createSlotStateService() {
  return {
    onStateChanged: vi.fn(() => ({ dispose: vi.fn() })),
    onPhaseStart: vi.fn(() => ({ dispose: vi.fn() })),
    onPhaseComplete: vi.fn(() => ({ dispose: vi.fn() })),
    onTokenUsageUpdated: vi.fn(() => ({ dispose: vi.fn() })),
    getState: vi.fn(async () => null),
  };
}

const PLATFORM = "example-org/platform";
const APP = "example-org/app";

async function slotItems(provider: PipelineTreeProvider): Promise<ConcurrentSlotTreeItem[]> {
  const roots = await provider.getChildren();
  return roots.filter((r): r is ConcurrentSlotTreeItem => r instanceof ConcurrentSlotTreeItem);
}

describe("PipelineTreeProvider — slot identity (#2403)", () => {
  let provider: PipelineTreeProvider;

  beforeEach(() => {
    provider = new PipelineTreeProvider();
    provider.addConcurrentSlot(
      0,
      21,
      "Platform 21",
      createSlotStateService() as any,
      undefined,
      undefined,
      PLATFORM
    );
    provider.addConcurrentSlot(
      1,
      21,
      "App 21",
      createSlotStateService() as any,
      undefined,
      undefined,
      APP
    );
  });

  it("shows two same-numbered issues of different repositories as two items", async () => {
    const items = await slotItems(provider);
    expect(items.map((i) => i.repo)).toEqual([PLATFORM, APP]);
    expect(new Set(items.map((i) => i.id)).size).toBe(2);
    expect(provider.getConcurrentSlot(21, PLATFORM)?.slotIndex).toBe(0);
    expect(provider.getConcurrentSlot(21, "Example-Org/App")?.slotIndex).toBe(1);
  });

  it("updates and removes only the named repository's slot", async () => {
    provider.updateConcurrentSlotStatus(21, "failed", APP);
    expect(provider.getConcurrentSlot(21, APP)?.contextValue).toContain("failed");
    expect(provider.getConcurrentSlot(21, PLATFORM)?.contextValue).toBe("concurrentSlot.running");

    provider.removeConcurrentSlot(21, APP);
    const items = await slotItems(provider);
    expect(items.map((i) => i.repo)).toEqual([PLATFORM]);
  });

  it("replaces only the named repository's preparing placeholder", async () => {
    const fresh = new PipelineTreeProvider();
    fresh.addPreparingSlot(21, "Platform 21", undefined, PLATFORM);
    fresh.addPreparingSlot(21, "App 21", undefined, APP);
    fresh.addConcurrentSlot(
      0,
      21,
      "App 21",
      createSlotStateService() as any,
      undefined,
      undefined,
      APP
    );

    const roots = await fresh.getChildren();
    expect(roots.map((r) => r.id)).toEqual(
      expect.arrayContaining([
        "concurrent-slot-example-org/app#21",
        "preparing-slot-example-org/platform#21",
      ])
    );
    expect(roots.some((r) => r.id === "preparing-slot-example-org/app#21")).toBe(false);
  });

  it("keeps the single-repository case working by number", async () => {
    const single = new PipelineTreeProvider();
    single.addConcurrentSlot(0, 42, "Only", createSlotStateService() as any);
    expect(single.getConcurrentSlot(42)).toBeDefined();
    single.removeConcurrentSlot(42);
    expect(await slotItems(single)).toEqual([]);
  });
});
