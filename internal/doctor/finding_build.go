package doctor

import (
	"sort"
	"strconv"
	"strings"
)

// Helpers native checks use to build ADR-025 findings. A native check emits
// one finding per affected object, keyed by a fingerprint over the evidence
// keys that name that object, never over counts or ages.

// newFinding builds a finding whose fingerprint is Fingerprint(code, identity...).
func newFinding(check, code string, sev Severity, title, cause string,
	evidence map[string]string, identity []string, remedies ...Remedy) Finding {
	if evidence == nil {
		evidence = map[string]string{}
	}
	if remedies == nil {
		remedies = []Remedy{}
	}
	return Finding{
		Code:        code,
		Check:       check,
		Severity:    sev,
		Title:       title,
		Cause:       cause,
		Evidence:    evidence,
		Docs:        DocsAnchor(code),
		Fingerprint: Fingerprint(code, append([]string{check}, identity...)...),
		Remedies:    remedies,
	}
}

// manualRemedy is an operator-only remedy: no verb, steps instead.
func manualRemedy(id, summary, verify string, steps ...string) Remedy {
	return Remedy{ID: id, Kind: RemedyManual, Summary: summary, Verify: verify, Steps: steps}
}

// unverifiableFinding reports a scan that could not run. It is never a pass:
// a clean report from a scan that never ran is an assertion about nothing
// (#296, #323). The remedy says not to act on the absence of findings.
func unverifiableFinding(check, code string, sev Severity, subject, reason string) Finding {
	return newFinding(check, code, sev,
		subject+" unverifiable",
		"the scan could not run, so the absence of findings here is not evidence of health: "+reason,
		map[string]string{"state": "unverifiable", "reason": reason},
		[]string{"unverifiable"},
		manualRemedy("investigate", "Make the scan runnable, then re-run doctor", check,
			"Run `nightgauge doctor` from inside the repository or workspace it should inspect",
			"Do not run a cleanup command on the strength of this report"))
}

// days renders a whole-day age for evidence.
func days(n int) string { return strconv.Itoa(n) + "d" }

// findingsText renders findings as one line each — title, cause, evidence and
// remedies — for logs and for tests that assert on what a report names.
func findingsText(fs []Finding) string {
	parts := make([]string, 0, len(fs))
	for _, f := range fs {
		keys := make([]string, 0, len(f.Evidence))
		for k := range f.Evidence {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		vals := make([]string, 0, len(keys))
		for _, k := range keys {
			vals = append(vals, k+"="+f.Evidence[k])
		}
		line := f.Code + " " + f.Title + " — " + f.Cause + " {" + strings.Join(vals, ", ") + "}"
		for _, r := range f.Remedies {
			line += " [" + string(r.Kind) + ": " + r.Summary
			if r.Preview != "" {
				line += "; " + r.Preview
			}
			if len(r.Steps) > 0 {
				line += "; " + strings.Join(r.Steps, "; ")
			}
			line += "]"
		}
		parts = append(parts, line)
	}
	return strings.Join(parts, "; ")
}
