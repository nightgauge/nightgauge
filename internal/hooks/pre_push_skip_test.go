package hooks

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/gittest"
	"github.com/nightgauge/nightgauge/internal/layout/layouttest"
)

// realGitRunner runs git for real and records `go` without running it, so the
// skip decision is exercised against genuine trees.
type realGitRunner struct {
	real ExecCmdRunner
	goes []string
}

func (r *realGitRunner) Run(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
	if name == "go" {
		r.goes = append(r.goes, args[0])
		return nil, nil
	}
	if name == "gitleaks" {
		return nil, nil
	}
	return r.real.Run(ctx, dir, name, args...)
}

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	return gittest.Run(t, dir, args...)
}

func write(t *testing.T, dir, rel, content string) {
	t.Helper()
	p := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// skipFixture is a repository on a feature branch with origin/main at its base.
func skipFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	layouttest.MkPipelineDir(t, dir)
	runGit(t, dir, "init", "-q", "-b", "main")
	runGit(t, dir, "config", "user.email", "t@example.com")
	runGit(t, dir, "config", "user.name", "t")
	write(t, dir, ".gitignore", ".ci-local-logs/\n")
	write(t, dir, "go.mod", "module x\n")
	write(t, dir, "pkg/a.go", "package pkg\n")
	write(t, dir, "docs/a.md", "doc\n")
	runGit(t, dir, "add", "-A")
	runGit(t, dir, "commit", "-qm", "init")
	runGit(t, dir, "update-ref", "refs/remotes/origin/main", "HEAD")
	runGit(t, dir, "checkout", "-qb", "feat/42-x")
	return dir
}

func evaluateFixture(t *testing.T, dir string) (PrePushResult, *realGitRunner) {
	t.Helper()
	runner := &realGitRunner{}
	result := EvaluatePrePush(context.Background(), runner, PrePushInput{
		IssueNumber: 42, WorkDir: dir, TargetBranch: "main", TimeoutSeconds: 60,
	})
	return result, runner
}

func ranGoVerb(r *realGitRunner, verb string) bool {
	for _, g := range r.goes {
		if g == verb {
			return true
		}
	}
	return false
}

func recordTree(t *testing.T, dir string) {
	t.Helper()
	tree, err := TreeFingerprint(context.Background(), &ExecCmdRunner{}, dir)
	if err != nil {
		t.Fatal(err)
	}
	write(t, dir, goTestPassRecord, tree+"\n")
}

func TestPrePushSkip_MatchingPassRecordSkips(t *testing.T) {
	dir := skipFixture(t)
	write(t, dir, "pkg/a.go", "package pkg\n\nvar X = 1\n")
	recordTree(t, dir)

	result, runner := evaluateFixture(t, dir)

	if ranGoVerb(runner, "test") {
		t.Error("go test ran although ci-local passed this exact tree")
	}
	if !strings.Contains(result.TestSkipReason, "ci-local already passed") {
		t.Errorf("reason=%q", result.TestSkipReason)
	}
}

func TestPrePushSkip_UnprovenChangeRuns(t *testing.T) {
	// A non-Go path counts: Go tests read skills/, docs/ and scripts/.
	for name, tc := range map[string]struct {
		path  string
		stale bool
	}{
		"go, no record":      {"pkg/a.go", false},
		"go, stale record":   {"pkg/a.go", true},
		"docs, no record":    {"docs/a.md", false},
		"docs, stale record": {"docs/a.md", true},
	} {
		t.Run(name, func(t *testing.T) {
			dir := skipFixture(t)
			if tc.stale {
				recordTree(t, dir)
			}
			write(t, dir, tc.path, "package pkg\n\nvar X = 1\n")

			result, runner := evaluateFixture(t, dir)

			if !ranGoVerb(runner, "test") || result.ValidationPhases["test"] != "passed" {
				t.Errorf("an unproven change must run: goes=%v phases=%v", runner.goes, result.ValidationPhases)
			}
			if !ranGoVerb(runner, "build") || !ranGoVerb(runner, "vet") {
				t.Errorf("build and vet must always run: %v", runner.goes)
			}
		})
	}
}

func TestDecideGoTestSkip_FailsClosed(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	if skip, _ := decideGoTestSkip(ctx, newMockRunner(), dir); skip {
		t.Error("a missing record skipped the suite")
	}
	write(t, dir, goTestPassRecord, "tree\n")
	if skip, _ := decideGoTestSkip(ctx, &failingRunner{}, dir); skip {
		t.Error("a failed fingerprint skipped the suite")
	}
}

type failingRunner struct{}

func (*failingRunner) Run(context.Context, string, string, ...string) ([]byte, error) {
	return nil, errors.New("boom")
}

// The record is only useful if both sides compute the same tree.
func TestTreeFingerprintMatchesCILocal(t *testing.T) {
	dir := skipFixture(t)
	write(t, dir, "pkg/a.go", "package pkg\n\nvar X = 1\n")
	write(t, dir, "untracked.txt", "new\n")

	script, err := filepath.Abs(filepath.Join("..", "..", "scripts", "ci-local-go-test.sh"))
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("bash", script, "--fingerprint")
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	got, err := TreeFingerprint(context.Background(), &ExecCmdRunner{}, dir)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(out)) != got {
		t.Errorf("bash %s, go %s", out, got)
	}
}
