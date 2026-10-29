/**
 * worktreeContainment.test.ts
 *
 * Issue #129: a stage must not write outside its own worktree, and when it
 * does, the pipeline must say so accurately and preserve the work.
 *
 * The incident: `feature-dev` ran for an issue whose code lived elsewhere,
 * produced ZERO changes in its own worktree, and left 5 modified + 2 new files
 * in one sibling repo and 1 modified + 3 new files in another that was checked
 * out on `main`. `feature-validate` then reported "no implementation work
 * detected" — true of the branch, and completely misleading. The run was billed
 * in full and recorded as a failure with substantial work stranded uncommitted
 * in a repo the operator was actively using.
 *
 * These tests run against REAL git repositories in a temp directory. The whole
 * behaviour under test is "what does git report, and what is on disk
 * afterwards" — a mocked git would prove nothing about either. In particular
 * the non-destructiveness assertions (nothing staged, committed, reverted or
 * even touched in a sibling repo) are only meaningful against real git.
 */

import { describe, it, expect, beforeEach, afterEach } from "vitest";
import { execFileSync } from "child_process";
import * as fs from "fs";
import * as os from "os";
import * as path from "path";
import {
  captureContainmentBaseline,
  detectContainmentBreach,
  formatContainmentFailure,
  formatContainmentRefMoveWarning,
  formatContainmentWarning,
  parsePorcelainZ,
  resolveContainmentTargets,
  containmentDir,
  CONTAINMENT_ERROR_MARKER,
  StageWriteRecord,
} from "../../src/utils/worktreeContainment";

let tmp: string;
/** The repo the stage belongs to. Its `.worktrees/issue-129` is the stage CWD. */
let primary: string;
/** The stage's worktree — the only place it is allowed to write. */
let worktree: string;
/** A second workspace repo the stage has no business touching. */
let sibling: string;

function git(args: string[], cwd: string): string {
  return execFileSync("git", args, { cwd, encoding: "utf-8" });
}

function initRepo(dir: string, branch: string): void {
  fs.mkdirSync(dir, { recursive: true });
  git(["init", "--quiet"], dir);
  git(["symbolic-ref", "HEAD", `refs/heads/${branch}`], dir);
  git(["config", "user.email", "pipeline@nightgauge.test"], dir);
  git(["config", "user.name", "Nightgauge Test"], dir);
  git(["config", "commit.gpgsign", "false"], dir);
  fs.mkdirSync(path.join(dir, "src"), { recursive: true });
  fs.writeFileSync(path.join(dir, "README.md"), "base\n");
  fs.writeFileSync(path.join(dir, "src", "router.ts"), "export const routes = [];\n");
  fs.writeFileSync(path.join(dir, "src", "handlers.ts"), "export function handle() {}\n");
  git(["add", "-A"], dir);
  git(["commit", "--quiet", "-m", "base"], dir);
}

/** Full dirty state + HEAD + branch, for "nothing was touched" assertions. */
function repoState(dir: string): { status: string; head: string; branch: string; log: string } {
  return {
    status: git(["status", "--porcelain", "--untracked-files=all"], dir),
    head: git(["rev-parse", "HEAD"], dir).trim(),
    branch: git(["rev-parse", "--abbrev-ref", "HEAD"], dir).trim(),
    log: git(["log", "--oneline"], dir),
  };
}

beforeEach(() => {
  // realpath: on macOS `os.tmpdir()` is `/var/...`, a symlink to `/private/var/...`.
  // git reports the resolved form, so the test's own paths must be resolved too
  // or every path comparison below is comparing two spellings of one directory.
  tmp = fs.realpathSync(fs.mkdtempSync(path.join(os.tmpdir(), "ng-containment-")));
  primary = path.join(tmp, "primary");
  sibling = path.join(tmp, "sibling");
  initRepo(primary, "main");
  initRepo(sibling, "main");
  // A real linked worktree, so the stage CWD is genuinely a worktree of
  // `primary` rather than a lookalike directory.
  worktree = path.join(primary, ".worktrees", "issue-129");
  git(["worktree", "add", "--quiet", "-b", "fix/129", worktree, "HEAD"], primary);
});

afterEach(() => {
  fs.rmSync(tmp, { recursive: true, force: true });
});

/** What the incident produced in a sibling repo: modified files plus new ones. */
function stageWritesIntoSibling(): void {
  fs.writeFileSync(
    path.join(sibling, "src", "handlers.ts"),
    "export function handle() { /* impl */ }\n"
  );
  fs.writeFileSync(
    path.join(sibling, "src", "router.ts"),
    "export const routes = ['/v1/things'];\n"
  );
  fs.mkdirSync(path.join(sibling, "src", "api"), { recursive: true });
  fs.writeFileSync(path.join(sibling, "src", "api", "things.ts"), "export const things = 1;\n");
}

describe("resolveContainmentTargets — what counts as out of bounds (#129)", () => {
  it("excludes the stage's own directory and keeps every sibling", async () => {
    const targets = await resolveContainmentTargets(worktree, [primary, sibling, worktree]);
    expect(targets).toContain(sibling);
    expect(targets).not.toContain(worktree);
  });

  it("keeps the repo's MAIN checkout in scope when the stage runs in a linked worktree", async () => {
    // The incident's second repo was a main checkout sitting on `main`.
    // A worktree's parent repo is a DIFFERENT working tree, so it stays in
    // scope even though it contains the stage CWD.
    const targets = await resolveContainmentTargets(worktree, [primary, sibling]);
    expect(targets).toContain(primary);
  });

  it("excludes the repo itself when the stage runs directly in it (single-repo mode)", async () => {
    // No worktree: `primary` IS the stage's tree, so every write to it is legitimate.
    const targets = await resolveContainmentTargets(primary, [primary, sibling]);
    expect(targets).toEqual([sibling]);
  });
});

describe("(a) stage writes into a sibling repo (#129)", () => {
  it("detects the breach and names the repo and every path", async () => {
    const baseline = await captureContainmentBaseline({
      stageCwd: worktree,
      repoPaths: [primary, sibling],
    });
    stageWritesIntoSibling();

    const report = await detectContainmentBreach({
      baseline,
      stage: "feature-dev",
      issueNumber: 129,
    });

    expect(report.breaches).toHaveLength(1);
    const breach = report.breaches[0];
    expect(breach.repoName).toBe("sibling");
    expect(breach.repoPath).toBe(sibling);
    expect(breach.paths).toEqual(["src/api/things.ts", "src/handlers.ts", "src/router.ts"]);
    expect(breach.ambiguousPaths).toEqual([]);
  });

  it("reports a reason that names the repo and paths, not 'no implementation work detected'", async () => {
    const baseline = await captureContainmentBaseline({
      stageCwd: worktree,
      repoPaths: [primary, sibling],
    });
    stageWritesIntoSibling();
    const report = await detectContainmentBreach({
      baseline,
      stage: "feature-dev",
      issueNumber: 129,
    });

    const reason = formatContainmentFailure("feature-dev", report);
    expect(reason).toContain(CONTAINMENT_ERROR_MARKER);
    expect(reason).toContain("wrote outside its worktree");
    expect(reason).toContain("sibling");
    expect(reason).toContain(sibling);
    expect(reason).toContain("src/handlers.ts");
    expect(reason).toContain("src/api/things.ts");
    // The whole point: triage must not be sent downstream.
    expect(reason).not.toContain("no implementation work detected");
  });

  it("preserves the changes as an applicable patch outside the repo it came from", async () => {
    const baseline = await captureContainmentBaseline({
      stageCwd: worktree,
      repoPaths: [primary, sibling],
    });
    stageWritesIntoSibling();
    const report = await detectContainmentBreach({
      baseline,
      stage: "feature-dev",
      issueNumber: 129,
    });

    const patchPath = report.breaches[0].patchPath;
    expect(patchPath).toBeDefined();
    expect(fs.existsSync(patchPath!)).toBe(true);

    // Written under the stage repo's CANONICAL root's per-checkout directory
    // (ADR-024 § 7), which survives the worktree teardown a re-dispatch
    // performs — not in the worktree's own CHECKOUT (git deletes it with the
    // worktree), not inside the repo that was written to, and never in a
    // working tree.
    expect(patchPath!.startsWith(containmentDir(primary))).toBe(true);
    expect(containmentDir(primary)).toBe(
      path.join(primary, ".git", "nightgauge-worktree", "containment")
    );
    expect(patchPath!.startsWith(containmentDir(worktree))).toBe(false);
    expect(patchPath!.startsWith(sibling)).toBe(false);
    expect(patchPath!.startsWith(worktree)).toBe(false);
    expect(fs.existsSync(path.join(primary, ".nightgauge", "containment"))).toBe(false);

    const patch = fs.readFileSync(patchPath!, "utf-8");
    expect(patch).toContain("src/handlers.ts");
    expect(patch).toContain("/* impl */");
    expect(patch).toContain("'/v1/things'");
    // Untracked files must be in the patch too — they are pure new work with
    // no HEAD version to recover from.
    expect(patch).toContain("export const things = 1;");

    // And it is genuinely recoverable: a clean checkout at the same base takes it.
    const recovery = path.join(tmp, "recovery");
    git(["clone", "--quiet", sibling, recovery], tmp);
    execFileSync("git", ["apply", patchPath!], { cwd: recovery });
    expect(fs.readFileSync(path.join(recovery, "src", "handlers.ts"), "utf-8")).toContain(
      "/* impl */"
    );
    expect(fs.readFileSync(path.join(recovery, "src", "api", "things.ts"), "utf-8")).toContain(
      "export const things = 1;"
    );
  });

  it("writes a manifest naming the repo, the paths and the recovery command", async () => {
    const baseline = await captureContainmentBaseline({
      stageCwd: worktree,
      repoPaths: [primary, sibling],
    });
    stageWritesIntoSibling();
    const report = await detectContainmentBreach({
      baseline,
      stage: "feature-dev",
      issueNumber: 129,
    });

    const manifest = JSON.parse(
      fs.readFileSync(path.join(report.artifactDir!, "manifest.json"), "utf-8")
    );
    expect(manifest.stage).toBe("feature-dev");
    expect(manifest.issueNumber).toBe(129);
    expect(manifest.breaches[0].repoPath).toBe(sibling);
    expect(manifest.breaches[0].paths).toContain("src/handlers.ts");
    expect(manifest.note).toContain("apply");
    expect(manifest.note).toContain("<repoName>.patch");
  });

  it("does not modify, stage, commit or revert ANYTHING in the repo it inspected", async () => {
    const baseline = await captureContainmentBaseline({
      stageCwd: worktree,
      repoPaths: [primary, sibling],
    });
    stageWritesIntoSibling();
    const before = repoState(sibling);

    await detectContainmentBreach({ baseline, stage: "feature-dev", issueNumber: 129 });

    // Identical HEAD, branch, commit log and dirty state: no add, no commit,
    // no stash, no checkout. The files are exactly where the stage left them.
    expect(repoState(sibling)).toEqual(before);
    expect(fs.readFileSync(path.join(sibling, "src", "handlers.ts"), "utf-8")).toBe(
      "export function handle() { /* impl */ }\n"
    );
    expect(fs.existsSync(path.join(sibling, "src", "api", "things.ts"))).toBe(true);
    // Nothing was written INTO the sibling either.
    expect(fs.existsSync(path.join(sibling, ".nightgauge"))).toBe(false);
  });

  it("catches a write into the repo's own main checkout while the stage runs in a worktree", async () => {
    // The incident's second repo: same repository as the stage, but the main
    // checkout, sitting on `main`. A `git pull` there destroys the work.
    const baseline = await captureContainmentBaseline({
      stageCwd: worktree,
      repoPaths: [primary, sibling],
    });
    fs.writeFileSync(path.join(primary, "src", "router.ts"), "export const routes = ['leaked'];\n");

    const report = await detectContainmentBreach({ baseline, stage: "feature-dev" });
    expect(report.breaches.map((b) => b.repoName)).toEqual(["primary"]);
    expect(report.breaches[0].paths).toEqual(["src/router.ts"]);
  });
});

describe("(b) pre-existing operator work is never attributed to the stage (#129)", () => {
  it("ignores a sibling that was already dirty and that the stage never touched", async () => {
    // The operator has been working in this repo for an hour.
    fs.writeFileSync(path.join(sibling, "src", "router.ts"), "// operator work in progress\n");
    fs.writeFileSync(path.join(sibling, "operator-notes.md"), "scratch\n");
    const before = repoState(sibling);

    const baseline = await captureContainmentBaseline({
      stageCwd: worktree,
      repoPaths: [primary, sibling],
    });
    // Stage does its job inside its own worktree.
    fs.writeFileSync(
      path.join(worktree, "src", "handlers.ts"),
      "export function handle() { /* ok */ }\n"
    );

    const report = await detectContainmentBreach({ baseline, stage: "feature-dev" });

    expect(report.breaches).toEqual([]);
    expect(report.warnings).toEqual([]);
    expect(report.artifactDir).toBeUndefined();
    // Untouched, byte for byte.
    expect(repoState(sibling)).toEqual(before);
    expect(fs.readFileSync(path.join(sibling, "src", "router.ts"), "utf-8")).toBe(
      "// operator work in progress\n"
    );
    expect(fs.existsSync(containmentDir(primary))).toBe(false);
  });

  it("attributes only the stage's new paths when the operator is dirty in the same repo", async () => {
    fs.writeFileSync(path.join(sibling, "src", "router.ts"), "// operator work in progress\n");
    fs.writeFileSync(path.join(sibling, "operator-notes.md"), "scratch\n");

    const baseline = await captureContainmentBaseline({
      stageCwd: worktree,
      repoPaths: [primary, sibling],
    });
    fs.writeFileSync(
      path.join(sibling, "src", "handlers.ts"),
      "export function handle() { /* impl */ }\n"
    );

    const report = await detectContainmentBreach({
      baseline,
      stage: "feature-dev",
      issueNumber: 129,
    });

    expect(report.breaches).toHaveLength(1);
    expect(report.breaches[0].paths).toEqual(["src/handlers.ts"]);
    expect(report.breaches[0].paths).not.toContain("src/router.ts");
    expect(report.breaches[0].paths).not.toContain("operator-notes.md");

    // The operator's content must not leak into the captured patch either.
    const patch = fs.readFileSync(report.breaches[0].patchPath!, "utf-8");
    expect(patch).toContain("/* impl */");
    expect(patch).not.toContain("operator work in progress");
    expect(patch).not.toContain("scratch");
  });

  it("warns but does NOT fail when an already-dirty path changes during the stage", async () => {
    // Indistinguishable from the filesystem: the operator saving their own
    // in-progress file, or the stage editing a file the operator had dirtied.
    // The operator is the likelier explanation and the delta is an inseparable
    // mix, so this is reported and left alone — never a failure, never captured.
    fs.writeFileSync(path.join(sibling, "src", "router.ts"), "// operator work, revision 1\n");

    const baseline = await captureContainmentBaseline({
      stageCwd: worktree,
      repoPaths: [primary, sibling],
    });
    fs.writeFileSync(
      path.join(sibling, "src", "router.ts"),
      "// operator work, revision 2 (longer)\n"
    );

    const report = await detectContainmentBreach({ baseline, stage: "feature-dev" });

    expect(report.breaches).toEqual([]);
    expect(report.warnings).toHaveLength(1);
    expect(report.warnings[0].ambiguousPaths).toEqual(["src/router.ts"]);
    expect(report.artifactDir).toBeUndefined();
    expect(fs.existsSync(containmentDir(primary))).toBe(false);
    expect(formatContainmentWarning("feature-dev", report)).toContain("src/router.ts");
    expect(fs.readFileSync(path.join(sibling, "src", "router.ts"), "utf-8")).toBe(
      "// operator work, revision 2 (longer)\n"
    );
  });
});

describe("(c) clean siblings — no false positives (#129)", () => {
  it("reports nothing and writes nothing when the stage stays in its worktree", async () => {
    const baseline = await captureContainmentBaseline({
      stageCwd: worktree,
      repoPaths: [primary, sibling],
    });
    fs.writeFileSync(
      path.join(worktree, "src", "handlers.ts"),
      "export function handle() { /* ok */ }\n"
    );
    fs.writeFileSync(path.join(worktree, "src", "new-thing.ts"), "export const x = 1;\n");

    const report = await detectContainmentBreach({
      baseline,
      stage: "feature-dev",
      issueNumber: 129,
    });

    expect(report.breaches).toEqual([]);
    expect(report.warnings).toEqual([]);
    expect(report.artifactDir).toBeUndefined();
    expect(repoState(sibling).status).toBe("");
    expect(fs.existsSync(containmentDir(primary))).toBe(false);
  });

  it("ignores .nightgauge/ artifacts the pipeline mirrors into the canonical root by design", async () => {
    // `writeDiagnosticWithMirror` and friends write stage diagnostics, exit
    // records and run state into the canonical checkout on purpose. Counting
    // them would make every single run report a breach against its own repo.
    const baseline = await captureContainmentBaseline({
      stageCwd: worktree,
      repoPaths: [primary, sibling],
    });
    fs.mkdirSync(path.join(primary, ".nightgauge", "pipeline", "history", "129"), {
      recursive: true,
    });
    fs.writeFileSync(
      path.join(primary, ".nightgauge", "pipeline", "history", "129", "stall-diagnostic.json"),
      "{}\n"
    );

    const report = await detectContainmentBreach({ baseline, stage: "feature-dev" });
    expect(report.breaches).toEqual([]);
    expect(report.warnings).toEqual([]);
  });

  it("is a no-op when no other repo is configured (single-repo workspace)", async () => {
    const baseline = await captureContainmentBaseline({
      stageCwd: primary,
      repoPaths: [primary],
    });
    expect(baseline.snapshots).toEqual([]);

    fs.writeFileSync(path.join(primary, "src", "handlers.ts"), "// legitimate\n");
    const report = await detectContainmentBreach({ baseline, stage: "feature-dev" });
    expect(report.breaches).toEqual([]);
  });
});

describe("retry — a rewrite of the same paths is still the stage's (#129)", () => {
  it("re-attributes paths a previous attempt already escaped with", async () => {
    // The incident: "A retry repeated it, rewriting the same paths." Without
    // the ledger the second attempt reads its own leftovers as pre-existing
    // operator dirt and passes silently.
    const first = await captureContainmentBaseline({
      stageCwd: worktree,
      repoPaths: [primary, sibling],
      issueNumber: 129,
    });
    stageWritesIntoSibling();
    const firstReport = await detectContainmentBreach({
      baseline: first,
      stage: "feature-dev",
      issueNumber: 129,
    });
    expect(firstReport.breaches).toHaveLength(1);

    // Retry: the paths are dirty at baseline time now.
    const second = await captureContainmentBaseline({
      stageCwd: worktree,
      repoPaths: [primary, sibling],
      issueNumber: 129,
    });
    fs.writeFileSync(
      path.join(sibling, "src", "handlers.ts"),
      "export function handle() { /* v2 */ }\n"
    );

    const secondReport = await detectContainmentBreach({
      baseline: second,
      stage: "feature-dev",
      issueNumber: 129,
    });
    expect(secondReport.breaches).toHaveLength(1);
    expect(secondReport.breaches[0].paths).toContain("src/handlers.ts");
    const patch = fs.readFileSync(secondReport.breaches[0].patchPath!, "utf-8");
    expect(patch).toContain("/* v2 */");
  });

  it("forgets a remembered path once it is clean again, so the operator's next edit is theirs", async () => {
    // Ledger rot would be a false-positive vector of its own: an entry left
    // behind after the operator recovered the patch and reverted the file would
    // make their NEXT edit of that file look like a stage write forever.
    const first = await captureContainmentBaseline({
      stageCwd: worktree,
      repoPaths: [primary, sibling],
    });
    stageWritesIntoSibling();
    await detectContainmentBreach({ baseline: first, stage: "feature-dev", issueNumber: 129 });

    // The operator recovers the work and cleans the repo.
    git(["checkout", "--", "."], sibling);
    fs.rmSync(path.join(sibling, "src", "api"), { recursive: true, force: true });
    expect(git(["status", "--porcelain", "-uall"], sibling)).toBe("");

    // Much later they start their own edit to one of those same files, and only
    // THEN does a stage run. That is pre-existing operator work.
    fs.writeFileSync(path.join(sibling, "src", "handlers.ts"), "// operator, weeks later\n");
    const later = await captureContainmentBaseline({
      stageCwd: worktree,
      repoPaths: [primary, sibling],
    });
    const report = await detectContainmentBreach({ baseline: later, stage: "feature-validate" });

    expect(report.breaches).toEqual([]);
    expect(report.warnings).toEqual([]);
    expect(fs.readFileSync(path.join(sibling, "src", "handlers.ts"), "utf-8")).toBe(
      "// operator, weeks later\n"
    );
  });

  it("attributes a breach reported by both feature-dev and feature-validate on the same run (#127)", async () => {
    // #127's incident: feature-dev wrote across sibling repos and still
    // self-reported success; feature-validate ran next in the SAME sibling
    // working tree without cleaning up first. The ledger must attribute the
    // breach to whichever stage's detectContainmentBreach call runs, not
    // silently treat the second stage's baseline as "already dirty, not
    // ours" just because the first stage already recorded it.
    const first = await captureContainmentBaseline({
      stageCwd: worktree,
      repoPaths: [primary, sibling],
    });
    stageWritesIntoSibling();
    const devReport = await detectContainmentBreach({
      baseline: first,
      stage: "feature-dev",
      issueNumber: 127,
    });
    expect(devReport.breaches).toHaveLength(1);

    // feature-validate runs next in the same worktree, same dirty sibling —
    // no new baseline capture between stages in the real pipeline. The
    // breach must still be attributed and reported.
    const validateReport = await detectContainmentBreach({
      baseline: first,
      stage: "feature-validate",
      issueNumber: 127,
    });
    expect(validateReport.breaches).toHaveLength(1);
    expect(validateReport.breaches[0].paths).toContain("src/handlers.ts");
  });
});

describe("a bare ref move is not a stage write (#1499)", () => {
  /**
   * The 2026-09-06 incident, reproduced exactly.
   *
   * `git update-ref refs/heads/main <newer>` on a CHECKED-OUT branch moves HEAD
   * and nothing else. The index and working tree still hold the old commit's
   * content, so `git status` — which is relative to HEAD — reports every path
   * the two commits differ in as a staged change that no process wrote. The
   * containment baseline was taken against the old HEAD, so all of them read
   * `clean -> dirty` and got attributed to whichever stage closed next. Three
   * concurrent slots closed within two minutes and all three were killed for
   * the identical 31-path "breach" in a repo none had touched.
   */
  function advanceSiblingOriginAndMoveRefOnly(): { before: string; after: string } {
    const before = git(["rev-parse", "HEAD"], sibling).trim();
    // A commit reachable from a ref other than the checked-out branch, standing
    // in for the `origin/main` that had moved ahead overnight.
    git(["checkout", "--quiet", "-b", "ahead"], sibling);
    fs.writeFileSync(
      path.join(sibling, "src", "router.ts"),
      "export const routes = ['/merged'];\n"
    );
    fs.writeFileSync(path.join(sibling, "docs.md"), "merged docs\n");
    git(["add", "-A"], sibling);
    git(["commit", "--quiet", "-m", "two merged PRs"], sibling);
    const after = git(["rev-parse", "HEAD"], sibling).trim();
    git(["checkout", "--quiet", "main"], sibling);
    // THE MOVE. No pull, no fetch, no checkout: a raw ref write, exactly what
    // go-git's Storer.SetReference produces — and, like it, no reflog message.
    git(["update-ref", "refs/heads/main", after], sibling);
    return { before, after };
  }

  it("does not attribute dirt that only a HEAD move explains", async () => {
    const baseline = await captureContainmentBaseline({
      stageCwd: worktree,
      repoPaths: [primary, sibling],
    });
    const { before, after } = advanceSiblingOriginAndMoveRefOnly();

    // The precondition the incident turned on: the tree never moved, but git
    // now reports the whole delta as dirty.
    expect(git(["status", "--porcelain"], sibling).trim()).not.toBe("");

    const report = await detectContainmentBreach({
      baseline,
      stage: "feature-dev",
      issueNumber: 1499,
    });

    expect(report.breaches).toHaveLength(0);
    expect(report.refMoves).toHaveLength(1);
    expect(report.refMoves[0].repoName).toBe("sibling");
    expect(report.refMoves[0].baselineHead).toBe(before);
    expect(report.refMoves[0].postHead).toBe(after);
    expect(report.refMoves[0].branch).toBe("main");
    expect(report.refMoves[0].explainedPaths).toEqual(["docs.md", "src/router.ts"]);

    const warning = formatContainmentRefMoveWarning("feature-dev", report);
    expect(warning).toContain("sibling");
    expect(warning).toContain(before.slice(0, 12));
    expect(warning).toContain(after.slice(0, 12));
    expect(warning).toContain("2 path(s)");
  });

  it("still attributes a path the ref move does not explain", async () => {
    const baseline = await captureContainmentBaseline({
      stageCwd: worktree,
      repoPaths: [primary, sibling],
    });
    advanceSiblingOriginAndMoveRefOnly();
    // A real escape, on a path neither commit touched. Narrowing attribution
    // must not become disabling it.
    fs.mkdirSync(path.join(sibling, "src", "api"), { recursive: true });
    fs.writeFileSync(path.join(sibling, "src", "api", "things.ts"), "export const things = 1;\n");

    const report = await detectContainmentBreach({
      baseline,
      stage: "feature-dev",
      issueNumber: 1499,
    });

    expect(report.breaches).toHaveLength(1);
    expect(report.breaches[0].paths).toEqual(["src/api/things.ts"]);
    expect(report.refMoves).toHaveLength(1);
    expect(report.refMoves[0].explainedPaths).not.toContain("src/api/things.ts");
  });

  it("attributes normally when HEAD held still", async () => {
    const baseline = await captureContainmentBaseline({
      stageCwd: worktree,
      repoPaths: [primary, sibling],
    });
    stageWritesIntoSibling();
    const report = await detectContainmentBreach({ baseline, stage: "feature-dev" });
    expect(report.refMoves).toHaveLength(0);
    expect(report.breaches).toHaveLength(1);
    expect(report.breaches[0].paths).toContain("src/api/things.ts");
  });
});

describe("a sibling slot's own repo is not this stage's breach (#1499)", () => {
  /** A second repo with its own running slot, as `max_concurrent: 2` produces. */
  let secondRepo: string;
  let secondWorktree: string;

  beforeEach(() => {
    secondRepo = path.join(tmp, "second");
    initRepo(secondRepo, "main");
    secondWorktree = path.join(secondRepo, ".worktrees", "issue-800");
    git(["worktree", "add", "--quiet", "-b", "fix/800", secondWorktree, "HEAD"], secondRepo);
  });

  it("demotes a repo another running slot owns to warning-only", async () => {
    // Slot A: this stage, in `primary`'s worktree. Slot B: another pipeline,
    // in `secondRepo`'s worktree. Both are in the running set.
    const baseline = await captureContainmentBaseline({
      stageCwd: worktree,
      repoPaths: [primary, sibling, secondRepo],
      runningWorktreePaths: [worktree, secondWorktree],
    });
    expect(baseline.warnOnlyRepoPaths).toEqual([secondRepo]);

    // Slot B does its job in its own repo root: a pr stage fetching and
    // writing there is legitimate, and invisible as such from slot A.
    fs.writeFileSync(path.join(secondRepo, "src", "router.ts"), "export const routes = ['/b'];\n");

    const report = await detectContainmentBreach({ baseline, stage: "feature-dev" });

    expect(report.breaches).toHaveLength(0);
    expect(report.warnings).toHaveLength(1);
    expect(report.warnings[0].repoName).toBe("second");
    expect(report.warnings[0].paths).toEqual(["src/router.ts"]);
    expect(report.warnings[0].warnOnlyReason).toContain("another running pipeline");
    expect(formatContainmentWarning("feature-dev", report)).toContain("src/router.ts");
  });

  it("keeps the stage's OWN repo a hard breach while its slot is running", async () => {
    // This stage's slot is in the running set too. Demoting on that basis
    // would disable #129 entirely — the incident's second repo WAS the
    // stage's own main checkout.
    const baseline = await captureContainmentBaseline({
      stageCwd: worktree,
      repoPaths: [primary, sibling, secondRepo],
      runningWorktreePaths: [worktree, secondWorktree],
    });
    expect(baseline.warnOnlyRepoPaths).not.toContain(primary);

    fs.writeFileSync(path.join(primary, "src", "router.ts"), "export const routes = ['/own'];\n");
    const report = await detectContainmentBreach({ baseline, stage: "feature-dev" });

    expect(report.breaches.map((b) => b.repoName)).toEqual(["primary"]);
  });

  it("still fails a repo NO running slot owns", async () => {
    const baseline = await captureContainmentBaseline({
      stageCwd: worktree,
      repoPaths: [primary, sibling, secondRepo],
      runningWorktreePaths: [worktree, secondWorktree],
    });
    stageWritesIntoSibling();
    const report = await detectContainmentBreach({ baseline, stage: "feature-dev" });
    expect(report.breaches.map((b) => b.repoName)).toEqual(["sibling"]);
  });
});

describe("parsePorcelainZ", () => {
  it("consumes the source field of a rename so following entries are not misparsed", () => {
    const stdout = "R  new.ts\0old.ts\0 M src/router.ts\0?? untracked.ts\0";
    expect(parsePorcelainZ(stdout)).toEqual([
      { code: "R ", path: "new.ts" },
      { code: " M", path: "src/router.ts" },
      { code: "??", path: "untracked.ts" },
    ]);
  });

  it("keeps paths with spaces intact (no git quoting to undo)", () => {
    expect(parsePorcelainZ("?? src/a file.ts\0")).toEqual([{ code: "??", path: "src/a file.ts" }]);
  });
});

describe("a pipeline worktree outside the repo root (#2038, ADR-024 § 9)", () => {
  /** A Go-manager worktree at `STATE/worktrees/<repo-key>/primary-issue-2038`. */
  let outside: string;

  beforeEach(() => {
    outside = path.join(tmp, "state", "worktrees", "0123456789ab", "primary-issue-2038");
    git(["worktree", "add", "--quiet", "-b", "fix/2038", outside, "HEAD"], primary);
    expect(path.relative(primary, outside).startsWith("..")).toBe(true);
  });

  it("accepts the stage's own writes there and keeps the main checkout in scope", async () => {
    const targets = await resolveContainmentTargets(outside, [primary, sibling]);
    expect(targets).toEqual([primary, sibling]);

    const baseline = await captureContainmentBaseline({
      stageCwd: outside,
      repoPaths: [primary, sibling],
    });
    fs.writeFileSync(path.join(outside, "src", "handlers.ts"), "export function handle() {}\n//\n");
    fs.writeFileSync(path.join(outside, "src", "new-thing.ts"), "export const x = 1;\n");

    const report = await detectContainmentBreach({
      baseline,
      stage: "feature-dev",
      issueNumber: 2038,
    });
    expect(report.breaches).toEqual([]);
    expect(report.warnings).toEqual([]);
  });

  it("still catches a write into the repo's own main checkout", async () => {
    const baseline = await captureContainmentBaseline({
      stageCwd: outside,
      repoPaths: [primary, sibling],
    });
    fs.writeFileSync(path.join(primary, "src", "router.ts"), "export const routes = ['leaked'];\n");

    const report = await detectContainmentBreach({
      baseline,
      stage: "feature-dev",
      issueNumber: 2038,
    });
    expect(report.breaches.map((b) => b.repoName)).toEqual(["primary"]);
    expect(report.breaches[0].paths).toEqual(["src/router.ts"]);
    // Artifacts go to the main checkout, which outlives the worktree.
    expect(report.artifactDir?.startsWith(containmentDir(primary))).toBe(true);
  });
});

describe("the retry ledger is scoped to the issue that recorded it (#2608)", () => {
  function ledgerFile(): string {
    return path.join(containmentDir(primary), "attributed.json");
  }

  async function attributeToIssue129(): Promise<void> {
    const first = await captureContainmentBaseline({
      stageCwd: worktree,
      repoPaths: [primary, sibling],
      issueNumber: 129,
    });
    stageWritesIntoSibling();
    const report = await detectContainmentBreach({
      baseline: first,
      stage: "feature-dev",
      issueNumber: 129,
    });
    expect(report.breaches).toHaveLength(1);
  }

  it("does not blame another issue for a path attributed to issue 129 and left untouched", async () => {
    await attributeToIssue129();

    const other = await captureContainmentBaseline({
      stageCwd: worktree,
      repoPaths: [primary, sibling],
      issueNumber: 2608,
    });
    const report = await detectContainmentBreach({
      baseline: other,
      stage: "feature-dev",
      issueNumber: 2608,
    });
    expect(report.breaches).toEqual([]);
    expect(report.warnings).toEqual([]);

    // The entry survives for its owner: a retry of 129 that rewrites it breaches.
    const retry = await captureContainmentBaseline({
      stageCwd: worktree,
      repoPaths: [primary, sibling],
      issueNumber: 129,
    });
    fs.writeFileSync(path.join(sibling, "src", "handlers.ts"), "// retry of 129\n");
    const retryReport = await detectContainmentBreach({
      baseline: retry,
      stage: "feature-dev",
      issueNumber: 129,
    });
    expect(retryReport.breaches).toHaveLength(1);
    expect(retryReport.breaches[0].paths).toContain("src/handlers.ts");
  });

  it("reports another issue's change to that path as ambiguous, never as a breach", async () => {
    await attributeToIssue129();

    const other = await captureContainmentBaseline({
      stageCwd: worktree,
      repoPaths: [primary, sibling],
      issueNumber: 2608,
    });
    fs.writeFileSync(path.join(sibling, "src", "handlers.ts"), "// issue 2608 edit\n");
    const report = await detectContainmentBreach({
      baseline: other,
      stage: "feature-dev",
      issueNumber: 2608,
    });
    expect(report.breaches).toEqual([]);
    expect(report.warnings).toHaveLength(1);
    expect(report.warnings[0].ambiguousPaths).toContain("src/handlers.ts");
  });

  it("ignores and rewrites a legacy unowned ledger", async () => {
    await attributeToIssue129();
    // Rewrite the ledger in the pre-#2608 shape with fingerprints that match.
    const v2 = JSON.parse(fs.readFileSync(ledgerFile(), "utf-8"));
    const legacy: Record<string, Record<string, string>> = {};
    for (const [repo, files] of Object.entries(v2.repos as Record<string, object>)) {
      legacy[repo] = {};
      for (const [file, entry] of Object.entries(
        files as Record<string, { fingerprint: string }>
      )) {
        legacy[repo][file] = entry.fingerprint;
      }
    }
    fs.writeFileSync(ledgerFile(), JSON.stringify(legacy));

    const baseline = await captureContainmentBaseline({
      stageCwd: worktree,
      repoPaths: [primary, sibling],
      issueNumber: 129,
    });
    const report = await detectContainmentBreach({
      baseline,
      stage: "feature-dev",
      issueNumber: 129,
    });
    expect(report.breaches).toEqual([]);
    expect(JSON.parse(fs.readFileSync(ledgerFile(), "utf-8"))).toEqual({ version: 2, repos: {} });
  });
});

describe("sub-agent calls and an honest write record (#2660)", () => {
  it("is available and names a sibling path a Task's nested Write wrote", () => {
    const record = new StageWriteRecord(worktree);
    record.observeToolUse("Task", { prompt: "go" }, { id: "t1" });
    record.observeToolUse(
      "Write",
      { file_path: path.join(sibling, "src", "a.ts") },
      { id: "w1", parentToolUseId: "t1" }
    );
    expect(record.available).toBe(true);
    expect(record.names([sibling], "src/a.ts")).toBe(true);
  });

  it("is unavailable when a Task contributed no nested calls", () => {
    const record = new StageWriteRecord(worktree);
    record.observeToolUse("Task", { prompt: "go" }, { id: "t1" });
    record.observeToolUse("Write", { file_path: path.join(worktree, "a.ts") }, { id: "w1" });
    expect(record.available).toBe(false);
    expect(record.unavailableReason).toContain("write record incomplete: 1 sub-agent call(s)");
  });

  it("excuses a rejected Task once the stream has linked another Task's calls", () => {
    const record = new StageWriteRecord(worktree);
    record.observeToolUse("Task", { subagent_type: "nope" }, { id: "t1" });
    record.observeToolResult("t1", true);
    record.observeToolUse("Task", { prompt: "go" }, { id: "t2" });
    record.observeToolUse("Read", { file_path: "a" }, { id: "r1", parentToolUseId: "t2" });
    expect(record.available).toBe(true);
  });

  it("does not excuse an errored silent Task when no nested call ever linked", () => {
    const record = new StageWriteRecord(worktree);
    record.observeToolUse("Task", { prompt: "go" }, { id: "t1" });
    record.observeToolResult("t1", true);
    expect(record.available).toBe(false);
  });

  it("stays unavailable when a silent Task's tool_result succeeded", () => {
    const record = new StageWriteRecord(worktree);
    record.observeToolUse("Task", { prompt: "go" }, { id: "t1" });
    record.observeToolResult("t1", false);
    expect(record.available).toBe(false);
  });

  it("links a nested call reported only by a later, deduped delivery", () => {
    const record = new StageWriteRecord(worktree);
    record.observeToolUse("Task", { prompt: "go" }, { id: "t1" });
    record.observeToolUse("Write", { file_path: path.join(worktree, "a.ts") }, { id: "w1" });
    record.observeNestedCall("t1");
    expect(record.available).toBe(true);
  });

  it("is unavailable when a Workflow contributed no nested calls", () => {
    const record = new StageWriteRecord(worktree);
    record.observeToolUse("Workflow", { script: "x" }, { id: "wf1" });
    expect(record.available).toBe(false);
    record.observeNestedCall("wf1");
    expect(record.available).toBe(true);
  });

  it("is unavailable when a delegation carries no id", () => {
    const record = new StageWriteRecord(worktree);
    record.observeToolUse("Agent", { prompt: "go" });
    expect(record.available).toBe(false);
  });

  it("is unavailable once the total recorded text cap trips", () => {
    const record = new StageWriteRecord(worktree);
    const chunk = `echo ${"x".repeat(60 * 1024)} > out.txt`;
    for (let i = 0; i < 80; i++) record.observeToolUse("Bash", { command: chunk });
    expect(record.available).toBe(false);
    expect(record.unavailableReason).toContain("MiB cap");
  });

  it("prints the record's reason instead of the no-record sentence", async () => {
    const baseline = await captureContainmentBaseline({
      stageCwd: worktree,
      repoPaths: [primary, sibling],
    });
    stageWritesIntoSibling();
    const record = new StageWriteRecord(worktree);
    record.observeToolUse("Task", { prompt: "go" }, { id: "t1" });
    const report = await detectContainmentBreach({
      baseline,
      stage: "feature-dev",
      issueNumber: 2660,
      writeRecord: record,
    });
    expect(report.breaches[0].attribution).toBe("filesystem-diff");
    expect(report.breaches[0].attributionNote).toContain("write record incomplete");
    const text = formatContainmentFailure("feature-dev", report);
    expect(text).toContain("write record incomplete");
    expect(text).not.toContain("adapter supplied no write record");
  });
});

describe("a concurrent edit is not the stage's write (#2656)", () => {
  /** A record of a stage that wrote only inside its own worktree. */
  function worktreeOnlyRecord(): StageWriteRecord {
    const record = new StageWriteRecord(worktree);
    record.observeToolUse("Write", { file_path: path.join(worktree, "src", "handlers.ts") });
    record.observeToolUse("Bash", { command: "npx vitest run src/handlers.ts" });
    return record;
  }

  async function baselineFor(issueNumber?: number) {
    return captureContainmentBaseline({
      stageCwd: worktree,
      repoPaths: [primary, sibling],
      ...(issueNumber !== undefined ? { issueNumber } : {}),
    });
  }

  it("warns, never fails, when a clean sibling file is dirtied by someone else (the #2655 incident)", async () => {
    const baseline = await baselineFor();
    // The stage works in its worktree...
    fs.writeFileSync(path.join(worktree, "src", "handlers.ts"), "// stage work\n");
    // ...while the supervising session edits a clean file in a sibling repo.
    fs.writeFileSync(path.join(sibling, "README.md"), "operator edit\n");

    const report = await detectContainmentBreach({
      baseline,
      stage: "feature-dev",
      issueNumber: 2656,
      writeRecord: worktreeOnlyRecord(),
    });

    expect(report.breaches).toEqual([]);
    expect(report.warnings).toHaveLength(1);
    expect(report.warnings[0].repoName).toBe("sibling");
    expect(report.warnings[0].paths).toEqual([]);
    expect(report.warnings[0].unattributedPaths).toEqual(["README.md"]);
    expect(report.warnings[0].attribution).toBe("write-record");
    const warning = formatContainmentWarning("feature-dev", report);
    expect(warning).toContain("[containment-ambiguous]");
    expect(warning).toContain("sibling: README.md");
    expect(warning).toContain("#2656");
    // Nothing captured, nothing remembered against the stage.
    expect(fs.existsSync(path.join(containmentDir(primary), "attributed.json"))).toBe(false);
    expect(fs.readFileSync(path.join(sibling, "README.md"), "utf-8")).toBe("operator edit\n");
  });

  it("fails and preserves a sibling file a write tool named", async () => {
    const baseline = await baselineFor();
    const record = worktreeOnlyRecord();
    record.observeToolUse("Write", { file_path: path.join(sibling, "src", "handlers.ts") });
    stageWritesIntoSibling();

    const report = await detectContainmentBreach({
      baseline,
      stage: "feature-dev",
      issueNumber: 129,
      writeRecord: record,
    });

    expect(report.breaches).toHaveLength(1);
    expect(report.breaches[0].paths).toEqual(["src/handlers.ts"]);
    expect(report.breaches[0].attribution).toBe("write-record");
    const patch = fs.readFileSync(report.breaches[0].patchPath!, "utf-8");
    expect(patch).toContain("/* impl */");
  });

  it("attributes a shell command that names the repo and the path, not one that names only the path", async () => {
    const named = new StageWriteRecord(worktree);
    named.observeToolUse("Bash", { command: "cd ../../../sibling && echo x > src/router.ts" });
    const roots = [sibling];
    expect(named.names(roots, "src/router.ts")).toBe(true);
    expect(named.names(roots, "src/handlers.ts")).toBe(false);

    const absolute = new StageWriteRecord(worktree);
    absolute.observeToolUse("Bash", { command: `sed -i '' s/a/b/ ${sibling}/src/router.ts` });
    expect(absolute.names(roots, "src/router.ts")).toBe(true);

    const local = new StageWriteRecord(worktree);
    local.observeToolUse("Bash", { command: "echo x > src/router.ts" });
    expect(local.names(roots, "src/router.ts")).toBe(false);

    // A worktree under the repo's main checkout: the absolute CWD contains
    // the repo root, but that is the stage's own tree, not the repo.
    const own = new StageWriteRecord(worktree);
    own.observeToolUse("Bash", { command: `cd ${worktree} && echo x > README.md` });
    expect(own.names([primary], "README.md")).toBe(false);

    // Reading a sibling file is not writing it.
    const read = new StageWriteRecord(worktree);
    read.observeToolUse("Read", { file_path: path.join(sibling, "src", "router.ts") });
    expect(read.available).toBe(true);
    expect(read.names(roots, "src/router.ts")).toBe(false);
  });

  it("does not attribute a read-only shell command that names a sibling path (#2659)", () => {
    const roots = [sibling];
    for (const command of [
      `cat ${sibling}/src/router.ts`,
      `git -C ${sibling} diff src/router.ts`,
      "git -C ../../../sibling diff src/router.ts",
      "cd ../../../sibling && cat src/router.ts | head -5",
      `grep -n route ${sibling}/src/router.ts 2>/dev/null`,
    ]) {
      const record = new StageWriteRecord(worktree);
      record.observeToolUse("Bash", { command });
      expect(record.available).toBe(true);
      expect(record.names(roots, "src/router.ts"), command).toBe(false);
    }
  });

  it("does not attribute a path carried by a tool that is not a write tool (#2659)", () => {
    const record = new StageWriteRecord(worktree);
    const target = path.join(sibling, "src", "router.ts");
    record.observeToolUse("mcp__fs__read_text_file", { path: target });
    record.observeToolUse("NotebookRead", { notebook_path: target });
    record.observeToolUse("view_image", { file_path: target });
    expect(record.available).toBe(true);
    expect(record.names([sibling], "src/router.ts")).toBe(false);
  });

  it("does not treat a bare word equal to the repo name as a mention of the repo (#2659)", () => {
    const record = new StageWriteRecord(worktree);
    record.observeToolUse("Bash", { command: "sibling layout path plans && touch src/router.ts" });
    expect(record.names([sibling], "src/router.ts")).toBe(false);
    const cd = new StageWriteRecord(worktree);
    cd.observeToolUse("Bash", { command: "cd sibling && touch src/router.ts" });
    expect(cd.names([sibling], "src/router.ts")).toBe(true);
  });

  it("still attributes genuine sibling writes through shell and patch tools (#2659)", () => {
    const roots = [sibling];
    const cases: Array<[string, unknown]> = [
      ["Edit", { file_path: path.join(sibling, "src", "router.ts") }],
      ["Bash", { command: `cat x | tee ${sibling}/src/router.ts` }],
      ["Bash", { command: "cd ../../../sibling && git status && echo x >> src/router.ts" }],
      ["Bash", { command: `cat > ${sibling}/src/router.ts <<'EOF'\nx\nEOF` }],
      ["Bash", { command: `cp a.ts ${sibling}/src/router.ts` }],
      ["apply_patch", { input: `*** Update File: ${sibling}/src/router.ts\n` }],
    ];
    for (const [tool, input] of cases) {
      const record = new StageWriteRecord(worktree);
      record.observeToolUse(tool, input);
      expect(record.names(roots, "src/router.ts"), JSON.stringify(input)).toBe(true);
    }
  });

  it("warns, never fails, when the stage only read a sibling file someone else then edited (#2659)", async () => {
    const baseline = await baselineFor();
    const record = worktreeOnlyRecord();
    record.observeToolUse("Bash", { command: `cat ${sibling}/README.md` });
    fs.writeFileSync(path.join(sibling, "README.md"), "operator edit\n");

    const report = await detectContainmentBreach({
      baseline,
      stage: "feature-dev",
      issueNumber: 2659,
      writeRecord: record,
    });

    expect(report.breaches).toEqual([]);
    expect(report.warnings[0].unattributedPaths).toEqual(["README.md"]);
  });

  it("splits a mixed repo: the named path fails and is patched, the rest is listed as not attributed", async () => {
    const baseline = await baselineFor();
    const record = worktreeOnlyRecord();
    record.observeToolUse("Edit", { file_path: path.join(sibling, "src", "router.ts") });
    fs.writeFileSync(path.join(sibling, "src", "router.ts"), "export const routes = [1];\n");
    fs.writeFileSync(path.join(sibling, "README.md"), "operator edit\n");

    const report = await detectContainmentBreach({
      baseline,
      stage: "feature-dev",
      issueNumber: 129,
      writeRecord: record,
    });

    expect(report.breaches).toHaveLength(1);
    const breach = report.breaches[0];
    expect(breach.paths).toEqual(["src/router.ts"]);
    expect(breach.unattributedPaths).toEqual(["README.md"]);
    const reason = formatContainmentFailure("feature-dev", report);
    expect(reason).toContain("never names them; not attributed");
    expect(reason).toContain("README.md");
    expect(reason).not.toContain("filesystem diff only");
    const patch = fs.readFileSync(breach.patchPath!, "utf-8");
    expect(patch).toContain("src/router.ts");
    expect(patch).not.toContain("README.md");
  });

  it.each([
    ["no write record", undefined],
    ["a record with no tool calls", "empty"],
  ])("keeps failing with %s, and says attribution was by filesystem diff only", async (_, kind) => {
    const baseline = await baselineFor();
    fs.writeFileSync(path.join(sibling, "README.md"), "edit\n");

    const report = await detectContainmentBreach({
      baseline,
      stage: "feature-dev",
      issueNumber: 129,
      ...(kind === "empty" ? { writeRecord: new StageWriteRecord(worktree) } : {}),
    });

    expect(report.breaches).toHaveLength(1);
    expect(report.breaches[0].paths).toEqual(["README.md"]);
    expect(report.breaches[0].attribution).toBe("filesystem-diff");
    expect(formatContainmentFailure("feature-dev", report)).toContain(
      "Attribution was by filesystem diff only"
    );
  });

  it("keeps this issue's ledger-remembered path attributed on retry even when the record does not name it", async () => {
    const first = await baselineFor(129);
    stageWritesIntoSibling();
    const firstReport = await detectContainmentBreach({
      baseline: first,
      stage: "feature-dev",
      issueNumber: 129,
    });
    expect(firstReport.breaches).toHaveLength(1);

    const second = await baselineFor(129);
    expect(second.snapshots.find((s) => s.repoPath === sibling)?.ledgerPaths).toContain(
      "src/handlers.ts"
    );
    const secondReport = await detectContainmentBreach({
      baseline: second,
      stage: "feature-dev",
      issueNumber: 129,
      writeRecord: worktreeOnlyRecord(),
    });
    expect(secondReport.breaches).toHaveLength(1);
    expect(secondReport.breaches[0].paths).toEqual([
      "src/api/things.ts",
      "src/handlers.ts",
      "src/router.ts",
    ]);
    expect(secondReport.breaches[0].unattributedPaths).toEqual([]);
  });
});
