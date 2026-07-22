import { describe, it, expect } from "vitest";
import * as vscode from "vscode";
import { RepositoryTreeItem } from "../../../src/views/items/RepositoryTreeItem";
import { Repository } from "../../../src/models/Repository";

/**
 * Tree-item rendering tests for the per-repo concurrency cap suffix
 * (Issue #2987 + #3051 unification). The description suffix mirrors the
 * resolved `MaxForRepo()` value:
 *   - numeric N≥2 → `[max: N]`
 *   - sequential / cap == 1 → `[max: 1]` (was `[seq]` before #3051)
 *   - no per-repo cap → no suffix
 */
describe("RepositoryTreeItem — per-repo concurrency cap suffix (Issue #2987)", () => {
  function buildRepo(): Repository {
    const repo = new Repository("my-repo", "/tmp/my-repo", "primary");
    return repo;
  }

  it("appends [max: N] when maxConcurrent ≥ 2", () => {
    const repo = buildRepo();
    const item = new RepositoryTreeItem(repo, false, true, false, 3);
    expect(item.description).toMatch(/\[max: 3\]$/);
    expect(item.maxConcurrent).toBe(3);
    expect(item.isSequential).toBe(false);
  });

  it("appends [max: 1] when isSequential is true (legacy boolean)", () => {
    const repo = buildRepo();
    const item = new RepositoryTreeItem(repo, false, true, true);
    expect(item.description).toMatch(/\[max: 1\]$/);
    expect(item.description).not.toMatch(/\[seq\]/);
    expect(item.isSequential).toBe(true);
    expect(item.maxConcurrent).toBeUndefined();
  });

  it("appends [max: 1] when maxConcurrent === 1 (numeric form of sequential)", () => {
    const repo = buildRepo();
    const item = new RepositoryTreeItem(repo, false, true, false, 1);
    expect(item.description).toMatch(/\[max: 1\]$/);
    expect(item.description).not.toMatch(/\[seq\]/);
    expect(item.isSequential).toBe(true);
    expect(item.maxConcurrent).toBe(1);
  });

  it("appends no suffix when no cap is set (defers to global)", () => {
    const repo = buildRepo();
    const item = new RepositoryTreeItem(repo, false, true, false, undefined);
    // No bracketed cap suffix on the description.
    expect(item.description).not.toMatch(/\[(max|seq)/);
    expect(item.isSequential).toBe(false);
    expect(item.maxConcurrent).toBeUndefined();
  });

  it("uses the resolved isSequential for contextValue (numeric cap of 1)", () => {
    const repo = buildRepo();
    const item = new RepositoryTreeItem(repo, false, true, false, 1);
    // The toggle command targets `repository-sequential` — numeric cap=1
    // must surface the same context so the existing menu entry still applies.
    expect(item.contextValue).toBe("repository-sequential");
  });

  it("uses concurrent contextValue when maxConcurrent ≥ 2", () => {
    const repo = buildRepo();
    const item = new RepositoryTreeItem(repo, false, true, false, 4);
    expect(item.contextValue).toBe("repository");
  });

  it("renders a MarkdownString tooltip when maxConcurrent ≥ 2", () => {
    const repo = buildRepo();
    const item = new RepositoryTreeItem(repo, false, true, false, 2);
    expect(item.tooltip).toBeInstanceOf(vscode.MarkdownString);
  });
});

/**
 * Branch + unified cap suffix in the description (Issue #3051). The
 * description now leads with the current git branch (or
 * `(detached @<sha7>)`) and appends the cap suffix when present. Role and
 * `owner/repo` were relocated to the tooltip.
 */
describe("RepositoryTreeItem — branch + unified cap suffix (Issue #3051)", () => {
  function buildRepo(): Repository {
    return new Repository("my-repo", "/tmp/my-repo", "primary");
  }

  it("shows the branch alone when there is no cap", () => {
    const repo = buildRepo();
    const item = new RepositoryTreeItem(repo, false, true, false, undefined, "feat/foo");
    expect(item.description).toBe("feat/foo");
  });

  it("shows the detached-HEAD short sha alone when there is no cap", () => {
    const repo = buildRepo();
    const item = new RepositoryTreeItem(repo, false, true, false, undefined, "(detached @abc1234)");
    expect(item.description).toBe("(detached @abc1234)");
  });

  it("renders empty description when branch lookup failed and no cap is set", () => {
    const repo = buildRepo();
    const item = new RepositoryTreeItem(repo, false, true, false, undefined, undefined);
    // No branch, no cap — and no role/owner fallback either. Empty string
    // is the correct degraded form.
    expect(item.description).toBe("");
    expect(item.description).not.toMatch(/primary/);
  });

  it("joins branch and [max: 1] when sequential", () => {
    const repo = buildRepo();
    const item = new RepositoryTreeItem(repo, false, true, false, 1, "feat/foo");
    expect(item.description).toBe("feat/foo • [max: 1]");
    expect(item.description).not.toMatch(/\[seq\]/);
  });

  it("joins branch and [max: N] for N ≥ 2", () => {
    const repo = buildRepo();
    const item = new RepositoryTreeItem(repo, false, true, false, 4, "main");
    expect(item.description).toBe("main • [max: 4]");
  });

  it("renders branch only (no suffix) when there is no cap", () => {
    const repo = buildRepo();
    const item = new RepositoryTreeItem(repo, false, true, false, undefined, "main");
    expect(item.description).toBe("main");
    expect(item.description).not.toMatch(/\[(max|seq)/);
  });

  it("description no longer contains role or owner/repo (those moved to tooltip)", () => {
    const repo = buildRepo();
    // Seed the backing config so the github getter resolves to acme/my-repo.
    (repo as any)._incrediConfig = { owner: "acme", repo: "my-repo" };
    const item = new RepositoryTreeItem(repo, false, true, false, undefined, "main");
    expect(item.description).toBe("main");
    expect(item.description).not.toMatch(/primary/);
    expect(item.description).not.toMatch(/acme\/my-repo/);
  });
});
