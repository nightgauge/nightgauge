package forge

import (
	"context"

	forgetypes "github.com/nightgauge/nightgauge/internal/forge/types"
)

// MaxSecurityAlertsPerRequest bounds how many alerts one ListOpenAlerts call
// enumerates.
//
// The service is contractually SINGLE-REQUEST for the alert list: it runs
// inside the attention sweep's shared 30-second budget alongside every other
// producer, so a paginating implementation would spend another producer's time.
// A repository holding more open alerts than this gets SecurityAlerts.Truncated
// set rather than a second round trip — under-reporting loudly beats starving
// the sweep, and a caller that reads Truncated cannot mistake the page for the
// whole set.
const MaxSecurityAlertsPerRequest = 100

// SecurityService is the forge-agnostic surface for dependency security
// advisories (GitHub Dependabot alerts; GitLab dependency scanning when it
// lands — tracked on #343).
//
// The contract has four parts, and each exists because a caller downstream
// cannot work without it:
//
//   - The advisory's OWN fields come back, not an inference. Severity,
//     identifier, package, manifest, vulnerable range and first patched
//     version are the advisory's; nothing here is derived from a label.
//
//   - Every alert carries its own deep link in SecurityAlert.URL. It is the
//     page that names this repository, this manifest and this alert, and offers
//     a dismiss — and for the alerts with no remediation PR it is the ONLY
//     useful destination, because the advisory URL is a public database page
//     that mentions none of the three. An implementation that cannot obtain the
//     link from its forge must construct it; leaving it empty silently
//     downgrades the card class this surface exists for.
//
//   - Scanning-disabled is a STATUS, not an error. See the design note on
//     forgetypes.SecurityAlerts. "Zero open alerts" and "nobody is looking"
//     are different facts and no caller should have to read an error string to
//     tell them apart.
//
//   - A failure to READ the alerts is an error wrapping forge.ErrUnauthorized
//     or forge.ErrPermissionDenied, and the denial must be one the FORGE
//     reported. Implementations must not answer an under-scoped token with an
//     empty list: a security surface that silently reports "clean" when it was
//     never allowed to look is worse than one that reports nothing at all. Nor
//     may they INFER a denial from a role, a scope guess, or any other proxy —
//     these sentinels are read by the attention sweep as repo-wide facts, so a
//     wrong guess inside one adapter degrades surfaces that never called it.
//     When an answer is ambiguous and no authoritative check is available,
//     return a plain error: "I could not confirm" is a real answer and it fails
//     closed without asserting something the forge never said.
type SecurityService interface {
	// ListOpenAlerts returns every OPEN security alert the forge reports for
	// owner/repo, up to MaxSecurityAlertsPerRequest, setting Truncated when the
	// forge holds more.
	ListOpenAlerts(ctx context.Context, owner, repo string) (*forgetypes.SecurityAlerts, error)
}
