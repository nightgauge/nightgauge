package main

import (
	"context"
	"encoding/json"
	"errors"
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
	"sync/atomic"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/agentworkspace"
	"github.com/nightgauge/nightgauge/internal/ipc"
	"github.com/nightgauge/nightgauge/internal/orchestrator"
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
	// sessionListeners are the daemon's session listeners (#2352).
	sessionListeners []func()
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

// OnSessionToken records the daemon's session listener (#2352); a test calls
// sessionChanged to play the extension pushing a session.
func (e *extensionSide) OnSessionToken(fn func()) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.sessionListeners = append(e.sessionListeners, fn)
}

func (e *extensionSide) sessionChanged() {
	e.mu.Lock()
	listeners := append([]func(){}, e.sessionListeners...)
	e.mu.Unlock()
	for _, fn := range listeners {
		fn()
	}
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
			filepath.Join(ws, "api"), agentworkspace.WindowFolders(os.Getenv), nil)
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
		runDaemonPlatformAgent(ctx, client, platform.NewAttentionSyncService(client), ext, "test", t.TempDir(), nil, nil)
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

// The daemon follows the platform throttle of the workspace it serves (#2352),
// by its slug and only with a signed-in session: it reads it after it
// registers and again on a `throttle` command, which it still relays. A
// throttle on another workspace never applies.
func TestRunDaemonPlatformAgent_FollowsItsWorkspaceThrottle(t *testing.T) {
	t.Setenv("NIGHTGAUGE_AGENT_ID", "test-machine-uuid")
	ws := t.TempDir()
	writeTestFile(t, filepath.Join(ws, ".vscode", "nightgauge-workspace.yaml"),
		"workspace:\n  name: Acme Platform\nrepositories:\n  - name: api\n    path: api\n")
	writeTestFile(t, filepath.Join(ws, "api", ".nightgauge", "config.yaml"), "github:\n  owner: acme\n  repo: api\n")
	windowJSON, _ := json.Marshal([]string{ws, filepath.Join(ws, "api")})
	t.Setenv(agentworkspace.WindowFoldersEnv, string(windowJSON))

	const throttleFrame = `{"commandId":"cmd-throttle-1","type":"throttle","commandType":"throttle","payload":{"action":"set","maxConcurrent":0,"resumeAt":null},"createdAt":"2026-10-02T00:00:00.000Z"}`
	var mu sync.Mutex
	workspaceCap := 2
	sendThrottle := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/agents/register":
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"agentId":"agent-daemon","ttl_seconds":90,"throttle":{"maxConcurrent":0,"resumeAt":null}}`))
		case r.Method == http.MethodGet && r.URL.Path == "/v1/workspaces":
			if got := r.Header.Get("Authorization"); got != "Bearer header.payload.signature" {
				t.Errorf("workspace list read with %q, want the session", got)
			}
			mu.Lock()
			body := fmt.Sprintf(`{"workspaces":[{"slug":"other","throttle":{"maxConcurrent":0,"resumeAt":null}},{"slug":"acme-platform","throttle":{"maxConcurrent":%d,"resumeAt":null}}]}`, workspaceCap)
			mu.Unlock()
			_, _ = w.Write([]byte(body))
		case r.Method == http.MethodGet && r.URL.Path == "/v1/agents/agent-daemon/commands":
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
			select {
			case <-sendThrottle:
				_, _ = fmt.Fprintf(w, "event: command\ndata: %s\n\n", throttleFrame)
				if f, ok := w.(http.Flusher); ok {
					f.Flush()
				}
			case <-r.Context().Done():
				return
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
	client.SetSessionToken("header.payload.signature")
	throttle := orchestrator.NewDispatchThrottle()
	ext := &extensionSide{}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		runDaemonPlatformAgent(ctx, client, platform.NewAttentionSyncService(client), ext, "test",
			filepath.Join(ws, "api"), agentworkspace.WindowFolders(os.Getenv), throttle)
	}()
	defer func() {
		cancel()
		<-done
	}()

	// Registered: its own workspace's cap, 2, not the agent-wide 0 the
	// registration reply carries, nor the other workspace's 0.
	waitUntil(t, "the throttle read after registration", func() bool { return throttle.Ceiling(5) == 2 })

	// A throttle command makes the daemon read again; it is still relayed.
	mu.Lock()
	workspaceCap = 1
	mu.Unlock()
	close(sendThrottle)
	waitUntil(t, "the throttle read on a throttle command", func() bool { return throttle.Ceiling(5) == 1 })
	waitUntil(t, "the throttle command relayed", func() bool {
		for _, e := range ext.relayed() {
			if relayed, ok := e.Data.(ipc.AgentCommandEvent); ok && strings.Contains(string(relayed.Frame), "cmd-throttle-1") {
				return true
			}
		}
		return false
	})

	// Without a session the throttle cannot be followed, and is lifted.
	client.SetSessionToken("")
	ext.sessionChanged()
	waitUntil(t, "the throttle lifted without a session", func() bool {
		_, known := throttle.Snapshot()
		return !known && throttle.Ceiling(5) == 5
	})
}

// A headless scheduler follows the throttle the workspace's daemon follows:
// a daemon that cannot be reached changes nothing, one that follows none
// lifts it (#2352).
func TestFollowDaemonWorkspaceThrottle(t *testing.T) {
	type answer struct {
		result ipc.PlatformWorkspaceThrottleResult
		err    error
	}
	answers := make(chan answer)
	throttle := orchestrator.NewDispatchThrottle()
	changes := make(chan struct{}, 8)
	throttle.OnChange(func() { changes <- struct{}{} })
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		followDaemonWorkspaceThrottle(ctx, throttle, time.Millisecond,
			func(context.Context) (ipc.PlatformWorkspaceThrottleResult, error) {
				a := <-answers
				return a.result, a.err
			})
	}()
	defer func() {
		cancel()
		close(answers)
		<-done
	}()
	ask := func(a answer) {
		t.Helper()
		select {
		case answers <- a:
		case <-time.After(5 * time.Second):
			t.Fatal("the follower stopped asking the daemon")
		}
	}

	ask(answer{result: ipc.PlatformWorkspaceThrottleResult{Known: true, Throttle: &platform.WorkspaceThrottle{MaxConcurrent: 1}}})
	<-changes
	if got := throttle.Ceiling(3); got != 1 {
		t.Fatalf("the daemon's throttle: ceiling = %d, want 1", got)
	}
	ask(answer{err: errors.New("dial unix: no such file")})
	ask(answer{err: errors.New("dial unix: no such file")}) // the previous answer has been handled
	if got := throttle.Ceiling(3); got != 1 {
		t.Fatalf("daemon unreachable: ceiling = %d, want the last throttle, 1", got)
	}
	ask(answer{result: ipc.PlatformWorkspaceThrottleResult{Known: false}})
	<-changes
	if got := throttle.Ceiling(3); got != 3 {
		t.Fatalf("daemon without a session: ceiling = %d, want 3", got)
	}
}

// A throttle command makes the daemon read its throttle again, and is
// relayed to the extension unchanged; other commands are only relayed.
func TestRefreshThrottleOnCommand(t *testing.T) {
	var relayed []string
	refreshed := make(chan struct{}, 2)
	relay := refreshThrottleOnCommand(func(_ string, cmd platform.PendingCommand) {
		relayed = append(relayed, cmd.Type)
	}, func() { refreshed <- struct{}{} })

	relay("agent-1", platform.PendingCommand{ID: "c1", Type: "pause"})
	relay("agent-1", platform.PendingCommand{ID: "c2", Type: "throttle"})
	if !reflect.DeepEqual(relayed, []string{"pause", "throttle"}) {
		t.Fatalf("relayed = %v, want both commands", relayed)
	}
	select {
	case <-refreshed:
	case <-time.After(5 * time.Second):
		t.Fatal("a throttle command did not refresh the throttle")
	}
	select {
	case <-refreshed:
		t.Fatal("a pause refreshed the throttle")
	case <-time.After(20 * time.Millisecond):
	}
}

// A headless scheduler holds its very first dispatch to the throttle: the
// daemon is asked before the scheduler starts, then again in the background
// (#2352).
func TestStartFollowingDaemonWorkspaceThrottle_ReadsBeforeReturning(t *testing.T) {
	throttle := orchestrator.NewDispatchThrottle()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var reads int32
	startFollowingDaemonWorkspaceThrottle(ctx, throttle, time.Hour,
		func(context.Context) (ipc.PlatformWorkspaceThrottleResult, error) {
			atomic.AddInt32(&reads, 1)
			return ipc.PlatformWorkspaceThrottleResult{Known: true, Throttle: &platform.WorkspaceThrottle{MaxConcurrent: 0}}, nil
		})
	if got := throttle.Ceiling(3); got != 0 {
		t.Fatalf("on return: ceiling = %d, want the daemon's 0", got)
	}
	if n := atomic.LoadInt32(&reads); n != 1 {
		t.Fatalf("reads before returning = %d, want 1", n)
	}
}

// A daemon that stops answering leaves the last throttle it reported in
// force, and the log says so and until when, rather than that the throttle
// is not followed (#2352).
func TestDaemonThrottleFollower_SaysTheLastThrottleIsKept(t *testing.T) {
	logs := &syncBuffer{}
	prev := log.Writer()
	log.SetOutput(logs)
	defer log.SetOutput(prev)

	throttle := orchestrator.NewDispatchThrottle()
	resumeAt := time.Date(2099, 1, 2, 3, 4, 5, 0, time.UTC)
	answer := ipc.PlatformWorkspaceThrottleResult{Known: true, Throttle: &platform.WorkspaceThrottle{MaxConcurrent: 1}}
	var readErr error
	f := &daemonThrottleFollower{throttle: throttle, read: func(context.Context) (ipc.PlatformWorkspaceThrottleResult, error) {
		return answer, readErr
	}}

	readErr = errors.New("dial unix: no such file")
	f.step(context.Background())
	if !strings.Contains(logs.String(), "the platform's throttle is not followed") {
		t.Fatalf("nothing learned, daemon unreachable: log = %q", logs.String())
	}

	readErr = nil
	f.step(context.Background())
	readErr = errors.New("dial unix: no such file")
	f.step(context.Background())
	if got := throttle.Ceiling(3); got != 1 {
		t.Fatalf("daemon unreachable: ceiling = %d, want the kept 1", got)
	}
	if !strings.Contains(logs.String(), "the last throttle it reported is kept: 1 run(s) at once until the platform clears it") {
		t.Fatalf("kept throttle: log = %q", logs.String())
	}

	answer.Throttle = &platform.WorkspaceThrottle{MaxConcurrent: 2, ResumeAt: &resumeAt}
	readErr = nil
	f.step(context.Background())
	readErr = errors.New("dial unix: no such file")
	f.step(context.Background())
	f.step(context.Background())
	if n := strings.Count(logs.String(), "is kept: 2 run(s) at once until 2099-01-02T03:04:05Z"); n != 1 {
		t.Fatalf("the kept throttle with its resumeAt was logged %d times, want once: %q", n, logs.String())
	}
}
