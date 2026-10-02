package ipc

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/nightgauge/nightgauge/internal/config"
	gh "github.com/nightgauge/nightgauge/internal/github"
	"github.com/nightgauge/nightgauge/internal/gittest"
	"github.com/nightgauge/nightgauge/internal/orchestrator"
)

// Regression tests for #2377: the epic PR of an epic that auto-closed is
// opened from the epic branch in the epic's own repository's checkout, with
// that repository's title, in that repository. The sub-issue is
// example-org/app#21; its epic is example-org/platform#20.

// epicCheckout makes a checkout on main whose origin, a local bare
// repository, also carries branch.
func epicCheckout(t *testing.T, branch string) string {
	t.Helper()
	bare := gittest.InitRepo(t, t.TempDir(), "--bare", "-q")
	dir := gittest.InitRepo(t, t.TempDir(), "-q", "-b", "main")
	gittest.Run(t, dir, "config", "user.email", "ipc-test@example.com")
	gittest.Run(t, dir, "config", "user.name", "IPC Test")
	gittest.Run(t, dir, "commit", "-q", "--allow-empty", "-m", "base")
	gittest.Run(t, dir, "remote", "add", "origin", bare)
	gittest.Run(t, dir, "push", "-q", "origin", "main", "main:refs/heads/"+branch)
	return dir
}

// epicServer returns a Server launched in the example-org/app checkout, with
// its scheduler resolving example-org/app and example-org/platform to their
// checkouts and refusing any other repository.
func epicServer(t *testing.T, buf *bytes.Buffer, app, platform, graphqlURL string) *Server {
	t.Helper()
	sched := orchestrator.NewScheduler(nil, orchestrator.SchedulerConfig{
		WorkspaceRoot: app,
		RuntimeConfig: &config.Config{Owner: "example-org", DefaultRepo: "app"},
	})
	sched.WithRepoPathResolver(epicRepoPaths(app, platform))
	s := newTestServer(buf)
	s.workspaceRoot = app
	s.scheduler = sched
	if graphqlURL != "" {
		s.client = gh.NewClientWithURL("test", graphqlURL)
		s.resolver = NewClientResolver(s.client, true)
	}
	return s
}

// epicRepoPaths resolves example-org/app and example-org/platform to their
// checkouts and any other repository to none.
func epicRepoPaths(app, platform string) func(string) string {
	return func(repo string) string {
		switch repo {
		case "example-org/app":
			return app
		case "example-org/platform":
			return platform
		}
		return ""
	}
}

// registerEpicMethods registers the server's methods, then restores the
// scheduler's checkouts: registration points the scheduler at the server's
// repository registry, which these tests leave empty so that no request
// resolves a real identity.
func registerEpicMethods(s *Server, app, platform string) {
	s.registerMethods()
	s.scheduler.WithRepoPathResolver(epicRepoPaths(app, platform))
}

type gqlRequest struct {
	Query     string                 `json:"query"`
	Variables map[string]interface{} `json:"variables"`
}

func TestEpicGitService_IsTheEpicRepositorysCheckout(t *testing.T) {
	app := epicCheckout(t, "epic/20-app-epic")
	platform := epicCheckout(t, "epic/20-platform-epic")
	s := epicServer(t, &bytes.Buffer{}, app, platform, "")

	svc, err := s.epicGitService("example-org/platform")
	if err != nil {
		t.Fatalf("epicGitService: %v", err)
	}
	if branch, err := svc.FindEpicBranch(20); err != nil || branch != "epic/20-platform-epic" {
		t.Errorf("FindEpicBranch(20) = %q, %v; want example-org/platform's epic/20-platform-epic", branch, err)
	}

	// A repository this workspace has no checkout of is refused, never
	// answered from the launch checkout's same-numbered branch.
	if svc, err := s.epicGitService("example-org/elsewhere"); err == nil {
		t.Errorf("epicGitService(example-org/elsewhere) = %s, want an error", svc.RepoPath())
	}
}

// Without a scheduler only a launch checkout whose origin is the epic's
// repository is used.
func TestEpicGitService_WithoutASchedulerChecksTheLaunchCheckout(t *testing.T) {
	launch := gittest.InitRepo(t, t.TempDir(), "-q", "-b", "main")
	gittest.Run(t, launch, "remote", "add", "origin", "https://github.com/example-org/app.git")
	s := newTestServer(&bytes.Buffer{})
	s.workspaceRoot = launch

	if _, err := s.epicGitService("Example-Org/App"); err != nil {
		t.Errorf("epicGitService for the launch checkout's own repository: %v", err)
	}
	_, err := s.epicGitService("example-org/platform")
	if err == nil || !strings.Contains(err.Error(), "example-org/platform") {
		t.Errorf("epicGitService(example-org/platform) error = %v, want a refusal naming it", err)
	}
}

// The callback reads the epic's title in its own repository and looks for the
// PR of the epic branch found in that repository's checkout. It used to read
// example-org/app#20 and search the launch checkout, which here holds
// example-org/app#20's own epic/20-app-epic.
func TestCompleteEpicPR_WorksInTheEpicsOwnRepository(t *testing.T) {
	var mu sync.Mutex
	var reqs []gqlRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req gqlRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		mu.Lock()
		reqs = append(reqs, req)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if _, ok := req.Variables["headRef"]; ok {
			// Stop at the PR lookup: everything this test asserts is decided.
			fmt.Fprint(w, `{"errors":[{"message":"test stops at the PR lookup"}]}`)
			return
		}
		fmt.Fprint(w, `{"data":{"repository":{"issue":{"id":"I_20","number":20,"title":"Platform Epic","state":"CLOSED"}}}}`)
	}))
	defer srv.Close()

	var buf bytes.Buffer
	app := epicCheckout(t, "epic/20-app-epic")
	platform := epicCheckout(t, "epic/20-platform-epic")
	s := epicServer(t, &buf, app, platform, srv.URL)

	s.completeEpicPR(context.Background(), "example-org/platform", 20)

	mu.Lock()
	defer mu.Unlock()
	if len(reqs) != 2 {
		t.Fatalf("GitHub requests = %d, want the epic read and the PR lookup: %+v", len(reqs), reqs)
	}
	issueVars, prVars := reqs[0].Variables, reqs[1].Variables
	if issueVars["owner"] != "example-org" || issueVars["name"] != "platform" || issueVars["number"] != float64(20) {
		t.Errorf("epic title read from %v/%v#%v, want example-org/platform#20",
			issueVars["owner"], issueVars["name"], issueVars["number"])
	}
	if prVars["owner"] != "example-org" || prVars["name"] != "platform" || prVars["headRef"] != "epic/20-platform-epic" {
		t.Errorf("epic PR looked up in %v/%v for %v, want example-org/platform for epic/20-platform-epic",
			prVars["owner"], prVars["name"], prVars["headRef"])
	}
	var ev struct {
		Event string                 `json:"event"`
		Data  map[string]interface{} `json:"data"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &ev); err != nil {
		t.Fatalf("emitted event: %v\n%s", err, buf.String())
	}
	if ev.Event != "epic.prFailed" || ev.Data["repo"] != "example-org/platform" {
		t.Errorf("event = %s %v, want epic.prFailed for example-org/platform", ev.Event, ev.Data)
	}
}

// A workspace with no checkout of the epic's repository holds none of its
// epic branches: the callback opens nothing and makes no GitHub request.
func TestCompleteEpicPR_NoCheckoutOfTheEpicsRepository(t *testing.T) {
	var calls int
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		calls++
		mu.Unlock()
		http.Error(w, "unexpected", http.StatusInternalServerError)
	}))
	defer srv.Close()

	var buf bytes.Buffer
	app := epicCheckout(t, "epic/20-app-epic")
	s := epicServer(t, &buf, app, "", srv.URL)

	s.completeEpicPR(context.Background(), "example-org/elsewhere", 20)

	mu.Lock()
	defer mu.Unlock()
	if calls != 0 {
		t.Errorf("GitHub requests = %d, want none", calls)
	}
	if buf.Len() != 0 {
		t.Errorf("emitted %s, want nothing", buf.String())
	}
}

// fakeEpicForge answers the GitHub GraphQL requests the epic PR handlers make,
// and records each one. A PR lookup (the headRef query) is answered with an
// error, so a create path stops there once everything a test asserts is
// decided.
type fakeEpicForge struct {
	mu   sync.Mutex
	reqs []gqlRequest
}

func (f *fakeEpicForge) serve(w http.ResponseWriter, r *http.Request) {
	var req gqlRequest
	_ = json.NewDecoder(r.Body).Decode(&req)
	f.mu.Lock()
	f.reqs = append(f.reqs, req)
	f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	switch {
	case req.Variables["headRef"] != nil:
		fmt.Fprint(w, `{"errors":[{"message":"test stops at the PR lookup"}]}`)
	case strings.Contains(req.Query, "mergePullRequest("):
		fmt.Fprint(w, `{"data":{"mergePullRequest":{"pullRequest":{"id":"PR_20","state":"MERGED"}}}}`)
	case strings.Contains(req.Query, "ref(qualifiedName"):
		fmt.Fprint(w, `{"data":{"repository":{"ref":null}}}`)
	case strings.Contains(req.Query, "issue("):
		fmt.Fprint(w, `{"data":{"repository":{"issue":{"id":"I_20","number":20,"title":"Platform Epic","state":"CLOSED"}}}}`)
	default:
		// The merge-queue probe: unanswered, so the merge goes direct.
		fmt.Fprint(w, `{"errors":[{"message":"not faked"}]}`)
	}
}

// requests returns the recorded requests that carry variable key.
func (f *fakeEpicForge) requests(key string) []gqlRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []gqlRequest
	for _, r := range f.reqs {
		if _, ok := r.Variables[key]; ok {
			out = append(out, r)
		}
	}
	return out
}

// assertEpicPRLookupInPlatform checks that the epic's title was read from
// example-org/platform#20 and its PR looked up there for
// example-org/platform's own epic branch.
func assertEpicPRLookupInPlatform(t *testing.T, forge *fakeEpicForge) {
	t.Helper()
	issueReads := forge.requests("number")
	if len(issueReads) != 1 {
		t.Fatalf("epic reads = %+v, want one", issueReads)
	}
	if v := issueReads[0].Variables; v["owner"] != "example-org" || v["name"] != "platform" || v["number"] != float64(20) {
		t.Errorf("epic title read from %v/%v#%v, want example-org/platform#20", v["owner"], v["name"], v["number"])
	}
	lookups := forge.requests("headRef")
	if len(lookups) != 1 {
		t.Fatalf("PR lookups = %+v, want one", lookups)
	}
	if v := lookups[0].Variables; v["owner"] != "example-org" || v["name"] != "platform" || v["headRef"] != "epic/20-platform-epic" {
		t.Errorf("epic PR looked up in %v/%v for %v, want example-org/platform for epic/20-platform-epic",
			v["owner"], v["name"], v["headRef"])
	}
}

// epic.createPR looks for the epic branch in the checkout of the repository
// the request names. It used to search the launch checkout, which here is
// example-org/app and holds example-org/app#20's own epic/20-app-epic.
func TestEpicCreatePR_FindsTheBranchInTheNamedRepositorysCheckout(t *testing.T) {
	forge := &fakeEpicForge{}
	srv := httptest.NewServer(http.HandlerFunc(forge.serve))
	defer srv.Close()
	app := epicCheckout(t, "epic/20-app-epic")
	platform := epicCheckout(t, "epic/20-platform-epic")
	s := epicServer(t, &bytes.Buffer{}, app, platform, srv.URL)
	registerEpicMethods(s, app, platform)

	_, err := s.methods["epic.createPR"](context.Background(),
		json.RawMessage(`{"owner":"example-org","repo":"platform","epicNumber":20}`))
	if err == nil || !strings.Contains(err.Error(), "test stops at the PR lookup") {
		t.Fatalf("epic.createPR error = %v, want the fake forge's stop at the PR lookup", err)
	}
	assertEpicPRLookupInPlatform(t, forge)

	// A repository this workspace has no checkout of is refused before any
	// request, not answered from the launch checkout's epic/20-app-epic.
	forge.mu.Lock()
	forge.reqs = nil
	forge.mu.Unlock()
	if _, err := s.methods["epic.createPR"](context.Background(),
		json.RawMessage(`{"owner":"example-org","repo":"elsewhere","epicNumber":20}`)); err == nil {
		t.Error("epic.createPR for a repository with no checkout succeeded, want an error")
	}
	if n := len(forge.requests("owner")); n != 0 {
		t.Errorf("GitHub requests for a repository with no checkout = %d, want none", n)
	}
}

// epic.mergePR cleans the merged epic branch up in the checkout of the
// repository the PR merged in. Both checkouts here carry a branch named
// epic/20-shared; the launch checkout's belongs to example-org/app#20 and
// used to be the one deleted.
func TestEpicMergePR_CleansUpInTheNamedRepositorysCheckout(t *testing.T) {
	forge := &fakeEpicForge{}
	srv := httptest.NewServer(http.HandlerFunc(forge.serve))
	defer srv.Close()
	app := epicCheckout(t, "epic/20-shared")
	platform := epicCheckout(t, "epic/20-shared")
	s := epicServer(t, &bytes.Buffer{}, app, platform, srv.URL)
	registerEpicMethods(s, app, platform)

	res, err := s.methods["epic.mergePR"](context.Background(), json.RawMessage(
		`{"owner":"example-org","repo":"platform","epicNumber":20,"prNodeId":"PR_20","epicBranch":"epic/20-shared"}`))
	if err != nil {
		t.Fatalf("epic.mergePR: %v", err)
	}
	if m, _ := res.(map[string]interface{}); m["success"] != true {
		t.Fatalf("epic.mergePR result = %v, want success", res)
	}
	if heads := remoteHeadsOf(t, platform); strings.Contains(heads, "refs/heads/epic/20-shared") {
		t.Errorf("example-org/platform's epic/20-shared survived the cleanup:\n%s", heads)
	}
	if heads := remoteHeadsOf(t, app); !strings.Contains(heads, "refs/heads/epic/20-shared") {
		t.Errorf("example-org/app's own epic/20-shared was deleted:\n%s", heads)
	}
}

func remoteHeadsOf(t *testing.T, dir string) string {
	t.Helper()
	out, err := gittest.Command(dir, "ls-remote", "--heads", "origin").CombinedOutput()
	if err != nil {
		t.Fatalf("git ls-remote: %v\n%s", err, out)
	}
	return string(out)
}

// eventWriter is the server's output for a test that runs a handler's
// goroutine: written under the server's lock, read under its own, and it
// reports when an event of the given name has been written.
type eventWriter struct {
	mu    sync.Mutex
	buf   bytes.Buffer
	event string
	seen  chan struct{}
	once  sync.Once
}

func (w *eventWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if bytes.Contains(p, []byte(`"event":"`+w.event+`"`)) {
		w.once.Do(func() { close(w.seen) })
	}
	return w.buf.Write(p)
}

// The epic-complete callback pipeline.run registers opens the epic PR in the
// repository the scheduler hands it: the epic's own (#2377), not the
// repository pipeline.run was asked to run an issue of.
func TestPipelineRun_EpicCallbackWorksInTheEpicsRepository(t *testing.T) {
	forge := &fakeEpicForge{}
	srv := httptest.NewServer(http.HandlerFunc(forge.serve))
	defer srv.Close()
	app := epicCheckout(t, "epic/20-app-epic")
	platform := epicCheckout(t, "epic/20-platform-epic")
	out := &eventWriter{event: "pipeline.error", seen: make(chan struct{})}
	s := epicServer(t, &bytes.Buffer{}, app, platform, srv.URL)
	s.writer = out

	// The context is cancelled, so the queue run pipeline.run starts stops
	// before its first issue and reports that as pipeline.error, the
	// goroutine's last act. Waiting for it leaves nothing running.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	registerEpicMethods(s, app, platform)
	if _, err := s.methods["pipeline.run"](ctx,
		json.RawMessage(`{"owner":"example-org","repo":"app","issueNumber":21}`)); err != nil {
		t.Fatalf("pipeline.run: %v", err)
	}
	<-out.seen

	fire := s.scheduler.EpicCompleteCallback()
	if fire == nil {
		t.Fatal("pipeline.run registered no epic-complete callback")
	}
	fire("example-org/platform", 20)
	assertEpicPRLookupInPlatform(t, forge)
}
