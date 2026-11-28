package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/agentworkspace"
	"github.com/nightgauge/nightgauge/internal/config"
	gh "github.com/nightgauge/nightgauge/internal/github"
	"github.com/nightgauge/nightgauge/internal/gittest"
	"github.com/nightgauge/nightgauge/internal/headlesstrigger"
	"github.com/nightgauge/nightgauge/internal/ipc"
	"github.com/nightgauge/nightgauge/internal/orchestrator"
	"github.com/nightgauge/nightgauge/internal/platform"
	"github.com/nightgauge/nightgauge/internal/runstate"
	"github.com/nightgauge/nightgauge/internal/state"
)

// triggerHost is a headlessTriggerHost whose attachment a test sets.
type triggerHost struct{ attached bool }

func (h triggerHost) ExecutorAttached() bool                   { return h.attached }
func (h triggerHost) TriggerIssueBusy(string, int) bool        { return false }
func (h triggerHost) ValidateTriggerPin(_, _, _ string) string { return "" }
func (h triggerHost) AddIssueBusyCheck(func(string, int) bool) {}
func (h triggerHost) TriggerIssueRunning(string, int) bool     { return false }

type relayRecorder struct {
	mu       sync.Mutex
	relayed  []string
	executed []string
}

func (r *relayRecorder) relay(_ string, cmd platform.PendingCommand) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.relayed = append(r.relayed, cmd.Type)
}

func (r *relayRecorder) execute(_ context.Context, _ string, cmd platform.PendingCommand) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.executed = append(r.executed, cmd.Type)
}

func TestHeadlessTriggerRelay_Routing(t *testing.T) {
	for _, tc := range []struct {
		name         string
		attached     bool
		cmdType      string
		wantRelayed  int
		wantExecuted int
	}{
		{"trigger, no extension: executed, not relayed", false, "trigger", 0, 1},
		{"trigger, extension attached: relayed, not executed", true, "trigger", 1, 0},
		{"other type, no extension: relayed only, so it still expires", false, "pause", 1, 0},
		{"other type, extension attached: relayed", true, "resume", 1, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := &relayRecorder{}
			relay := headlessTriggerRelay(triggerHost{attached: tc.attached}, rec.execute, rec.relay)
			relay("agent-1", platform.PendingCommand{ID: "cmd-1", Type: tc.cmdType, Frame: []byte(`{}`)})
			if len(rec.relayed) != tc.wantRelayed || len(rec.executed) != tc.wantExecuted {
				t.Errorf("relayed %v, executed %v; want %d relayed, %d executed",
					rec.relayed, rec.executed, tc.wantRelayed, tc.wantExecuted)
			}
		})
	}
}

// A headless daemon's relay emits no EventAgentCommand for a trigger: it goes
// to the handler, which acks it under the addressed agent and runs it once,
// however often it is delivered.
func TestHeadlessTriggerRelay_TriggerTwiceStartsOneRunAndEmitsNothing(t *testing.T) {
	ext := &extensionSide{}
	acks := &countingAcks{}
	runner := &countingRunner{}
	h := headlesstrigger.New(context.Background(), headlesstrigger.Config{
		Acks: acks, Reports: noReports{}, Runner: runner,
		Served: func() ([]platform.AgentRepo, error) {
			return []platform.AgentRepo{{Owner: "acme", Repo: "widgets"}}, nil
		},
	})
	relay := headlessTriggerRelay(triggerHost{}, h.HandleCommand, relayAgentCommandToExtension(ext))
	cmd := platform.PendingCommand{
		ID: "cmd-twice", Type: "trigger", Frame: []byte(`{}`),
		Payload: []byte(`{"owner":"acme","repo":"widgets","issueNumber":9}`),
	}
	relay("agent-addressed", cmd)
	relay("agent-addressed", cmd)
	h.Wait()
	if n := runner.count(); n != 1 {
		t.Errorf("runs = %d, want 1", n)
	}
	if got := acks.agents(); len(got) != 1 || got[0] != "agent-addressed" {
		t.Errorf("acks = %v, want one under the addressed agent", got)
	}
	ext.mu.Lock()
	defer ext.mu.Unlock()
	if len(ext.events) != 0 {
		t.Errorf("events emitted to the extension: %+v, want none", ext.events)
	}
}

type countingAcks struct {
	mu  sync.Mutex
	ids []string
}

func (a *countingAcks) AcknowledgeAgentCommand(_ context.Context, agent, _ string) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.ids = append(a.ids, agent)
	return "0190a1b2-c3d4-7e5f-8a9b-0c1d2e3f4a5b", nil
}

func (a *countingAcks) RejectAgentCommand(context.Context, string, string, string) error { return nil }

func (a *countingAcks) agents() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.ids...)
}

type noReports struct{}

func (noReports) ReportRemoteRunStart(context.Context, string, string, int, string) error {
	return nil
}

func (noReports) ReportRemoteRunTerminal(context.Context, string, platform.RemoteRunStatus, string) error {
	return nil
}

type countingRunner struct {
	mu sync.Mutex
	n  int
}

func (r *countingRunner) Run(context.Context, headlesstrigger.Request) (headlesstrigger.Result, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.n++
	return headlesstrigger.Result{Completed: true}, nil
}

func (r *countingRunner) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.n
}

// fakeStageRunner stands in for the adapter: it records every dispatch and
// fails it, so the run ends after its first stage.
type fakeStageRunner struct {
	mu    sync.Mutex
	calls []orchestrator.StageRunParams
}

func (f *fakeStageRunner) RunStage(_ context.Context, p orchestrator.StageRunParams) (*orchestrator.StageRunResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, p)
	return &orchestrator.StageRunResult{ExitCode: 1}, nil
}

// oneItemBoard answers the board's filtered item read with the issue the
// filter names, so RunQueue resolves the queued item to a board row.
func oneItemBoard(t *testing.T) *httptest.Server {
	t.Helper()
	filter := regexp.MustCompile(`^repo:(\S+) #(\d+)$`)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if regexp.MustCompile(`/collaborators/[^/]+/permission$`).MatchString(r.URL.Path) {
			_, _ = w.Write([]byte(`{"permission":"admin","role_name":"admin","user":{"login":"maint"}}`))
			return
		}
		var req struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		// The trust gate's issue read: the author is a maintainer.
		if regexp.MustCompile(`timelineItems`).MatchString(req.Query) {
			_, _ = w.Write([]byte(`{"data":{"repository":{"issue":{"author":{"login":"maint","__typename":"User"},` +
				`"labels":{"nodes":[]},"timelineItems":{"nodes":[]}}}}}`))
			return
		}
		q, _ := req.Variables["query"].(string)
		m := filter.FindStringSubmatch(q)
		if m == nil {
			_, _ = w.Write([]byte(`{"data":{}}`))
			return
		}
		fmt.Fprintf(w, `{"data":{"organization":{"projectV2":{"items":{"pageInfo":{"hasNextPage":false,"endCursor":null},"nodes":[`+
			`{"id":"PVTI_%[2]s","content":{"__typename":"Issue","number":%[2]s,"title":"t%[2]s","state":"OPEN",`+
			`"author":{"login":"maint","__typename":"User"},`+
			`"repository":{"nameWithOwner":%[1]q}},"fieldValues":{"nodes":[]}}]}}}}}`, m[1], m[2])
	}))
	t.Cleanup(srv.Close)
	return srv
}

func headlessGitWorkspace(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	gittest.Run(t, root, "init", "-b", "main")
	gittest.Run(t, root, "config", "user.email", "headless@example.com")
	gittest.Run(t, root, "config", "user.name", "Headless Test")
	for _, dir := range []string{
		"nightgauge-issue-pickup", "nightgauge-feature-planning", "nightgauge-feature-dev",
		"nightgauge-feature-validate", "nightgauge-pr-create", "nightgauge-pr-merge",
	} {
		p := filepath.Join(root, "skills", dir)
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(p, "SKILL.md"),
			[]byte("---\nname: test-stage\nallowed-tools: Read Write\n---\n# Test Stage\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	gittest.Run(t, root, "add", ".")
	gittest.Run(t, root, "commit", "-m", "fixture skills")
	return root
}

// Through the real RunQueue: the run's id is a fresh runstate id, neither the
// ack's run id nor the command id; the ack's run id is only the remote run it
// serves; the pin and private visibility reach the run (#3586 AC 3).
func TestRunHeadlessTrigger_RunIdentityPinAndVisibility(t *testing.T) {
	root := headlessGitWorkspace(t)
	srv := oneItemBoard(t)
	sched := orchestrator.NewScheduler(gh.NewClientWithURL("test-token", srv.URL), orchestrator.SchedulerConfig{
		Owner: "acme", ProjectNumber: 1, WorkspaceRoot: root, EphemeralQueue: true,
	})
	runner := &fakeStageRunner{}
	sched.WithStageRunner(runner)
	var (
		mu   sync.Mutex
		snap *state.RuntimeState
	)
	sched.OnPipelineComplete(func(_ string, _ int, rt *state.RuntimeState, _ bool) {
		mu.Lock()
		defer mu.Unlock()
		snap = rt
	})

	ack, err := runstate.NewRunID()
	if err != nil {
		t.Fatal(err)
	}
	res, err := runHeadlessTrigger(context.Background(), sched, headlesstrigger.Request{
		Repo: "acme/widgets", IssueNumber: 42, RemoteRunID: ack, Adapter: "codex", Model: "gpt-5",
		Visibility: state.VisibilityPrivate,
	})
	if err != nil {
		t.Fatalf("runHeadlessTrigger: %v", err)
	}
	if res.Completed {
		t.Error("the fake stage fails, so the run must not report completed")
	}

	runner.mu.Lock()
	calls := append([]orchestrator.StageRunParams(nil), runner.calls...)
	runner.mu.Unlock()
	if len(calls) == 0 {
		t.Fatal("no stage was dispatched; the fixture is wrong, not the assertion")
	}
	mu.Lock()
	defer mu.Unlock()
	if snap == nil {
		t.Fatal("the run never completed")
	}
	if !runstate.IsIdentity(snap.RunID) {
		t.Errorf("run id %q is not a runstate identity", snap.RunID)
	}
	if snap.RunID == ack {
		t.Errorf("run id %q reuses the ack's run id", snap.RunID)
	}
	if calls[0].RunID != snap.RunID {
		t.Errorf("dispatch carried run id %q, the run is %q", calls[0].RunID, snap.RunID)
	}
	if got := snap.RemoteRunID; got != ack {
		t.Errorf("remote run id = %q, want the ack's %q", got, ack)
	}
	if a, m := snap.RequestedPin(); a != "codex" || m != "gpt-5" {
		t.Errorf("pin = %s/%s, want codex/gpt-5", a, m)
	}
	if snap.RunVisibility() != state.VisibilityPrivate {
		t.Errorf("visibility = %q, want private", snap.RunVisibility())
	}
}

func TestResolveRunAdapter_ConfigDefaultOnlyWhenTheConfigLoaded(t *testing.T) {
	t.Setenv("NIGHTGAUGE_ADAPTER", "")
	cfg := &config.Config{UI: &config.UIConfig{Core: &config.UICoreConfig{Adapter: "codex"}}}
	a, explicit, err := resolveRunAdapter("", cfg, nil)
	if err != nil || a.Name() != "codex" || explicit != "" {
		t.Fatalf("loaded config: adapter %v explicit %q err %v, want codex, not explicit", a, explicit, err)
	}
	fallback, _, err := resolveRunAdapter("", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	a, _, err = resolveRunAdapter("", cfg, errors.New("refused"))
	if err != nil || a.Name() != fallback.Name() {
		t.Errorf("failed config load: adapter %v, want the built-in default %s", a, fallback.Name())
	}
	a, explicit, err = resolveRunAdapter("gemini", cfg, nil)
	if err != nil || a.Name() != "gemini" || explicit != "gemini" {
		t.Errorf("flag: adapter %v explicit %q, want gemini pinned", a, explicit)
	}
}

// headlessExt is an extensionSide that can decide a headless trigger, as
// *ipc.Server can.
type headlessExt struct {
	*extensionSide
	attached bool
}

func (e headlessExt) ExecutorAttached() bool                 { return e.attached }
func (headlessExt) TriggerIssueBusy(string, int) bool        { return false }
func (headlessExt) ValidateTriggerPin(_, _, _ string) string { return "" }
func (headlessExt) AddIssueBusyCheck(func(string, int) bool) {}
func (headlessExt) TriggerIssueRunning(string, int) bool     { return false }

type recordingTriggerRunner struct {
	mu   sync.Mutex
	reqs []headlesstrigger.Request
}

func (r *recordingTriggerRunner) Run(_ context.Context, req headlesstrigger.Request) (headlesstrigger.Result, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.reqs = append(r.reqs, req)
	return headlesstrigger.Result{Completed: true}, nil
}

func (r *recordingTriggerRunner) all() []headlesstrigger.Request {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]headlesstrigger.Request(nil), r.reqs...)
}

// serve with no extension attached executes a trigger on its own stream
// (#3586): it acks it under the addressed agent through the real command
// service, runs it once, reports pipeline_done for the ack's run id, and
// relays only the other command.
func TestRunDaemonPlatformAgent_HeadlessExecutesATrigger(t *testing.T) {
	t.Setenv("NIGHTGAUGE_AGENT_ID", "test-machine-uuid")
	t.Setenv(agentworkspace.WindowFoldersEnv, "")
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, ".nightgauge", "config.yaml"), "owner: acme\nrepo: api\n")
	const platformRun = "0190a1b2-c3d4-7e5f-8a9b-0c1d2e3f4a5b"

	runner := &recordingTriggerRunner{}
	orig := newHeadlessTriggerRunner
	newHeadlessTriggerRunner = func(string) headlesstrigger.Runner { return runner }
	t.Cleanup(func() { newHeadlessTriggerRunner = orig })

	trig := `{"commandId":"cmd-trig-1","type":"trigger","commandType":"trigger","payload":{"owner":"acme","repo":"api","issueNumber":51},"owner":"acme","repo":"api","issueNumber":51,"createdAt":"2026-10-11T00:00:00.000Z","expiresAt":"2026-10-12T00:00:00.000Z"}`
	pause := `{"commandId":"cmd-pause-1","type":"pause","commandType":"pause","payload":{"runId":"run-9"},"owner":"acme","repo":"api","createdAt":"2026-10-11T00:00:00.000Z","expiresAt":"2026-10-11T00:05:00.000Z"}`
	var (
		mu     sync.Mutex
		acks   []string
		events []map[string]any
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/agents/register":
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"agentId":"agent-daemon","ttl_seconds":90}`))
		case r.Method == http.MethodPost && r.URL.Path == "/v1/agents/agent-daemon/commands/cmd-trig-1/ack":
			mu.Lock()
			acks = append(acks, r.URL.Path)
			mu.Unlock()
			_, _ = w.Write([]byte(`{"runId":"` + platformRun + `"}`))
		case r.Method == http.MethodPost && r.URL.Path == "/v1/pipelines/events":
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			mu.Lock()
			events = append(events, body)
			mu.Unlock()
			w.WriteHeader(http.StatusAccepted)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/agents/agent-daemon/commands":
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			_, _ = fmt.Fprintf(w, "event: command\ndata: %s\n\nevent: command\ndata: %s\n\n", trig, pause)
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
	side := &extensionSide{}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		runDaemonPlatformAgent(ctx, client, platform.NewAttentionSyncService(client), headlessExt{extensionSide: side},
			"test", root, nil, nil, nil)
	}()
	defer func() {
		cancel()
		<-done
	}()

	waitUntil(t, "the terminal report", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(events) >= 2
	})
	waitUntil(t, "the relayed pause", func() bool { return len(side.relayed()) >= 1 })
	mu.Lock()
	defer mu.Unlock()
	if len(acks) != 1 {
		t.Errorf("acks = %v, want one under the addressed agent", acks)
	}
	if reqs := runner.all(); len(reqs) != 1 || reqs[0].RemoteRunID != platformRun || reqs[0].Repo != "acme/api" || reqs[0].IssueNumber != 51 {
		t.Errorf("runs = %+v, want one of acme/api#51 serving %s", reqs, platformRun)
	}
	if e := events[0]; e["type"] != "stage_started" || e["runId"] != platformRun || e["repo"] != "acme/api" || e["issueNumber"] != float64(51) {
		t.Errorf("start = %v, want stage_started for %s naming acme/api#51", e, platformRun)
	}
	if e := events[1]; e["type"] != "pipeline_done" || e["runId"] != platformRun || e["success"] != true {
		t.Errorf("report = %v, want pipeline_done success for %s", e, platformRun)
	}
	for _, e := range side.relayed() {
		if ev, ok := e.Data.(ipc.AgentCommandEvent); ok && strings.Contains(string(ev.Frame), `"trigger"`) {
			t.Errorf("a trigger was relayed to the extension: %s", ev.Frame)
		}
	}
}

// blockingTriggerRunner records peak concurrent runs and blocks each run
// until release is closed.
type blockingTriggerRunner struct {
	mu      sync.Mutex
	running int
	peak    int
	runs    int
	release chan struct{}
}

func (r *blockingTriggerRunner) Run(ctx context.Context, _ headlesstrigger.Request) (headlesstrigger.Result, error) {
	r.mu.Lock()
	r.running++
	r.runs++
	if r.running > r.peak {
		r.peak = r.running
	}
	r.mu.Unlock()
	select {
	case <-r.release:
	case <-ctx.Done():
	}
	r.mu.Lock()
	r.running--
	r.mu.Unlock()
	return headlesstrigger.Result{Completed: true}, nil
}

func (r *blockingTriggerRunner) snapshot() (running, peak, runs int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.running, r.peak, r.runs
}

func runTriggers(t *testing.T, ctx context.Context, runner headlesstrigger.Runner, n int) (*sync.WaitGroup, []error) {
	t.Helper()
	var wg sync.WaitGroup
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = runner.Run(ctx, headlesstrigger.Request{Repo: "acme/api", IssueNumber: 100 + i})
		}(i)
	}
	return &wg, errs
}

// Five triggers under a limit of two run two at a time, and every one runs
// once a slot frees (#3673).
func TestBoundedHeadlessTriggerRunner_HoldsTheConfiguredLimit(t *testing.T) {
	inner := &blockingTriggerRunner{release: make(chan struct{})}
	r := boundedHeadlessTriggerRunner{inner: inner, slots: newHeadlessTriggerSlots(func() int { return 2 }, nil)}
	wg, errs := runTriggers(t, context.Background(), r, 5)
	waitUntil(t, "two runs", func() bool { running, _, _ := inner.snapshot(); return running == 2 })
	time.Sleep(50 * time.Millisecond)
	if _, peak, _ := inner.snapshot(); peak != 2 {
		t.Fatalf("peak concurrent runs = %d, want 2", peak)
	}
	close(inner.release)
	wg.Wait()
	if _, peak, runs := inner.snapshot(); peak != 2 || runs != 5 {
		t.Fatalf("peak %d runs %d, want peak 2 and all 5 run", peak, runs)
	}
	for i, err := range errs {
		if err != nil {
			t.Errorf("trigger %d: %v", i, err)
		}
	}
}

// A workspace throttle lower than the configured limit holds the runs, and
// lifting it wakes a waiting run at once (#3673).
func TestBoundedHeadlessTriggerRunner_HeldToTheThrottle(t *testing.T) {
	throttle := orchestrator.NewDispatchThrottle()
	throttle.Set(&platform.WorkspaceThrottle{MaxConcurrent: 1}, true)
	inner := &blockingTriggerRunner{release: make(chan struct{})}
	r := boundedHeadlessTriggerRunner{inner: inner, slots: newHeadlessTriggerSlots(func() int { return 3 }, throttle)}
	wg, _ := runTriggers(t, context.Background(), r, 3)
	waitUntil(t, "one run", func() bool { running, _, _ := inner.snapshot(); return running == 1 })
	time.Sleep(50 * time.Millisecond)
	if _, peak, _ := inner.snapshot(); peak != 1 {
		t.Fatalf("peak under a throttle of 1 = %d, want 1", peak)
	}
	throttle.Set(nil, true)
	waitUntil(t, "the lifted throttle to wake the waiters", func() bool { running, _, _ := inner.snapshot(); return running == 3 })
	close(inner.release)
	wg.Wait()
}

// A run that waited re-checks the issue: when it started running elsewhere
// meanwhile the trigger never runs (#3673).
func TestBoundedHeadlessTriggerRunner_DedupesAfterTheWait(t *testing.T) {
	inner := &blockingTriggerRunner{release: make(chan struct{})}
	var mu sync.Mutex
	busy := false
	r := boundedHeadlessTriggerRunner{
		inner: inner,
		slots: newHeadlessTriggerSlots(func() int { return 1 }, nil),
		busy: func(string, int) bool {
			mu.Lock()
			defer mu.Unlock()
			return busy
		},
	}
	first := make(chan struct{})
	go func() {
		defer close(first)
		_, _ = r.Run(context.Background(), headlesstrigger.Request{Repo: "acme/api", IssueNumber: 1})
	}()
	waitUntil(t, "the first run", func() bool { running, _, _ := inner.snapshot(); return running == 1 })
	second := make(chan error, 1)
	go func() {
		_, err := r.Run(context.Background(), headlesstrigger.Request{Repo: "acme/api", IssueNumber: 2})
		second <- err
	}()
	time.Sleep(50 * time.Millisecond)
	mu.Lock()
	busy = true
	mu.Unlock()
	close(inner.release)
	<-first
	if err := <-second; !errors.Is(err, errHeadlessTriggerDeduped) {
		t.Fatalf("waited run error = %v, want the dedupe error", err)
	}
	if _, _, runs := inner.snapshot(); runs != 1 {
		t.Fatalf("inner runs = %d, want 1: the deduped trigger ran", runs)
	}
}

// Cancelling a waiting run returns the context error and leaks no slot.
func TestHeadlessTriggerSlots_CancelWhileWaitingLeaksNoSlot(t *testing.T) {
	slots := newHeadlessTriggerSlots(func() int { return 1 }, nil)
	release, err := slots.acquire(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	got := make(chan error, 1)
	go func() { _, err := slots.acquire(ctx, nil); got <- err }()
	time.Sleep(20 * time.Millisecond)
	cancel()
	if err := <-got; !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	release()
	release() // idempotent
	again, err := slots.acquire(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	again()
	slots.mu.Lock()
	defer slots.mu.Unlock()
	if slots.running != 0 {
		t.Fatalf("running = %d after every release, want 0", slots.running)
	}
}

// A workspace's pool takes its limit from the workspace concurrency config,
// and every handler serving that workspace shares it (#3673).
func TestHeadlessTriggerSlotPools_ForWorkspace(t *testing.T) {
	var pools headlessTriggerSlotPools
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, ".nightgauge", "config.yaml"),
		"owner: acme\nrepo: api\nconcurrency:\n  workspace_max: 3\n")
	s := pools.forWorkspace(root, nil)
	if got := s.limit(); got != 3 {
		t.Fatalf("limit = %d, want the configured workspace_max 3", got)
	}
	if pools.forWorkspace(root, nil) != s {
		t.Error("a second handler for the workspace got its own pool")
	}
	other := t.TempDir()
	if pools.forWorkspace(other, nil) == s {
		t.Error("another workspace shares this workspace's pool")
	}
	broken := t.TempDir()
	writeTestFile(t, filepath.Join(broken, ".nightgauge", "config.yaml"), "concurrency: [\n")
	if got := pools.forWorkspace(broken, nil).limit(); got != 1 {
		t.Errorf("limit with an unreadable config = %d, want 1", got)
	}
}
