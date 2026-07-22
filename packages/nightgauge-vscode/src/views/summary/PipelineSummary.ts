/**
 * PipelineSummary - WebView panel manager for the pipeline completion summary
 *
 * Displays comprehensive metrics after pr-merge completes successfully.
 * Provides export functionality and "Reset & Start New" action.
 *
 * @see docs/ARCHITECTURE.md for WebView patterns
 */

import * as vscode from "vscode";
import type { PipelineState } from "../../services/PipelineStateService";
import { getPipelineSummaryHtml } from "./PipelineSummaryHtml";

/**
 * Message from WebView to extension
 */
type WebViewMessage = { type: "export"; data: string } | { type: "reset" } | { type: "close" };

/**
 * PipelineSummary class manages the WebView panel for pipeline completion
 *
 * @example
 * ```typescript
 * const summary = new PipelineSummary(context.extensionUri);
 *
 * // Show the summary after pr-merge completes
 * const state = await pipelineStateService.getState();
 * if (state) {
 *   await summary.show(state);
 * }
 * ```
 */
export class PipelineSummary implements vscode.Disposable {
  private panel: vscode.WebviewPanel | undefined;
  private disposables: vscode.Disposable[] = [];
  private state: PipelineState | null = null;

  constructor(private readonly extensionUri: vscode.Uri) {}

  /**
   * Show the summary WebView panel
   *
   * If a panel already exists, it will be revealed and updated.
   * Otherwise, a new panel is created.
   */
  async show(state: PipelineState): Promise<void> {
    this.state = state;

    // If we already have a panel, reveal and update it
    if (this.panel) {
      this.panel.reveal(vscode.ViewColumn.One, true); // preserveFocus=true
      this.updatePanel();
      return;
    }

    // Create the WebView panel
    this.panel = vscode.window.createWebviewPanel(
      "incrediPipelineSummary",
      `Pipeline Complete - Issue #${state.issue_number}`,
      { viewColumn: vscode.ViewColumn.One, preserveFocus: true },
      {
        enableScripts: true,
        retainContextWhenHidden: false,
        localResourceRoots: [vscode.Uri.joinPath(this.extensionUri, "src", "views", "summary")],
      }
    );

    // Set initial content
    this.updatePanel();

    // Handle messages from the WebView
    this.panel.webview.onDidReceiveMessage(
      (message: WebViewMessage) => this.handleMessage(message),
      undefined,
      this.disposables
    );

    // Handle panel disposal
    this.panel.onDidDispose(() => this.handlePanelClosed(), undefined, this.disposables);
  }

  /**
   * Update the panel content
   */
  private updatePanel(): void {
    if (!this.panel || !this.state) return;

    this.panel.webview.html = getPipelineSummaryHtml(this.panel.webview, this.state);
  }

  /**
   * Handle messages from the WebView
   */
  private handleMessage(message: WebViewMessage): void {
    switch (message.type) {
      case "export":
        this.handleExport(message.data);
        break;

      case "reset":
        this.handleReset();
        break;

      case "close":
        this.handleClose();
        break;
    }
  }

  /**
   * Handle export request - save summary as JSON
   */
  private async handleExport(data: string): Promise<void> {
    if (!this.state) {
      vscode.window.showWarningMessage("No pipeline data to export.");
      return;
    }

    const filename = `pipeline-summary-${this.state.issue_number}.json`;

    const uri = await vscode.window.showSaveDialog({
      defaultUri: vscode.Uri.file(filename),
      filters: {
        JSON: ["json"],
      },
    });

    if (uri) {
      // Parse and re-stringify to ensure valid JSON
      const parsed = JSON.parse(data);
      const content = JSON.stringify(parsed, null, 2);
      await vscode.workspace.fs.writeFile(uri, Buffer.from(content, "utf-8"));
      vscode.window.showInformationMessage(`Summary exported to ${uri.fsPath}`);
    }
  }

  /**
   * Handle reset request - execute resetPipeline command and close panel
   */
  private async handleReset(): Promise<void> {
    // Execute the reset command with skipConfirm since user already confirmed in modal
    await vscode.commands.executeCommand("nightgauge.resetPipeline", {
      skipConfirm: true,
    });

    // Close the panel
    this.dispose();
  }

  /**
   * Handle close without reset
   */
  private handleClose(): void {
    // Show info message that files are retained
    vscode.window.showInformationMessage(
      'Pipeline files retained. Run "Nightgauge: Reset Pipeline" when ready to start a new issue.'
    );
  }

  /**
   * Handle panel closed by user (X button)
   */
  private handlePanelClosed(): void {
    // Show toast message about retained files
    vscode.window.showInformationMessage(
      "Pipeline summary closed. Files retained until manual reset."
    );

    this.panel = undefined;
    // Dispose subscriptions but keep the instance for potential re-show
    while (this.disposables.length) {
      const disposable = this.disposables.pop();
      if (disposable) {
        disposable.dispose();
      }
    }
  }

  /**
   * Check if the panel is currently visible
   */
  isVisible(): boolean {
    return this.panel?.visible ?? false;
  }

  /**
   * Dispose of the summary panel and clean up resources
   */
  dispose(): void {
    // Dispose of the panel
    if (this.panel) {
      this.panel.dispose();
      this.panel = undefined;
    }

    // Dispose of all disposables
    while (this.disposables.length) {
      const disposable = this.disposables.pop();
      if (disposable) {
        disposable.dispose();
      }
    }

    this.state = null;
  }
}
