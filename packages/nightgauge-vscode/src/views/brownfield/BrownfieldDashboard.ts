/**
 * BrownfieldDashboard - WebView panel manager for brownfield modernization dashboard
 *
 * Singleton webview panel that displays brownfield assessment data.
 * Subscribes to BrownfieldDataService.onDataChanged for auto-refresh.
 * Follows the Dashboard.ts pattern for panel lifecycle and debouncing.
 *
 * @see Issue #1163 - Brownfield Modernization Progress Dashboard
 */

import * as vscode from "vscode";
import { BrownfieldDataService } from "../../services/BrownfieldDataService";
import { BrownfieldDashboardState } from "./BrownfieldDashboardState";
import { getBrownfieldDashboardHtml } from "./BrownfieldDashboardHtml";
import { Logger } from "../../utils/logger";

/** Message from webview to extension */
type BrownfieldMessage = { type: "refresh" };

/**
 * BrownfieldDashboard manages the webview panel for brownfield metrics.
 *
 * @example
 * ```typescript
 * const dashboard = new BrownfieldDashboard(extensionUri, dataService);
 * dashboard.show();
 * ```
 */
export class BrownfieldDashboard implements vscode.Disposable {
  private panel: vscode.WebviewPanel | undefined;
  private disposables: vscode.Disposable[] = [];
  private state: BrownfieldDashboardState;
  private updateTimer: ReturnType<typeof setTimeout> | undefined;
  private logger = new Logger("Nightgauge Brownfield Dashboard");

  private static readonly DEBOUNCE_MS = 150;

  constructor(
    private readonly extensionUri: vscode.Uri,
    private readonly dataService: BrownfieldDataService
  ) {
    this.state = new BrownfieldDashboardState(dataService);

    // Subscribe to data changes for auto-refresh
    const dataChangedDisposable = this.dataService.onDataChanged(() => {
      this.scheduleUpdate("onDataChanged");
    });
    this.disposables.push(dataChangedDisposable);
  }

  /**
   * Show the brownfield dashboard panel.
   * Reveals existing panel or creates a new one.
   */
  show(): void {
    if (this.panel) {
      this.panel.reveal(vscode.ViewColumn.One);
      this.scheduleUpdate("show:reveal");
      return;
    }

    this.panel = vscode.window.createWebviewPanel(
      "incrediBrownfieldDashboard",
      "Brownfield Modernization Dashboard",
      vscode.ViewColumn.One,
      {
        enableScripts: true,
        retainContextWhenHidden: true,
        localResourceRoots: [vscode.Uri.joinPath(this.extensionUri, "src", "views", "brownfield")],
      }
    );

    // Initial render
    this.scheduleUpdate("show:initial");

    // Handle messages from webview
    this.panel.webview.onDidReceiveMessage(
      (message: BrownfieldMessage) => this.handleMessage(message),
      undefined,
      this.disposables
    );

    // Handle panel disposal
    this.panel.onDidDispose(() => this.handlePanelClosed(), undefined, this.disposables);
  }

  /**
   * Schedule a debounced panel update
   */
  private scheduleUpdate(trigger: string): void {
    if (!this.panel) return;

    if (this.updateTimer) {
      clearTimeout(this.updateTimer);
    }

    this.updateTimer = setTimeout(() => this.renderPanel(trigger), BrownfieldDashboard.DEBOUNCE_MS);
  }

  /**
   * Render the panel with current data
   */
  private async renderPanel(trigger: string): Promise<void> {
    if (!this.panel) return;

    this.logger.debug("renderPanel:start", { trigger });

    try {
      const data = await this.state.loadData();
      this.panel.webview.html = getBrownfieldDashboardHtml(this.panel.webview, data, this.state);

      this.logger.debug("renderPanel:complete", {
        trigger,
        hasAnyData: data.hasAnyData,
      });
    } catch (err) {
      this.logger.debug("renderPanel:error", { trigger, error: String(err) });
    }
  }

  /**
   * Handle messages from webview
   */
  private async handleMessage(message: BrownfieldMessage): Promise<void> {
    switch (message.type) {
      case "refresh":
        await this.renderPanel("msg:refresh");
        break;
    }
  }

  /**
   * Handle panel closed by user
   */
  private handlePanelClosed(): void {
    this.panel = undefined;
  }

  /**
   * Check if panel is visible
   */
  isVisible(): boolean {
    return this.panel?.visible ?? false;
  }

  /**
   * Dispose of all resources
   */
  dispose(): void {
    if (this.updateTimer) {
      clearTimeout(this.updateTimer);
      this.updateTimer = undefined;
    }

    if (this.panel) {
      this.panel.dispose();
      this.panel = undefined;
    }

    while (this.disposables.length) {
      this.disposables.pop()?.dispose();
    }

    this.logger.dispose();
  }
}
