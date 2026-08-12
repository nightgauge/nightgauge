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
 * On a failed fetch, or a zero-exit call whose stdout doesn't actually parse
 * as a PR list, the previous snapshot is served (if any) but is NOT written
 * back into the cache — the caller receives `null` to signal "unknown, not
 * fetched", distinguishable from `[]` ("fetched fine, repo genuinely has no
 * open PRs"). Callers MUST NOT persist a derived mapping (e.g. into a
 * downstream per-issue cache) when the result is `null` — that would stamp a
 * transient failure as a fresh, trusted fact for a full TTL window.
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
): Promise<OpenPRRecord[] | null> {
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
    const trimmed = stdout.trim();
    if (prs.length === 0 && trimmed !== "" && trimmed !== "[]") {
      // Zero-exit but the stdout didn't actually parse into a PR list (e.g.
      // gh printed a warning/partial line instead of JSON). This is NOT a
      // genuine "no open PRs" result — treat it the same as a fetch failure:
      // serve the last-known snapshot if we have one, else signal unknown.
      return cached?.prs ?? null;
    }
    repoPRListCache.set(key, { prs, fetchedAt: now });
    return prs;
  } catch {
    // Fetch failed — gh CLI not available, network issue, timeout, etc.
    // Serve the last-known snapshot if we have one; otherwise signal
    // "unknown" (null) rather than caching (and thus trusting) an empty
    // result that would mask real open PRs.
    return cached?.prs ?? null;
  }
}

/**
 * Find the PR associated with an issue from an open-PR snapshot.
 *
 * Recognizes two forms of issue reference, both word-boundary anchored so
 * "#2010"/".../issues/2010" never match issue 201 as a substring: the
 * `#<issueNumber>` shorthand, and a GitHub issue URL
 * (`.../issues/<issueNumber>`) — the form GitHub's own UI and many bots
 * insert, which the old per-issue `--search` query used to catch but a bare
 * `#N` regex does not.
 *
 * Resolution is ranked, not first-match, to avoid binding an issue to an
 * unrelated PR that merely name-drops it (this repo's own PR titles
 * routinely cross-reference other issues):
 *   1. A PR with a closing keyword (close/closes/closed, fix/fixes/fixed,
 *      resolve/resolves/resolved) immediately preceding the reference wins —
 *      mirrors the pipeline's own `Closes #<N>` convention (see
 *      docs/PR_CREATE_STAGE.md). An intervening URL prefix is allowed (e.g.
 *      "Fixes https://github.com/org/repo/issues/201").
 *   2. If no closing reference exists, fall back to a bare mention — but
 *      ONLY when exactly one open PR mentions the issue. Two or more bare
 *      mentions are ambiguous and resolve to `null` (refuse to guess; the
 *      caller treats `null` as "no PR", which is always the safe default —
 *      it never mutates anything).
 *
 * @param issueNumber - Issue number to match
 * @param prs - Open-PR snapshot from getOpenPRsForRepo()
 * @returns The resolved PR, or null if none (or too many, ambiguously) reference the issue
 */
export function findPRForIssueInList(issueNumber: number, prs: OpenPRRecord[]): PRInfo | null {
  const linkRef = String.raw`(?:#|/issues/)${issueNumber}\b`;
  const closing = new RegExp(
    `\\b(close[sd]?|fix(e[sd])?|resolve[sd]?)\\s*:?\\s+\\S*${linkRef}`,
    "i"
  );
  const bare = new RegExp(linkRef);
  const text = (pr: OpenPRRecord): string => `${pr.title ?? ""}\n${pr.body ?? ""}`;

  const closers = prs.filter((pr) => closing.test(text(pr)));
  let match: OpenPRRecord | null = closers[0] ?? null;
  if (!match) {
    const mentions = prs.filter((pr) => bare.test(text(pr)));
    match = mentions.length === 1 ? mentions[0] : null;
  }
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
 * Clear every cached open-PR snapshot for every repo. Called both from
 * `stopAutonomousStallWatchdog()` (so a stop→restart cycle within the TTL
 * window never acts on a pre-stop repo-wide snapshot — the per-issue
 * watchdog cache and this repo-level cache must go stale together, not just
 * on the TTL) and from tests, which want a clean slate between cases.
 */
export function clearOpenPRsCache(): void {
  repoPRListCache.clear();
}
