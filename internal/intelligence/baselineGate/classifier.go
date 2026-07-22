// Package baselineGate implements the baseline-CI dependency preflight gate.
//
// The gate classifies acceptance criterion text for "this issue requires a
// green CI baseline on `main` before it can land" semantics, then queries the
// referenced workflow's recent runs and decides whether to allow or defer
// pipeline dispatch. See docs/CONFIGURATION.md and docs/FAILURE_TAXONOMY.md
// for runtime configuration and weighting details.
//
// This file holds the pure-text classifier — no I/O, no network. The threshold
// evaluator lives in gate.go.
package baselineGate

import (
	"regexp"
	"strings"
)

// triggerPatterns is the case-insensitive keyword list that signals an AC
// item depends on a green CI baseline. Keep these tightly scoped — false
// positives mean valid issues get deferred.
//
// All patterns are lowercased substring matches except those declared
// explicitly as regexes below.
var triggerSubstrings = []string{
	"required check",
	"required status",
	"branch protection",
	"ruleset",
}

var triggerRegexes = []*regexp.Regexp{
	regexp.MustCompile(`(?i)promote\b.*?\bto\b.*?\brequired\b`),
	regexp.MustCompile(`(?i)enforce\b.*?\bon\b.*?\bmain\b`),
	regexp.MustCompile(`(?i)make\b.*?\brequired\b.*?\bcheck\b`),
}

// workflowRE matches `.github/workflows/<file>.ya?ml` references in AC text.
// The path may appear anywhere in the AC body — fenced code, plain prose, or a
// link target.
var workflowRE = regexp.MustCompile(`\.github/workflows/([A-Za-z0-9_.-]+\.ya?ml)`)

// jobBacktickRE captures the *first* backticked phrase in the AC text. Job
// names commonly appear as `Integration & E2E Tests` immediately following the
// workflow reference. Display-name fallback covers issues that say "the
// `Integration & E2E Tests` job" without a workflow path.
var jobBacktickRE = regexp.MustCompile("`([^`]{2,80})`")

// ACMatch is the structured result of running ClassifyAC on a single AC text.
//
// `Triggered` is true when at least one trigger pattern matched. `Workflow`
// and `Job` are populated best-effort from the same AC text — they may be
// empty when the gate cannot extract them (the gate evaluator treats those
// cases as "unparseable, allow dispatch" per ADR-003).
type ACMatch struct {
	// Triggered is true when the AC text matches a baseline-CI trigger phrase.
	Triggered bool
	// Workflow is the bare workflow filename (e.g. "ci.yml") extracted from
	// the AC text. Empty when no `.github/workflows/...` reference is present.
	Workflow string
	// Job is the display name of the workflow job referenced. Empty when no
	// backticked phrase appears or only the workflow path is given.
	Job string
	// TriggerText is the trigger phrase (or substring) that fired. Used as
	// evidence in the deferral comment and CLI JSON output.
	TriggerText string
}

// ClassifyAC parses a single AC text. Returns an ACMatch with `Triggered`
// false when no baseline-CI keyword is found.
//
// Pure function: same input → same output. No regex caching beyond
// package-level compilation.
func ClassifyAC(text string) ACMatch {
	if text == "" {
		return ACMatch{}
	}

	lower := strings.ToLower(text)

	var trigger string
	for _, sub := range triggerSubstrings {
		if strings.Contains(lower, sub) {
			trigger = sub
			break
		}
	}
	if trigger == "" {
		for _, re := range triggerRegexes {
			if loc := re.FindStringIndex(text); loc != nil {
				trigger = text[loc[0]:loc[1]]
				break
			}
		}
	}

	if trigger == "" {
		return ACMatch{}
	}

	match := ACMatch{
		Triggered:   true,
		TriggerText: trigger,
	}

	if wf := workflowRE.FindStringSubmatch(text); len(wf) >= 2 {
		match.Workflow = wf[1]
	}

	// Job extraction: prefer the first backticked phrase that looks like a job
	// name (excludes pure file paths and inline code snippets containing
	// punctuation that's not job-name-shaped).
	if jb := jobBacktickRE.FindAllStringSubmatch(text, -1); len(jb) > 0 {
		for _, m := range jb {
			candidate := strings.TrimSpace(m[1])
			if isLikelyJobName(candidate) {
				match.Job = candidate
				break
			}
		}
	}

	return match
}

// isLikelyJobName filters out obvious non-job-name backticked phrases (file
// paths, code keywords). A real job name is short, has no slashes, and is not
// a single lowercase identifier.
func isLikelyJobName(s string) bool {
	if s == "" || len(s) > 80 {
		return false
	}
	if strings.Contains(s, "/") || strings.Contains(s, "\\") {
		return false
	}
	if strings.HasSuffix(s, ".yml") || strings.HasSuffix(s, ".yaml") {
		return false
	}
	// Reject a single all-lowercase identifier (likely a code symbol).
	if !strings.ContainsAny(s, " &-_") && strings.ToLower(s) == s {
		return false
	}
	return true
}

// SplitACList splits an issue body into individual AC items. The pipeline's
// existing AC parsing convention treats checkbox bullets (`- [ ]`, `- [x]`)
// and numbered list items as discrete ACs. Returns the original body as a
// single item when no list markers are found.
func SplitACList(body string) []string {
	if body == "" {
		return nil
	}
	lines := strings.Split(body, "\n")
	var items []string
	var cur strings.Builder
	flush := func() {
		s := strings.TrimSpace(cur.String())
		if s != "" {
			items = append(items, s)
		}
		cur.Reset()
	}
	for _, line := range lines {
		trimmed := strings.TrimLeft(line, " \t")
		// New item markers: "- [ ]" / "- [x]" / "1. " / "* "
		if strings.HasPrefix(trimmed, "- [") || strings.HasPrefix(trimmed, "* ") || numericListPrefix(trimmed) {
			flush()
			cur.WriteString(trimmed)
			cur.WriteString("\n")
			continue
		}
		if cur.Len() > 0 {
			cur.WriteString(line)
			cur.WriteString("\n")
		}
	}
	flush()
	if len(items) == 0 {
		return []string{strings.TrimSpace(body)}
	}
	return items
}

// numericListPrefix reports whether trimmed begins with "<digits>. ".
func numericListPrefix(trimmed string) bool {
	end := 0
	for end < len(trimmed) && trimmed[end] >= '0' && trimmed[end] <= '9' {
		end++
	}
	if end == 0 || end >= len(trimmed)-1 {
		return false
	}
	return trimmed[end] == '.' && trimmed[end+1] == ' '
}
