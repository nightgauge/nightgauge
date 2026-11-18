/**
 * Output window escaping and Markdown sanitizing (#3401).
 *
 * The browser half (rendered Markdown passes through DOMPurify) is covered by
 * tests/playwright/outputWindow/OutputWindowSanitize.playwright.ts. This file
 * covers the document the host builds.
 */

import { describe, it, expect } from "vitest";
import {
  getOutputWindowHtml,
  WEBVIEW_VENDOR_SCRIPTS,
} from "../../../src/views/outputWindow/OutputWindowHtml";
import type { OutputEntry, SlotInfo } from "../../../src/views/outputWindow/OutputWindowState";

const webview = { cspSource: "test-csp" } as never;

function slot(title: string): SlotInfo {
  return {
    slotIndex: 0,
    issueNumber: 7,
    title,
    stages: new Map(),
    tokenUsage: {
      inputTokens: 0,
      outputTokens: 0,
      cacheReadTokens: 0,
      cacheCreationTokens: 0,
      costUsd: 0,
    },
  };
}

function render(
  slots: SlotInfo[],
  entries: OutputEntry[] = [],
  scriptUri?: (name: string) => string
): string {
  return getOutputWindowHtml(
    webview,
    entries,
    [],
    true,
    false,
    false,
    7,
    undefined,
    slots,
    0,
    new Map(),
    new Map(),
    scriptUri
  );
}

describe("OutputWindowHtml escaping (#3401)", () => {
  it("escapes the slot title in <title> and in the header", () => {
    const html = render([slot("</title><b>x</b>")]);
    const head = html.slice(0, html.indexOf("</head>"));

    expect(head).toContain(
      "<title>Nightgauge Output — #7 — &lt;/title&gt;&lt;b&gt;x&lt;/b&gt;</title>"
    );
    expect(html).not.toContain("<b>x</b>");
    expect(html).toContain(
      '<span class="output-title">Nightgauge Output — #7 — &lt;/title&gt;&lt;b&gt;x&lt;/b&gt;</span>'
    );
  });

  it("escapes an entry's language hint attribute", () => {
    const entry = {
      id: "e1",
      text: "x",
      level: "info",
      timestamp: new Date(0),
      contentType: "code",
      language: 'ts" data-x="1',
    } as unknown as OutputEntry;
    const html = render([], [entry]);

    expect(html).toContain('data-language="ts&quot; data-x=&quot;1"');
    expect(html).not.toContain('data-x="1"');
  });

  it("renders Markdown only through the sanitizer", () => {
    const html = render([]);

    expect(html).not.toContain("sanitize: false");
    expect(html).toContain("return sanitizeMarkdownHtml(marked.parse(processed));");
    // Both libraries must be present before anything renders as HTML.
    expect(html).toContain(
      "var markdownReady = typeof marked !== 'undefined' && typeof DOMPurify !== 'undefined';"
    );
    expect(html).not.toMatch(/return marked\.parse\(/);
  });

  it("loads marked and DOMPurify from the package, each tagged with the nonce", () => {
    const html = render([], [], (name) => `webview:/ext/dist/vendor/${name}`);
    const nonce = /script-src 'nonce-([^']+)'/.exec(html)![1];

    expect(WEBVIEW_VENDOR_SCRIPTS).toEqual(["marked.umd.js", "purify.min.js"]);
    for (const name of WEBVIEW_VENDOR_SCRIPTS) {
      expect(html).toContain(`<script src="webview:/ext/dist/vendor/${name}" nonce="${nonce}">`);
    }
    // The resource origin authorizes no script; the nonce does.
    expect(html).not.toMatch(/script-src[^;"]*test-csp/);
  });
});
