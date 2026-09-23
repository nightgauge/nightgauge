// Package credshape recognises strings shaped like the credentials Nightgauge
// handles: GitHub tokens and the license keys the platform issues.
//
// It is the one list both the config loader (which refuses a credential-shaped
// value where only an environment-variable name belongs) and `nightgauge
// doctor`'s tracked-file scan read, so the two cannot disagree about what a
// credential looks like.
package credshape

import (
	"regexp"

	"github.com/nightgauge/nightgauge/internal/platform"
)

// Pattern is one credential shape. Prefix is what a redacted report shows,
// followed by "…"; nothing else of a match is ever reported.
type Pattern struct {
	Name   string
	Prefix string
	re     *regexp.Regexp
}

// Count returns the number of non-overlapping matches of p in s.
func (p Pattern) Count(s string) int {
	return len(p.re.FindAllStringIndex(s, -1))
}

// patterns: GitHub's token prefixes and the license-key prefixes from the
// platform's key parser (platform.LicenseKeyPrefixes).
//
// No leading word boundary: a token glued to an identifier
// (`GH_TOKEN_ghp_…`) is still a token. The minimum body length (issued GitHub
// tokens carry 36 base62 characters, fine-grained ones 82) is what keeps prose
// such as "ghp_example" from matching.
var patterns = build()

func build() []Pattern {
	var out []Pattern
	for _, p := range []string{"ghp_", "gho_", "ghu_", "ghs_", "ghr_"} {
		out = append(out, Pattern{
			Name:   "github-token",
			Prefix: p,
			re:     regexp.MustCompile(regexp.QuoteMeta(p) + `[A-Za-z0-9]{20,}`),
		})
	}
	out = append(out, Pattern{
		Name:   "github-fine-grained-token",
		Prefix: "github_pat_",
		re:     regexp.MustCompile(`github_pat_[A-Za-z0-9_]{22,}`),
	})
	for _, p := range platform.LicenseKeyPrefixes() {
		out = append(out, Pattern{
			Name:   "nightgauge-license-key",
			Prefix: p,
			re:     regexp.MustCompile(regexp.QuoteMeta(p) + `[A-Za-z0-9_-]{16,}`),
		})
	}
	return out
}

// Patterns returns the credential shapes, in a stable order.
func Patterns() []Pattern {
	return append([]Pattern(nil), patterns...)
}

// Contains reports whether s contains anything shaped like a credential.
func Contains(s string) bool {
	for _, p := range patterns {
		if p.re.MatchString(s) {
			return true
		}
	}
	return false
}
