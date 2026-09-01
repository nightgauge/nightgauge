// Package triage is the record contract for ad-hoc failure triage (#1262):
// what an investigation of a red check must have established before it is
// allowed to call itself done.
//
// It exists because the investigation it models had already gone wrong twice
// before it went right. A nightly E2E sweep was red on every run for five
// weeks. Two sessions produced confident diagnoses derived from reading source
// rather than observing the running system, and both shipped code encoding
// those guesses — a magic-link redelivery retry, and a cold-restart probe that
// reported "no session was persisted" about a session that had in fact
// persisted. The probe was not merely useless; it actively misdirected the
// following session for its entire duration.
//
// The common shape of both failures is a report that names only the winning
// hypothesis. A diagnosis that explains the symptom is cheap — several always
// do — and the one that survives contact with the system is the one whose
// rivals were ruled OUT by something observed. So the record's central
// requirement is not "state a cause"; it is "name a hypothesis you killed, and
// the observation that killed it".
//
// Everything here is deliberately mechanical. A prose rule saying "observe
// before you fix" is what the skills already said, in effect, and it did not
// hold, because nothing could tell a grounded report from a plausible one. A
// schema can: it can require the falsification, require an explicit answer to
// "has this check ever passed", and refuse a fix on an investigation that never
// reproduced anything.
package triage

import (
	"fmt"
	"strings"
	"time"
)

// SchemaVersion is the triage-record schema version.
const SchemaVersion = 1

// Reproduction status values.
const (
	// ReproLocal — the failure was reproduced on this machine.
	ReproLocal = "local"
	// ReproCI — the failure was reproduced in CI (a re-run, an instrumented
	// branch). Weaker than local, and still a reproduction.
	ReproCI = "ci"
	// ReproNone — nothing reproduced it. This is a legitimate outcome and a
	// terminal one: an investigation that cannot make the failure happen has
	// no way to tell a fix from a coincidence, so it files a spike and stops.
	ReproNone = "none"
)

// Hypothesis verdicts.
const (
	// VerdictFalsified — ruled out by an observation, which must be named.
	VerdictFalsified = "falsified"
	// VerdictSupported — the surviving explanation.
	VerdictSupported = "supported"
	// VerdictUntested — considered and not investigated. Recorded honestly;
	// it satisfies nothing.
	VerdictUntested = "untested"
)

// Target is what the operator handed over: a check name, a workflow run URL, or
// a workflow file path.
type Target struct {
	Kind  string `json:"kind"` // check | run-url | workflow-file
	Value string `json:"value"`
	Repo  string `json:"repo"` // owner/repo the check belongs to
}

// History is the answer to "has this check ever been green?".
//
// It is a required field rather than an optional note because the wrong answer
// silently reframes the whole investigation. A check that has never passed is
// not a regression, and looking for "what changed" is then a search with no
// target — which is precisely how a session burns itself out bisecting a
// history in which nothing was ever different.
type History struct {
	// EverPassed is false when no passing run exists in the window examined.
	EverPassed bool `json:"ever_passed"`
	// Checked records that the question was actually asked. A record that
	// leaves this false has not answered it, and validation says so.
	Checked bool `json:"checked"`
	// Detail is how it was determined (the query, the window, the run count).
	Detail string `json:"detail,omitempty"`
}

// Reproduction is how, and whether, the failure was made to happen on demand.
type Reproduction struct {
	Status  string `json:"status"` // local | ci | none
	Command string `json:"command,omitempty"`
	// Evidence is what was observed that makes this a reproduction rather than
	// a hope — the failing output, the run URL, the assertion that tripped.
	Evidence string `json:"evidence,omitempty"`
	// Attempts is what was tried when Status is none. It is the substance of
	// the spike this record then files; without it the next session repeats
	// every one of them.
	Attempts []string `json:"attempts,omitempty"`
}

// Hypothesis is one candidate explanation and what became of it.
type Hypothesis struct {
	Statement string `json:"statement"`
	Verdict   string `json:"verdict"`
	// Observation is what was seen. Required for a falsified hypothesis: "I
	// decided it was not that" is the move this whole package exists to
	// prevent, and it is indistinguishable from a real ruling-out unless the
	// observation is written down.
	Observation string `json:"observation,omitempty"`
}

// Fix is the change, and the honest state of its test.
type Fix struct {
	Landed bool   `json:"landed"`
	Branch string `json:"branch,omitempty"`
	PR     string `json:"pr,omitempty"`
	// Test names the test that fails without the fix.
	Test string `json:"test,omitempty"`
	// TestFailsWithoutFix must be established by actually reverting the fix and
	// watching the test go red. A test that passes either way is decoration,
	// and decoration that ships as coverage is worse than no test, because it
	// tells the next person the case is guarded.
	TestFailsWithoutFix bool `json:"test_fails_without_fix"`
	// NoTestReason is required when there is no such test. Saying plainly that
	// coverage is absent is allowed; implying coverage that does not exist is
	// not.
	NoTestReason string `json:"no_test_reason,omitempty"`
}

// Record is one triage investigation.
type Record struct {
	V            int          `json:"v"`
	ID           string       `json:"id"`
	Target       Target       `json:"target"`
	History      History      `json:"history"`
	Reproduction Reproduction `json:"reproduction"`
	Hypotheses   []Hypothesis `json:"hypotheses"`
	Fix          *Fix         `json:"fix,omitempty"`
	// SpikeIssue is the type:spike filed when nothing reproduced.
	SpikeIssue string `json:"spike_issue,omitempty"`
	// TrackingIssue is the issue the landed fix is tracked by, so the work
	// lives somewhere other than a session transcript.
	TrackingIssue string `json:"tracking_issue,omitempty"`
	CreatedAt     string `json:"created_at"`
	// Notes is free prose. It is last, and nothing validates against it, on
	// purpose: a narrative field that could satisfy a requirement would become
	// the field every requirement was satisfied in.
	Notes string `json:"notes,omitempty"`
}

// Violation is one way a record fails the contract.
type Violation struct {
	Field   string
	Message string
}

func (v Violation) String() string { return v.Field + ": " + v.Message }

// Validate returns every way the record falls short. An empty slice means the
// investigation met the discipline bar; it does not mean the diagnosis is
// right, which no schema can tell.
func (r Record) Validate() []Violation {
	var out []Violation
	add := func(field, msg string) { out = append(out, Violation{Field: field, Message: msg}) }

	if r.V != SchemaVersion {
		add("v", fmt.Sprintf("schema version must be %d", SchemaVersion))
	}
	if strings.TrimSpace(r.ID) == "" {
		add("id", "required")
	}
	if strings.TrimSpace(r.Target.Value) == "" {
		add("target.value", "required — name the check, run URL or workflow file under investigation")
	}
	if strings.TrimSpace(r.Target.Repo) == "" {
		add("target.repo", "required — a check name means nothing without the repo it belongs to")
	}

	if !r.History.Checked {
		add("history.checked", "required — a check that has never passed is not a regression, and treating it as one sends the investigation looking for a change that does not exist")
	}

	switch r.Reproduction.Status {
	case ReproLocal, ReproCI:
		if strings.TrimSpace(r.Reproduction.Evidence) == "" {
			add("reproduction.evidence", "required — say what was observed, or this is a claim of reproduction rather than a reproduction")
		}
	case ReproNone:
		if len(r.Reproduction.Attempts) == 0 {
			add("reproduction.attempts", "required when nothing reproduced — without it the next session repeats every attempt this one made")
		}
		if r.Fix != nil {
			add("fix", "an investigation that never reproduced the failure may not propose a fix; file the spike and stop")
		}
		if strings.TrimSpace(r.SpikeIssue) == "" {
			add("spike_issue", "required when nothing reproduced — the dead end has to land somewhere the next session will find it")
		}
	default:
		add("reproduction.status", fmt.Sprintf("must be %q, %q or %q", ReproLocal, ReproCI, ReproNone))
	}

	var falsified, supported int
	for i, h := range r.Hypotheses {
		field := fmt.Sprintf("hypotheses[%d]", i)
		if strings.TrimSpace(h.Statement) == "" {
			add(field+".statement", "required")
		}
		switch h.Verdict {
		case VerdictFalsified:
			falsified++
			if strings.TrimSpace(h.Observation) == "" {
				add(field+".observation",
					"a falsified hypothesis must name the observation that killed it — deciding it was not that is the move this record exists to prevent")
			}
		case VerdictSupported:
			supported++
			if strings.TrimSpace(h.Observation) == "" {
				add(field+".observation", "required — cite what was seen, not what was inferred from reading the source")
			}
		case VerdictUntested:
		default:
			add(field+".verdict", fmt.Sprintf("must be %q, %q or %q", VerdictFalsified, VerdictSupported, VerdictUntested))
		}
	}

	// The central requirement, and the only one that cannot be satisfied by
	// writing more prose.
	if r.Reproduction.Status != ReproNone && falsified == 0 {
		add("hypotheses",
			"at least one hypothesis must be marked falsified with the observation that ruled it out — a report naming only the winning explanation is indistinguishable from a plausible guess, which is what shipped twice before")
	}
	if r.Fix != nil && supported == 0 {
		add("hypotheses", "a fix requires a supported hypothesis saying what the fix addresses")
	}

	if r.Fix != nil {
		if r.Fix.TestFailsWithoutFix {
			if strings.TrimSpace(r.Fix.Test) == "" {
				add("fix.test", "required when the fix claims a test that fails without it")
			}
		} else if strings.TrimSpace(r.Fix.NoTestReason) == "" {
			add("fix.no_test_reason",
				"required — either a test goes red without the fix, or say plainly that there is none; implying coverage that does not exist is worse than admitting none")
		}
		if r.Fix.Landed && strings.TrimSpace(r.TrackingIssue) == "" {
			add("tracking_issue", "required for a landed fix — otherwise the work exists only in a session transcript")
		}
	}
	return out
}

// NewID builds a stable, filesystem-safe record id from a target and a time.
func NewID(target string, at time.Time) string {
	slug := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-':
			return r
		case r >= 'A' && r <= 'Z':
			return r + 32
		case r == ' ', r == '_', r == '/', r == '.', r == ':':
			return '-'
		}
		return -1
	}, target)
	for strings.Contains(slug, "--") {
		slug = strings.ReplaceAll(slug, "--", "-")
	}
	slug = strings.Trim(slug, "-")
	if slug == "" {
		slug = "triage"
	}
	if len(slug) > 60 {
		slug = strings.Trim(slug[:60], "-")
	}
	return slug + "-" + at.UTC().Format("20060102T150405Z")
}
