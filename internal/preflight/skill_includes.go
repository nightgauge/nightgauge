// Dead-include gate (#337). Every `<!-- include: path -->` directive in a
// shipped skill must resolve to a file that exists.
//
// Why this needs a gate at all: include expansion is deliberately fail-open.
// `skillrender.ExpandIncludes` leaves a directive in place when its target
// cannot be read, because the same document has to stay readable under a host
// that does not expand includes at all (Claude Code, for one, never does). The
// cost of that portability rule is that a DEAD include and a DELIBERATELY
// UNEXPANDED one are byte-identical: the model receives the literal HTML
// comment where the shared content should have been, and nothing anywhere
// says so. Six shipped stage skills carried
// `<!-- include: ../_shared/BATCH_MODE.md -->` for the entire life of the
// repository against a file that was never written.
//
// The check resolves targets with skillrender.IncludePattern — the composer's
// OWN regex, not a copy. That matters: capture group 1 is non-greedy up to the
// first ` -->`, so `<!-- include: ../_shared/EPIC_HANDLING.md (sub-issue fetch
// section) -->` captures the parenthetical as part of the path. The file it
// names exists; the path it captures does not. A gate carrying its own regex
// could disagree with the composer about what the path IS and miss exactly
// that case, so the finding reports the captured path VERBATIM — seeing
// `../_shared/EPIC_HANDLING.md (sub-issue fetch section)` printed back is what
// makes a malformed directive self-evident, where "target not found" alone
// sends a reader hunting for a file that is sitting right there.
//
// Scope is SKILL.md plus supporting files under _includes/ and _shared/.
// skills/README.md is deliberately NOT scanned: it documents the directive
// shape in prose and in fenced examples, and those are not directives.
//
// Schema version 1 — field names (v, root, trees, files_checked, findings,
// warnings) are stable and consumed by callers via fixed jq paths.
package preflight

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/nightgauge/nightgauge/internal/skillrender"
)

// DefaultSkillIncludeTrees are the two trees that ship skills to a model: the
// canonical source, and the generated Claude-plugin mirror that plugin
// sessions are actually served from. Both are scanned — a fix applied only to
// the canonical tree still ships the dead include to every plugin session
// until the mirror is regenerated.
var DefaultSkillIncludeTrees = []string{
	"skills",
	filepath.Join("claude-plugins", "nightgauge", "skills"),
}

// SkillIncludesResult is the stable JSON output schema for
// `nightgauge preflight skill-includes`.
type SkillIncludesResult struct {
	V            int                `json:"v"`             // schema version, always 1
	Root         string             `json:"root"`          // absolute path
	Trees        []string           `json:"trees"`         // root-relative trees scanned
	FilesChecked int                `json:"files_checked"` // count of SKILL.md + supporting .md files inspected
	Findings     []SkillDeadInclude `json:"findings"`      // one entry per unresolvable directive
	Warnings     []string           `json:"warnings"`      // non-fatal issues (read errors, etc.)
}

// SkillDeadInclude describes a single include directive whose target does not
// resolve to an existing file.
type SkillDeadInclude struct {
	File      string `json:"file"`      // path relative to Root
	Line      int    `json:"line"`      // 1-based line number of the directive
	Target    string `json:"target"`    // capture group 1, VERBATIM — the string the composer tries to open
	Resolved  string `json:"resolved"`  // Target joined to the containing file's directory, relative to Root
	Directive string `json:"directive"` // the whole matched directive, as it appears in the file
}

// SkillIncludesOptions controls a single dead-include run.
type SkillIncludesOptions struct {
	// Root is the repository root. When empty, the caller's CWD is used.
	Root string
	// Trees are root-relative directories to scan. When empty,
	// DefaultSkillIncludeTrees is used. A tree that does not exist is skipped
	// silently — not every consumer of this package ships a plugin mirror.
	Trees []string
}

// RunSkillIncludesCheck walks the configured skill trees and emits a finding
// for every include directive whose target does not resolve. Returns a
// non-error result even when findings exist — the caller inspects
// len(result.Findings) to decide the gate exit code.
func RunSkillIncludesCheck(_ context.Context, opts SkillIncludesOptions) (*SkillIncludesResult, error) {
	root := opts.Root
	if root == "" {
		var err error
		root, err = os.Getwd()
		if err != nil {
			return nil, fmt.Errorf("resolve root: %w", err)
		}
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve root: %w", err)
	}
	if info, statErr := os.Stat(abs); statErr != nil || !info.IsDir() {
		return nil, fmt.Errorf("root %q is not a readable directory", root)
	}
	root = abs

	trees := opts.Trees
	if len(trees) == 0 {
		trees = DefaultSkillIncludeTrees
	}

	result := &SkillIncludesResult{
		V:        1,
		Root:     root,
		Trees:    append([]string{}, trees...),
		Findings: []SkillDeadInclude{},
		Warnings: []string{},
	}

	var files []string
	for _, tree := range trees {
		treeAbs := filepath.Join(root, tree)
		if info, statErr := os.Stat(treeAbs); statErr != nil || !info.IsDir() {
			continue // absent tree is a clean no-op
		}
		walkErr := filepath.WalkDir(treeAbs, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return nil // skip unreadable subtrees, do not abort the walk
			}
			if d.IsDir() || filepath.Ext(path) != ".md" {
				return nil
			}
			if isScannedSkillFile(path) {
				files = append(files, path)
			}
			return nil
		})
		if walkErr != nil {
			return nil, fmt.Errorf("walk %s: %w", treeAbs, walkErr)
		}
	}
	sort.Strings(files)
	result.FilesChecked = len(files)

	for _, path := range files {
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			result.Warnings = append(result.Warnings, fmt.Sprintf("read %s: %v", path, readErr))
			continue
		}
		content := string(data)
		dir := skillDirFor(path)
		for _, loc := range skillrender.IncludePattern.FindAllStringSubmatchIndex(content, -1) {
			directive := content[loc[0]:loc[1]]
			target := strings.TrimSpace(content[loc[2]:loc[3]])
			// Resolution is the composer's: ExpandIncludes always joins the
			// captured path to the SKILL.md's directory, never to the directory
			// of the file carrying the directive. For a directive inside
			// _includes/ those differ by one level, and resolving against the
			// wrong one reports a path the composer would never try.
			resolved := filepath.Join(dir, target)
			if info, statErr := os.Stat(resolved); statErr == nil && !info.IsDir() {
				continue
			}
			result.Findings = append(result.Findings, SkillDeadInclude{
				File:      relOrAbs(root, path),
				Line:      strings.Count(content[:loc[0]], "\n") + 1,
				Target:    target,
				Resolved:  relOrAbs(root, resolved),
				Directive: directive,
			})
		}
	}

	return result, nil
}

// skillDirFor returns the directory a directive in path resolves against —
// the composer's `skillDir`, i.e. the directory holding the owning SKILL.md.
// For a SKILL.md that is its own directory; for a supporting file it is the
// nearest ancestor containing a SKILL.md. A _shared/ file has no owning skill
// (the composer never expands one), so its own directory is used.
func skillDirFor(path string) string {
	dir := filepath.Dir(path)
	if filepath.Base(path) == "SKILL.md" {
		return dir
	}
	for cur := dir; ; {
		if _, err := os.Stat(filepath.Join(cur, "SKILL.md")); err == nil {
			return cur
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return dir
		}
		cur = parent
	}
}

// isScannedSkillFile reports whether a .md path is in scope: a SKILL.md, or a
// supporting file under a _includes/ or _shared/ directory. Everything else in
// the skill trees — README.md above all — documents the directive shape rather
// than using it, and flagging those examples would fail CI on the docs.
func isScannedSkillFile(path string) bool {
	if filepath.Base(path) == "SKILL.md" {
		return true
	}
	sep := string(filepath.Separator)
	for _, seg := range strings.Split(filepath.Dir(path), sep) {
		if seg == "_includes" || seg == "_shared" {
			return true
		}
	}
	return false
}
