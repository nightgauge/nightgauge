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

test("acquireVsCodeApi setState and getState are stubs", async ({ page }) => {
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
  expect(state).toEqual({});
});
