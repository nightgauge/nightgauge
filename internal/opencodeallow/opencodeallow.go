// Package opencodeallow computes the OpenCode external_directory allow-list
// as plain, resolved directory roots — the single source of truth both
// internal/execution/adapters (which projects these roots into opencode's
// own glob-pattern permission map, config-generation time) and
// internal/hooks' dispatch-time external-directory-gate (#1816, which
// resolves symlinks against these roots directly, once per tool call) build
// on.
//
// This is a standalone leaf package, deliberately not part of
// internal/execution/adapters: adapters imports
// internal/execution/opencodeplugin (to embed the OpenCode plugin tree), and
// internal/hooks' own tests import internal/execution/opencodeplugin too (to
// drive the embedded plugin's gates end to end) — so internal/hooks
// importing internal/execution/adapters directly would close an import
// cycle (opencodeplugin -> hooks -> adapters -> opencodeplugin). Neither
// adapters nor opencodeplugin import this package's own dependencies (only
// os/path/filepath/strings), so both sides can depend on it with no cycle.
package opencodeallow

import (
	"os"
	"path/filepath"
	"strings"
)

// TmpRoot and PrivateTmpRoot are the two /tmp scratch roots every dispatch's
// allow-list carries, in both the literal /tmp form and macOS's own
// symlink-resolved /private/tmp form.
const (
	TmpRoot        = "/tmp"
	PrivateTmpRoot = "/private/tmp"
)

// Options carries the allow-list-relevant fields a caller needs to compute
// Roots — the same three fields (plus the worktree) that
// adapters.RunOptions carries for this purpose, kept as an independent type
// here so this package never needs to import adapters.
type Options struct {
	// SkillPath is the dispatched skill's SKILL.md path; only its directory
	// (filepath.Dir) is used.
	SkillPath string
	// WorktreeDir is the run's git worktree root.
	WorktreeDir string
	// ContextFile is the input context JSON path.
	ContextFile string
	// OutputFile is the output context JSON path.
	OutputFile string
}

// SkillDir is opts.SkillPath's own directory (NIGHTGAUGE_SKILL_DIR), or ""
// when opts names no skill.
func SkillDir(opts Options) string {
	if opts.SkillPath == "" {
		return ""
	}
	return filepath.Dir(opts.SkillPath)
}

// InsideWorktree reports whether dir (as given, or resolved) is worktree or
// under it, in either's given/resolved form: a file's own directory and the
// worktree can each be reported unresolved or resolved independently (e.g.
// macOS's /var -> /private/var), so every combination is checked.
func InsideWorktree(worktree, dir string) bool {
	if worktree == "" || dir == "" {
		return false
	}
	dirForms := []string{dir}
	if r, err := filepath.EvalSymlinks(dir); err == nil {
		dirForms = append(dirForms, r)
	}
	worktreeForms := []string{worktree}
	if r, err := filepath.EvalSymlinks(worktree); err == nil {
		worktreeForms = append(worktreeForms, r)
	}
	for _, d := range dirForms {
		for _, w := range worktreeForms {
			if d == w || strings.HasPrefix(d, w+string(filepath.Separator)) {
				return true
			}
		}
	}
	return false
}

// Roots returns the resolved directories a read outside the worktree is
// allowed to touch: NIGHTGAUGE_SKILL_DIR, the context/output file
// directories (when outside the worktree), and the /tmp, /private/tmp
// scratch roots.
func Roots(opts Options) []string {
	var roots []string
	if dir := SkillDir(opts); dir != "" {
		roots = append(roots, dir)
	}
	addOutsideWorktree := func(file string) {
		if file == "" {
			return
		}
		dir := filepath.Dir(file)
		if InsideWorktree(opts.WorktreeDir, dir) {
			return
		}
		roots = append(roots, dir)
	}
	addOutsideWorktree(opts.ContextFile)
	addOutsideWorktree(opts.OutputFile)
	roots = append(roots, TmpRoot, PrivateTmpRoot)
	return roots
}

// RootsFromEnv rebuilds Options' three allow-list-relevant fields (skill
// dir, context file, output file) from the env vars manager.go/adapters set
// on every OpenCode child process (NIGHTGAUGE_SKILL_DIR,
// NIGHTGAUGE_CONTEXT_FILE, NIGHTGAUGE_OUTPUT_FILE), then calls Roots.
// worktreeDir is passed explicitly (the hook payload's own "cwd", not an env
// var).
func RootsFromEnv(worktreeDir string) []string {
	opts := Options{
		WorktreeDir: worktreeDir,
		ContextFile: os.Getenv("NIGHTGAUGE_CONTEXT_FILE"),
		OutputFile:  os.Getenv("NIGHTGAUGE_OUTPUT_FILE"),
	}
	if skillDir := os.Getenv("NIGHTGAUGE_SKILL_DIR"); skillDir != "" {
		opts.SkillPath = filepath.Join(skillDir, "SKILL.md")
	}
	return Roots(opts)
}
