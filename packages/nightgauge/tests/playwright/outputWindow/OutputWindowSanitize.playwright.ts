/**
 * Output window Markdown sanitizing in a real browser (#3401).
 *
 * Renders the real document from getOutputWindowHtml (with its real CSP),
 * loads the same marked and DOMPurify builds the VSIX vendors, streams agent
 * output into it the way the host does, and asserts what reaches the DOM.
 */

import { createRequire } from "node:module";
import { dirname, join } from "node:path";
import { test, expect, type Page } from "@playwright/test";
import { getOutputWindowHtml } from "../../../src/views/outputWindow/OutputWindowHtml.js";
import { dispatchInboundMessage, loadWebview } from "../helpers/webview-loader.js";

const require = createRequire(import.meta.url);
const MARKED = join(dirname(require.resolve("marked/package.json")), "lib", "marked.umd.js");
const PURIFY = require.resolve("dompurify/dist/purify.min.js");

async function openOutputWindow(page: Page, libraries: string[] = [MARKED, PURIFY]): Promise<void> {
  for (const path of libraries) await page.addInitScript({ path });
  const html = getOutputWindowHtml(
    { cspSource: "vscode-webview://test", asWebviewUri: (u: unknown) => u } as never,
    [],
    [],
    true,
    false,
    false
  );
  await loadWebview(page, html);
  await expect(page.locator(".output-content")).toBeVisible();
}

async function append(page: Page, text: string): Promise<void> {
  await dispatchInboundMessage(page, {
    type: "append",
    entry: { id: `e-${text.length}`, text, level: "info", timestamp: Date.now() },
  });
}

test("rendered agent output keeps formatting and loses injected markup", async ({ page }) => {
  await openOutputWindow(page);
  await append(
    page,
    "**bold** and [a link](https://example.com)\n\n" +
      '<img src=x onerror=alert(1)><button class="question-option-btn">x</button>'
  );

  const entry = page.locator(".entry-content").last();
  // Markdown rendered (the sanitizer path is live, not the escaped fallback).
  await expect(entry.locator("strong")).toHaveText("bold");
  await expect(entry.locator('a[href="https://example.com"]')).toHaveCount(1);
  // The raw HTML is shown as text and creates no element.
  await expect(page.locator("img")).toHaveCount(0);
  await expect(page.locator(".question-option-btn")).toHaveCount(0);
  await expect(entry).toContainText('<button class="question-option-btn">x</button>');
});

test("a fenced-code language cannot add a class or attribute", async ({ page }) => {
  await openOutputWindow(page);
  const body = Array.from({ length: 10 }, (_, i) => `line ${i}`).join("\n");
  await append(page, '```js"class="question-option-btn\n' + body + "\n```");

  await expect(page.locator(".question-option-btn")).toHaveCount(0);
  const code = page.locator(".collapsible-code code");
  await expect(code).toHaveAttribute("class", "language-js");

  // The collapse toggle still works on the sanitized markup.
  const container = page.locator(".collapsible-code");
  await expect(container).toHaveClass(/collapsed/);
  await expect(code).toBeHidden();
  await page.locator(".code-toggle").click();
  await expect(container).not.toHaveClass(/collapsed/);
  await expect(code).toBeVisible();
});

test("without the sanitizer, Markdown falls back to escaped text", async ({ page }) => {
  await openOutputWindow(page, [MARKED]);
  await append(page, "**bold** <img src=x onerror=alert(1)>");

  await expect(page.locator("img")).toHaveCount(0);
  await expect(page.locator(".entry-content strong")).toHaveCount(0);
  await expect(page.locator(".entry-content").last()).toContainText("**bold** <img");
});
