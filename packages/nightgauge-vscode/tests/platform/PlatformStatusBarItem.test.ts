/**
 * PlatformStatusBarItem unit tests.
 *
 * Verifies status bar updates for all state transitions, disabled mode,
 * and connection detail rendering.
 *
 * After Issue #2091, the status bar no longer subscribes to
 * PlatformApiClient.onConnectionStateChanged (removed). It starts as
 * 'connected' when platform is enabled and relies on SessionManager
 * for auth state updates.
 *
 * @see Issue #1461 - Add platform connection status indicator to status bar
 * @see Issue #2091 - Remove PlatformApiClient HTTP code and consolidate types
 */

import { describe, it, expect, vi, beforeEach } from "vitest";

// ---------------------------------------------------------------------------
// VSCode mock — factory must be self-contained (vi.mock is hoisted)
// ---------------------------------------------------------------------------

vi.mock("vscode", () => {
  class InternalEventEmitter<T> {
    listeners: Array<(e: T) => void> = [];
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
      for (const l of this.listeners) l(data);
    };
    dispose = () => {};
  }

  return {
    EventEmitter: InternalEventEmitter,
    StatusBarAlignment: { Left: 1, Right: 2 },
    ThemeColor: class {
      constructor(public readonly id: string) {}
    },
    window: {
      createStatusBarItem: vi.fn(() => ({
        text: "",
        tooltip: "",
        backgroundColor: undefined as unknown,
        command: undefined as unknown,
        show: vi.fn(),
        hide: vi.fn(),
        dispose: vi.fn(),
      })),
    },
  };
});

import * as vscode from "vscode";
import { PlatformStatusBarItem } from "../../src/platform/PlatformStatusBarItem";
import type { PlatformConfig } from "../../src/config/schema";

/** Matches the ConnectionState type defined locally in PlatformStatusBarItem.ts */
type ConnectionState = "connected" | "disconnected" | "degraded";

/** Matches the ConnectionStateEmitter interface in PlatformStatusBarItem.ts */
interface ConnectionStateEmitter {
  getConnectionState(): ConnectionState;
  onConnectionStateChanged: vscode.Event<ConnectionState>;
  onRateLimitRetry: vscode.Event<{ retryInSeconds: number; attempt: number }>;
}

// ---------------------------------------------------------------------------
// Local MockEventEmitter (NOT inside vi.mock — used in test bodies)
// ---------------------------------------------------------------------------

class MockEventEmitter<T> {
  readonly listeners: Array<(e: T) => void> = [];

  readonly event = (listener: (e: T) => void) => {
    this.listeners.push(listener);
    return {
      dispose: () => {
        const idx = this.listeners.indexOf(listener);
        if (idx >= 0) this.listeners.splice(idx, 1);
      },
    };
  };

  fire(data: T) {
    for (const l of [...this.listeners]) l(data);
  }
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

type MockItem = {
  text: string;
  tooltip: string;
  backgroundColor: unknown;
  command: unknown;
  show: ReturnType<typeof vi.fn>;
  hide: ReturnType<typeof vi.fn>;
  dispose: ReturnType<typeof vi.fn>;
};

function createMockItem(): MockItem {
  return {
    text: "",
    tooltip: "",
    backgroundColor: undefined,
    command: undefined,
    show: vi.fn(),
    hide: vi.fn(),
    dispose: vi.fn(),
  };
}

function createMockClient(initialState: ConnectionState = "disconnected"): {
  client: ConnectionStateEmitter;
  emitter: MockEventEmitter<ConnectionState>;
  rateLimitEmitter: MockEventEmitter<{
    retryInSeconds: number;
    attempt: number;
  }>;
  fireRateLimit: (retryInSeconds: number) => void;
} {
  const emitter = new MockEventEmitter<ConnectionState>();
  const rateLimitEmitter = new MockEventEmitter<{
    retryInSeconds: number;
    attempt: number;
  }>();
  const client: ConnectionStateEmitter = {
    getConnectionState: vi.fn(() => initialState),
    onConnectionStateChanged: emitter.event,
    onRateLimitRetry: rateLimitEmitter.event,
  };

  return {
    client,
    emitter,
    rateLimitEmitter,
    fireRateLimit: (retryInSeconds) => rateLimitEmitter.fire({ retryInSeconds, attempt: 0 }),
  };
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

describe("PlatformStatusBarItem", () => {
  let currentMockItem: MockItem;

  beforeEach(() => {
    vi.clearAllMocks();
    currentMockItem = createMockItem();
    (vscode.window.createStatusBarItem as ReturnType<typeof vi.fn>).mockReturnValue(
      currentMockItem
    );
  });

  describe("initial state", () => {
    it('shows "Platform: Disabled" when platform.enabled = false', () => {
      const config: PlatformConfig = { enabled: false };
      const sbi = new PlatformStatusBarItem(null, config);
      expect(sbi.getDisplayState()).toBe("disabled");
      expect(currentMockItem.text).toContain("Platform: Disabled");
      sbi.dispose();
    });

    it('shows "Platform: Connected" when platform is enabled (default)', () => {
      const { client } = createMockClient("connected");
      const sbi = new PlatformStatusBarItem(client, undefined);
      expect(sbi.getDisplayState()).toBe("connected");
      expect(currentMockItem.text).toContain("Platform: Connected");
      sbi.dispose();
    });

    it('shows "Platform: Offline" when initial state is disconnected', () => {
      const { client } = createMockClient("disconnected");
      const sbi = new PlatformStatusBarItem(client, undefined);
      expect(sbi.getDisplayState()).toBe("offline");
      expect(currentMockItem.text).toContain("Platform: Offline");
      sbi.dispose();
    });

    it("updates display state on connection state change", () => {
      const { client, emitter } = createMockClient("connected");
      const sbi = new PlatformStatusBarItem(client, undefined);
      expect(sbi.getDisplayState()).toBe("connected");

      emitter.fire("disconnected");
      expect(sbi.getDisplayState()).toBe("offline");
      expect(currentMockItem.text).toContain("Platform: Offline");

      emitter.fire("degraded");
      expect(sbi.getDisplayState()).toBe("degraded");
      expect(currentMockItem.text).toContain("Platform: Degraded");

      emitter.fire("connected");
      expect(sbi.getDisplayState()).toBe("connected");
      expect(currentMockItem.text).toContain("Platform: Connected");

      sbi.dispose();
    });

    it("calls item.show() after construction", () => {
      const { client } = createMockClient("connected");
      const sbi = new PlatformStatusBarItem(client, undefined);
      expect(currentMockItem.show).toHaveBeenCalled();
      sbi.dispose();
    });
  });

  describe("disabled mode", () => {
    it("works with null client when disabled", () => {
      const config: PlatformConfig = { enabled: false };
      const sbi = new PlatformStatusBarItem(null, config);
      expect(sbi.getDisplayState()).toBe("disabled");
      sbi.dispose();
    });
  });

  describe("connection details", () => {
    it("includes API URL in connection details", () => {
      const { client } = createMockClient("connected");
      const config: PlatformConfig = {
        enabled: true,
        api_url: "https://custom.example.com",
      };
      const sbi = new PlatformStatusBarItem(client, config);
      const details = sbi.getConnectionDetails();
      expect(details).toContain("https://custom.example.com");
      sbi.dispose();
    });

    it("uses default API URL when not configured", () => {
      const { client } = createMockClient("connected");
      const sbi = new PlatformStatusBarItem(client, undefined);
      const details = sbi.getConnectionDetails();
      expect(details).toContain("https://api.nightgauge.dev");
      sbi.dispose();
    });

    it("shows state label and URL in connection details", () => {
      const { client } = createMockClient("connected");
      const sbi = new PlatformStatusBarItem(client, undefined);
      const details = sbi.getConnectionDetails();
      expect(details).toContain("URL:");
      expect(details).toContain("https://api.nightgauge.dev");
      sbi.dispose();
    });
  });

  describe("visual styling", () => {
    it("applies no background color for connected state", () => {
      const { client } = createMockClient("connected");
      const sbi = new PlatformStatusBarItem(client, undefined);
      expect(currentMockItem.backgroundColor).toBeUndefined();
      sbi.dispose();
    });
  });

  describe("dispose", () => {
    it("disposes the status bar item", () => {
      const { client } = createMockClient("connected");
      const sbi = new PlatformStatusBarItem(client, undefined);

      sbi.dispose();

      expect(currentMockItem.dispose).toHaveBeenCalled();
    });
  });

  // ---------------------------------------------------------------------------
  // Auth state indicators (Issue #1469)
  // ---------------------------------------------------------------------------

  type MockSessionEvent = {
    previous: string;
    current: string;
    data: {
      accessToken: string | null;
      expiresAt: string | null;
      userEmail: string | null;
      userTier: string | null;
    };
    reason: string;
  };

  function createMockSessionManager(): {
    sessionEmitter: MockEventEmitter<MockSessionEvent>;
    sessionManager: {
      onSessionChanged: ReturnType<MockEventEmitter<MockSessionEvent>["event"]>;
    };
    fireSession: (evt: MockSessionEvent) => void;
  } {
    const sessionEmitter = new MockEventEmitter<MockSessionEvent>();
    const sessionManager = {
      onSessionChanged: sessionEmitter.event,
    } as unknown as {
      onSessionChanged: ReturnType<MockEventEmitter<MockSessionEvent>["event"]>;
    };
    return {
      sessionEmitter,
      sessionManager,
      fireSession: (evt) => sessionEmitter.fire(evt),
    };
  }

  describe("auth state indicators", () => {
    it('shows "Connected (signed out)" when connected and unauthenticated', () => {
      const { client } = createMockClient("connected");
      const { sessionManager, fireSession } = createMockSessionManager();
      const sbi = new PlatformStatusBarItem(client, undefined, sessionManager as any);

      fireSession({
        previous: "unauthenticated",
        current: "unauthenticated",
        data: {
          accessToken: null,
          expiresAt: null,
          userEmail: null,
          userTier: null,
        },
        reason: "test",
      });

      expect(currentMockItem.text).toContain("Sign In");
      sbi.dispose();
    });

    it('shows "Signed In" when authenticated but no email', () => {
      const { client } = createMockClient("connected");
      const { sessionManager, fireSession } = createMockSessionManager();
      const sbi = new PlatformStatusBarItem(client, undefined, sessionManager as any);

      fireSession({
        previous: "unauthenticated",
        current: "authenticated",
        data: {
          accessToken: "tok",
          expiresAt: null,
          userEmail: null,
          userTier: null,
        },
        reason: "test",
      });

      expect(currentMockItem.text).toContain("Signed In");
      sbi.dispose();
    });

    it("shows user email when authenticated with email", () => {
      const { client } = createMockClient("connected");
      const { sessionManager, fireSession } = createMockSessionManager();
      const sbi = new PlatformStatusBarItem(client, undefined, sessionManager as any);

      fireSession({
        previous: "unauthenticated",
        current: "authenticated",
        data: {
          accessToken: "tok",
          expiresAt: null,
          userEmail: "me@example.com",
          userTier: null,
        },
        reason: "test",
      });

      expect(currentMockItem.text).toContain("me@example.com");
      sbi.dispose();
    });

    it("shows tier in parens after email", () => {
      const { client } = createMockClient("connected");
      const { sessionManager, fireSession } = createMockSessionManager();
      const sbi = new PlatformStatusBarItem(client, undefined, sessionManager as any);

      fireSession({
        previous: "unauthenticated",
        current: "authenticated",
        data: {
          accessToken: "tok",
          expiresAt: null,
          userEmail: "me@example.com",
          userTier: "pro",
        },
        reason: "test",
      });

      expect(currentMockItem.text).toContain("me@example.com");
      expect(currentMockItem.text).toContain("Pro");
      sbi.dispose();
    });

    it("isAuthenticated() returns true when authenticated", () => {
      const { client } = createMockClient("connected");
      const { sessionManager, fireSession } = createMockSessionManager();
      const sbi = new PlatformStatusBarItem(client, undefined, sessionManager as any);

      fireSession({
        previous: "unauthenticated",
        current: "authenticated",
        data: {
          accessToken: "tok",
          expiresAt: null,
          userEmail: null,
          userTier: null,
        },
        reason: "test",
      });

      expect(sbi.isAuthenticated()).toBe(true);
      sbi.dispose();
    });

    it("isAuthenticated() returns false when unauthenticated", () => {
      const { client } = createMockClient("connected");
      const sbi = new PlatformStatusBarItem(client, undefined);
      expect(sbi.isAuthenticated()).toBe(false);
      sbi.dispose();
    });

    it("unsubscribes from session events on dispose", () => {
      const { client } = createMockClient("connected");
      const { sessionEmitter, sessionManager } = createMockSessionManager();
      const sbi = new PlatformStatusBarItem(client, undefined, sessionManager as any);

      sbi.dispose();

      expect(sessionEmitter.listeners).toHaveLength(0);
    });

    it("works without sessionManager (undefined)", () => {
      const { client } = createMockClient("connected");
      const sbi = new PlatformStatusBarItem(client, undefined);
      expect(sbi.getSessionState()).toBeNull();
      sbi.dispose();
    });
  });

  describe("trial countdown (#1138)", () => {
    function authenticate(trialStatus: unknown): { item: MockItem; dispose: () => void } {
      const { client } = createMockClient("connected");
      const sessionEmitter = new MockEventEmitter<{
        current: string;
        data: unknown;
      }>();
      const sessionManager = {
        onSessionChanged: sessionEmitter.event,
      } as never;
      const trialStore = { status: () => trialStatus } as never;
      const sbi = new PlatformStatusBarItem(client, undefined, sessionManager, trialStore);
      sessionEmitter.fire({
        current: "authenticated",
        data: { userEmail: "dev@example.com", userTier: "pro" },
      });
      return { item: currentMockItem, dispose: () => sbi.dispose() };
    }

    it("shows a days-left badge while a trial is active", () => {
      const { item, dispose } = authenticate({
        active: true,
        expired: false,
        daysRemaining: 12,
        record: { runAllowance: 50 },
      });
      expect(item.text).toContain("dev@example.com");
      expect(item.text).toContain("Trial · 12d left");
      expect(item.tooltip).toContain("Free Pro trial");
      expect(item.tooltip).toContain("50 runs");
      dispose();
    });

    it("shows an expired badge once the trial lapses", () => {
      const { item, dispose } = authenticate({
        active: false,
        expired: true,
        daysRemaining: 0,
        record: { runAllowance: 50 },
      });
      expect(item.text).toContain("(Trial expired)");
      expect(item.tooltip).toContain("upgrade to keep Pro");
      dispose();
    });

    it("falls back to the plain tier label with no trial", () => {
      const { item, dispose } = authenticate(null);
      expect(item.text).toContain("(Pro)");
      expect(item.text).not.toContain("Trial");
      dispose();
    });
  });
});
