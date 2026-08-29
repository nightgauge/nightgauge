/**
 * Reading back what a guard kill preserved (Issue #1105).
 *
 * `preserveWorkInProgress` (#128) is the writer: on a guard kill it commits the
 * dirty worktree and anchors that commit under `refs/nightgauge/wip/` so the
 * re-dispatch teardown cannot orphan it. Until this module existed the
 * namespace had **one writer and zero readers**. Nothing listed it, nothing
 * reported it, and — the failure that produced this issue — nothing consulted
 * it when the same issue was dispatched again.
 *
 * Observed end to end: a run killed on 2026-08-28 preserved 13 paths as
 * `641b9c8f`; the branch and worktree were cleaned up, leaving the commit
 * reachable only through the ref; the next day the issue was re-dispatched,
 * built a fresh worktree from `origin/<base>`, and planned from scratch without
 * mentioning the preserved work to the pipeline or the operator.
 *
 * So the promise "the work is committed, not discarded" held, and delivered
 * nothing: the operator was left exactly where they would have been without the
 * feature, minus the loss, and only if they already knew the namespace existed.
 *
 * This module is deliberately read-only and never throws. It runs on a dispatch
 * path, where a diagnostic that can fail a run is worse than no diagnostic.
 */

import { execFile } from "child_process";
import { promisify } from "util";
import { WIP_COMMIT_TRAILER, WIP_REF_NAMESPACE } from "./preserveWorkInProgress";

// Never a sync subprocess: this runs on the extension host event loop.
const execFileAsync = promisify(execFile);

/** Per-git-invocation timeout. This is a diagnostic, not a critical path. */
const GIT_TIMEOUT_MS = 10_000;

/** One preserved-work anchor and everything needed to decide whether to salvage it. */
export interface PreservedWipRef {
  /** Full ref name under {@link WIP_REF_NAMESPACE}. */
  ref: string;
  /** The preserved commit — what to `git show` / branch from. */
  commit: string;
  /** Issue the killed run belonged to, when the commit body recorded one. */
  issueNumber?: number;
  /** Stage that was terminated, e.g. `feature-validate`. */
  stage?: string;
  /** Number of paths the preserved commit touches. */
  filesChanged: number;
}

async function git(cwd: string, args: string[]): Promise<string> {
  const { stdout } = await execFileAsync("git", args, {
    cwd,
    encoding: "utf-8",
    timeout: GIT_TIMEOUT_MS,
    env: { ...process.env, GIT_TERMINAL_PROMPT: "0" },
  });
  return stdout;
}

/**
 * List the preserved-work anchors in a repository, optionally narrowed to one
 * issue.
 *
 * Returns `[]` for a repository with none, and also for any failure: a caller
 * on the dispatch path must never be destabilised by git, and "I could not
 * look" and "there is nothing" are equivalent for a purely advisory notice.
 * (The authoritative, fail-loud version of this scan is `nightgauge wip list`,
 * which distinguishes the two.)
 */
export async function listPreservedWip(
  repoRoot: string,
  issueNumber?: number
): Promise<PreservedWipRef[]> {
  if (!repoRoot) {
    return [];
  }
  let listing: string;
  try {
    // Space-separated, not `%x1f`: `for-each-ref` does not expand the hex
    // escapes `git log --pretty` does and would emit a literal "%x1f", which
    // parses as a single field and reports an empty namespace on a repo full
    // of preserved work. Neither a ref name nor an object name may contain a
    // space, so this is unambiguous.
    listing = await git(repoRoot, [
      "for-each-ref",
      "--format=%(refname) %(objectname)",
      WIP_REF_NAMESPACE,
    ]);
  } catch {
    return [];
  }

  const refs: PreservedWipRef[] = [];
  for (const line of listing.split("\n")) {
    const [ref, commit] = line.trim().split(/\s+/);
    if (!ref || !commit) {
      continue;
    }
    const entry: PreservedWipRef = { ref, commit, filesChanged: 0 };
    try {
      const body = await git(repoRoot, ["show", "-s", "--format=%B", commit]);
      const issueMatch = body.match(/^Refs:\s*#(\d+)\s*$/m);
      if (issueMatch) {
        entry.issueNumber = Number(issueMatch[1]);
      }
      const stageMatch = body.match(new RegExp(`^${WIP_COMMIT_TRAILER}:\\s*(.+)$`, "m"));
      if (stageMatch) {
        entry.stage = stageMatch[1].trim();
      }
    } catch {
      /* the anchor is still worth reporting without its detail */
    }
    try {
      const files = await git(repoRoot, [
        "diff-tree",
        "--no-commit-id",
        "--name-only",
        "-r",
        commit,
      ]);
      entry.filesChanged = files.split("\n").filter((l) => l.trim().length > 0).length;
    } catch {
      /* the count is evidence of magnitude, never part of a decision */
    }
    if (issueNumber !== undefined && entry.issueNumber !== issueNumber) {
      continue;
    }
    refs.push(entry);
  }
  return refs;
}

/**
 * One log-ready line telling the operator that this dispatch is about to start
 * over on top of work a previous kill preserved, and exactly how to get it back.
 *
 * The recovery command is spelled out because the alternative — "check the WIP
 * refs" — assumes the very knowledge whose absence made the feature inert.
 */
export function describePreservedWip(issueNumber: number, refs: PreservedWipRef[]): string {
  const parts = refs.map((r) => {
    const stage = r.stage ? ` killed in ${r.stage}` : "";
    return `${r.commit.slice(0, 8)} (${r.filesChanged} path(s)${stage}, ${r.ref})`;
  });
  return (
    `Issue #${issueNumber} has ${refs.length} preserved work-in-progress commit(s) from a previously ` +
    `killed stage: ${parts.join("; ")}. This run starts from a fresh worktree and will NOT include ` +
    `that work — recover it with \`git checkout -b salvage-${issueNumber} <commit>\`, list it with ` +
    `\`nightgauge wip list --issue ${issueNumber}\`, or drop it with ` +
    `\`nightgauge wip prune --issue ${issueNumber} --discard\`.`
  );
}
