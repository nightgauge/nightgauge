package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/agentworkspace"
	"github.com/nightgauge/nightgauge/internal/ipc"
	"github.com/nightgauge/nightgauge/internal/platform"
)

// extensionSide stands in for the IPC server the daemon's platform agent is
// wired to: it records every event relayed to the extension.
type extensionSide struct {
	mu     sync.Mutex
	events []ipc.Event
	// refusals records every SetRefusedWorkspaceWrites call, one per
	// registration (#2372).
	refusals [][]platform.RefusedWorkspaceWrite
}

func (e *extensionSide) SetRefusedWorkspaceWrites(refused []platform.RefusedWorkspaceWrite) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.refusals = append(e.refusals, refused)
}

func (e *extensionSide) recordedRefusals() [][]platform.RefusedWorkspaceWrite {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([][]platform.RefusedWorkspaceWrite(nil), e.refusals...)
}

func (e *extensionSide) ApplyRelayedResolve(context.Context, string, string, string, string) (platform.AttentionResolveOutcome, error) {
	return platform.AttentionResolveOutcome{Applied: true}, nil
}

func (e *extensionSide) Emit(event string, data interface{}) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.events = append(e.events, ipc.Event{Event: event, Data: data})
}

func (e *extensionSide) relayed() []ipc.Event {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]ipc.Event(nil), e.events...)
}

func writeTestFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func waitUntil(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// The platform agent serve starts (#2335): its registration declares the
// workspace of the VS Code window that started the daemon, both repos and the
// named workspace block, although the daemon's --workspace is one member of
// it; and a command the daemon does not execute is relayed to the extension
// whole, under the agent id the platform addressed.
func TestRunDaemonPlatformAgent_DeclaresTheWindowsWorkspaceAndRelays(t *testing.T) {
	t.Setenv("NIGHTGAUGE_AGENT_ID", "test-machine-uuid")

	// Window [ws, ws/api, ws/web]: the manifest is in ws, which has no project
	// config, so the extension starts the daemon with --workspace ws/api.
	ws := t.TempDir()
	writeTestFile(t, filepath.Join(ws, ".vscode", "nightgauge-workspace.yaml"),
		"workspace:\n  name: Acme Platform\nrepositories:\n  - name: api\n    path: api\n  - name: web\n    path: web\n")
	writeTestFile(t, filepath.Join(ws, "api", ".nightgauge", "config.yaml"), "github:\n  owner: acme\n  repo: api\n")
	writeTestFile(t, filepath.Join(ws, "web", ".nightgauge", "config.yaml"), "owner: acme\nrepo: web\n")
	windowJSON, _ := json.Marshal([]string{ws, filepath.Join(ws, "api"), filepath.Join(ws, "web")})
	t.Setenv(agentworkspace.WindowFoldersEnv, string(windowJSON))

	const frame = `{"commandId":"cmd-pause-1","type":"pause","commandType":"pause","payload":{"runId":"run-9"},"owner":"acme","repo":"web","createdAt":"2026-10-01T00:00:00.000Z","expiresAt":"2026-10-01T00:05:00.000Z"}`
	var mu sync.Mutex
	var registered map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/agents/register":
			raw, _ := io.ReadAll(r.Body)
			mu.Lock()
			_ = json.Unmarshal(raw, &registered)
			mu.Unlock()
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"agentId":"agent-daemon","ttl_seconds":90}`))
		case r.Method == http.MethodGet && r.URL.Path == "/v1/agents/agent-daemon/commands":
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			_, _ = fmt.Fprintf(w, "event: command\ndata: %s\n\n", frame)
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
			<-r.Context().Done()
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	client, err := platform.NewClient(platform.Config{BaseURL: srv.URL, APIKey: "test-key"})
	if err != nil {
		t.Fatal(err)
	}
	ext := &extensionSide{}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		// Exactly as serve calls it.
		runDaemonPlatformAgent(ctx, client, platform.NewAttentionSyncService(client), ext, "test",
			filepath.Join(ws, "api"), agentworkspace.WindowFolders(os.Getenv))
	}()
	defer func() {
		cancel()
		<-done
	}()

	waitUntil(t, "the relayed command", func() bool { return len(ext.relayed()) == 1 })

	mu.Lock()
	body := registered
	mu.Unlock()
	wantRepos := []any{
		map[string]any{"owner": "acme", "repo": "api"},
		map[string]any{"owner": "acme", "repo": "web"},
	}
	if !reflect.DeepEqual(body["repos"], wantRepos) {
		t.Errorf("registered repos = %v, want the window's two repos %v", body["repos"], wantRepos)
	}
	if want := map[string]any{"slug": "acme-platform", "display_name": "Acme Platform"}; !reflect.DeepEqual(body["workspace"], want) {
		t.Errorf("registered workspace = %v, want %v", body["workspace"], want)
	}

	got := ext.relayed()[0]
	if got.Event != ipc.EventAgentCommand {
		t.Fatalf("relayed on event %q, want %q", got.Event, ipc.EventAgentCommand)
	}
	relayed, ok := got.Data.(ipc.AgentCommandEvent)
	if !ok {
		t.Fatalf("relayed data is %T, want ipc.AgentCommandEvent", got.Data)
	}
	if relayed.AgentID != "agent-daemon" || string(relayed.Frame) != frame {
		t.Errorf("relayed %s %s, want the platform's frame under agent-daemon", relayed.AgentID, relayed.Frame)
	}
}

// A registration the platform refused workspace writes (#2372) is reported
// once: one log line per refusal naming the workspace and the permission it
// needs, and the daemon's status, which the registration replaces.
func TestRunDaemonPlatformAgent_ReportsRefusedWorkspaceWrites(t *testing.T) {
	t.Setenv("NIGHTGAUGE_AGENT_ID", "test-machine-uuid")
	t.Setenv(agentworkspace.WindowFoldersEnv, "")
	logs := &syncBuffer{}
	prev := log.Writer()
	log.SetOutput(logs)
	defer log.SetOutput(prev)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/agents/register":
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"agentId":"agent-daemon","ttl_seconds":90,"throttle":null,"refused_workspace_writes":[` +
				`{"workspace":"acme-platform","team_id":"team-1","code":"PERMISSION_DENIED","permission":"workspace:update","message":"m"}]}`))
		case r.Method == http.MethodGet && r.URL.Path == "/v1/agents/agent-daemon/commands":
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
			<-r.Context().Done()
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	client, err := platform.NewClient(platform.Config{BaseURL: srv.URL, APIKey: "test-key"})
	if err != nil {
		t.Fatal(err)
	}
	ext := &extensionSide{}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		runDaemonPlatformAgent(ctx, client, platform.NewAttentionSyncService(client), ext, "test", t.TempDir(), nil)
	}()
	defer func() {
		cancel()
		<-done
	}()

	waitUntil(t, "the registration's refusals", func() bool { return len(ext.recordedRefusals()) == 1 })
	got := ext.recordedRefusals()[0]
	if len(got) != 1 || got[0].Workspace != "acme-platform" || got[0].Permission != "workspace:update" {
		t.Fatalf("recorded refusals = %+v, want the one acme-platform refusal", got)
	}
	waitUntil(t, "the refusal's log line", func() bool {
		return strings.Contains(logs.String(), `agent registration: the platform did not write workspace "acme-platform": workspace:update needs the owner or admin role`)
	})
	if n := strings.Count(logs.String(), "the platform did not write"); n != 1 {
		t.Errorf("logged the refusal %d times, want once per registration", n)
	}
}
