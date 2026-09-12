/**
 * steeringSources — shared readers that pull baseline "system steering" content
 * out of a project's standards docs, used by the provider-aware context
 * generators (GEMINI.md for Gemini, AGENTS.md for Codex).
 *
 * Each provider receives system-level guidance differently — Claude via the
 * `claude_code` SDK preset, Gemini via GEMINI.md, Codex via AGENTS.md — but the
 * underlying baseline content (project description, coding standards, security,
 * git workflow) is provider-neutral and lives here so it is assembled once.
 *
 * @see Issue #1055 (GEMINI.md), Issue #4028 (provider-aware steering / AGENTS.md)
 */

import * as fs from "node:fs";
import * as path from "node:path";

/**
 * Read a file and return its contents, or null if the file doesn't exist.
 */
export function readFileGracefully(filePath: string): string | null {
  try {
    return fs.readFileSync(filePath, "utf-8");
  } catch {
    return null;
  }
}

const SUMMARY_HEADING_RE = /^(#{1,6})([ \t]|$)/;
const SUMMARY_BLANK_RE = /^[ \t\r]*$/;
const SUMMARY_FENCE_RE = /^[ \t]*(```|~~~)/;

/**
 * Return up to `maxLines` lines of a markdown document's meaningful content,
 * across sections. A heading is kept only when body text follows it before a
 * heading of the same or a higher level, so a title immediately followed by a
 * sub-heading keeps the sub-section's body instead of ending the summary at
 * the title (issue 1675). Runs of blank lines collapse to one, and lines inside
 * fenced code blocks are body, never headings. Mirrors the Go
 * `codexprovision.extractSummary` byte-for-byte.
 */
export function extractSummary(content: string, maxLines: number = 50): string {
  const result: string[] = [];
  const isHeading: boolean[] = [];
  let pending: Array<{ level: number; line: string; blankAfter: boolean }> = [];
  let pendingBlank = false;
  let inFence = false;

  const emit = (line: string, head: boolean): boolean => {
    if (result.length >= maxLines) return false;
    if (pendingBlank && result.length > 0) {
      result.push("");
      isHeading.push(false);
      pendingBlank = false;
      if (result.length >= maxLines) return false;
    }
    pendingBlank = false;
    result.push(line);
    isHeading.push(head);
    return result.length < maxLines;
  };

  for (const line of content.split("\n")) {
    if (!inFence) {
      const m = SUMMARY_HEADING_RE.exec(line);
      if (m) {
        const level = m[1].length;
        pending = pending.filter((h) => h.level < level);
        pending.push({ level, line, blankAfter: false });
        continue;
      }
      if (SUMMARY_BLANK_RE.test(line)) {
        if (pending.length > 0) {
          pending[pending.length - 1].blankAfter = true;
        } else {
          pendingBlank = true;
        }
        continue;
      }
    }
    if (SUMMARY_FENCE_RE.test(line)) inFence = !inFence;
    let cont = true;
    for (const h of pending) {
      cont = emit(h.line, true);
      if (!cont) break;
      pendingBlank = h.blankAfter;
    }
    pending = [];
    if (!cont || !emit(line, false)) break;
  }

  // The line budget can run out between a heading and its body; a trailing
  // heading is then as empty as any other bodiless heading, so drop it.
  while (
    result.length > 0 &&
    (isHeading[result.length - 1] || SUMMARY_BLANK_RE.test(result[result.length - 1]))
  ) {
    result.pop();
  }
  return result.join("\n").trim();
}

/**
 * Read the project description from the repository's canonical agent contract:
 * the user part of AGENTS.md (the managed steering block is stripped first so
 * generated steering is never read back). CLAUDE.md is only a fallback for a
 * repository with no usable AGENTS.md; a leading `@AGENTS.md` import is skipped
 * there because it is an adapter line, not a description (issue 1675).
 */
export function readProjectDescription(projectRoot: string): string | null {
  const agentsMd = readFileGracefully(path.join(projectRoot, "AGENTS.md"));
  if (agentsMd !== null) {
    // Strip any pipeline-managed block first so NO provider (Gemini or Codex)
    // reads generated steering back as the project description. #4028
    const userPart = stripManagedBlock(agentsMd).trim();
    if (userPart.length > 0) return extractSummary(userPart);
  }

  const claudeMd = readFileGracefully(path.join(projectRoot, "CLAUDE.md"));
  if (claudeMd !== null) {
    const body = stripLeadingAgentsImport(claudeMd).trim();
    if (body.length > 0) return extractSummary(body);
  }

  return null;
}

/**
 * Drop the first non-blank line of a CLAUDE.md when it is the `@AGENTS.md` (or
 * `@./AGENTS.md`) import. Mirrors the Go `stripLeadingAgentsImport`.
 */
export function stripLeadingAgentsImport(content: string): string {
  const lines = content.split("\n");
  for (let i = 0; i < lines.length; i++) {
    if (SUMMARY_BLANK_RE.test(lines[i])) continue;
    const t = lines[i].replace(/^[ \t\r]+|[ \t\r]+$/g, "");
    if (t === "@AGENTS.md" || t === "@./AGENTS.md") {
      return lines.slice(i + 1).join("\n");
    }
    return content;
  }
  return content;
}

/**
 * Read coding standards from standards/ directory, falling back to docs/.
 */
export function readStandards(projectRoot: string): string | null {
  const standardsFile = readFileGracefully(
    path.join(projectRoot, "standards", "code-standards.md")
  );
  if (standardsFile) {
    return extractSummary(standardsFile, 80);
  }

  const docsFile = readFileGracefully(path.join(projectRoot, "docs", "CODE_STANDARDS.md"));
  if (docsFile) {
    return extractSummary(docsFile, 80);
  }

  return null;
}

/**
 * Read security standards.
 */
export function readSecurity(projectRoot: string): string | null {
  const securityFile = readFileGracefully(path.join(projectRoot, "standards", "security.md"));
  if (securityFile) {
    return extractSummary(securityFile, 60);
  }

  const docsFile = readFileGracefully(
    path.join(projectRoot, "docs", "SECURITY_AND_ERROR_HANDLING.md")
  );
  if (docsFile) {
    return extractSummary(docsFile, 60);
  }

  return null;
}

/**
 * Read git workflow summary.
 */
export function readGitWorkflow(projectRoot: string): string | null {
  const gitFile = readFileGracefully(path.join(projectRoot, "docs", "GIT_WORKFLOW.md"));
  if (gitFile) {
    return extractSummary(gitFile, 40);
  }

  return null;
}

// ---------------------------------------------------------------------------
// Managed-block merge — shared so the marker logic lives in one place and the
// provider-neutral readers above can recognize/strip it. Used by
// CodexContextGenerator for AGENTS.md. @see Issue #4028
// ---------------------------------------------------------------------------

/**
 * Markers delimiting the pipeline-managed region of a steering file. Everything
 * between them is owned by the generator; everything outside is the user's.
 */
export const CODEX_MANAGED_BEGIN = "<!-- BEGIN NIGHTGAUGE MANAGED STEERING -->";
export const CODEX_MANAGED_END = "<!-- END NIGHTGAUGE MANAGED STEERING -->";

/**
 * Insert or replace the managed block in `existing`, preserving user content.
 * When the file has no managed block yet, the block is appended below the user's
 * content; when absent/empty entirely, the block becomes the whole file.
 */
export function upsertManagedBlock(existing: string | null, blockInner: string): string {
  const wrapped = `${CODEX_MANAGED_BEGIN}\n${blockInner}\n${CODEX_MANAGED_END}`;

  if (existing === null || existing.trim().length === 0) {
    return wrapped + "\n";
  }

  const beginIdx = existing.indexOf(CODEX_MANAGED_BEGIN);
  const endIdx = existing.indexOf(CODEX_MANAGED_END);
  if (beginIdx !== -1 && endIdx !== -1 && endIdx > beginIdx) {
    // Replace in place, normalizing the boundaries so blank lines never
    // accumulate across regenerations (same normalization as stripManagedBlock).
    const before = existing.slice(0, beginIdx).replace(/\n+$/, "");
    let after = existing.slice(endIdx + CODEX_MANAGED_END.length);
    while (after.startsWith("\n")) after = after.slice(1);
    if (before === "" && after === "") return wrapped + "\n";
    if (before === "") return wrapped + "\n\n" + after;
    if (after === "") return before + "\n\n" + wrapped + "\n";
    return before + "\n\n" + wrapped + "\n\n" + after;
  }

  // No managed block yet — append below the user's content.
  return existing.trimEnd() + "\n\n" + wrapped + "\n";
}

/**
 * Remove the managed block from `existing`, preserving user content. Returns the
 * empty string when the block was the only content (caller deletes the file).
 */
export function stripManagedBlock(existing: string): string {
  const beginIdx = existing.indexOf(CODEX_MANAGED_BEGIN);
  const endIdx = existing.indexOf(CODEX_MANAGED_END);
  if (beginIdx === -1 || endIdx === -1 || endIdx < beginIdx) {
    return existing;
  }

  const before = existing.slice(0, beginIdx).replace(/\n+$/, "");
  const after = existing.slice(endIdx + CODEX_MANAGED_END.length).replace(/^\n+/, "");

  if (before === "" && after === "") return "";
  if (before === "") return after;
  if (after === "") return before + "\n";
  return before + "\n\n" + after;
}
