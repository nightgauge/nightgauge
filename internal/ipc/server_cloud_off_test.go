package ipc

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/platform"
)

const validLicenseJSON = `{"valid":true,"status":"active","tier":"pro","expiresAt":"2027-01-01T00:00:00Z","expiresSoon":false,"machineBound":false,"machineCount":1,"features":{"batchProcessing":true,"concurrentPipelines":3,"pipelineRunsPerDay":50,"pipelineRunsPerMonth":null,"skillResolveRatePerMin":60}}`

// cloudMock is a platform that counts health checks and pipeline-run posts.
// healthFailures makes the first N health checks answer 503.
type cloudMock struct {
	server         *httptest.Server
	health         atomic.Int32
	runPosts       atomic.Int32
	healthFailures int32
}

func newCloudMock(t *testing.T, healthFailures int32) *cloudMock {
	t.Helper()
	m := &cloudMock{healthFailures: healthFailures}
	m.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/health":
			if m.health.Add(1) <= m.healthFailures {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			fmt.Fprint(w, `{"status":"ok"}`)
		case "/v1/license/validate":
			fmt.Fprint(w, validLicenseJSON)
		case "/v1/telemetry/pipeline-run":
			m.runPosts.Add(1)
			w.WriteHeader(http.StatusAccepted)
			fmt.Fprint(w, `{"accepted":1,"rejected":[]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(m.server.Close)
	return m
}

// waitFor polls cond until it holds or the deadline passes.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestCloudOff_SignedInSessionSendsNothingOnItsOwn pins what a signed-in
// session does with cloud features off: the daemon builds a client so the
// user's own requests work, but it polls nothing, and the extension's
// periodic platform.healthCheck reads offline without a request. The first
// account action checks the platform then, so validation does not answer
// "not valid" because the client built by the session had never checked.
func TestCloudOff_SignedInSessionSendsNothingOnItsOwn(t *testing.T) {
	mock := newCloudMock(t, 0)
	s := NewServer(nil, WithPlatformEndpoint(mock.server.URL)) // no cloud opt-in
	s.writer = &bytes.Buffer{}

	if _, err := callHandler(t, s, "platform.setSessionToken", PlatformSetSessionTokenParams{Token: "user.jwt.token"}); err != nil {
		t.Fatalf("setSessionToken: %v", err)
	}
	if s.getPlatformClient() == nil {
		t.Fatal("the session built no client; the user's own requests need one")
	}
	time.Sleep(150 * time.Millisecond) // a poller's first check runs at once
	if n := mock.health.Load(); n != 0 {
		t.Fatalf("a signed-in session with cloud features off made %d health checks on its own, want 0", n)
	}

	res, err := callHandler(t, s, "platform.healthCheck", nil)
	if err != nil {
		t.Fatalf("healthCheck: %v", err)
	}
	if m, ok := res.(map[string]interface{}); !ok || m["status"] != "offline" {
		t.Errorf("healthCheck with cloud features off = %v, want the offline answer", res)
	}
	if n := mock.health.Load(); n != 0 {
		t.Fatalf("the extension's periodic healthCheck reached the platform %d times with cloud features off", n)
	}

	res, err = callHandler(t, s, "platform.validateLicense", PlatformValidateLicenseParams{LicenseKey: "lic_entered", MachineID: "m-1"})
	if err != nil {
		t.Fatalf("validateLicense: %v", err)
	}
	if info, ok := res.(*platform.LicenseInfo); !ok || !info.Valid {
		t.Errorf("validateLicense = %#v, want a valid license: the account action must check the platform first", res)
	}
	if mock.health.Load() == 0 {
		t.Error("the account action never checked the platform's health")
	}
}

// TestCloudOn_SignedInSessionPolls keeps the opted-in behaviour (#756): the
// client a session builds polls the platform's health.
func TestCloudOn_SignedInSessionPolls(t *testing.T) {
	mock := newCloudMock(t, 0)
	s := NewServer(nil, WithPlatformEndpoint(mock.server.URL), WithTelemetryPolicy(true, true))
	s.writer = &bytes.Buffer{}

	if _, err := callHandler(t, s, "platform.setSessionToken", PlatformSetSessionTokenParams{Token: "user.jwt.token"}); err != nil {
		t.Fatalf("setSessionToken: %v", err)
	}
	waitFor(t, "the opted-in client's first health check", func() bool { return mock.health.Load() > 0 })
	t.Cleanup(s.getPlatformClient().StopHealthPolling)
}

// TestAccountAction_ProbesAClientThatReadsOffline pins that an account action
// checks the platform whenever the client reads offline, not only when the
// action built it: here a polling client's first check failed, and Activate
// License must still validate the key instead of answering "not valid".
func TestAccountAction_ProbesAClientThatReadsOffline(t *testing.T) {
	mock := newCloudMock(t, 1) // the poller's first check fails
	s := NewServer(nil, WithPlatformEndpoint(mock.server.URL), WithTelemetryPolicy(true, true))
	s.writer = &bytes.Buffer{}

	if _, err := callHandler(t, s, "platform.setSessionToken", PlatformSetSessionTokenParams{Token: "user.jwt.token"}); err != nil {
		t.Fatalf("setSessionToken: %v", err)
	}
	pc := s.getPlatformClient()
	t.Cleanup(pc.StopHealthPolling)
	waitFor(t, "the failed first health check", func() bool { return mock.health.Load() >= 1 })
	time.Sleep(20 * time.Millisecond) // let the failed check set the mode
	if pc.IsOnline() {
		t.Fatal("precondition: the client must read offline after its failed check")
	}

	res, err := callHandler(t, s, "platform.validateLicense", PlatformValidateLicenseParams{LicenseKey: "lic_entered", MachineID: "m-1"})
	if err != nil {
		t.Fatalf("validateLicense: %v", err)
	}
	if info, ok := res.(*platform.LicenseInfo); !ok || !info.Valid {
		t.Errorf("validateLicense = %#v, want a valid license after a fresh check", res)
	}
}

// TestServerAnalytics_GatedBelowTheHandlers pins the send gate on the
// analytics service the server builds (setPlatformServicesLocked): every
// handler checks TelemetryAllowed too, so only a direct call shows that a
// path which forgot that check would still send nothing once the editor
// withdraws its consent.
func TestServerAnalytics_GatedBelowTheHandlers(t *testing.T) {
	mock := newCloudMock(t, 0)
	s := NewServer(nil, WithPlatformEndpoint(mock.server.URL), WithTelemetryPolicy(true, true), WithEditorTelemetry("on"))
	s.writer = &bytes.Buffer{}
	ctx := context.Background()

	pc, err := s.accountActionClient(ctx)
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	t.Cleanup(pc.StopHealthPolling)
	waitFor(t, "the client to read online", pc.IsOnline)

	if _, err := callHandler(t, s, "platform.setTelemetryConsent", PlatformSetTelemetryConsentParams{Enabled: false}); err != nil {
		t.Fatalf("setTelemetryConsent: %v", err)
	}
	s.getAnalyticsSvc().PushPipelineRun(ctx, platform.ExecutionHistoryRunRecord{SchemaVersion: 5, IssueNumber: 1, Repo: "acme/app", Outcome: "complete"})
	time.Sleep(150 * time.Millisecond)
	if n := mock.runPosts.Load(); n != 0 {
		t.Fatalf("the analytics service posted %d run records after the editor withdrew consent, want 0", n)
	}

	if _, err := callHandler(t, s, "platform.setTelemetryConsent", PlatformSetTelemetryConsentParams{Enabled: true}); err != nil {
		t.Fatalf("setTelemetryConsent: %v", err)
	}
	s.getAnalyticsSvc().PushPipelineRun(ctx, platform.ExecutionHistoryRunRecord{SchemaVersion: 5, IssueNumber: 2, Repo: "acme/app", Outcome: "complete"})
	waitFor(t, "the run record once consent is back", func() bool { return mock.runPosts.Load() == 1 })
}
