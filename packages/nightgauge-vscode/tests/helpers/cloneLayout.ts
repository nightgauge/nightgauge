/**
 * Test helpers for the per-clone layout (ADR-024 § 7, #2037).
 *
 * Per-clone data lives at `<git-common-dir>/nightgauge/<class>`, and the
 * extension's helpers throw outside a git repository. A test that only needs
 * a deterministic mapping for a temp root uses {@link fakeCloneLayout}, which
 * fills the helper cache without git; a test whose behaviour depends on git
 * uses {@link initGitRepo} for a real repository.
 */

import { execFileSync } from "node:child_process";
import * as fs from "node:fs";
import * as os from "node:os";
import * as path from "node:path";
import { cloneLayoutFor, setCloneLayout, type CloneLayout } from "../../src/utils/cloneLayout";

/**
 * Maps `root` to the layout of a clone whose git directory is `gitDir`
 * (default `<root>/.git`), so every helper returns `<gitDir>/nightgauge/<class>`
 * for it without running git. Nothing is created on disk.
 */
export function fakeCloneLayout(root: string, gitDir = path.join(root, ".git")): CloneLayout {
  const layout = cloneLayoutFor(root, gitDir);
  setCloneLayout(root, layout);
  return layout;
}

/**
 * {@link fakeCloneLayout} plus the four class directories created on disk,
 * for tests that write fixtures where the code under test reads them.
 */
export function mkFakeCloneLayout(root: string, gitDir = path.join(root, ".git")): CloneLayout {
  const layout = fakeCloneLayout(root, gitDir);
  for (const dir of [layout.pipeline, layout.plans, layout.retros, layout.logs]) {
    fs.mkdirSync(dir, { recursive: true });
  }
  return layout;
}

/** Runs git in `cwd` with the inherited repository-location variables cleared. */
export function git(cwd: string, ...args: string[]): string {
  const env = { ...process.env };
  for (const k of ["GIT_DIR", "GIT_WORK_TREE", "GIT_COMMON_DIR", "GIT_INDEX_FILE"]) {
    delete env[k];
  }
  return execFileSync("git", args, { cwd, env, encoding: "utf8" });
}

/**
 * Makes `dir` (default: a fresh temp dir) a git repository and returns its
 * symlink-evaluated path, which is what the resolver reports.
 */
export function initGitRepo(dir?: string): string {
  const root = dir ?? fs.mkdtempSync(path.join(os.tmpdir(), "ng-clone-"));
  fs.mkdirSync(root, { recursive: true });
  git(root, "init", "-q");
  return fs.realpathSync(root);
}
