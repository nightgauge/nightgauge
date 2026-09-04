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

// TestIndexEntryTrustTier pins the tier derivation as it reaches .index.json.
// The tree view ranks the unverified backlog on this, so a fixture that
// promotes a stage to human-reviewed would make that list silently wrong.
func TestIndexEntryTrustTier(t *testing.T) {
	root := t.TempDir()
	kb := filepath.Join(root, ".nightgauge", "knowledge", "features")

	fixtures := map[string]struct {
		frontmatter string
		wantTier    string
	}{
		"1-none":    {"---\ntype: decisions\n---", knowledge.TrustUnverified},
		"2-process": {"---\ntype: decisions\nverified:\n  - by: process:retro\n---", knowledge.TrustMachineConfirmed},
		"3-stage":   {"---\ntype: decisions\nverified:\n  - by: feature-dev/claude-sonnet-5\n---", knowledge.TrustMachineConfirmed},
		"4-human":   {"---\ntype: decisions\nverified:\n  - by: human:mark\n---", knowledge.TrustHumanReviewed},
		"5-both":    {"---\ntype: decisions\nverified:\n  - by: process:retro\n  - by: human:mark\n---", knowledge.TrustHumanReviewed},
		// The discriminating case: a substring match on "human" would promote
		// this stage to the top tier.
		"6-humanish": {"---\ntype: decisions\nverified:\n  - by: feature-dev/human-review-model\n---", knowledge.TrustMachineConfirmed},
	}

	for dir, f := range fixtures {
		p := filepath.Join(kb, dir)
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatal(err)
		}
		body := f.frontmatter + "\n\n# Decisions\n\nbody\n"
		if err := os.WriteFile(filepath.Join(p, "decisions.md"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	idx, err := knowledge.BuildMetadataIndex(root)
	if err != nil {
		t.Fatalf("BuildMetadataIndex: %v", err)
	}

	byDir := map[string]knowledge.IndexEntry{}
	for _, e := range idx.Entries {
		byDir[filepath.Base(filepath.Dir(e.Path))] = e
	}
	for dir, f := range fixtures {
		got, ok := byDir[dir]
		if !ok {
			t.Fatalf("index missing %s (entries: %d)", dir, len(idx.Entries))
		}
		if got.TrustTier != f.wantTier {
			t.Errorf("%s trust_tier = %q, want %q", dir, got.TrustTier, f.wantTier)
		}
	}
}

// TestIndexEntryLifecycleFields pins the rest of what .index.json now carries.
func TestIndexEntryLifecycleFields(t *testing.T) {
	root := t.TempDir()
	p := filepath.Join(root, ".nightgauge", "knowledge", "features", "7-widget")
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatal(err)
	}
	body := "---\ntype: decisions\nstatus: deprecated\nstale_after: \"2027-01-01T00:00:00Z\"\ngenerated:\n  by: feature-dev/claude-sonnet-5\n  at: \"2026-09-03T10:00:00Z\"\n---\n\n# Decisions\n\nbody\n"
	if err := os.WriteFile(filepath.Join(p, "decisions.md"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	idx, err := knowledge.BuildMetadataIndex(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(idx.Entries) != 1 {
		t.Fatalf("entries = %d", len(idx.Entries))
	}
	e := idx.Entries[0]
	if e.Status != "deprecated" {
		t.Errorf("status = %q", e.Status)
	}
	if e.StaleAfter != "2027-01-01T00:00:00Z" {
		t.Errorf("stale_after = %q", e.StaleAfter)
	}
	if e.Generated == nil || e.Generated.By != "feature-dev/claude-sonnet-5" {
		t.Errorf("generated = %+v", e.Generated)
	}
}

// TestIndexSkipsReservedFiles pins the one reserved-name definition. Five
// separate `!= "README.md"` filters used to disagree about it, which is why
// _template.md leaked into three scan results as though it were an entry.
func TestIndexSkipsReservedFiles(t *testing.T) {
	root := t.TempDir()
	kb := filepath.Join(root, ".nightgauge", "knowledge")
	dir := filepath.Join(kb, "features", "1-widget")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}

	entry := "---\ntype: decisions\n---\n\n# Decisions\n\nbody\n"
	for _, f := range []string{"decisions.md", "index.md", "log.md", "README.md", "_template.md"} {
		if err := os.WriteFile(filepath.Join(dir, f), []byte(entry), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, f := range []string{"index.md", "log.md", "README.md"} {
		if err := os.WriteFile(filepath.Join(kb, f), []byte(entry), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	idx, err := knowledge.BuildMetadataIndex(root)
	if err != nil {
		t.Fatalf("BuildMetadataIndex: %v", err)
	}

	for _, e := range idx.Entries {
		base := filepath.Base(e.Path)
		if knowledge.IsReservedEntry(base) {
			t.Errorf("reserved file %s was indexed as an entry", e.Path)
		}
	}
	if len(idx.Entries) != 1 {
		t.Errorf("entries = %d, want exactly the one real entry: %+v", len(idx.Entries), idx.Entries)
	}
}
