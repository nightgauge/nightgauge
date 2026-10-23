/**
 * Notifier credential resolution — one path, one place.
 *
 * Every notifier's credential lives in VSCode SecretStorage, written by the
 * `Nightgauge: Configure … Notifications` commands and the Notifier Settings
 * panel. A fixed environment variable is the CI fallback.
 *
 * ## Why the env-var *name* is no longer configurable (#1107)
 *
 * Each notifier used to expose the name of its fallback variable as a free-text
 * setting (`slack.bot_token_env`, `discord.webhook_env`,
 * `mattermost.webhook_env`), rendered in the settings GUI directly beneath the
 * fields that genuinely take a channel or a URL. A box whose correct value is
 * "the name of the place the secret lives", styled identically to boxes whose
 * correct value *is* the secret, collects secrets.
 *
 * It did. An operator pasted a live `xoxb-` bot token into "Bot Token Env Var";
 * the lookup became `process.env["xoxb-…"]`, resolved to `undefined`, Slack
 * silently never posted (#1106), and the token was written in plaintext to
 * `config.yaml` — bypassing the SecretStorage path that exists to prevent
 * exactly that.
 *
 * The option bought nothing in exchange. Its documented default was never
 * implemented (`z.string().optional()` with no `.default()`, and a truthiness
 * guard on the key), so the documented headless setup — export the variable,
 * omit the optional key — resolved no token at all.
 *
 * So the name is a constant now. It cannot be mistaken for a place to paste a
 * token, and it is what the documentation always claimed.
 */

import type { Logger } from "../../utils/logger";

/** Fixed CI-fallback environment variable per notifier. Not configurable. */
export const CREDENTIAL_ENV_VAR = {
  slack: "SLACK_BOT_TOKEN",
  discord: "DISCORD_WEBHOOK_URL",
  mattermost: "MATTERMOST_WEBHOOK_URL",
} as const;

export type NotifierId = keyof typeof CREDENTIAL_ENV_VAR;

/** The removed config key each notifier used to expose. */
export const LEGACY_ENV_KEY: Record<NotifierId, string> = {
  slack: "bot_token_env",
  discord: "webhook_env",
  mattermost: "webhook_env",
};

/**
 * A POSIX environment variable name. Anything that fails this is not a variable
 * name, which makes it conclusive evidence that a *value* was pasted into the
 * name field — no guessing at secret prefixes required.
 */
const ENV_VAR_NAME = /^[A-Za-z_][A-Za-z0-9_]*$/;

/**
 * Report whether a legacy `*_env` value is a secret rather than a variable
 * name. Deliberately shape-based rather than prefix-based: it catches an
 * `xoxb-` token, a webhook URL, and anything else that cannot name a variable.
 */
export function isPastedSecret(value: string): boolean {
  const v = value.trim();
  if (v === "") return false;
  return !ENV_VAR_NAME.test(v);
}

/**
 * Refuse a legacy `*_env` value and tell the operator what to do about it.
 *
 * The value is never used — not as a variable name, and not as a credential.
 * When it is secret-shaped the message leads with rotation, because by the time
 * this runs the secret has already been written to a plaintext config file and
 * whatever else reads that file has seen it.
 *
 * Returns true when a legacy key was present, so callers can attribute an
 * otherwise-silent inert notifier to it.
 */
export function warnOnLegacyEnvKey(
  logger: Logger | undefined,
  notifier: NotifierId,
  config: Record<string, unknown> | null | undefined,
  configureCommand: string
): boolean {
  const key = LEGACY_ENV_KEY[notifier];
  const raw = config?.[key];
  if (typeof raw !== "string" || raw.trim() === "") return false;

  if (isPastedSecret(raw)) {
    logger?.warn(
      `${notifier}: notifications.${notifier}.${key} holds a secret, not the name of an environment variable. ` +
        "It has NOT been used. Rotate this credential now — it was written in plaintext to config.yaml — " +
        `then store the new one with "${configureCommand}". Finally delete ${key} from config.yaml; ` +
        `the CI fallback is the fixed variable ${CREDENTIAL_ENV_VAR[notifier]}.`
    );
  } else {
    logger?.warn(
      `${notifier}: notifications.${notifier}.${key} is no longer supported and has been ignored. ` +
        `The CI fallback is now the fixed variable ${CREDENTIAL_ENV_VAR[notifier]}; ` +
        `delete ${key} from config.yaml.`
    );
  }
  return true;
}
