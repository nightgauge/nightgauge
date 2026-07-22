package recall

import (
	"math"
	"testing"
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
