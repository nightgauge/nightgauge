package ipc

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/attention"
	"github.com/nightgauge/nightgauge/internal/depgraph"
	forgetypes "github.com/nightgauge/nightgauge/internal/forge/types"
	gh "github.com/nightgauge/nightgauge/internal/github"
	"github.com/nightgauge/nightgauge/internal/layout/layouttest"
	"github.com/nightgauge/nightgauge/internal/orchestrator"
	"github.com/nightgauge/nightgauge/internal/preflight"
	"github.com/nightgauge/nightgauge/pkg/types"
)

// testRepos are the repo slugs the attention tests raise against. They are
// REGISTERED on the test server's resolver because attention.raise rejects a
// repo this daemon has no configuration for (#305 review) — an unconfigured
// (repo, issue) pair is an unbounded card-injection primitive.
var testRepos = []string{"octocat/acme", "o/r"}

// newAttentionTestServer builds a minimal Server backed by a real, store-wired
// autonomous scheduler rooted at a temp workspace, with testRepos registered
// and pointed at that same root.
func newAttentionTestServer(t *testing.T) *Server {
	t.Helper()
	root := layouttest.Repo(t)
	as := orchestrator.NewAutonomousScheduler(nil, nil, nil, nil, orchestrator.DefaultAutonomousConfig(), root)
	if as.Attention() == nil {
		t.Fatal("attention store not wired")
	}
	resolver := NewClientResolver(nil, false)
	for _, slug := range testRepos {
		owner, name := splitSlug(slug)
		resolver.RegisterRepo(owner, name, root)
	}
	s := &Server{
		autonomousScheduler: as,
		writer:              io.Discard,
		workspaceRoot:       root,
		resolver:            resolver,
		activeRuntimes:      make(map[string]*runEntry),
		methods:             make(map[string]Handler),
	}
	// Register the real method table so tests can drive the daemon through the
	// SAME entry points a socket caller uses (#305 round-4 review). The
	// budget-ceiling corroboration rules are about what
	// `pipeline.notifyStageTransition` can and cannot establish, and a test that
	// seeded `activeRuntimes` by hand asserted their premise instead of
	// exercising it.
	s.registerMethods()
	return s
}

func splitSlug(slug string) (owner, name string) {
	for i := 0; i < len(slug); i++ {
		if slug[i] == '/' {
			return slug[:i], slug[i+1:]
		}
	}
	return "", slug
}

func TestAttentionIPCRoundTrip(t *testing.T) {
	layouttest.StateHome(t)
	s := newAttentionTestServer(t)
	store := s.attentionStore()

	id, err := attention.NewID()
	if err != nil {
		t.Fatalf("NewID: %v", err)
	}
	if _, _, err := store.Raise(attention.DecisionRequest{
		ID:             id,
		IdempotencyKey: "roundtrip:1",
		Kind:           attention.KindChoose,
		Severity:       attention.SeverityBlockingRun,
		Title:          "round trip",
		Body:           "why",
		Producer:       "test",
		Context:        attention.Context{Repo: "octocat/acme"}, // fleet-ish: no run_id
		Options: []attention.Option{
			{ID: "go", Label: "Go", Verb: attention.VerbNoop},
			{ID: "leave", Label: "Leave", Verb: attention.VerbNoop},
		},
		DefaultAction: "leave",
	}); err != nil {
		t.Fatalf("Raise: %v", err)
	}

	ctx := context.Background()

	// list → one open request
	listRaw, _ := json.Marshal(AttentionListParams{})
	lres, err := s.handleAttentionList(ctx, listRaw)
	if err != nil {
		t.Fatalf("handleAttentionList: %v", err)
	}
	if got := len(lres.(AttentionListResult).Requests); got != 1 {
		t.Fatalf("list returned %d, want 1", got)
	}

	// acknowledge → ok
	ackRaw, _ := json.Marshal(AttentionAcknowledgeParams{ID: id, Actor: "octocat"})
	ares, err := s.handleAttentionAcknowledge(ctx, ackRaw)
	if err != nil {
		t.Fatalf("handleAttentionAcknowledge: %v", err)
	}
	if !ares.(AttentionAcknowledgeResult).Ok {
		t.Error("acknowledge not ok")
	}

	// resolve (noop verb) → ok, not already-resolved
	resRaw, _ := json.Marshal(AttentionResolveParams{ID: id, OptionID: "go", Actor: "octocat", Note: "done"})
	rres, err := s.handleAttentionResolve(ctx, resRaw)
	if err != nil {
		t.Fatalf("handleAttentionResolve: %v", err)
	}
	rr := rres.(AttentionResolveResult)
	if !rr.Ok || rr.AlreadyResolved {
		t.Errorf("resolve result = %+v, want ok && !alreadyResolved", rr)
	}

	// list again → gone from the open set
	lres2, _ := s.handleAttentionList(ctx, listRaw)
	if got := len(lres2.(AttentionListResult).Requests); got != 0 {
		t.Fatalf("open list after resolve = %d, want 0", got)
	}

	// the persisted request is terminal
	got, _, _ := store.Get(id)
	if got.Lifecycle.State != attention.StateResolved {
		t.Errorf("state = %q, want resolved", got.Lifecycle.State)
	}
}

func TestAttentionResolveRejectsUnknownOption(t *testing.T) {
	s := newAttentionTestServer(t)
	store := s.attentionStore()
	id, _ := attention.NewID()
	if _, _, err := store.Raise(attention.DecisionRequest{
		ID:             id,
		IdempotencyKey: "reject:1",
		Kind:           attention.KindApprove,
		Severity:       attention.SeverityFYI,
		Title:          "t",
		Producer:       "test",
		Options:        []attention.Option{{ID: "ok", Verb: attention.VerbNoop}},
		DefaultAction:  attention.ExpireNoop,
	}); err != nil {
		t.Fatalf("Raise: %v", err)
	}
	resRaw, _ := json.Marshal(AttentionResolveParams{ID: id, OptionID: "smuggled"})
	if _, err := s.handleAttentionResolve(context.Background(), resRaw); err == nil {
		t.Fatal("expected rejection of an undeclared option id")
	}
}

// The Approve & re-queue card hands the issue to the shared architecture-hold
// release (#2717): it adds no Go-side queue item (one that never woke
// fillSlots stalled the fleet) and keeps the Failed entry, now ReleasePending,
// so the release can retry a failed board write.
func TestReleaseApprovedArchitecture_UsesSharedReleaseAndAddsNoQueueItem(t *testing.T) {
	root := layouttest.Repo(t)
	statePath, err := orchestrator.AutonomousStatePath(root)
	if err != nil {
		t.Fatal(err)
	}
	seed := orchestrator.AutonomousState{Failed: []orchestrator.FailedItem{{
		Repo: "o/r", Number: 7, Kind: orchestrator.TerminalKindArchitectureApprovalRequired, AttemptCount: 1,
	}}}
	b, _ := json.Marshal(seed)
	if err := os.MkdirAll(filepath.Dir(statePath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(statePath, b, 0o644); err != nil {
		t.Fatal(err)
	}
	// A configured board with no forge client: the release cannot read the
	// PR list, so it keeps the entry pending instead of dropping it.
	as := orchestrator.NewAutonomousScheduler(nil, nil,
		[]depgraph.RepoConfig{{Owner: "o", Name: "r", Project: 3}}, nil,
		orchestrator.DefaultAutonomousConfig(), root)
	t.Cleanup(func() { as.Shutdown(time.Second) })
	sched := orchestrator.NewScheduler(nil, orchestrator.SchedulerConfig{WorkspaceRoot: root})
	s := &Server{autonomousScheduler: as, scheduler: sched, writer: io.Discard, workspaceRoot: root}

	if err := s.releaseApprovedArchitecture(context.Background(), "o/r", 7, "approved:architecture"); err != nil {
		t.Fatalf("releaseApprovedArchitecture: %v", err)
	}

	if n := len(sched.QueuePendingItems()); n != 0 {
		t.Errorf("queue has %d pending items, want 0 — Ready on the board is the re-queue", n)
	}
	failed := as.Status().Failed
	if len(failed) != 1 || !failed[0].ReleasePending {
		t.Fatalf("Failed = %+v, want the hold kept as ReleasePending for the shared release", failed)
	}
}

const remainderBody = "## Acceptance Criteria\n\n- [ ] A is handled\n"

type remainderForge struct {
	state, body              string
	readErr, closeErr        error
	comments, closed, boards []string
}

func (f *remainderForge) GetIssueWithRelations(_ context.Context, _, _ string, n int, _ gh.IssueRelations) (*types.Issue, error) {
	if f.readErr != nil {
		return nil, f.readErr
	}
	return &types.Issue{NodeID: "I_7", Number: n, State: f.state, Body: f.body}, nil
}

func (f *remainderForge) AddComment(_ context.Context, _, body string) error {
	f.comments = append(f.comments, body)
	return nil
}

func (f *remainderForge) EditIssue(context.Context, string, string) (*types.Issue, error) {
	return nil, nil
}

func (f *remainderForge) CloseIssue(_ context.Context, id string) error {
	f.closed = append(f.closed, id)
	return f.closeErr
}

func (f *remainderForge) SyncStatus(_ context.Context, _, _ string, _ int, status string) error {
	f.boards = append(f.boards, status)
	return nil
}

func (f *remainderForge) AddIssueByNumber(context.Context, string, string, int) (string, error) {
	return "", nil
}

// #3487: resolving a no-op card with close, through the store as IPC and a
// relayed resolve both do, comments, closes, moves the board and releases the
// issue's hold. A failure leaves both the card and the hold.
func TestResolveNoOpCard(t *testing.T) {
	down := errors.New("forge down")
	for _, tc := range []struct {
		name, option, state, body string
		login, actor              string
		depsErr, readErr          error
		closeErr, wantErr         error
		superseded                bool
		comments, closed, boards  int
	}{
		{name: "close", login: "octocat", comments: 1, closed: 1, boards: 1},
		{name: "close, no login", actor: "jane.doe@example.com", comments: 1, closed: 1, boards: 1},
		{name: "already closed", state: "CLOSED", boards: 1},
		{name: "ack", option: "ack"},
		{name: "no client", depsErr: down, wantErr: down},
		{name: "issue unreadable", readErr: down, wantErr: down},
		{name: "close fails", closeErr: down, wantErr: down, comments: 1, closed: 1},
		{name: "revised", body: remainderBody + "- [ ] B is handled\n", wantErr: orchestrator.ErrRemainderRevised},
		{name: "superseded", superseded: true, wantErr: orchestrator.ErrRemainderSuperseded},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newAttentionTestServer(t)
			initial := s.autonomousScheduler
			t.Cleanup(func() { initial.Shutdown(time.Second) })
			dir := filepath.Join(s.workspaceRoot, ".nightgauge")
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			// What puts o/r in the configured set the verb checks.
			cfg := "project:\n  owner: o\n  number: 3\n  repo: r\n"
			if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(cfg), 0o644); err != nil {
				t.Fatal(err)
			}
			statePath, err := orchestrator.AutonomousStatePath(s.workspaceRoot)
			if err != nil {
				t.Fatal(err)
			}
			seed, err := json.Marshal(orchestrator.AutonomousState{Failed: []orchestrator.FailedItem{{
				Repo: "o/r", Number: 7, Kind: orchestrator.TerminalKindNotPipelineActionable, AttemptCount: 1,
			}}})
			if err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(filepath.Dir(statePath), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(statePath, seed, 0o644); err != nil {
				t.Fatal(err)
			}
			as := orchestrator.NewAutonomousScheduler(nil, nil, nil, nil, orchestrator.DefaultAutonomousConfig(), s.workspaceRoot)
			t.Cleanup(func() { as.Shutdown(time.Second) })
			s.autonomousScheduler = as
			if len(as.Status().Failed) != 1 {
				t.Fatal("precondition: no hold")
			}

			rec := orchestrator.PartialDeliveryFile{PR: 5, Criteria: []string{"A is handled"}}
			rec.CriteriaFingerprint = preflight.ACFingerprint(remainderBody)
			cardRecord, err := orchestrator.RemainderFingerprint(rec)
			if err != nil {
				t.Fatal(err)
			}
			if tc.superseded {
				rec.PR = 9
			}
			if err := orchestrator.WritePartialDeliveryContext(s.workspaceRoot, 7, rec); err != nil {
				t.Fatal(err)
			}

			f := &remainderForge{state: "OPEN", body: remainderBody, readErr: tc.readErr, closeErr: tc.closeErr}
			if tc.state != "" {
				f.state = tc.state
			}
			if tc.body != "" {
				f.body = tc.body
			}
			used := false
			s.remainderCloseDepsOverride = func(_ context.Context, owner, name string) (remainderCloseForge, error) {
				used = true
				if tc.depsErr != nil {
					return remainderCloseForge{}, tc.depsErr
				}
				return remainderCloseForge{
					deps:   orchestrator.PartialCloseDeps{Issues: f, Board: f, Owner: owner, Repo: name, ProjectNumber: 3},
					reader: f,
					login:  tc.login,
				}, nil
			}

			store := s.attentionStore()
			cardID, err := attention.NewID()
			if err != nil {
				t.Fatal(err)
			}
			_, id, err := store.Raise(attention.DecisionRequest{
				ID:             cardID,
				IdempotencyKey: "not-pipeline-actionable:o/r#7",
				Kind:           attention.KindHandoff,
				Severity:       attention.SeverityBlockingRun,
				Title:          "no-op",
				Body:           "open criteria",
				Producer:       attention.ProducerNotPipelineActionable,
				Context:        attention.Context{Repo: "o/r", Issue: 7, PR: 5},
				Options: []attention.Option{
					{ID: "ack", Label: "Acknowledge", Verb: attention.VerbNoop},
					{ID: "close", Label: "Close", Verb: attention.VerbIssueCloseRemainder, Args: map[string]any{
						"pr":                    5,
						"criteria":              []attention.RemainderCriterion{{Text: "A is handled", Evidence: "undecidable: prose"}},
						"remainder_fingerprint": cardRecord,
					}},
				},
				DefaultAction: "ack",
				ExpiresAt:     time.Now().Add(time.Hour).UTC().Format(time.RFC3339Nano),
			})
			if err != nil {
				t.Fatalf("raise: %v", err)
			}

			option, actor := tc.option, tc.actor
			if option == "" {
				option = "close"
			}
			if actor == "" {
				actor = "local-os-account"
			}
			_, err = store.Resolve(context.Background(), id, option, actor, "", "", s)
			wantState, wantHolds := attention.StateResolved, 0
			if option == "ack" {
				wantHolds = 1
			}
			if tc.wantErr != nil {
				wantState, wantHolds = attention.StateOpen, 1
				var cause *attention.VerbCauseError
				if !errors.As(err, &cause) || !strings.Contains(cause.Cause, tc.wantErr.Error()) {
					t.Fatalf("err = %v, want the cause %q", err, tc.wantErr)
				}
			} else if err != nil {
				t.Fatalf("resolve: %v", err)
			}
			card, found, err := store.Get(id)
			if err != nil || !found || card.Lifecycle.State != wantState {
				t.Fatalf("card = %+v (found %v, err %v), want state %s", card, found, err, wantState)
			}
			if got := len(as.Status().Failed); got != wantHolds {
				t.Errorf("holds = %d, want %d", got, wantHolds)
			}
			if used != (option == "close") || len(f.comments) != tc.comments || len(f.closed) != tc.closed || len(f.boards) != tc.boards {
				t.Fatalf("used=%v comments=%v closed=%v boards=%v", used, f.comments, f.closed, f.boards)
			}
			if tc.closed == 1 && f.closed[0] != "I_7" {
				t.Errorf("closed %q, want I_7", f.closed[0])
			}
			if tc.boards == 1 && !strings.EqualFold(f.boards[0], "done") {
				t.Errorf("board status = %q, want Done", f.boards[0])
			}
			if tc.comments == 1 {
				who := "An operator"
				if tc.login != "" {
					who = "An operator, acting through the `" + tc.login + "` account,"
				}
				for _, want := range []string{who + " decided", "PR #5", "- A is handled — undecidable: prose"} {
					if !strings.Contains(f.comments[0], want) {
						t.Errorf("comment lacks %q: %s", want, f.comments[0])
					}
				}
				for _, private := range []string{actor, "jane", "example.com"} {
					if strings.Contains(f.comments[0], private) {
						t.Errorf("comment names the resolve actor %q: %s", private, f.comments[0])
					}
				}
			}
		})
	}
}

type roundTripFunc3487 func(*http.Request) (*http.Response, error)

func (f roundTripFunc3487) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// The close writes to the card's repository's board, not the startup one, and
// still closes when no board resolves.
func TestRemainderCloseDeps(t *testing.T) {
	for _, tc := range []struct {
		name, repo, config string
		project            int
		ownerField         string
	}{
		{name: "the startup repo", repo: "r", project: 3, ownerField: "organization(",
			config: "project:\n  owner: o\n  number: 3\n  repo: r\n"},
		{name: "the repo's own board", repo: "app", project: 9, ownerField: "user(",
			config: "project:\n  owner: o\n  owner_type: user\n  number: 3\n  repo: r\n" +
				"autonomous:\n  repositories:\n    app:\n      project_number: 9\n"},
		{name: "only another repo's board", repo: "app",
			config: "project:\n  owner: o\n  number: 3\n  repo: r\n"},
		{name: "no board", repo: "r", config: "project:\n  owner: o\n  repo: r\n"},
		{name: "unreadable config", repo: "r", config: "project: [\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := layouttest.Repo(t)
			if err := os.MkdirAll(filepath.Join(root, ".nightgauge"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, ".nightgauge", "config.yaml"), []byte(tc.config), 0o644); err != nil {
				t.Fatal(err)
			}
			var queries []string
			client := gh.NewClientWithHTTPClient(&http.Client{Transport: roundTripFunc3487(func(r *http.Request) (*http.Response, error) {
				body := `{"login":"octocat"}`
				if r.URL.Path != "/user" {
					q, err := io.ReadAll(r.Body)
					if err != nil {
						return nil, err
					}
					queries = append(queries, string(q))
					body = `{"errors":[{"message":"stop"}]}`
				}
				return &http.Response{StatusCode: http.StatusOK, Header: http.Header{},
					Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
			})})
			s := &Server{resolver: NewClientResolver(client, false), workspaceRoot: root}

			f, err := s.remainderCloseDeps(context.Background(), "o", tc.repo)
			if err != nil {
				t.Fatalf("an unresolved board must not stop the close: %v", err)
			}
			if f.deps.Issues == nil || f.deps.Comments == nil || f.reader == nil || f.login != "octocat" || f.postedAs != "octocat" ||
				f.deps.Owner != "o" || f.deps.Repo != tc.repo {
				t.Fatalf("forge = %+v", f)
			}
			if f.deps.ProjectNumber != tc.project || (f.deps.Board != nil) != (tc.project > 0) {
				t.Fatalf("board = %v #%d, want project %d", f.deps.Board, f.deps.ProjectNumber, tc.project)
			}
			if tc.project == 0 {
				return
			}
			_ = f.deps.Board.SyncStatus(context.Background(), "o", tc.repo, 7, "done")
			if sent := strings.Join(queries, "\n"); !strings.Contains(sent, tc.ownerField) {
				t.Errorf("board was not read as %s: %s", tc.ownerField, sent)
			}
		})
	}

	t.Run("no client", func(t *testing.T) {
		s := &Server{resolver: NewClientResolver(nil, false), workspaceRoot: layouttest.Repo(t)}
		if _, err := s.remainderCloseDeps(context.Background(), "o", "r"); err == nil || !strings.Contains(err.Error(), "no GitHub client") {
			t.Fatalf("err = %v, want no client", err)
		}
	})
	t.Run("client resolution fails", func(t *testing.T) {
		root := layouttest.Repo(t)
		if err := os.MkdirAll(filepath.Join(root, ".nightgauge"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, ".nightgauge", "config.yaml"), []byte("project: [\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		resolver := NewClientResolver(nil, false)
		resolver.RegisterRepo("o", "r", root)
		s := &Server{resolver: resolver, workspaceRoot: root}
		if _, err := s.remainderCloseDeps(context.Background(), "o", "r"); err == nil ||
			!strings.Contains(err.Error(), "could not set up a GitHub client") || strings.Contains(err.Error(), root) {
			t.Fatalf("err = %v, want the failure without its local path", err)
		}
	})
}

type fakeIdentity struct {
	app   *gh.AppCredentials
	login string
	err   error
	none  bool
}

func (f fakeIdentity) App() *gh.AppCredentials { return f.app }

func (f fakeIdentity) Whoami(ctx context.Context) (*forgetypes.Actor, error) {
	if _, bounded := ctx.Deadline(); !bounded {
		return nil, errors.New("the lookup has no deadline")
	}
	if f.err != nil || f.none {
		return nil, f.err
	}
	return &forgetypes.Actor{Login: f.login}, nil
}

func TestClosingLogin(t *testing.T) {
	for name, tc := range map[string]struct {
		id   fakeIdentity
		want string
	}{
		"user":         {fakeIdentity{login: "octocat"}, "octocat"},
		"app":          {fakeIdentity{app: &gh.AppCredentials{Slug: "pipeline"}, login: "pipeline"}, ""},
		"whoami fails": {fakeIdentity{err: errors.New("down")}, ""},
		"empty":        {fakeIdentity{}, ""},
		"nobody":       {fakeIdentity{none: true}, ""},
	} {
		if got := closingLogin(context.Background(), tc.id); got != tc.want {
			t.Errorf("%s: login = %q, want %q", name, got, tc.want)
		}
	}
}

func TestRemainderCloser_RefusesATargetWithoutAnOwner(t *testing.T) {
	s := &Server{remainderCloseDepsOverride: func(context.Context, string, string) (remainderCloseForge, error) {
		t.Fatal("a malformed target reached the forge")
		return remainderCloseForge{}, nil
	}}
	err := remainderCloser{server: s}.CloseRemainder(context.Background(), "app", 7, 5, "sha256:x", nil)
	if err == nil || !strings.Contains(err.Error(), "not owner/name") {
		t.Fatalf("err = %v, want a refusal", err)
	}
}
