// Package docs provides deterministic markdown documentation operations. The
// CheckLinksResult JSON schema is stable — field names and types must not
// change after first merge. Skills parse `nightgauge docs check-links
// --json` output; any breaking change requires incrementing the V field.
//
// The check-links verb replaces the bash + grep + dirname link-validation
// chain duplicated across docs-write Phase 7 and update-docs Phase 4.5
// (audit row B6). It is non-fatal by design: missing files become
// findings[]; unreadable files become warnings[]; only hard input errors
// (e.g. unresolvable root) return a non-nil error.
package docs

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

// CheckLinksResult is the stable JSON output schema for `nightgauge docs
// check-links`. Schema version 1 — do not rename or remove fields after
// first merge.
type CheckLinksResult struct {
	V            int       `json:"v"`             // schema version, always 1
	Root         string    `json:"root"`          // absolute path that was scanned
	FilesScanned int       `json:"files_scanned"` // count of markdown files inspected
	LinksTotal   int       `json:"links_total"`   // total relative links examined (broken + resolved)
	LinksBroken  int       `json:"links_broken"`  // count of findings (== len(Findings))
	Findings     []Finding `json:"findings"`      // one entry per broken link
	Warnings     []string  `json:"warnings"`      // non-fatal scan warnings (unreadable files, etc.)
}

// Finding records a single broken relative link discovered in a scanned
// markdown file. Anchors are recorded verbatim but not verified — v1 only
// validates the file part. See ADR-003 in the issue knowledge base.
type Finding struct {
	File     string `json:"file"`     // path relative to Root
	Line     int    `json:"line"`     // 1-based line number where the link appeared
	Link     string `json:"link"`     // raw link text inside the markdown ()
	Resolved string `json:"resolved"` // absolute resolved path (or attempted resolution)
	Anchor   string `json:"anchor"`   // anchor portion after #, "" when absent
	Reason   string `json:"reason"`   // closed enum: file_not_found, outside_root, unreadable
}

// Reason values emitted in Finding.Reason. The enum is closed to keep skills
// parsing simple — additions require bumping V.
const (
	ReasonFileNotFound = "file_not_found"
	ReasonOutsideRoot  = "outside_root"
	ReasonUnreadable   = "unreadable"
)

// CheckLinksOptions controls a single check-links run.
type CheckLinksOptions struct {
	// Root is the directory tree to scan. When empty, the caller's CWD is
	// used. The path is resolved to its absolute form before scanning.
	Root string
	// Target restricts validation to a single markdown file (path relative
	// to Root, or absolute). When empty, the entire Root tree is walked.
	Target string
	// Section restricts validation to links found between a `## Section`
	// (or any heading whose text equals Section, case-insensitive) and the
	// next heading of the same-or-greater level. When empty, all links in
	// each file are validated.
	Section string
	// ExcludeTemplates skips skill and command files that contain template
	// content referencing files the template will create in target repos.
	// When true, paths matching `*/skills/*/SKILL.md` and
	// `*/claude-plugins/*/commands/*` are not scanned.
	ExcludeTemplates bool
}

// linkRe matches Markdown inline links of the form `[text](target)`. The
// alternation under `text` allows balanced single-level brackets inside the
// link label, which appear in real-world docs (e.g. `[[scoped]label](url)`).
// `target` is greedy up to the closing `)` — the same shape used by the
// AWK fence-aware extractor in update-docs Phase 4.5.
var linkRe = regexp.MustCompile(`\[[^\]]*\]\(([^)]+)\)`)

// fenceRe identifies code-fence open/close lines (``` or ~~~ optionally
// followed by an info string). Matching follows the AWK toggle in
// update-docs Phase 4.5 — any line whose first non-whitespace characters
// are three or more backticks (or tildes) toggles the in-fence state.
var fenceRe = regexp.MustCompile("^[ \\t]*(`{3,}|~{3,})")

// headingRe matches ATX headings (`#`, `##`, ...). Setext headings
// (`====` / `----` underlines) are not used by the section filter — the
// existing audit doc and skill files use ATX exclusively.
var headingRe = regexp.MustCompile(`^[ \t]*(#{1,6})[ \t]+(.+?)[ \t]*#*[ \t]*$`)

// skipDirs are directories the walker never descends into. Mirrors the
// implicit skips in update-docs Phase 4.5 plus common build outputs.
var skipDirs = map[string]struct{}{
	"node_modules": {},
	".git":         {},
	"dist":         {},
	"build":        {},
	"coverage":     {},
	".next":        {},
	"out":          {},
}

// Run executes the check-links scan and returns the structured result. The
// function never returns a non-nil error for missing-file findings or
// unreadable files — those are recorded inside Findings/Warnings. err is
// reserved for hard input errors (unresolvable root, target outside root).
func Run(_ context.Context, opts CheckLinksOptions) (*CheckLinksResult, error) {
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
	if info, err := os.Stat(abs); err != nil || !info.IsDir() {
		return nil, fmt.Errorf("root %q is not a readable directory", root)
	}
	root = abs

	result := &CheckLinksResult{
		V:        1,
		Root:     root,
		Findings: []Finding{},
		Warnings: []string{},
	}

	files, err := collectFiles(root, opts.Target, opts.ExcludeTemplates)
	if err != nil {
		return nil, err
	}
	result.FilesScanned = len(files)

	for _, file := range files {
		links, warns, readErr := extractLinks(file, opts.Section)
		if readErr != nil {
			result.Warnings = append(result.Warnings, fmt.Sprintf("%s: %v", relOrAbs(root, file), readErr))
			continue
		}
		for _, w := range warns {
			result.Warnings = append(result.Warnings, fmt.Sprintf("%s: %s", relOrAbs(root, file), w))
		}
		for _, link := range links {
			result.LinksTotal++
			if finding, ok := resolveLink(root, file, link.Raw); ok {
				finding.Line = link.Line
				result.Findings = append(result.Findings, finding)
			}
		}
	}

	result.LinksBroken = len(result.Findings)
	return result, nil
}

// collectFiles returns the list of markdown files to scan. When target is
// non-empty, only that file is returned (after validation). Otherwise the
// root tree is walked, skipping skipDirs and template files when requested.
func collectFiles(root, target string, excludeTemplates bool) ([]string, error) {
	if target != "" {
		path := target
		if !filepath.IsAbs(path) {
			path = filepath.Join(root, target)
		}
		path = filepath.Clean(path)
		// Reject targets that escape the root — skills always pass paths
		// inside root, and an escape almost certainly indicates a bug.
		if !isInside(root, path) {
			return nil, fmt.Errorf("target %q is outside root %q", target, root)
		}
		info, err := os.Stat(path)
		if err != nil {
			return nil, fmt.Errorf("target %q: %w", target, err)
		}
		if info.IsDir() {
			return nil, fmt.Errorf("target %q is a directory; expected a single file", target)
		}
		return []string{path}, nil
	}

	var files []string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			if _, skip := skipDirs[d.Name()]; skip {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.EqualFold(filepath.Ext(d.Name()), ".md") {
			return nil
		}
		if excludeTemplates && isTemplatePath(path) {
			return nil
		}
		files = append(files, path)
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(files)
	return files, nil
}

// isTemplatePath reports whether path matches the skill/command template
// patterns excluded by --exclude-templates. The matcher uses forward-slash
// path semantics so it behaves consistently across platforms.
func isTemplatePath(path string) bool {
	p := filepath.ToSlash(path)
	if strings.Contains(p, "/skills/") && strings.HasSuffix(p, "/SKILL.md") {
		return true
	}
	if strings.Contains(p, "/claude-plugins/") && strings.Contains(p, "/commands/") {
		return true
	}
	return false
}

// extractedLink is a relative link found in a markdown file along with the
// 1-based line number where it appeared.
type extractedLink struct {
	Raw  string
	Line int
}

// extractLinks scans file line-by-line, toggling an in-fence flag on code
// fences (``` or ~~~) and emitting only links found outside fences. When
// section is non-empty, only links within the heading subtree whose title
// equals section (case-insensitive trim) are returned.
func extractLinks(file, section string) ([]extractedLink, []string, error) {
	f, err := os.Open(file)
	if err != nil {
		return nil, nil, err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	// Markdown lines can be very long (tables, embedded JSON examples).
	// Bump the buffer ceiling to 1 MiB to avoid Scanner.Err returning
	// bufio.ErrTooLong on large docs.
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)

	var (
		links    []extractedLink
		warnings []string
		inFence  bool
		lineNum  int
		// Section filter state: when sectionWanted is true and inSection
		// is false, we are skipping lines outside the target section.
		// sectionLevel records the heading level that opened the section
		// so we close it when a heading of the same-or-greater level is
		// encountered.
		sectionWanted = section != ""
		inSection     = !sectionWanted // when no section requested, every line counts
		sectionLevel  int
	)
	wantedSectionTitle := strings.ToLower(strings.TrimSpace(section))

	for scanner.Scan() {
		lineNum++
		line := scanner.Text()

		if fenceRe.MatchString(line) {
			inFence = !inFence
			continue
		}
		if inFence {
			continue
		}

		// Heading detection — outside of fences only, so embedded code-block
		// pseudo-headings cannot trip the section state machine.
		if m := headingRe.FindStringSubmatch(line); m != nil {
			level := len(m[1])
			title := strings.ToLower(strings.TrimSpace(m[2]))
			if sectionWanted {
				if !inSection {
					if title == wantedSectionTitle {
						inSection = true
						sectionLevel = level
					}
					continue
				}
				// Already in section — a same-or-greater-level heading
				// closes the subtree.
				if level <= sectionLevel {
					inSection = false
					// The closing heading itself does not contain
					// validated links; fall through without emitting.
					continue
				}
			}
			continue
		}

		if sectionWanted && !inSection {
			continue
		}

		matches := linkRe.FindAllStringSubmatch(line, -1)
		for _, m := range matches {
			raw := strings.TrimSpace(m[1])
			if raw == "" {
				continue
			}
			if isExternalOrInPage(raw) {
				continue
			}
			links = append(links, extractedLink{Raw: raw, Line: lineNum})
		}
	}

	if err := scanner.Err(); err != nil {
		warnings = append(warnings, fmt.Sprintf("scan: %v", err))
	}

	return links, warnings, nil
}

// isExternalOrInPage reports whether a link target is not a relative
// filesystem reference. Skills exclude the same set from grep patterns —
// matching their behavior preserves the migration as a deterministic
// replacement, not a behavior change.
func isExternalOrInPage(raw string) bool {
	switch {
	case strings.HasPrefix(raw, "http://"),
		strings.HasPrefix(raw, "https://"),
		strings.HasPrefix(raw, "mailto:"),
		strings.HasPrefix(raw, "tel:"),
		strings.HasPrefix(raw, "ftp://"),
		strings.HasPrefix(raw, "//"),
		strings.HasPrefix(raw, "#"):
		return true
	}
	return false
}

// resolveLink validates that link points at an existing file inside root.
// Returns (Finding, true) when the link is broken — otherwise the second
// return value is false.
func resolveLink(root, file, raw string) (Finding, bool) {
	link, anchor := splitAnchor(raw)
	if link == "" {
		// `[text](#anchor)` already filtered by isExternalOrInPage; an
		// empty target after stripping is not a broken link.
		return Finding{}, false
	}

	resolved := filepath.Clean(filepath.Join(filepath.Dir(file), link))

	relFile, _ := filepath.Rel(root, file)

	if !isInside(root, resolved) {
		return Finding{
			File:     filepath.ToSlash(relFile),
			Link:     raw,
			Resolved: resolved,
			Anchor:   anchor,
			Reason:   ReasonOutsideRoot,
		}, true
	}

	if _, err := os.Stat(resolved); err != nil {
		if os.IsNotExist(err) {
			return Finding{
				File:     filepath.ToSlash(relFile),
				Link:     raw,
				Resolved: resolved,
				Anchor:   anchor,
				Reason:   ReasonFileNotFound,
			}, true
		}
		return Finding{
			File:     filepath.ToSlash(relFile),
			Link:     raw,
			Resolved: resolved,
			Anchor:   anchor,
			Reason:   ReasonUnreadable,
		}, true
	}

	return Finding{}, false
}

// splitAnchor splits a link target into (filePart, anchor). The anchor is
// the substring after the first `#`, with the leading `#` stripped.
func splitAnchor(raw string) (string, string) {
	if idx := strings.Index(raw, "#"); idx >= 0 {
		return raw[:idx], raw[idx+1:]
	}
	return raw, ""
}

// isInside reports whether child is within root (or equal to it). Both
// arguments must be absolute / cleaned. The check is prefix-based on the
// cleaned forms with a trailing separator to avoid `/foo` matching
// `/foobar`.
func isInside(root, child string) bool {
	root = filepath.Clean(root)
	child = filepath.Clean(child)
	if child == root {
		return true
	}
	rootSep := root + string(os.PathSeparator)
	return strings.HasPrefix(child, rootSep)
}

// relOrAbs returns the path relative to root when possible, falling back to
// the absolute path. Used for human-readable warnings only.
func relOrAbs(root, path string) string {
	if rel, err := filepath.Rel(root, path); err == nil {
		return filepath.ToSlash(rel)
	}
	return path
}
