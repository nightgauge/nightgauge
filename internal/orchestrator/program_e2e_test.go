package orchestrator

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/depgraph"
	"github.com/nightgauge/nightgauge/internal/forge"
	forgetypes "github.com/nightgauge/nightgauge/internal/forge/types"
	gh "github.com/nightgauge/nightgauge/internal/github"
	"github.com/nightgauge/nightgauge/internal/hooks"
	"github.com/nightgauge/nightgauge/internal/trust"
	"github.com/nightgauge/nightgauge/pkg/types"
)

// programCommentForge is one in-memory comment store behind a real trust.Gate.
// The post-merge hook writes the validation marker into it and the scheduler
// reads it back, so both sides of the #2574 contract see the same comments
// through the same trusted-comment filter production wires.
type programCommentForge struct {
	fakeTrustForge
	cmu      sync.Mutex
	comments map[string][]trust.Comment // "owner/repo#N"
	nodes    map[string]string          // issue node ID -> "owner/repo#N"
	bot      string                     // the author AddComment posts as
}

func (f *programCommentForge) IssueComments(_ context.Context, owner, repo string, number int) ([]trust.Comment, error) {
	f.cmu.Lock()
	defer f.cmu.Unlock()
	return append([]trust.Comment(nil), f.comments[fmt.Sprintf("%s/%s#%d", owner, repo, number)]...), nil
}

func (f *programCommentForge) post(key, author, body string) {
	f.cmu.Lock()
	defer f.cmu.Unlock()
	f.comments[key] = append(f.comments[key], trust.Comment{Author: author, Body: body, CreatedAt: time.Now()})
}

// programMarks is the hook's ValidationMarkWriter: trusted reads through the
// gate, writes into the store as the pipeline's identity.
type programMarks struct {
	*trust.Gate
	store *programCommentForge
}

func (m programMarks) AddComment(_ context.Context, subjectID, body string) error {
	key, ok := m.store.nodes[subjectID]
	if !ok {
		return fmt.Errorf("unknown subject %s", subjectID)
	}
	m.store.post(key, m.store.bot, body)
	return nil
}

type programIssues struct{ issues map[string]*types.Issue }

func (p programIssues) GetIssueWithRelations(_ context.Context, owner, repo string, number int, _ gh.IssueRelations) (*types.Issue, error) {
	if iss, ok := p.issues[fmt.Sprintf("%s/%s#%d", owner, repo, number)]; ok {
		return iss, nil
	}
	return nil, fmt.Errorf("no issue %s/%s#%d", owner, repo, number)
}

type programCloser struct{}

func (programCloser) CloseIssue(context.Context, string) error { return nil }

type programMergedPR struct{ info gh.PRMergeInfo }

func (p programMergedPR) GetPRState(context.Context, string, string, int) (string, error) {
	return "MERGED", nil
}

func (p programMergedPR) GetPRMergeInfo(context.Context, string, string, int) (gh.PRMergeInfo, error) {
	return p.info, nil
}

// programMainChecks reports one fixed conclusion for the required "test" check.
type programMainChecks struct{ conclusion string }

func (c programMainChecks) GetCommitChecks(context.Context, string, string, string) ([]forgetypes.CheckDetail, error) {
	return []forgetypes.CheckDetail{{Name: "test", Status: "COMPLETED", Conclusion: c.conclusion}}, nil
}

func (c programMainChecks) GetRequiredCheckNames(context.Context, string, string, string) ([]string, error) {
	return []string{"test"}, nil
}

// TestIntegration_Program_TwoRepos_ValidationGated runs a two-repository
// program end to end with no orchestrator session (#2575): O/flutter#20 is
// blockedBy O/platform#10 across repositories, both carry program:demo. The
// real scheduler cycle holds #20 while #10 is open, closed without the
// marker, closed with main red, or carrying a marker from an untrusted
// author; the real post-merge hook writes the marker only on a green main,
// and the next cycle dispatches #20.
func TestIntegration_Program_TwoRepos_ValidationGated(t *testing.T) {
	const (
		blockerKey = "O/platform#10"
		mergeSHA   = "feedc0de"
	)
	program := []string{"program:demo"}

	store := &programCommentForge{
		fakeTrustForge: fakeTrustForge{levels: map[string]trust.Level{
			"maintainer": trust.LevelWrite,
			"mallory":    trust.LevelRead,
		}},
		comments: map[string][]trust.Comment{},
		nodes:    map[string]string{"I_10": blockerKey},
		bot:      "nightgauge-pipeline[bot]",
	}
	gate := trust.NewGate(store, nil)
	if !gate.IsTrustedBot(store.bot) {
		t.Fatalf("harness: %s must be a default trusted bot", store.bot)
	}

	platformState := "OPEN"
	h := newTestHarness(t, AutonomousConfig{
		MaxConcurrent: 3,
		SafetyRails:   &SafetyConfig{RateLimitPerHour: 100},
	}, func() *depgraph.Graph {
		board := "Ready"
		if platformState == "CLOSED" {
			board = "Done"
		}
		return buildTestGraph([]*depgraph.Node{
			{Repo: "O/platform", Number: 10, Title: "Platform API", State: platformState, BoardStatus: board, Priority: "P0", Size: "M", Weight: 3, Labels: program},
			{Repo: "O/flutter", Number: 20, Title: "Flutter consumer", State: "OPEN", BoardStatus: "Ready", Priority: "P1", Size: "S", Weight: 2, Labels: program},
		}, []depgraph.Edge{
			{From: depgraph.NodeID{Repo: "O/flutter", Number: 20}, To: depgraph.NodeID{Repo: "O/platform", Number: 10}},
		})
	})
	as := h.scheduler
	// The production resolver (autonomous.go), minus the GitHub client: both
	// program members are on the board, so only trusted comments are read.
	as.resolveValidationFn = func(ctx context.Context, known map[string][]string, unknown []string) map[string]ValidationState {
		return ResolveValidationByKey(ctx, nil, gate, known, unknown)
	}

	// runOneCycle marks the harness "complete" when a cycle leaves nothing
	// dispatchable; the production loop keeps scanning, so a held program
	// dependent is re-evaluated on the next tick. Model that loop.
	cycle := func() {
		t.Helper()
		as.mu.Lock()
		as.state.Status = "running"
		as.mu.Unlock()
		h.runOneCycle(t)
	}
	running := func() []string {
		as.mu.Lock()
		defer as.mu.Unlock()
		out := make([]string, 0, len(as.state.Running))
		for _, r := range as.state.Running {
			out = append(out, fmt.Sprintf("%s#%d", r.Repo, r.Number))
		}
		return out
	}
	assertHeld := func(stage string) {
		t.Helper()
		cycle()
		if got := running(); len(got) != 0 {
			t.Fatalf("%s: dispatched %v; O/flutter#20 must stay held", stage, got)
		}
		as.mu.Lock()
		n := as.state.LastRejectionReasons["blocked-by-unvalidated-dep"]
		as.mu.Unlock()
		if n != 1 {
			t.Fatalf("%s: rejections = %v, want blocked-by-unvalidated-dep=1", stage, as.state.LastRejectionReasons)
		}
	}
	postMerge := func(conclusion string) hooks.PostMergeResult {
		t.Helper()
		issues := programIssues{issues: map[string]*types.Issue{
			blockerKey: {NodeID: "I_10", Number: 10, State: "CLOSED", Labels: program},
		}}
		pr := programMergedPR{info: gh.PRMergeInfo{SHA: mergeSHA, MergedAt: "2026-10-26T10:00:00Z", BaseRef: "main"}}
		return hooks.EvaluatePostMerge(context.Background(), issues, programCloser{}, nil, pr, nil, hooks.PostMergeInput{
			IssueNumber: 10, RepositoryOwner: "O", RepositoryName: "platform", PRNumber: 11,
			MainChecks:      programMainChecks{conclusion: conclusion},
			ValidationMarks: programMarks{Gate: gate, store: store},
		})
	}

	// Cycle 1: only the blocker runs.
	h.runOneCycle(t)
	if got := running(); len(got) != 1 || got[0] != blockerKey {
		t.Fatalf("cycle 1: running %v, want [%s]", got, blockerKey)
	}
	h.simulateCompletion("O/platform", 10, true)
	platformState = "CLOSED"

	// Closed with no marker: closure alone does not release a program dependent.
	assertHeld("closed without marker")

	// Main red after the merge: the hook writes nothing and #20 stays held.
	red := postMerge("FAILURE")
	if red.MainChecks == nil || red.MainChecks.Verdict != hooks.MainChecksRed || red.ValidationMarker != hooks.ValidationMarkerNotGreen {
		t.Fatalf("red post-merge = %+v / %q, want red / not_green", red.MainChecks, red.ValidationMarker)
	}
	assertHeld("main red")

	// A marker pasted by a read-only user is filtered out by the gate.
	store.post(blockerKey, "mallory", forge.ValidationMarkerComment("main", mergeSHA))
	assertHeld("untrusted marker")

	// Main green: the real hook writes the marker and the next cycle releases #20.
	green := postMerge("SUCCESS")
	if green.MainChecks == nil || green.MainChecks.Verdict != hooks.MainChecksGreen || green.ValidationMarker != hooks.ValidationMarkerWritten {
		t.Fatalf("green post-merge = %+v / %q, want green / written", green.MainChecks, green.ValidationMarker)
	}
	trusted, err := gate.TrustedComments(context.Background(), "O", "platform", 10)
	if err != nil || len(trusted) != 1 || !strings.Contains(trusted[0].Body, forge.ValidationMarker(mergeSHA)) {
		t.Fatalf("trusted comments on %s = %+v (err %v), want exactly the hook's marker for %s", blockerKey, trusted, err, mergeSHA)
	}
	cycle()
	if got := running(); len(got) != 1 || got[0] != "O/flutter#20" {
		t.Fatalf("after green post-merge: running %v, want [O/flutter#20]", got)
	}

	// A re-run of the hook is idempotent: the marker is present, not re-posted.
	if again := postMerge("SUCCESS"); again.ValidationMarker != hooks.ValidationMarkerPresent {
		t.Errorf("hook re-run = %q, want present", again.ValidationMarker)
	}
}

// The legacy bypass marker (#2575) releases a program blocker only from a
// trusted comment: the same trust filter gates it as the current marker.
func TestResolveValidationByKey_LegacyMarkerTrustGated(t *testing.T) {
	const key = "O/platform#10"
	program := []string{"program:demo"}
	resolve := func(author string) ValidationState {
		t.Helper()
		store := &programCommentForge{
			fakeTrustForge: fakeTrustForge{levels: map[string]trust.Level{
				"maintainer": trust.LevelWrite,
				"mallory":    trust.LevelRead,
			}},
			comments: map[string][]trust.Comment{},
			bot:      "nightgauge-pipeline[bot]",
		}
		store.post(key, author, "<!-- opencode-program:validated -->")
		gate := trust.NewGate(store, nil)
		return ResolveValidationByKey(context.Background(), nil, gate, map[string][]string{key: program}, nil)[key]
	}
	if got := resolve("maintainer"); !got.Program || !got.Validated {
		t.Errorf("trusted legacy marker = %+v, want program and validated", got)
	}
	if got := resolve("mallory"); !got.Program || got.Validated {
		t.Errorf("untrusted legacy marker = %+v, want program, not validated", got)
	}
}
