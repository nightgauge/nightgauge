package platform

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"unicode"

	"github.com/google/uuid"

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
// client.bearer() resolves to. POST /v1/agents/register upserts by
// (account, machine_id, workspace_key) when the body carries a workspace key
// (#1421), and by (account, machine_id) when it does not, so calling it on
// every daemon start is idempotent (re-register = revival), and each
// workspace the machine serves keeps its own agent.

// AgentRegisterCapabilityResolve is the capability the daemon advertises so the
// platform relays `attention_resolve` commands to it (the Action Center bridge).
const AgentRegisterCapabilityResolve = "attention_resolve"

// processInstanceID is this process's instance id on its platform agent
// (#2395). Every client of an agent (each editor window, and this daemon)
// advertises its own execution profile onto it, so the platform keeps one
// record per instance to tell one client whose profile changed from two that
// disagree. The registration and every heartbeat carry it.
//
// A random UUID v4, made once per process and kept in memory only: never
// persisted, and never derived from a path, host, user or workspace. The
// platform only compares it for equality and never returns it.
//
// A daemon an editor window spawned is handed that window's own instance id
// in AgentInstanceIDEnv (#2418) and carries it instead: the window's
// extension and its daemon register on two agent rows, and one id on both
// lets the platform count the window once.
var processInstanceID = sync.OnceValue(func() string {
	return resolveInstanceID(os.Getenv, uuid.NewString)
})

// AgentInstanceIDEnv names the environment variable through which the VS Code
// extension hands the daemon it spawns its activation's instance id (#2418;
// AGENT_INSTANCE_ID_ENV_VAR on the extension side).
const AgentInstanceIDEnv = "NIGHTGAUGE_AGENT_INSTANCE_ID"

// resolveInstanceID is the instance id AgentInstanceIDEnv hands this process
// when the platform accepts it, else a freshly minted one: an id the wire
// would drop is never adopted, so the process still has one.
func resolveInstanceID(getenv func(string) string, mint func() string) string {
	if handed := getenv(AgentInstanceIDEnv); wireInstanceID(handed) != "" {
		return handed
	}
	return mint()
}

// instanceIDPattern is what the platform accepts as an instance id: 1–64 of
// [A-Za-z0-9_-].
var instanceIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// wireInstanceID is id when the platform accepts it, else "", which leaves the
// field out: the bound on what leaves the machine is enforced at the wire.
func wireInstanceID(id string) string {
	if !instanceIDPattern.MatchString(id) {
		return ""
	}
	return id
}

// The workspace key (#1421). A machine runs one daemon per workspace it
// serves, and the attention store each daemon resolves a card against is the
// workspace's own (<root>/.nightgauge/attention), so the agent a relayed
// resolve is addressed to has to be the workspace's too. A registration that
// carries `workspace_key` is its own agent row on the hosted service, keyed on
// (account, machine_id, workspace_key), and a command addressed to it reaches
// only that workspace's command stream. One without it keeps the machine's
// unkeyed row, as every daemon did before, and every daemon on the machine
// then shares one agent and one command channel.
//
// The key is the HMAC-SHA256 of the workspace root in canonical form
// (absolute, symlinks resolved, cleaned), keyed by the machine id, in hex:
// 64 characters of [0-9a-f], inside the service's bound as it stands, so
// nothing is cut. It is never the path. Keying it by the machine id makes the
// same path on two machines two unrelated keys, so no precomputed table of
// digests of likely paths applies to it. The service compares it for
// equality only and never returns it.
//
// It is the same on every registration of one workspace: the root is put in
// canonical form once, when the daemon starts, and the machine id is stable,
// so a re-registration after an eviction keeps the workspace's own agent.
// Two spellings of one directory, a symlink and its target, give one key.

// workspaceKeyDomain separates this digest from any other the machine id
// keys.
const workspaceKeyDomain = "nightgauge/agent-workspace-key/v1"

// canonicalWorkspaceRoot is root absolute, with its symlinks resolved, and
// cleaned; "" when root is empty or cannot be made absolute. A root that
// cannot be resolved (it is gone) is kept absolute and cleaned as it is.
func canonicalWorkspaceRoot(root string) string {
	if root == "" {
		return ""
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return ""
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		abs = resolved
	}
	return filepath.Clean(abs)
}

// workspaceKey is the key of the workspace at canonicalRoot on the machine
// machineID, as the wire carries it: "" when there is no root, and "" (the
// field left out) when the key would fail the service's bound.
func workspaceKey(machineID, canonicalRoot string) string {
	if canonicalRoot == "" {
		return ""
	}
	mac := hmac.New(sha256.New, []byte(machineID))
	mac.Write([]byte(workspaceKeyDomain))
	mac.Write([]byte{0})
	mac.Write([]byte(canonicalRoot))
	return wireWorkspaceKey(hex.EncodeToString(mac.Sum(nil)))
}

// wireWorkspaceKey is key when the platform accepts it (1–64 of
// [A-Za-z0-9_-], the instance id's bound), else "", which leaves the field
// out: a malformed key would cost the whole registration a 422.
func wireWorkspaceKey(key string) string {
	if !instanceIDPattern.MatchString(key) {
		return ""
	}
	return key
}

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
// the service reads an absent `repos` as none. `instance_id` (#2395) rides
// top-level beside `execution_profile`, never inside it; a platform that
// predates it strips the unknown key. `workspace_key` (#1421) is the opaque
// key of the workspace root this agent serves (see workspaceKey), and makes
// the agent the workspace's own rather than the machine's.
type agentRegisterBody struct {
	MachineID        string            `json:"machine_id"`
	WorkspaceKey     string            `json:"workspace_key,omitempty"`
	AgentVersion     string            `json:"agent_version,omitempty"`
	Capabilities     []string          `json:"capabilities"`
	Repos            []AgentRepo       `json:"repos,omitempty"`
	Workspace        *AgentWorkspace   `json:"workspace,omitempty"`
	ExecutionProfile *ExecutionProfile `json:"execution_profile,omitempty"`
	InstanceID       string            `json:"instance_id,omitempty"`
}

// errNoTeamMembershipCode is the service's answer to a `workspace` block from
// an account that belongs to no team: 403 with this code. The agent row is
// already written by then, but the registration reports failure.
const errNoTeamMembershipCode = "NO_TEAM_MEMBERSHIP"

// agentHeartbeatBody is the PUT /v1/agents/:agentId/heartbeat body: the
// instance id on every beat (#2395), the profile when one resolved, and the
// capability list the agent advertises now (#1569). The list replaces the one
// stored at registration, so a switch to an adapter that cannot host a
// conversation turn drops `conversation` within one beat rather than at the
// next registration.
type agentHeartbeatBody struct {
	ExecutionProfile *ExecutionProfile `json:"execution_profile,omitempty"`
	InstanceID       string            `json:"instance_id,omitempty"`
	Capabilities     []string          `json:"capabilities,omitempty"`
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
	// instanceID is the process's instance id as the wire carries it ("" when
	// it fails the platform's bound, and is then left out).
	instanceID string
	// workspaceRoot is the canonical root of the workspace this agent serves
	// (#1421), the source of its workspace key; "" registers on the machine's
	// unkeyed agent.
	workspaceRoot string
}

// NewAgentRegistrationService builds a registration service bound to the platform
// client. agentVersion is the build version reported to the platform (pass ""
// to omit it — e.g. an unknown/dev build).
func NewAgentRegistrationService(client *Client, agentVersion string) *AgentRegistrationService {
	return &AgentRegistrationService{
		client:       client,
		agentVersion: agentVersion,
		instanceID:   wireInstanceID(processInstanceID()),
	}
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

// WithWorkspaceRoot sets the root of the workspace this agent serves, the one
// whose attention store it resolves cards against (#1421). Every registration
// then carries the workspace's key, so the hosted service gives this
// workspace its own agent and command stream. The root is put in canonical
// form here, once, so every registration of this daemon carries one key.
// Without it, or with a root that cannot be made absolute, the registration
// carries no key and shares the machine's agent.
func (s *AgentRegistrationService) WithWorkspaceRoot(root string) *AgentRegistrationService {
	s.workspaceRoot = canonicalWorkspaceRoot(root)
	if s.workspaceRoot == "" {
		log.Printf("agent registration: no workspace root, so the agent is the machine's own and a relayed resolve can reach another workspace's daemon (#1421)")
	}
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

// agentCapabilities is the capability list the daemon advertises on
// registration and on every heartbeat: attention_resolve always, and
// `conversation` only while the resolved profile's adapter can host a turn.
// A profile that did not resolve advertises no conversation.
func agentCapabilities(conversation bool) []string {
	caps := []string{AgentRegisterCapabilityResolve}
	if conversation {
		caps = append(caps, AgentCapabilityConversation)
	}
	return caps
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

// RegisterAgent POSTs the daemon's machine id, workspace key, capabilities
// and the workspace it serves to POST /v1/agents/register and returns the
// platform-assigned agent id + TTL. Idempotent server-side: the platform
// upserts on (account, machine_id, workspace_key), or on (account,
// machine_id) when no key is sent. A re-registration carries the same key, so
// it revives this workspace's agent and never another's. A non-201 response
// or a missing agentId is an error the caller retries with backoff.
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
		WorkspaceKey: workspaceKey(machineID, s.workspaceRoot),
		AgentVersion: s.agentVersion,
		InstanceID:   s.instanceID,
	}
	p, conversation := s.resolveProfile()
	body.ExecutionProfile = p
	body.Capabilities = agentCapabilities(conversation)
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
// platform TTL is 90s; the bridge heartbeats every 30s). Every beat carries
// the process's instance id (#2395). It returns ErrAgentNotFound on a 404 so
// the caller re-registers, and a generic error on any other non-2xx. Offline
// → nil (no-op; the sweep re-registers when online).
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
	p, conversation := s.resolveProfile()
	body := agentHeartbeatBody{
		InstanceID:       s.instanceID,
		ExecutionProfile: p,
		Capabilities:     agentCapabilities(conversation),
	}
	data, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("agent heartbeat: marshal: %w", err)
	}
	spec.Body = data
	spec.Headers["Content-Type"] = "application/json"
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
