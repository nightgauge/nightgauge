/**
 * DeviceKeyService — resolves a license key for a signed-in machine without a
 * paste (#1883, platform ADR 015).
 *
 * After any platform sign-in, and on session restore, it walks the precedence
 * ladder and requests a per-device key from `POST /v1/license/device-key`
 * only when no key resolves:
 *
 *   1. `NIGHTGAUGE_LICENSE_KEY` in the extension host environment;
 *   2. the CLI's store (`nightgauge auth license status`): the environment the
 *      binary sees, the OS keychain, or `platform.license_key` in the machine
 *      config;
 *   3. a key SecretStorage already holds.
 *
 * An explicit key is never replaced: the platform revokes a machine's previous
 * device key whenever the same machine asks again, so the request is made only
 * when the ladder is empty, at most once per session. The issued key is stored
 * through `persistLicenseKey` (SecretStorage plus the keychain entry the CLI
 * and daemon read), its fingerprint is recorded, and the running daemon is
 * told to re-resolve it (`platform.reloadLicense`) so the Action Center bridge
 * attaches without a reload.
 *
 * When the platform later reports the issued key revoked or expired, one
 * re-request is made per session. When a session exists and the daemon still
 * holds no key, a one-time notice names the consequence and offers
 * `nightgauge.linkMachine`.
 *
 * @see Issue #1883
 */

import * as vscode from "vscode";
import type { DeviceKeyResult, PlatformReloadLicenseResult } from "../services/IpcClientBase";
import {
  licenseKeyFingerprint,
  persistLicenseKey,
  type LicenseKeychainBridge,
  type LicenseSecrets,
} from "../services/licenseKeychainBridge";
import { SESSION_RESTORED_REASON, type SessionStateEvent } from "./SessionManager";
import type { IOnDemandTokenRefresher } from "./TokenRefreshManager";
import type { LicenseStatus } from "./types";
import type { Logger } from "../utils/logger";

/** SecretStorage key of the fingerprint of the key this service was issued. */
export const DEVICE_KEY_FINGERPRINT_SECRET = "nightgauge.platform.deviceKeyFingerprint";

/** globalState key recording that the unlinked-machine notice was shown. */
export const UNLINKED_NOTICE_STATE_KEY = "nightgauge.unlinkedMachineNoticeShown";

/** The command the unlinked-machine notice offers. */
export const LINK_MACHINE_COMMAND = "nightgauge.linkMachine";

const PRICING_PAGE_URL = "https://nightgauge.dev/pricing";

/**
 * Codes the Go side leads a platform.requestDeviceKey error message with
 * (internal/platform/license_device_key.go DeviceKeyErrorCode).
 */
const KNOWN_CODES = [
  "LICENSE_NOT_FOUND",
  "LICENSE_EXPIRED",
  "LICENSE_REVOKED",
  "LICENSE_MACHINE_LIMIT",
  "RATE_LIMITED",
  "UNAUTHORIZED",
  "UNAVAILABLE",
] as const;

export type DeviceKeyErrorCode = (typeof KNOWN_CODES)[number];

/** Notification buttons, compared by value when the user picks one. */
export const ACTION = {
  linkMachine: "Link This Machine",
  signIn: "Sign In",
  startTrial: "Start Free Trial",
  viewPricing: "View Pricing",
  renew: "Renew",
  activateLicense: "Activate License",
  manageMachines: "Manage Machines",
} as const;

type ActionLabel = (typeof ACTION)[keyof typeof ACTION];

/** Commands the notification buttons run. */
export const COMMAND = {
  signIn: "nightgauge.signIn",
  startTrial: "nightgauge.startTrial",
  activateLicense: "nightgauge.activateLicense",
} as const;

/** The license states the platform settles; each gets its own message. */
const LICENSE_STATE_CODES: ReadonlySet<DeviceKeyErrorCode> = new Set([
  "LICENSE_NOT_FOUND",
  "LICENSE_EXPIRED",
  "LICENSE_REVOKED",
  "LICENSE_MACHINE_LIMIT",
]);

/**
 * The code a platform.requestDeviceKey failure carries. The Go error message
 * leads with it (`LICENSE_MACHINE_LIMIT: ...`); anything else is UNAVAILABLE.
 */
export function deviceKeyErrorCode(err: unknown): DeviceKeyErrorCode {
  const message = err instanceof Error ? err.message : String(err);
  for (const code of KNOWN_CODES) {
    if (message.startsWith(`${code}:`) || message.includes(` ${code}:`)) return code;
  }
  return "UNAVAILABLE";
}

/** Where the precedence ladder found a key. */
export type ExplicitKeySource = "env" | "keychain" | "machine-file" | "secret-storage";

export type EnsureTrigger = "sign-in" | "session-restore" | "link-machine" | "license-rejected";

export type EnsureOutcome =
  | { kind: "no-session" }
  | { kind: "already-handled" }
  | { kind: "explicit-key"; source: ExplicitKeySource }
  | { kind: "issued"; tier: string }
  | { kind: "failed"; code: DeviceKeyErrorCode };

/** The machine fields platform.validateLicense already sends. */
export interface DeviceMachineInfo {
  machineId: string;
  hostname: string;
  platform: string;
}

/** The IPC calls this service makes. */
export interface DeviceKeyIpc {
  platformRequestDeviceKey(
    accessToken: string,
    machineId?: string,
    hostname?: string,
    platform?: string
  ): Promise<DeviceKeyResult>;
  platformReloadLicense(): Promise<PlatformReloadLicenseResult>;
}

/** The slice of SessionManager this service reads. */
export interface DeviceKeySession {
  isAuthenticated(): Promise<boolean>;
  getAccessToken(): Promise<string | null>;
  onSessionChanged: vscode.Event<SessionStateEvent>;
}

export interface DeviceKeyServiceDeps {
  getIpc: () => DeviceKeyIpc | null;
  session: DeviceKeySession;
  secrets: LicenseSecrets;
  /** SecretStorage key of the license key. */
  secretKey: string;
  bridge: Pick<LicenseKeychainBridge, "store" | "status">;
  machine: () => DeviceMachineInfo;
  globalState: Pick<vscode.Memento, "get" | "update">;
  logger: Logger;
  tokenRefresher?: IOnDemandTokenRefresher | null;
  env?: NodeJS.ProcessEnv;
  /** Called after an issued key is stored (cache refresh, sidebar tier). */
  onKeyIssued?: (result: DeviceKeyResult) => void | Promise<void>;
  /** The dashboard page that lists the account's machines. */
  machinesUrl?: () => string;
}

export class DeviceKeyService implements vscode.Disposable {
  private readonly disposables: vscode.Disposable[] = [];
  private inflight: Promise<EnsureOutcome> | null = null;
  /** The automatic pass ran to a settled answer in this session. */
  private handledThisSession = false;
  /** A revoked/expired issued key was re-requested in this session. */
  private rejectionRetried = false;
  /** Per-code messages already shown in this session. */
  private readonly shownCodes = new Set<DeviceKeyErrorCode>();

  constructor(private readonly deps: DeviceKeyServiceDeps) {
    this.disposables.push(deps.session.onSessionChanged((evt) => this.onSessionChanged(evt)));
  }

  dispose(): void {
    for (const d of this.disposables) d.dispose();
    this.disposables.length = 0;
  }

  /**
   * Sign-in and restore transitions (`previous !== authenticated`) run a pass;
   * a token refresh (authenticated → authenticated) does not. Every sign-in
   * path, whatever the provider, transitions through SessionManager, so this
   * covers them all.
   */
  private onSessionChanged(evt: SessionStateEvent): void {
    if (evt.current === "authenticated" && evt.previous !== "authenticated") {
      const trigger: EnsureTrigger =
        evt.reason === SESSION_RESTORED_REASON ? "session-restore" : "sign-in";
      void this.ensure(trigger).catch((err) =>
        this.deps.logger.warn(`[DeviceKeyService] ${trigger} pass failed: ${String(err)}`)
      );
    } else if (evt.current === "unauthenticated") {
      this.handledThisSession = false;
      this.rejectionRetried = false;
      this.shownCodes.clear();
    }
  }

  /**
   * One pass: walk the ladder, request a key when it is empty, tell the
   * daemon, and show the unlinked notice when the daemon still has none.
   * Concurrent callers share the pass in flight. `force` (the Link This
   * Machine command) ignores the once-per-session guard, never the ladder.
   */
  ensure(trigger: EnsureTrigger, opts: { force?: boolean } = {}): Promise<EnsureOutcome> {
    if (this.inflight) return this.inflight;
    const pass = this.runPass(trigger, opts.force === true).finally(() => {
      this.inflight = null;
    });
    this.inflight = pass;
    return pass;
  }

  /**
   * The platform reported the current key revoked, suspended or expired. When
   * that key is the one this service was issued, and the user is signed in,
   * request a replacement once per session. An explicit key is left alone.
   */
  async onLicenseRejected(status: LicenseStatus | "community", key: string): Promise<void> {
    if (status !== "revoked" && status !== "suspended" && status !== "expired") return;
    if (this.rejectionRetried || !key) return;
    if (!(await this.deps.session.isAuthenticated())) return;
    const issued = await this.deps.secrets.getSecret(DEVICE_KEY_FINGERPRINT_SECRET);
    if (!issued || issued !== (await licenseKeyFingerprint(key))) return;
    this.rejectionRetried = true;
    this.deps.logger.info(`[DeviceKeyService] issued key reported ${status}; requesting a new one`);
    if (this.inflight) await this.inflight.catch(() => undefined);
    const pass = this.issueAndReport("license-rejected").finally(() => {
      this.inflight = null;
    });
    this.inflight = pass;
    await pass;
  }

  /** Where the ladder finds a key, or null when none resolves. */
  async resolveExplicitKey(): Promise<ExplicitKeySource | null> {
    const env = this.deps.env ?? process.env;
    if (env.NIGHTGAUGE_LICENSE_KEY?.trim()) return "env";
    const status = await this.deps.bridge.status();
    if (status && status.source !== "none") {
      // "env" here is the binary's environment: still an explicit key.
      return status.source === "env" ? "env" : status.source;
    }
    const stored = await this.deps.secrets.getSecret(this.deps.secretKey);
    if (stored?.trim()) return "secret-storage";
    return null;
  }

  private async runPass(trigger: EnsureTrigger, force: boolean): Promise<EnsureOutcome> {
    if (!(await this.deps.session.isAuthenticated())) return { kind: "no-session" };
    if (this.handledThisSession && !force) return { kind: "already-handled" };

    const explicit = await this.resolveExplicitKey();
    if (explicit) {
      this.handledThisSession = true;
      this.deps.logger.info(
        `[DeviceKeyService] ${trigger}: a key resolves (${explicit}); no device key requested`
      );
      const daemon = await this.reloadDaemon();
      if (daemon && !daemon.licensePresent) await this.showUnlinkedNoticeOnce();
      return { kind: "explicit-key", source: explicit };
    }
    return this.issueAndReport(trigger);
  }

  /** Request, store and hand over a device key; report the outcome to the user. */
  private async issueAndReport(trigger: EnsureTrigger): Promise<EnsureOutcome> {
    let result: DeviceKeyResult;
    try {
      result = await this.request();
    } catch (err) {
      const code = deviceKeyErrorCode(err);
      this.deps.logger.warn(`[DeviceKeyService] ${trigger}: device key not issued (${code})`);
      // A settled license state ends the automatic attempts for this session;
      // a rate limit, an outage or a stale session leaves the next trigger free
      // to try again.
      if (LICENSE_STATE_CODES.has(code)) this.handledThisSession = true;
      const shown = await this.showErrorOnce(code, trigger);
      if (!shown) {
        const daemon = await this.reloadDaemon();
        if (!daemon || !daemon.licensePresent) await this.showUnlinkedNoticeOnce();
      }
      return { kind: "failed", code };
    }

    await persistLicenseKey(
      this.deps.secrets,
      this.deps.secretKey,
      result.licenseKey,
      this.deps.bridge
    );
    await this.deps.secrets.setSecret(
      DEVICE_KEY_FINGERPRINT_SECRET,
      await licenseKeyFingerprint(result.licenseKey)
    );
    this.handledThisSession = true;
    this.deps.logger.info(`[DeviceKeyService] ${trigger}: device key issued (tier=${result.tier})`);
    await this.deps.onKeyIssued?.(result);
    await this.reloadDaemon();
    void vscode.window.showInformationMessage(
      `Nightgauge: This machine is linked to your account (${result.tier} tier).`
    );
    return { kind: "issued", tier: result.tier };
  }

  /** platform.requestDeviceKey with one refresh-and-retry on a stale session. */
  private async request(): Promise<DeviceKeyResult> {
    const ipc = this.deps.getIpc();
    if (!ipc) throw new Error("UNAVAILABLE: the Nightgauge backend is not running");
    const token = await this.deps.session.getAccessToken();
    if (!token) throw new Error("UNAUTHORIZED: no session token");
    const m = this.deps.machine();
    try {
      return await ipc.platformRequestDeviceKey(token, m.machineId, m.hostname, m.platform);
    } catch (err) {
      if (deviceKeyErrorCode(err) === "UNAUTHORIZED" && this.deps.tokenRefresher) {
        const fresh = await this.deps.tokenRefresher.forceRefresh();
        if (fresh) {
          return ipc.platformRequestDeviceKey(fresh, m.machineId, m.hostname, m.platform);
        }
      }
      throw err;
    }
  }

  /** Ask the daemon to re-resolve the stored key; null when it cannot say. */
  private async reloadDaemon(): Promise<PlatformReloadLicenseResult | null> {
    const ipc = this.deps.getIpc();
    if (!ipc) return null;
    try {
      const res = await ipc.platformReloadLicense();
      this.deps.logger.info(
        `[DeviceKeyService] daemon license=${res.licensePresent ? "present" : "absent"} ` +
          `(source=${res.source}, bridge=${res.bridgeAttached ? "active" : "inactive"})`
      );
      return res;
    } catch (err) {
      this.deps.logger.warn(`[DeviceKeyService] daemon license reload failed: ${String(err)}`);
      return null;
    }
  }

  /** The one-time notice for a signed-in machine the daemon cannot link. */
  private async showUnlinkedNoticeOnce(): Promise<void> {
    if (this.deps.globalState.get<boolean>(UNLINKED_NOTICE_STATE_KEY)) return;
    await this.deps.globalState.update(UNLINKED_NOTICE_STATE_KEY, true);
    const action = await vscode.window.showWarningMessage(
      "Nightgauge: This machine isn't linked to your Nightgauge account, so the phone app and dashboard won't see its runs or approvals.",
      ACTION.linkMachine
    );
    if (action === ACTION.linkMachine) {
      await vscode.commands.executeCommand(LINK_MACHINE_COMMAND);
    }
  }

  /**
   * One message per code per session. Returns true when the code has a
   * user-facing message (shown now or earlier), false for the quiet codes.
   */
  private async showErrorOnce(code: DeviceKeyErrorCode, trigger: EnsureTrigger): Promise<boolean> {
    const message = messageForDeviceKeyError(code);
    if (!message) return false;
    // The user asked to link: always answer them.
    if (this.shownCodes.has(code) && trigger !== "link-machine") return true;
    this.shownCodes.add(code);
    const show =
      message.severity === "error"
        ? vscode.window.showErrorMessage
        : message.severity === "warning"
          ? vscode.window.showWarningMessage
          : vscode.window.showInformationMessage;
    const action = await show(message.text, ...message.actions);
    await this.runAction(action);
    return true;
  }

  private async runAction(action: string | undefined): Promise<void> {
    switch (action) {
      case ACTION.startTrial:
        await vscode.commands.executeCommand(COMMAND.startTrial);
        break;
      case ACTION.viewPricing:
      case ACTION.renew:
        await vscode.env.openExternal(vscode.Uri.parse(PRICING_PAGE_URL));
        break;
      case ACTION.activateLicense:
        await vscode.commands.executeCommand(COMMAND.activateLicense);
        break;
      case ACTION.manageMachines:
        if (this.deps.machinesUrl) {
          await vscode.env.openExternal(vscode.Uri.parse(this.deps.machinesUrl()));
        }
        break;
      case ACTION.signIn:
        await vscode.commands.executeCommand(COMMAND.signIn);
        break;
      default:
        break;
    }
  }
}

export interface DeviceKeyMessage {
  severity: "info" | "warning" | "error";
  text: string;
  actions: ActionLabel[];
}

/**
 * The message for each device-key failure (#1883). RATE_LIMITED and
 * UNAVAILABLE are quiet (logged; the next sign-in or restore retries), so they
 * return null.
 */
export function messageForDeviceKeyError(code: DeviceKeyErrorCode): DeviceKeyMessage | null {
  switch (code) {
    case "LICENSE_NOT_FOUND":
      return {
        severity: "info",
        text: "Nightgauge: Your account has no license yet, so this machine stays on the free Community tier. Start a free Pro trial or choose a plan.",
        actions: [ACTION.startTrial, ACTION.viewPricing],
      };
    case "LICENSE_EXPIRED":
      return {
        severity: "warning",
        text: "Nightgauge: Your account's license has expired, so this machine could not be linked. Renew it to restore your paid tier.",
        actions: [ACTION.renew],
      };
    case "LICENSE_REVOKED":
      return {
        severity: "error",
        text: "Nightgauge: Your account's license is revoked or suspended, so this machine could not be linked. Contact support, or activate another license key.",
        actions: [ACTION.activateLicense],
      };
    case "LICENSE_MACHINE_LIMIT":
      return {
        severity: "warning",
        text: "Nightgauge: Your account has reached its machine limit, so this machine could not be linked. Remove a machine you no longer use, or upgrade your plan.",
        actions: [ACTION.manageMachines, ACTION.activateLicense],
      };
    case "UNAUTHORIZED":
      return {
        severity: "warning",
        text: "Nightgauge: Your session has expired, so this machine could not be linked. Sign in again.",
        actions: [ACTION.signIn],
      };
    case "RATE_LIMITED":
    case "UNAVAILABLE":
      return null;
  }
}
