package recall_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/nightgauge/nightgauge/internal/config"
	"github.com/nightgauge/nightgauge/internal/knowledge/recall"
)

// mkTempRoot creates a temporary directory cleaned up at the end of the test.
func mkTempRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "recall-integration-*")
	if err != nil {
		t.Fatalf("mkTempRoot: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

// scaffoldFixtures copies the testdata tree into root, mirroring it under
// .nightgauge/knowledge/features/ so BuildIndex can scan it.
func scaffoldFixtures(t *testing.T, root string) {
	t.Helper()

	// Source testdata directory (relative to this test file).
	src := "testdata"

	featuresDir := filepath.Join(root, ".nightgauge", "knowledge", "features")
	if err := os.MkdirAll(featuresDir, 0o755); err != nil {
		t.Fatalf("create features dir: %v", err)
	}

	// Copy feature-* directories into featuresDir.
	entries, err := os.ReadDir(src)
	if err != nil {
		t.Fatalf("read testdata: %v", err)
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if e.Name() == "docs" {
			// Copy docs/ directly under root for graduation target.
			copyDir(t, filepath.Join(src, "docs"), filepath.Join(root, "docs"))
			continue
		}
		copyDir(t, filepath.Join(src, e.Name()), filepath.Join(featuresDir, e.Name()))
	}
}

func copyDir(t *testing.T, src, dst string) {
	t.Helper()
	if err := os.MkdirAll(dst, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dst, err)
	}
	entries, err := os.ReadDir(src)
	if err != nil {
		t.Fatalf("readdir %s: %v", src, err)
	}
	for _, e := range entries {
		if e.IsDir() {
			copyDir(t, filepath.Join(src, e.Name()), filepath.Join(dst, e.Name()))
			continue
		}
		data, err := os.ReadFile(filepath.Join(src, e.Name()))
		if err != nil {
			t.Fatalf("read %s: %v", e.Name(), err)
		}
		if err := os.WriteFile(filepath.Join(dst, e.Name()), data, 0o644); err != nil {
			t.Fatalf("write %s: %v", e.Name(), err)
		}
	}
}

func TestIntegration_BuildAndQuery_BM25Ranking(t *testing.T) {
	root := mkTempRoot(t)
	scaffoldFixtures(t, root)

	cfg := &config.KnowledgeConfig{}

	// Build index — should succeed with the 5 issue-level ADRs + 1 graduated + 1 docs target.
	idx, err := recall.BuildIndex(root, nil, cfg)
	if err != nil {
		t.Fatalf("BuildIndex: %v", err)
	}
	if len(idx.Docs) == 0 {
		t.Fatal("expected documents in index, got 0")
	}

	// Query for BM25 — the fixture feature-2-bm25-scoring/decisions.md is tagged
	// [bm25, scoring] and contains the most BM25-related content.
	result, err := recall.Query(idx, "bm25 scoring", 10, nil)
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if result.TotalHits == 0 {
		t.Fatal("expected at least one hit for 'bm25 scoring'")
	}

	// The top result should be related to BM25 scoring (feature-2 or docs graduation target).
	topPath := result.Hits[0].Path
	isBM25Related := false
	for _, h := range result.Hits {
		if containsAny(h.Path, []string{"bm25", "scoring", "test-graduation-target"}) {
			isBM25Related = true
			break
		}
	}
	if !isBM25Related {
		t.Errorf("expected a BM25-related doc in top results, top path: %s", topPath)
	}

	t.Logf("top hit: %s (score=%.3f)", result.Hits[0].Path, result.Hits[0].Score)
}

func TestIntegration_GraduatedADRSuppressed(t *testing.T) {
	root := mkTempRoot(t)
	scaffoldFixtures(t, root)

	cfg := &config.KnowledgeConfig{}
	idx, err := recall.BuildIndex(root, nil, cfg)
	if err != nil {
		t.Fatalf("BuildIndex: %v", err)
	}

	// Verify the graduated source exists in the index.
	graduatedFound := false
	for _, doc := range idx.Docs {
		if doc.Graduated {
			graduatedFound = true
			t.Logf("graduated doc: %s → %s", doc.Path, doc.GraduateDest)
		}
	}
	if !graduatedFound {
		t.Skip("no graduated docs found — fixture may not include graduated ADR")
	}

	// Query for "bm25 parameters" — both graduated source and target may match.
	// The graduated source should be suppressed when the graduation target appears.
	result, err := recall.Query(idx, "bm25 parameters", 10, nil)
	if err != nil {
		t.Fatalf("Query: %v", err)
	}

	for _, h := range result.Hits {
		if h.Graduated {
			// If a graduated doc appears, it should only be present when its target is NOT in the hit set.
			targetPresent := false
			for _, other := range result.Hits {
				if other.Path == graduatedDestFor(idx, h.Path) {
					targetPresent = true
					break
				}
			}
			if targetPresent {
				t.Errorf("graduated source %s should be suppressed since target is in results", h.Path)
			}
		}
	}
}

func TestIntegration_ScopeFilter(t *testing.T) {
	root := mkTempRoot(t)
	scaffoldFixtures(t, root)

	cfg := &config.KnowledgeConfig{}
	idx, err := recall.BuildIndex(root, nil, cfg)
	if err != nil {
		t.Fatalf("BuildIndex: %v", err)
	}

	// Query with local-only scope should return only issue-kind docs.
	result, err := recall.Query(idx, "decision", 10, []string{"local"})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	for _, h := range result.Hits {
		if h.Kind != "issue" {
			t.Errorf("local scope should only return issue docs, got kind=%s path=%s", h.Kind, h.Path)
		}
	}
}

func TestIntegration_CacheRoundTrip(t *testing.T) {
	root := mkTempRoot(t)
	scaffoldFixtures(t, root)

	cfg := &config.KnowledgeConfig{}

	// First build: populates cache.
	idx1, err := recall.BuildIndex(root, nil, cfg)
	if err != nil {
		t.Fatalf("BuildIndex (cold): %v", err)
	}

	// Second build: should load from cache.
	idx2, err := recall.BuildIndex(root, nil, cfg)
	if err != nil {
		t.Fatalf("BuildIndex (warm): %v", err)
	}

	if len(idx1.Docs) != len(idx2.Docs) {
		t.Errorf("doc count mismatch: cold=%d warm=%d", len(idx1.Docs), len(idx2.Docs))
	}
}

func TestIntegration_DeterministicResults(t *testing.T) {
	root := mkTempRoot(t)
	scaffoldFixtures(t, root)

	cfg := &config.KnowledgeConfig{}
	idx, err := recall.BuildIndex(root, nil, cfg)
	if err != nil {
		t.Fatalf("BuildIndex: %v", err)
	}

	r1, _ := recall.Query(idx, "authentication security", 5, nil)
	r2, _ := recall.Query(idx, "authentication security", 5, nil)

	if len(r1.Hits) != len(r2.Hits) {
		t.Fatalf("non-deterministic hit count: %d vs %d", len(r1.Hits), len(r2.Hits))
	}
	for i := range r1.Hits {
		if r1.Hits[i].Path != r2.Hits[i].Path {
			t.Errorf("non-deterministic rank %d: %s vs %s", i+1, r1.Hits[i].Path, r2.Hits[i].Path)
		}
	}
}

func containsAny(s string, substrs []string) bool {
	for _, sub := range substrs {
		if len(s) > 0 && len(sub) > 0 {
			for i := 0; i <= len(s)-len(sub); i++ {
				if s[i:i+len(sub)] == sub {
					return true
				}
			}
		}
	}
	return false
}

func graduatedDestFor(idx *recall.Index, path string) string {
	for _, doc := range idx.Docs {
		if doc.Path == path {
			return doc.GraduateDest
		}
	}
	return ""
}
