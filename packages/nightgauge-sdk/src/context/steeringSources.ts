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

/**
 * Extract the first section (up to the second H1/H2) from a markdown file
 * to keep the context concise.
 */
export function extractSummary(content: string, maxLines: number = 50): string {
  const lines = content.split("\n");
  const result: string[] = [];
  let foundFirstHeader = false;

  for (const line of lines) {
    if (line.startsWith("# ") || line.startsWith("## ")) {
      if (foundFirstHeader) break;
      foundFirstHeader = true;
    }
    result.push(line);
    if (result.length >= maxLines) break;
  }

  return result.join("\n").trim();
}

/**
 * Read project description from CLAUDE.md or AGENTS.md header.
 */
export function readProjectDescription(projectRoot: string): string | null {
  const claudeMd = readFileGracefully(path.join(projectRoot, "CLAUDE.md"));
  if (claudeMd) {
    return extractSummary(claudeMd);
  }

  const agentsMd = readFileGracefully(path.join(projectRoot, "AGENTS.md"));
  if (agentsMd) {
    // Strip any pipeline-managed block first so NO provider (Gemini or Codex)
    // reads generated steering back as the project description — the managed
    // block is generated content, not user-authored project context. #4028
    const userPart = stripManagedBlock(agentsMd).trim();
    return userPart.length > 0 ? extractSummary(userPart) : null;
  }

  return null;
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
