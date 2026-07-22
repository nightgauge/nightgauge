/**
 * New ADR command
 *
 * Creates a numbered Architecture Decision Record under
 * .nightgauge/knowledge/architecture/. Bypasses KnowledgeService.create()
 * because the service maps `adr` → `decisions.md`, which conflicts with the
 * numbered filename convention (NNN-slug.md) used by ADRs.
 *
 * @see Issue #1688 - Add Knowledge Entry Creation Commands
 */

import * as vscode from "vscode";
import * as path from "node:path";
import * as fs from "node:fs/promises";
import { KnowledgeService } from "@nightgauge/sdk";
import { getWorkspaceRoot } from "../../config/settings.js";
import type { Logger } from "../../utils/logger.js";

/** Generate the ADR frontmatter + body template */
function buildAdrTemplate(numStr: string, title: string): string {
  const now = new Date().toISOString();
  const date = now.slice(0, 10);
  return `---
type: adr
title: "${numStr} — ${title}"
created: ${now}
updated: ${now}
status: draft
---

# ADR ${numStr}: ${title}

**Status**: Draft  **Date**: ${date}

## Context

<!-- Why is this decision needed? What forces are at play? -->

## Decision

<!-- What was decided? -->

## Consequences

<!-- What are the trade-offs and future implications? -->
`;
}

export function registerKnowledgeNewADRCommand(logger: Logger): vscode.Disposable {
  return vscode.commands.registerCommand("nightgauge.knowledge.newADR", async () => {
    logger.info("Knowledge: newADR command invoked");

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

    // Prompt for title
    const title = await vscode.window.showInputBox({
      prompt: "ADR title",
      placeHolder: "e.g. Use PostgreSQL for persistence",
      validateInput: (v) => (v.trim().length > 0 ? null : "Title cannot be empty"),
    });

    if (!title) {
      logger.debug("Knowledge: newADR cancelled at title input");
      return;
    }

    try {
      const adrDir = path.join(workspaceRoot, ".nightgauge", "knowledge", "architecture");

      // Ensure architecture directory exists
      await fs.mkdir(adrDir, { recursive: true });

      // Auto-number: scan for existing NNN-*.md files
      const files = await fs.readdir(adrDir).catch(() => [] as string[]);
      const nums = files.map((f) => parseInt(f.slice(0, 3), 10)).filter((n) => !isNaN(n));
      const nextNum = nums.length > 0 ? Math.max(...nums) + 1 : 1;
      const numStr = String(nextNum).padStart(3, "0");

      // Generate slug from title
      const service = new KnowledgeService(workspaceRoot);
      const slug = service.generateSlug(title);

      const filename = `${numStr}-${slug}.md`;
      const filePath = path.join(adrDir, filename);

      // Write ADR directly — bypass service.create() to get proper numbered filename
      const content = buildAdrTemplate(numStr, title);
      await fs.writeFile(filePath, content, "utf-8");

      // Open in editor
      const doc = await vscode.workspace.openTextDocument(filePath);
      await vscode.window.showTextDocument(doc);

      logger.info("Knowledge: newADR created", { numStr, title, filename });
      vscode.window.showInformationMessage(`ADR ${numStr} created: ${title}`);
    } catch (error) {
      const message = error instanceof Error ? error.message : "Unknown error";
      logger.error("Knowledge: newADR failed", { error });
      vscode.window.showErrorMessage(`Failed to create ADR: ${message}`);
    }
  });
}
