/**
 * missingCredentialsAudit — the activation-time check for enabled features
 * whose credential is missing (#2513).
 *
 * The extension identifier changed from `nightgauge.nightgauge-vscode` to
 * `nightgauge.nightgauge`. VS Code scopes SecretStorage per extension ID, so
 * every Slack, Discord, Mattermost and platform credential the old build
 * stored is unreadable — and no extension can read another's secrets, so they
 * cannot be migrated. A feature config still marks enabled then goes quiet,
 * and the only trace used to be a WARN repeated on every stage.
 *
 * This module raises ONE Action Center card per activation naming each missing
 * credential. The card's words are the daemon's (`missing-credentials`
 * producer, `BuildMissingCredentials`): this side sends credential ids from a
 * closed enum and one boolean, nothing else.
 *
 * "Present" mirrors each service's own resolution — SecretStorage first, then
 * the env-var fallback — so the audit never flags a credential the service
 * itself can find.
 *
 * Fail-open: a failed probe or raise is logged once and never blocks
 * activation.
 */

import { existsSync } from "fs";
import { dirname, join } from "path";
import { SECRET_KEYS } from "../services/SecretStorageService";
import { CREDENTIAL_ENV_VAR } from "../services/notifications/credentials";

/** The closed credential enum — must match orchestrator.MissingCredentialIDs. */
export type MissingCredentialId =
  "slack-bot-token" | "discord-webhook" | "mattermost-webhook" | "platform-license";

/** Global-storage directory name of the retired extension identifier. */
export const LEGACY_EXTENSION_ID = "nightgauge.nightgauge-vscode";

/** The slice of effective config the audit reads. */
export interface CredentialAuditConfig {
  notifications?: {
    slack?: { enabled?: boolean };
    discord?: { enabled?: boolean };
    mattermost?: { enabled?: boolean };
  };
  platform?: { enabled?: boolean };
}

export interface CredentialProbe {
  /** Read one SecretStorage key; undefined/empty means absent. */
  getSecret(key: string): Promise<string | undefined>;
  /** The platform OAuth access token, wherever TokenStorage keeps it. */
  getAccessToken(): Promise<string | null | undefined>;
  env: Record<string, string | undefined>;
}

/**
 * Pure: which enabled features have no resolvable credential. Sorted, so the
 * daemon's idempotency key is stable.
 */
export async function findMissingCredentials(
  config: CredentialAuditConfig | null | undefined,
  probe: CredentialProbe
): Promise<MissingCredentialId[]> {
  const missing: MissingCredentialId[] = [];
  const n = config?.notifications;
  const has = async (key: string, envName?: string): Promise<boolean> =>
    Boolean(await probe.getSecret(key)) || Boolean(envName && probe.env[envName]);

  if (
    n?.slack?.enabled === true &&
    !(await has(SECRET_KEYS.slackBotToken, CREDENTIAL_ENV_VAR.slack))
  ) {
    missing.push("slack-bot-token");
  }
  if (
    n?.discord?.enabled === true &&
    !(await has(SECRET_KEYS.discordWebhookUrl, CREDENTIAL_ENV_VAR.discord))
  ) {
    missing.push("discord-webhook");
  }
  if (
    n?.mattermost?.enabled === true &&
    !(await has(SECRET_KEYS.mattermostWebhookUrl, CREDENTIAL_ENV_VAR.mattermost))
  ) {
    missing.push("mattermost-webhook");
  }
  // Mirrors TelemetryUploaderService: a license key, else the access token.
  if (
    config?.platform?.enabled === true &&
    !(await has(SECRET_KEYS.platformLicenseKey)) &&
    !(await probe.getAccessToken())
  ) {
    missing.push("platform-license");
  }
  return missing.sort();
}

/**
 * Whether the retired extension left its global-storage directory beside
 * ours. The extension API cannot list another extension's secrets; this is the
 * closest observable signal that this machine ran the old identifier.
 */
export function legacyExtensionDataPresent(globalStoragePath: string | undefined): boolean {
  if (!globalStoragePath) return false;
  try {
    return existsSync(join(dirname(globalStoragePath), LEGACY_EXTENSION_ID));
  } catch {
    return false;
  }
}

export interface MissingCredentialsAuditDeps {
  config: CredentialAuditConfig | null | undefined;
  probe: CredentialProbe;
  /** "owner/name" of the primary repo; the audit skips when absent. */
  repo: string | undefined;
  globalStoragePath: string | undefined;
  raise(
    repo: string,
    credentials: MissingCredentialId[],
    legacyIdDetected: boolean
  ): Promise<unknown>;
  logger?: { warn(msg: string, meta?: Record<string, unknown>): void };
}

/**
 * Raise at most one card. Never throws; returns the ids it raised for, or an
 * empty list when nothing was missing or the raise was skipped/failed.
 */
export async function auditMissingCredentials(
  deps: MissingCredentialsAuditDeps
): Promise<MissingCredentialId[]> {
  try {
    const missing = await findMissingCredentials(deps.config, deps.probe);
    if (missing.length === 0) return [];
    if (!deps.repo || !deps.repo.includes("/")) {
      deps.logger?.warn("Missing credentials for enabled features, but no repo to card it on", {
        missing,
      });
      return [];
    }
    await deps.raise(deps.repo, missing, legacyExtensionDataPresent(deps.globalStoragePath));
    return missing;
  } catch (err) {
    deps.logger?.warn("Missing-credentials audit could not raise its card", {
      error: err instanceof Error ? err.message : String(err),
    });
    return [];
  }
}
