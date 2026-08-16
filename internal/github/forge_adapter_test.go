package github

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/forge"
	forgetypes "github.com/nightgauge/nightgauge/internal/forge/types"
)

// TestForgeAdapter_SatisfiesForgeClient is a runtime echo of the
// compile-time assert in forge_adapter.go. It documents the expected
// type-shape and serves as a discoverable reference for future
// adapters porting from another forge.
func TestForgeAdapter_SatisfiesForgeClient(t *testing.T) {
	client := NewClientWithToken("fake")
	adapter := NewForgeAdapter(client, "nightgauge", 1, OwnerTypeOrg)

	var fc forge.ForgeClient = adapter
	if fc == nil {
		t.Fatal("ForgeAdapter is not assignable to forge.ForgeClient")
	}
}

// TestForgeAdapter_LazyServiceConstruction confirms the lazy/cached
// service construction returns the same instance on repeated access —
// callers can hold onto a sub-service across calls without surprise.
func TestForgeAdapter_LazyServiceConstruction(t *testing.T) {
	client := NewClientWithToken("fake")
	adapter := NewForgeAdapter(client, "nightgauge", 1, OwnerTypeOrg)

	if adapter.Issues() != adapter.Issues() {
		t.Error("Issues() should return the same instance on repeated access")
	}
	if adapter.PRs() != adapter.PRs() {
		t.Error("PRs() should return the same instance on repeated access")
	}
	if adapter.Project() != adapter.Project() {
		t.Error("Project() should return the same instance on repeated access")
	}
	if adapter.Board() != adapter.Board() {
		t.Error("Board() should return the same instance on repeated access")
	}
	if adapter.CI() != adapter.CI() {
		t.Error("CI() should return the same instance on repeated access")
	}
	if adapter.Rulesets() != adapter.Rulesets() {
		t.Error("Rulesets() should return the same instance on repeated access")
	}
}

// TestClient_Forge confirms the *Client.Forge() convenience accessor
// returns a usable ForgeClient.
func TestClient_Forge(t *testing.T) {
	client := NewClientWithToken("fake")

	var fc forge.ForgeClient = client.Forge("nightgauge", 1, OwnerTypeOrg)
	if fc == nil {
		t.Fatal("Client.Forge() returned nil")
	}
	if fc.Issues() == nil {
		t.Error("Forge().Issues() returned nil")
	}
}

// TestForgeAdapter_AuthIsClient documents the design choice (ADR-006)
// that *Client itself satisfies forge.AuthService directly, without a
// wrapper struct. Future contributors who try to add a wrapper will see
// this test fail and find the rationale in the ADR.
func TestForgeAdapter_AuthIsClient(t *testing.T) {
	client := NewClientWithToken("fake")
	adapter := NewForgeAdapter(client, "nightgauge", 1, OwnerTypeOrg)

	auth := adapter.Auth()
	if auth == nil {
		t.Fatal("Auth() returned nil")
	}
	// ADR-006: AuthService is satisfied by *Client itself, so the
	// returned value should be the same client instance (interface
	// equality of underlying pointer).
	if asClient, ok := auth.(*Client); !ok || asClient != client {
		t.Error("Auth() should return the wrapping *Client itself; ADR-006 documents this choice")
	}
}

// --- SecurityService (#343) --------------------------------------------------
//
// These live here rather than in a sibling file so the adapter's newest service
// is exercised next to the assertions that keep the aggregate honest.

// securityStub is a stub GitHub API with BOTH surfaces the service uses: the
// GraphQL endpoint that carries the answer, and the REST dependabot/alerts
// endpoint the empty-answer guard consults. It records the GraphQL query it was
// sent and counts REST probes, so a test can assert the mapping, the wire
// query, and the traffic.
type securityStub struct {
	// graphQLStatus / graphQLBody answer the GraphQL POST. Zero status means 200.
	graphQLStatus int
	graphQLBody   string

	// restStatus / restBody answer GET /repos/{o}/{r}/dependabot/alerts. Zero
	// status means the live shape for a readable, genuinely clean repository:
	// 200 with an empty array (verified against api.github.com).
	restStatus int
	restBody   string
	// restHeader is set on the REST response before the status is written.
	restHeader map[string]string

	seenQuery string
	restCalls int
}

func newSecurityStub(t *testing.T, stub *securityStub) *SecurityService {
	t.Helper()
	if stub.graphQLStatus == 0 {
		stub.graphQLStatus = http.StatusOK
	}
	if stub.restStatus == 0 {
		stub.restStatus = http.StatusOK
		stub.restBody = "[]"
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/dependabot/alerts") {
			stub.restCalls++
			for k, v := range stub.restHeader {
				w.Header().Set(k, v)
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(stub.restStatus)
			_, _ = w.Write([]byte(stub.restBody))
			return
		}
		raw, _ := io.ReadAll(r.Body)
		stub.seenQuery = string(raw)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(stub.graphQLStatus)
		_, _ = w.Write([]byte(stub.graphQLBody))
	}))
	t.Cleanup(srv.Close)
	return NewSecurityService(NewClientWithURL("test-token", srv.URL+"/graphql"))
}

// securityPayload builds a GraphQL success envelope around one repository
// object.
func securityPayload(repo string) string {
	return `{"data":{"repository":` + repo + `}}`
}

// repoHeader is the repository-level part of every payload below. `url` is
// load-bearing: it is what makes each alert's own deep link constructible.
const repoHeader = `"url":"https://github.com/o/r","hasVulnerabilityAlertsEnabled":true,`

// emptyAlerts is the ambiguous answer — scanning on, nothing returned — that
// GraphQL gives BOTH for a clean repository and for a token that may not read
// the alerts.
const emptyAlerts = `"vulnerabilityAlerts":{"totalCount":0,"pageInfo":{"hasNextPage":false},"nodes":[]}`

const securityAlertNodeWithPR = `{
  "number": 7,
  "createdAt": "2026-01-02T03:04:05Z",
  "vulnerableManifestPath": "package-lock.json",
  "dependencyScope": "RUNTIME",
  "dependencyRelationship": "DIRECT",
  "securityAdvisory": {
    "ghsaId": "GHSA-aaaa-bbbb-cccc",
    "summary": "prototype pollution in widget",
    "severity": "MODERATE",
    "permalink": "https://github.com/advisories/GHSA-aaaa-bbbb-cccc",
    "identifiers": [{"type":"GHSA","value":"GHSA-aaaa-bbbb-cccc"},{"type":"CVE","value":"CVE-2026-0001"}]
  },
  "securityVulnerability": {
    "severity": "HIGH",
    "package": {"name":"widget","ecosystem":"NPM"},
    "vulnerableVersionRange": "< 2.1.0",
    "firstPatchedVersion": {"identifier":"2.1.0"}
  },
  "dependabotUpdate": {
    "pullRequest": {"number":412,"url":"https://github.com/o/r/pull/412","title":"bump widget"},
    "error": null
  }
}`

const securityAlertNodeNoFix = `{
  "number": 9,
  "createdAt": "2026-01-02T03:04:05Z",
  "vulnerableManifestPath": "tools/package-lock.json",
  "dependencyScope": "DEVELOPMENT",
  "dependencyRelationship": "TRANSITIVE",
  "securityAdvisory": {
    "ghsaId": "GHSA-dddd-eeee-ffff",
    "summary": "rce in sprocket",
    "severity": "CRITICAL",
    "permalink": "https://github.com/advisories/GHSA-dddd-eeee-ffff",
    "identifiers": [{"type":"GHSA","value":"GHSA-dddd-eeee-ffff"}]
  },
  "securityVulnerability": {
    "severity": "CRITICAL",
    "package": {"name":"sprocket","ecosystem":"NPM"},
    "vulnerableVersionRange": ">= 1.0.0",
    "firstPatchedVersion": null
  },
  "dependabotUpdate": {
    "pullRequest": null,
    "error": {"errorType":"security_update_not_possible","title":"Dependabot cannot update sprocket to a non-vulnerable version"}
  }
}`

// oneAlert wraps a node list in an alerts connection.
func oneAlert(nodes ...string) string {
	return `"vulnerabilityAlerts":{"totalCount":` + strconv.Itoa(len(nodes)) +
		`,"pageInfo":{"hasNextPage":false},"nodes":[` + strings.Join(nodes, ",") + `]}`
}

// TestSecurityService_QueryIsOneRequestForEverythingTheProducerNeeds pins the
// shape of the wire query. The producer runs inside a shared sweep budget, so a
// second round trip is a regression, and every selection below is one the REST
// dependabot/alerts endpoint cannot supply.
func TestSecurityService_QueryIsOneRequestForEverythingTheProducerNeeds(t *testing.T) {
	stub := &securityStub{graphQLBody: securityPayload(`{` + repoHeader + oneAlert(securityAlertNodeWithPR) + `}`)}
	svc := newSecurityStub(t, stub)

	if _, err := svc.ListOpenAlerts(context.Background(), "o", "r"); err != nil {
		t.Fatalf("ListOpenAlerts: %v", err)
	}
	for _, want := range []string{
		"vulnerabilityAlerts(first: $first, states: [OPEN])",
		"hasVulnerabilityAlertsEnabled",
		// The repository's own web URL, which is how each alert's deep link is
		// built without a second round trip (and stays right on GHES).
		"url",
		// The remediation fact, and the reason there is no PR — neither is on
		// the REST alert payload at all.
		"dependabotUpdate",
		"pullRequest",
		"errorType",
		"firstPatchedVersion",
		"vulnerableManifestPath",
	} {
		if !strings.Contains(stub.seenQuery, want) {
			t.Errorf("query does not select %q:\n%s", want, stub.seenQuery)
		}
	}
	// viewerPermission is deliberately NOT selected any more: the role does not
	// decide alert visibility (GitHub scopes it to a token scope plus granted
	// access), and selecting it invites the inference back.
	if strings.Contains(stub.seenQuery, "viewerPermission") {
		t.Errorf("query still selects viewerPermission — the role is not the authority on alert visibility:\n%s", stub.seenQuery)
	}
	// An answer that contained alerts is self-evidently readable, so the guard
	// must not spend a probe on it.
	if stub.restCalls != 0 {
		t.Errorf("REST probes = %d, want 0 when GraphQL returned alerts", stub.restCalls)
	}
}

func TestSecurityService_MapsTheAdvisorysOwnFields(t *testing.T) {
	svc := newSecurityStub(t, &securityStub{
		graphQLBody: securityPayload(`{` + repoHeader + oneAlert(securityAlertNodeWithPR) + `}`)})

	got, err := svc.ListOpenAlerts(context.Background(), "o", "r")
	if err != nil {
		t.Fatalf("ListOpenAlerts: %v", err)
	}
	if got.Status != forgetypes.SecurityAlertsEnabled {
		t.Fatalf("status = %q, want enabled", got.Status)
	}
	if len(got.Alerts) != 1 {
		t.Fatalf("alerts = %d, want 1", len(got.Alerts))
	}
	a := got.Alerts[0]
	// The per-package severity (HIGH) wins over the advisory-wide one
	// (MODERATE): an advisory spanning several packages states a different
	// severity for each, and the one that applies here is this package's.
	if a.Severity != forgetypes.AlertSeverityHigh {
		t.Errorf("severity = %q, want high", a.Severity)
	}
	if a.AdvisoryID != "GHSA-aaaa-bbbb-cccc" || a.CVE != "CVE-2026-0001" {
		t.Errorf("identifiers = %q / %q", a.AdvisoryID, a.CVE)
	}
	if a.Package != "widget" || a.Ecosystem != "npm" {
		t.Errorf("package = %q ecosystem = %q", a.Package, a.Ecosystem)
	}
	if a.ManifestPath != "package-lock.json" {
		t.Errorf("manifest = %q", a.ManifestPath)
	}
	if a.VulnerableRange != "< 2.1.0" || a.FirstPatchedVersion != "2.1.0" {
		t.Errorf("range = %q first patched = %q", a.VulnerableRange, a.FirstPatchedVersion)
	}
	if a.FirstSeenAt != "2026-01-02T03:04:05Z" {
		t.Errorf("first seen = %q", a.FirstSeenAt)
	}
	if a.Scope != "development" && a.Scope != "runtime" {
		t.Errorf("scope = %q, want GitHub's SCREAMING enum lower-cased", a.Scope)
	}
	if a.Remediation.State != forgetypes.RemediationPROpen || a.Remediation.PRNumber != 412 {
		t.Errorf("remediation = %+v, want the open PR", a.Remediation)
	}
}

// TestSecurityService_EveryAlertCarriesItsOwnDeepLink is the field the whole
// no-remediation-PR card class depends on.
//
// GraphQL's RepositoryVulnerabilityAlert has no url (verified by live
// introspection), so the adapter builds it from the repository URL the same
// query returns. The expected form is REST's html_url verbatim — verified live
// against api.github.com, which answers
// `https://github.com/nightgauge/nightgauge/security/dependabot/12`.
//
// Without it a card that says "there is nothing to merge, go decide something"
// falls back to the public advisory page, which names neither the repository
// nor the manifest and offers no dismiss.
func TestSecurityService_EveryAlertCarriesItsOwnDeepLink(t *testing.T) {
	svc := newSecurityStub(t, &securityStub{
		graphQLBody: securityPayload(`{` + repoHeader +
			oneAlert(securityAlertNodeWithPR, securityAlertNodeNoFix) + `}`)})

	got, err := svc.ListOpenAlerts(context.Background(), "o", "r")
	if err != nil {
		t.Fatalf("ListOpenAlerts: %v", err)
	}
	want := map[int]string{
		7: "https://github.com/o/r/security/dependabot/7",
		9: "https://github.com/o/r/security/dependabot/9",
	}
	for _, a := range got.Alerts {
		if a.URL != want[a.Number] {
			t.Errorf("alert #%d URL = %q, want %q", a.Number, a.URL, want[a.Number])
		}
		if a.URL == a.AdvisoryURL {
			t.Errorf("alert #%d deep link is the advisory page — that page names neither the repository nor the manifest", a.Number)
		}
	}
}

// TestSecurityService_DeepLinkFollowsTheForgesOwnHost keeps GitHub Enterprise
// Server working: the host comes from the repository object, never a literal.
func TestSecurityService_DeepLinkFollowsTheForgesOwnHost(t *testing.T) {
	svc := newSecurityStub(t, &securityStub{
		graphQLBody: securityPayload(
			`{"url":"https://ghe.example.test/o/r","hasVulnerabilityAlertsEnabled":true,` +
				oneAlert(securityAlertNodeWithPR) + `}`)})

	got, err := svc.ListOpenAlerts(context.Background(), "o", "r")
	if err != nil {
		t.Fatalf("ListOpenAlerts: %v", err)
	}
	if want := "https://ghe.example.test/o/r/security/dependabot/7"; got.Alerts[0].URL != want {
		t.Errorf("URL = %q, want %q — the host must come from the forge's own answer", got.Alerts[0].URL, want)
	}
}

// TestSecurityService_RemediationIsTriStateNotBoolean is the distinction the
// whole epic rests on: "there is a PR", "there is provably no PR", and "the
// forge never tried" are three answers, and only the middle one carries the
// forge's own reason.
func TestSecurityService_RemediationIsTriStateNotBoolean(t *testing.T) {
	svc := newSecurityStub(t, &securityStub{
		graphQLBody: securityPayload(`{` + repoHeader + oneAlert(
			securityAlertNodeWithPR, securityAlertNodeNoFix,
			`{"number":11,"createdAt":"2026-01-02T03:04:05Z","vulnerableManifestPath":"go.mod",`+
				`"securityAdvisory":{"ghsaId":"GHSA-1111","severity":"LOW","identifiers":[]},`+
				`"securityVulnerability":{"severity":"LOW","package":{"name":"gopkg","ecosystem":"GO"},`+
				`"vulnerableVersionRange":"< 1","firstPatchedVersion":null},"dependabotUpdate":null}`,
		) + `}`)})

	got, err := svc.ListOpenAlerts(context.Background(), "o", "r")
	if err != nil {
		t.Fatalf("ListOpenAlerts: %v", err)
	}
	if len(got.Alerts) != 3 {
		t.Fatalf("alerts = %d, want 3", len(got.Alerts))
	}
	if s := got.Alerts[0].Remediation.State; s != forgetypes.RemediationPROpen {
		t.Errorf("alert 0 remediation = %q, want %q", s, forgetypes.RemediationPROpen)
	}
	notPossible := got.Alerts[1].Remediation
	if notPossible.State != forgetypes.RemediationNotPossible {
		t.Errorf("alert 1 remediation = %q, want %q", notPossible.State, forgetypes.RemediationNotPossible)
	}
	if notPossible.Reason != "security_update_not_possible" {
		t.Errorf("alert 1 dropped the forge's own reason: %+v", notPossible)
	}
	if s := got.Alerts[2].Remediation.State; s != forgetypes.RemediationNone {
		t.Errorf("alert 2 remediation = %q, want %q", s, forgetypes.RemediationNone)
	}
}

// TestSecurityService_DisabledIsAStatusNotAnError is the outcome sibling issues
// depend on: alerts-off must be distinguishable from zero-alerts WITHOUT
// reading an error message.
func TestSecurityService_DisabledIsAStatusNotAnError(t *testing.T) {
	off := &securityStub{graphQLBody: securityPayload(
		`{"url":"https://github.com/o/r","hasVulnerabilityAlertsEnabled":false,` + emptyAlerts + `}`)}
	svc := newSecurityStub(t, off)

	got, err := svc.ListOpenAlerts(context.Background(), "o", "r")
	if err != nil {
		t.Fatalf("ListOpenAlerts returned an error for a disabled scanner: %v", err)
	}
	if got.Status != forgetypes.SecurityAlertsDisabled {
		t.Errorf("status = %q, want %q", got.Status, forgetypes.SecurityAlertsDisabled)
	}
	if got.Enabled() {
		t.Error("Enabled() is true for a repository with scanning off")
	}
	// The emptiness is already explained, so the guard must not spend a probe
	// on it — and must not be able to turn a disabled scanner into a denial.
	if off.restCalls != 0 {
		t.Errorf("REST probes = %d, want 0 when scanning is switched off", off.restCalls)
	}

	// ... and the zero-alert case is a DIFFERENT, equally successful answer.
	clean := newSecurityStub(t, &securityStub{graphQLBody: securityPayload(`{` + repoHeader + emptyAlerts + `}`)})
	cleanRes, err := clean.ListOpenAlerts(context.Background(), "o", "r")
	if err != nil {
		t.Fatalf("ListOpenAlerts(clean): %v", err)
	}
	if !cleanRes.Enabled() || len(cleanRes.Alerts) != 0 {
		t.Errorf("clean repo = %+v, want enabled with zero alerts", cleanRes)
	}
	if cleanRes.Status == got.Status {
		t.Fatal("zero-alerts and alerts-disabled produce the same status — the coverage producer cannot tell them apart")
	}
}

// TestSecurityService_CleanRepoIsCleanWhateverTheViewersRole is the regression
// this guard most needs.
//
// GitHub grants Dependabot alert visibility through a token SCOPE
// (`security_events`) plus whatever access administrators have granted — the
// REST reference states no repository-role requirement, and the alerts
// documentation says administrators "can grant access to additional people or
// teams" and that "users with write access or higher can assign Dependabot
// alerts", which is impossible for an alert you cannot see. Live introspection
// of RepositoryPermission lists ADMIN, MAINTAIN, WRITE, TRIAGE and READ.
//
// An earlier revision inferred a denial from anything below ADMIN, so four of
// those five roles turned the HEALTHIEST possible observation — a genuinely
// clean repository — into a permission failure, and downstream that failure is
// repo-wide, so the guess disabled every other attention producer for the
// repository.
//
// The payload below carries no role at all, and that is the assertion: the
// query no longer selects viewerPermission (a GraphQL answer containing it
// would fail to unmarshal), so there is no role the service could branch on.
// The verdict comes from the forge — see the REST cases either side of this.
func TestSecurityService_CleanRepoIsCleanWhateverTheViewersRole(t *testing.T) {
	stub := &securityStub{graphQLBody: securityPayload(`{` + repoHeader + emptyAlerts + `}`)}
	svc := newSecurityStub(t, stub)

	got, err := svc.ListOpenAlerts(context.Background(), "o", "r")
	if err != nil {
		t.Fatalf("a readable, genuinely clean repository was reported as a failure: %v", err)
	}
	if !got.Enabled() || len(got.Alerts) != 0 || got.TotalOpen != 0 {
		t.Errorf("result = %+v, want enabled with zero alerts", got)
	}
	if stub.restCalls != 1 {
		t.Errorf("REST probes = %d, want exactly 1 — the empty answer is ambiguous and is confirmed once", stub.restCalls)
	}
}

// TestSecurityService_EmptyAnswerTheForgeRefusesIsAPermissionFailure defuses
// GraphQL's one dangerous property, using the forge's own verdict rather than
// an inference.
//
// Verified live: for a repository the token holds only read access on, REST
// answers `403 You are not authorized to perform this operation.` while GraphQL
// answers `{"totalCount":0,"nodes":[]}` at HTTP 200. Reporting that as "no
// vulnerabilities" is the worst thing this surface could do.
func TestSecurityService_EmptyAnswerTheForgeRefusesIsAPermissionFailure(t *testing.T) {
	svc := newSecurityStub(t, &securityStub{
		graphQLBody: securityPayload(`{` + repoHeader + emptyAlerts + `}`),
		restStatus:  http.StatusForbidden,
		restBody:    `{"message":"You are not authorized to perform this operation."}`,
	})

	got, err := svc.ListOpenAlerts(context.Background(), "o", "r")
	if err == nil {
		t.Fatalf("ListOpenAlerts reported a clean repository the forge refused to serve: %+v", got)
	}
	if !errors.Is(err, forge.ErrPermissionDenied) {
		t.Errorf("err = %v, want it to wrap forge.ErrPermissionDenied", err)
	}
	// The message must attribute the denial to the forge, not to a role we
	// guessed — the sentinel is read downstream as a forge-reported fact.
	if !strings.Contains(err.Error(), "refused") {
		t.Errorf("err = %v, want it to name the forge's refusal", err)
	}
}

// TestSecurityService_ProbeRejectionsMapToTheRightSentinel covers the rest of
// the guard's verdicts. The default branch is the important one: an answer that
// is neither a clear success nor a clear denial fails CLOSED (no clean bill of
// health) without fabricating a sentinel the forge never justified.
func TestSecurityService_ProbeRejectionsMapToTheRightSentinel(t *testing.T) {
	tests := []struct {
		name    string
		status  int
		body    string
		header  map[string]string
		wantIs  error
		wantNot []error
	}{
		{
			name:   "credential rejected",
			status: http.StatusUnauthorized,
			body:   `{"message":"Bad credentials"}`,
			wantIs: forge.ErrUnauthorized,
		},
		{
			name:   "throttled, not denied",
			status: http.StatusForbidden,
			body:   `{"message":"API rate limit exceeded for user"}`,
			// Retry-After keeps the client's own retry loop short; without it
			// the backoff probes for a reset time this stub cannot supply.
			header:  map[string]string{"Retry-After": "0"},
			wantIs:  forge.ErrRateLimited,
			wantNot: []error{forge.ErrPermissionDenied},
		},
		{
			name:   "alerts endpoint absent",
			status: http.StatusNotFound,
			body:   `{"message":"Not Found"}`,
			// A 404 is not a denial and not a clean repository. Refusing to
			// classify it is the point: the caller leaves its cards alone.
			wantNot: []error{forge.ErrPermissionDenied, forge.ErrUnauthorized},
		},
		{
			name:    "forge is broken",
			status:  http.StatusBadGateway,
			body:    `{"message":"Server Error"}`,
			wantNot: []error{forge.ErrPermissionDenied, forge.ErrUnauthorized},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			svc := newSecurityStub(t, &securityStub{
				graphQLBody: securityPayload(`{` + repoHeader + emptyAlerts + `}`),
				restStatus:  tc.status,
				restBody:    tc.body,
				restHeader:  tc.header,
			})

			got, err := svc.ListOpenAlerts(context.Background(), "o", "r")
			if err == nil {
				t.Fatalf("an unconfirmed empty answer was reported as a clean repository: %+v", got)
			}
			if tc.wantIs != nil && !errors.Is(err, tc.wantIs) {
				t.Errorf("err = %v, want it to wrap %v", err, tc.wantIs)
			}
			for _, not := range tc.wantNot {
				if errors.Is(err, not) {
					t.Errorf("err = %v, must NOT wrap %v — the forge said no such thing", err, not)
				}
			}
		})
	}
}

func TestSecurityService_BadCredentialsIsUnauthorized(t *testing.T) {
	svc := newSecurityStub(t, &securityStub{
		graphQLStatus: http.StatusUnauthorized,
		graphQLBody:   `{"message":"Bad credentials"}`,
	})

	if _, err := svc.ListOpenAlerts(context.Background(), "o", "r"); !errors.Is(err, forge.ErrUnauthorized) {
		t.Errorf("err = %v, want it to wrap forge.ErrUnauthorized", err)
	}
}

// TestSecurityService_TruncationIsReportedNotHidden keeps the single-request
// budget honest: the service says it under-reported rather than paginating out
// of every other producer's time. Truncated is what stops a caller reconciling
// one page as if it were the open set.
func TestSecurityService_TruncationIsReportedNotHidden(t *testing.T) {
	svc := newSecurityStub(t, &securityStub{
		graphQLBody: securityPayload(`{` + repoHeader +
			`"vulnerabilityAlerts":{"totalCount":250,"pageInfo":{"hasNextPage":true},"nodes":[` +
			securityAlertNodeWithPR + `]}}`)})

	got, err := svc.ListOpenAlerts(context.Background(), "o", "r")
	if err != nil {
		t.Fatalf("ListOpenAlerts: %v", err)
	}
	if !got.Truncated {
		t.Error("Truncated is false while the forge reports another page")
	}
	if got.TotalOpen != 250 {
		t.Errorf("TotalOpen = %d, want the forge's own count 250", got.TotalOpen)
	}
}

func TestForgeAdapter_SecurityIsLazyAndCached(t *testing.T) {
	client := NewClientWithToken("fake")
	adapter := NewForgeAdapter(client, "nightgauge", 1, OwnerTypeOrg)

	if adapter.Security() == nil {
		t.Fatal("Security() returned nil")
	}
	if adapter.Security() != adapter.Security() {
		t.Error("Security() should return the same instance on repeated access")
	}
}
