/**
 * GettingStartedPanel — singleton webview for first-run onboarding (#4155).
 *
 * Mirrors the singleton lifecycle of AdapterDoctorPanel: a static `show()`
 * reveals (or creates) the panel, messages from the webview are dispatched
 * to an injected callback, and the panel resets its own singleton reference
 * on disposal so a later `show()` builds a fresh one.
 *
 * The panel itself has no opinion on *when* it should appear — see
 * `onboardingGate.ts` for the pure activation-condition logic and
 * `../../commands/quickstart.ts` for the wiring that decides to call
 * `show()` on extension activation vs. only in response to the
 * `nightgauge.showGettingStarted` command.
 */

import * as vscode from "vscode";
import { renderGettingStartedHtml } from "./GettingStartedHtml.js";
import { getNonce } from "../dashboard/DashboardComponents.js";

/** The onboarding steps a user can trigger from the webview. */
export type GettingStartedAction = "init" | "pickup" | "docs";

export class GettingStartedPanel implements vscode.Disposable {
  private static currentPanel: GettingStartedPanel | undefined;

  private panel: vscode.WebviewPanel | undefined;
  private readonly disposables: vscode.Disposable[] = [];
  private onAction: (action: GettingStartedAction) => void;

  private constructor(onAction: (action: GettingStartedAction) => void) {
    this.onAction = onAction;
  }

  /**
   * Reveal the panel, creating it on first call. A second call reuses the
   * existing webview (singleton) and swaps in the latest action callback.
   */
  static show(onAction: (action: GettingStartedAction) => void): GettingStartedPanel {
    if (!GettingStartedPanel.currentPanel) {
      GettingStartedPanel.currentPanel = new GettingStartedPanel(onAction);
    } else {
      GettingStartedPanel.currentPanel.onAction = onAction;
    }
    GettingStartedPanel.currentPanel.reveal();
    return GettingStartedPanel.currentPanel;
  }

  static get current(): GettingStartedPanel | undefined {
    return GettingStartedPanel.currentPanel;
  }

  /** Render with a strict CSP + fresh script nonce bound to this webview. */
  private renderHtml(): string {
    const cspSource = this.panel?.webview.cspSource ?? "";
    return renderGettingStartedHtml({ cspSource, nonce: getNonce() });
  }

  private reveal(): void {
    if (this.panel) {
      this.panel.webview.html = this.renderHtml();
      this.panel.reveal(vscode.ViewColumn.One);
      return;
    }

    this.panel = vscode.window.createWebviewPanel(
      "incrediGettingStarted",
      "Nightgauge: Getting Started",
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

  private handleMessage(msg: unknown): void {
    if (!msg || typeof msg !== "object") return;
    const { type, action } = msg as { type?: string; action?: string };
    if (type !== "action") return;
    if (action === "init" || action === "pickup" || action === "docs") {
      this.onAction(action);
    }
  }

  private handlePanelClosed(): void {
    this.panel = undefined;
    GettingStartedPanel.currentPanel = undefined;
    while (this.disposables.length) {
      this.disposables.pop()?.dispose();
    }
  }

  dispose(): void {
    this.handlePanelClosed();
    this.panel?.dispose();
  }
}
