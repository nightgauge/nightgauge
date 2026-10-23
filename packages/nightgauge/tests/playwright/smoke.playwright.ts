import { test, expect } from "@playwright/test";
import { loadWebview, getPostedMessages } from "./helpers/webview-loader.js";

test("webview loader renders HTML and captures postMessage calls", async ({ page }) => {
  const html = `
    <!DOCTYPE html>
    <html>
      <body>
        <script>
          const vscodeApi = acquireVsCodeApi();
          vscodeApi.postMessage({ type: "ready", payload: "hello" });
        </script>
      </body>
    </html>
  `;

  await loadWebview(page, html);

  const messages = await getPostedMessages(page);
  expect(messages).toHaveLength(1);
  expect(messages[0]).toEqual({ type: "ready", payload: "hello" });
});

test("acquireVsCodeApi setState/getState is a real in-memory store, not a no-op stub (#751)", async ({
  page,
}) => {
  // Upgraded from a no-op stub (getState() always returned {}) so
  // DashboardHtml.ts's tab-restoration logic — which reads
  // vscode.getState().activeTab back after vscode.setState() — is
  // actually exercisable under test. See TabActivation.playwright.ts.
  const html = `
    <!DOCTYPE html>
    <html>
      <body>
        <script>
          const vscodeApi = acquireVsCodeApi();
          vscodeApi.setState({ key: "value" });
          window.__state = vscodeApi.getState();
        </script>
      </body>
    </html>
  `;

  await loadWebview(page, html);

  const state = await page.evaluate(() => (window as any).__state);
  expect(state).toEqual({ key: "value" });
});

test("acquireVsCodeApi getState() can be seeded via loadWebview()'s initialState", async ({
  page,
}) => {
  const html = `
    <!DOCTYPE html>
    <html>
      <body>
        <script>
          window.__seededState = acquireVsCodeApi().getState();
        </script>
      </body>
    </html>
  `;

  await loadWebview(page, html, { activeTab: "runs" });

  const state = await page.evaluate(() => (window as any).__seededState);
  expect(state).toEqual({ activeTab: "runs" });
});
