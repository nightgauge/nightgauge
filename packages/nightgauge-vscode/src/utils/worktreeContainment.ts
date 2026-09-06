/**
 * Worktree write containment (Issue #129).
 *
 * ## Why this exists
 *
 * A stage runs with its CWD set to the issue's worktree, and every downstream
 * stage — `feature-validate`'s commit, `pr-create`'s push — looks only there.
 * But nothing STOPS a stage writing anywhere else on disk. When an issue's work
 * genuinely lives in another repo of the workspace (a misfiled issue, or
 * legitimately cross-repo acceptance criteria), the agent reasons correctly
 * about where the code lives and writes into THAT repo's live checkout:
 * uncommitted, on whatever branch the operator happens to have out.
 *
 * The observed incident left 5 modified + 2 new files in one sibling repo and
 * 1 modified + 3 new files in another that was checked out on `main`. Nothing
 * surfaced it. `feature-validate` reported "no implementation work detected" —
 * correct from where it was looking — the run was billed in full and recorded
 * as a failure, and substantial completed work sat uncommitted in a repo the
 * operator was actively using, one `git checkout .` or `git pull` from
 * oblivion.
 *
 * Worktree isolation was a convention the agent happened to follow. This module
 * makes it a boundary the pipeline enforces.
 *
 * ## How it works
 *
 * A `git status --porcelain` baseline is taken of every configured workspace
 * repo BEFORE the stage spawns, and again after it closes. The comparison is
 * what makes the check safe: a sibling repo is very often dirty because the
 * OPERATOR is working in it, and that work must never be attributed to the
 * stage, let alone touched.
 *
 * Attribution rule — deliberately asymmetric:
 *
 *   - **clean → dirty** (a path with no baseline entry) is the stage's. Its
 *     pre-stage content was HEAD-or-absent, so the entire delta belongs to the
 *     stage. This is the FAILING signal, and it covers the whole observed
 *     incident: all 9 escaped paths were new dirt.
 *   - **dirty → dirty with a changed fingerprint** is AMBIGUOUS. The operator's
 *     own edit and a stage write are indistinguishable from the filesystem, and
 *     the delta is an inseparable mix of both. Reported as a warning so the
 *     operator knows to look; never attributed, never captured, never a
 *     failure. Failing here would make the pipeline unusable next to a human.
 *   - **dirty at baseline, unchanged** is the operator's standing work. Ignored
 *     completely.
 *
 * Under-detecting (a stage edit to a file the operator had already dirtied is
 * missed) is strictly preferable to misattributing: a missed breach costs one
 * more incident, a misattribution costs the operator's trust and, if the
 * response were destructive, their work.
 *
 * ## Preservation — capture, never mutate
 *
 * On detection the attributed changes are written out as a `git apply`-able
 * patch under `.nightgauge/containment/` in the stage repo's canonical root
 * (which outlives the worktree a re-dispatch tears down). **Nothing in the
 * sibling repo is modified**: no `git add`, no commit, no stash, no checkout,
 * no revert. The files are left exactly where the stage put them.
 *
 * This is why {@link preserveWorkInProgress} (#128) is NOT reused here. That
 * function commits the worktree in place — `git add -A` plus a commit on the
 * current branch. Pointed at a sibling repo it would sweep the operator's
 * unrelated dirty files into a machine-authored commit, move their HEAD and
 * index, and refuse outright on `main` (exactly where the incident's second
 * repo was checked out), stranding the work it was called to save. Its
 * contract — "the pipeline destroyed this work, so the pipeline commits it" —
 * holds only for a worktree the pipeline owns. A sibling checkout belongs to
 * the operator, so the containment response captures a copy and touches
 * nothing.
 *
 * The attributed paths are also recorded in a small ledger so that a RETRY
 * rewriting the same paths is still attributed to the stage rather than read
 * as pre-existing operator dirt.
 */

import { execFile } from "child_process";
import { promisify } from "util";
import * as fs from "fs/promises";
import * as path from "path";

const execFileAsync = promisify(execFile);

/** Per-git-invocation timeout. `git status` on a large tree is the slow case. */
const GIT_TIMEOUT_MS = 20_000;

/**
 * Paths the pipeline writes into the canonical checkout BY DESIGN and which
 * must therefore never count as a containment breach: stage diagnostics,
 * exit records, telemetry, run state and the knowledge base are all mirrored
 * out of the worktree on purpose (see `writeDiagnosticWithMirror`). Without
 * this exclusion every run would report a breach against its own repo.
 */
const PIPELINE_OWNED_PREFIX = ".nightgauge/";

/** Directory under the canonical root that holds patches and the ledger. */
export const CONTAINMENT_DIR = path.join(".nightgauge", "containment");

/** Ledger of paths a previous attempt already attributed to the pipeline. */
const LEDGER_FILE = "attributed.json";

/** Upper bound on paths patched per repo. Beyond this the patch is truncated. */
const MAX_PATCHED_PATHS = 200;

/** Marker prefix for the stage error, matching skillRunner's `[stage:*]` style. */
export const CONTAINMENT_ERROR_MARKER = "[stage:worktree-containment]";

/** One dirty path as `git status --porcelain` saw it, plus a content fingerprint. */
interface DirtyEntry {
  /** Repo-relative path. */
  path: string;
  /** Two-character porcelain status code, e.g. `" M"`, `"??"`, `"A "`. */
  code: string;
  /**
   * `code|size|mtimeMs`, or `code|absent` when the path is gone (deletions).
   *
   * A change detector, not a content hash: hashing every dirty file in every
   * workspace repo twice per stage is not worth paying for, and the question
   * is only ever "did this move during the stage window", which size+mtime
   * answers for anything a program wrote.
   */
  fingerprint: string;
}

/** The dirty state of one repo at a point in time. */
export interface RepoSnapshot {
  repoPath: string;
  repoName: string;
  /** Repo-relative path → fingerprint. */
  entries: Record<string, string>;
  /** Codes keyed by path, so the patch writer can split tracked vs untracked. */
  codes: Record<string, string>;
  /**
   * `git rev-parse HEAD` at snapshot time, or null when it cannot be resolved
   * (an unborn branch, or a repo git refuses to answer for).
   *
   * Dirtiness is a statement RELATIVE TO HEAD, so it is only comparable across
   * two snapshots when HEAD is the same in both. See {@link RefMove}.
   */
  head: string | null;
  /** Short branch name, or null when HEAD is detached. Diagnostics only. */
  branch: string | null;
}

/**
 * A repo whose HEAD moved while the stage ran (#1499).
 *
 * `git status` reports the working tree and index RELATIVE TO HEAD. Move HEAD
 * without moving either — `git update-ref refs/heads/main origin/main` on a
 * checked-out branch is enough — and every path the two commits differ in
 * appears as a staged change that no process wrote. The containment baseline
 * was taken against the old HEAD, so those paths read `clean → dirty` and are
 * attributed in full to whichever stage happens to close next.
 *
 * That is exactly the 2026-09-06 incident: three concurrent slots in three
 * different repos were each killed for the identical 31-path "breach" in a
 * fourth repo that none of them had touched.
 */
export interface RefMove {
  repoPath: string;
  repoName: string;
  /** HEAD when the baseline was taken. */
  baselineHead: string;
  /** HEAD at the post-stage snapshot. */
  postHead: string;
  /** Short branch name at the post-stage snapshot, when not detached. */
  branch: string | null;
  /**
   * Paths that were attributed to the stage but are fully explained by the
   * ref move, and were therefore subtracted. Paths dirty for a reason the ref
   * move does NOT explain stay attributed.
   */
  explainedPaths: string[];
}

/** Pre-stage state, threaded from spawn to the process-close handler. */
export interface ContainmentBaseline {
  /** The directory the stage was spawned in. */
  stageCwd: string;
  /** Canonical root of the stage's own repo — where artifacts are written. */
  artifactRoot: string;
  /** Repos in scope, with their pre-stage dirty state. */
  snapshots: RepoSnapshot[];
  /**
   * Repos that another RUNNING pipeline owns (#1499). Still snapshotted and
   * still reported, but never a breach: with `max_concurrent > 1` a sibling
   * slot's legitimate work in its own repo — its worktree lives under that
   * repo's `.worktrees/`, and its `pr-*` stages fetch and push in the repo
   * root — is indistinguishable, from here, from this stage escaping into it.
   * Failing this stage for another slot's correct behaviour is a pure false
   * positive, and with three slots up it fires three times at once.
   */
  warnOnlyRepoPaths: string[];
}

/** A repo the stage wrote into. */
export interface ContainmentBreach {
  repoPath: string;
  repoName: string;
  /** Repo-relative paths that went clean → dirty during the stage. */
  paths: string[];
  /**
   * Paths that were already dirty at baseline and changed during the stage.
   * Ambiguous — surfaced for the operator, never attributed or captured.
   */
  ambiguousPaths: string[];
  /** Absolute path of the captured patch, when one could be written. */
  patchPath?: string;
  /** Why no patch exists, when {@link patchPath} is unset. */
  patchError?: string;
  /**
   * Set when this repo was demoted from breach to warning because something
   * other than the stage explains the dirt — currently only "another running
   * pipeline owns it". Names the reason so the warning line can say it.
   */
  warnOnlyReason?: string;
}

export interface ContainmentReport {
  /** Repos with at least one clean → dirty path. Empty means no breach. */
  breaches: ContainmentBreach[];
  /**
   * Repos with only ambiguous changes. Warn-only: the stage is NOT failed for
   * these, because an operator editing their own dirty file during a stage is
   * indistinguishable from a stage write and is the likelier explanation.
   */
  warnings: ContainmentBreach[];
  /** Directory the patches and manifest were written to, when anything was. */
  artifactDir?: string;
  /**
   * Repos whose HEAD moved during the stage. Diagnostics, not a verdict: the
   * paths a ref move explains have already been subtracted from
   * {@link ContainmentBreach.paths} by the time this is returned.
   */
  refMoves: RefMove[];
}

async function git(cwd: string, args: string[]): Promise<string> {
  const { stdout } = await execFileAsync("git", args, {
    cwd,
    encoding: "utf-8",
    timeout: GIT_TIMEOUT_MS,
    maxBuffer: 32 * 1024 * 1024,
    env: { ...process.env, GIT_TERMINAL_PROMPT: "0" },
  });
  return stdout;
}

/**
 * `git diff` exits 1 when there are differences, which `execFile` reports as an
 * error. Return stdout for exit 0 and 1 alike; anything else is a real failure.
 */
async function gitDiff(cwd: string, args: string[]): Promise<string> {
  try {
    return await git(cwd, args);
  } catch (err) {
    const e = err as { code?: unknown; stdout?: unknown };
    if (e.code === 1 && typeof e.stdout === "string") return e.stdout;
    throw err;
  }
}

function errText(err: unknown): string {
  if (err && typeof err === "object" && "stderr" in err) {
    const stderr = (err as { stderr?: unknown }).stderr;
    if (typeof stderr === "string" && stderr.trim()) {
      return stderr.trim().split("\n").slice(-1)[0];
    }
  }
  return err instanceof Error ? err.message.split("\n")[0] : String(err);
}

/**
 * Parse `git status --porcelain -z --untracked-files=all`.
 *
 * NUL-separated so paths with spaces or non-ASCII bytes arrive verbatim rather
 * than in git's quoted form. Rename/copy entries carry a second NUL-terminated
 * field (the source path) that must be consumed, or every subsequent entry is
 * misparsed.
 */
export function parsePorcelainZ(stdout: string): Array<{ code: string; path: string }> {
  const fields = stdout.split("\0");
  const out: Array<{ code: string; path: string }> = [];
  for (let i = 0; i < fields.length; i++) {
    const field = fields[i];
    if (!field || field.length < 4) continue;
    const code = field.slice(0, 2);
    const filePath = field.slice(3);
    if (!filePath) continue;
    out.push({ code, path: filePath });
    // Rename/copy: the next field is the source path, not a new entry.
    if (code[0] === "R" || code[0] === "C") i++;
  }
  return out;
}

/** True for paths the pipeline itself writes into the canonical checkout. */
function isPipelineOwned(repoRelative: string): boolean {
  const normalized = repoRelative.split(path.sep).join("/");
  return normalized === ".nightgauge" || normalized.startsWith(PIPELINE_OWNED_PREFIX);
}

async function fingerprint(
  repoPath: string,
  entry: { code: string; path: string }
): Promise<string> {
  try {
    const st = await fs.stat(path.join(repoPath, entry.path));
    return `${entry.code}|${st.size}|${st.mtimeMs}`;
  } catch {
    // Deleted, or a path git reports that no longer exists on disk.
    return `${entry.code}|absent`;
  }
}

/**
 * The commit HEAD points at, and the branch it is on. Both null-tolerant: an
 * unborn branch has no HEAD commit and a detached HEAD has no branch, and
 * neither is a reason to fail a snapshot.
 */
async function readHead(repoPath: string): Promise<{ head: string | null; branch: string | null }> {
  let head: string | null = null;
  let branch: string | null = null;
  try {
    head = (await git(repoPath, ["rev-parse", "HEAD"])).trim() || null;
  } catch {
    /* unborn branch, or a repo git will not answer for */
  }
  try {
    branch = (await git(repoPath, ["symbolic-ref", "--quiet", "--short", "HEAD"])).trim() || null;
  } catch {
    /* detached HEAD */
  }
  return { head, branch };
}

/** Snapshot one repo's dirty state. Returns null when the repo is unusable. */
async function snapshotRepo(repoPath: string): Promise<RepoSnapshot | null> {
  let stdout: string;
  try {
    stdout = await git(repoPath, ["status", "--porcelain", "-z", "--untracked-files=all"]);
  } catch {
    // Not a repo, or git is unhappy. Fail open — containment must never be the
    // reason a run cannot start.
    return null;
  }
  const entries: Record<string, string> = {};
  const codes: Record<string, string> = {};
  for (const entry of parsePorcelainZ(stdout)) {
    if (isPipelineOwned(entry.path)) continue;
    entries[entry.path] = await fingerprint(repoPath, entry);
    codes[entry.path] = entry.code;
  }
  // Recorded so detection can tell "the stage wrote this" from "HEAD moved
  // under a still-untouched tree" (#1499). Read AFTER the status so a ref move
  // racing the snapshot is caught by the post-snapshot comparison rather than
  // being baked invisibly into this one.
  const { head, branch } = await readHead(repoPath);
  return { repoPath, repoName: path.basename(repoPath), entries, codes, head, branch };
}

/**
 * Paths that differ between two commits — the dirt a ref move alone explains.
 *
 * Returns null when the diff cannot be taken (either commit missing from this
 * repo, e.g. a ref move that also pruned objects). Null means "cannot explain
 * anything", which leaves every path attributed: this narrows attribution on
 * evidence and never widens it on a guess.
 */
async function pathsExplainedByRefMove(
  repoPath: string,
  fromSha: string,
  toSha: string
): Promise<Set<string> | null> {
  try {
    const out = await gitDiff(repoPath, ["diff", "--name-only", "-z", fromSha, toSha]);
    return new Set(out.split("\0").filter((p) => p.length > 0));
  } catch {
    return null;
  }
}

/**
 * Whether `stageCwd` is a LINKED git worktree (`git worktree add`) rather than
 * a repository's main checkout.
 *
 * This is what decides whether a configured repo that CONTAINS the stage's CWD
 * is in scope. When the stage runs in `<repo>/.worktrees/issue-N`, `<repo>`'s
 * main checkout is a separate working tree the stage has no business writing to
 * — and the incident's second repo was exactly such a checkout, sitting on
 * `main`. When the stage runs directly in `<repo>` (single-repo, non-concurrent
 * mode), `<repo>` IS the stage's own tree and every write to it is legitimate.
 *
 * Asking git rather than pattern-matching `.worktrees/` keeps this correct for
 * every worktree base the pipeline uses (`.worktrees`, `.nightgauge/worktrees`,
 * `.claude/worktrees`) and for any the operator invents.
 */
async function isLinkedWorktree(stageCwd: string): Promise<boolean> {
  try {
    const out = await git(stageCwd, ["rev-parse", "--git-dir", "--git-common-dir"]);
    const [gitDir, commonDir] = out.trim().split("\n");
    if (!gitDir || !commonDir) return false;
    return path.resolve(stageCwd, gitDir) !== path.resolve(stageCwd, commonDir);
  } catch {
    return false;
  }
}

/** True when `child` is `parent` or lives underneath it. */
function isInside(child: string, parent: string): boolean {
  const rel = path.relative(path.resolve(parent), path.resolve(child));
  return rel === "" || (!rel.startsWith("..") && !path.isAbsolute(rel));
}

/**
 * The canonical main checkout of the stage's own repo — `<repo>` for a stage
 * running in `<repo>/.worktrees/issue-N`. Containment artifacts go here because
 * a re-dispatch force-removes the worktree.
 */
async function resolveArtifactRoot(stageCwd: string): Promise<string> {
  try {
    const commonDir = (await git(stageCwd, ["rev-parse", "--git-common-dir"])).trim();
    if (commonDir) return path.dirname(path.resolve(stageCwd, commonDir));
  } catch {
    /* fall through */
  }
  return stageCwd;
}

/**
 * Repos the stage must not write into: every configured workspace repo except
 * the stage's own working tree.
 */
export async function resolveContainmentTargets(
  stageCwd: string,
  repoPaths: readonly string[]
): Promise<string[]> {
  const cwd = path.resolve(stageCwd);
  const linked = await isLinkedWorktree(cwd);
  const seen = new Set<string>();
  const targets: string[] = [];
  for (const raw of repoPaths) {
    if (!raw) continue;
    const repoPath = path.resolve(raw);
    if (seen.has(repoPath)) continue;
    seen.add(repoPath);
    // The stage's own directory is always in bounds.
    if (repoPath === cwd) continue;
    // A repo that CONTAINS the stage CWD is the stage's own checkout unless the
    // stage is in a linked worktree, in which case the main checkout is a
    // distinct tree and stays in scope.
    if (!linked && isInside(cwd, repoPath)) continue;
    targets.push(repoPath);
  }
  return targets;
}

/**
 * What a previous attempt left behind: `repoPath → { filePath → fingerprint }`.
 *
 * The fingerprint, not just the path, is what makes the ledger safe. It records
 * the exact state the pipeline left the file in, so a later baseline can ask
 * "is this still MY leftover?" rather than "did I once touch this?". A file
 * whose fingerprint still matches is the pipeline's — subtract it, so a retry
 * that rewrites it is attributed rather than read as pre-existing dirt. A file
 * that has since been recovered, reverted, or edited by anyone no longer
 * matches, and reverts to being the operator's. Without the fingerprint the
 * entry would be permanent and would eventually blame the operator's own future
 * edit of that path on a stage.
 */
type ContainmentLedger = Record<string, Record<string, string>>;

async function readLedger(artifactRoot: string): Promise<ContainmentLedger> {
  try {
    const raw = await fs.readFile(path.join(artifactRoot, CONTAINMENT_DIR, LEDGER_FILE), "utf-8");
    const parsed: unknown = JSON.parse(raw);
    if (parsed && typeof parsed === "object" && !Array.isArray(parsed)) {
      return parsed as ContainmentLedger;
    }
  } catch {
    /* no ledger yet */
  }
  return {};
}

async function writeLedger(artifactRoot: string, ledger: ContainmentLedger): Promise<void> {
  const dir = path.join(artifactRoot, CONTAINMENT_DIR);
  await fs.mkdir(dir, { recursive: true });
  await fs.writeFile(path.join(dir, LEDGER_FILE), JSON.stringify(ledger, null, 2), "utf-8");
}

/**
 * Snapshot every out-of-bounds repo before the stage runs.
 *
 * Never throws: a containment baseline that cannot be taken degrades to "no
 * repos in scope", which detection reads as "no breach". Enforcement must never
 * be the reason a stage fails to start.
 */
export async function captureContainmentBaseline(args: {
  stageCwd: string;
  repoPaths: readonly string[];
  /**
   * Working directories of every pipeline currently RUNNING, including this
   * one (#1499). Each is resolved to the repo it belongs to; every repo other
   * than this stage's own becomes warning-only, because a repo another slot is
   * actively working in is expected to move for reasons this stage cannot see.
   *
   * Worktree paths rather than repo roots on purpose: the orchestrator already
   * knows a slot by its worktree, and resolving through `--git-common-dir` is
   * exact where guessing a root from a `.worktrees/` path is not.
   */
  runningWorktreePaths?: readonly string[];
}): Promise<ContainmentBaseline> {
  const stageCwd = path.resolve(args.stageCwd);
  let artifactRoot = stageCwd;
  const snapshots: RepoSnapshot[] = [];
  const warnOnlyRepoPaths: string[] = [];
  try {
    artifactRoot = await resolveArtifactRoot(stageCwd);
    for (const raw of args.runningWorktreePaths ?? []) {
      if (!raw) continue;
      const root = await resolveArtifactRoot(path.resolve(raw));
      // The stage's own repo is never demoted: keeping the stage's OWN main
      // checkout in scope is the whole point of #129, and this slot appears in
      // the running set like every other.
      if (root === artifactRoot) continue;
      if (!warnOnlyRepoPaths.includes(root)) warnOnlyRepoPaths.push(root);
    }
    const targets = await resolveContainmentTargets(stageCwd, args.repoPaths);
    if (targets.length > 0) {
      // Leftovers a previous attempt already attributed to the pipeline are NOT
      // operator work, even though they are dirty now. Dropping them from the
      // baseline is what makes a retry that rewrites the same paths fail again
      // instead of reading its own output as pre-existing dirt.
      const ledger = await readLedger(artifactRoot);
      let ledgerChanged = false;
      for (const repoPath of targets) {
        const snap = await snapshotRepo(repoPath);
        if (!snap) continue;
        const remembered = ledger[repoPath] ?? {};
        const surviving: Record<string, string> = {};
        for (const [filePath, rememberedFp] of Object.entries(remembered)) {
          // Untouched since we left it → still ours.
          if (snap.entries[filePath] === rememberedFp) {
            surviving[filePath] = rememberedFp;
            delete snap.entries[filePath];
          }
          // Otherwise it has been recovered, reverted or edited since; the
          // entry has outlived its purpose and is dropped, so the path goes
          // back to being whatever the baseline says it is.
        }
        if (Object.keys(surviving).length !== Object.keys(remembered).length) {
          ledgerChanged = true;
          if (Object.keys(surviving).length > 0) ledger[repoPath] = surviving;
          else delete ledger[repoPath];
        }
        snapshots.push(snap);
      }
      if (ledgerChanged) await writeLedger(artifactRoot, ledger);
    }
  } catch {
    /* fail open — see doc comment */
  }
  return { stageCwd, artifactRoot, snapshots, warnOnlyRepoPaths };
}

/** Build a `git apply`-able patch for exactly the attributed paths. */
async function buildPatch(
  repoPath: string,
  paths: string[],
  codes: Record<string, string>
): Promise<string> {
  const capped = paths.slice(0, MAX_PATCHED_PATHS);
  const tracked = capped.filter((p) => codes[p] !== "??");
  const untracked = capped.filter((p) => codes[p] === "??");
  const chunks: string[] = [];

  if (paths.length > capped.length) {
    // Preamble, not postamble: `git apply` scans forward for the first
    // `diff --git` header, so a note here is skipped rather than parsed.
    chunks.push(
      `# NOTE: ${paths.length - capped.length} further path(s) are omitted from this ` +
        `patch (cap ${MAX_PATCHED_PATHS}); manifest.json has the full list.\n`
    );
  }

  if (tracked.length > 0) {
    // `HEAD` rather than the index so staged and unstaged edits both land.
    chunks.push(await gitDiff(repoPath, ["diff", "--binary", "HEAD", "--", ...tracked]));
  }
  for (const rel of untracked) {
    // `--no-index` against /dev/null yields a proper "new file" hunk without
    // touching the index (`--intent-to-add` would mutate the operator's repo).
    // The path must be REPO-RELATIVE: `--no-index` echoes whatever it is given
    // into the `+++ b/...` header, and an absolute path there produces a patch
    // that `git apply` cannot land in a recovery checkout.
    chunks.push(
      await gitDiff(repoPath, ["diff", "--binary", "--no-index", "--", "/dev/null", rel])
    );
  }
  return chunks.filter((c) => c.length > 0).join("");
}

export interface DetectContainmentArgs {
  baseline: ContainmentBaseline;
  stage: string;
  issueNumber?: number;
}

/**
 * Compare the post-stage dirty state against the baseline and capture whatever
 * the stage wrote outside its worktree.
 *
 * Never throws, and never modifies a repo it is inspecting.
 */
export async function detectContainmentBreach(
  args: DetectContainmentArgs
): Promise<ContainmentReport> {
  const { baseline, stage, issueNumber } = args;
  const report: ContainmentReport = { breaches: [], warnings: [], refMoves: [] };
  if (baseline.snapshots.length === 0) return report;
  const warnOnly = new Set(baseline.warnOnlyRepoPaths ?? []);

  /**
   * Post-stage snapshot per repo. Only the AFTER snapshot has codes and
   * fingerprints for paths that did not exist at baseline — the patch writer
   * needs the codes to split tracked edits from untracked new files, and the
   * ledger needs the fingerprints to recognise its own leftovers later.
   */
  const post = new Map<string, RepoSnapshot>();

  for (const before of baseline.snapshots) {
    let after: RepoSnapshot | null;
    try {
      after = await snapshotRepo(before.repoPath);
    } catch {
      continue;
    }
    if (!after) continue;
    post.set(before.repoPath, after);

    let attributed: string[] = [];
    const ambiguous: string[] = [];
    for (const [filePath, fp] of Object.entries(after.entries)) {
      const baselineFp = before.entries[filePath];
      if (baselineFp === undefined) {
        attributed.push(filePath);
      } else if (baselineFp !== fp) {
        ambiguous.push(filePath);
      }
    }

    // ── Did HEAD move under us? (#1499) ──────────────────────────────────
    // `git status` is relative to HEAD, so the clean → dirty comparison above
    // only means "the stage wrote this" while HEAD holds still. When it moved,
    // subtract exactly the paths the two commits differ in — no more. A path
    // dirty for a reason the ref move does not explain is STILL the stage's.
    if (before.head && after.head && before.head !== after.head && attributed.length > 0) {
      const explained = await pathsExplainedByRefMove(before.repoPath, before.head, after.head);
      if (explained) {
        const subtracted = attributed.filter((p) => explained.has(p));
        if (subtracted.length > 0) {
          attributed = attributed.filter((p) => !explained.has(p));
          report.refMoves.push({
            repoPath: before.repoPath,
            repoName: before.repoName,
            baselineHead: before.head,
            postHead: after.head,
            branch: after.branch,
            explainedPaths: subtracted.sort(),
          });
        }
      }
    }

    if (attributed.length === 0 && ambiguous.length === 0) continue;

    const breach: ContainmentBreach = {
      repoPath: before.repoPath,
      repoName: before.repoName,
      paths: attributed.sort(),
      ambiguousPaths: ambiguous.sort(),
    };
    if (warnOnly.has(before.repoPath)) {
      // Another running pipeline owns this repo. Its worktree lives under this
      // repo and its pr stages fetch and push here, so movement is expected
      // and belongs to that slot, not to this stage.
      breach.warnOnlyReason = "another running pipeline owns this repository";
      report.warnings.push(breach);
    } else if (attributed.length > 0) {
      report.breaches.push(breach);
    } else {
      report.warnings.push(breach);
    }
  }

  if (report.breaches.length === 0) return report;

  // ── Preserve: write patches OUTSIDE the repos that were written to. ──
  const stamp = new Date().toISOString().replace(/[:.]/g, "-");
  const dirName = `${stage}-${issueNumber ?? "unknown"}-${stamp}`;
  const artifactDir = path.join(baseline.artifactRoot, CONTAINMENT_DIR, dirName);
  try {
    await fs.mkdir(artifactDir, { recursive: true });
    report.artifactDir = artifactDir;
  } catch (err) {
    for (const breach of report.breaches) {
      breach.patchError = `could not create ${artifactDir}: ${errText(err)}`;
    }
    return report;
  }

  const ledger = await readLedger(baseline.artifactRoot);
  for (const breach of report.breaches) {
    const after = post.get(breach.repoPath);
    try {
      const patch = await buildPatch(breach.repoPath, breach.paths, after?.codes ?? {});
      const patchPath = path.join(artifactDir, `${breach.repoName}.patch`);
      await fs.writeFile(patchPath, patch, "utf-8");
      breach.patchPath = patchPath;
    } catch (err) {
      breach.patchError = errText(err);
    }
    // Remember the exact state we are leaving behind, so the next baseline can
    // tell our own leftovers from anything that happens to the path afterwards.
    const entry = { ...(ledger[breach.repoPath] ?? {}) };
    for (const p of breach.paths) {
      const fp = after?.entries[p];
      if (fp !== undefined) entry[p] = fp;
    }
    ledger[breach.repoPath] = entry;
  }

  try {
    await fs.writeFile(
      path.join(artifactDir, "manifest.json"),
      JSON.stringify(
        {
          issue: "129",
          stage,
          issueNumber: issueNumber ?? null,
          stageCwd: baseline.stageCwd,
          detectedAt: new Date().toISOString(),
          breaches: report.breaches.map((b) => ({
            repoName: b.repoName,
            repoPath: b.repoPath,
            paths: b.paths,
            ambiguousPaths: b.ambiguousPaths,
            patchPath: b.patchPath ?? null,
            patchError: b.patchError ?? null,
          })),
          warnings: report.warnings.map((w) => ({
            repoName: w.repoName,
            repoPath: w.repoPath,
            paths: w.paths,
            ambiguousPaths: w.ambiguousPaths,
            warnOnlyReason: w.warnOnlyReason ?? null,
          })),
          refMoves: report.refMoves.map((m) => ({
            repoName: m.repoName,
            repoPath: m.repoPath,
            branch: m.branch,
            baselineHead: m.baselineHead,
            postHead: m.postHead,
            explainedPaths: m.explainedPaths,
          })),
          note:
            "Captured by Nightgauge. NOTHING in the listed repositories was " +
            "modified, staged, committed or reverted — the files are still on " +
            "disk exactly as the stage left them. Apply a patch with: " +
            "git -C <repoPath> apply <repoName>.patch",
        },
        null,
        2
      ),
      "utf-8"
    );
  } catch {
    /* the patches are the artifact that matters */
  }

  try {
    await writeLedger(baseline.artifactRoot, ledger);
  } catch {
    /* best-effort: only affects whether a retry re-attributes these paths */
  }

  return report;
}

/**
 * The stage-failure reason. Names the repos and paths, because the alternative
 * — letting the run die downstream as "no implementation work detected" — sends
 * triage in entirely the wrong direction.
 */
export function formatContainmentFailure(stage: string, report: ContainmentReport): string {
  const lines: string[] = [
    `${CONTAINMENT_ERROR_MARKER} Stage ${stage} wrote outside its worktree into ` +
      `${report.breaches.length} repository/repositories it does not own.`,
  ];
  for (const breach of report.breaches) {
    lines.push(`  ${breach.repoName} (${breach.repoPath}) — ${breach.paths.length} path(s):`);
    for (const p of breach.paths.slice(0, 25)) lines.push(`    ${p}`);
    if (breach.paths.length > 25) lines.push(`    ... and ${breach.paths.length - 25} more`);
    if (breach.ambiguousPaths.length > 0) {
      lines.push(
        `    (${breach.ambiguousPaths.length} path(s) already dirty before the stage also ` +
          `changed; not attributed — check them yourself: ${breach.ambiguousPaths
            .slice(0, 5)
            .join(", ")})`
      );
    }
    if (breach.patchPath) lines.push(`    preserved: ${breach.patchPath}`);
    else if (breach.patchError) lines.push(`    NOT preserved: ${breach.patchError}`);
  }
  lines.push(
    `  Nothing in those repositories was modified, staged, committed or reverted — the ` +
      `files are still on disk exactly as the stage left them. A stage's work must land ` +
      `on the issue's branch in its own worktree; cross-repo work needs an issue filed ` +
      `in each repo that must change.`
  );
  return lines.join("\n");
}

/** Warn-only line for repos where only pre-existing dirty paths changed. */
export function formatContainmentWarning(stage: string, report: ContainmentReport): string {
  const parts = report.warnings.map((w) => {
    const shown = (w.warnOnlyReason ? [...w.paths, ...w.ambiguousPaths] : w.ambiguousPaths).slice(
      0,
      5
    );
    const suffix = w.warnOnlyReason ? ` [${w.warnOnlyReason}]` : "";
    return `${w.repoName}: ${shown.join(", ")}${suffix}`;
  });
  return (
    `[containment-ambiguous] Stage ${stage}: path(s) that were ALREADY dirty before the ` +
    `stage changed while it ran, or that belong to a repository another running pipeline ` +
    `owns (${parts.join("; ")}). Not attributed to the stage — an operator editing their own ` +
    `uncommitted file, or a sibling slot working in its own repo, is indistinguishable from ` +
    `a stage write and is the likelier explanation. Nothing was touched. (Issues #129, #1499)`
  );
}

/**
 * Warn line for repos whose HEAD moved during the stage (#1499).
 *
 * Named in full — repo, both SHAs, the count — because a bare ref move leaves
 * no other trace: the branch reflog entry for `git update-ref` carries an empty
 * message and HEAD's reflog carries nothing at all, so this line is the only
 * durable record that the checkout's HEAD went somewhere while a stage ran.
 */
export function formatContainmentRefMoveWarning(stage: string, report: ContainmentReport): string {
  const parts = report.refMoves.map(
    (m) =>
      `${m.repoName}${m.branch ? ` (${m.branch})` : ""} ${m.baselineHead.slice(0, 12)} → ` +
      `${m.postHead.slice(0, 12)}: ${m.explainedPaths.length} path(s)`
  );
  return (
    `[containment-ref-move] Stage ${stage}: HEAD moved in ${report.refMoves.length} ` +
    `repository/repositories while the stage ran (${parts.join("; ")}). \`git status\` is ` +
    `relative to HEAD, so those path(s) read as dirty without anything writing them; they ` +
    `were NOT attributed to the stage. Any remaining dirt still was. A ref move with no ` +
    `working-tree move usually means a raw \`git update-ref\`/\`git fetch <sha>:refs/heads/…\` ` +
    `against the checkout — repair it with \`git -C <repo> reset --hard HEAD\` after checking ` +
    `nothing of yours is in the delta. (Issue #1499)`
  );
}
