// Package scaffold writes the files a repository needs before the pipeline
// runs in it. Today that is the .nightgauge/ ignore rules.
//
// # One template, two writers
//
// nightgauge.gitignore is the canonical .nightgauge/.gitignore. The binary
// embeds it; the VS Code extension carries the same text as GITIGNORE_CONTENT
// in packages/nightgauge-vscode/src/utils/ensureGitignore.ts. They cannot
// drift silently: TestGitignoreTemplateMatchesCommittedCopy pins this file to
// the repository's own committed .nightgauge/.gitignore, and the extension's
// ensureGitignore.test.ts pins GITIGNORE_CONTENT to both. Change the rules in
// all three, and bump the version marker when a rule changes.
//
// Before this package only the extension wrote the rules, so a clone driven
// by the CLI alone (CI, a terminal-only operator) had none: `git add -A`
// committed logs and pipeline state and turned each .nightgauge/worktrees/*
// checkout into an embedded repository.
package scaffold

import (
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/nightgauge/nightgauge/internal/atomicfile"
)

// GitignoreTemplate is the canonical .nightgauge/.gitignore content.
//
//go:embed nightgauge.gitignore
var GitignoreTemplate string

// ExcludeBlockID names the info/exclude block that carries the template's
// rules when the repository's committed copy is behind. Shared with the
// extension, which writes the same block.
const ExcludeBlockID = "nightgauge-gitignore"

// LocalAdditionsMarker starts the section of .nightgauge/.gitignore that
// belongs to the repository; a rewrite keeps every line below it.
const LocalAdditionsMarker = "# ─── Local additions (kept on upgrade) ──────────────────────────────"

var versionLine = regexp.MustCompile(`(?m)^# nightgauge-gitignore-version: (\d+)$`)

// GitignoreVersionMarker returns the template's version marker line.
func GitignoreVersionMarker() string {
	return versionLine.FindString(GitignoreTemplate)
}

// IgnoreAction is what EnsureIgnoreRules did.
type IgnoreAction string

const (
	// IgnoreCreated: .nightgauge/.gitignore was missing and is now written,
	// with the .gitkeep files its rules anchor to.
	IgnoreCreated IgnoreAction = "created"
	// IgnoreUpdated: an untracked, older file was rewritten, keeping the
	// lines below its Local additions marker.
	IgnoreUpdated IgnoreAction = "updated"
	// IgnoreDeferred: the committed file is older; it was left untouched and
	// the current rules are in force through info/exclude.
	IgnoreDeferred IgnoreAction = "deferred"
	// IgnoreCurrent: the file already carries the template's version.
	IgnoreCurrent IgnoreAction = "current"
	// IgnoreSkipped: nothing was written; Note says why.
	IgnoreSkipped IgnoreAction = "skipped"
)

// IgnoreResult reports EnsureIgnoreRules' outcome.
type IgnoreResult struct {
	Action IgnoreAction `json:"action"`
	// Path is the file written or inspected, when there is one.
	Path string `json:"path,omitempty"`
	// Note explains a skip or a deferral in one line.
	Note string `json:"note,omitempty"`
}

// gitkeepDirs are the directories whose .gitkeep the template un-ignores.
var gitkeepDirs = []string{"pipeline/history", "plans", "logs", "pipeline"}

// EnsureIgnoreRules makes the template's .nightgauge/ ignore rules take
// effect in the repository at repoRoot, the same way the extension's
// ensureGitignore does:
//
//   - Missing .nightgauge/.gitignore: write the template and the .gitkeep
//     files it anchors to (the initial scaffold, which the operator commits).
//   - Current version marker: nothing.
//   - Older and TRACKED: never edit the committed file (a change nothing
//     commits leaves the checkout dirty); write the rules to the
//     repository's info/exclude in a marked block instead. The committed
//     file is upgraded by pull request.
//   - Older and untracked: rewrite it, keeping its local additions.
//
// It writes nothing when the repository is not initialized (no
// .nightgauge/config.yaml; the caller must not resurrect .nightgauge/ in a
// repository nobody onboarded) or is not a git work tree (git ignores
// nothing there), and says so in Note. It is idempotent.
func EnsureIgnoreRules(repoRoot string) (IgnoreResult, error) {
	ngDir := filepath.Join(repoRoot, ".nightgauge")
	ignorePath := filepath.Join(ngDir, ".gitignore")

	if info, err := os.Stat(filepath.Join(ngDir, "config.yaml")); err != nil || !info.Mode().IsRegular() {
		return IgnoreResult{Action: IgnoreSkipped, Note: "not initialized: no .nightgauge/config.yaml"}, nil
	}
	if !InsideWorkTree(repoRoot) {
		return IgnoreResult{
			Action: IgnoreSkipped,
			Note:   "not a git work tree: .nightgauge/ ignore rules not written",
		}, nil
	}

	info, err := os.Lstat(ignorePath)
	switch {
	case os.IsNotExist(err):
		return createIgnoreFile(ngDir, ignorePath)
	case err != nil:
		return IgnoreResult{}, fmt.Errorf("inspect %s: %w", ignorePath, err)
	case info.Mode()&os.ModeSymlink != 0:
		return IgnoreResult{}, fmt.Errorf("refusing symlinked %s", ignorePath)
	case !info.Mode().IsRegular():
		return IgnoreResult{}, fmt.Errorf("%s is not a regular file", ignorePath)
	}

	data, err := os.ReadFile(ignorePath)
	if err != nil {
		return IgnoreResult{}, fmt.Errorf("read %s: %w", ignorePath, err)
	}
	existing := string(data)
	if hasLine(existing, GitignoreVersionMarker()) {
		return IgnoreResult{Action: IgnoreCurrent, Path: ignorePath}, nil
	}

	if IsTracked(repoRoot, ".nightgauge/.gitignore") {
		if _, err := WriteExcludeBlock(repoRoot, ExcludeBlockID, ToRootPatterns(".nightgauge", GitignoreTemplate)); err != nil {
			return IgnoreResult{}, err
		}
		excludePath, _ := LocalExcludePath(repoRoot)
		return IgnoreResult{
			Action: IgnoreDeferred,
			Path:   excludePath,
			Note:   "the committed .nightgauge/.gitignore is older; current rules applied per machine",
		}, nil
	}

	if err := atomicfile.Write(ignorePath, []byte(GitignoreTemplate+localAdditions(existing)), info.Mode().Perm()); err != nil {
		return IgnoreResult{}, fmt.Errorf("write %s: %w", ignorePath, err)
	}
	return IgnoreResult{Action: IgnoreUpdated, Path: ignorePath}, nil
}

func createIgnoreFile(ngDir, ignorePath string) (IgnoreResult, error) {
	for _, sub := range gitkeepDirs {
		if err := os.MkdirAll(filepath.Join(ngDir, sub), 0o755); err != nil {
			return IgnoreResult{}, fmt.Errorf("create %s: %w", filepath.Join(ngDir, sub), err)
		}
	}
	if err := atomicfile.Write(ignorePath, []byte(GitignoreTemplate), 0o644); err != nil {
		return IgnoreResult{}, fmt.Errorf("write %s: %w", ignorePath, err)
	}
	for _, sub := range gitkeepDirs {
		keep := filepath.Join(ngDir, sub, ".gitkeep")
		if _, err := os.Lstat(keep); os.IsNotExist(err) {
			if err := os.WriteFile(keep, nil, 0o644); err != nil {
				return IgnoreResult{}, fmt.Errorf("write %s: %w", keep, err)
			}
		}
	}
	return IgnoreResult{Action: IgnoreCreated, Path: ignorePath}, nil
}

// localAdditions returns what an existing file carries below its Local
// additions marker, minus the template's own text there, exactly as the
// extension computes it.
func localAdditions(existing string) string {
	at := strings.Index(existing, LocalAdditionsMarker)
	if at < 0 {
		return ""
	}
	after := existing[at+len(LocalAdditionsMarker):]
	tmplAt := strings.Index(GitignoreTemplate, LocalAdditionsMarker)
	canonicalTail := GitignoreTemplate[tmplAt+len(LocalAdditionsMarker):]
	if strings.HasPrefix(after, canonicalTail) {
		return after[len(canonicalTail):]
	}
	return after
}

func hasLine(content, line string) bool {
	if line == "" {
		return false
	}
	for _, l := range strings.Split(content, "\n") {
		if strings.TrimRight(l, "\r") == line {
			return true
		}
	}
	return false
}
