/**
 * Tests for StallStatusBarItem
 *
 * Verifies lifecycle transitions: show/hide, state management, 30s ticker,
 * time formatting, dispose cleanup, and click command configuration.
 *
 * @see Issue #2655 — Persistent stall indicators in status bar and output panel
 */

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";

// Mock vscode before imports
vi.mock("vscode", () => ({
  window: {
    createStatusBarItem: vi.fn(),
  },
  StatusBarAlignment: {
    Left: 1,
    Right: 2,
  },
}));

import * as vscode from "vscode";
import { StallStatusBarItem } from "../../src/services/StallStatusBarItem";

describe("StallStatusBarItem", () => {
  let mockItem: {
    text: string;
    tooltip: string;
    command: string | undefined;
    show: ReturnType<typeof vi.fn>;
    hide: ReturnType<typeof vi.fn>;
    dispose: ReturnType<typeof vi.fn>;
  };

  beforeEach(() => {
    vi.useFakeTimers();

    mockItem = {
      text: "",
      tooltip: "",
      command: undefined,
      show: vi.fn(),
      hide: vi.fn(),
      dispose: vi.fn(),
    };

    vi.mocked(vscode.window.createStatusBarItem).mockReturnValue(
      mockItem as unknown as vscode.StatusBarItem
    );
  });

  afterEach(() => {
    vi.useRealTimers();
    vi.restoreAllMocks();
  });

  // ---------------------------------------------------------------------------
  // Initial state
  // ---------------------------------------------------------------------------

  it("starts hidden with displayState 'hidden'", () => {
    const bar = new StallStatusBarItem();

    expect(bar.getDisplayState()).toBe("hidden");
    expect(mockItem.show).not.toHaveBeenCalled();

    bar.dispose();
  });

  it("sets click command to nightgauge.showOutput", () => {
    const bar = new StallStatusBarItem();

    expect(mockItem.command).toBe("nightgauge.showOutputWindow");

    bar.dispose();
  });

  it("creates status bar item with priority 95 (left of remote commands at 96)", () => {
    new StallStatusBarItem().dispose();

    expect(vscode.window.createStatusBarItem).toHaveBeenCalledWith(
      vscode.StatusBarAlignment.Left,
      95
    );
  });

  // ---------------------------------------------------------------------------
  // showStalled
  // ---------------------------------------------------------------------------

  it("shows stalled state and item when showStalled() called", () => {
    const bar = new StallStatusBarItem();

    bar.showStalled("feature-dev", 30_000);

    expect(bar.getDisplayState()).toBe("stalled");
    expect(mockItem.show).toHaveBeenCalledTimes(1);

    bar.dispose();
  });

  it("renders elapsed time in seconds for short durations", () => {
    const bar = new StallStatusBarItem();

    bar.showStalled("feature-dev", 45_000);

    expect(mockItem.text).toBe("$(clock) Stall: feature-dev running for 45s");

    bar.dispose();
  });

  it("renders elapsed time as 'Xm Ys' for durations >= 60s", () => {
    const bar = new StallStatusBarItem();

    bar.showStalled("feature-planning", 90_000);

    expect(mockItem.text).toBe("$(clock) Stall: feature-planning running for 1m 30s");

    bar.dispose();
  });

  it("renders elapsed time for longer durations (> 1 hour)", () => {
    const bar = new StallStatusBarItem();

    bar.showStalled("pr-merge", 3_720_000); // 62 minutes

    expect(mockItem.text).toBe("$(clock) Stall: pr-merge running for 62m 0s");

    bar.dispose();
  });

  it("sets tooltip with stage name and click instruction", () => {
    const bar = new StallStatusBarItem();

    bar.showStalled("feature-dev", 30_000);

    expect(mockItem.tooltip).toContain("feature-dev");
    expect(mockItem.tooltip).toContain("Click to view output panel");

    bar.dispose();
  });

  it("includes stage name in text matching the input stage", () => {
    const bar = new StallStatusBarItem();

    bar.showStalled("pr-create", 60_000);

    expect(mockItem.text).toContain("pr-create");

    bar.dispose();
  });

  // ---------------------------------------------------------------------------
  // 30s ticker
  // ---------------------------------------------------------------------------

  it("updates elapsed time every 30s via internal ticker", () => {
    const bar = new StallStatusBarItem();

    bar.showStalled("feature-dev", 30_000);
    expect(mockItem.text).toBe("$(clock) Stall: feature-dev running for 30s");

    vi.advanceTimersByTime(30_000);
    expect(mockItem.text).toBe("$(clock) Stall: feature-dev running for 1m 0s");

    vi.advanceTimersByTime(30_000);
    expect(mockItem.text).toBe("$(clock) Stall: feature-dev running for 1m 30s");

    bar.dispose();
  });

  it("clears the 30s ticker when clear() is called", () => {
    const bar = new StallStatusBarItem();

    bar.showStalled("feature-dev", 30_000);
    bar.clear();

    // Advance time — ticker should be cleared, text should not update
    vi.advanceTimersByTime(30_000);
    expect(mockItem.text).toBe("$(clock) Stall: feature-dev running for 30s"); // unchanged

    bar.dispose();
  });

  // ---------------------------------------------------------------------------
  // clear
  // ---------------------------------------------------------------------------

  it("hides item and resets state when clear() called", () => {
    const bar = new StallStatusBarItem();

    bar.showStalled("feature-dev", 30_000);
    bar.clear();

    expect(bar.getDisplayState()).toBe("hidden");
    expect(mockItem.hide).toHaveBeenCalledTimes(1);

    bar.dispose();
  });

  it("handles rapid show/clear cycles without accumulating tickers", () => {
    const bar = new StallStatusBarItem();

    // Rapid cycles — each showStalled should replace the previous ticker
    bar.showStalled("feature-dev", 30_000);
    bar.clear();
    bar.showStalled("feature-planning", 60_000);
    bar.clear();
    bar.showStalled("feature-dev", 90_000);

    // Should only have one active ticker; advance 30s
    vi.advanceTimersByTime(30_000);
    expect(mockItem.text).toBe("$(clock) Stall: feature-dev running for 2m 0s");

    bar.dispose();
  });

  it("clear() is idempotent — safe to call multiple times", () => {
    const bar = new StallStatusBarItem();

    bar.showStalled("feature-dev", 30_000);
    bar.clear();
    bar.clear(); // Second call must not throw

    expect(bar.getDisplayState()).toBe("hidden");
    expect(mockItem.hide).toHaveBeenCalledTimes(2);

    bar.dispose();
  });

  // ---------------------------------------------------------------------------
  // dispose
  // ---------------------------------------------------------------------------

  it("disposes the status bar item on dispose()", () => {
    const bar = new StallStatusBarItem();

    bar.dispose();

    expect(mockItem.dispose).toHaveBeenCalledTimes(1);
  });

  it("clears the ticker on dispose() to prevent memory leaks", () => {
    const bar = new StallStatusBarItem();

    bar.showStalled("feature-dev", 30_000);
    bar.dispose();

    // Ticker should be cleared; advancing time should not update text
    vi.advanceTimersByTime(30_000);
    expect(mockItem.text).toBe("$(clock) Stall: feature-dev running for 30s"); // unchanged

    // No exception from disposed interval
  });
});
