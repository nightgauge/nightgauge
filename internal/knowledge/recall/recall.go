package recall

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/nightgauge/nightgauge/internal/config"
	"github.com/nightgauge/nightgauge/internal/knowledge"
	"github.com/nightgauge/nightgauge/internal/knowledge/okf"
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

	// Lifecycle fields, from the frontmatter contract.
	TrustTier string // human-reviewed | machine-confirmed | unverified
	Status    string // draft | stable | deprecated
	// StaleAfter is the RAW stamp, not a precomputed expiry bool. An entry
	// expiring is a clock event with no file change, so a bool frozen at
	// index time would stay false forever for every entry indexed before its
	// stale_after — and the cache is keyed on mtime.
	StaleAfter string
}

// Index is the in-memory BM25 index.
type Index struct {
	// Root is the workspace root every Document.Path is relative to.
	//
	// The index stores WORKSPACE-RELATIVE paths, so anything that reopens a
	// document has to join them against this. extractSnippet did not, and
	// resolved them against the process cwd instead — which for the IPC daemon
	// is not the workspace root. Every snippet then came back empty and the
	// sidebar fell back to the file's basename, rendering rows that read
	// `decisions 0.69` (#1207).
	Root      string
	Docs      []*Document
	DF        map[string]int // document frequency per term
	AvgDocLen float64
	K1        float64
	B         float64

	// Weights are the lifecycle multipliers. A zero value resolves to the
	// built-in defaults, so a bare &Index{} literal still scores correctly.
	Weights config.RecallWeights
	// Now supplies the clock expiry is evaluated against. Nil means time.Now.
	Now func() time.Time
}

// weights returns the index's multipliers, substituting defaults for a
// zero-value set so there is exactly one scoring path.
func (idx *Index) weights() config.RecallWeights {
	if idx.Weights.HumanReviewed == nil {
		var cfg *config.KnowledgeConfig
		return cfg.ResolveRecallWeights()
	}
	return idx.Weights
}

func (idx *Index) now() time.Time {
	if idx.Now == nil {
		return time.Now()
	}
	return idx.Now()
}

// lifecycleMultiplier composes the trust, status and expiry factors for one
// document. Composing multiplicatively rather than picking a single factor is
// what lets "deprecated and expired" rank below either one alone.
func lifecycleMultiplier(doc *Document, w config.RecallWeights, now time.Time) float64 {
	m := *w.Unverified
	switch doc.TrustTier {
	case okf.TrustHumanReviewed:
		m = *w.HumanReviewed
	case okf.TrustMachineConfirmed:
		m = *w.MachineConfirmed
	}

	switch doc.Status {
	case okf.StatusDraft:
		m *= *w.StatusDraft
	case okf.StatusDeprecated:
		m *= *w.StatusDeprecated
	}

	if okf.IsExpiredStamp(doc.StaleAfter, now) {
		m *= *w.Expired
	}
	return m
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

	// Lifecycle fields, so the ranking is explainable rather than magic.
	TrustTier string `json:"trust_tier,omitempty"`
	Status    string `json:"status,omitempty"`
	// Stale and LifecycleMultiplier are emitted unconditionally: a false or
	// 1.0 that vanishes from the JSON is indistinguishable, to a jq consumer,
	// from an old binary that never had the field.
	Stale               bool    `json:"stale"`
	LifecycleMultiplier float64 `json:"lifecycle_multiplier"`
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

	// Enumerate once and hand the same list to both the cache validator and the
	// scanner, so index membership cannot differ between the two paths.
	refs := enumerateDocs(workdir)

	docs, err := loadFromCache(workdir, k1, b, refs)
	if err != nil {
		// Cache miss, corrupt, or stale — full scan.
		docs = nil
	}

	if docs == nil {
		docs = scanRefs(workdir, refs)
		if saveErr := saveToCache(workdir, docs, k1, b); saveErr != nil {
			// Non-fatal — continue without cache persistence.
			_ = saveErr
		}
	}

	// Filter by scope.
	if len(scopes) > 0 && !allScopes(scopes) {
		docs = filterByScope(docs, scopes)
	}

	idx := buildIndexFromDocs(workdir, docs, k1, b)
	idx.Weights = cfg.ResolveRecallWeights()
	return idx, nil
}

// Query scores all documents against query and returns the top limit hits.
func Query(idx *Index, query string, limit int, scopes []string) (RecallResult, error) {
	if limit <= 0 {
		limit = 10
	}
	queryTerms := TokenizeQuery(query)

	type scored struct {
		doc        *Document
		score      float64
		multiplier float64
	}

	weights := idx.weights()
	now := idx.now()

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
		// Lifecycle weighting is applied last, after the path and tag boosts,
		// so the reported Score is the final ranked score and not a number
		// that disagrees with the ordering.
		mult := lifecycleMultiplier(doc, weights, now)
		s *= mult
		candidates = append(candidates, scored{doc: doc, score: s, multiplier: mult})
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
			Snippet:     extractSnippet(idx.Root, doc.Path, queryTerms),
			Graduated:   doc.Graduated,

			TrustTier:           doc.TrustTier,
			Status:              doc.Status,
			Stale:               okf.IsExpiredStamp(doc.StaleAfter, now),
			LifecycleMultiplier: math.Round(c.multiplier*1000) / 1000,
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
func buildIndexFromDocs(root string, docs []*Document, k1, b float64) *Index {
	idx := &Index{
		Root: root,
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

// docRef is one enumerated knowledge document: where it lives on disk and how
// it should be classified. Enumeration is deliberately separate from indexing.
//
// It is the single source of truth for index MEMBERSHIP. scanRefs indexes
// exactly what enumerateDocs returns, and loadFromCache treats any path it
// returns that the cache does not carry as staleness. Two independent walks
// would be free to disagree about which files belong in the index, and the
// disagreement would present as a silent recall miss.
type docRef struct {
	absPath  string
	kind     string
	issueNum int
}

// enumerateDocs lists every knowledge document across all KB scopes WITHOUT
// reading or tokenising any of them. It costs directory reads only, which is
// the part the cache was never saving.
func enumerateDocs(workdir string) []docRef {
	var refs []docRef

	// Local features/ directory.
	featuresDir := filepath.Join(workdir, ".nightgauge", "knowledge", "features")
	refs = append(refs, walkKBPaths(featuresDir, "issue")...)

	// Cross-repo knowledge via workspace config.
	crossEntries, _ := knowledge.ScanCrossRepoKnowledge(workdir, 200)
	for _, entry := range crossEntries {
		absPath := filepath.Join(workdir, entry.Path)
		for _, relEntry := range entry.Entries {
			refs = append(refs, docRef{absPath: filepath.Join(absPath, relEntry), kind: "repo-topic"})
		}
	}

	// Workspace-level KB (product/, cross-repo/, architecture/).
	wsEntries, _ := knowledge.ScanWorkspaceKB(workdir, 200)
	for _, entry := range wsEntries {
		catDir := filepath.Join(workdir, entry.Path)
		for _, fname := range entry.Entries {
			refs = append(refs, docRef{absPath: filepath.Join(catDir, fname), kind: "workspace"})
		}
	}

	return refs
}

// scanRefs reads and indexes every enumerated document. A file that cannot be
// read is skipped, exactly as before.
func scanRefs(workdir string, refs []docRef) []*Document {
	docs := make([]*Document, 0, len(refs))
	for _, r := range refs {
		doc, err := indexFile(workdir, r.absPath, r.kind, r.issueNum)
		if err == nil {
			docs = append(docs, doc)
		}
	}
	return docs
}

// walkKBPaths lists all indexable .md files in a knowledge directory tree.
func walkKBPaths(dir, defaultKind string) []docRef {
	if _, err := os.Stat(dir); err != nil {
		return nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var refs []docRef
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
			refs = append(refs, docRef{
				absPath:  filepath.Join(issueDir, mde.Name()),
				kind:     defaultKind,
				issueNum: issueNum,
			})
		}
	}
	return refs
}

// indexFile reads a markdown file and produces a Document for the index.
func indexFile(workdir, absPath, kind string, issueNumber int) (*Document, error) {
	data, err := os.ReadFile(absPath)
	if err != nil {
		return nil, err
	}
	content := string(data)
	relPath, _ := filepath.Rel(workdir, absPath)

	// Parse frontmatter for tags, repos and the lifecycle fields.
	var tags, repos []string
	trustTier := okf.TrustUnverified
	status := okf.DefaultStatus
	staleAfter := ""
	fm, fmErr := knowledge.ParseFrontmatter(content)
	if fmErr == nil && fm != nil {
		tags = fm.Tags
		repos = fm.Repos
		trustTier = fm.TrustTier()
		status = fm.EffectiveStatus()
		staleAfter = fm.StaleAfter
	}

	// Detect graduation marker.
	graduated := false
	graduateDest := ""
	if m := graduatedToRe.FindStringSubmatch(content); m != nil {
		graduated = true
		graduateDest = m[1]
	}

	// Tokenize the body only. Frontmatter is metadata: indexing it puts an
	// actor like `process:retro` and a PR URL's host into every document
	// retro has ever touched, which flattens IDF for those terms and makes a
	// query for "retro" match the entire corpus. The same reasoning already
	// keeps frontmatter out of the boilerplate check in
	// knowledge.contentIsSubstantive.
	_, body := knowledge.SplitFrontmatter(content)
	tokens := Tokenize(body)
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
		TrustTier:    trustTier,
		Status:       status,
		StaleAfter:   staleAfter,
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
// extractSnippet reopens an indexed document to quote the matching line.
//
// relPath is workspace-relative, so it MUST be joined against the index root.
// Reading it directly resolves against the process cwd, which is the workspace
// root only by coincidence — the IPC daemon runs elsewhere, so every snippet
// came back empty and the sidebar rendered a bare filename and score (#1207).
func extractSnippet(root, relPath string, queryTerms []string) string {
	// Best-effort: snippet is not required for correctness.
	if root != "" && !filepath.IsAbs(relPath) {
		relPath = filepath.Join(root, relPath)
	}
	data, err := os.ReadFile(relPath)
	if err != nil {
		return ""
	}
	// Snippets come from the body for the same reason the index does: a
	// frontmatter line is metadata, and showing `by: process:retro` as the
	// reason a document matched tells the reader nothing.
	_, body := knowledge.SplitFrontmatter(string(data))
	lines := strings.SplitN(body, "\n", 50)
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
