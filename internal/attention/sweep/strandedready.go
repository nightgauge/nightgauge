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
// The producer evaluates TWO conditions, and the order matters:
//
//  1. **Reachability (ground truth).** Does the board the scheduler actually
//     polls contain any of the repo's open issues? This is asked of the forge,
//     independent of both config sources.
//
//  2. **A known stale board (hint).** When Source A and Source B disagree, it
//     lists Source A's "Ready" items and cards each one, naming the issue and
//     where it is.
//
// Condition 1 exists because the original producer had only condition 2, which
// cannot detect the failure it was built for (#280). Config agreement is
// neither necessary nor sufficient for reachability — two files can agree
// perfectly on a board that is empty, and that is exactly what happened: ~28
// open issues lived on board A while both config sources named board B, the
// scheduler reported "0 candidates" for hours, and `doctor`, the sweep, and
// the startup warning all reported healthy. An audit that validates a source
// against itself is not an audit.
//
// When neither condition holds it returns an empty slice — the positive
// "nothing wrong" signal that auto-resolves any previously raised card, per
// the Producer interface contract.

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
	FindMismatches func(cfg *config.Config, startDir string) (config.ProjectMappingReport, error)
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

func (p *StrandedReadyItems) findMismatches(cfg *config.Config, startDir string) (config.ProjectMappingReport, error) {
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

	// The manifest cross-check is a HINT about where stranded work might be —
	// never a precondition for looking (#280). A missing manifest (single-repo
	// mode) leaves the report empty and the reachability check below still
	// runs; config agreement is not evidence that the board holds anything.
	report, _ := p.findMismatches(cfg, wd)

	var mismatch *config.ProjectMappingMismatch
	for i := range report.Mismatches {
		if repoMatches(report.Mismatches[i].Repo, in.Repo, in.Owner, in.Name) {
			mismatch = &report.Mismatches[i]
			break
		}
	}

	var reqs []attention.DecisionRequest

	// --- Condition 1: the polled board is unreachable for this repo --------
	//
	// Ground truth, independent of both config sources: does the board the
	// scheduler actually polls contain ANY of this repo's open issues? A repo
	// with open work whose polled board holds none of it is unreachable — the
	// scheduler will report "0 candidates" forever and every config check will
	// pass, which is exactly the state #280 was filed from.
	unreachable, err := p.boardUnreachable(ctx, in)
	if err != nil {
		// "I could not look" — never an empty slice (Invariant 1).
		return nil, err
	}
	if unreachable != nil {
		reqs = append(reqs, p.unreachableRequest(in.Repo, *unreachable, mismatch))
	}

	// --- Condition 2: items Ready on a known stale board -------------------
	//
	// Only reachable when the manifest names a different board than the one
	// the scheduler polls. Kept because it is strictly more actionable than
	// condition 1: it names the individual issues and where they are.
	if mismatch != nil {
		staleClient, err := p.staleClient(cfg, in.Owner, mismatch.ManifestProject)
		if err != nil {
			return nil, fmt.Errorf("stranded-ready-items: build Source A client for %s: %w", in.Repo, err)
		}
		items, err := staleClient.Board().ListItems(ctx, "Ready")
		if err != nil {
			return nil, fmt.Errorf("stranded-ready-items: list Ready items on stale project %d for %s: %w", mismatch.ManifestProject, in.Repo, err)
		}
		for _, item := range items {
			if item.Repo != "" && item.Repo != in.Repo {
				continue
			}
			reqs = append(reqs, p.request(in.Repo, item, *mismatch))
		}
	}

	sort.Slice(reqs, func(i, j int) bool { return reqs[i].IdempotencyKey < reqs[j].IdempotencyKey })
	return reqs, nil
}

// boardCoverage is the observed relationship between a repo's open issues and
// the board the scheduler polls for it.
type boardCoverage struct {
	// OpenIssues is how many open issues the repo has.
	OpenIssues int
	// OnPolledBoard is how many of them are on the polled board.
	OnPolledBoard int
}

// boardUnreachable answers the reachability question against the live forge:
// the repo has open issues, and NONE of them are on the board the sweep's
// client is bound to (the one the scheduler polls). Returns nil when the board
// is reachable or when the repo has no open work to strand.
//
// Both queries must succeed. An error here propagates so the producer's cards
// are left untouched rather than silently retracted — a forge outage is not
// evidence that a board became reachable.
func (p *StrandedReadyItems) boardUnreachable(ctx context.Context, in Input) (*boardCoverage, error) {
	// A forge client missing either service cannot answer the question. Report
	// it rather than dereferencing: the sweep runs inside the long-lived
	// daemon, where a nil-service panic takes down every other producer with
	// it. An error here is also the correct verdict under Invariant 1 — "I
	// could not look" must never read as "nothing is wrong".
	if in.Forge == nil || in.Forge.Issues() == nil || in.Forge.Board() == nil {
		return nil, fmt.Errorf("stranded-ready-items: %s has no issue/board service to verify reachability against", in.Repo)
	}

	openIssues, err := in.Forge.Issues().ListIssues(ctx, in.Owner, in.Name, nil)
	if err != nil {
		return nil, fmt.Errorf("stranded-ready-items: list open issues for %s: %w", in.Repo, err)
	}
	if len(openIssues) == 0 {
		// No open work — an empty board is correct, not stranded.
		return nil, nil
	}

	items, _, err := in.Forge.Board().ListOpenItems(ctx)
	if err != nil {
		return nil, fmt.Errorf("stranded-ready-items: list open items on the polled board for %s: %w", in.Repo, err)
	}
	onBoard := 0
	for _, item := range items {
		// A shared board carries several repos' items; only this repo's count.
		// An adapter that leaves Repo empty is bound to a single repo, so the
		// item belongs to it by construction.
		if item.Repo == "" || item.Repo == in.Repo {
			onBoard++
		}
	}
	if onBoard > 0 {
		return nil, nil
	}
	return &boardCoverage{OpenIssues: len(openIssues), OnPolledBoard: 0}, nil
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

// unreachableRequest builds the standing observation for a repo whose polled
// board holds none of its open issues. One card per repo, not per issue: the
// condition is a property of the mapping, and carding 28 issues for one broken
// mapping is the spam-folder failure mode.
//
// blocking_fleet is literally true here — every unit of work in the repo is
// unreachable by the scheduler, not just one.
func (p *StrandedReadyItems) unreachableRequest(repo string, cov boardCoverage, mismatch *config.ProjectMappingMismatch) attention.DecisionRequest {
	title := fmt.Sprintf("%s has %d open issue(s), and none are on the board the scheduler polls",
		repo, cov.OpenIssues)

	body := fmt.Sprintf(
		"The autonomous scheduler polls one project board for %s. That board currently holds "+
			"ZERO of the repo's %d open issues, so promoting an issue to Ready cannot make it "+
			"dispatchable — the scheduler will report `0 candidates` indefinitely.\n\n"+
			"This is observed against the forge, not inferred from config: every config check can "+
			"pass while this is true, because two config files agreeing says nothing about which "+
			"board the issues are actually on.\n\n",
		repo, cov.OpenIssues)

	if mismatch != nil {
		body += fmt.Sprintf(
			"The workspace manifest also disagrees with the runtime config for this repo "+
				"(manifest says project %d, runtime resolves to %d), which is the likely cause. "+
				"Run `nightgauge doctor` for the exact config path.",
			mismatch.ManifestProject, mismatch.ResolvedProject)
	} else {
		body += "The workspace manifest and runtime config AGREE for this repo, so the mapping is " +
			"internally consistent and still wrong — the agreed-on board is not where the issues " +
			"live. Find the board that holds them (`nightgauge board list --repo " + repo +
			"`), then either point the config at it or move the items onto the polled board."
	}

	return attention.DecisionRequest{
		IdempotencyKey: fmt.Sprintf("%s:unreachable:%s", ProducerStrandedReadyItems, repo),
		Kind:           attention.KindUnblock,
		Severity:       attention.SeverityBlockingFleet,
		Title:          title,
		Body:           body,
		// Material state: how much open work is unreachable. It re-alerts when
		// the backlog changes size, and auto-resolves the moment ANY of the
		// repo's issues appear on the polled board. Never time-derived.
		Fingerprint: fmt.Sprintf("unreachable:open:%d", cov.OpenIssues),
		Context: attention.Context{
			Repo:    repo,
			Blocker: fmt.Sprintf("polled board holds 0 of %d open issues", cov.OpenIssues),
		},
		Options: []attention.Option{
			// No automated repair: which board is correct is a human decision,
			// and moving 28 items on a guess is worse than doing nothing.
			{ID: "dismiss", Label: "Dismiss — I've fixed the mapping", Verb: attention.VerbNoop, Style: attention.StyleDefault},
		},
		DefaultAction: attention.ExpireNoop,
	}
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
