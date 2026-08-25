package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/nightgauge/nightgauge/internal/config"
	"github.com/nightgauge/nightgauge/internal/execution"
	gh "github.com/nightgauge/nightgauge/internal/github"
	"github.com/nightgauge/nightgauge/internal/orchestrator"
	"github.com/nightgauge/nightgauge/internal/reclaim"
	"github.com/nightgauge/nightgauge/internal/state"
	"github.com/spf13/cobra"
)

// worktreeCmd groups worktree reclamation (#110). Removal normally happens
// inline when a run finishes and, for runs that never reached their cleanup
// step, folds into the autonomous reconcile pass — `sweep` exposes the same
// pass for manual/CI invocation.
func worktreeCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "worktree",
		Short: "Pipeline worktree reclamation",
	}
	cmd.AddCommand(worktreeSweepCmd())
	cmd.AddCommand(worktreeRecoverCmd())
	return cmd
}

// worktreeRecoverCmd exposes the scheduler's uncommitted-work rescue (#3542) to
// any caller, which in practice means the extension's HeadlessOrchestrator
// (#223). The rescue existed only as an in-process call on the Go scheduler's
// failure path, so runs driven by the extension — most of them — had none: #221
// left a complete, passing implementation uncommitted in its worktree, one
// sweep away from deletion, while the halt card reported zero file changes.
func worktreeRecoverCmd() *cobra.Command {
	var (
		worktree string
		issue    int
		stage    string
		jsonOut  bool
	)

	cmd := &cobra.Command{
		Use:   "recover",
		Short: "Commit a worktree's uncommitted work so a failed stage's output survives",
		Long: `Stage everything in a pipeline worktree, commit it as an auto-recovery
commit, and push. The pipeline's own UNTRACKED exhaust under .nightgauge/ and
.claude/ is excluded so the rescue never publishes it into the issue branch — but
a tracked bookkeeping file the stage changed IS the deliverable and is committed.

Idempotent: a clean worktree is a no-op, reported as recovered=false. The push
is best-effort — a push failure still leaves the recovery commit on the local
branch, which is what actually prevents the work being lost.`,
		SilenceUsage: true,
		Example: `  nightgauge worktree recover --worktree .worktrees/issue-221 --issue 221 --stage feature-dev
  nightgauge worktree recover --worktree .worktrees/issue-221 --issue 221 --stage feature-dev --json`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if worktree == "" {
				return fmt.Errorf("--worktree is required")
			}
			if issue <= 0 {
				return fmt.Errorf("--issue is required and must be positive")
			}
			if stage == "" {
				stage = "unknown"
			}

			dirty, err := worktreeHasChanges(worktree)
			if err != nil {
				return fmt.Errorf("inspect %s: %w", worktree, err)
			}
			if !dirty {
				return emitRecoverResult(cmd, jsonOut, false, "worktree is clean — nothing to recover")
			}

			if err := orchestrator.RecoverUncommittedWork(worktree, issue, stage); err != nil {
				return fmt.Errorf("recover %s: %w", worktree, err)
			}
			return emitRecoverResult(cmd, jsonOut, true,
				fmt.Sprintf("committed uncommitted %s work on #%d", stage, issue))
		},
	}

	cmd.Flags().StringVar(&worktree, "worktree", "", "Path to the pipeline worktree (required)")
	cmd.Flags().IntVar(&issue, "issue", 0, "Issue number the worktree belongs to (required)")
	cmd.Flags().StringVar(&stage, "stage", "", "Stage whose work is being recovered (recorded in the commit message)")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "Output the result as JSON")
	return cmd
}

// worktreeHasChanges reports whether the worktree holds anything worth
// rescuing. The pipeline's own untracked exhaust is excluded for the same
// reason RecoverUncommittedWork unstages it: a worktree whose only diff is the
// run's own state files has produced nothing, and treating that as recoverable
// would create an empty commit on every failed stage.
//
// A TRACKED bookkeeping change is not exhaust and counts (#332). This check
// used to run against ci.DeliverablePathspec(), which excludes `.nightgauge`
// wholesale — so a stage whose entire deliverable was untracking pipeline
// assessments read as "clean", and `recover` reported "nothing to recover" over
// 209 staged deletions. Gating the rescue on the same classifier the rescue
// itself uses is what keeps the two answers from disagreeing.
func worktreeHasChanges(path string) (bool, error) {
	out, err := exec.Command("git", "-C", path, "status", "--porcelain", "--untracked-files=all").Output()
	if err != nil {
		return false, err
	}
	return reclaim.ClassifyStatus(string(out)).Blocked(), nil
}

func emitRecoverResult(cmd *cobra.Command, jsonOut, recovered bool, message string) error {
	if jsonOut {
		return json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]any{
			"recovered": recovered,
			"message":   message,
		})
	}
	fmt.Fprintln(cmd.OutOrStdout(), message)
	return nil
}

// worktreeSweepOutput is what `sweep --json` emits. An ARRAY of per-root
// results, because the command's default is now the whole workspace (#410) — a
// single bare object could only ever describe one of them.
type worktreeSweepOutput struct {
	Results []execution.WorktreeSweepResult `json:"results"`
	// Warnings carries the in-flight scan's own uncertainty per root, so a
	// reclaim decision made against an incomplete in-flight set is auditable
	// from the command's output instead of only from the operator's memory.
	Warnings []string `json:"warnings,omitempty"`
	// SkippedRoots names every root that was NOT swept, each WITH ITS REASON.
	// Three different failures land here and they are not interchangeable — a
	// root nobody could canonicalize, a root whose in-flight set was unreadable,
	// and a root whose git sweep failed after the in-flight set read fine. A
	// single string list forced every consumer to assert one cause for all
	// three, which is the same defect class as the sweep's old "content already
	// on <base>" line: a report naming a check that was not the one performed.
	SkippedRoots []skippedRoot `json:"skippedRoots,omitempty"`
}

// skippedRoot is one root the sweep declined to touch, and why.
type skippedRoot struct {
	Root   string `json:"root"`
	Reason string `json:"reason"`
}

// The reasons a root is skipped. Stable strings: the extension logs them
// verbatim rather than inferring a cause from the field name.
const (
	// skipNotAGitRepo — config.MainCheckoutRoot could not resolve a main
	// checkout (not a git repo, or a bare one). Never swept: without a
	// canonical root there is no snapshot directory to protect it with.
	skipNotAGitRepo = "not-a-git-repo"
	// skipInFlightUnreadable — the snapshot scan failed. "I could not look" is
	// never "nothing is running" (#296).
	skipInFlightUnreadable = "in-flight-set-unreadable"
	// skipSweepFailed — the in-flight set read fine and the git sweep itself
	// failed (git unusable, base ref unresolvable).
	skipSweepFailed = "sweep-failed"
)

func worktreeSweepCmd() *cobra.Command {
	var (
		workdir       string
		defaultBranch string
		dryRun        bool
		jsonOut       bool
	)

	cmd := &cobra.Command{
		Use:   "sweep",
		Short: "Reclaim pipeline worktrees whose branch already landed",
		Long: `Scan the worktrees git has registered across the workspace and remove the
pipeline-created ones (issue-NNN / <repo>-issue-NNN) whose branch carries no
content the default branch is missing.

Every repo root in the workspace is swept by default — a run's worktree lives in
its TARGET repo, so a single-root sweep is blind to exactly the cross-repo runs
that leak most. Pass --workdir to sweep one repository instead.

Merged-ness is decided by content diff against origin/<default>, not by
ancestry: a squash merge leaves the branch tip a non-ancestor of the default
branch, so an ancestry check reports a false negative for every merged branch.

Every root is canonicalized to its repository's MAIN checkout first, including an
explicit --workdir. A linked worktree has a .nightgauge/pipeline directory of its
own (the .gitkeep is tracked) and it is always empty, so a sweep rooted there
would read "no runs in flight" while git still listed — and this command still
removed — every worktree of the repository.

Runs in flight are protected by their runtime snapshots
(.nightgauge/pipeline/runtime-<issue>-<runId>.json), read per canonicalized root,
plus the in-flight sidecar (.nightgauge/pipeline/current-run.json) when the
process it names is alive: an issue with a live run is never reclaimed however
merged its branch looks. A root whose snapshot directory cannot be read, or that
resolves to no main checkout at all, is skipped entirely rather than swept blind.

Never removed: the primary checkout, locked worktrees, detached worktrees,
worktrees with uncommitted or untracked changes, branches carrying unmerged
work, branches with no commits of their own (indistinguishable from a worktree
created for a run about to start), and any issue with a run in flight.`,
		SilenceUsage: true,
		Example: `  nightgauge worktree sweep --dry-run
  nightgauge worktree sweep --json
  nightgauge worktree sweep --workdir /path/to/repo`,
		RunE: func(cmd *cobra.Command, args []string) error {
			out := cmd.OutOrStdout()
			errOut := cmd.ErrOrStderr()

			roots, skipped, err := worktreeSweepRoots(workdir)
			if err != nil {
				return err
			}

			result := worktreeSweepOutput{SkippedRoots: skipped}
			for _, s := range skipped {
				fmt.Fprintf(errOut, "[WARN] worktree sweep: %s: %s — root NOT swept\n", s.Root, s.Reason)
			}
			for _, root := range roots {
				// THE IN-FLIGHT SET IS RESOLVED PER ROOT, from that root's own
				// snapshot directory (#410). A run's state is rooted in its
				// target repo since #229, so one shared set would answer with a
				// sibling repo's runs — and this command deletes directories.
				//
				// `root` is a MAIN CHECKOUT by construction (worktreeSweepRoots
				// canonicalizes every root, --workdir included), which is the
				// precondition ActiveIssuesFromSnapshots documents: a linked
				// worktree's state dir exists, is empty, and answers "nothing is
				// running" with no error at all.
				active, scanErr := state.ActiveIssuesFromSnapshots(state.PipelineStateDir(root))
				if scanErr != nil {
					// "I could not look" is never "nothing is running" (#296).
					// The only protection this command has against destroying a
					// live run's worktree is this set, so an unreadable snapshot
					// dir disqualifies the root rather than downgrading to an
					// unprotected sweep.
					if len(roots) == 1 {
						// Nothing else to report and nothing was swept: exit
						// non-zero rather than printing a warning that reads
						// like a clean pass.
						return fmt.Errorf("worktree sweep: %s: in-flight set unreadable, refusing to sweep blind: %w", root, scanErr)
					}
					fmt.Fprintf(errOut, "[WARN] worktree sweep: %s: in-flight set unreadable (%v) — root NOT swept\n", root, scanErr)
					result.SkippedRoots = append(result.SkippedRoots, skippedRoot{Root: root, Reason: skipInFlightUnreadable})
					continue
				}
				for _, w := range active.Warnings {
					result.Warnings = append(result.Warnings, root+": "+w)
					fmt.Fprintf(errOut, "[WARN] worktree sweep: %s: %s\n", root, w)
				}

				res, sweepErr := execution.SweepMergedWorktrees(execution.WorktreeSweepOptions{
					RepoRoot:      root,
					DefaultBranch: defaultBranch,
					ActiveIssues:  active.Issues,
					DryRun:        dryRun,
					// Report-only, and the only place an operator sees it
					// without running `doctor` (#912).
					ReportStrandedBranches: true,
					// The second door, per root (#916). Between #593 and #916
					// this field was set by nothing in production, so the
					// `update-branch` case it was written for never worked and
					// every merged branch went invisible as soon as `main`
					// touched its files. nil when no client or no GitHub
					// origin, which is the closed door the sweep already
					// handles.
					MergedPRLookup: mergedPRDoorFor(cmd.Context(), root),
				})
				if sweepErr != nil {
					// Best-effort per root, like the autonomous sweep: one
					// unreadable sibling must not hide the other roots' leaks.
					if len(roots) == 1 {
						return sweepErr
					}
					fmt.Fprintf(errOut, "[WARN] worktree sweep: %s: %v\n", root, sweepErr)
					result.SkippedRoots = append(result.SkippedRoots, skippedRoot{Root: root, Reason: skipSweepFailed})
					continue
				}
				result.Results = append(result.Results, res)
			}

			if jsonOut {
				// Through the COBRA WRITER, not fmt.Println to os.Stdout (the
				// emitRecoverResult idiom above). The extension's startup sweep
				// parses this document, and a contract nothing can capture is a
				// contract nothing can pin: every cmd-level test here would have
				// exercised only the human output.
				enc := json.NewEncoder(out)
				enc.SetIndent("", "  ")
				return enc.Encode(result)
			}

			multi := len(roots) > 1
			for _, res := range result.Results {
				if multi {
					fmt.Fprintf(out, "%s\n", res.RepoRoot)
				}
				if res.BaseRefFetchError != "" {
					fmt.Fprintf(errOut, "[WARN] worktree sweep: failed to fetch origin/%s: %s — classifying against local %s\n",
						res.DefaultBranch, res.BaseRefFetchError, res.BaseRef)
				}
				if len(res.Reclaimed) == 0 {
					fmt.Fprintf(out, "  No reclaimable worktrees (scanned %d, base %s).\n", res.Scanned, res.BaseRef)
				}
				for _, wt := range res.Reclaimed {
					verb := "reclaimed"
					if res.DryRun {
						verb = "would reclaim"
					}
					// The DOOR is printed, not inferred (#410): the sweep has two
					// reclaim rules and the default-branch one compares no
					// content at all, so an operator auditing a removal needs to
					// know which rule authorized it.
					fmt.Fprintf(out, "  %-14s %s (branch %s, door %s)\n", verb, wt.Path, wt.Branch, wt.Door)
				}
				for _, wt := range res.Skipped {
					fmt.Fprintf(out, "  %-14s %s (%s)\n", "skipped", wt.Path, wt.Reason)
				}
				// Report-only and printed LAST, after the worktree verdicts, so
				// nothing here reads as something the sweep did. The wording
				// says what it is and hands the operator the command, because
				// this tool deliberately will not run it (#912).
				if sb := res.StrandedBranches; sb != nil && len(sb.Stranded) > 0 {
					names := make([]string, 0, len(sb.Stranded))
					for _, b := range sb.Stranded {
						names = append(names, b.Name)
					}
					fmt.Fprintf(out, "  %-14s %d merged branch(es) no worktree holds, base %s (report only — not deleted):\n",
						"stranded", len(sb.Stranded), sb.BaseRef)
					for _, b := range sb.Stranded {
						tip := b.Tip
						if len(tip) > 8 {
							tip = tip[:8]
						}
						fmt.Fprintf(out, "                   %s (%s)\n", b.Name, tip)
					}
					// `git -C <root>`, not a bare `git branch -D` (#920). This
					// command is printed once per repo and the sweep's default
					// scope is EVERY root in the workspace, so a bare form is
					// correct for at most one of the blocks — whichever repo
					// the operator happens to be standing in. A real workspace
					// run printed six blocks and 26 branches; five of the six
					// lines silently applied to the wrong repository.
					//
					// The defect is invisible under --workdir, where one block
					// makes "run this here" accidentally true, and that is how
					// it shipped.
					fmt.Fprintf(out, "                   delete by hand once verified:\n")
					fmt.Fprintf(out, "                     git -C %s branch -D %s\n", res.RepoRoot, strings.Join(names, " "))
				}
				for _, e := range res.Errors {
					fmt.Fprintf(errOut, "[WARN] worktree sweep: %s\n", e)
				}
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&workdir, "workdir", "", "Sweep this repository only (default: every repo root in the workspace)")
	cmd.Flags().StringVar(&defaultBranch, "default-branch", "", "Default branch to compare against (default: detected from origin/HEAD)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Classify without removing anything")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "Emit JSON instead of human-readable output")
	return cmd
}

// mergedPRDoorFor builds the merged-PR second door for one repo root (#916).
//
// Best-effort by design and silent on failure: an unauthenticated or
// offline machine gets the closed door and the content test alone, which is
// exactly what this command did before the door existed. Failing the sweep
// because a supplementary check is unavailable would trade a complete answer
// for no answer.
//
// The door is LAZY — building it here issues no request. A root whose every
// branch passes the content test never calls it and never pays.
func mergedPRDoorFor(ctx context.Context, root string) execution.MergedPRLookup {
	// clientFromConfig is passed as a FACTORY, not a built client: it can shell
	// out to `gh auth token`, and a root whose origin is not a GitHub URL never
	// needs it. NewMergedPRLookupForRoot resolves the slug first and only then
	// asks for a client.
	lookup := gh.NewMergedPRLookupForRoot(ctx, clientFromConfig, root)
	if lookup == nil {
		return nil
	}
	return lookup
}

// worktreeSweepRoots resolves which repositories the sweep covers, CANONICALIZED
// to main checkouts, plus the roots it refuses to sweep and why.
//
// --workdir is the explicit SINGLE-root override. Without it the default is
// every repo root the workspace resolves (config.WorkspaceRepoRoots — the same
// resolver `doctor`'s leaked-worktree report walks), because since #229 a run's
// worktree is created in its TARGET repo: a sweep rooted only at the invocation
// directory cannot see a cross-repo run's leftovers at all, which is the
// population that accumulates fastest.
//
// EVERY root is then canonicalized with config.MainCheckoutRoot, --workdir
// included, because a root here is used for two different things: `git worktree
// list` (correct from any worktree of the repo) and the `.nightgauge/pipeline`
// state dir (correct ONLY at the main checkout). A linked worktree satisfies the
// first and silently defeats the second — its state dir exists, is empty, and
// reports a determined "no runs in flight" — so the un-canonicalized form ran
// `git worktree remove --force` over a live run's directory with zero protection
// and zero warnings. The default resolution puts the worktree FIRST (the git
// toplevel of cwd precedes the manifest entries), so in the dogfood shape the
// blind pass ran before the protected one; deduping after canonicalization
// collapses both into the one repository they always were.
//
// config.WorkspaceRepoRoots itself is deliberately NOT changed: `doctor` and
// `cleanup` use it for repo-wide `git worktree list`, where a worktree root is
// correct and harmless. The narrowing belongs to the destructive caller.
//
// Zero resolved roots is an ERROR, not an empty sweep. Even a single-repo
// workspace resolves its primary root, so an empty set means the lookup failed —
// and this is the leak-detection pass: reporting "nothing to reclaim" from a scan
// that never ran is how worktree accumulation stays invisible (#302). A root that
// resolves to no main checkout at all is reported as skipped rather than swept on
// a guess; when that leaves nothing to sweep it is an error for the same reason.
func worktreeSweepRoots(workdir string) ([]string, []skippedRoot, error) {
	var raw []string
	origin := workdir
	if workdir != "" {
		raw = []string{workdir}
	} else {
		cwd, err := os.Getwd()
		if err != nil {
			return nil, nil, fmt.Errorf("get working directory: %w", err)
		}
		origin = cwd
		raw = config.WorkspaceRepoRoots(cwd)
		if len(raw) == 0 {
			return nil, nil, fmt.Errorf("no repo roots resolved from %s — run inside a git repository or pass --workdir", cwd)
		}
	}

	var roots []string
	var skipped []skippedRoot
	seen := map[string]bool{}
	for _, r := range raw {
		main := config.MainCheckoutRoot(r)
		if main == "" {
			skipped = append(skipped, skippedRoot{Root: r, Reason: skipNotAGitRepo})
			continue
		}
		if seen[main] {
			continue
		}
		seen[main] = true
		roots = append(roots, main)
	}
	if len(roots) == 0 {
		return nil, skipped, fmt.Errorf("no main checkout resolved from %s — %d root(s) skipped as %s; run inside a git repository or pass --workdir at a repository",
			origin, len(skipped), skipNotAGitRepo)
	}
	return roots, skipped, nil
}
