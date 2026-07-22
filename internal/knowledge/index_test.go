package knowledge_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/knowledge"
)

func writeFileForIndex(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func TestBuildMetadataIndex_empty(t *testing.T) {
	tmp := t.TempDir()
	idx, err := knowledge.BuildMetadataIndex(tmp)
	if err != nil {
		t.Fatalf("BuildMetadataIndex: %v", err)
	}
	if idx == nil {
		t.Fatal("expected non-nil index")
	}
	if len(idx.Entries) != 0 {
		t.Fatalf("expected 0 entries, got %d", len(idx.Entries))
	}
	// File should still be written.
	if _, err := os.Stat(filepath.Join(tmp, ".nightgauge", "knowledge", ".index.json")); err != nil {
		t.Fatalf("index file not written: %v", err)
	}
}

func TestBuildMetadataIndex_basic(t *testing.T) {
	tmp := t.TempDir()

	// Three files under features/, two with a wiki-link to the third.
	writeFileForIndex(t, filepath.Join(tmp, ".nightgauge/knowledge/features/100-foo/PRD.md"),
		"# Foo PRD\n\nSee [[#200]] for design.\n")
	writeFileForIndex(t, filepath.Join(tmp, ".nightgauge/knowledge/features/101-bar/PRD.md"),
		"# Bar PRD\n\nDepends on [[#200]].\n")
	writeFileForIndex(t, filepath.Join(tmp, ".nightgauge/knowledge/features/200-shared/PRD.md"),
		"# Shared PRD\n\nThe canonical design.\n")

	idx, err := knowledge.BuildMetadataIndex(tmp)
	if err != nil {
		t.Fatalf("BuildMetadataIndex: %v", err)
	}
	if len(idx.Entries) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(idx.Entries))
	}

	// Locate the shared entry's backlinks.
	var shared *knowledge.IndexEntry
	for i, e := range idx.Entries {
		if strings.Contains(e.Path, "200-shared") {
			shared = &idx.Entries[i]
			break
		}
	}
	if shared == nil {
		t.Fatal("shared entry missing from index")
	}
	if len(shared.Backlinks) != 2 {
		t.Fatalf("expected 2 backlinks to 200-shared, got %d (%v)", len(shared.Backlinks), shared.Backlinks)
	}
	// Backlinks sorted alphabetically — assert deterministic order.
	if shared.Backlinks[0] >= shared.Backlinks[1] {
		t.Errorf("backlinks not sorted: %v", shared.Backlinks)
	}
}

func TestBuildMetadataIndex_titleFallback(t *testing.T) {
	tmp := t.TempDir()
	// No H1 → filename used as title.
	writeFileForIndex(t, filepath.Join(tmp, ".nightgauge/knowledge/features/300-titleless/decisions.md"),
		"Just some prose, no heading.\n")
	idx, err := knowledge.BuildMetadataIndex(tmp)
	if err != nil {
		t.Fatalf("BuildMetadataIndex: %v", err)
	}
	if len(idx.Entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(idx.Entries))
	}
	if idx.Entries[0].Title != "decisions" {
		t.Errorf("expected title 'decisions', got %q", idx.Entries[0].Title)
	}
}

func TestLoadMetadataIndex_missing(t *testing.T) {
	tmp := t.TempDir()
	idx, err := knowledge.LoadMetadataIndex(tmp)
	if err != nil {
		t.Fatalf("LoadMetadataIndex: %v", err)
	}
	if idx != nil {
		t.Fatalf("expected nil index for missing file, got %+v", idx)
	}
}

func TestLoadMetadataIndex_roundtrip(t *testing.T) {
	tmp := t.TempDir()
	writeFileForIndex(t, filepath.Join(tmp, ".nightgauge/knowledge/features/1-roundtrip/PRD.md"),
		"# Roundtrip\n")

	original, err := knowledge.BuildMetadataIndex(tmp)
	if err != nil {
		t.Fatalf("BuildMetadataIndex: %v", err)
	}

	loaded, err := knowledge.LoadMetadataIndex(tmp)
	if err != nil {
		t.Fatalf("LoadMetadataIndex: %v", err)
	}
	if loaded == nil {
		t.Fatal("expected non-nil loaded index")
	}
	if len(loaded.Entries) != len(original.Entries) {
		t.Errorf("entry count mismatch: original=%d loaded=%d",
			len(original.Entries), len(loaded.Entries))
	}
}

func TestLoadMetadataIndex_schemaMismatch(t *testing.T) {
	tmp := t.TempDir()
	indexDir := filepath.Join(tmp, ".nightgauge", "knowledge")
	if err := os.MkdirAll(indexDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// Write an index claiming a future schema version.
	stale := map[string]interface{}{
		"schema_version": 9999,
		"built_at":       "2026-01-01T00:00:00Z",
		"entries":        []interface{}{},
	}
	data, _ := json.Marshal(stale)
	if err := os.WriteFile(filepath.Join(indexDir, ".index.json"), data, 0o644); err != nil {
		t.Fatalf("write stale index: %v", err)
	}

	loaded, err := knowledge.LoadMetadataIndex(tmp)
	if err != nil {
		t.Fatalf("LoadMetadataIndex: %v", err)
	}
	if loaded != nil {
		t.Errorf("expected nil on schema mismatch, got %+v", loaded)
	}
}

func TestBacklinksFor(t *testing.T) {
	idx := &knowledge.MetadataIndex{
		SchemaVersion: 1,
		Entries: []knowledge.IndexEntry{
			{Path: "a.md", Backlinks: []string{"b.md", "c.md"}},
			{Path: "b.md"},
		},
	}
	links := knowledge.BacklinksFor(idx, "a.md")
	if len(links) != 2 {
		t.Fatalf("expected 2 backlinks, got %d", len(links))
	}
	if knowledge.BacklinksFor(idx, "b.md") != nil {
		t.Error("expected nil for entry with no backlinks")
	}
	if knowledge.BacklinksFor(idx, "missing.md") != nil {
		t.Error("expected nil for missing entry")
	}
	if knowledge.BacklinksFor(nil, "a.md") != nil {
		t.Error("expected nil for nil index")
	}
}

func TestFindByTitle(t *testing.T) {
	idx := &knowledge.MetadataIndex{
		SchemaVersion: 1,
		Entries: []knowledge.IndexEntry{
			{Path: "a.md", Title: "Authentication Flow"},
			{Path: "b.md", Title: "Billing Setup"},
			{Path: "c.md", Title: "Auth Migration"},
		},
	}
	hits := knowledge.FindByTitle(idx, "auth")
	if len(hits) != 2 {
		t.Fatalf("expected 2 hits for 'auth', got %d", len(hits))
	}
	if knowledge.FindByTitle(idx, "") != nil {
		t.Error("empty query should return nil")
	}
}

func TestBuildMetadataIndex_atomicWrite(t *testing.T) {
	// After a successful build there must be no leftover .tmp file.
	tmp := t.TempDir()
	writeFileForIndex(t, filepath.Join(tmp, ".nightgauge/knowledge/features/1-a/PRD.md"), "# A\n")
	if _, err := knowledge.BuildMetadataIndex(tmp); err != nil {
		t.Fatalf("BuildMetadataIndex: %v", err)
	}
	if _, err := os.Stat(filepath.Join(tmp, ".nightgauge/knowledge/.index.json.tmp")); !os.IsNotExist(err) {
		t.Errorf("leftover .tmp file: err=%v", err)
	}
}
