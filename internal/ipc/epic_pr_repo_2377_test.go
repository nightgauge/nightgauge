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
	sched.WithRepoPathResolver(func(repo string) string {
		switch repo {
		case "example-org/app":
			return app
		case "example-org/platform":
			return platform
		}
		return ""
	})
	s := newTestServer(buf)
	s.workspaceRoot = app
	s.scheduler = sched
	if graphqlURL != "" {
		s.client = gh.NewClientWithURL("test", graphqlURL)
	}
	return s
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
