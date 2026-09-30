/**
 * Unit tests for the clone layout helpers (#2036, #2037).
 *
 * Per-clone data lives at `<git-common-dir>/nightgauge/<class>` (ADR-024
 * § 7): `<repo>/.git/nightgauge/<class>` for a normal clone, and the main
 * clone's directory from a linked worktree. The parity suite pins these
 * helpers to the Go binary's `nightgauge layout`.
 */

import { execFileSync } from "node:child_process";
import * as fs from "node:fs";
import * as os from "node:os";
import * as path from "node:path";
import { afterAll, afterEach, beforeAll, describe, expect, it } from "vitest";
import {
  CHECKOUT_DISPLAY,
  checkoutDir,
  checkoutPath,
  clearCloneLayoutCache,
  cloneLogsDir,
  CLONE_LOGS_DISPLAY,
  isAbsoluteRoot,
  isUsableWorkspaceRoot,
  listCloneFiles,
  pipelineStateDir,
  PIPELINE_STATE_DISPLAY,
  plansDir,
  PLANS_DISPLAY,
  primeCloneLayouts,
  refreshCloneLayouts,
  retrosDir,
  RETROS_DISPLAY,
  setCloneLayoutBinaryResolver,
} from "../../src/utils/cloneLayout";
import { fakeCloneLayout, git, initGitRepo } from "../helpers/cloneLayout";

/**
 * Runs `fn` with git unreachable (an empty PATH), so any git spawn fails:
 * a helper that still answers inside it answered from the cache.
 */
function withoutGit<T>(fn: () => T): T {
  const saved = process.env.PATH;
  process.env.PATH = "";
  try {
    return fn();
  } finally {
    process.env.PATH = saved;
  }
}

const helpers = [
  { name: "pipelineStateDir", fn: pipelineStateDir, cls: "pipeline" },
  { name: "plansDir", fn: plansDir, cls: "plans" },
  { name: "retrosDir", fn: retrosDir, cls: "retros" },
  { name: "cloneLogsDir", fn: cloneLogsDir, cls: "logs" },
] as const;

const tmpDirs: string[] = [];
function tmp(prefix: string): string {
  const d = fs.mkdtempSync(path.join(os.tmpdir(), prefix));
  tmpDirs.push(d);
  return d;
}

/** A fresh repo with one commit (a linked worktree needs a commit to branch from). */
function repoWithCommit(): string {
  const root = initGitRepo(tmp("ng-clone-repo-"));
  fs.writeFileSync(path.join(root, "README"), "x\n");
  git(root, "add", ".");
  git(root, "commit", "-q", "-m", "init");
  return root;
}

afterEach(() => {
  clearCloneLayoutCache();
});

afterAll(() => {
  for (const d of tmpDirs) fs.rmSync(d, { recursive: true, force: true });
});

describe("cloneLayout", () => {
  describe("in a git repository", () => {
    for (const { name, fn, cls } of helpers) {
      it(`${name} returns <realpath(repo)>/.git/nightgauge/${cls}`, () => {
        const root = repoWithCommit();
        expect(fn(root)).toBe(path.join(root, ".git", "nightgauge", cls));
      });
    }

    it("creates CLONE 0700 but not the class directories", () => {
      const root = repoWithCommit();
      pipelineStateDir(root);
      const clone = path.join(root, ".git", "nightgauge");
      expect(fs.statSync(clone).mode & 0o777).toBe(0o700);
      expect(fs.readdirSync(clone)).toEqual([]);
      expect(fs.existsSync(path.join(root, ".nightgauge"))).toBe(false);
    });

    it("gives the main checkout and a linked worktree separate checkout dirs", () => {
      const root = repoWithCommit();
      const wt = path.join(tmp("ng-clone-wt-"), "wt");
      git(root, "worktree", "add", "-q", "-b", "feat-checkout", wt);
      const common = path.join(root, ".git");
      expect(checkoutDir(root)).toBe(path.join(common, "nightgauge-worktree"));
      expect(checkoutDir(wt)).toBe(path.join(common, "worktrees", "wt", "nightgauge-worktree"));
      expect(checkoutPath(wt, "currentRun")).toBe(
        path.join(common, "worktrees", "wt", "nightgauge-worktree", "current-run.json")
      );
      expect(checkoutPath(root, "attention", "cards", "x.json")).toBe(
        path.join(common, "nightgauge-worktree", "attention", "cards", "x.json")
      );
      expect(pipelineStateDir(wt)).toBe(pipelineStateDir(root));
      expect(CHECKOUT_DISPLAY).toBe(".git/nightgauge-worktree");
    });

    it("resolves a linked worktree to the main clone's directory", () => {
      const root = repoWithCommit();
      const wt = path.join(tmp("ng-clone-wt-"), "wt");
      git(root, "worktree", "add", "-q", "-b", "feat", wt);
      for (const { fn, cls } of helpers) {
        expect(fn(wt)).toBe(path.join(root, ".git", "nightgauge", cls));
      }
    });

    it("never runs git on a cache hit", () => {
      const root = repoWithCommit();
      const first = pipelineStateDir(root);
      withoutGit(() => {
        expect(pipelineStateDir(root)).toBe(first);
        for (const { fn, cls } of helpers) {
          expect(fn(root)).toBe(path.join(root, ".git", "nightgauge", cls));
        }
        expect(isUsableWorkspaceRoot(root)).toBe(true);
      });
    });

    it("an uncached root does need git (the cache-hit check is not vacuous)", () => {
      const root = repoWithCommit();
      expect(() => withoutGit(() => pipelineStateDir(root))).toThrow(/not a git repository/);
    });

    it("ignores an inherited GIT_DIR", () => {
      const root = repoWithCommit();
      const other = repoWithCommit();
      const saved = process.env.GIT_DIR;
      process.env.GIT_DIR = path.join(other, ".git");
      try {
        expect(pipelineStateDir(root)).toBe(path.join(root, ".git", "nightgauge", "pipeline"));
      } finally {
        if (saved === undefined) delete process.env.GIT_DIR;
        else process.env.GIT_DIR = saved;
      }
    });
  });

  describe("outside a git repository", () => {
    for (const { name, fn } of helpers) {
      it(`${name} throws "not a git repository"`, () => {
        const dir = tmp("ng-clone-nogit-");
        expect(() => fn(dir)).toThrow(/not a git repository/);
        expect(fs.existsSync(path.join(dir, ".nightgauge"))).toBe(false);
      });
    }

    it("remembers the answer, so repeated calls never re-run git", () => {
      const dir = tmp("ng-clone-nogit-");
      let first = "";
      try {
        pipelineStateDir(dir);
      } catch (e) {
        first = (e as Error).message;
      }
      expect(first).toMatch(/not a git repository/);
      expect(first).not.toMatch(/not installed/);
      withoutGit(() => {
        expect(() => pipelineStateDir(dir)).toThrow(first);
        expect(isUsableWorkspaceRoot(dir)).toBe(false);
      });
    });
  });

  describe("root validation", () => {
    for (const { name, fn } of helpers) {
      it(`${name} rejects an empty, relative or non-string root`, () => {
        expect(() => fn("")).toThrow(/empty/);
        expect(() => fn("   ")).toThrow(/empty/);
        expect(() => fn("relative/repo")).toThrow(/relative/);
        expect(() => fn(".")).toThrow(/relative/);
        expect(() => fn(undefined as unknown as string)).toThrow(/empty/);
      });
    }

    it("isUsableWorkspaceRoot accepts only an absolute path inside a repository", () => {
      expect(isUsableWorkspaceRoot(repoWithCommit())).toBe(true);
      expect(isUsableWorkspaceRoot(tmp("ng-clone-nogit-"))).toBe(false);
      expect(isUsableWorkspaceRoot("")).toBe(false);
      expect(isUsableWorkspaceRoot(undefined)).toBe(false);
      expect(isUsableWorkspaceRoot(null)).toBe(false);
      expect(isUsableWorkspaceRoot("relative/repo")).toBe(false);
    });
  });

  it("fakeCloneLayout maps a root without git", () => {
    const root = path.resolve("/nonexistent/ng-fake-root");
    fakeCloneLayout(root);
    for (const { fn, cls } of helpers) {
      expect(fn(root)).toBe(path.join(root, ".git", "nightgauge", cls));
    }
    expect(isUsableWorkspaceRoot(root)).toBe(true);
  });

  it("the display spellings name the normal clone's git directory", () => {
    expect(PIPELINE_STATE_DISPLAY).toBe(".git/nightgauge/pipeline");
    expect(PLANS_DISPLAY).toBe(".git/nightgauge/plans");
    expect(RETROS_DISPLAY).toBe(".git/nightgauge/retros");
    expect(CLONE_LOGS_DISPLAY).toBe(".git/nightgauge/logs");
  });

  describe("priming", () => {
    it("primes from git when no binary is installed, then never runs git", async () => {
      setCloneLayoutBinaryResolver(async () => null);
      const root = repoWithCommit();
      await primeCloneLayouts([root, "relative/ignored"]);
      withoutGit(() => {
        expect(plansDir(root)).toBe(path.join(root, ".git", "nightgauge", "plans"));
      });
    });

    it.skipIf(process.platform === "win32")(
      "uses the binary's `nightgauge layout` answer when a binary is installed",
      async () => {
        const root = repoWithCommit();
        const gitDir = path.join(tmp("ng-clone-bin-git-"), "common");
        const clone = path.join(gitDir, "nightgauge");
        const json = JSON.stringify({
          schema_version: 2,
          root,
          git_common_dir: gitDir,
          git_dir: gitDir,
          clone,
          pipeline: path.join(clone, "pipeline"),
          plans: path.join(clone, "plans"),
          retros: path.join(clone, "retros"),
          logs: path.join(clone, "logs"),
          checkout: path.join(gitDir, "nightgauge-worktree"),
        });
        const fake = path.join(tmp("ng-clone-bin-"), "nightgauge");
        fs.writeFileSync(fake, `#!/bin/sh\ncat <<'EOF'\n${json}\nEOF\n`, { mode: 0o755 });
        setCloneLayoutBinaryResolver(async () => fake);
        await primeCloneLayouts([root]);
        expect(pipelineStateDir(root)).toBe(path.join(clone, "pipeline"));
        expect(cloneLogsDir(root)).toBe(path.join(clone, "logs"));
        expect(checkoutDir(root)).toBe(path.join(gitDir, "nightgauge-worktree"));
      }
    );

    it("resolves with git when the binary cannot answer", async () => {
      const root = repoWithCommit();
      const fake = path.join(tmp("ng-clone-bin-"), "nightgauge");
      fs.writeFileSync(fake, "#!/bin/sh\nexit 3\n", { mode: 0o755 });
      setCloneLayoutBinaryResolver(async () => fake);
      await primeCloneLayouts([root]);
      withoutGit(() => {
        expect(retrosDir(root)).toBe(path.join(root, ".git", "nightgauge", "retros"));
      });
    });

    it("never rejects for a root outside a repository", async () => {
      setCloneLayoutBinaryResolver(async () => null);
      const dir = tmp("ng-clone-nogit-");
      await expect(primeCloneLayouts([dir])).resolves.toBeUndefined();
      expect(() => pipelineStateDir(dir)).toThrow(/not a git repository/);
    });

    it("refreshCloneLayouts forgets removed roots and re-primes added ones", async () => {
      setCloneLayoutBinaryResolver(async () => null);
      const dir = tmp("ng-clone-late-");
      await primeCloneLayouts([dir]);
      expect(isUsableWorkspaceRoot(dir)).toBe(false);
      git(dir, "init", "-q");
      await refreshCloneLayouts([dir], []);
      expect(pipelineStateDir(dir)).toBe(
        path.join(fs.realpathSync(dir), ".git", "nightgauge", "pipeline")
      );
      await refreshCloneLayouts([], [dir]);
      fs.rmSync(path.join(dir, ".git"), { recursive: true, force: true });
      expect(isUsableWorkspaceRoot(dir)).toBe(false);
    });
  });

  describe("listCloneFiles", () => {
    it("lists matching regular files and tolerates a missing directory", async () => {
      const dir = tmp("ng-clone-list-");
      fs.writeFileSync(path.join(dir, "issue-1.json"), "{}");
      fs.writeFileSync(path.join(dir, "notes.md"), "");
      fs.mkdirSync(path.join(dir, "sub.json"));
      expect(await listCloneFiles(dir, (n) => n.endsWith(".json"))).toEqual([
        path.join(dir, "issue-1.json"),
      ]);
      expect(await listCloneFiles(path.join(dir, "missing"), () => true)).toEqual([]);
    });
  });

  describe("isAbsoluteRoot", () => {
    it("accepts POSIX absolute roots and refuses relative ones", () => {
      expect(isAbsoluteRoot("/repo", path.posix)).toBe(true);
      expect(isAbsoluteRoot("repo", path.posix)).toBe(false);
      expect(isAbsoluteRoot("", path.posix)).toBe(false);
    });

    it("on win32 accepts drive and UNC roots", () => {
      expect(isAbsoluteRoot("C:\\repo", path.win32)).toBe(true);
      expect(isAbsoluteRoot("c:/repo", path.win32)).toBe(true);
      expect(isAbsoluteRoot("\\\\server\\share\\repo", path.win32)).toBe(true);
      expect(isAbsoluteRoot("//server/share", path.win32)).toBe(true);
    });

    it("on win32 refuses drive-relative and relative roots", () => {
      expect(isAbsoluteRoot("\\repo", path.win32)).toBe(false);
      expect(isAbsoluteRoot("/repo", path.win32)).toBe(false);
      expect(isAbsoluteRoot("C:repo", path.win32)).toBe(false);
      expect(isAbsoluteRoot("repo\\sub", path.win32)).toBe(false);
      expect(isAbsoluteRoot("\\\\server", path.win32)).toBe(false);
    });

    it("uses the host path by default", () => {
      expect(isAbsoluteRoot(path.resolve("/tmp"))).toBe(true);
    });
  });
});

/**
 * Parity with the Go binary (ADR-024 § 7, "One path source": a TypeScript
 * reimplementation is allowed only with a parity test). Builds
 * `cmd/nightgauge` into a temp dir and compares `nightgauge layout` with the
 * helpers, for a main checkout and a linked worktree, both from git and from
 * the binary-primed cache. Requires the Go toolchain; skips when absent.
 */
const repoRoot = path.resolve(__dirname, "../../../..");

function isGoAvailable(): boolean {
  try {
    execFileSync("go", ["version"], { stdio: "pipe", cwd: repoRoot });
    return true;
  } catch {
    return false;
  }
}

describe.skipIf(!isGoAvailable())("parity with `nightgauge layout` (Go binary)", () => {
  let bin = "";

  beforeAll(() => {
    const out = tmp("ng-layout-bin-");
    bin = path.join(out, process.platform === "win32" ? "nightgauge.exe" : "nightgauge");
    execFileSync("go", ["build", "-o", bin, "./cmd/nightgauge"], {
      cwd: repoRoot,
      stdio: "pipe",
    });
  }, 600_000);

  afterEach(() => {
    setCloneLayoutBinaryResolver(async () => null);
  });

  function layoutJson(root: string): Record<string, string> {
    return JSON.parse(
      execFileSync(bin, ["layout", "--workdir", root], { cwd: root, encoding: "utf8" })
    ) as Record<string, string>;
  }

  function expectParity(root: string): void {
    const want = layoutJson(root);
    expect(pipelineStateDir(root)).toBe(want.pipeline);
    expect(plansDir(root)).toBe(want.plans);
    expect(retrosDir(root)).toBe(want.retros);
    expect(cloneLogsDir(root)).toBe(want.logs);
  }

  function checkoutAndWorktree(): { main: string; worktree: string } {
    const main = repoWithCommit();
    const worktree = path.join(fs.realpathSync(tmp("ng-layout-wt-")), "wt");
    git(main, "worktree", "add", "-q", "-b", "parity", worktree);
    return { main, worktree };
  }

  it("the git-resolved helpers match the binary for a checkout and a linked worktree", () => {
    const { main, worktree } = checkoutAndWorktree();
    expectParity(main);
    expectParity(worktree);
    expect(pipelineStateDir(worktree)).toBe(pipelineStateDir(main));
  });

  it("priming from the binary yields the same paths without running git", async () => {
    const { main, worktree } = checkoutAndWorktree();
    setCloneLayoutBinaryResolver(async () => bin);
    await primeCloneLayouts([main, worktree]);
    const primed = withoutGit(() =>
      [main, worktree].map((r) => [pipelineStateDir(r), plansDir(r), retrosDir(r), cloneLogsDir(r)])
    );
    expect(primed).toEqual(
      [main, worktree].map((r) => {
        const want = layoutJson(r);
        return [want.pipeline, want.plans, want.retros, want.logs];
      })
    );
  });

  it("the binary also refuses a directory outside a repository", () => {
    const dir = tmp("ng-layout-nogit-");
    expect(() =>
      execFileSync(bin, ["layout", "--workdir", dir], { cwd: dir, stdio: "pipe" })
    ).toThrow(/not a git repository/);
    expect(() => pipelineStateDir(dir)).toThrow(/not a git repository/);
  });
});
