package platform

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// TestRegisterAgent_Success verifies the 201 parse and the request body/auth the
// platform's RegisterAgentSchema expects (#341).
func TestRegisterAgent_Success(t *testing.T) {
	t.Setenv(machineIDEnv, "test-machine-uuid") // deterministic + hermetic (no home-dir read)

	var gotBody []byte
	var gotAuth, gotMethod, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
		gotAuth = r.Header.Get("Authorization")
		gotMethod = r.Method
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"agentId":"9c1f0f2e-1111-4222-8333-444455556666","commandsUrl":"/v1/agents/9c1f0f2e-1111-4222-8333-444455556666/commands","ttl_seconds":90}`))
	}))
	defer srv.Close()

	reg := NewAgentRegistrationService(onlineClient(t, srv.URL), "1.2.3")
	info, err := reg.RegisterAgent(context.Background())
	if err != nil {
		t.Fatalf("RegisterAgent: %v", err)
	}

	if info.AgentID != "9c1f0f2e-1111-4222-8333-444455556666" {
		t.Errorf("agentId = %q", info.AgentID)
	}
	if info.CommandsURL != "/v1/agents/9c1f0f2e-1111-4222-8333-444455556666/commands" {
		t.Errorf("commandsUrl = %q", info.CommandsURL)
	}
	if info.TTLSeconds != 90 {
		t.Errorf("ttl_seconds = %d, want 90", info.TTLSeconds)
	}

	if gotMethod != http.MethodPost || gotPath != "/v1/agents/register" {
		t.Errorf("request = %s %s, want POST /v1/agents/register", gotMethod, gotPath)
	}
	if gotAuth != "Bearer test-key" {
		t.Errorf("Authorization = %q, want Bearer test-key", gotAuth)
	}

	var body agentRegisterBody
	if err := json.Unmarshal(gotBody, &body); err != nil {
		t.Fatalf("unmarshal register body: %v", err)
	}
	if body.MachineID != "test-machine-uuid" {
		t.Errorf("machine_id = %q, want the resolved machine id", body.MachineID)
	}
	if body.AgentVersion != "1.2.3" {
		t.Errorf("agent_version = %q, want 1.2.3", body.AgentVersion)
	}
	if len(body.Capabilities) != 1 || body.Capabilities[0] != AgentRegisterCapabilityResolve {
		t.Errorf("capabilities = %v, want [%s]", body.Capabilities, AgentRegisterCapabilityResolve)
	}
}

// TestRegisterAgent_OmitsAgentVersionWhenEmpty verifies a dev/unknown build omits
// agent_version (the field is omitempty; the platform schema treats it optional).
func TestRegisterAgent_OmitsAgentVersionWhenEmpty(t *testing.T) {
	t.Setenv(machineIDEnv, "test-machine-uuid")

	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"agentId":"a","commandsUrl":"/v1/agents/a/commands","ttl_seconds":90}`))
	}))
	defer srv.Close()

	reg := NewAgentRegistrationService(onlineClient(t, srv.URL), "")
	if _, err := reg.RegisterAgent(context.Background()); err != nil {
		t.Fatalf("RegisterAgent: %v", err)
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(gotBody, &raw); err != nil {
		t.Fatalf("unmarshal raw body: %v", err)
	}
	if _, present := raw["agent_version"]; present {
		t.Errorf("agent_version present with empty version — must be omitted")
	}
}

// TestRegisterAgent_NonCreatedError verifies a non-201 response is an error the
// caller retries (no agentId returned).
func TestRegisterAgent_NonCreatedError(t *testing.T) {
	t.Setenv(machineIDEnv, "test-machine-uuid")

	for _, status := range []int{http.StatusUnprocessableEntity, http.StatusInternalServerError, http.StatusUnauthorized} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(status)
			_, _ = w.Write([]byte(`{"code":"ERR"}`))
		}))
		reg := NewAgentRegistrationService(onlineClient(t, srv.URL), "1.0.0")
		info, err := reg.RegisterAgent(context.Background())
		srv.Close()
		if err == nil {
			t.Errorf("status %d: expected error, got nil (agentId=%q)", status, info.AgentID)
		}
		if info.AgentID != "" {
			t.Errorf("status %d: agentId = %q, want empty on error", status, info.AgentID)
		}
	}
}

// TestRegisterAgent_MissingAgentIDError verifies a 201 whose body has no agentId
// is an error (defensive — never treat a malformed 201 as registered).
func TestRegisterAgent_MissingAgentIDError(t *testing.T) {
	t.Setenv(machineIDEnv, "test-machine-uuid")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"commandsUrl":"/x","ttl_seconds":90}`))
	}))
	defer srv.Close()

	reg := NewAgentRegistrationService(onlineClient(t, srv.URL), "1.0.0")
	if _, err := reg.RegisterAgent(context.Background()); err == nil {
		t.Fatalf("expected error for 201 missing agentId, got nil")
	}
}

// TestAgentHeartbeat_OKAndNotFound verifies a 2xx heartbeat is nil and a 404
// heartbeat returns the sentinel ErrAgentNotFound (the re-register signal).
func TestAgentHeartbeat_OKAndNotFound(t *testing.T) {
	t.Setenv(machineIDEnv, "test-machine-uuid")

	var status atomic.Int32
	status.Store(http.StatusOK)
	var gotMethod, gotPath, gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath, gotAuth = r.Method, r.URL.Path, r.Header.Get("Authorization")
		code := int(status.Load())
		w.WriteHeader(code)
		if code == http.StatusOK {
			_, _ = w.Write([]byte(`{"ttl_seconds":90}`))
		} else {
			_, _ = w.Write([]byte(`{"code":"NOT_FOUND"}`))
		}
	}))
	defer srv.Close()

	reg := NewAgentRegistrationService(onlineClient(t, srv.URL), "1.0.0")

	if err := reg.Heartbeat(context.Background(), "agent-xyz"); err != nil {
		t.Fatalf("heartbeat 200: %v", err)
	}
	if gotMethod != http.MethodPut || gotPath != "/v1/agents/agent-xyz/heartbeat" {
		t.Errorf("request = %s %s, want PUT /v1/agents/agent-xyz/heartbeat", gotMethod, gotPath)
	}
	if gotAuth != "Bearer test-key" {
		t.Errorf("Authorization = %q, want Bearer test-key", gotAuth)
	}

	status.Store(http.StatusNotFound)
	err := reg.Heartbeat(context.Background(), "agent-xyz")
	if !errors.Is(err, ErrAgentNotFound) {
		t.Fatalf("heartbeat 404 err = %v, want ErrAgentNotFound", err)
	}
}

// TestAgentRegistration_ReRegisterOn404 exercises the exact primitives the serve
// bridge composes for TTL-eviction recovery: a heartbeat 404 signals eviction,
// a fresh RegisterAgent yields a new agent id, and the new id heartbeats OK.
func TestAgentRegistration_ReRegisterOn404(t *testing.T) {
	t.Setenv(machineIDEnv, "test-machine-uuid")

	// registerCount drives a distinct agent id per registration so we can prove
	// the id actually swaps.
	var registerCount atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v1/agents/register" && r.Method == http.MethodPost:
			n := registerCount.Add(1)
			w.WriteHeader(http.StatusCreated)
			id := "agent-old"
			if n >= 2 {
				id = "agent-new"
			}
			_, _ = w.Write([]byte(`{"agentId":"` + id + `","commandsUrl":"/v1/agents/` + id + `/commands","ttl_seconds":90}`))
		case r.URL.Path == "/v1/agents/agent-old/heartbeat" && r.Method == http.MethodPut:
			// The old agent has been evicted.
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"code":"NOT_FOUND"}`))
		case r.URL.Path == "/v1/agents/agent-new/heartbeat" && r.Method == http.MethodPut:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"ttl_seconds":90}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	reg := NewAgentRegistrationService(onlineClient(t, srv.URL), "1.0.0")
	ctx := context.Background()

	first, err := reg.RegisterAgent(ctx)
	if err != nil || first.AgentID != "agent-old" {
		t.Fatalf("first register = (%q, %v), want agent-old", first.AgentID, err)
	}

	// Heartbeat the old id → 404 → re-register.
	if !errors.Is(reg.Heartbeat(ctx, first.AgentID), ErrAgentNotFound) {
		t.Fatalf("expected ErrAgentNotFound heartbeating evicted agent")
	}
	second, err := reg.RegisterAgent(ctx)
	if err != nil || second.AgentID != "agent-new" {
		t.Fatalf("re-register = (%q, %v), want agent-new", second.AgentID, err)
	}
	// The new id heartbeats OK.
	if err := reg.Heartbeat(ctx, second.AgentID); err != nil {
		t.Fatalf("heartbeat new agent: %v", err)
	}
}

// registerBodies runs one RegisterAgent against a server that answers each
// POST with the next of responses, and returns every body it received.
func registerBodies(t *testing.T, reg func(*Client) *AgentRegistrationService, responses ...func(http.ResponseWriter)) ([]map[string]any, error) {
	t.Helper()
	t.Setenv(machineIDEnv, "test-machine-uuid")
	var bodies []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Errorf("register body is not JSON: %v", err)
		}
		bodies = append(bodies, body)
		if i := len(bodies) - 1; i < len(responses) {
			responses[i](w)
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	_, err := reg(onlineClient(t, srv.URL)).RegisterAgent(context.Background())
	return bodies, err
}

func created(w http.ResponseWriter) {
	w.WriteHeader(http.StatusCreated)
	_, _ = w.Write([]byte(`{"agentId":"9c1f0f2e-1111-4222-8333-444455556666","ttl_seconds":90}`))
}

func twoRepoWorkspace() (WorkspaceDeclaration, error) {
	return WorkspaceDeclaration{
		Repos:     []AgentRepo{{Owner: "acme", Repo: "api"}, {Owner: "acme", Repo: "web"}},
		Workspace: &AgentWorkspace{Slug: "acme-platform", DisplayName: "Acme Platform"},
	}, nil
}

// The declaration rides on the registration exactly as the hosted service's
// RegisterAgentSchema reads it (#2335).
func TestRegisterAgent_DeclaresTheWorkspace(t *testing.T) {
	bodies, err := registerBodies(t, func(c *Client) *AgentRegistrationService {
		return NewAgentRegistrationService(c, "1.2.3").WithWorkspace(twoRepoWorkspace)
	}, created)
	if err != nil {
		t.Fatalf("RegisterAgent: %v", err)
	}
	repos, _ := json.Marshal(bodies[0]["repos"])
	if string(repos) != `[{"owner":"acme","repo":"api"},{"owner":"acme","repo":"web"}]` {
		t.Errorf("repos = %s", repos)
	}
	ws, _ := bodies[0]["workspace"].(map[string]any)
	if len(ws) != 2 || ws["slug"] != "acme-platform" || ws["display_name"] != "Acme Platform" {
		t.Errorf("workspace = %v, want exactly slug and display_name", bodies[0]["workspace"])
	}
}

// Without a workspace source the body is what it was before #2335.
func TestRegisterAgent_NoWorkspaceSourceDeclaresNothing(t *testing.T) {
	bodies, err := registerBodies(t, func(c *Client) *AgentRegistrationService {
		return NewAgentRegistrationService(c, "1.2.3")
	}, created)
	if err != nil {
		t.Fatalf("RegisterAgent: %v", err)
	}
	for _, key := range []string{"repos", "workspace"} {
		if _, ok := bodies[0][key]; ok {
			t.Errorf("body carries %q with no workspace source: %v", key, bodies[0])
		}
	}
}

// A part the service would refuse is dropped, and the rest still registers: a
// 422 would cost the daemon its agent id, and the attention mirror with it.
func TestRegisterAgent_DropsAnOutOfBoundsPart(t *testing.T) {
	bodies, err := registerBodies(t, func(c *Client) *AgentRegistrationService {
		return NewAgentRegistrationService(c, "").WithWorkspace(func() (WorkspaceDeclaration, error) {
			d, _ := twoRepoWorkspace()
			d.Workspace.DisplayName = strings.Repeat("n", 201)
			return d, nil
		})
	}, created)
	if err != nil {
		t.Fatalf("RegisterAgent: %v", err)
	}
	if _, ok := bodies[0]["workspace"]; ok {
		t.Errorf("an out-of-bounds workspace block was sent: %v", bodies[0]["workspace"])
	}
	if repos, _ := bodies[0]["repos"].([]any); len(repos) != 2 {
		t.Errorf("repos = %v, want both still declared", bodies[0]["repos"])
	}
}

// A source that fails declares nothing and does not stop the registration.
func TestRegisterAgent_WorkspaceSourceErrorDeclaresNothing(t *testing.T) {
	bodies, err := registerBodies(t, func(c *Client) *AgentRegistrationService {
		return NewAgentRegistrationService(c, "").WithWorkspace(func() (WorkspaceDeclaration, error) {
			return WorkspaceDeclaration{}, errors.New("manifest unreadable")
		})
	}, created)
	if err != nil || len(bodies) != 1 {
		t.Fatalf("RegisterAgent: %v after %d posts", err, len(bodies))
	}
	if _, ok := bodies[0]["repos"]; ok {
		t.Errorf("repos declared from a failed source: %v", bodies[0])
	}
}

// An account in no team cannot name a workspace (403 NO_TEAM_MEMBERSHIP). The
// daemon registers again without the block, still declaring its repos, rather
// than failing every retry.
func TestRegisterAgent_NoTeamRegistersWithoutTheBlock(t *testing.T) {
	noTeam := func(w http.ResponseWriter) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"code":"NO_TEAM_MEMBERSHIP","message":"Account is not a member of any team"}`))
	}
	bodies, err := registerBodies(t, func(c *Client) *AgentRegistrationService {
		return NewAgentRegistrationService(c, "").WithWorkspace(twoRepoWorkspace)
	}, noTeam, created)
	if err != nil {
		t.Fatalf("RegisterAgent: %v", err)
	}
	if len(bodies) != 2 {
		t.Fatalf("%d posts, want the refused one and one retry", len(bodies))
	}
	if _, ok := bodies[1]["workspace"]; ok {
		t.Errorf("the retry still named the workspace: %v", bodies[1])
	}
	if repos, _ := bodies[1]["repos"].([]any); len(repos) != 2 {
		t.Errorf("the retry dropped the repos: %v", bodies[1]["repos"])
	}

	// Any other refusal is the caller's to retry, not a reason to drop the block.
	forbidden := func(w http.ResponseWriter) { w.WriteHeader(http.StatusForbidden) }
	bodies, err = registerBodies(t, func(c *Client) *AgentRegistrationService {
		return NewAgentRegistrationService(c, "").WithWorkspace(twoRepoWorkspace)
	}, forbidden, created)
	if err == nil || len(bodies) != 1 {
		t.Errorf("a plain 403 gave err=%v after %d posts, want an error after 1", err, len(bodies))
	}
}

// The registration reply names the workspace writes the operator's role kept
// it from making (#2372); a reply without the field, or with an empty list,
// decodes to none.
func TestRegisterAgent_DecodesRefusedWorkspaceWrites(t *testing.T) {
	cases := map[string]struct {
		reply string
		want  []RefusedWorkspaceWrite
	}{
		"refusals": {
			reply: `{"agentId":"a","ttl_seconds":90,"throttle":null,"refused_workspace_writes":[` +
				`{"workspace":"acme-platform","team_id":"team-1","code":"PERMISSION_DENIED","permission":"workspace:update","message":"Registration did not write workspace 'acme-platform': workspace:update needs the owner or admin role on its team"},` +
				`{"workspace":"default","team_id":"team-1","code":"PERMISSION_DENIED","permission":"workspace:update","message":"m"}]}`,
			want: []RefusedWorkspaceWrite{
				{Workspace: "acme-platform", TeamID: "team-1", Code: "PERMISSION_DENIED", Permission: "workspace:update",
					Message: "Registration did not write workspace 'acme-platform': workspace:update needs the owner or admin role on its team"},
				{Workspace: "default", TeamID: "team-1", Code: "PERMISSION_DENIED", Permission: "workspace:update", Message: "m"},
			},
		},
		"empty list":    {reply: `{"agentId":"a","ttl_seconds":90,"refused_workspace_writes":[]}`},
		"field missing": {reply: `{"agentId":"a","ttl_seconds":90}`},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Setenv(machineIDEnv, "test-machine-uuid")
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusCreated)
				_, _ = w.Write([]byte(tc.reply))
			}))
			defer srv.Close()
			info, err := NewAgentRegistrationService(onlineClient(t, srv.URL), "1.0.0").RegisterAgent(context.Background())
			if err != nil {
				t.Fatalf("RegisterAgent: %v", err)
			}
			if len(info.RefusedWorkspaceWrites) != len(tc.want) {
				t.Fatalf("refusals = %+v, want %+v", info.RefusedWorkspaceWrites, tc.want)
			}
			for i := range tc.want {
				if info.RefusedWorkspaceWrites[i] != tc.want[i] {
					t.Errorf("refusal %d = %+v, want %+v", i, info.RefusedWorkspaceWrites[i], tc.want[i])
				}
			}
		})
	}
}

// The operator's line names the workspace and the permission the write needs,
// and a reply cannot put control characters or unbounded text into it.
func TestRefusedWorkspaceWrite_Describe(t *testing.T) {
	named := RefusedWorkspaceWrite{Workspace: "acme-platform", TeamID: "team-1", Code: "PERMISSION_DENIED", Permission: "workspace:create"}
	line := named.Describe()
	for _, want := range []string{`workspace "acme-platform"`, "workspace:create", "owner or admin role", "team-1"} {
		if !strings.Contains(line, want) {
			t.Errorf("Describe() = %q, want it to contain %q", line, want)
		}
	}

	def := RefusedWorkspaceWrite{Workspace: "default", Permission: "workspace:update"}
	if line := def.Describe(); !strings.Contains(line, "the team's Default workspace") || !strings.Contains(line, "workspace:update") {
		t.Errorf("Describe() for the Default workspace = %q", line)
	}

	hostile := RefusedWorkspaceWrite{Workspace: "a\nb\x1b[31m" + strings.Repeat("x", 300), Permission: "workspace:update"}
	line = hostile.Describe()
	if strings.ContainsAny(line, "\n\x1b") {
		t.Errorf("Describe() kept a control character: %q", line)
	}
	if strings.Contains(line, strings.Repeat("x", 101)) {
		t.Errorf("Describe() did not bound the workspace field: %q", line)
	}

	// The fields printed as they are, not quoted, are the ones a reply could
	// forge a log line through: a line break, a line separator (U+2028), a
	// bidirectional override (U+202E), a zero-width joiner, a tab.
	forging := "workspace:update\n[nightgauge] forged line\u2028\u202eevil\u200d\t"
	forged := RefusedWorkspaceWrite{Workspace: "acme", TeamID: forging, Code: forging, Permission: forging}
	line = forged.Describe()
	for _, r := range []rune{'\n', '\u2028', '\u202e', '\u200d', '\t'} {
		if strings.ContainsRune(line, r) {
			t.Errorf("Describe() kept %U: %q", r, line)
		}
	}
	// Only the unprintable runes go; square brackets show as parentheses, so
	// no markdown link reaches the extension's notification.
	if !strings.Contains(line, "workspace:update(nightgauge) forged lineevil") {
		t.Errorf("Describe() dropped more than the unprintable runes: %q", line)
	}
}

// The status and the extension get the refusal's bounded fields and the
// operator's line, never the platform's raw message (#2372).
func TestRefusedWorkspaceWrite_Report(t *testing.T) {
	refused := RefusedWorkspaceWrite{
		Workspace: "acme-platform", TeamID: "team-1\u202e", Code: "PERMISSION_DENIED",
		Permission: "workspace:update", Message: strings.Repeat("raw message ", 1000),
	}
	got := refused.Report()
	want := RefusedWorkspaceWriteReport{
		Workspace: "acme-platform", TeamID: "team-1", Code: "PERMISSION_DENIED",
		Permission: "workspace:update", Description: refused.Describe(),
	}
	if got != want {
		t.Errorf("Report() = %+v, want %+v", got, want)
	}
	if strings.Contains(got.Description, "raw message") {
		t.Errorf("the report carries the platform's message: %q", got.Description)
	}
	if empty := (RefusedWorkspaceWrite{}).Report(); empty.Workspace != "unknown" || empty.Permission != "unknown" {
		t.Errorf("an empty refusal reports %+v, want unknown fields", empty)
	}
}

// The extension shows a refusal's line in a notification, which renders
// markdown link syntax as a link, and a command: link runs a command when
// clicked (#2372). A reply field cannot put one in front of the operator.
func TestRefusedWorkspaceWrite_ShowsNoMarkdownLink(t *testing.T) {
	link := "[Fix](command:workbench.action.terminal.sendSequence?%7B%22text%22%3A%22id%5Cn%22%7D)"
	refused := RefusedWorkspaceWrite{
		Workspace: link, TeamID: link, Code: "PERMISSION_DENIED", Permission: "[workspace:update]",
	}
	report := refused.Report()
	for name, field := range map[string]string{
		"description": report.Description, "workspace": report.Workspace,
		"teamId": report.TeamID, "permission": report.Permission,
	} {
		if strings.ContainsAny(field, "[]") {
			t.Errorf("%s keeps link syntax: %q", name, field)
		}
	}
	if !strings.Contains(report.Workspace, "(Fix)(command:") {
		t.Errorf("workspace = %q, want the brackets shown as parentheses", report.Workspace)
	}
}
