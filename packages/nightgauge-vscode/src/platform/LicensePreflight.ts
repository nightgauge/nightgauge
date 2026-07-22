/**
 * LicensePreflight — Pre-pipeline license validation with caching and degradation.
 *
 * Called by PipelineBridge before any pipeline stage executes. Validates the
 * user's license key against the Go IPC backend and caches the result for the
 * pipeline run lifetime (5-minute TTL). Handles all degradation cases:
 * - No license key → community tier (allow)
 * - Not authenticated → community tier (allow)
 * - Offline/timeout → community tier (allow)
 * - Invalid license → block with appropriate message
 *
 * @see Issue #1470 - Integrate license validation call at pipeline preflight
 * @see Issue #2091 - Migrated from PlatformApiClient HTTP to Go IPC
 */

import * as os from "node:os";
import type { IpcClient } from "../services/IpcClient";
import type { MachineFingerprint } from "./MachineFingerprint";
import type { LicenseStatus, Tier } from "./types";

/** Result of a license preflight check. */
export interface LicensePreflightResult {
  allowed: boolean;
  tier: Tier;
  /**
   * ISO 8601 timestamp — re-validate when Date.now() exceeds this.
   * Derived from CACHE_TTL_MS (IPC LicenseInfo does not include expiresAt).
   */
  cacheUntil: string;
  /** Human-readable block reason (set when allowed=false). */
  reason?: string;
  /** Actionable URL — renew page (expired) or support page (revoked). */
  actionUrl?: string;
  /** License expiry date (ISO 8601), null for community tier (no expiry). */
  expiresAt: string | null;
  /** True when preflight fell back to community tier due to network failure. */
  offline: boolean;
  /**
   * Full license status (#4156). "community" covers both a genuine
   * community-tier license AND a degraded/unconfirmed result (no license
   * key, tier override, offline fallback, IPC error) — those cases carry no
   * definitive status, so they render identically to community in the UI.
   * A confirmed "revoked"/"suspended" from the platform is the one signal
   * that must survive re-serialization over IPC so Go can fail closed on it
   * even after a later timeout (see internal/ipc/license_checker.go).
   */
  status: LicenseStatus | "community";
  /** True when this machine is bound to the license (absent for community). */
  machineBound: boolean;
  /** Number of machines currently bound to the license (0 for community). */
  machineCount: number;
}

export class LicensePreflight {
  private cached: { result: LicensePreflightResult; expiresAt: number } | null = null;
  private static readonly CACHE_TTL_MS = 5 * 60 * 1000; // 5 minutes
  private static readonly PREFLIGHT_TIMEOUT_MS = 5_000;

  constructor(
    private readonly ipcClient: IpcClient,
    private readonly machineFingerprint: MachineFingerprint,
    private readonly getLicenseKey: () => string | undefined,
    private readonly getTierOverride?: () => Tier | undefined
  ) {}

  /** Clear cached result — call at start of each pipeline run. */
  clearCache(): void {
    this.cached = null;
  }

  /**
   * Validate the current license against the platform via IPC.
   * Returns cached result if still fresh.
   */
  async validate(): Promise<LicensePreflightResult> {
    // Return cached if fresh
    if (this.cached && Date.now() < this.cached.expiresAt) {
      return this.cached.result;
    }

    // Tier override — self-hosted / local development bypass
    const tierOverride = this.getTierOverride?.();
    if (tierOverride) {
      return this.cacheResult(this.overrideResult(tierOverride));
    }

    const licenseKey = this.getLicenseKey();
    if (!licenseKey) {
      // No license key configured → community tier (allow)
      return this.cacheResult(this.communityResult());
    }

    try {
      const response = await this.withTimeout(
        this.ipcClient.platformValidateLicense(
          licenseKey,
          this.machineFingerprint.getMachineId(),
          os.hostname(),
          process.platform
        ),
        LicensePreflight.PREFLIGHT_TIMEOUT_MS
      );
      return this.cacheResult(this.interpretResult(response));
    } catch (err) {
      return this.cacheResult(this.handleError(err));
    }
  }

  // ---------------------------------------------------------------------------
  // Private helpers
  // ---------------------------------------------------------------------------

  private communityResult(offline = false): LicensePreflightResult {
    return {
      allowed: true,
      tier: "community",
      cacheUntil: new Date(Date.now() + LicensePreflight.CACHE_TTL_MS).toISOString(),
      expiresAt: null,
      offline,
      status: "community",
      machineBound: false,
      machineCount: 0,
    };
  }

  private overrideResult(tier: Tier): LicensePreflightResult {
    // Self-hosted / local-dev tier override — always allowed, uncapped.
    return {
      allowed: true,
      tier,
      cacheUntil: new Date(Date.now() + LicensePreflight.CACHE_TTL_MS).toISOString(),
      expiresAt: null,
      offline: false,
      status: tier === "community" ? "community" : "active",
      machineBound: false,
      machineCount: 0,
    };
  }

  /**
   * Interpret the platform.validateLicense IPC response (#4156).
   *
   * Honors the richer `status` field the platform sends (active/expired/
   * revoked/suspended) instead of collapsing every `valid:false` case to the
   * same generic "renew" message — revoked/suspended licenses point the user
   * to support, not the renewal page, and (critically) the confirmed status
   * is forwarded back to Go verbatim via pipeline.licenseResult so
   * IpcLicenseChecker can fail closed on it even after a later re-validation
   * times out.
   */
  private interpretResult(response: {
    valid: boolean;
    tier: string;
    status?: string;
    machineBound?: boolean;
    machineCount?: number;
    expiresAt?: string | null;
  }): LicensePreflightResult {
    const cacheUntil = new Date(Date.now() + LicensePreflight.CACHE_TTL_MS).toISOString();
    const status = normalizeLicenseStatus(response.status, response.valid);
    const machineBound = response.machineBound ?? false;
    const machineCount = response.machineCount ?? 0;
    const expiresAt = response.expiresAt ?? null;

    if (response.valid) {
      return {
        allowed: true,
        tier: response.tier as Tier,
        cacheUntil,
        expiresAt,
        offline: false,
        status,
        machineBound,
        machineCount,
      };
    }

    // Go validated and returned invalid — block with a status-specific message.
    const { reason, actionUrl } = messageForBlockedStatus(status);
    return {
      allowed: false,
      tier: "community",
      cacheUntil,
      reason,
      actionUrl,
      expiresAt,
      offline: false,
      status,
      machineBound,
      machineCount,
    };
  }

  /**
   * Degrade gracefully on a timeout or IPC error talking to the platform.
   *
   * Deliberately reports status:"community" (via communityResult) rather
   * than a confirmed LicenseStatus — this is a network-failure fallback, NOT
   * a platform-authoritative answer. offline:true is the signal PipelineBridge
   * uses to withhold this status from the pipeline.licenseResult payload sent
   * to Go (#4156), so a transient failure here can never overwrite Go's
   * cached last-confirmed-revoked/suspended status with a spurious "clean"
   * one.
   */
  private handleError(err: unknown): LicensePreflightResult {
    // AbortError (timeout) — degrade to community, mark offline
    if (err instanceof Error && err.name === "AbortError") {
      console.warn("[LicensePreflight] Timeout (degrading to community tier)");
      return this.communityResult(true);
    }

    // IPC error or other — degrade to community, mark offline
    console.warn(
      `[LicensePreflight] Error (degrading to community): ${err instanceof Error ? err.message : String(err)}`
    );
    return this.communityResult(true);
  }

  private cacheResult(result: LicensePreflightResult): LicensePreflightResult {
    this.cached = {
      result,
      expiresAt: new Date(result.cacheUntil).getTime(),
    };
    return result;
  }

  private withTimeout<T>(promise: Promise<T>, timeoutMs: number): Promise<T> {
    return new Promise<T>((resolve, reject) => {
      const timer = setTimeout(() => {
        const err = new Error("License preflight timeout");
        err.name = "AbortError";
        reject(err);
      }, timeoutMs);

      promise.then(
        (val) => {
          clearTimeout(timer);
          resolve(val);
        },
        (err) => {
          clearTimeout(timer);
          reject(err);
        }
      );
    });
  }
}

// ---------------------------------------------------------------------------
// Module-level helpers (exported for testing)
// ---------------------------------------------------------------------------

const KNOWN_LICENSE_STATUSES: ReadonlySet<string> = new Set([
  "active",
  "expired",
  "revoked",
  "suspended",
]);

/**
 * Normalize the platform's `status` field to a known LicenseStatus.
 *
 * Falls back to a sensible default when the platform omits status (older
 * platform versions) or sends something unrecognized: valid → "active",
 * invalid → "expired" (the existing generic "renew" messaging), matching the
 * pre-#4156 behavior for the case where no richer signal is available.
 */
export function normalizeLicenseStatus(
  status: string | undefined,
  valid: boolean
): LicenseStatus | "community" {
  if (status && KNOWN_LICENSE_STATUSES.has(status)) {
    return status as LicenseStatus;
  }
  return valid ? "active" : "expired";
}

/** Block reason + actionable URL for a blocked (allowed=false) status (#4156). */
export function messageForBlockedStatus(status: LicenseStatus | "community"): {
  reason: string;
  actionUrl: string;
} {
  switch (status) {
    case "revoked":
      return {
        reason: "Your license has been revoked. Contact support for assistance.",
        actionUrl: "https://github.com/nightgauge/nightgauge/issues",
      };
    case "suspended":
      return {
        reason: "Your license has been suspended. Contact support for assistance.",
        actionUrl: "https://github.com/nightgauge/nightgauge/issues",
      };
    case "expired":
    case "community":
    default:
      return {
        reason: "Your license is not valid. Please check your subscription status.",
        actionUrl: "https://nightgauge.dev/account/renew",
      };
  }
}
