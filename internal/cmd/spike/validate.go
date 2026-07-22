package spike

import (
	"fmt"
	"regexp"
)

// pathDeclarationRe matches "## Spike Contract (Path A)", "(Path B)", or
// "(Path C)" as a second-level heading anywhere in the body.
var pathDeclarationRe = regexp.MustCompile(`(?m)^##\s+Spike Contract\s+\(Path\s+[ABC]\)`)

// artifactPathRe matches a well-formed spike artifact path reference in the
// body: docs/(spikes|decisions)/NNN-slug.md
var artifactPathRe = regexp.MustCompile(`docs/(?:spikes|decisions)/[0-9]+-[a-z0-9-]+\.md`)

// ValidateBody validates a spike issue body string before GitHub issue creation.
// It checks:
//  1. A fenced ```yaml recommendations block is present.
//  2. The block parses as valid YAML per the spike contract schema.
//  3. A "## Spike Contract (Path A/B/C)" heading is present.
//  4. An artifact path matching docs/(spikes|decisions)/NNN-slug.md is present.
//
// Returns a descriptive error for the first failing check, nil on success.
func ValidateBody(body string) error {
	// Check 1: yaml recommendations block present
	if !recommendationsBlockRe.Match([]byte(body)) {
		return fmt.Errorf("body is missing a fenced ```yaml recommendations block — see docs/SPIKE_CONTRACT.md")
	}

	// Check 2: parse and validate YAML schema
	art, err := ParseArtifactBytes([]byte(body))
	if err != nil {
		return fmt.Errorf("yaml recommendations block is invalid: %w", err)
	}
	if err := ValidateSchema(art); err != nil {
		return fmt.Errorf("yaml recommendations failed schema validation: %w", err)
	}

	// Check 3: Path declaration heading
	if !pathDeclarationRe.MatchString(body) {
		return fmt.Errorf("body is missing a spike contract path declaration — expected a heading like:\n  ## Spike Contract (Path A)\nsee docs/SPIKE_CONTRACT.md")
	}

	// Check 4: artifact path reference
	if !artifactPathRe.MatchString(body) {
		return fmt.Errorf("body is missing a well-formed artifact path matching docs/(spikes|decisions)/NNN-slug.md — see docs/SPIKE_CONTRACT.md")
	}

	return nil
}
