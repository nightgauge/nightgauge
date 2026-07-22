/**
 * OutputWindowResponsive.test.ts - Tests for responsive header toolbar
 *
 * Tests collapsible search bar, flex-wrap header, and responsive CSS.
 *
 * @see Issue #850 - Output window header toolbar overflows at narrow widths
 */

import { describe, it, expect, vi } from "vitest";
import { getOutputWindowHtml, escapeHtml } from "../../../src/views/outputWindow/OutputWindowHtml";

// Mock vscode module
vi.mock("vscode", () => ({
  Uri: {
    joinPath: vi.fn(),
  },
  window: {
    createOutputChannel: vi.fn(() => ({
      appendLine: vi.fn(),
      show: vi.fn(),
      clear: vi.fn(),
      dispose: vi.fn(),
    })),
  },
}));

// Minimal webview mock for HTML generation
const mockWebview = {
  cspSource: "https://test.vscode-cdn.net",
  asWebviewUri: vi.fn((uri: unknown) => uri),
  options: {},
  html: "",
  onDidReceiveMessage: vi.fn(),
  postMessage: vi.fn(),
} as unknown as import("vscode").Webview;

describe("OutputWindow Responsive Header (Issue #850)", () => {
  describe("Collapsible search container", () => {
    it("should add collapsed class when no search text", () => {
      const html = getOutputWindowHtml(
        mockWebview,
        [],
        [],

        true,
        true,
        true,
        42
      );

      expect(html).toContain("search-container collapsed");
    });

    it("should not add collapsed class when search text is present", () => {
      const html = getOutputWindowHtml(
        mockWebview,
        [],
        [],

        true,
        true,
        true,
        42,
        { searchText: "error", caseSensitive: false, useRegex: false }
      );

      // Should have search-container but NOT search-container collapsed
      expect(html).toContain('id="searchContainer"');
      expect(html).not.toContain("search-container collapsed");
    });

    it("should include search toggle icon button", () => {
      const html = getOutputWindowHtml(mockWebview, [], [], true, true, true);

      expect(html).toContain('id="searchToggleBtn"');
      expect(html).toContain("search-toggle-icon-btn");
      expect(html).toContain('title="Search (Ctrl+F)"');
    });
  });

  describe("Flex-wrap header", () => {
    it("should include flex-wrap CSS for header-actions", () => {
      const html = getOutputWindowHtml(mockWebview, [], [], true, true, true);

      expect(html).toContain("flex-wrap: wrap");
    });

    it("should include row-gap for wrapped rows", () => {
      const html = getOutputWindowHtml(mockWebview, [], [], true, true, true);

      expect(html).toContain("row-gap");
    });
  });

  describe("Title truncation", () => {
    it("should include text-overflow ellipsis for output-title", () => {
      const html = getOutputWindowHtml(mockWebview, [], [], true, true, true);

      expect(html).toContain("text-overflow: ellipsis");
    });

    it("should not have max-width: 250px on output-title (Issue #2813)", () => {
      const html = getOutputWindowHtml(mockWebview, [], [], true, true, true);

      expect(html).not.toContain("max-width: 250px");
    });

    it("should handle long titles with repo slug without breaking layout (Issue #2813)", () => {
      const slot = {
        slotIndex: 0,
        issueNumber: 9999,
        title: "A Very Long Issue Title That Might Cause Wrapping in Narrow Viewports",
        repoSlug: "nightgauge/nightgauge",
      };
      const html = getOutputWindowHtml(
        mockWebview,
        [],
        [],
        true,
        false,
        false,
        undefined,
        undefined,
        [slot],
        0,
        new Map()
      );

      expect(html).not.toContain("max-width: 250px");
      expect(html).toContain("9999");
      expect(html).toContain("A Very Long Issue Title");
      expect(html).toContain("nightgauge/nightgauge");
    });
  });

  describe("Responsive media queries", () => {
    it("should include @media query at 500px", () => {
      const html = getOutputWindowHtml(mockWebview, [], [], true, true, true);

      expect(html).toContain("@media (max-width: 500px)");
    });

    it("should include @media query at 400px", () => {
      const html = getOutputWindowHtml(mockWebview, [], [], true, true, true);

      expect(html).toContain("@media (max-width: 400px)");
    });
  });

  describe("Search expand/collapse JS", () => {
    it("should include expandSearch function in script", () => {
      const html = getOutputWindowHtml(mockWebview, [], [], true, true, true);

      expect(html).toContain("function expandSearch()");
    });

    it("should include collapseSearch function in script", () => {
      const html = getOutputWindowHtml(mockWebview, [], [], true, true, true);

      expect(html).toContain("function collapseSearch()");
    });

    it("should expand search on Ctrl+F", () => {
      const html = getOutputWindowHtml(mockWebview, [], [], true, true, true);

      // Ctrl+F handler should call expandSearch
      expect(html).toContain("expandSearch()");
    });

    it("should collapse search on Escape", () => {
      const html = getOutputWindowHtml(mockWebview, [], [], true, true, true);

      expect(html).toContain("collapseSearch()");
    });
  });

  describe("XSS safety preserved", () => {
    it("should still escape search text in HTML attribute", () => {
      const html = getOutputWindowHtml(
        mockWebview,
        [],
        [],

        true,
        true,
        true,
        42,
        {
          searchText: '<script>alert("xss")</script>',
          caseSensitive: false,
          useRegex: false,
        }
      );

      expect(html).not.toContain("<script>alert");
      expect(html).toContain(escapeHtml('<script>alert("xss")</script>'));
    });
  });
});
