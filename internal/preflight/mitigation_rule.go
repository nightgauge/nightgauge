// Mitigation-rule gate (#1263). Two mechanical halves of one rule:
//
//	Presence — the UNOBSERVED-MECHANISM RULE must be stated in the skills that
//	           are supposed to carry it. A rule nobody can find is a rule
//	           nobody follows, and "we wrote it down somewhere" is exactly the
//	           standard that let two unverified mitigations ship.
//	Markers  — every deliberate mitigation marker in the tree must name a
//	           tracking issue. Prose in a doc comment cannot be swept, counted
//	           or found again by anyone who does not already know it is there.
//
// The rule itself is judgement — no linter can tell an observed mechanism from
// a confident guess. What a linter CAN do is refuse the two silent failures
// around it: the guidance quietly disappearing from a skill during an edit, and
// an intentional mitigation shipping with an undertaking to come back that
// nothing records.
//
// Schema version 1 — field names are stable and consumed via fixed jq paths.
package preflight

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// RuleMarker is the canonical rule text. It is a literal, not a regex, so a
// grep for it in any tool finds exactly what this gate finds.
const RuleMarker = "UNOBSERVED-MECHANISM RULE"

// MitigationMarkerPrefix introduces a deliberate, tracked mitigation.
const MitigationMarkerPrefix = "NIGHTGAUGE-MITIGATION:"

// RequiredRuleSkills are the skills that must state the rule.
//
// feature-dev because it is where mitigation gets written, and check-triage
// because it is where the temptation is strongest — a session that has just
// spent an hour failing to reproduce something is the session most likely to
// reach for a retry.
var RequiredRuleSkills = []string{
	"nightgauge-feature-dev",
	"nightgauge-check-triage",
}

// MitigationRuleResult is the stable JSON output schema for
// `nightgauge preflight mitigation-rule`.
type MitigationRuleResult struct {
	V             int                     `json:"v"`
	Root          string                  `json:"root"`
	SkillsChecked int                     `json:"skills_checked"`
	FilesScanned  int                     `json:"files_scanned"`
	Findings      []MitigationRuleFinding `json:"findings"`
	Warnings      []string                `json:"warnings"`
}

// Finding check identifiers.
const (
	// CheckRuleMissing — a required skill does not state the rule.
	CheckRuleMissing = "rule_missing"
	// CheckMarkerUntracked — a mitigation marker names no tracking issue.
	CheckMarkerUntracked = "marker_untracked"
)

// MitigationRuleFinding is one violation.
type MitigationRuleFinding struct {
	Check   string `json:"check"`
	File    string `json:"file"` // relative to Root; the skill dir for rule_missing
	Line    int    `json:"line"` // 0 when whole-file
	Match   string `json:"match"`
	Message string `json:"message"`
}

// markerIssueRE matches the `issue=` field of a mitigation marker. The value
// must be non-empty and contain no whitespace — `issue=` followed by nothing,
// or by a prose sentence, is the same undertaking-nobody-recorded this gate
// exists to refuse.
var markerIssueRE = regexp.MustCompile(`\bissue=([^\s]+)`)

// scanExtensions are the file types a mitigation marker can live in. A marker
// belongs beside the code it excuses, so this follows source files rather than
// documentation.
var scanExtensions = map[string]bool{
	".go": true, ".ts": true, ".tsx": true, ".js": true, ".jsx": true,
	".dart": true, ".py": true, ".sh": true, ".yml": true, ".yaml": true,
}

// skipDirs are trees whose contents are not ours to police.
var skipDirs = map[string]bool{
	"node_modules": true, ".git": true, "dist": true, "out": true,
	"coverage": true, ".ci-local-logs": true,
}

// MitigationRuleOptions controls one run.
type MitigationRuleOptions struct {
	// Root is the repository root. Empty means the caller's CWD.
	Root string
}

// RunMitigationRuleCheck evaluates both halves of the gate. It returns a
// non-error result even when findings exist; the caller inspects
// len(result.Findings) to decide the exit code.
func RunMitigationRuleCheck(_ context.Context, opts MitigationRuleOptions) (*MitigationRuleResult, error) {
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

	res := &MitigationRuleResult{
		V:        1,
		Root:     root,
		Findings: []MitigationRuleFinding{},
		Warnings: []string{},
	}

	// --- Presence ---------------------------------------------------------
	for _, skill := range RequiredRuleSkills {
		res.SkillsChecked++
		dir := filepath.Join(root, "skills", skill)
		found, warn := skillStatesRule(dir)
		if warn != "" {
			res.Warnings = append(res.Warnings, warn)
		}
		if !found {
			res.Findings = append(res.Findings, MitigationRuleFinding{
				Check: CheckRuleMissing,
				File:  filepath.Join("skills", skill),
				Match: RuleMarker,
				Message: fmt.Sprintf(
					"%s does not state the %s — a rule nobody can find is a rule nobody follows",
					skill, RuleMarker),
			})
		}
	}

	// --- Markers ----------------------------------------------------------
	walkErr := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if skipDirs[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if !scanExtensions[filepath.Ext(path)] {
			return nil
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			rel = path
		}
		res.FilesScanned++
		findings, warn := scanMarkers(path, rel)
		if warn != "" {
			res.Warnings = append(res.Warnings, warn)
		}
		res.Findings = append(res.Findings, findings...)
		return nil
	})
	if walkErr != nil {
		return nil, fmt.Errorf("walk %s: %w", root, walkErr)
	}

	sort.SliceStable(res.Findings, func(i, j int) bool {
		if res.Findings[i].File != res.Findings[j].File {
			return res.Findings[i].File < res.Findings[j].File
		}
		return res.Findings[i].Line < res.Findings[j].Line
	})
	return res, nil
}

// skillStatesRule reports whether any markdown file in a skill directory states
// the rule. Any file, not specifically SKILL.md: a skill that states it in a
// supporting file has still stated it, and demanding a particular location
// would be a rule about filing rather than about the guidance.
func skillStatesRule(dir string) (bool, string) {
	var found bool
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || found {
			return nil
		}
		if d.IsDir() || filepath.Ext(path) != ".md" {
			return nil
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return nil
		}
		if strings.Contains(string(data), RuleMarker) {
			found = true
		}
		return nil
	})
	if err != nil {
		return found, fmt.Sprintf("walk %s: %v", dir, err)
	}
	return found, ""
}

// commentLeaders introduce a comment in the file types scanned. A marker
// belongs beside the code it excuses, which means in a comment.
var commentLeaders = []string{"//", "#", "*", "<!--"}

// inComment reports whether the text preceding a marker on its line puts the
// marker inside a comment. Deliberately syntactic and shallow: it asks whether
// a comment leader appears before the marker on the same line, which is true of
// every real marker and false of the string literals that merely mention the
// token.
func inComment(before string) bool {
	for _, leader := range commentLeaders {
		if strings.Contains(before, leader) {
			return true
		}
	}
	return false
}

// scanMarkers finds mitigation markers in one file and flags any that name no
// tracking issue.
func scanMarkers(path, rel string) ([]MitigationRuleFinding, string) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Sprintf("read %s: %v", rel, err)
	}
	defer f.Close()

	var out []MitigationRuleFinding
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for line := 1; sc.Scan(); line++ {
		text := sc.Text()
		idx := strings.Index(text, MitigationMarkerPrefix)
		if idx < 0 {
			continue
		}
		// A marker only counts inside a comment. The token also appears in
		// this file's own constant and in test fixtures that build a marker as
		// data, and neither is a mitigation — a gate that cannot tell its own
		// definition from a violation fails on its first run, which is a
		// perfect way to teach everyone to skip it.
		if !inComment(text[:idx]) {
			continue
		}
		rest := strings.TrimSpace(text[idx+len(MitigationMarkerPrefix):])
		if rest == "" {
			continue
		}
		m := markerIssueRE.FindStringSubmatch(rest)
		if m == nil || strings.TrimSpace(m[1]) == "" {
			out = append(out, MitigationRuleFinding{
				Check: CheckMarkerUntracked,
				File:  rel,
				Line:  line,
				Match: strings.TrimSpace(text),
				Message: "mitigation marker names no tracking issue — add issue=<owner/repo#N>, " +
					"so an undertaking to come back outlives the session that made it",
			})
		}
	}
	if err := sc.Err(); err != nil {
		return out, fmt.Sprintf("scan %s: %v", rel, err)
	}
	return out, ""
}
