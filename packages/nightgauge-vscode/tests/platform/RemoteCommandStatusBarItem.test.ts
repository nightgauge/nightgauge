/**
 * RemoteCommandStatusBarItem unit tests.
 *
 * Verifies status bar visibility and text transitions for idle, polling,
 * and hasCommands states.
 *
 * @see Issue #2170 — Add IPC bridge for remote command status
 */

import { describe, it, expect, vi, beforeEach } from "vitest";

// ---------------------------------------------------------------------------
// VSCode mock
// ---------------------------------------------------------------------------

vi.mock("vscode", () => ({
  StatusBarAlignment: { Left: 1, Right: 2 },
  window: {
    createStatusBarItem: vi.fn(() => ({
      text: "",
      tooltip: "",
      backgroundColor: undefined as unknown,
      command: undefined as unknown,
      show: vi.fn(),
      hide: vi.fn(),
      dispose: vi.fn(),
    })),
  },
}));

import * as vscode from "vscode";
import { RemoteCommandStatusBarItem } from "../../src/platform/RemoteCommandStatusBarItem";

describe("RemoteCommandStatusBarItem", () => {
  let item: RemoteCommandStatusBarItem;
  let mockBarItem: ReturnType<typeof vscode.window.createStatusBarItem>;

  beforeEach(() => {
    vi.clearAllMocks();
    item = new RemoteCommandStatusBarItem();
    mockBarItem = item.item as ReturnType<typeof vscode.window.createStatusBarItem>;
  });

  it("starts hidden with idle state", () => {
    expect(item.getDisplayState()).toBe("idle");
    expect(mockBarItem.show).not.toHaveBeenCalled();
  });

  it("shows polling text when setPolling() is called", () => {
    item.setPolling();
    expect(item.getDisplayState()).toBe("polling");
    expect(mockBarItem.show).toHaveBeenCalled();
    expect(mockBarItem.text).toContain("Polling");
  });

  it("shows command count when setCommandCount() > 0", () => {
    item.setCommandCount(3);
    expect(item.getDisplayState()).toBe("hasCommands");
    expect(mockBarItem.show).toHaveBeenCalled();
    expect(mockBarItem.text).toContain("3");
  });

  it("hides when setCommandCount(0) is called", () => {
    item.setCommandCount(3);
    vi.clearAllMocks();
    item.setCommandCount(0);
    expect(item.getDisplayState()).toBe("idle");
    expect(mockBarItem.hide).toHaveBeenCalled();
  });

  it("hides when setIdle() is called", () => {
    item.setPolling();
    vi.clearAllMocks();
    item.setIdle();
    expect(item.getDisplayState()).toBe("idle");
    expect(mockBarItem.hide).toHaveBeenCalled();
  });

  it("shows polling state when update(true, 0) is called", () => {
    item.update(true, 0);
    expect(item.getDisplayState()).toBe("polling");
    expect(mockBarItem.show).toHaveBeenCalled();
  });

  it("shows command count when update(false, 5) is called", () => {
    item.update(false, 5);
    expect(item.getDisplayState()).toBe("hasCommands");
    expect(item.getCommandCount()).toBe(5);
    expect(mockBarItem.show).toHaveBeenCalled();
  });

  it("hides when update(false, 0) is called", () => {
    item.update(true, 0);
    vi.clearAllMocks();
    item.update(false, 0);
    expect(item.getDisplayState()).toBe("idle");
    expect(mockBarItem.hide).toHaveBeenCalled();
  });

  it('uses singular "command" for count of 1', () => {
    item.setCommandCount(1);
    expect(mockBarItem.text).toContain("1 command");
    expect(mockBarItem.text).not.toContain("commands");
  });

  it('uses plural "commands" for count > 1', () => {
    item.setCommandCount(2);
    expect(mockBarItem.text).toContain("2 commands");
  });

  it("disposes the status bar item", () => {
    item.dispose();
    expect(mockBarItem.dispose).toHaveBeenCalled();
  });

  it("creates status bar item at priority 96", () => {
    expect(vscode.window.createStatusBarItem).toHaveBeenCalledWith(
      vscode.StatusBarAlignment.Left,
      96
    );
  });
});
