package github

// GitHub implementation of forge.SecurityService — Dependabot alerts (#343).
//
// TRANSPORT: GraphQL carries the answer; REST is asked exactly one question
// GraphQL cannot answer.
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
// `{"totalCount":0,"nodes":[]}` at HTTP 200. Reporting "no vulnerabilities" for
// a repository nobody was allowed to scan is the worst failure this package
// could have.
//
// WHY THE GUARD ASKS REST INSTEAD OF READING viewerPermission. An earlier
// revision inferred the answer from the viewer's repository role and treated
// anything below ADMIN as a denial. That inference is simply false: GitHub
// documents Dependabot alert access as a TOKEN SCOPE (`security_events`) plus
// whatever access repository administrators have granted, and states that
// "users with write access or higher can assign Dependabot alerts" — which is
// impossible for an alert you cannot see. Live introspection lists five roles
// (`ADMIN, MAINTAIN, WRITE, TRIAGE, READ`), so the inference rejected four of
// them, converting the healthiest possible observation — a genuinely clean
// repository read by a WRITE token — into a permission failure.
//
// REST answers the question outright, so the guard asks it rather than
// guessing, and ONLY on the ambiguous path (scanning on, zero alerts
// returned). A repository that returned alerts has self-evidently been allowed
// to read them, and a repository with scanning switched off is answered before
// the guard runs. The cost is one extra REST GET per sweep of a clean
// repository — a different rate-limit bucket from GraphQL, and the sweep has
// nothing else to do on that path.

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
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
//   - url is the repository's own canonical web URL, which is what makes each
//     alert's deep link constructible (see alertWebURL). It also keeps the link
//     correct on GitHub Enterprise Server, where the host is not github.com.
//   - hasVulnerabilityAlertsEnabled is the coverage fact.
//   - dependabotUpdate is the remediation fact, unavailable over REST.
//   - totalCount + hasNextPage let a truncated answer say so instead of
//     silently under-reporting.
type vulnerabilityAlertsQuery struct {
	Repository *struct {
		URL                           graphql.String `graphql:"url"`
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

// ListOpenAlerts implements forge.SecurityService.
//
// One GraphQL request always; one additional REST request only on the
// ambiguous empty answer (see the file header).
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

	// Scanning off is answered first, and without the guard: the emptiness is
	// already explained, and an alert list from a repository nobody is scanning
	// would invite a reader to treat it as evidence.
	if !bool(q.Repository.HasVulnerabilityAlertsEnabled) {
		return &forgetypes.SecurityAlerts{Status: forgetypes.SecurityAlertsDisabled}, nil
	}

	conn := q.Repository.VulnerabilityAlerts
	repoURL := string(q.Repository.URL)
	alerts := make([]forgetypes.SecurityAlert, 0, len(conn.Nodes))
	for i := range conn.Nodes {
		alerts = append(alerts, convertVulnerabilityAlert(repoURL, &conn.Nodes[i]))
	}

	// The silent-empty guard. GraphQL cannot tell "clean" from "filtered out";
	// REST can, because it answers with a status code rather than an empty set.
	if len(alerts) == 0 {
		if err := s.confirmAlertsAreReadable(ctx, owner, repo); err != nil {
			return nil, err
		}
	}

	return &forgetypes.SecurityAlerts{
		Status:    forgetypes.SecurityAlertsEnabled,
		Alerts:    alerts,
		TotalOpen: int(conn.TotalCount),
		Truncated: bool(conn.PageInfo.HasNextPage),
	}, nil
}

// confirmAlertsAreReadable asks REST whether the token may read this
// repository's Dependabot alerts at all, and returns nil only when the forge
// itself says yes.
//
// The distinction this preserves is the one the whole surface rests on: a
// denial reported here is the FORGE's answer, not an inference of ours, so the
// forge.ErrPermissionDenied it wraps means what its name says. Anything that is
// neither a clear success nor a clear denial (a 404, a 5xx, a transport
// failure) returns a plain error with no sentinel: the empty answer is not
// confirmed, so it must not be reported as a clean repository, but neither may
// this fabricate a specific verdict it did not receive.
func (s *SecurityService) confirmAlertsAreReadable(ctx context.Context, owner, repo string) error {
	body, status, err := s.client.restDoStatus(ctx, http.MethodGet, alertsProbePath(owner, repo), nil)
	if err != nil {
		return fmt.Errorf("security alerts %s/%s: could not confirm the empty answer was a real one: %w", owner, repo, err)
	}
	switch {
	case status >= 200 && status < 300:
		return nil
	case status == http.StatusUnauthorized:
		return fmt.Errorf("security alerts %s/%s: the forge rejected the credential when asked for alerts (REST %d): %w",
			owner, repo, status, forge.ErrUnauthorized)
	case (status == http.StatusForbidden || status == http.StatusTooManyRequests) && restBodyLooksRateLimited(body):
		return fmt.Errorf("security alerts %s/%s: rate limited while confirming the empty answer (REST %d): %w",
			owner, repo, status, forge.ErrRateLimited)
	case status == http.StatusForbidden:
		return fmt.Errorf("security alerts %s/%s: the forge refused to serve this repository's alerts (REST %d: %s) — GraphQL answered an empty set for the same repository, so that emptiness is not evidence of a clean repository: %w",
			owner, repo, status, restErrorSummary(body), forge.ErrPermissionDenied)
	default:
		return fmt.Errorf("security alerts %s/%s: could not confirm the empty answer was a real one (REST %d: %s)",
			owner, repo, status, restErrorSummary(body))
	}
}

// alertsProbePath is the REST endpoint the empty-answer guard hits. One alert
// is requested because only the STATUS is read — the body is never mapped.
func alertsProbePath(owner, repo string) string {
	return fmt.Sprintf("/repos/%s/%s/dependabot/alerts?state=open&per_page=1",
		url.PathEscape(owner), url.PathEscape(repo))
}

// restErrorSummaryMax bounds how much of a REST error body reaches an error
// string, so a pathological response cannot flood a log line.
const restErrorSummaryMax = 200

// restErrorSummary renders a REST error body compactly for an error message.
func restErrorSummary(body []byte) string {
	s := strings.TrimSpace(string(body))
	if s == "" {
		return "empty response body"
	}
	if len(s) > restErrorSummaryMax {
		return s[:restErrorSummaryMax] + "…"
	}
	return s
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
// types declare. repoURL is the repository's canonical web URL, used to build
// the alert's deep link.
func convertVulnerabilityAlert(repoURL string, n *vulnerabilityAlertNode) forgetypes.SecurityAlert {
	out := forgetypes.SecurityAlert{
		Number:       int(n.Number),
		URL:          alertWebURL(repoURL, int(n.Number)),
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

// alertWebURL builds the alert's own deep link — the destination an operator
// needs when there is no remediation PR to send them to, and the only page that
// names this repository, this manifest, and offers a dismiss.
//
// GraphQL's RepositoryVulnerabilityAlert has no url field. Live introspection
// of the type lists autoDismissedAt, createdAt, dependabotUpdate,
// dependencyRelationship, dependencyScope, dismiss*, fixedAt, id, number,
// repository, securityAdvisory, securityVulnerability, state,
// vulnerableManifestFilename, vulnerableManifestPath, vulnerableRequirements —
// and nothing else. REST's alert object does carry it, as html_url, in exactly
// the form `<repository web url>/security/dependabot/<number>` (verified live:
// `https://github.com/nightgauge/nightgauge/security/dependabot/12`).
//
// So the link is derived from the repository URL the SAME GraphQL request
// already returns, rather than bought with a second round trip inside a shared
// sweep budget. Taking the host from the forge's own answer keeps it right on
// GitHub Enterprise Server too.
func alertWebURL(repoURL string, number int) string {
	base := strings.TrimRight(strings.TrimSpace(repoURL), "/")
	if base == "" || number <= 0 {
		return ""
	}
	return fmt.Sprintf("%s/security/dependabot/%d", base, number)
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
