// Package hooks implements Claude Code hook logic for the Nightgauge pipeline.
// These are called via thin shell wrappers registered as Claude Code hooks.
package hooks

import (
	"regexp"
	"strings"
)

// PatternCategory identifies the type of sanitization pattern.
type PatternCategory string

const (
	CategoryDestructive         PatternCategory = "destructive"
	CategoryExfiltration        PatternCategory = "exfiltration"
	CategoryPrivilegeEscalation PatternCategory = "privilege_escalation"
	CategoryPromptInjection     PatternCategory = "prompt_injection"
	CategoryPathTraversal       PatternCategory = "path_traversal"
)

// CompiledPattern is a pre-compiled regex with its category and source.
type CompiledPattern struct {
	Category PatternCategory
	Regex    *regexp.Regexp
	Source   string // original pattern string for logging
}

// PatternMatch holds the result of a pattern scan.
type PatternMatch struct {
	Category PatternCategory
	Pattern  string // source pattern that matched
}

// patternSet holds all compiled patterns, grouped by category.
var patternSet []CompiledPattern

func init() {
	patternSet = compilePatterns()
}

func compilePatterns() []CompiledPattern {
	var patterns []CompiledPattern
	add := func(cat PatternCategory, src string) {
		patterns = append(patterns, CompiledPattern{
			Category: cat,
			Regex:    regexp.MustCompile("(?i)" + src),
			Source:   src,
		})
	}

	// --- Destructive patterns ---
	// rm -rf / or rm -rf /home (but NOT rm -rf ./anything)
	add(CategoryDestructive, `rm\s+(-[a-zA-Z]*[rf][a-zA-Z]*\s+)+/($|[a-zA-Z])`)
	add(CategoryDestructive, `rm\s+(-[a-zA-Z]*[rf][a-zA-Z]*\s+)+/\*`)
	add(CategoryDestructive, `rm\s+--no-preserve-root`)
	add(CategoryDestructive, `dd\s+if=.+of=/dev/`)
	add(CategoryDestructive, `mkfs\.\w+\s+/dev/`)

	// --- Exfiltration patterns ---
	add(CategoryExfiltration, `cat\s+~/\.ssh/`)
	add(CategoryExfiltration, `cat\s+~/\.aws/`)
	add(CategoryExfiltration, `cat\s+~/\.gnupg/`)
	add(CategoryExfiltration, `(printenv|env)\s*\|.*\b(curl|wget|nc|ncat)\b`)

	// --- Privilege escalation patterns ---
	add(CategoryPrivilegeEscalation, `sudo\s+(rm|chmod|chown)\s`)
	add(CategoryPrivilegeEscalation, `passwd\s+root`)

	// --- Prompt injection patterns ---
	add(CategoryPromptInjection, `ignore\s+(all\s+)?previous\s+instructions`)
	add(CategoryPromptInjection, `disregard\s+(all\s+)?(prior|previous)\s+instructions`)
	add(CategoryPromptInjection, `you\s+are\s+now\s+a\s+`)
	add(CategoryPromptInjection, `(new|override)\s+system\s+prompt`)
	add(CategoryPromptInjection, `developer\s+mode\s+enable`)

	// --- Path traversal patterns ---
	add(CategoryPathTraversal, `\.\./\.\./`)
	add(CategoryPathTraversal, `%2e%2e/`)

	return patterns
}

// MatchPatterns checks text against all patterns of the given categories.
// Returns the first match, or nil if no match.
func MatchPatterns(text string, categories ...PatternCategory) *PatternMatch {
	catSet := make(map[PatternCategory]bool, len(categories))
	for _, c := range categories {
		catSet[c] = true
	}

	for _, p := range patternSet {
		if len(catSet) > 0 && !catSet[p.Category] {
			continue
		}
		if p.Regex.MatchString(text) {
			return &PatternMatch{Category: p.Category, Pattern: p.Source}
		}
	}
	return nil
}

// MatchAllCategories checks text against all sanitization categories.
// Returns the first match, or nil if clean.
func MatchAllCategories(text string) *PatternMatch {
	return MatchPatterns(text)
}

// IsSensitiveFile checks if a filename looks like a secrets/credential file.
func IsSensitiveFile(filename string) bool {
	lower := strings.ToLower(filename)

	// Exact matches
	sensitiveNames := []string{".env", "credentials.json"}
	for _, name := range sensitiveNames {
		if lower == name {
			return true
		}
	}

	// Prefix matches
	if strings.HasPrefix(lower, ".env.") {
		return true
	}
	if strings.HasPrefix(lower, "secrets.") || strings.HasPrefix(lower, "secrets_") {
		return true
	}

	// Extension matches
	sensitiveExts := []string{".pem", ".key"}
	for _, ext := range sensitiveExts {
		if strings.HasSuffix(lower, ext) {
			return true
		}
	}

	// Contains "secret" in filename
	if strings.Contains(lower, "secret") {
		return true
	}

	return false
}
