package ipc

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// writeIssueContext drops an issue-{N}.json under root's pipeline dir.
func writeIssueContext(t *testing.T, root string, issueNumber int, body string) {
	t.Helper()
	dir := filepath.Join(root, ".nightgauge", "pipeline")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	path := filepath.Join(dir, fmt.Sprintf("issue-%d.json", issueNumber))
	if err := os.WriteFile(path, []byte(body), 0644); err != nil {
		t.Fatalf("write context: %v", err)
	}
}

func TestLoadIssueClassificationFromRepoRoot(t *testing.T) {
	root := t.TempDir()
	writeIssueContext(t, root, 112, `{
	  "type": "bug",
	  "labels": ["type:bug", "priority:high", "size:M", "area:vscode"]
	}`)

	cls := loadIssueClassification(root, "", 112)

	if cls.Size != "M" {
		t.Errorf("Size = %q, want M", cls.Size)
	}
	if cls.Type != "bug" {
		t.Errorf("Type = %q, want bug", cls.Type)
	}
	if len(cls.Labels) != 4 || cls.Labels[0] != "type:bug" {
		t.Errorf("Labels = %v, want the 4 context labels", cls.Labels)
	}
}

// The extension's stages run in an isolated worktree and write their context
// files there — never into the canonical root — so the worktree layout must be
// searched even when the Go side never learned the worktree path.
func TestLoadIssueClassificationFromExtensionWorktree(t *testing.T) {
	root := t.TempDir()
	worktree := filepath.Join(root, ".worktrees", "issue-112")
	writeIssueContext(t, worktree, 112, `{"type":"feature","labels":["size:L"]}`)

	cls := loadIssueClassification(root, "", 112)

	if cls.Size != "L" {
		t.Errorf("Size = %q, want L", cls.Size)
	}
	if cls.Type != "feature" {
		t.Errorf("Type = %q, want feature", cls.Type)
	}
}

// A runtime-supplied worktree path is the run's real workdir and wins over the
// stale copy that may still sit in the repo root.
func TestLoadIssueClassificationPrefersRuntimeWorktree(t *testing.T) {
	root := t.TempDir()
	worktree := t.TempDir()
	writeIssueContext(t, root, 112, `{"type":"chore","labels":["size:XS"]}`)
	writeIssueContext(t, worktree, 112, `{"type":"feature","labels":["size:XL"]}`)

	cls := loadIssueClassification(root, worktree, 112)

	if cls.Size != "XL" {
		t.Errorf("Size = %q, want XL (worktree copy)", cls.Size)
	}
}

func TestLoadIssueClassificationLabelObjects(t *testing.T) {
	root := t.TempDir()
	writeIssueContext(t, root, 7, `{
	  "labels": [{"name": "type:docs"}, {"name": "size:S"}, 42]
	}`)

	cls := loadIssueClassification(root, "", 7)

	if cls.Size != "S" {
		t.Errorf("Size = %q, want S", cls.Size)
	}
	// type is absent from the file — derived from the labels instead.
	if cls.Type != "docs" {
		t.Errorf("Type = %q, want docs", cls.Type)
	}
	if len(cls.Labels) != 2 {
		t.Errorf("Labels = %v, want the 2 decodable entries", cls.Labels)
	}
}

func TestLoadIssueClassificationMissingOrUnparseable(t *testing.T) {
	root := t.TempDir()

	if cls := loadIssueClassification(root, "", 999); cls.Size != "" || cls.Type != "" || cls.Labels != nil {
		t.Errorf("missing context = %+v, want zero value", cls)
	}

	writeIssueContext(t, root, 5, `{ not json`)
	if cls := loadIssueClassification(root, "", 5); cls.Size != "" {
		t.Errorf("unparseable context = %+v, want zero value", cls)
	}
}

// An issue with no size:* label must leave Size empty rather than defaulting —
// a wrong bucket poisons calibration for every future run of that size.
func TestLoadIssueClassificationNoSizeLabel(t *testing.T) {
	root := t.TempDir()
	writeIssueContext(t, root, 10, `{"type":"bug","labels":["type:bug","area:vscode"]}`)

	cls := loadIssueClassification(root, "", 10)

	if cls.Size != "" {
		t.Errorf("Size = %q, want empty", cls.Size)
	}
	if cls.Type != "bug" {
		t.Errorf("Type = %q, want bug", cls.Type)
	}
}
