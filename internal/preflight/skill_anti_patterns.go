// Skill anti-pattern linter. Covers the three mechanically-detectable
// authoring anti-patterns Anthropic warns against (issue #3813, epic #3808):
//
//	A — nested references: a referenced supporting file (_includes/, _shared/)
//	    must not itself direct the agent to read a *further* supporting file.
//	    Anthropic guidance: keep references one level deep — nested refs cause
//	    partial head-100 reads and lost context.
//	B — backslash paths: a path-like token using Windows '\' separators
//	    (skills\foo\bar.md). Cross-platform skills must use '/'.
//	C — missing TOC: a long supporting file lacking a '## Contents' heading,
//	    matching the established _includes/ convention.
//
// The four judgment-based anti-patterns (time-sensitive info, inconsistent
// terminology, options-without-default, magic numbers) are NOT mechanizable
// without high false-positive rates — they are handled by the manual sweep in
// docs/skills-anti-pattern-sweep.md, not this gate (see ADR-002 in the issue
// knowledge base).
//
// Mirrors scripts/lint-skills/anti-patterns.sh — same scope, same checks,
// same exit-code semantics. The Go form is what CI runs (faster, no bash
// required); the shell form is the developer-friendly path.
//
// Schema version 1 — field names (v, root, files_checked, findings, warnings)
// are stable and consumed by callers via fixed jq paths.
package preflight

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// tocMinLines is the line-count threshold above which a supporting file must
// carry a '## Contents' (or '## Table of Contents') heading. Chosen at 150:
// the existing well-formed _includes/ supporting files that already open with
// a Contents block are all ≥150 lines, while every supporting file below that
// is short enough to scan without a TOC. Named (not inlined) deliberately —
// this issue is itself about magic numbers (#3813).
const tocMinLines = 150

// Anti-pattern check identifiers used in the Check field of a finding.
const (
	CheckNestedReference  = "nested_reference"
	CheckBackslashPath    = "backslash_path"
	CheckMissingTOC       = "missing_toc"
	CheckAdminMergeBypass = "admin_merge_bypass"
)

// SkillAntiPatternsResult is the stable JSON output schema for
// `nightgauge preflight skill-anti-patterns`.
type SkillAntiPatternsResult struct {
	V            int                `json:"v"`             // schema version, always 1
	Root         string             `json:"root"`          // absolute path
	FilesChecked int                `json:"files_checked"` // count of skill/supporting .md files inspected
	Findings     []SkillAntiPattern `json:"findings"`      // one entry per occurrence, categorized by Check
	Warnings     []string           `json:"warnings"`      // non-fatal issues (read errors, etc.)
}

// SkillAntiPattern describes a single anti-pattern occurrence.
type SkillAntiPattern struct {
	Check string `json:"check"` // one of nested_reference, backslash_path, missing_toc
	File  string `json:"file"`  // path relative to Root
	Line  int    `json:"line"`  // 1-based line number (0 for whole-file findings like missing_toc)
	Match string `json:"match"` // offending line content (trimmed) or a short description
}

// SkillAntiPatternsOptions controls a single linter run.
type SkillAntiPatternsOptions struct {
	// Root is the repository root. When empty, the caller's CWD is used.
	Root string
}

// nestedRefRE matches an imperative read-directive pointing at a supporting
// file path (_includes/ or _shared/). When such a directive appears INSIDE a
// supporting file (rather than a top-level SKILL.md), it is a nested
// reference. The path must travel through _includes/ or _shared/ so that
// runtime data reads like "Read PLAN.md" or "Read decisions.md and PRD.md"
// are NOT matched — those are not structural supporting-file references.
var nestedRefRE = regexp.MustCompile(`(?i)(?:read|see|follow)\b[^\n]*?` + "`?" + `[^` + "`" + `\s]*(?:_includes|_shared)/[^` + "`" + `\s]*\.md`)

// includeDirectiveRE matches the markdown include directive shape
// (<!-- include: ... -->). When it appears inside a supporting file it is also
// a nested reference (a supporting file pulling in another supporting file).
var includeDirectiveRE = regexp.MustCompile(`<!--\s*include:\s*[^\s]+\.md`)

// backslashPathRE matches a path-like token that uses a Windows backslash
// separator: a known path directory segment immediately followed by '\' and a
// word char, OR any word\word.<ext> file token. Escape/regex contexts
// (\n \t \d \w \s \. \( etc.) are excluded because the segment to the LEFT of
// the backslash must be a path-directory word or a filename word, not empty.
var backslashPathRE = regexp.MustCompile(`(?:skills|src|docs|packages|internal|cmd|scripts|tests|node_modules)\\[A-Za-z0-9_.]+|[A-Za-z0-9_]+\\[A-Za-z0-9_]+\.(?:md|go|ts|js|tsx|jsx|sh|json|ya?ml|py)`)

// tocHeadingRE matches a Contents / Table of Contents heading at H1 or H2
// (the established supporting-file convention opens with '## Contents').
var tocHeadingRE = regexp.MustCompile(`(?im)^#{1,2}\s+(Contents|Table of Contents)\b`)

// adminMergeRE matches a merge invocation or advertisement carrying an
// admin/auto bypass flag: `pr merge ... --admin`, `mr merge ... --auto`, or
// a skill argument line advertising `--admin` on the skill's own slash
// command. Merges must never bypass branch protection (#186 — a pipeline
// agent improvised `gh pr merge --admin` because the skill advertised the
// flag). Prohibition prose ("never pass `--admin`") is not matched: the flag
// must appear on the same line as a merge invocation or slash command.
// The trailing guard excludes longer flags like `--auto-fix` (RE2 has no
// lookahead, so a non-word/non-hyphen follower or end-of-line is required).
var adminMergeRE = regexp.MustCompile(`(?i)(?:\b(?:pr|mr)\s+merge\b|/[a-z0-9-]*pr-merge\b)[^\n]*\s--(?:admin|auto)(?:[^a-z0-9-]|$)`)

// RunSkillAntiPatternsCheck walks the skill tree rooted at Root and emits a
// finding for each occurrence of the three mechanical anti-patterns. Returns a
// non-error result even when findings exist — the caller inspects
// len(result.Findings) to decide the gate exit code.
func RunSkillAntiPatternsCheck(_ context.Context, opts SkillAntiPatternsOptions) (*SkillAntiPatternsResult, error) {
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

	result := &SkillAntiPatternsResult{
		V:        1,
		Root:     root,
		Findings: []SkillAntiPattern{},
		Warnings: []string{},
	}

	// Collect the files to inspect. SKILL.md bodies are checked for backslash
	// paths (Check B applies everywhere); supporting files (_includes/,
	// _shared/) are additionally checked for nested references (Check A) and
	// missing TOC (Check C). Only files ending in exactly ".md" are walked —
	// editor backups like SKILL.md.bak are skipped by extension.
	skillsDir := filepath.Join(root, "skills")
	var skillMDs, supportingMDs []string
	walkErr := filepath.WalkDir(skillsDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil // skip unreadable subtrees, do not abort the walk
		}
		if d.IsDir() {
			return nil
		}
		if filepath.Ext(path) != ".md" {
			return nil
		}
		base := filepath.Base(path)
		dir := filepath.Dir(path)
		switch {
		case base == "SKILL.md":
			skillMDs = append(skillMDs, path)
		case strings.Contains(dir, string(filepath.Separator)+"_includes") ||
			strings.HasSuffix(dir, string(filepath.Separator)+"_shared") ||
			filepath.Base(dir) == "_shared":
			supportingMDs = append(supportingMDs, path)
		}
		return nil
	})
	if walkErr != nil {
		return nil, fmt.Errorf("walk %s: %w", skillsDir, walkErr)
	}
	sort.Strings(skillMDs)
	sort.Strings(supportingMDs)
	result.FilesChecked = len(skillMDs) + len(supportingMDs)

	// Checks B (backslash paths) and D (admin merge bypass) apply to ALL
	// skill + supporting .md files.
	for _, path := range append(append([]string{}, skillMDs...), supportingMDs...) {
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			result.Warnings = append(result.Warnings, fmt.Sprintf("read %s: %v", path, readErr))
			continue
		}
		rel := relOrAbs(root, path)
		for i, line := range strings.Split(string(data), "\n") {
			if backslashPathRE.MatchString(line) {
				result.Findings = append(result.Findings, SkillAntiPattern{
					Check: CheckBackslashPath,
					File:  rel,
					Line:  i + 1,
					Match: trimMatch(line),
				})
			}
			if adminMergeRE.MatchString(line) {
				result.Findings = append(result.Findings, SkillAntiPattern{
					Check: CheckAdminMergeBypass,
					File:  rel,
					Line:  i + 1,
					Match: trimMatch(line),
				})
			}
		}
	}

	// Checks A (nested references) and C (missing TOC) apply to supporting
	// files only.
	for _, path := range supportingMDs {
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			result.Warnings = append(result.Warnings, fmt.Sprintf("read %s: %v", path, readErr))
			continue
		}
		rel := relOrAbs(root, path)
		content := string(data)
		lines := strings.Split(content, "\n")

		// Check A — nested references inside a supporting file.
		for i, line := range lines {
			if nestedRefRE.MatchString(line) || includeDirectiveRE.MatchString(line) {
				result.Findings = append(result.Findings, SkillAntiPattern{
					Check: CheckNestedReference,
					File:  rel,
					Line:  i + 1,
					Match: trimMatch(line),
				})
			}
		}

		// Check C — missing TOC on a long supporting file. Inspect the first
		// 40 lines (the convention opens with a Contents block near the top).
		if len(lines) > tocMinLines {
			head := content
			if len(lines) > 40 {
				head = strings.Join(lines[:40], "\n")
			}
			if !tocHeadingRE.MatchString(head) {
				result.Findings = append(result.Findings, SkillAntiPattern{
					Check: CheckMissingTOC,
					File:  rel,
					Line:  0,
					Match: fmt.Sprintf("%d lines, no '## Contents' heading in first 40 lines (threshold %d)", len(lines), tocMinLines),
				})
			}
		}
	}

	return result, nil
}

// trimMatch trims surrounding whitespace and caps the length so a single
// pathological line cannot blow up the JSON output.
func trimMatch(line string) string {
	trimmed := strings.TrimSpace(line)
	if len(trimmed) > 200 {
		trimmed = trimmed[:200] + "…"
	}
	return trimmed
}
