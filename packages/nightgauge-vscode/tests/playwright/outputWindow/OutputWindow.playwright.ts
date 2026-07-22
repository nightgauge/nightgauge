/**
 * Playwright tests for OutputWindow webview behaviors (Issue #1246)
 *
 * Targets embedded JS behaviors that Vitest cannot reach:
 *   - Real-time output appending via dispatch of { type: "append" } messages
 *   - Clear behavior via { type: "clear" }
 *   - Auto-scroll: content scrolls to bottom on append when enabled
 *   - Auto-scroll toggle button posts { type: "toggle-auto-scroll", enabled: false }
 *   - Question prompt rendering and response posting
 *   - Search bar posting { type: "search-text-change" }
 *
 * Uses the webview-loader helper from Issue #1243 to inject the acquireVsCodeApi() mock
 * before page scripts execute, capturing all postMessage calls in window.__vscodeMessages[].
 */

import { test, expect } from "@playwright/test";
import { loadWebview, getPostedMessages } from "../helpers/webview-loader.js";

/**
 * Minimal OutputWindow HTML fixture mirroring the script logic from OutputWindowHtml.ts
 * getScript(). Tests validate the behavior contract without importing the TS module
 * directly (which depends on the vscode API).
 */
function createOutputWindowHtml({
  autoScrollEnabled = true,
}: { autoScrollEnabled?: boolean } = {}): string {
  return `<!DOCTYPE html>
<html lang="en">
<head><meta charset="UTF-8"></head>
<body>
  <!-- Search bar -->
  <div class="search-bar">
    <input type="text" class="search-input" id="searchInput" placeholder="Search..." />
  </div>

  <!-- Toolbar -->
  <button class="action-btn auto-scroll-indicator ${autoScrollEnabled ? "enabled" : ""}" id="autoScrollBtn">
    ${autoScrollEnabled ? "Auto-scroll: ON" : "Auto-scroll: OFF"}
  </button>

  <!-- Main output container -->
  <main class="output-content" id="outputContent" style="height: 100px; overflow-y: auto;">
    <div class="empty-state"><p>No output yet.</p></div>
  </main>

  <script>
    // -----------------------------------------------------------------------
    // OutputWindow script (mirrors OutputWindowHtml.ts getScript() behaviors)
    // -----------------------------------------------------------------------
    (function() {
      const vscode = acquireVsCodeApi();
      let autoScroll = ${autoScrollEnabled ? "true" : "false"};

      const outputContent = document.getElementById('outputContent');
      const autoScrollBtn = document.getElementById('autoScrollBtn');
      const searchInput = document.getElementById('searchInput');

      // --- Auto-scroll toggle ---
      autoScrollBtn?.addEventListener('click', () => {
        autoScroll = !autoScroll;
        autoScrollBtn.classList.toggle('enabled', autoScroll);
        autoScrollBtn.textContent = autoScroll ? 'Auto-scroll: ON' : 'Auto-scroll: OFF';
        vscode.postMessage({ type: 'toggle-auto-scroll', enabled: autoScroll });
      });

      // --- Search input ---
      const SEARCH_DEBOUNCE_MS = 150;
      let _searchTimer = null;
      searchInput?.addEventListener('input', (e) => {
        clearTimeout(_searchTimer);
        _searchTimer = setTimeout(() => {
          vscode.postMessage({ type: 'search-text-change', text: e.target.value });
        }, SEARCH_DEBOUNCE_MS);
      });

      // --- Helper functions ---
      function escapeHtml(text) {
        const div = document.createElement('div');
        div.textContent = text;
        return div.innerHTML;
      }

      function appendEntry(entry) {
        if (!outputContent) return;
        const emptyState = outputContent.querySelector('.empty-state');
        if (emptyState) emptyState.remove();

        const entryEl = document.createElement('div');
        entryEl.className = 'output-entry';
        entryEl.dataset.entryId = entry.id;
        entryEl.innerHTML = '<div class="entry-content">' + escapeHtml(entry.text) + '</div>';
        outputContent.appendChild(entryEl);

        if (autoScroll) {
          outputContent.scrollTop = outputContent.scrollHeight;
        }
      }

      function appendQuestionPrompt(question) {
        if (!outputContent) return;
        const emptyState = outputContent.querySelector('.empty-state');
        if (emptyState) emptyState.remove();

        const promptEl = document.createElement('div');
        promptEl.className = 'question-prompt waiting';
        promptEl.dataset.questionId = question.id;

        let html = '';
        question.questions.forEach((q, qIndex) => {
          html += '<div class="question-item" data-question-index="' + qIndex + '">';
          html += '<div class="question-text">' + escapeHtml(q.question) + '</div>';
          html += '<div class="question-options">';
          q.options.forEach((opt, optIndex) => {
            html += '<button class="question-option-btn" ';
            html += 'data-question-id="' + question.id + '" ';
            html += 'data-question-index="' + qIndex + '" ';
            html += 'data-option-index="' + optIndex + '" ';
            html += 'data-option-label="' + escapeHtml(opt.label) + '">';
            html += escapeHtml(opt.label) + '</button>';
          });
          html += '</div>';
          html += '<div class="question-custom-input">';
          html += '<input type="text" class="question-text-input" placeholder="Or type your own answer..." ';
          html += 'data-question-id="' + question.id + '" data-question-index="' + qIndex + '" />';
          html += '</div>';
          html += '</div>';
        });

        html += '<div class="question-actions">';
        html += '<button class="question-submit-btn" data-question-id="' + question.id + '">Submit</button>';
        html += '<button class="question-cancel-btn" data-question-id="' + question.id + '">Skip</button>';
        html += '</div>';

        promptEl.innerHTML = html;
        outputContent.appendChild(promptEl);

        if (autoScroll) {
          outputContent.scrollTop = outputContent.scrollHeight;
        }

        // Option click — select and enable submit
        promptEl.querySelectorAll('.question-option-btn').forEach(btn => {
          btn.addEventListener('click', () => {
            const qId = btn.dataset.questionId;
            const qIndex = parseInt(btn.dataset.questionIndex, 10);
            const opts = promptEl.querySelectorAll('[data-question-index="' + qIndex + '"].question-option-btn');
            opts.forEach(b => b.classList.remove('selected'));
            btn.classList.add('selected');
            // Enable submit
            const submitBtn = promptEl.querySelector('.question-submit-btn');
            if (submitBtn) submitBtn.disabled = false;
          });
        });

        // Custom text input — enable submit when non-empty
        promptEl.querySelectorAll('.question-text-input').forEach(input => {
          input.addEventListener('input', () => {
            const submitBtn = promptEl.querySelector('.question-submit-btn');
            if (submitBtn) submitBtn.disabled = input.value.trim() === '';
          });
        });

        // Submit button
        const submitBtn = promptEl.querySelector('.question-submit-btn');
        submitBtn?.addEventListener('click', () => {
          const qId = submitBtn.dataset.questionId;
          const answers = [];
          promptEl.querySelectorAll('.question-item').forEach((item, idx) => {
            const selected = item.querySelector('.question-option-btn.selected');
            const customInput = item.querySelector('.question-text-input');
            const answer = selected ? selected.dataset.optionLabel : (customInput ? customInput.value.trim() : '');
            answers.push(answer);
          });
          vscode.postMessage({
            type: 'question-response',
            questionId: qId,
            response: { answers: answers }
          });
          promptEl.classList.remove('waiting');
          promptEl.classList.add('answered');
        });

        // Cancel button
        const cancelBtn = promptEl.querySelector('.question-cancel-btn');
        cancelBtn?.addEventListener('click', () => {
          const qId = cancelBtn.dataset.questionId;
          vscode.postMessage({
            type: 'question-response',
            questionId: qId,
            response: null
          });
          promptEl.classList.remove('waiting');
          promptEl.classList.add('cancelled');
        });
      }

      // --- Message handler ---
      window.addEventListener('message', event => {
        const message = event.data;
        switch (message.type) {
          case 'append':
            appendEntry(message.entry);
            break;
          case 'clear':
            if (outputContent) {
              outputContent.innerHTML = '<div class="empty-state"><p>Output cleared.</p></div>';
            }
            break;
          case 'question-prompt':
            appendQuestionPrompt(message.question);
            break;
          case 'set-auto-scroll':
            autoScroll = message.enabled;
            if (autoScrollBtn) {
              autoScrollBtn.classList.toggle('enabled', autoScroll);
              autoScrollBtn.textContent = autoScroll ? 'Auto-scroll: ON' : 'Auto-scroll: OFF';
            }
            break;
        }
      });

      // Initialize auto-scroll
      if (autoScroll && outputContent) {
        outputContent.scrollTop = outputContent.scrollHeight;
      }
    })();
  </script>
</body>
</html>`;
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

test.describe("OutputWindow webview", () => {
  test('dispatching { type: "append" } renders entry text in output container', async ({
    page,
  }) => {
    await loadWebview(page, createOutputWindowHtml());

    await page.evaluate(() => {
      window.dispatchEvent(
        new MessageEvent("message", {
          data: {
            type: "append",
            entry: {
              id: "e1",
              text: "hello",
              level: "info",
              timestamp: new Date().toISOString(),
            },
          },
        })
      );
    });

    const text = await page.textContent("#outputContent");
    expect(text).toContain("hello");
  });

  test('dispatching { type: "clear" } empties the output container', async ({ page }) => {
    await loadWebview(page, createOutputWindowHtml());

    // First append some content
    await page.evaluate(() => {
      window.dispatchEvent(
        new MessageEvent("message", {
          data: {
            type: "append",
            entry: {
              id: "e1",
              text: "some content",
              level: "info",
              timestamp: new Date().toISOString(),
            },
          },
        })
      );
    });

    // Then clear
    await page.evaluate(() => {
      window.dispatchEvent(new MessageEvent("message", { data: { type: "clear" } }));
    });

    const text = await page.textContent("#outputContent");
    expect(text).not.toContain("some content");
    // Should show cleared state
    expect(text).toContain("Output cleared");
  });

  test("auto-scroll: output container scrolls to bottom when new content appended (auto-scroll enabled)", async ({
    page,
  }) => {
    await loadWebview(page, createOutputWindowHtml({ autoScrollEnabled: true }));

    // Set a small fixed height so the container is scrollable
    await page.evaluate(() => {
      const oc = document.getElementById("outputContent") as HTMLElement;
      if (oc) {
        oc.style.height = "80px";
        oc.style.overflowY = "auto";
      }
    });

    // Append enough entries to force scrolling
    for (let i = 0; i < 20; i++) {
      await page.evaluate((idx) => {
        window.dispatchEvent(
          new MessageEvent("message", {
            data: {
              type: "append",
              entry: {
                id: `e${idx}`,
                text: `Line ${idx}`,
                level: "info",
                timestamp: new Date().toISOString(),
              },
            },
          })
        );
      }, i);
    }

    const atBottom = await page.evaluate(() => {
      const oc = document.getElementById("outputContent") as HTMLElement;
      if (!oc) return false;
      return oc.scrollTop + oc.clientHeight >= oc.scrollHeight - 5;
    });

    expect(atBottom).toBe(true);
  });

  test('auto-scroll toggle button posts { type: "toggle-auto-scroll", enabled: false } when clicked (starts ON)', async ({
    page,
  }) => {
    await loadWebview(page, createOutputWindowHtml({ autoScrollEnabled: true }));

    await page.click("#autoScrollBtn");

    const messages = await getPostedMessages(page);
    expect(messages).toContainEqual({
      type: "toggle-auto-scroll",
      enabled: false,
    });
  });

  test("auto-scroll toggle posts enabled: true when toggled back ON", async ({ page }) => {
    await loadWebview(page, createOutputWindowHtml({ autoScrollEnabled: true }));

    // Click once (OFF), then again (ON)
    await page.click("#autoScrollBtn");
    await page.click("#autoScrollBtn");

    const messages = await getPostedMessages(page);
    expect(messages).toContainEqual({
      type: "toggle-auto-scroll",
      enabled: true,
    });
  });

  test('{ type: "question-prompt" } renders a visible input prompt', async ({ page }) => {
    await loadWebview(page, createOutputWindowHtml());

    await page.evaluate(() => {
      window.dispatchEvent(
        new MessageEvent("message", {
          data: {
            type: "question-prompt",
            question: {
              id: "q1",
              questions: [
                {
                  question: "Continue?",
                  header: "Confirmation",
                  options: [{ label: "Yes" }, { label: "No" }],
                  multiSelect: false,
                },
              ],
            },
          },
        })
      );
    });

    // Prompt element is visible
    const promptVisible = await page.isVisible(".question-prompt");
    expect(promptVisible).toBe(true);

    // Question text is rendered
    const text = await page.textContent(".question-prompt");
    expect(text).toContain("Continue?");
  });

  test('selecting an option and submitting question prompt posts { type: "question-response" }', async ({
    page,
  }) => {
    await loadWebview(page, createOutputWindowHtml());

    await page.evaluate(() => {
      window.dispatchEvent(
        new MessageEvent("message", {
          data: {
            type: "question-prompt",
            question: {
              id: "q1",
              questions: [
                {
                  question: "Continue?",
                  header: "Confirmation",
                  options: [{ label: "Yes" }, { label: "No" }],
                  multiSelect: false,
                },
              ],
            },
          },
        })
      );
    });

    // Select "Yes" option
    await page.click('.question-option-btn[data-option-label="Yes"]');
    // Click submit
    await page.click(".question-submit-btn");

    const messages = await getPostedMessages(page);
    const responseMsg = messages.find((m: any) => m.type === "question-response") as any;
    expect(responseMsg).toBeDefined();
    expect(responseMsg.questionId).toBe("q1");
    expect(responseMsg.response.answers).toContain("Yes");
  });

  test('typing in search input posts { type: "search-text-change", text: <query> }', async ({
    page,
  }) => {
    await loadWebview(page, createOutputWindowHtml());

    await page.fill("#searchInput", "hello world");

    // Wait for debounce (150ms) plus margin
    await page.waitForTimeout(300);

    const messages = await getPostedMessages(page);
    expect(messages).toContainEqual({
      type: "search-text-change",
      text: "hello world",
    });
  });
});
