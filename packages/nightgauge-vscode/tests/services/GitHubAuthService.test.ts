/**
 * GitHubAuthService unit tests.
 *
 * Tests GitHub auth flow: happy path, user cancellation, IPC error,
 * null TokenStorage, and isSignedIn().
 *
 * @see Issue #1467 - Add GitHub Sign-in as alternative auth path
 * @see Issue #2090 - Migrate to IPC
 */

import { describe, it, expect, vi, beforeEach } from "vitest";

// Mock vscode before imports
vi.mock("vscode", () => {
  class MockEventEmitter<T> {
    private listeners: Array<(e: T) => void> = [];
    event = (listener: (e: T) => void) => {
      this.listeners.push(listener);
      return {
        dispose: () => {
          const idx = this.listeners.indexOf(listener);
          if (idx >= 0) this.listeners.splice(idx, 1);
        },
      };
    };
    fire = (data: T) => {
      for (const listener of this.listeners) {
        listener(data);
      }
    };
    dispose = vi.fn();
  }
  return {
    EventEmitter: MockEventEmitter,
    Disposable: { from: vi.fn() },
    authentication: {
      getSession: vi.fn(),
    },
    window: {
      showErrorMessage: vi.fn(),
      showInformationMessage: vi.fn(),
      createOutputChannel: vi.fn(() => ({
        appendLine: vi.fn(),
        show: vi.fn(),
        clear: vi.fn(),
        dispose: vi.fn(),
      })),
    },
  };
});

// Mock TokenStorage
const mockTokenStore = vi.fn().mockResolvedValue(undefined);
const mockTokenRetrieve = vi.fn().mockResolvedValue(null);
const mockTokenClear = vi.fn().mockResolvedValue(undefined);
let mockTokenStorageInstance: object | null = {
  store: mockTokenStore,
  retrieve: mockTokenRetrieve,
  clear: mockTokenClear,
};

vi.mock("../../src/platform/TokenStorage", () => ({
  TokenStorage: {
    getInstance: () => mockTokenStorageInstance,
  },
}));

import * as vscode from "vscode";
import { GitHubAuthService } from "../../src/services/GitHubAuthService";
import type { IpcClient } from "../../src/services/IpcClient";
import type { Logger } from "../../src/utils/logger";

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

function createMockIpcClient() {
  return {
    platformAuthGithub: vi.fn(),
  } as unknown as IpcClient;
}

function createMockLogger(): Logger {
  return {
    info: vi.fn(),
    warn: vi.fn(),
    error: vi.fn(),
    debug: vi.fn(),
  } as unknown as Logger;
}

function makeTokenResponse() {
  return {
    access_token: "ib_access_123",
    refresh_token: "ib_refresh_456",
    token_type: "Bearer" as const,
    expires_in: 3600,
  };
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

describe("GitHubAuthService", () => {
  let ipcClient: ReturnType<typeof createMockIpcClient>;
  let logger: Logger;
  let service: GitHubAuthService;

  beforeEach(() => {
    ipcClient = createMockIpcClient();
    logger = createMockLogger();
    service = new GitHubAuthService(ipcClient, logger);

    // Reset token storage to always-present state
    mockTokenStorageInstance = {
      store: mockTokenStore,
      retrieve: mockTokenRetrieve,
      clear: mockTokenClear,
    };
    vi.mocked(mockTokenStore).mockReset().mockResolvedValue(undefined);
    vi.mocked(mockTokenRetrieve).mockReset().mockResolvedValue(null);
    vi.mocked(vscode.window.showErrorMessage).mockReset();
    vi.mocked(vscode.window.showInformationMessage).mockReset();
    vi.mocked(vscode.authentication.getSession).mockReset();
  });

  describe("signInWithGitHub()", () => {
    it("happy path: calls getSession, exchanges token via IPC, stores credentials, fires onSignedIn, returns true", async () => {
      const mockSession = { accessToken: "gh_token_abc" };
      vi.mocked(vscode.authentication.getSession).mockResolvedValue(
        mockSession as vscode.AuthenticationSession
      );
      const tokenResponse = makeTokenResponse();
      vi.mocked(ipcClient.platformAuthGithub as ReturnType<typeof vi.fn>).mockResolvedValue(
        tokenResponse
      );

      const onSignedInFired = vi.fn();
      service.onSignedIn(onSignedInFired);

      const result = await service.signInWithGitHub();

      expect(result).toBe(true);
      expect(vscode.authentication.getSession).toHaveBeenCalledWith("github", ["user:email"], {
        createIfNone: true,
      });
      expect(ipcClient.platformAuthGithub).toHaveBeenCalledWith("gh_token_abc");
      expect(mockTokenStore).toHaveBeenCalledWith("accessToken", tokenResponse.access_token);
      expect(mockTokenStore).toHaveBeenCalledWith("refreshToken", tokenResponse.refresh_token);
      expect(mockTokenStore).toHaveBeenCalledWith("expiresAt", expect.any(String));
      expect(onSignedInFired).toHaveBeenCalledOnce();
      expect(vscode.window.showInformationMessage).toHaveBeenCalledWith(
        "Nightgauge: Signed in via GitHub!"
      );
    });

    it("user cancels getSession: returns false without calling IPC", async () => {
      vi.mocked(vscode.authentication.getSession).mockRejectedValue(new Error("User cancelled"));

      const result = await service.signInWithGitHub();

      expect(result).toBe(false);
      expect(ipcClient.platformAuthGithub).not.toHaveBeenCalled();
      expect(vscode.window.showErrorMessage).not.toHaveBeenCalled();
      expect(logger.info).toHaveBeenCalledWith(expect.stringContaining("cancelled"));
    });

    it("IPC error: shows error message, returns false, onSignedIn NOT fired", async () => {
      const mockSession = { accessToken: "gh_token_abc" };
      vi.mocked(vscode.authentication.getSession).mockResolvedValue(
        mockSession as vscode.AuthenticationSession
      );
      const ipcError = new Error("Invalid GitHub token");
      vi.mocked(ipcClient.platformAuthGithub as ReturnType<typeof vi.fn>).mockRejectedValue(
        ipcError
      );

      const onSignedInFired = vi.fn();
      service.onSignedIn(onSignedInFired);

      const result = await service.signInWithGitHub();

      expect(result).toBe(false);
      expect(onSignedInFired).not.toHaveBeenCalled();
      expect(vscode.window.showErrorMessage).toHaveBeenCalledWith(
        expect.stringContaining("Invalid GitHub token")
      );
      expect(logger.error).toHaveBeenCalledWith(
        expect.stringContaining("platform token exchange failed"),
        ipcError
      );
    });

    it("TokenStorage returns null instance: logs error, does not crash", async () => {
      const mockSession = { accessToken: "gh_token_abc" };
      vi.mocked(vscode.authentication.getSession).mockResolvedValue(
        mockSession as vscode.AuthenticationSession
      );
      vi.mocked(ipcClient.platformAuthGithub as ReturnType<typeof vi.fn>).mockResolvedValue(
        makeTokenResponse()
      );

      // Simulate TokenStorage not initialized
      mockTokenStorageInstance = null;

      const result = await service.signInWithGitHub();

      // onSignedIn still fires before storeTokens returns (storeTokens is called after fire)
      // But storeTokens logs an error and doesn't crash
      expect(logger.error).toHaveBeenCalledWith(
        expect.stringContaining("TokenStorage not available")
      );
      // Returns true — the sign-in flow completed from the service's perspective
      expect(result).toBe(true);
    });
  });

  describe("isSignedIn()", () => {
    it("returns true when TokenStorage has an access token", async () => {
      vi.mocked(mockTokenRetrieve).mockResolvedValue("ib_access_123");

      expect(await service.isSignedIn()).toBe(true);
    });

    it("returns false when TokenStorage returns null", async () => {
      vi.mocked(mockTokenRetrieve).mockResolvedValue(null);

      expect(await service.isSignedIn()).toBe(false);
    });

    it("returns false when TokenStorage is not initialized", async () => {
      mockTokenStorageInstance = null;

      expect(await service.isSignedIn()).toBe(false);
    });
  });

  describe("dispose()", () => {
    it("disposes event emitters without throwing", () => {
      expect(() => service.dispose()).not.toThrow();
    });
  });
});
