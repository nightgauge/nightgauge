/**
 * Scaffold Knowledge for Issue command
 *
 * Creates a full knowledge scaffold (PRD.md + decisions.md) for an issue
 * via KnowledgeService.scaffoldForIssue(). Always creates the directory
 * even if auto_scaffold is disabled — this is an explicit user action.
 *
 * @see Issue #1688 - Add Knowledge Entry Creation Commands
 */

import * as vscode from "vscode";
import * as path from "node:path";
import { KnowledgeService, type KnowledgeConfig } from "@nightgauge/sdk";
import { getWorkspaceRoot } from "../../config/settings.js";
import type { Logger } from "../../utils/logger.js";

export function registerKnowledgeScaffoldForIssueCommand(logger: Logger): vscode.Disposable {
  return vscode.commands.registerCommand("nightgauge.knowledge.scaffoldForIssue", async () => {
    logger.info("Knowledge: scaffoldForIssue command invoked");

    const workspaceRoot = getWorkspaceRoot();
    if (!workspaceRoot) {
      vscode.window.showErrorMessage("No workspace folder open.");
      return;
    }

    // Guard: knowledge must be enabled
    const knowledgeConfig = vscode.workspace.getConfiguration("nightgauge.knowledge");
    const enabled = knowledgeConfig.get<boolean>("enabled", false);
    if (!enabled) {
      vscode.window.showInformationMessage(
        "Knowledge base is disabled. Enable it in settings (nightgauge.knowledge.enabled)."
      );
      return;
    }

    // Prompt for issue number
    const issueInput = await vscode.window.showInputBox({
      prompt: "Issue number",
      placeHolder: "e.g. 1688",
      validateInput: (v) => (/^\d+$/.test(v.trim()) ? null : "Must be a number"),
    });

    if (!issueInput) {
      logger.debug("Knowledge: scaffoldForIssue cancelled at issue number");
      return;
    }

    const issueNumber = parseInt(issueInput.trim(), 10);

    // Prompt for issue title (used for slug generation)
    const issueTitle = await vscode.window.showInputBox({
      prompt: "Issue title",
      placeHolder: "e.g. Add knowledge entry creation commands",
    });

    if (issueTitle === undefined) {
      logger.debug("Knowledge: scaffoldForIssue cancelled at title input");
      return;
    }

    // Prompt for issue type (epic or feature)
    const typeSelection = await vscode.window.showQuickPick(
      ["Feature (features/)", "Epic (epics/)"],
      { placeHolder: "Issue type" }
    );

    if (!typeSelection) {
      logger.debug("Knowledge: scaffoldForIssue cancelled at type selection");
      return;
    }

    const isEpic = typeSelection.startsWith("Epic");

    // Build KnowledgeConfig — user explicitly triggered this, so auto_scaffold=true
    const config: KnowledgeConfig = {
      enabled: true,
      auto_scaffold: true,
    };

    try {
      const service = new KnowledgeService(workspaceRoot);
      const result = await service.scaffoldForIssue(issueNumber, issueTitle, "", isEpic, config);

      if (result.skipped) {
        vscode.window.showInformationMessage(
          `Knowledge scaffold skipped: ${result.skip_reason ?? "unknown reason"}`
        );
        return;
      }

      // Open PRD.md in editor
      if (result.files_created.includes("PRD.md")) {
        const prdPath = path.join(workspaceRoot, result.knowledge_path, "PRD.md");
        const doc = await vscode.workspace.openTextDocument(prdPath);
        await vscode.window.showTextDocument(doc);
      }

      logger.info("Knowledge: scaffoldForIssue created", {
        issueNumber,
        path: result.knowledge_path,
      });
      vscode.window.showInformationMessage(`Knowledge scaffolded at ${result.knowledge_path}`);
    } catch (error) {
      const message = error instanceof Error ? error.message : "Unknown error";
      logger.error("Knowledge: scaffoldForIssue failed", { error });
      vscode.window.showErrorMessage(
        `Failed to scaffold knowledge for issue #${issueNumber}: ${message}`
      );
    }
  });
}
