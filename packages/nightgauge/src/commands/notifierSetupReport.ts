/**
 * notifierSetupReport — say something true after a notifier credential is stored.
 *
 * Each configure command used to end with an unconditional instruction to go
 * hand-edit YAML:
 *
 *   "Slack bot token stored. Add the channel to .nightgauge/config.yaml to finish."
 *   "Discord webhook configured. Make sure notifications.discord.enabled: true
 *    is set in .nightgauge/config.yaml."
 *
 * Three things were wrong with that, and they compounded:
 *
 *  1. **It never checked.** The message fired whether or not the setting was
 *     already present, so an operator whose channel had been configured for
 *     months was told the setup was unfinished. Slack's version also prompted
 *     for the channel, validated it, and then discarded it — asking for a value
 *     and then telling the user to go enter that same value by hand.
 *  2. **It named the wrong file.** `.nightgauge/config.yaml` is the PROJECT
 *     tier; `notifications.*` is normally set in the MACHINE tier
 *     (`~/.nightgauge/config.yaml`). Following the instruction produced a second,
 *     shadowing block — the same tier confusion the daemon already warns about
 *     ("github_user is in project YAML but is owned by the machine tier").
 *  3. **It was stale.** #1097 gave the notifications block a settings surface, so
 *     these values are editable in the Settings panel. Directing people to raw
 *     YAML described a world that no longer existed.
 *
 * This module reads what is actually configured and reports only what is
 * actually missing.
 */

import * as vscode from "vscode";
import { ConfigBridge } from "../services/ConfigBridge";
import { NightgaugeYamlService } from "../views/settings/NightgaugeYamlService";
import { getWorkspaceRoot } from "../config/settings";

export type NotifierId = "slack" | "discord" | "mattermost";

const LABEL: Record<NotifierId, string> = {
  slack: "Slack",
  discord: "Discord",
  mattermost: "Mattermost",
};

/** Read `notifications.<id>` from the effective (merged-tier) config. */
function readNotifierConfig(id: NotifierId): Record<string, unknown> | null {
  try {
    const result = ConfigBridge.getInstance()?.getEffectiveConfig();
    // eslint-disable-next-line @typescript-eslint/no-explicit-any
    return ((result?.config as any)?.notifications?.[id] as Record<string, unknown>) ?? null;
  } catch {
    return null;
  }
}

/**
 * Persist notifier config to the MACHINE tier (`~/.nightgauge/config.yaml`).
 *
 * Machine tier, not project: a notifier's channel and enablement are properties
 * of this operator's machine, not of the repository, and writing them into a
 * committed project file would share one person's channel with everyone.
 *
 * Returns false when the write could not be attempted or failed; callers then
 * fall back to telling the operator what to set themselves.
 */
async function writeMachineNotifierConfig(
  id: NotifierId,
  patch: Record<string, unknown>
): Promise<boolean> {
  const root = getWorkspaceRoot();
  if (!root) return false;
  try {
    const yaml = new NightgaugeYamlService(root);
    const result = await yaml.writeGlobal({
      notifications: { [id]: patch },
    } as Parameters<NightgaugeYamlService["writeGlobal"]>[0]);
    return result.success === true;
  } catch {
    return false;
  }
}

/**
 * Enable a notifier and, for Slack, record its channel — then report the real
 * remaining state. Offers the Settings panel rather than a YAML path, because
 * that is where these values are edited now.
 */
export async function reportNotifierSetup(
  id: NotifierId,
  opts: { channel?: string } = {}
): Promise<void> {
  const label = LABEL[id];
  const existing = readNotifierConfig(id);

  const patch: Record<string, unknown> = {};
  if (existing?.enabled !== true) patch.enabled = true;
  if (id === "slack" && opts.channel) {
    const current = typeof existing?.channel === "string" ? existing.channel.trim() : "";
    if (current !== opts.channel.trim()) patch.channel = opts.channel.trim();
  }

  // Nothing to change: the credential is stored and config already agrees.
  if (Object.keys(patch).length === 0) {
    const where =
      id === "slack" && typeof existing?.channel === "string"
        ? ` Pipeline status will post to ${existing.channel}.`
        : "";
    vscode.window.showInformationMessage(`Nightgauge: ${label} is configured and enabled.${where}`);
    return;
  }

  const wrote = await writeMachineNotifierConfig(id, patch);

  if (wrote) {
    const bits: string[] = [];
    if (patch.enabled === true) bits.push("enabled it");
    if (typeof patch.channel === "string") bits.push(`set the channel to ${patch.channel}`);
    const channelNow =
      typeof patch.channel === "string"
        ? patch.channel
        : typeof existing?.channel === "string"
          ? existing.channel
          : "";
    const where =
      id === "slack" && channelNow ? ` Pipeline status will post to ${channelNow}.` : "";
    vscode.window.showInformationMessage(
      `Nightgauge: ${label} credential stored — and ${bits.join(" and ")} in ~/.nightgauge/config.yaml.${where}`
    );
    return;
  }

  // The write did not happen. Say exactly what is still unset and offer the
  // panel that edits it — never a bare "go edit YAML" with the wrong path.
  const missing: string[] = [];
  if (patch.enabled === true) missing.push(`notifications.${id}.enabled: true`);
  if (typeof patch.channel === "string") missing.push(`notifications.${id}.channel`);

  const choice = await vscode.window.showWarningMessage(
    `Nightgauge: ${label} credential stored, but ${missing.join(" and ")} still needs to be set.`,
    "Open Settings"
  );
  if (choice === "Open Settings") {
    await vscode.commands.executeCommand("nightgauge.showSettings");
  }
}
