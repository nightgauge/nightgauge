/**
 * SlotViewProvider — unit tests
 *
 * Verifies the thin TreeDataProvider wrapper used by concurrent slot views.
 * Each test exercises one contract from the plan:
 *   1. getChildren() returns [] when no slot item is set
 *   2. getChildren() returns [slotItem] at the root level when a slot is set
 *   3. getTreeItem() returns the element unchanged
 *   4. setSlotItem(item) fires onDidChangeTreeData
 *   5. setSlotItem(null) fires onDidChangeTreeData
 *
 * @see Issue #1632 - Register Dynamic Concurrent Slot Views
 */

import { describe, it, expect, vi, beforeEach } from "vitest";
import { SlotViewProvider } from "../../src/views/SlotViewProvider";

// ---------------------------------------------------------------------------
// Minimal stub for ConcurrentSlotTreeItem
// ---------------------------------------------------------------------------

/**
 * Build a minimal stub that satisfies the parts of ConcurrentSlotTreeItem
 * that SlotViewProvider interacts with: it is a TreeItem and has getChildren().
 */
function makeSlotItem(children: any[] = []) {
  return {
    label: "Test Slot",
    getChildren: vi.fn(() => children),
  } as any;
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

describe("SlotViewProvider", () => {
  let provider: SlotViewProvider;

  beforeEach(() => {
    provider = new SlotViewProvider();
  });

  it("getChildren() returns [] when no slot item is set (root call)", async () => {
    const result = await provider.getChildren(undefined);
    expect(result).toEqual([]);
  });

  it("getChildren() returns [slotItem] at root level when a slot item is set", async () => {
    const slotItem = makeSlotItem();
    provider.setSlotItem(slotItem);

    const result = await provider.getChildren(undefined);
    expect(result).toEqual([slotItem]);
  });

  it("getChildren() delegates to element.getChildren() for non-root element", async () => {
    const childItems = [{ label: "child1" }, { label: "child2" }];
    const slotItem = makeSlotItem(childItems);
    provider.setSlotItem(slotItem);

    const result = await provider.getChildren(slotItem);
    expect(slotItem.getChildren).toHaveBeenCalledOnce();
    expect(result).toEqual(childItems);
  });

  it("getTreeItem() returns the element unchanged", () => {
    const slotItem = makeSlotItem();
    const result = provider.getTreeItem(slotItem);
    expect(result).toBe(slotItem);
  });

  it("setSlotItem(item) fires onDidChangeTreeData", () => {
    const fired = vi.fn();
    // The EventEmitter mock in setup.ts exposes .event as a vi.fn() and .fire as vi.fn().
    // Access the internal emitter via the public event property subscriber pattern.
    // Since tests run with the global vscode mock, EventEmitter.event is vi.fn().
    // We can verify by spying on the emitter's fire method.
    const slotItem = makeSlotItem();

    // Capture the fire spy from the EventEmitter mock

    const emitter = (provider as any)._onDidChangeTreeData;
    emitter.fire = fired;

    provider.setSlotItem(slotItem);
    expect(fired).toHaveBeenCalledOnce();
  });

  it("setSlotItem(null) fires onDidChangeTreeData", () => {
    const fired = vi.fn();
    const slotItem = makeSlotItem();
    provider.setSlotItem(slotItem);

    // Replace fire spy after initial set

    const emitter = (provider as any)._onDidChangeTreeData;
    emitter.fire = fired;

    provider.setSlotItem(null);
    expect(fired).toHaveBeenCalledOnce();
  });

  it("getChildren() returns [] after setSlotItem(null) clears a previous item", async () => {
    const slotItem = makeSlotItem();
    provider.setSlotItem(slotItem);
    provider.setSlotItem(null);

    const result = await provider.getChildren(undefined);
    expect(result).toEqual([]);
  });
});
