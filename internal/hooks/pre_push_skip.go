package hooks

import (
	"context"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
)

// goTestPassRecord is written by scripts/ci-local-go-test.sh after `go test`
// passes, relative to the worktree. It holds the tree that passed.
var goTestPassRecord = filepath.Join(".ci-local-logs", "go-test-pass.tree")

// changedPaths is the branch's changed set, or why it could not be derived.
type changedPaths struct {
	paths []string
	err   error
}

// goTestTimeoutArg ties Go's per-package timer to the gate's limit, so it
// cannot expire first. 0 is Go's own "no limit".
func goTestTimeoutArg(timeoutSeconds int) string {
	if timeoutSeconds <= 0 {
		return "0"
	}
	return strconv.Itoa(timeoutSeconds) + "s"
}

// collectChangedPaths lists everything that differs from the merge base,
// including uncommitted and untracked work, since the gate runs before commit.
// --no-renames reports both sides, so a file renamed away from .go still counts.
func collectChangedPaths(ctx context.Context, runner CmdRunner, workDir, targetBranch string) changedPaths {
	baseOut, err := runner.Run(ctx, workDir, "git", "merge-base", "HEAD", "origin/"+targetBranch)
	if err != nil {
		return changedPaths{err: fmt.Errorf("merge-base: %w", err)}
	}
	base := strings.TrimSpace(string(baseOut))
	if base == "" {
		return changedPaths{err: fmt.Errorf("merge-base came back empty")}
	}
	diffOut, err := runner.Run(ctx, workDir, "git", "diff", "--name-only", "--no-renames", base)
	if err != nil {
		return changedPaths{err: fmt.Errorf("diff: %w", err)}
	}
	untrackedOut, err := runner.Run(ctx, workDir, "git", "ls-files", "--others", "--exclude-standard")
	if err != nil {
		return changedPaths{err: fmt.Errorf("ls-files: %w", err)}
	}
	return changedPaths{paths: append(lines(diffOut), lines(untrackedOut)...)}
}

// decideGoTestSkip decides whether the test phase can be skipped on the merged
// tree. It fails closed: anything it cannot prove runs the suite. The returned
// reason says why either way.
func decideGoTestSkip(ctx context.Context, runner CmdRunner, workDir string, changes changedPaths) (bool, string) {
	if changes.err != nil {
		return false, "the changed set could not be derived: " + changes.err.Error()
	}
	if len(changes.paths) == 0 {
		return false, "the changed set came back empty, which is indistinguishable from a broken derivation"
	}
	goFilesOut, err := runner.Run(ctx, workDir, "git", "ls-files", "--", "*.go")
	if err != nil {
		return false, "tracked Go files could not be listed: " + err.Error()
	}
	goFiles := lines(goFilesOut)
	if len(goFiles) == 0 {
		return false, "no tracked Go files were listed, which cannot be right in a Go project"
	}
	packageDirs := make(map[string]bool, len(goFiles))
	for _, f := range goFiles {
		packageDirs[path.Dir(f)] = true
	}

	goInput := ""
	for _, p := range changes.paths {
		if isGoInput(p, packageDirs) {
			goInput = p
			break
		}
	}
	if goInput == "" {
		return true, fmt.Sprintf("none of the %d changed path(s) is a Go input", len(changes.paths))
	}

	recorded, err := os.ReadFile(filepath.Join(workDir, goTestPassRecord))
	if err != nil {
		return false, fmt.Sprintf("a Go input changed (%s) and ci-local recorded no pass", goInput)
	}
	want := strings.TrimSpace(string(recorded))
	got, err := treeFingerprint(ctx, runner, workDir)
	if err != nil {
		return false, fmt.Sprintf("a Go input changed (%s) and the tree could not be fingerprinted: %v", goInput, err)
	}
	if want == "" || want != got {
		return false, fmt.Sprintf("a Go input changed (%s) and ci-local's pass record is for another tree", goInput)
	}
	return true, fmt.Sprintf("ci-local already passed go test on tree %s", got)
}

// isGoInput reports whether a path can change what `go test` does. The ancestor
// walk covers //go:embed, which cannot reach outside its package directory.
func isGoInput(p string, packageDirs map[string]bool) bool {
	base := path.Base(p)
	if strings.HasSuffix(base, ".go") || base == "go.mod" || base == "go.sum" || base == "go.work" {
		return true
	}
	for d := path.Dir(p); ; d = path.Dir(d) {
		if packageDirs[d] {
			return true
		}
		if d == "." || d == "/" {
			return false
		}
	}
}

// treeFingerprint hashes the working tree through a scratch index, leaving the
// real index alone. scripts/ci-local-go-test.sh computes the same tree;
// scripts/test-ci-local-go-pass-stamp.sh pins that they agree.
func treeFingerprint(ctx context.Context, runner CmdRunner, workDir string) (string, error) {
	dir, err := os.MkdirTemp("", "ng-pre-push-tree-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(dir)
	indexEnv := "GIT_INDEX_FILE=" + filepath.Join(dir, "index")
	if out, err := runner.Run(ctx, workDir, "env", indexEnv, "git", "add", "-A", "--", "."); err != nil {
		return "", fmt.Errorf("stage: %v: %s", err, truncateStr(strings.TrimSpace(string(out)), 200))
	}
	out, err := runner.Run(ctx, workDir, "env", indexEnv, "git", "write-tree")
	if err != nil {
		return "", fmt.Errorf("write-tree: %v: %s", err, truncateStr(strings.TrimSpace(string(out)), 200))
	}
	tree := strings.TrimSpace(string(out))
	if tree == "" {
		return "", fmt.Errorf("write-tree came back empty")
	}
	return tree, nil
}

func lines(out []byte) []string {
	var result []string
	for _, l := range strings.Split(string(out), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			result = append(result, l)
		}
	}
	return result
}
