package orchestrator

import (
	"context"
	"fmt"
	"log"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nightgauge/nightgauge/internal/attention"
	"github.com/nightgauge/nightgauge/internal/config"
	"github.com/nightgauge/nightgauge/internal/graph"
	"github.com/nightgauge/nightgauge/internal/graph/extract"
	"github.com/nightgauge/nightgauge/internal/trust"
)

// Pre-dispatch repo affinity (#139).
//
// Before an issue is dispatched in a multi-repo workspace, the paths its body
// mentions are resolved against every workspace repository's tracked files.
// An issue whose work evidently lives in a sibling repository (misfiled), or
// whose acceptance criteria name paths in more than one repository
// (cross_repo), is refused before any stage is billed, with an Action Center
// card naming the owning repository. Every other outcome — and every error —
// dispatches: the check is fail-open and adds no new failure mode.

const (
	affinityDispatch  = "dispatch"
	affinityMisfiled  = "misfiled"
	affinityCrossRepo = "cross_repo"

	producerRepoAffinity = "repo-affinity"
	// pausedRepoAffinity is the queue pause kind for an item the repo-affinity
	// gate refused; a later dequeue releases it once the gate dispatches.
	pausedRepoAffinity = "repo_affinity"
)

// affinityEvidence is one referenced path and where it resolved.
type affinityEvidence struct {
	Path string
	Line int
	Repo string // the sibling repository the path resolves in only
}

// affinityVerdict is the gate's decision for one issue.
type affinityVerdict struct {
	Action      string
	Reason      string
	OwningRepos []string
	Evidence    []affinityEvidence
}

// Refused reports whether the verdict blocks dispatch.
func (v affinityVerdict) Refused() bool { return v.Action != affinityDispatch }

// decideRepoAffinity classifies an issue's `references` edges against the
// workspace's per-repo file sets (slug → set of tracked paths). It is pure.
//
// A path counts as own whenever the filed repo tracks it, even if a sibling
// does too — the false-positive guard. Rules:
//  1. fewer than two repos → dispatch;
//  2. no own and no sibling-only refs → dispatch (ambiguous or reference-free);
//  3. no own refs and ≥2 sibling-only refs, one repo holding a strict majority
//     of them → misfiled;
//  4. ≥1 own ref and ≥1 sibling-only ref inside the acceptance criteria →
//     cross_repo;
//  5. anything else → dispatch.
func decideRepoAffinity(filedRepo string, refs []graph.Edge, files map[string]map[string]bool) affinityVerdict {
	if len(files) < 2 {
		return affinityVerdict{Action: affinityDispatch, Reason: "single-repo workspace"}
	}
	own, ownSet := 0, findRepoKey(files, filedRepo)
	if ownSet == "" {
		return affinityVerdict{Action: affinityDispatch, Reason: fmt.Sprintf("filed repo %s is not in the workspace", filedRepo)}
	}
	var sibling, siblingInAC []affinityEvidence
	perRepo := map[string]int{}
	for _, e := range refs {
		if e.Kind != graph.EdgeReferences || e.To.Kind() != graph.NodeFile {
			continue
		}
		path := strings.TrimPrefix(string(e.To), string(graph.NodeFile)+":")
		dir := e.Attrs["dir"] == "true"
		if pathResolves(files[ownSet], path, dir) {
			own++
			continue
		}
		var hits []string
		for slug, set := range files {
			if slug != ownSet && pathResolves(set, path, dir) {
				hits = append(hits, slug)
			}
		}
		if len(hits) != 1 {
			continue // unresolved, or ambiguous between siblings
		}
		ev := affinityEvidence{Path: path, Line: e.Prov.SourceLine, Repo: hits[0]}
		sibling = append(sibling, ev)
		perRepo[hits[0]]++
		if e.Attrs["in_acceptance_criteria"] == "true" {
			siblingInAC = append(siblingInAC, ev)
		}
	}

	switch {
	case own == 0 && len(sibling) == 0:
		return affinityVerdict{Action: affinityDispatch, Reason: "no reference resolves in any workspace repo"}
	case own == 0 && len(sibling) >= 2:
		for slug, n := range perRepo {
			if 2*n > len(sibling) {
				return affinityVerdict{
					Action:      affinityMisfiled,
					Reason:      fmt.Sprintf("%d of %d referenced paths resolve only in %s and none in %s", n, len(sibling), slug, filedRepo),
					OwningRepos: []string{slug},
					Evidence:    sibling,
				}
			}
		}
	case own > 0 && len(siblingInAC) > 0:
		repos := []string{ownSet}
		seen := map[string]bool{ownSet: true}
		for _, ev := range siblingInAC {
			if !seen[ev.Repo] {
				seen[ev.Repo] = true
				repos = append(repos, ev.Repo)
			}
		}
		sort.Strings(repos[1:])
		return affinityVerdict{
			Action:      affinityCrossRepo,
			Reason:      fmt.Sprintf("acceptance criteria reference paths in %s", strings.Join(repos, ", ")),
			OwningRepos: repos,
			Evidence:    siblingInAC,
		}
	}
	return affinityVerdict{Action: affinityDispatch, Reason: "references resolve in the filed repo or are inconclusive"}
}

// findRepoKey returns the key of files matching slug case-insensitively.
func findRepoKey(files map[string]map[string]bool, slug string) string {
	for k := range files {
		if strings.EqualFold(k, slug) {
			return k
		}
	}
	return ""
}

// pathResolves reports whether path is tracked in set; a directory reference
// resolves when any tracked file sits under it.
func pathResolves(set map[string]bool, path string, dir bool) bool {
	if set[path] {
		return true
	}
	if !dir {
		return false
	}
	prefix := strings.TrimSuffix(path, "/") + "/"
	for p := range set {
		if strings.HasPrefix(p, prefix) {
			return true
		}
	}
	return false
}

// affinityDeps are the gate's seams. Production wiring reads the workspace
// registry, the forge and `git ls-files`; tests substitute all three.
type affinityDeps struct {
	repoPaths func() map[string]string
	body      func(ctx context.Context, owner, repo string, number int) (string, error)
	files     func(root string) (map[string]bool, error)
}

// affinityCycle caches per-repo file sets for one scan.
type affinityCycle struct {
	mu    sync.Mutex
	files map[string]map[string]bool
}

// resetAffinityCycle drops the cached file sets, so one scan reads each
// repository's tracked files once and the next scan sees new commits.
func (s *Scheduler) resetAffinityCycle() {
	s.affinity.mu.Lock()
	s.affinity.files = nil
	s.affinity.mu.Unlock()
}

func (s *Scheduler) affinityRepoPaths() map[string]string {
	if s.affinityDeps.repoPaths != nil {
		return s.affinityDeps.repoPaths()
	}
	if s.execMgr == nil {
		return nil
	}
	return config.WorkspaceRepoPaths(s.execMgr.WorkspaceRoot())
}

func (s *Scheduler) affinityBody(ctx context.Context, owner, repo string, number int) (string, error) {
	if s.affinityDeps.body != nil {
		return s.affinityDeps.body(ctx, owner, repo, number)
	}
	svc := s.issueServiceFor(ctx, owner, repo)
	if svc == nil {
		return "", fmt.Errorf("no issue service")
	}
	got, err := svc.GetIssuesByNumbersWithoutRelations(ctx, owner, repo, []int{number})
	if err != nil {
		return "", err
	}
	issue := got[number]
	if issue == nil {
		return "", fmt.Errorf("issue not returned")
	}
	return issue.Body, nil
}

func (s *Scheduler) affinityFiles(root string) (map[string]bool, error) {
	if s.affinityDeps.files != nil {
		return s.affinityDeps.files(root)
	}
	return trackedFiles(root)
}

// trackedFiles is the set of paths `git ls-files` reports under root.
func trackedFiles(root string) (map[string]bool, error) {
	cmd := exec.Command("git", "ls-files", "-z")
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git ls-files in %s: %w", root, err)
	}
	set := map[string]bool{}
	for _, p := range strings.Split(string(out), "\x00") {
		if p = strings.TrimSpace(p); p != "" {
			set[p] = true
		}
	}
	return set, nil
}

// workspaceFiles returns the per-repo file sets, cached for this scan.
func (s *Scheduler) workspaceFiles(paths map[string]string) (map[string]map[string]bool, error) {
	s.affinity.mu.Lock()
	defer s.affinity.mu.Unlock()
	if s.affinity.files != nil {
		return s.affinity.files, nil
	}
	files := make(map[string]map[string]bool, len(paths))
	for slug, root := range paths {
		set, err := s.affinityFiles(root)
		if err != nil {
			return nil, err
		}
		files[slug] = set
	}
	s.affinity.files = files
	return files, nil
}

// repoAffinity decides whether repo#number may dispatch. It fails open: any
// error, and any single-repo workspace, dispatches. A refusal raises the
// Action Center card.
func (s *Scheduler) repoAffinity(ctx context.Context, repo string, number int, title string) affinityVerdict {
	paths := s.affinityRepoPaths()
	if len(paths) < 2 {
		return affinityVerdict{Action: affinityDispatch, Reason: "single-repo workspace"}
	}
	owner, name := splitOwnerRepo(repo)
	if owner == "" || name == "" {
		owner, name = s.owner, repo
	}
	if owner == "" || name == "" {
		return affinityVerdict{Action: affinityDispatch, Reason: "filed repo unknown"}
	}
	filed := owner + "/" + name
	failOpen := func(why string, err error) affinityVerdict {
		log.Printf("repo-affinity: %s#%d dispatching (fail-open) — %s: %v", filed, number, why, err)
		return affinityVerdict{Action: affinityDispatch, Reason: why}
	}
	body, err := s.affinityBody(ctx, owner, name, number)
	if err != nil {
		return failOpen("issue body unavailable", err)
	}
	issueRef := filed + "#" + strconv.Itoa(number)
	res := extract.References(issueRef, body)
	if res.Graph == nil || res.Skipped != "" {
		return affinityVerdict{Action: affinityDispatch, Reason: "no references"}
	}
	files, err := s.workspaceFiles(paths)
	if err != nil {
		return failOpen("workspace file sets unavailable", err)
	}
	v := decideRepoAffinity(filed, res.Graph.EdgesFrom(graph.MakeNodeID(graph.NodeIssue, issueRef)), files)
	if v.Refused() {
		s.raiseRepoAffinityCard(filed, number, title, v)
	}
	return v
}

// raiseRepoAffinityCard surfaces a refusal. It proposes, and never performs, a
// transfer (misfiled) or one linked issue per repository (cross_repo).
func (s *Scheduler) raiseRepoAffinityCard(filed string, number int, title string, v affinityVerdict) {
	var b strings.Builder
	b.WriteString(v.Reason + ".\n\nEvidence:\n")
	for _, ev := range v.Evidence {
		fmt.Fprintf(&b, "- `%s` (body line %d) resolves only in %s\n", ev.Path, ev.Line, ev.Repo)
	}
	var cardTitle string
	var options []attention.Option
	if v.Action == affinityMisfiled {
		cardTitle = fmt.Sprintf("#%d looks misfiled — its work belongs in %s", number, v.OwningRepos[0])
		b.WriteString("\nTransfer the issue to " + v.OwningRepos[0] + ", or edit its body; the next scan re-checks it.")
		options = []attention.Option{noopOption("transfer", "I'll transfer it to "+v.OwningRepos[0])}
	} else {
		cardTitle = fmt.Sprintf("#%d spans repos — split into linked issues", number)
		b.WriteString("\nCreate one linked issue per repository (" + strings.Join(v.OwningRepos, ", ") +
			") and narrow this issue's criteria to " + filed + "; the next scan re-checks it.")
		options = []attention.Option{noopOption("split_linked_issues", "I'll split it into linked issues")}
	}
	if title != "" {
		cardTitle += ": " + title
	}
	options = append(options, noopOption("leave", "Leave refused"))
	s.raiseAttention(attention.DecisionRequest{
		IdempotencyKey: fmt.Sprintf("%s:%s#%d", producerRepoAffinity, filed, number),
		Kind:           attention.KindChoose,
		Severity:       attention.SeverityBlockingRun,
		Title:          cardTitle,
		Body:           b.String(),
		Producer:       producerRepoAffinity,
		Context:        attention.Context{Repo: filed, Issue: number},
		Options:        options,
		DefaultAction:  "leave",
		ExpiresAt:      expiryFromNow(72 * time.Hour),
	})
}

// queueAffinity decides repo affinity for every queued item that could claim
// a slot this dequeue — pending items the eligibility gate passed, and items
// held by an earlier affinity refusal — keyed by repoIssueKey. A single-repo
// workspace decides nothing. Called without s.mu held.
func (s *Scheduler) queueAffinity(ctx context.Context, eligible map[string]trust.Decision) map[string]affinityVerdict {
	if len(s.affinityRepoPaths()) < 2 {
		return nil
	}
	s.resetAffinityCycle()
	s.mu.Lock()
	items := make([]QueueItem, 0, len(s.queue))
	for _, it := range s.queue {
		held := it.Status == "paused" && it.PausedReason != nil && it.PausedReason.Kind == pausedRepoAffinity
		if d, ok := eligible[repoIssueKey(it.Repo, it.IssueNumber)]; held || (it.Status != "paused" && it.Status != "processing" && it.Status != QueueStatusDropped && ok && d.Eligible) {
			items = append(items, it)
		}
	}
	s.mu.Unlock()

	out := make(map[string]affinityVerdict, len(items))
	for _, it := range items {
		out[repoIssueKey(it.Repo, it.IssueNumber)] = s.repoAffinity(ctx, it.Repo, it.IssueNumber, it.Title)
	}
	return out
}
