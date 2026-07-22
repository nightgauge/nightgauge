// Package recall implements BM25 indexing and ranking for knowledge base documents.
// It provides a deterministic, cache-backed recall API for the nightgauge CLI.
package recall

import (
	"strings"
	"unicode"
)

// Tokenize splits text into lowercase stemmed tokens suitable for BM25 indexing.
// Non-alphabetic characters are treated as word boundaries. Empty tokens and
// tokens under 2 characters are discarded. Returns nil for empty input.
func Tokenize(text string) []string {
	if text == "" {
		return nil
	}
	var tokens []string
	var word strings.Builder
	flush := func() {
		if word.Len() >= 2 {
			tokens = append(tokens, stem(word.String()))
		}
		word.Reset()
	}
	for _, r := range text {
		if unicode.IsLetter(r) {
			word.WriteRune(unicode.ToLower(r))
		} else if unicode.IsDigit(r) {
			word.WriteRune(r)
		} else {
			flush()
		}
	}
	flush()
	return tokens
}

// TokenizeQuery is identical to Tokenize but is the named entry point for
// query strings. A separate function keeps the call sites readable and
// allows future divergence (e.g., phrase handling).
func TokenizeQuery(q string) []string {
	return Tokenize(q)
}

// stem applies a minimal Porter stemmer (English, steps 1a–5) to a lowercase
// word. Tokens that contain digits (e.g. "bm25", "oauth2") are returned as-is
// since Porter rules apply only to alphabetic English words.
//
// This is a compact implementation of the classic Porter algorithm sufficient
// for technical English vocabulary in ADR/KB documents.
func stem(word string) string {
	if len(word) <= 2 {
		return word
	}
	// Skip stemming for tokens with digits — preserves identifiers like "bm25", "oauth2".
	for _, r := range word {
		if unicode.IsDigit(r) {
			return word
		}
	}

	// Step 1a
	if strings.HasSuffix(word, "sses") {
		word = word[:len(word)-2]
	} else if strings.HasSuffix(word, "ies") {
		word = word[:len(word)-2]
	} else if strings.HasSuffix(word, "ss") {
		// no change
	} else if strings.HasSuffix(word, "s") {
		word = word[:len(word)-1]
	}

	// Step 1b
	if strings.HasSuffix(word, "eed") {
		if measure(word[:len(word)-3]) > 0 {
			word = word[:len(word)-1]
		}
	} else if strings.HasSuffix(word, "ed") {
		stem1b := word[:len(word)-2]
		if containsVowel(stem1b) {
			word = step1bPost(stem1b)
		}
	} else if strings.HasSuffix(word, "ing") {
		stem1b := word[:len(word)-3]
		if containsVowel(stem1b) {
			word = step1bPost(stem1b)
		}
	}

	// Step 1c
	if strings.HasSuffix(word, "y") && len(word) > 2 && containsVowel(word[:len(word)-1]) {
		word = word[:len(word)-1] + "i"
	}

	// Step 2
	word = applyStep(word, step2Rules)

	// Step 3
	word = applyStep(word, step3Rules)

	// Step 4
	word = applyStep(word, step4Rules)

	// Step 5a
	if strings.HasSuffix(word, "e") {
		stem5 := word[:len(word)-1]
		m := measure(stem5)
		if m > 1 || (m == 1 && !cvc(stem5)) {
			word = stem5
		}
	}

	// Step 5b
	if measure(word) > 1 && strings.HasSuffix(word, "ll") {
		word = word[:len(word)-1]
	}

	return word
}

func step1bPost(s string) string {
	switch {
	case strings.HasSuffix(s, "at"):
		return s + "e"
	case strings.HasSuffix(s, "bl"):
		return s + "e"
	case strings.HasSuffix(s, "iz"):
		return s + "e"
	case doubleConsonantSuffix(s) && !strings.HasSuffix(s, "l") && !strings.HasSuffix(s, "s") && !strings.HasSuffix(s, "z"):
		return s[:len(s)-1]
	case measure(s) == 1 && cvc(s):
		return s + "e"
	}
	return s
}

func doubleConsonantSuffix(s string) bool {
	if len(s) < 2 {
		return false
	}
	return s[len(s)-1] == s[len(s)-2] && !isVowel(rune(s[len(s)-1]))
}

type stepRule struct {
	suffix  string
	replace string
	minM    int
}

var step2Rules = []stepRule{
	{"ational", "ate", 0}, {"tional", "tion", 0}, {"enci", "ence", 0},
	{"anci", "ance", 0}, {"izer", "ize", 0}, {"abli", "able", 0},
	{"alli", "al", 0}, {"entli", "ent", 0}, {"eli", "e", 0},
	{"ousli", "ous", 0}, {"ization", "ize", 0}, {"ation", "ate", 0},
	{"ator", "ate", 0}, {"alism", "al", 0}, {"iveness", "ive", 0},
	{"fulness", "ful", 0}, {"ousness", "ous", 0}, {"aliti", "al", 0},
	{"iviti", "ive", 0}, {"biliti", "ble", 0},
}

var step3Rules = []stepRule{
	{"icate", "ic", 0}, {"ative", "", 0}, {"alize", "al", 0},
	{"iciti", "ic", 0}, {"ical", "ic", 0}, {"ful", "", 0}, {"ness", "", 0},
}

var step4Rules = []stepRule{
	{"al", "", 1}, {"ance", "", 1}, {"ence", "", 1}, {"er", "", 1},
	{"ic", "", 1}, {"able", "", 1}, {"ible", "", 1}, {"ant", "", 1},
	{"ement", "", 1}, {"ment", "", 1}, {"ent", "", 1}, {"ion", "", 1},
	{"ou", "", 1}, {"ism", "", 1}, {"ate", "", 1}, {"iti", "", 1},
	{"ous", "", 1}, {"ive", "", 1}, {"ize", "", 1},
}

func applyStep(word string, rules []stepRule) string {
	for _, r := range rules {
		if strings.HasSuffix(word, r.suffix) {
			stem := word[:len(word)-len(r.suffix)]
			if measure(stem) > r.minM {
				return stem + r.replace
			}
			// For step2/3 rules with minM==0, apply when measure >= 1
			if r.minM == 0 && measure(stem) >= 1 {
				return stem + r.replace
			}
			return word
		}
	}
	return word
}

// measure counts the number of VC sequences in a word stem.
func measure(s string) int {
	count := 0
	inVowel := false
	for _, r := range s {
		if isVowel(r) {
			inVowel = true
		} else if inVowel {
			count++
			inVowel = false
		}
	}
	return count
}

// cvc reports whether s ends with consonant-vowel-consonant where the last
// consonant is not w, x, or y (classic Porter rule).
func cvc(s string) bool {
	if len(s) < 3 {
		return false
	}
	last := rune(s[len(s)-1])
	mid := rune(s[len(s)-2])
	prev := rune(s[len(s)-3])
	if isVowel(last) || !isVowel(mid) || isVowel(prev) {
		return false
	}
	return last != 'w' && last != 'x' && last != 'y'
}

func containsVowel(s string) bool {
	for _, r := range s {
		if isVowel(r) {
			return true
		}
	}
	return false
}

func isVowel(r rune) bool {
	return r == 'a' || r == 'e' || r == 'i' || r == 'o' || r == 'u'
}
