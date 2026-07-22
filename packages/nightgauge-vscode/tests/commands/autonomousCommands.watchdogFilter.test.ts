/**
 * Tests for the autonomous stall watchdog's `enabled_repos` allowlist filter.
 *
 * Covers Issue #3427 — repository deselection had no effect on the watchdog
 * refresh loop, causing every workspace folder to be polled every 30s
 * regardless of the user's selection. The watchdog now filters its repo set
 * via `filterRepoContextsByEnabledRepos` on each cycle, reading the latest
 * allowlist from `_enabledReposConfigService` so toggles take effect on the
 * next tick (no autonomous Stop/Start required).
 *
 * @see src/commands/autonomousCommands.ts
 * @see src/utils/enabledReposConfig.ts
 * @see Issue #3427
 */

import { describe, it, expect } from "vitest";
import { filterRepoContextsByEnabledRepos } from "../../src/commands/autonomousCommands";

interface RepoCtx {
  workspaceRoot: string;
  owner: string;
  repo: string;
  projectNumber: number;
}

const ctx = (repo: string, owner = "nightgauge", projectNumber = 1): RepoCtx => ({
  workspaceRoot: `/tmp/${repo}`,
  owner,
  repo,
  projectNumber,
});

const ALL: RepoCtx[] = [
  ctx("nightgauge"),
  ctx("acme-platform"),
  ctx("acme-mobile"),
  ctx("acme-dashboard"),
];

describe("filterRepoContextsByEnabledRepos", () => {
  it("returns the full set when the allowlist is empty (scan-all default)", () => {
    expect(filterRepoContextsByEnabledRepos(ALL, [])).toEqual(ALL);
  });

  it("returns the full set when the allowlist is undefined", () => {
    // The runtime store may return undefined when the key is absent — the
    // helper coerces to a no-op rather than dropping every repo.
    expect(filterRepoContextsByEnabledRepos(ALL, undefined as unknown as string[])).toEqual(ALL);
  });

  it("keeps only the repos that match an allowlist entry by short name", () => {
    const result = filterRepoContextsByEnabledRepos(ALL, ["acme-platform"]);
    expect(result.map((r) => r.repo)).toEqual(["acme-platform"]);
  });

  it("keeps repos when the allowlist uses fully-qualified owner/repo form", () => {
    const result = filterRepoContextsByEnabledRepos(ALL, [
      "nightgauge/nightgauge",
      "nightgauge/acme-mobile",
    ]);
    expect(result.map((r) => r.repo).sort()).toEqual(["acme-mobile", "nightgauge"]);
  });

  it("matches case-insensitively", () => {
    const result = filterRepoContextsByEnabledRepos(ALL, [
      "NIGHTGAUGE",
      "nightgauge/Acme-Platform",
    ]);
    expect(result.map((r) => r.repo).sort()).toEqual(["acme-platform", "nightgauge"]);
  });

  it("returns an empty array when no allowlist entry matches any context", () => {
    // Mirrors the bulk "Exclude All" sentinel `__none__` — the watchdog should
    // skip every repo, which is the desired outcome for a user who has
    // explicitly excluded everything.
    expect(filterRepoContextsByEnabledRepos(ALL, ["__none__"])).toEqual([]);
  });

  it("ignores empty / whitespace allowlist entries without dropping repos", () => {
    // Defensive: malformed config shouldn't make every repo silently match.
    const result = filterRepoContextsByEnabledRepos(ALL, ["", "  ", "nightgauge"]);
    expect(result.map((r) => r.repo)).toEqual(["nightgauge"]);
  });

  it("preserves the original context shape (owner, projectNumber, etc.)", () => {
    const result = filterRepoContextsByEnabledRepos(ALL, ["acme-dashboard"]);
    expect(result).toEqual([
      {
        workspaceRoot: "/tmp/acme-dashboard",
        owner: "nightgauge",
        repo: "acme-dashboard",
        projectNumber: 1,
      },
    ]);
  });

  it("is generic over context shape (works for any object with a repo field)", () => {
    type Alt = { repo: string; foo: number };
    const items: Alt[] = [
      { repo: "alpha", foo: 1 },
      { repo: "beta", foo: 2 },
    ];
    const result = filterRepoContextsByEnabledRepos(items, ["alpha"]);
    expect(result).toEqual([{ repo: "alpha", foo: 1 }]);
  });
});
