/**
 * Clone layout — where per-clone data lives (ADR-024 § 1, § 7).
 *
 * `CLONE = <git-common-dir>/nightgauge` holds the four per-clone classes
 * (`pipeline`, `plans`, `retros`, `logs`). Git never tracks its own
 * directory, so nothing here can be committed, and every linked worktree of a
 * clone resolves to the main clone's directory.
 *
 * The Go binary is the path source (`nightgauge layout` prints the same
 * object as JSON). This module is the TypeScript side of that contract for
 * code that cannot ask the binary synchronously: it runs the same git command
 * the Go resolver runs (`git rev-parse --path-format=absolute
 * --git-common-dir`, with the inherited GIT_* location variables cleared) once
 * per root and caches the answer for the life of the process. A parity test
 * pins the two. `setCloneLayout` fills the cache from the binary's JSON (or,
 * in tests, from a fixture), so a caller that primes it never spawns git.
 *
 * Outside a git repository resolution throws {@link NotAGitRepositoryError};
 * no caller falls back to the working tree or the process cwd.
 */

import { execFile, execFileSync } from "child_process";
import * as fs from "fs";
import * as path from "path";

/** The four per-clone classes, in the order `nightgauge layout` reports them. */
export const CLONE_CLASSES = ["pipeline", "plans", "retros", "logs"] as const;
export type CloneClass = (typeof CLONE_CLASSES)[number];

/** CLONE's directory name inside the git common dir. */
export const CLONE_DIR_NAME = "nightgauge";

/** The resolved per-clone layout of one repository root. */
export interface CloneLayout {
  /** The root the layout was resolved for. */
  root: string;
  /** Absolute, symlink-evaluated git common dir. */
  gitCommonDir: string;
  /** `<gitCommonDir>/nightgauge`. */
  clone: string;
  pipeline: string;
  plans: string;
  retros: string;
  logs: string;
}

/** Thrown when a root is not inside a git repository. */
export class NotAGitRepositoryError extends Error {
  constructor(
    public readonly root: string,
    public readonly detail?: string
  ) {
    super(
      `not a git repository: ${root} (per-clone data lives in the git directory, ADR-024 § 7)` +
        (detail ? `: ${detail}` : "")
    );
    this.name = "NotAGitRepositoryError";
  }
}

/** Variables that redirect which repository git reads (ADR-024 § 7). */
const GIT_LOCATION_ENV = [
  "GIT_DIR",
  "GIT_WORK_TREE",
  "GIT_COMMON_DIR",
  "GIT_INDEX_FILE",
  "GIT_OBJECT_DIRECTORY",
];

function gitEnv(): NodeJS.ProcessEnv {
  const env: NodeJS.ProcessEnv = { ...process.env };
  for (const k of GIT_LOCATION_ENV) delete env[k];
  return env;
}

const GIT_ARGS = ["rev-parse", "--path-format=absolute", "--git-common-dir"];

const cache = new Map<string, CloneLayout | NotAGitRepositoryError>();

/** Builds the layout object for a resolved git common dir. */
export function cloneLayoutFor(root: string, gitCommonDir: string): CloneLayout {
  const clone = path.join(gitCommonDir, CLONE_DIR_NAME);
  return {
    root,
    gitCommonDir,
    clone,
    pipeline: path.join(clone, "pipeline"),
    plans: path.join(clone, "plans"),
    retros: path.join(clone, "retros"),
    logs: path.join(clone, "logs"),
  };
}

function fromGitOutput(root: string, stdout: string): CloneLayout {
  let common = stdout.trim();
  if (!common || !path.isAbsolute(common)) {
    throw new NotAGitRepositoryError(root, `git returned ${JSON.stringify(common)}`);
  }
  try {
    common = fs.realpathSync(common);
  } catch {
    // Keep git's answer; it is absolute.
  }
  return cloneLayoutFor(root, path.normalize(common));
}

/**
 * Creates CLONE with mode 0700 when absent (git's group mode when the git
 * common dir is group-writable, i.e. core.sharedRepository) and refuses a
 * CLONE that is a symlink or not a directory (ADR-024 § 17). An unwritable
 * git directory is not an error here: a writer's own error names the path.
 */
function ensureCloneRoot(layout: CloneLayout): void {
  let st: fs.Stats | undefined;
  try {
    st = fs.lstatSync(layout.clone);
  } catch {
    let mode = 0o700;
    try {
      if ((fs.statSync(layout.gitCommonDir).mode & 0o020) !== 0) mode = 0o2770;
    } catch {
      // Default mode.
    }
    try {
      fs.mkdirSync(layout.clone, { mode: mode & 0o777 });
      fs.chmodSync(layout.clone, mode);
    } catch {
      return;
    }
    st = fs.lstatSync(layout.clone);
  }
  if (st.isSymbolicLink() || !st.isDirectory()) {
    throw new Error(
      `unsafe per-clone directory: ${layout.clone} is ${
        st.isSymbolicLink() ? "a symlink" : "not a directory"
      }; remove it`
    );
  }
}

/**
 * A cached failure, re-created per throw: a cached error object keeps the stack
 * of the call that first resolved the root, so every later throw would point at
 * that call instead of at the caller that failed to guard against it.
 */
function rethrowable(hit: Error): Error {
  return hit instanceof NotAGitRepositoryError
    ? new NotAGitRepositoryError(hit.root, hit.detail)
    : hit;
}

function remember(root: string, layout: CloneLayout): CloneLayout {
  ensureCloneRoot(layout);
  cache.set(root, layout);
  return layout;
}

/**
 * Resolves the clone layout for `root` (absolute), synchronously. The first
 * call per root runs git once; every later call is a cache hit. A root that
 * is not in a git repository throws {@link NotAGitRepositoryError}, and that
 * answer is cached too, so an unresolvable root never spawns git per call.
 */
export function resolveCloneLayout(root: string): CloneLayout {
  const hit = cache.get(root);
  if (hit instanceof Error) throw rethrowable(hit);
  if (hit) return hit;
  const missing = missingRoot(root);
  if (missing) throw missing;
  let stdout: string;
  try {
    stdout = execFileSync("git", GIT_ARGS, {
      cwd: root,
      env: gitEnv(),
      encoding: "utf8",
      stdio: ["ignore", "pipe", "pipe"],
      windowsHide: true,
    });
  } catch (err) {
    const e = new NotAGitRepositoryError(root, gitFailureDetail(err));
    cache.set(root, e);
    throw e;
  }
  return remember(root, fromGitOutput(root, stdout));
}

/**
 * The directory of `cls` for the repository containing `cwd` (default: the
 * process working directory). This is the default of every SDK API that used
 * to take a cwd-relative `.nightgauge/<class>` path: outside a git repository
 * it throws {@link NotAGitRepositoryError} instead of writing into the cwd.
 */
export function cloneClassDir(cls: CloneClass, cwd: string = process.cwd()): string {
  return resolveCloneLayout(path.resolve(cwd))[cls];
}

/**
 * Async variant for activation: resolves without blocking the event loop and
 * fills the cache, so the synchronous helpers never spawn git afterwards.
 */
export function primeCloneLayout(root: string): Promise<CloneLayout> {
  const hit = cache.get(root);
  if (hit instanceof Error) return Promise.reject(rethrowable(hit));
  if (hit) return Promise.resolve(hit);
  const missing = missingRoot(root);
  if (missing) return Promise.reject(missing);
  return new Promise((resolve, reject) => {
    execFile(
      "git",
      GIT_ARGS,
      { cwd: root, env: gitEnv(), encoding: "utf8", windowsHide: true },
      (err, stdout) => {
        if (err) {
          const e = new NotAGitRepositoryError(root, gitFailureDetail(err));
          cache.set(root, e);
          reject(e);
          return;
        }
        try {
          resolve(remember(root, fromGitOutput(root, stdout)));
        } catch (e) {
          reject(e);
        }
      }
    );
  });
}

/**
 * Parses `nightgauge layout` JSON into a {@link CloneLayout}. Throws when a
 * required field is missing.
 */
export function cloneLayoutFromJson(json: string): CloneLayout {
  const raw = JSON.parse(json) as Record<string, unknown>;
  const str = (k: string): string => {
    const v = raw[k];
    if (typeof v !== "string" || v === "") {
      throw new Error(`nightgauge layout JSON: missing "${k}"`);
    }
    return v;
  };
  return {
    root: str("root"),
    gitCommonDir: str("git_common_dir"),
    clone: str("clone"),
    pipeline: str("pipeline"),
    plans: str("plans"),
    retros: str("retros"),
    logs: str("logs"),
  };
}

/**
 * Fills the cache for `root` with a layout obtained elsewhere (the binary's
 * `nightgauge layout` JSON at activation, or a test fixture). CLONE is not
 * created here; the first writer creates its class directory.
 */
export function setCloneLayout(root: string, layout: CloneLayout): void {
  cache.set(root, layout);
}

/** Drops the cached layout of `root`, or of every root. */
export function clearCloneLayoutCache(root?: string): void {
  if (root === undefined) cache.clear();
  else cache.delete(root);
}

/**
 * A root that is not an existing directory. Reported as such, not as the
 * spawn's ENOENT ("git is not installed"), and never cached: the directory
 * may be created later in the life of the process.
 */
function missingRoot(root: string): NotAGitRepositoryError | undefined {
  try {
    if (fs.statSync(root).isDirectory()) return undefined;
  } catch {
    // Falls through.
  }
  return new NotAGitRepositoryError(root, "the directory does not exist");
}

function gitFailureDetail(err: unknown): string {
  const e = err as { stderr?: string | Buffer; code?: string; message?: string };
  if (e?.code === "ENOENT") return "git is not installed or not on PATH";
  const stderr = e?.stderr ? String(e.stderr).trim() : "";
  return stderr || e?.message || String(err);
}
