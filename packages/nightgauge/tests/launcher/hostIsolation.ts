/**
 * Keeps a VS Code window that runs this package as its development extension
 * off the operator's agent installs and OS secret store (#2935). Shared by the
 * host smoke tier (`tests/vscode-host/launch.ts`) and `npm run demo`
 * (`scripts/demo-session.ts`), so the two launchers cannot drift.
 *
 * A fresh user-data dir has no install stamp, so activation re-syncs every
 * present skill install (#2762 ADR-003). Under the operator's HOME that wrote
 * the worktree's skills into their real ~/.codex, ~/.grok and
 * ~/.config/opencode.
 */
import * as path from "node:path";

/**
 * The environment for such a window: `home` becomes HOME (USERPROFILE on
 * Windows), and the agent-home overrides the setup services honour point
 * under `agentHomes`.
 */
export function agentHomeEnv(
  home: string,
  agentHomes: string,
  platform: NodeJS.Platform = process.platform
): Record<string, string> {
  return {
    HOME: home,
    ...(platform === "win32" ? { USERPROFILE: home } : {}),
    CODEX_HOME: path.join(agentHomes, "codex"),
    GROK_HOME: path.join(agentHomes, "grok"),
    XDG_CONFIG_HOME: path.join(agentHomes, "config"),
    CLAUDE_CONFIG_DIR: path.join(agentHomes, "claude"),
  };
}

/**
 * Launch arguments that keep the window off the OS secret store. With HOME
 * moved, Electron's safe storage cannot find the user's login keychain: on
 * macOS each window raised a modal "A keychain cannot be found to store
 * 'Code Key'" dialog, and on Linux it would look for a keyring under the
 * fake home. VS Code's own SecretStorage goes to memory.
 */
export function secretStoreArgs(platform: NodeJS.Platform = process.platform): string[] {
  const args = ["--use-inmemory-secretstorage"];
  if (platform === "darwin") args.push("--use-mock-keychain");
  if (platform === "linux") args.push("--password-store=basic");
  return args;
}
