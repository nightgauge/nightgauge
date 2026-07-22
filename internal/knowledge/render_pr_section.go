package knowledge

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"
)

// wellKnownDescriptions maps reserved knowledge filenames to the bullet
// description used in the PR ## Knowledge section. The order of keys here
// also defines the rendering order — well-known entries always render before
// any other files (see RenderPRSection).
var wellKnownDescriptions = []struct {
	filename    string
	label       string
	description string
}{
	{"PRD.md", "PRD", "Product requirements and design decisions"},
	{"decisions.md", "Decisions", "Architecture and implementation decisions"},
	{"outcomes.md", "Outcomes", ""},
}

// excludedScaffoldingFiles are filenames produced by scaffolding helpers
// (`nightgauge knowledge new`) that must never appear in PR bodies.
var excludedScaffoldingFiles = map[string]bool{
	"README.md":    true,
	"_template.md": true,
}

// RenderPRSection builds the ## Knowledge Markdown block for the PR body of
// the given issue. It mirrors the bash dictionary loop previously embedded in
// skills/nightgauge-pr-create/SKILL.md Phase 1.7.
//
// Behavior:
//   - Locates `.nightgauge/knowledge/features/{issueNumber}-*/` (one match expected).
//   - Returns ("", nil) when the directory is missing, has no qualifying entries,
//     or contains only scaffolding files (README.md, _template.md).
//   - Renders well-known entries (PRD.md, decisions.md, outcomes.md) in fixed
//     order, then any remaining .md files in case-insensitive alphabetical order.
//   - Output paths are repo-relative so GitHub renders them as clickable links.
func RenderPRSection(workspaceRoot string, issueNumber int) (string, error) {
	if issueNumber <= 0 {
		return "", fmt.Errorf("issue number must be positive")
	}

	dirPath, err := findIssueKnowledgeDir(workspaceRoot, issueNumber)
	if err != nil {
		return "", err
	}
	if dirPath == "" {
		return "", nil
	}

	entries, err := os.ReadDir(dirPath)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", fmt.Errorf("read knowledge dir: %w", err)
	}

	present := make(map[string]bool, len(entries))
	var others []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(name, ".md") {
			continue
		}
		if excludedScaffoldingFiles[name] {
			continue
		}
		present[name] = true
	}

	var ordered []string
	for _, wk := range wellKnownDescriptions {
		if present[wk.filename] {
			ordered = append(ordered, wk.filename)
			delete(present, wk.filename)
		}
	}
	for name := range present {
		others = append(others, name)
	}
	sort.Slice(others, func(i, j int) bool {
		return strings.ToLower(others[i]) < strings.ToLower(others[j])
	})
	ordered = append(ordered, others...)

	if len(ordered) == 0 {
		return "", nil
	}

	relDir, err := filepath.Rel(workspaceRoot, dirPath)
	if err != nil {
		return "", fmt.Errorf("relativize knowledge dir: %w", err)
	}
	relDir = filepath.ToSlash(relDir)

	var b strings.Builder
	b.WriteString("## Knowledge\n\n")
	for _, name := range ordered {
		b.WriteString(renderBullet(relDir, name))
	}
	return b.String(), nil
}

// findIssueKnowledgeDir locates `.nightgauge/knowledge/features/{N}-*/`
// for the given issue. Returns ("", nil) when no match is found, matching the
// bash no-op semantics.
func findIssueKnowledgeDir(workspaceRoot string, issueNumber int) (string, error) {
	featuresDir := filepath.Join(workspaceRoot, ".nightgauge", "knowledge", "features")
	prefix := fmt.Sprintf("%d-", issueNumber)

	entries, err := os.ReadDir(featuresDir)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", fmt.Errorf("read features dir: %w", err)
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if strings.HasPrefix(e.Name(), prefix) {
			return filepath.Join(featuresDir, e.Name()), nil
		}
	}
	return "", nil
}

// renderBullet emits a single Markdown bullet for the given knowledge entry.
// Mirrors the case-statement in the original bash:
//   - PRD.md       → "- [PRD](path/PRD.md) — Product requirements and design decisions"
//   - decisions.md → "- [Decisions](path/decisions.md) — Architecture and implementation decisions"
//   - other        → "- [Title Case](path/file.md)"
//
// Each bullet is terminated with a single trailing newline.
func renderBullet(relDir, filename string) string {
	for _, wk := range wellKnownDescriptions {
		if wk.filename == filename {
			if wk.description != "" {
				return fmt.Sprintf("- [%s](%s/%s) — %s\n", wk.label, relDir, filename, wk.description)
			}
			return fmt.Sprintf("- [%s](%s/%s)\n", wk.label, relDir, filename)
		}
	}
	label := titleCaseFromFilename(filename)
	return fmt.Sprintf("- [%s](%s/%s)\n", label, relDir, filename)
}

// RenderCoverageMapSection builds the ## PRD Coverage Markdown block from a
// coverage-map-{N}.json file. Returns ("", nil) when path is empty or the file
// does not exist (omit-if-missing semantics).
func RenderCoverageMapSection(coverageMapPath string) (string, error) {
	if coverageMapPath == "" {
		return "", nil
	}
	data, err := os.ReadFile(coverageMapPath)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", fmt.Errorf("read coverage map: %w", err)
	}

	var cm CoverageMap
	if err := json.Unmarshal(data, &cm); err != nil {
		// Warn but don't fail — caller can still emit the rest of the PR body.
		fmt.Fprintf(os.Stderr, "[knowledge] WARNING: invalid coverage-map JSON at %s: %v\n", coverageMapPath, err)
		return "", nil
	}

	covered := 0
	for _, c := range cm.Criteria {
		if c.Status == "covered" {
			covered++
		}
	}
	total := len(cm.Criteria)

	var b strings.Builder
	b.WriteString("## PRD Coverage\n\n")
	if total == 0 {
		b.WriteString("No acceptance criteria recorded.\n")
		return b.String(), nil
	}

	fmt.Fprintf(&b, "Covered: %d / %d acceptance criteria.\n\n", covered, total)
	for _, c := range cm.Criteria {
		if c.Status == "covered" {
			evidence := ""
			if len(c.Evidence) > 0 {
				evidence = " — " + c.Evidence[0]
			}
			fmt.Fprintf(&b, "✅ %s%s\n", c.Text, evidence)
		} else {
			fmt.Fprintf(&b, "❌ %s — no evidence found\n", c.Text)
		}
	}

	if len(cm.Violations) > 0 {
		fmt.Fprintf(&b, "\n⚠️ Constraint violations: %d\n\n", len(cm.Violations))
		for _, v := range cm.Violations {
			fmt.Fprintf(&b, "- **%s**\n", v.Constraint)
			for _, f := range v.ViolatingFiles {
				fmt.Fprintf(&b, "  - Violating file: `%s`\n", f)
			}
		}
	}

	return b.String(), nil
}

// titleCaseFromFilename derives a human-readable label from a knowledge
// filename. It strips the .md extension, replaces `-` and `_` with spaces,
// and uppercases the first letter of each whitespace-separated word —
// matching the awk transform in the original bash:
//
//	awk '{for(i=1;i<=NF;i++) $i=toupper(substr($i,1,1)) substr($i,2)} 1'
func titleCaseFromFilename(filename string) string {
	base := strings.TrimSuffix(filename, ".md")
	base = strings.ReplaceAll(base, "-", " ")
	base = strings.ReplaceAll(base, "_", " ")

	var out strings.Builder
	startOfWord := true
	for _, r := range base {
		if unicode.IsSpace(r) {
			startOfWord = true
			out.WriteRune(r)
			continue
		}
		if startOfWord {
			out.WriteRune(unicode.ToUpper(r))
			startOfWord = false
		} else {
			out.WriteRune(r)
		}
	}
	return out.String()
}
