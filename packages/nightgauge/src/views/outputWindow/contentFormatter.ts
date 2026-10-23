/**
 * Content Formatter - Detects and formats output content types
 *
 * Handles detection and formatting of:
 * - Unified diffs (git diff format)
 * - JSON objects and arrays
 * - Structured patches (from Edit/Write tool results)
 * - Code blocks with language detection
 *
 * @see Issue #428 - Format raw tool output in Nightgauge Output view
 * @see docs/ARCHITECTURE.md for WebView patterns
 */

/**
 * Content types that can be detected and formatted
 */
export type ContentType = "text" | "diff" | "json" | "code" | "structured-patch";

/**
 * Options for content formatting
 */
export interface FormatOptions {
  /** Number of lines before content is collapsed (default: 50) */
  collapseThreshold?: number;
  /** Number of summary lines to show when collapsed (default: 5) */
  summaryLines?: number;
  /** Maximum line length before truncation (default: 500) */
  maxLineLength?: number;
}

/**
 * Result of formatting content for display
 */
export interface FormattedContent {
  /** The formatted HTML content */
  html: string;
  /** Detected content type */
  contentType: ContentType;
  /** Detected or inferred language (for code blocks) */
  language?: string;
  /** Whether content should be collapsed */
  shouldCollapse: boolean;
  /** Summary content if collapsed */
  summary?: string;
  /** Full content for expansion */
  details?: string;
}

/**
 * Structured patch format from Edit/Write tool results
 */
interface StructuredPatch {
  oldFileName?: string;
  newFileName?: string;
  oldStart?: number;
  newStart?: number;
  hunks?: Array<{
    oldStart: number;
    oldLines: number;
    newStart: number;
    newLines: number;
    lines: string[];
  }>;
}

/**
 * Collapse threshold for code blocks (lines)
 * Lower than the general threshold to keep the output log clean.
 * @see Issue #639 - Dashboard data population audit
 */
export const CODE_COLLAPSE_THRESHOLD = 8;

/**
 * Character count threshold for collapsing content.
 * Content exceeding this length is collapsed regardless of line count.
 * This catches single-line or few-line content that is still very large
 * (e.g., file contents arriving with literal \n escapes instead of real newlines).
 */
export const CHAR_COLLAPSE_THRESHOLD = 2000;

/**
 * Default formatting options
 */
const DEFAULT_OPTIONS: Required<FormatOptions> = {
  collapseThreshold: 50,
  summaryLines: 5,
  maxLineLength: 500,
};

/**
 * Detect the content type of text
 *
 * Detection priority:
 * 1. Structured patch (JSON with specific fields)
 * 2. Unified diff (lines starting with @@, +, -)
 * 3. JSON (valid JSON starting with { or [)
 * 4. Default to text
 *
 * @param text - The text to analyze
 * @returns Detected content type
 */
export function detectContentType(text: string): ContentType {
  if (!text || text.trim().length === 0) {
    return "text";
  }

  const trimmed = text.trim();

  // Check for structured patch (JSON with patch-specific fields)
  if (isStructuredPatch(trimmed)) {
    return "structured-patch";
  }

  // Check for unified diff
  if (isDiff(trimmed)) {
    return "diff";
  }

  // Check for JSON
  if (isJson(trimmed)) {
    return "json";
  }

  // Check for code blocks (Issue #639)
  if (isCode(trimmed)) {
    return "code";
  }

  return "text";
}

/**
 * Check if text is a unified diff
 *
 * Unified diff patterns:
 * - Lines starting with @@ (hunk headers)
 * - Lines starting with + or - followed by content (not +++ or ---)
 * - File headers starting with --- or +++
 */
function isDiff(text: string): boolean {
  const lines = text.split("\n");

  // Must have at least 2 lines to be a meaningful diff
  if (lines.length < 2) {
    return false;
  }

  // Count diff indicators
  let hunkHeaders = 0;
  let additions = 0;
  let deletions = 0;
  let fileHeaders = 0;

  for (const line of lines) {
    if (line.startsWith("@@") && line.includes("@@")) {
      hunkHeaders++;
    } else if (line.startsWith("--- ") || line.startsWith("+++ ")) {
      fileHeaders++;
    } else if (line.startsWith("+") && !line.startsWith("+++")) {
      additions++;
    } else if (line.startsWith("-") && !line.startsWith("---")) {
      deletions++;
    }
  }

  // Consider it a diff if:
  // - Has at least one hunk header, OR
  // - Has file headers and changes, OR
  // - Has multiple additions/deletions in a diff-like pattern
  return (
    hunkHeaders > 0 ||
    (fileHeaders >= 2 && (additions > 0 || deletions > 0)) ||
    (additions >= 2 && deletions >= 2)
  );
}

/**
 * Check if text is valid JSON
 */
function isJson(text: string): boolean {
  const trimmed = text.trim();

  // Must start with { or [
  if (!trimmed.startsWith("{") && !trimmed.startsWith("[")) {
    return false;
  }

  try {
    JSON.parse(trimmed);
    return true;
  } catch {
    return false;
  }
}

/**
 * Check if text is a structured patch (from Edit/Write results)
 */
function isStructuredPatch(text: string): boolean {
  if (!text.trim().startsWith("{")) {
    return false;
  }

  try {
    const parsed = JSON.parse(text);

    // Check for structured patch fields
    return (
      typeof parsed === "object" &&
      parsed !== null &&
      (("oldStart" in parsed && "newStart" in parsed) ||
        ("hunks" in parsed && Array.isArray(parsed.hunks)) ||
        "structuredPatch" in parsed)
    );
  } catch {
    return false;
  }
}

/**
 * Check if text looks like a code block
 *
 * Uses a scoring system to detect code content without false-positiving
 * on regular prose. Requires multiple distinct indicators and a high
 * proportion of code-like lines.
 *
 * @see Issue #639 - Code blocks should be formatted and collapsible
 */
function isCode(text: string): boolean {
  const lines = text.split("\n");
  const nonEmptyLines = lines.filter((l) => l.trim().length > 0);

  // Need at least 3 non-empty lines to have enough signal
  if (nonEmptyLines.length < 3) {
    return false;
  }

  // Track distinct indicator categories found
  const indicators = new Set<string>();
  let codeLineCount = 0;

  // Structural patterns (strong signals)
  const structuralPatterns: Array<{ pattern: RegExp; name: string }> = [
    { pattern: /[{]\s*$/, name: "open-brace" },
    { pattern: /^\s*[}]/, name: "close-brace" },
    { pattern: /;\s*$/, name: "semicolon" },
    { pattern: /=>\s*[{(]?/, name: "arrow" },
    { pattern: /\)\s*[:{]/, name: "paren-block" },
    { pattern: /^\s{2,}\S/, name: "indented" },
  ];

  // Keyword patterns (moderate signals)
  const keywordPattern =
    /\b(function|class|interface|type|const|let|var|import|export|return|if|else|for|while|switch|case|throw|async|await|new|this|extends|implements)\b/;

  // Type annotation patterns (TypeScript/Java/etc.)
  const typePattern =
    /:\s*(string|number|boolean|void|any|null|undefined|Promise|Array|Record|Map|Set)\b/;

  for (const line of nonEmptyLines) {
    let isCodeLine = false;

    // Check structural patterns
    for (const { pattern, name } of structuralPatterns) {
      if (pattern.test(line)) {
        indicators.add(name);
        isCodeLine = true;
      }
    }

    // Check keywords
    if (keywordPattern.test(line)) {
      indicators.add("keyword");
      isCodeLine = true;
    }

    // Check type annotations
    if (typePattern.test(line)) {
      indicators.add("type-annotation");
      isCodeLine = true;
    }

    if (isCodeLine) {
      codeLineCount++;
    }
  }

  // Require at least 3 distinct indicator categories
  // AND >40% of non-empty lines matching code patterns
  const codeRatio = codeLineCount / nonEmptyLines.length;
  return indicators.size >= 3 && codeRatio > 0.4;
}

/**
 * Detect programming language from text content or filename
 *
 * @param text - The code content
 * @param filename - Optional filename for extension-based detection
 * @returns Language identifier for syntax highlighting
 */
export function detectLanguage(text: string, filename?: string): string {
  // Extension-based detection
  if (filename) {
    const ext = filename.split(".").pop()?.toLowerCase();
    const extMap: Record<string, string> = {
      ts: "typescript",
      tsx: "typescript",
      js: "javascript",
      jsx: "javascript",
      py: "python",
      rb: "ruby",
      go: "go",
      rs: "rust",
      java: "java",
      kt: "kotlin",
      swift: "swift",
      cs: "csharp",
      cpp: "cpp",
      c: "c",
      h: "c",
      hpp: "cpp",
      md: "markdown",
      json: "json",
      yaml: "yaml",
      yml: "yaml",
      xml: "xml",
      html: "html",
      css: "css",
      scss: "scss",
      sql: "sql",
      sh: "bash",
      bash: "bash",
      zsh: "bash",
      dockerfile: "dockerfile",
    };

    if (ext && extMap[ext]) {
      return extMap[ext];
    }

    // Filename-based detection
    const lowerFilename = filename.toLowerCase();
    if (lowerFilename === "dockerfile") return "dockerfile";
    if (lowerFilename === "makefile") return "makefile";
    if (lowerFilename.endsWith(".d.ts")) return "typescript";
  }

  // Content-based detection (basic heuristics)
  const trimmed = text.trim();

  if (trimmed.startsWith("{") || trimmed.startsWith("[")) {
    try {
      JSON.parse(trimmed);
      return "json";
    } catch {
      // Not valid JSON
    }
  }

  if (trimmed.startsWith("#!/bin/bash") || trimmed.startsWith("#!/bin/sh")) {
    return "bash";
  }

  if (trimmed.startsWith("#!/usr/bin/env python")) {
    return "python";
  }

  // Default to plain text
  return "text";
}

/**
 * Check if content exceeds the collapse threshold
 *
 * Checks both line count AND character count. Content is collapsed if either
 * threshold is exceeded. The character threshold catches large single-line
 * content (e.g., file contents with literal \n escapes instead of real newlines).
 *
 * @param text - The text to check
 * @param threshold - Line count threshold (default: 50)
 * @returns true if content should be collapsed
 */
export function shouldCollapse(text: string, threshold?: number): boolean {
  const lineLimit = threshold ?? DEFAULT_OPTIONS.collapseThreshold;
  const lineCount = text.split("\n").length;
  if (lineCount > lineLimit) {
    return true;
  }
  // Also collapse if character count exceeds threshold, regardless of line count
  return text.length > CHAR_COLLAPSE_THRESHOLD;
}

/**
 * Create a collapsible entry from large content
 *
 * Generates a compact summary for display with full content in details.
 * Handles both multi-line content (summary by line count) and long
 * single-line content (summary by character truncation with size label).
 *
 * @param text - The full text content
 * @param summaryLines - Number of lines to show in summary (default: 5)
 * @returns Object with summary and details for collapsible display
 */
export function createCollapsibleEntry(
  text: string,
  summaryLines?: number
): { summary: string; details: string } {
  const limit = summaryLines ?? DEFAULT_OPTIONS.summaryLines;
  const lines = text.split("\n");

  // Multi-line content: use line-based summary
  if (lines.length > limit) {
    const summary = lines.slice(0, limit).join("\n") + "\n...";
    return { summary, details: text };
  }

  // Few lines but long character count: use character-based summary with size label
  if (text.length > CHAR_COLLAPSE_THRESHOLD) {
    const sizeKb = (text.length / 1024).toFixed(1);
    const preview = text.slice(0, 200).replace(/\n/g, " ");
    const summary = `${preview}... (${sizeKb}KB content)`;
    return { summary, details: text };
  }

  return { summary: text, details: text };
}

/**
 * Format diff content with CSS classes for styling
 *
 * Each line is wrapped with appropriate class:
 * - diff-add: Lines starting with +
 * - diff-del: Lines starting with -
 * - diff-hunk: Lines starting with @@
 * - diff-context: Context lines (space prefix or no prefix)
 *
 * @param text - The diff text to format
 * @returns HTML string with styled diff lines
 */
export function formatDiff(text: string): string {
  const lines = text.split("\n");
  const formattedLines = lines.map((line) => {
    const escapedLine = escapeHtmlBasic(line);

    if (line.startsWith("@@") && line.includes("@@")) {
      return `<div class="diff-line diff-hunk">${escapedLine}</div>`;
    } else if (line.startsWith("+++") || line.startsWith("---")) {
      return `<div class="diff-line diff-header">${escapedLine}</div>`;
    } else if (line.startsWith("+")) {
      return `<div class="diff-line diff-add">${escapedLine}</div>`;
    } else if (line.startsWith("-")) {
      return `<div class="diff-line diff-del">${escapedLine}</div>`;
    } else {
      return `<div class="diff-line diff-context">${escapedLine}</div>`;
    }
  });

  return `<div class="diff-container">${formattedLines.join("")}</div>`;
}

/**
 * Format JSON with pretty-printing
 *
 * @param text - The JSON string to format
 * @returns Formatted HTML string, or original if invalid JSON
 */
export function formatJson(text: string): string {
  try {
    const parsed = JSON.parse(text.trim());
    const formatted = JSON.stringify(parsed, null, 2);
    const escaped = escapeHtmlBasic(formatted);
    return `<pre class="formatted-json"><code class="language-json">${escaped}</code></pre>`;
  } catch {
    // Return escaped original if parsing fails
    return `<pre class="formatted-json"><code>${escapeHtmlBasic(text)}</code></pre>`;
  }
}

/**
 * Format structured patch as readable diff
 *
 * Converts Edit/Write tool structuredPatch JSON to unified diff format.
 *
 * @param text - The structured patch JSON string
 * @returns Formatted diff HTML, or formatted JSON if conversion fails
 */
export function formatStructuredPatch(text: string): string {
  try {
    const parsed = JSON.parse(text.trim()) as StructuredPatch;

    // Build unified diff from structured patch
    const diffLines: string[] = [];

    // File header
    if (parsed.oldFileName || parsed.newFileName) {
      diffLines.push(`--- ${parsed.oldFileName || "a/file"}`);
      diffLines.push(`+++ ${parsed.newFileName || "b/file"}`);
    }

    // Process hunks
    if (parsed.hunks && Array.isArray(parsed.hunks)) {
      for (const hunk of parsed.hunks) {
        // Hunk header
        diffLines.push(
          `@@ -${hunk.oldStart},${hunk.oldLines} +${hunk.newStart},${hunk.newLines} @@`
        );

        // Hunk lines
        if (hunk.lines && Array.isArray(hunk.lines)) {
          diffLines.push(...hunk.lines);
        }
      }
    }

    if (diffLines.length > 0) {
      return formatDiff(diffLines.join("\n"));
    }

    // Fallback: just format as JSON if no hunks found
    return formatJson(text);
  } catch {
    return formatJson(text);
  }
}

/**
 * Format content for display based on detected type
 *
 * @param text - The raw text content
 * @param contentType - The content type (auto-detected if not provided)
 * @param options - Formatting options
 * @returns Formatted content with metadata
 */
export function formatForDisplay(
  text: string,
  contentType?: ContentType,
  options?: FormatOptions
): FormattedContent {
  const opts = { ...DEFAULT_OPTIONS, ...options };
  const type = contentType ?? detectContentType(text);
  const collapse = shouldCollapse(text, opts.collapseThreshold);

  let html: string;
  let language: string | undefined;

  switch (type) {
    case "diff":
      html = formatDiff(text);
      language = "diff";
      break;

    case "json":
      html = formatJson(text);
      language = "json";
      break;

    case "structured-patch":
      html = formatStructuredPatch(text);
      language = "diff";
      break;

    case "code":
      language = detectLanguage(text);
      html = `<pre class="code-block"><code class="language-${language}">${escapeHtmlBasic(text)}</code></pre>`;
      break;

    case "text":
    default:
      // For plain text, just escape HTML (markdown rendering handled by marked.js)
      html = escapeHtmlBasic(text);
      break;
  }

  const result: FormattedContent = {
    html,
    contentType: type,
    language,
    shouldCollapse: collapse,
  };

  if (collapse) {
    const { summary, details } = createCollapsibleEntry(text, opts.summaryLines);
    result.summary = summary;
    result.details = details;
  }

  return result;
}

/**
 * Basic HTML escaping for content
 *
 * Note: Full escaping with more entities is in OutputWindowHtml.ts (escapeHtml)
 * This is a simpler version for internal use.
 */
function escapeHtmlBasic(text: string): string {
  return text
    .replace(/&/g, "&amp;")
    .replace(/</g, "&lt;")
    .replace(/>/g, "&gt;")
    .replace(/"/g, "&quot;")
    .replace(/'/g, "&#39;");
}
