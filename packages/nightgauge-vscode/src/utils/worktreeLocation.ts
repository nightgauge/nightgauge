/**
 * worktreeLocation — where the Go manager puts a run's worktree (ADR-024 § 9).
 *
 * Pipeline worktrees live OUTSIDE the working tree since #2038:
 * `<worktree base>/<repo>-issue-<N>`, where the base is the machine- or
 * local-tier `pipeline.worktree_base`, or, unset, `STATE/worktrees/<repo-key>`.
 * The Go binary is the one resolver (`config.ResolveWorktreeBase`); the
 * extension asks it (`nightgauge worktree base --json`) rather than re-deriving
 * the machine-state root and the per-clone key, so the two cannot disagree.
 *
 * The readers that need the answer (issue-context candidates, the Knowledge
 * view, the attention plan reader) are synchronous, so the answer is cached per
 * repository root: {@link primeWorktreeBase} starts the lookup, and
 * {@link cachedWorktreeBase} returns it once it has arrived. Until then — or
 * when the binary is missing or refuses the configuration — the readers fall
 * back to the layouts they can name without it, never to a guess.
 *
 * @see internal/config/worktree_base.go
 * @see Issue #2038
 */

import { execFile } from "child_process";
import * as path from "path";
import { promisify } from "util";

const execFileAsync = promisify(execFile);

/** Per-call timeout for `nightgauge worktree base`. */
const RESOLVE_TIMEOUT_MS = 10_000;

/**
 * Repo-relative directory the Go manager created worktrees in before #2038.
 * Worktrees still there are read (a run that began there keeps its worktree);
 * nothing new is created there.
 */
export const LEGACY_GO_WORKTREE_BASE = path.join(".nightgauge", "worktrees");

/** Mirrors `layout.WorktreeDirName`'s accepted repository-name shape. */
const REPO_NAME_RE = /^[A-Za-z0-9][A-Za-z0-9._-]*$/;

/**
 * The Go manager's worktree directory name, `<repo>-issue-<N>`, or undefined
 * when `repo` cannot name one (the same refusal `layout.WorktreeDirName` makes,
 * so a crafted repo string never produces a path outside the base).
 */
export function goWorktreeDirName(repo: string, issueNumber: number): string | undefined {
  const slash = repo.lastIndexOf("/");
  const name = slash >= 0 ? repo.slice(slash + 1) : repo;
  if (!REPO_NAME_RE.test(name) || name.includes("..")) return undefined;
  if (!Number.isInteger(issueNumber) || issueNumber <= 0) return undefined;
  return `${name}-issue-${issueNumber}`;
}

/** Resolves the nightgauge binary; null when none is installed. */
export type BinaryPathResolver = () => Promise<string | null>;

let binaryPathResolver: BinaryPathResolver = async () => {
  // Imported lazily: BinaryResolver reads VS Code settings, and the pure
  // path helpers here must stay importable without a VS Code host.
  const { BinaryResolver } = await import("../services/BinaryResolver");
  return BinaryResolver.fromVSCode().resolve();
};

/** Replace how the binary is found (tests, or a host without VS Code). */
export function setWorktreeBaseBinaryResolver(resolver: BinaryPathResolver): void {
  binaryPathResolver = resolver;
}

/** How long a failed lookup is remembered before the next one is tried. */
const FAILURE_RETRY_MS = 60_000;

/** repoRoot → resolved base. */
const resolved = new Map<string, string>();
/** repoRoot → when the last lookup failed. */
const failedAt = new Map<string, number>();
const inflight = new Map<string, Promise<string | undefined>>();

function key(repoRoot: string): string {
  return path.resolve(repoRoot);
}

/**
 * The resolved worktree base for `repoRoot` if a lookup has completed, else
 * undefined. Never blocks and never spawns.
 */
export function cachedWorktreeBase(repoRoot: string): string | undefined {
  if (!repoRoot) return undefined;
  return resolved.get(key(repoRoot));
}

/**
 * Ask the binary for `repoRoot`'s worktree base. Resolves to undefined when
 * there is no binary, the directory is not a checkout, or the configuration is
 * refused (retried after a minute); the error is the binary's to report at
 * worktree creation, not a reader's.
 */
export function resolveWorktreeBase(repoRoot: string): Promise<string | undefined> {
  if (!repoRoot || !path.isAbsolute(repoRoot)) return Promise.resolve(undefined);
  const k = key(repoRoot);
  const known = resolved.get(k);
  if (known) return Promise.resolve(known);
  const failed = failedAt.get(k);
  if (failed !== undefined && Date.now() - failed < FAILURE_RETRY_MS) {
    return Promise.resolve(undefined);
  }
  const pending = inflight.get(k);
  if (pending) return pending;
  const lookup = (async () => {
    let base: string | undefined;
    try {
      const bin = await binaryPathResolver();
      if (bin) {
        const { stdout } = await execFileAsync(
          bin,
          ["worktree", "base", "--workdir", k, "--json"],
          { timeout: RESOLVE_TIMEOUT_MS, windowsHide: true }
        );
        const parsed = JSON.parse(stdout) as { base?: unknown };
        if (typeof parsed.base === "string" && path.isAbsolute(parsed.base)) {
          base = parsed.base;
        }
      }
    } catch {
      base = undefined;
    }
    if (base) {
      resolved.set(k, base);
      failedAt.delete(k);
    } else {
      failedAt.set(k, Date.now());
    }
    inflight.delete(k);
    return base;
  })();
  inflight.set(k, lookup);
  return lookup;
}

/** Start {@link resolveWorktreeBase} for each root without awaiting it. */
export function primeWorktreeBase(...repoRoots: string[]): void {
  for (const root of repoRoots) {
    void resolveWorktreeBase(root);
  }
}

/** Test seam: set (or, with undefined, forget) the cached base for a root. */
export function setCachedWorktreeBaseForTest(repoRoot: string, base: string | undefined): void {
  if (base === undefined) resolved.delete(key(repoRoot));
  else resolved.set(key(repoRoot), base);
  failedAt.delete(key(repoRoot));
}

/** Test seam: forget every cached and in-flight lookup. */
export function clearWorktreeBaseCache(): void {
  resolved.clear();
  failedAt.clear();
  inflight.clear();
}
