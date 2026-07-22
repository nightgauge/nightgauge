/**
 * ContextFileViewer - TextDocumentContentProvider for viewing context JSON
 *
 * Provides read-only, pretty-printed JSON views of pipeline context files.
 * Registers the 'nightgauge-context' URI scheme.
 */

import * as vscode from "vscode";
import * as fs from "node:fs/promises";
import * as path from "node:path";

/**
 * URI scheme for context file viewing
 */
export const CONTEXT_URI_SCHEME = "nightgauge-context";

/**
 * ContextFileViewer - TextDocumentContentProvider for context JSON files
 *
 * @example
 * ```typescript
 * const viewer = new ContextFileViewer('/workspace/.nightgauge/pipeline');
 * context.subscriptions.push(
 *   vscode.workspace.registerTextDocumentContentProvider(
 *     CONTEXT_URI_SCHEME,
 *     viewer
 *   )
 * );
 *
 * // Open a context file
 * const uri = viewer.createUri('issue-42.json');
 * await vscode.commands.executeCommand('vscode.open', uri);
 * ```
 */
export class ContextFileViewer implements vscode.TextDocumentContentProvider {
  private contextPath: string;
  private _onDidChange = new vscode.EventEmitter<vscode.Uri>();

  /**
   * Event that fires when document content changes
   */
  readonly onDidChange = this._onDidChange.event;

  constructor(contextPath: string) {
    this.contextPath = contextPath;
  }

  /**
   * Provide content for a virtual document
   */
  async provideTextDocumentContent(uri: vscode.Uri): Promise<string> {
    const filename = uri.path.slice(1); // Remove leading slash
    const filePath = path.join(this.contextPath, filename);

    try {
      const content = await fs.readFile(filePath, "utf-8");
      const json = JSON.parse(content);
      return JSON.stringify(json, null, 2);
    } catch (error) {
      if ((error as NodeJS.ErrnoException).code === "ENOENT") {
        return `// Context file not found: ${filename}\n// File may not exist yet or has been cleaned up.`;
      }
      return `// Error reading context file: ${error instanceof Error ? error.message : "Unknown error"}`;
    }
  }

  /**
   * Create a URI for viewing a context file
   */
  createUri(filename: string): vscode.Uri {
    return vscode.Uri.parse(`${CONTEXT_URI_SCHEME}:/${filename}`);
  }

  /**
   * Notify that a document has changed (triggers refresh)
   */
  refresh(filename: string): void {
    this._onDidChange.fire(this.createUri(filename));
  }

  /**
   * Refresh all known context files
   */
  refreshAll(issueNumber: number): void {
    const files = [
      `issue-${issueNumber}.json`,
      `planning-${issueNumber}.json`,
      `dev-${issueNumber}.json`,
      `pr-${issueNumber}.json`,
    ];

    for (const file of files) {
      this._onDidChange.fire(this.createUri(file));
    }
  }

  /**
   * Get the current context path
   */
  getContextPath(): string {
    return this.contextPath;
  }

  /**
   * Update the context path
   */
  setContextPath(contextPath: string): void {
    this.contextPath = contextPath;
  }

  /**
   * Dispose the event emitter
   */
  dispose(): void {
    this._onDidChange.dispose();
  }
}

/**
 * Open a context file in the editor
 */
export async function openContextFile(viewer: ContextFileViewer, filename: string): Promise<void> {
  const uri = viewer.createUri(filename);
  const doc = await vscode.workspace.openTextDocument(uri);
  await vscode.window.showTextDocument(doc, { preview: true });
}
