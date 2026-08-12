/**
 * PR Detection Helpers
 *
 * Utilities for detecting and fetching PR information associated with issues.
 * Uses `gh` CLI for authenticated access to GitHub API.
 */

import * as vscode from "vscode";
import { exec } from "child_process";
import { promisify } from "util";

const execAsync = promisify(exec);

/**
 * PR information structure
 */
export interface PRInfo {
  /** PR number */
  number: number;
  /** Full PR URL */
  url: string;
  /** PR title */
  title?: string;
}

/**
 * Get PR information for an issue
 *
 * Uses `gh pr list` command to find PRs associated with the issue.
 * This is a best-effort check - returns null if no PR found or on error.
 *
 * @param issueNumber - Issue number to search for
 * @param workspaceRoot - Workspace root directory for git operations
 * @returns PR info if found, null otherwise
 */
export async function getPRForIssue(
  issueNumber: number,
  workspaceRoot: string
): Promise<PRInfo | null> {
  try {
    // Use gh CLI to search for PRs linked to this issue
    // Search by issue number in PR body/title
    const { stdout } = await execAsync(
      `gh pr list --json number,url,title --search "${issueNumber} in:body,title"`,
      {
        cwd: workspaceRoot,
        timeout: 5000, // 5 second timeout
      }
    );

    return parsePRFromGHCLI(stdout);
  } catch (error) {
    // Graceful degradation - gh CLI not available, network issues, etc.
    // Don't log as error since this is optional enrichment
    return null;
  }
}

/**
 * Parse PR information from `gh pr list` JSON output
 *
 * @param output - JSON output from gh CLI
 * @returns First PR found, or null if none
 */
export function parsePRFromGHCLI(output: string): PRInfo | null {
  try {
    const prs = JSON.parse(output);

    if (!Array.isArray(prs) || prs.length === 0) {
      return null;
    }

    // Return first PR (most relevant)
    const pr = prs[0];

    // Validate structure
    if (typeof pr.number !== "number" || typeof pr.url !== "string") {
      return null;
    }

    return {
      number: pr.number,
      url: pr.url,
      title: typeof pr.title === "string" ? pr.title : undefined,
    };
  } catch (error) {
    // Malformed JSON - return null
    return null;
  }
}

/**
 * Check if an issue has status:in-review label (heuristic for PR existence)
 *
 * This is a fast, deterministic check that doesn't require API calls.
 *
 * @param labels - Array of label strings from issue
 * @returns True if issue likely has a PR (has in-review label)
 */
export function hasInReviewLabel(labels: string[]): boolean {
  return labels.some((label) => label.toLowerCase().includes("status:in-review"));
}

// ─────────────────────────────────────────────────────────────────────────
// Batched, cached open-PR listing (Issue #483)
//
// getPRForIssue() above shells out to `gh pr list --search` — GitHub's
// separate, much tighter search-bucket quota (30 req/min) — once PER ISSUE.
// The autonomous stall watchdog called it for every in-progress issue on
// every ~2-minute tick, uncached, which was the single largest source of
// rate-limit exhaustion (#483). getPRForIssue() itself is left unchanged
// (other callers — drag-and-drop, epic queue filtering, concurrent pipeline
// management — invoke it occasionally, not on a fixed poll cadence).
//
// getOpenPRsForRepo() below replaces the watchdog's per-issue search with
// ONE plain (non-`--search`) `gh pr list` call per repo, cached with a TTL,
// and findPRForIssueInList() derives the PR↔issue linkage locally from that
// snapshot instead of asking GitHub per issue.
// ─────────────────────────────────────────────────────────────────────────

/** An open PR entry with enough text to derive issue linkage locally. */
export interface OpenPRRecord extends PRInfo {
  /** PR body — scanned (with title) for a "#<issueNumber>" reference. */
  body?: string;
}

interface RepoPRListCacheEntry {
  prs: OpenPRRecord[];
  fetchedAt: number;
}

/**
 * TTL for the cached open-PR↔issue mapping. Matches the watchdog's
 * `EMPTY_BOARD_SKIP_MS` cadence (autonomousCommands.ts) so a steady-state
 * board (ticks ~2 min apart) reuses the same snapshot across many ticks
 * instead of re-fetching every time.
 */
export const PR_MAPPING_CACHE_TTL_MS = 5 * 60_000;

const repoPRListCache = new Map<string, RepoPRListCacheEntry>();

function repoCacheKey(owner: string, repo: string): string {
  return `${owner}/${repo}`;
}

/**
 * Parse `gh pr list --json number,url,title,body` output into open-PR
 * records. Malformed entries (missing `number`/`url`, wrong types) are
 * dropped rather than failing the whole batch.
 *
 * @param output - JSON output from gh CLI
 */
export function parseOpenPRList(output: string): OpenPRRecord[] {
  try {
    const raw = JSON.parse(output);
    if (!Array.isArray(raw)) return [];
    const result: OpenPRRecord[] = [];
    for (const pr of raw) {
      if (typeof pr?.number !== "number" || typeof pr?.url !== "string") continue;
      result.push({
        number: pr.number,
        url: pr.url,
        title: typeof pr.title === "string" ? pr.title : undefined,
        body: typeof pr.body === "string" ? pr.body : undefined,
      });
    }
    return result;
  } catch {
    // Malformed JSON — return empty batch (graceful degradation, matches
    // parsePRFromGHCLI's behavior above).
    return [];
  }
}

/**
 * Fetch every open PR for a repo in ONE call — no `--search`, so this draws
 * from GitHub's normal quota rather than the separate search bucket — and
 * cache the snapshot for {@link PR_MAPPING_CACHE_TTL_MS}. Repeated calls for
 * the same repo within the TTL window (whether from the same tick checking
 * multiple issues, or from a later tick with unchanged state) return the
 * cached snapshot and make no GitHub call at all.
 *
 * On a failed fetch, the previous snapshot (if any) is kept and returned
 * rather than caching an empty result that would mask real open PRs.
 *
 * @param workspaceRoot - Workspace root directory for git operations
 * @param owner - Repository owner
 * @param repo - Repository name
 * @param now - Injectable clock for tests (defaults to Date.now())
 */
export async function getOpenPRsForRepo(
  workspaceRoot: string,
  owner: string,
  repo: string,
  now: number = Date.now()
): Promise<OpenPRRecord[]> {
  const key = repoCacheKey(owner, repo);
  const cached = repoPRListCache.get(key);
  if (cached && now - cached.fetchedAt < PR_MAPPING_CACHE_TTL_MS) {
    return cached.prs;
  }

  try {
    const { stdout } = await execAsync(
      `gh pr list --json number,url,title,body --state open --limit 200`,
      {
        cwd: workspaceRoot,
        timeout: 5000, // 5 second timeout
      }
    );
    const prs = parseOpenPRList(stdout);
    repoPRListCache.set(key, { prs, fetchedAt: now });
    return prs;
  } catch {
    // Graceful degradation — gh CLI not available, network issues, etc.
    // Serve the last-known snapshot rather than caching (and thus trusting)
    // an empty result.
    return cached?.prs ?? [];
  }
}

/**
 * Find the PR associated with an issue from an open-PR snapshot, by looking
 * for a `#<issueNumber>` reference (word-boundary — "#48" does not match
 * "#483") in the PR's title or body. Mirrors the pipeline's own PR
 * convention of a `Closes #<N>` body line (see docs/PR_CREATE_STAGE.md),
 * evaluated locally instead of via a per-issue GitHub search query.
 *
 * @param issueNumber - Issue number to match
 * @param prs - Open-PR snapshot from getOpenPRsForRepo()
 * @returns First matching PR, or null if none reference the issue
 */
export function findPRForIssueInList(issueNumber: number, prs: OpenPRRecord[]): PRInfo | null {
  const pattern = new RegExp(`#${issueNumber}\\b`);
  const match = prs.find((pr) => pattern.test(pr.title ?? "") || pattern.test(pr.body ?? ""));
  if (!match) return null;
  return { number: match.number, url: match.url, title: match.title };
}

/**
 * Invalidate the cached open-PR snapshot for a repo so the next
 * getOpenPRsForRepo() call re-fetches instead of serving a stale snapshot.
 * Call after a PR event this process itself causes (e.g. the stall
 * watchdog's own `nightgauge pr merge` recovery re-run) so the following
 * tick doesn't act on now-outdated data.
 *
 * @param owner - Repository owner
 * @param repo - Repository name
 */
export function invalidateOpenPRsCache(owner: string, repo: string): void {
  repoPRListCache.delete(repoCacheKey(owner, repo));
}

/**
 * Reset all cached open-PR snapshots. Exported for use in tests only — not
 * part of the public extension API.
 */
export function _resetOpenPRsCacheForTests(): void {
  repoPRListCache.clear();
}
