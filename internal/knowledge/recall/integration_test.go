package recall_test

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

// docPaths returns the set of document paths an index carries.
func docPaths(idx *recall.Index) map[string]bool {
	out := make(map[string]bool, len(idx.Docs))
	for _, d := range idx.Docs {
		out[d.Path] = true
	}
	return out
}

// writeKnowledgeDoc scaffolds one knowledge document the way issue pickup does.
func writeKnowledgeDoc(t *testing.T, root, issueDir, name, body string) string {
	t.Helper()
	dir := filepath.Join(root, ".nightgauge", "knowledge", "features", issueDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("create %s: %v", dir, err)
	}
	abs := filepath.Join(dir, name)
	if err := os.WriteFile(abs, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", abs, err)
	}
	rel, err := filepath.Rel(root, abs)
	if err != nil {
		t.Fatalf("rel: %v", err)
	}
	return rel
}

// A warm build must see a document that was added after the cache was written.
//
// The previous version of this test compared cold and warm doc COUNTS, which are
// equal whether or not the cache is consulted at all — it stayed green with
// saveToCache stubbed to `return nil`. It therefore could not observe that the
// cache validated only the entries it already held and never enumerated the
// tree, so an added file was invisible until an already-indexed file changed.
func TestIntegration_CacheSeesAddedDocument(t *testing.T) {
	root := mkTempRoot(t)
	scaffoldFixtures(t, root)

	cfg := &config.KnowledgeConfig{}

	// Cold build: populates the cache with the documents present right now.
	idx1, err := recall.BuildIndex(root, nil, cfg)
	if err != nil {
		t.Fatalf("BuildIndex (cold): %v", err)
	}

	// Issue pickup scaffolds a new knowledge document.
	rel := writeKnowledgeDoc(t, root, "999-late-arrival", "PRD.md",
		"# Late arrival\n\nzarquon vestibule chronosynclastic\n")

	if docPaths(idx1)[rel] {
		t.Fatalf("cold index already contains %s — fixture is not testing an ADDITION", rel)
	}

	// Warm build: the cache is now missing a document that exists on disk.
	idx2, err := recall.BuildIndex(root, nil, cfg)
	if err != nil {
		t.Fatalf("BuildIndex (warm): %v", err)
	}

	if !docPaths(idx2)[rel] {
		t.Errorf("warm build did not index the added document %s\n"+
			"cold=%d docs, warm=%d docs — an added file is invisible until an "+
			"already-indexed file changes", rel, len(idx1.Docs), len(idx2.Docs))
	}

	// And it must be reachable by query, which is what a user actually observes.
	result, err := recall.Query(idx2, "chronosynclastic", 10, nil)
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if result.TotalHits == 0 {
		t.Errorf("recall returned 0 hits for a term unique to the added document; "+
			"exit 0 with no hits is indistinguishable from %q", "no matching knowledge exists")
	}
}

// A cached document that is MODIFIED must be re-indexed. This half already
// worked; it is pinned so the membership check added alongside it cannot
// quietly cost us mtime invalidation later.
func TestIntegration_CacheSeesModifiedDocument(t *testing.T) {
	root := mkTempRoot(t)
	scaffoldFixtures(t, root)

	cfg := &config.KnowledgeConfig{}

	rel := writeKnowledgeDoc(t, root, "500-mutable", "PRD.md", "# Mutable\n\noriginalterm\n")
	if _, err := recall.BuildIndex(root, nil, cfg); err != nil {
		t.Fatalf("BuildIndex (cold): %v", err)
	}

	abs := filepath.Join(root, rel)
	if err := os.WriteFile(abs, []byte("# Mutable\n\nreplacedterm\n"), 0o644); err != nil {
		t.Fatalf("rewrite: %v", err)
	}
	future := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(abs, future, future); err != nil {
		t.Fatalf("chtimes: %v", err)
	}

	idx, err := recall.BuildIndex(root, nil, cfg)
	if err != nil {
		t.Fatalf("BuildIndex (warm): %v", err)
	}
	fresh, err := recall.Query(idx, "replacedterm", 10, nil)
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if fresh.TotalHits == 0 {
		t.Error("warm build served stale content for a modified document")
	}
	stale, err := recall.Query(idx, "originalterm", 10, nil)
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if stale.TotalHits != 0 {
		t.Error("warm build still matches the replaced content")
	}
}

// A cached document that is DELETED must drop out of the index.
func TestIntegration_CacheSeesDeletedDocument(t *testing.T) {
	root := mkTempRoot(t)
	scaffoldFixtures(t, root)

	cfg := &config.KnowledgeConfig{}

	rel := writeKnowledgeDoc(t, root, "501-doomed", "PRD.md", "# Doomed\n\nephemeralterm\n")
	idx1, err := recall.BuildIndex(root, nil, cfg)
	if err != nil {
		t.Fatalf("BuildIndex (cold): %v", err)
	}
	if !docPaths(idx1)[rel] {
		t.Fatalf("cold build did not index %s", rel)
	}

	if err := os.Remove(filepath.Join(root, rel)); err != nil {
		t.Fatalf("remove: %v", err)
	}

	idx2, err := recall.BuildIndex(root, nil, cfg)
	if err != nil {
		t.Fatalf("BuildIndex (warm): %v", err)
	}
	if docPaths(idx2)[rel] {
		t.Errorf("warm build still carries the deleted document %s", rel)
	}
}

// The cache must actually be READ on a warm build. Without this, the addition
// test above would still pass with caching entirely disabled.
func TestIntegration_CacheIsConsulted(t *testing.T) {
	root := mkTempRoot(t)
	scaffoldFixtures(t, root)

	cfg := &config.KnowledgeConfig{}

	if _, err := recall.BuildIndex(root, nil, cfg); err != nil {
		t.Fatalf("BuildIndex (cold): %v", err)
	}

	cachePath := filepath.Join(root, ".nightgauge", "knowledge", ".recall-cache", "index.jsonl")
	before, err := os.ReadFile(cachePath)
	if err != nil {
		t.Fatalf("cold build wrote no cache at %s: %v", cachePath, err)
	}

	// Poison one cached entry with a term that exists in no file on disk. BM25
	// needs the term in BOTH fields: Tokens feeds the document-frequency table
	// (buildIndexFromDocs) and TermFreq feeds tf, so either alone scores zero.
	// A warm build that returns a hit proves the cache read path ran; a full
	// rescan would silently discard the poison and return nothing.
	poisoned := bytes.Replace(before, []byte(`"tokens":["`), []byte(`"tokens":["zzsentinel","`), 1)
	poisoned = bytes.Replace(poisoned, []byte(`"term_freq":{"`), []byte(`"term_freq":{"zzsentinel":5,"`), 1)
	if bytes.Equal(poisoned, before) {
		t.Fatalf("could not poison cache entry — format changed, update this test")
	}
	if err := os.WriteFile(cachePath, poisoned, 0o644); err != nil {
		t.Fatalf("write poisoned cache: %v", err)
	}

	idx, err := recall.BuildIndex(root, nil, cfg)
	if err != nil {
		t.Fatalf("BuildIndex (warm): %v", err)
	}

	result, err := recall.Query(idx, "zzsentinel", 10, nil)
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if result.TotalHits == 0 {
		t.Error("warm build did not serve cached tokens — the cache read path did not run")
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

// --- Snippets survive a cwd that is not the workspace root (#1207) ---
//
// Document.Path is WORKSPACE-RELATIVE, and extractSnippet did os.ReadFile on it
// with no root join, so it resolved against the process cwd. For the IPC daemon
// that is not the workspace root — every snippet came back "", the sidebar fell
// back to path.basename(hit.path), and every Related Decisions row rendered as
// the filename plus a bare score: `decisions 0.69`.
//
// The chdir IS the test. Every existing test in this package runs with the cwd
// at the package directory and would pass against the broken code for a
// workspace that happened to be the cwd.
func TestIntegration_SnippetSurvivesAForeignWorkingDirectory(t *testing.T) {
	root := mkTempRoot(t)
	scaffoldFixtures(t, root)

	cfg := &config.KnowledgeConfig{}
	idx, err := recall.BuildIndex(root, nil, cfg)
	if err != nil {
		t.Fatalf("BuildIndex: %v", err)
	}
	if idx.Root != root {
		t.Fatalf("idx.Root = %q, want the index root %q — without it the "+
			"snippet read has nothing to resolve against", idx.Root, root)
	}

	// Move away from anywhere the relative paths could accidentally resolve.
	t.Chdir(t.TempDir())

	result, err := recall.Query(idx, "bm25 scoring", 10, nil)
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(result.Hits) == 0 {
		t.Fatal("expected at least one hit for 'bm25 scoring'")
	}

	withSnippet := 0
	for _, h := range result.Hits {
		if h.Snippet != "" {
			withSnippet++
		}
	}
	if withSnippet == 0 {
		t.Errorf("every one of %d hits has an empty snippet from a foreign cwd — "+
			"this is #1207: the sidebar then renders `<filename> <score>` with "+
			"nothing explaining either", len(result.Hits))
	}
}

// The snippet must quote the line the query actually matched, not merely be
// non-empty — the fallback branch returns the first non-heading line, which
// would satisfy a non-emptiness check while explaining nothing.
func TestIntegration_SnippetQuotesTheMatchingLine(t *testing.T) {
	root := mkTempRoot(t)
	scaffoldFixtures(t, root)

	cfg := &config.KnowledgeConfig{}
	idx, err := recall.BuildIndex(root, nil, cfg)
	if err != nil {
		t.Fatalf("BuildIndex: %v", err)
	}
	t.Chdir(t.TempDir())

	result, err := recall.Query(idx, "bm25", 10, nil)
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	for _, h := range result.Hits {
		if h.Snippet == "" {
			continue
		}
		if containsAny(strings.ToLower(h.Snippet), []string{"bm25"}) {
			return
		}
	}
	t.Error("no hit quoted a line containing the query term")
}
