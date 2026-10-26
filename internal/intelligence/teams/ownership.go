package teams

import (
	"path"
	"regexp"
	"sort"
	"strings"

	"github.com/nightgauge/nightgauge/internal/intelligence/acparse"
)

// OwnershipFindingType is the finding type every CheckFileOwnership result
// carries (#561).
const OwnershipFindingType = "FILE_OWNERSHIP_UNSUBSTANTIATED"

// Ownership rules — the deterministic failure classes CheckFileOwnership
// detects. "No production edit needed" and "change is provably inert" need
// runtime reasoning and stay a human review item (docs/ISSUE_AUDIT.md).
const (
	OwnershipRuleTestMissing     = "test-missing"
	OwnershipRuleGeneratedMirror = "generated-mirror"
	OwnershipRulePathMissing     = "path-missing"
)

// GeneratedMirror is a generated path (or directory prefix, ending in "/")
// and the canonical path it is generated from. Editing the generated side is
// overwritten by the next sync, so a declared set naming it cannot satisfy an
// AC.
type GeneratedMirror struct {
	Generated string
	Canonical string
	// Generator is the script that writes Generated from Canonical.
	Generator string
}

// GeneratedMirrors is the single Go list of generated mirrors. A parity test
// reads each Generator script and fails when a pair it writes is missing here.
var GeneratedMirrors = []GeneratedMirror{
	{
		Generated: "claude-plugins/nightgauge/skills/",
		Canonical: "skills/",
		Generator: "scripts/install-agent-skills.sh",
	},
	{
		Generated: "internal/models/model-registry.json",
		Canonical: "packages/nightgauge-sdk/src/eval/model-registry.json",
		Generator: "scripts/sync-model-registry.sh",
	},
}

// OwnershipFinding is one declared path that the issue's ACs do not
// substantiate. Path is empty for set-level findings (test-missing).
type OwnershipFinding struct {
	Type      string `json:"type"`
	Rule      string `json:"rule"`
	Path      string `json:"path"`
	Reason    string `json:"reason"`
	Canonical string `json:"canonical,omitempty"`
}

var (
	acTestRe   = regexp.MustCompile(`(?i)\btests?\b|_test\.go|\.test\.|\.spec\.`)
	acCreateRe = regexp.MustCompile(`(?i)\b(create[sd]?|add(s|ed)?|new|introduce[sd]?)\b`)
)

// isTestPath reports whether p is a test file by the repository-agnostic
// conventions the check recognises.
func isTestPath(p string) bool {
	base := path.Base(p)
	return strings.HasSuffix(base, "_test.go") ||
		strings.Contains(base, ".test.") ||
		strings.Contains(base, ".spec.") ||
		(strings.HasPrefix(base, "test_") && strings.HasSuffix(base, ".py")) ||
		strings.HasPrefix(p, "tests/") || strings.Contains(p, "/tests/")
}

// canonicalFor maps a generated path to its canonical source. The skills
// mirror strips the `nightgauge-` directory prefix (install-agent-skills.sh),
// so `<mirror>/<x>/...` resolves to `skills/nightgauge-<x>/...` when that
// exists, otherwise `skills/<x>/...` (for example `_shared`).
func canonicalFor(m GeneratedMirror, p string, exists func(string) bool) string {
	if !strings.HasSuffix(m.Generated, "/") {
		return m.Canonical
	}
	rest := strings.TrimPrefix(p, m.Generated)
	if rest == "" {
		return m.Canonical
	}
	if m.Canonical == "skills/" {
		prefixed := m.Canonical + "nightgauge-" + rest
		dir := strings.SplitN(rest, "/", 2)[0]
		if exists(m.Canonical + "nightgauge-" + dir) {
			return prefixed
		}
	}
	return m.Canonical + rest
}

func matchMirror(p string) (GeneratedMirror, bool) {
	for _, m := range GeneratedMirrors {
		if p == m.Generated || (strings.HasSuffix(m.Generated, "/") && strings.HasPrefix(p, m.Generated)) {
			return m, true
		}
	}
	return GeneratedMirror{}, false
}

// CheckFileOwnership verifies an issue body's declared `file_ownership` set
// against its acceptance criteria (#561). exists reports whether a
// repository-relative path exists. A body with no declaration returns nil:
// inferred targets are not a declaration. Output is sorted by path, then rule.
func CheckFileOwnership(body string, exists func(string) bool) []OwnershipFinding {
	declared := declaredTargetFiles(body)
	if len(declared) == 0 {
		return nil
	}
	items := acparse.List(body)
	var findings []OwnershipFinding

	// test-missing: an AC asks for a test and no declared path is a test
	// file. An existing sibling test does not count: it sits outside the
	// owned set, so the issue cannot edit it (#536).
	acWantsTest := false
	for _, it := range items {
		if acTestRe.MatchString(it.Text) {
			acWantsTest = true
			break
		}
	}
	if acWantsTest {
		covered := false
		for _, p := range declared {
			if isTestPath(p) {
				covered = true
				break
			}
		}
		if !covered {
			findings = append(findings, OwnershipFinding{
				Type:   OwnershipFindingType,
				Rule:   OwnershipRuleTestMissing,
				Reason: "AC requires a test, declared set contains none",
			})
		}
	}

	for _, p := range declared {
		if m, ok := matchMirror(p); ok {
			canonical := canonicalFor(m, p, exists)
			findings = append(findings, OwnershipFinding{
				Type:      OwnershipFindingType,
				Rule:      OwnershipRuleGeneratedMirror,
				Path:      p,
				Reason:    "generated by " + m.Generator + "; edit " + canonical + " instead",
				Canonical: canonical,
			})
			continue
		}
		if !exists(p) && !createIntended(body, items, p) {
			findings = append(findings, OwnershipFinding{
				Type:   OwnershipFindingType,
				Rule:   OwnershipRulePathMissing,
				Path:   p,
				Reason: "path does not exist and no AC says to create it",
			})
		}
	}

	sort.SliceStable(findings, func(i, j int) bool {
		if findings[i].Path != findings[j].Path {
			return findings[i].Path < findings[j].Path
		}
		return findings[i].Rule < findings[j].Rule
	})
	return findings
}

// createIntended reports whether an AC, or any body line naming p's
// basename, says to create/add/introduce it.
func createIntended(body string, items []acparse.Item, p string) bool {
	base := path.Base(p)
	for _, it := range items {
		if acCreateRe.MatchString(it.Text) && strings.Contains(it.Text, base) {
			return true
		}
	}
	for _, line := range strings.Split(body, "\n") {
		if strings.Contains(line, "file_ownership") || strings.HasPrefix(strings.TrimSpace(line), "- "+p) {
			continue
		}
		if strings.Contains(line, base) && acCreateRe.MatchString(line) {
			return true
		}
	}
	return false
}
