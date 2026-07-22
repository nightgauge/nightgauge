/**
 * SecretStorageService - Singleton wrapper around VSCode SecretStorage
 *
 * Provides secure API key management for adapters that require credentials.
 * Uses VSCode's built-in SecretStorage API (available since VSCode 1.53).
 *
 * @see Issue #1056 - Gemini VSCode configuration UI
 */

import * as vscode from "vscode";

type ApiKeyName = "gemini" | "anthropic";

const KEY_PREFIX = "nightgauge";

const KEY_MAP: Record<ApiKeyName, string> = {
  gemini: `${KEY_PREFIX}.gemini.apiKey`,
  anthropic: `${KEY_PREFIX}.anthropic.apiKey`,
};

/** Well-known secret keys for non-API-key credentials (webhooks, tokens, etc.) */
export const SECRET_KEYS = {
  discordWebhookUrl: `${KEY_PREFIX}.discord.webhookUrl`,
  mattermostWebhookUrl: `${KEY_PREFIX}.mattermost.webhookUrl`,
  mattermostBotToken: `${KEY_PREFIX}.mattermost.botToken`,
  platformLicenseKey: `${KEY_PREFIX}.platform.licenseKey`,
  platformAccessToken: `${KEY_PREFIX}.platform.accessToken`,
  platformRefreshToken: `${KEY_PREFIX}.platform.refreshToken`,
  platformTokenExpiresAt: `${KEY_PREFIX}.platform.tokenExpiresAt`,
  platformUserEmail: `${KEY_PREFIX}.platform.userEmail`,
  platformUserTier: `${KEY_PREFIX}.platform.userTier`,
  platformUserRole: `${KEY_PREFIX}.platform.userRole`,
} as const;

/** Prefix for per-channel Mattermost outgoing-webhook signing tokens */
export const MATTERMOST_SIGNING_KEY_PREFIX = `${KEY_PREFIX}.mattermost.signingToken.`;

/** Prefix for per-forge-instance credentials. Key pattern: nightgauge-forge-<instanceId> */
export const FORGE_SECRET_PREFIX = `${KEY_PREFIX}-forge-`;

/** Prefix for per-environment platform credentials. Key pattern: nightgauge.platform.{env}.{field} */
export const PLATFORM_ENV_TOKEN_PREFIX = `${KEY_PREFIX}.platform.`;

/**
 * Returns the SecretStorage key for a per-environment platform token.
 * envKey: PlatformEnvironment string ("production"|"canary"|"local") or normalized hostname for custom envs.
 */
export function platformTokenKey(envKey: string, field: string): string {
  return `${PLATFORM_ENV_TOKEN_PREFIX}${envKey}.${field}`;
}

/** Maps TokenKey names to their storage sub-key suffix. */
export const PLATFORM_TOKEN_FIELDS = {
  accessToken: "accessToken",
  refreshToken: "refreshToken",
  expiresAt: "tokenExpiresAt",
  userEmail: "userEmail",
  userTier: "userTier",
  userRole: "userRole",
} as const satisfies Record<string, string>;

/** Build the SecretStorage key for a per-channel Mattermost signing token */
export function mattermostSigningKey(channelId: string): string {
  return `${MATTERMOST_SIGNING_KEY_PREFIX}${channelId}`;
}

export class SecretStorageService implements vscode.Disposable {
  private static instance: SecretStorageService | null = null;
  private secrets: vscode.SecretStorage;

  private constructor(secrets: vscode.SecretStorage) {
    this.secrets = secrets;
  }

  static initialize(secrets: vscode.SecretStorage): void {
    if (SecretStorageService.instance) {
      return;
    }
    SecretStorageService.instance = new SecretStorageService(secrets);
  }

  static getInstance(): SecretStorageService | null {
    return SecretStorageService.instance;
  }

  static resetInstance(): void {
    SecretStorageService.instance = null;
  }

  async getApiKey(key: ApiKeyName): Promise<string | undefined> {
    return this.secrets.get(KEY_MAP[key]);
  }

  async setApiKey(key: ApiKeyName, value: string): Promise<void> {
    await this.secrets.store(KEY_MAP[key], value);
  }

  async deleteApiKey(key: ApiKeyName): Promise<void> {
    await this.secrets.delete(KEY_MAP[key]);
  }

  async hasApiKey(key: ApiKeyName): Promise<boolean> {
    const value = await this.secrets.get(KEY_MAP[key]);
    return value !== undefined && value.length > 0;
  }

  /** Store a named secret (for webhook URLs, tokens, etc.) */
  async setSecret(key: string, value: string): Promise<void> {
    await this.secrets.store(key, value);
  }

  /** Retrieve a named secret, or undefined if not set */
  async getSecret(key: string): Promise<string | undefined> {
    return this.secrets.get(key);
  }

  /** Delete a named secret */
  async deleteSecret(key: string): Promise<void> {
    await this.secrets.delete(key);
  }

  /** Retrieve the credential for a forge instance */
  async getForgeSecret(instanceId: string): Promise<string | undefined> {
    return this.secrets.get(`${FORGE_SECRET_PREFIX}${instanceId}`);
  }

  /** Store the credential for a forge instance */
  async setForgeSecret(instanceId: string, secret: string): Promise<void> {
    await this.secrets.store(`${FORGE_SECRET_PREFIX}${instanceId}`, secret);
  }

  /** Delete the credential for a forge instance */
  async deleteForgeSecret(instanceId: string): Promise<void> {
    await this.secrets.delete(`${FORGE_SECRET_PREFIX}${instanceId}`);
  }

  /** Retrieve the last-tested ISO timestamp for a forge instance */
  async getForgeLastTested(instanceId: string): Promise<string | undefined> {
    return this.secrets.get(`${FORGE_SECRET_PREFIX}${instanceId}.lastTested`);
  }

  /** Store the last-tested ISO timestamp for a forge instance */
  async setForgeLastTested(instanceId: string, isoTimestamp: string): Promise<void> {
    await this.secrets.store(`${FORGE_SECRET_PREFIX}${instanceId}.lastTested`, isoTimestamp);
  }

  dispose(): void {
    // SecretStorage lifecycle is managed by VSCode
  }
}
