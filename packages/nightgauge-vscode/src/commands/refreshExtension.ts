/**
 * refreshExtensionFromMain — runs `git pull` + `dev-install.sh` in the
 * workspace and prompts the user to reload the window. Triggered by the
 * status-bar staleness UI (#3300).
 *
 * The command intentionally does NOT auto-reload — the user may have
 * unsaved work in other files. It opens an integrated terminal, runs the
 * commands, and then offers the reload prompt.
 */

import * as vscode from "vscode";
import * as path from "node:path";

const COMMAND_ID = "nightgauge.refreshExtensionFromMain";
const TERMINAL_NAME = "Nightgauge: Refresh Extension";

export function registerRefreshExtensionCommand(workspaceRoot: string): vscode.Disposable {
  return vscode.commands.registerCommand(COMMAND_ID, async () => {
    const devInstall = path.join(
      workspaceRoot,
      "packages",
      "nightgauge-vscode",
      "scripts",
      "dev-install.sh"
    );
    const cmd = `git pull && bash "${devInstall}"`;

    // Reuse an existing terminal of the same name if one is open; else create.
    const existing = vscode.window.terminals.find((t) => t.name === TERMINAL_NAME);
    const terminal =
      existing ??
      vscode.window.createTerminal({
        name: TERMINAL_NAME,
        cwd: workspaceRoot,
      });
    terminal.show(true);
    terminal.sendText(cmd, true);

    // Don't auto-reload — wait for user. Offer the prompt after a short delay
    // so the terminal can start emitting output. The user controls when to
    // reload.
    setTimeout(() => {
      void promptReload();
    }, 2_000);
  });
}

async function promptReload(): Promise<void> {
  const reload = "Reload Window";
  const choice = await vscode.window.showInformationMessage(
    "Extension refresh is running in the terminal. After 'dev-install.sh' completes, reload the window to pick up the new build.",
    reload,
    "Later"
  );
  if (choice === reload) {
    await vscode.commands.executeCommand("workbench.action.reloadWindow");
  }
}
