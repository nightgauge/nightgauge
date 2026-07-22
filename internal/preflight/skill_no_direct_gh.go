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
	SkillsChecked  int                    `json:"skills_checked"`  // count of skills/*/SKILL.md files inspected
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

	skillsGlob := filepath.Join(root, "skills", "*", "SKILL.md")
	matches, err := filepath.Glob(skillsGlob)
	if err != nil {
		return nil, fmt.Errorf("glob %s: %w", skillsGlob, err)
	}
	sort.Strings(matches)
	result.SkillsChecked = len(matches)

	exempted := map[string]bool{}
	for _, path := range matches {
		// Derive the skill directory name from the SKILL.md path:
		//   <root>/skills/<name>/SKILL.md → <name>
		skillName := filepath.Base(filepath.Dir(path))
		isAllowed := allowed[skillName]

		data, err := os.ReadFile(path)
		if err != nil {
			result.Warnings = append(result.Warnings, fmt.Sprintf("read %s: %v", path, err))
			continue
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			rel = path
		}
		lines := strings.Split(string(data), "\n")
		for i, line := range lines {
			if directGHRE.MatchString(line) {
				if isAllowed {
					exempted[skillName] = true
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
