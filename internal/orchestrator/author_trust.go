// Package orchestrator — author_trust.go implements the fail-closed
// author-trust boundary for the autonomous pipeline (#270). A stranger's
// issue on a public repo has no privilege to reach refinement or dispatch
// purely because it landed on the board — author identity must be
// affirmatively trusted at every entry point.
package orchestrator

import "strings"

// defaultTrustedAssociations is the built-in trust set used when
// autonomous.trusted_author_associations is unset in config. These are the
// GitHub author_association values that imply write access to the repo.
var defaultTrustedAssociations = map[string]bool{
	"OWNER":        true,
	"MEMBER":       true,
	"COLLABORATOR": true,
}

// isTrustedAuthor reports whether authorAssociation is trusted for
// autonomous processing (refinement, promotion, dispatch). Fails closed:
// empty, unknown, or unrecognized values (CONTRIBUTOR, FIRST_TIME_CONTRIBUTOR,
// FIRST_TIMER, NONE, MANNEQUIN, and any future GitHub value) are untrusted.
//
// When configured is non-empty, it fully overrides the default set — this is
// the autonomous.trusted_author_associations escape hatch documented in
// docs/CONFIGURATION.md.
func isTrustedAuthor(authorAssociation string, configured []string) bool {
	assoc := strings.ToUpper(strings.TrimSpace(authorAssociation))
	if assoc == "" {
		return false
	}
	if len(configured) == 0 {
		return defaultTrustedAssociations[assoc]
	}
	for _, c := range configured {
		if strings.EqualFold(strings.TrimSpace(c), assoc) {
			return true
		}
	}
	return false
}
