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

/** Typed keys for platform authentication tokens and user profile. */
export type TokenKey =
  "accessToken" | "refreshToken" | "expiresAt" | "userEmail" | "userTier" | "userRole";

/** Payload emitted on every token mutation. */
export interface TokenChangeEvent {
  key: TokenKey | "all";
  action: "stored" | "deleted" | "cleared";
}

export interface ITokenStorage extends vscode.Disposable {
  store(key: TokenKey, value: string): Promise<void>;
  retrieve(key: TokenKey): Promise<string | null>;
  delete(key: TokenKey): Promise<void>;
  clear(): Promise<void>;
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
   * Migrates legacy unscoped platform tokens to production-scoped keys.
   * Run once on extension activation for users upgrading from pre-#3722 builds.
   * No-op if legacy tokens do not exist or migration has already been done.
   */
  async migrateFromLegacy(): Promise<void> {
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

  dispose(): void {
    this._onTokenChanged.dispose();
  }
}
