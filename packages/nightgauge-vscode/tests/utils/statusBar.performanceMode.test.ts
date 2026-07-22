/**
 * statusBar.performanceMode.test.ts (Issue #3009)
 *
 * Verifies the new mode-aware status bar item rendering:
 *   - constructed in Elevated mode (default) — no warning background.
 *   - setPerformanceMode("maximum") flips the background and label.
 *   - setPerformanceMode("efficiency") restores muted styling.
 *   - the status-bar item is wired to the QuickPick command.
 */
import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";

vi.mock("vscode", () => ({
  window: {
    createStatusBarItem: vi.fn(),
  },
  commands: {
    executeCommand: vi.fn(),
  },
  StatusBarAlignment: { Left: 1, Right: 2 },
  ThemeColor: class ThemeColor {
    constructor(public id: string) {}
  },
}));

import * as vscode from "vscode";
import { StatusBarManager } from "../../src/utils/statusBar";

describe("StatusBarManager — performance mode (Issue #3009)", () => {
  let statusBar: StatusBarManager;
  let mainItem: vscode.StatusBarItem;
  let modeItem: vscode.StatusBarItem;

  function makeMockItem(): vscode.StatusBarItem {
    return {
      text: "",
      tooltip: "",
      backgroundColor: undefined,
      command: "",
      show: vi.fn(),
      hide: vi.fn(),
      dispose: vi.fn(),
    } as unknown as vscode.StatusBarItem;
  }

  beforeEach(() => {
    mainItem = makeMockItem();
    const targetItem = makeMockItem();
    const usageItem = makeMockItem();
    modeItem = makeMockItem();
    const rateLimitItem = makeMockItem();

    let call = 0;
    vi.mocked(vscode.window.createStatusBarItem).mockImplementation(() => {
      call++;
      if (call === 1) return mainItem;
      if (call === 2) return targetItem;
      if (call === 3) return usageItem;
      if (call === 4) return modeItem;
      return rateLimitItem;
    });
    vi.mocked(vscode.commands.executeCommand).mockResolvedValue(undefined);

    statusBar = new StatusBarManager();
  });

  afterEach(() => {
    statusBar.dispose();
  });

  it("renders the Elevated default on construction with no warning background", () => {
    expect(modeItem.text).toBe("$(zap) Mode: Elevated");
    expect(String(modeItem.tooltip)).toContain("Performance mode: Elevated");
    expect(modeItem.backgroundColor).toBeUndefined();
    expect(modeItem.command).toBe("nightgauge.selectPerformanceMode");
    expect(modeItem.show).toHaveBeenCalled();
  });

  it("setPerformanceMode('maximum') flips to warning background", () => {
    statusBar.setPerformanceMode("maximum");

    expect(modeItem.text).toBe("$(zap) Mode: Maximum");
    expect(String(modeItem.tooltip)).toContain("Performance mode: Maximum");
    expect(modeItem.backgroundColor).toBeInstanceOf(vscode.ThemeColor);
  });

  it("setPerformanceMode('efficiency') uses muted styling", () => {
    statusBar.setPerformanceMode("maximum");
    statusBar.setPerformanceMode("efficiency");

    expect(modeItem.text).toBe("$(zap) Mode: Efficiency");
    expect(modeItem.backgroundColor).toBeUndefined();
  });

  it("getPerformanceMode reflects the most recent set", () => {
    expect(statusBar.getPerformanceMode()).toBe("elevated");
    statusBar.setPerformanceMode("maximum");
    expect(statusBar.getPerformanceMode()).toBe("maximum");
  });

  it("idle state shows no badge for Elevated and a bolt otherwise", () => {
    statusBar.setPerformanceMode("elevated");
    statusBar.showIdle();
    expect(mainItem.text).toBe("$(nightgauge) Nightgauge");

    statusBar.setPerformanceMode("maximum");
    statusBar.showIdle();
    // Mode label lives in the dedicated mode item; main item only shows a bolt
    // to avoid duplicating "Maximum" right next to "Mode: Maximum".
    expect(mainItem.text).toBe("$(nightgauge) Nightgauge ⚡");
    expect(String(mainItem.text)).not.toContain("MAXIMUM");
  });

  it("legacy setSuperchargeActive maps active=true → maximum", () => {
    statusBar.setSuperchargeActive(true);
    expect(statusBar.getPerformanceMode()).toBe("maximum");

    statusBar.setSuperchargeActive(false);
    expect(statusBar.getPerformanceMode()).toBe("elevated");
  });

  it("dispose() disposes the mode item", () => {
    statusBar.dispose();
    expect(modeItem.dispose).toHaveBeenCalled();
  });

  // ---- Custom per-stage models (Issue #20) ----

  it("setCustomOverridesActive(true) renders 'Mode: Custom' regardless of preset", () => {
    statusBar.setPerformanceMode("maximum");
    statusBar.setCustomOverridesActive(true);

    expect(modeItem.text).toBe("$(zap) Mode: Custom");
    expect(String(modeItem.tooltip)).toContain("Custom");
    // Custom shadows the preset — no warning tint even though preset is maximum.
    expect(modeItem.backgroundColor).toBeUndefined();
  });

  it("setCustomOverridesActive(false) restores the preset label", () => {
    statusBar.setPerformanceMode("efficiency");
    statusBar.setCustomOverridesActive(true);
    expect(modeItem.text).toBe("$(zap) Mode: Custom");

    statusBar.setCustomOverridesActive(false);
    expect(modeItem.text).toBe("$(zap) Mode: Efficiency");
  });

  it("idle main item shows a bolt when custom overrides are active even on Elevated", () => {
    statusBar.setPerformanceMode("elevated");
    statusBar.setCustomOverridesActive(true);
    statusBar.showIdle();
    expect(mainItem.text).toBe("$(nightgauge) Nightgauge ⚡");
    expect(String(mainItem.tooltip)).toContain("Custom");
  });

  it("publishes the custom-models context key", () => {
    statusBar.setCustomOverridesActive(true);
    expect(vscode.commands.executeCommand).toHaveBeenCalledWith(
      "setContext",
      "nightgauge.customStageModels",
      true
    );
  });
});
