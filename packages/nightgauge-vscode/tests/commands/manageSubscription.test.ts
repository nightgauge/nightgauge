/**
 * Tests for the Manage Subscription command.
 *
 * @see Issue #1478 - Implement upgrade/downgrade flows via Stripe Customer Portal
 * @see Issue #2091 - Migrated from PlatformApiClient HTTP to Go IPC
 */

import { describe, it, expect, beforeEach, vi } from "vitest";
import * as vscode from "vscode";
import { registerManageSubscriptionCommand } from "../../src/commands/manageSubscription";
import { IpcClient } from "../../src/services/IpcClient";
import type { SessionManager } from "../../src/platform/SessionManager";
import type { LicensePreflight } from "../../src/platform/LicensePreflight";
import type { Logger } from "../../src/utils/logger";

// Mock IpcClient singleton
vi.mock("../../src/services/IpcClient", () => ({
  IpcClient: {
    getInstance: vi.fn(),
  },
}));

describe("registerManageSubscriptionCommand", () => {
  let mockSessionManager: SessionManager;
  let mockLicensePreflight: LicensePreflight;
  let mockLogger: Logger;
  let mockIpcClient: any;
  let commandHandler: () => Promise<void>;

  beforeEach(() => {
    vi.clearAllMocks();

    // Set up withProgress to call the task immediately
    (vscode.window as any).withProgress = vi.fn(async (_opts: any, task: () => Promise<void>) => {
      await task();
    });

    // Set up openExternal
    (vscode.env as any) = {
      openExternal: vi.fn().mockResolvedValue(true),
    };

    mockLogger = {
      info: vi.fn(),
      error: vi.fn(),
      warn: vi.fn(),
      debug: vi.fn(),
    } as unknown as Logger;

    mockSessionManager = {
      isAuthenticated: vi.fn().mockResolvedValue(true),
      state: "authenticated",
    } as unknown as SessionManager;

    mockLicensePreflight = {
      validate: vi.fn().mockResolvedValue({ tier: "pro", allowed: true }),
      clearCache: vi.fn(),
    } as unknown as LicensePreflight;

    mockIpcClient = {
      platformCreatePortalSession: vi.fn().mockResolvedValue({
        url: "https://billing.stripe.com/session/test_abc",
      }),
    };

    (IpcClient.getInstance as any).mockReturnValue(mockIpcClient);

    // Register the command and capture the handler
    registerManageSubscriptionCommand(mockSessionManager, mockLicensePreflight, mockLogger);

    const calls = (vscode.commands.registerCommand as any).mock.calls;
    const lastCall = calls[calls.length - 1];
    commandHandler = lastCall[1];
  });

  it("registers the nightgauge.manageSubscription command", () => {
    expect(vscode.commands.registerCommand).toHaveBeenCalledWith(
      "nightgauge.manageSubscription",
      expect.any(Function)
    );
  });

  describe("authenticated flow — paid tier", () => {
    it("fetches portal session URL via IPC and opens it in the browser", async () => {
      await commandHandler();

      expect(mockIpcClient.platformCreatePortalSession).toHaveBeenCalled();
      expect(vscode.env.openExternal).toHaveBeenCalledWith(
        expect.objectContaining({ toString: expect.any(Function) })
      );
    });

    it("clears the license cache after opening the portal", async () => {
      await commandHandler();

      expect(mockLicensePreflight.clearCache).toHaveBeenCalled();
    });

    it("shows progress notification while fetching", async () => {
      await commandHandler();

      expect(vscode.window.withProgress).toHaveBeenCalledWith(
        expect.objectContaining({
          location: vscode.ProgressLocation.Notification,
          title: "Nightgauge: Opening subscription portal…",
        }),
        expect.any(Function)
      );
    });
  });

  describe("unauthenticated flow", () => {
    beforeEach(() => {
      (mockSessionManager.isAuthenticated as any).mockResolvedValue(false);
    });

    it("prompts user to sign in instead of opening portal", async () => {
      (vscode.window.showInformationMessage as any).mockResolvedValue(undefined);

      await commandHandler();

      expect(vscode.window.showInformationMessage).toHaveBeenCalledWith(
        expect.stringContaining("must be signed in"),
        "Sign In"
      );
      expect(mockIpcClient.platformCreatePortalSession).not.toHaveBeenCalled();
    });

    it("triggers sign-in command when user clicks Sign In", async () => {
      (vscode.window.showInformationMessage as any).mockResolvedValue("Sign In");
      (vscode.commands.executeCommand as any).mockResolvedValue(undefined);

      await commandHandler();

      expect(vscode.commands.executeCommand).toHaveBeenCalledWith("nightgauge.signIn");
    });
  });

  describe("community tier flow", () => {
    beforeEach(() => {
      (mockLicensePreflight.validate as any).mockResolvedValue({
        tier: "community",
        allowed: true,
      });
    });

    it("shows upgrade message instead of opening portal", async () => {
      (vscode.window.showInformationMessage as any).mockResolvedValue(undefined);

      await commandHandler();

      expect(vscode.window.showInformationMessage).toHaveBeenCalledWith(
        expect.stringContaining("Upgrade"),
        "View Pricing"
      );
      expect(mockIpcClient.platformCreatePortalSession).not.toHaveBeenCalled();
    });

    it("opens pricing page when user clicks View Pricing", async () => {
      (vscode.window.showInformationMessage as any).mockResolvedValue("View Pricing");

      await commandHandler();

      expect(vscode.env.openExternal).toHaveBeenCalledWith(
        expect.objectContaining({ toString: expect.any(Function) })
      );
    });
  });

  describe("error handling", () => {
    it("shows error message when portal session fetch fails", async () => {
      mockIpcClient.platformCreatePortalSession.mockRejectedValue(new Error("Network error"));

      await commandHandler();

      expect(vscode.window.showErrorMessage).toHaveBeenCalledWith(
        expect.stringContaining("Network error")
      );
    });

    it("shows error when IPC client is not available", async () => {
      (IpcClient.getInstance as any).mockReturnValue(null);

      await commandHandler();

      expect(vscode.window.showErrorMessage).toHaveBeenCalledWith(
        expect.stringContaining("IPC backend not available")
      );
    });
  });

  // A stale access token can produce a one-off 401 on the portal call. The
  // command now attempts a single centralized refresh-and-retry before telling
  // the user their session expired, and defers the terminal "session expired"
  // prompt to TokenRefreshManager to avoid double-prompting (#3754).
  describe("auth-error handling (#3754)", () => {
    let mockTokenRefresher: { forceRefresh: ReturnType<typeof vi.fn> };

    /** Re-register the command with a refresher and return its handler. */
    function handlerWithRefresher(): () => Promise<void> {
      registerManageSubscriptionCommand(
        mockSessionManager,
        mockLicensePreflight,
        mockLogger,
        mockTokenRefresher
      );
      const calls = (vscode.commands.registerCommand as any).mock.calls;
      return calls[calls.length - 1][1];
    }

    beforeEach(() => {
      mockTokenRefresher = { forceRefresh: vi.fn() };
    });

    it("refreshes and retries the portal call on a 401, then opens the URL", async () => {
      mockTokenRefresher.forceRefresh.mockResolvedValue("fresh-access-token");
      mockIpcClient.platformCreatePortalSession
        .mockRejectedValueOnce(new Error("IPC error UNAUTHORIZED: unexpected status 401"))
        .mockResolvedValueOnce({ url: "https://billing.stripe.com/session/retry_ok" });

      await handlerWithRefresher()();

      expect(mockTokenRefresher.forceRefresh).toHaveBeenCalledTimes(1);
      expect(mockIpcClient.platformCreatePortalSession).toHaveBeenCalledTimes(2);
      expect(vscode.env.openExternal).toHaveBeenCalled();
      expect(vscode.window.showErrorMessage).not.toHaveBeenCalled();
    });

    it("prompts sign-in when refresh fails but the session is still alive (transient)", async () => {
      mockTokenRefresher.forceRefresh.mockResolvedValue(null);
      (mockSessionManager.isAuthenticated as any).mockResolvedValue(true); // not signed out
      mockIpcClient.platformCreatePortalSession.mockRejectedValue(
        new Error("IPC error UNAUTHORIZED: unexpected status 401")
      );
      (vscode.window.showErrorMessage as any).mockResolvedValue("Sign In");

      await handlerWithRefresher()();

      expect(mockTokenRefresher.forceRefresh).toHaveBeenCalledTimes(1);
      expect(vscode.window.showErrorMessage).toHaveBeenCalledWith(
        expect.stringContaining("session may have expired"),
        "Sign In"
      );
      expect(vscode.commands.executeCommand).toHaveBeenCalledWith("nightgauge.signIn");
    });

    it("does NOT prompt when the centralized refresher already signed out (no double-prompt)", async () => {
      mockTokenRefresher.forceRefresh.mockResolvedValue(null);
      // Auth-fatal: TokenRefreshManager cleared tokens + showed its own prompt.
      (mockSessionManager.isAuthenticated as any).mockResolvedValue(false);
      mockIpcClient.platformCreatePortalSession.mockRejectedValue(
        new Error("IPC error FORBIDDEN: unexpected status 403")
      );

      await handlerWithRefresher()();

      expect(vscode.window.showErrorMessage).not.toHaveBeenCalled();
      expect(vscode.commands.executeCommand).not.toHaveBeenCalledWith("nightgauge.signIn");
    });

    it("does not attempt refresh for non-auth errors", async () => {
      mockIpcClient.platformCreatePortalSession.mockRejectedValue(new Error("Network error"));

      await handlerWithRefresher()();

      expect(mockTokenRefresher.forceRefresh).not.toHaveBeenCalled();
      expect(vscode.window.showErrorMessage).toHaveBeenCalledWith(
        expect.stringContaining("Network error")
      );
    });
  });
});
