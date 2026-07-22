/**
 * NotificationService.test.ts
 *
 * Unit tests for NotificationService, focusing on:
 * - Notification methods (notifyUserInputNeeded, notifyPipelineComplete, notifyPipelineError)
 * - Batch notification methods (notifyBatchProgress, notifyBatchComplete)
 * - Platform-specific behavior (macOS detection)
 * - Configuration toggles and debounce logic
 * - Do Not Disturb detection
 *
 * @see Issue #274 - Add NotificationService unit tests
 * @see Epic #270 - Improve extension test coverage
 */

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import type { NotificationSettings } from "../../src/config/notificationSettings";

// Store original platform for restoration
const originalPlatform = process.platform;

// Helper to mock platform
function mockPlatform(platform: NodeJS.Platform): void {
  Object.defineProperty(process, "platform", {
    value: platform,
    configurable: true,
  });
}

// Restore platform after tests
function restorePlatform(): void {
  Object.defineProperty(process, "platform", {
    value: originalPlatform,
    configurable: true,
  });
}

// Mock child_process
vi.mock("child_process", () => ({
  spawn: vi.fn(() => ({
    on: vi.fn((event: string, callback: () => void) => {
      if (event === "close") {
        // Immediately invoke close callback
        callback();
      }
      return { on: vi.fn() }; // Chain support
    }),
  })),
  exec: vi.fn((_cmd: string, callback?: (error: Error | null) => void) => {
    if (callback) {
      callback(null);
    }
  }),
}));

// Mock util.promisify to return a function that immediately resolves
vi.mock("util", () => ({
  promisify: vi.fn(() => vi.fn().mockResolvedValue({ stdout: "0" })),
}));

// Mock vscode
vi.mock("vscode", () => ({
  window: {
    showWarningMessage: vi.fn().mockResolvedValue(undefined),
    showInformationMessage: vi.fn().mockResolvedValue(undefined),
    showErrorMessage: vi.fn().mockResolvedValue(undefined),
    showTextDocument: vi.fn(),
  },
  commands: {
    executeCommand: vi.fn(),
  },
  workspace: {
    getConfiguration: vi.fn(() => ({
      get: vi.fn((_key: string, defaultValue: unknown) => defaultValue),
    })),
    openTextDocument: vi.fn().mockResolvedValue({}),
  },
  ViewColumn: {
    Beside: 2,
  },
}));

// Mock notification settings
vi.mock("../../src/config/notificationSettings", () => ({
  getNotificationSettings: vi.fn(),
  getSoundForType: vi.fn(),
}));

// Import after mocks are set up
import { NotificationService } from "../../src/services/NotificationService";
import { spawn, exec } from "child_process";
import * as vscode from "vscode";
import { getNotificationSettings, getSoundForType } from "../../src/config/notificationSettings";

// Default notification settings for tests
function createDefaultSettings(
  overrides: Partial<NotificationSettings> = {}
): NotificationSettings {
  return {
    enabled: true,
    sounds: {
      enabled: true,
      alert: "Glass",
      success: "Hero",
      error: "Basso",
      volume: 0.5,
    },
    banner: {
      enabled: true,
    },
    dockBounce: {
      enabled: true,
    },
    respectDoNotDisturb: true,
    events: ["pipeline.completed", "pipeline.failed", "issue.assigned", "pr.review_requested"],
    ...overrides,
  };
}

describe("NotificationService", () => {
  let service: NotificationService;

  beforeEach(() => {
    vi.clearAllMocks();

    // Reset platform to macOS for most tests
    mockPlatform("darwin");

    // Default settings - DND check returns false (not in DND mode)
    vi.mocked(getNotificationSettings).mockReturnValue(createDefaultSettings());
    vi.mocked(getSoundForType).mockReturnValue("Glass");

    // Mock batch config
    vi.mocked(vscode.workspace.getConfiguration).mockReturnValue({
      get: vi.fn((_key: string, defaultValue: unknown) => defaultValue),
      has: vi.fn(),
      inspect: vi.fn(),
      update: vi.fn(),
    } as unknown as vscode.WorkspaceConfiguration);

    service = new NotificationService();
  });

  afterEach(() => {
    service.dispose();
    restorePlatform();
  });

  describe("constructor", () => {
    it("should detect macOS platform", () => {
      mockPlatform("darwin");
      const macService = new NotificationService();
      expect(macService).toBeDefined();
      macService.dispose();
    });

    it("should detect non-macOS platform", () => {
      mockPlatform("linux");
      const linuxService = new NotificationService();
      expect(linuxService).toBeDefined();
      linuxService.dispose();
    });

    it("should initialize debounce state", () => {
      const newService = new NotificationService();
      expect(newService).toBeDefined();
      newService.dispose();
    });
  });

  describe("notifyUserInputNeeded", () => {
    it("should show warning notification with stage and issue number", async () => {
      await service.notifyUserInputNeeded("feature-planning", 42);

      expect(vscode.window.showWarningMessage).toHaveBeenCalledWith(
        "Nightgauge: Approval needed for #42 (feature-planning)",
        "Review Plan"
      );
    });

    it("should play alert sound on macOS", async () => {
      await service.notifyUserInputNeeded("feature-planning", 42);

      expect(spawn).toHaveBeenCalledWith("afplay", [
        "-v",
        "0.5",
        "/System/Library/Sounds/Glass.aiff",
      ]);
    });

    it("should execute viewContext command when Review Plan is clicked", async () => {
      vi.mocked(vscode.window.showWarningMessage).mockResolvedValue(
        "Review Plan" as unknown as string | undefined
      );

      await service.notifyUserInputNeeded("feature-planning", 42);

      // Allow promise to settle
      await new Promise((resolve) => setTimeout(resolve, 10));

      expect(vscode.commands.executeCommand).toHaveBeenCalledWith("nightgauge.viewContext");
    });

    it("should request dock bounce on macOS when enabled", async () => {
      await service.notifyUserInputNeeded("feature-planning", 42);

      expect(exec).toHaveBeenCalledWith(expect.stringContaining("osascript"), expect.any(Function));
    });

    it("should not show notification when disabled", async () => {
      vi.mocked(getNotificationSettings).mockReturnValue(createDefaultSettings({ enabled: false }));

      await service.notifyUserInputNeeded("feature-planning", 42);

      expect(vscode.window.showWarningMessage).not.toHaveBeenCalled();
    });

    it("should not show banner when banner is disabled", async () => {
      vi.mocked(getNotificationSettings).mockReturnValue(
        createDefaultSettings({ banner: { enabled: false } })
      );

      await service.notifyUserInputNeeded("feature-planning", 42);

      expect(vscode.window.showWarningMessage).not.toHaveBeenCalled();
    });

    it("should not request dock bounce when dockBounce is disabled", async () => {
      vi.mocked(getNotificationSettings).mockReturnValue(
        createDefaultSettings({ dockBounce: { enabled: false } })
      );

      await service.notifyUserInputNeeded("feature-planning", 42);

      expect(exec).not.toHaveBeenCalled();
    });

    it("should not request dock bounce on non-macOS", async () => {
      mockPlatform("linux");
      const linuxService = new NotificationService();

      await linuxService.notifyUserInputNeeded("feature-planning", 42);

      expect(exec).not.toHaveBeenCalled();
      linuxService.dispose();
    });
  });

  describe("notifyPipelineComplete", () => {
    it("should show success notification with issue number", async () => {
      await service.notifyPipelineComplete(42);

      expect(vscode.window.showInformationMessage).toHaveBeenCalledWith(
        "Nightgauge: Pipeline complete for #42",
        "View Dashboard"
      );
    });

    it("should play success sound", async () => {
      vi.mocked(getSoundForType).mockReturnValue("Hero");

      await service.notifyPipelineComplete(42);

      expect(spawn).toHaveBeenCalledWith("afplay", [
        "-v",
        "0.5",
        "/System/Library/Sounds/Hero.aiff",
      ]);
    });

    it("should execute showDashboard command when View Dashboard is clicked", async () => {
      vi.mocked(vscode.window.showInformationMessage).mockResolvedValue(
        "View Dashboard" as unknown as string | undefined
      );

      await service.notifyPipelineComplete(42);

      // Allow promise to settle
      await new Promise((resolve) => setTimeout(resolve, 10));

      expect(vscode.commands.executeCommand).toHaveBeenCalledWith("nightgauge.showDashboard");
    });

    it("should not show notification when disabled", async () => {
      vi.mocked(getNotificationSettings).mockReturnValue(createDefaultSettings({ enabled: false }));

      await service.notifyPipelineComplete(42);

      expect(vscode.window.showInformationMessage).not.toHaveBeenCalled();
    });
  });

  describe("notifyPipelineError", () => {
    it("should show error notification with stage and error message", async () => {
      await service.notifyPipelineError("feature-dev", "Build failed");

      expect(vscode.window.showErrorMessage).toHaveBeenCalledWith(
        "Nightgauge: Error in feature-dev: Build failed",
        "View Output"
      );
    });

    it("should truncate long error messages", async () => {
      const longError = "A".repeat(150);

      await service.notifyPipelineError("feature-dev", longError);

      const call = vi.mocked(vscode.window.showErrorMessage).mock.calls[0];
      expect(call[0]).toContain("...");
      expect((call[0] as string).length).toBeLessThan(200);
    });

    it("should play error sound", async () => {
      vi.mocked(getSoundForType).mockReturnValue("Basso");

      await service.notifyPipelineError("feature-dev", "Build failed");

      expect(spawn).toHaveBeenCalledWith("afplay", [
        "-v",
        "0.5",
        "/System/Library/Sounds/Basso.aiff",
      ]);
    });

    it("should execute showOutputWindow command when View Output is clicked", async () => {
      vi.mocked(vscode.window.showErrorMessage).mockResolvedValue(
        "View Output" as unknown as string | undefined
      );

      await service.notifyPipelineError("feature-dev", "Build failed");

      // Allow promise to settle
      await new Promise((resolve) => setTimeout(resolve, 10));

      expect(vscode.commands.executeCommand).toHaveBeenCalledWith("nightgauge.showOutputWindow");
    });

    it("should request dock bounce for errors", async () => {
      await service.notifyPipelineError("feature-dev", "Build failed");

      expect(exec).toHaveBeenCalledWith(expect.stringContaining("osascript"), expect.any(Function));
    });
  });

  describe("notifyBatchProgress", () => {
    it("should not notify when notifyOnEachIssue is disabled", async () => {
      vi.mocked(vscode.workspace.getConfiguration).mockReturnValue({
        get: vi.fn((key: string, defaultValue: unknown) => {
          if (key === "notifyOnEachIssue") return false;
          return defaultValue;
        }),
        has: vi.fn(),
        inspect: vi.fn(),
        update: vi.fn(),
      } as unknown as vscode.WorkspaceConfiguration);

      await service.notifyBatchProgress(42, 1, 5);

      expect(vscode.window.showInformationMessage).not.toHaveBeenCalled();
    });

    it("should show progress notification when enabled", async () => {
      vi.mocked(vscode.workspace.getConfiguration).mockReturnValue({
        get: vi.fn((key: string, defaultValue: unknown) => {
          if (key === "notifyOnEachIssue") return true;
          return defaultValue;
        }),
        has: vi.fn(),
        inspect: vi.fn(),
        update: vi.fn(),
      } as unknown as vscode.WorkspaceConfiguration);

      await service.notifyBatchProgress(42, 2, 5);

      expect(vscode.window.showInformationMessage).toHaveBeenCalledWith(
        "Nightgauge: Batch progress 2/5 (#42 complete)"
      );
    });

    it("should include metrics in notification when provided", async () => {
      vi.mocked(vscode.workspace.getConfiguration).mockReturnValue({
        get: vi.fn((key: string, defaultValue: unknown) => {
          if (key === "notifyOnEachIssue") return true;
          return defaultValue;
        }),
        has: vi.fn(),
        inspect: vi.fn(),
        update: vi.fn(),
      } as unknown as vscode.WorkspaceConfiguration);

      await service.notifyBatchProgress(42, 2, 5, {
        tokens: 5000,
        cost: 0.25,
        time: 60,
      });

      expect(vscode.window.showInformationMessage).toHaveBeenCalledWith(
        expect.stringContaining("$0.25")
      );
    });
  });

  describe("notifyBatchComplete", () => {
    it("should not notify when notifyOnComplete is disabled", async () => {
      vi.mocked(vscode.workspace.getConfiguration).mockReturnValue({
        get: vi.fn((key: string, defaultValue: unknown) => {
          if (key === "notifyOnComplete") return false;
          return defaultValue;
        }),
        has: vi.fn(),
        inspect: vi.fn(),
        update: vi.fn(),
      } as unknown as vscode.WorkspaceConfiguration);

      await service.notifyBatchComplete({
        totalIssues: 5,
        successfulIssues: 5,
        failedIssues: 0,
        skippedIssues: 0,
      });

      expect(vscode.window.showInformationMessage).not.toHaveBeenCalled();
    });

    it("should show completion notification with success count", async () => {
      await service.notifyBatchComplete({
        totalIssues: 5,
        successfulIssues: 4,
        failedIssues: 1,
        skippedIssues: 0,
      });

      expect(vscode.window.showWarningMessage).toHaveBeenCalledWith(
        expect.stringContaining("4/5 succeeded"),
        expect.any(String),
        expect.any(String)
      );
    });

    it("should show failed count when there are failures", async () => {
      await service.notifyBatchComplete({
        totalIssues: 5,
        successfulIssues: 3,
        failedIssues: 2,
        skippedIssues: 0,
      });

      expect(vscode.window.showWarningMessage).toHaveBeenCalledWith(
        expect.stringContaining("2 failed"),
        expect.any(String),
        expect.any(String)
      );
    });

    it("should show skipped count when there are skipped issues", async () => {
      await service.notifyBatchComplete({
        totalIssues: 5,
        successfulIssues: 3,
        failedIssues: 1,
        skippedIssues: 1,
      });

      expect(vscode.window.showWarningMessage).toHaveBeenCalledWith(
        expect.stringContaining("1 skipped"),
        expect.any(String),
        expect.any(String)
      );
    });

    it("should use information message when no failures", async () => {
      await service.notifyBatchComplete({
        totalIssues: 5,
        successfulIssues: 5,
        failedIssues: 0,
        skippedIssues: 0,
      });

      expect(vscode.window.showInformationMessage).toHaveBeenCalled();
      expect(vscode.window.showWarningMessage).not.toHaveBeenCalled();
    });

    it("should play error sound when there are failures", async () => {
      vi.mocked(getSoundForType).mockReturnValue("Basso");

      await service.notifyBatchComplete({
        totalIssues: 5,
        successfulIssues: 3,
        failedIssues: 2,
        skippedIssues: 0,
      });

      expect(getSoundForType).toHaveBeenCalledWith(expect.any(Object), "error");
    });

    it("should play success sound when no failures", async () => {
      vi.mocked(getSoundForType).mockReturnValue("Hero");

      await service.notifyBatchComplete({
        totalIssues: 5,
        successfulIssues: 5,
        failedIssues: 0,
        skippedIssues: 0,
      });

      expect(getSoundForType).toHaveBeenCalledWith(expect.any(Object), "success");
    });
  });

  describe("Sound playback", () => {
    it("should not play sound when sounds are disabled", async () => {
      vi.mocked(getNotificationSettings).mockReturnValue(
        createDefaultSettings({
          sounds: {
            enabled: false,
            alert: "Glass",
            success: "Hero",
            error: "Basso",
            volume: 0.5,
          },
        })
      );
      vi.mocked(getSoundForType).mockReturnValue(null);

      await service.notifyPipelineComplete(42);

      expect(spawn).not.toHaveBeenCalled();
    });

    it("should not play sound when sound is set to none", async () => {
      vi.mocked(getSoundForType).mockReturnValue(null);

      await service.notifyPipelineComplete(42);

      expect(spawn).not.toHaveBeenCalled();
    });

    it("should not play sound on non-macOS platforms", async () => {
      mockPlatform("win32");
      const winService = new NotificationService();

      await winService.notifyPipelineComplete(42);

      expect(spawn).not.toHaveBeenCalled();
      winService.dispose();
    });

    it("should handle sound playback errors gracefully", async () => {
      vi.mocked(spawn).mockReturnValue({
        on: vi.fn((event: string, callback: (code?: number) => void) => {
          if (event === "error") {
            callback();
          }
          return { on: vi.fn() };
        }),
      } as any);

      await expect(service.notifyPipelineComplete(42)).resolves.toBeUndefined();
    });
  });

  describe("Do Not Disturb detection", () => {
    it("should not suppress notifications when respectDoNotDisturb is false", async () => {
      vi.mocked(getNotificationSettings).mockReturnValue(
        createDefaultSettings({ respectDoNotDisturb: false })
      );

      await service.notifyPipelineComplete(42);

      expect(vscode.window.showInformationMessage).toHaveBeenCalled();
    });

    it("should not check DND on non-macOS platforms", async () => {
      mockPlatform("linux");
      const linuxService = new NotificationService();

      await linuxService.notifyPipelineComplete(42);

      expect(vscode.window.showInformationMessage).toHaveBeenCalled();
      linuxService.dispose();
    });
  });

  describe("dispose", () => {
    it("should clear debounce state", () => {
      expect(() => service.dispose()).not.toThrow();
    });

    it("should allow multiple dispose calls", () => {
      service.dispose();
      expect(() => service.dispose()).not.toThrow();
    });
  });

  describe("Debounce behavior", () => {
    it("should track different notification types independently", async () => {
      // First call for alert type
      await service.notifyUserInputNeeded("feature-planning", 42);

      // Second call for success type - different type so should work
      await service.notifyPipelineComplete(42);

      expect(vscode.window.showWarningMessage).toHaveBeenCalledTimes(1);
      expect(vscode.window.showInformationMessage).toHaveBeenCalledTimes(1);
    });

    it("should reset debounce after dispose and new instance", async () => {
      await service.notifyPipelineComplete(42);

      service.dispose();
      service = new NotificationService();

      await service.notifyPipelineComplete(43);

      expect(vscode.window.showInformationMessage).toHaveBeenCalledTimes(2);
    });
  });

  describe("Platform-specific behavior", () => {
    it("should handle darwin platform correctly", async () => {
      mockPlatform("darwin");
      const macService = new NotificationService();

      await macService.notifyUserInputNeeded("feature-planning", 42);

      expect(spawn).toHaveBeenCalled();
      expect(exec).toHaveBeenCalled();

      macService.dispose();
    });

    it("should handle linux platform correctly", async () => {
      mockPlatform("linux");
      const linuxService = new NotificationService();

      await linuxService.notifyUserInputNeeded("feature-planning", 42);

      expect(spawn).not.toHaveBeenCalled();
      expect(exec).not.toHaveBeenCalled();

      linuxService.dispose();
    });

    it("should handle win32 platform correctly", async () => {
      mockPlatform("win32");
      const winService = new NotificationService();

      await winService.notifyUserInputNeeded("feature-planning", 42);

      expect(spawn).not.toHaveBeenCalled();
      expect(exec).not.toHaveBeenCalled();

      winService.dispose();
    });
  });
});
