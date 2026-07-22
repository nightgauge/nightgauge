/**
 * SessionManager.test.ts
 *
 * Unit tests covering:
 * - Initial state (unauthenticated before restore())
 * - restore() with token → transitions to authenticated
 * - restore() without token → stays unauthenticated, no event
 * - OAuthDeviceFlowService.onSignedIn → authenticated
 * - OAuthDeviceFlowService.onSignedOut → unauthenticated
 * - GitHubAuthService.onSignedIn → authenticated
 * - GitHubAuthService.onSignedOut → unauthenticated
 * - TokenRefreshManager.onRefreshSucceeded → authenticated (data update)
 * - TokenRefreshManager.onRefreshFailed → error
 * - TokenStorage cleared event → unauthenticated
 * - onSessionChanged event shape (previous, current, data, reason)
 * - dispose() guard — _transition after dispose does not emit event
 * - dispose() cleans up subscriptions (no memory leaks)
 *
 * @see Issue #1468 - Build Session State Manager with Auth State Machine
 */

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import type { TokenChangeEvent } from "../../src/platform/types";

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

  return {
    EventEmitter: MockEventEmitter,
  };
});

import { SessionManager } from "../../src/platform/SessionManager";
import type { ITokenStorage, TokenKey } from "../../src/platform/TokenStorage";
import type { TokenRefreshManager } from "../../src/platform/TokenRefreshManager";
import type { OAuthDeviceFlowService } from "../../src/services/OAuthDeviceFlowService";
import type { GitHubAuthService } from "../../src/services/GitHubAuthService";
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

  const emitter = new vscode.EventEmitter<TokenChangeEvent>();

  return {
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
}

/** Create a minimal mock TokenRefreshManager */
function createMockTokenRefreshManager(): {
  onRefreshSucceeded: vscode.Event<void>;
  onRefreshFailed: vscode.Event<Error>;
  _fireSucceeded: () => void;
  _fireFailed: (err: Error) => void;
  dispose: () => void;
} {
  const succeededEmitter = new vscode.EventEmitter<void>();
  const failedEmitter = new vscode.EventEmitter<Error>();
  return {
    onRefreshSucceeded: succeededEmitter.event,
    onRefreshFailed: failedEmitter.event,
    _fireSucceeded: () => succeededEmitter.fire(),
    _fireFailed: (err: Error) => failedEmitter.fire(err),
    dispose: () => {
      succeededEmitter.dispose();
      failedEmitter.dispose();
    },
  };
}

/** Create a minimal mock OAuthDeviceFlowService */
function createMockOAuthDeviceFlowService(): {
  onSignedIn: vscode.Event<void>;
  onSignedOut: vscode.Event<void>;
  _fireSignedIn: () => void;
  _fireSignedOut: () => void;
  dispose: () => void;
} {
  const signedInEmitter = new vscode.EventEmitter<void>();
  const signedOutEmitter = new vscode.EventEmitter<void>();
  return {
    onSignedIn: signedInEmitter.event,
    onSignedOut: signedOutEmitter.event,
    _fireSignedIn: () => signedInEmitter.fire(),
    _fireSignedOut: () => signedOutEmitter.fire(),
    dispose: () => {
      signedInEmitter.dispose();
      signedOutEmitter.dispose();
    },
  };
}

/** Create a minimal mock GitHubAuthService */
function createMockGitHubAuthService(): {
  onSignedIn: vscode.Event<void>;
  onSignedOut: vscode.Event<void>;
  _fireSignedIn: () => void;
  _fireSignedOut: () => void;
  dispose: () => void;
} {
  const signedInEmitter = new vscode.EventEmitter<void>();
  const signedOutEmitter = new vscode.EventEmitter<void>();
  return {
    onSignedIn: signedInEmitter.event,
    onSignedOut: signedOutEmitter.event,
    _fireSignedIn: () => signedInEmitter.fire(),
    _fireSignedOut: () => signedOutEmitter.fire(),
    dispose: () => {
      signedInEmitter.dispose();
      signedOutEmitter.dispose();
    },
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

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

describe("SessionManager", () => {
  // =========================================================================
  // Initial state
  // =========================================================================
  describe("initial state", () => {
    it("starts in unauthenticated state before restore()", () => {
      const tokenStorage = createMockTokenStorage();
      const tokenRefreshManager = createMockTokenRefreshManager();
      const oauthService = createMockOAuthDeviceFlowService();
      const githubService = createMockGitHubAuthService();
      const logger = createMockLogger();

      const mgr = new SessionManager(
        tokenStorage as unknown as ITokenStorage,
        tokenRefreshManager as unknown as TokenRefreshManager,
        oauthService as unknown as OAuthDeviceFlowService,
        githubService as unknown as GitHubAuthService,
        logger
      );

      expect(mgr.state).toBe("unauthenticated");
      mgr.dispose();
    });
  });

  // =========================================================================
  // restore()
  // =========================================================================
  describe("restore()", () => {
    it("transitions to authenticated when access token exists in storage", async () => {
      const tokenStorage = createMockTokenStorage({
        accessToken: "tok-abc",
      });
      const tokenRefreshManager = createMockTokenRefreshManager();
      const oauthService = createMockOAuthDeviceFlowService();
      const githubService = createMockGitHubAuthService();
      const logger = createMockLogger();

      const mgr = new SessionManager(
        tokenStorage as unknown as ITokenStorage,
        tokenRefreshManager as unknown as TokenRefreshManager,
        oauthService as unknown as OAuthDeviceFlowService,
        githubService as unknown as GitHubAuthService,
        logger
      );

      const eventHandler = vi.fn();
      mgr.onSessionChanged(eventHandler);

      await mgr.restore();

      expect(mgr.state).toBe("authenticated");
      // Wait for fire-and-forget async event emission
      await Promise.resolve();
      await Promise.resolve();
      expect(eventHandler).toHaveBeenCalledTimes(1);
      expect(eventHandler).toHaveBeenCalledWith(
        expect.objectContaining({
          previous: "unauthenticated",
          current: "authenticated",
          reason: "session restored from storage",
        })
      );

      mgr.dispose();
    });

    it("stays unauthenticated when no access token in storage", async () => {
      const tokenStorage = createMockTokenStorage(); // no tokens
      const tokenRefreshManager = createMockTokenRefreshManager();
      const oauthService = createMockOAuthDeviceFlowService();
      const githubService = createMockGitHubAuthService();
      const logger = createMockLogger();

      const mgr = new SessionManager(
        tokenStorage as unknown as ITokenStorage,
        tokenRefreshManager as unknown as TokenRefreshManager,
        oauthService as unknown as OAuthDeviceFlowService,
        githubService as unknown as GitHubAuthService,
        logger
      );

      const eventHandler = vi.fn();
      mgr.onSessionChanged(eventHandler);

      await mgr.restore();

      expect(mgr.state).toBe("unauthenticated");
      await Promise.resolve();
      expect(eventHandler).not.toHaveBeenCalled();

      mgr.dispose();
    });

    it("is a no-op when called after dispose()", async () => {
      const tokenStorage = createMockTokenStorage({ accessToken: "tok" });
      const tokenRefreshManager = createMockTokenRefreshManager();
      const oauthService = createMockOAuthDeviceFlowService();
      const githubService = createMockGitHubAuthService();
      const logger = createMockLogger();

      const mgr = new SessionManager(
        tokenStorage as unknown as ITokenStorage,
        tokenRefreshManager as unknown as TokenRefreshManager,
        oauthService as unknown as OAuthDeviceFlowService,
        githubService as unknown as GitHubAuthService,
        logger
      );

      mgr.dispose();
      await mgr.restore();

      // State stays unauthenticated (transition was blocked by _disposed guard)
      expect(mgr.state).toBe("unauthenticated");
    });
  });

  // =========================================================================
  // OAuthDeviceFlowService events
  // =========================================================================
  describe("OAuthDeviceFlowService events", () => {
    it("transitions to authenticated on onSignedIn", async () => {
      const tokenStorage = createMockTokenStorage({ accessToken: "tok" });
      const tokenRefreshManager = createMockTokenRefreshManager();
      const oauthService = createMockOAuthDeviceFlowService();
      const githubService = createMockGitHubAuthService();
      const logger = createMockLogger();

      const mgr = new SessionManager(
        tokenStorage as unknown as ITokenStorage,
        tokenRefreshManager as unknown as TokenRefreshManager,
        oauthService as unknown as OAuthDeviceFlowService,
        githubService as unknown as GitHubAuthService,
        logger
      );

      oauthService._fireSignedIn();

      expect(mgr.state).toBe("authenticated");
      mgr.dispose();
    });

    it("transitions to unauthenticated on onSignedOut", async () => {
      const tokenStorage = createMockTokenStorage({ accessToken: "tok" });
      const tokenRefreshManager = createMockTokenRefreshManager();
      const oauthService = createMockOAuthDeviceFlowService();
      const githubService = createMockGitHubAuthService();
      const logger = createMockLogger();

      const mgr = new SessionManager(
        tokenStorage as unknown as ITokenStorage,
        tokenRefreshManager as unknown as TokenRefreshManager,
        oauthService as unknown as OAuthDeviceFlowService,
        githubService as unknown as GitHubAuthService,
        logger
      );

      // Start authenticated
      oauthService._fireSignedIn();
      expect(mgr.state).toBe("authenticated");

      // Sign out
      oauthService._fireSignedOut();
      expect(mgr.state).toBe("unauthenticated");

      mgr.dispose();
    });
  });

  // =========================================================================
  // GitHubAuthService events
  // =========================================================================
  describe("GitHubAuthService events", () => {
    it("transitions to authenticated on onSignedIn", () => {
      const tokenStorage = createMockTokenStorage({ accessToken: "tok" });
      const tokenRefreshManager = createMockTokenRefreshManager();
      const oauthService = createMockOAuthDeviceFlowService();
      const githubService = createMockGitHubAuthService();
      const logger = createMockLogger();

      const mgr = new SessionManager(
        tokenStorage as unknown as ITokenStorage,
        tokenRefreshManager as unknown as TokenRefreshManager,
        oauthService as unknown as OAuthDeviceFlowService,
        githubService as unknown as GitHubAuthService,
        logger
      );

      githubService._fireSignedIn();

      expect(mgr.state).toBe("authenticated");
      mgr.dispose();
    });

    it("transitions to unauthenticated on onSignedOut", () => {
      const tokenStorage = createMockTokenStorage({ accessToken: "tok" });
      const tokenRefreshManager = createMockTokenRefreshManager();
      const oauthService = createMockOAuthDeviceFlowService();
      const githubService = createMockGitHubAuthService();
      const logger = createMockLogger();

      const mgr = new SessionManager(
        tokenStorage as unknown as ITokenStorage,
        tokenRefreshManager as unknown as TokenRefreshManager,
        oauthService as unknown as OAuthDeviceFlowService,
        githubService as unknown as GitHubAuthService,
        logger
      );

      githubService._fireSignedIn();
      expect(mgr.state).toBe("authenticated");

      githubService._fireSignedOut();
      expect(mgr.state).toBe("unauthenticated");

      mgr.dispose();
    });
  });

  // =========================================================================
  // TokenRefreshManager events
  // =========================================================================
  describe("TokenRefreshManager events", () => {
    it("fires onSessionChanged with current:authenticated on onRefreshSucceeded", async () => {
      const tokenStorage = createMockTokenStorage({ accessToken: "tok" });
      const tokenRefreshManager = createMockTokenRefreshManager();
      const oauthService = createMockOAuthDeviceFlowService();
      const githubService = createMockGitHubAuthService();
      const logger = createMockLogger();

      const mgr = new SessionManager(
        tokenStorage as unknown as ITokenStorage,
        tokenRefreshManager as unknown as TokenRefreshManager,
        oauthService as unknown as OAuthDeviceFlowService,
        githubService as unknown as GitHubAuthService,
        logger
      );

      // Start authenticated and drain the async event emission before subscribing
      oauthService._fireSignedIn();
      expect(mgr.state).toBe("authenticated");
      for (let i = 0; i < 6; i++) await Promise.resolve(); // drain first transition

      const eventHandler = vi.fn();
      mgr.onSessionChanged(eventHandler);

      tokenRefreshManager._fireSucceeded();

      expect(mgr.state).toBe("authenticated");
      // Drain microtasks for fire-and-forget _readSessionData().then() chain
      for (let i = 0; i < 6; i++) await Promise.resolve();
      expect(eventHandler).toHaveBeenCalledTimes(1);
      expect(eventHandler).toHaveBeenCalledWith(
        expect.objectContaining({
          current: "authenticated",
          reason: "token refresh succeeded",
        })
      );

      mgr.dispose();
    });

    it("transitions to error on onRefreshFailed", () => {
      const tokenStorage = createMockTokenStorage({ accessToken: "tok" });
      const tokenRefreshManager = createMockTokenRefreshManager();
      const oauthService = createMockOAuthDeviceFlowService();
      const githubService = createMockGitHubAuthService();
      const logger = createMockLogger();

      const mgr = new SessionManager(
        tokenStorage as unknown as ITokenStorage,
        tokenRefreshManager as unknown as TokenRefreshManager,
        oauthService as unknown as OAuthDeviceFlowService,
        githubService as unknown as GitHubAuthService,
        logger
      );

      oauthService._fireSignedIn();
      tokenRefreshManager._fireFailed(new Error("refresh failed"));

      expect(mgr.state).toBe("error");
      mgr.dispose();
    });
  });

  // =========================================================================
  // TokenStorage cleared event
  // =========================================================================
  describe("TokenStorage cleared event", () => {
    it("transitions to unauthenticated when tokens are cleared", () => {
      const tokenStorage = createMockTokenStorage({ accessToken: "tok" });
      const tokenRefreshManager = createMockTokenRefreshManager();
      const oauthService = createMockOAuthDeviceFlowService();
      const githubService = createMockGitHubAuthService();
      const logger = createMockLogger();

      const mgr = new SessionManager(
        tokenStorage as unknown as ITokenStorage,
        tokenRefreshManager as unknown as TokenRefreshManager,
        oauthService as unknown as OAuthDeviceFlowService,
        githubService as unknown as GitHubAuthService,
        logger
      );

      oauthService._fireSignedIn();
      expect(mgr.state).toBe("authenticated");

      tokenStorage._fireChange({ key: "all", action: "cleared" });
      expect(mgr.state).toBe("unauthenticated");

      mgr.dispose();
    });

    it("does NOT transition on individual token store/delete events", () => {
      const tokenStorage = createMockTokenStorage({ accessToken: "tok" });
      const tokenRefreshManager = createMockTokenRefreshManager();
      const oauthService = createMockOAuthDeviceFlowService();
      const githubService = createMockGitHubAuthService();
      const logger = createMockLogger();

      const mgr = new SessionManager(
        tokenStorage as unknown as ITokenStorage,
        tokenRefreshManager as unknown as TokenRefreshManager,
        oauthService as unknown as OAuthDeviceFlowService,
        githubService as unknown as GitHubAuthService,
        logger
      );

      oauthService._fireSignedIn();
      expect(mgr.state).toBe("authenticated");

      tokenStorage._fireChange({ key: "accessToken", action: "stored" });
      expect(mgr.state).toBe("authenticated"); // no change

      tokenStorage._fireChange({ key: "refreshToken", action: "deleted" });
      expect(mgr.state).toBe("authenticated"); // no change

      mgr.dispose();
    });
  });

  // =========================================================================
  // onSessionChanged event shape
  // =========================================================================
  describe("onSessionChanged event shape", () => {
    it("emits { previous, current, data: { accessToken, expiresAt }, reason }", async () => {
      const tokenStorage = createMockTokenStorage({
        accessToken: "access-123",
        expiresAt: "2026-03-11T23:00:00Z",
      });
      const tokenRefreshManager = createMockTokenRefreshManager();
      const oauthService = createMockOAuthDeviceFlowService();
      const githubService = createMockGitHubAuthService();
      const logger = createMockLogger();

      const mgr = new SessionManager(
        tokenStorage as unknown as ITokenStorage,
        tokenRefreshManager as unknown as TokenRefreshManager,
        oauthService as unknown as OAuthDeviceFlowService,
        githubService as unknown as GitHubAuthService,
        logger
      );

      const eventHandler = vi.fn();
      mgr.onSessionChanged(eventHandler);

      oauthService._fireSignedIn();

      // Drain microtasks for fire-and-forget _readSessionData().then() chain
      for (let i = 0; i < 6; i++) await Promise.resolve();

      expect(eventHandler).toHaveBeenCalledTimes(1);
      const evt = eventHandler.mock.calls[0][0];
      expect(evt.previous).toBe("unauthenticated");
      expect(evt.current).toBe("authenticated");
      expect(evt.reason).toBe("device-flow sign-in succeeded");
      expect(evt.data.accessToken).toBe("access-123");
      expect(evt.data.expiresAt).toBe("2026-03-11T23:00:00Z");

      mgr.dispose();
    });
  });

  // =========================================================================
  // isAuthenticated()
  // =========================================================================
  describe("isAuthenticated()", () => {
    it("returns true when access token exists in storage", async () => {
      const tokenStorage = createMockTokenStorage({ accessToken: "tok" });
      const tokenRefreshManager = createMockTokenRefreshManager();
      const oauthService = createMockOAuthDeviceFlowService();
      const githubService = createMockGitHubAuthService();
      const logger = createMockLogger();

      const mgr = new SessionManager(
        tokenStorage as unknown as ITokenStorage,
        tokenRefreshManager as unknown as TokenRefreshManager,
        oauthService as unknown as OAuthDeviceFlowService,
        githubService as unknown as GitHubAuthService,
        logger
      );

      expect(await mgr.isAuthenticated()).toBe(true);
      mgr.dispose();
    });

    it("returns false when no access token in storage", async () => {
      const tokenStorage = createMockTokenStorage();
      const tokenRefreshManager = createMockTokenRefreshManager();
      const oauthService = createMockOAuthDeviceFlowService();
      const githubService = createMockGitHubAuthService();
      const logger = createMockLogger();

      const mgr = new SessionManager(
        tokenStorage as unknown as ITokenStorage,
        tokenRefreshManager as unknown as TokenRefreshManager,
        oauthService as unknown as OAuthDeviceFlowService,
        githubService as unknown as GitHubAuthService,
        logger
      );

      expect(await mgr.isAuthenticated()).toBe(false);
      mgr.dispose();
    });
  });

  // =========================================================================
  // dispose() guards
  // =========================================================================
  describe("dispose() guards", () => {
    it("does not emit onSessionChanged after dispose()", async () => {
      const tokenStorage = createMockTokenStorage({ accessToken: "tok" });
      const tokenRefreshManager = createMockTokenRefreshManager();
      const oauthService = createMockOAuthDeviceFlowService();
      const githubService = createMockGitHubAuthService();
      const logger = createMockLogger();

      const mgr = new SessionManager(
        tokenStorage as unknown as ITokenStorage,
        tokenRefreshManager as unknown as TokenRefreshManager,
        oauthService as unknown as OAuthDeviceFlowService,
        githubService as unknown as GitHubAuthService,
        logger
      );

      const eventHandler = vi.fn();
      mgr.onSessionChanged(eventHandler);

      mgr.dispose();

      // Fire events after dispose — no transitions should occur
      oauthService._fireSignedIn();
      githubService._fireSignedIn();
      tokenRefreshManager._fireSucceeded();

      await Promise.resolve();
      await Promise.resolve();

      expect(eventHandler).not.toHaveBeenCalled();
    });

    it("state remains unauthenticated after dispose when events fire", () => {
      const tokenStorage = createMockTokenStorage({ accessToken: "tok" });
      const tokenRefreshManager = createMockTokenRefreshManager();
      const oauthService = createMockOAuthDeviceFlowService();
      const githubService = createMockGitHubAuthService();
      const logger = createMockLogger();

      const mgr = new SessionManager(
        tokenStorage as unknown as ITokenStorage,
        tokenRefreshManager as unknown as TokenRefreshManager,
        oauthService as unknown as OAuthDeviceFlowService,
        githubService as unknown as GitHubAuthService,
        logger
      );

      mgr.dispose();
      oauthService._fireSignedIn();

      expect(mgr.state).toBe("unauthenticated");
    });
  });
});
