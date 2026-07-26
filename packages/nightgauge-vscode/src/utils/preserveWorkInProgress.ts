/**
 * Work-in-progress preservation for terminated stages (Issue #128).
 *
 * ## Why this exists
 *
 * `feature-dev` never commits — the commit lives in `feature-validate` Phase 5
 * (Issue #1608). So for the whole of feature-dev, and for any stage between
 * its first edit and its commit, the ENTIRE deliverable exists only as
 * uncommitted changes in the worktree.
 *
 * Every guard that terminates a stage (progress-runaway, idle-stall, hard-cap,
 * quota fast-fail, autonomous abort) therefore lands on work that nothing
 * downstream can see: `feature-validate` inspects the branch, finds nothing it
 * recognises as implementation, and the run is recorded as a failure while a
 * complete, correct implementation sits on disk waiting to be pruned with the
 * worktree.
 *
 * The observed incident (#128) cost $5.65 and a 36-line test plus a ~194-line
 * harness addition that had to be salvaged by hand.
 *
 * ## What this does
 *
 * When a guard kill fires with a dirty worktree, commit that worktree to the
 * stage branch before the process is reaped. The commit is:
 *
 *   - **local only** — no push, no network, no credentials;
 *   - **hook-free** (`--no-verify`) and **signature-free**
 *     (`-c commit.gpgsign=false`) so it cannot hang or fail on a pre-commit
 *     hook or a GPG pinentry prompt at kill time;
 *   - **refused on `main` / `master`** and on a detached HEAD, where a WIP
 *     commit would be either dangerous or unreachable;
 *   - **anchored by a durable ref** under `refs/nightgauge/wip/`. This matters:
 *     re-dispatching an issue force-removes the worktree and runs
 *     `git branch -D <branch>` before re-creating the branch from
 *     `origin/<base>` (`WorktreeManager.create`), which would make a
 *     branch-only commit unreachable and eventually collectable. The extra ref
 *     lives in the shared object store, survives both operations, and makes
 *     the salvage a `git log refs/nightgauge/wip/` away.
 *
 * `feature-validate`'s Phase 5 composes with it unchanged: a dirty tree still
 * takes the stage-and-commit path, and a clean tree with commits ahead of base
 * takes the reuse-and-push path.
 *
 * This is the safety net that makes the whole class of guard kill
 * non-destructive regardless of how the guards are tuned — a mis-tuned kill
 * costs a retry, never the work.
 */

import { execFile } from "child_process";
import { promisify } from "util";

// #2884: never a sync subprocess — this runs on the extension host event loop.
const execFileAsync = promisify(execFile);

/** Branches a WIP commit must never be written to. */
const PROTECTED_BRANCHES: ReadonlySet<string> = new Set(["main", "master"]);

/** Per-git-invocation timeout. Generous for a large tree, bounded for a kill path. */
const GIT_TIMEOUT_MS = 15_000;

/** Trailer that marks a commit as machine-authored WIP preservation. */
export const WIP_COMMIT_TRAILER = "Nightgauge-WIP";

/**
 * Namespace for the durable salvage refs. Outside `refs/heads/`, so
 * `git branch -D` on the stage branch cannot orphan the commit, and outside
 * `refs/remotes/`, so nothing tries to push or prune it.
 */
export const WIP_REF_NAMESPACE = "refs/nightgauge/wip";

export type PreserveWipOutcome =
  /** A WIP commit was created on the stage branch. */
  | "committed"
  /** Nothing to preserve — the worktree was clean. */
  | "clean"
  /** `cwd` is missing or is not inside a git work tree. */
  | "not-a-repo"
  /** HEAD is a protected branch — refusing to write a WIP commit there. */
  | "protected-branch"
  /** HEAD is detached — a commit here would be unreachable. */
  | "detached-head"
  /** git rejected the stage or the commit (index lock, identity, ...). */
  | "failed";

export interface PreserveWipResult {
  outcome: PreserveWipOutcome;
  /** One-line, log-ready explanation. Always populated. */
  detail: string;
  /** Set when `outcome === "committed"`. */
  commitSha?: string;
  /**
   * Durable salvage ref pointing at {@link commitSha}, when one could be
   * written. Survives the `git branch -D` that re-dispatch performs.
   */
  preservedRef?: string;
  /** Branch HEAD pointed at, when it could be resolved. */
  branch?: string;
  /** Number of dirty paths observed before staging. */
  filesChanged?: number;
}

/**
 * Whether a finished stage's uncommitted worktree must be preserved (#128).
 *
 * TRUE only for a GUARD KILL: the pipeline terminated the process
 * (progress-runaway sets `costCapExceeded`; idle-stall / hard-cap / quota
 * fast-fail / autonomous abort set `stallKilled`) while its work was still
 * uncommitted. Those kills are the pipeline's own decision, so the pipeline
 * owns the consequences — and after re-dispatch rebuilds the worktree from
 * `origin/<base>` the work is unrecoverable.
 *
 * FALSE for a stage that exited on its own, including a non-zero exit. A
 * self-failed stage's tree is evidence: `feature-validate` explicitly leaves
 * a failed tree in place for triage, and committing it would fight that
 * contract. Only work the pipeline destroyed is work the pipeline preserves.
 */
export function shouldPreserveWorkOnExit(args: {
  success: boolean;
  stallKilled: boolean;
  costCapExceeded: boolean;
}): boolean {
  return !args.success && (args.stallKilled || args.costCapExceeded);
}

export interface PreserveWipOptions {
  /** The stage's working directory (the issue's worktree). */
  cwd: string;
  /** Pipeline stage that was terminated, e.g. `feature-dev`. */
  stage: string;
  /** Issue the run belongs to, when known. */
  issueNumber?: number;
  /**
   * Short machine-ish reason the stage was terminated, e.g.
   * `runaway-progress`, `stall-kill`, `hard-cap`. Recorded in the commit body
   * so a later retro can tell why the WIP commit exists.
   */
  killReason: string;
}

async function git(cwd: string, args: string[]): Promise<string> {
  const { stdout } = await execFileAsync("git", args, {
    cwd,
    encoding: "utf-8",
    timeout: GIT_TIMEOUT_MS,
    // Never let a credential/pinentry prompt block the kill path.
    env: { ...process.env, GIT_TERMINAL_PROMPT: "0" },
  });
  return stdout;
}

function buildCommitMessage(opts: PreserveWipOptions): string {
  const issueRef = opts.issueNumber ? `#${opts.issueNumber}` : "unknown issue";
  return [
    `wip(${opts.stage}): preserve uncommitted work from a terminated stage`,
    ``,
    `The ${opts.stage} stage was terminated by the pipeline (${opts.killReason})`,
    `while its work was still uncommitted. Nightgauge committed the worktree`,
    `in place so the work is not discarded with the branch, and so downstream`,
    `stages see the work that exists rather than an empty branch.`,
    ``,
    `This commit is machine-authored and expected to be superseded or amended`,
    `by the next successful run of the stage.`,
    ``,
    `Refs: ${issueRef}`,
    `${WIP_COMMIT_TRAILER}: ${opts.stage}`,
  ].join("\n");
}

/**
 * Commit the worktree in place so a terminated stage's work survives.
 *
 * Never throws: every failure mode is reported as a {@link PreserveWipResult}
 * so the caller (a process-kill path) cannot be destabilised by git.
 */
export async function preserveWorkInProgress(opts: PreserveWipOptions): Promise<PreserveWipResult> {
  const { cwd } = opts;
  if (!cwd) {
    return { outcome: "not-a-repo", detail: "no working directory for the stage" };
  }

  // 1. Must be a git work tree.
  try {
    if ((await git(cwd, ["rev-parse", "--is-inside-work-tree"])).trim() !== "true") {
      return { outcome: "not-a-repo", detail: `${cwd} is not inside a git work tree` };
    }
  } catch (err) {
    return {
      outcome: "not-a-repo",
      detail: `${cwd} is not a git repository (${errText(err)})`,
    };
  }

  // 2. Nothing to preserve? Then there is nothing to do — the common case for
  //    stages that never touch the tree (issue-pickup, pr-merge, ...).
  let dirtyPaths: string[];
  try {
    dirtyPaths = (await git(cwd, ["status", "--porcelain"]))
      .split("\n")
      .map((l) => l.trim())
      .filter((l) => l.length > 0);
  } catch (err) {
    return { outcome: "failed", detail: `git status failed: ${errText(err)}` };
  }
  if (dirtyPaths.length === 0) {
    return { outcome: "clean", detail: "worktree clean — nothing to preserve" };
  }

  // 3. Resolve the branch. A detached HEAD cannot carry the work forward.
  let branch: string;
  try {
    branch = (await git(cwd, ["symbolic-ref", "--quiet", "--short", "HEAD"])).trim();
  } catch {
    return {
      outcome: "detached-head",
      detail: `HEAD is detached — refusing to strand ${dirtyPaths.length} changed path(s) on an unreachable commit`,
      filesChanged: dirtyPaths.length,
    };
  }
  if (!branch || PROTECTED_BRANCHES.has(branch)) {
    return {
      outcome: "protected-branch",
      detail: `refusing to write a WIP commit to protected branch '${branch}' (${dirtyPaths.length} changed path(s) left in place)`,
      branch,
      filesChanged: dirtyPaths.length,
    };
  }

  // 4. Stage and commit. `--no-verify` skips hooks (a lint hook must never run
  //    on a kill path) and gpgsign is disabled so no pinentry can block.
  try {
    await git(cwd, ["add", "-A"]);
    await git(cwd, [
      "-c",
      "commit.gpgsign=false",
      "commit",
      "--no-verify",
      "-m",
      buildCommitMessage(opts),
    ]);
  } catch (err) {
    return {
      outcome: "failed",
      detail: `git refused the WIP commit (${errText(err)}) — ${dirtyPaths.length} changed path(s) left in the worktree`,
      branch,
      filesChanged: dirtyPaths.length,
    };
  }

  let commitSha = "";
  try {
    commitSha = (await git(cwd, ["rev-parse", "HEAD"])).trim();
  } catch {
    /* the commit landed; the sha is a nicety */
  }

  // Anchor the commit outside refs/heads/ so the re-dispatch teardown
  // (`git worktree remove --force` + `git branch -D <branch>` +
  // `worktree add -b <branch> origin/<base>`) cannot orphan it.
  let preservedRef: string | undefined;
  try {
    const ref = `${WIP_REF_NAMESPACE}/${sanitizeRefComponent(branch)}-${Math.floor(Date.now() / 1000)}`;
    await git(cwd, ["update-ref", ref, "HEAD"]);
    preservedRef = ref;
  } catch {
    /* the commit is on the branch either way — the anchor is best-effort */
  }

  return {
    outcome: "committed",
    detail:
      `committed ${dirtyPaths.length} changed path(s) to ${branch}` +
      `${commitSha ? ` as ${commitSha.slice(0, 8)}` : ""}` +
      `${preservedRef ? `, anchored at ${preservedRef}` : ""}`,
    commitSha: commitSha || undefined,
    ...(preservedRef ? { preservedRef } : {}),
    branch,
    filesChanged: dirtyPaths.length,
  };
}

/** Collapse a branch name into a single, ref-safe path component. */
function sanitizeRefComponent(branch: string): string {
  const cleaned = branch.replace(/[^A-Za-z0-9._-]+/g, "-").replace(/^[-.]+|[-.]+$/g, "");
  return cleaned || "stage";
}

function errText(err: unknown): string {
  if (err && typeof err === "object" && "stderr" in err) {
    const stderr = (err as { stderr?: unknown }).stderr;
    const text = typeof stderr === "string" ? stderr : "";
    if (text.trim()) {
      return text.trim().split("\n").slice(-1)[0];
    }
  }
  return err instanceof Error ? err.message.split("\n")[0] : String(err);
}
