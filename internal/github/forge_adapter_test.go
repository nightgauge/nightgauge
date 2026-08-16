package github

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
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

// securityServer stands up a stub GraphQL endpoint that replies with body and
// records the request it received, so a test can assert BOTH the mapping and
// the query the adapter actually sends.
func securityServer(t *testing.T, status int, body string) (*SecurityService, *string) {
	t.Helper()
	var seen string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		seen = string(raw)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return NewSecurityService(NewClientWithURL("test-token", srv.URL+"/graphql")), &seen
}

// securityPayload builds a GraphQL success envelope around one repository
// object.
func securityPayload(repo string) string {
	return `{"data":{"repository":` + repo + `}}`
}

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

// TestSecurityService_QueryIsOneRequestForEverythingTheProducerNeeds pins the
// shape of the wire query. The producer runs inside a shared sweep budget, so a
// second round trip is a regression, and every selection below is one the REST
// dependabot/alerts endpoint cannot supply.
func TestSecurityService_QueryIsOneRequestForEverythingTheProducerNeeds(t *testing.T) {
	svc, seen := securityServer(t, http.StatusOK, securityPayload(
		`{"viewerPermission":"ADMIN","hasVulnerabilityAlertsEnabled":true,`+
			`"vulnerabilityAlerts":{"totalCount":0,"pageInfo":{"hasNextPage":false},"nodes":[]}}`))

	if _, err := svc.ListOpenAlerts(context.Background(), "o", "r"); err != nil {
		t.Fatalf("ListOpenAlerts: %v", err)
	}
	for _, want := range []string{
		"vulnerabilityAlerts(first: $first, states: [OPEN])",
		"hasVulnerabilityAlertsEnabled",
		// The silent-empty guard: without this selection an under-privileged
		// token reads as a clean repository.
		"viewerPermission",
		// The remediation fact, and the reason there is no PR — neither is on
		// the REST alert payload at all.
		"dependabotUpdate",
		"pullRequest",
		"errorType",
		"firstPatchedVersion",
		"vulnerableManifestPath",
	} {
		if !strings.Contains(*seen, want) {
			t.Errorf("query does not select %q:\n%s", want, *seen)
		}
	}
}

func TestSecurityService_MapsTheAdvisorysOwnFields(t *testing.T) {
	svc, _ := securityServer(t, http.StatusOK, securityPayload(
		`{"viewerPermission":"ADMIN","hasVulnerabilityAlertsEnabled":true,"vulnerabilityAlerts":`+
			`{"totalCount":1,"pageInfo":{"hasNextPage":false},"nodes":[`+securityAlertNodeWithPR+`]}}`))

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

// TestSecurityService_RemediationIsTriStateNotBoolean is the distinction the
// whole epic rests on: "there is a PR", "there is provably no PR", and "the
// forge never tried" are three answers, and only the middle one carries the
// forge's own reason.
func TestSecurityService_RemediationIsTriStateNotBoolean(t *testing.T) {
	svc, _ := securityServer(t, http.StatusOK, securityPayload(
		`{"viewerPermission":"ADMIN","hasVulnerabilityAlertsEnabled":true,"vulnerabilityAlerts":`+
			`{"totalCount":3,"pageInfo":{"hasNextPage":false},"nodes":[`+
			securityAlertNodeWithPR+`,`+securityAlertNodeNoFix+`,`+
			`{"number":11,"createdAt":"2026-01-02T03:04:05Z","vulnerableManifestPath":"go.mod",`+
			`"securityAdvisory":{"ghsaId":"GHSA-1111","severity":"LOW","identifiers":[]},`+
			`"securityVulnerability":{"severity":"LOW","package":{"name":"gopkg","ecosystem":"GO"},`+
			`"vulnerableVersionRange":"< 1","firstPatchedVersion":null},"dependabotUpdate":null}`+
			`]}}`))

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
	svc, _ := securityServer(t, http.StatusOK, securityPayload(
		`{"viewerPermission":"ADMIN","hasVulnerabilityAlertsEnabled":false,`+
			`"vulnerabilityAlerts":{"totalCount":0,"pageInfo":{"hasNextPage":false},"nodes":[]}}`))

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

	// ... and the zero-alert case is a DIFFERENT, equally successful answer.
	clean, _ := securityServer(t, http.StatusOK, securityPayload(
		`{"viewerPermission":"ADMIN","hasVulnerabilityAlertsEnabled":true,`+
			`"vulnerabilityAlerts":{"totalCount":0,"pageInfo":{"hasNextPage":false},"nodes":[]}}`))
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

// TestSecurityService_EmptyAnswerFromANonAdminIsAPermissionFailure defuses
// GraphQL's one dangerous property. Verified against the live API: for a
// repository the token holds only read access on, REST answers 403 while
// GraphQL answers an empty connection with no error. Reporting that as "no
// vulnerabilities" is the worst thing this surface could do.
func TestSecurityService_EmptyAnswerFromANonAdminIsAPermissionFailure(t *testing.T) {
	svc, _ := securityServer(t, http.StatusOK, securityPayload(
		`{"viewerPermission":"READ","hasVulnerabilityAlertsEnabled":true,`+
			`"vulnerabilityAlerts":{"totalCount":0,"pageInfo":{"hasNextPage":false},"nodes":[]}}`))

	got, err := svc.ListOpenAlerts(context.Background(), "o", "r")
	if err == nil {
		t.Fatalf("ListOpenAlerts reported a clean repository to a token that cannot read alerts: %+v", got)
	}
	if !errors.Is(err, forge.ErrPermissionDenied) {
		t.Errorf("err = %v, want it to wrap forge.ErrPermissionDenied", err)
	}

	// The guard must not fire when alerts DID come back: a token that returned
	// them has self-evidently been allowed to read them.
	visible, _ := securityServer(t, http.StatusOK, securityPayload(
		`{"viewerPermission":"WRITE","hasVulnerabilityAlertsEnabled":true,"vulnerabilityAlerts":`+
			`{"totalCount":1,"pageInfo":{"hasNextPage":false},"nodes":[`+securityAlertNodeWithPR+`]}}`))
	if _, err := visible.ListOpenAlerts(context.Background(), "o", "r"); err != nil {
		t.Errorf("ListOpenAlerts refused an answer it had already received: %v", err)
	}
}

func TestSecurityService_BadCredentialsIsUnauthorized(t *testing.T) {
	svc, _ := securityServer(t, http.StatusUnauthorized, `{"message":"Bad credentials"}`)

	if _, err := svc.ListOpenAlerts(context.Background(), "o", "r"); !errors.Is(err, forge.ErrUnauthorized) {
		t.Errorf("err = %v, want it to wrap forge.ErrUnauthorized", err)
	}
}

// TestSecurityService_TruncationIsReportedNotHidden keeps the single-request
// budget honest: the service says it under-reported rather than paginating out
// of every other producer's time.
func TestSecurityService_TruncationIsReportedNotHidden(t *testing.T) {
	svc, _ := securityServer(t, http.StatusOK, securityPayload(
		`{"viewerPermission":"ADMIN","hasVulnerabilityAlertsEnabled":true,"vulnerabilityAlerts":`+
			`{"totalCount":250,"pageInfo":{"hasNextPage":true},"nodes":[`+securityAlertNodeWithPR+`]}}`))

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
