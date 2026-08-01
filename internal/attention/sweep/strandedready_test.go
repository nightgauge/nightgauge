package sweep

import (
	"context"
	"strconv"
	"testing"

	"github.com/nightgauge/nightgauge/internal/config"
	"github.com/nightgauge/nightgauge/internal/forge"
	forgetypes "github.com/nightgauge/nightgauge/internal/forge/types"
)

// --- fakes -------------------------------------------------------------

type strandedBoardForge struct {
	board *strandedBoard
}

func (f *strandedBoardForge) Issues() forge.IssueService     { return nil }
func (f *strandedBoardForge) PRs() forge.PRService           { return nil }
func (f *strandedBoardForge) Project() forge.ProjectService  { return nil }
func (f *strandedBoardForge) Board() forge.BoardService      { return f.board }
func (f *strandedBoardForge) CI() forge.CIService            { return nil }
func (f *strandedBoardForge) Labels() forge.LabelService     { return nil }
func (f *strandedBoardForge) Rulesets() forge.RulesetService { return nil }
func (f *strandedBoardForge) Auth() forge.AuthService        { return nil }
func (f *strandedBoardForge) Repo() forge.RepoService        { return nil }

type strandedBoard struct {
	items       []forgetypes.BoardItem
	err         error
	sawStatus   string
	statusCalls int
}

func (b *strandedBoard) ListItems(_ context.Context, statusFilter string) ([]forgetypes.BoardItem, error) {
	b.sawStatus = statusFilter
	b.statusCalls++
	return b.items, b.err
}
func (b *strandedBoard) ListOpenItems(context.Context) ([]forgetypes.BoardItem, int, error) {
	return nil, 0, forge.ErrUnsupported
}
func (b *strandedBoard) CountsByStatus(context.Context) (*forgetypes.StatusCounts, error) {
	return nil, forge.ErrUnsupported
}
func (b *strandedBoard) GetItem(context.Context, string, string, int) (*forgetypes.BoardItem, error) {
	return nil, forge.ErrUnsupported
}

func strandedInput() Input {
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
		FindMismatches: func(*config.Config, string) ([]config.ProjectMappingMismatch, error) {
			return []config.ProjectMappingMismatch{
				{Repo: "nightgauge/nightgauge", ManifestProject: 1, ResolvedProject: 4},
			}, nil
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
		FindMismatches: func(*config.Config, string) ([]config.ProjectMappingMismatch, error) {
			return nil, nil // Source A and Source B agree — nothing stranded
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
		FindMismatches: func(*config.Config, string) ([]config.ProjectMappingMismatch, error) {
			return nil, errUnsupportedNoManifest
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
