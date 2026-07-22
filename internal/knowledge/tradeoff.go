package knowledge

import (
	"fmt"
	"regexp"
	"strings"
)

// TradeoffSignal represents a detected tradeoff keyword match in plan text.
type TradeoffSignal struct {
	// Keyword is the matched tradeoff term (e.g., "tradeoff", "chose").
	Keyword string `json:"keyword"`
	// LineNumber is the 1-indexed line where the match occurs.
	LineNumber int `json:"line_number"`
	// Context is up to 100 chars of surrounding text for error messaging.
	Context string `json:"context"`
	// Position is the byte offset of the match within the matching line.
	Position int `json:"position"`
}

// DetectTradeoffs returns true when planText contains at least 2 distinct
// tradeoff keywords from the provided list.
//
// The 2-keyword minimum reduces false positives from incidental single-word
// occurrences of common English words (e.g. a plan that says "we chose a
// library" contains only one signal word — not enough to trigger the gate).
//
// Word boundary matching (\b) is applied for single-word keywords to avoid
// substring false positives (e.g. "chose" matches but "choose" does not).
// Multi-word phrases and hyphenated terms use plain substring matching.
// Matching is always case-insensitive.
func DetectTradeoffs(planText string, keywords []string) bool {
	return len(matchedKeywords(planText, keywords)) >= 2
}

// FindTradeoffSignals returns all keyword matches in planText with their
// line numbers and surrounding context. Uses the same matching rules as
// DetectTradeoffs. Returns an empty slice when no matches are found.
//
// Duplicate matches for the same keyword on the same line are deduplicated.
func FindTradeoffSignals(planText string, keywords []string) []TradeoffSignal {
	var signals []TradeoffSignal
	seen := map[string]bool{}
	lines := strings.Split(planText, "\n")

	for _, kw := range keywords {
		re, err := compileKeywordPattern(kw)
		if err != nil {
			continue
		}
		for lineIdx, line := range lines {
			locs := re.FindAllStringIndex(strings.ToLower(line), -1)
			for _, loc := range locs {
				dedupeKey := fmt.Sprintf("%s:%d", kw, lineIdx+1)
				if seen[dedupeKey] {
					continue
				}
				seen[dedupeKey] = true
				signals = append(signals, TradeoffSignal{
					Keyword:    kw,
					LineNumber: lineIdx + 1,
					Context:    extractContext(line, loc[0], 100),
					Position:   loc[0],
				})
			}
		}
	}
	return signals
}

// FormatSignalList formats a slice of TradeoffSignals as a human-readable
// bullet list suitable for inclusion in error messages.
func FormatSignalList(signals []TradeoffSignal) string {
	var sb strings.Builder
	for _, s := range signals {
		fmt.Fprintf(&sb, "  - Line %d: %q — %s\n", s.LineNumber, s.Keyword, s.Context)
	}
	return sb.String()
}

// matchedKeywords returns the distinct keywords that appear in planText
// (case-insensitive, with word boundary matching for single-word terms).
func matchedKeywords(planText string, keywords []string) []string {
	lower := strings.ToLower(planText)
	var matched []string
	for _, kw := range keywords {
		re, err := compileKeywordPattern(kw)
		if err != nil {
			continue
		}
		if re.MatchString(lower) {
			matched = append(matched, kw)
		}
	}
	return matched
}

// compileKeywordPattern builds a compiled regex for keyword matching.
// Single-word keywords get word-boundary anchors (\b).
// Multi-word phrases and hyphenated terms use plain case-insensitive substring matching.
func compileKeywordPattern(keyword string) (*regexp.Regexp, error) {
	lower := strings.ToLower(keyword)
	escaped := regexp.QuoteMeta(lower)

	// Hyphenated or space-separated: no word boundaries needed.
	if strings.ContainsAny(lower, " \t-") {
		return regexp.Compile(escaped)
	}
	// Single word: wrap with word boundaries.
	return regexp.Compile(`\b` + escaped + `\b`)
}

// extractContext returns at most maxLen characters centered around pos in line.
func extractContext(line string, pos, maxLen int) string {
	half := maxLen / 2
	start := pos - half
	if start < 0 {
		start = 0
	}
	end := start + maxLen
	if end > len(line) {
		end = len(line)
	}
	return strings.TrimSpace(line[start:end])
}
