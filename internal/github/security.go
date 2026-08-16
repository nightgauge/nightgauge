package github

// GitHub implementation of forge.SecurityService — Dependabot alerts (#343).
//
// TRANSPORT CHOICE, AND WHY IT IS NOT THE REST ENDPOINT.
//
// The obvious endpoint is REST `GET /repos/{o}/{r}/dependabot/alerts`. It was
// probed against a live repository before this file was written, and its
// payload does NOT carry the remediation pull request: the object's keys are
// exactly assignees, auto_dismissed_at, created_at, dependency,
// dismissal_request, dismissed_at, dismissed_by, dismissed_comment,
// dismissed_reason, fixed_at, html_url, number, security_advisory,
// security_vulnerability, state, updated_at, url. There is no link from an
// alert to the PR that fixes it, and no statement of why one does not exist.
//
// GraphQL's RepositoryVulnerabilityAlert has `dependabotUpdate`, which carries
// EITHER `pullRequest` (the remediation PR) OR `error` (a typed reason the
// forge cannot produce one, e.g. errorType "security_update_not_possible").
// That is precisely the distinction this producer exists to make, and one
// GraphQL request also returns `hasVulnerabilityAlertsEnabled`, so the
// coverage answer costs no second round trip.
//
// GraphQL has one dangerous property that REST does not, and this file is
// built around defusing it: asked for the alerts of a repository the token
// cannot read them on, GraphQL returns an EMPTY CONNECTION with totalCount 0
// and no error, while REST returns a loud 403. Verified against a public
// repository where the token holds only read access — REST answered
// `403 You are not authorized to perform this operation.` and GraphQL answered
// `{"totalCount":0,"nodes":[]}`. Reporting "no vulnerabilities" for a
// repository nobody was allowed to scan is the worst failure this package
// could have, so the same query also selects `viewerPermission` and an empty
// answer from a non-administrator is returned as forge.ErrPermissionDenied
// rather than as a clean bill of health.

import (
	"context"
	"fmt"
	"strings"

	"github.com/shurcooL/graphql"

	"github.com/nightgauge/nightgauge/internal/forge"
	forgetypes "github.com/nightgauge/nightgauge/internal/forge/types"
)

// SecurityService is the github adapter's implementation of
// forge.SecurityService.
type SecurityService struct {
	client *Client
}

// NewSecurityService wraps a *Client as a SecurityService.
func NewSecurityService(client *Client) *SecurityService {
	return &SecurityService{client: client}
}

// vulnerabilityAlertsQuery is the single request the service issues. Every
// field on it is load-bearing:
//
//   - viewerPermission distinguishes "clean" from "not allowed to look".
//   - hasVulnerabilityAlertsEnabled is the coverage fact.
//   - dependabotUpdate is the remediation fact, unavailable over REST.
//   - totalCount + hasNextPage let a truncated answer say so instead of
//     silently under-reporting.
type vulnerabilityAlertsQuery struct {
	Repository *struct {
		ViewerPermission              *graphql.String
		HasVulnerabilityAlertsEnabled graphql.Boolean
		VulnerabilityAlerts           struct {
			TotalCount graphql.Int
			PageInfo   struct {
				HasNextPage graphql.Boolean
			}
			Nodes []vulnerabilityAlertNode
		} `graphql:"vulnerabilityAlerts(first: $first, states: [OPEN])"`
	} `graphql:"repository(owner: $owner, name: $name)"`
}

type vulnerabilityAlertNode struct {
	Number                 graphql.Int
	CreatedAt              graphql.String
	VulnerableManifestPath graphql.String
	DependencyScope        *graphql.String
	DependencyRelationship *graphql.String

	SecurityAdvisory *struct {
		GhsaID      graphql.String `graphql:"ghsaId"`
		Summary     graphql.String
		Severity    graphql.String
		Permalink   graphql.String
		Identifiers []struct {
			Type  graphql.String
			Value graphql.String
		}
	}

	SecurityVulnerability *struct {
		Severity graphql.String
		Package  struct {
			Name      graphql.String
			Ecosystem graphql.String
		}
		VulnerableVersionRange graphql.String
		FirstPatchedVersion    *struct {
			Identifier graphql.String
		}
	}

	DependabotUpdate *struct {
		PullRequest *struct {
			Number graphql.Int
			URL    graphql.String `graphql:"url"`
			Title  graphql.String
		}
		Error *struct {
			ErrorType graphql.String
			Title     graphql.String
		}
	}
}

// ListOpenAlerts implements forge.SecurityService in one GraphQL request.
func (s *SecurityService) ListOpenAlerts(ctx context.Context, owner, repo string) (*forgetypes.SecurityAlerts, error) {
	if strings.TrimSpace(owner) == "" || strings.TrimSpace(repo) == "" {
		return nil, fmt.Errorf("security alerts: owner and name are required")
	}
	first, err := checkedGraphQLInt("first", forge.MaxSecurityAlertsPerRequest)
	if err != nil {
		return nil, err
	}

	var q vulnerabilityAlertsQuery
	vars := map[string]interface{}{
		"owner": graphql.String(owner),
		"name":  graphql.String(repo),
		"first": first,
	}
	if qerr := s.client.Query(ctx, &q, vars); qerr != nil {
		return nil, securityQueryError(owner, repo, qerr)
	}
	if q.Repository == nil {
		return nil, fmt.Errorf("security alerts %s/%s: %w", owner, repo, forge.ErrNotFound)
	}

	conn := q.Repository.VulnerabilityAlerts
	alerts := make([]forgetypes.SecurityAlert, 0, len(conn.Nodes))
	for i := range conn.Nodes {
		alerts = append(alerts, convertVulnerabilityAlert(&conn.Nodes[i]))
	}

	// The silent-empty guard. An administrator seeing nothing is a clean repo;
	// anyone else seeing nothing may simply have been filtered out by the API,
	// and this surface must never turn that into "no vulnerabilities". The
	// check is scoped to the empty answer on purpose: a token that returned
	// alerts has self-evidently been allowed to read them, whatever its
	// nominal repository permission says.
	if len(alerts) == 0 && !viewerCanReadAlerts(q.Repository.ViewerPermission) {
		return nil, fmt.Errorf(
			"security alerts %s/%s: viewer permission %q cannot read Dependabot alerts (GitHub answers an empty set rather than an error): %w",
			owner, repo, viewerPermissionOrUnknown(q.Repository.ViewerPermission), forge.ErrPermissionDenied)
	}

	out := &forgetypes.SecurityAlerts{
		Status:    forgetypes.SecurityAlertsEnabled,
		Alerts:    alerts,
		TotalOpen: int(conn.TotalCount),
		Truncated: bool(conn.PageInfo.HasNextPage),
	}
	if !bool(q.Repository.HasVulnerabilityAlertsEnabled) {
		// Scanning is off. Report the coverage fact and carry no alerts: an
		// alert list from a repository that is not being scanned would invite
		// a reader to treat its emptiness as evidence.
		out.Status = forgetypes.SecurityAlertsDisabled
		out.Alerts = nil
		out.TotalOpen = 0
		out.Truncated = false
	}
	return out, nil
}

// viewerCanReadAlerts reports whether the viewer's repository permission is one
// GitHub grants Dependabot alert visibility to. Only ADMIN qualifies: GitHub
// scopes the security tab to repository administrators and organisation owners.
//
// A nil permission (the API declined to state one, as it can for some app
// installations) is trusted rather than refused — the guard exists to catch the
// KNOWN under-privileged read, not to invent a new way to fail.
func viewerCanReadAlerts(perm *graphql.String) bool {
	if perm == nil {
		return true
	}
	return strings.EqualFold(string(*perm), "ADMIN")
}

func viewerPermissionOrUnknown(perm *graphql.String) string {
	if perm == nil {
		return "unknown"
	}
	return string(*perm)
}

// securityQueryError translates a GraphQL transport failure into a forge
// sentinel.
//
// This is the ONE place in the security path allowed to look at an error's
// text, and it is the adapter boundary — by design. Callers downstream
// (the sweep, the coverage producer) branch on errors.Is against the sentinels
// this produces, never on prose, so GitHub can reword a message without
// silently changing behaviour anywhere but here.
func securityQueryError(owner, repo string, err error) error {
	base := fmt.Errorf("security alerts %s/%s: %w", owner, repo, err)
	if isRateLimited(err) {
		return fmt.Errorf("%w: %s", forge.ErrRateLimited, base)
	}
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "bad credentials"),
		strings.Contains(msg, "401"),
		strings.Contains(msg, "unauthorized"):
		return fmt.Errorf("%w: %s", forge.ErrUnauthorized, base)
	case strings.Contains(msg, "403"),
		strings.Contains(msg, "forbidden"),
		strings.Contains(msg, "not authorized"):
		return fmt.Errorf("%w: %s", forge.ErrPermissionDenied, base)
	case strings.Contains(msg, "could not resolve to a repository"),
		strings.Contains(msg, "not_found"):
		return fmt.Errorf("%w: %s", forge.ErrNotFound, base)
	default:
		return base
	}
}

// convertVulnerabilityAlert maps one GraphQL node onto the forge-neutral shape,
// normalising GitHub's SCREAMING enums to the lower-case vocabulary the forge
// types declare.
func convertVulnerabilityAlert(n *vulnerabilityAlertNode) forgetypes.SecurityAlert {
	out := forgetypes.SecurityAlert{
		Number:       int(n.Number),
		FirstSeenAt:  string(n.CreatedAt),
		ManifestPath: string(n.VulnerableManifestPath),
		Severity:     forgetypes.AlertSeverityUnknown,
	}
	if n.DependencyScope != nil {
		out.Scope = strings.ToLower(string(*n.DependencyScope))
	}
	if n.DependencyRelationship != nil {
		out.Relationship = strings.ToLower(string(*n.DependencyRelationship))
	}

	if adv := n.SecurityAdvisory; adv != nil {
		out.AdvisoryID = string(adv.GhsaID)
		out.Summary = string(adv.Summary)
		out.AdvisoryURL = string(adv.Permalink)
		out.Severity = normalizeAlertSeverity(string(adv.Severity))
		for _, id := range adv.Identifiers {
			if strings.EqualFold(string(id.Type), "CVE") {
				out.CVE = string(id.Value)
				break
			}
		}
	}

	if vuln := n.SecurityVulnerability; vuln != nil {
		out.Package = string(vuln.Package.Name)
		out.Ecosystem = strings.ToLower(string(vuln.Package.Ecosystem))
		out.VulnerableRange = string(vuln.VulnerableVersionRange)
		if vuln.FirstPatchedVersion != nil {
			out.FirstPatchedVersion = string(vuln.FirstPatchedVersion.Identifier)
		}
		// The per-vulnerability severity is the one that applies to THIS
		// package's affected range; prefer it over the advisory-wide value when
		// the advisory spans several packages at different severities.
		if s := normalizeAlertSeverity(string(vuln.Severity)); s != forgetypes.AlertSeverityUnknown {
			out.Severity = s
		}
	}

	out.Remediation = convertRemediation(n)
	return out
}

// convertRemediation resolves the tri-state. The three branches are exactly the
// three shapes GitHub returns and must never collapse into a boolean.
func convertRemediation(n *vulnerabilityAlertNode) forgetypes.Remediation {
	upd := n.DependabotUpdate
	if upd == nil {
		return forgetypes.Remediation{State: forgetypes.RemediationNone}
	}
	if pr := upd.PullRequest; pr != nil {
		return forgetypes.Remediation{
			State:    forgetypes.RemediationPROpen,
			PRNumber: int(pr.Number),
			PRURL:    string(pr.URL),
			PRTitle:  string(pr.Title),
		}
	}
	if e := upd.Error; e != nil {
		return forgetypes.Remediation{
			State:        forgetypes.RemediationNotPossible,
			Reason:       strings.ToLower(string(e.ErrorType)),
			ReasonDetail: string(e.Title),
		}
	}
	return forgetypes.Remediation{State: forgetypes.RemediationNone}
}

// normalizeAlertSeverity lower-cases GitHub's SecurityAdvisorySeverity enum
// (LOW / MODERATE / HIGH / CRITICAL / UNKNOWN) into the forge vocabulary. An
// unrecognised value maps to unknown rather than being passed through, so a new
// GitHub enum member cannot leak an untyped string into a card.
func normalizeAlertSeverity(s string) forgetypes.AlertSeverity {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "low":
		return forgetypes.AlertSeverityLow
	case "moderate", "medium":
		return forgetypes.AlertSeverityModerate
	case "high":
		return forgetypes.AlertSeverityHigh
	case "critical":
		return forgetypes.AlertSeverityCritical
	default:
		return forgetypes.AlertSeverityUnknown
	}
}
