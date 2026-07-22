/**
 * ApprovalDialog - WebView panel for plan approval
 *
 * Displays PLAN.md content with markdown rendering and syntax highlighting,
 * providing Approve, Edit, Skip, and Cancel actions for pipeline stage gates.
 *
 * @see docs/ARCHITECTURE.md for WebView patterns
 * @see Issue #433 - config.yaml (formerly nightgauge.yaml)
 */

import * as vscode from "vscode";
import * as fs from "fs";
import * as path from "path";
import { type PipelineStage } from "@nightgauge/sdk";
import { getApprovalDialogHtml } from "./ApprovalDialogHtml";
import { resolveConfigPathSync, logDeprecationWarning } from "../../utils/configPathResolver";
import { readEffectiveConfigTextSync } from "../../utils/mergedConfigReader";

/**
 * Available approval actions
 */
export type ApprovalAction = "approve" | "edit" | "skip" | "cancel";

/**
 * Result returned from the approval dialog
 */
export interface ApprovalResult {
  action: ApprovalAction;
}

/**
 * Message from WebView to extension
 */
interface WebViewMessage {
  type: "action";
  action: ApprovalAction;
}

/**
 * Check if stage should be auto-accepted based on nightgauge config
 */
function shouldAutoAcceptStage(stage: PipelineStage): boolean {
  // Check environment variable override first
  if (process.env.NIGHTGAUGE_AUTO_ACCEPT_STAGES === "true") {
    return true;
  }

  const workspaceRoot = vscode.workspace.workspaceFolders?.[0]?.uri.fsPath;
  if (!workspaceRoot) {
    return false;
  }

  try {
    // Resolve config path with fallback to legacy
    const pathResult = resolveConfigPathSync(workspaceRoot);
    if (!pathResult.exists) {
      return false; // No config file, no auto-accept
    }

    // Log deprecation warning if using legacy path
    if (pathResult.isLegacy) {
      logDeprecationWarning(pathResult.path);
    }

    // Read and parse config file
    const configContent = readEffectiveConfigTextSync(pathResult);
    const lines = configContent.split("\n");
    let inHumanInTheLoop = false;
    let autoAcceptStages = false;
    const trustedStages: string[] = [];

    for (const line of lines) {
      const trimmed = line.trim();

      if (trimmed.startsWith("human_in_the_loop:")) {
        inHumanInTheLoop = true;
        continue;
      }

      if (inHumanInTheLoop) {
        // Check for next top-level key
        if (
          trimmed &&
          !trimmed.startsWith("#") &&
          /^[a-z_]+:/.test(trimmed) &&
          !trimmed.startsWith("  ")
        ) {
          inHumanInTheLoop = false;
          continue;
        }

        if (trimmed.includes("auto_accept_stages:")) {
          autoAcceptStages = trimmed.includes("true");
        } else if (trimmed.startsWith("- ") && trustedStages.length > 0) {
          // Part of trusted_stages array
          const stageName = trimmed.substring(2).trim();
          trustedStages.push(stageName);
        } else if (trimmed.includes("trusted_stages:")) {
          // Start of array
          const afterColon = trimmed.split("trusted_stages:")[1];
          if (afterColon && afterColon.trim().startsWith("[")) {
            // Inline array
            const match = afterColon.match(/\[(.*)\]/);
            if (match) {
              const items = match[1].split(",").map((s) => s.trim().replace(/['"]/g, ""));
              trustedStages.push(...items);
            }
          } else {
            // Multiline array
            trustedStages.push("__marker__");
          }
        }
      }
    }

    // Remove marker if present
    const markerIndex = trustedStages.indexOf("__marker__");
    if (markerIndex >= 0) {
      trustedStages.splice(markerIndex, 1);
    }

    // Return true if global auto_accept_stages or stage is in trusted list
    return autoAcceptStages || trustedStages.includes(stage);
  } catch (error) {
    // Fail-safe: on error, don't auto-accept
    console.error("Failed to check auto-accept config:", error);
    return false;
  }
}

/**
 * ApprovalDialog class manages the WebView panel for plan approval
 *
 * @example
 * ```typescript
 * const dialog = new ApprovalDialog(context.extensionUri);
 * const result = await dialog.show('feature-planning', 42, planContent);
 *
 * if (result.action === 'approve') {
 *   orchestrator.approve();
 * }
 * ```
 */
export class ApprovalDialog implements vscode.Disposable {
  private panel: vscode.WebviewPanel | undefined;
  private disposables: vscode.Disposable[] = [];
  private resultPromise: {
    resolve: (result: ApprovalResult) => void;
    reject: (error: Error) => void;
  } | null = null;

  constructor(private readonly extensionUri: vscode.Uri) {}

  /**
   * Show the approval dialog with plan content
   *
   * Checks auto-accept configuration before showing the dialog.
   * If auto_accept_stages is enabled or the stage is in trusted_stages,
   * returns immediate approval without showing UI.
   *
   * @param stage - The pipeline stage requesting approval
   * @param issueNumber - The issue number being worked on
   * @param planContent - The markdown content of the plan file
   * @returns Promise resolving to the user's action
   */
  async show(
    stage: PipelineStage,
    issueNumber: number,
    planContent: string
  ): Promise<ApprovalResult> {
    // Check if this stage should be auto-accepted
    if (shouldAutoAcceptStage(stage)) {
      console.log(`[ApprovalDialog] Auto-accepting stage: ${stage} (issue #${issueNumber})`);

      // Show a brief notification to user
      vscode.window.showInformationMessage(`✓ Auto-approved: ${stage} (issue #${issueNumber})`);

      // Return immediate approval (skip dialog)
      return { action: "approve" };
    }

    // Create the WebView panel
    this.panel = vscode.window.createWebviewPanel(
      "incrediApprovalDialog",
      `Review Plan #${issueNumber}`,
      vscode.ViewColumn.One,
      {
        enableScripts: true,
        retainContextWhenHidden: true,
        localResourceRoots: [vscode.Uri.joinPath(this.extensionUri, "src", "views", "approval")],
      }
    );

    // Set the HTML content
    this.panel.webview.html = getApprovalDialogHtml(
      this.panel.webview,
      this.extensionUri,
      stage,
      issueNumber,
      planContent
    );

    // Handle messages from the WebView
    this.panel.webview.onDidReceiveMessage(
      (message: WebViewMessage) => this.handleMessage(message),
      undefined,
      this.disposables
    );

    // Handle panel disposal (user closed the panel)
    this.panel.onDidDispose(() => this.handlePanelClosed(), undefined, this.disposables);

    // Return a promise that resolves when the user takes an action
    return new Promise<ApprovalResult>((resolve, reject) => {
      this.resultPromise = { resolve, reject };
    });
  }

  /**
   * Handle messages from the WebView
   */
  private handleMessage(message: WebViewMessage): void {
    if (message.type === "action" && this.resultPromise) {
      const result: ApprovalResult = { action: message.action };
      this.resultPromise.resolve(result);
      this.resultPromise = null;
      this.dispose();
    }
  }

  /**
   * Handle panel closed by user (X button)
   */
  private handlePanelClosed(): void {
    if (this.resultPromise) {
      // Treat closing the panel as cancel
      this.resultPromise.resolve({ action: "cancel" });
      this.resultPromise = null;
    }
  }

  /**
   * Update the plan content (for live refresh)
   */
  updateContent(planContent: string): void {
    if (this.panel) {
      this.panel.webview.postMessage({
        type: "update",
        content: planContent,
      });
    }
  }

  /**
   * Dispose of the dialog and clean up resources
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
  }
}
