// Skill direct-gh deprecation linter. Mirrors
// scripts/lint-skills/no-direct-gh.sh — same scope (skills/*/SKILL.md),
// same regex (\bgh ), same exit-code semantics. The Go form is what CI
// runs (faster, no bash required); the shell form is the developer-
// friendly path during interactive editing.
//
// Schema version 1 — field names (v, root, skills_checked, findings,
// warnings) are stable and consumed by callers via fixed jq paths.
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

// SkillNoDirectGHResult is the stable JSON output schema for
// `nightgauge preflight skill-no-direct-gh`.
type SkillNoDirectGHResult struct {
	V              int                    `json:"v"`               // schema version, always 1
	Root           string                 `json:"root"`            // absolute path
	SkillsChecked  int                    `json:"skills_checked"`  // count of executable skill files inspected (SKILL.md + _includes/ + _shared/)
	SkillsExempted []string               `json:"skills_exempted"` // allowlist entries that suppressed findings
	Findings       []SkillDirectGHFinding `json:"findings"`        // one entry per direct gh occurrence in non-allowlisted skills
	Warnings       []string               `json:"warnings"`        // non-fatal issues (read errors, etc.)
}

// SkillDirectGHFinding describes a single offending line in a SKILL.md.
type SkillDirectGHFinding struct {
	SkillFile string `json:"skill_file"` // path relative to Root
	Line      int    `json:"line"`       // 1-based line number
	Match     string `json:"match"`      // line content (trimmed)
}

// SkillNoDirectGHOptions controls a single linter run.
type SkillNoDirectGHOptions struct {
	// Root is the repository root. When empty, the caller's CWD is used.
	Root string
	// AllowlistPath is the file containing skill directory names to
	// exempt from the gate (one name per line; '#' starts a comment).
	// When empty, defaults to <Root>/scripts/lint-skills/allowlist.txt.
	// When the file does not exist, no skills are exempted.
	AllowlistPath string
}

// directGHRE matches `gh ` as a standalone token (word boundary on the
// left, space on the right). The simple form is intentional — false
// positives like the word "though" do not start with `gh` because of the
// word boundary, and `gh` immediately followed by a space is the shape
// every real CLI invocation takes.
var directGHRE = regexp.MustCompile(`\bgh `)

// RunSkillNoDirectGHCheck walks every `skills/*/SKILL.md` rooted at Root
// and emits a finding for each line containing the directGHRE pattern.
// Returns a non-error result even when findings exist — the caller
// inspects len(result.Findings) to decide the gate exit code.
func RunSkillNoDirectGHCheck(_ context.Context, opts SkillNoDirectGHOptions) (*SkillNoDirectGHResult, error) {
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

	result := &SkillNoDirectGHResult{
		V:              1,
		Root:           root,
		Findings:       []SkillDirectGHFinding{},
		SkillsExempted: []string{},
		Warnings:       []string{},
	}

	// Load the allowlist (best-effort — missing file means no exemptions).
	allowlistPath := opts.AllowlistPath
	if allowlistPath == "" {
		allowlistPath = filepath.Join(root, "scripts", "lint-skills", "allowlist.txt")
	}
	allowed, allowErr := loadSkillAllowlist(allowlistPath)
	if allowErr != nil {
		result.Warnings = append(result.Warnings, fmt.Sprintf("read allowlist %s: %v", allowlistPath, allowErr))
	}

	// Scope: every file a stage actually EXECUTES, not just SKILL.md.
	//
	// This used to be one glob, `skills/*/SKILL.md`. A skill's SKILL.md is
	// the smallest part of what runs: the bodies live in `_includes/` next
	// to it and in the cross-skill `_shared/` directory, both pulled in by
	// `<!-- include: -->`. So every expensive direct call in the tree sat in
	// exactly the directories this gate never opened, and the skills that
	// inherited them passed clean. `_shared/AUTO_SELECTION.md` pulled the
	// whole project board per tier that way, on the default pickup path.
	//
	// CHANGELOG.md, reference/ and tests/ files under a skill are prose
	// about the skill rather than steps a stage runs, so they stay out of
	// scope; widening to every .md would gate documentation on a CLI ban.
	var matches []string
	for _, pattern := range []string{
		filepath.Join(root, "skills", "*", "SKILL.md"),
		filepath.Join(root, "skills", "*", "_includes", "*.md"),
		filepath.Join(root, "skills", "_shared", "*.md"),
	} {
		found, err := filepath.Glob(pattern)
		if err != nil {
			return nil, fmt.Errorf("glob %s: %w", pattern, err)
		}
		matches = append(matches, found...)
	}
	sort.Strings(matches)
	result.SkillsChecked = len(matches)

	exempted := map[string]bool{}
	for _, path := range matches {
		// Derive the owning skill from the path:
		//   <root>/skills/<name>/SKILL.md            → <name>
		//   <root>/skills/<name>/_includes/x.md      → <name>
		//   <root>/skills/_shared/x.md               → _shared
		//
		// A `_shared` file resolves to "_shared", which is deliberately not
		// a skill name: an include used by many skills must never inherit
		// one skill's allowlist exemption.
		skillName := filepath.Base(filepath.Dir(path))
		if skillName == "_includes" {
			skillName = filepath.Base(filepath.Dir(filepath.Dir(path)))
		}
		data, err := os.ReadFile(path)
		if err != nil {
			result.Warnings = append(result.Warnings, fmt.Sprintf("read %s: %v", path, err))
			continue
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			rel = path
		}
		// An allowlist entry is either a skill directory name or a single
		// repo-relative file path. The path form exists for `_shared/`,
		// which has no skill name to exempt, and because exempting one
		// reviewed file is honest where exempting a whole skill is a
		// blanket nobody re-reads.
		isAllowed := allowed[skillName] || allowed[filepath.ToSlash(rel)]
		lines := strings.Split(string(data), "\n")
		inFence := false
		for i, line := range lines {
			// Only lines a stage actually RUNS can spend quota. A skill is
			// a Markdown document: most of a `gh ` match in it is prose —
			// and `_shared/CI_GATE.md` even spends two lines FORBIDDING a
			// hand-rolled `gh pr checks` poll. Flagging that as a violation
			// is how a gate earns a reputation for noise and gets
			// allowlisted into uselessness, which is the failure this whole
			// guard exists to prevent. So: fenced code only, comments
			// excluded.
			if strings.HasPrefix(strings.TrimSpace(line), "```") {
				inFence = !inFence
				continue
			}
			if !inFence {
				continue
			}
			if strings.HasPrefix(strings.TrimSpace(line), "#") {
				continue
			}
			if directGHRE.MatchString(line) {
				if isAllowed {
					// Report the exemption by the thing that was actually
					// exempted. A path-scoped entry covers one file; saying
					// "_shared" would read as the whole shared directory
					// being waived, which it is not — AUTO_SELECTION.md and
					// its neighbours are still gated.
					if allowed[filepath.ToSlash(rel)] {
						exempted[filepath.ToSlash(rel)] = true
					} else {
						exempted[skillName] = true
					}
					continue
				}
				trimmed := strings.TrimSpace(line)
				if len(trimmed) > 200 {
					trimmed = trimmed[:200] + "…"
				}
				result.Findings = append(result.Findings, SkillDirectGHFinding{
					SkillFile: rel,
					Line:      i + 1,
					Match:     trimmed,
				})
			}
		}
	}

	for name := range exempted {
		result.SkillsExempted = append(result.SkillsExempted, name)
	}
	sort.Strings(result.SkillsExempted)

	return result, nil
}

// loadSkillAllowlist parses the allowlist file. Format: one skill
// directory name per line; lines starting with '#' are comments; blank
// lines are ignored. Returns an empty (non-nil) map when the file is
// absent — the caller treats that as "no exemptions".
func loadSkillAllowlist(path string) (map[string]bool, error) {
	allowed := map[string]bool{}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return allowed, nil
	}
	if err != nil {
		return allowed, err
	}
	for _, line := range strings.Split(string(data), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		allowed[trimmed] = true
	}
	return allowed, nil
}
