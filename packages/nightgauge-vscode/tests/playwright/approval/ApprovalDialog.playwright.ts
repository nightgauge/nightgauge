/**
 * Playwright tests for ApprovalDialog webview behaviors (Issue #1246)
 *
 * Targets embedded JS behaviors that Vitest cannot reach:
 *   - Action button clicks post { type: "action", action: <approve|edit|skip|cancel> }
 *   - Plan markdown content is rendered visibly in the panel body
 *   - Keyboard shortcuts: Enter = approve, Escape = cancel
 *
 * Uses the webview-loader helper from Issue #1243 to inject the acquireVsCodeApi() mock
 * before page scripts execute, capturing all postMessage calls in window.__vscodeMessages[].
 */

import { test, expect } from "@playwright/test";
import { loadWebview, getPostedMessages } from "../helpers/webview-loader.js";

/**
 * Minimal ApprovalDialog HTML fixture mirroring the script logic from ApprovalDialogHtml.ts
 * getScript(). Validates the behavior contract without importing the TS module directly
 * (which depends on the vscode API and marked library).
 */
function createApprovalDialogHtml(planHtml = "<p>Plan content here</p>"): string {
  return `<!DOCTYPE html>
<html lang="en">
<head><meta charset="UTF-8"></head>
<body>
  <div class="approval-dialog">
    <header class="dialog-header">
      <h1>Review Plan for Issue #42</h1>
      <p class="stage-info">
        <span class="stage-badge">Feature Planning</span>
        requires approval before continuing
      </p>
    </header>

    <main class="plan-content" id="planContent">
      ${planHtml}
    </main>

    <footer class="actions">
      <button class="action-button primary" data-action="approve" title="Approve plan and continue pipeline">
        Approve
      </button>
      <button class="action-button secondary" data-action="edit" title="Open plan file in editor">
        Edit Plan
      </button>
      <button class="action-button secondary" data-action="skip" title="Skip this stage and continue">
        Skip Stage
      </button>
      <button class="action-button danger" data-action="cancel" title="Cancel and abort pipeline">
        Cancel
      </button>
    </footer>
  </div>

  <script>
    // -----------------------------------------------------------------------
    // ApprovalDialog script (mirrors ApprovalDialogHtml.ts getScript())
    // -----------------------------------------------------------------------
    (function() {
      const vscode = acquireVsCodeApi();

      // Handle button clicks
      document.querySelectorAll('.action-button').forEach(function(button) {
        button.addEventListener('click', function() {
          var action = button.getAttribute('data-action');
          if (action) {
            vscode.postMessage({ type: 'action', action: action });
          }
        });
      });

      // Handle keyboard shortcuts
      document.addEventListener('keydown', function(e) {
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
      window.addEventListener('message', function(event) {
        var message = event.data;
        if (message.type === 'update') {
          var contentEl = document.getElementById('planContent');
          if (contentEl) {
            contentEl.innerHTML = message.content;
          }
        }
      });
    })();
  </script>
</body>
</html>`;
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

test.describe("ApprovalDialog webview", () => {
  test('"Approve" button click posts { type: "action", action: "approve" }', async ({ page }) => {
    await loadWebview(page, createApprovalDialogHtml());

    await page.click('[data-action="approve"]');

    const messages = await getPostedMessages(page);
    expect(messages).toContainEqual({ type: "action", action: "approve" });
  });

  test('"Edit" button click posts { type: "action", action: "edit" }', async ({ page }) => {
    await loadWebview(page, createApprovalDialogHtml());

    await page.click('[data-action="edit"]');

    const messages = await getPostedMessages(page);
    expect(messages).toContainEqual({ type: "action", action: "edit" });
  });

  test('"Skip" button click posts { type: "action", action: "skip" }', async ({ page }) => {
    await loadWebview(page, createApprovalDialogHtml());

    await page.click('[data-action="skip"]');

    const messages = await getPostedMessages(page);
    expect(messages).toContainEqual({ type: "action", action: "skip" });
  });

  test('"Cancel" button click posts { type: "action", action: "cancel" }', async ({ page }) => {
    await loadWebview(page, createApprovalDialogHtml());

    await page.click('[data-action="cancel"]');

    const messages = await getPostedMessages(page);
    expect(messages).toContainEqual({ type: "action", action: "cancel" });
  });

  test("plan markdown content is rendered visibly in the panel body", async ({ page }) => {
    const planHtml =
      "<h2>Implementation Plan</h2><p>Step 1: Do something</p><ul><li>Task A</li><li>Task B</li></ul>";
    await loadWebview(page, createApprovalDialogHtml(planHtml));

    const planContent = await page.textContent("#planContent");
    expect(planContent).toContain("Implementation Plan");
    expect(planContent).toContain("Step 1: Do something");
    expect(planContent).toContain("Task A");

    // Verify the element is visible
    const isVisible = await page.isVisible("#planContent");
    expect(isVisible).toBe(true);
  });

  test('Enter key posts { type: "action", action: "approve" }', async ({ page }) => {
    await loadWebview(page, createApprovalDialogHtml());

    await page.keyboard.press("Enter");

    const messages = await getPostedMessages(page);
    expect(messages).toContainEqual({ type: "action", action: "approve" });
  });

  test('Escape key posts { type: "action", action: "cancel" }', async ({ page }) => {
    await loadWebview(page, createApprovalDialogHtml());

    await page.keyboard.press("Escape");

    const messages = await getPostedMessages(page);
    expect(messages).toContainEqual({ type: "action", action: "cancel" });
  });
});
