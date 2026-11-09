package knowledge

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func stampedDoc(at, body string) string {
	return "---\ntype: decisions\nstatus: draft\ngenerated:\n  by: scaffold\n  at: \"" + at +
		"\"\n---\n\n" + body
}

// The #2898 shape: a stale, frontmatter-less dir sorts before the freshly
// scaffolded one, and used to shadow it.
func TestFindDecisionsPath_StaleSameNumberedDirDoesNotShadowFresh(t *testing.T) {
	root := t.TempDir()
	writeKnowledgeFixture(t, root, 2893, "auto-regenerate-knowledge", map[string]string{
		"decisions.md": "# Decisions\n\nstale\n",
	})
	writeKnowledgeFixture(t, root, 2893, "test-vscode-unawaited", map[string]string{
		"decisions.md": stampedDoc("2026-11-03T07:15:44Z", "# Decisions\n\nfresh\n"),
	})

	got, err := FindDecisionsPath(root, 2893)
	if err != nil {
		t.Fatalf("FindDecisionsPath: %v", err)
	}
	want := filepath.Join(".nightgauge", "knowledge", "features", "2893-test-vscode-unawaited", "decisions.md")
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}

	text, err := readDecisionsText(2893, root)
	if err != nil {
		t.Fatalf("readDecisionsText: %v", err)
	}
	if !strings.Contains(text, "fresh") {
		t.Errorf("readDecisionsText read the stale file: %q", text)
	}
}

func TestFindDecisionsPath_NewerGeneratedAtWinsOverLaterName(t *testing.T) {
	root := t.TempDir()
	writeKnowledgeFixture(t, root, 7, "aaa-newer", map[string]string{
		"decisions.md": stampedDoc("2026-11-05T00:00:00Z", "new\n"),
	})
	writeKnowledgeFixture(t, root, 7, "zzz-older", map[string]string{
		"decisions.md": stampedDoc("2026-04-01T00:00:00Z", "old\n"),
	})
	got, err := FindDecisionsPath(root, 7)
	if err != nil {
		t.Fatalf("FindDecisionsPath: %v", err)
	}
	if !strings.Contains(got, "7-aaa-newer") {
		t.Errorf("got %q, want the 7-aaa-newer dir", got)
	}
}

func TestResolveIssueDir_MtimeThenNameBreakTies(t *testing.T) {
	root := t.TempDir()
	older := writeKnowledgeFixture(t, root, 5, "b-older", map[string]string{"PRD.md": "x\n"})
	newer := writeKnowledgeFixture(t, root, 5, "a-newer", map[string]string{"PRD.md": "x\n"})
	past := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(filepath.Join(older, "PRD.md"), past, past); err != nil {
		t.Fatal(err)
	}
	cat := filepath.Dir(older)
	got, err := resolveIssueDir(cat, 5, nil)
	if err != nil || got != newer {
		t.Fatalf("mtime tie-break: got %q (%v), want %q", got, err, newer)
	}

	// Equal timestamps: lexically greatest name wins.
	if err := os.Chtimes(filepath.Join(newer, "PRD.md"), past, past); err != nil {
		t.Fatal(err)
	}
	got, err = resolveIssueDir(cat, 5, nil)
	if err != nil || got != older {
		t.Fatalf("name tie-break: got %q (%v), want %q", got, err, older)
	}
}

func TestResolveIssueDir_ExactNumberPrefix(t *testing.T) {
	root := t.TempDir()
	writeKnowledgeFixture(t, root, 28, "other", map[string]string{"PRD.md": "x\n"})
	writeKnowledgeFixture(t, root, 289, "other", map[string]string{"PRD.md": "x\n"})
	cat := filepath.Join(root, ".nightgauge", "knowledge", "features")
	for _, n := range []int{2, 2890} {
		if got, err := resolveIssueDir(cat, n, nil); err != nil || got != "" {
			t.Errorf("issue %d: got %q (%v), want no match", n, got, err)
		}
	}
	if got, _ := resolveIssueDir(cat, 28, nil); filepath.Base(got) != "28-other" {
		t.Errorf("issue 28: got %q", got)
	}
}

func TestResolveIssueDir_MissingCategoryIsNoMatch(t *testing.T) {
	got, err := resolveIssueDir(filepath.Join(t.TempDir(), "absent"), 1, nil)
	if err != nil || got != "" {
		t.Fatalf("got %q (%v), want no match", got, err)
	}
}
