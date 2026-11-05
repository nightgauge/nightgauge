package gateledger

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Runner runs one command in dir and returns its combined output.
type Runner func(ctx context.Context, dir, name string, args ...string) ([]byte, error)

// ExecRunner is the os/exec Runner.
func ExecRunner(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	return cmd.CombinedOutput()
}

// Fingerprint names the working tree as the git tree id every ledger event
// is keyed on. It stages through a scratch index, leaving the real index alone.
// The scratch index is seeded from HEAD first: staged into an empty index,
// `add -A` would apply ignore rules to tracked files too and drop any tracked
// file that matches .gitignore, info/exclude or core.excludesFile (#2889). An
// unborn HEAD keeps the empty index. scripts/ci-local-go-test.sh computes the
// same tree; scripts/test-ci-local-go-pass-stamp.sh pins that they agree.
//
// It lives here, beside the ledger it keys, so every reader of the ledger
// fingerprints a tree the way the writers did (#2941).
func Fingerprint(ctx context.Context, run Runner, workDir string) (string, error) {
	dir, err := os.MkdirTemp("", "ng-pre-push-tree-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(dir)
	indexEnv := "GIT_INDEX_FILE=" + filepath.Join(dir, "index")
	if _, err := run(ctx, workDir, "git", "rev-parse", "-q", "--verify", "HEAD^{tree}"); err == nil {
		if out, err := run(ctx, workDir, "env", indexEnv, "git", "read-tree", "HEAD"); err != nil {
			return "", fmt.Errorf("read-tree: %v: %s", err, clip(out))
		}
	}
	if out, err := run(ctx, workDir, "env", indexEnv, "git", "add", "-A", "--", "."); err != nil {
		return "", fmt.Errorf("stage: %v: %s", err, clip(out))
	}
	out, err := run(ctx, workDir, "env", indexEnv, "git", "write-tree")
	if err != nil {
		return "", fmt.Errorf("write-tree: %v: %s", err, clip(out))
	}
	tree := strings.TrimSpace(string(out))
	if tree == "" {
		return "", fmt.Errorf("write-tree came back empty")
	}
	return tree, nil
}

func clip(out []byte) string {
	s := strings.TrimSpace(string(out))
	if len(s) > 200 {
		return s[:200] + "..."
	}
	return s
}

// LatestForWorktree answers Latest for the tree checked out at workDir.
func LatestForWorktree(ctx context.Context, workDir, kind string) (Answer, error) {
	tree, err := Fingerprint(ctx, ExecRunner, workDir)
	if err != nil {
		return Answer{Reason: fmt.Sprintf("the tree could not be fingerprinted: %v", err)}, nil
	}
	return Latest(workDir, tree, kind)
}
