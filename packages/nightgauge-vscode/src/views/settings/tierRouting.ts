/**
 * Which file tier a config write lands in.
 *
 * Extracted from `SettingsPanel` so `NightgaugeYamlService` can route a
 * runtime write without importing the panel (which imports the service).
 * `SettingsPanel` re-exports `MACHINE_TIER_KEY_PATHS` so existing importers
 * are unaffected.
 *
 * @see docs/SETTINGS_ARCHITECTURE.md — tier table and write targets
 * @see Issue #1516 — runtime writes must never touch the committed team file
 */

/**
 * Dotted-path config keys that are personal/machine-specific — routed to
 * `~/.nightgauge/config.yaml` (machine tier) and stripped from the
 * project-tier YAML write. Keys here must never appear in a committed file.
 *
 * Phase 4 of the settings-tier epic promoted the machine tier to first class;
 * the license key was moved onto it so it is never committed.
 */
export const MACHINE_TIER_KEY_PATHS = new Set<string>([
  "ui.core.default_model",
  "ui.core.fallback_model",
  "ui.core.auth_provider",
  // Discord and Mattermost live under the TOP-LEVEL `notifications:` key, not
  // under `ui.notifications` — UINotificationsConfigSchema holds only the
  // VSCode-native sounds/banners and has no discord or mattermost member. The
  // old paths could never match, so these webhook env names were never
  // stripped as machine-tier keys. Found by #499.
  "notifications.discord.webhook_env",
  "notifications.mattermost.webhook_env",
  "platform.license_key",
]);

/**
 * True when `dottedPath` is a machine-tier key, or sits underneath one.
 *
 * The prefix rule matters for block-level keys: writing `platform.license_key`
 * and writing `platform` must both route to the machine file.
 */
export function isMachineTierPath(dottedPath: string): boolean {
  if (MACHINE_TIER_KEY_PATHS.has(dottedPath)) return true;
  for (const machinePath of MACHINE_TIER_KEY_PATHS) {
    if (dottedPath.startsWith(`${machinePath}.`)) return true;
  }
  return false;
}

/**
 * The tier a runtime/UI write of `dottedPath` must target.
 *
 * Never `"project"`: the committed team file changes only through an explicit
 * user action that names it (the Settings panel's Project tab, or
 * "Edit team config"), never as a side effect of flipping a control (#1516).
 */
export function runtimeWriteTierFor(dottedPath: string): "local" | "global" {
  return isMachineTierPath(dottedPath) ? "global" : "local";
}
