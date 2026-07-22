/**
 * TokenRefreshManager.test.ts
 *
 * Unit tests covering:
 * - Proactive refresh timing (5 min before expiry or 10%, whichever sooner)
 * - Refresh success path — new tokens stored in tokenStorage
 * - Failure handling — expired refresh token triggers sign-out
 * - No refresh loop — after sign-out, timer not rescheduled
 * - Offline pause — timer cleared on non-online state
 * - Offline resume — timer scheduled when online event fires
 * - dispose() cleanup — no refresh after dispose
 *
 * @see Issue #1466 - Implement token refresh lifecycle and automatic renewal
 */

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import type { ConnectionStateEvent, TokenChangeEvent } from "../../src/platform/types";

// ---------------------------------------------------------------------------
// vscode mock — functional EventEmitter that supports subscribe + fire
// ---------------------------------------------------------------------------
vi.mock("vscode", () => {
  class MockEventEmitter<T> {
    private listeners: ((e: T) => void)[] = [];
    event = (listener: (e: T) => void): { dispose: () => void } => {
      this.listeners.push(listener);
      return {
        dispose: () => {
          const idx = this.listeners.indexOf(listener);
          if (idx >= 0) this.listeners.splice(idx, 1);
        },
      };
    };
    fire(data: T): void {
      for (const l of [...this.listeners]) l(data);
    }
    dispose(): void {
      this.listeners = [];
    }
  }

  const showWarningMessageMock = vi.fn().mockResolvedValue(undefined);

  return {
    EventEmitter: MockEventEmitter,
    window: {
      showWarningMessage: showWarningMessageMock,
    },
  };
});

import { TokenRefreshManager } from "../../src/platform/TokenRefreshManager";
import type { ITokenStorage, TokenKey } from "../../src/platform/TokenStorage";
import type { IpcClient } from "../../src/services/IpcClient";
import type { OfflineManager } from "../../src/platform/OfflineManager";
import type { Logger } from "../../src/utils/logger";
import * as vscode from "vscode";

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

/** Create a minimal mock ITokenStorage */
function createMockTokenStorage(
  initialValues: Partial<Record<TokenKey, string>> = {}
): ITokenStorage & {
  _values: Map<TokenKey, string>;
  _fireChange: (evt: TokenChangeEvent) => void;
} {
  const values = new Map<TokenKey, string>(Object.entries(initialValues) as [TokenKey, string][]);

  // We need a real EventEmitter-like for onTokenChanged
  const emitter = new vscode.EventEmitter<TokenChangeEvent>();

  const mock = {
    _values: values,
    _fireChange: (evt: TokenChangeEvent) => emitter.fire(evt),
    onTokenChanged: emitter.event,
    store: vi.fn(async (key: TokenKey, value: string) => {
      values.set(key, value);
      emitter.fire({ key, action: "stored" });
    }),
    retrieve: vi.fn(async (key: TokenKey) => values.get(key) ?? null),
    delete: vi.fn(async (key: TokenKey) => {
      values.delete(key);
      emitter.fire({ key, action: "deleted" });
    }),
    clear: vi.fn(async () => {
      values.clear();
      emitter.fire({ key: "all", action: "cleared" });
    }),
    dispose: vi.fn(() => emitter.dispose()),
  };

  return mock;
}

/** Create a minimal mock IpcClient */
function createMockIpcClient(): Pick<IpcClient, "platformAuthRefresh"> {
  return {
    platformAuthRefresh: vi.fn(),
  };
}

/** Create a minimal mock OfflineManager */
function createMockOfflineManager(initialState: "online" | "degraded" | "offline" = "online"): {
  state: "online" | "degraded" | "offline";
  onStateChanged: vscode.Event<ConnectionStateEvent>;
  _fire: (evt: ConnectionStateEvent) => void;
  dispose: () => void;
} {
  const emitter = new vscode.EventEmitter<ConnectionStateEvent>();
  let state: "online" | "degraded" | "offline" = initialState;
  return {
    get state() {
      return state;
    },
    onStateChanged: emitter.event,
    _fire: (evt: ConnectionStateEvent) => {
      state = evt.current;
      emitter.fire(evt);
    },
    dispose: () => emitter.dispose(),
  };
}

/** Create a minimal mock Logger */
function createMockLogger(): Logger {
  return {
    info: vi.fn(),
    debug: vi.fn(),
    warn: vi.fn(),
    error: vi.fn(),
  } as unknown as Logger;
}

/**
 * Returns an ISO 8601 string for N milliseconds from now.
 */
function expiresInMs(ms: number): string {
  return new Date(Date.now() + ms).toISOString();
}

const MINUTE = 60_000;

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

describe("TokenRefreshManager", () => {
  beforeEach(() => {
    vi.useFakeTimers();
    // showWarningMessage is a module-level mock shared across tests — reset its
    // call history so per-test count/absence assertions aren't polluted.
    (vscode.window.showWarningMessage as ReturnType<typeof vi.fn>).mockClear();
  });

  afterEach(() => {
    vi.useRealTimers();
  });

  // =========================================================================
  // Proactive refresh timing
  // =========================================================================
  describe("proactive refresh timing", () => {
    it("schedules refresh at 10% before expiry for short-lived tokens (< 50 min)", async () => {
      // Token expires in 10 minutes — 10% = 1 minute, so advance = 1 min, delay = 9 min
      const TEN_MIN = 10 * MINUTE;
      const tokenStorage = createMockTokenStorage({
        expiresAt: expiresInMs(TEN_MIN),
      });
      const ipcClient = createMockIpcClient();
      const offlineManager = createMockOfflineManager("online");
      const onSignOut = vi.fn().mockResolvedValue(undefined);
      const logger = createMockLogger();

      const mgr = new TokenRefreshManager(
        tokenStorage as unknown as ITokenStorage,
        ipcClient as unknown as IpcClient,
        offlineManager as unknown as OfflineManager,
        onSignOut,
        logger
      );

      // Mock a successful refresh response
      (ipcClient.platformAuthRefresh as ReturnType<typeof vi.fn>).mockResolvedValue({
        access_token: "new-access",
        refresh_token: "new-refresh",
        token_type: "Bearer",
        expires_in: 3600,
      });
      tokenStorage._values.set("refreshToken", "ref-token");

      await mgr.start();

      // Advance 8 minutes — should not have refreshed yet (delay is 9 min)
      await vi.advanceTimersByTimeAsync(8 * MINUTE);
      expect(ipcClient.platformAuthRefresh).not.toHaveBeenCalled();

      // Advance 1 more minute (9 total) — timer fires
      await vi.advanceTimersByTimeAsync(1 * MINUTE);
      expect(ipcClient.platformAuthRefresh).toHaveBeenCalledTimes(1);

      mgr.dispose();
    });

    it("schedules refresh at 5 minutes before expiry for long-lived tokens (> 50 min)", async () => {
      // Token expires in 60 minutes — 10% = 6 min, so advance = min(5, 6) = 5 min, delay = 55 min
      const SIXTY_MIN = 60 * MINUTE;
      const tokenStorage = createMockTokenStorage({
        expiresAt: expiresInMs(SIXTY_MIN),
      });
      const ipcClient = createMockIpcClient();
      const offlineManager = createMockOfflineManager("online");
      const onSignOut = vi.fn().mockResolvedValue(undefined);
      const logger = createMockLogger();

      (ipcClient.platformAuthRefresh as ReturnType<typeof vi.fn>).mockResolvedValue({
        access_token: "new-access",
        refresh_token: "new-refresh",
        token_type: "Bearer",
        expires_in: 3600,
      });
      tokenStorage._values.set("refreshToken", "ref-token");

      const mgr = new TokenRefreshManager(
        tokenStorage as unknown as ITokenStorage,
        ipcClient as unknown as IpcClient,
        offlineManager as unknown as OfflineManager,
        onSignOut,
        logger
      );

      await mgr.start();

      // Advance 54 minutes — should not have refreshed yet (delay is 55 min)
      await vi.advanceTimersByTimeAsync(54 * MINUTE);
      expect(ipcClient.platformAuthRefresh).not.toHaveBeenCalled();

      // Advance 1 more minute (55 total) — timer fires
      await vi.advanceTimersByTimeAsync(1 * MINUTE);
      expect(ipcClient.platformAuthRefresh).toHaveBeenCalledTimes(1);

      mgr.dispose();
    });

    it("schedules immediate refresh when token is already expired", async () => {
      // Token expired 5 minutes ago
      const tokenStorage = createMockTokenStorage({
        expiresAt: new Date(Date.now() - 5 * MINUTE).toISOString(),
        refreshToken: "ref-token",
      });
      const ipcClient = createMockIpcClient();
      const offlineManager = createMockOfflineManager("online");
      const onSignOut = vi.fn().mockResolvedValue(undefined);
      const logger = createMockLogger();

      (ipcClient.platformAuthRefresh as ReturnType<typeof vi.fn>).mockResolvedValue({
        access_token: "new-access",
        refresh_token: "new-refresh",
        token_type: "Bearer",
        expires_in: 3600,
      });

      const mgr = new TokenRefreshManager(
        tokenStorage as unknown as ITokenStorage,
        ipcClient as unknown as IpcClient,
        offlineManager as unknown as OfflineManager,
        onSignOut,
        logger
      );

      await mgr.start();
      // Flush immediate setTimeout(fn, 0)
      await vi.advanceTimersByTimeAsync(0);

      expect(ipcClient.platformAuthRefresh).toHaveBeenCalledTimes(1);

      mgr.dispose();
    });

    it("stays idle when no expiresAt is stored", async () => {
      const tokenStorage = createMockTokenStorage(); // no expiresAt
      const ipcClient = createMockIpcClient();
      const offlineManager = createMockOfflineManager("online");
      const onSignOut = vi.fn().mockResolvedValue(undefined);
      const logger = createMockLogger();

      const mgr = new TokenRefreshManager(
        tokenStorage as unknown as ITokenStorage,
        ipcClient as unknown as IpcClient,
        offlineManager as unknown as OfflineManager,
        onSignOut,
        logger
      );

      await mgr.start();
      await vi.advanceTimersByTimeAsync(60 * MINUTE);

      expect(ipcClient.platformAuthRefresh).not.toHaveBeenCalled();

      mgr.dispose();
    });
  });

  // =========================================================================
  // Refresh success path
  // =========================================================================
  describe("refresh success path", () => {
    it("stores new access token, refresh token, and expiresAt on success", async () => {
      const tokenStorage = createMockTokenStorage({
        expiresAt: expiresInMs(0), // expire immediately
        refreshToken: "old-refresh",
      });
      const ipcClient = createMockIpcClient();
      const offlineManager = createMockOfflineManager("online");
      const onSignOut = vi.fn().mockResolvedValue(undefined);
      const logger = createMockLogger();

      (ipcClient.platformAuthRefresh as ReturnType<typeof vi.fn>).mockResolvedValue({
        access_token: "new-access",
        refresh_token: "new-refresh",
        token_type: "Bearer",
        expires_in: 3600,
      });

      const mgr = new TokenRefreshManager(
        tokenStorage as unknown as ITokenStorage,
        ipcClient as unknown as IpcClient,
        offlineManager as unknown as OfflineManager,
        onSignOut,
        logger
      );

      await mgr.start();
      await vi.advanceTimersByTimeAsync(0);

      expect(tokenStorage.store).toHaveBeenCalledWith("accessToken", "new-access");
      expect(tokenStorage.store).toHaveBeenCalledWith("refreshToken", "new-refresh");
      // expiresAt stored as ISO string
      const expiresAtCalls = (tokenStorage.store as ReturnType<typeof vi.fn>).mock.calls.filter(
        (c: unknown[]) => c[0] === "expiresAt"
      );
      expect(expiresAtCalls).toHaveLength(1);
      expect(typeof expiresAtCalls[0][1]).toBe("string");

      mgr.dispose();
    });

    it("fires onRefreshSucceeded event on success", async () => {
      const tokenStorage = createMockTokenStorage({
        expiresAt: expiresInMs(0),
        refreshToken: "old-refresh",
      });
      const ipcClient = createMockIpcClient();
      const offlineManager = createMockOfflineManager("online");
      const onSignOut = vi.fn().mockResolvedValue(undefined);
      const logger = createMockLogger();

      (ipcClient.platformAuthRefresh as ReturnType<typeof vi.fn>).mockResolvedValue({
        access_token: "new-access",
        refresh_token: "new-refresh",
        token_type: "Bearer",
        expires_in: 3600,
      });

      const mgr = new TokenRefreshManager(
        tokenStorage as unknown as ITokenStorage,
        ipcClient as unknown as IpcClient,
        offlineManager as unknown as OfflineManager,
        onSignOut,
        logger
      );

      const successHandler = vi.fn();
      mgr.onRefreshSucceeded(successHandler);

      await mgr.start();
      await vi.advanceTimersByTimeAsync(0);

      expect(successHandler).toHaveBeenCalledTimes(1);

      mgr.dispose();
    });

    it("passes the stored refresh token to ipcClient.platformAuthRefresh", async () => {
      const tokenStorage = createMockTokenStorage({
        expiresAt: expiresInMs(0),
        refreshToken: "the-refresh-token",
      });
      const ipcClient = createMockIpcClient();
      const offlineManager = createMockOfflineManager("online");
      const onSignOut = vi.fn().mockResolvedValue(undefined);
      const logger = createMockLogger();

      (ipcClient.platformAuthRefresh as ReturnType<typeof vi.fn>).mockResolvedValue({
        access_token: "new-access",
        refresh_token: "new-refresh",
        token_type: "Bearer",
        expires_in: 3600,
      });

      const mgr = new TokenRefreshManager(
        tokenStorage as unknown as ITokenStorage,
        ipcClient as unknown as IpcClient,
        offlineManager as unknown as OfflineManager,
        onSignOut,
        logger
      );

      await mgr.start();
      await vi.advanceTimersByTimeAsync(0);

      expect(ipcClient.platformAuthRefresh).toHaveBeenCalledWith("the-refresh-token");

      mgr.dispose();
    });
  });

  // =========================================================================
  // Failure handling — expired refresh token
  // =========================================================================
  describe("failure handling", () => {
    it("calls onSignOut when refresh fails", async () => {
      const tokenStorage = createMockTokenStorage({
        expiresAt: expiresInMs(0),
        refreshToken: "expired-refresh",
      });
      const ipcClient = createMockIpcClient();
      const offlineManager = createMockOfflineManager("online");
      const onSignOut = vi.fn().mockResolvedValue(undefined);
      const logger = createMockLogger();

      (ipcClient.platformAuthRefresh as ReturnType<typeof vi.fn>).mockRejectedValue(
        new Error("401 Unauthorized")
      );

      const mgr = new TokenRefreshManager(
        tokenStorage as unknown as ITokenStorage,
        ipcClient as unknown as IpcClient,
        offlineManager as unknown as OfflineManager,
        onSignOut,
        logger
      );

      await mgr.start();
      await vi.advanceTimersByTimeAsync(0);

      expect(onSignOut).toHaveBeenCalledTimes(1);

      mgr.dispose();
    });

    it("fires onRefreshFailed event with the error on failure", async () => {
      const tokenStorage = createMockTokenStorage({
        expiresAt: expiresInMs(0),
        refreshToken: "expired-refresh",
      });
      const ipcClient = createMockIpcClient();
      const offlineManager = createMockOfflineManager("online");
      const onSignOut = vi.fn().mockResolvedValue(undefined);
      const logger = createMockLogger();

      // Realistic auth-fatal rejection: the platform returns 403 for an expired
      // refresh token, surfaced by the Go IPC layer as "unexpected status 403".
      const refreshError = new Error("IPC error FORBIDDEN: authRefresh: unexpected status 403");
      (ipcClient.platformAuthRefresh as ReturnType<typeof vi.fn>).mockRejectedValue(refreshError);

      const mgr = new TokenRefreshManager(
        tokenStorage as unknown as ITokenStorage,
        ipcClient as unknown as IpcClient,
        offlineManager as unknown as OfflineManager,
        onSignOut,
        logger
      );

      const failedHandler = vi.fn();
      mgr.onRefreshFailed(failedHandler);

      await mgr.start();
      await vi.advanceTimersByTimeAsync(0);

      expect(failedHandler).toHaveBeenCalledTimes(1);
      expect(failedHandler).toHaveBeenCalledWith(refreshError);

      mgr.dispose();
    });

    it("does not retry — onSignOut called exactly once per failure", async () => {
      const tokenStorage = createMockTokenStorage({
        expiresAt: expiresInMs(0),
        refreshToken: "expired-refresh",
      });
      const ipcClient = createMockIpcClient();
      const offlineManager = createMockOfflineManager("online");
      const onSignOut = vi.fn().mockResolvedValue(undefined);
      const logger = createMockLogger();

      (ipcClient.platformAuthRefresh as ReturnType<typeof vi.fn>).mockRejectedValue(
        new Error("401 Unauthorized")
      );

      const mgr = new TokenRefreshManager(
        tokenStorage as unknown as ITokenStorage,
        ipcClient as unknown as IpcClient,
        offlineManager as unknown as OfflineManager,
        onSignOut,
        logger
      );

      await mgr.start();
      await vi.advanceTimersByTimeAsync(0);

      // Advance extra time — no retry should occur
      await vi.advanceTimersByTimeAsync(10 * MINUTE);

      expect(ipcClient.platformAuthRefresh).toHaveBeenCalledTimes(1);
      expect(onSignOut).toHaveBeenCalledTimes(1);

      mgr.dispose();
    });

    it("shows warning message to user on refresh failure", async () => {
      const tokenStorage = createMockTokenStorage({
        expiresAt: expiresInMs(0),
        refreshToken: "expired-refresh",
      });
      const ipcClient = createMockIpcClient();
      const offlineManager = createMockOfflineManager("online");
      const onSignOut = vi.fn().mockResolvedValue(undefined);
      const logger = createMockLogger();

      (ipcClient.platformAuthRefresh as ReturnType<typeof vi.fn>).mockRejectedValue(
        new Error("401 Unauthorized")
      );

      const mgr = new TokenRefreshManager(
        tokenStorage as unknown as ITokenStorage,
        ipcClient as unknown as IpcClient,
        offlineManager as unknown as OfflineManager,
        onSignOut,
        logger
      );

      await mgr.start();
      await vi.advanceTimersByTimeAsync(0);

      expect(vscode.window.showWarningMessage).toHaveBeenCalledWith(
        "Nightgauge: Your session has expired. Please sign in again."
      );

      mgr.dispose();
    });
  });

  // =========================================================================
  // No refresh loop after sign-out
  // =========================================================================
  describe("no refresh loop after sign-out", () => {
    it("does not reschedule after sign-out clears tokens", async () => {
      const tokenStorage = createMockTokenStorage({
        expiresAt: expiresInMs(0),
        refreshToken: "expired-refresh",
      });
      const ipcClient = createMockIpcClient();
      const offlineManager = createMockOfflineManager("online");
      const logger = createMockLogger();

      // sign-out clears tokens (fires 'cleared' event)
      const onSignOut = vi.fn().mockImplementation(async () => {
        await tokenStorage.clear();
      });

      (ipcClient.platformAuthRefresh as ReturnType<typeof vi.fn>).mockRejectedValue(
        new Error("401 Unauthorized")
      );

      const mgr = new TokenRefreshManager(
        tokenStorage as unknown as ITokenStorage,
        ipcClient as unknown as IpcClient,
        offlineManager as unknown as OfflineManager,
        onSignOut,
        logger
      );

      await mgr.start();
      await vi.advanceTimersByTimeAsync(0); // fires refresh, fails → sign-out → tokens cleared

      // Advance more time — no timer should have been rescheduled
      await vi.advanceTimersByTimeAsync(60 * MINUTE);

      expect(ipcClient.platformAuthRefresh).toHaveBeenCalledTimes(1);

      mgr.dispose();
    });
  });

  // =========================================================================
  // Offline pause
  // =========================================================================
  describe("offline pause", () => {
    it("clears timer and sets paused when state transitions to non-online", async () => {
      const tokenStorage = createMockTokenStorage({
        expiresAt: expiresInMs(30 * MINUTE),
        refreshToken: "ref-token",
      });
      const ipcClient = createMockIpcClient();
      const offlineManager = createMockOfflineManager("online");
      const onSignOut = vi.fn().mockResolvedValue(undefined);
      const logger = createMockLogger();

      (ipcClient.platformAuthRefresh as ReturnType<typeof vi.fn>).mockResolvedValue({
        access_token: "new-access",
        refresh_token: "new-refresh",
        token_type: "Bearer",
        expires_in: 3600,
      });

      const mgr = new TokenRefreshManager(
        tokenStorage as unknown as ITokenStorage,
        ipcClient as unknown as IpcClient,
        offlineManager as unknown as OfflineManager,
        onSignOut,
        logger
      );

      await mgr.start();
      // Timer is scheduled for ~27 minutes from now (10% advance on 30 min = 3 min)

      // Go offline (degraded)
      offlineManager._fire({
        previous: "online",
        current: "degraded",
        at: new Date().toISOString(),
        reason: "test",
      });

      // Advance past when the timer would have fired
      await vi.advanceTimersByTimeAsync(30 * MINUTE);

      // Refresh should NOT have been called
      expect(ipcClient.platformAuthRefresh).not.toHaveBeenCalled();

      mgr.dispose();
    });

    it("does not schedule timer when started in offline state", async () => {
      const tokenStorage = createMockTokenStorage({
        expiresAt: expiresInMs(30 * MINUTE),
        refreshToken: "ref-token",
      });
      const ipcClient = createMockIpcClient();
      const offlineManager = createMockOfflineManager("offline"); // starts offline
      const onSignOut = vi.fn().mockResolvedValue(undefined);
      const logger = createMockLogger();

      const mgr = new TokenRefreshManager(
        tokenStorage as unknown as ITokenStorage,
        ipcClient as unknown as IpcClient,
        offlineManager as unknown as OfflineManager,
        onSignOut,
        logger
      );

      await mgr.start();
      await vi.advanceTimersByTimeAsync(60 * MINUTE);

      expect(ipcClient.platformAuthRefresh).not.toHaveBeenCalled();

      mgr.dispose();
    });

    it("does not schedule when paused and new token is stored", async () => {
      const tokenStorage = createMockTokenStorage({
        refreshToken: "ref-token",
      });
      const ipcClient = createMockIpcClient();
      const offlineManager = createMockOfflineManager("offline");
      const onSignOut = vi.fn().mockResolvedValue(undefined);
      const logger = createMockLogger();

      const mgr = new TokenRefreshManager(
        tokenStorage as unknown as ITokenStorage,
        ipcClient as unknown as IpcClient,
        offlineManager as unknown as OfflineManager,
        onSignOut,
        logger
      );

      await mgr.start(); // paused immediately

      // New token arrives while offline — should NOT schedule
      await tokenStorage.store("accessToken", "new-access");
      tokenStorage._values.set("expiresAt", expiresInMs(30 * MINUTE));

      await vi.advanceTimersByTimeAsync(60 * MINUTE);

      expect(ipcClient.platformAuthRefresh).not.toHaveBeenCalled();

      mgr.dispose();
    });
  });

  // =========================================================================
  // Offline resume
  // =========================================================================
  describe("offline resume", () => {
    it("reschedules when online event fires after being offline", async () => {
      const tokenStorage = createMockTokenStorage({
        expiresAt: expiresInMs(10 * MINUTE), // 10% = 1 min advance, delay = 9 min
        refreshToken: "ref-token",
      });
      const ipcClient = createMockIpcClient();
      const offlineManager = createMockOfflineManager("offline"); // start offline
      const onSignOut = vi.fn().mockResolvedValue(undefined);
      const logger = createMockLogger();

      (ipcClient.platformAuthRefresh as ReturnType<typeof vi.fn>).mockResolvedValue({
        access_token: "new-access",
        refresh_token: "new-refresh",
        token_type: "Bearer",
        expires_in: 3600,
      });

      const mgr = new TokenRefreshManager(
        tokenStorage as unknown as ITokenStorage,
        ipcClient as unknown as IpcClient,
        offlineManager as unknown as OfflineManager,
        onSignOut,
        logger
      );

      await mgr.start(); // paused

      // Still no refresh scheduled
      await vi.advanceTimersByTimeAsync(5 * MINUTE);
      expect(ipcClient.platformAuthRefresh).not.toHaveBeenCalled();

      // Now go online — should schedule refresh
      offlineManager._fire({
        previous: "offline",
        current: "online",
        at: new Date().toISOString(),
        reason: "recovered",
      });

      // Wait for async _scheduleNext to compute delay (no-op timer needed)
      await vi.advanceTimersByTimeAsync(0);

      // Advance past the 9-minute mark from the NEW expiresAt
      // (expiresAt is still ~5 min from now after the previous 5 min advance)
      // With 5 min remaining: 10% = 30s advance, delay = 4.5 min
      await vi.advanceTimersByTimeAsync(5 * MINUTE);

      expect(ipcClient.platformAuthRefresh).toHaveBeenCalledTimes(1);

      mgr.dispose();
    });
  });

  // =========================================================================
  // dispose() cleanup
  // =========================================================================
  describe("dispose() cleanup", () => {
    it("does not call _doRefresh after dispose", async () => {
      const tokenStorage = createMockTokenStorage({
        expiresAt: expiresInMs(10 * MINUTE), // delay = 9 min
        refreshToken: "ref-token",
      });
      const ipcClient = createMockIpcClient();
      const offlineManager = createMockOfflineManager("online");
      const onSignOut = vi.fn().mockResolvedValue(undefined);
      const logger = createMockLogger();

      const mgr = new TokenRefreshManager(
        tokenStorage as unknown as ITokenStorage,
        ipcClient as unknown as IpcClient,
        offlineManager as unknown as OfflineManager,
        onSignOut,
        logger
      );

      await mgr.start();

      // Dispose before timer fires
      mgr.dispose();

      // Advance past when the timer would have fired
      await vi.advanceTimersByTimeAsync(10 * MINUTE);

      expect(ipcClient.platformAuthRefresh).not.toHaveBeenCalled();
    });

    it("does not reschedule after dispose even if token changes arrive", async () => {
      const tokenStorage = createMockTokenStorage({
        expiresAt: expiresInMs(10 * MINUTE),
        refreshToken: "ref-token",
      });
      const ipcClient = createMockIpcClient();
      const offlineManager = createMockOfflineManager("online");
      const onSignOut = vi.fn().mockResolvedValue(undefined);
      const logger = createMockLogger();

      const mgr = new TokenRefreshManager(
        tokenStorage as unknown as ITokenStorage,
        ipcClient as unknown as IpcClient,
        offlineManager as unknown as OfflineManager,
        onSignOut,
        logger
      );

      await mgr.start();
      mgr.dispose();

      // Fire token change after dispose
      tokenStorage._fireChange({ key: "accessToken", action: "stored" });

      await vi.advanceTimersByTimeAsync(60 * MINUTE);

      expect(ipcClient.platformAuthRefresh).not.toHaveBeenCalled();
    });
  });

  // =========================================================================
  // Transient failure handling — retry with backoff, never sign out (#3751)
  // =========================================================================
  describe("transient failure handling", () => {
    const SUCCESS_RESPONSE = {
      access_token: "recovered-access",
      refresh_token: "recovered-refresh",
      token_type: "Bearer",
      expires_in: 3600,
    };

    it("retries a transient (network) failure with backoff instead of signing out", async () => {
      const tokenStorage = createMockTokenStorage({
        expiresAt: expiresInMs(0),
        refreshToken: "ref-token",
      });
      const ipcClient = createMockIpcClient();
      const offlineManager = createMockOfflineManager("online");
      const onSignOut = vi.fn().mockResolvedValue(undefined);
      const logger = createMockLogger();

      // First attempt: transient network error. Second attempt (after backoff): success.
      (ipcClient.platformAuthRefresh as ReturnType<typeof vi.fn>)
        .mockRejectedValueOnce(new Error("connect ECONNREFUSED 127.0.0.1:443"))
        .mockResolvedValueOnce(SUCCESS_RESPONSE);

      const mgr = new TokenRefreshManager(
        tokenStorage as unknown as ITokenStorage,
        ipcClient as unknown as IpcClient,
        offlineManager as unknown as OfflineManager,
        onSignOut,
        logger
      );

      await mgr.start();
      await vi.advanceTimersByTimeAsync(0); // first attempt fails (transient)

      expect(ipcClient.platformAuthRefresh).toHaveBeenCalledTimes(1);
      expect(onSignOut).not.toHaveBeenCalled();
      expect(vscode.window.showWarningMessage).not.toHaveBeenCalled();

      // Backoff is RETRY_BASE_MS = 5s for the first retry.
      await vi.advanceTimersByTimeAsync(5_000);

      expect(ipcClient.platformAuthRefresh).toHaveBeenCalledTimes(2);
      expect(onSignOut).not.toHaveBeenCalled();
      expect(tokenStorage.store).toHaveBeenCalledWith("accessToken", "recovered-access");

      mgr.dispose();
    });

    it("never signs out while transient failures persist (5xx)", async () => {
      const tokenStorage = createMockTokenStorage({
        expiresAt: expiresInMs(0),
        refreshToken: "ref-token",
      });
      const ipcClient = createMockIpcClient();
      const offlineManager = createMockOfflineManager("online");
      const onSignOut = vi.fn().mockResolvedValue(undefined);
      const logger = createMockLogger();

      (ipcClient.platformAuthRefresh as ReturnType<typeof vi.fn>).mockRejectedValue(
        new Error("IPC error INTERNAL: authRefresh: unexpected status 503")
      );

      const mgr = new TokenRefreshManager(
        tokenStorage as unknown as ITokenStorage,
        ipcClient as unknown as IpcClient,
        offlineManager as unknown as OfflineManager,
        onSignOut,
        logger
      );

      await mgr.start();
      // Drive many backoff cycles (5s, 10s, 20s, 40s, ...).
      await vi.advanceTimersByTimeAsync(0);
      for (let i = 0; i < 6; i++) {
        await vi.advanceTimersByTimeAsync(5 * MINUTE);
      }

      expect(
        (ipcClient.platformAuthRefresh as ReturnType<typeof vi.fn>).mock.calls.length
      ).toBeGreaterThan(2);
      expect(onSignOut).not.toHaveBeenCalled();
      expect(vscode.window.showWarningMessage).not.toHaveBeenCalled();

      mgr.dispose();
    });
  });

  // =========================================================================
  // Single-use-token dedup — concurrent refreshes share one request (#3751)
  // =========================================================================
  describe("concurrent refresh dedup", () => {
    it("collapses concurrent forceRefresh calls into a single platform request", async () => {
      const tokenStorage = createMockTokenStorage({
        expiresAt: expiresInMs(30 * MINUTE),
        refreshToken: "ref-token",
      });
      const ipcClient = createMockIpcClient();
      const offlineManager = createMockOfflineManager("online");
      const onSignOut = vi.fn().mockResolvedValue(undefined);
      const logger = createMockLogger();

      // Hold the refresh in-flight via a deferred promise so both callers overlap.
      let resolveRefresh!: (v: unknown) => void;
      (ipcClient.platformAuthRefresh as ReturnType<typeof vi.fn>).mockReturnValue(
        new Promise((res) => {
          resolveRefresh = res;
        })
      );

      const mgr = new TokenRefreshManager(
        tokenStorage as unknown as ITokenStorage,
        ipcClient as unknown as IpcClient,
        offlineManager as unknown as OfflineManager,
        onSignOut,
        logger
      );

      const p1 = mgr.forceRefresh();
      const p2 = mgr.forceRefresh();

      // Flush the microtasks (refresh-token retrieve) so the in-flight request
      // reaches platformAuthRefresh, then assert both callers share it.
      await vi.advanceTimersByTimeAsync(0);
      expect(ipcClient.platformAuthRefresh).toHaveBeenCalledTimes(1);

      resolveRefresh({
        access_token: "shared-access",
        refresh_token: "shared-refresh",
        token_type: "Bearer",
        expires_in: 3600,
      });

      const [t1, t2] = await Promise.all([p1, p2]);
      expect(t1).toBe("shared-access");
      expect(t2).toBe("shared-access");
      expect(ipcClient.platformAuthRefresh).toHaveBeenCalledTimes(1);

      mgr.dispose();
    });
  });

  // =========================================================================
  // forceRefresh failure semantics (#3751)
  // =========================================================================
  describe("forceRefresh failure semantics", () => {
    it("returns null without signing out on a transient failure", async () => {
      const tokenStorage = createMockTokenStorage({ refreshToken: "ref-token" });
      const ipcClient = createMockIpcClient();
      const offlineManager = createMockOfflineManager("online");
      const onSignOut = vi.fn().mockResolvedValue(undefined);
      const logger = createMockLogger();

      (ipcClient.platformAuthRefresh as ReturnType<typeof vi.fn>).mockRejectedValue(
        new Error("fetch failed: ETIMEDOUT")
      );

      const mgr = new TokenRefreshManager(
        tokenStorage as unknown as ITokenStorage,
        ipcClient as unknown as IpcClient,
        offlineManager as unknown as OfflineManager,
        onSignOut,
        logger
      );

      const token = await mgr.forceRefresh();
      expect(token).toBeNull();
      expect(onSignOut).not.toHaveBeenCalled();

      mgr.dispose();
    });

    it("signs out exactly once across repeated auth-fatal forceRefresh calls", async () => {
      const tokenStorage = createMockTokenStorage({ refreshToken: "dead-refresh" });
      const ipcClient = createMockIpcClient();
      const offlineManager = createMockOfflineManager("online");
      const onSignOut = vi.fn().mockResolvedValue(undefined);
      const logger = createMockLogger();

      (ipcClient.platformAuthRefresh as ReturnType<typeof vi.fn>).mockRejectedValue(
        new Error("IPC error UNAUTHORIZED: authRefresh: unexpected status 401")
      );

      const mgr = new TokenRefreshManager(
        tokenStorage as unknown as ITokenStorage,
        ipcClient as unknown as IpcClient,
        offlineManager as unknown as OfflineManager,
        onSignOut,
        logger
      );

      expect(await mgr.forceRefresh()).toBeNull();
      expect(await mgr.forceRefresh()).toBeNull();

      // Terminal handling is idempotent — one sign-out, one popup.
      expect(onSignOut).toHaveBeenCalledTimes(1);
      expect(vscode.window.showWarningMessage).toHaveBeenCalledTimes(1);

      mgr.dispose();
    });

    it("returns null without signing out when no refresh token is stored", async () => {
      const tokenStorage = createMockTokenStorage(); // no refresh token
      const ipcClient = createMockIpcClient();
      const offlineManager = createMockOfflineManager("online");
      const onSignOut = vi.fn().mockResolvedValue(undefined);
      const logger = createMockLogger();

      const mgr = new TokenRefreshManager(
        tokenStorage as unknown as ITokenStorage,
        ipcClient as unknown as IpcClient,
        offlineManager as unknown as OfflineManager,
        onSignOut,
        logger
      );

      const token = await mgr.forceRefresh();
      expect(token).toBeNull();
      expect(ipcClient.platformAuthRefresh).not.toHaveBeenCalled();
      expect(onSignOut).not.toHaveBeenCalled();

      mgr.dispose();
    });
  });
});
