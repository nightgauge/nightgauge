package recall

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/nightgauge/nightgauge/internal/config"
	"github.com/nightgauge/nightgauge/internal/knowledge"
)

const (
	// pathBoostFactor multiplies BM25 score when a query term appears in doc.Path.
	pathBoostFactor = 1.5

	// tagBoostPerMatch adds 0.5 * baseScore per query term matching a doc tag.
	tagBoostPerMatch = 0.5
)

// Document is an indexed knowledge file.
type Document struct {
	ID           string         // workspace-relative path (same as Path)
	Path         string         // workspace-relative path to .md file
	Kind         string         // "issue" | "repo-topic" | "workspace"
	IssueNumber  int            // 0 when not issue-scoped
	Tags         []string       // from frontmatter
	Repos        []string       // from frontmatter (workspace entries)
	Tokens       []string       // pre-tokenized at index time
	TermFreq     map[string]int // term → count within this doc
	Graduated    bool           // true when <!-- graduated-to: --> marker present
	GraduateDest string         // destination path when Graduated=true
}

// Index is the in-memory BM25 index.
type Index struct {
	Docs      []*Document
	DF        map[string]int // document frequency per term
	AvgDocLen float64
	K1        float64
	B         float64
}

// RecallHit is one ranked result.
type RecallHit struct {
	Rank        int      `json:"rank"`
	Score       float64  `json:"score"`
	Path        string   `json:"path"`
	Kind        string   `json:"kind"`
	IssueNumber int      `json:"issue_number,omitempty"`
	Tags        []string `json:"tags,omitempty"`
	Snippet     string   `json:"snippet"`
	Graduated   bool     `json:"graduated,omitempty"`
}

// RecallResult is the full output of a Query call.
type RecallResult struct {
	QueryID   string      `json:"query_id"`
	Query     string      `json:"query"`
	Hits      []RecallHit `json:"hits"`
	TotalHits int         `json:"total_hits"`
}

var graduatedToRe = regexp.MustCompile(`<!--\s*graduated-to:\s*([^\s>]+)\s*-->`)

// BuildIndex scans all KB scopes and builds an in-memory index.
// It checks the cache first and rebuilds only stale entries.
func BuildIndex(workdir string, scopes []string, cfg *config.KnowledgeConfig) (*Index, error) {
	k1 := cfg.RecallBM25K1()
	b := cfg.RecallBM25B()

	docs, err := loadFromCache(workdir, k1, b)
	if err != nil {
		// Cache miss or corrupt — full scan.
		docs = nil
	}

	if docs == nil {
		docs, err = scanAllDocs(workdir)
		if err != nil {
			return nil, fmt.Errorf("scan knowledge docs: %w", err)
		}
		if saveErr := saveToCache(workdir, docs, k1, b); saveErr != nil {
			// Non-fatal — continue without cache persistence.
			_ = saveErr
		}
	}

	// Filter by scope.
	if len(scopes) > 0 && !allScopes(scopes) {
		docs = filterByScope(docs, scopes)
	}

	return buildIndexFromDocs(docs, k1, b), nil
}

// Query scores all documents against query and returns the top limit hits.
func Query(idx *Index, query string, limit int, scopes []string) (RecallResult, error) {
	if limit <= 0 {
		limit = 10
	}
	queryTerms := TokenizeQuery(query)

	type scored struct {
		doc   *Document
		score float64
	}

	var candidates []scored
	for _, doc := range idx.Docs {
		// Scope filter (secondary guard — primary is in BuildIndex).
		if len(scopes) > 0 && !allScopes(scopes) && !matchesScope(doc, scopes) {
			continue
		}
		s := ScoreDoc(idx, doc, queryTerms, idx.K1, idx.B)
		if s <= 0 {
			continue
		}
		// Path boost: multiply if any query term appears in the doc path.
		pathLower := strings.ToLower(doc.Path)
		for _, qt := range queryTerms {
			if strings.Contains(pathLower, qt) {
				s *= pathBoostFactor
				break
			}
		}
		// Tag boost: +0.5 * base per matching tag.
		for _, qt := range queryTerms {
			for _, tag := range doc.Tags {
				if strings.ToLower(tag) == qt {
					s += tagBoostPerMatch * s
				}
			}
		}
		candidates = append(candidates, scored{doc: doc, score: s})
	}

	// Sort descending by score, tie-break by path (lexicographic).
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].score != candidates[j].score {
			return candidates[i].score > candidates[j].score
		}
		return candidates[i].doc.Path < candidates[j].doc.Path
	})

	// De-duplicate graduated ADRs: graduation target wins over source.
	// Build a set of paths that appear in the top results.
	pathSet := make(map[string]bool, len(candidates))
	for _, c := range candidates {
		pathSet[c.doc.Path] = true
	}

	var hits []RecallHit
	rank := 1
	for _, c := range candidates {
		if rank > limit {
			break
		}
		doc := c.doc
		// Suppress graduated source when the graduation target is in the result set.
		if doc.Graduated && doc.GraduateDest != "" && pathSet[doc.GraduateDest] {
			continue
		}
		hits = append(hits, RecallHit{
			Rank:        rank,
			Score:       math.Round(c.score*1000) / 1000,
			Path:        doc.Path,
			Kind:        doc.Kind,
			IssueNumber: doc.IssueNumber,
			Tags:        doc.Tags,
			Snippet:     extractSnippet(doc.Path, queryTerms),
			Graduated:   doc.Graduated,
		})
		rank++
	}

	totalHits := len(candidates)

	return RecallResult{
		Query:     query,
		Hits:      hits,
		TotalHits: totalHits,
	}, nil
}

// ScoreDoc computes the BM25 score for a single document against queryTerms.
func ScoreDoc(idx *Index, doc *Document, queryTerms []string, k1, b float64) float64 {
	n := float64(len(idx.Docs))
	if n == 0 {
		return 0
	}
	docLen := float64(len(doc.Tokens))
	score := 0.0
	for _, term := range queryTerms {
		tf := float64(doc.TermFreq[term])
		if tf == 0 {
			continue
		}
		df := float64(idx.DF[term])
		if df == 0 {
			continue
		}
		idf := math.Log((n-df+0.5)/(df+0.5) + 1)
		tfNorm := (tf * (k1 + 1)) / (tf + k1*(1-b+b*(docLen/idx.AvgDocLen)))
		score += idf * tfNorm
	}
	return score
}

// buildIndexFromDocs constructs an Index from a slice of documents.
func buildIndexFromDocs(docs []*Document, k1, b float64) *Index {
	idx := &Index{
		Docs: docs,
		DF:   make(map[string]int),
		K1:   k1,
		B:    b,
	}
	totalTokens := 0
	for _, doc := range docs {
		totalTokens += len(doc.Tokens)
		seen := make(map[string]bool)
		for _, t := range doc.Tokens {
			if !seen[t] {
				idx.DF[t]++
				seen[t] = true
			}
		}
	}
	if len(docs) > 0 {
		idx.AvgDocLen = float64(totalTokens) / float64(len(docs))
	}
	return idx
}

// scanAllDocs walks all KB scopes and indexes every .md file.
// The CLI orchestrator handles scope passing; this function indexes everything
// and scope filtering happens at query time.
func scanAllDocs(workdir string) ([]*Document, error) {
	var docs []*Document

	// Local features/ directory.
	featuresDir := filepath.Join(workdir, ".nightgauge", "knowledge", "features")
	localDocs, err := walkKBDir(workdir, featuresDir, "issue")
	if err == nil {
		docs = append(docs, localDocs...)
	}

	// Cross-repo knowledge via workspace config.
	crossEntries, _ := knowledge.ScanCrossRepoKnowledge(workdir, 200)
	for _, entry := range crossEntries {
		absPath := filepath.Join(workdir, entry.Path)
		for _, relEntry := range entry.Entries {
			filePath := filepath.Join(absPath, relEntry)
			doc, err := indexFile(workdir, filePath, "repo-topic", 0)
			if err == nil {
				docs = append(docs, doc)
			}
		}
	}

	// Workspace-level KB (product/, cross-repo/, architecture/).
	wsEntries, _ := knowledge.ScanWorkspaceKB(workdir, 200)
	for _, entry := range wsEntries {
		catDir := filepath.Join(workdir, entry.Path)
		for _, fname := range entry.Entries {
			filePath := filepath.Join(catDir, fname)
			doc, err := indexFile(workdir, filePath, "workspace", 0)
			if err == nil {
				docs = append(docs, doc)
			}
		}
	}

	return docs, nil
}

// walkKBDir indexes all .md files in a knowledge directory tree.
func walkKBDir(workdir, dir, defaultKind string) ([]*Document, error) {
	if _, err := os.Stat(dir); err != nil {
		return nil, nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var docs []*Document
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		// Parse issue number from directory name like "3591-knowledge-recall-api".
		issueNum := 0
		fmt.Sscanf(e.Name(), "%d", &issueNum)

		issueDir := filepath.Join(dir, e.Name())
		mdEntries, _ := os.ReadDir(issueDir)
		for _, mde := range mdEntries {
			if mde.IsDir() || !strings.HasSuffix(mde.Name(), ".md") || mde.Name() == "README.md" {
				continue
			}
			filePath := filepath.Join(issueDir, mde.Name())
			doc, err := indexFile(workdir, filePath, defaultKind, issueNum)
			if err == nil {
				docs = append(docs, doc)
			}
		}
	}
	return docs, nil
}

// indexFile reads a markdown file and produces a Document for the index.
func indexFile(workdir, absPath, kind string, issueNumber int) (*Document, error) {
	data, err := os.ReadFile(absPath)
	if err != nil {
		return nil, err
	}
	content := string(data)
	relPath, _ := filepath.Rel(workdir, absPath)

	// Parse frontmatter for tags and repos.
	var tags, repos []string
	fm, _ := knowledge.ParseFrontmatter(content)
	if fm != nil {
		tags = fm.Tags
		repos = fm.Repos
	}

	// Detect graduation marker.
	graduated := false
	graduateDest := ""
	if m := graduatedToRe.FindStringSubmatch(content); m != nil {
		graduated = true
		graduateDest = m[1]
	}

	tokens := Tokenize(content)
	termFreq := make(map[string]int, len(tokens))
	for _, t := range tokens {
		termFreq[t]++
	}

	return &Document{
		ID:           relPath,
		Path:         relPath,
		Kind:         kind,
		IssueNumber:  issueNumber,
		Tags:         tags,
		Repos:        repos,
		Tokens:       tokens,
		TermFreq:     termFreq,
		Graduated:    graduated,
		GraduateDest: graduateDest,
	}, nil
}

func allScopes(scopes []string) bool {
	scopeSet := map[string]bool{}
	for _, s := range scopes {
		scopeSet[strings.TrimSpace(s)] = true
	}
	return scopeSet["local"] && scopeSet["cross-repo"] && scopeSet["workspace"]
}

func filterByScope(docs []*Document, scopes []string) []*Document {
	scopeSet := map[string]bool{}
	for _, s := range scopes {
		scopeSet[strings.TrimSpace(strings.ToLower(s))] = true
	}
	var out []*Document
	for _, d := range docs {
		if matchesScope(d, scopes) {
			_ = scopeSet
			out = append(out, d)
		}
	}
	return out
}

func matchesScope(doc *Document, scopes []string) bool {
	scopeSet := map[string]bool{}
	for _, s := range scopes {
		scopeSet[strings.TrimSpace(strings.ToLower(s))] = true
	}
	switch doc.Kind {
	case "issue":
		return scopeSet["local"]
	case "repo-topic":
		return scopeSet["cross-repo"]
	case "workspace":
		return scopeSet["workspace"]
	}
	return true
}

// extractSnippet returns a short excerpt from the file containing a query term.
func extractSnippet(relPath string, queryTerms []string) string {
	// Best-effort: snippet is not required for correctness.
	data, err := os.ReadFile(relPath)
	if err != nil {
		return ""
	}
	lines := strings.SplitN(string(data), "\n", 50)
	for _, line := range lines {
		lineLower := strings.ToLower(line)
		for _, qt := range queryTerms {
			if strings.Contains(lineLower, qt) {
				trimmed := strings.TrimSpace(line)
				if len(trimmed) > 120 {
					trimmed = trimmed[:120] + "…"
				}
				return trimmed
			}
		}
	}
	// Fallback: first non-empty non-heading line.
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed != "" && !strings.HasPrefix(trimmed, "#") && !strings.HasPrefix(trimmed, "---") {
			if len(trimmed) > 120 {
				trimmed = trimmed[:120] + "…"
			}
			return trimmed
		}
	}
	return ""
}
