import { describe, it, expect, beforeEach } from "vitest";
import { OutputWindowState } from "../../../src/views/outputWindow/OutputWindowState";

/**
 * OutputWindowState.evictions.test.ts
 *
 * The in-memory entry buffer caps at MAX_ENTRIES (500) via slice, but the
 * webview's DOM has no such cap — we need to tell it which nodes to remove
 * whenever TS trims its buffer. These tests pin the eviction-tracking
 * contract so the webview stays aligned with the in-memory view.
 */
describe("OutputWindowState eviction tracking", () => {
  let state: OutputWindowState;

  beforeEach(() => {
    state = new OutputWindowState();
  });

  it("drainEvictions returns empty when nothing has been trimmed", () => {
    state.addEntry("hello", "info");
    expect(state.drainEvictions()).toEqual([]);
  });

  it("emits an aggregate eviction when the top-level buffer is trimmed past MAX_ENTRIES", () => {
    // MAX_ENTRIES is 500 — add 501 to force exactly one aggregate eviction.
    for (let i = 0; i < 500; i++) state.addEntry(`line ${i}`, "info");
    expect(state.drainEvictions()).toEqual([]); // no trim yet
    state.addEntry("line 500", "info");
    const drained = state.drainEvictions();
    expect(drained).toEqual([{ scope: "aggregate", count: 1 }]);
  });

  it("drain clears pending events so a second call returns empty", () => {
    for (let i = 0; i < 501; i++) state.addEntry(`line ${i}`, "info");
    state.drainEvictions();
    expect(state.drainEvictions()).toEqual([]);
  });

  it("emits a slot eviction when a per-slot buffer overflows", () => {
    for (let i = 0; i < 501; i++) {
      state.addEntry(`line ${i}`, "info", undefined, { slotIndex: 2 });
    }
    const drained = state.drainEvictions();
    // Both the slot buffer and the aggregate buffer overflow (entries route to
    // both). Only the slot-scoped eviction is specific to slot 2; the other
    // is an aggregate eviction.
    expect(drained).toContainEqual({ scope: "slot", slotIndex: 2, count: 1 });
    expect(drained).toContainEqual({ scope: "aggregate", count: 1 });
  });

  it("emits slot-cleared when clearSlot is called", () => {
    state.addEntry("line", "info", undefined, { slotIndex: 4 });
    state.drainEvictions(); // clear any from addEntry
    state.clearSlot(4);
    expect(state.drainEvictions()).toEqual([{ scope: "slot-cleared", slotIndex: 4 }]);
  });

  it("emits slot-cleared when removeSlot is called on a registered slot", () => {
    state.registerSlot(7, 101, "Issue #101");
    state.drainEvictions();
    state.removeSlot(7);
    expect(state.drainEvictions()).toEqual([{ scope: "slot-cleared", slotIndex: 7 }]);
  });

  it("removeSlot for an unknown slot emits nothing", () => {
    state.removeSlot(999);
    expect(state.drainEvictions()).toEqual([]);
  });
});
