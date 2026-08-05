package gates

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/nightgauge/nightgauge/internal/ci"
	"github.com/nightgauge/nightgauge/internal/reclaim"
)

// maxStrandedReported caps how many sibling worktrees a failing gate names.
// The list is diagnostic, not an inventory; a workspace with dozens of leaked
// worktrees should not turn one gate failure into an unreadable wall.
const maxStrandedReported = 5

// statusArgs builds `git status --porcelain` limited to the deliverable. The
// exclusion is essential, not cosmetic: the stage's own dev-{N}.json lands in
// `.nightgauge/pipeline/`, so counting bookkeeping as work would make every
// empty workspace look productive and silently disable this gate in any repo
// that does not happen to gitignore it. See ci.BookkeepingDirs.
// `--untracked-files=all` is load-bearing, not a preference. Porcelain's
// default collapses untracked directories to a single `internal/` entry, so a
// stage that created ten files in one new package reads as one changed path —
// and the count and file list a gate reports become wrong in the direction that
// understates the work. Worse, it is silently ambient: a machine with
// `status.showUntrackedFiles=all` in its git config enumerates the files and
// agrees with the assertion, while CI (default config) does not. Pinning it
// here makes the answer independent of whoever's git is running (#223).
func statusArgs() []string {
	return append([]string{"status", "--porcelain", "--untracked-files=all", "--"}, ci.DeliverablePathspec()...)
}

// devWorkState is the answer to "did this stage actually produce anything?"
// derived from git, not from what the skill wrote about itself.
type devWorkState struct {
	// Determined is false when git could not answer — the workspace is not a
	// repo, git is unavailable, or no base ref resolved. Callers MUST treat
	// this as "cannot verify" and pass. An undetermined answer failing closed
	// would halt the fleet on any repo whose default branch is not main.
	Determined bool
	// HasWork is true when the workspace holds uncommitted changes or the
	// branch carries commits its base does not.
	HasWork bool
	// Files names the deliverable paths git found, capped at
	// maxFilesReported. Set only when HasWork is true.
	//
	// #223: an exoneration has to be legible. Telling an operator "the dev
	// context is empty but git disagrees" is only marginally better than the
	// old "zero file changes" — naming the files is what turns the verdict
	// into something they can act on without opening the worktree.
	Files []string
	// FileCount is the true total, which may exceed len(Files).
	FileCount int
	// Stranded names sibling worktrees that DO hold uncommitted work while
	// this one is empty — the #202 signature. Diagnostic only; it never
	// changes the verdict, it explains it.
	Stranded []string
	// Mode records which probe produced this verdict: "standard" (the
	// default exclusion-scoped check) or "bookkeeping" (#237 — the declared
	// deliverable is entirely under BookkeepingDirs, so the probe was
	// re-scoped to exactly those declared paths). Empty when HasWork is
	// false or the standard path answered.
	Mode string
	// DeclaredCount and ConfirmedCount are populated only when Mode ==
	// "bookkeeping": how many paths the stage declared, and how many of them
	// git confirmed as actually changed.
	DeclaredCount  int
	ConfirmedCount int
}

// withoutOwnHandoff drops the running stage's own dev-{N}.json from a declared
// file list.
//
// The bookkeeping probe (#237) widens its scope to whatever the stage declared,
// then confirms those paths against git. A stage's own handoff is a bookkeeping
// path that is always present and always modified at the moment the gate runs —
// every run writes it by definition — so declaring it alone satisfied both
// halves and passed a run that produced nothing (#249). That is #202 reached
// through a new door: the exclusion still stands, but a declaration naming the
// exhaust routes around it.
//
// No legitimate deliverable is lost: a stage's handoff is never the work. Work
// that untracks OTHER issues' context files — #237's motivating case — is
// unaffected, since only the current issue's own path is dropped.
//
// This mirrors enforceValidateCommitContract on the TypeScript side, which
// already strips .nightgauge/ from claimed files before judging them.
func withoutOwnHandoff(files []string, issueNumber int) []string {
	own := fmt.Sprintf(".nightgauge/pipeline/dev-%d.json", issueNumber)
	out := make([]string, 0, len(files))
	for _, f := range files {
		if filepath.ToSlash(strings.TrimPrefix(filepath.Clean(f), "./")) == own {
			continue
		}
		out = append(out, f)
	}
	return out
}

// allBookkeeping reports whether files is non-empty and every entry is a
// bookkeeping path. Used to decide whether the ground-truth probe should
// widen its scope to the stage's declared deliverable (#237).
func allBookkeeping(files []string) bool {
	if len(files) == 0 {
		return false
	}
	for _, f := range files {
		if !ci.IsBookkeepingPath(f) {
			return false
		}
	}
	return true
}

// maxFilesReported caps how many changed paths a gate verdict names, for the
// same reason as maxStrandedReported: the list is evidence, not a manifest.
const maxFilesReported = 10

// statusPaths extracts the deliverable paths from `git status --porcelain`
// output.
//
// Delegates to reclaim.ParseStatus so the tree holds exactly one porcelain
// parser. This was a byte-identical second copy, and #330/#332 added a third
// consumer — at which point "each caller parses status its own way" is the
// Dual-Path Drift defect class the review checklist names, not a hypothetical.
func statusPaths(status string) []string {
	return reclaim.StatusPaths(status)
}

// capped returns at most maxFilesReported entries.
func capped(files []string) []string {
	if len(files) > maxFilesReported {
		return files[:maxFilesReported]
	}
	return files
}

// inspectDevWork derives ground truth for a feature-dev workspace.
//
// feature-dev's contract is to leave its changes UNCOMMITTED in the stage
// workspace — `skills/nightgauge-feature-dev/SKILL.md` Phase 7: "No commit or
// push in feature-dev. Code is committed and pushed by
// /nightgauge-feature-validate after validation passes" (#1608). So a dirty
// working tree is the expected healthy state, and the committed-diff check is
// the secondary path for a stage that committed anyway.
//
// Fail-open on every uncertainty. This runs on the success path of a stage
// that just spent real money; a false accusation costs a re-run and, via the
// safety rails, potentially the whole queue.
func inspectDevWork(workspace string, declared []string) devWorkState {
	status, err := gitOutput(workspace, statusArgs()...)
	if err != nil {
		// Not a git repo, or git is unusable here. Cannot verify.
		return devWorkState{}
	}
	if strings.TrimSpace(status) != "" {
		paths := statusPaths(status)
		return devWorkState{
			Determined: true,
			HasWork:    true,
			Files:      capped(paths),
			FileCount:  len(paths),
		}
	}

	// #237: the stage's declared deliverable is entirely bookkeeping paths
	// (e.g. untracking .nightgauge/pipeline from the index). The default
	// exclusion above can never see that work, so re-scope the status check
	// to exactly the declared paths — no exclusion pathspec — instead of
	// relaxing the default exclusion globally. Scoping to what the stage
	// itself declared (not the whole bookkeeping tree) keeps the #202
	// self-certification risk from reapplying: a stage cannot pass by
	// silently declaring paths it never touched, because the scoped status
	// must still come back non-empty.
	if allBookkeeping(declared) {
		bkStatus, err := gitOutput(workspace, append([]string{"status", "--porcelain", "--untracked-files=all", "--"}, declared...)...)
		if err == nil && strings.TrimSpace(bkStatus) != "" {
			paths := statusPaths(bkStatus)
			return devWorkState{
				Determined:     true,
				HasWork:        true,
				Files:          capped(paths),
				FileCount:      len(paths),
				Mode:           "bookkeeping",
				DeclaredCount:  len(declared),
				ConfirmedCount: len(paths),
			}
		}
	}

	// Clean tree. The branch may still carry commits — a stage that committed
	// despite the contract has produced real work and must not be accused.
	committed, resolved := ci.ChangedFilesAgainstDefaultBaseResolved(workspace)
	if !resolved {
		// The tree is clean but no base ref resolved, so "did this branch gain
		// anything?" is unanswerable. Cannot verify.
		return devWorkState{}
	}
	var deliverable []string
	for _, f := range committed {
		if !ci.IsBookkeepingPath(f) {
			deliverable = append(deliverable, f)
		}
	}
	if len(deliverable) > 0 {
		return devWorkState{
			Determined: true,
			HasWork:    true,
			Files:      capped(deliverable),
			FileCount:  len(deliverable),
		}
	}

	return devWorkState{
		Determined: true,
		HasWork:    false,
		Stranded:   strandedWorktrees(workspace),
	}
}

// devHandoffMissing asks git whether an absent or empty dev context is being
// contradicted by the workspace, and if so builds the verdict.
//
// #223. #202 wired inspectDevWork as an additional FAILURE condition: it
// catches a context that claims work the tree does not have. It was never
// reachable on the paths where the context claims nothing, so git could convict
// a lying context but never exonerate a missing one — the same question, asked
// of the filesystem in only one direction.
//
// The verdict is deliberately KindFail, not KindNoOp. The scheduler wraps every
// KindNoOp reason into "premature turn end: stage exited 0 with no state
// change", and there WAS a state change; that wrapper is precisely the sentence
// that sent an operator looking for work that was sitting on disk. A failure
// that names the handoff as the missing artifact keeps the work in the frame.
//
// devHandoffVerdict is the result of devHandoffMissing. A struct return
// replaces what would otherwise be a 7-value tuple (#134 added Files/
// FileCount on top of the existing 5) — past 5-6 returns the positional
// tuple stops being readable at call sites.
type devHandoffVerdict struct {
	OK           bool
	Reason       string
	Evidence     []string
	Kind         Kind
	TerminalKind string
	// Files and FileCount surface work.Files/work.FileCount so callers (the
	// gate JSON CLI output) can hand the deliverable file list to a
	// downstream consumer (feature-validate's Phase 0, #134) instead of only
	// the human-readable Evidence strings.
	Files     []string
	FileCount int
}

// Returns OK=false when git cannot answer or found nothing, leaving the
// caller's original no-op verdict intact. Fail-open, like every other
// ground-truth check here: this runs after the money is spent.
func devHandoffMissing(workspace, contextCondition, ctxPath string) devHandoffVerdict {
	work := inspectDevWork(workspace, nil)
	if !work.Determined || !work.HasWork {
		return devHandoffVerdict{}
	}

	evidence := []string{
		fmt.Sprintf("workspace: %s", workspace),
		fmt.Sprintf("context: %s (%s)", ctxPath, contextCondition),
		fmt.Sprintf("git: %d changed deliverable file(s) present", work.FileCount),
	}
	for _, f := range work.Files {
		evidence = append(evidence, "  "+f)
	}
	if work.FileCount > len(work.Files) {
		evidence = append(evidence, fmt.Sprintf("  … and %d more", work.FileCount-len(work.Files)))
	}
	evidence = append(evidence,
		"the work exists and must be preserved — a retry must not re-derive it from scratch")

	// The `[dev-handoff-missing]` marker mirrors `[dev-produced-no-changes]`
	// (#202) for the same reason: the text-based classifiers and the gate path
	// must agree on the kind, or the dashboards learn to distrust it.
	return devHandoffVerdict{
		OK: true,
		Reason: fmt.Sprintf(
			"[dev-handoff-missing] %s, but git finds %d changed file(s) in the stage workspace — the stage did the work and ended without writing its handoff",
			contextCondition, work.FileCount,
		),
		Evidence:     evidence,
		Kind:         KindFail,
		TerminalKind: TerminalKindDevHandoffMissing,
		Files:        work.Files,
		FileCount:    work.FileCount,
	}
}

// strandedWorktrees lists sibling worktrees of the same repository that hold
// uncommitted work. When the stage's own workspace is empty and one of these
// is not, the implementation exists — it just landed where no later stage
// will look.
//
// This is the #202 mechanism: feature-dev delegated to a subagent running under
// worktree isolation, which wrote five files into
// `.claude/worktrees/agent-<id>`. The pipeline only ever reads
// `.worktrees/issue-<n>`, so the work was invisible to feature-validate,
// pr-create, and the operator — and would have been destroyed by the next
// worktree sweep. Naming the path turns "dev produced nothing" from a
// confusing verdict into a recoverable one.
func strandedWorktrees(workspace string) []string {
	out, err := gitOutput(workspace, "worktree", "list", "--porcelain")
	if err != nil {
		return nil
	}
	self := resolvePath(workspace)

	var stranded []string
	for _, line := range strings.Split(out, "\n") {
		path, ok := strings.CutPrefix(strings.TrimSpace(line), "worktree ")
		if !ok || path == "" {
			continue
		}
		if resolvePath(path) == self {
			continue
		}
		status, err := gitOutput(path, statusArgs()...)
		if err != nil {
			continue
		}
		if n := countLines(status); n > 0 {
			stranded = append(stranded, fmt.Sprintf("%s (%d uncommitted file(s))", path, n))
			if len(stranded) == maxStrandedReported {
				break
			}
		}
	}
	return stranded
}

// gitOutput runs git in dir and returns stdout. Split out so the ground-truth
// checks share one invocation shape.
func gitOutput(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	return string(out), err
}

// resolvePath canonicalizes a worktree path for comparison. Symlinks are
// evaluated because macOS resolves /tmp to /private/tmp — without this the
// stage's own worktree fails to match itself and reports as stranded.
func resolvePath(p string) string {
	abs, err := filepath.Abs(p)
	if err != nil {
		abs = p
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		return resolved
	}
	return abs
}

func countLines(s string) int {
	n := 0
	for _, line := range strings.Split(s, "\n") {
		if strings.TrimSpace(line) != "" {
			n++
		}
	}
	return n
}
