/**
 * Tests for DeviceKeyService — license resolution after sign-in (#1883).
 */

import { describe, it, expect, beforeEach, vi } from "vitest";
import * as vscode from "vscode";
import {
  DEVICE_KEY_FINGERPRINT_SECRET,
  DeviceKeyService,
  LINK_MACHINE_COMMAND,
  UNLINKED_NOTICE_STATE_KEY,
  deviceKeyErrorCode,
  messageForDeviceKeyError,
  type DeviceKeyErrorCode,
  type DeviceKeyServiceDeps,
} from "../../src/platform/DeviceKeyService";
import {
  LICENSE_SYNCED_FINGERPRINT_SECRET,
  licenseKeyFingerprint,
  type LicenseStatus as StoreStatus,
} from "../../src/services/licenseKeychainBridge";
import { SESSION_RESTORED_REASON, type SessionStateEvent } from "../../src/platform/SessionManager";
import { registerLinkMachineCommand } from "../../src/commands/linkMachine";
import type { Logger } from "../../src/utils/logger";

const SECRET_KEY = "nightgauge.platform.licenseKey";
const ISSUED = {
  licenseKey: "ng_live_issued_device_key",
  deviceKeyId: "dk-1",
  tier: "pro",
  expiresAt: "2027-01-01T00:00:00Z",
};

interface Harness {
  service: DeviceKeyService;
  deps: DeviceKeyServiceDeps;
  secrets: Map<string, string>;
  globalState: Map<string, unknown>;
  fire: (evt: Partial<SessionStateEvent>) => void;
  requestDeviceKey: ReturnType<typeof vi.fn>;
  reloadLicense: ReturnType<typeof vi.fn>;
  bridgeStore: ReturnType<typeof vi.fn>;
  bridgeStatus: ReturnType<typeof vi.fn>;
  forceRefresh: ReturnType<typeof vi.fn>;
  onKeyIssued: ReturnType<typeof vi.fn>;
  authed: { value: boolean };
}

function harness(
  opts: {
    env?: NodeJS.ProcessEnv;
    storeStatus?: StoreStatus | null;
    secret?: string;
    daemonLicensePresent?: boolean;
  } = {}
): Harness {
  const secrets = new Map<string, string>();
  if (opts.secret) secrets.set(SECRET_KEY, opts.secret);
  const globalState = new Map<string, unknown>();
  let listener: ((evt: SessionStateEvent) => void) | null = null;
  const authed = { value: true };
  const requestDeviceKey = vi.fn().mockResolvedValue(ISSUED);
  const reloadLicense = vi.fn().mockImplementation(async () => ({
    licensePresent: opts.daemonLicensePresent ?? true,
    adopted: true,
    source: "keychain",
    bridgeAttached: true,
  }));
  const bridgeStore = vi.fn().mockResolvedValue({ ok: true, source: "keychain" });
  const bridgeStatus = vi
    .fn()
    .mockResolvedValue(
      opts.storeStatus === undefined
        ? { source: "none", keychainAvailable: true }
        : opts.storeStatus
    );
  const forceRefresh = vi.fn().mockResolvedValue(null);
  const onKeyIssued = vi.fn();
  const deps: DeviceKeyServiceDeps = {
    getIpc: () => ({
      platformRequestDeviceKey: requestDeviceKey,
      platformReloadLicense: reloadLicense,
    }),
    session: {
      isAuthenticated: async () => authed.value,
      getAccessToken: async () => (authed.value ? "header.payload.sig" : null),
      onSessionChanged: ((fn: (evt: SessionStateEvent) => void) => {
        listener = fn;
        return { dispose: vi.fn() };
      }) as unknown as vscode.Event<SessionStateEvent>,
    },
    secrets: {
      getSecret: async (k) => secrets.get(k),
      setSecret: async (k, v) => {
        secrets.set(k, v);
      },
      deleteSecret: async (k) => {
        secrets.delete(k);
      },
    },
    secretKey: SECRET_KEY,
    bridge: { store: bridgeStore, status: bridgeStatus },
    machine: () => ({ machineId: "vscode-machine-id", hostname: "mac", platform: "darwin" }),
    globalState: {
      get: ((k: string) => globalState.get(k)) as vscode.Memento["get"],
      update: async (k: string, v: unknown) => {
        globalState.set(k, v);
      },
    },
    logger: { info: vi.fn(), warn: vi.fn(), error: vi.fn(), debug: vi.fn() } as unknown as Logger,
    tokenRefresher: { forceRefresh },
    env: opts.env ?? {},
    onKeyIssued,
    machinesUrl: () => "https://app.nightgauge.dev/account",
  };
  const service = new DeviceKeyService(deps);
  return {
    service,
    deps,
    secrets,
    globalState,
    fire: (evt) =>
      listener?.({
        previous: "unauthenticated",
        current: "authenticated",
        reason: "device-flow sign-in succeeded",
        data: {} as SessionStateEvent["data"],
        ...evt,
      }),
    requestDeviceKey,
    reloadLicense,
    bridgeStore,
    bridgeStatus,
    forceRefresh,
    onKeyIssued,
    authed,
  };
}

/** Let the fire-and-forget pass a session event starts run to completion. */
/**
 * Wait for the fire-and-forget pass a session event starts to log its
 * outcome, and return that line. It never starts a pass of its own, so a
 * trigger that subscribed to nothing stays red.
 */
async function settle(h: Harness): Promise<string | undefined> {
  const info = h.deps.logger.info as unknown as ReturnType<typeof vi.fn>;
  for (let i = 0; i < 200; i++) {
    const line = info.mock.calls
      .map((c) => String(c[0]))
      .find((m) => /device key issued|no device key requested|not issued/.test(m));
    if (line) return line;
    await new Promise((r) => setTimeout(r, 0));
  }
  return undefined;
}

beforeEach(() => {
  vi.clearAllMocks();
});

describe("DeviceKeyService precedence ladder", () => {
  it("requests a device key only when no key resolves", async () => {
    const h = harness();
    const outcome = await h.service.ensure("sign-in");
    expect(outcome).toEqual({ kind: "issued", tier: "pro" });
    expect(h.requestDeviceKey).toHaveBeenCalledWith(
      "header.payload.sig",
      "vscode-machine-id",
      "mac",
      "darwin"
    );
  });

  it("rung 1: NIGHTGAUGE_LICENSE_KEY in the environment wins", async () => {
    const h = harness({ env: { NIGHTGAUGE_LICENSE_KEY: "ng_live_env" } });
    expect(await h.service.ensure("sign-in")).toEqual({ kind: "explicit-key", source: "env" });
    expect(h.requestDeviceKey).not.toHaveBeenCalled();
  });

  it("rung 2: a key stored with `auth license set` wins", async () => {
    const h = harness({ storeStatus: { source: "keychain", keychainAvailable: true } });
    expect(await h.service.ensure("sign-in")).toEqual({ kind: "explicit-key", source: "keychain" });
    expect(h.requestDeviceKey).not.toHaveBeenCalled();
  });

  it("rung 3: platform.license_key in the machine config wins", async () => {
    const h = harness({ storeStatus: { source: "machine-file", keychainAvailable: false } });
    expect(await h.service.ensure("sign-in")).toEqual({
      kind: "explicit-key",
      source: "machine-file",
    });
    expect(h.requestDeviceKey).not.toHaveBeenCalled();
  });

  it("rung 4: a key in SecretStorage wins when the binary cannot report", async () => {
    const h = harness({ storeStatus: null, secret: "ng_live_pasted" });
    expect(await h.service.ensure("sign-in")).toEqual({
      kind: "explicit-key",
      source: "secret-storage",
    });
    expect(h.requestDeviceKey).not.toHaveBeenCalled();
    // An explicit key is never replaced by an issued one.
    expect(h.secrets.get(SECRET_KEY)).toBe("ng_live_pasted");
  });

  it("Link This Machine never replaces an explicit key either", async () => {
    const h = harness({ secret: "ng_live_pasted", storeStatus: null });
    await h.service.ensure("sign-in");
    expect(await h.service.ensure("link-machine", { force: true })).toEqual({
      kind: "explicit-key",
      source: "secret-storage",
    });
    expect(h.requestDeviceKey).not.toHaveBeenCalled();
  });
});

describe("DeviceKeyService triggers", () => {
  it("runs on a sign-in transition from any provider", async () => {
    const h = harness();
    h.fire({ reason: "github sign-in succeeded" });
    expect(await settle(h)).toMatch(/^\[DeviceKeyService\] sign-in: device key issued/);
    expect(h.requestDeviceKey).toHaveBeenCalledTimes(1);
  });

  it("runs on session restore", async () => {
    const h = harness();
    h.fire({ reason: SESSION_RESTORED_REASON });
    expect(await settle(h)).toMatch(/^\[DeviceKeyService\] session-restore: device key issued/);
    expect(h.requestDeviceKey).toHaveBeenCalledTimes(1);
  });

  it("ignores a token refresh (authenticated → authenticated)", async () => {
    const h = harness();
    h.fire({ previous: "authenticated", reason: "token refresh succeeded" });
    for (let i = 0; i < 20; i++) await new Promise((r) => setTimeout(r, 0));
    expect(h.requestDeviceKey).not.toHaveBeenCalled();
  });

  it("requests at most once per session, and again after sign-out", async () => {
    const h = harness();
    await h.service.ensure("sign-in");
    h.secrets.delete(SECRET_KEY);
    expect(await h.service.ensure("session-restore")).toEqual({ kind: "already-handled" });
    h.fire({ previous: "authenticated", current: "unauthenticated", reason: "sign-out" });
    await h.service.ensure("sign-in");
    expect(h.requestDeviceKey).toHaveBeenCalledTimes(2);
  });

  it("concurrent triggers share one request", async () => {
    const h = harness();
    await Promise.all([h.service.ensure("sign-in"), h.service.ensure("session-restore")]);
    expect(h.requestDeviceKey).toHaveBeenCalledTimes(1);
  });

  it("does nothing without a session", async () => {
    const h = harness();
    h.authed.value = false;
    expect(await h.service.ensure("sign-in")).toEqual({ kind: "no-session" });
    expect(h.requestDeviceKey).not.toHaveBeenCalled();
  });
});

describe("DeviceKeyService storage and daemon pickup", () => {
  it("stores the key in SecretStorage and the shared keychain, then reloads the daemon", async () => {
    const h = harness();
    await h.service.ensure("sign-in");
    expect(h.secrets.get(SECRET_KEY)).toBe(ISSUED.licenseKey);
    expect(h.bridgeStore).toHaveBeenCalledWith(ISSUED.licenseKey);
    const fp = await licenseKeyFingerprint(ISSUED.licenseKey);
    expect(h.secrets.get(LICENSE_SYNCED_FINGERPRINT_SECRET)).toBe(fp);
    expect(h.secrets.get(DEVICE_KEY_FINGERPRINT_SECRET)).toBe(fp);
    expect(h.onKeyIssued).toHaveBeenCalledWith(ISSUED);
    expect(h.reloadLicense).toHaveBeenCalled();
    // The reload runs after the key is in the store the daemon reads.
    expect(h.bridgeStore.mock.invocationCallOrder[0]).toBeLessThan(
      h.reloadLicense.mock.invocationCallOrder[0]!
    );
  });

  it("refreshes a stale session once and retries", async () => {
    const h = harness();
    h.requestDeviceKey.mockRejectedValueOnce(new Error("UNAUTHORIZED: your session has expired"));
    h.forceRefresh.mockResolvedValueOnce("fresh.jwt.token");
    expect(await h.service.ensure("sign-in")).toEqual({ kind: "issued", tier: "pro" });
    expect(h.requestDeviceKey).toHaveBeenLastCalledWith(
      "fresh.jwt.token",
      "vscode-machine-id",
      "mac",
      "darwin"
    );
  });
});

describe("DeviceKeyService error codes", () => {
  const cases: Array<[DeviceKeyErrorCode, "info" | "warning" | "error", RegExp]> = [
    ["LICENSE_NOT_FOUND", "info", /no license/],
    ["LICENSE_EXPIRED", "warning", /expired/],
    ["LICENSE_REVOKED", "error", /revoked or suspended/],
    ["LICENSE_MACHINE_LIMIT", "warning", /machine limit/],
    ["UNAUTHORIZED", "warning", /session has expired/],
  ];
  const showFor = {
    info: () => vscode.window.showInformationMessage,
    warning: () => vscode.window.showWarningMessage,
    error: () => vscode.window.showErrorMessage,
  };

  for (const [code, severity, text] of cases) {
    it(`${code} shows its own message`, async () => {
      const h = harness();
      h.requestDeviceKey.mockRejectedValue(new Error(`${code}: platform said no`));
      expect(await h.service.ensure("sign-in")).toEqual({ kind: "failed", code });
      const show = showFor[severity]() as unknown as ReturnType<typeof vi.fn>;
      expect(show).toHaveBeenCalledWith(
        expect.stringMatching(text),
        ...messageForDeviceKeyError(code)!.actions
      );
      expect(String(show.mock.calls[0]![0])).not.toMatch(/not accepted/i);
      expect(h.secrets.has(SECRET_KEY)).toBe(false);
    });
  }

  it("RATE_LIMITED and UNAVAILABLE are quiet and leave the next trigger free to retry", async () => {
    for (const code of ["RATE_LIMITED", "UNAVAILABLE"] as const) {
      const h = harness({ daemonLicensePresent: false });
      h.requestDeviceKey.mockRejectedValueOnce(new Error(`${code}: later`));
      expect(await h.service.ensure("sign-in")).toEqual({ kind: "failed", code });
      expect(vscode.window.showErrorMessage).not.toHaveBeenCalled();
      expect(vscode.window.showInformationMessage).not.toHaveBeenCalled();
      const warnings = (
        vscode.window.showWarningMessage as unknown as ReturnType<typeof vi.fn>
      ).mock.calls.map((c) => String(c[0]));
      expect(warnings.every((m) => /isn't linked/.test(m))).toBe(true);
      expect(await h.service.ensure("session-restore")).toEqual({ kind: "issued", tier: "pro" });
      vi.clearAllMocks();
    }
  });

  it("a settled license state ends automatic attempts for the session", async () => {
    const h = harness();
    h.requestDeviceKey.mockRejectedValue(new Error("LICENSE_NOT_FOUND: none"));
    await h.service.ensure("sign-in");
    expect(await h.service.ensure("session-restore")).toEqual({ kind: "already-handled" });
    expect(h.requestDeviceKey).toHaveBeenCalledTimes(1);
  });

  it("parses the code the Go error leads with", () => {
    expect(deviceKeyErrorCode(new Error("LICENSE_MACHINE_LIMIT: full"))).toBe(
      "LICENSE_MACHINE_LIMIT"
    );
    expect(deviceKeyErrorCode(new Error("ipc: LICENSE_EXPIRED: gone"))).toBe("LICENSE_EXPIRED");
    expect(deviceKeyErrorCode(new Error("socket closed"))).toBe("UNAVAILABLE");
  });
});

describe("DeviceKeyService revoke/expire re-request", () => {
  it("re-requests once when the issued key is reported revoked", async () => {
    const h = harness();
    await h.service.ensure("sign-in");
    h.requestDeviceKey.mockResolvedValueOnce({ ...ISSUED, licenseKey: "ng_live_second" });
    await h.service.onLicenseRejected("revoked", ISSUED.licenseKey);
    await h.service.onLicenseRejected("expired", "ng_live_second");
    expect(h.requestDeviceKey).toHaveBeenCalledTimes(2);
    expect(h.secrets.get(SECRET_KEY)).toBe("ng_live_second");
  });

  it("never replaces an explicit key the platform rejects", async () => {
    const h = harness({ secret: "ng_live_pasted", storeStatus: null });
    await h.service.onLicenseRejected("revoked", "ng_live_pasted");
    expect(h.requestDeviceKey).not.toHaveBeenCalled();
  });

  it("surfaces the platform code when the re-request fails", async () => {
    const h = harness();
    await h.service.ensure("sign-in");
    h.requestDeviceKey.mockRejectedValueOnce(new Error("LICENSE_EXPIRED: still"));
    await h.service.onLicenseRejected("expired", ISSUED.licenseKey);
    expect(vscode.window.showWarningMessage).toHaveBeenCalledWith(
      expect.stringMatching(/expired/),
      "Renew"
    );
  });

  it("ignores statuses that a new key cannot fix", async () => {
    const h = harness();
    await h.service.ensure("sign-in");
    await h.service.onLicenseRejected("machine_limit", ISSUED.licenseKey);
    expect(h.requestDeviceKey).toHaveBeenCalledTimes(1);
  });
});

describe("DeviceKeyService unlinked-machine notice", () => {
  it("shows once when the daemon still has no key, naming the consequence", async () => {
    const h = harness({ daemonLicensePresent: false });
    h.requestDeviceKey.mockRejectedValue(new Error("UNAVAILABLE: not deployed"));
    await h.service.ensure("sign-in");
    await h.service.ensure("link-machine", { force: true });
    const warn = vscode.window.showWarningMessage as unknown as ReturnType<typeof vi.fn>;
    const notices = warn.mock.calls.filter((c) => /isn't linked/.test(String(c[0])));
    expect(notices).toHaveLength(1);
    expect(String(notices[0]![0])).toMatch(/phone app and dashboard/);
    expect(notices[0]![1]).toBe("Link This Machine");
    expect(h.globalState.get(UNLINKED_NOTICE_STATE_KEY)).toBe(true);
  });

  it("the notice's button runs Link This Machine", async () => {
    const h = harness({ daemonLicensePresent: false });
    h.requestDeviceKey.mockRejectedValue(new Error("RATE_LIMITED: slow down"));
    (vscode.window.showWarningMessage as unknown as ReturnType<typeof vi.fn>).mockResolvedValueOnce(
      "Link This Machine"
    );
    await h.service.ensure("sign-in");
    expect(vscode.commands.executeCommand).toHaveBeenCalledWith(LINK_MACHINE_COMMAND);
  });

  it("stays silent when the daemon holds a key", async () => {
    const h = harness({ secret: "ng_live_pasted", storeStatus: null, daemonLicensePresent: true });
    await h.service.ensure("sign-in");
    expect(vscode.window.showWarningMessage).not.toHaveBeenCalled();
  });
});

describe("nightgauge.linkMachine", () => {
  function handler(getService: () => DeviceKeyService | null): () => Promise<void> {
    registerLinkMachineCommand(getService);
    const calls = (vscode.commands.registerCommand as unknown as ReturnType<typeof vi.fn>).mock
      .calls;
    const last = calls[calls.length - 1]!;
    expect(last[0]).toBe(LINK_MACHINE_COMMAND);
    return last[1] as () => Promise<void>;
  }

  it("forces a pass past the once-per-session guard", async () => {
    const h = harness();
    h.requestDeviceKey.mockRejectedValueOnce(new Error("LICENSE_NOT_FOUND: none"));
    await h.service.ensure("sign-in");
    await handler(() => h.service)();
    expect(h.requestDeviceKey).toHaveBeenCalledTimes(2);
  });

  it("explains itself when platform features are off", async () => {
    await handler(() => null)();
    expect(vscode.window.showInformationMessage).toHaveBeenCalledWith(
      expect.stringMatching(/platform\.enabled/)
    );
  });

  it("prompts sign-in without a session", async () => {
    const h = harness();
    h.authed.value = false;
    (
      vscode.window.showInformationMessage as unknown as ReturnType<typeof vi.fn>
    ).mockResolvedValueOnce("Sign In");
    await handler(() => h.service)();
    expect(vscode.commands.executeCommand).toHaveBeenCalledWith("nightgauge.signIn");
    expect(h.requestDeviceKey).not.toHaveBeenCalled();
  });

  it("says an explicit key was not replaced", async () => {
    const h = harness({ storeStatus: { source: "keychain", keychainAvailable: true } });
    await handler(() => h.service)();
    expect(vscode.window.showInformationMessage).toHaveBeenCalledWith(
      expect.stringMatching(/already has a license key \(keychain\); it was not replaced/)
    );
  });
});

describe("DeviceKeyService edge paths", () => {
  it("shows the Sign In message when the refresh yields no token", async () => {
    const h = harness();
    h.requestDeviceKey.mockRejectedValue(new Error("UNAUTHORIZED: stale"));
    h.forceRefresh.mockResolvedValueOnce(null);
    expect(await h.service.ensure("sign-in")).toEqual({ kind: "failed", code: "UNAUTHORIZED" });
    expect(h.requestDeviceKey).toHaveBeenCalledTimes(1);
    expect(vscode.window.showWarningMessage).toHaveBeenCalledWith(
      expect.stringMatching(/session has expired/),
      "Sign In"
    );
  });

  it("does not loop when the retried request is also refused", async () => {
    const h = harness();
    h.requestDeviceKey.mockRejectedValue(new Error("UNAUTHORIZED: stale"));
    h.forceRefresh.mockResolvedValue("fresh.jwt.token");
    await h.service.ensure("sign-in");
    expect(h.requestDeviceKey).toHaveBeenCalledTimes(2);
  });

  it("an explicit key the daemon cannot see still gets the unlinked notice", async () => {
    const h = harness({ secret: "ng_live_pasted", storeStatus: null, daemonLicensePresent: false });
    await h.service.ensure("sign-in");
    expect(vscode.window.showWarningMessage).toHaveBeenCalledWith(
      expect.stringMatching(/isn't linked/),
      "Link This Machine"
    );
  });

  it("does not re-request when a pasted key replaced the issued one", async () => {
    const h = harness();
    await h.service.ensure("sign-in");
    h.secrets.set(SECRET_KEY, "ng_live_pasted_later");
    await h.service.onLicenseRejected("revoked", "ng_live_pasted_later");
    expect(h.requestDeviceKey).toHaveBeenCalledTimes(1);
  });

  it("does not re-request while signed out", async () => {
    const h = harness();
    await h.service.ensure("sign-in");
    h.authed.value = false;
    await h.service.onLicenseRejected("revoked", ISSUED.licenseKey);
    expect(h.requestDeviceKey).toHaveBeenCalledTimes(1);
  });

  it("sign-out makes the re-request and per-code messages available again", async () => {
    const h = harness();
    await h.service.ensure("sign-in");
    await h.service.onLicenseRejected("revoked", ISSUED.licenseKey);
    h.fire({ previous: "authenticated", current: "unauthenticated", reason: "sign-out" });
    await h.service.onLicenseRejected("revoked", ISSUED.licenseKey);
    expect(h.requestDeviceKey).toHaveBeenCalledTimes(3);
  });
});
