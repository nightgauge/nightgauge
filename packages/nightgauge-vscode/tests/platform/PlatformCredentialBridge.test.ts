/**
 * PlatformCredentialBridge.test.ts
 *
 * The bridge is the thing that stops #742 from coming back. Its contract:
 *
 * - sync() pushes whatever session token storage currently holds;
 * - a stored accessToken (sign-in, or a rotation by TokenRefreshManager) is
 *   pushed immediately — a credential handed over only at spawn dies with the
 *   first access token;
 * - sign-out (bulk 'cleared', or the access token deleted) pushes an empty
 *   token, dropping the daemon back to its license-key fallback;
 * - unrelated token keys are ignored;
 * - at most one push is in flight, and the value pushed is always re-read from
 *   storage, so overlapping changes cannot leave a revoked token installed;
 * - a failing transport is swallowed — a community build has no platform client
 *   and says so on every push.
 *
 * @see Issue #742 - The Go backend never receives the signed-in session token
 */

import { describe, it, expect, vi, beforeEach } from "vitest";

vi.mock("vscode", () => ({}));

import { PlatformCredentialBridge } from "../../src/platform/PlatformCredentialBridge";
import type { ITokenStorage, TokenChangeEvent, TokenKey } from "../../src/platform/TokenStorage";

// ---------------------------------------------------------------------------
// Fakes
// ---------------------------------------------------------------------------

class FakeTokenStorage implements ITokenStorage {
  private readonly values = new Map<TokenKey, string>();
  private readonly listeners: ((evt: TokenChangeEvent) => void)[] = [];
  /** Set to make retrieve() throw, simulating a keychain failure. */
  retrieveError: Error | null = null;

  readonly onTokenChanged = (listener: (evt: TokenChangeEvent) => void) => {
    this.listeners.push(listener);
    return {
      dispose: () => {
        const i = this.listeners.indexOf(listener);
        if (i >= 0) this.listeners.splice(i, 1);
      },
    };
  };

  async store(key: TokenKey, value: string): Promise<void> {
    this.values.set(key, value);
    this.fire({ key, action: "stored" });
  }

  async retrieve(key: TokenKey): Promise<string | null> {
    if (this.retrieveError) throw this.retrieveError;
    return this.values.get(key) ?? null;
  }

  async delete(key: TokenKey): Promise<void> {
    this.values.delete(key);
    this.fire({ key, action: "deleted" });
  }

  async clear(): Promise<void> {
    this.values.clear();
    this.fire({ key: "all", action: "cleared" });
  }

  /** Mutate storage without emitting — used to stage a value mid-flight. */
  setQuietly(key: TokenKey, value: string | null): void {
    if (value === null) this.values.delete(key);
    else this.values.set(key, value);
  }

  notifyHostChanged(): void {
    this.fire({ key: "all", action: "rekeyed" });
  }

  fire(evt: TokenChangeEvent): void {
    for (const l of [...this.listeners]) l(evt);
  }

  dispose(): void {
    this.listeners.length = 0;
  }
}

/** Transport that records every pushed token and can be held open. */
function makeTransport() {
  const pushed: string[] = [];
  let release: (() => void) | null = null;
  let gate: Promise<void> | null = null;

  return {
    pushed,
    /** Block the next push(es) until releaseGate() is called. */
    hold(): void {
      gate = new Promise<void>((resolve) => {
        release = resolve;
      });
    },
    releaseGate(): void {
      release?.();
      release = null;
      gate = null;
    },
    fail: null as Error | null,
    async setSessionToken(token: string): Promise<void> {
      pushed.push(token);
      if (gate) await gate;
      if (this.fail) throw this.fail;
    },
  };
}

async function flush(depth = 12): Promise<void> {
  for (let i = 0; i < depth; i++) await Promise.resolve();
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

describe("PlatformCredentialBridge", () => {
  let storage: FakeTokenStorage;
  let transport: ReturnType<typeof makeTransport>;
  let bridge: PlatformCredentialBridge;

  beforeEach(() => {
    storage = new FakeTokenStorage();
    transport = makeTransport();
    bridge = new PlatformCredentialBridge(transport, storage);
  });

  it("pushes the stored session token on sync", async () => {
    storage.setQuietly("accessToken", "jwt.signed.in");

    await bridge.sync();

    expect(transport.pushed).toEqual(["jwt.signed.in"]);
  });

  it("pushes an empty token when there is no session", async () => {
    await bridge.sync();

    expect(transport.pushed).toEqual([""]);
  });

  it("pushes the new token when the refresh manager rotates it", async () => {
    storage.setQuietly("accessToken", "jwt.first");
    await bridge.sync();

    await storage.store("accessToken", "jwt.rotated");
    await flush();

    expect(transport.pushed).toEqual(["jwt.first", "jwt.rotated"]);
  });

  it("clears the credential on bulk sign-out", async () => {
    storage.setQuietly("accessToken", "jwt.first");
    await bridge.sync();

    await storage.clear();
    await flush();

    expect(transport.pushed).toEqual(["jwt.first", ""]);
  });

  it("clears the credential when only the access token is deleted", async () => {
    storage.setQuietly("accessToken", "jwt.first");
    await bridge.sync();

    await storage.delete("accessToken");
    await flush();

    expect(transport.pushed).toEqual(["jwt.first", ""]);
  });

  it("ignores changes to other token keys", async () => {
    storage.setQuietly("accessToken", "jwt.first");
    await bridge.sync();

    await storage.store("refreshToken", "rt");
    await storage.store("userEmail", "someone@example.com");
    await storage.store("expiresAt", new Date().toISOString());
    await flush();

    expect(transport.pushed).toEqual(["jwt.first"]);
  });

  it("keeps one push in flight and re-reads storage, so the newest token wins", async () => {
    storage.setQuietly("accessToken", "jwt.first");
    transport.hold();

    const first = bridge.sync();
    await flush();
    expect(transport.pushed).toEqual(["jwt.first"]);

    // Two rotations land while the first push is still in flight. They collapse
    // into one follow-up push carrying the newest value, never the older one.
    storage.setQuietly("accessToken", "jwt.second");
    storage.fire({ key: "accessToken", action: "stored" });
    storage.setQuietly("accessToken", "jwt.third");
    storage.fire({ key: "accessToken", action: "stored" });
    await flush();
    expect(transport.pushed).toEqual(["jwt.first"]);

    transport.releaseGate();
    await first;
    await flush();

    expect(transport.pushed).toEqual(["jwt.first", "jwt.third"]);
  });

  it("swallows transport failures — a community build has no platform client", async () => {
    transport.fail = new Error("platform client not configured");
    storage.setQuietly("accessToken", "jwt.first");

    await expect(bridge.sync()).resolves.toBeUndefined();
    expect(transport.pushed).toEqual(["jwt.first"]);

    // And a later change still gets attempted; one failure does not wedge it.
    transport.fail = null;
    await storage.store("accessToken", "jwt.second");
    await flush();
    expect(transport.pushed).toEqual(["jwt.first", "jwt.second"]);
  });

  it("swallows a keychain read failure without pushing", async () => {
    storage.retrieveError = new Error("keychain locked");

    await expect(bridge.sync()).resolves.toBeUndefined();
    expect(transport.pushed).toEqual([]);
  });

  // ── host re-keying (#797) ────────────────────────────────────────────────

  it("re-reads storage when the platform host is re-keyed", async () => {
    // Credentials are stored per host. A host switch changes what every key
    // resolves to without any token being written, so nothing in the
    // store/delete/clear vocabulary fires — the daemon would otherwise keep a
    // credential belonging to the previous host indefinitely.
    storage.setQuietly("accessToken", "jwt.hostA");
    await bridge.sync();
    expect(transport.pushed).toEqual(["jwt.hostA"]);

    storage.setQuietly("accessToken", "jwt.hostB");
    storage.notifyHostChanged();
    await flush();

    expect(transport.pushed).toEqual(["jwt.hostA", "jwt.hostB"]);
  });

  it("recovers the daemon credential when a re-key exposes a token that was there all along", async () => {
    // The #797 failure in miniature: the first read lands while config has not
    // loaded, so the key resolves to an empty bucket and the daemon is handed
    // "" — dropping it to its license key, which the user-scoped analytics
    // routes reject. Once config loads and the host is re-keyed, the real token
    // must be pushed without the user signing out and back in.
    await bridge.sync();
    expect(transport.pushed).toEqual([""]);

    storage.setQuietly("accessToken", "jwt.real");
    storage.notifyHostChanged();
    await flush();

    expect(transport.pushed).toEqual(["", "jwt.real"]);
  });

  it("stops pushing once disposed", async () => {
    storage.setQuietly("accessToken", "jwt.first");
    await bridge.sync();
    bridge.dispose();

    await storage.store("accessToken", "jwt.second");
    await storage.clear();
    await flush();

    expect(transport.pushed).toEqual(["jwt.first"]);
  });
});
