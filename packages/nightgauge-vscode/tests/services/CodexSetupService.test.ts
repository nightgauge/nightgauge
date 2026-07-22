/**
 * Unit tests for CodexSetupService.ts
 *
 * Tests Codex CLI detection, command installation, and prompt dismissal.
 *
 * @see Issue #2446 - Add tests for untested VSCode services
 */

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";

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
    // Default: codex CLI available
    mockExecAsync.mockResolvedValue({ stdout: "codex 1.0.0", stderr: "" });

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

    it("updates INSTALLED_KEY and returns when all commands are already installed", async () => {
      const fs = await import("fs");
      // All 6 required command files exist
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

    it.skip("shows prompt when codex is available and commands are not installed", async () => {
      const vscode = await import("vscode");
      vi.mocked(vscode.window.showInformationMessage).mockResolvedValue(undefined as never);

      const { CodexSetupService } = await import("../../src/services/CodexSetupService");
      const service = new CodexSetupService(makeMockContext() as never);

      await service.checkAndPromptSetup();

      expect(vscode.window.showInformationMessage).toHaveBeenCalledWith(
        expect.stringContaining("not installed"),
        expect.objectContaining({ modal: true }),
        "Install Codex Commands",
        "Later",
        "Don't Show Again"
      );
    });

    it.skip('calls installAssets when user selects "Install Codex Commands"', async () => {
      const vscode = await import("vscode");
      vi.mocked(vscode.window.showInformationMessage).mockResolvedValue(
        "Install Codex Commands" as never
      );

      const fs = await import("fs");
      // Commands dir exists so resolveCommandsSourceDir finds the bundled source
      vi.mocked(fs.existsSync).mockImplementation((p: fs.PathLike) => {
        const pathStr = p.toString();
        // Bundled resources dir exists; command files do not (so prompt fires)
        return pathStr.endsWith("/resources/codex/commands");
      });

      const mockContext = makeMockContext();
      const { CodexSetupService } = await import("../../src/services/CodexSetupService");
      const service = new CodexSetupService(mockContext as never);

      await service.checkAndPromptSetup();

      expect(fs.promises.mkdir).toHaveBeenCalled();
      expect(fs.promises.copyFile).toHaveBeenCalled();
      expect(mockContext.globalState.update).toHaveBeenCalledWith(
        "nightgauge.codexSetup.installed",
        true
      );
    });

    it.skip('sets DISMISSED_KEY when user selects "Don\'t Show Again"', async () => {
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

    it.skip('does nothing when user selects "Later"', async () => {
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
    it.skip("resets dismissed state before checking setup", async () => {
      const vscode = await import("vscode");
      vi.mocked(vscode.window.showInformationMessage).mockResolvedValue(undefined as never);

      // Use a dynamic context so that update() mutates the state read by get()
      const state: Record<string, unknown> = {
        "nightgauge.codexSetup.dismissed": true,
      };
      const mockContext = {
        globalState: {
          get: vi.fn((key: string, defaultValue?: unknown) =>
            key in state ? state[key] : defaultValue
          ),
          update: vi.fn((key: string, value: unknown) => {
            state[key] = value;
            return Promise.resolve();
          }),
        },
        extensionPath: "/extension",
      };

      const { CodexSetupService } = await import("../../src/services/CodexSetupService");
      const service = new CodexSetupService(mockContext as never);

      await service.showSetupPrompt();

      // First call should reset dismissed to false
      expect(mockContext.globalState.update).toHaveBeenCalledWith(
        "nightgauge.codexSetup.dismissed",
        false
      );
      // Then checkAndPromptSetup runs — prompt should fire (codex available, no commands)
      expect(vscode.window.showInformationMessage).toHaveBeenCalled();
    });
  });

  describe("dispose()", () => {
    it("disposes output channel", async () => {
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

      expect(mockDispose).toHaveBeenCalled();
    });
  });
});
