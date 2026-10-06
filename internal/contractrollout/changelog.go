package contractrollout

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/template"
)

// ChangelogPath is a target's changelog: the root CHANGELOG.md, under the
// workspace changelog contract (docs/GIT_WORKFLOW.md § Changelog). A
// contract's files and CI job are never visible from the VS Code extension,
// so the core's extension changelog takes no entry from a rollout.
const ChangelogPath = "CHANGELOG.md"

// changelogSections are Keep a Changelog's sections, in its order.
var changelogSections = []string{"Added", "Changed", "Deprecated", "Removed", "Fixed", "Security"}

// ChangelogEntry is the entry a contract adds under `## [Unreleased]` in
// every target's changelog (#2433).
type ChangelogEntry struct {
	// Section is the Keep a Changelog section the entry goes under.
	Section string `yaml:"section"`
	// Entry is the entry's prose, a text/template executed with BodyData.
	// The rollout makes it one list item: a "- " on its first line and two
	// spaces before each other line.
	Entry string `yaml:"entry"`
}

// ChangelogResult is what adding the entry to one target did.
type ChangelogResult struct {
	Path string `json:"path"`
	// Action is added (or, planning, would be added), present (the
	// [Unreleased] section already holds the entry), or absent (the
	// repository has no changelog, so it takes no entry).
	Action string `json:"action"`
	// Section is the subsection the entry went under.
	Section string `json:"section,omitempty"`
}

func validChangelogSection(s string) bool {
	for _, v := range changelogSections {
		if s == v {
			return true
		}
	}
	return false
}

func sectionRank(s string) int {
	for i, v := range changelogSections {
		if s == v {
			return i
		}
	}
	return -1
}

// RenderChangelogEntry executes the entry template for repo and returns the
// entry as list-item lines.
func (c *Contract) RenderChangelogEntry(repo string) ([]string, error) {
	if c.Changelog == nil {
		return nil, fmt.Errorf("the contract declares no changelog entry")
	}
	tmpl, err := template.New("entry").Option("missingkey=error").Parse(c.Changelog.Entry)
	if err != nil {
		return nil, fmt.Errorf("changelog.entry: %w", err)
	}
	var b strings.Builder
	if err := tmpl.Execute(&b, BodyData{Contract: c.Name, Repo: repo}); err != nil {
		return nil, fmt.Errorf("changelog.entry: %w", err)
	}
	return formatEntry(b.String())
}

// formatEntry checks entry prose and makes it one Markdown list item. Prose
// that would change the file's structure is refused: a heading line (the
// shape check-changelog.sh rejects), a blank line, a list marker, or a
// control character.
func formatEntry(text string) ([]string, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil, fmt.Errorf("changelog.entry is empty")
	}
	var out []string
	for i, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case line == "":
			return nil, fmt.Errorf("changelog.entry holds a blank line; an entry is one list item")
		case strings.IndexFunc(line, isControl) >= 0:
			return nil, fmt.Errorf("changelog.entry holds a control character")
		case strings.HasPrefix(line, "#"):
			return nil, fmt.Errorf("changelog.entry line %q is a heading", line)
		case isListMarker(line):
			return nil, fmt.Errorf("changelog.entry line %q starts a list item; the rollout adds the one bullet itself", line)
		}
		if i == 0 {
			out = append(out, "- "+line)
		} else {
			out = append(out, "  "+line)
		}
	}
	return out, nil
}

func isListMarker(line string) bool {
	for _, m := range []string{"- ", "* ", "+ "} {
		if strings.HasPrefix(line, m) || line == strings.TrimSpace(m) {
			return true
		}
	}
	i := 0
	for i < len(line) && line[i] >= '0' && line[i] <= '9' {
		i++
	}
	return i > 0 && i < len(line) && (line[i] == '.' || line[i] == ')') && (i+1 == len(line) || line[i+1] == ' ')
}

// AddChangelogEntry adds entry (list-item lines from RenderChangelogEntry)
// as the first item of `### <section>` under root's single
// `## [Unreleased]` heading, creating the subsection in Keep a Changelog
// order when it is missing and following the existing list's tight or loose
// spacing. A repository with no changelog takes no entry; one whose
// changelog does not have exactly one `## [Unreleased]` heading is refused,
// since its own changelog gate would refuse it too. With write false nothing
// is written and the result says what writing would do.
func AddChangelogEntry(root, section string, entry []string, write bool) (ChangelogResult, error) {
	res := ChangelogResult{Path: ChangelogPath, Section: section}
	if !validChangelogSection(section) {
		return res, fmt.Errorf("changelog: section %q is not one of %s", section, strings.Join(changelogSections, ", "))
	}
	if len(entry) == 0 {
		return res, fmt.Errorf("changelog: an empty entry")
	}
	if err := refuseSymlinkPath(root, ChangelogPath); err != nil {
		return res, err
	}
	file := filepath.Join(root, ChangelogPath)
	data, err := os.ReadFile(file)
	if os.IsNotExist(err) {
		res.Action = "absent"
		return res, nil
	}
	if err != nil {
		return res, err
	}
	info, err := os.Lstat(file)
	if err != nil {
		return res, err
	}
	if !info.Mode().IsRegular() {
		return res, fmt.Errorf("%s is not a regular file", ChangelogPath)
	}

	lines := strings.Split(string(data), "\n")
	start := -1
	count := 0
	for i, l := range lines {
		if strings.TrimRight(l, " \t") == "## [Unreleased]" {
			start, count = i, count+1
		}
	}
	if count != 1 {
		return res, fmt.Errorf("%s has %d `## [Unreleased]` headings, want exactly one", ChangelogPath, count)
	}
	end := len(lines)
	for i := start + 1; i < len(lines); i++ {
		if strings.HasPrefix(lines[i], "## ") {
			end = i
			break
		}
	}
	// An entry the section already holds is not added twice.
	body := "\n" + strings.Join(lines[start+1:end], "\n") + "\n"
	if strings.Contains(body, "\n"+strings.Join(entry, "\n")+"\n") {
		res.Action = "present"
		return res, nil
	}

	heading := "### " + section
	var insert []string
	at := -1
	for i := start + 1; i < end; i++ {
		if strings.TrimRight(lines[i], " \t") == heading {
			at = i
			break
		}
	}
	if at >= 0 {
		// Insert before the subsection's first item, keeping its spacing.
		first := at + 1
		for first < end && strings.TrimSpace(lines[first]) == "" {
			first++
		}
		insert = append(insert, entry...)
		if first == end || strings.HasPrefix(lines[first], "#") || looseList(lines[first:end]) || !isListMarker(lines[first]) {
			insert = append(insert, "")
		}
		if first == at+1 {
			// No blank line separated the heading from what follows.
			insert = append([]string{""}, insert...)
		}
		at = first
	} else {
		// Create the subsection before the first one that sorts after it,
		// else after the section's last non-blank line.
		rank := sectionRank(section)
		for i := start + 1; i < end; i++ {
			if strings.HasPrefix(lines[i], "### ") && sectionRank(strings.TrimSpace(lines[i][4:])) > rank {
				at = i
				break
			}
		}
		if at >= 0 {
			insert = append(append([]string{heading, ""}, entry...), "")
			if strings.TrimSpace(lines[at-1]) != "" {
				insert = append([]string{""}, insert...)
			}
		} else {
			last := end - 1
			for last > start && strings.TrimSpace(lines[last]) == "" {
				last--
			}
			at = last + 1
			insert = append([]string{"", heading, ""}, entry...)
			if at == end && end < len(lines) {
				insert = append(insert, "")
			}
		}
	}

	res.Action = "added"
	if !write {
		return res, nil
	}
	out := make([]string, 0, len(lines)+len(insert))
	out = append(out, lines[:at]...)
	out = append(out, insert...)
	out = append(out, lines[at:]...)
	return res, writeFileAtomic(file, []byte(strings.Join(out, "\n")), info.Mode().Perm())
}

// looseList reports whether the list that starts lines separates its items
// with blank lines, so a new item follows the same spacing.
func looseList(lines []string) bool {
	blank := false
	for _, l := range lines {
		switch {
		case strings.HasPrefix(l, "#"):
			return false
		case strings.TrimSpace(l) == "":
			blank = true
		case blank && isListMarker(l):
			return true
		case blank:
			return false
		}
	}
	return false
}
