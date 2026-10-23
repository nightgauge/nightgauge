/**
 * SlotViewProvider — thin TreeDataProvider for a single concurrent pipeline slot.
 *
 * Wraps one ConcurrentSlotTreeItem as the root of its dedicated sidebar view.
 * When no slot is assigned (slotItem === null), getChildren returns [] so the
 * view shows its welcome content. The view is also hidden via context key
 * (nightgauge.slot.N.visible), but this guard prevents errors on race.
 *
 * @see Issue #1632 - Register Dynamic Concurrent Slot Views
 */

import * as vscode from "vscode";
import { ConcurrentSlotTreeItem } from "./items/ConcurrentSlotTreeItem";

export class SlotViewProvider implements vscode.TreeDataProvider<
  ConcurrentSlotTreeItem | vscode.TreeItem
> {
  private _onDidChangeTreeData = new vscode.EventEmitter<void>();
  readonly onDidChangeTreeData = this._onDidChangeTreeData.event;

  private slotItem: ConcurrentSlotTreeItem | null = null;

  setSlotItem(item: ConcurrentSlotTreeItem | null): void {
    this.slotItem = item;
    this._onDidChangeTreeData.fire();
  }

  getTreeItem(element: ConcurrentSlotTreeItem): vscode.TreeItem {
    return element;
  }

  getChildren(
    element?: ConcurrentSlotTreeItem
  ): vscode.ProviderResult<(ConcurrentSlotTreeItem | vscode.TreeItem)[]> {
    if (!element) {
      // Root: return the slot item itself, or empty if no slot assigned
      return this.slotItem ? [this.slotItem] : [];
    }
    // Delegate children to the element (the ConcurrentSlotTreeItem itself)
    return element.getChildren();
  }
}
