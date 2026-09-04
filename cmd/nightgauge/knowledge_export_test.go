package main

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// exportWorkspace builds a small but complete bundle: entries with every
// wiki-link form, navigation files, and derived state that must not be copied.
func exportWorkspace(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	kb := filepath.Join(root, ".nightgauge", "knowledge")

	write := func(rel, content string) {
		p := filepath.Join(kb, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	write("index.md", "---\ntype: index\nokf_version: \"0.2\"\n---\n\n# Features\n\n* [Decisions](/features/1234-widget/decisions.md)\n")
	write("log.md", "---\ntype: log\n---\n\n# Change Log\n")
	write("features/1234-widget/PRD.md", "---\ntype: prd\n---\n\n# PRD: #1234\n")
	write("features/1234-widget/decisions.md",
		"---\ntype: decisions\ntitle: Decisions\n---\n\n# Decisions: #1234\n\n"+
			"Resolved: [[#1234]], [[topic:ring-buffer]], [[architecture:layering]], [[PRD]].\n"+
			"Unresolvable: [[#9999]].\n")
	write("glossary/ring-buffer.md", "---\ntype: glossary\n---\n\n# Ring Buffer\n")
	write("architecture/layering.md", "---\ntype: architecture\n---\n\n# Layering\n")
	write("architecture/_template.md", "# Template\n")
	write(".recall-cache/notes.md", "derived state, not bundle content\n")

	return root
}

func runExport(t *testing.T, args ...string) error {
	t.Helper()
	cmd := knowledgeExportCmd()
	cmd.SetOut(&strings.Builder{})
	cmd.SetErr(&strings.Builder{})
	cmd.SetArgs(args)
	return cmd.Execute()
}

// wikiLinkOutsideCode finds a [[...]] that is NOT inside a code span. A code
// example documenting the syntax is content, not an unresolved link.
var codeSpans = regexp.MustCompile("(?s)```.*?```|`[^`\n]*`")

func hasUnresolvedLink(content string) bool {
	return strings.Contains(codeSpans.ReplaceAllString(content, ""), "[[")
}

func TestKnowledgeExportOKF(t *testing.T) {
	root := exportWorkspace(t)
	out := filepath.Join(t.TempDir(), "bundle")

	if err := runExport(t, out, "--okf", "--workdir", root); err != nil {
		t.Fatalf("export: %v", err)
	}

	var files []string
	if err := filepath.WalkDir(out, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(out, p)
		files = append(files, filepath.ToSlash(rel))
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	// Navigation files come along; derived state does not.
	for _, want := range []string{"index.md", "log.md", "features/1234-widget/decisions.md"} {
		if !exportContains(files, want) {
			t.Errorf("export missing %s (got %v)", want, files)
		}
	}
	for _, unwanted := range files {
		if strings.HasPrefix(unwanted, ".recall-cache/") {
			t.Errorf("export copied derived state: %s", unwanted)
		}
	}

	// Every emitted link target exists in the export, or is an https:// URL.
	linkRe := regexp.MustCompile(`\]\((/[^)#]+)`)
	for _, rel := range files {
		data, err := os.ReadFile(filepath.Join(out, filepath.FromSlash(rel)))
		if err != nil {
			t.Fatal(err)
		}
		content := string(data)
		if hasUnresolvedLink(content) {
			t.Errorf("%s still contains an unresolved wiki-link:\n%s", rel, content)
		}
		for _, m := range linkRe.FindAllStringSubmatch(content, -1) {
			target := filepath.Join(out, filepath.FromSlash(strings.TrimPrefix(m[1], "/")))
			if _, err := os.Stat(target); err != nil {
				t.Errorf("%s links to %s, which is not in the export", rel, m[1])
			}
		}
	}

	// The frontmatter half is byte-identical: the export resolves the body and
	// re-attaches the original block rather than re-rendering it.
	orig, err := os.ReadFile(filepath.Join(root, ".nightgauge", "knowledge", "features", "1234-widget", "decisions.md"))
	if err != nil {
		t.Fatal(err)
	}
	exported, err := os.ReadFile(filepath.Join(out, "features", "1234-widget", "decisions.md"))
	if err != nil {
		t.Fatal(err)
	}
	if frontmatterOf(string(orig)) != frontmatterOf(string(exported)) {
		t.Errorf("frontmatter changed across the export:\n--- orig ---\n%s\n--- exported ---\n%s",
			frontmatterOf(string(orig)), frontmatterOf(string(exported)))
	}
}

func TestKnowledgeExportOKF_RefusesADirectoryInsideTheKnowledgeRoot(t *testing.T) {
	root := exportWorkspace(t)
	inside := filepath.Join(root, ".nightgauge", "knowledge", "out")

	// Exporting a bundle into itself would make the walk consume its own
	// output.
	if err := runExport(t, inside, "--okf", "--workdir", root); err == nil {
		t.Fatal("expected an error for an export directory inside the knowledge root")
	}
	if _, err := os.Stat(inside); err == nil {
		t.Error("a rejected export still created the directory")
	}
}

func TestKnowledgeExportOKF_RequiresTheFormatFlag(t *testing.T) {
	root := exportWorkspace(t)
	if err := runExport(t, filepath.Join(t.TempDir(), "b"), "--workdir", root); err == nil {
		t.Error("expected --okf to be required")
	}
}

func TestKnowledgeExportOKF_NoKnowledgeBase(t *testing.T) {
	if err := runExport(t, filepath.Join(t.TempDir(), "b"), "--okf", "--workdir", t.TempDir()); err == nil {
		t.Error("expected an error when there is no knowledge base")
	}
}

func exportContains(haystack []string, needle string) bool {
	for _, h := range haystack {
		if h == needle {
			return true
		}
	}
	return false
}

func frontmatterOf(content string) string {
	if !strings.HasPrefix(content, "---\n") {
		return ""
	}
	i := strings.Index(content[4:], "\n---")
	if i < 0 {
		return ""
	}
	return content[4 : 4+i]
}
