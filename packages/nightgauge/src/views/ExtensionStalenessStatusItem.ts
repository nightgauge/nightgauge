/**
 * ExtensionStalenessStatusItem — surfaces ExtensionStalenessService state in
 * the VSCode status bar.
 *
 *   - fresh   → hidden (no clutter when up-to-date)
 *   - stale on critical paths → red error background:
 *       `$(error) Extension stale (N commits behind, M critical files)`
 *     Click → modal with details + "Refresh extension" button that runs
 *     the `nightgauge.refreshExtensionFromMain` command.
 *   - stale on non-critical paths only → yellow warning:
 *       `$(warning) Extension N commits behind`
 *     Click → modal with the same options. Dispatch is NOT refused for
 *     non-critical staleness.
 *   - unknown (no build-info.json) → muted:
 *       `$(question) Extension version unknown`
 *
 * @see Issue #3300 — Extension staleness detection.
 */

import * as vscode from "vscode";
import type {
  ExtensionStalenessService,
  StalenessState,
} from "../services/ExtensionStalenessService";

const REFRESH_COMMAND = "nightgauge.refreshExtensionFromMain";
const SHOW_DETAILS_COMMAND = "nightgauge.showStalenessDetails";

export class ExtensionStalenessStatusItem implements vscode.Disposable {
  readonly item: vscode.StatusBarItem;
  private readonly disposables: vscode.Disposable[] = [];

  constructor(private readonly service: ExtensionStalenessService) {
    this.item = vscode.window.createStatusBarItem(
      vscode.StatusBarAlignment.Left,
      95 // Just to the right of PipelineConnectivityStatusItem (96)
    );
    this.item.command = SHOW_DETAILS_COMMAND;

    this.disposables.push(
      this.service.onChanged((state) => this.render(state)),
      vscode.commands.registerCommand(SHOW_DETAILS_COMMAND, () => this.showDetails())
    );
    this.render(this.service.getState());
  }

  private render(state: StalenessState): void {
    // `fresh` and `unknown` both hide the item. `unknown` is never actionable —
    // it means freshness can't be determined (no build-info.json, workspace
    // isn't a git repo, or the build SHA isn't in this workspace's history
    // because the extension is installed in a different repo). The last case is
    // the normal situation for every real customer, so surfacing a developer-
    // oriented status item there leaked internal dev tooling into the customer
    // UI. The only signal worth showing is `stale`, which only occurs when the
    // workspace IS the extension's source repo (i.e. we're dogfooding).
    if (state.kind === "fresh" || state.kind === "unknown") {
      this.item.hide();
      return;
    }
    // stale
    const isCritical = state.criticalPathsChanged.length > 0;
    if (isCritical) {
      this.item.text = `$(warning) Extension stale (${state.commitsBehind} commits, ${state.criticalPathsChanged.length} critical)`;
      this.item.tooltip =
        `Extension is ${state.commitsBehind} commits behind workspace HEAD with changes on ${state.criticalPathsChanged.length} critical pipeline files.\n` +
        `Run dev-install.sh and reload window to update.\n\n` +
        `Click for details and refresh action.`;
      this.item.backgroundColor = new vscode.ThemeColor("statusBarItem.errorBackground");
    } else {
      this.item.text = `$(warning) Extension ${state.commitsBehind} commits behind`;
      this.item.tooltip =
        `Extension is ${state.commitsBehind} commits behind workspace HEAD on non-critical files (docs, tests, configs).\n` +
        `Dispatch is allowed. Refresh at your convenience.\n\nClick for details.`;
      this.item.backgroundColor = new vscode.ThemeColor("statusBarItem.warningBackground");
    }
    this.item.show();
  }

  private async showDetails(): Promise<void> {
    const state = this.service.getState();
    if (state.kind === "fresh") {
      void vscode.window.showInformationMessage("Extension is up to date with workspace HEAD.");
      return;
    }
    if (state.kind === "unknown") {
      void vscode.window.showInformationMessage(
        `Extension build provenance unavailable: ${state.reason}.\n\nDispatch is allowed; build freshness cannot be verified.`
      );
      return;
    }
    const isCritical = state.criticalPathsChanged.length > 0;
    const lines: string[] = [
      `Extension is ${state.commitsBehind} commits behind workspace HEAD.`,
      ``,
      `Build SHA:    ${state.buildSha.slice(0, 12)}`,
      `Current HEAD: ${state.currentSha.slice(0, 12)}`,
      ``,
    ];
    if (state.criticalPathsChanged.length > 0) {
      lines.push(`Critical pipeline files changed (consider refreshing the extension):`);
      for (const f of state.criticalPathsChanged.slice(0, 15)) {
        lines.push(`  · ${f}`);
      }
      if (state.criticalPathsChanged.length > 15) {
        lines.push(`  · …and ${state.criticalPathsChanged.length - 15} more`);
      }
      lines.push("");
    }
    if (state.otherPathsChanged.length > 0) {
      lines.push(`Other files changed: ${state.otherPathsChanged.length}`);
    }
    const message = lines.join("\n");
    const refreshAction = "Refresh extension";
    const choice = await vscode.window.showWarningMessage(message, { modal: true }, refreshAction);
    if (choice === refreshAction) {
      void vscode.commands.executeCommand(REFRESH_COMMAND);
    }
    // Suppress unused-var lint when isCritical not used in message construction.
    void isCritical;
  }

  dispose(): void {
    for (const d of this.disposables) d.dispose();
    this.item.dispose();
  }
}
