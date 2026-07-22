// Package graduation implements deterministic scoring of decisions.md ADR
// blocks against telemetry signals and structural heuristics, producing a
// ranked list of graduation candidates for the retro stage to surface.
//
// The package is strictly read-only: no edits to decisions.md, no LLM calls.
// All scoring rubrics, keyword lists, and threshold constants live here so a
// future schema change to telemetry can be made in one place.
//
// See docs/GO_BINARY.md ("knowledge graduate-candidates") and issue #3596 for
// the contract this implements.
package graduation

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/nightgauge/nightgauge/internal/knowledge"
	"github.com/nightgauge/nightgauge/internal/knowledge/telemetry"
	"github.com/nightgauge/nightgauge/internal/pipeline"
)

// DefaultMinScore is the default threshold an ADR must clear to qualify as a
// graduation candidate. The value is per the AC of issue #3596 — chosen so
// pattern-language alone (+2) is never sufficient without at least one other
// positive signal. Exposed via Options.MinScore for testability.
const DefaultMinScore = 4

// FallbackDest is returned by suggestedDest when no docs/*.md file scores any
// keyword hit. Matches the AC requirement that suggested_dest always points
// at a real file.
const FallbackDest = "docs/KNOWLEDGE_BASE.md"

// Options configures a Candidates() invocation. Zero values yield the AC
// defaults.
type Options struct {
	MinScore int
}

// Candidate is one ranked ADR that scored at or above the threshold.
type Candidate struct {
	ADRTitle      string   `json:"adr_title"`
	ADRIndex      int      `json:"adr_index"`
	Score         int      `json:"score"`
	Signals       []string `json:"signals"`
	SuggestedDest string   `json:"suggested_dest"`
}

// Result is the full output of one Candidates() call. The Decisions field
// preserves the workspace-relative decisions.md path so callers can include
// it in human or telemetry output.
type Result struct {
	Issue         int         `json:"issue"`
	DecisionsPath string      `json:"decisions_path"`
	Candidates    []Candidate `json:"candidates"`
}

// signalSet captures the booleans and counts that feed scoreADR. Held in a
// struct so the scoring rubric is one self-contained switch.
type signalSet struct {
	RecallHitsDistinct  int
	GeneralLanguage     bool
	PatternLanguage     bool
	FilledConsequences  bool
	AlreadyGraduated    bool
	IssueSpecificTitle  bool
}

// filePathRe finds `packages/`, `internal/`, or `src/` style paths inside an
// ADR Decision body — the structural signal that an ADR is too specific to
// graduate as-written. Anchored with \b to avoid false positives like the
// word "internals" appearing in prose.
var filePathRe = regexp.MustCompile(`(?m)\b(packages|internal|src)/[\w./-]+\b`)

// patternKeywordsWord are case-insensitive word-bounded keywords. Word
// boundaries prevent partial hits like "wallet" → "all" or "rationally" →
// "rational".
var patternKeywordsWord = []string{"always", "never", "every", "all", "any"}

// patternKeywordsSubstring are matched case-insensitively as substrings (the
// embedded space makes them safe).
var patternKeywordsSubstring = []string{"any service"}

// issueSpecificRe flags ADR titles that obviously refer to a single issue or
// PR, which makes them poor graduation candidates regardless of content.
var issueSpecificRe = regexp.MustCompile(`(?i)\b(issue|this[ -]pr|#\d+)\b`)

// scoreADR applies the rubric per the issue #3596 acceptance criteria. The
// returned reasons are stable strings — tests pin them and the retro skill
// surfaces them verbatim in the Graduation Candidates table.
func scoreADR(sig signalSet) (int, []string) {
	score := 0
	var reasons []string
	if sig.RecallHitsDistinct >= 2 {
		score += 3
		reasons = append(reasons, fmt.Sprintf("recall_hits:%d", sig.RecallHitsDistinct))
	}
	if sig.GeneralLanguage {
		score += 2
		reasons = append(reasons, "general_language")
	}
	if sig.PatternLanguage {
		score += 2
		reasons = append(reasons, "pattern_language")
	}
	if sig.FilledConsequences {
		score += 1
		reasons = append(reasons, "filled_consequences")
	}
	if sig.AlreadyGraduated {
		score -= 2
		reasons = append(reasons, "already_graduated")
	}
	if sig.IssueSpecificTitle {
		score -= 1
		reasons = append(reasons, "issue_specific_title")
	}
	return score, reasons
}

// detectSignals derives the structural signalSet for one ADR given the
// distinct recall-hit count (computed once per decisions.md file at the
// caller).
func detectSignals(adr knowledge.ADRBlock, distinctRecallHits int) signalSet {
	return signalSet{
		RecallHitsDistinct: distinctRecallHits,
		GeneralLanguage:    !filePathRe.MatchString(adr.Decision),
		PatternLanguage:    containsPatternKeyword(adr.Decision),
		FilledConsequences: isFilledConsequences(adr.Consequences),
		AlreadyGraduated:   adr.Graduated,
		IssueSpecificTitle: issueSpecificRe.MatchString(adr.Title),
	}
}

// containsPatternKeyword reports whether decisionBody contains any of the
// rule-like keywords. "MUST" is matched case-sensitively per RFC 2119
// convention; everything else is case-insensitive.
func containsPatternKeyword(decisionBody string) bool {
	if strings.Contains(decisionBody, "MUST") {
		return true
	}
	lower := strings.ToLower(decisionBody)
	for _, kw := range patternKeywordsWord {
		if containsWord(lower, kw) {
			return true
		}
	}
	for _, kw := range patternKeywordsSubstring {
		if strings.Contains(lower, kw) {
			return true
		}
	}
	return false
}

// containsWord reports whether s contains keyword surrounded by word
// boundaries (start, end, or non-letter on either side).
func containsWord(s, keyword string) bool {
	idx := 0
	for {
		i := strings.Index(s[idx:], keyword)
		if i < 0 {
			return false
		}
		i += idx
		startOK := i == 0 || !isLetter(s[i-1])
		endOK := i+len(keyword) == len(s) || !isLetter(s[i+len(keyword)])
		if startOK && endOK {
			return true
		}
		idx = i + 1
		if idx >= len(s) {
			return false
		}
	}
}

func isLetter(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}

// isFilledConsequences applies the AC threshold: >30 non-whitespace chars and
// no template placeholder marker. Tests pin both the lower-bound and the
// template-marker cases.
func isFilledConsequences(consequences string) bool {
	trimmed := strings.TrimSpace(consequences)
	if len(trimmed) < 30 {
		return false
	}
	lower := strings.ToLower(trimmed)
	if strings.Contains(lower, "replace this placeholder") {
		return false
	}
	if strings.Contains(trimmed, "[Expected impact, trade-offs, and follow-up actions]") {
		return false
	}
	return true
}

// recallSignal counts how many distinct future-issue recall_hit events
// reference decisionsRelPath after sourceCutoff. Events from sourceIssue or
// events with timestamps at/before sourceCutoff are excluded.
//
// The Path field on an event may be workspace-relative or absolute depending
// on the emitter. recallSignal normalizes both forms against decisionsRelPath
// so a mixed event log still aggregates correctly.
func recallSignal(events []telemetry.Event, decisionsRelPath, decisionsAbsPath string, sourceIssue int, sourceCutoff time.Time) (distinct int, total int) {
	seen := map[int]struct{}{}
	for _, ev := range events {
		if ev.Type != telemetry.EventRecallHit {
			continue
		}
		if ev.IssueNumber <= 0 || ev.IssueNumber == sourceIssue {
			continue
		}
		if ev.Path == "" {
			continue
		}
		if ev.Path != decisionsRelPath && ev.Path != decisionsAbsPath {
			continue
		}
		ts, perr := time.Parse(time.RFC3339, ev.Timestamp)
		if perr != nil {
			continue
		}
		if !ts.After(sourceCutoff) {
			continue
		}
		total++
		seen[ev.IssueNumber] = struct{}{}
	}
	return len(seen), total
}

// sourceCutoffFor returns the time before which recall events are considered
// pre-existing (and therefore irrelevant) for a given issue's decisions.md.
//
// Strategy: take the earliest of the earliest EventScaffold for the issue and
// the file mtime. Both are approximations — they bracket when the file
// physically existed. Either one alone would over-count old reads.
func sourceCutoffFor(decisionsAbsPath string, sourceIssue int, events []telemetry.Event) time.Time {
	var cutoff time.Time
	for _, ev := range events {
		if ev.Type != telemetry.EventScaffold {
			continue
		}
		if ev.IssueNumber != sourceIssue {
			continue
		}
		ts, perr := time.Parse(time.RFC3339, ev.Timestamp)
		if perr != nil {
			continue
		}
		if cutoff.IsZero() || ts.Before(cutoff) {
			cutoff = ts
		}
	}
	if info, err := os.Stat(decisionsAbsPath); err == nil {
		mtime := info.ModTime()
		if cutoff.IsZero() || mtime.Before(cutoff) {
			cutoff = mtime
		}
	}
	return cutoff
}

// suggestedDest scores each *.md file under docsDir by how many tokens from
// the ADR title (and optional Tags line) appear in the filename, lowercased.
// Highest score wins; ties break alphabetically. Returns FallbackDest when no
// docs file scores any hit, when docsDir is missing, or when there are no
// markdown files.
func suggestedDest(adr knowledge.ADRBlock, docsDir string) string {
	tokens := tokenize(adr.Title + " " + adr.Tags)
	if len(tokens) == 0 {
		return FallbackDest
	}

	entries, err := os.ReadDir(docsDir)
	if err != nil {
		return FallbackDest
	}

	type scored struct {
		name  string
		score int
	}
	var candidates []scored
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(strings.ToLower(name), ".md") {
			continue
		}
		nameLower := strings.ToLower(strings.TrimSuffix(name, filepath.Ext(name)))
		// Replace common separators with spaces so multi-token matches work
		// for "CODE_STANDARDS.md" vs token "standards".
		nameLower = strings.NewReplacer("_", " ", "-", " ", ".", " ").Replace(nameLower)
		score := 0
		for _, tok := range tokens {
			if tok == "" {
				continue
			}
			// Substring match so "test" matches "testing.md" and
			// "standard" matches "code standards". Length floor (>=3)
			// already filters out noise from tokenize().
			if strings.Contains(nameLower, tok) {
				score++
			}
		}
		if score > 0 {
			candidates = append(candidates, scored{name: name, score: score})
		}
	}
	if len(candidates) == 0 {
		return FallbackDest
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].score != candidates[j].score {
			return candidates[i].score > candidates[j].score
		}
		return candidates[i].name < candidates[j].name
	})
	return filepath.ToSlash(filepath.Join("docs", candidates[0].name))
}

// tokenize lowercases, strips punctuation, and returns whitespace-separated
// tokens longer than 2 characters. Stopwords are dropped to reduce noise from
// generic ADR titles.
func tokenize(s string) []string {
	if s == "" {
		return nil
	}
	cleaned := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z',
			r >= 'A' && r <= 'Z',
			r >= '0' && r <= '9':
			return r
		default:
			return ' '
		}
	}, s)
	stopwords := map[string]bool{
		"the": true, "and": true, "for": true, "with": true,
		"this": true, "that": true, "from": true, "into": true,
		"adr": true,
	}
	fields := strings.Fields(strings.ToLower(cleaned))
	out := make([]string, 0, len(fields))
	seen := map[string]bool{}
	for _, f := range fields {
		if len(f) < 3 || stopwords[f] || seen[f] {
			continue
		}
		seen[f] = true
		out = append(out, f)
	}
	return out
}

// Candidates is the package entrypoint. It loads decisions.md for the given
// issue, scores every ADR block, and returns those at or above
// opts.MinScore (default DefaultMinScore) sorted by descending score and
// then ascending ADR index.
//
// The function never mutates files. Missing knowledge-events.jsonl is
// non-fatal — recall signals score 0.
func Candidates(workspaceRoot string, issueNumber int, opts Options) (Result, error) {
	if issueNumber <= 0 {
		return Result{}, fmt.Errorf("issue number must be positive")
	}
	if opts.MinScore == 0 {
		opts.MinScore = DefaultMinScore
	}

	decisionsRel, err := knowledge.FindDecisionsPath(workspaceRoot, issueNumber)
	if err != nil {
		return Result{}, err
	}
	decisionsAbs := decisionsRel
	if !filepath.IsAbs(decisionsAbs) {
		decisionsAbs = filepath.Join(workspaceRoot, decisionsRel)
	}

	adrs, err := knowledge.EnumerateADRBlocks(decisionsAbs)
	if err != nil {
		return Result{}, err
	}

	events, err := pipeline.LoadKnowledgeEvents(workspaceRoot)
	if err != nil {
		// Treat as missing — telemetry is best-effort.
		events = nil
	}

	cutoff := sourceCutoffFor(decisionsAbs, issueNumber, events)
	distinct, _ := recallSignal(events, decisionsRel, decisionsAbs, issueNumber, cutoff)

	docsDir := filepath.Join(workspaceRoot, "docs")

	result := Result{
		Issue:         issueNumber,
		DecisionsPath: decisionsRel,
		Candidates:    []Candidate{},
	}

	for _, adr := range adrs {
		sig := detectSignals(adr, distinct)
		score, reasons := scoreADR(sig)
		if score < opts.MinScore {
			continue
		}
		result.Candidates = append(result.Candidates, Candidate{
			ADRTitle:      adr.Title,
			ADRIndex:      adr.Index,
			Score:         score,
			Signals:       reasons,
			SuggestedDest: suggestedDest(adr, docsDir),
		})
	}

	sort.Slice(result.Candidates, func(i, j int) bool {
		if result.Candidates[i].Score != result.Candidates[j].Score {
			return result.Candidates[i].Score > result.Candidates[j].Score
		}
		return result.Candidates[i].ADRIndex < result.Candidates[j].ADRIndex
	})

	return result, nil
}
