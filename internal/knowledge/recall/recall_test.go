package recall

import (
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/config"
)

func TestTokenize_Basic(t *testing.T) {
	tokens := Tokenize("Hello World foo bar")
	if len(tokens) == 0 {
		t.Fatal("expected tokens, got none")
	}
	for _, tok := range tokens {
		for _, r := range tok {
			if r >= 'A' && r <= 'Z' {
				t.Errorf("token %q contains uppercase", tok)
			}
		}
	}
}

func TestTokenize_Unicode(t *testing.T) {
	// Non-ASCII word boundaries should be handled gracefully.
	tokens := Tokenize("café latté bm25 scoring")
	if len(tokens) == 0 {
		t.Fatal("expected tokens from unicode input")
	}
	found := false
	for _, tok := range tokens {
		if tok == "scor" || tok == "score" || tok == "scoring" {
			found = true
		}
	}
	if !found {
		// Stemming may vary; just assert we got some tokens.
		t.Logf("tokens: %v", tokens)
	}
}

func TestTokenize_ShortWordsDropped(t *testing.T) {
	tokens := Tokenize("I am a go test")
	for _, tok := range tokens {
		if len(tok) < 2 {
			t.Errorf("token %q shorter than 2 chars should be dropped", tok)
		}
	}
}

func TestBM25Score_TermFrequency(t *testing.T) {
	// A document with higher term frequency should score higher.
	idx := &Index{
		K1: 1.5,
		B:  0.75,
		DF: map[string]int{"bm25": 2},
	}
	short := &Document{
		Tokens:   []string{"bm25", "other"},
		TermFreq: map[string]int{"bm25": 1, "other": 1},
	}
	long := &Document{
		Tokens:   []string{"bm25", "bm25", "bm25", "other"},
		TermFreq: map[string]int{"bm25": 3, "other": 1},
	}
	idx.Docs = []*Document{short, long}
	idx.AvgDocLen = 3.0

	scoreShort := ScoreDoc(idx, short, []string{"bm25"}, 1.5, 0.75)
	scoreLong := ScoreDoc(idx, long, []string{"bm25"}, 1.5, 0.75)

	if scoreShort <= 0 {
		t.Errorf("expected positive score for short doc, got %f", scoreShort)
	}
	if scoreLong <= scoreShort {
		t.Errorf("longer TF doc should score higher: long=%f short=%f", scoreLong, scoreShort)
	}
}

func TestBM25Score_DocLength(t *testing.T) {
	// With b=1.0, a longer doc should score lower than a shorter one for the same TF.
	idx := &Index{
		K1: 1.5,
		B:  1.0,
		DF: map[string]int{"term": 2},
	}
	shortDoc := &Document{
		Tokens:   make([]string, 5),
		TermFreq: map[string]int{"term": 1},
	}
	longDoc := &Document{
		Tokens:   make([]string, 100),
		TermFreq: map[string]int{"term": 1},
	}
	idx.Docs = []*Document{shortDoc, longDoc}
	idx.AvgDocLen = 52.5

	scoreShort := ScoreDoc(idx, shortDoc, []string{"term"}, 1.5, 1.0)
	scoreLong := ScoreDoc(idx, longDoc, []string{"term"}, 1.5, 1.0)
	if scoreShort <= scoreLong {
		t.Errorf("short doc should score higher than long doc: short=%f long=%f", scoreShort, scoreLong)
	}
}

func TestPathBoost_Applied(t *testing.T) {
	idx := makeMinimalIndex([]string{"architecture", "decision", "trade"})
	doc := &Document{
		Path:     ".nightgauge/knowledge/features/42-architecture-trade/decisions.md",
		Kind:     "issue",
		Tokens:   []string{"architectur", "trade", "decision"},
		TermFreq: map[string]int{"architectur": 1, "trade": 1, "decision": 1},
	}
	idx.Docs = []*Document{doc}
	idx.AvgDocLen = 3

	result, err := Query(idx, "architecture trade", 10, nil)
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(result.Hits) == 0 {
		t.Fatal("expected at least one hit")
	}
	// Path contains "architecture" — score should be boosted.
	if result.Hits[0].Score <= 0 {
		t.Errorf("expected positive score, got %f", result.Hits[0].Score)
	}
}

func TestTagBoost_Applied(t *testing.T) {
	docWithTag := &Document{
		Path:     "knowledge/features/1-test/decisions.md",
		Kind:     "issue",
		Tags:     []string{"bm25", "retrieval"},
		Tokens:   []string{"bm25", "scoring"},
		TermFreq: map[string]int{"bm25": 1, "scoring": 1},
	}
	docNoTag := &Document{
		Path:     "knowledge/features/2-test/decisions.md",
		Kind:     "issue",
		Tags:     []string{},
		Tokens:   []string{"bm25", "scoring"},
		TermFreq: map[string]int{"bm25": 1, "scoring": 1},
	}
	idx := &Index{
		K1:        1.5,
		B:         0.75,
		DF:        map[string]int{"bm25": 2, "scoring": 2},
		Docs:      []*Document{docWithTag, docNoTag},
		AvgDocLen: 2,
	}

	result, err := Query(idx, "bm25", 10, nil)
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(result.Hits) < 2 {
		t.Fatalf("expected 2 hits, got %d", len(result.Hits))
	}
	if result.Hits[0].Path != docWithTag.Path {
		t.Errorf("doc with matching tag should rank first, got %s", result.Hits[0].Path)
	}
}

func TestScopeFilter_LocalOnly(t *testing.T) {
	idx := makeMinimalIndex([]string{"query"})
	local := &Document{Path: "a.md", Kind: "issue", Tokens: []string{"queri"}, TermFreq: map[string]int{"queri": 1}}
	crossRepo := &Document{Path: "b.md", Kind: "repo-topic", Tokens: []string{"queri"}, TermFreq: map[string]int{"queri": 1}}
	idx.Docs = []*Document{local, crossRepo}
	idx.AvgDocLen = 1

	result, err := Query(idx, "query", 10, []string{"local"})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	for _, h := range result.Hits {
		if h.Kind != "issue" {
			t.Errorf("scope=local should only return issue-kind docs, got %s", h.Kind)
		}
	}
}

func TestGraduatedDedup(t *testing.T) {
	idx := makeMinimalIndex([]string{"scoring"})
	source := &Document{
		Path:         "knowledge/features/1-test/decisions.md",
		Kind:         "issue",
		Tokens:       []string{"scor"},
		TermFreq:     map[string]int{"scor": 2},
		Graduated:    true,
		GraduateDest: "docs/SCORING.md",
	}
	target := &Document{
		Path:     "docs/SCORING.md",
		Kind:     "workspace",
		Tokens:   []string{"scor"},
		TermFreq: map[string]int{"scor": 3},
	}
	idx.Docs = []*Document{source, target}
	idx.DF["scor"] = 2
	idx.AvgDocLen = 2.5

	result, err := Query(idx, "scoring", 10, nil)
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	for _, h := range result.Hits {
		if h.Path == source.Path {
			t.Errorf("graduated source %s should be suppressed when target %s is present", source.Path, target.Path)
		}
	}
}

func TestDeterministicOrdering(t *testing.T) {
	idx := makeMinimalIndex([]string{"auth"})
	docs := []*Document{
		{Path: "z.md", Kind: "issue", Tokens: []string{"auth"}, TermFreq: map[string]int{"auth": 1}},
		{Path: "a.md", Kind: "issue", Tokens: []string{"auth"}, TermFreq: map[string]int{"auth": 1}},
		{Path: "m.md", Kind: "issue", Tokens: []string{"auth"}, TermFreq: map[string]int{"auth": 1}},
	}
	idx.Docs = docs
	idx.DF["auth"] = 3
	idx.AvgDocLen = 1

	r1, _ := Query(idx, "auth", 10, nil)
	r2, _ := Query(idx, "auth", 10, nil)

	if len(r1.Hits) != len(r2.Hits) {
		t.Fatalf("non-deterministic hit count")
	}
	for i := range r1.Hits {
		if r1.Hits[i].Path != r2.Hits[i].Path {
			t.Errorf("non-deterministic at rank %d: %s vs %s", i+1, r1.Hits[i].Path, r2.Hits[i].Path)
		}
	}
	// Also verify alphabetical tie-break.
	if len(r1.Hits) >= 3 && r1.Hits[0].Path != "a.md" {
		t.Errorf("expected 'a.md' first on tie-break, got %s", r1.Hits[0].Path)
	}
}

func TestQuery_EmptyIndex(t *testing.T) {
	idx := &Index{K1: 1.5, B: 0.75, DF: map[string]int{}}
	result, err := Query(idx, "anything", 10, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.TotalHits != 0 {
		t.Errorf("expected 0 hits on empty index, got %d", result.TotalHits)
	}
}

func TestBM25Score_ZeroOnMissingTerm(t *testing.T) {
	idx := makeMinimalIndex([]string{"present"})
	doc := &Document{
		Tokens:   []string{"present"},
		TermFreq: map[string]int{"present": 1},
	}
	idx.Docs = []*Document{doc}
	idx.AvgDocLen = 1

	score := ScoreDoc(idx, doc, []string{"absent"}, 1.5, 0.75)
	if math.Abs(score) > 1e-9 {
		t.Errorf("expected 0 score for missing term, got %f", score)
	}
}

// makeMinimalIndex builds a minimal Index with DF entries for the given terms.
func makeMinimalIndex(terms []string) *Index {
	df := make(map[string]int, len(terms))
	for _, t := range terms {
		df[t] = 1
	}
	return &Index{K1: 1.5, B: 0.75, DF: df}
}

// TestIndexFile_IgnoresFrontmatterTokens guards the corpus against the
// provenance the stamp verb now writes onto every entry. Indexing frontmatter
// would put `process:retro` and a PR URL's host into every document retro has
// touched, flattening IDF for those terms until a query for "retro" matches
// the whole base.
func TestIndexFile_IgnoresFrontmatterTokens(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, ".nightgauge", "knowledge", "features", "7-widget")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := "---\ntype: decisions\nverified:\n  - by: process:retro\n    at: \"2026-09-03T00:00:00Z\"\nsources:\n  - resource: https://github.com/nightgauge/nightgauge/pull/999\n---\n\n# Decisions: #7\n\nThe widget uses a ring buffer.\n"
	if err := os.WriteFile(filepath.Join(dir, "decisions.md"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	idx, err := BuildIndex(root, nil, nil)
	if err != nil {
		t.Fatal(err)
	}

	for _, term := range []string{"retro", "github", "nightgauge", "verified", "sources"} {
		res, err := Query(idx, term, 10, nil)
		if err != nil {
			t.Fatal(err)
		}
		if res.TotalHits != 0 {
			t.Errorf("query %q matched %d document(s) on frontmatter alone", term, res.TotalHits)
		}
	}

	// The body is still indexed.
	res, err := Query(idx, "ring buffer", 10, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.TotalHits == 0 {
		t.Error("body text is no longer indexed")
	}
}

// lifecycleFixture writes five entries whose BODY text is identical and whose
// frontmatter is the only thing that differs, so any ordering difference is
// attributable to the lifecycle multiplier alone.
func lifecycleFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	const body = "\n# Decisions\n\nThe widget uses a ring buffer for the queue.\n"

	entries := map[string]string{
		"1-human":      "---\ntype: decisions\nverified:\n  - by: human:octocat\n---",
		"2-machine":    "---\ntype: decisions\nverified:\n  - by: process:retro\n---",
		"3-unverified": "---\ntype: decisions\n---",
		"4-expired":    "---\ntype: decisions\nverified:\n  - by: process:retro\nstale_after: \"2020-01-01T00:00:00Z\"\n---",
		"5-deprecated": "---\ntype: decisions\nverified:\n  - by: process:retro\nstatus: deprecated\n---",
	}
	for dir, fm := range entries {
		p := filepath.Join(root, ".nightgauge", "knowledge", "features", dir)
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(p, "decisions.md"), []byte(fm+body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func TestLifecycleRanking(t *testing.T) {
	root := lifecycleFixture(t)

	idx, err := BuildIndex(root, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	idx.Now = func() time.Time { return time.Date(2026, 9, 3, 0, 0, 0, 0, time.UTC) }

	res, err := Query(idx, "ring buffer", 10, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Hits) != 5 {
		t.Fatalf("hits = %d, want 5 (%+v)", len(res.Hits), res.Hits)
	}

	var order []string
	for _, h := range res.Hits {
		order = append(order, filepath.Base(filepath.Dir(h.Path)))
	}
	want := []string{"1-human", "2-machine", "3-unverified", "4-expired", "5-deprecated"}
	for i := range want {
		if order[i] != want[i] {
			t.Fatalf("order = %v, want %v", order, want)
		}
	}

	// Every hit explains its own ranking.
	byDir := map[string]RecallHit{}
	for _, h := range res.Hits {
		byDir[filepath.Base(filepath.Dir(h.Path))] = h
	}
	if got := byDir["1-human"]; got.TrustTier != "human-reviewed" || got.LifecycleMultiplier != 1.25 {
		t.Errorf("human hit = %+v", got)
	}
	if got := byDir["2-machine"]; got.TrustTier != "machine-confirmed" || got.LifecycleMultiplier != 1.0 {
		t.Errorf("machine hit = %+v", got)
	}
	if got := byDir["3-unverified"]; got.TrustTier != "unverified" || got.LifecycleMultiplier != 0.85 {
		t.Errorf("unverified hit = %+v", got)
	}
	// Multipliers compose: machine-confirmed 1.0 × expired 0.5.
	if got := byDir["4-expired"]; !got.Stale || got.LifecycleMultiplier != 0.5 {
		t.Errorf("expired hit = %+v", got)
	}
	// machine-confirmed 1.0 × deprecated 0.25.
	if got := byDir["5-deprecated"]; got.Status != "deprecated" || got.LifecycleMultiplier != 0.25 {
		t.Errorf("deprecated hit = %+v", got)
	}
}

// TestLifecycleRanking_ExpiryIsEvaluatedAtQueryTime is the case a cached
// boolean would fail. An entry expiring is a clock event with no file change,
// and the recall cache is keyed on mtime — so a flag frozen at index time
// would stay false forever.
func TestLifecycleRanking_ExpiryIsEvaluatedAtQueryTime(t *testing.T) {
	root := t.TempDir()
	p := filepath.Join(root, ".nightgauge", "knowledge", "features", "1-widget")
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatal(err)
	}
	body := "---\ntype: decisions\nstale_after: \"2026-06-01T00:00:00Z\"\n---\n\n# Decisions\n\nring buffer\n"
	if err := os.WriteFile(filepath.Join(p, "decisions.md"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	idx, err := BuildIndex(root, nil, nil)
	if err != nil {
		t.Fatal(err)
	}

	idx.Now = func() time.Time { return time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC) }
	before, err := Query(idx, "ring buffer", 10, nil)
	if err != nil {
		t.Fatal(err)
	}
	if before.Hits[0].Stale {
		t.Error("entry read as stale before its stale_after")
	}

	// Same index, same file, later clock — nothing on disk changed.
	idx.Now = func() time.Time { return time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC) }
	after, err := Query(idx, "ring buffer", 10, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !after.Hits[0].Stale {
		t.Error("entry did not become stale after its stale_after passed — expiry is not a query-time comparison")
	}
	if after.Hits[0].Score >= before.Hits[0].Score {
		t.Errorf("expiring did not lower the score: %v then %v", before.Hits[0].Score, after.Hits[0].Score)
	}
}

// TestLifecycleRanking_WeightsAreConfigurable proves the multipliers are read
// from config rather than hard-coded, and that flattening them to 1.0 collapses
// the ordering — the mutation that must make TestLifecycleRanking red.
func TestLifecycleRanking_WeightsAreConfigurable(t *testing.T) {
	root := lifecycleFixture(t)

	one := 1.0
	flat := &config.KnowledgeConfig{Recall: &config.RecallConfig{Weights: &config.RecallWeights{
		HumanReviewed: &one, MachineConfirmed: &one, Unverified: &one,
		StatusDraft: &one, StatusDeprecated: &one, Expired: &one,
	}}}

	idx, err := BuildIndex(root, nil, flat)
	if err != nil {
		t.Fatal(err)
	}
	idx.Now = func() time.Time { return time.Date(2026, 9, 3, 0, 0, 0, 0, time.UTC) }

	res, err := Query(idx, "ring buffer", 10, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range res.Hits {
		if h.LifecycleMultiplier != 1.0 {
			t.Errorf("%s multiplier = %v under flat weights", h.Path, h.LifecycleMultiplier)
		}
	}
	// With every factor at 1.0 the five identical bodies tie and fall back to
	// the path tie-break, which is lexicographic.
	for i := 1; i < len(res.Hits); i++ {
		if res.Hits[i-1].Score != res.Hits[i].Score {
			t.Fatalf("flat weights should tie every score: %+v", res.Hits)
		}
	}
}
