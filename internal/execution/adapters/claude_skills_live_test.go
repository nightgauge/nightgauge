//go:build claude_integration

package adapters

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/hometest"
)

// Run with: go test -tags claude_integration -run TestClaudeSkillsTreeLive ./internal/execution/adapters/
// It spends a few Haiku turns and needs an authenticated claude CLI.

// TestClaudeSkillsTreeLive drives the flags stages use through the real CLI
// (#2718). It asserts on a random sentinel and on the filesystem, never on the
// model's wording, and each arm has a control without the flag under test, so a
// model that ignores the prompt fails the test rather than passing it.
// --setting-sources project keeps user-level additionalDirectories out of it.
// The write arms carry a stage's --allowedTools, so the refusal is the deny
// outranking a Write allow. The read arms do not: a Read allow could open the
// tree on its own and leave the --add-dir they measure unproven.
func TestClaudeSkillsTreeLive(t *testing.T) {
	if _, err := exec.LookPath("claude"); err != nil {
		t.Skip("claude is not on PATH, so nothing here can be proven")
	}

	tree := t.TempDir()
	worktree := t.TempDir()
	stageDir := filepath.Join(tree, "nightgauge-feature-dev")
	if err := os.MkdirAll(stageDir, 0o755); err != nil {
		t.Fatal(err)
	}
	sentinel := "sentinel-" + randomHex(t)
	include := filepath.Join(stageDir, "include.md")
	if err := os.WriteFile(include, []byte(sentinel+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	flags := claudeSkillsDirArgs(RunOptions{
		SkillPath:   filepath.Join(stageDir, "SKILL.md"),
		WorktreeDir: worktree,
	})
	if len(flags) == 0 {
		t.Fatal("claudeSkillsDirArgs returned no flags for a tree outside the worktree")
	}
	writeFlags := append(append([]string(nil), flags...), stageAllow...)
	addDirOnly := append([]string{"--add-dir", tree}, stageAllow...)

	readPrompt := "Use the Read tool on " + include + " and reply with its first line, verbatim."
	writePrompt := func(path string) string {
		return "Use the Write tool to create " + path + " containing the word done. " +
			"If it is refused, reply refused and do not try again."
	}

	t.Run("read succeeds with the stage flags", func(t *testing.T) {
		run := runClaudeLive(t, worktree, readPrompt, flags)
		if !strings.Contains(run.Result, sentinel) {
			t.Errorf("result %q does not hold the sentinel; a default user cannot read the tree", run.Result)
		}
	})

	t.Run("read is refused without them", func(t *testing.T) {
		run := runClaudeLive(t, worktree, readPrompt, nil)
		if strings.Contains(run.Result, sentinel) {
			t.Errorf("the tree was readable with no flags, so the read arm proves nothing: %q", run.Result)
		}
	})

	t.Run("write is refused with the stage flags", func(t *testing.T) {
		target := filepath.Join(tree, "written-with-flags.txt")
		run := runClaudeLive(t, worktree, writePrompt(target), bypass(writeFlags))
		if _, err := os.Stat(target); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("the deny let a file-tool write through (stat err %v): %q", err, run.Result)
		}
	})

	t.Run("write lands with --add-dir alone", func(t *testing.T) {
		target := filepath.Join(tree, "written-add-dir-only.txt")
		run := runClaudeLive(t, worktree, writePrompt(target), bypass(addDirOnly))
		if _, err := os.Stat(target); err != nil {
			t.Errorf("control wrote nothing, so the refusal above proves nothing: %v: %q", err, run.Result)
		}
	})

	t.Run("the stage flags draw no inert-rule warning", func(t *testing.T) {
		const warning = "not matched by file permission"
		inert := []string{"--add-dir", tree, "--disallowedTools", "Write(//" + strings.TrimLeft(tree, "/") + "/**)"}
		if got := claudeLiveWarnings(t, worktree, inert); !strings.Contains(got, warning) {
			t.Fatalf("a Write(path) rule no longer warns, so this arm can prove nothing: %q", got)
		}
		if got := claudeLiveWarnings(t, worktree, flags); strings.Contains(got, warning) {
			t.Errorf("the CLI warned about a stage rule:\n%s", got)
		}
	})
}

// stageAllow is the file-tool part of the allow list stages send, from the
// allowed-tools frontmatter of skills/nightgauge-feature-dev/SKILL.md. Bash is
// left out because withBashDenied removes it from every arm.
var stageAllow = []string{"--allowedTools", "Read,Write,Edit,Glob,Grep"}

type claudeLiveRun struct {
	Subtype string `json:"subtype"`
	IsError bool   `json:"is_error"`
	Result  string `json:"result"`
}

// runClaudeLive runs one prompt and fails on anything that is not a completed
// turn. A negative arm would otherwise pass on an auth or CLI error.
func runClaudeLive(t *testing.T, cwd, prompt string, flags []string) claudeLiveRun {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	args := []string{
		"-p", prompt,
		"--model", "haiku",
		"--max-turns", "3",
		"--output-format", "json",
		"--setting-sources", "project",
		"--no-session-persistence",
	}
	args = append(args, withBashDenied(flags)...)
	cmd := exec.CommandContext(ctx, "claude", args...)
	cmd.Dir = cwd
	cmd.Env = claudeLiveEnv(t)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	runErr := cmd.Run()

	var run claudeLiveRun
	if err := json.Unmarshal(stdout.Bytes(), &run); err != nil {
		t.Fatalf("claude did not complete (run error %v, parse error %v)\nstdout: %s\nstderr: %s",
			runErr, err, stdout.String(), stderr.String())
	}
	// An unauthenticated CLI reports subtype "success" with is_error set, so
	// is_error decides. Running out of turns is a completed, refused attempt.
	if run.IsError && run.Subtype != "error_max_turns" {
		t.Fatalf("claude ended %q with an error (run error %v)\nstdout: %s\nstderr: %s",
			run.Subtype, runErr, stdout.String(), stderr.String())
	}
	return run
}

// claudeLiveWarnings returns stderr from a one-turn spawn. It uses text output
// because the CLI prints its inert-rule warning in no other mode.
func claudeLiveWarnings(t *testing.T, cwd string, flags []string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	args := append([]string{
		"-p", "Reply ok.", "--model", "haiku", "--max-turns", "1",
		"--setting-sources", "project", "--no-session-persistence",
	}, flags...)
	cmd := exec.CommandContext(ctx, "claude", args...)
	cmd.Dir = cwd
	cmd.Env = claudeLiveEnv(t)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if out, err := cmd.Output(); err != nil {
		t.Fatalf("claude did not complete: %v\nstdout: %s\nstderr: %s", err, out, stderr.String())
	}
	return stderr.String()
}

// claudeLiveEnv gives the CLI its real login: TestMain isolates HOME, and a
// claude under that fake home answers "Not logged in". It is an allowlist because
// these spawns run a model with permissions bypassed, so the operator's tokens
// stay out. NIGHTGAUGE_ variables stay out too: the real HOME is never paired with
// the test's temporary state root, the combination #2311 lost data to.
func claudeLiveEnv(t *testing.T) []string {
	t.Helper()
	if hometest.RealHome == "" {
		t.Fatal("the real home is unknown, so claude cannot authenticate")
	}
	env := []string{"HOME=" + hometest.RealHome}
	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		switch name {
		case "PATH", "TERM", "TMPDIR", "USER", "LOGNAME", "LANG":
			env = append(env, kv)
			continue
		}
		if strings.HasPrefix(name, "ANTHROPIC_") || strings.HasPrefix(name, "CLAUDE_") {
			env = append(env, kv)
		}
	}
	return env
}

// withBashDenied takes Bash away from every arm: a shell read or write would
// bypass what the arm measures, and ADR-022 already records that limit.
func withBashDenied(flags []string) []string {
	out := append([]string(nil), flags...)
	for i, flag := range out {
		if flag == "--disallowedTools" && i+1 < len(out) {
			out[i+1] += ",Bash"
			return out
		}
	}
	return append(out, "--disallowedTools", "Bash")
}

func bypass(flags []string) []string {
	return append([]string{"--permission-mode", "bypassPermissions"}, flags...)
}

func randomHex(t *testing.T) string {
	t.Helper()
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(b)
}
