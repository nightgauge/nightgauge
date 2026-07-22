/**
 * NotifierInstancesSection — HTML fragment for the Notifier Instances settings section.
 *
 * Renders a table of configured notifier instances with action buttons that post
 * messages back to the extension host via vscode.postMessage.
 *
 * Mirrors ForgeInstancesSection exactly — pure HTML fragment, no business logic.
 *
 * @see Issue #3379 — Notifier Settings Panel
 */

export interface NotifierInstanceRow {
  id: string;
  type: "discord" | "mattermost";
  channel?: string;
  status: "connected" | "errored" | "disabled" | "unknown";
  lastEventSentAt?: string;
  lastError?: string;
  webhookRedacted?: string;
}

function escapeHtml(str: string): string {
  return str
    .replace(/&/g, "&amp;")
    .replace(/</g, "&lt;")
    .replace(/>/g, "&gt;")
    .replace(/"/g, "&quot;");
}

function typeBadgeHtml(type: "discord" | "mattermost"): string {
  const label = type === "discord" ? "Discord" : "Mattermost";
  const cls = type === "discord" ? "badge-discord" : "badge-mattermost";
  return `<span class="notifier-badge ${cls}">${label}</span>`;
}

function statusPillHtml(status: NotifierInstanceRow["status"]): string {
  const labels: Record<NotifierInstanceRow["status"], string> = {
    connected: "Connected",
    errored: "Errored",
    disabled: "Disabled",
    unknown: "Unknown",
  };
  const classes: Record<NotifierInstanceRow["status"], string> = {
    connected: "status-connected",
    errored: "status-errored",
    disabled: "status-disabled",
    unknown: "status-unknown",
  };
  return `<span class="notifier-status ${classes[status]}">${labels[status]}</span>`;
}

function formatLastEvent(iso?: string): string {
  if (!iso) return "Never";
  try {
    return new Date(iso).toLocaleString();
  } catch {
    return iso;
  }
}

/**
 * Generate the HTML fragment for the Notifier Instances section.
 *
 * @param notifiers - List of notifier instances (may be empty).
 * @param disabled - When true, action buttons are rendered as disabled.
 */
export function getNotifierInstancesSectionHtml(
  notifiers: NotifierInstanceRow[],
  disabled: boolean
): string {
  const disabledAttr = disabled ? " disabled" : "";

  const tableRows =
    notifiers.length === 0
      ? `<tr><td colspan="6" class="notifier-empty-row">No notifier instances configured. Click <strong>Add Discord</strong> or <strong>Add Mattermost</strong> to get started.</td></tr>`
      : notifiers
          .map((n) => {
            const errorTitle = n.lastError ? ` title="${escapeHtml(n.lastError)}"` : "";
            return `
          <tr class="notifier-row" data-instance-id="${escapeHtml(n.id)}">
            <td class="notifier-cell-id"><code>${escapeHtml(n.id)}</code></td>
            <td class="notifier-cell-type">${typeBadgeHtml(n.type)}</td>
            <td class="notifier-cell-channel">${escapeHtml(n.channel ?? "—")}</td>
            <td class="notifier-cell-status"${errorTitle}>${statusPillHtml(n.status)}</td>
            <td class="notifier-cell-last">${escapeHtml(formatLastEvent(n.lastEventSentAt))}</td>
            <td class="notifier-cell-actions">
              <button class="notifier-action-btn" data-notifier-action="test" data-instance-id="${escapeHtml(n.id)}"${disabledAttr} title="Send test message">
                <span class="codicon codicon-debug-alt"></span>
              </button>
              <button class="notifier-action-btn notifier-action-delete" data-notifier-action="remove" data-instance-id="${escapeHtml(n.id)}"${disabledAttr} title="Remove">
                <span class="codicon codicon-trash"></span>
              </button>
            </td>
          </tr>`;
          })
          .join("");

  return `
    <div class="subsection">
      <div class="notifier-toolbar">
        <button class="notifier-add-btn" id="notifier-add-discord-btn"${disabledAttr}>
          <span class="codicon codicon-add"></span>
          Add Discord
        </button>
        <button class="notifier-add-btn" id="notifier-add-mattermost-btn"${disabledAttr}>
          <span class="codicon codicon-add"></span>
          Add Mattermost
        </button>
      </div>
      <table class="notifier-table">
        <thead>
          <tr>
            <th>ID</th>
            <th>Type</th>
            <th>Channel</th>
            <th>Status</th>
            <th>Last Event</th>
            <th>Actions</th>
          </tr>
        </thead>
        <tbody>
          ${tableRows}
        </tbody>
      </table>
    </div>
    <style>
      .notifier-toolbar {
        margin-bottom: 12px;
        display: flex;
        gap: 8px;
      }
      .notifier-add-btn {
        display: inline-flex;
        align-items: center;
        gap: 6px;
        padding: 4px 10px;
        background: var(--vscode-button-background);
        color: var(--vscode-button-foreground);
        border: none;
        border-radius: 2px;
        cursor: pointer;
        font-size: 12px;
      }
      .notifier-add-btn:hover:not(:disabled) {
        background: var(--vscode-button-hoverBackground);
      }
      .notifier-add-btn:disabled {
        opacity: 0.5;
        cursor: default;
      }
      .notifier-table {
        width: 100%;
        border-collapse: collapse;
        font-size: 12px;
      }
      .notifier-table th {
        text-align: left;
        padding: 6px 8px;
        border-bottom: 1px solid var(--vscode-panel-border);
        color: var(--vscode-descriptionForeground);
        font-weight: 600;
      }
      .notifier-table td {
        padding: 6px 8px;
        border-bottom: 1px solid var(--vscode-panel-border);
        vertical-align: middle;
      }
      .notifier-empty-row {
        text-align: center;
        color: var(--vscode-descriptionForeground);
        padding: 16px !important;
      }
      .notifier-badge {
        display: inline-block;
        padding: 1px 6px;
        border-radius: 10px;
        font-size: 10px;
        font-weight: 600;
      }
      .badge-discord { background: #5865f2; color: #fff; }
      .badge-mattermost { background: #0058cc; color: #fff; }
      .notifier-status {
        display: inline-block;
        padding: 1px 6px;
        border-radius: 10px;
        font-size: 10px;
        font-weight: 600;
      }
      .status-connected { background: #57f287; color: #000; }
      .status-errored { background: #ed4245; color: #fff; }
      .status-disabled { background: var(--vscode-disabledForeground); color: var(--vscode-editor-background); }
      .status-unknown { background: var(--vscode-badge-background); color: var(--vscode-badge-foreground); }
      .notifier-cell-actions { white-space: nowrap; }
      .notifier-action-btn {
        background: none;
        border: none;
        cursor: pointer;
        padding: 2px 4px;
        color: var(--vscode-foreground);
        opacity: 0.7;
        border-radius: 2px;
      }
      .notifier-action-btn:hover:not(:disabled) {
        opacity: 1;
        background: var(--vscode-toolbar-hoverBackground);
      }
      .notifier-action-btn:disabled {
        opacity: 0.3;
        cursor: default;
      }
      .notifier-action-delete:hover:not(:disabled) {
        color: var(--vscode-errorForeground);
      }
    </style>
    <script>
      (function() {
        const addDiscordBtn = document.getElementById('notifier-add-discord-btn');
        if (addDiscordBtn) {
          addDiscordBtn.addEventListener('click', () => {
            vscode.postMessage({ type: 'notifier-add', notifierType: 'discord' });
          });
        }
        const addMattermostBtn = document.getElementById('notifier-add-mattermost-btn');
        if (addMattermostBtn) {
          addMattermostBtn.addEventListener('click', () => {
            vscode.postMessage({ type: 'notifier-add', notifierType: 'mattermost' });
          });
        }
        document.querySelectorAll('.notifier-action-btn').forEach(btn => {
          btn.addEventListener('click', () => {
            const action = btn.getAttribute('data-notifier-action');
            const id = btn.getAttribute('data-instance-id');
            if (action && id) {
              vscode.postMessage({ type: 'notifier-action', action, id });
            }
          });
        });
      })();
    </script>
  `;
}
