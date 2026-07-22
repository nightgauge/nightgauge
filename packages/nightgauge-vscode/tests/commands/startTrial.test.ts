/**
 * Tests for the Start Free Trial command.
 *
 * @see Issue #1138 - Commercialization: in-extension free trial
 */

import { describe, it, expect, beforeEach, vi } from "vitest";
import * as vscode from "vscode";
import { registerStartTrialCommand } from "../../src/commands/startTrial";
import { IpcClient } from "../../src/services/IpcClient";
import { SecretStorageService, SECRET_KEYS } from "../../src/services/SecretStorageService";
import type { SessionManager } from "../../src/platform/SessionManager";
import type { LicensePreflight } from "../../src/platform/LicensePreflight";
import type { IOnDemandTokenRefresher } from "../../src/platform/TokenRefreshManager";
import type { Logger } from "../../src/utils/logger";

vi.mock("../../src/services/IpcClient", () => ({
  IpcClient: { getInstance: vi.fn() },
}));

vi.mock("../../src/services/SecretStorageService", () => ({
  SecretStorageService: { getInstance: vi.fn() },
  SECRET_KEYS: { platformLicenseKey: "nightgauge.platform.licenseKey" },
}));

const TRIAL_RESULT = {
  licenseKey: "IB-TRIAL-AAAA-BBBB",
  tier: "pro",
  trial: true,
  expiresAt: "2026-07-12T00:00:00.000Z",
  runAllowance: 50,
};

describe("startTrial", () => {
  const platformStartTrial = vi.fn();
  const setSecret = vi.fn().mockResolvedValue(undefined);
  const clearCache = vi.fn();
  const isAuthenticated = vi.fn();
  const getAccessToken = vi.fn();
  const forceRefresh = vi.fn();
  let logger: Logger;

  const sessionManager = {
    isAuthenticated,
    getAccessToken,
  } as unknown as SessionManager;
  const licensePreflight = { clearCache } as unknown as LicensePreflight;
  const tokenRefresher = {
    forceRefresh,
  } as unknown as IOnDemandTokenRefresher;
  const trialSet = vi.fn().mockResolvedValue(undefined);
  const trialStore = { set: trialSet } as unknown as Parameters<
    typeof registerStartTrialCommand
  >[4];

  function handlerFor(): (...args: unknown[]) => Promise<void> {
    registerStartTrialCommand(sessionManager, licensePreflight, logger, tokenRefresher, trialStore);
    const calls = (
      vscode.commands.registerCommand as unknown as {
        mock: { calls: unknown[][] };
      }
    ).mock.calls;
    return calls[calls.length - 1]![1] as (...args: unknown[]) => Promise<void>;
  }

  beforeEach(() => {
    vi.clearAllMocks();
    (IpcClient.getInstance as ReturnType<typeof vi.fn>).mockReturnValue({
      platformStartTrial,
    });
    (SecretStorageService.getInstance as ReturnType<typeof vi.fn>).mockReturnValue({ setSecret });
    (vscode.window as unknown as Record<string, unknown>)["withProgress"] = vi.fn(
      async (_opts: unknown, task: () => Promise<unknown>) => task()
    );
    logger = {
      info: vi.fn(),
      error: vi.fn(),
      warn: vi.fn(),
    } as unknown as Logger;
  });

  it("registers the command", () => {
    const disposable = registerStartTrialCommand(
      sessionManager,
      licensePreflight,
      logger,
      tokenRefresher
    );
    expect(disposable).toBeDefined();
    expect(vscode.commands.registerCommand).toHaveBeenCalledWith(
      "nightgauge.startTrial",
      expect.any(Function)
    );
  });

  it("issues a trial, stores the key, and offers a reload", async () => {
    isAuthenticated.mockResolvedValue(true);
    getAccessToken.mockResolvedValue("jwt-token");
    platformStartTrial.mockResolvedValue(TRIAL_RESULT);
    (vscode.window.showInformationMessage as ReturnType<typeof vi.fn>).mockResolvedValue(
      "Reload Window"
    );

    await handlerFor()();

    expect(platformStartTrial).toHaveBeenCalledWith("jwt-token");
    expect(setSecret).toHaveBeenCalledWith(SECRET_KEYS.platformLicenseKey, "IB-TRIAL-AAAA-BBBB");
    expect(clearCache).toHaveBeenCalled();
    // The trial record is persisted so the status bar can show a countdown.
    expect(trialSet).toHaveBeenCalledWith(
      expect.objectContaining({
        tier: "pro",
        expiresAt: TRIAL_RESULT.expiresAt,
        runAllowance: 50,
      })
    );
    expect(vscode.window.showInformationMessage).toHaveBeenCalledWith(
      expect.stringContaining("Free Pro trial activated"),
      "Reload Window"
    );
    expect(vscode.commands.executeCommand).toHaveBeenCalledWith("workbench.action.reloadWindow");
  });

  it("prompts sign-in when not authenticated (no trial call)", async () => {
    isAuthenticated.mockResolvedValue(false);
    (vscode.window.showInformationMessage as ReturnType<typeof vi.fn>).mockResolvedValue("Sign In");

    await handlerFor()();

    expect(platformStartTrial).not.toHaveBeenCalled();
    expect(vscode.commands.executeCommand).toHaveBeenCalledWith("nightgauge.signIn");
  });

  it("surfaces a not-eligible (409) result without storing", async () => {
    isAuthenticated.mockResolvedValue(true);
    getAccessToken.mockResolvedValue("jwt-token");
    platformStartTrial.mockRejectedValue(
      new Error(
        "IPC error: this account already has a license and is not eligible for a free trial"
      )
    );
    (vscode.window.showInformationMessage as ReturnType<typeof vi.fn>).mockResolvedValue(undefined);

    await handlerFor()();

    expect(setSecret).not.toHaveBeenCalled();
    expect(vscode.window.showInformationMessage).toHaveBeenCalledWith(
      expect.stringContaining("isn't eligible for a free trial"),
      "Activate License",
      "View Pricing"
    );
  });

  it("refreshes once and retries on a 401, then succeeds", async () => {
    isAuthenticated.mockResolvedValue(true);
    getAccessToken.mockResolvedValue("stale-jwt");
    platformStartTrial
      .mockRejectedValueOnce(new Error("IPC error 401 unauthorized"))
      .mockResolvedValueOnce(TRIAL_RESULT);
    forceRefresh.mockResolvedValue("fresh-jwt");
    (vscode.window.showInformationMessage as ReturnType<typeof vi.fn>).mockResolvedValue(undefined);

    await handlerFor()();

    expect(platformStartTrial).toHaveBeenNthCalledWith(1, "stale-jwt");
    expect(forceRefresh).toHaveBeenCalled();
    expect(platformStartTrial).toHaveBeenNthCalledWith(2, "fresh-jwt");
    expect(setSecret).toHaveBeenCalledWith(SECRET_KEYS.platformLicenseKey, "IB-TRIAL-AAAA-BBBB");
  });

  it("errors out when the session has no access token", async () => {
    isAuthenticated.mockResolvedValue(true);
    getAccessToken.mockResolvedValue(null);

    await handlerFor()();

    expect(platformStartTrial).not.toHaveBeenCalled();
    expect(vscode.window.showErrorMessage).toHaveBeenCalledWith(
      expect.stringContaining("sign in again")
    );
  });
});
