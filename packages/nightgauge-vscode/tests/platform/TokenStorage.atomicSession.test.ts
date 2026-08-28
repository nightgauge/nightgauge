/**
 * Issue #1024: a fresh sign-in left the refresh scheduler idle.
 *
 * The scheduler is re-armed as a SIDE EFFECT of writing one field:
 * `store("accessToken", …)` fires `{key:"accessToken", action:"stored"}`
 * synchronously, and TokenRefreshManager's subscription immediately reads a
 * DIFFERENT field, `expiresAt`. A multi-field session write was therefore only
 * correct if expiresAt happened to be written first — an undocumented ordering
 * contract that lived in a comment in one of three call sites, and which both
 * sign-in paths violated.
 *
 * The ordering was the defect's expression; the contract itself was the defect.
 * These tests pin the contract's absence: one event, fired only once every
 * field is durable.
 */

import { describe, it, expect, vi, beforeEach } from "vitest";

vi.mock("vscode", () => {
  class MockEventEmitter<T> {
    private listeners: Array<(e: T) => void> = [];
    event = (l: (e: T) => void) => {
      this.listeners.push(l);
      return { dispose: () => {} };
    };
    fire = (data: T) => {
      for (const l of this.listeners) l(data);
    };
    dispose = vi.fn();
  }
  return { EventEmitter: MockEventEmitter, Disposable: { from: vi.fn() } };
});

import { TokenStorage } from "../../src/platform/TokenStorage";

/** A secret store that records write order and can be read back mid-event. */
function makeSecretService() {
  const values = new Map<string, string>();
  const writes: string[] = [];
  return {
    values,
    writes,
    setSecret: vi.fn(async (k: string, v: string) => {
      values.set(k, v);
      writes.push(k);
    }),
    getSecret: vi.fn(async (k: string) => values.get(k)),
    deleteSecret: vi.fn(async (k: string) => {
      values.delete(k);
    }),
  };
}

function makeStorage() {
  const secrets = makeSecretService();
  TokenStorage.resetInstance();
  TokenStorage.initialize(secrets as never, () => "test-host");
  const storage = TokenStorage.getInstance()!;
  return { storage, secrets };
}

describe("TokenStorage.storeSession (#1024)", () => {
  beforeEach(() => vi.clearAllMocks());

  it("fires exactly one event, and every field is readable when it fires", async () => {
    const { storage } = makeStorage();

    const seen: Array<{ key: string; action: string }> = [];
    const reads: Array<Promise<string | null>> = [];
    storage.onTokenChanged((evt) => {
      seen.push({ key: evt.key, action: evt.action });
      // This is the read the refresh scheduler performs on being notified.
      // Before the fix it returned null on a fresh sign-in, so nothing armed.
      // The listener is sync (the emitter is), so the async read is captured
      // and awaited by the test rather than raced against the assertion.
      reads.push(storage.retrieve("expiresAt"));
    });

    await storage.storeSession({
      accessToken: "at",
      refreshToken: "rt",
      expiresAt: "2026-01-01T00:00:00.000Z",
    });

    expect(seen).toEqual([{ key: "all", action: "stored" }]);
    // Not three per-field events — a per-field event here would reintroduce
    // exactly the ordering hazard this method exists to delete.
    expect(seen).toHaveLength(1);
    expect(await reads[0]).toBe("2026-01-01T00:00:00.000Z");
  });

  it("writes all three fields", async () => {
    const { storage, secrets } = makeStorage();
    await storage.storeSession({ accessToken: "at", refreshToken: "rt", expiresAt: "exp" });

    expect(await storage.retrieve("accessToken")).toBe("at");
    expect(await storage.retrieve("refreshToken")).toBe("rt");
    expect(await storage.retrieve("expiresAt")).toBe("exp");
    expect(secrets.setSecret).toHaveBeenCalledTimes(3);
  });

  it("emits no event before the last field is durable", async () => {
    const { storage, secrets } = makeStorage();
    let writesAtEvent = -1;
    storage.onTokenChanged(() => {
      writesAtEvent = secrets.writes.length;
    });

    await storage.storeSession({ accessToken: "at", refreshToken: "rt", expiresAt: "exp" });

    // The whole point: a listener can never observe a half-written session.
    expect(writesAtEvent).toBe(3);
  });
});

/**
 * The WIRING half.
 *
 * The tests above pin that `storeSession` behaves atomically. They say nothing
 * about whether the sign-in paths call it — and that is the half the defect
 * actually lived in: `storeSession` did not exist, and three ordered `store()`
 * calls did. Reverting a sign-in path to those three calls left every test
 * above green, which is precisely the logic-vs-wiring gap this repo's testing
 * rules exist to close.
 *
 * A source-level guard is the right instrument here: both sign-in services
 * construct their own `TokenStorage.getInstance()` and are otherwise awkward to
 * drive, and the property being protected is structural — no session write may
 * go back to per-field `store()` calls.
 */
describe("the sign-in paths write a session atomically (#1024)", () => {
  const SESSION_WRITERS = [
    "../../src/services/GitHubAuthService.ts",
    "../../src/services/OAuthDeviceFlowService.ts",
    "../../src/platform/TokenRefreshManager.ts",
  ];

  it.each(SESSION_WRITERS)("%s calls storeSession, never per-field store()", async (rel) => {
    const fs = await import("fs");
    const path = await import("path");
    const src = fs.readFileSync(path.join(__dirname, rel), "utf-8");

    expect(src, `${rel} must write the session atomically`).toContain("storeSession(");

    // The three session fields must never be written one at a time again —
    // that is the ordering hazard, and it is invisible at runtime because the
    // scheduler simply arms nothing.
    for (const field of ["accessToken", "refreshToken", "expiresAt"]) {
      expect(
        src.includes(`store("${field}"`),
        `${rel} writes ${field} with a per-field store() call — the refresh ` +
          `scheduler re-arms on one field's event and reads another, so a ` +
          `multi-field write must be atomic`
      ).toBe(false);
    }
  });
});
