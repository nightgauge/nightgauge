/**
 * Mirrors internal/execution/issue_context_paths_test.go.
 *
 * The pipeline state directory lives under the git common dir (ADR-024 § 7),
 * so every root of one clone — the checkout, a Go-manager worktree, a legacy
 * in-tree worktree, the extension's worktree — resolves to the SAME
 * issue-{N}.json, and the candidate list collapses to that one path after
 * de-duplication, whichever worktree layout the run used (#994, #1206, #2037).
 */
import { afterAll, describe, it, expect } from "vitest";
import * as fs from "node:fs";
import * as os from "node:os";
import * as path from "node:path";
import {
  issueContextCandidates,
  pipelineFileCandidates,
} from "../../src/utils/issueContextCandidates";
import { pipelineStateDir } from "../../src/utils/cloneLayout";
import { setCachedWorktreeBaseForTest } from "../../src/utils/worktreeLocation";
import { fakeCloneLayout, git, initGitRepo } from "../helpers/cloneLayout";

const tmpDirs: string[] = [];
afterAll(() => {
  for (const d of tmpDirs) fs.rmSync(d, { recursive: true, force: true });
});

function repo(): string {
  const root = initGitRepo(fs.mkdtempSync(path.join(os.tmpdir(), "ng-ctx-cand-")));
  tmpDirs.push(root);
  return root;
}

/** The one path every root of `root`'s clone resolves to. */
function issueContextFile(root: string, name = "issue-42.json"): string {
  return path.join(pipelineStateDir(root), name);
}

describe("issueContextCandidates (#1206, mirrors Go #994)", () => {
  it("a run that never took a worktree reads the clone's pipeline directory", () => {
    const root = repo();
    expect(issueContextCandidates(root, "", "acme/widget", 42)).toEqual([issueContextFile(root)]);
  });

  it("a linked worktree shares the clone's file", () => {
    const root = repo();
    fs.writeFileSync(path.join(root, "README"), "x\n");
    git(root, "add", ".");
    git(root, "commit", "-q", "-m", "init");
    const wt = path.join(root, ".worktrees", "issue-42");
    git(root, "worktree", "add", "-q", "--detach", wt);

    expect(issueContextFile(wt)).toBe(issueContextFile(root));
    for (const worktreeDir of ["", wt]) {
      expect(issueContextCandidates(root, worktreeDir, "acme/widget", 42)).toEqual([
        issueContextFile(root),
      ]);
    }
  });

  it("puts a known worktree first, even one of another clone", () => {
    const root = repo();
    const other = repo();
    expect(issueContextCandidates(root, other, "acme/widget", 42)).toEqual([
      issueContextFile(other),
      issueContextFile(root),
    ]);
  });

  it("still works when the repo name is unknown", () => {
    const root = repo();
    expect(issueContextCandidates(root, "", "", 42)).toEqual([issueContextFile(root)]);
  });

  it("names no path outside a git repository", () => {
    const dir = fs.mkdtempSync(path.join(os.tmpdir(), "ng-ctx-nogit-"));
    tmpDirs.push(dir);
    expect(issueContextCandidates(dir, dir, "acme/widget", 42)).toEqual([]);
  });

  it("returns nothing when there is no root and no worktree", () => {
    expect(issueContextCandidates("", "", "widget", 42)).toEqual([]);
  });

  it("orders the roots most-specific first: worktree, Go base, legacy layouts, root", () => {
    // Each root mapped to its own git dir, so no two collapse and the order
    // of the roots is visible in the result.
    const root = path.resolve("/repo");
    const base = path.join(path.sep + "state", "worktrees", "0123456789ab");
    const roots = [
      path.resolve("/elsewhere/wt"),
      path.join(base, "widget-issue-42"),
      path.join(root, ".nightgauge", "worktrees", "widget-issue-42"),
      path.join(root, ".worktrees", "issue-42"),
      root,
    ];
    const layouts = roots.map((r, i) => fakeCloneLayout(r, path.resolve(`/gitdirs/${i}`)));
    expect(issueContextCandidates(root, roots[0], "acme/widget", 42, base)).toEqual(
      layouts.map((l) => path.join(l.pipeline, "issue-42.json"))
    );
  });

  it("strips owner/ from the repo for the Go layout leaf", () => {
    const root = path.resolve("/repo-strip");
    const bare = fakeCloneLayout(
      path.join(root, ".nightgauge", "worktrees", "widget-issue-42"),
      path.resolve("/gitdirs/bare")
    );
    fakeCloneLayout(
      path.join(root, ".nightgauge", "worktrees", "acme", "widget-issue-42"),
      path.resolve("/gitdirs/nested")
    );
    const got = issueContextCandidates(root, "", "acme/widget", 42, "");
    expect(got).toEqual([path.join(bare.pipeline, "issue-42.json")]);
  });

  it("pipelineFileCandidates keeps the order and swaps only the filename", () => {
    const root = repo();
    const other = repo();
    const ctx = issueContextCandidates(root, other, "widget", 42);
    const planning = pipelineFileCandidates(root, other, "widget", 42, "planning-42.json");
    expect(planning).toHaveLength(ctx.length);
    planning.forEach((p, i) => {
      expect(path.dirname(p)).toBe(path.dirname(ctx[i]));
      expect(path.basename(p)).toBe("planning-42.json");
    });
  });
});

describe("issueContextCandidates — the Go worktree base outside the tree (#2038)", () => {
  const base = path.join(path.sep + "state", "worktrees", "fedcba987654");

  it("uses the binary's cached answer when no base is passed", () => {
    const root = path.resolve("/repo-cached");
    const wt = fakeCloneLayout(path.join(base, "widget-issue-42"), path.resolve("/gitdirs/wt"));
    setCachedWorktreeBaseForTest(root, base);
    try {
      expect(pipelineFileCandidates(root, "", "acme/widget", 42, "planning-42.json")).toEqual([
        path.join(wt.pipeline, "planning-42.json"),
      ]);
    } finally {
      setCachedWorktreeBaseForTest(root, undefined);
    }
  });

  it("never builds a candidate from a repo name that could escape the base", () => {
    const root = path.resolve("/repo-escape");
    // Map every root an unchecked leaf could produce, so a refused name would
    // show up as a candidate if the refusal were missing.
    for (const leaf of ["..-issue-42", "a b-issue-42", "widget-issue-42"]) {
      fakeCloneLayout(path.join(base, leaf), path.resolve("/escaped"));
    }
    fakeCloneLayout(path.dirname(base), path.resolve("/escaped"));
    for (const repoName of ["acme/..", "..", "acme/a b"]) {
      const got = issueContextCandidates(root, "", repoName, 42, base);
      expect(got.some((p) => p.startsWith(path.resolve("/escaped")))).toBe(false);
    }
  });
});
