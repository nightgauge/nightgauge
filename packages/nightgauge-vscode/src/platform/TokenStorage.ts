/**
 * TokenStorage — Typed singleton for platform authentication token persistence.
 *
 * Wraps SecretStorageService with:
 * - Typed TokenKey union (no raw string keys in callers)
 * - Per-host credential scoping to prevent cross-environment token leakage (#3722)
 * - Event emission on store/delete/clear
 * - Bulk clear() for sign-out (scoped to the active host)
 * - One-time migration of legacy unscoped tokens to production-scoped keys
 *
 * @see Issue #1465 - Integrate vscode.SecretStorage for secure token persistence
 * @see Issue #3722 - Scope auth cookies/tokens per host
 */

import * as vscode from "vscode";
import {
  SecretStorageService,
  SECRET_KEYS,
  platformTokenKey,
  PLATFORM_TOKEN_FIELDS,
} from "../services/SecretStorageService";
import { PLATFORM_ENV_PRESETS } from "../config/schema";

/** Typed keys for platform authentication tokens and user profile. */
export type TokenKey =
  "accessToken" | "refreshToken" | "expiresAt" | "userEmail" | "userTier" | "userRole";

/**
 * Payload emitted on every token mutation.
 *
 * `rekeyed` is not a mutation of the stored bytes — it says the *bucket* those
 * bytes live in has changed (the platform host switched), so anything holding a
 * value read from this storage must re-read it. See {@link TokenStorage.notifyHostChanged}.
 */
export interface TokenChangeEvent {
  key: TokenKey | "all";
  action: "stored" | "deleted" | "cleared" | "rekeyed";
}

export interface ITokenStorage extends vscode.Disposable {
  store(key: TokenKey, value: string): Promise<void>;
  retrieve(key: TokenKey): Promise<string | null>;
  delete(key: TokenKey): Promise<void>;
  clear(): Promise<void>;
  notifyHostChanged(): void;
  readonly onTokenChanged: vscode.Event<TokenChangeEvent>;
}

/** Maps TokenKey to the per-environment storage sub-key field. */
const TOKEN_FIELD_MAP: Record<TokenKey, string> = {
  accessToken: PLATFORM_TOKEN_FIELDS.accessToken,
  refreshToken: PLATFORM_TOKEN_FIELDS.refreshToken,
  expiresAt: PLATFORM_TOKEN_FIELDS.expiresAt,
  userEmail: PLATFORM_TOKEN_FIELDS.userEmail,
  userTier: PLATFORM_TOKEN_FIELDS.userTier,
  userRole: PLATFORM_TOKEN_FIELDS.userRole,
};

/** Legacy unscoped keys mapped to their production-scoped equivalents (for one-time migration). */
const LEGACY_KEY_MAP: ReadonlyArray<{ legacy: string; field: string }> = [
  { legacy: SECRET_KEYS.platformAccessToken, field: PLATFORM_TOKEN_FIELDS.accessToken },
  { legacy: SECRET_KEYS.platformRefreshToken, field: PLATFORM_TOKEN_FIELDS.refreshToken },
  { legacy: SECRET_KEYS.platformTokenExpiresAt, field: PLATFORM_TOKEN_FIELDS.expiresAt },
  { legacy: SECRET_KEYS.platformUserEmail, field: PLATFORM_TOKEN_FIELDS.userEmail },
  { legacy: SECRET_KEYS.platformUserTier, field: PLATFORM_TOKEN_FIELDS.userTier },
  { legacy: SECRET_KEYS.platformUserRole, field: PLATFORM_TOKEN_FIELDS.userRole },
];

export class TokenStorage implements ITokenStorage {
  private static instance: TokenStorage | null = null;

  private readonly _onTokenChanged = new vscode.EventEmitter<TokenChangeEvent>();
  readonly onTokenChanged = this._onTokenChanged.event;

  private constructor(
    private readonly secretService: SecretStorageService,
    private readonly _getHostKey: () => string
  ) {}

  static initialize(secretService: SecretStorageService, getHostKey: () => string): void {
    if (TokenStorage.instance) {
      return;
    }
    TokenStorage.instance = new TokenStorage(secretService, getHostKey);
  }

  static getInstance(): TokenStorage | null {
    return TokenStorage.instance;
  }

  static resetInstance(): void {
    TokenStorage.instance?.dispose();
    TokenStorage.instance = null;
  }

  /** Returns the SecretStorage keys for the currently active host. */
  private getKeyMap(): Record<TokenKey, string> {
    let host: string;
    try {
      host = this._getHostKey();
    } catch {
      host = "production"; // safe fallback if config is not yet loaded
    }
    return {
      accessToken: platformTokenKey(host, TOKEN_FIELD_MAP.accessToken),
      refreshToken: platformTokenKey(host, TOKEN_FIELD_MAP.refreshToken),
      expiresAt: platformTokenKey(host, TOKEN_FIELD_MAP.expiresAt),
      userEmail: platformTokenKey(host, TOKEN_FIELD_MAP.userEmail),
      userTier: platformTokenKey(host, TOKEN_FIELD_MAP.userTier),
      userRole: platformTokenKey(host, TOKEN_FIELD_MAP.userRole),
    };
  }

  async store(key: TokenKey, value: string): Promise<void> {
    await this.secretService.setSecret(this.getKeyMap()[key], value);
    try {
      this._onTokenChanged.fire({ key, action: "stored" });
    } catch {
      // Event emission is fire-and-forget — errors must not propagate to callers
    }
  }

  async retrieve(key: TokenKey): Promise<string | null> {
    const value = await this.secretService.getSecret(this.getKeyMap()[key]);
    return value ?? null;
  }

  async delete(key: TokenKey): Promise<void> {
    await this.secretService.deleteSecret(this.getKeyMap()[key]);
    try {
      this._onTokenChanged.fire({ key, action: "deleted" });
    } catch {
      // Event emission is fire-and-forget
    }
  }

  /** Idempotent — removes all platform tokens for the active host in sequence. */
  async clear(): Promise<void> {
    const keyMap = this.getKeyMap();
    await this.secretService.deleteSecret(keyMap.accessToken);
    await this.secretService.deleteSecret(keyMap.refreshToken);
    await this.secretService.deleteSecret(keyMap.expiresAt);
    await this.secretService.deleteSecret(keyMap.userEmail);
    await this.secretService.deleteSecret(keyMap.userTier);
    await this.secretService.deleteSecret(keyMap.userRole);
    try {
      this._onTokenChanged.fire({ key: "all", action: "cleared" });
    } catch {
      // Event emission is fire-and-forget
    }
  }

  /**
   * Announce that the active host key changed, so every credential derived from
   * this storage is re-read against the new bucket.
   *
   * Storage is per-host ({@link platformTokenKey}), so a host switch silently
   * changes what every key resolves to. Consumers that cached a value — above
   * all the PlatformCredentialBridge, which mirrors the access token into the Go
   * daemon — would otherwise keep serving a credential for the previous host
   * with nothing to tell them it went stale.
   *
   * @see Issue #797 - One endpoint, one token key
   */
  notifyHostChanged(): void {
    try {
      this._onTokenChanged.fire({ key: "all", action: "rekeyed" });
    } catch {
      // Event emission is fire-and-forget
    }
  }

  /**
   * Migrates tokens written under a superseded key scheme into the current one.
   *
   * Two schemes preceded this one and both are handled here:
   *
   * 1. **Unscoped keys** — a single global bucket predating per-host scoping,
   *    moved to "production".
   * 2. **Hostname-keyed preset buckets** (pre-#797) — `resolvePlatformHostKey`
   *    returned a preset's *hostname* whenever `environment` was not set
   *    explicitly, so the same endpoint could be written as both
   *    "api.nightgauge.dev" and "production". Tokens stranded under the
   *    hostname form are moved to the preset name, which is now the only key a
   *    preset endpoint produces. Without this, the correctness fix would read
   *    as a forced sign-out for every user whose token landed in the hostname
   *    bucket.
   *
   * Run once on extension activation. Idempotent and safe to call repeatedly;
   * a bucket already holding an access token is never overwritten.
   *
   * @see Issue #797 - One endpoint, one token key
   */
  async migrateFromLegacy(): Promise<void> {
    await this.migrateHostnameKeyedPresets();

    const legacyAccessToken = await this.secretService.getSecret(SECRET_KEYS.platformAccessToken);
    if (!legacyAccessToken) {
      return; // Nothing to migrate
    }

    const prodAccessKey = platformTokenKey("production", PLATFORM_TOKEN_FIELDS.accessToken);
    const alreadyMigrated = await this.secretService.getSecret(prodAccessKey);
    if (alreadyMigrated) {
      // Migration already done — clean up remaining legacy keys
      for (const { legacy } of LEGACY_KEY_MAP) {
        await this.secretService.deleteSecret(legacy);
      }
      return;
    }

    // Copy each legacy key to its production-scoped equivalent
    for (const { legacy, field } of LEGACY_KEY_MAP) {
      const value = await this.secretService.getSecret(legacy);
      if (value) {
        await this.secretService.setSecret(platformTokenKey("production", field), value);
      }
    }

    // Delete legacy keys after successful migration
    for (const { legacy } of LEGACY_KEY_MAP) {
      await this.secretService.deleteSecret(legacy);
    }
  }

  /**
   * Moves tokens out of hostname-keyed buckets for endpoints that are now
   * addressed by preset name (pre-#797 writes).
   *
   * The destination is only written when it is empty: a token already sitting
   * under the preset key is the current session and must win over whatever an
   * older build stranded under the hostname.
   */
  private async migrateHostnameKeyedPresets(): Promise<void> {
    for (const [preset, presetUrl] of Object.entries(PLATFORM_ENV_PRESETS)) {
      if (!presetUrl) continue; // "custom" has no fixed endpoint

      let hostname: string;
      try {
        hostname = new URL(presetUrl).hostname.toLowerCase();
      } catch {
        continue;
      }
      if (hostname === preset) continue; // Nothing to move

      const staleAccessKey = platformTokenKey(hostname, PLATFORM_TOKEN_FIELDS.accessToken);
      const staleAccessToken = await this.secretService.getSecret(staleAccessKey);
      if (!staleAccessToken) continue;

      const currentAccessKey = platformTokenKey(preset, PLATFORM_TOKEN_FIELDS.accessToken);
      const alreadyCurrent = await this.secretService.getSecret(currentAccessKey);

      for (const field of Object.values(PLATFORM_TOKEN_FIELDS)) {
        const staleKey = platformTokenKey(hostname, field);
        if (!alreadyCurrent) {
          const value = await this.secretService.getSecret(staleKey);
          if (value) {
            await this.secretService.setSecret(platformTokenKey(preset, field), value);
          }
        }
        await this.secretService.deleteSecret(staleKey);
      }
    }
  }

  dispose(): void {
    this._onTokenChanged.dispose();
  }
}
