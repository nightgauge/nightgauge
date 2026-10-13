package tools

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/platform"
	"github.com/nightgauge/nightgauge/internal/runstate"
	"github.com/nightgauge/nightgauge/internal/state"
	"github.com/nightgauge/nightgauge/pkg/types"
)

// noNetwork fails the test on any forge or hosted call: the run tools answer
// from this machine's state alone (#1587).
type noNetwork struct{ t *testing.T }

func (n noNetwork) GetIssue(context.Context, string, string, int) (*types.Issue, error) {
	n.t.Error("a run tool reached the forge")
	return nil, os.ErrInvalid
}

func (n noNetwork) GetPullRequest(context.Context, string, string, int) (*types.PullRequest, error) {
	n.t.Error("a run tool reached the forge")
	return nil, os.ErrInvalid
}

func (n noNetwork) SearchIssues(context.Context, string, string, string, int) ([]types.Issue, error) {
	n.t.Error("a run tool reached the forge")
	return nil, os.ErrInvalid
}

func (n noNetwork) CreateIssue(context.Context, string, string, string, string, bool) (*types.Issue, error) {
	n.t.Error("a run tool reached the forge")
	return nil, os.ErrInvalid
}

func (n noNetwork) ListCards(context.Context, platform.TurnCardQuery) (platform.TurnCardList, error) {
	n.t.Error("a run tool reached the hosted service")
	return platform.TurnCardList{}, os.ErrInvalid
}

func (n noNetwork) ActOnCard(context.Context, string, platform.TurnCardAct) (platform.TurnCardActResult, error) {
	n.t.Error("a run tool reached the hosted service")
	return platform.TurnCardActResult{}, os.ErrInvalid
}

// runFixture is a repository with three runs on disk: one executing, one
// paused, and one finished in the execution history.
type runFixture struct {
	root, running, paused, finished string
}

func newRunFixture(t *testing.T) runFixture {
	t.Helper()
	root := gitRepo(t)
	dir, err := state.PipelineStateDir(root)
	if err != nil {
		t.Fatal(err)
	}
	id := func() string {
		id, err := runstate.NewRunID()
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	f := runFixture{root: root, running: id(), paused: id(), finished: id()}
	live := state.NewRuntimeState("acme/api", 42, "item-42", f.running)
	live.Title = "Speed up login"
	live.BeginStage(state.StageFeatureDev)
	if err := live.Persist(dir); err != nil {
		t.Fatal(err)
	}
	held := state.NewRuntimeState("acme/api", 43, "item-43", f.paused)
	held.BeginStage(state.StageFeatureValidate)
	held.SetPaused(true)
	if err := held.Persist(dir); err != nil {
		t.Fatal(err)
	}
	// A run of a repository the teammate may not read, rooted here.
	other := state.NewRuntimeState("acme/secret", 44, "item-44", id())
	other.BeginStage(state.StageFeatureDev)
	if err := other.Persist(dir); err != nil {
		t.Fatal(err)
	}
	if err := state.NewHistoryWriter(root).WriteV2Record(state.V2RunRecord{
		SchemaVersion: "2", RecordType: "run", IssueNumber: 41, RunID: f.finished, Repo: "acme/api",
		Title: "Fix logout", Outcome: "complete", StartedAt: "2026-10-11T10:00:00Z", CompletedAt: "2026-10-11T11:00:00Z",
		RecordedAt: "2026-10-11T11:00:00Z",
	}, time.Now()); err != nil {
		t.Fatal(err)
	}
	return f
}

func runToolset(t *testing.T, root string, turn Turn) *Toolset {
	t.Helper()
	s := NewToolset(Env{Root: root, RepoRoots: map[string]string{"acme/api": root}, Forge: noNetwork{t},
		Runs: LocalRuns{}, Attention: noNetwork{t}}, turn)
	t.Cleanup(s.Close)
	return s
}

func runsIn(t *testing.T, content map[string]any, key string) []map[string]any {
	t.Helper()
	var out []map[string]any
	list, _ := content[key].([]any)
	for _, r := range list {
		out = append(out, r.(map[string]any))
	}
	return out
}

// "What is running?" is answered from local state: the executing run's id
// and stage, live by its fresh snapshot, and nothing paused or finished. No
// forge or hosted call is made, and the state directory is as it was.
func TestListRunningJobs_ReadsLocalStateOnly(t *testing.T) {
	f := newRunFixture(t)
	dir, _ := state.PipelineStateDir(f.root)
	before := listing(t, dir)
	s := runToolset(t, f.root, operatorTurn("What is running?"))

	text, err := call(t, s, ToolListRunningJobs, map[string]any{})
	if err != nil {
		t.Fatalf("list_running_jobs: %v", err)
	}
	notice, content := envelope(t, text)
	if notice != UntrustedNotice {
		t.Errorf("notice = %q", notice)
	}
	running := runsIn(t, content, "running")
	ids := map[string]map[string]any{}
	for _, r := range running {
		ids[r["run_id"].(string)] = r
	}
	got, ok := ids[f.running]
	if !ok || got["stage"] != string(state.StageFeatureDev) || got["issue"] != float64(42) ||
		got["state"] != "running" || !strings.HasPrefix(got["evidence"].(string), "timestamp-lease") {
		t.Errorf("running = %+v, want run %s in %s", running, f.running, state.StageFeatureDev)
	}
	if _, ok := ids[f.paused]; ok {
		t.Errorf("a paused run is listed as running: %+v", running)
	}
	if after := listing(t, dir); after != before {
		t.Errorf("reading changed the state directory:\nbefore %s\nafter  %s", before, after)
	}
	if invs := s.Invocations(); len(invs) != 1 || invs[0].Mutating {
		t.Errorf("invocations = %+v", invs)
	}
}

// pipeline_status shows the current runs with their states and the recent
// finished runs, bounded by limit.
func TestPipelineStatus_ShowsCurrentAndRecentRuns(t *testing.T) {
	f := newRunFixture(t)
	s := runToolset(t, f.root, operatorTurn("How is the pipeline doing?"))
	text, err := call(t, s, ToolPipelineStatus, map[string]any{"owner": "acme", "repo": "api"})
	if err != nil {
		t.Fatalf("pipeline_status: %v", err)
	}
	_, content := envelope(t, text)
	repos := runsIn(t, content, "repositories")
	if len(repos) != 1 || repos[0]["repo"] != "acme/api" || repos[0]["running"] != float64(1) || repos[0]["paused"] != float64(1) {
		t.Fatalf("repositories = %+v", repos)
	}
	states := map[string]string{}
	for _, r := range runsIn(t, repos[0], "current") {
		states[r["run_id"].(string)] = r["state"].(string)
	}
	if states[f.running] != "running" || states[f.paused] != "paused" {
		t.Errorf("current states = %v", states)
	}
	recent := runsIn(t, repos[0], "recent")
	if len(recent) != 1 || recent[0]["run_id"] != f.finished || recent[0]["state"] != "complete" || recent[0]["title"] != "Fix logout" {
		t.Errorf("recent = %+v", recent)
	}
	text, _ = call(t, s, ToolPipelineStatus, map[string]any{"limit": 0})
	_, content = envelope(t, text)
	if recent := runsIn(t, runsIn(t, content, "repositories")[0], "recent"); len(recent) != 0 {
		t.Errorf("limit 0 still listed %+v", recent)
	}
	for _, bad := range []map[string]any{{"limit": 51}, {"limit": -1}, {"owner": "acme"}, {"cancel": true}} {
		if _, err := call(t, s, ToolPipelineStatus, bad); code(err) != CodeInvalidArguments {
			t.Errorf("%v: %v, want %s", bad, err, CodeInvalidArguments)
		}
	}
	if _, err := call(t, s, ToolPipelineStatus, map[string]any{"owner": "acme", "repo": "web"}); code(err) != CodeRepoNotInWorkspace {
		t.Errorf("a repository with no checkout: %v", err)
	}
}

// A teammate sees runs only of the repositories the turn command lists as
// readable, including when a checkout's state holds another repository's
// run.
func TestRunTools_ShowATeammateOnlyReadableRepositories(t *testing.T) {
	f := newRunFixture(t)
	reader := runToolset(t, f.root, teammateTurn("acct-bob", "bob", "What is running?", Repo{"acme", "api"}))
	text, err := call(t, reader, ToolListRunningJobs, map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	_, content := envelope(t, text)
	for _, r := range runsIn(t, content, "running") {
		if r["repo"] != "acme/api" {
			t.Errorf("a teammate saw %v", r)
		}
	}
	if len(runsIn(t, content, "running")) != 1 {
		t.Errorf("running = %+v, want acme/api's one run", content["running"])
	}

	outsider := runToolset(t, f.root, teammateTurn("acct-eve", "eve", "What is running?"))
	text, err = call(t, outsider, ToolListRunningJobs, map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if _, content := envelope(t, text); len(runsIn(t, content, "running")) != 0 {
		t.Errorf("a teammate with no readable repository saw %+v", content["running"])
	}
	if _, err := call(t, outsider, ToolPipelineStatus, map[string]any{"owner": "acme", "repo": "api"}); code(err) != CodeRepoNotReadable {
		t.Errorf("an unreadable repository by name: %v", err)
	}
}

// Without a run source the run tools refuse rather than answer "nothing is
// running".
func TestRunTools_RefuseWithoutState(t *testing.T) {
	s := toolset(t, gitRepo(t), newFakeForge(), operatorTurn("What is running?"))
	for _, name := range []string{ToolPipelineStatus, ToolListRunningJobs} {
		if _, err := call(t, s, name, map[string]any{}); code(err) != CodeStateUnavailable {
			t.Errorf("%s: %v, want %s", name, err, CodeStateUnavailable)
		}
	}
}

func listing(t *testing.T, dir string) string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	for _, e := range entries {
		info, _ := e.Info()
		b.WriteString(e.Name() + "@" + info.ModTime().String() + ";")
	}
	return b.String()
}

// A private run is its creator's alone (#2400), and every private run on
// this machine is the operator's: the operator is shown it, marked; a
// teammate who may read the repository is shown neither the private run in
// flight nor the private finished one (#1587, security review).
func TestRunTools_NeverShowATeammateAPrivateRun(t *testing.T) {
	f := newRunFixture(t)
	dir, _ := state.PipelineStateDir(f.root)
	privateID, err := runstate.NewRunID()
	if err != nil {
		t.Fatal(err)
	}
	hidden := state.NewRuntimeState("acme/api", 46, "item-46", privateID)
	hidden.Title = "Private spike"
	hidden.SetVisibility(state.VisibilityPrivate)
	hidden.BeginStage(state.StageFeatureDev)
	if err := hidden.Persist(dir); err != nil {
		t.Fatal(err)
	}
	doneID, _ := runstate.NewRunID()
	if err := state.NewHistoryWriter(f.root).WriteV2Record(state.V2RunRecord{
		SchemaVersion: "2", RecordType: "run", IssueNumber: 47, RunID: doneID, Repo: "acme/api",
		Title: "Private finished", Outcome: "complete", Visibility: state.VisibilityPrivate,
		StartedAt: "2026-10-11T12:00:00Z", CompletedAt: "2026-10-11T12:30:00Z", RecordedAt: "2026-10-11T12:30:00Z",
	}, time.Now()); err != nil {
		t.Fatal(err)
	}

	operator := runToolset(t, f.root, operatorTurn("How is the pipeline doing?"))
	text, callErr := call(t, operator, ToolPipelineStatus, map[string]any{})
	if callErr != nil {
		t.Fatal(callErr)
	}
	if !strings.Contains(text, privateID) || !strings.Contains(text, doneID) || !strings.Contains(text, `"private":true`) {
		t.Errorf("the operator does not see their own private runs: %s", text)
	}

	bob := teammateTurn("acct-bob", "bob", "How is the pipeline doing?", Repo{"acme", "api"})
	for _, name := range []string{ToolPipelineStatus, ToolListRunningJobs} {
		text, err := call(t, runToolset(t, f.root, bob), name, map[string]any{})
		if err != nil {
			t.Fatal(err)
		}
		for _, leaked := range []string{privateID, doneID, "Private spike", "Private finished"} {
			if strings.Contains(text, leaked) {
				t.Errorf("%s shows a teammate the operator's private run (%s): %s", name, leaked, text)
			}
		}
		if !strings.Contains(text, f.running) {
			t.Errorf("%s hides the team run too: %s", name, text)
		}
	}
}

// Nothing the run tools return names a file on this machine or a process
// id: an unreadable snapshot is counted, not described (#1587, security
// review).
func TestRunTools_NameNoLocalPathOrProcess(t *testing.T) {
	f := newRunFixture(t)
	dir, _ := state.PipelineStateDir(f.root)
	badID, _ := runstate.NewRunID()
	if err := os.WriteFile(dir+"/"+state.SnapshotFilename(45, badID), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	live := state.NewRuntimeState("acme/api", 48, "item-48", mustID(t))
	live.SetStageChild(os.Getpid())
	live.BeginStage(state.StageFeatureDev)
	live.SetStageChild(os.Getpid())
	if err := live.Persist(dir); err != nil {
		t.Fatal(err)
	}
	for _, turn := range []Turn{operatorTurn("What is running?"), teammateTurn("acct-bob", "bob", "What is running?", Repo{"acme", "api"})} {
		for _, name := range []string{ToolPipelineStatus, ToolListRunningJobs} {
			text, err := call(t, runToolset(t, f.root, turn), name, map[string]any{})
			if err != nil {
				t.Fatal(err)
			}
			for _, leaked := range []string{f.root, dir, "runtime-45", badID, "pid "} {
				if strings.Contains(text, leaked) {
					t.Errorf("%s names %q: %s", name, leaked, text)
				}
			}
			if !strings.Contains(text, "could not be") {
				t.Errorf("%s does not say a file was unreadable: %s", name, text)
			}
		}
	}
}

func mustID(t *testing.T) string {
	t.Helper()
	id, err := runstate.NewRunID()
	if err != nil {
		t.Fatal(err)
	}
	return id
}
