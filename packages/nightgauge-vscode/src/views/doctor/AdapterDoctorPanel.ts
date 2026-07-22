/**
 * AdapterDoctorPanel — singleton webview for the Adapter Doctor report (#4031).
 *
 * Mirrors the singleton lifecycle of TelemetrySettingsPanel. The command layer
 * builds an {@link AdapterDoctorReport} and a `refresh` callback that recomputes
 * it; the panel renders the report and re-runs the callback when the user clicks
 * "Re-run checks".
 */

import * as vscode from "vscode";
import { renderAdapterDoctorHtml, type AdapterDoctorReport } from "./AdapterDoctorHtml.js";
import { getNonce } from "../dashboard/DashboardComponents.js";

export type AdapterDoctorRefresh = () => Promise<AdapterDoctorReport>;

export class AdapterDoctorPanel implements vscode.Disposable {
  private static currentPanel: AdapterDoctorPanel | undefined;

  private panel: vscode.WebviewPanel | undefined;
  private readonly disposables: vscode.Disposable[] = [];
  private report: AdapterDoctorReport;
  private refresh: AdapterDoctorRefresh;
  private refreshing = false;

  private constructor(report: AdapterDoctorReport, refresh: AdapterDoctorRefresh) {
    this.report = report;
    this.refresh = refresh;
  }

  /**
   * Reveal the panel with a fresh report. A second call reuses the existing
   * webview (singleton) and updates its content + refresh callback.
   */
  static show(report: AdapterDoctorReport, refresh: AdapterDoctorRefresh): AdapterDoctorPanel {
    if (!AdapterDoctorPanel.currentPanel) {
      AdapterDoctorPanel.currentPanel = new AdapterDoctorPanel(report, refresh);
    } else {
      AdapterDoctorPanel.currentPanel.report = report;
      AdapterDoctorPanel.currentPanel.refresh = refresh;
    }
    AdapterDoctorPanel.currentPanel.reveal();
    return AdapterDoctorPanel.currentPanel;
  }

  static get current(): AdapterDoctorPanel | undefined {
    return AdapterDoctorPanel.currentPanel;
  }

  /** Render with a strict CSP + fresh script nonce bound to this webview. */
  private renderHtml(): string {
    const cspSource = this.panel?.webview.cspSource ?? "";
    return renderAdapterDoctorHtml(this.report, { cspSource, nonce: getNonce() });
  }

  private reveal(): void {
    if (this.panel) {
      this.panel.webview.html = this.renderHtml();
      this.panel.reveal(vscode.ViewColumn.One);
      return;
    }

    this.panel = vscode.window.createWebviewPanel(
      "incrediAdapterDoctor",
      "Nightgauge: Adapter Doctor",
      vscode.ViewColumn.One,
      { enableScripts: true, retainContextWhenHidden: true, localResourceRoots: [] }
    );

    this.panel.webview.html = this.renderHtml();
    this.panel.webview.onDidReceiveMessage(
      (msg) => this.handleMessage(msg),
      undefined,
      this.disposables
    );
    this.panel.onDidDispose(() => this.handlePanelClosed(), undefined, this.disposables);
  }

  private async handleMessage(msg: unknown): Promise<void> {
    if (!msg || typeof msg !== "object" || (msg as { type?: string }).type !== "refresh") {
      return;
    }
    if (this.refreshing) return;
    this.refreshing = true;
    try {
      this.report = await this.refresh();
      if (this.panel) {
        this.panel.webview.html = this.renderHtml();
      }
    } catch (err) {
      void vscode.window.showErrorMessage(
        `Adapter Doctor refresh failed: ${err instanceof Error ? err.message : String(err)}`
      );
    } finally {
      this.refreshing = false;
    }
  }

  private handlePanelClosed(): void {
    this.panel = undefined;
    AdapterDoctorPanel.currentPanel = undefined;
    while (this.disposables.length) {
      this.disposables.pop()?.dispose();
    }
  }

  dispose(): void {
    this.handlePanelClosed();
    this.panel?.dispose();
  }
}
