import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import { OutputWindowState } from "../../../src/views/outputWindow/OutputWindowState";

/**
 * Tests for debounced `workspaceState` persistence.
 *
 * Why this exists: high-frequency mutations (`addEntry`, `addToolCall`,
 * `setTokenUsage`) previously persisted on every call. Under 5 concurrent
 * pipelines that produced enough synchronous serialization work to trip
 * VSCode's UNRESPONSIVE detector and kill the extension host.
 * See docs/EXTENSION_HOST_CRASH_DIAGNOSIS.md — Suspect 2.
 */
describe("OutputWindowState — debounced persistence", () => {
  let memento: { update: ReturnType<typeof vi.fn>; get: ReturnType<typeof vi.fn> };

  beforeEach(() => {
    vi.useFakeTimers();
    memento = {
      update: vi.fn(async () => undefined),
      get: vi.fn(() => undefined),
    };
  });

  afterEach(() => {
    vi.useRealTimers();
  });

  it("coalesces a burst of mutations into a single workspaceState.update", async () => {
    const state = new OutputWindowState(memento as any);

    for (let i = 0; i < 100; i++) {
      state.addEntry(`entry ${i}`, "info");
    }

    // Within the debounce window, no writes yet.
    expect(memento.update).not.toHaveBeenCalled();

    // Advance past the debounce window — exactly one write should fire.
    await vi.advanceTimersByTimeAsync(600);

    expect(memento.update).toHaveBeenCalledTimes(1);
    const [, payload] = memento.update.mock.calls[0];
    expect((payload as { entries: unknown[] }).entries).toHaveLength(100);
  });

  it("flush() forces an immediate write and cancels the pending timer", async () => {
    const state = new OutputWindowState(memento as any);
    state.addEntry("hello", "info");
    expect(memento.update).not.toHaveBeenCalled();

    await state.flush();

    expect(memento.update).toHaveBeenCalledTimes(1);

    // Advancing past the debounce window must NOT produce a duplicate write.
    await vi.advanceTimersByTimeAsync(1000);
    expect(memento.update).toHaveBeenCalledTimes(1);
  });

  it("dispose() cancels the pending timer without writing", async () => {
    const state = new OutputWindowState(memento as any);
    state.addEntry("hello", "info");
    state.dispose();

    await vi.advanceTimersByTimeAsync(1000);
    expect(memento.update).not.toHaveBeenCalled();
  });

  it("ignores further scheduleSave calls after dispose()", async () => {
    const state = new OutputWindowState(memento as any);
    state.dispose();

    state.addEntry("post-dispose", "info");
    await vi.advanceTimersByTimeAsync(1000);

    expect(memento.update).not.toHaveBeenCalled();
  });

  it("caps oversized entry text when persisting but keeps full text in memory", async () => {
    const state = new OutputWindowState(memento as any);
    const huge = "x".repeat(20 * 1024);
    state.addEntry(huge, "info");

    // In-memory entries retain the full content for the webview.
    expect(state.getEntries()[0].text).toBe(huge);

    await vi.advanceTimersByTimeAsync(600);

    const [, payload] = memento.update.mock.calls[0];
    const persistedText = (payload as { entries: Array<{ text: string }> }).entries[0].text;
    expect(persistedText.length).toBeLessThan(huge.length);
    expect(persistedText).toContain("[truncated");
  });
});
