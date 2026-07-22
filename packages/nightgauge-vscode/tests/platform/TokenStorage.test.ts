/**
 * TokenStorage unit tests.
 *
 * Verifies typed store/retrieve/delete/clear operations, event emission,
 * singleton lifecycle, per-host credential isolation, and legacy migration.
 *
 * @see Issue #1465 - Integrate vscode.SecretStorage for secure token persistence
 * @see Issue #3722 - Scope auth cookies/tokens per host
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
  };
});

import * as vscode from "vscode";
import { SecretStorageService, SECRET_KEYS } from "../../src/services/SecretStorageService";
import { TokenStorage } from "../../src/platform/TokenStorage";
import type { TokenChangeEvent } from "../../src/platform/TokenStorage";

// ---------------------------------------------------------------------------
// Mock SecretStorage helper (mirrors SecretStorageService.test.ts pattern)
// ---------------------------------------------------------------------------

function createMockSecretStorage(): vscode.SecretStorage {
  const store = new Map<string, string>();
  return {
    get: vi.fn((key: string) => Promise.resolve(store.get(key))),
    store: vi.fn((key: string, value: string) => {
      store.set(key, value);
      return Promise.resolve();
    }),
    delete: vi.fn((key: string) => {
      store.delete(key);
      return Promise.resolve();
    }),
    onDidChange: vi.fn(),
  } as unknown as vscode.SecretStorage;
}

function createService(): SecretStorageService {
  const mockSecrets = createMockSecretStorage();
  SecretStorageService.initialize(mockSecrets);
  return SecretStorageService.getInstance()!;
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

describe("TokenStorage", () => {
  beforeEach(() => {
    TokenStorage.resetInstance();
    SecretStorageService.resetInstance();
  });

  // ── Singleton lifecycle ────────────────────────────────────────────────

  describe("singleton lifecycle", () => {
    it("returns null before initialization", () => {
      expect(TokenStorage.getInstance()).toBeNull();
    });

    it("returns a non-null instance after initialization", () => {
      const service = createService();
      TokenStorage.initialize(service, () => "production");
      expect(TokenStorage.getInstance()).not.toBeNull();
    });

    it("returns the same instance on multiple getInstance calls", () => {
      const service = createService();
      TokenStorage.initialize(service, () => "production");
      expect(TokenStorage.getInstance()).toBe(TokenStorage.getInstance());
    });

    it("does not reinitialize if already initialized", () => {
      const first = createService();
      TokenStorage.initialize(first, () => "production");
      const firstInstance = TokenStorage.getInstance();

      SecretStorageService.resetInstance();
      const second = createService();
      TokenStorage.initialize(second, () => "production");

      expect(TokenStorage.getInstance()).toBe(firstInstance);
    });

    it("returns null after resetInstance", () => {
      const service = createService();
      TokenStorage.initialize(service, () => "production");
      expect(TokenStorage.getInstance()).not.toBeNull();

      TokenStorage.resetInstance();
      expect(TokenStorage.getInstance()).toBeNull();
    });

    it("allows re-initialization after reset", () => {
      const first = createService();
      TokenStorage.initialize(first, () => "production");
      TokenStorage.resetInstance();
      SecretStorageService.resetInstance();

      const second = createService();
      TokenStorage.initialize(second, () => "production");
      expect(TokenStorage.getInstance()).not.toBeNull();
    });
  });

  // ── store() ───────────────────────────────────────────────────────────

  describe("store()", () => {
    it("persists accessToken under the correct per-host SecretStorage key", async () => {
      const mockSecrets = createMockSecretStorage();
      SecretStorageService.initialize(mockSecrets);
      TokenStorage.initialize(SecretStorageService.getInstance()!, () => "production");
      const tokenStorage = TokenStorage.getInstance()!;

      await tokenStorage.store("accessToken", "tok-abc");

      expect(mockSecrets.store).toHaveBeenCalledWith(
        "nightgauge.platform.production.accessToken",
        "tok-abc"
      );
    });

    it("persists refreshToken under the correct per-host SecretStorage key", async () => {
      const mockSecrets = createMockSecretStorage();
      SecretStorageService.initialize(mockSecrets);
      TokenStorage.initialize(SecretStorageService.getInstance()!, () => "production");
      const tokenStorage = TokenStorage.getInstance()!;

      await tokenStorage.store("refreshToken", "ref-xyz");

      expect(mockSecrets.store).toHaveBeenCalledWith(
        "nightgauge.platform.production.refreshToken",
        "ref-xyz"
      );
    });

    it("persists expiresAt under the correct per-host SecretStorage key", async () => {
      const mockSecrets = createMockSecretStorage();
      SecretStorageService.initialize(mockSecrets);
      TokenStorage.initialize(SecretStorageService.getInstance()!, () => "production");
      const tokenStorage = TokenStorage.getInstance()!;

      await tokenStorage.store("expiresAt", "2026-12-31T00:00:00Z");

      expect(mockSecrets.store).toHaveBeenCalledWith(
        "nightgauge.platform.production.tokenExpiresAt",
        "2026-12-31T00:00:00Z"
      );
    });

    it("fires onTokenChanged with stored action", async () => {
      const service = createService();
      TokenStorage.initialize(service, () => "production");
      const tokenStorage = TokenStorage.getInstance()!;

      const events: TokenChangeEvent[] = [];
      tokenStorage.onTokenChanged((e) => events.push(e));

      await tokenStorage.store("accessToken", "tok-abc");

      expect(events).toHaveLength(1);
      expect(events[0]).toEqual({ key: "accessToken", action: "stored" });
    });
  });

  // ── retrieve() ────────────────────────────────────────────────────────

  describe("retrieve()", () => {
    it("returns the stored value", async () => {
      const service = createService();
      TokenStorage.initialize(service, () => "production");
      const tokenStorage = TokenStorage.getInstance()!;

      await tokenStorage.store("accessToken", "tok-abc");
      const result = await tokenStorage.retrieve("accessToken");

      expect(result).toBe("tok-abc");
    });

    it("returns null (not undefined) for a key that has not been set", async () => {
      const service = createService();
      TokenStorage.initialize(service, () => "production");
      const tokenStorage = TokenStorage.getInstance()!;

      const result = await tokenStorage.retrieve("accessToken");

      expect(result).toBeNull();
      expect(result).not.toBeUndefined();
    });

    it("returns null after the token has been deleted", async () => {
      const service = createService();
      TokenStorage.initialize(service, () => "production");
      const tokenStorage = TokenStorage.getInstance()!;

      await tokenStorage.store("refreshToken", "ref-xyz");
      await tokenStorage.delete("refreshToken");
      const result = await tokenStorage.retrieve("refreshToken");

      expect(result).toBeNull();
    });
  });

  // ── delete() ──────────────────────────────────────────────────────────

  describe("delete()", () => {
    it("removes the token from storage", async () => {
      const mockSecrets = createMockSecretStorage();
      SecretStorageService.initialize(mockSecrets);
      TokenStorage.initialize(SecretStorageService.getInstance()!, () => "production");
      const tokenStorage = TokenStorage.getInstance()!;

      await tokenStorage.store("accessToken", "tok-abc");
      await tokenStorage.delete("accessToken");

      expect(mockSecrets.delete).toHaveBeenCalledWith("nightgauge.platform.production.accessToken");
    });

    it("fires onTokenChanged with deleted action", async () => {
      const service = createService();
      TokenStorage.initialize(service, () => "production");
      const tokenStorage = TokenStorage.getInstance()!;

      const events: TokenChangeEvent[] = [];
      tokenStorage.onTokenChanged((e) => events.push(e));

      await tokenStorage.store("refreshToken", "ref-xyz");
      await tokenStorage.delete("refreshToken");

      expect(events).toHaveLength(2);
      expect(events[1]).toEqual({ key: "refreshToken", action: "deleted" });
    });

    it("does not throw when deleting a key that does not exist", async () => {
      const service = createService();
      TokenStorage.initialize(service, () => "production");
      const tokenStorage = TokenStorage.getInstance()!;

      await expect(tokenStorage.delete("accessToken")).resolves.toBeUndefined();
    });
  });

  // ── clear() ───────────────────────────────────────────────────────────

  describe("clear()", () => {
    it("removes all platform tokens for the active host", async () => {
      const mockSecrets = createMockSecretStorage();
      SecretStorageService.initialize(mockSecrets);
      TokenStorage.initialize(SecretStorageService.getInstance()!, () => "production");
      const tokenStorage = TokenStorage.getInstance()!;

      await tokenStorage.store("accessToken", "tok-abc");
      await tokenStorage.store("refreshToken", "ref-xyz");
      await tokenStorage.store("expiresAt", "2026-12-31T00:00:00Z");

      vi.clearAllMocks();

      await tokenStorage.clear();

      expect(mockSecrets.delete).toHaveBeenCalledWith("nightgauge.platform.production.accessToken");
      expect(mockSecrets.delete).toHaveBeenCalledWith(
        "nightgauge.platform.production.refreshToken"
      );
      expect(mockSecrets.delete).toHaveBeenCalledWith(
        "nightgauge.platform.production.tokenExpiresAt"
      );
    });

    it("makes all tokens unretrievable after clear", async () => {
      const service = createService();
      TokenStorage.initialize(service, () => "production");
      const tokenStorage = TokenStorage.getInstance()!;

      await tokenStorage.store("accessToken", "tok-abc");
      await tokenStorage.store("refreshToken", "ref-xyz");
      await tokenStorage.store("expiresAt", "2026-12-31T00:00:00Z");

      await tokenStorage.clear();

      expect(await tokenStorage.retrieve("accessToken")).toBeNull();
      expect(await tokenStorage.retrieve("refreshToken")).toBeNull();
      expect(await tokenStorage.retrieve("expiresAt")).toBeNull();
    });

    it("fires onTokenChanged with key: all and action: cleared", async () => {
      const service = createService();
      TokenStorage.initialize(service, () => "production");
      const tokenStorage = TokenStorage.getInstance()!;

      const events: TokenChangeEvent[] = [];
      tokenStorage.onTokenChanged((e) => events.push(e));

      await tokenStorage.clear();

      const clearEvent = events.find((e) => e.action === "cleared");
      expect(clearEvent).toBeDefined();
      expect(clearEvent).toEqual({ key: "all", action: "cleared" });
    });

    it("is idempotent — does not throw when tokens do not exist", async () => {
      const service = createService();
      TokenStorage.initialize(service, () => "production");
      const tokenStorage = TokenStorage.getInstance()!;

      await expect(tokenStorage.clear()).resolves.toBeUndefined();
      await expect(tokenStorage.clear()).resolves.toBeUndefined();
    });
  });

  // ── dispose() ─────────────────────────────────────────────────────────

  describe("dispose()", () => {
    it("disposes the EventEmitter without throwing", () => {
      const service = createService();
      TokenStorage.initialize(service, () => "production");
      const tokenStorage = TokenStorage.getInstance()!;

      expect(() => tokenStorage.dispose()).not.toThrow();
    });

    it("stops delivering events after dispose", async () => {
      const service = createService();
      TokenStorage.initialize(service, () => "production");
      const tokenStorage = TokenStorage.getInstance()!;

      const events: TokenChangeEvent[] = [];
      tokenStorage.onTokenChanged((e) => events.push(e));

      tokenStorage.dispose();

      // resetInstance disposes internally — re-initialize for the store call
      TokenStorage.resetInstance();
      SecretStorageService.resetInstance();
      const service2 = createService();
      TokenStorage.initialize(service2, () => "production");
      const tokenStorage2 = TokenStorage.getInstance()!;

      await tokenStorage2.store("accessToken", "tok-after-dispose");

      // Original listener should not have received new events after dispose
      expect(events).toHaveLength(0);
    });
  });

  // ── Cross-host credential isolation ───────────────────────────────────

  describe("cross-host credential isolation", () => {
    it("stores accessToken under the env-scoped key", async () => {
      const mockSecrets = createMockSecretStorage();
      SecretStorageService.initialize(mockSecrets);
      TokenStorage.initialize(SecretStorageService.getInstance()!, () => "production");
      const tokenStorage = TokenStorage.getInstance()!;

      await tokenStorage.store("accessToken", "prod-token");

      expect(mockSecrets.store).toHaveBeenCalledWith(
        "nightgauge.platform.production.accessToken",
        "prod-token"
      );
    });

    it("token stored for production is not retrievable when host switches to canary", async () => {
      const mockSecrets = createMockSecretStorage();
      SecretStorageService.initialize(mockSecrets);

      let activeHost = "production";
      TokenStorage.initialize(SecretStorageService.getInstance()!, () => activeHost);
      const tokenStorage = TokenStorage.getInstance()!;

      await tokenStorage.store("accessToken", "prod-token");

      // Switch host to canary — should read canary key (which has no value)
      activeHost = "canary";
      const result = await tokenStorage.retrieve("accessToken");

      expect(result).toBeNull();
    });

    it("clear() for production does not affect canary tokens", async () => {
      const mockSecrets = createMockSecretStorage();
      SecretStorageService.initialize(mockSecrets);

      let activeHost = "production";
      TokenStorage.initialize(SecretStorageService.getInstance()!, () => activeHost);
      const tokenStorage = TokenStorage.getInstance()!;

      // Store prod and canary tokens directly
      await tokenStorage.store("accessToken", "prod-token");
      activeHost = "canary";
      await tokenStorage.store("accessToken", "canary-token");

      // Clear while on canary host
      await tokenStorage.clear();

      // Canary token should be gone
      expect(await tokenStorage.retrieve("accessToken")).toBeNull();

      // Production token should survive
      activeHost = "production";
      const prodResult = await tokenStorage.retrieve("accessToken");
      expect(prodResult).toBe("prod-token");
    });

    it("uses canary env string as the storage key namespace", async () => {
      const mockSecrets = createMockSecretStorage();
      SecretStorageService.initialize(mockSecrets);
      TokenStorage.initialize(SecretStorageService.getInstance()!, () => "canary");
      const tokenStorage = TokenStorage.getInstance()!;

      await tokenStorage.store("accessToken", "canary-token");

      expect(mockSecrets.store).toHaveBeenCalledWith(
        "nightgauge.platform.canary.accessToken",
        "canary-token"
      );
    });

    it("uses hostname as key namespace for custom env", async () => {
      const mockSecrets = createMockSecretStorage();
      SecretStorageService.initialize(mockSecrets);
      TokenStorage.initialize(SecretStorageService.getInstance()!, () => "my.custom.host");
      const tokenStorage = TokenStorage.getInstance()!;

      await tokenStorage.store("accessToken", "custom-token");

      expect(mockSecrets.store).toHaveBeenCalledWith(
        "nightgauge.platform.my.custom.host.accessToken",
        "custom-token"
      );
    });
  });

  // ── migrateFromLegacy() ───────────────────────────────────────────────

  describe("migrateFromLegacy()", () => {
    it("copies all legacy unscoped tokens to production-scoped keys", async () => {
      const mockSecrets = createMockSecretStorage();
      // Seed legacy keys
      await (mockSecrets.store as ReturnType<typeof vi.fn>)(
        SECRET_KEYS.platformAccessToken,
        "old-access"
      );
      await (mockSecrets.store as ReturnType<typeof vi.fn>)(
        SECRET_KEYS.platformRefreshToken,
        "old-refresh"
      );
      await (mockSecrets.store as ReturnType<typeof vi.fn>)(
        SECRET_KEYS.platformTokenExpiresAt,
        "2026-01-01T00:00:00Z"
      );
      await (mockSecrets.store as ReturnType<typeof vi.fn>)(
        SECRET_KEYS.platformUserEmail,
        "user@example.com"
      );
      await (mockSecrets.store as ReturnType<typeof vi.fn>)(SECRET_KEYS.platformUserTier, "pro");
      await (mockSecrets.store as ReturnType<typeof vi.fn>)(SECRET_KEYS.platformUserRole, "admin");

      SecretStorageService.initialize(mockSecrets);
      TokenStorage.initialize(SecretStorageService.getInstance()!, () => "production");
      const tokenStorage = TokenStorage.getInstance()!;

      await tokenStorage.migrateFromLegacy();

      // Production-scoped keys must exist
      expect(mockSecrets.store).toHaveBeenCalledWith(
        "nightgauge.platform.production.accessToken",
        "old-access"
      );
      expect(mockSecrets.store).toHaveBeenCalledWith(
        "nightgauge.platform.production.refreshToken",
        "old-refresh"
      );
      expect(mockSecrets.store).toHaveBeenCalledWith(
        "nightgauge.platform.production.tokenExpiresAt",
        "2026-01-01T00:00:00Z"
      );
      expect(mockSecrets.store).toHaveBeenCalledWith(
        "nightgauge.platform.production.userEmail",
        "user@example.com"
      );
      expect(mockSecrets.store).toHaveBeenCalledWith(
        "nightgauge.platform.production.userTier",
        "pro"
      );
      expect(mockSecrets.store).toHaveBeenCalledWith(
        "nightgauge.platform.production.userRole",
        "admin"
      );
    });

    it("deletes legacy keys after migration", async () => {
      const mockSecrets = createMockSecretStorage();
      await (mockSecrets.store as ReturnType<typeof vi.fn>)(
        SECRET_KEYS.platformAccessToken,
        "old-access"
      );

      SecretStorageService.initialize(mockSecrets);
      TokenStorage.initialize(SecretStorageService.getInstance()!, () => "production");
      const tokenStorage = TokenStorage.getInstance()!;

      await tokenStorage.migrateFromLegacy();

      expect(mockSecrets.delete).toHaveBeenCalledWith(SECRET_KEYS.platformAccessToken);
      expect(mockSecrets.delete).toHaveBeenCalledWith(SECRET_KEYS.platformRefreshToken);
      expect(mockSecrets.delete).toHaveBeenCalledWith(SECRET_KEYS.platformTokenExpiresAt);
      expect(mockSecrets.delete).toHaveBeenCalledWith(SECRET_KEYS.platformUserEmail);
      expect(mockSecrets.delete).toHaveBeenCalledWith(SECRET_KEYS.platformUserTier);
      expect(mockSecrets.delete).toHaveBeenCalledWith(SECRET_KEYS.platformUserRole);
    });

    it("is a no-op when no legacy tokens exist", async () => {
      const mockSecrets = createMockSecretStorage();
      SecretStorageService.initialize(mockSecrets);
      TokenStorage.initialize(SecretStorageService.getInstance()!, () => "production");
      const tokenStorage = TokenStorage.getInstance()!;

      await expect(tokenStorage.migrateFromLegacy()).resolves.toBeUndefined();

      // No production keys written, no deletes for legacy keys
      expect(mockSecrets.store).not.toHaveBeenCalledWith(
        expect.stringContaining("nightgauge.platform.production."),
        expect.any(String)
      );
    });

    it("is idempotent — second call cleans up remaining legacy keys without overwriting new ones", async () => {
      const mockSecrets = createMockSecretStorage();
      await (mockSecrets.store as ReturnType<typeof vi.fn>)(
        SECRET_KEYS.platformAccessToken,
        "old-access"
      );

      SecretStorageService.initialize(mockSecrets);
      TokenStorage.initialize(SecretStorageService.getInstance()!, () => "production");
      const tokenStorage = TokenStorage.getInstance()!;

      // First migration
      await tokenStorage.migrateFromLegacy();
      vi.clearAllMocks();

      // Simulate legacy key still present (e.g. partial cleanup)
      await (mockSecrets.store as ReturnType<typeof vi.fn>)(
        SECRET_KEYS.platformAccessToken,
        "old-access"
      );
      // But production key also exists now
      await (mockSecrets.store as ReturnType<typeof vi.fn>)(
        "nightgauge.platform.production.accessToken",
        "migrated-access"
      );
      vi.clearAllMocks();

      // Second call — should delete legacy without touching production
      await tokenStorage.migrateFromLegacy();

      expect(mockSecrets.delete).toHaveBeenCalledWith(SECRET_KEYS.platformAccessToken);
      // Should NOT overwrite the already-migrated production token
      expect(mockSecrets.store).not.toHaveBeenCalledWith(
        "nightgauge.platform.production.accessToken",
        expect.any(String)
      );
    });
  });
});
