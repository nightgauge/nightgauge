/**
 * Placing a demo tree's per-clone data (#2037, ADR-024 § 7): the target
 * becomes a git repository, each staged class lands at the target's resolved
 * clone layout, and the working-tree files stay where they are.
 */

import { execFileSync } from "node:child_process";
import * as fs from "node:fs";
import * as os from "node:os";
import * as path from "node:path";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import {
  STAGED_CHECKOUT_ENTRIES,
  STAGED_CLONE_CLASSES,
  placeCloneData,
} from "../../demo/workspace-clone";
import {
  checkoutPath,
  clearCloneLayoutCache,
  cloneLogsDir,
  isUsableWorkspaceRoot,
  pipelineStateDir,
  plansDir,
  retrosDir,
} from "../../src/utils/cloneLayout";

let scratch: string;
beforeEach(() => {
  scratch = fs.realpathSync(fs.mkdtempSync(path.join(os.tmpdir(), "workspace-clone-test-")));
});
afterEach(() => {
  clearCloneLayoutCache();
  fs.rmSync(scratch, { recursive: true, force: true });
});

function write(root: string, rel: string, content: string): void {
  fs.mkdirSync(path.dirname(path.join(root, rel)), { recursive: true });
  fs.writeFileSync(path.join(root, rel), content);
}

/** A demo-shaped tree: config and knowledge in the tree, one file per staged class. */
function stagedTree(root: string): void {
  write(root, ".nightgauge/config.yaml", "project: demo\n");
  write(root, ".nightgauge/knowledge/issue-1/PRD.md", "# PRD\n");
  write(root, ".nightgauge/pipeline/state.json", '{"issue":1}\n');
  write(root, ".nightgauge/pipeline/history/2026-01-15.jsonl", '{"n":1}\n');
  write(root, ".nightgauge/plans/1.md", "plan\n");
  write(root, ".nightgauge/retros/1.md", "retro\n");
  write(root, ".nightgauge/logs/run.log", "log\n");
  // Per-checkout entries: a directory, a file, and a run-control singleton
  // staged in the class directory it sat in before ADR-024 § 7.
  write(root, ".nightgauge/health/trends.jsonl", '{"t":1}\n');
  write(root, ".nightgauge/focus.yaml", "focus: 1\n");
  write(root, ".nightgauge/pipeline/current-run.json", '{"issue_number":1}\n');
}

const read = (file: string) => fs.readFileSync(file, "utf8");

describe("placeCloneData", () => {
  it("makes a target outside any repository a git repository", () => {
    const target = path.join(scratch, "target");
    fs.mkdirSync(target);
    // Resolved (and cached) as not a repository before the call.
    expect(isUsableWorkspaceRoot(target)).toBe(false);
    placeCloneData(target);
    const gitDir = execFileSync("git", ["rev-parse", "--absolute-git-dir"], {
      cwd: target,
      encoding: "utf8",
    }).trim();
    expect(gitDir).toBe(path.join(target, ".git"));
    expect(isUsableWorkspaceRoot(target)).toBe(true);
  });

  it("puts each staged class at the target's resolved layout and leaves the rest in the tree", () => {
    const tree = path.join(scratch, "tree");
    const target = path.join(scratch, "target");
    stagedTree(tree);
    fs.mkdirSync(target);
    placeCloneData(tree, target);

    expect(read(path.join(pipelineStateDir(target), "state.json"))).toBe('{"issue":1}\n');
    expect(read(path.join(pipelineStateDir(target), "history", "2026-01-15.jsonl"))).toBe(
      '{"n":1}\n'
    );
    expect(read(path.join(plansDir(target), "1.md"))).toBe("plan\n");
    expect(read(path.join(retrosDir(target), "1.md"))).toBe("retro\n");
    expect(read(path.join(cloneLogsDir(target), "run.log"))).toBe("log\n");
    expect(read(path.join(checkoutPath(target, "health"), "trends.jsonl"))).toBe('{"t":1}\n');
    expect(read(checkoutPath(target, "focus"))).toBe("focus: 1\n");
    expect(read(checkoutPath(target, "currentRun"))).toBe('{"issue_number":1}\n');
    expect(fs.existsSync(path.join(pipelineStateDir(target), "current-run.json"))).toBe(false);
    expect(checkoutPath(target, "focus")).toBe(
      path.join(target, ".git", "nightgauge-worktree", "focus.yaml")
    );

    // The staging paths are gone; working-tree files are untouched.
    for (const [staged] of [...STAGED_CLONE_CLASSES, ...STAGED_CHECKOUT_ENTRIES]) {
      expect(fs.existsSync(path.join(tree, staged))).toBe(false);
    }
    expect(read(path.join(tree, ".nightgauge", "config.yaml"))).toBe("project: demo\n");
    expect(read(path.join(tree, ".nightgauge", "knowledge", "issue-1", "PRD.md"))).toBe("# PRD\n");
    // Nothing lands in the target's working tree: the caller copies the rest.
    expect(fs.readdirSync(target)).toEqual([".git"]);
  });

  it("works in place and merges into an existing clone directory", () => {
    const root = path.join(scratch, "root");
    stagedTree(root);
    placeCloneData(root);
    // A second copy over the same clone (the host tier materializes twice).
    write(root, ".nightgauge/pipeline/state.json", '{"issue":2}\n');
    write(root, ".nightgauge/plans/2.md", "plan 2\n");
    placeCloneData(root);

    expect(read(path.join(pipelineStateDir(root), "state.json"))).toBe('{"issue":2}\n');
    expect(fs.readdirSync(plansDir(root)).sort()).toEqual(["1.md", "2.md"]);
    expect(read(path.join(root, ".nightgauge", "config.yaml"))).toBe("project: demo\n");
    expect(fs.existsSync(path.join(root, ".nightgauge", "pipeline"))).toBe(false);
  });
});
