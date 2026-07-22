package release

import (
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"strings"
)

// bracketRE matches `[BRACKETED]` annotations within a release-body line.
var bracketRE = regexp.MustCompile(`\[([^\]]+)\]`)

// Change buckets emitted in ClassifiedChange.Type. Mirror the five buckets
// used by skills/nightgauge-release-watch/SKILL.md Phase 4.
const (
	TypeFeature     = "feature"
	TypeFix         = "fix"
	TypeBreaking    = "breaking"
	TypeDeprecation = "deprecation"
	TypeImprovement = "improvement"
)

// Classify walks a slice of Release and returns a slice of ClassifiedRelease
// — one entry per release that produced at least one classified change.
// Releases with no `-`-prefixed body lines are dropped, mirroring the
// `if changes:` guard in the existing Python.
func Classify(releases []Release) []ClassifiedRelease {
	out := make([]ClassifiedRelease, 0, len(releases))
	for _, r := range releases {
		changes := classifyBody(r.Body)
		if len(changes) == 0 {
			continue
		}
		version := strings.TrimPrefix(r.TagName, "v")
		version = strings.TrimPrefix(version, "V")
		out = append(out, ClassifiedRelease{
			Version:     version,
			PublishedAt: r.PublishedAt,
			Changes:     changes,
		})
	}
	return out
}

// classifyBody parses a release body line-by-line and returns one
// ClassifiedChange per `-`-prefixed line.
func classifyBody(body string) []ClassifiedChange {
	if strings.TrimSpace(body) == "" {
		return nil
	}
	out := make([]ClassifiedChange, 0, 8)
	for _, raw := range strings.Split(body, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || !strings.HasPrefix(line, "-") {
			continue
		}
		// Strip leading `-` and any whitespace following it.
		line = strings.TrimSpace(strings.TrimPrefix(line, "-"))
		if line == "" {
			continue
		}
		out = append(out, classifyLine(line))
	}
	return out
}

// classifyLine determines the change type, extracts bracket tags, and trims
// the description down to the core text. Mirrors the Python prefix-match in
// SKILL.md Phase 4 (lines 282–306) byte-for-byte.
func classifyLine(line string) ClassifiedChange {
	lower := strings.ToLower(line)
	changeType := TypeImprovement
	switch {
	case strings.HasPrefix(lower, "added"):
		changeType = TypeFeature
	case strings.HasPrefix(lower, "fixed"):
		changeType = TypeFix
	case strings.HasPrefix(lower, "breaking"):
		changeType = TypeBreaking
	case strings.HasPrefix(lower, "deprecated"):
		changeType = TypeDeprecation
	case strings.HasPrefix(lower, "improved"):
		changeType = TypeImprovement
	case strings.HasPrefix(lower, "changed"):
		changeType = TypeImprovement
	}

	// Extract bracket tags in order of appearance.
	matches := bracketRE.FindAllStringSubmatch(line, -1)
	tags := make([]string, 0, len(matches))
	for _, m := range matches {
		if len(m) >= 2 {
			tags = append(tags, m[1])
		}
	}

	// Description: line minus bracket annotations and backticks, trimmed.
	desc := bracketRE.ReplaceAllString(line, "")
	desc = strings.ReplaceAll(desc, "`", "")
	desc = collapseWhitespace(desc)

	return ClassifiedChange{
		Type:        changeType,
		Description: desc,
		Tags:        tags,
	}
}

// collapseWhitespace replaces internal runs of whitespace with single spaces
// and trims the ends. Mirrors the `\s*\[...\]\s*` strip + `.strip()` chain in
// the existing Python.
func collapseWhitespace(s string) string {
	fields := strings.Fields(s)
	return strings.Join(fields, " ")
}

// ReadInput consumes a JSON document from r and returns the contained
// releases. Two shapes are accepted:
//
//   - A FetchResult (the output of `release fetch --json`)
//   - A bare `[]Release` (a stripped-down array, for piping convenience)
//
// Callers (typically `release classify-changes`) feed the result into
// Classify.
func ReadInput(r io.Reader) ([]Release, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, fmt.Errorf("read input: %w", err)
	}
	trimmed := strings.TrimLeft(string(data), " \t\r\n")
	if strings.HasPrefix(trimmed, "[") {
		var arr []Release
		if err := json.Unmarshal(data, &arr); err != nil {
			return nil, fmt.Errorf("decode releases array: %w", err)
		}
		return arr, nil
	}
	var fr FetchResult
	if err := json.Unmarshal(data, &fr); err != nil {
		return nil, fmt.Errorf("decode FetchResult: %w", err)
	}
	return fr.Releases, nil
}
