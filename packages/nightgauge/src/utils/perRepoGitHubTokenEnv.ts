/**
 * Per-repo GitHub token injection for integrated terminals AND the extension host.
 *
 * gh's keyring "active account" is a single machine-global value, so it cannot
 * represent concurrent sessions/workspaces owned by *different* GitHub users on
 * one machine. `GH_TOKEN` is per-process and outranks both `GITHUB_TOKEN` and
 * the gh keyring (GH_TOKEN > GITHUB_TOKEN > active account), so setting it
 * per-window — one VSCode window = one workspace = one repo — makes every
 * `gh` call authenticate as that repo's configured user, and lets concurrent
 * windows for different users coexist.
 *
 * The per-repo token is mirrored into two scopes so EVERY `gh` invocation a
 * window can make is covered, with no machine-global active-account fallback:
 *   1. the terminal `EnvironmentVariableCollection` — integrated-terminal and
 *      task `gh` calls (ad-hoc and skill-driven);
 *   2. the extension host's own `process.env` — direct `gh`/`gh api` calls made
 *      from extension code (board field writes, dashboard, commands), which
 *      inherit `process.env` rather than the terminal collection.
 *
 * The other `gh` surfaces are already covered by self-injection: spawned
 * pipeline subprocesses (skillRunner resolveTokenForSubprocess / perRepoTokenEnv),
 * the Go binary (PersistentPreRunE exports GH_TOKEN/GITHUB_TOKEN), shell hooks
 * (hooks/lib/guard.sh) and skill bodies (_shared/PREFLIGHT.md).
 *
 * @see Issue #2487, #2670 — config-based per-repo token resolution
 */

import * as vscode from "vscode";
import { resolveTokenForSubprocess } from "./skillRunner";

const COLLECTION_DESCRIPTION =
  "Nightgauge: per-repo GitHub token — gh CLI authenticates as this repo's configured user";

/**
 * Resolve the active workspace's GitHub token and mirror it into both the
 * terminal `EnvironmentVariableCollection` and the extension host's own
 * `process.env` as GH_TOKEN + GITHUB_TOKEN. Re-runs whenever the workspace
 * folders or the nightgauge config files change.
 *
 * Fail-safe: never throws — token wiring must not break activation or terminal
 * creation. When no per-repo token resolves, the terminal variables are cleared
 * and the extension-host `process.env` is restored to its ambient (pre-injection)
 * values, so the user's ambient `gh auth` / exported token is never shadowed.
 *
 * @param context - The extension context (provides the terminal env collection)
 */
export function applyPerRepoGitHubTokenEnv(context: vscode.ExtensionContext): void {
  const collection = context.environmentVariableCollection;

  // Capture the ambient (pre-injection) extension-host token values once so we
  // can restore them verbatim when no per-repo token resolves — never clobbering
  // a value the user exported into VSCode's own launch environment.
  const ambient: Record<"GH_TOKEN" | "GITHUB_TOKEN", string | undefined> = {
    GH_TOKEN: process.env.GH_TOKEN,
    GITHUB_TOKEN: process.env.GITHUB_TOKEN,
  };
  const setProcessEnv = (name: "GH_TOKEN" | "GITHUB_TOKEN", value: string | undefined): void => {
    if (value === undefined) {
      delete process.env[name];
    } else {
      process.env[name] = value;
    }
  };

  const refresh = (): void => {
    try {
      const root = vscode.workspace.workspaceFolders?.[0]?.uri.fsPath;
      const result = root ? resolveTokenForSubprocess(root) : null;
      if (result?.token) {
        collection.description = COLLECTION_DESCRIPTION;
        // Set both: GH_TOKEN for the gh CLI, GITHUB_TOKEN for direct-API
        // consumers. GH_TOKEN outranks the gh keyring's active account.
        collection.replace("GH_TOKEN", result.token);
        collection.replace("GITHUB_TOKEN", result.token);
        // Also set the extension host's own process env so direct `gh`/`gh api`
        // calls from extension code (board writes, dashboard, commands) inherit
        // the per-repo token — not just integrated terminals.
        setProcessEnv("GH_TOKEN", result.token);
        setProcessEnv("GITHUB_TOKEN", result.token);
      } else {
        // No per-repo token — don't shadow the user's ambient gh auth.
        collection.delete("GH_TOKEN");
        collection.delete("GITHUB_TOKEN");
        // Restore the extension-host env to its ambient values verbatim.
        setProcessEnv("GH_TOKEN", ambient.GH_TOKEN);
        setProcessEnv("GITHUB_TOKEN", ambient.GITHUB_TOKEN);
      }
    } catch {
      // Token wiring is best-effort; never surface as an activation failure.
    }
  };

  refresh();

  // Re-resolve when the set of workspace folders changes (open/close folder).
  context.subscriptions.push(vscode.workspace.onDidChangeWorkspaceFolders(() => refresh()));

  // Re-resolve when the per-repo config (project or local tier) changes, so a
  // newly-added github_auth.token takes effect without reloading the window.
  const watcher = vscode.workspace.createFileSystemWatcher("**/.nightgauge/config*.yaml");
  watcher.onDidCreate(() => refresh());
  watcher.onDidChange(() => refresh());
  watcher.onDidDelete(() => refresh());
  context.subscriptions.push(watcher);
}
