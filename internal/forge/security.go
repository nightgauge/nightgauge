package forge

import (
	"context"

	forgetypes "github.com/nightgauge/nightgauge/internal/forge/types"
)

// MaxSecurityAlertsPerRequest bounds one ListOpenAlerts call.
//
// The service is contractually SINGLE-REQUEST: it runs inside the attention
// sweep's shared 30-second budget alongside every other producer, so a
// paginating implementation would spend another producer's time. A repository
// holding more open alerts than this gets SecurityAlerts.Truncated set rather
// than a second round trip — under-reporting loudly beats starving the sweep.
const MaxSecurityAlertsPerRequest = 100

// SecurityService is the forge-agnostic surface for dependency security
// advisories (GitHub Dependabot alerts; GitLab dependency scanning when it
// lands — tracked on #343).
//
// The contract has three parts, and each exists because a caller downstream
// cannot work without it:
//
//   - The advisory's OWN fields come back, not an inference. Severity,
//     identifier, package, manifest, vulnerable range and first patched
//     version are the advisory's; nothing here is derived from a label.
//
//   - Scanning-disabled is a STATUS, not an error. See the design note on
//     forgetypes.SecurityAlerts. "Zero open alerts" and "nobody is looking"
//     are different facts and no caller should have to read an error string to
//     tell them apart.
//
//   - A failure to READ the alerts is an error wrapping forge.ErrUnauthorized
//     or forge.ErrPermissionDenied. Implementations must not answer an
//     under-scoped token with an empty list: a security surface that silently
//     reports "clean" when it was never allowed to look is worse than one that
//     reports nothing at all.
type SecurityService interface {
	// ListOpenAlerts returns every OPEN security alert the forge reports for
	// owner/repo, in a single request.
	ListOpenAlerts(ctx context.Context, owner, repo string) (*forgetypes.SecurityAlerts, error)
}
