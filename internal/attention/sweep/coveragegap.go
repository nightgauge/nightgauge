package sweep

// Workspace producer: coverage gaps (issue #260).
//
// The Action Center's value rests on absence meaning something. Right now
// absence has two very different meanings an operator cannot tell apart:
//
//  1. the repo is healthy, or
//  2. the repo is not being looked at
//
// An operator reasonably reads a quiet board as (1). That is the observed
// failure: a sibling repo sat outside the configured list while every one of
// its open PRs was blocked by a broken required check for roughly six weeks,
// and the board stayed quiet and correct the entire time.
//
// This producer makes (2) say so. It compares the repos actually PRESENT —
// local git checkouts beside the workspace root, and repos linked to the same
// project board — against the configured list, and raises one fyi card per
// repo that is present but uncovered.
//
// fyi, not blocking, and with a plain dismiss: an intentionally excluded repo
// is a legitimate and common state (this very workspace deliberately excludes
// a private strategy repo), so the card must be dismissable without friction.
// The standing fingerprint keeps it from re-nagging once dismissed, and only
// re-alerts when the SET of uncovered repos changes.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/nightgauge/nightgauge/internal/attention"
)

// ProducerCoverageGap is the stable producer id — half of the sticky
// (producer, idempotency_key) identity, so it must never change.
const ProducerCoverageGap = "coverage-gap"

// CoverageGap reports repos present in the workspace but absent from the
// configured repo list.
type CoverageGap struct {
	// DiscoverLocal overrides local-checkout discovery (tests). Nil uses the
	// real filesystem walk.
	DiscoverLocal func(workspaceRoot string) ([]string, error)
	// DiscoverBoard overrides board-linked repo discovery (tests). Nil uses
	// the forge client on WorkspaceInput.
	DiscoverBoard func(ctx context.Context, in WorkspaceInput) ([]string, error)
}

func init() { Default.RegisterWorkspace(&CoverageGap{}) }

// Name implements WorkspaceProducer.
func (p *CoverageGap) Name() string { return ProducerCoverageGap }

// Evaluate implements WorkspaceProducer.
func (p *CoverageGap) Evaluate(ctx context.Context, in WorkspaceInput) ([]attention.DecisionRequest, error) {
	present := map[string]bool{}

	// Two independent discovery sources. Each is best-effort ON ITS OWN, but
	// at least one must succeed: if BOTH fail we have observed nothing, and
	// returning an empty slice would assert "no coverage gaps" on the strength
	// of having looked nowhere — Invariant 1's exact failure mode.
	localOK, boardOK := false, false

	if local, err := p.discoverLocal(in.WorkspaceRoot); err == nil {
		localOK = true
		for _, r := range local {
			present[r] = true
		}
	}
	if board, err := p.discoverBoard(ctx, in); err == nil {
		boardOK = true
		for _, r := range board {
			present[r] = true
		}
	}
	if !localOK && !boardOK {
		return nil, fmt.Errorf("coverage-gap: neither local checkouts nor the project board could be enumerated — coverage is unknown, not clean")
	}

	uncovered := make([]string, 0, len(present))
	for repo := range present {
		if !in.Covers(repo) {
			uncovered = append(uncovered, repo)
		}
	}
	sort.Strings(uncovered)
	if len(uncovered) == 0 {
		return nil, nil // positive observation: every present repo is covered
	}

	// The fingerprint is the SET of uncovered repos, not this one repo: adding
	// a fourth uncovered repo is a genuine change worth re-alerting, while
	// re-observing the same set is not. Sorted so forge/filesystem ordering
	// cannot fake a transition.
	fingerprint := "uncovered:" + strings.Join(uncovered, ",")

	reqs := make([]attention.DecisionRequest, 0, len(uncovered))
	for _, repo := range uncovered {
		reqs = append(reqs, p.request(repo, uncovered, fingerprint))
	}
	return reqs, nil
}

func (p *CoverageGap) discoverLocal(workspaceRoot string) ([]string, error) {
	if p.DiscoverLocal != nil {
		return p.DiscoverLocal(workspaceRoot)
	}
	return discoverLocalCheckouts(workspaceRoot)
}

func (p *CoverageGap) discoverBoard(ctx context.Context, in WorkspaceInput) ([]string, error) {
	if p.DiscoverBoard != nil {
		return p.DiscoverBoard(ctx, in)
	}
	if in.Forge == nil || in.Forge.Board() == nil {
		return nil, fmt.Errorf("coverage-gap: no board service")
	}
	items, _, err := in.Forge.Board().ListOpenItems(ctx)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	out := []string{}
	for _, item := range items {
		if item.Repo == "" || seen[item.Repo] {
			continue
		}
		seen[item.Repo] = true
		out = append(out, item.Repo)
	}
	return out, nil
}

// discoverLocalCheckouts finds git checkouts in the workspace root and its
// immediate siblings, returning "owner/name" specs derived from each one's
// origin remote.
//
// Siblings are included because the common multi-repo layout puts the manifest
// inside one member repo and its peers next to it (`path: ../other-repo`), so
// scanning only below the root would miss every peer — including, in the
// motivating case, the one nobody was watching.
// DiscoverLocalCheckouts exposes the sibling-repository scan to surfaces that
// need the same answer the coverage-gap producer computes.
//
// The Settings panel's "add an unlisted folder" list is the same question this
// producer already answers — which git checkouts sit beside the workspace root
// — and a second scanner written in TypeScript would drift from this one, so
// the two surfaces would disagree about what exists. Returns canonical
// "owner/name" specs read from each checkout's git remote.
func DiscoverLocalCheckouts(workspaceRoot string) ([]string, error) {
	return discoverLocalCheckouts(workspaceRoot)
}

func discoverLocalCheckouts(workspaceRoot string) ([]string, error) {
	if strings.TrimSpace(workspaceRoot) == "" {
		return nil, fmt.Errorf("coverage-gap: no workspace root")
	}
	dirs := map[string]bool{workspaceRoot: true}
	parent := filepath.Dir(workspaceRoot)
	entries, err := os.ReadDir(parent)
	if err != nil {
		// The root alone is still a legitimate (if narrow) observation.
		entries = nil
	}
	for _, e := range entries {
		if e.IsDir() {
			dirs[filepath.Join(parent, e.Name())] = true
		}
	}

	seen := map[string]bool{}
	out := []string{}
	for dir := range dirs {
		spec, ok := originRepoSpec(dir)
		if !ok || seen[spec] {
			continue
		}
		seen[spec] = true
		out = append(out, spec)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("coverage-gap: no git checkouts found under %s", parent)
	}
	sort.Strings(out)
	return out, nil
}

// originRepoSpec reads dir/.git/config and returns the origin remote's
// "owner/name". Reads the file directly rather than shelling out to git: this
// runs inside the daemon's sweep, once per candidate directory, and a process
// spawn per sibling is a poor trade for a string that is one grep away.
// RepoSpecForDir resolves one checkout's canonical "owner/name" from its git
// remote, without shelling out to git.
//
// Exposed alongside DiscoverLocalCheckouts so a surface can identify a
// directory the sibling scan would not reach — a VSCode workspace folder that
// lives outside the workspace root's parent — using the same resolution the
// producer uses, rather than a second parser.
func RepoSpecForDir(dir string) (string, bool) {
	return originRepoSpec(dir)
}

func originRepoSpec(dir string) (string, bool) {
	data, err := os.ReadFile(filepath.Join(dir, ".git", "config"))
	if err != nil {
		return "", false
	}
	inOrigin := false
	for _, line := range strings.Split(string(data), "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "[") {
			inOrigin = strings.HasPrefix(trimmed, `[remote "origin"`)
			continue
		}
		if !inOrigin || !strings.HasPrefix(trimmed, "url") {
			continue
		}
		_, value, found := strings.Cut(trimmed, "=")
		if !found {
			continue
		}
		if spec, ok := repoSpecFromRemoteURL(strings.TrimSpace(value)); ok {
			return spec, true
		}
	}
	return "", false
}

// repoSpecFromRemoteURL extracts "owner/name" from an SSH or HTTPS remote URL.
func repoSpecFromRemoteURL(url string) (string, bool) {
	url = strings.TrimSuffix(strings.TrimSpace(url), ".git")
	if url == "" {
		return "", false
	}
	// SSH: git@host:owner/name — the colon separates host from path.
	if i := strings.LastIndex(url, ":"); i >= 0 && !strings.Contains(url[i+1:], "/../") {
		if candidate := url[i+1:]; strings.Count(candidate, "/") == 1 && !strings.HasPrefix(candidate, "//") {
			return candidate, true
		}
	}
	// HTTPS: scheme://host/owner/name — take the last two path segments.
	parts := strings.Split(strings.Trim(url, "/"), "/")
	if len(parts) >= 2 {
		owner, name := parts[len(parts)-2], parts[len(parts)-1]
		if owner != "" && name != "" && !strings.Contains(owner, ":") {
			return owner + "/" + name, true
		}
	}
	return "", false
}

// request builds the standing fyi observation for one uncovered repo.
func (p *CoverageGap) request(repo string, allUncovered []string, fingerprint string) attention.DecisionRequest {
	body := fmt.Sprintf(
		"%s is present in this workspace but is not in the configured repo list, so NO attention "+
			"producer evaluates it. Nothing is known to be wrong with it — and nothing would be "+
			"reported if something were. A quiet Action Center currently means \"healthy\" for "+
			"covered repos and \"unwatched\" for this one, which is the distinction this card exists "+
			"to restore.\n\n"+
			"If the omission is deliberate, dismiss this — it will stay dismissed until the set of "+
			"uncovered repos changes. To cover it, choose \"Add to the workspace manifest\": the "+
			"repository is appended to .vscode/nightgauge-workspace.yaml with its resolved project "+
			"board, and this card clears itself once the sweep sees it covered.",
		repo)
	if len(allUncovered) > 1 {
		body += fmt.Sprintf("\n\nUncovered in this workspace: %s.", strings.Join(allUncovered, ", "))
	}

	return attention.DecisionRequest{
		IdempotencyKey: fmt.Sprintf("%s:%s", ProducerCoverageGap, repo),
		Kind:           attention.KindHandoff,
		Severity:       attention.SeverityFYI,
		Title:          fmt.Sprintf("%s is in this workspace but no producer watches it", repo),
		Body:           body,
		Fingerprint:    fingerprint,
		Context:        attention.Context{Repo: repo, Blocker: "not in the configured repo list"},
		Options: []attention.Option{
			// #703 gave the workspace manifest a deterministic writer, so the
			// repair this card describes became a bounded registered verb
			// (#706). Before that there was no repair option here at all: no
			// registered verb could edit the manifest, and a button that
			// silently does nothing is worse than none (Invariant 3).
			//
			// The option carries NO args. The verb reads its target from this
			// request's Context.Repo, so the resolving surface cannot redirect
			// the write at another repository.
			{ID: "add", Label: "Add to the workspace manifest", Verb: attention.VerbWorkspaceAddRepo, Style: attention.StylePrimary},
			// Dismiss stays first-class: a deliberate omission is a legitimate
			// and common answer (this very workspace excludes a private repo on
			// purpose), and it mutes until the SET of uncovered repos changes.
			{ID: "dismiss", Label: "Dismiss — the omission is deliberate", Verb: attention.VerbNoop, Style: attention.StyleDefault},
		},
		DefaultAction: attention.ExpireNoop,
	}
}
