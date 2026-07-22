package knowledge

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var wikiLinkRe = regexp.MustCompile(`\[\[([^\]]+)\]\]`)

// WikiLink is a parsed wiki-link extracted from markdown content.
type WikiLink struct {
	// Raw is the content between [[ and ]], e.g. "#2090" or "topic:auth".
	Raw string
	// Match is the full [[...]] match including brackets.
	Match string
	// Index is the byte offset in the source string.
	Index int
}

// ExtractWikiLinks returns all [[wiki-link]] spans from markdown content.
func ExtractWikiLinks(content string) []WikiLink {
	matches := wikiLinkRe.FindAllStringIndex(content, -1)
	links := make([]WikiLink, 0, len(matches))
	for _, loc := range matches {
		full := content[loc[0]:loc[1]]
		// Extract the inner text (strip [[ and ]])
		inner := strings.TrimSpace(full[2 : len(full)-2])
		links = append(links, WikiLink{
			Raw:   inner,
			Match: full,
			Index: loc[0],
		})
	}
	return links
}

// ResolveWikiLinks rewrites [[wiki-links]] in content to standard Markdown links.
// Broken links are kept as-is and appended to the returned warnings slice.
//
// Resolution order:
//  1. [[#NNNN]] / [[#NNNN#anchor]] — scan knowledge/features/ + knowledge/epics/ for {N}-* directory
//  2. [[topic:term]] — try knowledge/glossary/{term}.md; graceful degradation on miss
//  3. [[relative/path]] — resolve relative to fromFile directory, then knowledge root
func ResolveWikiLinks(content, fromFile, workspaceRoot string) (rendered string, warnings []string, err error) {
	rendered = wikiLinkRe.ReplaceAllStringFunc(content, func(match string) string {
		inner := strings.TrimSpace(match[2 : len(match)-2])

		resolved, display, exists, w := resolveWikiLinkGo(inner, fromFile, workspaceRoot)
		if w != "" {
			warnings = append(warnings, w)
		}

		if !exists {
			// Keep the raw wiki-link for broken targets.
			return match
		}

		return fmt.Sprintf("[%s](%s)", display, resolved)
	})
	return rendered, warnings, nil
}

// workspaceNamespaces maps the literal [[ns:slug]] prefix to its target
// subdirectory under <workspaceRoot>/.nightgauge/knowledge/. Extending
// the resolver to a fourth namespace requires a code change — intentional,
// so unknown prefixes fall through to relative-path resolution with actionable
// error messages.
var workspaceNamespaces = map[string]string{
	"product":      "product",
	"cross-repo":   "cross-repo",
	"architecture": "architecture",
}

// resolveWikiLinkGo resolves a single inner wiki-link string (without [[ ]]).
// Returns (resolvedPath, displayText, exists, warningOrEmpty).
func resolveWikiLinkGo(inner, fromFile, workspaceRoot string) (resolvedPath, display string, exists bool, warning string) {
	// 1. Issue-ref: [[#NNNN]] or [[#NNNN#anchor]]
	if strings.HasPrefix(inner, "#") {
		return resolveIssueRefGo(inner, workspaceRoot)
	}

	// 2. Topic-ref: [[topic:term]]
	if strings.HasPrefix(inner, "topic:") {
		return resolveTopicRefGo(inner, workspaceRoot)
	}

	// 3. Workspace-namespace refs: [[product:slug]], [[cross-repo:slug]], [[architecture:slug]]
	for ns, subdir := range workspaceNamespaces {
		if strings.HasPrefix(inner, ns+":") {
			return resolveWorkspaceNamespaceGo(ns, subdir, inner, workspaceRoot)
		}
	}

	// 4. Relative path (cross-repo [[repo:path]] not supported in Go layer — fall through)
	return resolveRelativePathGo(inner, fromFile, workspaceRoot)
}

// resolveIssueRefGo resolves [[#NNNN]] or [[#NNNN#anchor]] by scanning
// knowledge/features/ and knowledge/epics/ for a directory starting with "{N}-".
func resolveIssueRefGo(inner, workspaceRoot string) (resolvedPath, display string, exists bool, warning string) {
	// Strip the leading #.
	ref := inner[1:]

	// Split on # to extract optional anchor.
	anchor := ""
	if idx := strings.Index(ref, "#"); idx >= 0 {
		anchor = ref[idx+1:]
		ref = ref[:idx]
	}

	issueNum := strings.TrimSpace(ref)
	prefix := issueNum + "-"

	knowledgeRoot := filepath.Join(workspaceRoot, ".nightgauge", "knowledge")

	for _, category := range []string{"features", "epics"} {
		categoryDir := filepath.Join(knowledgeRoot, category)
		entries, err := os.ReadDir(categoryDir)
		if err != nil {
			// Directory may not exist — treat as not found.
			continue
		}
		for _, e := range entries {
			if e.IsDir() && strings.HasPrefix(e.Name(), prefix) {
				dirPath := filepath.Join(categoryDir, e.Name())
				relPath, _ := filepath.Rel(workspaceRoot, dirPath)
				if anchor != "" {
					relPath = relPath + "#" + anchor
					display = "#" + issueNum + " § " + anchor
				} else {
					display = "#" + issueNum
				}
				return relPath, display, true, ""
			}
		}
	}

	warning = fmt.Sprintf("wiki-link [[#%s]]: no knowledge directory found for issue %s", issueNum, issueNum)
	return "", "#" + issueNum, false, warning
}

// resolveTopicRefGo resolves [[topic:term]] by looking up knowledge/glossary/{term}.md.
// Gracefully degrades to exists=false if the file is not found.
func resolveTopicRefGo(inner, workspaceRoot string) (resolvedPath, display string, exists bool, warning string) {
	term := strings.TrimPrefix(inner, "topic:")
	glossaryPath := filepath.Join(workspaceRoot, ".nightgauge", "knowledge", "glossary", term+".md")

	_, err := os.Stat(glossaryPath)
	relPath, _ := filepath.Rel(workspaceRoot, glossaryPath)
	display = term

	if err == nil {
		return relPath, display, true, ""
	}

	warning = fmt.Sprintf("wiki-link [[topic:%s]]: glossary file not found: %s", term, relPath)
	return relPath, display, false, warning
}

// resolveWorkspaceNamespaceGo resolves [[product:slug]], [[cross-repo:slug]],
// or [[architecture:slug]] to
// <workspaceRoot>/.nightgauge/knowledge/<subdir>/<slug>.md.
func resolveWorkspaceNamespaceGo(ns, subdir, inner, workspaceRoot string) (resolvedPath, display string, exists bool, warning string) {
	slug := strings.TrimPrefix(inner, ns+":")
	slug = strings.TrimSpace(slug)
	if !strings.HasSuffix(slug, ".md") {
		slug = slug + ".md"
	}
	target := filepath.Join(workspaceRoot, ".nightgauge", "knowledge", subdir, slug)
	rel, _ := filepath.Rel(workspaceRoot, target)
	display = strings.TrimSuffix(filepath.Base(slug), ".md")

	if _, err := os.Stat(target); err == nil {
		return rel, display, true, ""
	}
	warning = fmt.Sprintf("wiki-link [[%s]]: workspace entry not found at %s", inner, rel)
	return rel, display, false, warning
}

// resolveRelativePathGo resolves [[relative/path]] links.
// Tries: (1) relative to fromFile directory, (2) relative to knowledge root.
func resolveRelativePathGo(inner, fromFile, workspaceRoot string) (resolvedPath, display string, exists bool, warning string) {
	withExt := inner
	if !strings.HasSuffix(withExt, ".md") {
		withExt = withExt + ".md"
	}

	fromDir := filepath.Dir(fromFile)
	knowledgeRoot := filepath.Join(workspaceRoot, ".nightgauge", "knowledge")

	// (1) Relative to the file containing the link.
	candidate1 := filepath.Join(fromDir, withExt)
	if _, err := os.Stat(candidate1); err == nil {
		rel, _ := filepath.Rel(workspaceRoot, candidate1)
		base := strings.TrimSuffix(filepath.Base(withExt), ".md")
		return rel, base, true, ""
	}

	// (2) Relative to knowledge root.
	candidate2 := filepath.Join(knowledgeRoot, withExt)
	base := strings.TrimSuffix(filepath.Base(withExt), ".md")
	if _, err := os.Stat(candidate2); err == nil {
		rel, _ := filepath.Rel(workspaceRoot, candidate2)
		return rel, base, true, ""
	}

	warning = fmt.Sprintf("wiki-link [[%s]]: file not found", inner)
	rel, _ := filepath.Rel(workspaceRoot, candidate2)
	return rel, base, false, warning
}
