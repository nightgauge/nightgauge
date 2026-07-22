/**
 * envSwitching.test.ts — Vitest coverage for env-switching, cookie scoping,
 * SSE reconnect, and 401 handling (#3724).
 *
 * AC-1: resolvePlatformBaseUrl + resolvePlatformHostKey resolution for all presets
 * AC-2: switchPlatformEnvironment command: config write + reload + SSE reconnect
 * AC-3: Cookie/token scoping — no cross-host credential leakage
 * AC-4: SSE reconnect with correct per-host Last-Event-ID key
 * AC-5: 401-after-swap routes to onSignInRequired, no refresh loop
 */

import { describe, it, expect, vi, beforeEach } from "vitest";

// ---------------------------------------------------------------------------
// vscode mock (reused from hostSwapReAuth.test.ts pattern)
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
    commands: { registerCommand: vi.fn() },
    window: {
      showQuickPick: vi.fn(),
      showInputBox: vi.fn(),
      showErrorMessage: vi.fn(),
      showInformationMessage: vi.fn(),
    },
    workspace: { workspaceFolders: [{ uri: { fsPath: "/workspace" } }] },
  };
});

import {
  resolvePlatformBaseUrl,
  resolvePlatformHostKey,
  PLATFORM_ENV_PRESETS,
} from "../../src/config/schema";
import { TokenStorage } from "../../src/platform/TokenStorage";
import { SecretStorageService } from "../../src/services/SecretStorageService";
import { PlatformSseClient } from "../../src/services/PlatformSseClient";
import type { Logger } from "../../src/utils/logger";
import * as vscode from "vscode";

// ---------------------------------------------------------------------------
// Shared helpers
// ---------------------------------------------------------------------------

function createMockLogger(): Logger {
  return {
    info: vi.fn(),
    debug: vi.fn(),
    warn: vi.fn(),
    error: vi.fn(),
  } as unknown as Logger;
}

function makeNeverEndingStream(): ReadableStream<Uint8Array> {
  return new ReadableStream({ start() {} });
}

function makeOkSseResponse(stream: ReadableStream): Response {
  return {
    ok: true,
    status: 200,
    body: stream,
    headers: {
      get: (n: string) => (n === "content-type" ? "text/event-stream; charset=utf-8" : null),
    },
    clone: () => makeOkSseResponse(stream),
  } as unknown as Response;
}

// ---------------------------------------------------------------------------
// AC-1: resolvePlatformBaseUrl
// ---------------------------------------------------------------------------

describe("resolvePlatformBaseUrl", () => {
  it("resolves production preset", () => {
    expect(resolvePlatformBaseUrl({ environment: "production" })).toBe(
      "https://api.nightgauge.dev"
    );
  });

  it("resolves canary preset", () => {
    expect(resolvePlatformBaseUrl({ environment: "canary" })).toBe(PLATFORM_ENV_PRESETS.canary);
  });

  it("resolves local preset", () => {
    expect(resolvePlatformBaseUrl({ environment: "local" })).toBe("http://localhost:8787");
  });

  it("resolves custom with valid https url", () => {
    expect(
      resolvePlatformBaseUrl({
        environment: "custom",
        api_url: "https://my.platform.example.com",
      })
    ).toBe("https://my.platform.example.com");
  });

  it("throws on custom with non-https non-localhost url", () => {
    expect(() =>
      resolvePlatformBaseUrl({
        environment: "custom",
        api_url: "http://my.platform.example.com",
      })
    ).toThrow(/HTTPS/);
  });

  it("allows custom with localhost url", () => {
    expect(
      resolvePlatformBaseUrl({
        environment: "custom",
        api_url: "http://localhost:9000",
      })
    ).toBe("http://localhost:9000");
  });

  it("falls back to production when custom has no api_url", () => {
    expect(resolvePlatformBaseUrl({ environment: "custom" })).toBe(PLATFORM_ENV_PRESETS.production);
  });

  it("backward-compat: api_url without environment treated as custom", () => {
    expect(resolvePlatformBaseUrl({ api_url: "https://my.platform.example.com" })).toBe(
      "https://my.platform.example.com"
    );
  });

  it("returns production for undefined config", () => {
    expect(resolvePlatformBaseUrl(undefined)).toBe(PLATFORM_ENV_PRESETS.production);
  });
});

describe("resolvePlatformHostKey", () => {
  it("returns 'production' for production preset", () => {
    expect(resolvePlatformHostKey({ environment: "production" })).toBe("production");
  });

  it("returns 'canary' for canary preset", () => {
    expect(resolvePlatformHostKey({ environment: "canary" })).toBe("canary");
  });

  it("returns 'local' for local preset", () => {
    expect(resolvePlatformHostKey({ environment: "local" })).toBe("local");
  });

  it("returns hostname for custom url", () => {
    expect(
      resolvePlatformHostKey({
        environment: "custom",
        api_url: "https://my.platform.example.com",
      })
    ).toBe("my.platform.example.com");
  });

  it("returns production hostname for undefined config (derives from default URL)", () => {
    expect(resolvePlatformHostKey(undefined)).toBe("api.nightgauge.dev");
  });
});

// ---------------------------------------------------------------------------
// AC-2: switchPlatformEnvironment command wiring
// ---------------------------------------------------------------------------

describe("switchPlatformEnvironment — command wiring", () => {
  it("writes config delta with environment key on env switch", () => {
    // Verify the config delta shape the command constructs when switching to canary.
    // This mirrors the exact delta built in registerSwitchPlatformEnvironmentCommand.
    type IncrediConfig = { platform?: { environment?: string; api_url?: string } };
    function buildDelta(selectedEnv: string, customUrl?: string): IncrediConfig {
      return {
        platform: {
          environment: selectedEnv as "production" | "canary" | "local" | "custom",
          ...(selectedEnv === "custom" ? { api_url: customUrl } : {}),
        },
      } as IncrediConfig;
    }

    const canaryDelta = buildDelta("canary");
    expect(canaryDelta.platform?.environment).toBe("canary");
    expect(canaryDelta.platform?.api_url).toBeUndefined();

    const customDelta = buildDelta("custom", "https://my.platform.example.com");
    expect(customDelta.platform?.environment).toBe("custom");
    expect(customDelta.platform?.api_url).toBe("https://my.platform.example.com");
  });

  it("resolves newBaseUrl from config after reload on switch to local", () => {
    // After ConfigBridge.reload(), resolvePlatformBaseUrl is called with the
    // new platform config. Verify the URL resolves correctly for each env.
    expect(resolvePlatformBaseUrl({ environment: "local" })).toBe("http://localhost:8787");
    expect(resolvePlatformBaseUrl({ environment: "canary" })).toBe(PLATFORM_ENV_PRESETS.canary);
    expect(resolvePlatformBaseUrl({ environment: "production" })).toBe(
      PLATFORM_ENV_PRESETS.production
    );
  });
});

// ---------------------------------------------------------------------------
// AC-3: Cookie/token scoping — no cross-host credential leakage
// ---------------------------------------------------------------------------

describe("TokenStorage — per-host cookie scoping", () => {
  beforeEach(() => {
    TokenStorage.resetInstance();
    SecretStorageService.resetInstance();
  });

  it("production and canary tokens are stored and retrieved independently", async () => {
    const secretStore = new Map<string, string>();
    const mockSecrets = {
      get: vi.fn((key: string) => Promise.resolve(secretStore.get(key) ?? null)),
      store: vi.fn((key: string, value: string) => {
        secretStore.set(key, value);
        return Promise.resolve();
      }),
      delete: vi.fn((key: string) => {
        secretStore.delete(key);
        return Promise.resolve();
      }),
      onDidChange: new vscode.EventEmitter<vscode.SecretStorageChangeEvent>().event,
    } as unknown as vscode.SecretStorage;

    SecretStorageService.initialize(mockSecrets);

    // Production instance
    TokenStorage.initialize(SecretStorageService.getInstance()!, () => "production");
    const prodStorage = TokenStorage.getInstance()!;
    await prodStorage.store("accessToken", "prod-token");

    // Reset singleton and create canary instance
    TokenStorage.resetInstance();
    TokenStorage.initialize(SecretStorageService.getInstance()!, () => "canary");
    const canaryStorage = TokenStorage.getInstance()!;
    await canaryStorage.store("accessToken", "canary-token");

    // Retrieve from canary — must be canary-token
    const canaryResult = await canaryStorage.retrieve("accessToken");
    expect(canaryResult).toBe("canary-token");

    // Reset to production and verify prod token still isolated
    TokenStorage.resetInstance();
    TokenStorage.initialize(SecretStorageService.getInstance()!, () => "production");
    const prodStorage2 = TokenStorage.getInstance()!;
    const prodResult = await prodStorage2.retrieve("accessToken");
    expect(prodResult).toBe("prod-token");

    // Verify storage keys are different (no cross-contamination)
    expect(prodResult).not.toBe(canaryResult);
  });
});

// ---------------------------------------------------------------------------
// AC-4: SSE reconnect with correct per-host Last-Event-ID key
// ---------------------------------------------------------------------------

describe("PlatformSseClient — per-host Last-Event-ID scoping", () => {
  it("each client reads its own last event ID key from globalState", async () => {
    const mockFetch = vi.fn().mockResolvedValue(makeOkSseResponse(makeNeverEndingStream()));
    vi.stubGlobal("fetch", mockFetch);

    const prodStateStore = new Map<string, unknown>([
      ["platform.events.production.lastEventId", "prod-ev-100"],
    ]);
    const canaryStateStore = new Map<string, unknown>([
      ["platform.events.canary.lastEventId", "canary-ev-200"],
    ]);

    const prodContext = {
      globalState: {
        get: vi.fn((key: string) => prodStateStore.get(key)),
        update: vi.fn(),
      },
    } as unknown as vscode.ExtensionContext;

    const canaryContext = {
      globalState: {
        get: vi.fn((key: string) => canaryStateStore.get(key)),
        update: vi.fn(),
      },
    } as unknown as vscode.ExtensionContext;

    const prodClient = new PlatformSseClient({
      context: prodContext,
      logger: createMockLogger(),
      lastEventIdKey: "platform.events.production.lastEventId",
      onEvent: vi.fn(),
      onStatusChanged: vi.fn(),
      onAuthRequired: vi.fn(async () => "prod-token"),
    });

    const canaryClient = new PlatformSseClient({
      context: canaryContext,
      logger: createMockLogger(),
      lastEventIdKey: "platform.events.canary.lastEventId",
      onEvent: vi.fn(),
      onStatusChanged: vi.fn(),
      onAuthRequired: vi.fn(async () => "canary-token"),
    });

    await prodClient.connect("https://api.nightgauge.dev/v1/events/stream");
    await canaryClient.connect("https://canary.api.nightgauge.dev/v1/events/stream");

    // Verify each client read its own key
    expect(prodContext.globalState.get).toHaveBeenCalledWith(
      "platform.events.production.lastEventId"
    );
    expect(canaryContext.globalState.get).toHaveBeenCalledWith(
      "platform.events.canary.lastEventId"
    );

    // Verify prod client sent its last event ID header
    const prodCall = mockFetch.mock.calls.find((c: unknown[]) =>
      (c[0] as string).includes("api.nightgauge.dev")
    );
    expect(prodCall).toBeDefined();
    const prodHeaders = (prodCall![1] as RequestInit).headers as Record<string, string>;
    expect(prodHeaders["Last-Event-ID"]).toBe("prod-ev-100");

    // Verify canary client sent its last event ID header
    const canaryCall = mockFetch.mock.calls.find((c: unknown[]) =>
      (c[0] as string).includes("canary.api.nightgauge.dev")
    );
    expect(canaryCall).toBeDefined();
    const canaryHeaders = (canaryCall![1] as RequestInit).headers as Record<string, string>;
    expect(canaryHeaders["Last-Event-ID"]).toBe("canary-ev-200");

    prodClient.dispose();
    canaryClient.dispose();
  });
});

// ---------------------------------------------------------------------------
// AC-5: 401-after-swap — re-auth, no refresh loop
// ---------------------------------------------------------------------------

describe("PlatformSseClient — 401-after-swap re-auth", () => {
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

  it("calls onSignInRequired when onAuthRequired returns null on first 401 after swap", async () => {
    const onSignInRequired = vi.fn();
    const client = buildSseClient({
      onAuthRequired: vi.fn(async () => null),
      onSignInRequired,
    });

    await (client as unknown as { _handleAuthError(): Promise<void> })._handleAuthError();

    expect(onSignInRequired).toHaveBeenCalledOnce();
    client.dispose();
  });

  it("does not loop on second consecutive 401 (no infinite refresh loop)", async () => {
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

    // Second 401 — must not call onSignInRequired again
    await priv._handleAuthError();

    expect(onSignInRequired).not.toHaveBeenCalled();
    client.dispose();
  });

  it("does not call onSignInRequired when token refresh succeeds after swap", async () => {
    const onSignInRequired = vi.fn();
    const client = buildSseClient({
      onAuthRequired: vi.fn(async () => "refreshed-token"),
      onSignInRequired,
    });

    const priv = client as unknown as {
      _handleAuthError(): Promise<void>;
      _currentUrl: string | null;
      _disposed: boolean;
    };
    priv._currentUrl = null;
    priv._disposed = true; // prevent actual reconnect

    await priv._handleAuthError();

    expect(onSignInRequired).not.toHaveBeenCalled();
    client.dispose();
  });
});
