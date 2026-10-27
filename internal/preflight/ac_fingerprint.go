package preflight

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// NormalizeACText is the comparison form of one criterion (#2623): trimmed,
// with internal whitespace collapsed. Fingerprint and coverage both use it,
// so a re-wrapped line is not a scope change but any word edit is.
func NormalizeACText(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// ACFingerprint identifies the acceptance-criteria scope of an issue body
// (#2623): sha256 over the ordered, normalized checkbox texts parseACs finds.
// Checkbox state is excluded — ticking a box does not change the scope.
func ACFingerprint(body string) string {
	h := sha256.New()
	for _, ac := range parseACs(body) {
		h.Write([]byte(NormalizeACText(ac.text)))
		h.Write([]byte{0})
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil))
}

// ACCount returns the number of checkbox criteria parseACs finds in body.
func ACCount(body string) int {
	return len(parseACs(body))
}

// UncoveredACs returns the unchecked criteria in body whose normalized text
// is not among rec's criteria, in body order (#2623). A nil rec covers
// nothing, so every unchecked criterion is returned. Checked criteria are
// never uncovered: the issue already records them as done.
func UncoveredACs(body string, rec *ACReconcileResult) []string {
	covered := map[string]bool{}
	if rec != nil {
		for _, c := range rec.AcceptanceCriteria {
			covered[NormalizeACText(c.Text)] = true
		}
	}
	var out []string
	for _, ac := range parseACs(body) {
		if ac.state == "checked" {
			continue
		}
		if !covered[NormalizeACText(ac.text)] {
			out = append(out, ac.text)
		}
	}
	return out
}
