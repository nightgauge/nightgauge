/**
 * Sign In / Sign Out command unit tests.
 *
 * Tests that signIn shows a quick pick and delegates to the correct service,
 * and that signOut delegates to OAuthDeviceFlowService.
 *
 * @see Issue #1467 - Add GitHub Sign-in as alternative auth path
 * @see Issue #1464 - Implement OAuth Device Flow login command and UI
 */

import { describe, it, expect, vi, beforeEach } from "vitest";

// Mock vscode before imports
vi.mock("vscode", () => {
  const registeredCommands = new Map<string, (...args: unknown[]) => unknown>();
  return {
    commands: {
      registerCommand: vi.fn((id: string, handler: (...args: unknown[]) => unknown) => {
        registeredCommands.set(id, handler);
        return { dispose: vi.fn() };
      }),
      _getHandler: (id: string) => registeredCommands.get(id),
    },
    window: {
      showInformationMessage: vi.fn().mockResolvedValue(undefined),
      showErrorMessage: vi.fn(),
      showQuickPick: vi.fn(),
    },
    Disposable: { from: vi.fn() },
  };
});

import * as vscode from "vscode";
import { registerSignInCommand } from "../../src/commands/signIn";
import { registerSignOutCommand } from "../../src/commands/signOut";
import type { OAuthDeviceFlowService } from "../../src/services/OAuthDeviceFlowService";
import type { GitHubAuthService } from "../../src/services/GitHubAuthService";
import type { Logger } from "../../src/utils/logger";

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

function createMockOAuthService() {
  return {
    startDeviceFlow: vi.fn().mockResolvedValue(undefined),
    signOut: vi.fn().mockResolvedValue(undefined),
  } as unknown as OAuthDeviceFlowService;
}

function createMockGitHubAuthService() {
  return {
    signInWithGitHub: vi.fn().mockResolvedValue(true),
  } as unknown as GitHubAuthService;
}

function createMockLogger(): Logger {
  return {
    info: vi.fn(),
    warn: vi.fn(),
    error: vi.fn(),
    debug: vi.fn(),
  } as unknown as Logger;
}

/** Returns a QuickPickItem matching "GitHub" (first option). */
function pickGitHub() {
  return {
    label: "$(mark-github) GitHub",
    description: "Sign in using your GitHub account (recommended)",
    method: "github",
  };
}

/** Returns a QuickPickItem matching "Device Flow" (second option). */
function pickDeviceFlow() {
  return {
    label: "$(device-desktop) Device Flow",
    description: "Sign in with a browser code (works without GitHub in VSCode)",
    method: "device-flow",
  };
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

describe("registerSignInCommand", () => {
  let oauthService: ReturnType<typeof createMockOAuthService>;
  let gitHubAuthService: ReturnType<typeof createMockGitHubAuthService>;
  let logger: Logger;

  beforeEach(() => {
    oauthService = createMockOAuthService();
    gitHubAuthService = createMockGitHubAuthService();
    logger = createMockLogger();
    vi.mocked(vscode.window.showErrorMessage).mockReset();
    vi.mocked(vscode.window.showQuickPick).mockReset();
  });

  it("registers the nightgauge.signIn command", () => {
    registerSignInCommand(oauthService, gitHubAuthService, logger);

    expect(vscode.commands.registerCommand).toHaveBeenCalledWith(
      "nightgauge.signIn",
      expect.any(Function)
    );
  });

  it("user selects GitHub: calls gitHubAuthService.signInWithGitHub, NOT startDeviceFlow", async () => {
    vi.mocked(vscode.window.showQuickPick).mockResolvedValue(pickGitHub() as any);
    registerSignInCommand(oauthService, gitHubAuthService, logger);

    const handler = (vscode.commands as any)._getHandler("nightgauge.signIn");
    await handler();

    expect(gitHubAuthService.signInWithGitHub).toHaveBeenCalledOnce();
    expect(oauthService.startDeviceFlow).not.toHaveBeenCalled();
  });

  it("user selects Device Flow: calls oauthService.startDeviceFlow, NOT signInWithGitHub", async () => {
    vi.mocked(vscode.window.showQuickPick).mockResolvedValue(pickDeviceFlow() as any);
    registerSignInCommand(oauthService, gitHubAuthService, logger);

    const handler = (vscode.commands as any)._getHandler("nightgauge.signIn");
    await handler();

    expect(oauthService.startDeviceFlow).toHaveBeenCalledOnce();
    expect(gitHubAuthService.signInWithGitHub).not.toHaveBeenCalled();
  });

  it("user dismisses quick pick (undefined): neither service is called", async () => {
    vi.mocked(vscode.window.showQuickPick).mockResolvedValue(undefined as any);
    registerSignInCommand(oauthService, gitHubAuthService, logger);

    const handler = (vscode.commands as any)._getHandler("nightgauge.signIn");
    await handler();

    expect(gitHubAuthService.signInWithGitHub).not.toHaveBeenCalled();
    expect(oauthService.startDeviceFlow).not.toHaveBeenCalled();
  });

  it("signInWithGitHub throws: shows error message without re-throwing", async () => {
    vi.mocked(vscode.window.showQuickPick).mockResolvedValue(pickGitHub() as any);
    vi.mocked(gitHubAuthService.signInWithGitHub).mockRejectedValue(
      new Error("Unexpected failure")
    );
    registerSignInCommand(oauthService, gitHubAuthService, logger);

    const handler = (vscode.commands as any)._getHandler("nightgauge.signIn");
    await handler(); // should not throw

    expect(logger.error).toHaveBeenCalledWith("Sign-in failed", expect.any(Error));
    expect(vscode.window.showErrorMessage).toHaveBeenCalledWith(
      expect.stringContaining("Unexpected failure")
    );
  });

  it("startDeviceFlow throws: shows error message without re-throwing", async () => {
    vi.mocked(vscode.window.showQuickPick).mockResolvedValue(pickDeviceFlow() as any);
    vi.mocked(oauthService.startDeviceFlow).mockRejectedValue(new Error("Connection timeout"));
    registerSignInCommand(oauthService, gitHubAuthService, logger);

    const handler = (vscode.commands as any)._getHandler("nightgauge.signIn");
    await handler(); // should not throw

    expect(logger.error).toHaveBeenCalledWith("Sign-in failed", expect.any(Error));
    expect(vscode.window.showErrorMessage).toHaveBeenCalledWith(
      expect.stringContaining("Connection timeout")
    );
  });
});

describe("registerSignOutCommand", () => {
  let oauthService: ReturnType<typeof createMockOAuthService>;
  let logger: Logger;

  beforeEach(() => {
    oauthService = createMockOAuthService();
    logger = createMockLogger();
    vi.mocked(vscode.window.showInformationMessage).mockReset().mockResolvedValue(undefined);
    vi.mocked(vscode.window.showErrorMessage).mockReset();
  });

  it("registers the nightgauge.signOut command", () => {
    registerSignOutCommand(oauthService, logger);

    expect(vscode.commands.registerCommand).toHaveBeenCalledWith(
      "nightgauge.signOut",
      expect.any(Function)
    );
  });

  it("delegates to oauthService.signOut() and shows confirmation", async () => {
    registerSignOutCommand(oauthService, logger);

    const handler = (vscode.commands as any)._getHandler("nightgauge.signOut");
    await handler();

    expect(oauthService.signOut).toHaveBeenCalledOnce();
    expect(vscode.window.showInformationMessage).toHaveBeenCalledWith("Nightgauge: Signed out.");
  });

  it("shows error message on failure without re-throwing", async () => {
    oauthService.signOut = vi.fn().mockRejectedValue(new Error("Storage error"));
    registerSignOutCommand(oauthService, logger);

    const handler = (vscode.commands as any)._getHandler("nightgauge.signOut");
    await handler(); // should not throw

    expect(logger.error).toHaveBeenCalledWith("Sign-out failed", expect.any(Error));
    expect(vscode.window.showErrorMessage).toHaveBeenCalledWith(
      expect.stringContaining("Storage error")
    );
  });
});
