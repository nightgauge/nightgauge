package forgetypes

// Forge-neutral security-advisory shapes (issue #343).
//
// These carry the ADVISORY'S OWN facts — severity, identifier, package,
// manifest, vulnerable range, first patched version — rather than anything
// Nightgauge inferred from a label. A critical RCE and a low-severity ReDoS
// are different objects here, which is the whole point: nothing downstream can
// rank, threshold, or escalate a fact it never read.

// SecurityAlertsStatus reports whether the forge is scanning this repository at
// all. It is a COVERAGE fact, not a failure, and is deliberately modelled as a
// value rather than an error: "the feature is off" is a normal, actionable
// answer that every caller must be able to read without entering an error path
// (see the design note on SecurityAlerts).
type SecurityAlertsStatus string

const (
	// SecurityAlertsEnabled — the forge scans this repository. Alerts is the
	// complete open set (subject to Truncated).
	SecurityAlertsEnabled SecurityAlertsStatus = "enabled"

	// SecurityAlertsDisabled — the repository has dependency scanning turned
	// off. Alerts is empty and that emptiness means NOTHING about the
	// repository's actual exposure: nobody looked.
	SecurityAlertsDisabled SecurityAlertsStatus = "disabled"
)

// AlertSeverity is the advisory's own severity, normalised to lower case so a
// GitHub `HIGH` and a hypothetical GitLab `high` compare equal. UNKNOWN is a
// real value the forge emits, not a parse failure, and is preserved as such.
type AlertSeverity string

const (
	AlertSeverityUnknown  AlertSeverity = "unknown"
	AlertSeverityLow      AlertSeverity = "low"
	AlertSeverityModerate AlertSeverity = "moderate"
	AlertSeverityHigh     AlertSeverity = "high"
	AlertSeverityCritical AlertSeverity = "critical"
)

// Rank orders severities most-severe-first when negated, and gives an unknown
// severity a defined slot instead of sorting it arbitrarily. Callers use it to
// present the worst alert first; it is never part of a fingerprint.
func (s AlertSeverity) Rank() int {
	switch s {
	case AlertSeverityCritical:
		return 4
	case AlertSeverityHigh:
		return 3
	case AlertSeverityModerate:
		return 2
	case AlertSeverityLow:
		return 1
	default:
		return 0
	}
}

// RemediationState is the tri-state answer to "can this be fixed by merging
// something the forge already prepared?".
//
// A boolean would be wrong. "There is a PR" and "there is no PR" are not
// complements: the third case — the forge tried, and reports that no
// non-vulnerable version can be reached from this manifest — is the class of
// alert that nothing in the pipeline reported before this existed, and it needs
// a completely different operator action from either of the other two.
type RemediationState string

const (
	// RemediationPROpen — the forge has an open pull/merge request that fixes
	// this alert. PRNumber and PRURL name it.
	RemediationPROpen RemediationState = "pr_open"

	// RemediationNotPossible — the forge attempted an update and reports it
	// cannot reach a non-vulnerable version. Reason/ReasonDetail carry the
	// forge's own words for why. This is the alert that needs a human most and
	// is the least likely to be noticed.
	RemediationNotPossible RemediationState = "not_possible"

	// RemediationNone — the forge reports no update attempt for this alert at
	// all: no PR, no failure. Distinct from NotPossible because "nobody tried"
	// and "trying is futile" lead to different next steps.
	RemediationNone RemediationState = "none"
)

// Remediation describes what the forge has already done, or explicitly cannot
// do, about one alert.
type Remediation struct {
	State RemediationState `json:"state"`

	// PRNumber / PRURL / PRTitle identify the remediation pull request. Set
	// only when State is RemediationPROpen.
	PRNumber int    `json:"pr_number,omitempty"`
	PRURL    string `json:"pr_url,omitempty"`
	PRTitle  string `json:"pr_title,omitempty"`

	// Reason is the forge's machine-readable failure code (GitHub emits e.g.
	// "security_update_not_possible"). Set only when State is
	// RemediationNotPossible.
	Reason string `json:"reason,omitempty"`
	// ReasonDetail is the forge's own one-line explanation. Rendered verbatim
	// so the card states what was observed rather than what it implies.
	ReasonDetail string `json:"reason_detail,omitempty"`
}

// SecurityAlert is one open advisory against one dependency of one repository.
type SecurityAlert struct {
	// Number is the forge's per-repository alert number.
	Number int `json:"number"`
	// URL deep-links the alert on the forge.
	URL string `json:"url,omitempty"`

	// Severity is the advisory's severity, not an inference from a label.
	Severity AlertSeverity `json:"severity"`
	// AdvisoryID is the forge's advisory identifier (GHSA on GitHub).
	AdvisoryID string `json:"advisory_id,omitempty"`
	// CVE is the CVE identifier when the advisory carries one.
	CVE string `json:"cve,omitempty"`
	// Summary is the advisory's one-line title.
	Summary string `json:"summary,omitempty"`
	// AdvisoryURL links the advisory itself (as opposed to this repository's
	// alert about it).
	AdvisoryURL string `json:"advisory_url,omitempty"`

	// Package / Ecosystem name the vulnerable dependency.
	Package   string `json:"package"`
	Ecosystem string `json:"ecosystem,omitempty"`
	// ManifestPath is the file that pulls the vulnerable version in. In a
	// monorepo this is the difference between "our shipped binary" and "one
	// skill's dev lockfile", so it belongs on the card.
	ManifestPath string `json:"manifest_path,omitempty"`
	// Scope is the forge's dependency scope, normalised to lower case
	// ("runtime" / "development" on GitHub). Empty when unreported.
	Scope string `json:"scope,omitempty"`
	// Relationship is "direct" or "transitive", normalised to lower case.
	// Empty when unreported.
	Relationship string `json:"relationship,omitempty"`

	// VulnerableRange is the advisory's own affected-version expression.
	VulnerableRange string `json:"vulnerable_range,omitempty"`
	// FirstPatchedVersion is the earliest fixed release, or empty when the
	// advisory has no published fix yet.
	FirstPatchedVersion string `json:"first_patched_version,omitempty"`

	// FirstSeenAt is when the forge first raised the alert, RFC3339. It is the
	// forge's own timestamp so a grace window costs no local state.
	FirstSeenAt string `json:"first_seen_at,omitempty"`

	// Remediation is the tri-state remediation answer. Never a bare bool.
	Remediation Remediation `json:"remediation"`
}

// SecurityAlerts is one repository's open-alert answer.
//
// Design note — why Status is a FIELD and not a sentinel error (issue #343):
//
// Three outcomes must be told apart by a caller, none of them by inspecting an
// error message:
//
//  1. scanning on, nothing open → Status == SecurityAlertsEnabled, len(Alerts) == 0
//  2. scanning off for this repo → Status == SecurityAlertsDisabled
//  3. the token cannot read alerts → a non-nil error wrapping
//     forge.ErrPermissionDenied / forge.ErrUnauthorized
//
// (1) and (2) are both successful observations — one says "clean", the other
// says "unmeasured" — so they belong in the same value, and a caller that only
// wants the alert list stays on the happy path for both. (3) is genuinely a
// failure to observe, and it must stay an error because the attention sweep
// keys its "do not retract live cards" behaviour off exactly those sentinels.
// Modelling (2) as an error would drag a normal coverage answer through that
// same machinery and make "the feature is off" indistinguishable from "the
// scan broke" at every call site.
type SecurityAlerts struct {
	Status SecurityAlertsStatus `json:"status"`
	// Alerts is the open set, empty when Status is SecurityAlertsDisabled.
	Alerts []SecurityAlert `json:"alerts,omitempty"`
	// TotalOpen is the forge's own count of open alerts, which may exceed
	// len(Alerts) — see Truncated.
	TotalOpen int `json:"total_open"`
	// Truncated reports that the forge holds more open alerts than one request
	// returns. The service is deliberately single-request (it runs inside a
	// shared sweep budget), so it says so rather than silently under-reporting.
	Truncated bool `json:"truncated,omitempty"`
}

// Enabled reports whether the forge is scanning this repository. A caller that
// treats an empty Alerts slice as "clean" without checking this is reading
// "nobody looked" as "nothing found".
func (a *SecurityAlerts) Enabled() bool {
	return a != nil && a.Status == SecurityAlertsEnabled
}
