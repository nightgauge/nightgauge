package modelwatch

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/nightgauge/nightgauge/internal/models"
)

// sentence is one sentence of a source page with the source it came from.
type sentence struct {
	Text   string
	Source string
}

// fact is a typed value plus the sentence that established it.
type fact[T any] struct {
	Value T
	Found bool
	From  sentence
}

// facts is everything the generic rules extracted from a release's pages.
type facts struct {
	ModelID         fact[string]
	RateInput       fact[float64]
	RateOutput      fact[float64]
	RateCacheRead   fact[float64]
	ContextWindow   fact[int]
	MaxOutputTokens fact[int]
	Efforts         fact[[]string]
	EffortDefault   fact[string]
	ThinkingDefault fact[string]
	ThinkingCeiling fact[string]
	Propensity      map[string]fact[string]
}

var (
	sentenceSplit = regexp.MustCompile(`[.!?]\s+|\n\s*\n|\n`)
	idPattern     = regexp.MustCompile(`^[a-z][a-z0-9.\-]*[0-9][a-z0-9.\-]*$`)
	idCue         = regexp.MustCompile(`(?i)\b(model id|model name|api id|model identifier)\b`)
	backticked    = regexp.MustCompile("`([^`]+)`")
	rateRe        = regexp.MustCompile(`(?i)\$([0-9]+(?:\.[0-9]+)?)\s*(?:/|per)\s*(?:1\s*)?(?:million|m|mtok)\b(?:\s+(input|output|cache[- ]read))?`)
	cacheReadCue  = regexp.MustCompile(`(?i)\bcache[- ]reads?\b`)
	contextCue    = regexp.MustCompile(`(?i)\bcontext window\b`)
	maxOutputCue  = regexp.MustCompile(`(?i)\bmax(?:imum)? output\b`)
	tokenCount    = regexp.MustCompile(`(?i)\b([0-9][0-9,]*(?:\.[0-9]+)?)\s*([km])?\b`)
	effortCue     = regexp.MustCompile(`(?i)\beffort\b.*\bsupports?\b|\bsupports?\b.*\beffort\b`)
	effortDefault = regexp.MustCompile(`(?i)\b(?:default effort is|effort defaults to)\s+([a-z]+)`)
	thinkingCue   = regexp.MustCompile(`(?i)\bthinking\b`)
	byDefaultCue  = regexp.MustCompile(`(?i)\bby default\b`)
	onWord        = regexp.MustCompile(`(?i)\b(on|enabled)\b`)
	offWord       = regexp.MustCompile(`(?i)\b(off|disabled)\b`)
	disableCue    = regexp.MustCompile(`(?i)\bdisabl`)
	rejectCue     = regexp.MustCompile(`(?i)\b(400|error|rejected|rejects)\b`)
	ceilingRe     = regexp.MustCompile(`(?i)\b([a-z]+)\s+or\s+(?:lower|below)\b`)
	neverCue      = regexp.MustCompile(`(?i)\b(cannot|can't) be disabled\b|\bat any effort\b`)
	highWord      = regexp.MustCompile(`(?i)\b(more|readily|frequently|often|increased)\b`)
	lowWord       = regexp.MustCompile(`(?i)\b(less|fewer|rarely|seldom|reduced)\b`)
)

// propensityAxes maps each Propensity axis to the cue that names it.
var propensityAxes = []struct {
	Axis string
	Cue  *regexp.Regexp
}{
	{"verification", regexp.MustCompile(`(?i)\bverif|\bchecks? its own work\b`)},
	{"delegation", regexp.MustCompile(`(?i)\bsubagents?\b|\bdelegat`)},
	{"narration", regexp.MustCompile(`(?i)\bnarrat|\bprogress updates?\b`)},
}

// splitSentences breaks page text into trimmed, non-empty sentences.
func splitSentences(text, source string) []sentence {
	var out []sentence
	for _, s := range sentenceSplit.Split(text, -1) {
		s = strings.Join(strings.Fields(s), " ")
		if s != "" {
			out = append(out, sentence{Text: s, Source: source})
		}
	}
	return out
}

// effortSet returns the closed-set effort levels named in s, in ladder order.
func effortSet(s string) []string {
	var out []string
	for _, level := range models.EffortOrder {
		if regexp.MustCompile(`(?i)\b` + level + `\b`).MatchString(s) {
			out = append(out, level)
		}
	}
	return out
}

func isEffort(level string) bool {
	for _, l := range models.EffortOrder {
		if l == level {
			return true
		}
	}
	return false
}

// parseCount reads "1,000,000", "200K" or "1M" as an integer.
func parseCount(num, suffix string) (int, bool) {
	f, err := strconv.ParseFloat(strings.ReplaceAll(num, ",", ""), 64)
	if err != nil {
		return 0, false
	}
	switch strings.ToLower(suffix) {
	case "k":
		f *= 1_000
	case "m":
		f *= 1_000_000
	}
	return int(f), f >= 1_000
}

// largestCount returns the largest plausible token count in s.
func largestCount(s string) (int, bool) {
	best, found := 0, false
	for _, m := range tokenCount.FindAllStringSubmatch(s, -1) {
		if n, ok := parseCount(m[1], m[2]); ok && n > best {
			best, found = n, true
		}
	}
	return best, found
}

// extract applies every rule to every sentence. The first sentence that
// establishes a fact wins, so earlier sources take precedence.
func extract(sentences []sentence) facts {
	f := facts{Propensity: map[string]fact[string]{}}
	for _, s := range sentences {
		t := s.Text
		if !f.ModelID.Found && idCue.MatchString(t) {
			for _, m := range backticked.FindAllStringSubmatch(t, -1) {
				if idPattern.MatchString(m[1]) {
					f.ModelID = fact[string]{m[1], true, s}
					break
				}
			}
		}
		rates := rateRe.FindAllStringSubmatch(t, -1)
		for _, m := range rates {
			v, err := strconv.ParseFloat(m[1], 64)
			if err != nil {
				continue
			}
			kind := strings.ToLower(strings.ReplaceAll(m[2], "-", " "))
			if kind == "" && len(rates) == 1 && cacheReadCue.MatchString(t) {
				kind = "cache read"
			}
			switch kind {
			case "input":
				setOnce(&f.RateInput, v, s)
			case "output":
				setOnce(&f.RateOutput, v, s)
			case "cache read":
				setOnce(&f.RateCacheRead, v, s)
			}
		}
		if contextCue.MatchString(t) {
			if n, ok := largestCount(t); ok {
				setOnce(&f.ContextWindow, n, s)
			}
		}
		if maxOutputCue.MatchString(t) {
			if n, ok := largestCount(t); ok {
				setOnce(&f.MaxOutputTokens, n, s)
			}
		}
		if m := effortDefault.FindStringSubmatch(t); m != nil && isEffort(strings.ToLower(m[1])) {
			setOnce(&f.EffortDefault, strings.ToLower(m[1]), s)
		} else if effortCue.MatchString(t) && !thinkingCue.MatchString(t) {
			if levels := effortSet(t); len(levels) > 1 {
				setOnce(&f.Efforts, levels, s)
			}
		}
		if thinkingCue.MatchString(t) {
			switch {
			case disableCue.MatchString(t) && rejectCue.MatchString(t):
				if neverCue.MatchString(t) {
					setOnce(&f.ThinkingCeiling, models.ThinkingDisableNever, s)
				} else if m := ceilingRe.FindStringSubmatch(t); m != nil && isEffort(strings.ToLower(m[1])) {
					setOnce(&f.ThinkingCeiling, strings.ToLower(m[1]), s)
				}
			case byDefaultCue.MatchString(t) && onWord.MatchString(t):
				setOnce(&f.ThinkingDefault, "on", s)
			case byDefaultCue.MatchString(t) && offWord.MatchString(t):
				setOnce(&f.ThinkingDefault, "off", s)
			}
		}
		for _, ax := range propensityAxes {
			if _, done := f.Propensity[ax.Axis]; done || !ax.Cue.MatchString(t) {
				continue
			}
			switch {
			case lowWord.MatchString(t):
				f.Propensity[ax.Axis] = fact[string]{models.PropensityLow, true, s}
			case highWord.MatchString(t):
				f.Propensity[ax.Axis] = fact[string]{models.PropensityHigh, true, s}
			}
		}
	}
	return f
}

func setOnce[T any](dst *fact[T], v T, s sentence) {
	if !dst.Found {
		*dst = fact[T]{v, true, s}
	}
}
