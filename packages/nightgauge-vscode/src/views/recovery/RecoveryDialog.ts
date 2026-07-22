/**
 * RecoveryDialog — WebView panel for pipeline error recovery.
 *
 * Mirrors the `ApprovalDialog` lifecycle (panel + onDidReceiveMessage +
 * `Promise<Result>`) but with a recovery-specific action set, in-dialog
 * confirmation flow for destructive actions, and chained re-render
 * support for follow-up failures.
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

/** Maximum chained re-render depth — prevents unbounded recovery loops. */
const MAX_CHAIN_DEPTH = 3;

export class RecoveryDialog implements vscode.Disposable {
  private panel: vscode.WebviewPanel | undefined;
  private disposables: vscode.Disposable[] = [];
  private resultPromise: {
    resolve: (result: RecoveryResult) => void;
    reject: (error: Error) => void;
  } | null = null;
  private chainDepth = 0;

  constructor(private readonly extensionUri: vscode.Uri) {}

  /**
   * Show the Recovery Dialog with the given payload.
   *
   * Returns a promise that resolves with the chosen action. Closing the
   * panel resolves with `cancel`. Subsequent calls on the same instance
   * re-render in the same panel and increment chain depth; once the
   * depth cap is reached the dialog auto-closes with `cancel`.
   */
  async show(payload: RecoveryRequiredPayload): Promise<RecoveryResult> {
    if (this.chainDepth >= MAX_CHAIN_DEPTH) {
      vscode.window.showWarningMessage(
        `Recovery aborted after ${MAX_CHAIN_DEPTH} chained failures — manual intervention required.`
      );
      this.dispose();
      return { action: "cancel" };
    }

    if (this.panel) {
      this.chainDepth += 1;
      this.panel.webview.html = getRecoveryDialogHtml(this.panel.webview, payload);
    } else {
      this.chainDepth = 1;
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
        (message: WebViewMessage) => this.handleMessage(message),
        undefined,
        this.disposables
      );
      this.panel.onDidDispose(() => this.handlePanelClosed(), undefined, this.disposables);
    }

    return new Promise<RecoveryResult>((resolve, reject) => {
      this.resultPromise = { resolve, reject };
    });
  }

  /**
   * Re-render with a follow-up payload (chained recovery). Returns the
   * promise for the next user choice.
   */
  rerender(payload: RecoveryRequiredPayload): Promise<RecoveryResult> {
    return this.show(payload);
  }

  private handleMessage(message: WebViewMessage): void {
    if (message.type !== "action" || !this.resultPromise) return;
    if (!message.confirmed) return;
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
  }

  /** Test-only: read the chain depth. */
  getChainDepth(): number {
    return this.chainDepth;
  }
}
