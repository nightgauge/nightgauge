/**
 * OutputWindowState.archivedTokens.test.ts
 *
 * Unit tests for archived slot token reconstruction (Issue #3708).
 * Covers: registerArchivedSlot + setSlotTokenUsage integration,
 * and the zero-fallback case when no history record is applied.
 */

import { describe, it, expect, beforeEach } from "vitest";
import { OutputWindowState } from "../../../src/views/outputWindow/OutputWindowState";

describe("OutputWindowState archived token reconstruction (Issue #3708)", () => {
  let state: OutputWindowState;

  beforeEach(() => {
    state = new OutputWindowState();
  });

  it("returns zero token usage immediately after registerArchivedSlot (graceful fallback)", () => {
    state.registerArchivedSlot(0, 42, "Issue #42");
    const usage = state.getSlotTokenUsage(0);
    expect(usage).toEqual({
      inputTokens: 0,
      outputTokens: 0,
      cacheReadTokens: 0,
      cacheCreationTokens: 0,
      costUsd: 0,
    });
  });

  it("reflects real token totals after setSlotTokenUsage is called with fixture values", () => {
    state.registerArchivedSlot(0, 42, "Issue #42");

    // Simulate what rehydrateFromLogs does after reading execution history
    state.setSlotTokenUsage(0, {
      inputTokens: 12345,
      outputTokens: 6789,
      cacheReadTokens: 1000,
      cacheCreationTokens: 500,
      costUsd: 0.0432,
    });

    const usage = state.getSlotTokenUsage(0);
    expect(usage.inputTokens).toBe(12345);
    expect(usage.outputTokens).toBe(6789);
    expect(usage.cacheReadTokens).toBe(1000);
    expect(usage.cacheCreationTokens).toBe(500);
    expect(usage.costUsd).toBeCloseTo(0.0432, 6);
  });

  it("does not overwrite a running slot's token usage with archived data", () => {
    // Register a running slot first
    state.registerSlot(0, 42, "Issue #42");
    state.setSlotTokenUsage(0, {
      inputTokens: 999,
      outputTokens: 888,
      cacheReadTokens: 0,
      cacheCreationTokens: 0,
      costUsd: 0.001,
    });

    // registerArchivedSlot should be a no-op for running slots
    state.registerArchivedSlot(0, 42, "Issue #42");

    // Token usage must remain from the live run
    const usage = state.getSlotTokenUsage(0);
    expect(usage.inputTokens).toBe(999);
    expect(usage.outputTokens).toBe(888);
    expect(usage.costUsd).toBeCloseTo(0.001, 6);
  });

  it("supports multiple archived slots with independent token totals", () => {
    state.registerArchivedSlot(0, 10, "Issue #10");
    state.registerArchivedSlot(1, 20, "Issue #20");

    state.setSlotTokenUsage(0, {
      inputTokens: 100,
      outputTokens: 200,
      cacheReadTokens: 0,
      cacheCreationTokens: 0,
      costUsd: 0.01,
    });
    state.setSlotTokenUsage(1, {
      inputTokens: 300,
      outputTokens: 400,
      cacheReadTokens: 50,
      cacheCreationTokens: 25,
      costUsd: 0.02,
    });

    const usage0 = state.getSlotTokenUsage(0);
    const usage1 = state.getSlotTokenUsage(1);

    expect(usage0.inputTokens).toBe(100);
    expect(usage0.outputTokens).toBe(200);

    expect(usage1.inputTokens).toBe(300);
    expect(usage1.outputTokens).toBe(400);
    expect(usage1.cacheReadTokens).toBe(50);
    expect(usage1.cacheCreationTokens).toBe(25);
  });
});
