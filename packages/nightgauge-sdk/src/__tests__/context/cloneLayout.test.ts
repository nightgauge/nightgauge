/**
 * cloneLayout tests (ADR-024 § 7, #2037).
 *
 * Every test builds its own temporary repository, so nothing resolves against
 * this checkout's git directory.
 */
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import * as child_process from "child_process";
import * as fs from "fs";
import * as os from "os";
import * as path from "path";

vi.mock("child_process", async (importOriginal) => {
  const actual = await importOriginal<typeof import("child_process")>();
  return {
    ...actual,
    execFileSync: vi.fn(actual.execFileSync),
    execFile: vi.fn(actual.execFile),
  };
});

type CloneLayoutModule = typeof import("../../context/cloneLayout.js");
type ChildProcessModule = typeof import("child_process");

// The hermetic setup file already loaded cloneLayout with the real
// child_process; load a fresh copy that sees the spies.
let CL: CloneLayoutModule;
let actualChild: ChildProcessModule;
beforeAll(async () => {
  vi.resetModules();
  CL = await import("../../context/cloneLayout.js");
  actualChild = await vi.importActual<ChildProcessModule>("child_process");
});

function git(cwd: string, ...args: string[]): void {
  const env = { ...process.env };
  for (const k of ["GIT_DIR", "GIT_WORK_TREE", "GIT_COMMON_DIR", "GIT_INDEX_FILE"]) delete env[k];
  actualChild.execFileSync(
    "git",
    ["-c", "user.name=t", "-c", "user.email=t@example.invalid", ...args],
    { cwd, env, stdio: "ignore" }
  );
}

const tmpDirs: string[] = [];

function tmp(prefix: string): string {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), prefix));
  tmpDirs.push(dir);
  return dir;
}

function repo(): string {
  const dir = tmp("clonelayout-repo-");
  git(dir, "init", "-q");
  return dir;
}

beforeEach(() => {
  CL.clearCloneLayoutCache();
  vi.mocked(child_process.execFileSync).mockClear();
  vi.mocked(child_process.execFile).mockClear();
});

afterEach(() => {
  CL.clearCloneLayoutCache();
  for (const d of tmpDirs.splice(0)) fs.rmSync(d, { recursive: true, force: true });
});

describe("CL.resolveCloneLayout", () => {
  it("resolves every class under <realpath>/.git/nightgauge", () => {
    const root = repo();
    const layout = CL.resolveCloneLayout(root);
    const common = path.join(fs.realpathSync(root), ".git");
    expect(layout.root).toBe(root);
    expect(layout.gitCommonDir).toBe(common);
    expect(layout.clone).toBe(path.join(common, "nightgauge"));
    for (const cls of CL.CLONE_CLASSES) {
      expect(layout[cls]).toBe(path.join(common, "nightgauge", cls));
    }
  });

  it("creates CLONE with mode 0700 and no class directories", () => {
    const root = repo();
    const layout = CL.resolveCloneLayout(root);
    const st = fs.statSync(layout.clone);
    expect(st.isDirectory()).toBe(true);
    expect(st.mode & 0o777).toBe(0o700);
    for (const cls of CL.CLONE_CLASSES) expect(fs.existsSync(layout[cls])).toBe(false);
  });

  it("resolves a linked worktree to the main clone's directory and its own checkout", () => {
    const root = repo();
    git(root, "commit", "-q", "--allow-empty", "-m", "init");
    const wt = path.join(tmp("clonelayout-wt-"), "wt");
    git(root, "worktree", "add", "-q", wt);
    const main = CL.resolveCloneLayout(root);
    const linked = CL.resolveCloneLayout(wt);
    expect(linked.root).toBe(wt);
    expect(linked.gitCommonDir).toBe(main.gitCommonDir);
    expect(linked.clone).toBe(main.clone);
    expect(main.gitDir).toBe(main.gitCommonDir);
    expect(main.checkout).toBe(path.join(main.gitCommonDir, "nightgauge-worktree"));
    expect(linked.gitDir).toBe(path.join(main.gitCommonDir, "worktrees", "wt"));
    expect(linked.checkout).toBe(path.join(linked.gitDir, "nightgauge-worktree"));
    expect(fs.statSync(linked.checkout).mode & 0o777).toBe(0o700);
    expect(CL.checkoutPath("runState", wt)).toBe(path.join(linked.checkout, "run-state.json"));
    expect(linked.pipeline).toBe(main.pipeline);
  });

  it("throws CL.NotAGitRepositoryError outside a repository, and caches it", () => {
    const dir = tmp("clonelayout-norepo-");
    // A ceiling keeps git from discovering a repository above the temp dir.
    const prev = process.env.GIT_CEILING_DIRECTORIES;
    process.env.GIT_CEILING_DIRECTORIES = path.dirname(fs.realpathSync(dir));
    try {
      expect(() => CL.resolveCloneLayout(dir)).toThrow(CL.NotAGitRepositoryError);
      expect(() => CL.resolveCloneLayout(dir)).toThrow(/not a git repository/);
    } finally {
      if (prev === undefined) delete process.env.GIT_CEILING_DIRECTORIES;
      else process.env.GIT_CEILING_DIRECTORIES = prev;
    }
    expect(child_process.execFileSync).toHaveBeenCalledTimes(1);
  });

  it("throws a fresh error per call from the cache, so each stack names its caller", () => {
    const dir = tmp("clonelayout-norepo-");
    const prev = process.env.GIT_CEILING_DIRECTORIES;
    process.env.GIT_CEILING_DIRECTORIES = path.dirname(fs.realpathSync(dir));
    const thrown: unknown[] = [];
    try {
      for (let i = 0; i < 2; i++) {
        try {
          CL.resolveCloneLayout(dir);
        } catch (e) {
          thrown.push(e);
        }
      }
    } finally {
      if (prev === undefined) delete process.env.GIT_CEILING_DIRECTORIES;
      else process.env.GIT_CEILING_DIRECTORIES = prev;
    }
    expect(thrown).toHaveLength(2);
    expect(thrown[1]).toBeInstanceOf(CL.NotAGitRepositoryError);
    expect(thrown[1]).not.toBe(thrown[0]);
    expect((thrown[1] as Error).message).toBe((thrown[0] as Error).message);
  });

  it("ignores GIT_DIR in the environment", () => {
    const root = repo();
    const other = repo();
    const prev = process.env.GIT_DIR;
    process.env.GIT_DIR = path.join(other, ".git");
    try {
      const layout = CL.resolveCloneLayout(root);
      expect(layout.gitCommonDir).toBe(path.join(fs.realpathSync(root), ".git"));
    } finally {
      if (prev === undefined) delete process.env.GIT_DIR;
      else process.env.GIT_DIR = prev;
    }
  });

  it("runs git once per root; later calls are cache hits", () => {
    const root = repo();
    const first = CL.resolveCloneLayout(root);
    const second = CL.resolveCloneLayout(root);
    expect(second).toBe(first);
    expect(child_process.execFileSync).toHaveBeenCalledTimes(1);
  });

  it("refuses a CLONE that is a symlink", () => {
    const root = repo();
    const target = tmp("clonelayout-target-");
    fs.symlinkSync(target, path.join(root, ".git", "nightgauge"));
    expect(() => CL.resolveCloneLayout(root)).toThrow(/symlink/);
  });
});

describe("CL.primeCloneLayout", () => {
  it("fills the cache so the synchronous resolver never spawns git", async () => {
    const root = repo();
    const primed = await CL.primeCloneLayout(root);
    expect(CL.resolveCloneLayout(root)).toBe(primed);
    expect(child_process.execFile).toHaveBeenCalledTimes(1);
    expect(child_process.execFileSync).not.toHaveBeenCalled();
  });
});

describe("CL.setCloneLayout / CL.cloneLayoutFromJson", () => {
  const json = JSON.stringify({
    schema_version: 2,
    root: "/r",
    git_common_dir: "/r/.git",
    git_dir: "/r/.git",
    clone: "/r/.git/nightgauge",
    pipeline: "/r/.git/nightgauge/pipeline",
    plans: "/r/.git/nightgauge/plans",
    retros: "/r/.git/nightgauge/retros",
    logs: "/r/.git/nightgauge/logs",
    checkout: "/r/.git/nightgauge-worktree",
    state: "/s",
    cache: "/c",
    runtime: "/rt",
  });

  it("parses `nightgauge layout` JSON", () => {
    expect(CL.cloneLayoutFromJson(json)).toEqual({
      root: "/r",
      gitCommonDir: "/r/.git",
      gitDir: "/r/.git",
      clone: "/r/.git/nightgauge",
      pipeline: "/r/.git/nightgauge/pipeline",
      plans: "/r/.git/nightgauge/plans",
      retros: "/r/.git/nightgauge/retros",
      logs: "/r/.git/nightgauge/logs",
      checkout: "/r/.git/nightgauge-worktree",
    });
  });

  // Parity with the Go binary: the fixture the Go `nightgauge layout` test
  // checks the real output against derives every path field from the git
  // common dir and the checkout's git dir. JSON built from it must parse to
  // exactly what cloneLayoutFor derives, and CHECKOUT_ENTRIES must name the
  // Go per-checkout entries.
  it("matches the shared Go layout fixture", () => {
    const fixture = JSON.parse(
      fs.readFileSync(
        path.resolve(__dirname, "../../../../../internal/layout/testdata/layout-fields.json"),
        "utf8"
      )
    ) as {
      schema_version: number;
      fields: Record<string, string>;
      checkout_entries: Record<string, string>;
    };
    const common = "/x/repo/.git";
    for (const gitDir of [common, `${common}/worktrees/wt`]) {
      const out: Record<string, unknown> = {
        schema_version: fixture.schema_version,
        root: "/x/wt",
      };
      for (const [field, tmpl] of Object.entries(fixture.fields)) {
        out[field] = tmpl.replace("{common}", common).replace("{gitdir}", gitDir);
      }
      expect(CL.cloneLayoutFromJson(JSON.stringify(out))).toEqual(
        CL.cloneLayoutFor("/x/wt", common, gitDir)
      );
    }
    expect({ ...CL.CHECKOUT_ENTRIES }).toEqual(fixture.checkout_entries);
  });

  it("rejects JSON missing a field", () => {
    const raw = JSON.parse(json) as Record<string, unknown>;
    delete raw.plans;
    expect(() => CL.cloneLayoutFromJson(JSON.stringify(raw))).toThrow(/missing "plans"/);
  });

  it("a set layout is returned without spawning git", () => {
    const layout = CL.cloneLayoutFromJson(json);
    CL.setCloneLayout("/r", layout);
    expect(CL.resolveCloneLayout("/r")).toBe(layout);
    expect(child_process.execFileSync).not.toHaveBeenCalled();
  });
});

describe("resolveCloneLayout: a root that does not exist", () => {
  it("says so, and does not cache the answer", () => {
    const parent = fs.mkdtempSync(path.join(os.tmpdir(), "ng-clone-missing-"));
    const root = path.join(parent, "later");
    try {
      expect(() => CL.resolveCloneLayout(root)).toThrow(/does not exist/);
      fs.mkdirSync(root);
      git(root, "init", "-q");
      expect(CL.resolveCloneLayout(root).clone).toBe(
        path.join(fs.realpathSync(root), ".git", "nightgauge")
      );
    } finally {
      fs.rmSync(parent, { recursive: true, force: true });
    }
  });
});
