/**
 * ForgeInstancesSection - HTML fragment for the Forge Instances settings section.
 *
 * Renders a table of configured forge instances with action buttons that post
 * messages back to the extension host via vscode.postMessage.
 *
 * @see Issue #3364 - VSCode extension settings UI for managing forge instances
 */

export interface ForgeInstanceRow {
  id: string;
  kind: string;
  base_url: string;
  auth_method: string;
  ca_bundle?: string;
  lastTested?: string;
  isDefault?: boolean;
}

function escapeHtml(str: string): string {
  return str
    .replace(/&/g, "&amp;")
    .replace(/</g, "&lt;")
    .replace(/>/g, "&gt;")
    .replace(/"/g, "&quot;");
}

function authMethodLabel(method: string): string {
  switch (method) {
    case "pat":
      return "PAT";
    case "oauth2":
      return "OAuth2";
    case "ci_job_token":
      return "CI Job Token";
    case "deploy_token":
      return "Deploy Token";
    case "token":
      return "Token";
    case "app":
      return "GitHub App";
    default:
      return method || "—";
  }
}

function kindBadgeHtml(kind: string): string {
  const label = kind === "gitlab" ? "GitLab" : "GitHub";
  const cls = kind === "gitlab" ? "badge-gitlab" : "badge-github";
  return `<span class="forge-badge ${cls}">${escapeHtml(label)}</span>`;
}

function formatLastTested(iso?: string): string {
  if (!iso) return "Never";
  try {
    const d = new Date(iso);
    return d.toLocaleString();
  } catch {
    return iso;
  }
}

/**
 * Generate the HTML fragment for the Forge Instances section.
 *
 * @param forges - List of forge instances from the extension host (may be empty).
 * @param disabled - When true, action buttons are rendered as disabled.
 * @param defaultForgeId - The current default_forge value from config.
 */
export function getForgeInstancesSectionHtml(
  forges: ForgeInstanceRow[],
  disabled: boolean,
  defaultForgeId?: string
): string {
  const disabledAttr = disabled ? " disabled" : "";

  const tableRows =
    forges.length === 0
      ? `<tr><td colspan="6" class="forge-empty-row">No forge instances configured. Click <strong>Add Forge Instance</strong> to get started.</td></tr>`
      : forges
          .map((f) => {
            const isDefault = f.id === defaultForgeId || f.isDefault;
            const defaultBadge = isDefault
              ? ` <span class="forge-badge badge-default" title="Default forge">default</span>`
              : "";
            return `
          <tr class="forge-row" data-instance-id="${escapeHtml(f.id)}">
            <td class="forge-cell-id">
              <code>${escapeHtml(f.id)}</code>${defaultBadge}
            </td>
            <td class="forge-cell-kind">${kindBadgeHtml(f.kind)}</td>
            <td class="forge-cell-url" title="${escapeHtml(f.base_url)}">
              ${escapeHtml(f.base_url || "(default)")}
            </td>
            <td class="forge-cell-auth">${escapeHtml(authMethodLabel(f.auth_method))}</td>
            <td class="forge-cell-tested">${escapeHtml(formatLastTested(f.lastTested))}</td>
            <td class="forge-cell-actions">
              <button class="forge-action-btn" data-forge-action="test" data-instance-id="${escapeHtml(f.id)}"${disabledAttr} title="Test connection">
                <span class="codicon codicon-debug-alt"></span>
              </button>
              <button class="forge-action-btn" data-forge-action="edit" data-instance-id="${escapeHtml(f.id)}"${disabledAttr} title="Edit">
                <span class="codicon codicon-edit"></span>
              </button>
              ${
                !isDefault
                  ? `<button class="forge-action-btn" data-forge-action="set-default" data-instance-id="${escapeHtml(f.id)}"${disabledAttr} title="Set as default">
                <span class="codicon codicon-star-empty"></span>
              </button>`
                  : `<button class="forge-action-btn" data-forge-action="set-default" data-instance-id="${escapeHtml(f.id)}" disabled title="Already default">
                <span class="codicon codicon-star-full"></span>
              </button>`
              }
              <button class="forge-action-btn forge-action-delete" data-forge-action="delete" data-instance-id="${escapeHtml(f.id)}"${disabledAttr} title="Delete">
                <span class="codicon codicon-trash"></span>
              </button>
            </td>
          </tr>`;
          })
          .join("");

  return `
    <div class="subsection">
      <div class="forge-toolbar">
        <button class="forge-add-btn" id="forge-add-btn"${disabledAttr}>
          <span class="codicon codicon-add"></span>
          Add Forge Instance
        </button>
      </div>
      <table class="forge-table">
        <thead>
          <tr>
            <th>ID</th>
            <th>Kind</th>
            <th>URL</th>
            <th>Auth Method</th>
            <th>Last Tested</th>
            <th>Actions</th>
          </tr>
        </thead>
        <tbody>
          ${tableRows}
        </tbody>
      </table>
    </div>
    <style>
      .forge-toolbar {
        margin-bottom: 12px;
      }
      .forge-add-btn {
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
      .forge-add-btn:hover:not(:disabled) {
        background: var(--vscode-button-hoverBackground);
      }
      .forge-add-btn:disabled {
        opacity: 0.5;
        cursor: default;
      }
      .forge-table {
        width: 100%;
        border-collapse: collapse;
        font-size: 12px;
      }
      .forge-table th {
        text-align: left;
        padding: 6px 8px;
        border-bottom: 1px solid var(--vscode-panel-border);
        color: var(--vscode-descriptionForeground);
        font-weight: 600;
      }
      .forge-table td {
        padding: 6px 8px;
        border-bottom: 1px solid var(--vscode-panel-border);
        vertical-align: middle;
      }
      .forge-empty-row {
        text-align: center;
        color: var(--vscode-descriptionForeground);
        padding: 16px !important;
      }
      .forge-badge {
        display: inline-block;
        padding: 1px 6px;
        border-radius: 10px;
        font-size: 10px;
        font-weight: 600;
      }
      .badge-github { background: #24292e; color: #fff; }
      .badge-gitlab { background: #fc6d26; color: #fff; }
      .badge-default { background: var(--vscode-badge-background); color: var(--vscode-badge-foreground); margin-left: 4px; }
      .forge-cell-url {
        max-width: 200px;
        overflow: hidden;
        text-overflow: ellipsis;
        white-space: nowrap;
      }
      .forge-cell-actions {
        white-space: nowrap;
      }
      .forge-action-btn {
        background: none;
        border: none;
        cursor: pointer;
        padding: 2px 4px;
        color: var(--vscode-foreground);
        opacity: 0.7;
        border-radius: 2px;
      }
      .forge-action-btn:hover:not(:disabled) {
        opacity: 1;
        background: var(--vscode-toolbar-hoverBackground);
      }
      .forge-action-btn:disabled {
        opacity: 0.3;
        cursor: default;
      }
      .forge-action-delete:hover:not(:disabled) {
        color: var(--vscode-errorForeground);
      }
    </style>
    <script>
      (function() {
        const addBtn = document.getElementById('forge-add-btn');
        if (addBtn) {
          addBtn.addEventListener('click', () => {
            vscode.postMessage({ type: 'forge-add' });
          });
        }
        document.querySelectorAll('.forge-action-btn').forEach(btn => {
          btn.addEventListener('click', () => {
            const action = btn.getAttribute('data-forge-action');
            const instanceId = btn.getAttribute('data-instance-id');
            if (action && instanceId) {
              vscode.postMessage({ type: 'forge-action', action, instanceId });
            }
          });
        });
      })();
    </script>
  `;
}
