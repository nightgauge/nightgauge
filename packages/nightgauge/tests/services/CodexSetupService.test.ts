/**
 * Unit tests for CodexSetupService.ts
 *
 * Tests Codex CLI detection, skill install prompts, and prompt dismissal.
 *
 * @see Issue #2446 - Add tests for untested VSCode services
 */

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { resetMainChannelForTests } from "../../src/utils/logger";

// ── hoisted mocks ──────────────────────────────────────────────────────────
const mockExecAsync = vi.hoisted(() => vi.fn());

vi.mock("vscode", () => ({
  window: {
    createOutputChannel: vi.fn(() => ({
      show: vi.fn(),
      appendLine: vi.fn(),
      dispose: vi.fn(),
    })),
    showInformationMessage: vi.fn(),
    showErrorMessage: vi.fn(),
  },
  workspace: {
    getConfiguration: vi.fn(() => ({
      get: vi.fn().mockReturnValue(true),
    })),
    workspaceFolders: [{ uri: { fsPath: "/workspace" } }],
  },
}));

vi.mock("child_process", () => ({
  exec: vi.fn(),
}));

vi.mock("util", () => ({
  promisify: vi.fn(() => mockExecAsync),
}));

vi.mock("fs", () => ({
  existsSync: vi.fn().mockReturnValue(false),
  promises: {
    mkdir: vi.fn().mockResolvedValue(undefined),
    copyFile: vi.fn().mockResolvedValue(undefined),
    readdir: vi.fn().mockResolvedValue([]),
    rm: vi.fn().mockResolvedValue(undefined),
    cp: vi.fn().mockResolvedValue(undefined),
  },
}));

// ── helpers ────────────────────────────────────────────────────────────────

function makeMockContext(overrides?: { dismissed?: boolean; installed?: boolean }) {
  return {
    globalState: {
      get: vi.fn((key: string, defaultValue?: unknown) => {
        if (key === "nightgauge.codexSetup.dismissed") {
          return overrides?.dismissed ?? defaultValue ?? false;
        }
        if (key === "nightgauge.codexSetup.installed") {
          return overrides?.installed ?? defaultValue ?? false;
        }
        return defaultValue;
      }),
      update: vi.fn().mockResolvedValue(undefined),
    },
    extensionPath: "/extension",
  };
}

// ── tests ──────────────────────────────────────────────────────────────────

describe("CodexSetupService", () => {
  beforeEach(async () => {
    vi.clearAllMocks();
    // CodexSetupService now shares the memoized main "Nightgauge" channel
    // (#749) — drop it between tests so each one sees its own mocked
    // createOutputChannel() rather than a channel cached by an earlier test.
    resetMainChannelForTests();
    // Default: codex CLI available
    mockExecAsync.mockResolvedValue({ stdout: "codex 1.0.0", stderr: "" });
    // Default: nothing installed. The fs mock is module-wide, so a test that
    // marks the skills installed must not leak into the next one.
    const fs = await import("fs");
    vi.mocked(fs.existsSync).mockReturnValue(false);

    // Reset vscode mocks to defaults
    const vscode = await import("vscode");
    vi.mocked(vscode.workspace.getConfiguration).mockReturnValue({
      get: vi.fn().mockReturnValue(true),
    } as never);
  });

  afterEach(() => {
    vi.restoreAllMocks();
  });

  describe("checkAndPromptSetup()", () => {
    it("returns early when autoPrompt config is false", async () => {
      const vscode = await import("vscode");
      vi.mocked(vscode.workspace.getConfiguration).mockReturnValue({
        get: vi.fn().mockReturnValue(false),
      } as never);

      const { CodexSetupService } = await import("../../src/services/CodexSetupService");
      const service = new CodexSetupService(makeMockContext() as never);

      await service.checkAndPromptSetup();

      expect(vscode.window.showInformationMessage).not.toHaveBeenCalled();
    });

    it("returns early when dismissed=true in globalState", async () => {
      const vscode = await import("vscode");

      const { CodexSetupService } = await import("../../src/services/CodexSetupService");
      const service = new CodexSetupService(makeMockContext({ dismissed: true }) as never);

      await service.checkAndPromptSetup();

      expect(vscode.window.showInformationMessage).not.toHaveBeenCalled();
    });

    it("returns silently when codex CLI is not available", async () => {
      const vscode = await import("vscode");
      mockExecAsync.mockRejectedValue(new Error("command not found: codex"));

      const { CodexSetupService } = await import("../../src/services/CodexSetupService");
      const service = new CodexSetupService(makeMockContext() as never);

      await service.checkAndPromptSetup();

      expect(vscode.window.showInformationMessage).not.toHaveBeenCalled();
    });

    it("updates INSTALLED_KEY and returns when the skills are already installed", async () => {
      const fs = await import("fs");
      vi.mocked(fs.existsSync).mockReturnValue(true);

      const mockContext = makeMockContext();
      const { CodexSetupService } = await import("../../src/services/CodexSetupService");
      const service = new CodexSetupService(mockContext as never);

      await service.checkAndPromptSetup();

      expect(mockContext.globalState.update).toHaveBeenCalledWith(
        "nightgauge.codexSetup.installed",
        true
      );

      const vscode = await import("vscode");
      expect(vscode.window.showInformationMessage).not.toHaveBeenCalled();
    });

    it("shows prompt when codex is available and skills are not installed", async () => {
      const vscode = await import("vscode");
      vi.mocked(vscode.window.showInformationMessage).mockResolvedValue(undefined as never);

      const { CodexSetupService } = await import("../../src/services/CodexSetupService");
      const service = new CodexSetupService(makeMockContext() as never);

      await service.checkAndPromptSetup();

      expect(vscode.window.showInformationMessage).toHaveBeenCalledWith(
        expect.stringContaining("not installed"),
        expect.objectContaining({ modal: true }),
        "Install Codex Skills",
        "Later",
        "Don't Show Again"
      );
    });

    it('sets DISMISSED_KEY when user selects "Don\'t Show Again"', async () => {
      const vscode = await import("vscode");
      vi.mocked(vscode.window.showInformationMessage).mockResolvedValue(
        "Don't Show Again" as never
      );

      const mockContext = makeMockContext();
      const { CodexSetupService } = await import("../../src/services/CodexSetupService");
      const service = new CodexSetupService(mockContext as never);

      await service.checkAndPromptSetup();

      expect(mockContext.globalState.update).toHaveBeenCalledWith(
        "nightgauge.codexSetup.dismissed",
        true
      );
    });

    it('does nothing when user selects "Later"', async () => {
      const vscode = await import("vscode");
      vi.mocked(vscode.window.showInformationMessage).mockResolvedValue("Later" as never);

      const mockContext = makeMockContext();
      const { CodexSetupService } = await import("../../src/services/CodexSetupService");
      const service = new CodexSetupService(mockContext as never);

      await service.checkAndPromptSetup();

      expect(mockContext.globalState.update).not.toHaveBeenCalled();
    });
  });

  describe("showSetupPrompt()", () => {
    it("resets dismissed and offers an install even with autoPrompt off (#2762)", async () => {
      const vscode = await import("vscode");
      vi.mocked(vscode.workspace.getConfiguration).mockReturnValue({
        get: vi.fn().mockReturnValue(false),
      } as never);
      vi.mocked(vscode.window.showInformationMessage).mockResolvedValue(undefined as never);

      const mockContext = makeMockContext({ dismissed: true });
      const { CodexSetupService } = await import("../../src/services/CodexSetupService");
      await new CodexSetupService(mockContext as never).showSetupPrompt();

      expect(mockContext.globalState.update).toHaveBeenCalledWith(
        "nightgauge.codexSetup.dismissed",
        false
      );
      expect(vscode.window.showInformationMessage).toHaveBeenCalledWith(
        expect.stringContaining("not installed"),
        expect.objectContaining({ modal: true }),
        "Install Codex Skills",
        "Later",
        "Don't Show Again"
      );
    });

    it("offers an update when the skills are already installed", async () => {
      const vscode = await import("vscode");
      const fs = await import("fs");
      vi.mocked(fs.existsSync).mockReturnValue(true);
      vi.mocked(vscode.window.showInformationMessage).mockResolvedValue(undefined as never);

      const { CodexSetupService } = await import("../../src/services/CodexSetupService");
      await new CodexSetupService(makeMockContext() as never).showSetupPrompt();

      expect(vscode.window.showInformationMessage).toHaveBeenCalledWith(
        expect.stringContaining("Update"),
        expect.objectContaining({ modal: true }),
        "Update Codex Skills",
        "Later",
        "Don't Show Again"
      );
    });

    it("says the CLI is missing instead of prompting", async () => {
      const vscode = await import("vscode");
      mockExecAsync.mockRejectedValue(new Error("command not found: codex"));

      const { CodexSetupService } = await import("../../src/services/CodexSetupService");
      await new CodexSetupService(makeMockContext() as never).showSetupPrompt();

      expect(vscode.window.showInformationMessage).toHaveBeenCalledTimes(1);
      expect(vscode.window.showInformationMessage).toHaveBeenCalledWith(
        expect.stringContaining("`codex` CLI was not found")
      );
    });
  });

  describe("dispose()", () => {
    it("does not tear down the shared Nightgauge channel it no longer owns (#749)", async () => {
      const vscode = await import("vscode");
      const mockDispose = vi.fn();
      vi.mocked(vscode.window.createOutputChannel).mockReturnValue({
        show: vi.fn(),
        appendLine: vi.fn(),
        dispose: mockDispose,
      } as never);

      const { CodexSetupService } = await import("../../src/services/CodexSetupService");
      const service = new CodexSetupService(makeMockContext() as never);
      service.dispose();

      // Codex Setup is folded into the shared main channel behind a
      // "codex-setup" prefix — disposing this service must not kill the
      // channel every other subsystem still writes to.
      expect(mockDispose).not.toHaveBeenCalled();
    });
  });
});
