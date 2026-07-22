/**
 * PromptTemplateService - Template loading and rendering for the VSCode extension
 *
 * Wraps the SDK's TemplateRegistry and PromptRenderer with VSCode-specific
 * concerns: extension path resolution, in-memory caching of rendered results,
 * and typed helper methods for common dialog prompts.
 *
 * @see docs/PROMPT_TEMPLATES.md for full documentation
 */

import * as path from "node:path";
import { TemplateRegistry, PromptRenderer, type TemplateContext } from "@nightgauge/sdk";

/**
 * Context type for system prompt templates (pipeline stage roles)
 */
export interface SystemPromptContext {
  issueNumber?: number;
  issueTitle?: string;
  complexity?: string;
  docScopePath?: string;
  planFile?: string;
  retryCount?: number;
  buildCommand?: string;
  testCommand?: string;
  baseBranch?: string;
  reviewers?: string;
  mergeStrategy?: string;
  waitForCi?: boolean;
  repoName?: string;
  defaultBranch?: string;
  [key: string]: unknown;
}

/**
 * Context type for dialog prompt templates (user-facing dialogs)
 */
export interface DialogPromptContext {
  issueNumber?: number;
  issueTitle?: string;
  stageName?: string;
  summary?: string;
  assessedComplexity?: string;
  rationale?: string;
  planFile?: string;
  [key: string]: unknown;
}

/**
 * Service providing prompt template rendering for the VSCode extension.
 *
 * Initialize once during extension activation by calling `initialize()`.
 * After initialization, all `render*` methods are synchronous.
 *
 * @example
 * ```typescript
 * const service = new PromptTemplateService('/path/to/extension');
 * await service.initialize();
 *
 * const prompt = service.renderSystemPrompt('feature-planning', {
 *   issueNumber: 42,
 *   issueTitle: 'Add dark mode',
 * });
 * ```
 */
export class PromptTemplateService {
  private readonly registry: TemplateRegistry;
  private readonly renderer: PromptRenderer;
  private initialized = false;

  /**
   * @param extensionPath - Absolute path to the extension root (used to
   *   locate the `skills/templates` directory within the workspace).
   *   Typically `context.extensionPath` from the activate function.
   * @param workspaceRoot - Optional override for the workspace root.
   *   Defaults to two levels above `extensionPath`
   *   (handles `packages/nightgauge-vscode/` nesting).
   */
  constructor(
    private readonly extensionPath: string,
    private readonly workspaceRoot?: string
  ) {
    this.registry = new TemplateRegistry();
    this.renderer = new PromptRenderer();
  }

  /**
   * Load all templates from `skills/templates/` relative to the workspace root.
   *
   * Safe to call multiple times — subsequent calls reload templates from disk
   * and merge into the registry (new versions win on conflicts).
   *
   * @throws Error when the templates directory is missing and the error is
   *   not recoverable. Does NOT throw when the directory simply does not
   *   exist yet (treats it as empty — `ignore: true`).
   */
  async initialize(): Promise<void> {
    const templatesDir = path.join(this.resolveWorkspaceRoot(), "skills", "templates");
    await this.registry.loadTemplates(templatesDir, { ignore: true });
    this.initialized = true;
  }

  /**
   * Render a system prompt template for a pipeline stage.
   *
   * @param stageName - Stage identifier (e.g., `"feature-planning"`,
   *   `"issue-pickup"`). Automatically appended with `-system` for lookup.
   * @param context - Template variables
   * @returns Rendered prompt string, or `null` if the template is not found
   */
  renderSystemPrompt(stageName: string, context: SystemPromptContext = {}): string | null {
    this.assertInitialized();
    const templateName = stageName.endsWith("-system") ? stageName : `${stageName}-system`;
    return this.renderByName(templateName, context);
  }

  /**
   * Render the complexity-assessment dialog.
   *
   * @returns Rendered dialog string, or `null` if template not found
   */
  renderComplexityAssessment(context: DialogPromptContext): string | null {
    this.assertInitialized();
    return this.renderByName("complexity-assessment-dialog", context);
  }

  /**
   * Render the approval prompt dialog.
   *
   * @returns Rendered dialog string, or `null` if template not found
   */
  renderApprovalPrompt(context: DialogPromptContext): string | null {
    this.assertInitialized();
    return this.renderByName("approval-prompt-dialog", context);
  }

  /**
   * Render any registered template by name.
   *
   * @param name - Template name
   * @param context - Variable bindings
   * @param version - Optional exact version; defaults to latest
   * @returns Rendered string or `null` if template not found
   */
  renderByName(name: string, context: TemplateContext = {}, version?: string): string | null {
    const template = this.registry.getTemplate(name, version);
    if (!template) return null;
    return this.renderer.render(template, context);
  }

  /**
   * Check whether a template with the given name (and optional version) exists.
   */
  hasTemplate(name: string, version?: string): boolean {
    return this.registry.getTemplate(name, version) !== null;
  }

  /**
   * Returns `true` after `initialize()` has been called at least once.
   */
  get isInitialized(): boolean {
    return this.initialized;
  }

  /**
   * Number of loaded templates.
   */
  get templateCount(): number {
    return this.registry.size;
  }

  // ---------------------------------------------------------------------------
  // Private helpers
  // ---------------------------------------------------------------------------

  private resolveWorkspaceRoot(): string {
    if (this.workspaceRoot) return this.workspaceRoot;
    // Extension lives at packages/nightgauge-vscode/ within the monorepo.
    // Two levels up from extensionPath gives the repo root.
    return path.resolve(this.extensionPath, "..", "..");
  }

  private assertInitialized(): void {
    if (!this.initialized) {
      throw new Error("PromptTemplateService: call initialize() before using render methods");
    }
  }
}
