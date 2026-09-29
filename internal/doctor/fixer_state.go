package doctor

import (
	"context"
	"fmt"
	"strings"
)

// The Fixer's current state, for callers that keep one Fixer across several
// requests (the IPC `doctor.*` methods). The current state is the last scan
// with every check a recheck or a remedy's verification re-ran replaced by
// that re-run: a fingerprint is "produced by the current scan" exactly when
// this state reports it.

// current returns each check's latest result in registration order: a
// re-run when there is one, else the scan's. A check neither scanned nor
// re-run is absent.
func (fx *Fixer) current() []CheckResult {
	scanned := make(map[string]CheckResult, len(fx.results))
	for _, r := range fx.results {
		scanned[r.ID] = r
	}
	var out []CheckResult
	for _, id := range fx.Registry.IDs() {
		if r, ok := fx.latest[id]; ok {
			out = append(out, r)
		} else if r, ok := scanned[id]; ok {
			out = append(out, r)
		}
	}
	return out
}

// State is the current state as JSON v2, restricted to the findings opts
// selects (its Only and Severities; nothing else in opts is read). The
// derived fields are recomputed from the selected findings.
func (fx *Fixer) State(opts FixOptions) DoctorResult {
	cur := fx.current()
	selected := make([]CheckResult, len(cur))
	for i, r := range cur {
		r.Findings = nil
		for _, f := range cur[i].Findings {
			if opts.selects(f) {
				r.Findings = append(r.Findings, f)
			}
		}
		selected[i] = r
	}
	res := BuildResult(selected)
	fx.Env.mu.Lock()
	res.InstallInstructions = fx.Env.install
	res.Adapters = fx.Env.adapter
	fx.Env.mu.Unlock()
	return res
}

// CheckFor returns the check whose current result reports fingerprint.
func (fx *Fixer) CheckFor(fingerprint string) (string, bool) {
	for _, r := range fx.current() {
		if hasFingerprint(r, fingerprint) {
			return r.ID, true
		}
	}
	return "", false
}

// CheckForCode resolves a finding code to its owning check: the check that
// currently reports the code, else the check whose primary code it is.
func (fx *Fixer) CheckForCode(code string) (string, bool) {
	for _, r := range fx.current() {
		for _, f := range r.Findings {
			if strings.EqualFold(f.Code, code) {
				return r.ID, true
			}
		}
	}
	for _, c := range fx.Registry.checks {
		if strings.EqualFold(c.Code, code) {
			return c.ID, true
		}
	}
	return "", false
}

// PreviewFingerprint is ApplyFingerprint's dry run: it re-runs the check,
// returns `stale` for a fingerprint the check no longer reports, and
// otherwise previews the remedy without consent, precondition or apply.
func (fx *Fixer) PreviewFingerprint(ctx context.Context, checkID, fingerprint, remedyID string) FixResult {
	cur, ok := fx.Recheck(ctx, checkID)
	var f *Finding
	if ok {
		for i := range cur.Findings {
			if cur.Findings[i].Fingerprint == fingerprint {
				f = &cur.Findings[i]
			}
		}
	}
	if f == nil {
		return FixResult{
			Finding: Finding{Check: checkID, Fingerprint: fingerprint, Evidence: map[string]string{}, Remedies: []Remedy{}},
			Action:  ActionPreviewed, Outcome: OutcomeStale,
			Detail: fmt.Sprintf("%s no longer reports fingerprint %s", checkID, fingerprint),
		}
	}
	var rem *Remedy
	for i := range f.Remedies {
		if f.Remedies[i].ID == remedyID {
			rem = &f.Remedies[i]
		}
	}
	if rem == nil {
		return newFixResult(*f, nil, ActionPreviewed, OutcomeBlocked, "", fmt.Sprintf("the finding declares no remedy %q", remedyID))
	}
	if rem.Kind == RemedyManual {
		return newFixResult(*f, rem, ActionManual, OutcomeSkipped, "", "only the operator can act on this finding")
	}
	preview := rem.Preview
	verb, registered := fx.Verbs.Lookup(rem.Verb)
	if registered {
		if p, err := verb.Preview(ctx, *f); err == nil && p != "" {
			preview = p
		}
	}
	detail := ""
	switch {
	case !registered:
		detail = fmt.Sprintf("verb %q is not registered: applying would refuse this remedy", rem.Verb)
	case rem.Kind == RemedyConfirm:
		detail = "a confirm remedy: applying it needs explicit consent"
	}
	return newFixResult(*f, rem, ActionPreviewed, "", preview, detail)
}
