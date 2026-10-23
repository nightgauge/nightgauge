import { describe, it, expect } from "vitest";
import {
  detectContentType,
  detectLanguage,
  shouldCollapse,
  createCollapsibleEntry,
  formatDiff,
  formatJson,
  formatStructuredPatch,
  formatForDisplay,
  CODE_COLLAPSE_THRESHOLD,
  CHAR_COLLAPSE_THRESHOLD,
  type ContentType,
} from "../../../src/views/outputWindow/contentFormatter";

describe("contentFormatter", () => {
  describe("detectContentType", () => {
    it("should return text for empty or whitespace-only input", () => {
      expect(detectContentType("")).toBe("text");
      expect(detectContentType("   ")).toBe("text");
      expect(detectContentType("\n\n")).toBe("text");
    });

    it("should detect unified diff format", () => {
      const unifiedDiff = `--- a/file.ts
+++ b/file.ts
@@ -1,3 +1,4 @@
 const a = 1;
+const b = 2;
 const c = 3;`;

      expect(detectContentType(unifiedDiff)).toBe("diff");
    });

    it("should detect diff with only hunk header and changes", () => {
      const simpleDiff = `@@ -10,5 +10,6 @@
 context line
-removed line
+added line
 more context`;

      expect(detectContentType(simpleDiff)).toBe("diff");
    });

    it("should detect diff based on multiple additions and deletions", () => {
      const changesDiff = `+line added 1
-line removed 1
+line added 2
-line removed 2`;

      expect(detectContentType(changesDiff)).toBe("diff");
    });

    it("should not detect single + or - line as diff", () => {
      expect(detectContentType("+single line")).toBe("text");
      expect(detectContentType("-single line")).toBe("text");
    });

    it("should detect valid JSON objects", () => {
      expect(detectContentType('{"key": "value"}')).toBe("json");
      expect(detectContentType('{ "nested": { "key": 123 } }')).toBe("json");
    });

    it("should detect valid JSON arrays", () => {
      expect(detectContentType("[1, 2, 3]")).toBe("json");
      expect(detectContentType('[{"id": 1}, {"id": 2}]')).toBe("json");
    });

    it("should not detect invalid JSON as json type", () => {
      expect(detectContentType("{invalid json}")).toBe("text");
      expect(detectContentType("[unclosed array")).toBe("text");
    });

    it("should detect structured patch format", () => {
      const structuredPatch = JSON.stringify({
        oldStart: 1,
        newStart: 1,
        hunks: [
          {
            oldStart: 1,
            oldLines: 3,
            newStart: 1,
            newLines: 4,
            lines: [" const a = 1;", "+const b = 2;", " const c = 3;"],
          },
        ],
      });

      expect(detectContentType(structuredPatch)).toBe("structured-patch");
    });

    it("should detect structuredPatch key format", () => {
      const withStructuredPatchKey = JSON.stringify({
        structuredPatch: true,
        changes: [],
      });

      expect(detectContentType(withStructuredPatchKey)).toBe("structured-patch");
    });

    it("should return text for plain text", () => {
      expect(detectContentType("Hello, World!")).toBe("text");
      expect(detectContentType("Some plain text\nwith newlines")).toBe("text");
    });

    it("should handle text with leading/trailing whitespace", () => {
      expect(detectContentType('  {"key": "value"}  ')).toBe("json");
      expect(detectContentType("\n--- a/file\n+++ b/file\n@@ -1 +1 @@\n")).toBe("diff");
    });

    it("should detect TypeScript method body as code", () => {
      const tsCode = `  dispose(): void {
    this.stop();
    this.stopBatch();
    for (const disposable of this.disposables) {
      disposable.dispose();
    }
  }`;
      expect(detectContentType(tsCode)).toBe("code");
    });

    it("should detect TypeScript class as code", () => {
      const tsClass = `export class OutputWindow {
  private panel: vscode.WebviewPanel | undefined;
  private disposables: vscode.Disposable[] = [];

  constructor(private readonly extensionUri: vscode.Uri) {
    this.config = { autoOpen: true };
  }

  show(): void {
    if (this.panel) {
      this.panel.reveal();
      return;
    }
  }
}`;
      expect(detectContentType(tsClass)).toBe("code");
    });

    it("should detect function with arrow functions as code", () => {
      const arrowCode = `const processItems = async (items: string[]) => {
  const results = [];
  for (const item of items) {
    const result = await transform(item);
    results.push(result);
  }
  return results;
};`;
      expect(detectContentType(arrowCode)).toBe("code");
    });

    it("should NOT detect regular prose as code", () => {
      const prose = `This is a paragraph about how the system works.
It has multiple lines and describes the architecture.
The function of the module is to process data.
We use class-based patterns for organization.`;
      expect(detectContentType(prose)).toBe("text");
    });

    it("should NOT detect short 1-2 line snippets as code", () => {
      expect(detectContentType("const x = 1;")).toBe("text");
      expect(detectContentType("function foo() {\n}")).toBe("text");
    });

    it("should NOT detect markdown content as code", () => {
      const markdown = `# Heading

This is a paragraph with some text.
Here is another line of text.
And one more line for good measure.`;
      expect(detectContentType(markdown)).toBe("text");
    });
  });

  describe("detectLanguage", () => {
    it("should detect language from common file extensions", () => {
      expect(detectLanguage("", "file.ts")).toBe("typescript");
      expect(detectLanguage("", "file.tsx")).toBe("typescript");
      expect(detectLanguage("", "file.js")).toBe("javascript");
      expect(detectLanguage("", "file.py")).toBe("python");
      expect(detectLanguage("", "file.go")).toBe("go");
      expect(detectLanguage("", "file.rs")).toBe("rust");
      expect(detectLanguage("", "file.json")).toBe("json");
      expect(detectLanguage("", "file.yaml")).toBe("yaml");
      expect(detectLanguage("", "file.sh")).toBe("bash");
    });

    it("should handle special filenames", () => {
      expect(detectLanguage("", "Dockerfile")).toBe("dockerfile");
      expect(detectLanguage("", "Makefile")).toBe("makefile");
      expect(detectLanguage("", "types.d.ts")).toBe("typescript");
    });

    it("should detect JSON from content", () => {
      expect(detectLanguage('{"key": "value"}')).toBe("json");
      expect(detectLanguage("[1, 2, 3]")).toBe("json");
    });

    it("should detect bash from shebang", () => {
      expect(detectLanguage('#!/bin/bash\necho "hello"')).toBe("bash");
      expect(detectLanguage('#!/bin/sh\necho "hello"')).toBe("bash");
    });

    it("should detect python from shebang", () => {
      expect(detectLanguage('#!/usr/bin/env python\nprint("hello")')).toBe("python");
    });

    it("should return text for unknown content", () => {
      expect(detectLanguage("random text here")).toBe("text");
    });
  });

  describe("shouldCollapse", () => {
    it("should return false for content under threshold", () => {
      const shortContent = "line 1\nline 2\nline 3";
      expect(shouldCollapse(shortContent)).toBe(false);
    });

    it("should return true for content over threshold", () => {
      const lines = Array(60).fill("line").join("\n");
      expect(shouldCollapse(lines)).toBe(true);
    });

    it("should use default threshold of 50 lines", () => {
      const exactly50 = Array(50).fill("line").join("\n");
      const exactly51 = Array(51).fill("line").join("\n");

      expect(shouldCollapse(exactly50)).toBe(false);
      expect(shouldCollapse(exactly51)).toBe(true);
    });

    it("should use custom threshold when provided", () => {
      const content = Array(30).fill("line").join("\n");

      expect(shouldCollapse(content, 50)).toBe(false);
      expect(shouldCollapse(content, 25)).toBe(true);
    });

    it("should handle boundary cases", () => {
      const lines49 = Array(49).fill("line").join("\n");
      const lines50 = Array(50).fill("line").join("\n");
      const lines51 = Array(51).fill("line").join("\n");

      expect(shouldCollapse(lines49)).toBe(false);
      expect(shouldCollapse(lines50)).toBe(false);
      expect(shouldCollapse(lines51)).toBe(true);
    });

    it("should collapse code blocks at CODE_COLLAPSE_THRESHOLD", () => {
      expect(CODE_COLLAPSE_THRESHOLD).toBe(8);

      const shortCode = Array(CODE_COLLAPSE_THRESHOLD).fill("  const x = 1;").join("\n");
      const longCode = Array(CODE_COLLAPSE_THRESHOLD + 1)
        .fill("  const x = 1;")
        .join("\n");

      expect(shouldCollapse(shortCode, CODE_COLLAPSE_THRESHOLD)).toBe(false);
      expect(shouldCollapse(longCode, CODE_COLLAPSE_THRESHOLD)).toBe(true);
    });

    it("should collapse single-line content exceeding character threshold", () => {
      // Simulate a 45KB file arriving as a single line with literal \\n escapes
      const longSingleLine = "x".repeat(CHAR_COLLAPSE_THRESHOLD + 1);
      expect(longSingleLine.split("\n").length).toBe(1); // Confirms single line
      expect(shouldCollapse(longSingleLine)).toBe(true);
    });

    it("should not collapse short content under character threshold", () => {
      const shortContent = "x".repeat(CHAR_COLLAPSE_THRESHOLD - 1);
      expect(shouldCollapse(shortContent)).toBe(false);
    });

    it("should collapse few-line content that exceeds character threshold", () => {
      // 3 very long lines (under 50 line threshold but over char threshold)
      const longLines = Array(3).fill("a".repeat(1000)).join("\n");
      expect(longLines.split("\n").length).toBe(3); // Under 50 lines
      expect(longLines.length).toBeGreaterThan(CHAR_COLLAPSE_THRESHOLD);
      expect(shouldCollapse(longLines)).toBe(true);
    });

    it("should collapse content with literal backslash-n (escaped newlines)", () => {
      // Simulates file content arriving with \\n instead of real newlines
      const escapedContent = Array(200).fill("const x = 1;").join("\\n"); // literal \n characters, not real newlines
      expect(escapedContent.split("\n").length).toBe(1); // It's ONE line
      expect(escapedContent.length).toBeGreaterThan(CHAR_COLLAPSE_THRESHOLD);
      expect(shouldCollapse(escapedContent)).toBe(true);
    });

    it("should export CHAR_COLLAPSE_THRESHOLD as 2000", () => {
      expect(CHAR_COLLAPSE_THRESHOLD).toBe(2000);
    });
  });

  describe("createCollapsibleEntry", () => {
    it("should return original content if under summary line count", () => {
      const shortContent = "line 1\nline 2\nline 3";
      const result = createCollapsibleEntry(shortContent);

      expect(result.summary).toBe(shortContent);
      expect(result.details).toBe(shortContent);
    });

    it("should create summary with default 5 lines", () => {
      const lines = Array(20)
        .fill(0)
        .map((_, i) => `line ${i + 1}`)
        .join("\n");
      const result = createCollapsibleEntry(lines);

      expect(result.summary).toBe("line 1\nline 2\nline 3\nline 4\nline 5\n...");
      expect(result.details).toBe(lines);
    });

    it("should use custom summary line count", () => {
      const lines = Array(20)
        .fill(0)
        .map((_, i) => `line ${i + 1}`)
        .join("\n");
      const result = createCollapsibleEntry(lines, 3);

      expect(result.summary).toBe("line 1\nline 2\nline 3\n...");
      expect(result.details).toBe(lines);
    });

    it("should handle content exactly at limit", () => {
      const lines = Array(5)
        .fill(0)
        .map((_, i) => `line ${i + 1}`)
        .join("\n");
      const result = createCollapsibleEntry(lines, 5);

      expect(result.summary).toBe(lines);
      expect(result.details).toBe(lines);
    });

    it("should create character-based summary for long single-line content", () => {
      // Simulate 45KB file content arriving as single line
      const longContent = "x".repeat(5000);
      const result = createCollapsibleEntry(longContent);

      expect(result.summary).toContain("(4.9KB content)");
      expect(result.summary).toContain("...");
      expect(result.summary.length).toBeLessThan(300); // Summary is compact
      expect(result.details).toBe(longContent); // Full content preserved
    });

    it("should create character-based summary for few long lines over char threshold", () => {
      // 3 lines, each 1000 chars — under 5 line summary but over char limit
      const content = Array(3).fill("a".repeat(1000)).join("\n");
      const result = createCollapsibleEntry(content);

      expect(result.summary).toContain("KB content)");
      expect(result.details).toBe(content);
    });

    it("should prefer line-based summary when content has many lines", () => {
      // 20 lines of moderate length — over line limit, uses line-based summary
      const lines = Array(20)
        .fill(0)
        .map((_, i) => `line ${i + 1}`)
        .join("\n");
      const result = createCollapsibleEntry(lines);

      expect(result.summary).toBe("line 1\nline 2\nline 3\nline 4\nline 5\n...");
    });
  });

  describe("formatDiff", () => {
    it("should wrap lines with appropriate CSS classes", () => {
      const diff = `@@ -1,3 +1,3 @@
 context
-deleted
+added`;

      const result = formatDiff(diff);

      expect(result).toContain('class="diff-container"');
      expect(result).toContain('class="diff-line diff-hunk"');
      expect(result).toContain('class="diff-line diff-context"');
      expect(result).toContain('class="diff-line diff-del"');
      expect(result).toContain('class="diff-line diff-add"');
    });

    it("should handle file headers", () => {
      const diff = `--- a/file.ts
+++ b/file.ts`;

      const result = formatDiff(diff);

      expect(result).toContain('class="diff-line diff-header"');
    });

    it("should escape HTML in diff content", () => {
      const diff = '+const html = "<div>test</div>";';
      const result = formatDiff(diff);

      expect(result).toContain("&lt;div&gt;");
      expect(result).not.toContain("<div>test</div>");
    });

    it("should handle empty lines", () => {
      const diff = "@@ -1 +1 @@\n\n+added";
      const result = formatDiff(diff);

      expect(result).toContain('class="diff-line diff-context"');
    });
  });

  describe("formatJson", () => {
    it("should pretty-print valid JSON", () => {
      const json = '{"a":1,"b":2}';
      const result = formatJson(json);

      expect(result).toContain('class="formatted-json"');
      expect(result).toContain('class="language-json"');
      // Should be indented - quotes are escaped as &quot;
      expect(result).toContain("&quot;a&quot;: 1");
    });

    it("should handle nested JSON", () => {
      const json = '{"outer":{"inner":"value"}}';
      const result = formatJson(json);

      // Quotes are escaped as &quot; in HTML output
      expect(result).toContain("&quot;outer&quot;");
      expect(result).toContain("&quot;inner&quot;");
    });

    it("should handle JSON arrays", () => {
      const json = "[1,2,3]";
      const result = formatJson(json);

      expect(result).toContain('class="formatted-json"');
    });

    it("should escape HTML in JSON values", () => {
      const json = '{"html":"<script>alert(1)</script>"}';
      const result = formatJson(json);

      expect(result).toContain("&lt;script&gt;");
      expect(result).not.toContain("<script>");
    });

    it("should handle invalid JSON gracefully", () => {
      const invalid = "{not valid json}";
      const result = formatJson(invalid);

      // Should still return something wrapped
      expect(result).toContain('class="formatted-json"');
      expect(result).toContain("not valid json");
    });
  });

  describe("formatStructuredPatch", () => {
    it("should convert structured patch to diff format", () => {
      const patch = JSON.stringify({
        oldFileName: "a/file.ts",
        newFileName: "b/file.ts",
        hunks: [
          {
            oldStart: 1,
            oldLines: 2,
            newStart: 1,
            newLines: 3,
            lines: [" const a = 1;", "+const b = 2;", " const c = 3;"],
          },
        ],
      });

      const result = formatStructuredPatch(patch);

      expect(result).toContain('class="diff-container"');
      expect(result).toContain("--- a/file.ts");
      expect(result).toContain("+++ b/file.ts");
      expect(result).toContain("@@ -1,2 +1,3 @@");
    });

    it("should handle patch without file names", () => {
      const patch = JSON.stringify({
        hunks: [
          {
            oldStart: 5,
            oldLines: 1,
            newStart: 5,
            newLines: 1,
            lines: ["-old", "+new"],
          },
        ],
      });

      const result = formatStructuredPatch(patch);

      expect(result).toContain("@@ -5,1 +5,1 @@");
    });

    it("should fall back to JSON format for invalid patch", () => {
      const notAPatch = '{"some": "json"}';
      const result = formatStructuredPatch(notAPatch);

      expect(result).toContain('class="formatted-json"');
    });

    it("should handle malformed JSON", () => {
      const invalid = "{not valid}";
      const result = formatStructuredPatch(invalid);

      expect(result).toContain('class="formatted-json"');
    });
  });

  describe("formatForDisplay", () => {
    it("should auto-detect content type when not provided", () => {
      const jsonResult = formatForDisplay('{"key": "value"}');
      expect(jsonResult.contentType).toBe("json");

      const diffResult = formatForDisplay("@@ -1 +1 @@\n-old\n+new\n+another");
      expect(diffResult.contentType).toBe("diff");
    });

    it("should use provided content type", () => {
      const result = formatForDisplay("plain text", "text");
      expect(result.contentType).toBe("text");
    });

    it("should set shouldCollapse based on content length", () => {
      const shortContent = "short";
      const longContent = Array(100).fill("line").join("\n");

      expect(formatForDisplay(shortContent).shouldCollapse).toBe(false);
      expect(formatForDisplay(longContent).shouldCollapse).toBe(true);
    });

    it("should include summary and details for collapsible content", () => {
      const longContent = Array(100).fill("line").join("\n");
      const result = formatForDisplay(longContent);

      expect(result.shouldCollapse).toBe(true);
      expect(result.summary).toBeDefined();
      expect(result.details).toBeDefined();
      expect(result.summary).toContain("...");
    });

    it("should respect custom collapse threshold", () => {
      const content = Array(30).fill("line").join("\n");

      const defaultResult = formatForDisplay(content);
      expect(defaultResult.shouldCollapse).toBe(false);

      const customResult = formatForDisplay(content, undefined, {
        collapseThreshold: 20,
      });
      expect(customResult.shouldCollapse).toBe(true);
    });

    it("should set language for code content", () => {
      const jsonResult = formatForDisplay('{"key": "value"}');
      expect(jsonResult.language).toBe("json");

      const diffResult = formatForDisplay("@@ -1 +1 @@\n-old\n+new\n+another");
      expect(diffResult.language).toBe("diff");
    });

    it("should return formatted HTML", () => {
      const diffResult = formatForDisplay("@@ -1 +1 @@\n-old\n+new\n+more");
      expect(diffResult.html).toContain('class="diff-container"');

      const jsonResult = formatForDisplay('{"key": "value"}');
      expect(jsonResult.html).toContain('class="formatted-json"');
    });
  });
});
