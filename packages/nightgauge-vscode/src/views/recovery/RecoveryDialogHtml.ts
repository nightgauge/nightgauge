/**
 * RecoveryDialogHtml — HTML/CSS/JS generator for the Recovery Dialog
 * webview. Mirrors the conventions in `ApprovalDialogHtml.ts`:
 * `--vscode-*` theme tokens, `--spacing-*` helpers, codicons, nonce-secured
 * inline scripts.
 *
 * The dialog is a thin renderer — it receives a fully computed
 * `RecoveryRequiredPayload` and posts back the chosen `RecoveryAction`.
 * All availability logic lives in `HeadlessOrchestrator.computeRecoveryRequired`.
 *
 * @see Issue #3239
 */

import * as vscode from "vscode";
import type { RecoveryAction, RecoveryRequiredPayload } from "@nightgauge/sdk";

const ACTION_LABELS: Record<RecoveryAction, string> = {
  "resume-from-paused-stage": "Resume from paused stage",
  "run-producing-stage": "Run producing stage now",
  "restart-from-beginning": "Restart from beginning",
  "discard-run": "Discard run",
  "open-run-state-directory": "Open run-state directory",
  cancel: "Cancel",
};

const ACTION_ICONS: Record<RecoveryAction, string> = {
  "resume-from-paused-stage": "codicon-debug-continue",
  "run-producing-stage": "codicon-play",
  "restart-from-beginning": "codicon-debug-restart",
  "discard-run": "codicon-trash",
  "open-run-state-directory": "codicon-folder-opened",
  cancel: "codicon-close",
};

const DESTRUCTIVE_ACTIONS: ReadonlySet<RecoveryAction> = new Set(["discard-run"]);

/**
 * Whether `restart-from-beginning` should require confirmation. Computed
 * from the run state — destructive only when an existing paused state
 * would be archived.
 */
function isRestartDestructive(payload: RecoveryRequiredPayload): boolean {
  return payload.runState === "paused";
}

function buttonClass(action: RecoveryAction, payload: RecoveryRequiredPayload): string {
  if (DESTRUCTIVE_ACTIONS.has(action)) return "danger";
  if (action === "restart-from-beginning" && isRestartDestructive(payload)) return "danger";
  if (action === "resume-from-paused-stage" || action === "run-producing-stage") return "primary";
  return "secondary";
}

/**
 * Generate HTML for the Recovery Dialog webview.
 */
export function getRecoveryDialogHtml(
  webview: vscode.Webview,
  payload: RecoveryRequiredPayload
): string {
  const nonce = getNonce();
  const titleBase = `Recovery Required — Issue #${payload.issueNumber} • ${formatStageName(payload.triggeringStage)}`;
  const subtitle = renderSubtitle(payload);
  const detail = escapeHtml(payload.errorDetail);

  const buttons = payload.availableActions
    .map((action) => {
      const label = escapeHtml(ACTION_LABELS[action] ?? action);
      const icon = ACTION_ICONS[action] ?? "codicon-circle-outline";
      const cls = buttonClass(action, payload);
      const destructive =
        DESTRUCTIVE_ACTIONS.has(action) ||
        (action === "restart-from-beginning" && isRestartDestructive(payload));
      return `      <button class="action-button ${cls}" data-action="${action}" data-destructive="${destructive ? "true" : "false"}">
        <span class="codicon ${icon}"></span>
        <span class="action-label">${label}</span>
      </button>`;
    })
    .join("\n");

  return `<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="UTF-8">
  <meta name="viewport" content="width=device-width, initial-scale=1.0">
  <meta http-equiv="Content-Security-Policy" content="default-src 'none'; style-src ${webview.cspSource} 'unsafe-inline'; script-src 'nonce-${nonce}';">
  <title>${escapeHtml(titleBase)}</title>
  <style>
    ${getStyles()}
  </style>
</head>
<body>
  <div class="recovery-dialog">
    <header class="dialog-header">
      <h1>
        <span class="codicon codicon-warning"></span>
        ${escapeHtml(titleBase)}
      </h1>
      <p class="subtitle">${subtitle}</p>
    </header>

    <main class="dialog-body">
      <section class="error-block">
        <h2>What happened</h2>
        <pre class="error-detail">${detail}</pre>
      </section>
      <section class="state-block">
        <h2>Pipeline state</h2>
        <dl class="state-fields">
          <dt>Run state</dt><dd>${escapeHtml(payload.runState)}</dd>
          <dt>Triggering stage</dt><dd>${escapeHtml(formatStageName(payload.triggeringStage))}</dd>
          <dt>Producing stage</dt><dd>${
            payload.producingStage
              ? escapeHtml(formatStageName(payload.producingStage))
              : '<em class="muted">unknown</em>'
          }</dd>
          <dt>Error kind</dt><dd>${escapeHtml(payload.errorKind)}</dd>
        </dl>
      </section>
    </main>

    <footer class="actions">
${buttons}
    </footer>
  </div>

  <script nonce="${nonce}">
    ${getScript()}
  </script>
</body>
</html>`;
}

function renderSubtitle(payload: RecoveryRequiredPayload): string {
  const producer = payload.producingStage
    ? `Producer: <strong>${escapeHtml(formatStageName(payload.producingStage))}</strong>. `
    : "";
  return `${producer}Choose how to recover from this failure.`;
}

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

    * { box-sizing: border-box; margin: 0; padding: 0; }

    body {
      font-family: var(--vscode-font-family);
      font-size: var(--vscode-font-size);
      color: var(--vscode-foreground);
      background: var(--vscode-editor-background);
      line-height: 1.5;
      height: 100vh;
      display: flex;
      flex-direction: column;
    }

    .recovery-dialog {
      display: flex;
      flex-direction: column;
      height: 100%;
      max-width: 900px;
      margin: 0 auto;
      padding: var(--spacing-md);
    }

    .dialog-header {
      padding-bottom: var(--spacing-md);
      border-bottom: 1px solid var(--vscode-panel-border);
      margin-bottom: var(--spacing-md);
    }

    .dialog-header h1 {
      font-size: 1.3em;
      font-weight: 600;
      display: flex;
      align-items: center;
      gap: var(--spacing-sm);
    }

    .dialog-header h1 .codicon {
      color: var(--vscode-editorWarning-foreground, var(--vscode-symbolIcon-fieldForeground));
    }

    .subtitle {
      margin-top: var(--spacing-sm);
      color: var(--vscode-descriptionForeground);
      font-size: 0.9em;
    }

    .dialog-body {
      flex: 1;
      overflow-y: auto;
      display: flex;
      flex-direction: column;
      gap: var(--spacing-md);
    }

    .error-block,
    .state-block {
      padding: var(--spacing-md);
      background: var(--vscode-editorWidget-background);
      border: 1px solid var(--vscode-panel-border);
      border-radius: var(--border-radius);
    }

    .error-block h2,
    .state-block h2 {
      font-size: 0.85em;
      font-weight: 600;
      text-transform: uppercase;
      letter-spacing: 0.04em;
      color: var(--vscode-descriptionForeground);
      margin-bottom: var(--spacing-sm);
    }

    .error-detail {
      font-family: var(--vscode-editor-font-family);
      font-size: 0.85em;
      white-space: pre-wrap;
      word-break: break-word;
      background: var(--vscode-textCodeBlock-background);
      padding: var(--spacing-sm);
      border-radius: var(--border-radius);
    }

    .state-fields {
      display: grid;
      grid-template-columns: max-content 1fr;
      gap: var(--spacing-xs) var(--spacing-md);
      font-size: 0.9em;
    }

    .state-fields dt {
      color: var(--vscode-descriptionForeground);
      font-weight: 500;
    }

    .state-fields dd {
      color: var(--vscode-foreground);
    }

    .state-fields .muted {
      color: var(--vscode-descriptionForeground);
      font-style: italic;
    }

    .actions {
      display: flex;
      gap: var(--spacing-sm);
      padding-top: var(--spacing-md);
      margin-top: var(--spacing-md);
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
    }

    .action-button:disabled {
      opacity: 0.5;
      cursor: not-allowed;
    }

    .action-button:focus {
      outline: 2px solid var(--vscode-focusBorder);
      outline-offset: 2px;
    }

    .action-button.primary {
      background: var(--vscode-button-background);
      color: var(--vscode-button-foreground);
    }

    .action-button.primary:hover:not(:disabled) {
      background: var(--vscode-button-hoverBackground);
    }

    .action-button.secondary {
      background: var(--vscode-button-secondaryBackground);
      color: var(--vscode-button-secondaryForeground);
    }

    .action-button.secondary:hover:not(:disabled) {
      background: var(--vscode-button-secondaryHoverBackground);
    }

    .action-button.danger {
      background: var(--vscode-inputValidation-errorBackground);
      color: var(--vscode-inputValidation-errorForeground);
      border: 1px solid var(--vscode-inputValidation-errorBorder);
    }

    .action-button.danger.confirming {
      background: var(--vscode-errorForeground, #ff5555);
      color: var(--vscode-button-foreground);
    }

    .codicon::before {
      font-family: codicon;
      font-size: 14px;
    }

    .codicon-warning::before { content: "\\ea6c"; }
    .codicon-debug-continue::before { content: "\\eacf"; }
    .codicon-play::before { content: "\\eb2c"; }
    .codicon-debug-restart::before { content: "\\ead4"; }
    .codicon-trash::before { content: "\\ea81"; }
    .codicon-folder-opened::before { content: "\\eaf7"; }
    .codicon-close::before { content: "\\ea76"; }
    .codicon-circle-outline::before { content: "\\eabc"; }

    @media (max-width: 600px) {
      .actions { flex-direction: column; }
      .action-button { width: 100%; justify-content: center; }
    }
  `;
}

function getScript(): string {
  return `
    (function() {
      const vscode = acquireVsCodeApi();
      let pendingConfirm = null;
      let pendingResetTimer = null;

      function send(action) {
        vscode.postMessage({ type: 'action', action: action, confirmed: true });
        document.querySelectorAll('.action-button').forEach(b => b.setAttribute('disabled', 'true'));
      }

      function clearPending() {
        if (pendingConfirm) {
          const button = document.querySelector('[data-action="' + pendingConfirm + '"]');
          if (button) {
            const labelEl = button.querySelector('.action-label');
            if (labelEl) labelEl.textContent = button.getAttribute('data-original-label') || labelEl.textContent;
            button.classList.remove('confirming');
          }
          pendingConfirm = null;
        }
        if (pendingResetTimer) {
          clearTimeout(pendingResetTimer);
          pendingResetTimer = null;
        }
      }

      document.querySelectorAll('.action-button').forEach(button => {
        const labelEl = button.querySelector('.action-label');
        if (labelEl) button.setAttribute('data-original-label', labelEl.textContent);

        button.addEventListener('click', () => {
          const action = button.getAttribute('data-action');
          const destructive = button.getAttribute('data-destructive') === 'true';
          if (!action) return;

          if (destructive && pendingConfirm !== action) {
            clearPending();
            pendingConfirm = action;
            const labelEl2 = button.querySelector('.action-label');
            if (labelEl2) labelEl2.textContent = 'Click again to confirm';
            button.classList.add('confirming');
            pendingResetTimer = setTimeout(clearPending, 5000);
            return;
          }

          clearPending();
          send(action);
        });
      });

      document.addEventListener('keydown', (e) => {
        if (e.key === 'Escape') {
          clearPending();
          send('cancel');
        }
      });
    })();
  `;
}

function formatStageName(stage: string): string {
  const labels: Record<string, string> = {
    "issue-pickup": "Issue Pickup",
    "feature-planning": "Feature Planning",
    "feature-dev": "Feature Development",
    "feature-validate": "Feature Validation",
    "pr-create": "PR Creation",
    "pr-merge": "PR Merge",
  };
  return labels[stage] ?? stage;
}

function escapeHtml(text: string): string {
  return text
    .replace(/&/g, "&amp;")
    .replace(/</g, "&lt;")
    .replace(/>/g, "&gt;")
    .replace(/"/g, "&quot;")
    .replace(/'/g, "&#39;");
}

function getNonce(): string {
  const possible = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789";
  let text = "";
  for (let i = 0; i < 32; i++) {
    text += possible.charAt(Math.floor(Math.random() * possible.length));
  }
  return text;
}
