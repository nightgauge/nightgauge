/**
 * Tests for StatusBarManager
 *
 * @see Issue #320 - Stop After Current Issue Button for Batch Mode
 * Addresses critical test gap for StatusBarManager
 */

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";

// Mock vscode before imports
vi.mock("vscode", () => ({
  window: {
    createStatusBarItem: vi.fn(),
  },
  commands: {
    executeCommand: vi.fn(),
  },
  StatusBarAlignment: {
    Left: 1,
    Right: 2,
  },
  ThemeColor: class ThemeColor {
    constructor(public id: string) {}
  },
}));

import * as vscode from "vscode";
import { StatusBarManager } from "../../src/utils/statusBar";

describe("StatusBarManager", () => {
  let statusBar: StatusBarManager;
  let mockStatusBarItem: vscode.StatusBarItem;
  let mockTargetBranchItem: vscode.StatusBarItem;
  let mockUsageItem: vscode.StatusBarItem;
  let mockModeItem: vscode.StatusBarItem;
  let mockRateLimitItem: vscode.StatusBarItem;

  beforeEach(() => {
    // Create mock status bar items
    mockStatusBarItem = {
      text: "",
      tooltip: "",
      backgroundColor: undefined,
      command: "",
      show: vi.fn(),
      hide: vi.fn(),
      dispose: vi.fn(),
    } as unknown as vscode.StatusBarItem;

    mockTargetBranchItem = {
      text: "",
      tooltip: "",
      backgroundColor: undefined,
      command: "",
      show: vi.fn(),
      hide: vi.fn(),
      dispose: vi.fn(),
    } as unknown as vscode.StatusBarItem;

    mockUsageItem = {
      text: "",
      tooltip: "",
      backgroundColor: undefined,
      command: "",
      show: vi.fn(),
      hide: vi.fn(),
      dispose: vi.fn(),
    } as unknown as vscode.StatusBarItem;

    mockModeItem = {
      text: "",
      tooltip: "",
      backgroundColor: undefined,
      command: "",
      show: vi.fn(),
      hide: vi.fn(),
      dispose: vi.fn(),
    } as unknown as vscode.StatusBarItem;

    mockRateLimitItem = {
      text: "",
      tooltip: "",
      backgroundColor: undefined,
      command: "",
      show: vi.fn(),
      hide: vi.fn(),
      dispose: vi.fn(),
    } as unknown as vscode.StatusBarItem;

    // Mock vscode.window.createStatusBarItem
    // Call 1 → main pipeline item, call 2 → target branch item,
    // call 3 → usage item, call 4 → performance-mode item (Issue #3009),
    // call 5 → rate-limit counter item
    let callCount = 0;
    vi.mocked(vscode.window.createStatusBarItem).mockImplementation(() => {
      callCount++;
      if (callCount === 1) return mockStatusBarItem;
      if (callCount === 2) return mockTargetBranchItem;
      if (callCount === 3) return mockUsageItem;
      if (callCount === 4) return mockModeItem;
      return mockRateLimitItem;
    });

    // Mock vscode.commands.executeCommand
    vi.mocked(vscode.commands.executeCommand).mockResolvedValue(undefined);

    statusBar = new StatusBarManager();
  });

  afterEach(() => {
    statusBar.dispose();
  });

  describe("showStoppingAfterCurrent", () => {
    it("should display correct message with issue number", () => {
      statusBar.showStoppingAfterCurrent(123);

      expect(mockStatusBarItem.text).toBe("$(debug-pause) Stopping after #123");
      expect(mockStatusBarItem.tooltip).toBe("⏸ Batch will stop after issue #123 completes");
    });

    it("should use pause background color", () => {
      statusBar.showStoppingAfterCurrent(123);

      expect(mockStatusBarItem.backgroundColor).toBeInstanceOf(vscode.ThemeColor);
      // The actual color is 'statusBarItem.warningBackground' for paused state
    });

    it("should set context key nightgauge.stopAfterCurrentBatch to true", () => {
      statusBar.showStoppingAfterCurrent(123);

      expect(vscode.commands.executeCommand).toHaveBeenCalledWith(
        "setContext",
        "nightgauge.stopAfterCurrentBatch",
        true
      );
    });

    it("should set context key nightgauge.pipelineRunning to true", () => {
      statusBar.showStoppingAfterCurrent(123);

      expect(vscode.commands.executeCommand).toHaveBeenCalledWith(
        "setContext",
        "nightgauge.pipelineRunning",
        true
      );
    });

    it("should set command to stopPipeline", () => {
      statusBar.showStoppingAfterCurrent(123);

      expect(mockStatusBarItem.command).toBe("nightgauge.stopPipeline");
    });

    it("should handle different issue numbers", () => {
      statusBar.showStoppingAfterCurrent(456);

      expect(mockStatusBarItem.text).toBe("$(debug-pause) Stopping after #456");
      expect(mockStatusBarItem.tooltip).toBe("⏸ Batch will stop after issue #456 completes");
    });
  });

  describe("showIdle", () => {
    it("should show idle state", () => {
      statusBar.showIdle();

      expect(mockStatusBarItem.text).toBe("$(nightgauge) Nightgauge");
      expect(mockStatusBarItem.tooltip).toBe("Nightgauge — Click to open Dashboard");
      expect(mockStatusBarItem.command).toBe("nightgauge.showDashboard");
    });

    it("should set pipelineRunning context to false", () => {
      statusBar.showIdle();

      expect(vscode.commands.executeCommand).toHaveBeenCalledWith(
        "setContext",
        "nightgauge.pipelineRunning",
        false
      );
    });
  });

  describe("showRunning", () => {
    it("should show running state with stage name", () => {
      statusBar.showRunning("feature-dev");

      expect(mockStatusBarItem.text).toBe("$(sync~spin) Development");
      expect(mockStatusBarItem.tooltip).toBe("Pipeline running: Development");
    });

    it("should set pipelineRunning context to true", () => {
      statusBar.showRunning("feature-dev");

      expect(vscode.commands.executeCommand).toHaveBeenCalledWith(
        "setContext",
        "nightgauge.pipelineRunning",
        true
      );
    });

    it("should set command to stopPipeline", () => {
      statusBar.showRunning("feature-dev");

      expect(mockStatusBarItem.command).toBe("nightgauge.stopPipeline");
    });
  });

  describe("showComplete", () => {
    it("should show complete state", () => {
      statusBar.showComplete("feature-dev");

      expect(mockStatusBarItem.text).toBe("$(check) Development");
      expect(mockStatusBarItem.tooltip).toBe("Development complete");
    });

    it("should auto-reset to idle after timeout", async () => {
      vi.useFakeTimers();

      statusBar.showComplete("feature-dev");
      expect(mockStatusBarItem.text).toBe("$(check) Development");

      // Fast-forward time by 5 seconds
      vi.advanceTimersByTime(5000);

      expect(mockStatusBarItem.text).toBe("$(nightgauge) Nightgauge");

      vi.useRealTimers();
    });
  });

  describe("showError", () => {
    it("should show error state with message", () => {
      statusBar.showError("Test error message");

      expect(mockStatusBarItem.text).toBe("$(error) Error");
      expect(mockStatusBarItem.tooltip).toBe("Test error message");
    });

    it("should set pipelineRunning context to false", () => {
      statusBar.showError("Test error");

      expect(vscode.commands.executeCommand).toHaveBeenCalledWith(
        "setContext",
        "nightgauge.pipelineRunning",
        false
      );
    });
  });

  describe("dispose", () => {
    it("should dispose both status bar items", () => {
      statusBar.dispose();

      expect(mockStatusBarItem.dispose).toHaveBeenCalled();
      expect(mockTargetBranchItem.dispose).toHaveBeenCalled();
    });
  });

  describe("setTargetBranch (Issue #102)", () => {
    it("should display target branch with git-branch icon", () => {
      statusBar.setTargetBranch("main");

      expect(mockTargetBranchItem.text).toBe("$(git-branch) → main");
      expect(mockTargetBranchItem.show).toHaveBeenCalled();
    });

    it("should set tooltip with branch name", () => {
      statusBar.setTargetBranch("develop");

      expect(mockTargetBranchItem.tooltip).toContain("Target branch: develop");
      expect(mockTargetBranchItem.tooltip).toContain("Click to change");
    });

    it("should highlight non-default branches with warning background", () => {
      statusBar.setTargetBranch("release/v2.0");

      expect(mockTargetBranchItem.backgroundColor).toBeInstanceOf(vscode.ThemeColor);
    });

    it("should not highlight main branch", () => {
      statusBar.setTargetBranch("main");

      expect(mockTargetBranchItem.backgroundColor).toBeUndefined();
    });

    it("should not highlight master branch", () => {
      statusBar.setTargetBranch("master");

      expect(mockTargetBranchItem.backgroundColor).toBeUndefined();
    });

    it("should set command to selectTargetBranch", () => {
      expect(mockTargetBranchItem.command).toBe("nightgauge.selectTargetBranch");
    });
  });

  describe("hideTargetBranch (Issue #102)", () => {
    it("should hide target branch status bar item", () => {
      statusBar.setTargetBranch("develop");
      statusBar.hideTargetBranch();

      expect(mockTargetBranchItem.hide).toHaveBeenCalled();
    });

    it("should clear current target branch", () => {
      statusBar.setTargetBranch("develop");
      expect(statusBar.getTargetBranch()).toBe("develop");

      statusBar.hideTargetBranch();
      expect(statusBar.getTargetBranch()).toBeNull();
    });
  });

  describe("getTargetBranch (Issue #102)", () => {
    it("should return current target branch", () => {
      statusBar.setTargetBranch("epic/auth");

      expect(statusBar.getTargetBranch()).toBe("epic/auth");
    });

    it("should return null when no target branch set", () => {
      // Initially hidden via constructor
      expect(statusBar.getTargetBranch()).toBeNull();
    });

    it("should return null after hideTargetBranch", () => {
      statusBar.setTargetBranch("develop");
      statusBar.hideTargetBranch();

      expect(statusBar.getTargetBranch()).toBeNull();
    });
  });

  describe("performance mode selector (Issue #3009 — replaces Supercharge from #2433)", () => {
    it("renders Mode: Elevated by default with no warning background", () => {
      expect(mockModeItem.text).toBe("$(zap) Mode: Elevated");
      expect(String(mockModeItem.tooltip)).toContain("Performance mode: Elevated");
      expect(mockModeItem.backgroundColor).toBeUndefined();
    });

    it("wires click to the QuickPick command and is shown by default", () => {
      expect(mockModeItem.command).toBe("nightgauge.selectPerformanceMode");
      expect(mockModeItem.show).toHaveBeenCalled();
    });

    it("flips to Maximum label + warning background when set to maximum", () => {
      statusBar.setPerformanceMode("maximum");

      expect(mockModeItem.text).toBe("$(zap) Mode: Maximum");
      expect(String(mockModeItem.tooltip)).toContain("Performance mode: Maximum");
      expect(mockModeItem.backgroundColor).toBeInstanceOf(vscode.ThemeColor);
    });

    it("reverts to muted styling when set back to elevated", () => {
      statusBar.setPerformanceMode("maximum");
      statusBar.setPerformanceMode("elevated");

      expect(mockModeItem.text).toBe("$(zap) Mode: Elevated");
      expect(mockModeItem.backgroundColor).toBeUndefined();
    });

    it("legacy setSuperchargeActive maps active=true → maximum", () => {
      statusBar.setSuperchargeActive(true);
      expect(mockModeItem.text).toBe("$(zap) Mode: Maximum");
      statusBar.setSuperchargeActive(false);
      expect(mockModeItem.text).toBe("$(zap) Mode: Elevated");
    });

    it("disposes the mode item on dispose()", () => {
      statusBar.dispose();
      expect(mockModeItem.dispose).toHaveBeenCalled();
    });
  });
});

// ── Issue #3446: Autonomous quota cooldown status-bar visibility ──────────

import { formatCooldownLabel, formatCooldownRemaining } from "../../src/utils/statusBar";

describe("formatCooldownLabel (#3446)", () => {
  it("renders ISO-8601 deadline as HH:MM UTC", () => {
    const until = new Date("2026-05-11T03:31:00Z");
    expect(formatCooldownLabel(until, new Date("2026-05-11T02:30:00Z"))).toBe("03:31 UTC");
  });

  it("zero-pads single-digit hours and minutes", () => {
    const until = new Date("2026-05-11T07:05:00Z");
    expect(formatCooldownLabel(until, new Date("2026-05-11T06:00:00Z"))).toBe("07:05 UTC");
  });

  it("returns 'soon' for already-expired deadlines", () => {
    const until = new Date("2026-05-11T01:00:00Z");
    expect(formatCooldownLabel(until, new Date("2026-05-11T02:00:00Z"))).toBe("soon");
  });

  it("returns 'soon' for malformed input", () => {
    expect(formatCooldownLabel(new Date("not-a-date"), new Date())).toBe("soon");
  });
});

describe("formatCooldownRemaining (#3446)", () => {
  it("renders hour-grade remainders as 'Xh Ym'", () => {
    const until = new Date("2026-05-11T03:31:00Z");
    const now = new Date("2026-05-11T02:30:00Z");
    expect(formatCooldownRemaining(until, now)).toBe("1h 1m");
  });

  it("renders minute-grade remainders as 'Ym Zs'", () => {
    const until = new Date("2026-05-11T03:01:30Z");
    const now = new Date("2026-05-11T03:00:00Z");
    expect(formatCooldownRemaining(until, now)).toBe("1m 30s");
  });

  it("renders second-grade remainders as 'Zs'", () => {
    const until = new Date("2026-05-11T03:00:42Z");
    const now = new Date("2026-05-11T03:00:00Z");
    expect(formatCooldownRemaining(until, now)).toBe("42s");
  });

  it("returns 0s for already-expired deadlines", () => {
    const until = new Date("2026-05-11T02:00:00Z");
    const now = new Date("2026-05-11T03:00:00Z");
    expect(formatCooldownRemaining(until, now)).toBe("0s");
  });
});

describe("StatusBarManager.showAutonomousCooldown (#3446)", () => {
  let sb: StatusBarManager;
  let mainItem: vscode.StatusBarItem;

  beforeEach(() => {
    mainItem = {
      text: "",
      tooltip: "",
      backgroundColor: undefined,
      command: "",
      show: vi.fn(),
      hide: vi.fn(),
      dispose: vi.fn(),
    } as unknown as vscode.StatusBarItem;
    const dummy = (): vscode.StatusBarItem =>
      ({
        text: "",
        tooltip: "",
        backgroundColor: undefined,
        command: "",
        show: vi.fn(),
        hide: vi.fn(),
        dispose: vi.fn(),
      }) as unknown as vscode.StatusBarItem;
    let n = 0;
    vi.mocked(vscode.window.createStatusBarItem).mockImplementation(() => {
      n++;
      if (n === 1) return mainItem;
      return dummy();
    });
    vi.mocked(vscode.commands.executeCommand).mockResolvedValue(undefined);
    sb = new StatusBarManager();
  });

  afterEach(() => {
    sb.dispose();
  });

  it("shows 'cooldown until HH:MM UTC (Xh Ym)' instead of 'running' when active", () => {
    const until = new Date("2026-05-11T03:31:00Z");
    const now = new Date("2026-05-11T02:30:00Z");
    sb.showAutonomousCooldown(until, now);
    expect(mainItem.text).toBe("$(watch) Autonomous: cooldown until 03:31 UTC (1h 1m)");
    expect(typeof mainItem.tooltip).toBe("string");
    expect(String(mainItem.tooltip)).toContain("quota cooldown active");
    expect(mainItem.command).toBe("nightgauge.autonomousStatus");
  });

  it("uses the paused/warning background to differentiate from active running", () => {
    sb.showAutonomousCooldown(new Date(Date.now() + 60_000));
    expect(mainItem.backgroundColor).toBeInstanceOf(vscode.ThemeColor);
  });
});
