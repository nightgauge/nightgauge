/**
 * New Knowledge Entry command
 *
 * Creates a standalone knowledge entry via KnowledgeService.create().
 * Prompts for type and title, then opens the created file in the editor.
 *
 * @see Issue #1688 - Add Knowledge Entry Creation Commands
 */

import * as vscode from "vscode";
import * as path from "node:path";
import { KnowledgeService } from "@nightgauge/sdk";
import type { KnowledgeType } from "@nightgauge/sdk";
import { getWorkspaceRoot } from "../../config/settings.js";
import type { Logger } from "../../utils/logger.js";

/** Map KnowledgeType to its default filename (mirrors KnowledgeService.TYPE_FILENAME_MAP) */
function typeToFilename(type: KnowledgeType): string {
  const map: Record<string, string> = {
    prd: "PRD.md",
    decision: "decisions.md",
    adr: "decisions.md",
    conversation: "conversation.md",
    reference: "reference.md",
    note: "note.md",
  };
  return map[type] ?? `${type}.md`;
}

interface TypeQuickPickItem extends vscode.QuickPickItem {
  value: KnowledgeType;
}

export function registerKnowledgeNewEntryCommand(logger: Logger): vscode.Disposable {
  return vscode.commands.registerCommand("nightgauge.knowledge.newEntry", async () => {
    logger.info("Knowledge: newEntry command invoked");

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

    // Prompt for entry type
    const typeItems: TypeQuickPickItem[] = [
      {
        label: "$(file-text) Note",
        value: "note",
        description: "General project notes",
      },
      {
        label: "$(book) PRD",
        value: "prd",
        description: "Product requirements document",
      },
      {
        label: "$(law) Decision",
        value: "decision",
        description: "Architecture/design decision",
      },
      {
        label: "$(archive) ADR",
        value: "adr",
        description: "Architecture Decision Record",
      },
      {
        label: "$(bookmark) Reference",
        value: "reference",
        description: "Reference material",
      },
    ];

    const selectedType = await vscode.window.showQuickPick(typeItems, {
      placeHolder: "Select knowledge entry type",
      title: "New Knowledge Entry",
    });

    if (!selectedType) {
      logger.debug("Knowledge: newEntry cancelled at type selection");
      return;
    }

    // Prompt for title
    const title = await vscode.window.showInputBox({
      prompt: "Entry title",
      placeHolder: "e.g. Database connection strategy",
      validateInput: (v) => (v.trim().length > 0 ? null : "Title cannot be empty"),
    });

    if (!title) {
      logger.debug("Knowledge: newEntry cancelled at title input");
      return;
    }

    try {
      const service = new KnowledgeService(workspaceRoot);
      const generatedSlug = service.generateSlug(title);
      const slug = `standalone/${generatedSlug}`;

      await service.create(selectedType.value, slug, "", {
        title,
        type: selectedType.value,
      });

      // Resolve absolute file path and open in editor
      const absPath = path.join(
        workspaceRoot,
        ".nightgauge",
        "knowledge",
        slug,
        typeToFilename(selectedType.value)
      );

      const doc = await vscode.workspace.openTextDocument(absPath);
      await vscode.window.showTextDocument(doc);

      logger.info("Knowledge: newEntry created", {
        title,
        type: selectedType.value,
        slug,
      });
      vscode.window.showInformationMessage(`Knowledge entry created: ${title}`);
    } catch (error) {
      const message = error instanceof Error ? error.message : "Unknown error";
      logger.error("Knowledge: newEntry failed", { error });
      vscode.window.showErrorMessage(
        message.includes("already exists")
          ? "Entry already exists at that path."
          : `Failed to create knowledge entry: ${message}`
      );
    }
  });
}
