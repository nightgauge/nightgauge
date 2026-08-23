package platform

// Contract conformance (#750).
//
// This file is the mechanical detector for the bug class in nightgauge#741:
// a platform route that authorises a *user* being called with an
// account-scoped license key, and the resulting 401 surfacing far from its
// cause — as an empty panel, or as a confident wrong sentence in a webview.
//
// Nothing in either repository could disagree with that arrangement. The
// platform's OpenAPI document declares one undifferentiated bearerAuth scheme
// for nearly every operation, and the Go call sites hand-rolled their own URL
// and Authorization header, so there was no artifact to check against another.
//
// The three tests below close that gap by driving REAL service methods:
//
//   TestContractCallSitesCoverEveryOperation
//       every operation in the generated contract is bound to exactly one
//       service method, and vice versa — a new operation cannot be added
//       without conformance coverage, and a call site cannot invent a route.
//
//   TestUserScopedOperationsRefuseLicenseKey
//       the detector. Every SecurityUserJWT operation, invoked by a client
//       holding only a license key, must fail with ErrCredentialInsufficient
//       and must NOT reach the network.
//
//   TestPipelineOperationsAcceptLicenseKey
//       the counterweight. Every SecurityPipeline operation must still be
//       reachable on the license-key path — a guard that refuses everything
//       would break every headless CLI run and pass the test above.
//
// WHAT THIS CANNOT DETECT, stated plainly
//
// Editing `security:` in api/platform-operations.yaml and regenerating does
// NOT turn these tests red, and it is worth being precise about why: the
// enforcement point (Client.newRequest) and the expectation both read the same
// generated field, so a relabel moves them together. Nothing inside this
// repository can adjudicate whether the platform really demands a user JWT for
// a given route — that fact lives in the platform's route middleware. Keeping
// the manifest true to it is a review obligation, not a test.
//
// What these tests DO detect, and what a passing run is evidence of:
//
//   - a call site wired to the wrong operation (the path/method assertion);
//   - an operation with no call site, or a call site naming a route the
//     contract does not declare;
//   - the credential check being weakened or removed from newRequest — the
//     user-scoped operations would then reach the network;
//   - a service method SWALLOWING the credential error into an empty
//     zero-valued result. That last one is the original bug's actual shape:
//     the 401 never reached the caller, and a webview rendered zeros as fact.
//
// TestConformanceHarnessDetectsAMismatch injects each of those faults
// in-process and asserts the harness rejects them, so the guard is never
// merely assumed to work.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"time"

	api "github.com/nightgauge/nightgauge/api/generated/go/platform"
	"github.com/nightgauge/nightgauge/internal/attention"
)

// conformanceLicenseKey is shaped like a real account-scoped key, which is what
// credentialKindOf classifies on. A key of any other shape is deliberately
// treated as opaque and passed through.
const conformanceLicenseKey = "ib_live_conformance0000000000"

// conformanceSessionJWT is shaped like a user-scoped session token: three
// dot-separated segments.
const conformanceSessionJWT = "header.payload.signature"

// callSite binds one contract operation to the service method that invokes it.
//
// The Invoke funcs call the real exported (or package-internal) methods rather
// than poking newRequest directly: a test that drove newRequest would prove the
// enforcement point works while saying nothing about whether GetAnalyticsHealth
// is wired to OpAnalyticsHealth. Binding the two is the whole point.
type callSite struct {
	Op api.Operation

	// Invoke performs the call. The returned error is inspected; a non-nil
	// error from the *server's* response (a 4xx, a decode failure) is fine and
	// expected — these tests assert on credential behaviour and on which path
	// was requested, never on success.
	Invoke func(ctx context.Context, c *Client) error
}

// contractCallSites must name every operation in api.Operations exactly once.
// TestContractCallSitesCoverEveryOperation enforces that both ways.
var contractCallSites = []callSite{
	{api.OpAgentsAckCommand, func(ctx context.Context, c *Client) error {
		_, err := NewCommandService(c).AcknowledgeAgentCommand(ctx, "agent-1", "cmd-1")
		return err
	}},
	{api.OpAgentsHeartbeat, func(ctx context.Context, c *Client) error {
		return NewAgentRegistrationService(c, "test").Heartbeat(ctx, "agent-1")
	}},
	{api.OpAgentsRegister, func(ctx context.Context, c *Client) error {
		_, err := NewAgentRegistrationService(c, "test").RegisterAgent(ctx)
		return err
	}},
	{api.OpAgentsStreamCommands, func(ctx context.Context, c *Client) error {
		_, _, err := streamAgentCommands(ctx, c, nil, "agent-1")
		return err
	}},
	{api.OpAnalyticsCost, func(ctx context.Context, c *Client) error {
		_, err := NewAnalyticsService(c).GetCostAnalytics(ctx, "2026-01-01", "2026-01-31")
		return err
	}},
	{api.OpAnalyticsHealth, func(ctx context.Context, c *Client) error {
		_, err := NewAnalyticsService(c).GetAnalyticsHealth(ctx)
		return err
	}},
	{api.OpAnalyticsRuns, func(ctx context.Context, c *Client) error {
		_, err := NewAnalyticsService(c).GetAnalyticsRuns(ctx, "", 0)
		return err
	}},
	{api.OpAnalyticsTrends, func(ctx context.Context, c *Client) error {
		_, err := NewAnalyticsService(c).GetAnalyticsTrends(ctx, "7d")
		return err
	}},
	{api.OpAttentionSync, func(ctx context.Context, c *Client) error {
		return NewAttentionSyncService(c).pushBatch(ctx, []attention.DecisionRequest{{ID: "req-1"}})
	}},
	{api.OpAuditIntegrityVerify, func(ctx context.Context, c *Client) error {
		_, err := NewAuditRetentionService(c).VerifyIntegrity(ctx, 30)
		return err
	}},
	{api.OpAuditRetentionGet, func(ctx context.Context, c *Client) error {
		_, err := NewAuditRetentionService(c).GetRetentionConfig(ctx)
		return err
	}},
	{api.OpAuditRetentionUpdate, func(ctx context.Context, c *Client) error {
		_, err := NewAuditRetentionService(c).UpdateRetentionConfig(ctx, 90)
		return err
	}},
	{api.OpAuditReportsGenerate, func(ctx context.Context, c *Client) error {
		_, err := NewComplianceService(c).GenerateReport(ctx, "SOC2", "2026-01-01", "2026-01-31", "pdf")
		return err
	}},
	{api.OpAuditReportsGet, func(ctx context.Context, c *Client) error {
		_, err := NewComplianceService(c).GetReport(ctx, "report-1")
		return err
	}},
	{api.OpAuditReportsList, func(ctx context.Context, c *Client) error {
		_, err := NewComplianceService(c).ListReports(ctx)
		return err
	}},
	{api.OpAuditReportsDownload, func(ctx context.Context, c *Client) error {
		_, err := NewComplianceService(c).DownloadReport(ctx, "report-1")
		return err
	}},
	{api.OpBillingPortalSession, func(ctx context.Context, c *Client) error {
		_, err := NewBillingService(c).CreatePortalSession(ctx)
		return err
	}},
	{api.OpCommandsAck, func(ctx context.Context, c *Client) error {
		return NewCommandService(c).AcknowledgeCommand(ctx, "cmd-1", CommandResult{Status: "success"})
	}},
	{api.OpCommandsPending, func(ctx context.Context, c *Client) error {
		_, err := NewCommandService(c).PollCommands(ctx)
		return err
	}},
	{api.OpPipelineIngestEvent, func(ctx context.Context, c *Client) error {
		return NewAnalyticsService(c).emitPipelineEventSync(ctx, PipelineEvent{
			RunID:     "run-1",
			EventType: "stage_started",
			Stage:     "feature-dev",
			Timestamp: time.Unix(0, 0).UTC(),
		})
	}},
	{api.OpQueueSync, func(ctx context.Context, c *Client) error {
		return NewAnalyticsService(c).syncQueueSync(ctx, QueueSyncPayload{MachineID: "machine-1"})
	}},
	{api.OpTelemetryIngestPipelineRun, func(ctx context.Context, c *Client) error {
		return NewAnalyticsService(c).pushPipelineRunSync(ctx, ExecutionHistoryRunRecord{})
	}},
}

// conformanceServer records the requests it receives and answers every one with
// a 200 and an empty JSON object, so a call that gets as far as the network
// always completes rather than tripping over a decode error that could be
// mistaken for a credential refusal.
type conformanceServer struct {
	srv      *httptest.Server
	requests []conformanceRequest
}

type conformanceRequest struct {
	Method string
	Path   string
	Auth   string
}

func newConformanceServer(t *testing.T) *conformanceServer {
	t.Helper()
	cs := &conformanceServer{}
	cs.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cs.requests = append(cs.requests, conformanceRequest{
			Method: r.Method,
			Path:   r.URL.Path,
			Auth:   r.Header.Get("Authorization"),
		})
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]any{})
	}))
	t.Cleanup(cs.srv.Close)
	return cs
}

// newConformanceClient builds an online client with exactly one credential
// installed. Online matters: most service methods short-circuit when offline
// and would never reach the credential check.
func newConformanceClient(t *testing.T, baseURL, licenseKey, sessionToken string) *Client {
	t.Helper()
	c, err := NewClient(Config{BaseURL: baseURL, LicenseKey: licenseKey, AgentID: "agent-1"})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if sessionToken != "" {
		c.SetSessionToken(sessionToken)
	}
	c.setMode(ModeOnline)
	return c
}

func TestContractCallSitesCoverEveryOperation(t *testing.T) {
	bound := map[string]int{}
	for _, cs := range contractCallSites {
		if cs.Op.ID == "" {
			t.Fatalf("call site with a zero-value operation: %+v", cs.Op)
		}
		bound[cs.Op.ID]++
	}

	for _, op := range api.Operations {
		switch bound[op.ID] {
		case 1:
			delete(bound, op.ID)
		case 0:
			t.Errorf("operation %s is declared in api/platform-operations.yaml but no call site invokes it; "+
				"add one to contractCallSites so the credential check covers it", op)
		default:
			t.Errorf("operation %s is bound to %d call sites; expected exactly 1", op, bound[op.ID])
		}
	}
	for id := range bound {
		t.Errorf("call site references operation %q, which is not in the generated contract", id)
	}
}

// checkUserScopedRefusal runs the detector's assertions against one call site
// and returns the reasons it does not conform, or nil when it does.
//
// Returning reasons rather than calling t.Errorf is what lets
// TestConformanceHarnessDetectsAMismatch feed the same logic a deliberately
// broken call site and assert that it is rejected.
func checkUserScopedRefusal(t *testing.T, cs callSite) []string {
	t.Helper()
	srv := newConformanceServer(t)
	c := newConformanceClient(t, srv.srv.URL, conformanceLicenseKey, "")

	var reasons []string
	err := cs.Invoke(context.Background(), c)
	if !errors.Is(err, ErrCredentialInsufficient) {
		// Covers both "the guard let it through" and "the method swallowed the
		// credential error into a zero-valued result" — the original bug.
		reasons = append(reasons, fmt.Sprintf(
			"%s with a license key: err = %v, want ErrCredentialInsufficient (a swallowed error renders as fact downstream)",
			cs.Op, err))
	}
	// Refusing after the request has gone out would still present the license
	// key to the platform and still produce a 401 to misinterpret. The refusal
	// has to happen before the wire.
	if len(srv.requests) != 0 {
		reasons = append(reasons, fmt.Sprintf(
			"%s: %d request(s) reached the platform despite an insufficient credential: %+v",
			cs.Op, len(srv.requests), srv.requests))
	}
	return reasons
}

// TestUserScopedOperationsRefuseLicenseKey is the detector.
func TestUserScopedOperationsRefuseLicenseKey(t *testing.T) {
	t.Setenv("NIGHTGAUGE_AGENT_ID", "agent-1")

	var checked int
	for _, cs := range contractCallSites {
		if cs.Op.Security != api.SecurityUserJWT {
			continue
		}
		checked++
		t.Run(cs.Op.ID, func(t *testing.T) {
			for _, reason := range checkUserScopedRefusal(t, cs) {
				t.Error(reason)
			}
		})
	}

	if checked == 0 {
		t.Fatal("no user-scoped operations were exercised; the detector is inert")
	}
}

// TestUserScopedOperationsAcceptSessionToken proves the refusal above is
// specific to the credential and not a blanket failure of those call sites.
func TestUserScopedOperationsAcceptSessionToken(t *testing.T) {
	t.Setenv("NIGHTGAUGE_AGENT_ID", "agent-1")

	for _, cs := range contractCallSites {
		if cs.Op.Security != api.SecurityUserJWT {
			continue
		}
		t.Run(cs.Op.ID, func(t *testing.T) {
			srv := newConformanceServer(t)
			c := newConformanceClient(t, srv.srv.URL, conformanceLicenseKey, conformanceSessionJWT)

			if err := cs.Invoke(context.Background(), c); errors.Is(err, ErrCredentialInsufficient) {
				t.Fatalf("%s with a session token: unexpected ErrCredentialInsufficient", cs.Op)
			}
			for _, reason := range contractPathReasons(cs.Op, srv, "Bearer "+conformanceSessionJWT) {
				t.Error(reason)
			}
		})
	}
}

// checkPipelineReachability runs the counterweight's assertions against one
// call site and returns the reasons it does not conform, or nil when it does.
func checkPipelineReachability(t *testing.T, cs callSite) []string {
	t.Helper()
	srv := newConformanceServer(t)
	c := newConformanceClient(t, srv.srv.URL, conformanceLicenseKey, "")

	var reasons []string
	if err := cs.Invoke(context.Background(), c); errors.Is(err, ErrCredentialInsufficient) {
		reasons = append(reasons, fmt.Sprintf(
			"%s: refused a license key, but the contract declares SecurityPipeline", cs.Op))
	}
	return append(reasons, contractPathReasons(cs.Op, srv, "Bearer "+conformanceLicenseKey)...)
}

// TestPipelineOperationsAcceptLicenseKey is the counterweight: a guard that
// refused every license key would satisfy the detector above and break every
// headless run. These operations sit behind the platform's pipelineAuth and
// must stay reachable.
func TestPipelineOperationsAcceptLicenseKey(t *testing.T) {
	t.Setenv("NIGHTGAUGE_AGENT_ID", "agent-1")

	var checked int
	for _, cs := range contractCallSites {
		if cs.Op.Security != api.SecurityPipeline {
			continue
		}
		checked++
		t.Run(cs.Op.ID, func(t *testing.T) {
			for _, reason := range checkPipelineReachability(t, cs) {
				t.Error(reason)
			}
		})
	}

	if checked == 0 {
		t.Fatal("no pipeline operations were exercised; the counterweight is inert")
	}
}

// contractPathReasons checks that the call site requested the method and path
// its operation declares — the binding that makes the credential column mean
// anything. Without it a call site could name OpAnalyticsHealth and request
// /v1/anything.
func contractPathReasons(op api.Operation, srv *conformanceServer, wantAuth string) []string {
	if len(srv.requests) == 0 {
		return []string{fmt.Sprintf("%s: no request reached the platform", op)}
	}
	var reasons []string
	got := srv.requests[0]
	if got.Method != op.Method {
		reasons = append(reasons, fmt.Sprintf("%s: method = %s, want %s", op, got.Method, op.Method))
	}
	if !pathMatchesTemplate(got.Path, op.Path) {
		reasons = append(reasons, fmt.Sprintf(
			"%s: requested path %q does not match the contract template %q", op, got.Path, op.Path))
	}
	if got.Auth != wantAuth {
		reasons = append(reasons, fmt.Sprintf("%s: Authorization = %q, want %q", op, got.Auth, wantAuth))
	}
	return reasons
}

// pathMatchesTemplate compares a concrete request path against a contract path
// template, treating each {placeholder} as exactly one non-empty segment.
func pathMatchesTemplate(path, tmpl string) bool {
	gotSegs := strings.Split(strings.Trim(path, "/"), "/")
	wantSegs := strings.Split(strings.Trim(tmpl, "/"), "/")
	if len(gotSegs) != len(wantSegs) {
		return false
	}
	for i, want := range wantSegs {
		if strings.HasPrefix(want, "{") && strings.HasSuffix(want, "}") {
			if gotSegs[i] == "" {
				return false
			}
			continue
		}
		if gotSegs[i] != want {
			return false
		}
	}
	return true
}

// TestConformanceHarnessDetectsAMismatch is the guard on the guard.
//
// A conformance test that has never been observed to fail is not evidence.
// Each subtest hands the SAME assertion helpers the real tests use a call site
// carrying a deliberately injected fault, and requires them to report it. If
// any subtest finds no reasons, the corresponding real test has gone inert.
func TestConformanceHarnessDetectsAMismatch(t *testing.T) {
	t.Setenv("NIGHTGAUGE_AGENT_ID", "agent-1")

	// A call site whose declared operation demands a user JWT while the code it
	// runs is a pipelineAuth route that happily accepts a license key. This is
	// the epic's bug expressed as a fault: the contract says one thing, the
	// credential the call site can actually supply says another.
	mislabelledAsUserScoped := callSite{
		Op: api.Operation{
			ID:       "fault.pipelineRouteLabelledUserScoped",
			Method:   api.OpAnalyticsRuns.Method,
			Path:     api.OpAnalyticsRuns.Path,
			Security: api.SecurityUserJWT,
			Upstream: api.UpstreamDeclared,
		},
		Invoke: func(ctx context.Context, c *Client) error {
			_, err := NewAnalyticsService(c).GetAnalyticsRuns(ctx, "", 0)
			return err
		},
	}

	// The inverse: an operation declared reachable on the license-key path whose
	// call site really refuses one.
	mislabelledAsPipeline := callSite{
		Op: api.Operation{
			ID:       "fault.userScopedRouteLabelledPipeline",
			Method:   api.OpAnalyticsHealth.Method,
			Path:     api.OpAnalyticsHealth.Path,
			Security: api.SecurityPipeline,
			Upstream: api.UpstreamDeclared,
		},
		Invoke: func(ctx context.Context, c *Client) error {
			_, err := NewAnalyticsService(c).GetAnalyticsHealth(ctx)
			return err
		},
	}

	// A call site wired to the wrong route entirely: it claims the runs
	// operation but requests queue sync. Both are SecurityPipeline, so the
	// credential check is silent and only the path/method assertion can catch
	// it. Catching it is what stops the contract from becoming decoration.
	wrongRoute := callSite{
		Op: api.OpAnalyticsRuns,
		Invoke: func(ctx context.Context, c *Client) error {
			return NewAnalyticsService(c).syncQueueSync(ctx, QueueSyncPayload{MachineID: "machine-1"})
		},
	}

	// A service method that swallows the credential refusal into an empty
	// zero-valued result — the exact shape of the original bug, where a webview
	// rendered numbers nobody ever fetched.
	swallowsTheError := callSite{
		Op: api.OpAnalyticsHealth,
		Invoke: func(ctx context.Context, c *Client) error {
			if _, err := NewAnalyticsService(c).GetAnalyticsHealth(ctx); err != nil {
				return nil // the fault
			}
			return nil
		},
	}

	for _, tc := range []struct {
		name  string
		check func(*testing.T, callSite) []string
		site  callSite
	}{
		{"pipeline route labelled user_jwt", checkUserScopedRefusal, mislabelledAsUserScoped},
		{"user_jwt route labelled pipeline", checkPipelineReachability, mislabelledAsPipeline},
		{"call site wired to the wrong route", checkPipelineReachability, wrongRoute},
		{"credential error swallowed into an empty result", checkUserScopedRefusal, swallowsTheError},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reasons := tc.check(t, tc.site)
			if len(reasons) == 0 {
				t.Fatalf("injected fault %q was NOT reported; the conformance check is inert", tc.name)
			}
			t.Logf("fault correctly reported: %s", strings.Join(reasons, "; "))
		})
	}
}

// TestOpaqueAndAbsentCredentialsArePassedThrough pins the deliberate narrowness
// of the guard. Only a credential recognisable AS a license key is refused: an
// unrecognised token shape, or none at all, is the platform's call. Widening
// this would turn every diagnosable 401 into a local refusal — and would break
// every existing test in this package, which configure no credential at all.
func TestOpaqueAndAbsentCredentialsArePassedThrough(t *testing.T) {
	for _, tc := range []struct {
		name   string
		bearer string
		want   credentialKind
	}{
		{"absent", "", credentialNone},
		{"whitespace", "   ", credentialNone},
		{"live license key", "ib_live_abc123", credentialLicenseKey},
		{"ci license key", "ib_ci_abc123", credentialLicenseKey},
		{"session jwt", "aaa.bbb.ccc", credentialUserJWT},
		{"opaque test fixture", "test-key", credentialOpaque},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := credentialKindOf(tc.bearer); got != tc.want {
				t.Fatalf("credentialKindOf(%q) = %q, want %q", tc.bearer, got, tc.want)
			}
			satisfied := credentialKindOf(tc.bearer).satisfies(api.SecurityUserJWT)
			wantSatisfied := tc.want != credentialLicenseKey
			if satisfied != wantSatisfied {
				t.Fatalf("%q satisfies user_jwt = %v, want %v", tc.bearer, satisfied, wantSatisfied)
			}
			if !credentialKindOf(tc.bearer).satisfies(api.SecurityPipeline) {
				t.Fatalf("%q must satisfy a pipeline operation", tc.bearer)
			}
		})
	}
}

// TestOperationUpstreamDriftIsKnown freezes the set of operations this binary
// calls that the platform's OpenAPI document does not declare.
//
// These are live findings, not decoration: the flat /v1/commands/* pair is
// absent entirely while agent command delivery lives under
// /v1/agents/{agentId}/commands. Freezing the set means a NEW undeclared route
// cannot be added quietly, and fixing one of these requires deleting its entry
// here — which is the visible acknowledgement that was missing before.
//
// audit.verifyIntegrity was the third entry until #822 moved the call to
// /v1/audit/integrity, the path the platform actually mounts. Its deletion
// from this list is that fix's acknowledgement.
func TestOperationUpstreamDriftIsKnown(t *testing.T) {
	known := []string{
		"commands.ack",
		"commands.listPending",
	}

	var got []string
	for _, op := range api.Operations {
		if op.Upstream == api.UpstreamUndeclared {
			got = append(got, op.ID)
		}
	}
	sort.Strings(got)

	if strings.Join(got, ",") != strings.Join(known, ",") {
		t.Fatalf("undeclared-upstream operations = %v, want %v.\n"+
			"An operation absent from the platform OpenAPI document was added or removed. "+
			"If it was fixed, delete it from the `known` list; if it is new, confirm the route "+
			"actually exists before shipping a call to it.", got, known)
	}
}
