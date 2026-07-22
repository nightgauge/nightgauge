/**
 * hostSwapReAuth.test.ts
 *
 * Tests covering host-swap re-authentication scenarios (#3723):
 * - SessionManager transitions to unauthenticated on onPlatformHostChanged
 * - TokenRefreshManager clears timer on onPlatformHostChanged
 * - TokenRefreshManager discards in-flight refresh result after host change
 * - PlatformSseClient calls onSignInRequired when onAuthRequired returns null
 * - PlatformSseClient does not call onSignInRequired on second consecutive 401 (no-loop)
 */

import { describe, it, expect, vi } from "vitest";

// ---------------------------------------------------------------------------
// vscode mock
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

  return { EventEmitter: MockEventEmitter };
});

import { SessionManager } from "../../src/platform/SessionManager";
import { TokenRefreshManager } from "../../src/platform/TokenRefreshManager";
import { PlatformSseClient } from "../../src/services/PlatformSseClient";
import type { ITokenStorage, TokenKey } from "../../src/platform/TokenStorage";
import type { TokenRefreshManager as TRM } from "../../src/platform/TokenRefreshManager";
import type { OAuthDeviceFlowService } from "../../src/services/OAuthDeviceFlowService";
import type { GitHubAuthService } from "../../src/services/GitHubAuthService";
import type { Logger } from "../../src/utils/logger";
import type { PlatformHostChangedEvent } from "../../src/services/ConfigBridge";
import type { OfflineManager } from "../../src/platform/OfflineManager";
import type { IpcClient } from "../../src/services/IpcClient";
import * as vscode from "vscode";

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

function createMockLogger(): Logger {
  return { info: vi.fn(), debug: vi.fn(), warn: vi.fn(), error: vi.fn() } as unknown as Logger;
}

function createMockConfigBridge() {
  const emitter = new vscode.EventEmitter<PlatformHostChangedEvent>();
  return {
    onPlatformHostChanged: emitter.event,
    _fire: (evt: PlatformHostChangedEvent) => emitter.fire(evt),
    getPlatform: vi.fn(() => undefined),
    getEffectiveConfig: vi.fn(() => null),
  };
}

function createMockTokenStorage(): ITokenStorage & {
  _fireChange: (evt: { key: string; action: string }) => void;
} {
  const emitter = new vscode.EventEmitter<{ key: string; action: string }>();
  return {
    onTokenChanged: emitter.event,
    _fireChange: (evt) => emitter.fire(evt as never),
    store: vi.fn(async () => {}),
    retrieve: vi.fn(async () => null),
    delete: vi.fn(async () => {}),
    clear: vi.fn(async () => {}),
    dispose: vi.fn(),
    migrateFromLegacy: vi.fn(async () => {}),
  } as unknown as ITokenStorage & { _fireChange: (evt: { key: string; action: string }) => void };
}

function createMockEventService() {
  const succeededEmitter = new vscode.EventEmitter<void>();
  const failedEmitter = new vscode.EventEmitter<Error>();
  const signedInEmitter = new vscode.EventEmitter<void>();
  const signedOutEmitter = new vscode.EventEmitter<void>();
  return {
    onRefreshSucceeded: succeededEmitter.event,
    onRefreshFailed: failedEmitter.event,
    onSignedIn: signedInEmitter.event,
    onSignedOut: signedOutEmitter.event,
  };
}

// ---------------------------------------------------------------------------
// SessionManager — host-swap tests
// ---------------------------------------------------------------------------

describe("SessionManager — host-swap re-auth", () => {
  it("transitions to unauthenticated on onPlatformHostChanged", async () => {
    const configBridge = createMockConfigBridge();
    const tokenStorage = createMockTokenStorage();
    // Pre-load access token so restore() sets authenticated
    (tokenStorage.retrieve as ReturnType<typeof vi.fn>).mockResolvedValueOnce("tok");

    const tokenRefreshManager = createMockEventService();
    const oauthService = createMockEventService();
    const githubService = createMockEventService();
    const logger = createMockLogger();

    const mgr = new SessionManager(
      tokenStorage as unknown as ITokenStorage,
      tokenRefreshManager as unknown as TRM,
      oauthService as unknown as OAuthDeviceFlowService,
      githubService as unknown as GitHubAuthService,
      logger,
      configBridge as never
    );

    await mgr.restore();
    expect(mgr.state).toBe("authenticated");

    const events: string[] = [];
    mgr.onSessionChanged((e) => events.push(e.current));

    configBridge._fire({ previousHost: "production", newHost: "canary" });

    expect(mgr.state).toBe("unauthenticated");

    // Wait for async event emission
    await new Promise((r) => setTimeout(r, 10));
    expect(events).toContain("unauthenticated");

    mgr.dispose();
  });

  it("no-op when configBridge is not provided (backward compat)", () => {
    const tokenStorage = createMockTokenStorage();
    const tokenRefreshManager = createMockEventService();
    const oauthService = createMockEventService();
    const githubService = createMockEventService();
    const logger = createMockLogger();

    // Should not throw when configBridge is omitted
    const mgr = new SessionManager(
      tokenStorage as unknown as ITokenStorage,
      tokenRefreshManager as unknown as TRM,
      oauthService as unknown as OAuthDeviceFlowService,
      githubService as unknown as GitHubAuthService,
      logger
    );

    expect(mgr.state).toBe("unauthenticated");
    mgr.dispose();
  });
});

// ---------------------------------------------------------------------------
// TokenRefreshManager — host-swap tests
// ---------------------------------------------------------------------------

describe("TokenRefreshManager — host-swap re-auth", () => {
  function buildTRM(opts: {
    getHostKey?: () => string;
    configBridge?: ReturnType<typeof createMockConfigBridge>;
    retrieve?: (key: TokenKey) => Promise<string | null>;
  }) {
    const tokenStorage = createMockTokenStorage();
    if (opts.retrieve) {
      (tokenStorage.retrieve as ReturnType<typeof vi.fn>).mockImplementation(opts.retrieve);
    }

    const offlineManager = {
      state: "online" as const,
      onStateChanged: new vscode.EventEmitter<{ current: string }>().event,
    } as unknown as OfflineManager;

    const ipcClient = {
      platformAuthRefresh: vi.fn(async () => ({
        access_token: "new-token",
        refresh_token: "new-refresh",
        expires_in: 3600,
      })),
    } as unknown as IpcClient;

    const logger = createMockLogger();

    const trm = new TokenRefreshManager(
      tokenStorage as unknown as ITokenStorage,
      ipcClient,
      offlineManager,
      vi.fn(async () => {}),
      logger,
      opts.getHostKey,
      opts.configBridge as never
    );

    return { trm, tokenStorage, ipcClient, logger };
  }

  it("clears timer on onPlatformHostChanged", () => {
    const configBridge = createMockConfigBridge();
    const { trm } = buildTRM({ getHostKey: () => "production", configBridge });

    // Inject a fake timer (simulate a scheduled refresh)
    (trm as unknown as { _timer: ReturnType<typeof setTimeout> | null })._timer = setTimeout(
      () => {},
      100_000
    );

    configBridge._fire({ previousHost: "production", newHost: "canary" });

    expect((trm as unknown as { _timer: ReturnType<typeof setTimeout> | null })._timer).toBeNull();

    trm.dispose();
  });

  it("discards in-flight refresh result after host change (host snapshot guard)", async () => {
    const configBridge = createMockConfigBridge();
    let hostKey = "production";

    let resolveRefresh!: (v: {
      access_token: string;
      refresh_token: string;
      expires_in: number;
    }) => void;
    const refreshPromise = new Promise<{
      access_token: string;
      refresh_token: string;
      expires_in: number;
    }>((r) => (resolveRefresh = r));

    const ipcClient = { platformAuthRefresh: vi.fn(() => refreshPromise) } as unknown as IpcClient;
    const tokenStorage = createMockTokenStorage();
    (tokenStorage.retrieve as ReturnType<typeof vi.fn>).mockResolvedValue("refresh-tok");

    const offlineManager = {
      state: "online" as const,
      onStateChanged: new vscode.EventEmitter<{ current: string }>().event,
    } as unknown as OfflineManager;

    const trm = new TokenRefreshManager(
      tokenStorage as unknown as ITokenStorage,
      ipcClient,
      offlineManager,
      vi.fn(async () => {}),
      createMockLogger(),
      () => hostKey,
      configBridge as never
    );

    // Start force refresh — it will hang on the refresh promise
    const forceRefreshPromise = trm.forceRefresh();

    // Simulate host change before the refresh resolves
    hostKey = "canary";
    configBridge._fire({ previousHost: "production", newHost: "canary" });

    // Now resolve the refresh with a response
    resolveRefresh({ access_token: "new-token", refresh_token: "new-refresh", expires_in: 3600 });

    const result = await forceRefreshPromise;

    // The result should be discarded because host changed mid-flight
    expect(result).toBeNull();
    // The new access token must NOT have been stored
    expect(tokenStorage.store).not.toHaveBeenCalledWith("accessToken", "new-token");

    trm.dispose();
  });
});

// ---------------------------------------------------------------------------
// PlatformSseClient — onSignInRequired tests
// ---------------------------------------------------------------------------

describe("PlatformSseClient — onSignInRequired on 401", () => {
  function buildSseClient(opts: {
    onAuthRequired?: () => Promise<string | null>;
    onSignInRequired?: () => void;
  }) {
    const context = {
      globalState: {
        get: vi.fn(() => undefined),
        update: vi.fn(async () => {}),
      },
    } as unknown as vscode.ExtensionContext;

    return new PlatformSseClient({
      context,
      logger: createMockLogger(),
      lastEventIdKey: "test.lastEventId",
      onEvent: vi.fn(),
      onStatusChanged: vi.fn(),
      onAuthRequired: opts.onAuthRequired ?? vi.fn(async () => null),
      onSignInRequired: opts.onSignInRequired,
    });
  }

  it("calls onSignInRequired when onAuthRequired returns null on first 401", async () => {
    const onSignInRequired = vi.fn();
    const client = buildSseClient({
      onAuthRequired: vi.fn(async () => null),
      onSignInRequired,
    });

    // Directly invoke _handleAuthError via the private method (cast to any)
    await (client as unknown as { _handleAuthError(): Promise<void> })._handleAuthError();

    expect(onSignInRequired).toHaveBeenCalledOnce();

    client.dispose();
  });

  it("does not call onSignInRequired on second consecutive 401 (no-loop guarantee)", async () => {
    const onSignInRequired = vi.fn();
    const client = buildSseClient({
      onAuthRequired: vi.fn(async () => null),
      onSignInRequired,
    });

    const priv = client as unknown as {
      _handleAuthError(): Promise<void>;
      _consecutiveAuthErrors: number;
    };

    // Simulate first 401 already consumed
    priv._consecutiveAuthErrors = 1;

    // Second 401 — should hit the >= 2 guard and not call onSignInRequired
    await priv._handleAuthError();

    expect(onSignInRequired).not.toHaveBeenCalled();

    client.dispose();
  });

  it("does not call onSignInRequired when token refresh succeeds", async () => {
    const onSignInRequired = vi.fn();
    const client = buildSseClient({
      onAuthRequired: vi.fn(async () => "new-token"),
      onSignInRequired,
    });

    // Prime _currentUrl so reconnect doesn't short-circuit
    const priv = client as unknown as {
      _handleAuthError(): Promise<void>;
      _currentUrl: string | null;
      _disposed: boolean;
    };
    priv._currentUrl = null;
    priv._disposed = true; // prevent actual connect after token refresh

    await priv._handleAuthError();

    expect(onSignInRequired).not.toHaveBeenCalled();

    client.dispose();
  });
});
