/**
 * Gates UI text that names a `/nightgauge:*` command on the Claude plugin
 * actually being available (#2760). The plugin is optional, so a hint must not
 * send someone to a command their install does not have.
 *
 * The state is process-global because the hint sites render synchronously in
 * unrelated layers. `PluginSetupService` publishes it; until then every hint
 * shows its fallback, which is true whether or not the plugin is installed.
 */

import type { PluginState } from "../services/PluginSetupService";

let pluginState: PluginState = "unknown";

export function setPluginState(state: PluginState): void {
  pluginState = state;
}

export function isPluginEnabled(): boolean {
  return pluginState === "enabled";
}

/**
 * `fallback` unless the plugin is enabled, then `/nightgauge:<skill>`, passed
 * through `render` when the command sits inside a longer sentence.
 */
export function pluginHint(
  skill: string,
  fallback: string,
  render: (command: string) => string = (command) => command
): string {
  return isPluginEnabled() ? render(`/nightgauge:${skill}`) : fallback;
}
