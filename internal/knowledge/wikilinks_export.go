package knowledge

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/nightgauge/nightgauge/internal/knowledge/okf"
)

// Warning is one unresolvable or rejected wiki-link encountered during an
// export. Warnings are never fatal, and never silent: a link the exporter
// could not resolve degrades to plain text and says so.
type Warning struct {
	// Source is the bundle-relative entry the link appeared in.
	Source string
	// Link is the wiki-link text, without brackets.
	Link string
	// Reason explains why it was not rewritten.
	Reason string
}

func (w Warning) String() string {
	return fmt.Sprintf("%s: [[%s]] — %s", w.Source, w.Link, w.Reason)
}

// codeSpanRe matches fenced blocks and inline code, which are excluded from
// rewriting: an entry that DOCUMENTS the wiki-link syntax must keep its
// examples verbatim, and the shipped architecture seed does exactly that.
var codeSpanRe = regexp.MustCompile("(?s)```.*?```|`[^`\n]*`")

// issueRefEntryPreference orders the files an issue-ref link resolves to.
// A bare [[#1234]] names an issue, and an issue is a directory — but a
// directory renders no edge in any consumer, so the export picks the entry a
// reader following that link actually wants.
var issueRefEntryPreference = []string{"decisions.md", "PRD.md"}

// ResolveToMarkdown rewrites every wiki-link in an entry body to a
// bundle-absolute markdown link, for export to a consumer that has never
// heard of Nightgauge.
//
// It is a SECOND renderer, deliberately not a flag on ResolveWikiLinks:
//
//   - `knowledge render` keeps unresolvable links as literal `[[...]]`, which
//     is right for a human reading the base in place — the brackets are a
//     visible "fix me". An exported bundle must contain no `[[` at all, so
//     here an unresolvable link degrades to its display TEXT.
//   - `knowledge render` emits workspace-relative paths. A bundle consumer
//     has no workspace; it resolves paths against the bundle root.
//
// Repointing the existing renderer at either behaviour would break its
// documented contract, so the two coexist.
//
// fromFile is required: the relative-path form resolves against the directory
// of the file containing the link, and dropping it silently breaks every
// sibling reference.
func ResolveToMarkdown(body, fromFile, workspaceRoot string) (string, []Warning) {
	var warnings []Warning
	bundleRoot := okf.KnowledgeRoot(workspaceRoot)

	source := fromFile
	if rel, err := filepath.Rel(bundleRoot, fromFile); err == nil {
		source = filepath.ToSlash(rel)
	}

	// Protect code spans by replacing them with placeholders that cannot
	// contain a wiki-link, then restoring them afterwards.
	var spans []string
	protected := codeSpanRe.ReplaceAllStringFunc(body, func(m string) string {
		spans = append(spans, m)
		return fmt.Sprintf("\x00CODE%d\x00", len(spans)-1)
	})

	rendered := wikiLinkRe.ReplaceAllStringFunc(protected, func(match string) string {
		inner := strings.TrimSpace(match[2 : len(match)-2])

		target, display, err := resolveBundleLink(inner, fromFile, workspaceRoot, bundleRoot)
		if err != nil {
			warnings = append(warnings, Warning{Source: source, Link: inner, Reason: err.Error()})
			// Plain text, not brackets: the exported bundle must contain no
			// `[[`, and the display text is still the useful part.
			return display
		}
		return fmt.Sprintf("[%s](%s)", display, target)
	})

	for i, span := range spans {
		rendered = strings.Replace(rendered, fmt.Sprintf("\x00CODE%d\x00", i), span, 1)
	}
	return rendered, warnings
}

// resolveBundleLink resolves one wiki-link to a bundle-absolute target.
//
// Every candidate path is containment-checked against the bundle root before
// it is emitted. The existing relative-path resolver joins model-authored link
// text onto a directory with no such check, so `[[../../../../etc/hosts]]`
// renders a working link whenever that file happens to exist — in a bundle
// whose entire purpose is to be handed to someone else.
func resolveBundleLink(inner, fromFile, workspaceRoot, bundleRoot string) (target, display string, err error) {
	resolved, display, exists, _ := resolveWikiLinkGo(inner, fromFile, workspaceRoot)
	if display == "" {
		display = inner
	}

	// [[repo:path]] names a file in a sibling repository. It is outside the
	// bundle by definition, and the Go resolver has never handled it, so it
	// degrades rather than being rewritten into a link that cannot resolve.
	if strings.HasPrefix(inner, "repo:") {
		return "", display, fmt.Errorf("cross-repo reference is outside the bundle")
	}

	if !exists || resolved == "" {
		return "", display, fmt.Errorf("no entry found")
	}

	// Split the anchor before touching the filesystem path.
	pathPart, anchor := resolved, ""
	if i := strings.LastIndex(resolved, "#"); i >= 0 {
		pathPart, anchor = resolved[:i], resolved[i+1:]
	}

	abs := pathPart
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(workspaceRoot, pathPart)
	}

	// An issue-ref resolves to a DIRECTORY. A directory renders no edge, so
	// pick the entry inside it a reader following the link actually wants.
	if info, statErr := os.Stat(abs); statErr == nil && info.IsDir() {
		entry, pickErr := pickIssueEntry(abs)
		if pickErr != nil {
			return "", display, pickErr
		}
		abs = entry
	}

	rel, containErr := okf.ContainedPath(abs, bundleRoot)
	if containErr != nil {
		return "", display, fmt.Errorf("resolves outside the bundle")
	}
	if _, statErr := os.Stat(abs); statErr != nil {
		return "", display, fmt.Errorf("no entry found")
	}

	target = "/" + path.Clean(rel)
	if anchor != "" {
		target += "#" + anchor
	}
	return target, display, nil
}

// pickIssueEntry chooses which file inside an issue directory an issue-ref
// link points at: decisions.md, then PRD.md, then the first non-reserved
// entry alphabetically.
func pickIssueEntry(dir string) (string, error) {
	for _, name := range issueRefEntryPreference {
		p := filepath.Join(dir, name)
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", fmt.Errorf("no entry found")
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") || okf.IsReservedEntry(e.Name()) {
			continue
		}
		names = append(names, e.Name())
	}
	if len(names) == 0 {
		return "", fmt.Errorf("issue directory holds no entries")
	}
	sort.Strings(names)
	return filepath.Join(dir, names[0]), nil
}
