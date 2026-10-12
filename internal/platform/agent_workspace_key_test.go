package platform

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// registrationRecorder is a hosted service that records every registration
// body, raw, and answers each with a fresh agent id.
type registrationRecorder struct {
	mu     sync.Mutex
	bodies []string
}

func (r *registrationRecorder) server(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		switch {
		case req.Method == http.MethodPost && req.URL.Path == "/v1/agents/register":
			raw, _ := io.ReadAll(req.Body)
			r.mu.Lock()
			r.bodies = append(r.bodies, string(raw))
			r.mu.Unlock()
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"agentId":"agent-1","ttl_seconds":90}`))
		case req.Method == http.MethodPut && strings.HasSuffix(req.URL.Path, "/heartbeat"):
			// Evicted: the caller re-registers.
			w.WriteHeader(http.StatusNotFound)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// keys is the workspace_key of every recorded body, in order ("" when the
// body carries none).
func (r *registrationRecorder) keys(t *testing.T) []string {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	keys := make([]string, 0, len(r.bodies))
	for _, raw := range r.bodies {
		var body map[string]json.RawMessage
		if err := json.Unmarshal([]byte(raw), &body); err != nil {
			t.Fatalf("register body is not JSON: %v", err)
		}
		var key string
		if v, ok := body["workspace_key"]; ok {
			if err := json.Unmarshal(v, &key); err != nil {
				t.Fatalf("workspace_key is not a string: %s", v)
			}
			if key == "" {
				t.Errorf("workspace_key sent empty; an absent key must be left out")
			}
		}
		keys = append(keys, key)
	}
	return keys
}

func (r *registrationRecorder) raw() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.bodies...)
}

// The acceptance case for #1421 on the registration side: two daemons on one
// machine, serving workspaces A and B, register under two different keys, so
// the hosted service gives each its own agent and command stream. A daemon
// always sends the same key, on its first registration, after an eviction,
// and after a restart, and no part of either root reaches the wire.
func TestWorkspaceKey_TwoDaemonsOneMachine(t *testing.T) {
	t.Setenv(machineIDEnv, "test-machine-uuid")
	base := t.TempDir()
	rootA := filepath.Join(base, "ws-alpha-7f3c", "checkout")
	rootB := filepath.Join(base, "ws-bravo-91d2", "checkout")
	for _, root := range []string{rootA, rootB} {
		if err := os.MkdirAll(root, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	rec := &registrationRecorder{}
	srv := rec.server(t)
	ctx := context.Background()

	daemonA := NewAgentRegistrationService(onlineClient(t, srv.URL), "1.2.3").WithWorkspaceRoot(rootA)
	daemonB := NewAgentRegistrationService(onlineClient(t, srv.URL), "1.2.3").WithWorkspaceRoot(rootB)

	// A registers, B registers, A is evicted and re-registers, and A's daemon
	// restarts and registers again.
	if _, err := daemonA.RegisterAgent(ctx); err != nil {
		t.Fatalf("A register: %v", err)
	}
	if _, err := daemonB.RegisterAgent(ctx); err != nil {
		t.Fatalf("B register: %v", err)
	}
	if err := daemonA.Heartbeat(ctx, "agent-1"); !errors.Is(err, ErrAgentNotFound) {
		t.Fatalf("A heartbeat = %v, want ErrAgentNotFound", err)
	}
	if _, err := daemonA.RegisterAgent(ctx); err != nil {
		t.Fatalf("A re-register: %v", err)
	}
	restartedA := NewAgentRegistrationService(onlineClient(t, srv.URL), "1.2.3").WithWorkspaceRoot(rootA)
	if _, err := restartedA.RegisterAgent(ctx); err != nil {
		t.Fatalf("restarted A register: %v", err)
	}

	keys := rec.keys(t)
	if len(keys) != 4 {
		t.Fatalf("recorded %d registrations, want 4", len(keys))
	}
	keyA, keyB, reKeyA, restartKeyA := keys[0], keys[1], keys[2], keys[3]
	for name, key := range map[string]string{"A": keyA, "B": keyB} {
		if !instanceIDPattern.MatchString(key) {
			t.Errorf("%s's workspace_key %q is outside the service's bound ^[A-Za-z0-9_-]{1,64}$", name, key)
		}
	}
	if keyA == keyB {
		t.Errorf("A and B, two workspaces on one machine, sent one key %q: they would share one agent", keyA)
	}
	if reKeyA != keyA {
		t.Errorf("A re-registered after eviction with key %q, want its own %q", reKeyA, keyA)
	}
	if restartKeyA != keyA {
		t.Errorf("A's restarted daemon registered with key %q, want %q", restartKeyA, keyA)
	}

	// The key is a digest of the root, never the root: no spelling or part of
	// either path is on the wire.
	for _, raw := range rec.raw() {
		for _, root := range []string{rootA, rootB} {
			for _, part := range []string{root, canonicalWorkspaceRoot(root), filepath.Base(filepath.Dir(root)), filepath.ToSlash(root)} {
				if strings.Contains(raw, part) {
					t.Errorf("register body carries %q from a workspace root: %s", part, raw)
				}
			}
		}
	}
}

// Every spelling of one directory is one workspace: relative, with dot
// segments, or through a symlink to it. A daemon started through a symlink
// keeps the agent the directory already has.
func TestWorkspaceKey_SpellingsOfOneRootAgree(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "real", "ws")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(filepath.Join(base, "real"), link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	want := workspaceKey("m", canonicalWorkspaceRoot(root))
	if want == "" {
		t.Fatal("no key for an existing root")
	}
	for _, spelling := range []string{
		root + string(filepath.Separator),
		filepath.Join(base, "real", ".", "ws"),
		base + string(filepath.Separator) + filepath.Join("real", "x", "..", "ws"),
		filepath.Join(link, "ws"),
	} {
		if got := workspaceKey("m", canonicalWorkspaceRoot(spelling)); got != want {
			t.Errorf("key for %q = %q, want %q (the key for %q)", spelling, got, want, root)
		}
	}
}

// The key depends on the machine as well as the root: one path on two
// machines is two unrelated keys, so a digest of a likely path from one
// machine says nothing about another.
func TestWorkspaceKey_DependsOnTheMachine(t *testing.T) {
	root := canonicalWorkspaceRoot(t.TempDir())
	if workspaceKey("machine-one", root) == workspaceKey("machine-two", root) {
		t.Error("one root on two machines gave one key")
	}
	if workspaceKey("machine-one", root) != workspaceKey("machine-one", root) {
		t.Error("the key is not a function of the machine and the root")
	}
}

// A daemon with no workspace root registers as before #1421: the body leaves
// workspace_key out, and the service keeps the machine's unkeyed agent.
func TestWorkspaceKey_NoRootSendsNoKey(t *testing.T) {
	t.Setenv(machineIDEnv, "test-machine-uuid")
	for name, reg := range map[string]func(*Client) *AgentRegistrationService{
		"no root set": func(c *Client) *AgentRegistrationService { return NewAgentRegistrationService(c, "1.2.3") },
		"empty root": func(c *Client) *AgentRegistrationService {
			return NewAgentRegistrationService(c, "1.2.3").WithWorkspaceRoot("")
		},
	} {
		rec := &registrationRecorder{}
		srv := rec.server(t)
		if _, err := reg(onlineClient(t, srv.URL)).RegisterAgent(context.Background()); err != nil {
			t.Fatalf("%s: RegisterAgent: %v", name, err)
		}
		for _, raw := range rec.raw() {
			if strings.Contains(raw, "workspace_key") {
				t.Errorf("%s: body carries workspace_key: %s", name, raw)
			}
		}
	}
}

// Only a key inside the service's bound leaves the machine; anything else is
// left out rather than costing the registration a 422.
func TestWireWorkspaceKey_KeepsOnlyTheBound(t *testing.T) {
	for _, tc := range []struct {
		key, want string
	}{
		{"0123abcd", "0123abcd"},
		{strings.Repeat("a", 64), strings.Repeat("a", 64)},
		{strings.Repeat("a", 65), ""},
		{"", ""},
		{"/home/user/workspace", ""},
		{"has space", ""},
	} {
		if got := wireWorkspaceKey(tc.key); got != tc.want {
			t.Errorf("wireWorkspaceKey(%q) = %q, want %q", tc.key, got, tc.want)
		}
	}
	if got := workspaceKey("m", "/a/root"); len(got) != 64 {
		t.Errorf("workspaceKey is %d characters, want the full 64 of hex SHA-256", len(got))
	}
}
