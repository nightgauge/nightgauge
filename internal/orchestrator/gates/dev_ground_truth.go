package gates

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/nightgauge/nightgauge/internal/ci"
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
func statusArgs() []string {
	return append([]string{"status", "--porcelain", "--"}, ci.DeliverablePathspec()...)
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
	// Stranded names sibling worktrees that DO hold uncommitted work while
	// this one is empty — the #202 signature. Diagnostic only; it never
	// changes the verdict, it explains it.
	Stranded []string
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
func inspectDevWork(workspace string) devWorkState {
	status, err := gitOutput(workspace, statusArgs()...)
	if err != nil {
		// Not a git repo, or git is unusable here. Cannot verify.
		return devWorkState{}
	}
	if strings.TrimSpace(status) != "" {
		return devWorkState{Determined: true, HasWork: true}
	}

	// Clean tree. The branch may still carry commits — a stage that committed
	// despite the contract has produced real work and must not be accused.
	committed, resolved := ci.ChangedFilesAgainstDefaultBaseResolved(workspace)
	if !resolved {
		// The tree is clean but no base ref resolved, so "did this branch gain
		// anything?" is unanswerable. Cannot verify.
		return devWorkState{}
	}
	for _, f := range committed {
		if !ci.IsBookkeepingPath(f) {
			return devWorkState{Determined: true, HasWork: true}
		}
	}

	return devWorkState{
		Determined: true,
		HasWork:    false,
		Stranded:   strandedWorktrees(workspace),
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
