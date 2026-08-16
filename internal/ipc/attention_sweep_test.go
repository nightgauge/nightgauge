package ipc

// Tests for the attention.sweep / attention.mute / attention.unmute IPC surface
// (issue #93).
//
// These drive the REAL producer registry against a canned forge client rather
// than a scripted stub, because the thing worth pinning is the wiring: a sweep
// asked for over IPC has to reconcile into the same store attention.list reads
// and the `attention.event` push is subscribed to. A stubbed registry would
// prove the handler compiles and nothing else.

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"testing"

	"github.com/nightgauge/nightgauge/internal/attention"
	"github.com/nightgauge/nightgauge/internal/attention/sweep"
	"github.com/nightgauge/nightgauge/internal/forge"
	forgetypes "github.com/nightgauge/nightgauge/internal/forge/types"
	"github.com/nightgauge/nightgauge/pkg/types"
)

const sweepTestRepo = "octocat/acme-web"

// --- canned forge client ----------------------------------------------------
//
// Each service embeds its interface so the fake satisfies it at compile time
// and panics loudly if a producer starts calling something this fixture never
// modelled — better than silently returning a zero value that reads as "no
// condition" and makes a broken producer look healthy.

type sweepFakeForge struct {
	ci       *sweepFakeCI
	prs      *sweepFakePRs
	repo     *sweepFakeRepo
	security *sweepFakeSecurity
}

func (f *sweepFakeForge) Issues() forge.IssueService      { return nil }
func (f *sweepFakeForge) PRs() forge.PRService            { return f.prs }
func (f *sweepFakeForge) Project() forge.ProjectService   { return nil }
func (f *sweepFakeForge) Board() forge.BoardService       { return nil }
func (f *sweepFakeForge) CI() forge.CIService             { return f.ci }
func (f *sweepFakeForge) Labels() forge.LabelService      { return nil }
func (f *sweepFakeForge) Rulesets() forge.RulesetService  { return nil }
func (f *sweepFakeForge) Security() forge.SecurityService { return f.security }
func (f *sweepFakeForge) Auth() forge.AuthService         { return nil }
func (f *sweepFakeForge) Repo() forge.RepoService         { return f.repo }

type sweepFakeCI struct {
	forge.CIService
	required []string
	runs     []forgetypes.CheckDetail
}

func (c *sweepFakeCI) GetRequiredCheckNames(context.Context, string, string, string) ([]string, error) {
	return c.required, nil
}

func (c *sweepFakeCI) GetIndividualCheckRuns(context.Context, string, string, string) ([]forgetypes.CheckDetail, error) {
	return c.runs, nil
}

type sweepFakePRs struct {
	forge.PRService
	prs []types.PullRequest
}

func (p *sweepFakePRs) ListPRs(context.Context, string, string, string, string) ([]types.PullRequest, error) {
	return p.prs, nil
}

// sweepFakeSecurity models a repository whose dependency scanning is on and
// whose open-alert set is empty, so the dependabot-alerts producer contributes
// nothing to these wiring tests instead of erroring out of them.
type sweepFakeSecurity struct {
	forge.SecurityService
}

func (s *sweepFakeSecurity) ListOpenAlerts(context.Context, string, string) (*forgetypes.SecurityAlerts, error) {
	return &forgetypes.SecurityAlerts{Status: forgetypes.SecurityAlertsEnabled}, nil
}

type sweepFakeRepo struct {
	forge.RepoService
	defaultBranch string
}

func (r *sweepFakeRepo) RepoMetadata(_ context.Context, owner, name string) (*forgetypes.Repo, error) {
	return &forgetypes.Repo{Owner: owner, Name: name, DefaultBranch: r.defaultBranch}, nil
}

// redMain returns a forge whose default branch has a failing required check,
// completed long enough ago to be past the producer's grace window.
func redMain() *sweepFakeForge {
	return &sweepFakeForge{
		repo:     &sweepFakeRepo{defaultBranch: "main"},
		prs:      &sweepFakePRs{},
		security: &sweepFakeSecurity{},
		ci: &sweepFakeCI{
			required: []string{"Security & license gates"},
			runs: []forgetypes.CheckDetail{{
				Name:        "Security & license gates",
				Conclusion:  "FAILURE",
				HeadSHA:     "abc1234def",
				DetailsURL:  "https://github.com/octocat/acme-web/actions/runs/1",
				CompletedAt: "2020-01-01T00:00:00Z",
			}},
		},
	}
}

// greenMain returns a forge with the same required check passing.
func greenMain() *sweepFakeForge {
	f := redMain()
	f.ci.runs[0].Conclusion = "SUCCESS"
	return f
}

func sweepServer(t *testing.T, client forge.ForgeClient) *Server {
	t.Helper()
	s := newAttentionTestServer(t)
	s.workspaceRoot = t.TempDir()
	s.forgeClientFn = func(string) (forge.ForgeClient, error) { return client, nil }
	return s
}

func runSweep(t *testing.T, s *Server, repos ...string) AttentionSweepResult {
	t.Helper()
	raw, _ := json.Marshal(AttentionSweepParams{Repos: repos, Reason: "test"})
	out, err := s.handleAttentionSweep(context.Background(), raw)
	if err != nil {
		t.Fatalf("handleAttentionSweep: %v", err)
	}
	res, ok := out.(AttentionSweepResult)
	if !ok {
		t.Fatalf("handleAttentionSweep returned %T, want AttentionSweepResult", out)
	}
	return res
}

func listOpen(t *testing.T, s *Server) []attention.DecisionRequest {
	t.Helper()
	raw, _ := json.Marshal(AttentionListParams{})
	out, err := s.handleAttentionList(context.Background(), raw)
	if err != nil {
		t.Fatalf("handleAttentionList: %v", err)
	}
	return out.(AttentionListResult).Requests
}

// A sweep raises the repo-scoped card into the SAME store attention.list reads,
// and the card carries no run and no issue — the shape the extension had never
// had to render before #93.
func TestAttentionSweepRaisesRepoScopedCard(t *testing.T) {
	s := sweepServer(t, redMain())

	res := runSweep(t, s, sweepTestRepo)
	if res.Unavailable || res.Busy {
		t.Fatalf("sweep declined: %+v", res)
	}
	if res.Created != 1 {
		t.Fatalf("created = %d, want 1 (%+v)", res.Created, res.Repos)
	}

	open := listOpen(t, s)
	if len(open) != 1 {
		t.Fatalf("attention.list returned %d requests, want 1", len(open))
	}
	card := open[0]
	if card.Producer != sweep.ProducerDefaultBranchHealth {
		t.Errorf("producer = %q, want %q", card.Producer, sweep.ProducerDefaultBranchHealth)
	}
	if card.Context.Repo != sweepTestRepo {
		t.Errorf("context.repo = %q, want %q", card.Context.Repo, sweepTestRepo)
	}
	if card.Context.RunID != "" || card.Context.Issue != 0 {
		t.Errorf("repo-scoped card must carry no run and no issue, got run=%q issue=%d",
			card.Context.RunID, card.Context.Issue)
	}
	if card.Context.URL == "" {
		t.Error("repo-scoped card must carry a URL — it is the only real affordance it has")
	}
	if !card.Standing {
		t.Error("a swept card must be marked standing so it can auto-resolve")
	}
	if card.Severity != attention.SeverityBlockingFleet {
		t.Errorf("severity = %q, want blocking_fleet (nothing can land)", card.Severity)
	}
}

// Repeated sweeps over an unchanged condition produce no duplicate requests —
// the property that makes the four invocation points safe (epic #88 AC).
func TestAttentionSweepIsIdempotentOverAnUnchangedCondition(t *testing.T) {
	s := sweepServer(t, redMain())

	first := runSweep(t, s, sweepTestRepo)
	second := runSweep(t, s, sweepTestRepo)

	if first.Created != 1 {
		t.Fatalf("first sweep created = %d, want 1", first.Created)
	}
	if second.Created != 0 || second.Updated != 0 {
		t.Errorf("second sweep created=%d updated=%d, want 0/0", second.Created, second.Updated)
	}
	if got := len(listOpen(t, s)); got != 1 {
		t.Errorf("open requests after two sweeps = %d, want 1", got)
	}
}

// The card disappears with no operator action once the branch goes green.
func TestAttentionSweepAutoResolvesWhenTheConditionClears(t *testing.T) {
	forgeClient := redMain()
	s := sweepServer(t, forgeClient)
	runSweep(t, s, sweepTestRepo)

	forgeClient.ci.runs[0].Conclusion = "SUCCESS"
	res := runSweep(t, s, sweepTestRepo)

	if res.AutoResolved != 1 {
		t.Fatalf("autoResolved = %d, want 1 (%+v)", res.AutoResolved, res.Repos)
	}
	if got := len(listOpen(t, s)); got != 0 {
		t.Errorf("open requests after the condition cleared = %d, want 0", got)
	}
}

// A green PR blocked on a review raises a card that names the PR but still has
// no run and no issue.
func TestAttentionSweepCardsAGreenPRWaitingOnAHuman(t *testing.T) {
	forgeClient := greenMain()
	forgeClient.prs.prs = []types.PullRequest{{
		Number:           42,
		Title:            "feat: add the thing",
		State:            "OPEN",
		CheckStatus:      "SUCCESS",
		MergeStateStatus: "BLOCKED",
		ReviewStatus:     string(types.ReviewReviewRequired),
		URL:              "https://github.com/octocat/acme-web/pull/42",
	}}
	s := sweepServer(t, forgeClient)

	if res := runSweep(t, s, sweepTestRepo); res.Created != 1 {
		t.Fatalf("created = %d, want 1 (%+v)", res.Created, res.Repos)
	}

	open := listOpen(t, s)
	if len(open) != 1 {
		t.Fatalf("attention.list returned %d requests, want 1", len(open))
	}
	card := open[0]
	if card.Context.PR != 42 {
		t.Errorf("context.pr = %d, want 42", card.Context.PR)
	}
	if card.Context.Issue != 0 || card.Context.RunID != "" {
		t.Errorf("PR card must carry no issue and no run, got issue=%d run=%q",
			card.Context.Issue, card.Context.RunID)
	}
	if card.Context.URL == "" {
		t.Error("PR card must carry the PR URL")
	}
}

// A daemon with no store or no forge factory reports Unavailable rather than
// failing — the sweep fires on activation and must never surface an error there
// (epic #88 AC: "disabled configurations are a no-op, never a hard failure").
func TestAttentionSweepIsANoOpWhenUnconfigured(t *testing.T) {
	t.Run("no forge factory", func(t *testing.T) {
		s := newAttentionTestServer(t)
		res := runSweep(t, s, sweepTestRepo)
		if !res.Unavailable {
			t.Errorf("want Unavailable with no forge factory, got %+v", res)
		}
	})

	t.Run("no attention store", func(t *testing.T) {
		s := &Server{writer: io.Discard}
		s.forgeClientFn = func(string) (forge.ForgeClient, error) { return redMain(), nil }
		res := runSweep(t, s, sweepTestRepo)
		if !res.Unavailable {
			t.Errorf("want Unavailable with no store, got %+v", res)
		}
	})

	t.Run("no repos", func(t *testing.T) {
		s := sweepServer(t, redMain())
		res := runSweep(t, s)
		if res.Unavailable || len(res.Repos) != 0 {
			t.Errorf("want a quiet no-op for an empty repo list, got %+v", res)
		}
	})
}

// An unresolvable forge client is reported on the repo, not raised as an error:
// one bad repo must not hide the outcome of the others.
func TestAttentionSweepFoldsForgeResolutionFailureIntoTheRepoResult(t *testing.T) {
	s := sweepServer(t, redMain())
	s.forgeClientFn = func(repo string) (forge.ForgeClient, error) {
		if repo == "octocat/broken" {
			return nil, errors.New("no token for octocat/broken")
		}
		return redMain(), nil
	}

	res := runSweep(t, s, "octocat/broken", sweepTestRepo)

	if len(res.Repos) != 2 {
		t.Fatalf("want a result per repo, got %d", len(res.Repos))
	}
	if res.Repos[0].Error == "" {
		t.Error("the unresolvable repo must report its error")
	}
	if res.Created != 1 {
		t.Errorf("created = %d — the healthy repo must still be swept", res.Created)
	}
}

// Duplicate specs cost one round of forge traffic, not two.
func TestAttentionSweepDeduplicatesRepoSpecs(t *testing.T) {
	s := sweepServer(t, redMain())
	res := runSweep(t, s, sweepTestRepo, " "+sweepTestRepo+" ", "")
	if len(res.Repos) != 1 {
		t.Errorf("want 1 repo result after dedupe, got %d (%+v)", len(res.Repos), res.Repos)
	}
}

// Muting silences a card without removing it: it stays in attention.list at its
// severity, and unmute restores alerting.
func TestAttentionMuteKeepsTheCardInTheInbox(t *testing.T) {
	s := sweepServer(t, redMain())
	runSweep(t, s, sweepTestRepo)
	card := listOpen(t, s)[0]

	muteRaw, _ := json.Marshal(AttentionMuteParams{ID: card.ID, Actor: "octocat"})
	out, err := s.handleAttentionMute(context.Background(), muteRaw)
	if err != nil {
		t.Fatalf("handleAttentionMute: %v", err)
	}
	if res := out.(AttentionMuteResult); !res.Ok || !res.Muted {
		t.Fatalf("mute result = %+v, want ok+muted", res)
	}

	open := listOpen(t, s)
	if len(open) != 1 {
		t.Fatalf("a muted card must stay in the inbox, got %d requests", len(open))
	}
	if !open[0].IsMuted() {
		t.Error("listed card is not reported as muted")
	}
	if open[0].Severity != card.Severity {
		t.Errorf("severity changed on mute: %q → %q", card.Severity, open[0].Severity)
	}

	unmuteRaw, _ := json.Marshal(AttentionUnmuteParams{ID: card.ID, Actor: "octocat"})
	out, err = s.handleAttentionUnmute(context.Background(), unmuteRaw)
	if err != nil {
		t.Fatalf("handleAttentionUnmute: %v", err)
	}
	if res := out.(AttentionMuteResult); !res.Ok || res.Muted {
		t.Errorf("unmute result = %+v, want ok and not muted", res)
	}
}

func TestAttentionMuteRejectsAMissingID(t *testing.T) {
	s := sweepServer(t, redMain())
	raw, _ := json.Marshal(AttentionMuteParams{})
	if _, err := s.handleAttentionMute(context.Background(), raw); err == nil {
		t.Error("want an error for a mute with no id")
	}
}
