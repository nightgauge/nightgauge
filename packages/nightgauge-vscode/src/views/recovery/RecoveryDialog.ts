/**
 * RecoveryDialog — WebView panel for pipeline error recovery.
 *
 * Mirrors the `ApprovalDialog` lifecycle (panel + onDidReceiveMessage +
 * `Promise<Result>`) but with a recovery-specific action set, in-dialog
 * strict action validation and repeatable observational actions.
 *
 * Action computation lives in `HeadlessOrchestrator.computeRecoveryRequired`
 * — this class is a thin renderer.
 *
 * @see Issue #3239
 * @see ADR-002 in .nightgauge/knowledge/features/3239-pipeline-error-ux-surface-recovery-actions-when-pi/decisions.md
 */

import * as vscode from "vscode";
import type { RecoveryAction, RecoveryRequiredPayload } from "@nightgauge/sdk";
import { getRecoveryDialogHtml } from "./RecoveryDialogHtml";

export interface RecoveryResult {
  action: RecoveryAction;
}

interface WebViewMessage {
  type: "action";
  action: RecoveryAction;
  confirmed: boolean;
}

const RECOVERY_ACTIONS: ReadonlySet<RecoveryAction> = new Set([
  "open-run-state-directory",
  "cancel",
]);

export class RecoveryDialog implements vscode.Disposable {
  private panel: vscode.WebviewPanel | undefined;
  private disposables: vscode.Disposable[] = [];
  private resultPromise: {
    resolve: (result: RecoveryResult) => void;
  } | null = null;
  private currentPayload: RecoveryRequiredPayload | null = null;

  constructor(private readonly extensionUri: vscode.Uri) {}

  /**
   * Show the Recovery Dialog with the given payload.
   *
   * Returns a promise that resolves with the chosen action. Closing the
   * panel resolves with `cancel`. Subsequent calls on the same live instance
   * update the panel contents.
   */
  async show(payload: RecoveryRequiredPayload): Promise<RecoveryResult> {
    this.currentPayload = payload;
    if (this.panel) {
      this.panel.webview.html = getRecoveryDialogHtml(this.panel.webview, payload);
    } else {
      this.panel = vscode.window.createWebviewPanel(
        "incrediRecoveryDialog",
        `Recovery Required #${payload.issueNumber}`,
        vscode.ViewColumn.One,
        {
          enableScripts: true,
          retainContextWhenHidden: true,
          localResourceRoots: [vscode.Uri.joinPath(this.extensionUri, "src", "views", "recovery")],
        }
      );
      this.panel.webview.html = getRecoveryDialogHtml(this.panel.webview, payload);

      this.panel.webview.onDidReceiveMessage(
        (message: unknown) => void this.handleMessage(message),
        undefined,
        this.disposables
      );
      this.panel.onDidDispose(() => this.handlePanelClosed(), undefined, this.disposables);
    }

    return this.waitForNextAction();
  }

  /** Re-enable the panel and wait for another observational action. */
  waitForNextAction(): Promise<RecoveryResult> {
    if (!this.panel) {
      return Promise.resolve({ action: "cancel" });
    }
    void this.panel.webview.postMessage({ type: "recoveryActionComplete" });
    return new Promise<RecoveryResult>((resolve) => {
      this.resultPromise = { resolve };
    });
  }

  private handleMessage(message: unknown): void {
    if (!isWebViewMessage(message) || !this.resultPromise || !this.currentPayload) return;
    if (!this.currentPayload.availableActions.includes(message.action)) return;
    const pending = this.resultPromise;
    this.resultPromise = null;
    pending.resolve({ action: message.action });
  }

  private handlePanelClosed(): void {
    if (this.resultPromise) {
      this.resultPromise.resolve({ action: "cancel" });
      this.resultPromise = null;
    }
    this.panel = undefined;
    this.currentPayload = null;
  }

  dispose(): void {
    if (this.panel) {
      this.panel.dispose();
      this.panel = undefined;
    }
    while (this.disposables.length) {
      const d = this.disposables.pop();
      if (d) d.dispose();
    }
    this.currentPayload = null;
  }
}

function isWebViewMessage(message: unknown): message is WebViewMessage {
  if (!message || typeof message !== "object") return false;
  const value = message as Record<string, unknown>;
  if (Object.keys(value).sort().join(",") !== "action,confirmed,type") return false;
  return (
    value.type === "action" &&
    typeof value.action === "string" &&
    RECOVERY_ACTIONS.has(value.action as RecoveryAction) &&
    value.confirmed === true
  );
}
