/**
 * OutputWindowSearch.test.ts - Tests for output window search functionality
 *
 * Tests search state management, message handling, and XSS prevention.
 *
 * @see Issue #158 - Add search/filter capability to Nightgauge Output window
 */

import { describe, it, expect, beforeEach } from "vitest";
import { OutputWindowState } from "../../../src/views/outputWindow/OutputWindowState";
import { OutputWindowMessageHandler } from "../../../src/views/outputWindow/OutputWindowMessageHandler";
import { escapeHtml } from "../../../src/views/outputWindow/OutputWindowHtml";

describe("OutputWindow Search (Issue #158)", () => {
  describe("OutputWindowState - Search State", () => {
    let state: OutputWindowState;

    beforeEach(() => {
      state = new OutputWindowState();
    });

    describe("searchText", () => {
      it("should default to empty string", () => {
        expect(state.getSearchText()).toBe("");
      });

      it("should allow setting search text", () => {
        state.setSearchText("error");
        expect(state.getSearchText()).toBe("error");
      });

      it("should handle empty string", () => {
        state.setSearchText("something");
        state.setSearchText("");
        expect(state.getSearchText()).toBe("");
      });

      it("should handle special characters", () => {
        state.setSearchText("test.*pattern");
        expect(state.getSearchText()).toBe("test.*pattern");
      });
    });

    describe("searchCaseSensitive", () => {
      it("should default to false", () => {
        expect(state.getSearchCaseSensitive()).toBe(false);
      });

      it("should allow toggling case sensitivity", () => {
        state.setSearchCaseSensitive(true);
        expect(state.getSearchCaseSensitive()).toBe(true);

        state.setSearchCaseSensitive(false);
        expect(state.getSearchCaseSensitive()).toBe(false);
      });
    });

    describe("searchUseRegex", () => {
      it("should default to false", () => {
        expect(state.getSearchUseRegex()).toBe(false);
      });

      it("should allow toggling regex mode", () => {
        state.setSearchUseRegex(true);
        expect(state.getSearchUseRegex()).toBe(true);

        state.setSearchUseRegex(false);
        expect(state.getSearchUseRegex()).toBe(false);
      });
    });

    describe("clearSearch", () => {
      it("should clear search text", () => {
        state.setSearchText("something");
        state.clearSearch();
        expect(state.getSearchText()).toBe("");
      });

      it("should preserve case sensitive and regex settings", () => {
        state.setSearchCaseSensitive(true);
        state.setSearchUseRegex(true);
        state.setSearchText("test");
        state.clearSearch();

        // clearSearch only clears the text, not the toggle preferences
        expect(state.getSearchCaseSensitive()).toBe(true);
        expect(state.getSearchUseRegex()).toBe(true);
      });
    });

    describe("state persistence across clear()", () => {
      it("should not affect search preferences on clear()", () => {
        // Search preferences are display settings that should persist
        state.setSearchText("test");
        state.setSearchCaseSensitive(true);
        state.setSearchUseRegex(true);

        state.clear();

        // Note: clear() resets entries/tokens but display prefs persist
        // This matches how autoScroll and wordWrap behave
        expect(state.getSearchCaseSensitive()).toBe(true);
        expect(state.getSearchUseRegex()).toBe(true);
      });
    });
  });

  describe("OutputWindowMessageHandler - Search Messages", () => {
    describe("search-text-change message", () => {
      it("should call onSearchTextChange callback with text", () => {
        let receivedText: string | undefined;
        const handler = new OutputWindowMessageHandler({
          onSearchTextChange: (text) => {
            receivedText = text;
          },
        });

        handler.handleMessage({ type: "search-text-change", text: "error" });
        expect(receivedText).toBe("error");
      });

      it("should handle empty text", () => {
        let receivedText: string | undefined;
        const handler = new OutputWindowMessageHandler({
          onSearchTextChange: (text) => {
            receivedText = text;
          },
        });

        handler.handleMessage({ type: "search-text-change", text: "" });
        expect(receivedText).toBe("");
      });

      it("should reject invalid message (non-string text)", () => {
        let callbackCalled = false;
        const handler = new OutputWindowMessageHandler({
          onSearchTextChange: () => {
            callbackCalled = true;
          },
        });

        // @ts-expect-error Testing invalid input
        handler.handleMessage({ type: "search-text-change", text: 123 });
        expect(callbackCalled).toBe(false);
      });

      it("should reject invalid message (missing text)", () => {
        let callbackCalled = false;
        const handler = new OutputWindowMessageHandler({
          onSearchTextChange: () => {
            callbackCalled = true;
          },
        });

        // @ts-expect-error Testing invalid input
        handler.handleMessage({ type: "search-text-change" });
        expect(callbackCalled).toBe(false);
      });
    });

    describe("toggle-search-case-sensitive message", () => {
      it("should call onToggleSearchCaseSensitive callback", () => {
        let receivedEnabled: boolean | undefined;
        const handler = new OutputWindowMessageHandler({
          onToggleSearchCaseSensitive: (enabled) => {
            receivedEnabled = enabled;
          },
        });

        handler.handleMessage({
          type: "toggle-search-case-sensitive",
          enabled: true,
        });
        expect(receivedEnabled).toBe(true);

        handler.handleMessage({
          type: "toggle-search-case-sensitive",
          enabled: false,
        });
        expect(receivedEnabled).toBe(false);
      });

      it("should reject invalid message (non-boolean enabled)", () => {
        let callbackCalled = false;
        const handler = new OutputWindowMessageHandler({
          onToggleSearchCaseSensitive: () => {
            callbackCalled = true;
          },
        });

        // @ts-expect-error Testing invalid input
        handler.handleMessage({
          type: "toggle-search-case-sensitive",
          enabled: "true",
        });
        expect(callbackCalled).toBe(false);
      });
    });

    describe("toggle-search-use-regex message", () => {
      it("should call onToggleSearchUseRegex callback", () => {
        let receivedEnabled: boolean | undefined;
        const handler = new OutputWindowMessageHandler({
          onToggleSearchUseRegex: (enabled) => {
            receivedEnabled = enabled;
          },
        });

        handler.handleMessage({
          type: "toggle-search-use-regex",
          enabled: true,
        });
        expect(receivedEnabled).toBe(true);

        handler.handleMessage({
          type: "toggle-search-use-regex",
          enabled: false,
        });
        expect(receivedEnabled).toBe(false);
      });

      it("should reject invalid message (non-boolean enabled)", () => {
        let callbackCalled = false;
        const handler = new OutputWindowMessageHandler({
          onToggleSearchUseRegex: () => {
            callbackCalled = true;
          },
        });

        // @ts-expect-error Testing invalid input
        handler.handleMessage({
          type: "toggle-search-use-regex",
          enabled: "false",
        });
        expect(callbackCalled).toBe(false);
      });
    });
  });

  describe("XSS Prevention - escapeHtml", () => {
    it("should escape HTML special characters", () => {
      expect(escapeHtml('<script>alert("xss")</script>')).toBe(
        "&lt;script&gt;alert(&quot;xss&quot;)&lt;/script&gt;"
      );
    });

    it("should escape ampersands", () => {
      expect(escapeHtml("foo & bar")).toBe("foo &amp; bar");
    });

    it("should escape single quotes", () => {
      expect(escapeHtml("it's")).toBe("it&#39;s");
    });

    it("should handle search terms with special characters", () => {
      // Search terms that could be XSS payloads
      const maliciousSearch = "<img src=x onerror=alert(1)>";
      const escaped = escapeHtml(maliciousSearch);

      // escapeHtml prevents XSS by escaping angle brackets
      // The browser won't interpret it as HTML because < and > are escaped
      expect(escaped).not.toContain("<img");
      expect(escaped).not.toContain("<");
      expect(escaped).not.toContain(">");
      expect(escaped).toBe("&lt;img src=x onerror=alert(1)&gt;");
    });

    it("should handle regex special characters safely", () => {
      // Regex patterns should be escaped for display, not for matching
      const regexSearch = ".*error.*";
      const escaped = escapeHtml(regexSearch);

      expect(escaped).toBe(".*error.*"); // No HTML entities needed
    });

    it("should handle empty strings", () => {
      expect(escapeHtml("")).toBe("");
    });

    it("should handle strings with no special characters", () => {
      expect(escapeHtml("normal text")).toBe("normal text");
    });
  });

  describe("Search Integration", () => {
    let state: OutputWindowState;

    beforeEach(() => {
      state = new OutputWindowState();
    });

    it("should persist search state with entries", () => {
      // Add some entries
      state.addEntry("Error occurred", "error");
      state.addEntry("Info message", "info");

      // Set search state
      state.setSearchText("error");
      state.setSearchCaseSensitive(false);
      state.setSearchUseRegex(false);

      // Search state should be independent of entries
      expect(state.getSearchText()).toBe("error");
      expect(state.getEntryCount()).toBe(2);
    });

    it("should maintain search state after adding new entries", () => {
      state.setSearchText("test");

      // Add entry after setting search
      state.addEntry("Test message", "info");

      // Search should persist
      expect(state.getSearchText()).toBe("test");
    });
  });
});
