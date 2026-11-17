/**
 * Markdown escaping for tree-view tooltips (#3369).
 *
 * Tooltip text built from forge- or agent-derived values (issue titles,
 * branches, error messages) must not be able to inject Markdown structure:
 * links, images, raw HTML, or emphasis. These helpers mirror the Go pair
 * `attention.EscapeMarkdown` / `attention.CodeSpan` (internal/attention/markdown.go)
 * so tooltips and attention cards escape the same way.
 */

import { UNDETERMINED_BRANCH_LABEL } from "./dashboard/DashboardComponents";

/** CommonMark inline-structure characters; kept in step with Go `markdownSpecial`. */
const MARKDOWN_SPECIAL = new Set("\\`*_[]<>!~|&");

/** Control (Cc) and format (Cf) characters, including newlines and bidi overrides. */
const CONTROL_OR_FORMAT = /[\p{Cc}\p{Cf}]/u;

/**
 * Escapes a value for literal display as Markdown prose: every inline-structure
 * character is backslash-escaped, and control/format characters become spaces
 * so the value cannot break out of its line.
 */
export function escapeMarkdown(text: string): string {
  let out = "";
  for (const ch of text) {
    if (CONTROL_OR_FORMAT.test(ch)) {
      out += " ";
    } else if (MARKDOWN_SPECIAL.has(ch)) {
      out += "\\" + ch;
    } else {
      out += ch;
    }
  }
  return out;
}

/**
 * Renders a literal value (branch, label, path) as a CommonMark code span.
 * The fence is one backtick longer than the longest inner backtick run; the
 * content is padded when it starts or ends with a backtick or space. Control
 * and format characters become spaces. An empty value renders as "".
 */
export function codeSpan(text: string): string {
  if (text === "") return "";
  let content = "";
  let longest = 0;
  let run = 0;
  for (let ch of text) {
    if (CONTROL_OR_FORMAT.test(ch)) ch = " ";
    if (ch === "`") {
      run++;
      longest = Math.max(longest, run);
    } else {
      run = 0;
    }
    content += ch;
  }
  const fence = "`".repeat(longest + 1);
  const first = content[0];
  const last = content[content.length - 1];
  if (first === "`" || first === " " || last === "`" || last === " ") {
    content = ` ${content} `;
  }
  return fence + content + fence;
}

/**
 * Renders a branch for a Markdown tooltip: a real branch as a code span, an
 * undetermined one as the shared prose label (#448).
 */
export function branchMarkdown(branch: string | null | undefined): string {
  return branch?.trim() ? codeSpan(branch) : UNDETERMINED_BRANCH_LABEL;
}

/**
 * Returns a link destination for an `https:` URL, or undefined for anything
 * else. Parentheses are percent-encoded so the URL cannot close the Markdown
 * link destination early.
 */
export function safeHttpsHref(url: string | undefined): string | undefined {
  if (!url) return undefined;
  let parsed: URL;
  try {
    parsed = new URL(url);
  } catch {
    return undefined;
  }
  if (parsed.protocol !== "https:") return undefined;
  return parsed.href.replace(/\(/g, "%28").replace(/\)/g, "%29");
}
