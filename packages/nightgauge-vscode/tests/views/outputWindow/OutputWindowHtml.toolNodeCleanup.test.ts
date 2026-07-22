/**
 * OutputWindowHtml.toolNodeCleanup.test.ts
 *
 * Regression smoke tests for the two webview memory-leak fixes shipped in
 * the "prune tool indicator/summary DOM nodes and cap oversize entry HTML"
 * PR. Because OutputWindowHtml.ts returns an HTML string with inline JS
 * (webview-context), we cannot execute the webview code here — we grep the
 * generated HTML to pin that the new constants and the tool-node removal
 * logic remain in place. Removing or renaming any of these strings should
 * break this test and force a reviewer to re-evaluate the memory impact.
 */

import { describe, it, expect } from "vitest";
import { getOutputWindowHtml } from "../../../src/views/outputWindow/OutputWindowHtml";

const mockWebview = { cspSource: "test-csp" } as any;

function renderHtml(): string {
  return getOutputWindowHtml(mockWebview, [], [], true, false, false);
}

describe("OutputWindowHtml tool-node cleanup + oversize entry cap", () => {
  it("declares MAX_WEBVIEW_ENTRY_BYTES and MAX_WEBVIEW_DETAILS_BYTES constants", () => {
    const html = renderHtml();
    // Both caps should be declared near the top of the inline script so they
    // are in scope inside appendEntry().
    expect(html).toContain("MAX_WEBVIEW_ENTRY_BYTES = 256 * 1024");
    expect(html).toContain("MAX_WEBVIEW_DETAILS_BYTES = 1024 * 1024");
  });

  it("consults the byte caps inside appendEntry before rendering", () => {
    const html = renderHtml();
    // These reference-sites are what actually trigger the placeholder path.
    // If they disappear, oversize entries will once again route through
    // renderContent() and balloon the DOM.
    expect(html).toContain("MAX_WEBVIEW_ENTRY_BYTES");
    expect(html).toContain("MAX_WEBVIEW_DETAILS_BYTES");
    expect(html).toContain("entry-content-oversize");
    expect(html).toContain("Content too large to render inline");
    expect(html).toContain("see the on-disk log file");
  });

  it("prunes .tool-indicator and .tool-summary nodes in collapseStageEntries", () => {
    const html = renderHtml();
    // The collapse handler should remove transient progress nodes on stage
    // completion. We pin both the querySelectorAll shape and the explanatory
    // comment prefix so a refactor cannot silently drop the cleanup.
    expect(html).toContain("details.querySelectorAll('.tool-indicator, .tool-summary')");
    expect(html).toContain("Drop transient progress nodes");
    // And the count chip must be refreshed after removal.
    expect(html).toContain("updateStageGroupCount(details)");
  });
});
