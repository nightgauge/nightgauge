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
	"strconv"
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

var versionLine = regexp.MustCompile(`(?m)^# nightgauge-gitignore-version: (\d+)\r?$`)

// GitignoreVersionMarker returns the template's version marker line.
func GitignoreVersionMarker() string {
	return strings.TrimSuffix(versionLine.FindString(GitignoreTemplate), "\r")
}

// GitignoreVersion is the template's version number.
func GitignoreVersion() int {
	v, _ := parseVersion(GitignoreTemplate)
	return v
}

// parseVersion reads the first version marker in content. ok is false when
// there is none or it does not parse; callers treat that as older.
func parseVersion(content string) (int, bool) {
	m := versionLine.FindStringSubmatch(content)
	if m == nil {
		return 0, false
	}
	v, err := strconv.Atoi(m[1])
	if err != nil {
		return 0, false
	}
	return v, true
}

// retiredRulesFile lists rule lines earlier template versions carried and
// the current one dropped (retired-rules.txt). A rewrite recognises them as
// the template's own, not the repository's, so it does not carry them into
// Local additions. The extension's RETIRED_RULES is pinned to this file by
// its test.
//
//go:embed retired-rules.txt
var retiredRulesFile string

// RetiredRules returns the rule lines in retired-rules.txt.
func RetiredRules() []string { return ruleLines(retiredRulesFile) }

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
	// Note explains a skip, a deferral or carried lines in one line.
	Note string `json:"note,omitempty"`
	// Changed is true when a file on disk was written.
	Changed bool `json:"changed"`
	// Carried lists rules an untracked file held outside its Local additions
	// section that the rewrite moved into it.
	Carried []string `json:"carried,omitempty"`
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
	own := GitignoreVersion()
	if v, ok := parseVersion(existing); ok && v >= own {
		res := IgnoreResult{Action: IgnoreCurrent, Path: ignorePath}
		if v > own {
			res.Note = fmt.Sprintf("version %d is newer than this writer's %d; left alone", v, own)
		}
		return res, nil
	}

	if IsTracked(repoRoot, ".nightgauge/.gitignore") {
		return deferToExclude(repoRoot, own)
	}

	local, carried := localAdditions(existing)
	if err := atomicfile.Write(ignorePath, []byte(GitignoreTemplate+local), info.Mode().Perm()); err != nil {
		return IgnoreResult{}, fmt.Errorf("write %s: %w", ignorePath, err)
	}
	res := IgnoreResult{Action: IgnoreUpdated, Path: ignorePath, Changed: true, Carried: carried}
	if len(carried) > 0 {
		res.Note = fmt.Sprintf("kept %d rule(s) from outside the Local additions section", len(carried))
	}
	return res, nil
}

// deferToExclude writes the template's rules to info/exclude for a committed
// older .nightgauge/.gitignore. The block carries its own version marker so a
// newer writer's block is never replaced by an older one.
func deferToExclude(repoRoot string, own int) (IgnoreResult, error) {
	excludePath, err := LocalExcludePath(repoRoot)
	if err != nil {
		return IgnoreResult{}, err
	}
	res := IgnoreResult{
		Action: IgnoreDeferred,
		Path:   excludePath,
		Note:   "the committed .nightgauge/.gitignore is older; current rules applied per machine",
	}
	block, err := ReadExcludeBlock(repoRoot, ExcludeBlockID)
	if err != nil {
		return IgnoreResult{}, err
	}
	if v, ok := parseVersion(strings.Join(block, "\n")); ok && v > own {
		res.Note = fmt.Sprintf("info/exclude carries version %d, newer than this writer's %d; left alone", v, own)
		return res, nil
	}
	changed, err := WriteExcludeBlock(repoRoot, ExcludeBlockID, ExcludePatterns())
	if err != nil {
		return IgnoreResult{}, err
	}
	res.Changed = changed
	return res, nil
}

// ExcludePatterns is the info/exclude block body: the version marker, then
// the template's rules re-anchored at the repository root.
func ExcludePatterns() []string {
	return append([]string{GitignoreVersionMarker()}, ToRootPatterns(".nightgauge", GitignoreTemplate)...)
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
	return IgnoreResult{Action: IgnoreCreated, Path: ignorePath, Changed: true}, nil
}

// localAdditions returns the text to keep after the template on a rewrite,
// exactly as the extension computes it: everything an existing file carries
// below its Local additions marker (minus the template's own text there),
// then every rule line above the marker (or anywhere, with no marker) that is
// neither a current nor a retired template rule and is not already kept.
// Without the second part a hand-extended file with no marker lost its
// custom rules silently. carried lists those moved rules.
func localAdditions(existing string) (string, []string) {
	head, local := existing, ""
	if at := strings.Index(existing, LocalAdditionsMarker); at >= 0 {
		head = existing[:at]
		after := existing[at+len(LocalAdditionsMarker):]
		tmplAt := strings.Index(GitignoreTemplate, LocalAdditionsMarker)
		canonicalTail := GitignoreTemplate[tmplAt+len(LocalAdditionsMarker):]
		local = strings.TrimPrefix(after, canonicalTail)
	}
	known := map[string]bool{}
	for _, l := range append(ruleLines(GitignoreTemplate), RetiredRules()...) {
		known[l] = true
	}
	for _, l := range ruleLines(local) {
		known[l] = true
	}
	var carried []string
	for _, l := range ruleLines(head) {
		if !known[l] {
			known[l] = true
			carried = append(carried, l)
		}
	}
	if len(carried) > 0 {
		if local != "" && !strings.HasSuffix(local, "\n") {
			local += "\n"
		}
		local += strings.Join(carried, "\n") + "\n"
	}
	return local, carried
}

// ruleLines returns content's non-blank, non-comment lines, trimmed.
func ruleLines(content string) []string {
	var out []string
	for _, l := range strings.Split(content, "\n") {
		l = strings.TrimSpace(l)
		if l != "" && !strings.HasPrefix(l, "#") {
			out = append(out, l)
		}
	}
	return out
}
