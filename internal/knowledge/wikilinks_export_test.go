package knowledge_test

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/knowledge"
)

// exportFixture builds a knowledge base with every wiki-link form present.
func exportFixture(t *testing.T) (workspaceRoot, entryPath string) {
	t.Helper()
	workspaceRoot = t.TempDir()
	kb := filepath.Join(workspaceRoot, ".nightgauge", "knowledge")

	write := func(rel, content string) string {
		p := filepath.Join(kb, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}

	write("features/1234-widget/PRD.md", "---\ntype: prd\n---\n\n# PRD\n")
	write("glossary/ring-buffer.md", "---\ntype: glossary\n---\n\n# Ring Buffer\n")
	write("architecture/layering.md", "---\ntype: architecture\n---\n\n# Layering\n")
	entryPath = write("features/1234-widget/decisions.md", "---\ntype: decisions\n---\n\n# Decisions\n")
	return workspaceRoot, entryPath
}

func TestResolveToMarkdown(t *testing.T) {
	root, from := exportFixture(t)

	cases := []struct {
		name string
		body string
		want string
	}{
		{"issue ref", "See [[#1234]].", "See [#1234](/features/1234-widget/decisions.md)."},
		{"issue ref with anchor", "See [[#1234#adr-001]].", "See [#1234 § adr-001](/features/1234-widget/decisions.md#adr-001)."},
		{"topic ref", "See [[topic:ring-buffer]].", "See [ring-buffer](/glossary/ring-buffer.md)."},
		{"namespace ref", "See [[architecture:layering]].", "See [layering](/architecture/layering.md)."},
		{"sibling relative path", "See [[PRD]].", "See [PRD](/features/1234-widget/PRD.md)."},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, warnings := knowledge.ResolveToMarkdown(tc.body, from, root)
			if got != tc.want {
				t.Errorf("got  %q\nwant %q", got, tc.want)
			}
			if len(warnings) != 0 {
				t.Errorf("unexpected warnings: %+v", warnings)
			}
		})
	}
}

// TestResolveToMarkdown_IssueRefTargetsAFile is the case the spec skipped.
// [[#1234]] names an ISSUE, and an issue is a directory — but a directory
// renders no edge in any consumer, so the export has to pick an entry inside
// it.
func TestResolveToMarkdown_IssueRefTargetsAFile(t *testing.T) {
	root, from := exportFixture(t)

	got, _ := knowledge.ResolveToMarkdown("[[#1234]]", from, root)
	if !strings.HasSuffix(got, ".md)") {
		t.Errorf("issue ref resolved to a directory, which renders no edge: %q", got)
	}
	if !strings.Contains(got, "decisions.md") {
		t.Errorf("issue ref should prefer decisions.md, got %q", got)
	}

	// With no decisions.md, it falls back to PRD.md.
	if err := os.Remove(filepath.Join(root, ".nightgauge", "knowledge", "features", "1234-widget", "decisions.md")); err != nil {
		t.Fatal(err)
	}
	got, _ = knowledge.ResolveToMarkdown("[[#1234]]", from, root)
	if !strings.Contains(got, "PRD.md") {
		t.Errorf("fallback = %q, want PRD.md", got)
	}
}

func TestResolveToMarkdown_UnresolvableDegradesToPlainText(t *testing.T) {
	root, from := exportFixture(t)

	got, warnings := knowledge.ResolveToMarkdown("Broken: [[#9999]].", from, root)
	if strings.Contains(got, "[[") {
		t.Errorf("brackets survived into the export: %q", got)
	}
	if got != "Broken: #9999." {
		t.Errorf("got %q, want the display text kept", got)
	}
	if len(warnings) != 1 {
		t.Fatalf("warnings = %+v, want exactly one — never dropped silently", warnings)
	}
	if !strings.Contains(warnings[0].Source, "decisions.md") {
		t.Errorf("warning does not name the source entry: %+v", warnings[0])
	}
}

// TestResolveToMarkdown_RejectsAResolutionLeavingTheBundle is the containment
// case, and the file outside the bundle REALLY EXISTS — otherwise the case
// passes through the "not found" branch and proves nothing, which is exactly
// the vacuity it is written to avoid.
func TestResolveToMarkdown_RejectsAResolutionLeavingTheBundle(t *testing.T) {
	root, from := exportFixture(t)

	// Four levels up from features/1234-widget/ is the workspace root, which
	// is outside the bundle root at .nightgauge/knowledge.
	outside := filepath.Join(root, "outside-secret.md")
	if err := os.WriteFile(outside, []byte("# Secret\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, warnings := knowledge.ResolveToMarkdown("[[../../../../outside-secret]]", from, root)
	if strings.Contains(got, "outside-secret.md)") {
		t.Fatalf("emitted a link to a file outside the bundle: %q", got)
	}
	if strings.Contains(got, "[[") {
		t.Errorf("brackets survived: %q", got)
	}
	if len(warnings) != 1 {
		t.Fatalf("warnings = %+v, want one", warnings)
	}
	if !strings.Contains(warnings[0].Reason, "outside the bundle") {
		t.Errorf("reason = %q, want it to name the containment failure", warnings[0].Reason)
	}
}

func TestResolveToMarkdown_RejectsASymlinkOutOfTheBundle(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks require elevation on Windows")
	}
	root, from := exportFixture(t)

	outside := filepath.Join(root, "outside-target.md")
	if err := os.WriteFile(outside, []byte("# Secret\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, ".nightgauge", "knowledge", "features", "1234-widget", "looks-local.md")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}

	got, warnings := knowledge.ResolveToMarkdown("[[looks-local]]", from, root)
	if strings.Contains(got, "](/") {
		t.Fatalf("emitted a link through a symlink that leaves the bundle: %q", got)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0].Reason, "outside the bundle") {
		t.Errorf("warnings = %+v", warnings)
	}
}

// TestResolveToMarkdown_LeavesCodeSpansAlone keeps an entry that DOCUMENTS the
// wiki-link syntax intact. The shipped architecture seed does exactly that,
// and rewriting its examples would turn documentation into a lie.
func TestResolveToMarkdown_LeavesCodeSpansAlone(t *testing.T) {
	root, from := exportFixture(t)

	body := "Inline `[[#1234]]` stays.\n\n```markdown\n[[topic:ring-buffer]]\n```\n\nBut [[#1234]] resolves.\n"
	got, _ := knowledge.ResolveToMarkdown(body, from, root)

	if !strings.Contains(got, "`[[#1234]]`") {
		t.Errorf("inline code example was rewritten:\n%s", got)
	}
	if !strings.Contains(got, "```markdown\n[[topic:ring-buffer]]\n```") {
		t.Errorf("fenced code example was rewritten:\n%s", got)
	}
	if !strings.Contains(got, "But [#1234](/features/1234-widget/decisions.md) resolves.") {
		t.Errorf("prose link was not resolved:\n%s", got)
	}
}

// TestResolveToMarkdown_CrossRepoRefDegrades — [[repo:path]] names a file in a
// sibling repository, which is outside the bundle by definition.
func TestResolveToMarkdown_CrossRepoRefDegrades(t *testing.T) {
	root, from := exportFixture(t)

	got, warnings := knowledge.ResolveToMarkdown("[[repo:platform/docs/api.md]]", from, root)
	if strings.Contains(got, "[[") || strings.Contains(got, "](/") {
		t.Errorf("cross-repo ref was rewritten: %q", got)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0].Reason, "outside the bundle") {
		t.Errorf("warnings = %+v", warnings)
	}
}

// TestResolveWikiLinks_Unchanged guards the authoring renderer: the export is
// additive, and `knowledge render` still keeps unresolvable links as literal
// brackets, which is right for a human reading the base in place.
func TestResolveWikiLinks_StillKeepsBrokenBrackets(t *testing.T) {
	root, from := exportFixture(t)

	got, _, err := knowledge.ResolveWikiLinks("Broken: [[#9999]].", from, root)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "[[#9999]]") {
		t.Errorf("the authoring renderer's contract changed: %q", got)
	}
}
