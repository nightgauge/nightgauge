package platform

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"unicode"

	api "github.com/nightgauge/nightgauge/api/generated/go/platform"
)

// Action Center agent registration — the daemon self-registers as a platform
// agent so its DecisionRequest mirror (attention_sync.go) carries a real
// `agent_id` (nightgauge/nightgauge#341). The Go binary's local attention
// store (in the checkout's CHECKOUT) is the authoritative writer; the
// platform's `decision_requests` table has an FK `decision_requests_agent_id_agents_id_fk`
// → `agents.id`, so mirroring a request whose `agent_id` is an unregistered id
// (the daemon's machine id) throws and 500s the whole sweep. Registering here
// creates the `agents` row the FK requires, and returns the platform-assigned
// UUID the sync + command poller must use instead of the machine id.
//
// This rides the same bearer auth and raw-HTTP shape the sync uses
// (see attention_sync.go): the platform's pipelineAuth accepts whatever
// client.bearer() resolves to. POST /v1/agents/register upserts by machine_id
// per account, so calling it on every daemon start is idempotent (re-register =
// revival).

// AgentRegisterCapabilityResolve is the capability the daemon advertises so the
// platform relays `attention_resolve` commands to it (the Action Center bridge).
const AgentRegisterCapabilityResolve = "attention_resolve"

// ErrAgentNotFound is returned by Heartbeat when the platform reports the agent
// no longer exists (HTTP 404 — evicted after the 90s TTL, or the platform lost
// the row). The bridge treats it as a signal to re-register and swap in the new
// agent id rather than a transient transport error.
var ErrAgentNotFound = errors.New("agent registration: agent not found")

// AgentRegistration is the platform's 201 response to POST /v1/agents/register.
type AgentRegistration struct {
	AgentID     string `json:"agentId"`
	CommandsURL string `json:"commandsUrl"`
	TTLSeconds  int    `json:"ttl_seconds"`
	// RefusedWorkspaceWrites are the workspace writes this registration was
	// refused (#2372): creating the named workspace, updating its agent or
	// display name, or linking the declared repositories to it, which need
	// the owner or admin role on the workspace's team. The agent registers
	// all the same, so nothing else tells the operator; the daemon reports
	// each one (see RefusedWorkspaceWrite.Describe). Empty when none was.
	RefusedWorkspaceWrites []RefusedWorkspaceWrite `json:"refused_workspace_writes,omitempty"`
}

// RefusedWorkspaceWrite is one workspace write the platform refused a
// registration, as its reply carries it.
type RefusedWorkspaceWrite struct {
	// Workspace is the named workspace's slug, or "default" for the team's
	// Default workspace the declared repositories are linked to.
	Workspace string `json:"workspace"`
	TeamID    string `json:"team_id"`
	// Code is the refusal's code, PERMISSION_DENIED.
	Code string `json:"code"`
	// Permission is the permission the write needs: workspace:create or
	// workspace:update.
	Permission string `json:"permission"`
	// Message is the platform's own sentence for the refusal.
	Message string `json:"message"`
}

// Describe is the operator's line for the refusal: the workspace that was not
// written and the permission the write needs. It is built from the fields,
// each cut to a bounded printable form, so a reply cannot put arbitrary text
// into the daemon's log.
func (r RefusedWorkspaceWrite) Describe() string {
	workspace := fmt.Sprintf("workspace %q", printableField(r.Workspace))
	if r.Workspace == "default" {
		workspace = "the team's Default workspace"
	}
	return fmt.Sprintf("the platform did not write %s: %s needs the owner or admin role on its team (team %s, %s); repositories this agent declares stay unlinked from it, so a remote trigger for one is refused",
		workspace, printableField(r.Permission), printableField(r.TeamID), printableField(r.Code))
}

// RefusedWorkspaceWriteReport is a refusal as the daemon reports it to a
// client (#2372): every field cut to its bounded printable form, and the
// operator's line, never the platform's own message, which is unbounded.
type RefusedWorkspaceWriteReport struct {
	Workspace  string `json:"workspace"`
	TeamID     string `json:"teamId"`
	Code       string `json:"code"`
	Permission string `json:"permission"`
	// Description is Describe(): the workspace that was not written and the
	// permission the write needs.
	Description string `json:"description"`
}

// Report is the refusal's bounded form, for the daemon's status and the
// extension.
func (r RefusedWorkspaceWrite) Report() RefusedWorkspaceWriteReport {
	return RefusedWorkspaceWriteReport{
		Workspace:   printableField(r.Workspace),
		TeamID:      printableField(r.TeamID),
		Code:        printableField(r.Code),
		Permission:  printableField(r.Permission),
		Description: r.Describe(),
	}
}

// printableField bounds one field of a platform reply for a log line and
// the operator's notification: at most 100 runes, keeping only graphic ones
// (letters, marks, numbers, punctuation, symbols and spaces), so no control,
// format or separator character, such as a line break, U+2028 or a
// bidirectional override, reaches the line. Square brackets become
// parentheses: the extension shows the line in a notification, which renders
// markdown link syntax as a link, and a `command:` link runs a command.
func printableField(s string) string {
	var b strings.Builder
	n := 0
	for _, r := range s {
		if !unicode.IsGraphic(r) {
			continue
		}
		if n == 100 {
			b.WriteString("…")
			break
		}
		switch r {
		case '[':
			r = '('
		case ']':
			r = ')'
		}
		b.WriteRune(r)
		n++
	}
	if b.Len() == 0 {
		return "unknown"
	}
	return b.String()
}

// agentRegisterBody is the POST /v1/agents/register request body. The platform's
// RegisterAgentSchema (zod) requires `machine_id`; `agent_version` is optional
// (omitted when the build version is unknown) and `capabilities` is always sent.
// `repos` and `workspace` declare the workspace this daemon serves (#2335);
// the service reads an absent `repos` as none.
type agentRegisterBody struct {
	MachineID        string            `json:"machine_id"`
	AgentVersion     string            `json:"agent_version,omitempty"`
	Capabilities     []string          `json:"capabilities"`
	Repos            []AgentRepo       `json:"repos,omitempty"`
	Workspace        *AgentWorkspace   `json:"workspace,omitempty"`
	ExecutionProfile *ExecutionProfile `json:"execution_profile,omitempty"`
}

// errNoTeamMembershipCode is the service's answer to a `workspace` block from
// an account that belongs to no team: 403 with this code. The agent row is
// already written by then, but the registration reports failure.
const errNoTeamMembershipCode = "NO_TEAM_MEMBERSHIP"

// agentHeartbeatBody is the PUT /v1/agents/:agentId/heartbeat body. It is only
// sent when a profile resolved; otherwise the beat stays the bodiless PUT the
// platform has always accepted.
type agentHeartbeatBody struct {
	ExecutionProfile *ExecutionProfile `json:"execution_profile,omitempty"`
}

// ProfileFunc resolves the execution profile to advertise and whether the
// workspace can host a conversational turn (#1567). It is called on every
// registration and every heartbeat, so a changed adapter, mode or effort
// reaches the platform within one beat. An error advertises no profile.
type ProfileFunc func() (profile ExecutionProfile, conversation bool, err error)

// AgentRegistrationService registers this daemon as a platform agent and keeps
// it alive with heartbeats. It holds no id itself — the caller (the serve
// bridge) owns the registered agent id and late-binds it onto the sync + poller.
type AgentRegistrationService struct {
	client       *Client
	agentVersion string
	profile      ProfileFunc
	workspace    WorkspaceFunc
}

// NewAgentRegistrationService builds a registration service bound to the platform
// client. agentVersion is the build version reported to the platform (pass ""
// to omit it — e.g. an unknown/dev build).
func NewAgentRegistrationService(client *Client, agentVersion string) *AgentRegistrationService {
	return &AgentRegistrationService{client: client, agentVersion: agentVersion}
}

// WithExecutionProfile sets the profile source advertised on registration and
// heartbeat. Without one, both bodies are exactly what they were before #1567.
func (s *AgentRegistrationService) WithExecutionProfile(fn ProfileFunc) *AgentRegistrationService {
	s.profile = fn
	return s
}

// WithWorkspace sets the source of the workspace this daemon declares it
// serves (#2335). Without one, the registration declares no repos and no
// workspace, and covers no workspace on the hosted service.
func (s *AgentRegistrationService) WithWorkspace(fn WorkspaceFunc) *AgentRegistrationService {
	s.workspace = fn
	return s
}

// resolveWorkspace runs the workspace source and keeps only what the hosted
// service will accept. A part outside the service's bounds would cost the
// whole registration a 422, and with it the agent id the attention mirror
// needs, so it is dropped and logged instead.
func (s *AgentRegistrationService) resolveWorkspace() ([]AgentRepo, *AgentWorkspace) {
	if s.workspace == nil {
		return nil, nil
	}
	decl, err := s.workspace()
	if err != nil {
		log.Printf("agent registration: workspace not declared: %v", err)
		return nil, nil
	}
	repos, block := decl.Repos, decl.Workspace
	if err := validateRepos(repos); err != nil {
		log.Printf("agent registration: repos not declared: %v", err)
		repos = nil
	}
	if err := validateWorkspace(block); err != nil {
		log.Printf("agent registration: workspace block not sent: %v", err)
		block = nil
	}
	return repos, block
}

// resolveProfile runs the profile source and re-validates what it returned:
// the bound on what leaves the machine is enforced here, at the wire, not
// trusted to the source.
func (s *AgentRegistrationService) resolveProfile() (*ExecutionProfile, bool) {
	if s.profile == nil {
		return nil, false
	}
	p, conversation, err := s.profile()
	if err != nil || p.Validate() != nil {
		return nil, false
	}
	return &p, conversation
}

// RegisterAgent POSTs the daemon's machine id, capabilities and the workspace
// it serves to POST /v1/agents/register and returns the platform-assigned
// agent id + TTL. Idempotent server-side (upsert by machine_id per account). A
// non-201 response or a missing agentId is an error the caller retries with
// backoff.
//
// A `workspace` block needs the account to belong to a team. When the service
// answers NO_TEAM_MEMBERSHIP, the registration is sent once more without the
// block, still declaring the repos: an account with no team then registers as
// it did before #2335, instead of failing every retry for good.
func (s *AgentRegistrationService) RegisterAgent(ctx context.Context) (AgentRegistration, error) {
	if s == nil || s.client == nil {
		return AgentRegistration{}, fmt.Errorf("agent registration: no platform client")
	}
	machineID, err := MachineID()
	if err != nil {
		return AgentRegistration{}, fmt.Errorf("agent registration: %w", err)
	}
	body := agentRegisterBody{
		MachineID:    machineID,
		AgentVersion: s.agentVersion,
		Capabilities: []string{AgentRegisterCapabilityResolve},
	}
	if p, conversation := s.resolveProfile(); p != nil {
		body.ExecutionProfile = p
		if conversation {
			body.Capabilities = append(body.Capabilities, AgentCapabilityConversation)
		}
	}
	body.Repos, body.Workspace = s.resolveWorkspace()

	info, status, code, err := s.postRegister(ctx, body)
	if err != nil && status == http.StatusForbidden && code == errNoTeamMembershipCode && body.Workspace != nil {
		log.Printf("agent registration: the account belongs to no team, so workspace %q cannot be named; registering without it", body.Workspace.Slug)
		body.Workspace = nil
		info, _, _, err = s.postRegister(ctx, body)
	}
	return info, err
}

// postRegister sends one registration. On a non-201 it returns the status and
// the response's error code, so the caller can tell a refusal it can recover
// from from one it cannot.
func (s *AgentRegistrationService) postRegister(ctx context.Context, body agentRegisterBody) (AgentRegistration, int, string, error) {
	data, err := json.Marshal(body)
	if err != nil {
		return AgentRegistration{}, 0, "", fmt.Errorf("agent registration: marshal: %w", err)
	}

	req, err := s.client.newRequest(ctx, requestSpec{
		Op:   api.OpAgentsRegister,
		Body: data,
		Headers: map[string]string{
			"Content-Type": "application/json",
			"Accept":       "application/json",
		},
	})
	if err != nil {
		return AgentRegistration{}, 0, "", fmt.Errorf("agent registration: request: %w", err)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return AgentRegistration{}, 0, "", fmt.Errorf("agent registration: POST: %w", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusCreated {
		var refusal struct {
			Code string `json:"code"`
		}
		_ = json.Unmarshal(respBody, &refusal)
		return AgentRegistration{}, resp.StatusCode, refusal.Code, fmt.Errorf("agent registration: server returned %d", resp.StatusCode)
	}

	var parsed AgentRegistration
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return AgentRegistration{}, resp.StatusCode, "", fmt.Errorf("agent registration: parse response: %w", err)
	}
	if parsed.AgentID == "" {
		return AgentRegistration{}, resp.StatusCode, "", fmt.Errorf("agent registration: response missing agentId")
	}
	return parsed, resp.StatusCode, "", nil
}

// Heartbeat PUTs /v1/agents/:agentId/heartbeat to keep the agent alive (the
// platform TTL is 90s; the bridge heartbeats every 30s). It returns
// ErrAgentNotFound on a 404 so the caller re-registers, and a generic error on
// any other non-2xx. Offline → nil (no-op; the sweep re-registers when online).
func (s *AgentRegistrationService) Heartbeat(ctx context.Context, agentID string) error {
	if s == nil || s.client == nil {
		return fmt.Errorf("agent heartbeat: no platform client")
	}
	if agentID == "" {
		return fmt.Errorf("agent heartbeat: agentId not set")
	}
	spec := requestSpec{
		Op:       api.OpAgentsHeartbeat,
		PathArgs: []string{agentID},
		Headers:  map[string]string{"Accept": "application/json"},
	}
	if p, _ := s.resolveProfile(); p != nil {
		data, err := json.Marshal(agentHeartbeatBody{ExecutionProfile: p})
		if err != nil {
			return fmt.Errorf("agent heartbeat: marshal: %w", err)
		}
		spec.Body = data
		spec.Headers["Content-Type"] = "application/json"
	}
	req, err := s.client.newRequest(ctx, spec)
	if err != nil {
		return fmt.Errorf("agent heartbeat: request: %w", err)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("agent heartbeat: PUT: %w", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode == http.StatusNotFound {
		return ErrAgentNotFound
	}
	if resp.StatusCode >= 400 {
		return fmt.Errorf("agent heartbeat: server returned %d", resp.StatusCode)
	}
	return nil
}
