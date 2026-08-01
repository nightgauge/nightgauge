package sweep

// Producer: stranded Ready items (issue #271).
//
// A multi-repo workspace names a repo's project board twice: the workspace
// manifest's repositories[].project_number (Source A) and the runtime-resolved
// autonomous.repositories.<repo>.project_number (Source B). The scheduler's
// PickNext only ever polls Source B — so when the two disagree, an issue
// promoted to "Ready" on the Source A board is invisible to the autonomous
// scheduler forever, with nothing surfacing the split until an operator
// notices the issue never ran.
//
// This producer names that split directly: for a repo where Source A and
// Source B disagree, it lists Source A's "Ready" items and cards each one as
// stranded. When the two sources agree, it returns an empty slice — the
// positive "nothing wrong" signal that auto-resolves any previously raised
// card, per the Producer interface contract.

import (
	"context"
	"fmt"
	"os"
	"sort"

	"github.com/nightgauge/nightgauge/internal/attention"
	"github.com/nightgauge/nightgauge/internal/config"
	"github.com/nightgauge/nightgauge/internal/forge"
	forgetypes "github.com/nightgauge/nightgauge/internal/forge/types"
	gh "github.com/nightgauge/nightgauge/internal/github"
)

// ProducerStrandedReadyItems is the stable producer id. It is half of the
// sticky (producer, idempotency_key) identity, so it must never change.
const ProducerStrandedReadyItems = "stranded-ready-items"

// StrandedReadyItems reports issues promoted to "Ready" on a workspace
// manifest's project board (Source A) that the runtime-resolved board
// (Source B) — the one the scheduler actually polls — disagrees with.
type StrandedReadyItems struct {
	// WorkingDir overrides the directory config/workspace-manifest discovery
	// starts from (tests). Empty uses os.Getwd().
	WorkingDir string
	// LoadConfig overrides config.Load (tests). Nil uses config.Load.
	LoadConfig func(dir string) (*config.Config, error)
	// FindMismatches overrides config.FindWorkspaceProjectMappingMismatches
	// (tests). Nil uses the real implementation.
	FindMismatches func(cfg *config.Config, startDir string) ([]config.ProjectMappingMismatch, error)
	// StaleClient overrides staleBoardClient (tests), avoiding a real token
	// resolution / forge.New call.
	StaleClient func(cfg *config.Config, owner string, staleProjectNumber int) (forge.ForgeClient, error)
}

func init() { Default.Register(&StrandedReadyItems{}) }

// Name implements Producer.
func (p *StrandedReadyItems) Name() string { return ProducerStrandedReadyItems }

func (p *StrandedReadyItems) workingDir() (string, error) {
	if p.WorkingDir != "" {
		return p.WorkingDir, nil
	}
	return os.Getwd()
}

func (p *StrandedReadyItems) loadConfig(dir string) (*config.Config, error) {
	if p.LoadConfig != nil {
		return p.LoadConfig(dir)
	}
	return config.Load(dir)
}

func (p *StrandedReadyItems) findMismatches(cfg *config.Config, startDir string) ([]config.ProjectMappingMismatch, error) {
	if p.FindMismatches != nil {
		return p.FindMismatches(cfg, startDir)
	}
	return config.FindWorkspaceProjectMappingMismatches(cfg, startDir)
}

func (p *StrandedReadyItems) staleClient(cfg *config.Config, owner string, staleProjectNumber int) (forge.ForgeClient, error) {
	if p.StaleClient != nil {
		return p.StaleClient(cfg, owner, staleProjectNumber)
	}
	return staleBoardClient(cfg, owner, staleProjectNumber)
}

// Evaluate implements Producer.
func (p *StrandedReadyItems) Evaluate(ctx context.Context, in Input) ([]attention.DecisionRequest, error) {
	wd, err := p.workingDir()
	if err != nil {
		return nil, fmt.Errorf("stranded-ready-items: resolve working dir: %w", err)
	}
	cfg, err := p.loadConfig(wd)
	if err != nil || cfg == nil {
		// No config loaded — this is a fresh/single-repo checkout, not a
		// producer failure. Nothing to cross-check.
		return nil, nil
	}

	mismatches, err := p.findMismatches(cfg, wd)
	if err != nil {
		// No workspace manifest — single-repo mode, nothing to check. Not an
		// error: a positive "nothing wrong" observation.
		return nil, nil
	}

	var mismatch *config.ProjectMappingMismatch
	for i := range mismatches {
		if repoMatches(mismatches[i].Repo, in.Repo, in.Owner, in.Name) {
			mismatch = &mismatches[i]
			break
		}
	}
	if mismatch == nil {
		// This repo's Source A and Source B agree — nothing stranded.
		return nil, nil
	}

	staleClient, err := p.staleClient(cfg, in.Owner, mismatch.ManifestProject)
	if err != nil {
		return nil, fmt.Errorf("stranded-ready-items: build Source A client for %s: %w", in.Repo, err)
	}

	items, err := staleClient.Board().ListItems(ctx, "Ready")
	if err != nil {
		return nil, fmt.Errorf("stranded-ready-items: list Ready items on stale project %d for %s: %w", mismatch.ManifestProject, in.Repo, err)
	}

	var reqs []attention.DecisionRequest
	for _, item := range items {
		if item.Repo != "" && item.Repo != in.Repo {
			continue
		}
		reqs = append(reqs, p.request(in.Repo, item, *mismatch))
	}
	sort.Slice(reqs, func(i, j int) bool { return reqs[i].IdempotencyKey < reqs[j].IdempotencyKey })
	return reqs, nil
}

// repoMatches reports whether a manifest repositories[].name entry (which may
// be a short name or an "owner/name" pair) identifies the same repo as the
// sweep Input.
func repoMatches(manifestName, repo, owner, name string) bool {
	if manifestName == repo {
		return true
	}
	if manifestName == name {
		return true
	}
	return manifestName == owner+"/"+name
}

// staleBoardClient builds a ForgeClient bound to the workspace manifest's
// (Source A) project number for owner, reusing the same token-resolution
// chain as every other forge client construction in the binary.
func staleBoardClient(cfg *config.Config, owner string, staleProjectNumber int) (forge.ForgeClient, error) {
	token, err := gh.ResolveTokenChain(cfg, owner)
	if err != nil {
		return nil, err
	}
	ownerType := cfg.OwnerType
	if ownerType == "" {
		ownerType = "org"
	}
	return forge.New(forge.Config{
		Kind:          forge.KindGitHub,
		Token:         token,
		Owner:         owner,
		ProjectNumber: staleProjectNumber,
		OwnerType:     ownerType,
	})
}

// request builds the standing observation for one issue stranded on the
// stale (Source A) board.
func (p *StrandedReadyItems) request(repo string, item forgetypes.BoardItem, mismatch config.ProjectMappingMismatch) attention.DecisionRequest {
	title := fmt.Sprintf("#%d %q is Ready on the stale board (project %d) — the scheduler polls project %d",
		item.Number, item.Title, mismatch.ManifestProject, mismatch.ResolvedProject)

	body := fmt.Sprintf(
		"%s is on project %d per .vscode/nightgauge-workspace.yaml, but the autonomous "+
			"scheduler polls project %d (autonomous.repositories.%s.project_number). This "+
			"issue is Ready on the board nobody polls, so it will never be dispatched.\n\n"+
			"Fix the mapping (`nightgauge doctor` names the exact config path), then re-add "+
			"the issue to the polled board: `nightgauge project add %d --repo %s --project %d`.",
		repo, mismatch.ManifestProject, mismatch.ResolvedProject, repo, item.Number, repo, mismatch.ResolvedProject)

	return attention.DecisionRequest{
		IdempotencyKey: fmt.Sprintf("%s:%s:%d", ProducerStrandedReadyItems, repo, item.Number),
		Kind:           attention.KindUnblock,
		Severity:       attention.SeverityBlockingRun,
		Title:          title,
		Body:           body,
		// The fingerprint is WHICH board pair disagrees and at what status —
		// never the observation time. A mapping fix (or the item moving off
		// Ready) is a genuine state transition and re-alerts or auto-resolves;
		// re-observing the same stranded item on the same two boards does not.
		Fingerprint: fmt.Sprintf("stale:%d:polled:%d:status:Ready", mismatch.ManifestProject, mismatch.ResolvedProject),
		Context: attention.Context{
			Repo: repo,
			Blocker: fmt.Sprintf("issue #%d is Ready on stale project %d, scheduler polls project %d",
				item.Number, mismatch.ManifestProject, mismatch.ResolvedProject),
			URL: item.URL,
		},
		Options: []attention.Option{
			// No automated repair: moving board membership is a human decision
			// (which board is actually correct, and whether the stale item
			// should be archived) — see the "no silent move" rule in #271's
			// plan. Dismiss only records that a human looked.
			{ID: "dismiss", Label: "Dismiss — I've fixed the mapping", Verb: attention.VerbNoop, Style: attention.StyleDefault},
		},
		DefaultAction: attention.ExpireNoop,
	}
}
