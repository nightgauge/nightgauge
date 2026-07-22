/**
 * Tests for the Activate License command.
 *
 * @see Issue #1138 - Commercialization: in-extension license activation
 */

import { describe, it, expect, beforeEach, vi } from "vitest";
import * as vscode from "vscode";
import { registerActivateLicenseCommand } from "../../src/commands/activateLicense";
import { IpcClient } from "../../src/services/IpcClient";
import { SecretStorageService, SECRET_KEYS } from "../../src/services/SecretStorageService";
import type { Logger } from "../../src/utils/logger";

vi.mock("../../src/services/IpcClient", () => ({
  IpcClient: { getInstance: vi.fn() },
}));

vi.mock("../../src/services/SecretStorageService", () => ({
  SecretStorageService: { getInstance: vi.fn() },
  SECRET_KEYS: { platformLicenseKey: "nightgauge.platform.licenseKey" },
}));

describe("activateLicense", () => {
  const platformValidateLicense = vi.fn();
  const setSecret = vi.fn().mockResolvedValue(undefined);
  const clearCache = vi.fn();
  let logger: Logger;
  const trialClear = vi.fn().mockResolvedValue(undefined);
  const trialStore = { clear: trialClear } as unknown as Parameters<
    typeof registerActivateLicenseCommand
  >[2];

  function handlerFor(): (...args: unknown[]) => Promise<void> {
    registerActivateLicenseCommand({ clearCache } as never, logger, trialStore);
    const calls = (vscode.commands.registerCommand as unknown as { mock: { calls: unknown[][] } })
      .mock.calls;
    return calls[calls.length - 1]![1] as (...args: unknown[]) => Promise<void>;
  }

  beforeEach(() => {
    vi.clearAllMocks();
    (IpcClient.getInstance as ReturnType<typeof vi.fn>).mockReturnValue({
      platformValidateLicense,
    });
    (SecretStorageService.getInstance as ReturnType<typeof vi.fn>).mockReturnValue({ setSecret });
    // The shared vscode mock omits these two — add them for this command.
    (vscode.window as unknown as Record<string, unknown>)["withProgress"] = vi.fn(
      async (_opts: unknown, task: () => Promise<unknown>) => task()
    );
    (vscode.window as unknown as Record<string, unknown>)["showInputBox"] = vi.fn();
    logger = {
      info: vi.fn(),
      error: vi.fn(),
      warn: vi.fn(),
    } as unknown as Logger;
  });

  it("registers the command", () => {
    const disposable = registerActivateLicenseCommand({ clearCache } as never, logger);
    expect(disposable).toBeDefined();
    expect(vscode.commands.registerCommand).toHaveBeenCalledWith(
      "nightgauge.activateLicense",
      expect.any(Function)
    );
  });

  it("stores a valid paid key and offers a reload", async () => {
    (vscode.window.showInputBox as ReturnType<typeof vi.fn>).mockResolvedValue(
      "  IB-REAL-KEY-1234  "
    );
    platformValidateLicense.mockResolvedValue({ valid: true, tier: "pro" });
    (vscode.window.showInformationMessage as ReturnType<typeof vi.fn>).mockResolvedValue(
      "Reload Window"
    );

    await handlerFor()();

    // Verified the trimmed key (not the padded input).
    expect(platformValidateLicense).toHaveBeenCalledWith("IB-REAL-KEY-1234");
    // Persisted under the SecretStorage key IpcClientBase reads.
    expect(setSecret).toHaveBeenCalledWith(SECRET_KEYS.platformLicenseKey, "IB-REAL-KEY-1234");
    expect(clearCache).toHaveBeenCalled();
    // Activating an explicit key supersedes any in-progress trial.
    expect(trialClear).toHaveBeenCalled();
    // Offered + performed the reload.
    expect(vscode.window.showInformationMessage).toHaveBeenCalledWith(
      expect.stringContaining("Pro license activated"),
      "Reload Window"
    );
    expect(vscode.commands.executeCommand).toHaveBeenCalledWith("workbench.action.reloadWindow");
  });

  it("does not store a key the platform rejects as invalid", async () => {
    (vscode.window.showInputBox as ReturnType<typeof vi.fn>).mockResolvedValue("BAD-KEY");
    platformValidateLicense.mockResolvedValue({ valid: false, tier: "" });

    await handlerFor()();

    expect(setSecret).not.toHaveBeenCalled();
    expect(vscode.window.showErrorMessage).toHaveBeenCalledWith(
      expect.stringContaining("was not accepted")
    );
  });

  it("treats a community-tier result as not accepted (no store)", async () => {
    (vscode.window.showInputBox as ReturnType<typeof vi.fn>).mockResolvedValue("SOME-KEY");
    platformValidateLicense.mockResolvedValue({
      valid: true,
      tier: "community",
    });

    await handlerFor()();

    expect(setSecret).not.toHaveBeenCalled();
    expect(vscode.window.showErrorMessage).toHaveBeenCalled();
  });

  it("does nothing when the input box is cancelled", async () => {
    (vscode.window.showInputBox as ReturnType<typeof vi.fn>).mockResolvedValue(undefined);

    await handlerFor()();

    expect(platformValidateLicense).not.toHaveBeenCalled();
    expect(setSecret).not.toHaveBeenCalled();
  });

  it("surfaces a verification error without storing", async () => {
    (vscode.window.showInputBox as ReturnType<typeof vi.fn>).mockResolvedValue("IB-KEY");
    platformValidateLicense.mockRejectedValue(new Error("network down"));

    await handlerFor()();

    expect(setSecret).not.toHaveBeenCalled();
    expect(vscode.window.showErrorMessage).toHaveBeenCalledWith(
      expect.stringContaining("Couldn't verify the license")
    );
    expect(logger.error).toHaveBeenCalled();
  });
});
