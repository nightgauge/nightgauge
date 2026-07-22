/**
 * OAuthDeviceFlowService unit tests.
 *
 * Tests the full OAuth Device Flow lifecycle: happy path, pending/slow-down
 * retries, terminal errors, timeout, cancellation, re-entrancy, sign-out,
 * and AuthProvider integration.
 *
 * @see Issue #1464 - Implement OAuth Device Flow login command and UI
 */

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";

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
    window: {
      showInformationMessage: vi.fn().mockResolvedValue(undefined),
      showErrorMessage: vi.fn(),
      showWarningMessage: vi.fn(),
    },
    env: {
      clipboard: { writeText: vi.fn().mockResolvedValue(undefined) },
      openExternal: vi.fn().mockResolvedValue(true),
    },
    Uri: {
      parse: (uri: string) => ({ toString: () => uri }),
    },
  };
});

// Mock TokenStorage
const mockTokenStore = vi.fn().mockResolvedValue(undefined);
const mockTokenRetrieve = vi.fn().mockResolvedValue(null);
const mockTokenDelete = vi.fn().mockResolvedValue(undefined);
const mockTokenClear = vi.fn().mockResolvedValue(undefined);

vi.mock("../../src/platform/TokenStorage", () => ({
  TokenStorage: {
    getInstance: () => ({
      store: mockTokenStore,
      retrieve: mockTokenRetrieve,
      delete: mockTokenDelete,
      clear: mockTokenClear,
    }),
  },
}));

import * as vscode from "vscode";
import {
  OAuthDeviceFlowService,
  type DeviceFlowState,
} from "../../src/services/OAuthDeviceFlowService";
import type { Logger } from "../../src/utils/logger";

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

function createMockIpcClient() {
  return {
    platformAuthDeviceCode: vi.fn(),
    platformAuthDeviceToken: vi.fn(),
  } as unknown as any;
}

function createMockLogger(): Logger {
  return {
    info: vi.fn(),
    warn: vi.fn(),
    error: vi.fn(),
    debug: vi.fn(),
  } as unknown as Logger;
}

/** Use short intervals to keep tests fast with timer advancement. */
const sampleDeviceCodeResponse = {
  device_code: "dc_abc123",
  user_code: "ABCD-1234",
  verification_uri: "https://auth.nightgauge.dev/device",
  expires_in: 900,
  interval: 5,
};

const sampleTokenResponse = {
  access_token: "at_xyz789",
  refresh_token: "rt_xyz789",
  token_type: "Bearer" as const,
  expires_in: 3600,
};

const samplePendingResponse = {
  status: "authorization_pending" as const,
};

const sampleSlowDownResponse = {
  status: "slow_down" as const,
};

/**
 * Start the device flow and advance fake timers to let the polling delay resolve.
 * Each poll cycle has a delay of `interval * 1000` ms.
 */
async function startFlowAndAdvance(
  service: OAuthDeviceFlowService,
  intervalMs: number,
  pollCycles: number = 1
): Promise<void> {
  const promise = service.startDeviceFlow();
  for (let i = 0; i < pollCycles; i++) {
    await vi.advanceTimersByTimeAsync(intervalMs);
  }
  await promise;
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

describe("OAuthDeviceFlowService", () => {
  let ipcClient: ReturnType<typeof createMockIpcClient>;
  let logger: Logger;
  let service: OAuthDeviceFlowService;

  beforeEach(() => {
    vi.useFakeTimers();
    ipcClient = createMockIpcClient();
    logger = createMockLogger();
    service = new OAuthDeviceFlowService(ipcClient, logger);

    mockTokenStore.mockReset().mockResolvedValue(undefined);
    mockTokenRetrieve.mockReset().mockResolvedValue(null);
    mockTokenDelete.mockReset().mockResolvedValue(undefined);
    mockTokenClear.mockReset().mockResolvedValue(undefined);
    vi.mocked(vscode.window.showInformationMessage).mockReset().mockResolvedValue(undefined);
    vi.mocked(vscode.window.showErrorMessage).mockReset();
    vi.mocked(vscode.window.showWarningMessage).mockReset();
  });

  afterEach(() => {
    service.dispose();
    vi.useRealTimers();
  });

  // =========================================================================
  // Happy path
  // =========================================================================

  describe("Happy path", () => {
    it("completes device flow: request code → poll → store tokens → fire event", async () => {
      ipcClient.platformAuthDeviceCode.mockResolvedValue(sampleDeviceCodeResponse);
      ipcClient.platformAuthDeviceToken.mockResolvedValue(sampleTokenResponse);

      const signedInSpy = vi.fn();
      service.onSignedIn(signedInSpy);

      await startFlowAndAdvance(service, 5001);

      expect(ipcClient.platformAuthDeviceCode).toHaveBeenCalledOnce();
      expect(ipcClient.platformAuthDeviceToken).toHaveBeenCalledWith("dc_abc123");
      expect(mockTokenStore).toHaveBeenCalledWith("accessToken", "at_xyz789");
      expect(mockTokenStore).toHaveBeenCalledWith("refreshToken", "rt_xyz789");
      expect(mockTokenStore).toHaveBeenCalledWith("expiresAt", expect.any(String));
      expect(signedInSpy).toHaveBeenCalledOnce();
      expect(service.state).toBe("signed-in");
    });

    it("shows notification with device code", async () => {
      ipcClient.platformAuthDeviceCode.mockResolvedValue(sampleDeviceCodeResponse);
      ipcClient.platformAuthDeviceToken.mockResolvedValue(sampleTokenResponse);

      await startFlowAndAdvance(service, 5001);

      expect(vscode.window.showInformationMessage).toHaveBeenCalledWith(
        expect.stringContaining("ABCD-1234"),
        "Copy Code",
        "Open Browser"
      );
    });
  });

  // =========================================================================
  // Polling: authorization_pending → retry
  // =========================================================================

  describe("Polling retries", () => {
    it("retries on authorization_pending then succeeds", async () => {
      ipcClient.platformAuthDeviceCode.mockResolvedValue(sampleDeviceCodeResponse);
      ipcClient.platformAuthDeviceToken
        .mockResolvedValueOnce(samplePendingResponse)
        .mockResolvedValueOnce(sampleTokenResponse);

      // Need 2 poll cycles: first pending, second success
      await startFlowAndAdvance(service, 5001, 2);

      expect(ipcClient.platformAuthDeviceToken).toHaveBeenCalledTimes(2);
      expect(service.state).toBe("signed-in");
    });

    it("increases interval on slow_down", async () => {
      ipcClient.platformAuthDeviceCode.mockResolvedValue(sampleDeviceCodeResponse);
      ipcClient.platformAuthDeviceToken
        .mockResolvedValueOnce(sampleSlowDownResponse)
        .mockResolvedValueOnce(sampleTokenResponse);

      const promise = service.startDeviceFlow();
      // First poll: 5s interval
      await vi.advanceTimersByTimeAsync(5001);
      // Second poll: 10s interval (5 + 5 from slow_down)
      await vi.advanceTimersByTimeAsync(10001);
      await promise;

      expect(ipcClient.platformAuthDeviceToken).toHaveBeenCalledTimes(2);
      expect(service.state).toBe("signed-in");
      expect(logger.info).toHaveBeenCalledWith(
        "Poll slow_down — increasing interval",
        expect.objectContaining({ currentInterval: 10 })
      );
    });
  });

  // =========================================================================
  // Terminal errors
  // =========================================================================

  describe("Terminal errors", () => {
    it("handles expired device code error", async () => {
      ipcClient.platformAuthDeviceCode.mockResolvedValue(sampleDeviceCodeResponse);
      ipcClient.platformAuthDeviceToken.mockRejectedValue(new Error("Device code expired"));

      await startFlowAndAdvance(service, 5001);

      expect(service.state).toBe("error");
      expect(vscode.window.showErrorMessage).toHaveBeenCalledWith(
        expect.stringContaining("Device code expired")
      );
    });

    it("handles access denied error", async () => {
      ipcClient.platformAuthDeviceCode.mockResolvedValue(sampleDeviceCodeResponse);
      ipcClient.platformAuthDeviceToken.mockRejectedValue(new Error("Access denied by user"));

      await startFlowAndAdvance(service, 5001);

      expect(service.state).toBe("error");
      expect(vscode.window.showErrorMessage).toHaveBeenCalledWith(
        expect.stringContaining("Access denied")
      );
    });

    it("handles network errors gracefully", async () => {
      ipcClient.platformAuthDeviceCode.mockResolvedValue(sampleDeviceCodeResponse);
      ipcClient.platformAuthDeviceToken.mockRejectedValueOnce(new Error("Network failure"));

      await startFlowAndAdvance(service, 5001);

      expect(service.state).toBe("error");
      expect(vscode.window.showErrorMessage).toHaveBeenCalledWith(
        expect.stringContaining("Network failure")
      );
    });
  });

  // =========================================================================
  // Timeout
  // =========================================================================

  describe("Timeout", () => {
    it("times out when expires_in elapses", async () => {
      const shortExpiry = {
        ...sampleDeviceCodeResponse,
        expires_in: 0, // expires immediately — while loop body never executes
        interval: 5,
      };
      ipcClient.platformAuthDeviceCode.mockResolvedValue(shortExpiry);

      // With expires_in=0, the while condition fails immediately → timeout
      const promise = service.startDeviceFlow();
      await promise;

      expect(service.state).toBe("error");
      expect(vscode.window.showWarningMessage).toHaveBeenCalledWith(
        expect.stringContaining("timed out")
      );
    });
  });

  // =========================================================================
  // Cancellation
  // =========================================================================

  describe("Cancellation", () => {
    it("cancelPolling() stops the loop", async () => {
      ipcClient.platformAuthDeviceCode.mockResolvedValue(sampleDeviceCodeResponse);
      ipcClient.platformAuthDeviceToken.mockResolvedValue(samplePendingResponse);

      const flowPromise = service.startDeviceFlow();
      // Cancel before the first delay resolves
      service.cancelPolling();
      await vi.advanceTimersByTimeAsync(5001);
      await flowPromise;

      expect(service.state).toBe("cancelled");
    });
  });

  // =========================================================================
  // Re-entrancy
  // =========================================================================

  describe("Re-entrancy", () => {
    it('shows "in progress" if called while polling', async () => {
      ipcClient.platformAuthDeviceCode.mockResolvedValue(sampleDeviceCodeResponse);
      // First poll returns pending, second returns token (for cleanup)
      ipcClient.platformAuthDeviceToken
        .mockResolvedValueOnce(samplePendingResponse)
        .mockResolvedValueOnce(sampleTokenResponse);

      // Start flow — requestDeviceCode resolves immediately (mocked),
      // then the service sets state='polling' and enters delay.
      const flowPromise = service.startDeviceFlow();
      // Flush microtasks to let requestDeviceCode resolve and state become 'polling'
      await vi.advanceTimersByTimeAsync(1);

      // Second call while first is polling — should show "in progress" message
      await service.startDeviceFlow();

      expect(vscode.window.showInformationMessage).toHaveBeenCalledWith(
        "Nightgauge: Sign-in already in progress."
      );

      // Cleanup: advance past both poll cycles so flowPromise resolves
      await vi.advanceTimersByTimeAsync(5001);
      await vi.advanceTimersByTimeAsync(5001);
      await flowPromise;
    });
  });

  // =========================================================================
  // signOut
  // =========================================================================

  describe("signOut()", () => {
    it("deletes all 3 token secrets and fires onSignedOut", async () => {
      const signedOutSpy = vi.fn();
      service.onSignedOut(signedOutSpy);

      await service.signOut();

      expect(mockTokenClear).toHaveBeenCalledOnce();
      expect(signedOutSpy).toHaveBeenCalledOnce();
      expect(service.state).toBe("idle");
    });
  });

  // =========================================================================
  // isSignedIn / getAccessToken
  // =========================================================================

  describe("isSignedIn()", () => {
    it("returns false when no access token is stored", async () => {
      mockTokenRetrieve.mockResolvedValue(null);
      expect(await service.isSignedIn()).toBe(false);
    });

    it("returns true when access token exists", async () => {
      mockTokenRetrieve.mockResolvedValue("at_xyz789");
      expect(await service.isSignedIn()).toBe(true);
    });
  });

  describe("getAccessToken()", () => {
    it("returns null when not signed in", async () => {
      mockTokenRetrieve.mockResolvedValue(null);
      expect(await service.getAccessToken()).toBeNull();
    });

    it("returns token string when signed in", async () => {
      mockTokenRetrieve.mockResolvedValue("at_xyz789");
      expect(await service.getAccessToken()).toBe("at_xyz789");
    });
  });
});
