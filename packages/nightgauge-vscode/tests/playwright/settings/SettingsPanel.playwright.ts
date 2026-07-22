/**
 * Playwright tests for SettingsPanel webview behaviors (Issue #1245)
 *
 * Targets embedded JS behaviors that Vitest cannot reach:
 *   - Text/select/toggle input change → vscode.postMessage({ type: 'change', path, value })
 *   - Save/Reset button → { type: 'save' } / { type: 'reset' }
 *   - Tier tab click → { type: 'switch-tier', tier }
 *   - Locked section — inputs within a locked section are disabled
 *   - List add button → { type: 'list-add', path, value }
 *   - List remove button → { type: 'list-remove', path, index }
 *   - "Open in Editor" button → { type: 'open-tier-file', tier }
 *
 * Uses the webview-loader helper from Issue #1243 to inject the acquireVsCodeApi() mock
 * before page scripts execute, capturing all postMessage calls in window.__vscodeMessages[].
 */

import { test, expect } from "@playwright/test";
import { loadWebview, getPostedMessages } from "../helpers/webview-loader.js";

/**
 * Minimal SettingsPanel HTML fixture containing the interactive elements and
 * script behaviors under test. Mirrors the script logic from SettingsHtml.ts
 * getScript() so tests validate the behavior contract.
 */
function createSettingsHtml(options: { locked?: boolean } = {}): string {
  const locked = options.locked ?? false;

  return `<!DOCTYPE html>
<html lang="en">
<head><meta charset="UTF-8"></head>
<body>

  <!-- Header actions -->
  <div class="settings-container">
    <header>
      <button id="resetBtn">Reset</button>
      <button id="saveBtn">Save</button>
    </header>

    <!-- Tier tabs -->
    <div class="tier-tabs">
      <button class="tier-tab active" data-tier="merged">Merged</button>
      <button class="tier-tab" data-tier="project">Project</button>
      <button class="tier-tab" data-tier="local">Local</button>
      <button class="tier-tab" data-tier="global">Global</button>
    </div>

    <!-- Tier info / Open in Editor -->
    <div class="tier-info-actions">
      <button class="tier-info-action" data-action="open-tier-file" data-tier="project">
        Open in Editor
      </button>
    </div>

    <!-- Normal (unlocked) section with text input -->
    <div class="section" id="section-branch">
      <div class="section-content">
        <div class="setting-row">
          <div class="setting-control">
            <input type="text"
                   id="branch.base"
                   class="text-input"
                   data-path="branch.base"
                   value="main">
          </div>
        </div>

        <!-- Select input -->
        <div class="setting-row">
          <div class="setting-control">
            <select id="pull_request.merge_strategy"
                    class="select-input"
                    data-path="pull_request.merge_strategy">
              <option value="squash" selected>Squash and merge</option>
              <option value="merge">Create a merge commit</option>
              <option value="rebase">Rebase and merge</option>
            </select>
          </div>
        </div>

        <!-- Toggle (checkbox) input -->
        <div class="setting-row">
          <div class="setting-control">
            <input type="checkbox"
                   id="branch.suggestions"
                   class="toggle-input"
                   data-path="branch.suggestions"
                   checked>
          </div>
        </div>

        <!-- List field: pull_request.reviewers -->
        <div class="setting-row setting-row-list">
          <div class="setting-control list-control">
            <div class="list-items" id="pull_request.reviewers-items">
              <div class="list-item" data-index="0">
                <span class="list-item-text">alice</span>
                <button class="list-item-remove"
                        data-path="pull_request.reviewers"
                        data-index="0">×</button>
              </div>
            </div>
            <div class="list-add">
              <input type="text"
                     id="pull_request.reviewers-input"
                     class="list-input"
                     placeholder="username">
              <button class="list-add-btn" data-path="pull_request.reviewers">+</button>
            </div>
          </div>
        </div>
      </div>
    </div>

    <!-- Locked section (pipeline running) -->
    <div class="section ${locked ? "section-locked" : ""}" id="section-pipeline">
      <div class="section-content">
        <div class="setting-row">
          <div class="setting-control">
            <input type="text"
                   id="pipeline.model"
                   class="text-input"
                   data-path="pipeline.model"
                   value="claude-sonnet-4-6"
                   ${locked ? "disabled" : ""}>
          </div>
        </div>
        <div class="setting-row">
          <div class="setting-control">
            <input type="checkbox"
                   id="pipeline.auto_fix"
                   class="toggle-input"
                   data-path="pipeline.auto_fix"
                   checked
                   ${locked ? "disabled" : ""}>
          </div>
        </div>
        <div class="setting-row">
          <div class="setting-control">
            <select id="pipeline.stage"
                    class="select-input"
                    data-path="pipeline.stage"
                    ${locked ? "disabled" : ""}>
              <option value="dev" selected>Dev</option>
            </select>
          </div>
        </div>
        <div class="setting-row setting-row-list">
          <div class="setting-control list-control">
            <div class="list-add">
              <input type="text"
                     id="pipeline.skip_stages-input"
                     class="list-input"
                     placeholder="stage"
                     ${locked ? "disabled" : ""}>
              <button class="list-add-btn"
                      data-path="pipeline.skip_stages"
                      ${locked ? "disabled" : ""}>+</button>
            </div>
          </div>
        </div>
      </div>
    </div>
  </div>

  <script>
    // -----------------------------------------------------------------------
    // SettingsPanel script (mirrors SettingsHtml.ts getScript())
    // -----------------------------------------------------------------------
    (function() {
      const vscode = acquireVsCodeApi();
      let modified = false;

      const state = vscode.getState() || {};

      // Handle input changes
      function handleChange(element) {
        const path = element.dataset.path;
        if (!path) return;

        let value;
        if (element.type === 'checkbox') {
          value = element.checked;
        } else if (element.type === 'number') {
          value = element.value === '' ? undefined : Number(element.value);
        } else {
          value = element.value;
        }

        setModified(true);
        vscode.postMessage({ type: 'change', path, value });
      }

      // Toggle inputs
      document.querySelectorAll('.toggle-input').forEach(function(input) {
        input.addEventListener('change', function() { handleChange(input); });
      });

      // Text inputs
      document.querySelectorAll('.text-input, .number-input').forEach(function(input) {
        input.addEventListener('change', function() { handleChange(input); });
      });

      // Select inputs
      document.querySelectorAll('.select-input').forEach(function(select) {
        select.addEventListener('change', function() { handleChange(select); });
      });

      // List add buttons
      document.querySelectorAll('.list-add-btn').forEach(function(btn) {
        btn.addEventListener('click', function() {
          var path = btn.dataset.path;
          var input = document.getElementById(path + '-input');
          if (input && input.value.trim()) {
            vscode.postMessage({ type: 'list-add', path: path, value: input.value.trim() });
            input.value = '';
            setModified(true);
          }
        });
      });

      // List input enter key
      document.querySelectorAll('.list-input').forEach(function(input) {
        input.addEventListener('keydown', function(e) {
          if (e.key === 'Enter' && input.value.trim()) {
            var path = input.id.replace('-input', '');
            vscode.postMessage({ type: 'list-add', path: path, value: input.value.trim() });
            input.value = '';
            setModified(true);
          }
        });
      });

      // List remove buttons
      document.querySelectorAll('.list-item-remove').forEach(function(btn) {
        btn.addEventListener('click', function() {
          var path = btn.dataset.path;
          var index = parseInt(btn.dataset.index, 10);
          vscode.postMessage({ type: 'list-remove', path: path, index: index });
          setModified(true);
        });
      });

      // Save button
      document.getElementById('saveBtn') && document.getElementById('saveBtn').addEventListener('click', function() {
        vscode.postMessage({ type: 'save' });
      });

      // Reset button
      document.getElementById('resetBtn') && document.getElementById('resetBtn').addEventListener('click', function() {
        vscode.postMessage({ type: 'reset' });
      });

      // Tier tabs
      document.querySelectorAll('.tier-tab').forEach(function(tab) {
        tab.addEventListener('click', function() {
          if (tab.disabled) return;
          var tier = tab.dataset.tier;
          vscode.postMessage({ type: 'switch-tier', tier: tier });
        });
      });

      // Tier info actions (Open in Editor)
      document.querySelectorAll('.tier-info-action').forEach(function(btn) {
        btn.addEventListener('click', function() {
          var action = btn.dataset.action;
          var tier = btn.dataset.tier;
          if (action === 'open-tier-file' && tier) {
            vscode.postMessage({ type: 'open-tier-file', tier: tier });
          }
        });
      });

      function setModified(value) {
        modified = value;
        document.querySelector('.settings-container') &&
          document.querySelector('.settings-container').classList.toggle('modified', value);
      }
    })();
  </script>

</body>
</html>`;
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

test.describe("SettingsPanel webview", () => {
  test('Text input change posts { type: "change", path, value }', async ({ page }) => {
    await loadWebview(page, createSettingsHtml());
    await page.fill("#branch\\.base", "develop");
    // Trigger change event (fill doesn't fire 'change' — dispatch it)
    await page.dispatchEvent("#branch\\.base", "change");
    const messages = await getPostedMessages(page);
    expect(messages).toContainEqual({
      type: "change",
      path: "branch.base",
      value: "develop",
    });
  });

  test('Select input change posts { type: "change", path, value }', async ({ page }) => {
    await loadWebview(page, createSettingsHtml());
    await page.selectOption("#pull_request\\.merge_strategy", "merge");
    const messages = await getPostedMessages(page);
    expect(messages).toContainEqual({
      type: "change",
      path: "pull_request.merge_strategy",
      value: "merge",
    });
  });

  test('Toggle (checkbox) change posts { type: "change", path, value: boolean }', async ({
    page,
  }) => {
    await loadWebview(page, createSettingsHtml());
    await page.click("#branch\\.suggestions");
    const messages = await getPostedMessages(page);
    expect(messages).toContainEqual({
      type: "change",
      path: "branch.suggestions",
      value: false,
    });
  });

  test('Save button posts { type: "save" }', async ({ page }) => {
    await loadWebview(page, createSettingsHtml());
    await page.click("#saveBtn");
    const messages = await getPostedMessages(page);
    expect(messages).toContainEqual({ type: "save" });
  });

  test('Reset button posts { type: "reset" }', async ({ page }) => {
    await loadWebview(page, createSettingsHtml());
    await page.click("#resetBtn");
    const messages = await getPostedMessages(page);
    expect(messages).toContainEqual({ type: "reset" });
  });

  test('Tier tab click posts { type: "switch-tier", tier }', async ({ page }) => {
    await loadWebview(page, createSettingsHtml());
    await page.click('.tier-tab[data-tier="project"]');
    const messages = await getPostedMessages(page);
    expect(messages).toContainEqual({ type: "switch-tier", tier: "project" });
  });

  test('Tier tab click — global tier posts { type: "switch-tier", tier: "global" }', async ({
    page,
  }) => {
    await loadWebview(page, createSettingsHtml());
    await page.click('.tier-tab[data-tier="global"]');
    const messages = await getPostedMessages(page);
    expect(messages).toContainEqual({ type: "switch-tier", tier: "global" });
  });

  test('Local tier tab posts { type: "switch-tier", tier: "local" }', async ({ page }) => {
    await loadWebview(page, createSettingsHtml());
    await page.click('.tier-tab[data-tier="local"]');
    const messages = await getPostedMessages(page);
    expect(messages).toContainEqual({ type: "switch-tier", tier: "local" });
  });

  test("Locked section — all inputs within a locked section are disabled", async ({ page }) => {
    await loadWebview(page, createSettingsHtml({ locked: true }));

    // The section-locked div should exist
    const lockedSection = page.locator("#section-pipeline.section-locked");
    await expect(lockedSection).toBeVisible();

    // Text input (text-input class) inside locked section is disabled
    const textInput = page.locator("#section-pipeline .text-input");
    await expect(textInput).toBeDisabled();

    // Checkbox inside locked section is disabled
    const checkbox = page.locator("#section-pipeline .toggle-input");
    await expect(checkbox).toBeDisabled();

    // Select inside locked section is disabled
    const select = page.locator("#section-pipeline .select-input");
    await expect(select).toBeDisabled();

    // List add button inside locked section is disabled
    const listAddBtn = page.locator("#section-pipeline .list-add-btn");
    await expect(listAddBtn).toBeDisabled();

    // List input inside locked section is disabled
    const listInput = page.locator("#section-pipeline .list-input");
    await expect(listInput).toBeDisabled();
  });

  test("Locked section — inputs in non-locked section remain enabled", async ({ page }) => {
    await loadWebview(page, createSettingsHtml({ locked: true }));
    const textInput = page.locator("#section-branch .text-input");
    await expect(textInput).not.toBeDisabled();
  });

  test('List add button posts { type: "list-add", path, value }', async ({ page }) => {
    await loadWebview(page, createSettingsHtml());
    await page.fill("#pull_request\\.reviewers-input", "bob");
    await page.click('.list-add-btn[data-path="pull_request.reviewers"]');
    const messages = await getPostedMessages(page);
    expect(messages).toContainEqual({
      type: "list-add",
      path: "pull_request.reviewers",
      value: "bob",
    });
  });

  test('List add via Enter key posts { type: "list-add", path, value }', async ({ page }) => {
    await loadWebview(page, createSettingsHtml());
    await page.fill("#pull_request\\.reviewers-input", "carol");
    await page.press("#pull_request\\.reviewers-input", "Enter");
    const messages = await getPostedMessages(page);
    expect(messages).toContainEqual({
      type: "list-add",
      path: "pull_request.reviewers",
      value: "carol",
    });
  });

  test('List remove button posts { type: "list-remove", path, index }', async ({ page }) => {
    await loadWebview(page, createSettingsHtml());
    await page.click('.list-item-remove[data-path="pull_request.reviewers"][data-index="0"]');
    const messages = await getPostedMessages(page);
    expect(messages).toContainEqual({
      type: "list-remove",
      path: "pull_request.reviewers",
      index: 0,
    });
  });

  test('"Open in Editor" button posts { type: "open-tier-file", tier }', async ({ page }) => {
    await loadWebview(page, createSettingsHtml());
    await page.click('.tier-info-action[data-action="open-tier-file"]');
    const messages = await getPostedMessages(page);
    expect(messages).toContainEqual({
      type: "open-tier-file",
      tier: "project",
    });
  });
});
