/**
 * Mirrors internal/execution/issue_context_paths_test.go.
 *
 * The two lists have to agree byte for byte: a TypeScript list that merely
 * looks right misses the file on exactly the runs it was added for, and the
 * failure is silent — an empty tree section, not an error (#1206).
 */
import { describe, it, expect } from "vitest";
import * as path from "node:path";
import {
  issueContextCandidates,
  issueContextRelPath,
  pipelineFileCandidates,
} from "../../src/utils/issueContextCandidates";

describe("issueContextCandidates (#1206, mirrors Go #994)", () => {
  it("covers both worktree layouts and the plain repo root", () => {
    const got = issueContextCandidates("/repo", "", "acme/widget", 42);

    // Go manager layout — must match worktreePath's construction exactly, or
    // the search misses every worktree the Go scheduler creates.
    expect(got).toContain(
      path.join(
        "/repo",
        ".nightgauge",
        "worktrees",
        "widget-issue-42",
        ".nightgauge",
        "pipeline",
        "issue-42.json"
      )
    );
    // VSCode extension layout.
    expect(got).toContain(
      path.join("/repo", ".worktrees", "issue-42", ".nightgauge", "pipeline", "issue-42.json")
    );
    // A run that never took a worktree.
    expect(got).toContain(path.join("/repo", ".nightgauge", "pipeline", "issue-42.json"));
  });

  it("puts a known worktree first — most-specific wins", () => {
    const got = issueContextCandidates("/repo", "/elsewhere/wt", "acme/widget", 42);
    expect(got[0]).toBe(path.join("/elsewhere/wt", ".nightgauge", "pipeline", "issue-42.json"));
  });

  it("strips owner/ from the repo for the Go layout leaf", () => {
    // The leaf is the BARE repo name; "acme/widget-issue-42" is a directory
    // that never exists. Compared as a whole path, not a substring — an
    // `includes` here would be the ambient-path shape #426 forbids.
    const goLayout = path.join(
      "/repo",
      ".nightgauge",
      "worktrees",
      "widget-issue-42",
      ".nightgauge",
      "pipeline",
      "issue-42.json"
    );
    const nested = path.join(
      "/repo",
      ".nightgauge",
      "worktrees",
      "acme",
      "widget-issue-42",
      ".nightgauge",
      "pipeline",
      "issue-42.json"
    );
    const got = issueContextCandidates("/repo", "", "acme/widget", 42);
    expect(got).toContain(goLayout);
    expect(got).not.toContain(nested);
  });

  it("accepts a bare repo name", () => {
    const got = issueContextCandidates("/repo", "", "widget", 42);
    expect(got).toContain(
      path.join(
        "/repo",
        ".nightgauge",
        "worktrees",
        "widget-issue-42",
        ".nightgauge",
        "pipeline",
        "issue-42.json"
      )
    );
  });

  it("skips the Go layout when the repo is unknown rather than emitting -issue-N", () => {
    const got = issueContextCandidates("/repo", "", "", 42);
    expect(got).toEqual([
      path.join("/repo", ".worktrees", "issue-42", ".nightgauge", "pipeline", "issue-42.json"),
      path.join("/repo", ".nightgauge", "pipeline", "issue-42.json"),
    ]);
  });

  it("deduplicates when the worktree is also a derived candidate", () => {
    const wt = path.join("/repo", ".worktrees", "issue-42");
    const got = issueContextCandidates("/repo", wt, "widget", 42);
    expect(new Set(got).size).toBe(got.length);
  });

  it("returns nothing when there is no root and no worktree", () => {
    expect(issueContextCandidates("", "", "widget", 42)).toEqual([]);
  });

  it("issueContextRelPath is the shape every writer uses", () => {
    expect(issueContextRelPath(42)).toBe(path.join(".nightgauge", "pipeline", "issue-42.json"));
  });

  it("pipelineFileCandidates keeps the order and swaps only the filename", () => {
    const ctx = issueContextCandidates("/repo", "", "widget", 42);
    const planning = pipelineFileCandidates("/repo", "", "widget", 42, "planning-42.json");
    expect(planning).toHaveLength(ctx.length);
    planning.forEach((p, i) => {
      expect(path.dirname(p)).toBe(path.dirname(ctx[i]));
      expect(path.basename(p)).toBe("planning-42.json");
    });
  });
});
