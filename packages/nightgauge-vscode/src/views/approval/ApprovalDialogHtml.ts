/**
 * ApprovalDialogHtml - HTML template generator for the approval WebView
 *
 * Generates the HTML, CSS, and JavaScript for rendering PLAN.md content
 * with syntax highlighting and action buttons.
 */

import * as vscode from "vscode";
import { type PipelineStage } from "@nightgauge/sdk";
import { marked } from "marked";

/**
 * Generate the HTML content for the approval dialog WebView
 */
export function getApprovalDialogHtml(
  webview: vscode.Webview,
  extensionUri: vscode.Uri,
  stage: PipelineStage,
  issueNumber: number,
  planContent: string
): string {
  // Configure marked for safe rendering
  marked.setOptions({
    gfm: true,
    breaks: true,
  });

  // Render markdown to HTML
  const renderedMarkdown = marked.parse(planContent) as string;

  // Generate nonce for script security
  const nonce = getNonce();

  return `<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="UTF-8">
  <meta name="viewport" content="width=device-width, initial-scale=1.0">
  <meta http-equiv="Content-Security-Policy" content="default-src 'none'; style-src ${webview.cspSource} 'unsafe-inline'; script-src 'nonce-${nonce}';">
  <title>Review Plan #${issueNumber}</title>
  <style>
    ${getStyles()}
  </style>
</head>
<body>
  <div class="approval-dialog">
    <header class="dialog-header">
      <h1>
        <span class="codicon codicon-checklist"></span>
        Review Plan for Issue #${issueNumber}
      </h1>
      <p class="stage-info">
        <span class="stage-badge">${formatStageName(stage)}</span>
        requires approval before continuing
      </p>
    </header>

    <main class="plan-content" id="planContent">
      ${renderedMarkdown}
    </main>

    <footer class="actions">
      <button class="action-button primary" data-action="approve" title="Approve plan and continue pipeline">
        <span class="codicon codicon-check"></span>
        Approve
      </button>
      <button class="action-button secondary" data-action="edit" title="Open plan file in editor">
        <span class="codicon codicon-edit"></span>
        Edit Plan
      </button>
      <button class="action-button secondary" data-action="skip" title="Skip this stage and continue">
        <span class="codicon codicon-debug-step-over"></span>
        Skip Stage
      </button>
      <button class="action-button danger" data-action="cancel" title="Cancel and abort pipeline">
        <span class="codicon codicon-close"></span>
        Cancel
      </button>
    </footer>
  </div>

  <script nonce="${nonce}">
    ${getScript()}
  </script>
</body>
</html>`;
}

/**
 * Get the CSS styles for the dialog
 */
function getStyles(): string {
  return `
    :root {
      --spacing-xs: 4px;
      --spacing-sm: 8px;
      --spacing-md: 16px;
      --spacing-lg: 24px;
      --spacing-xl: 32px;
      --border-radius: 4px;
    }

    * {
      box-sizing: border-box;
      margin: 0;
      padding: 0;
    }

    body {
      font-family: var(--vscode-font-family);
      font-size: var(--vscode-font-size);
      color: var(--vscode-foreground);
      background: var(--vscode-editor-background);
      line-height: 1.6;
      padding: 0;
      margin: 0;
      height: 100vh;
      display: flex;
      flex-direction: column;
    }

    .approval-dialog {
      display: flex;
      flex-direction: column;
      height: 100%;
      max-width: 900px;
      margin: 0 auto;
      padding: var(--spacing-md);
    }

    /* Header */
    .dialog-header {
      padding-bottom: var(--spacing-md);
      border-bottom: 1px solid var(--vscode-panel-border);
      margin-bottom: var(--spacing-md);
    }

    .dialog-header h1 {
      font-size: 1.4em;
      font-weight: 600;
      color: var(--vscode-foreground);
      display: flex;
      align-items: center;
      gap: var(--spacing-sm);
    }

    .dialog-header h1 .codicon {
      color: var(--vscode-symbolIcon-fieldForeground);
    }

    .stage-info {
      margin-top: var(--spacing-sm);
      color: var(--vscode-descriptionForeground);
      font-size: 0.9em;
    }

    .stage-badge {
      display: inline-block;
      background: var(--vscode-badge-background);
      color: var(--vscode-badge-foreground);
      padding: 2px 8px;
      border-radius: var(--border-radius);
      font-weight: 500;
      font-size: 0.85em;
    }

    /* Plan Content */
    .plan-content {
      flex: 1;
      overflow-y: auto;
      padding: var(--spacing-md);
      background: var(--vscode-editor-background);
      border: 1px solid var(--vscode-panel-border);
      border-radius: var(--border-radius);
      margin-bottom: var(--spacing-md);
    }

    /* Markdown Styling */
    .plan-content h1,
    .plan-content h2,
    .plan-content h3,
    .plan-content h4,
    .plan-content h5,
    .plan-content h6 {
      margin-top: var(--spacing-lg);
      margin-bottom: var(--spacing-sm);
      font-weight: 600;
      color: var(--vscode-foreground);
    }

    .plan-content h1 { font-size: 1.5em; border-bottom: 1px solid var(--vscode-panel-border); padding-bottom: var(--spacing-sm); }
    .plan-content h2 { font-size: 1.3em; }
    .plan-content h3 { font-size: 1.1em; }
    .plan-content h4 { font-size: 1em; }

    .plan-content h1:first-child,
    .plan-content h2:first-child,
    .plan-content h3:first-child {
      margin-top: 0;
    }

    .plan-content p {
      margin-bottom: var(--spacing-md);
    }

    .plan-content ul,
    .plan-content ol {
      margin-bottom: var(--spacing-md);
      padding-left: var(--spacing-lg);
    }

    .plan-content li {
      margin-bottom: var(--spacing-xs);
    }

    .plan-content li input[type="checkbox"] {
      margin-right: var(--spacing-sm);
    }

    .plan-content a {
      color: var(--vscode-textLink-foreground);
      text-decoration: none;
    }

    .plan-content a:hover {
      text-decoration: underline;
    }

    .plan-content code {
      font-family: var(--vscode-editor-font-family);
      font-size: 0.9em;
      background: var(--vscode-textCodeBlock-background);
      padding: 2px 6px;
      border-radius: 3px;
    }

    .plan-content pre {
      background: var(--vscode-textCodeBlock-background);
      padding: var(--spacing-md);
      border-radius: var(--border-radius);
      overflow-x: auto;
      margin-bottom: var(--spacing-md);
    }

    .plan-content pre code {
      background: transparent;
      padding: 0;
      font-size: 0.85em;
      line-height: 1.5;
    }

    .plan-content blockquote {
      border-left: 4px solid var(--vscode-textBlockQuote-border);
      padding-left: var(--spacing-md);
      margin: var(--spacing-md) 0;
      color: var(--vscode-textBlockQuote-foreground);
    }

    .plan-content table {
      border-collapse: collapse;
      width: 100%;
      margin-bottom: var(--spacing-md);
    }

    .plan-content th,
    .plan-content td {
      border: 1px solid var(--vscode-panel-border);
      padding: var(--spacing-sm) var(--spacing-md);
      text-align: left;
    }

    .plan-content th {
      background: var(--vscode-editorWidget-background);
      font-weight: 600;
    }

    .plan-content tr:nth-child(even) {
      background: var(--vscode-list-hoverBackground);
    }

    .plan-content hr {
      border: none;
      border-top: 1px solid var(--vscode-panel-border);
      margin: var(--spacing-lg) 0;
    }

    .plan-content img {
      max-width: 100%;
      height: auto;
    }

    /* Actions Footer */
    .actions {
      display: flex;
      gap: var(--spacing-sm);
      padding-top: var(--spacing-md);
      border-top: 1px solid var(--vscode-panel-border);
      flex-wrap: wrap;
    }

    .action-button {
      display: inline-flex;
      align-items: center;
      gap: var(--spacing-sm);
      padding: var(--spacing-sm) var(--spacing-md);
      border: none;
      border-radius: var(--border-radius);
      font-family: var(--vscode-font-family);
      font-size: var(--vscode-font-size);
      cursor: pointer;
      transition: background-color 0.15s, opacity 0.15s;
    }

    .action-button:hover {
      opacity: 0.9;
    }

    .action-button:active {
      opacity: 0.8;
    }

    .action-button:focus {
      outline: 2px solid var(--vscode-focusBorder);
      outline-offset: 2px;
    }

    .action-button.primary {
      background: var(--vscode-button-background);
      color: var(--vscode-button-foreground);
    }

    .action-button.primary:hover {
      background: var(--vscode-button-hoverBackground);
    }

    .action-button.secondary {
      background: var(--vscode-button-secondaryBackground);
      color: var(--vscode-button-secondaryForeground);
    }

    .action-button.secondary:hover {
      background: var(--vscode-button-secondaryHoverBackground);
    }

    .action-button.danger {
      background: var(--vscode-inputValidation-errorBackground);
      color: var(--vscode-inputValidation-errorForeground);
      border: 1px solid var(--vscode-inputValidation-errorBorder);
    }

    .action-button.danger:hover {
      opacity: 0.85;
    }

    /* Codicon placeholder - using unicode for icons */
    .codicon::before {
      font-family: codicon;
      font-size: 14px;
    }

    .codicon-check::before { content: "\\eab2"; }
    .codicon-edit::before { content: "\\ea73"; }
    .codicon-debug-step-over::before { content: "\\eb09"; }
    .codicon-close::before { content: "\\ea76"; }
    .codicon-checklist::before { content: "\\eb02"; }

    /* Responsive */
    @media (max-width: 600px) {
      .actions {
        flex-direction: column;
      }

      .action-button {
        width: 100%;
        justify-content: center;
      }
    }
  `;
}

/**
 * Get the JavaScript for the WebView
 */
function getScript(): string {
  return `
    (function() {
      const vscode = acquireVsCodeApi();

      // Handle button clicks
      document.querySelectorAll('.action-button').forEach(button => {
        button.addEventListener('click', () => {
          const action = button.getAttribute('data-action');
          if (action) {
            vscode.postMessage({
              type: 'action',
              action: action
            });
          }
        });
      });

      // Handle keyboard shortcuts
      document.addEventListener('keydown', (e) => {
        // Enter = Approve
        if (e.key === 'Enter' && !e.shiftKey && !e.ctrlKey && !e.metaKey) {
          vscode.postMessage({ type: 'action', action: 'approve' });
        }
        // Escape = Cancel
        if (e.key === 'Escape') {
          vscode.postMessage({ type: 'action', action: 'cancel' });
        }
      });

      // Handle content updates from extension
      window.addEventListener('message', event => {
        const message = event.data;
        if (message.type === 'update') {
          const contentEl = document.getElementById('planContent');
          if (contentEl) {
            contentEl.innerHTML = message.content;
          }
        }
      });
    })();
  `;
}

/**
 * Format stage name for display
 */
function formatStageName(stage: PipelineStage): string {
  const labels: Record<PipelineStage, string> = {
    "pipeline-start": "Initialize",
    "issue-pickup": "Issue Pickup",
    "feature-planning": "Feature Planning",
    "feature-dev": "Feature Development",
    "feature-validate": "Feature Validation",
    "pr-create": "PR Creation",
    "pr-merge": "PR Merge",
    "pipeline-finish": "Completion",
  };
  return labels[stage] ?? stage;
}

/**
 * Generate a nonce for script security
 */
function getNonce(): string {
  let text = "";
  const possible = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789";
  for (let i = 0; i < 32; i++) {
    text += possible.charAt(Math.floor(Math.random() * possible.length));
  }
  return text;
}
