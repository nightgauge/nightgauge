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
  MarkdownString: class MarkdownString {
    value: string = "";
    isTrusted?: boolean;
    constructor(value?: string) {
      this.value = value ?? "";
    }
    appendMarkdown(value: string) {
      this.value += value;
      return this;
    }
  },
}));

import * as vscode from "vscode";
import {
  StatusBarManager,
  formatUsageValue,
  formatUsageWindowText,
  renderUsageBar,
  usageThresholdColor,
  buildUsageTooltip,
} from "../../src/utils/statusBar";
import type { UsageSnapshot, UsageWindow } from "../../src/services/usage/types";
import type { ExecutionAdapter } from "../../src/config/schema";

/** Build a `UsageWindow` fixture, defaulting to an unlimited monthly window. */
function makeWindow(overrides: Partial<UsageWindow> = {}): UsageWindow {
  return {
    id: "local-telemetry:monthly",
    label: "This month",
    scope: "monthly",
    used: 0,
    limit: null,
    unit: "usd",
    resetsAt: null,
    confidence: "measured",
    ...overrides,
  };
}

/** Build a `UsageSnapshot` fixture from a window list. */
function makeSnapshot(windows: UsageWindow[], adapter: ExecutionAdapter = "claude"): UsageSnapshot {
  return {
    adapter,
    plan: { kind: windows.length > 0 ? "pay-per-token" : "unknown" },
    capturedAt: new Date("2026-08-18T10:00:00Z"),
    windows,
  };
}

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

  describe("usage meter (Issue #659)", () => {
    it("wires the click gesture to cycling, not to opening the dashboard", () => {
      expect(mockUsageItem.command).toBe("nightgauge.cycleUsageMetric");
    });

    it("stays hidden until the first snapshot arrives", () => {
      expect(mockUsageItem.show).not.toHaveBeenCalled();
    });

    it("renders a bar-mode window (limit known) and colors by threshold", () => {
      const window = makeWindow({ used: 50, limit: 100, unit: "usd" });
      statusBar.showUsageSnapshot(makeSnapshot([window]));

      expect(mockUsageItem.text).toBe(`$(flame) claude ${renderUsageBar(50)} 50%`);
      expect(mockUsageItem.backgroundColor).toBeUndefined();
      expect(mockUsageItem.show).toHaveBeenCalled();
    });

    it("colors warningBackground at >= 80%", () => {
      statusBar.showUsageSnapshot(makeSnapshot([makeWindow({ used: 80, limit: 100 })]));
      expect(mockUsageItem.backgroundColor).toBeInstanceOf(vscode.ThemeColor);
      expect((mockUsageItem.backgroundColor as vscode.ThemeColor).id).toBe(
        "statusBarItem.warningBackground"
      );
    });

    it("colors errorBackground at >= 90%", () => {
      statusBar.showUsageSnapshot(makeSnapshot([makeWindow({ used: 90, limit: 100 })]));
      expect(mockUsageItem.backgroundColor).toBeInstanceOf(vscode.ThemeColor);
      expect((mockUsageItem.backgroundColor as vscode.ThemeColor).id).toBe(
        "statusBarItem.errorBackground"
      );
    });

    it("leaves the warning/error background once usage drops back under 80% (bidirectional)", () => {
      statusBar.showUsageSnapshot(makeSnapshot([makeWindow({ used: 95, limit: 100 })]));
      expect(mockUsageItem.backgroundColor).toBeInstanceOf(vscode.ThemeColor);

      statusBar.showUsageSnapshot(makeSnapshot([makeWindow({ used: 10, limit: 100 })]));
      expect(mockUsageItem.backgroundColor).toBeUndefined();
    });

    it("renders an absolute figure — never a bar or a percentage — when limit is null", () => {
      const window = makeWindow({ used: 4.12, limit: null, unit: "usd", label: "Today" });
      statusBar.showUsageSnapshot(makeSnapshot([window]));

      expect(mockUsageItem.text).toBe("$(flame) claude $4.12 today");
      expect(mockUsageItem.text).not.toContain("%");
      expect(mockUsageItem.backgroundColor).toBeUndefined();
    });

    it("renders an explicit 'usage unknown' state rather than hiding when no provider claims the adapter", () => {
      statusBar.showUsageSnapshot(makeSnapshot([], "ollama"));

      expect(mockUsageItem.text).toBe("$(flame) ollama usage unknown");
      expect(mockUsageItem.show).toHaveBeenCalled();
      expect(mockUsageItem.backgroundColor).toBeUndefined();
    });

    it("builds a tooltip listing every window with used/limit, reset, and confidence", () => {
      const monthly = makeWindow({ id: "m", label: "This month", used: 8, limit: 10 });
      statusBar.showUsageSnapshot(makeSnapshot([monthly]));

      const tooltip = (mockUsageItem.tooltip as vscode.MarkdownString).value;
      expect(tooltip).toContain("This month");
      expect(tooltip).toContain("$8.00");
      expect(tooltip).toContain("$10.00");
      expect(tooltip).toContain("measured");
      expect(tooltip).toContain("[Open Dashboard](command:nightgauge.showDashboard)");
    });

    describe("cycleUsageWindow", () => {
      const session = makeWindow({ id: "session", label: "This session", scope: "session" });
      const daily = makeWindow({ id: "daily", label: "Today", scope: "daily" });
      const monthly = makeWindow({ id: "monthly", label: "This month", scope: "monthly" });

      it("returns null and does nothing when there is no snapshot yet", () => {
        expect(statusBar.cycleUsageWindow()).toBeNull();
      });

      it("returns null when the snapshot is unknown (zero windows)", () => {
        statusBar.showUsageSnapshot(makeSnapshot([], "ollama"));
        expect(statusBar.cycleUsageWindow()).toBeNull();
      });

      it("advances session -> daily -> monthly -> session, wrapping around", () => {
        statusBar.showUsageSnapshot(makeSnapshot([session, daily, monthly]));

        expect(statusBar.cycleUsageWindow()).toBe("daily");
        expect(statusBar.cycleUsageWindow()).toBe("monthly");
        expect(statusBar.cycleUsageWindow()).toBe("session");
      });

      it("re-renders the item text on each cycle", () => {
        statusBar.showUsageSnapshot(
          makeSnapshot([
            makeWindow({ id: "session", used: 1, limit: null, unit: "usd", label: "This session" }),
            makeWindow({ id: "daily", used: 2, limit: null, unit: "usd", label: "Today" }),
          ])
        );
        expect(mockUsageItem.text).toBe("$(flame) claude $1.00 this session");

        statusBar.cycleUsageWindow();
        expect(mockUsageItem.text).toBe("$(flame) claude $2.00 today");
      });
    });

    describe("setSelectedUsageWindowId", () => {
      it("restores a persisted selection before the first snapshot renders it", () => {
        statusBar.setSelectedUsageWindowId("daily");
        statusBar.showUsageSnapshot(
          makeSnapshot([
            makeWindow({ id: "session", used: 1, limit: null, label: "This session" }),
            makeWindow({ id: "daily", used: 2, limit: null, label: "Today" }),
          ])
        );

        expect(mockUsageItem.text).toBe("$(flame) claude $2.00 today");
      });

      it("falls back to the first window when the persisted id is not in this snapshot", () => {
        statusBar.setSelectedUsageWindowId("stale-id-from-a-different-adapter");
        statusBar.showUsageSnapshot(
          makeSnapshot([makeWindow({ id: "session", used: 1, limit: null, label: "This session" })])
        );

        expect(mockUsageItem.text).toBe("$(flame) claude $1.00 this session");
      });
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

// ── Issue #659: adapter usage meter — pure formatting functions ───────────

describe("renderUsageBar (#659)", () => {
  it("renders all-empty at 0%", () => {
    expect(renderUsageBar(0)).toBe("░░░░░░░░");
  });

  it("renders all-full at 100%", () => {
    expect(renderUsageBar(100)).toBe("████████");
  });

  it("renders an exact half-fill with no partial character at 50%", () => {
    expect(renderUsageBar(50)).toBe("████░░░░");
  });

  it("renders a partial eighths-block character for a fractional segment", () => {
    // 43.75% of 8 segments = 3.5 segments = 3 full + a half-filled 4th.
    expect(renderUsageBar(43.75)).toBe("███▌░░░░");
  });

  it("clamps negative and >100 fill to the bar's own bounds", () => {
    expect(renderUsageBar(-10)).toBe("░░░░░░░░");
    expect(renderUsageBar(250)).toBe("████████");
  });
});

describe("formatUsageValue (#659)", () => {
  it("formats usd to two decimal places", () => {
    expect(formatUsageValue(4.1, "usd")).toBe("$4.10");
  });

  it("formats percent rounded, with a % suffix", () => {
    expect(formatUsageValue(61.6, "percent")).toBe("62%");
  });

  it("formats small token counts as a bare integer", () => {
    expect(formatUsageValue(42, "tokens")).toBe("42 tokens");
  });

  it("formats thousands of tokens with a k suffix", () => {
    expect(formatUsageValue(812_000, "tokens")).toBe("812k tokens");
  });

  it("formats millions of tokens with an m suffix", () => {
    expect(formatUsageValue(1_250_000, "tokens")).toBe("1.3m tokens");
  });

  it("formats requests with singular/plural agreement", () => {
    expect(formatUsageValue(1, "requests")).toBe("1 request");
    expect(formatUsageValue(5, "requests")).toBe("5 requests");
  });
});

describe("formatUsageWindowText (#659)", () => {
  it("renders icon, adapter, bar, and rounded percentage for a bar-mode window", () => {
    const window: UsageWindow = {
      id: "w",
      label: "This month",
      scope: "monthly",
      used: 62,
      limit: 100,
      unit: "usd",
      resetsAt: null,
      confidence: "measured",
    };
    expect(formatUsageWindowText("claude", window)).toBe(
      `$(flame) claude ${renderUsageBar(62)} 62%`
    );
  });

  it("appends a resets-in suffix when the window has a scheduled reset", () => {
    const now = new Date("2026-08-18T10:00:00Z");
    const window: UsageWindow = {
      id: "w",
      label: "Today",
      scope: "daily",
      used: 10,
      limit: 100,
      unit: "usd",
      resetsAt: new Date("2026-08-18T11:30:00Z"), // 1h 30m from `now`
      confidence: "measured",
    };
    expect(formatUsageWindowText("codex", window, now)).toBe(
      `$(flame) codex ${renderUsageBar(10)} 10% · resets 1h 30m`
    );
  });

  it("omits the resets suffix when resetsAt is null (session windows)", () => {
    const window: UsageWindow = {
      id: "w",
      label: "This session",
      scope: "session",
      used: 5,
      limit: 20,
      unit: "usd",
      resetsAt: null,
      confidence: "measured",
    };
    expect(formatUsageWindowText("claude", window)).not.toContain("resets");
  });

  it("renders the absolute figure — not a bar — when limit is null", () => {
    const window: UsageWindow = {
      id: "w",
      label: "This session",
      scope: "session",
      used: 812_000,
      limit: null,
      unit: "tokens",
      resetsAt: null,
      confidence: "measured",
    };
    expect(formatUsageWindowText("gemini", window)).toBe(
      "$(flame) gemini 812k tokens this session"
    );
  });
});

describe("usageThresholdColor (#659)", () => {
  const base: UsageWindow = {
    id: "w",
    label: "This month",
    scope: "monthly",
    used: 0,
    limit: 100,
    unit: "usd",
    resetsAt: null,
    confidence: "measured",
  };

  it("returns undefined below 80%", () => {
    expect(usageThresholdColor({ ...base, used: 79 })).toBeUndefined();
  });

  it("returns the warning color at exactly 80%", () => {
    expect(usageThresholdColor({ ...base, used: 80 })?.id).toBe("statusBarItem.warningBackground");
  });

  it("returns the error color at exactly 90%", () => {
    expect(usageThresholdColor({ ...base, used: 90 })?.id).toBe("statusBarItem.errorBackground");
  });

  it("returns undefined when limit is null — no ceiling to measure against", () => {
    expect(usageThresholdColor({ ...base, used: 99999, limit: null })).toBeUndefined();
  });

  it("returns undefined when limit is zero (not a valid ceiling)", () => {
    expect(usageThresholdColor({ ...base, used: 5, limit: 0 })).toBeUndefined();
  });
});

describe("buildUsageTooltip (#659)", () => {
  it("lists every window's used/limit, reset time, and confidence", () => {
    const snapshot: UsageSnapshot = {
      adapter: "claude",
      plan: { kind: "pay-per-token" },
      capturedAt: new Date("2026-08-18T10:00:00Z"),
      windows: [
        {
          id: "s",
          label: "This session",
          scope: "session",
          used: 1.5,
          limit: null,
          unit: "usd",
          resetsAt: null,
          confidence: "measured",
        },
        {
          id: "m",
          label: "This month",
          scope: "monthly",
          used: 8,
          limit: 10,
          unit: "usd",
          resetsAt: new Date("2026-09-01T00:00:00Z"),
          confidence: "estimated",
        },
      ],
    };

    const tooltip = buildUsageTooltip(snapshot);

    expect(tooltip.isTrusted).toBe(true);
    expect(tooltip.value).toContain("claude usage");
    expect(tooltip.value).toContain("This session");
    expect(tooltip.value).toContain("$1.50");
    expect(tooltip.value).toContain("no limit configured");
    expect(tooltip.value).toContain("This month");
    expect(tooltip.value).toContain("$8.00");
    expect(tooltip.value).toContain("$10.00");
    expect(tooltip.value).toContain("estimated");
    expect(tooltip.value).toContain("[Open Dashboard](command:nightgauge.showDashboard)");
  });

  it("explains the unknown state rather than showing an empty list", () => {
    const snapshot: UsageSnapshot = {
      adapter: "ollama",
      plan: { kind: "unknown" },
      capturedAt: new Date(),
      windows: [],
    };

    const tooltip = buildUsageTooltip(snapshot);

    expect(tooltip.value).toContain("unknown");
    expect(tooltip.value).toContain("[Open Dashboard](command:nightgauge.showDashboard)");
  });
});
