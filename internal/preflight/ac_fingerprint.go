package preflight

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
)

// NormalizeACText is the comparison form of one criterion (#2623): trimmed,
// with internal whitespace collapsed. Fingerprint and coverage both use it,
// so a re-wrapped line is not a scope change but any word edit is.
func NormalizeACText(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// ACFingerprint identifies the acceptance-criteria scope of an issue body
// (#2623): sha256 over the ordered, normalized criterion texts parseACs finds.
// Checkbox state is excluded — ticking a box does not change the scope.
func ACFingerprint(body string) string {
	h := sha256.New()
	for _, ac := range parseACs(body) {
		h.Write([]byte(NormalizeACText(ac.text)))
		h.Write([]byte{0})
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil))
}

// ACCount returns the number of criteria parseACs finds in body.
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

// ACTexts returns the criterion texts parseACs finds in body, in body order,
// one per normalized text (#3228). It is the list ACFingerprint hashes, so a
// caller that rebuilds the criteria from a live body agrees with the
// fingerprint it compared.
func ACTexts(body string) []string {
	var out []string
	seen := map[string]bool{}
	for _, ac := range parseACs(body) {
		key := NormalizeACText(ac.text)
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, ac.text)
	}
	return out
}

// NoACsMarker is the hidden marker on the one note posted when an issue has
// no acceptance criteria (#2906), so the note is posted once per issue.
const NoACsMarker = "<!-- nightgauge:no-acceptance-criteria -->"

// NoACsComment is the zero-criteria note for issueNumber.
func NoACsComment(issueNumber int) string {
	return NoACsMarker + "\n" +
		"Nightgauge found no acceptance criteria in this issue. Add a `## Acceptance Criteria` " +
		"(or `## Verification` / `## Done when`) section with checkbox, numbered or bulleted " +
		fmt.Sprintf("items; without one, the pull request will say `Refs #%d` and this issue ", issueNumber) +
		"will not close on merge.\n"
}
