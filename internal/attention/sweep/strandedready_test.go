package sweep

import (
	"context"
	"strconv"
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/attention"

	"github.com/nightgauge/nightgauge/internal/config"
	"github.com/nightgauge/nightgauge/internal/forge"
	forgetypes "github.com/nightgauge/nightgauge/internal/forge/types"
)

// --- fakes -------------------------------------------------------------

type strandedBoardForge struct {
	board  *strandedBoard
	issues *strandedIssues
}

func (f *strandedBoardForge) Issues() forge.IssueService {
	if f.issues == nil {
		return nil
	}
	return f.issues
}
func (f *strandedBoardForge) PRs() forge.PRService           { return nil }
func (f *strandedBoardForge) Project() forge.ProjectService  { return nil }
func (f *strandedBoardForge) Board() forge.BoardService      { return f.board }
func (f *strandedBoardForge) CI() forge.CIService            { return nil }
func (f *strandedBoardForge) Labels() forge.LabelService     { return nil }
func (f *strandedBoardForge) Rulesets() forge.RulesetService { return nil }
func (f *strandedBoardForge) Auth() forge.AuthService        { return nil }
func (f *strandedBoardForge) Repo() forge.RepoService        { return nil }

// strandedIssues is the repo's open-issue list — the "is there work at all?"
// half of the reachability question. Embeds the interface so only ListIssues
// needs an implementation; any other method the producer starts calling will
// panic loudly rather than silently return a zero value.
type strandedIssues struct {
	forge.IssueService
	issues []forgetypes.Issue
	err    error
}

func (i *strandedIssues) ListIssues(context.Context, string, string, []string) ([]forgetypes.Issue, error) {
	return i.issues, i.err
}

// openIssues builds n placeholder open issues.
func openIssues(n int) []forgetypes.Issue {
	out := make([]forgetypes.Issue, n)
	for k := range out {
		out[k] = forgetypes.Issue{Number: k + 1}
	}
	return out
}

type strandedBoard struct {
	items       []forgetypes.BoardItem
	err         error
	sawStatus   string
	statusCalls int

	// openItems is what the POLLED board returns from ListOpenItems — the
	// other half of the reachability question. Distinct from items, which is
	// the STALE board's Ready list.
	openItems    []forgetypes.BoardItem
	openItemsErr error
}

func (b *strandedBoard) ListItems(_ context.Context, statusFilter string) ([]forgetypes.BoardItem, error) {
	b.sawStatus = statusFilter
	b.statusCalls++
	return b.items, b.err
}
func (b *strandedBoard) ListOpenItems(context.Context) ([]forgetypes.BoardItem, int, error) {
	return b.openItems, len(b.openItems), b.openItemsErr
}
func (b *strandedBoard) CountsByStatus(context.Context) (*forgetypes.StatusCounts, error) {
	return nil, forge.ErrUnsupported
}
func (b *strandedBoard) GetItem(context.Context, string, string, int) (*forgetypes.BoardItem, error) {
	return nil, forge.ErrUnsupported
}

// strandedInput builds the sweep Input with a REACHABLE polled board: the repo
// has open issues and the board holds them. Tests that exercise condition 1
// override the forge via strandedInputWith.
func strandedInput() Input {
	return strandedInputWith(
		&strandedIssues{issues: openIssues(2)},
		&strandedBoard{openItems: []forgetypes.BoardItem{
			{Number: 1, Repo: "nightgauge/nightgauge"},
			{Number: 2, Repo: "nightgauge/nightgauge"},
		}},
	)
}

// strandedInputWith builds the sweep Input around a specific polled-board and
// issue-list view.
func strandedInputWith(issues *strandedIssues, polled *strandedBoard) Input {
	in := baseStrandedInput()
	in.Forge = &strandedBoardForge{board: polled, issues: issues}
	return in
}

func baseStrandedInput() Input {
	return Input{
		Repo:  "nightgauge/nightgauge",
		Owner: "nightgauge",
		Name:  "nightgauge",
	}
}

// --- tests ---------------------------------------------------------------

func TestStrandedReadyItems_MismatchRaisesOneRequestPerItem(t *testing.T) {
	board := &strandedBoard{items: []forgetypes.BoardItem{
		{Number: 216, Title: "Reconcile board sync", Repo: "nightgauge/nightgauge", URL: "https://example.com/216"},
		{Number: 217, Title: "Reconcile board sync followup", Repo: "nightgauge/nightgauge", URL: "https://example.com/217"},
	}}

	p := &StrandedReadyItems{
		LoadConfig: func(string) (*config.Config, error) {
			return &config.Config{Owner: "nightgauge", DefaultRepo: "nightgauge", ProjectNumber: 4}, nil
		},
		FindMismatches: func(*config.Config, string) (config.ProjectMappingReport, error) {
			return config.ProjectMappingReport{Mismatches: []config.ProjectMappingMismatch{
				{Repo: "nightgauge/nightgauge", ManifestProject: 1, ResolvedProject: 4},
			}}, nil
		},
		StaleClient: func(_ *config.Config, owner string, staleProjectNumber int) (forge.ForgeClient, error) {
			if owner != "nightgauge" || staleProjectNumber != 1 {
				t.Fatalf("staleClient called with owner=%s project=%d, want nightgauge/1", owner, staleProjectNumber)
			}
			return &strandedBoardForge{board: board}, nil
		},
	}

	reqs, err := p.Evaluate(context.Background(), strandedInput())
	if err != nil {
		t.Fatalf("Evaluate: unexpected error: %v", err)
	}
	if len(reqs) != 2 {
		t.Fatalf("Evaluate: got %d requests, want 2: %+v", len(reqs), reqs)
	}
	if board.sawStatus != "Ready" {
		t.Errorf("ListItems called with status %q, want Ready", board.sawStatus)
	}
	for i, want := range []int{216, 217} {
		if reqs[i].Producer != "" {
			t.Errorf("request[%d].Producer = %q, want empty (set by sweeper, not producer)", i, reqs[i].Producer)
		}
		if reqs[i].Fingerprint == "" {
			t.Errorf("request[%d].Fingerprint is empty", i)
		}
		wantKey := ProducerStrandedReadyItems + ":" + "nightgauge/nightgauge:" + strconv.Itoa(want)
		if reqs[i].IdempotencyKey != wantKey {
			t.Errorf("request[%d].IdempotencyKey = %q, want %q", i, reqs[i].IdempotencyKey, wantKey)
		}
	}
}

func TestStrandedReadyItems_NoMismatchReturnsEmpty(t *testing.T) {
	p := &StrandedReadyItems{
		LoadConfig: func(string) (*config.Config, error) {
			return &config.Config{Owner: "nightgauge", DefaultRepo: "nightgauge", ProjectNumber: 4}, nil
		},
		FindMismatches: func(*config.Config, string) (config.ProjectMappingReport, error) {
			return config.ProjectMappingReport{}, nil // Source A and Source B agree
		},
		StaleClient: func(*config.Config, string, int) (forge.ForgeClient, error) {
			t.Fatal("staleClient should not be called when there is no mismatch")
			return nil, nil
		},
	}

	reqs, err := p.Evaluate(context.Background(), strandedInput())
	if err != nil {
		t.Fatalf("Evaluate: unexpected error: %v", err)
	}
	if len(reqs) != 0 {
		t.Fatalf("Evaluate: got %d requests, want 0 (positive no-mismatch observation)", len(reqs))
	}
}

func TestStrandedReadyItems_NoWorkspaceManifestReturnsEmpty(t *testing.T) {
	p := &StrandedReadyItems{
		LoadConfig: func(string) (*config.Config, error) {
			return &config.Config{Owner: "nightgauge", DefaultRepo: "nightgauge", ProjectNumber: 4}, nil
		},
		FindMismatches: func(*config.Config, string) (config.ProjectMappingReport, error) {
			return config.ProjectMappingReport{}, errUnsupportedNoManifest
		},
	}

	reqs, err := p.Evaluate(context.Background(), strandedInput())
	if err != nil {
		t.Fatalf("Evaluate: unexpected error: %v", err)
	}
	if len(reqs) != 0 {
		t.Fatalf("Evaluate: got %d requests, want 0", len(reqs))
	}
}

var errUnsupportedNoManifest = fakeErr("no workspace manifest found")

type fakeErr string

func (e fakeErr) Error() string { return string(e) }

// --- #280: reachability, not config agreement ------------------------------

// agreeingConfig is the producer wired so BOTH config sources agree — the
// exact state in which the pre-#280 producer was structurally unable to fire.
func agreeingConfig(t *testing.T) *StrandedReadyItems {
	t.Helper()
	return &StrandedReadyItems{
		LoadConfig: func(string) (*config.Config, error) {
			return &config.Config{Owner: "nightgauge", DefaultRepo: "nightgauge", ProjectNumber: 4}, nil
		},
		FindMismatches: func(*config.Config, string) (config.ProjectMappingReport, error) {
			return config.ProjectMappingReport{}, nil // Source A and Source B agree
		},
		StaleClient: func(*config.Config, string, int) (forge.ForgeClient, error) {
			t.Fatal("staleClient must not be called when there is no mismatch")
			return nil, nil
		},
	}
}

// THE acceptance criterion for #280: both config sources agree, and the board
// they agree on holds none of the repo's open issues. Every config check
// passes; the work is still unreachable. Pre-fix this returned zero requests.
func TestStrandedReady_ConfigsAgreeButBoardHoldsNothing_RaisesCard(t *testing.T) {
	in := strandedInputWith(
		&strandedIssues{issues: openIssues(28)},
		&strandedBoard{openItems: nil}, // the polled board is empty for this repo
	)

	reqs, err := agreeingConfig(t).Evaluate(context.Background(), in)
	if err != nil {
		t.Fatalf("Evaluate: unexpected error: %v", err)
	}
	if len(reqs) != 1 {
		t.Fatalf("want exactly 1 unreachable card, got %d: %+v", len(reqs), reqs)
	}
	r := reqs[0]
	if r.Severity != attention.SeverityBlockingFleet {
		t.Errorf("an unreachable board blocks every unit of work; severity = %q", r.Severity)
	}
	if r.IdempotencyKey != ProducerStrandedReadyItems+":unreachable:nightgauge/nightgauge" {
		t.Errorf("unexpected idempotency key %q", r.IdempotencyKey)
	}
	if !strings.Contains(r.Body, "AGREE") {
		t.Errorf("body should name the agreeing-but-wrong config as the surprise; got %q", r.Body)
	}
}

// The primary repo takes ResolveRepoProjectNumber's short-circuit path, where
// Source B is not an independent authority at all. #280 called this out
// explicitly as the case that regressed, so it gets its own test: the input
// above IS the primary repo (DefaultRepo == "nightgauge"), and the card must
// still be raised without any mismatch to key off.
func TestStrandedReady_PrimaryRepoShortCircuitPath_StillDetected(t *testing.T) {
	in := strandedInputWith(
		&strandedIssues{issues: openIssues(3)},
		&strandedBoard{openItems: []forgetypes.BoardItem{
			// A shared board holding only OTHER repos' items: the polled board
			// is non-empty, yet holds nothing for the repo being swept.
			{Number: 900, Repo: "acme/unrelated-repo"},
		}},
	)

	reqs, err := agreeingConfig(t).Evaluate(context.Background(), in)
	if err != nil {
		t.Fatalf("Evaluate: unexpected error: %v", err)
	}
	if len(reqs) != 1 {
		t.Fatalf("a shared board with none of THIS repo's items is unreachable; got %d cards", len(reqs))
	}
}

// A reachable board raises nothing — the positive observation that
// auto-resolves any previous card.
func TestStrandedReady_BoardHoldsTheIssues_NoCard(t *testing.T) {
	reqs, err := agreeingConfig(t).Evaluate(context.Background(), strandedInput())
	if err != nil {
		t.Fatalf("Evaluate: unexpected error: %v", err)
	}
	if len(reqs) != 0 {
		t.Fatalf("reachable board must raise nothing, got %+v", reqs)
	}
}

// A repo with no open issues has nothing to strand — an empty board is correct.
func TestStrandedReady_NoOpenIssues_NoCard(t *testing.T) {
	in := strandedInputWith(&strandedIssues{issues: nil}, &strandedBoard{openItems: nil})
	reqs, err := agreeingConfig(t).Evaluate(context.Background(), in)
	if err != nil {
		t.Fatalf("Evaluate: unexpected error: %v", err)
	}
	if len(reqs) != 0 {
		t.Fatalf("no open work means nothing is stranded, got %+v", reqs)
	}
}

// Invariant 1: a forge failure is "I could not look", never "nothing is
// wrong". Returning an empty slice here would auto-resolve a real card on a
// transient outage.
func TestStrandedReady_ForgeErrorPropagates_DoesNotAutoResolve(t *testing.T) {
	t.Run("issue list fails", func(t *testing.T) {
		in := strandedInputWith(
			&strandedIssues{err: fakeErr("boom")},
			&strandedBoard{openItems: nil},
		)
		if _, err := agreeingConfig(t).Evaluate(context.Background(), in); err == nil {
			t.Fatal("want an error so the sweeper leaves existing cards untouched")
		}
	})
	t.Run("board list fails", func(t *testing.T) {
		in := strandedInputWith(
			&strandedIssues{issues: openIssues(2)},
			&strandedBoard{openItemsErr: fakeErr("boom")},
		)
		if _, err := agreeingConfig(t).Evaluate(context.Background(), in); err == nil {
			t.Fatal("want an error so the sweeper leaves existing cards untouched")
		}
	})
}

// Single-repo mode (no workspace manifest) must still get the reachability
// check — the manifest is a hint, not a precondition for looking.
func TestStrandedReady_NoManifest_StillChecksReachability(t *testing.T) {
	p := &StrandedReadyItems{
		LoadConfig: func(string) (*config.Config, error) {
			return &config.Config{Owner: "nightgauge", DefaultRepo: "nightgauge", ProjectNumber: 4}, nil
		},
		FindMismatches: func(*config.Config, string) (config.ProjectMappingReport, error) {
			return config.ProjectMappingReport{}, errUnsupportedNoManifest
		},
	}
	in := strandedInputWith(&strandedIssues{issues: openIssues(5)}, &strandedBoard{openItems: nil})

	reqs, err := p.Evaluate(context.Background(), in)
	if err != nil {
		t.Fatalf("Evaluate: unexpected error: %v", err)
	}
	if len(reqs) != 1 {
		t.Fatalf("a missing manifest must not suppress the reachability check; got %d cards", len(reqs))
	}
}

// A forge client missing a service must produce an error, not a panic. The
// sweep runs inside the long-lived daemon, so a nil-service dereference here
// takes down every other producer with it — and an error is also the correct
// Invariant 1 verdict ("I could not look" is never "nothing is wrong").
func TestStrandedReady_NilForgeService_ErrorsNotPanics(t *testing.T) {
	in := baseStrandedInput()
	in.Forge = &strandedBoardForge{board: nil, issues: nil} // both services nil

	_, err := agreeingConfig(t).Evaluate(context.Background(), in)
	if err == nil {
		t.Fatal("a forge with no issue/board service must error, not report a clean sweep")
	}
	if !strings.Contains(err.Error(), "reachability") {
		t.Errorf("error should name what could not be verified; got %v", err)
	}
}
