package gates

import (
	"fmt"
	"strings"
	"time"

	"github.com/nightgauge/nightgauge/internal/ci"
)

// ProtectedPathsGateName names the protected-path check on the run record.
const ProtectedPathsGateName = "protected_paths"

// PipelineProtectedPaths are repository paths no pipeline stage may change
// (#1970). Each is a control whose own configuration the pipeline is gated by:
// `.github/publication-boundary.yaml` is the fail-closed allowlist of the
// publication-boundary check, so a stage that could edit it could widen the
// gate to pass its own output. Changes to these paths land only in a
// human-authored pull request that changes nothing else (enforced in CI by
// scripts/check-boundary-allowlist-isolation.sh).
//
// Paths are repository-relative with forward slashes, exactly as git prints
// them.
var PipelineProtectedPaths = []string{
	".github/publication-boundary.yaml",
}

// CheckProtectedPaths reports whether the stage workspace changed a
// PipelineProtectedPaths entry. It looks in two places, because either alone
// can be evaded: the working tree and index (`git status`, covering an edit
// not yet committed and a staged deletion) and the branch's commits since its
// merge-base with the default base (`git diff base...HEAD`, covering an edit
// a stage committed).
//
// It is independent of the per-stage gate registry, so NIGHTGAUGE_DISABLE_GATES
// cannot turn it off. A workspace that is not a git repository has nothing
// to protect and passes; a branch with no resolvable base is checked on the
// working tree alone.
func CheckProtectedPaths(workspace string) GateResult {
	start := time.Now()
	res := GateResult{
		GateName:  ProtectedPathsGateName,
		Timestamp: start.UTC().Format(time.RFC3339),
	}

	hits := map[string]bool{}
	args := append([]string{"status", "--porcelain=v1", "--untracked-files=all", "--"},
		PipelineProtectedPaths...)
	status, err := gitOutput(workspace, args...)
	if err != nil {
		res.Passed = true
		res.Kind = KindOK
		res.Reason = "workspace is not a git repository; no protected paths to check"
		res.DurationMs = time.Since(start).Milliseconds()
		return res
	}
	for _, p := range statusPaths(status) {
		if isPipelineProtected(p) {
			hits[p] = true
		}
	}
	// A skip-worktree or assume-unchanged bit hides a working-tree edit from
	// `git status`; `ls-files -v` prints any tag other than `H` for such a
	// path, so a hidden protected path counts as changed.
	lsArgs := append([]string{"ls-files", "-v", "--"}, PipelineProtectedPaths...)
	if ls, lsErr := gitOutput(workspace, lsArgs...); lsErr == nil {
		for _, line := range strings.Split(strings.TrimRight(ls, "\n"), "\n") {
			if len(line) > 2 && line[0] != 'H' && isPipelineProtected(line[2:]) {
				hits[line[2:]] = true
			}
		}
	}
	if committed, ok := ci.ChangedFilesAgainstDefaultBaseResolved(workspace); ok {
		for _, p := range committed {
			if isPipelineProtected(p) {
				hits[p] = true
			}
		}
	}

	res.DurationMs = time.Since(start).Milliseconds()
	if len(hits) == 0 {
		res.Passed = true
		res.Kind = KindOK
		res.Reason = "no pipeline-protected path changed"
		return res
	}
	for _, p := range PipelineProtectedPaths {
		if hits[p] {
			res.Evidence = append(res.Evidence, p)
		}
	}
	res.Passed = false
	res.Kind = KindFail
	res.Reason = fmt.Sprintf(
		"stage modified pipeline-protected path(s) %s: the pipeline may not change the "+
			"publication-boundary allowlist that gates its own output (#1970). Revert the "+
			"change; a human applies any allowlist change in a separate pull request",
		strings.Join(res.Evidence, ", "))
	return res
}

func isPipelineProtected(path string) bool {
	path = strings.TrimPrefix(strings.TrimSpace(path), "./")
	for _, p := range PipelineProtectedPaths {
		if path == p {
			return true
		}
	}
	return false
}
