package sweep

// Workspace-scoped producers (issue #260).
//
// Every producer before this one was repo-scoped: Evaluate receives ONE repo,
// and the caller iterates a configured repo list. That shape can express
// "something is wrong with repo X" and cannot express "repo Y is not in the
// list at all" — a producer is never invoked for a repo nobody configured, so
// no repo-scoped producer can observe its own blind spot.
//
// That is not an oversight in the producers; it is the reason #260 exists.
// A sibling repo sat outside the configured list for six weeks with every one
// of its PRs blocked by a broken required check, and the Action Center stayed
// quiet — because "not in scope" and "nothing wrong" render identically when
// the only thing that can speak is scoped to what is already in scope.
//
// A workspace producer is evaluated ONCE per sweep and receives the whole
// configured repo list, so it can compare that list against what is actually
// present. Its cards are still per-repo (Context.Repo names the uncovered
// repo); only the evaluation is workspace-wide.
//
// Reconciliation uses Store.AutoResolveUnobserved rather than
// ReconcileStanding: a workspace producer genuinely re-answers its ENTIRE
// condition set on every sweep, which is exactly the precondition
// AutoResolveUnobserved documents. ReconcileStanding is repo-scoped and would
// be the wrong primitive here — it would retract cards for repos this pass
// never claimed to be reasoning about.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/nightgauge/nightgauge/internal/attention"
	"github.com/nightgauge/nightgauge/internal/config"
	"github.com/nightgauge/nightgauge/internal/forge"
	yaml "gopkg.in/yaml.v3"
)

// WorkspaceInput is what a workspace-scoped producer gets. It deliberately
// carries the CONFIGURED repo list rather than a single repo: the whole point
// is to reason about that list as an object, including what is missing from it.
type WorkspaceInput struct {
	// ConfiguredRepos is every repo this sweep covers, canonical "owner/name",
	// sorted and de-duplicated. May be empty — a workspace with nothing
	// configured is itself a reportable condition.
	ConfiguredRepos []string
	// WorkspaceRoot is the directory workspace discovery starts from. Empty
	// when the caller has no workspace context.
	WorkspaceRoot string
	// Forge is a client bound to the primary repo's board. Producers use it to
	// discover repos linked to the same project. Never nil when a producer is
	// called, but sub-services may be — guard before dereferencing.
	Forge forge.ForgeClient
	// Existing is every non-terminal request already open across the whole
	// workspace, so a producer can decline to duplicate another's card.
	// Read-only advisory context.
	Existing []attention.DecisionRequest
}

// Covers reports whether repo is in the configured list, matching on the
// canonical "owner/name" spec or on the bare name.
func (in WorkspaceInput) Covers(repo string) bool {
	for _, c := range in.ConfiguredRepos {
		if strings.EqualFold(c, repo) {
			return true
		}
		if i := strings.Index(c, "/"); i >= 0 && strings.EqualFold(c[i+1:], repo) {
			return true
		}
		if i := strings.Index(repo, "/"); i >= 0 && strings.EqualFold(c, repo[i+1:]) {
			return true
		}
	}
	return false
}

// WorkspaceProducer observes a condition about the workspace as a whole.
//
// The Evaluate contract matches the repo-scoped Producer exactly, including
// Invariant 1: an empty slice is a positive assertion that no condition holds
// and auto-resolves this producer's open cards; an error means "I could not
// look" and leaves them untouched. Never return an empty slice to signal a
// failure.
type WorkspaceProducer interface {
	Name() string
	Evaluate(ctx context.Context, in WorkspaceInput) ([]attention.DecisionRequest, error)
}

// RegisterWorkspace adds a workspace-scoped producer. A duplicate Name
// replaces the earlier registration, mirroring Register.
func (r *Registry) RegisterWorkspace(p WorkspaceProducer) {
	if r == nil || p == nil || strings.TrimSpace(p.Name()) == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for i, existing := range r.workspaceProducers {
		if existing.Name() == p.Name() {
			r.workspaceProducers[i] = p
			return
		}
	}
	r.workspaceProducers = append(r.workspaceProducers, p)
}

// WorkspaceProducers returns the registered workspace producers in a stable
// name order.
func (r *Registry) WorkspaceProducers() []WorkspaceProducer {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]WorkspaceProducer, len(r.workspaceProducers))
	copy(out, r.workspaceProducers)
	sort.Slice(out, func(i, j int) bool { return out[i].Name() < out[j].Name() })
	return out
}

// WorkspaceResult reports what one workspace-scoped sweep did.
type WorkspaceResult struct {
	// SweptRepos is how many repos the surrounding sweep covered. This is the
	// number `attention list` reports so that "no cards" is distinguishable
	// from "nothing was looked at" (#260).
	SweptRepos int `json:"swept_repos"`
	// Evaluated lists the workspace producers that observed successfully.
	Evaluated []string `json:"evaluated,omitempty"`
	// Failed maps a producer name to the error that stopped it. Its cards were
	// deliberately left untouched.
	Failed map[string]string `json:"failed,omitempty"`
	// Created, Updated and AutoResolved are the card deltas.
	Created      int `json:"created"`
	Updated      int `json:"updated"`
	AutoResolved int `json:"auto_resolved"`
}

// SweepWorkspace evaluates every registered workspace producer once against
// the configured repo list and reconciles the results.
//
// Errors only on caller mistakes (no store). Everything environmental is a
// logged degradation recorded on the result — a workspace sweep failing must
// never be fatal to the per-repo sweeps that surround it.
func (s *Sweeper) SweepWorkspace(ctx context.Context, configuredRepos []string) (WorkspaceResult, error) {
	res := WorkspaceResult{SweptRepos: len(configuredRepos)}
	if s == nil || s.Store == nil {
		return res, fmt.Errorf("sweep: workspace sweep requires a store")
	}
	registry := s.Registry
	if registry == nil {
		registry = Default
	}
	producers := registry.WorkspaceProducers()
	if len(producers) == 0 {
		return res, nil
	}

	timeout := s.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// One snapshot for every producer, for the same reason the repo-scoped
	// sweep takes one: reading per-producer lets the first producer's writes
	// influence the second's decisions, making the outcome depend on
	// registration order.
	existing, err := s.Store.List(attention.ListFilter{})
	if err != nil {
		s.logf("attention sweep: could not read open requests for the workspace (producers lose dedupe context): %v", err)
		existing = nil
	}

	// ConfiguredRepos is what CONFIG covers, not what this invocation swept.
	//
	// Those are different sets and conflating them is a bug I shipped and had
	// to walk back: `attention sweep --repo X` sweeps one repo, and passing
	// that as the configured list made every OTHER repo — including four that
	// were configured perfectly well — look uncovered. Coverage is a property
	// of configuration; the swept count is a property of the call. Both are
	// reported, separately.
	configured := workspaceConfiguredRepos(s.WorkspaceRoot)
	if len(configured) == 0 {
		// No manifest and no config: the invocation list is the only evidence
		// of intent available, and is better than declaring everything
		// uncovered.
		configured = configuredRepos
	}

	in := WorkspaceInput{
		ConfiguredRepos: normalizeRepos(configured),
		WorkspaceRoot:   s.WorkspaceRoot,
		Forge:           s.Forge,
		Existing:        existing,
	}

	for _, p := range producers {
		reqs, perr := p.Evaluate(ctx, in)
		if perr != nil {
			if res.Failed == nil {
				res.Failed = map[string]string{}
			}
			res.Failed[p.Name()] = perr.Error()
			s.logf("attention sweep: workspace producer %q failed (its cards left untouched): %v", p.Name(), perr)
			continue
		}

		// Reconcile per repo, then auto-resolve across the producer.
		//
		// Each card is about ONE repo, so grouping by Context.Repo lets
		// ReconcileStanding do the work it already does correctly: ID
		// stamping, expiry refresh, and — critically — the standing
		// create/update/refresh/suppress decision that distinguishes a
		// genuinely changed condition from a re-observation. Raising directly
		// would bypass all of it and re-alert on every sweep.
		byRepo := map[string][]attention.DecisionRequest{}
		observed := make([]string, 0, len(reqs))
		for i := range reqs {
			reqs[i].Producer = p.Name()
			repo := reqs[i].Context.Repo
			if strings.TrimSpace(repo) == "" {
				s.logf("attention sweep: workspace producer %q emitted %q with no context repo — skipped", p.Name(), reqs[i].IdempotencyKey)
				continue
			}
			byRepo[repo] = append(byRepo[repo], reqs[i])
			observed = append(observed, reqs[i].IdempotencyKey)
		}
		for repo, group := range byRepo {
			rec, rerr := s.Store.ReconcileStanding(attention.StandingSweep{
				Repo:      repo,
				Producers: []string{p.Name()},
				Observed:  group,
			})
			if rerr != nil {
				s.logf("attention sweep: workspace producer %q could not reconcile %s: %v", p.Name(), repo, rerr)
				continue
			}
			res.Created += rec.Created
			res.Updated += rec.Updated
		}

		// The per-repo reconcile above only auto-resolves within repos this
		// pass observed. A repo that STOPPED being uncovered produces no
		// observation at all, so nothing above would ever retract its card —
		// this producer-scoped pass is what closes it. Safe precisely because
		// a workspace producer re-answers its entire condition set every
		// sweep, which is AutoResolveUnobserved's documented precondition.
		n, arErr := s.Store.AutoResolveUnobserved(p.Name(), observed)
		if arErr != nil {
			s.logf("attention sweep: workspace auto-resolve %q failed (fail-open): %v", p.Name(), arErr)
		} else {
			res.AutoResolved += n
		}
		res.Evaluated = append(res.Evaluated, p.Name())
	}

	// Record what was covered so `attention list` can qualify an all-clear.
	// Best-effort — failing to write the footer's input must not fail a sweep
	// that already did its real work.
	if err := WriteCoverage(s.WorkspaceRoot, configuredRepos, time.Now()); err != nil {
		s.logf("attention sweep: could not record coverage (list footer will omit it): %v", err)
	}
	return res, nil
}

// workspaceConfiguredRepos returns every repo configuration says is covered,
// as canonical "owner/name" specs: the workspace manifest's repositories[],
// autonomous.enabled_repos, and the primary repo itself.
//
// The union is deliberate. A repo named in EITHER place is one somebody
// configured on purpose, and carding it as an unwatched gap would be noise
// pointed at exactly the person who already did the work.
//
// Returns nil when nothing can be read — the caller decides what to do with
// "no configuration found", which is not the same as "nothing is configured".
func workspaceConfiguredRepos(workspaceRoot string) []string {
	if strings.TrimSpace(workspaceRoot) == "" {
		return nil
	}
	cfg, err := config.Load(workspaceRoot)
	if err != nil || cfg == nil {
		return nil
	}

	out := []string{}
	owner := cfg.Owner
	if owner != "" && cfg.DefaultRepo != "" {
		out = append(out, owner+"/"+cfg.DefaultRepo)
	}
	out = append(out, cfg.Autonomous.ResolvedEnabledRepos(owner)...)

	// Manifest entries may be bare names or owner/name pairs.
	for _, name := range manifestRepoNames(workspaceRoot) {
		if name == "" {
			continue
		}
		if strings.Contains(name, "/") {
			out = append(out, name)
			continue
		}
		if owner != "" {
			out = append(out, owner+"/"+name)
		}
	}
	return normalizeRepos(out)
}

// manifestRepoNames reads repositories[].name from the workspace manifest.
// Best-effort: a missing or malformed manifest yields nothing, and the other
// configuration sources still stand.
func manifestRepoNames(workspaceRoot string) []string {
	data, err := os.ReadFile(filepath.Join(workspaceRoot, ".vscode", "nightgauge-workspace.yaml"))
	if err != nil {
		return nil
	}
	var manifest struct {
		Repositories []struct {
			Name string `yaml:"name"`
		} `yaml:"repositories"`
	}
	if err := yaml.Unmarshal(data, &manifest); err != nil {
		return nil
	}
	out := make([]string, 0, len(manifest.Repositories))
	for _, r := range manifest.Repositories {
		out = append(out, strings.TrimSpace(r.Name))
	}
	return out
}

// Coverage records what the last sweep actually looked at (#260).
//
// It is deliberately an OBSERVATION, not a config read. The configured repo
// list says what WOULD be swept; only a sweep record says what WAS. In a
// workspace where no sweep has ever run, deriving the footer from config would
// claim coverage that never happened — the same "absence reads as health"
// error this whole issue is about, one level up.
type Coverage struct {
	// Repos is the repo list the last sweep covered.
	Repos []string `json:"repos"`
	// SweptAt is when that sweep ran, RFC3339. Written by the sweeper.
	SweptAt string `json:"sweptAt"`
}

// coveragePath is a SIBLING of the attention directory, not a file inside it:
// Store.List parses every *.json in that directory as a DecisionRequest, and
// a foreign file there survives only by being silently skipped on parse error.
func coveragePath(workspaceRoot string) string {
	return filepath.Join(workspaceRoot, ".nightgauge", "attention-coverage.json")
}

// WriteCoverage persists what this sweep covered. Best-effort: a failure to
// record coverage must never fail the sweep that produced it.
func WriteCoverage(workspaceRoot string, repos []string, sweptAt time.Time) error {
	if strings.TrimSpace(workspaceRoot) == "" {
		return fmt.Errorf("sweep: no workspace root for coverage record")
	}
	path := coveragePath(workspaceRoot)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(Coverage{
		Repos:   normalizeRepos(repos),
		SweptAt: sweptAt.UTC().Format(time.RFC3339),
	}, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

// ReadCoverage returns the last recorded sweep coverage. A missing file means
// no sweep has run — reported as (zero, false), never as zero repos swept.
func ReadCoverage(workspaceRoot string) (Coverage, bool) {
	if strings.TrimSpace(workspaceRoot) == "" {
		return Coverage{}, false
	}
	data, err := os.ReadFile(coveragePath(workspaceRoot))
	if err != nil {
		return Coverage{}, false
	}
	var c Coverage
	if err := json.Unmarshal(data, &c); err != nil {
		return Coverage{}, false
	}
	return c, true
}

// normalizeRepos lowercases nothing (repo specs are case-sensitive on the
// forge) but trims, drops empties, sorts, and de-duplicates so a producer's
// output is stable regardless of the caller's ordering.
func normalizeRepos(repos []string) []string {
	seen := make(map[string]bool, len(repos))
	out := make([]string, 0, len(repos))
	for _, r := range repos {
		r = strings.TrimSpace(r)
		if r == "" || seen[r] {
			continue
		}
		seen[r] = true
		out = append(out, r)
	}
	sort.Strings(out)
	return out
}
