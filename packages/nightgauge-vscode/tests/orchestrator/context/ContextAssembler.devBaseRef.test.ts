/**
 * The deterministic dev handoff, against the RIGHT base ref (#1241).
 *
 * `generateDeterministicDevContext` writes `dev-{N}.json` when feature-dev did
 * not. Its file list came from `git diff --name-status <base>...HEAD` where
 * `<base>` was the bare local branch (`main`), with `HEAD~1` as the fallback.
 * Both readings answer a different question than the one the deliverable
 * claims to answer, and both over-report:
 *
 *   - A local `main` in a long-lived checkout lags `origin/main` by every
 *     commit merged since the last pull, so the diff lists OTHER issues'
 *     already-merged files as this stage's output.
 *   - `HEAD~1` names whatever the previous commit is — in a fresh worktree, a
 *     stranger's merge commit.
 *
 * That is not a cosmetic inaccuracy. In the specimen run feature-dev
 * correctly produced nothing (the issue was a legal review awaiting counsel);
 * this generator diffed against a `main` five squash-merges stale and stamped a
 * handoff claiming six modified files. The Go gate's ground truth resolves
 * `origin/main` FIRST (`internal/ci.DefaultDiffBases`), saw the empty tree,
 * read the fabricated claim, and convicted the stage of
 * `dev_produced_no_changes` — an agent-class terminal that halted the entire
 * repository over a receipt this function wrote.
 *
 * These tests use a REAL git repository rather than a mocked `execFile`,
 * because the defect is entirely about which ref git resolves: a mock that
 * returns canned output for `main...HEAD` would pass against the buggy code and
 * the fixed code alike, and assert nothing.
 *
 * RED-PROOFS (each leaves the code compiling):
 *   A. `const diffBases = [baseBranch];` (drop the remote-first entry)
 *      → "ignores a stale local base" goes red: six files, not zero. OBSERVED.
 *   B. `if (resolvedBase === null) resolvedBase = "HEAD~1";`
 *      → "records nothing when no base resolves" goes red. OBSERVED.
 *
 * @see Issue #1241
 */

import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import * as fs from "fs";
import * as os from "os";
import * as path from "path";
import { execFileSync } from "child_process";

vi.mock("../../../src/utils/skillRunner", () => ({
  findSkillFile: vi.fn().mockReturnValue(null),
  runStageSkillHeadless: vi.fn(),
}));

import { ContextAssembler } from "../../../src/orchestrator/context/ContextAssembler";
import type { Logger } from "../../../src/utils/logger";

const ISSUE = 1241;

function makeLogger(): Logger {
  return {
    info: vi.fn(),
    warn: vi.fn(),
    error: vi.fn(),
    debug: vi.fn(),
  } as unknown as Logger;
}

function git(cwd: string, ...args: string[]): string {
  return execFileSync("git", args, {
    cwd,
    encoding: "utf-8",
    env: {
      ...process.env,
      GIT_AUTHOR_NAME: "t",
      GIT_AUTHOR_EMAIL: "t@example.com",
      GIT_COMMITTER_NAME: "t",
      GIT_COMMITTER_EMAIL: "t@example.com",
    },
  });
}

function write(root: string, rel: string, body: string): void {
  const full = path.join(root, rel);
  fs.mkdirSync(path.dirname(full), { recursive: true });
  fs.writeFileSync(full, body, "utf-8");
}

/**
 * A checkout in the shape that produced #1241: `origin/main` is the true tip,
 * the LOCAL `main` is several merges behind it, and the run's branch sits
 * exactly at `origin/main` having changed nothing.
 *
 * Built with a real `origin` remote rather than a hand-written ref so that
 * `origin/main` resolves the way it does in production.
 */
function staleLocalMainCheckout(): string {
  const tmp = fs.mkdtempSync(path.join(os.tmpdir(), "ng-1241-"));
  const origin = path.join(tmp, "origin");
  const work = path.join(tmp, "work");

  fs.mkdirSync(origin, { recursive: true });
  git(origin, "init", "--quiet", "--initial-branch=main", ".");
  write(origin, "README.md", "seed\n");
  git(origin, "add", "-A");
  git(origin, "commit", "--quiet", "-m", "seed");

  git(tmp, "clone", "--quiet", origin, work);

  // Five merges land on origin AFTER this checkout last pulled — the files
  // the fabricated handoff claimed in the specimen run.
  // Six paths, matching the count the fabricated handoff claimed in the
  // specimen run. The names are deliberately generic: what matters is that
  // they belong to commits this branch did not make, not what they are.
  const landed = [
    "config/build.yml",
    "config/release.yml",
    "src/content/pricing.md",
    "src/content/privacy.md",
    "src/content/terms.md",
    "src/pages/community.astro",
  ];
  for (const f of landed) {
    write(origin, f, `landed: ${f}\n`);
    git(origin, "add", "-A");
    git(origin, "commit", "--quiet", "-m", `feat: ${f}`);
  }

  // The working checkout fetches (so `origin/main` is current) but never
  // fast-forwards its own `main` — the ordinary state of a long-lived clone.
  git(work, "fetch", "--quiet", "origin");
  git(work, "checkout", "--quiet", "-b", `feat/${ISSUE}-legal-review`, "origin/main");

  return work;
}

let repo: string | null = null;

afterEach(() => {
  if (repo) {
    fs.rmSync(path.dirname(repo), { recursive: true, force: true });
    repo = null;
  }
  vi.restoreAllMocks();
});

/** Run the generator against `root` and return the `files_changed` it wrote. */
async function generatedFilesChanged(
  root: string
): Promise<{ created: string[]; modified: string[]; deleted: string[] }> {
  const assembler = new ContextAssembler(makeLogger(), () => root, null);
  const ok = await (
    assembler as unknown as {
      generateDeterministicDevContext(n: number): Promise<boolean>;
    }
  ).generateDeterministicDevContext(ISSUE);
  expect(ok).toBe(true);

  const written = JSON.parse(
    fs.readFileSync(path.join(root, ".nightgauge", "pipeline", `dev-${ISSUE}.json`), "utf-8")
  ) as { files_changed: { created: string[]; modified: string[]; deleted: string[] } };
  return written.files_changed;
}

describe("generateDeterministicDevContext — base ref (#1241)", () => {
  beforeEach(() => {
    repo = staleLocalMainCheckout();
  });

  it("ignores a stale local base and reports the empty tree honestly", async () => {
    // Ground the fixture: the bug is only reproducible while the two refs
    // genuinely disagree, so assert that they do before asserting the fix.
    const staleDiff = git(repo!, "diff", "--name-only", "main...HEAD").trim();
    expect(staleDiff.split("\n").filter(Boolean)).toHaveLength(6);
    expect(git(repo!, "diff", "--name-only", "origin/main...HEAD").trim()).toBe("");

    const filesChanged = await generatedFilesChanged(repo!);

    expect(filesChanged.created).toEqual([]);
    expect(filesChanged.modified).toEqual([]);
    expect(filesChanged.deleted).toEqual([]);
  });

  it("still reports the stage's own work when the branch really did change", async () => {
    write(repo!, "src/pages/community.astro", "the stage's actual edit\n");
    write(repo!, "src/new-thing.ts", "export const x = 1;\n");
    git(repo!, "add", "-A");
    git(repo!, "commit", "--quiet", "-m", `feat(#${ISSUE}): real work`);

    const filesChanged = await generatedFilesChanged(repo!);

    expect(filesChanged.created).toEqual(["src/new-thing.ts"]);
    expect(filesChanged.modified).toEqual(["src/pages/community.astro"]);
  });

  it("records nothing when no base resolves, rather than inventing one from HEAD~1", async () => {
    // No remote, and no local `main` — the "cannot verify" case. `HEAD~1`
    // exists and names a stranger's commit, which is exactly the answer the
    // old fallback gave and the one that must not be given.
    git(repo!, "remote", "remove", "origin");
    git(repo!, "branch", "-D", "main");
    expect(git(repo!, "diff", "--name-only", "HEAD~1").trim()).not.toBe("");

    const filesChanged = await generatedFilesChanged(repo!);

    expect(filesChanged.created).toEqual([]);
    expect(filesChanged.modified).toEqual([]);
    expect(filesChanged.deleted).toEqual([]);
  });
});
